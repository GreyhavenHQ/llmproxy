package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/secrets"
	"github.com/greyhavenhq/llmproxy/internal/store"
)

// Browser sessions for the built-in UI. The session cookie holds an opaque
// random token whose keyed hash lives in the session table; logout and
// revocation delete the row (same philosophy as API keys: delete, don't
// flag). Signed HMAC session cookies from earlier releases
// ("value[.issuedAt].expiry.signature") are still accepted until they expire
// so a deploy never logs anyone out; that legacy path, and the revocation
// watermark that sweeps it, drain within one SessionTTL.
//
// The OAuth state cookie stays a signed stateless token: it lives for ten
// minutes and only ever binds a login attempt to a browser, so a server-side
// row would buy nothing.

const sessionCookieName = "llmproxy_session"
const stateCookieName = "llmproxy_oauth_state"

func deriveSessionKey(secret []byte) []byte {
	sum := sha256.Sum256(append(append([]byte{}, secret...), []byte("/sessions")...))
	return sum[:]
}

func (s *Server) signToken(payload string) string {
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) makeToken(value string, ttl time.Duration) string {
	now := time.Now()
	payload := value + "." + strconv.FormatInt(now.Unix(), 10) +
		"." + strconv.FormatInt(now.Add(ttl).Unix(), 10)
	return payload + "." + s.signToken(payload)
}

// verifyToken returns the embedded value and issue time (unix seconds), or
// ("", 0) for a bad or expired token. Legacy two-field payloads verify with a
// zero issue time.
func (s *Server) verifyToken(token string) (value string, issuedAt int64) {
	lastDot := strings.LastIndexByte(token, '.')
	if lastDot < 0 {
		return "", 0
	}
	payload, signature := token[:lastDot], token[lastDot+1:]
	if !hmac.Equal([]byte(s.signToken(payload)), []byte(signature)) {
		return "", 0
	}
	parts := strings.Split(payload, ".")
	var expiryStr string
	switch len(parts) {
	case 2: // legacy: value.expiry
		expiryStr = parts[1]
	case 3:
		issued, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return "", 0
		}
		issuedAt = issued
		expiryStr = parts[2]
	default:
		return "", 0
	}
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil || time.Now().Unix() >= expiry {
		return "", 0
	}
	return parts[0], issuedAt
}

// requestIsHTTPS reports whether the request arrived over TLS, directly or
// via a TLS-terminating reverse proxy. It can only widen the Secure cookie
// flag: forging X-Forwarded-Proto on a plain-HTTP request yields a Secure
// cookie the same browser then refuses to send back, breaking only that
// caller's own login.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if comma := strings.IndexByte(proto, ','); comma >= 0 {
		proto = proto[:comma]
	}
	return strings.TrimSpace(proto) == "https"
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge time.Duration) {
	maxAgeSeconds := int(maxAge.Seconds())
	if maxAge < 0 {
		// int((-1ns).Seconds()) truncates to 0, which net/http renders as no
		// Max-Age at all; force the delete-now value.
		maxAgeSeconds = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAgeSeconds,
		HttpOnly: true,
		Secure:   s.cookieSecure || requestIsHTTPS(r),
		// Lax, not Strict: the cookie must survive the top-level redirect
		// back from the IdP. Mutations are additionally origin-checked.
		SameSite: http.SameSiteLaxMode,
	})
}

// issueSessionCookie mints an opaque token, stores its keyed hash and sets
// the cookie.
func (s *Server) issueSessionCookie(w http.ResponseWriter, r *http.Request, principalID string) error {
	token := secrets.GenerateSessionToken()
	expiresAt := time.Now().Add(s.cfg.SessionTTL).UTC().Format(store.TimeFormat)
	if err := s.store.CreateSession(r.Context(), principalID,
		secrets.HashAPIKey(s.secret, token), expiresAt); err != nil {
		return err
	}
	s.setCookie(w, r, sessionCookieName, token, s.cfg.SessionTTL)
	return nil
}

func (s *Server) clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	s.setCookie(w, r, name, "", -1)
}

func (s *Server) issueStateCookie(w http.ResponseWriter, r *http.Request, state string) {
	s.setCookie(w, r, stateCookieName, s.makeToken(state, 10*time.Minute), 10*time.Minute)
}

func (s *Server) readStateCookie(r *http.Request) string {
	cookie, err := r.Cookie(stateCookieName)
	if err != nil {
		return ""
	}
	state, _ := s.verifyToken(cookie.Value)
	return state
}
