package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/monadical/llmproxy/internal/apierr"
	"github.com/monadical/llmproxy/internal/secrets"
	"github.com/monadical/llmproxy/internal/store"
)

type Auth struct {
	PrincipalID   string
	PrincipalName string
	Role          string
	KeyID         string
	// Client is the request's truncated User-Agent, set by withAuth so usage
	// recording can attribute traffic to the tool that sent it.
	Client string
	// ViaSession marks browser-session auth (the built-in UI). Sessions carry
	// the principal's role; mutations are origin-checked in sessionAuth.
	ViaSession bool
}

func extractAPIKey(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return r.Header.Get("x-api-key")
}

func (s *Server) authenticate(r *http.Request) (*Auth, *apierr.ProxyError) {
	raw := extractAPIKey(r)
	if raw == "" {
		if auth, perr := s.sessionAuth(r); auth != nil || perr != nil {
			return auth, perr
		}
		return nil, apierr.New(401, "missing_api_key",
			"provide an API key via 'Authorization: Bearer <key>' or 'x-api-key'")
	}
	hash := secrets.HashAPIKey(s.secret, raw)
	res, err := s.store.AuthByKeyHash(r.Context(), hash)
	if err != nil {
		slog.Error("auth lookup failed", "error", err)
		return nil, apierr.New(500, "internal_error", "authentication lookup failed")
	}
	if res == nil {
		// Deleted keys land here too: deletion is the revocation mechanism.
		return nil, apierr.New(401, "invalid_api_key", "unknown API key")
	}
	// last_used_at is coarse on purpose: refresh at most once a minute so the
	// hot path is read-mostly. Timestamps share one format, so string
	// comparison is chronological.
	threshold := time.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000000Z")
	if !res.LastUsedAt.Valid || res.LastUsedAt.String < threshold {
		if err := s.store.TouchAPIKey(r.Context(), res.KeyID); err != nil {
			slog.Warn("failed to update key last_used_at", "error", err)
		}
	}
	return &Auth{
		PrincipalID:   res.PrincipalID,
		PrincipalName: res.PrincipalName,
		Role:          res.Role,
		KeyID:         res.KeyID,
	}, nil
}

// checkSameOrigin rejects cross-origin browser requests. Behind a
// Host-rewriting reverse proxy r.Host is the internal name, so the public
// host taken from the OIDC redirect URL is accepted as well.
func (s *Server) checkSameOrigin(r *http.Request) *apierr.ProxyError {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err == nil && (parsed.Host == r.Host || (s.publicHost != "" && parsed.Host == s.publicHost)) {
		return nil
	}
	return apierr.New(403, "cross_origin_rejected",
		"cross-origin browser requests are not allowed; use an API key")
}

// sessionAuth authenticates a browser session cookie, with an origin check on
// mutations since cookies (unlike API keys) are attached by browsers
// automatically.
func (s *Server) sessionAuth(r *http.Request) (*Auth, *apierr.ProxyError) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, nil
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if perr := s.checkSameOrigin(r); perr != nil {
			return nil, perr
		}
	}
	// Signed cookies from before the session table contain '.'; opaque
	// tokens (base64url) never do. The legacy path drains within one
	// SessionTTL of the upgrade.
	if strings.Contains(cookie.Value, ".") {
		return s.legacySessionAuth(r, cookie.Value)
	}
	res, err := s.store.AuthBySessionHash(r.Context(), secrets.HashAPIKey(s.secret, cookie.Value))
	if err != nil {
		slog.Error("session lookup failed", "error", err)
		return nil, apierr.New(500, "internal_error", "authentication lookup failed")
	}
	if res == nil {
		return nil, nil // unknown, expired or revoked (deleted) session
	}
	// Same coarse last_used_at refresh as API keys: at most once a minute.
	threshold := time.Now().Add(-time.Minute).UTC().Format(store.TimeFormat)
	if !res.LastUsedAt.Valid || res.LastUsedAt.String < threshold {
		if err := s.store.TouchSession(r.Context(), res.SessionID); err != nil {
			slog.Warn("failed to update session last_used_at", "error", err)
		}
	}
	return &Auth{
		PrincipalID:   res.PrincipalID,
		PrincipalName: res.PrincipalName,
		Role:          res.Role,
		ViaSession:    true,
	}, nil
}

// legacySessionAuth verifies signed session cookies issued before the session
// table existed. They cannot be deleted, so the per-principal revocation
// watermark sweeps them instead.
func (s *Server) legacySessionAuth(r *http.Request, token string) (*Auth, *apierr.ProxyError) {
	principalID, issuedAt := s.verifyToken(token)
	if principalID == "" {
		return nil, nil
	}
	principal, err := s.store.GetPrincipalByID(r.Context(), principalID)
	if err != nil {
		slog.Error("session principal lookup failed", "error", err)
		return nil, apierr.New(500, "internal_error", "authentication lookup failed")
	}
	if principal == nil {
		return nil, nil // stale session for a deleted principal
	}
	if principal.SessionsRevokedBefore.Valid && principal.SessionsRevokedBefore.String != "" {
		watermark, err := time.Parse(store.TimeFormat, principal.SessionsRevokedBefore.String)
		if err != nil {
			slog.Error("unparsable sessions_revoked_before; rejecting session",
				"principal", principal.ID, "error", err)
			return nil, nil
		}
		// Tokens without an issue time count as issued at zero and are swept.
		if issuedAt < watermark.Unix() {
			return nil, nil // revoked; same handling as a stale session
		}
	}
	return &Auth{
		PrincipalID:   principal.ID,
		PrincipalName: principal.Name,
		Role:          principal.Role,
		ViaSession:    true,
	}, nil
}

func (s *Server) withAuth(next func(http.ResponseWriter, *http.Request, *Auth)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, perr := s.authenticate(r)
		if perr != nil {
			writeProxyError(w, perr)
			return
		}
		auth.Client = clientFrom(r)
		next(w, r, auth)
	})
}

// withAdmin admits admin API keys and admin browser sessions (the built-in
// UI). Session mutations are origin-checked in sessionAuth, and session
// cookies are HttpOnly + SameSite=Lax, which is the same posture the rest of
// the session surface relies on.
func (s *Server) withAdmin(next func(http.ResponseWriter, *http.Request, *Auth)) http.Handler {
	return s.withAuth(func(w http.ResponseWriter, r *http.Request, auth *Auth) {
		if auth.Role != "admin" {
			writeProxyError(w, apierr.New(403, "admin_required", "this endpoint requires the admin role"))
			return
		}
		next(w, r, auth)
	})
}

func readLimited(r *http.Request, maxBytes int64) ([]byte, *apierr.ProxyError) {
	tooLarge := apierr.Newf(413, "request_too_large", "request body exceeds %d bytes", maxBytes)
	if r.ContentLength > maxBytes {
		return nil, tooLarge
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		return nil, apierr.New(400, "invalid_request", "failed to read request body")
	}
	if int64(len(data)) > maxBytes {
		return nil, tooLarge
	}
	return data, nil
}
