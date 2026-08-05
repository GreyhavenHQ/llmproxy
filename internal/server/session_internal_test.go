package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monadical/llmproxy/internal/config"
	"github.com/monadical/llmproxy/internal/secrets"
	"github.com/monadical/llmproxy/internal/store"
)

func newSessionTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return New(config.Config{SessionTTL: time.Hour}, st, []byte("test-secret"))
}

// tokenAt builds a current-format token with an explicit issue time.
func tokenAt(s *Server, value string, issued time.Time, ttl time.Duration) string {
	payload := value + "." + strconv.FormatInt(issued.Unix(), 10) +
		"." + strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	return payload + "." + s.signToken(payload)
}

// legacyToken builds a token in the pre-issuedAt layout ("value.expiry.sig")
// as issued by releases before the revocation watermark existed.
func legacyToken(s *Server, value string, ttl time.Duration) string {
	payload := value + "." + strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	return payload + "." + s.signToken(payload)
}

func TestVerifyTokenFormats(t *testing.T) {
	s := newSessionTestServer(t)

	value, issuedAt := s.verifyToken(s.makeToken("principal-1", time.Hour))
	if value != "principal-1" {
		t.Fatalf("roundtrip value: %q", value)
	}
	if drift := time.Now().Unix() - issuedAt; drift < 0 || drift > 5 {
		t.Fatalf("issuedAt drift: %d", drift)
	}

	// Legacy tokens stay valid across the format change (no logout on deploy).
	value, issuedAt = s.verifyToken(legacyToken(s, "principal-1", time.Hour))
	if value != "principal-1" || issuedAt != 0 {
		t.Fatalf("legacy token: value=%q issuedAt=%d", value, issuedAt)
	}

	badIssued := "p.notanumber." + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	badIssued += "." + s.signToken(badIssued)
	for name, token := range map[string]string{
		"expired":        tokenAt(s, "p", time.Now(), -time.Minute),
		"expired legacy": legacyToken(s, "p", -time.Minute),
		"tampered":       s.makeToken("p", time.Hour) + "0",
		"four-part":      "a.b.c.d.e",
		"no signature":   "principal-1",
		"empty":          "",
		"bad issuedAt":   badIssued,
	} {
		if value, _ := s.verifyToken(token); value != "" {
			t.Errorf("%s token must be rejected, got %q", name, value)
		}
	}
}

func authWith(t *testing.T, s *Server, token string) *Auth {
	t.Helper()
	req := httptest.NewRequest("GET", "/my/keys", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	auth, perr := s.sessionAuth(req)
	if perr != nil {
		t.Fatalf("unexpected error: %v", perr)
	}
	return auth
}

// TestLegacySessionCompat covers the signed-cookie sessions issued before the
// session table: they keep authenticating across the upgrade (no logout on
// deploy) and the revocation watermark sweeps them, since they have no row to
// delete.
func TestLegacySessionCompat(t *testing.T) {
	s := newSessionTestServer(t)
	ctx := context.Background()
	principal, err := s.store.GetOrCreatePrincipal(ctx, "alice", "user", "member", nil)
	if err != nil {
		t.Fatal(err)
	}

	old := tokenAt(s, principal.ID, time.Now().Add(-time.Hour), time.Hour)
	legacy := legacyToken(s, principal.ID, time.Hour)

	for name, token := range map[string]string{"three-field": old, "two-field": legacy} {
		if authWith(t, s, token) == nil {
			t.Fatalf("%s signed token must authenticate before revocation", name)
		}
	}

	if _, err := s.store.RevokePrincipalSessions(ctx, principal.ID, nil); err != nil {
		t.Fatal(err)
	}

	if authWith(t, s, old) != nil {
		t.Fatal("pre-watermark signed token must be rejected")
	}
	if authWith(t, s, legacy) != nil {
		t.Fatal("two-field signed token must be rejected after revocation")
	}
}

func TestDBSessionLifecycle(t *testing.T) {
	s := newSessionTestServer(t)
	ctx := context.Background()
	principal, err := s.store.GetOrCreatePrincipal(ctx, "alice", "user", "admin", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Issue: cookie value is opaque (no dots) and authenticates via the row.
	rec := httptest.NewRecorder()
	if err := s.issueSessionCookie(rec, httptest.NewRequest("GET", "/auth/callback", nil), principal.ID); err != nil {
		t.Fatal(err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName {
		t.Fatalf("expected one session cookie, got %v", cookies)
	}
	token := cookies[0].Value
	if strings.Contains(token, ".") {
		t.Fatalf("opaque token must not contain dots: %q", token)
	}
	auth := authWith(t, s, token)
	if auth == nil || auth.Role != "admin" || !auth.ViaSession {
		t.Fatalf("session must authenticate with the principal's role: %+v", auth)
	}

	// Revocation deletes the row and reports the count; the token dies even
	// though the cookie still exists client-side.
	deleted, err := s.store.RevokePrincipalSessions(ctx, principal.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted sessions: %d", deleted)
	}
	if authWith(t, s, token) != nil {
		t.Fatal("deleted session must not authenticate")
	}

	// An expired row never authenticates.
	expired := "expired-token-value"
	if err := s.store.CreateSession(ctx, principal.ID,
		secrets.HashAPIKey(s.secret, expired),
		time.Now().Add(-time.Hour).UTC().Format(store.TimeFormat)); err != nil {
		t.Fatal(err)
	}
	if authWith(t, s, expired) != nil {
		t.Fatal("expired session must not authenticate")
	}
}

func TestClearCookieDeletes(t *testing.T) {
	s := newSessionTestServer(t)
	rec := httptest.NewRecorder()
	s.clearCookie(rec, httptest.NewRequest("GET", "/auth/logout", nil), sessionCookieName)
	header := rec.Header().Get("Set-Cookie")
	if !strings.Contains(header, "Max-Age=0") {
		t.Fatalf("clearCookie must delete the cookie, got %q", header)
	}
}

func TestSecureCookieWidening(t *testing.T) {
	s := newSessionTestServer(t)
	principal, err := s.store.GetOrCreatePrincipal(context.Background(), "alice", "user", "member", nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*http.Request)
		secure bool
	}{
		{"plain http", func(r *http.Request) {}, false},
		{"direct tls", func(r *http.Request) { r.URL.Scheme = "https" }, true},
		{"forwarded https", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }, true},
		{"forwarded list", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https, http") }, true},
		{"forwarded http", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "http") }, false},
	}
	for _, tc := range cases {
		target := "http://proxy.internal/auth/callback"
		if tc.name == "direct tls" {
			target = "https://proxy.internal/auth/callback" // sets r.TLS in httptest
		}
		req := httptest.NewRequest("GET", target, nil)
		tc.mutate(req)
		rec := httptest.NewRecorder()
		if err := s.issueSessionCookie(rec, req, principal.ID); err != nil {
			t.Fatal(err)
		}
		got := strings.Contains(rec.Header().Get("Set-Cookie"), "Secure")
		if got != tc.secure {
			t.Errorf("%s: Secure=%v, want %v (%q)", tc.name, got, tc.secure, rec.Header().Get("Set-Cookie"))
		}
	}
}
