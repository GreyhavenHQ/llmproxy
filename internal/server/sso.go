package server

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/monadical/llmproxy/internal/apierr"
	"github.com/monadical/llmproxy/internal/secrets"
)

// retryStateSuffix marks a state minted by the automatic retry after a state
// mismatch, so the callback stops after one attempt instead of looping. It
// must not contain '.', which delimits token payload fields.
const retryStateSuffix = "-retry"

// SSO login flow (authorization code). A principal is created on first login
// with the IdP subject as the stable join key; email and the group-derived
// role are reconciled on every login. Group to role mapping is configuration:
// members of OIDCAdminGroup get the admin role, everyone else is a member,
// and OIDCRequiredGroup (when set) gates access entirely.

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.sso == nil {
		writeProxyError(w, apierr.New(404, "sso_not_configured", "SSO is not configured on this proxy"))
		return
	}
	state := secrets.NewID()
	if r.URL.Query().Get("retry") == "1" {
		state += retryStateSuffix
	}
	s.issueStateCookie(w, r, state)
	http.Redirect(w, r, s.sso.AuthCodeURL(state), http.StatusFound)
}

func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.sso == nil {
		writeProxyError(w, apierr.New(404, "sso_not_configured", "SSO is not configured on this proxy"))
		return
	}
	if errCode := r.URL.Query().Get("error"); errCode != "" {
		writeProxyError(w, apierr.Newf(400, "sso_denied", "the identity provider reported: %s", errCode))
		return
	}
	expected := s.readStateCookie(r)
	received := r.URL.Query().Get("state")
	if expected == "" || received == "" ||
		subtle.ConstantTimeCompare([]byte(expected), []byte(received)) != 1 {
		// A parallel login overwrites the single state cookie, so the older
		// tab's callback arrives with a clobbered state. Retry the whole login
		// once; the marker on the retried state stops a second attempt, so a
		// browser that never sends the cookie gets an error, not a loop. The
		// authorization code is dropped, never exchanged, so a forged callback
		// cannot ride the retry into a session.
		if received != "" && !strings.HasSuffix(received, retryStateSuffix) {
			http.Redirect(w, r, "/auth/login?retry=1", http.StatusSeeOther)
			return
		}
		writeProxyError(w, apierr.New(400, "sso_state_mismatch",
			"login state did not match (are cookies enabled?); start again from /auth/login"))
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeProxyError(w, apierr.New(400, "sso_code_missing", "no authorization code in callback"))
		return
	}
	accessToken, err := s.sso.Exchange(r.Context(), code)
	if err != nil {
		slog.Error("sso code exchange failed", "error", err)
		writeProxyError(w, apierr.New(502, "sso_exchange_failed", "could not exchange the authorization code"))
		return
	}
	identity, err := s.sso.Userinfo(r.Context(), accessToken)
	if err != nil {
		slog.Error("sso userinfo failed", "error", err)
		writeProxyError(w, apierr.New(502, "sso_userinfo_failed", "could not fetch identity from the provider"))
		return
	}
	if s.cfg.OIDCRequiredGroup != "" && !slices.Contains(identity.Groups, s.cfg.OIDCRequiredGroup) {
		writeProxyError(w, apierr.Newf(403, "sso_group_required",
			"your account is not in the required group '%s'", s.cfg.OIDCRequiredGroup))
		return
	}
	role := "member"
	if s.cfg.OIDCAdminGroup != "" && slices.Contains(identity.Groups, s.cfg.OIDCAdminGroup) {
		role = "admin"
	}
	principal, err := s.store.UpsertSSOPrincipal(r.Context(),
		identity.Sub, identity.Email, identity.PreferredName, role)
	if err != nil {
		slog.Error("sso principal upsert failed", "error", err)
		internalErr(w, "failed to provision your account")
		return
	}
	s.clearCookie(w, r, stateCookieName)
	if err := s.issueSessionCookie(w, r, principal.ID); err != nil {
		slog.Error("session creation failed", "error", err)
		internalErr(w, "failed to create your session")
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	// Delete the session row, not just the cookie: a stolen copy of the
	// token dies here too. Legacy signed cookies have no row to delete.
	if cookie, err := r.Cookie(sessionCookieName); err == nil &&
		cookie.Value != "" && !strings.Contains(cookie.Value, ".") {
		if err := s.store.DeleteSessionByHash(r.Context(),
			secrets.HashAPIKey(s.secret, cookie.Value)); err != nil {
			slog.Warn("failed to delete session on logout", "error", err)
		}
	}
	s.clearCookie(w, r, sessionCookieName)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
