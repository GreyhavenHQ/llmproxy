package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monadical/llmproxy/internal/config"
	"github.com/monadical/llmproxy/internal/secrets"
	"github.com/monadical/llmproxy/internal/server"
	"github.com/monadical/llmproxy/internal/store"
)

// Fake IdP: discovery, token exchange and userinfo. The /authorize endpoint
// is never called in tests; the browser hop is simulated by hitting the
// proxy's callback directly with the state from the login redirect.
func newFakeIdP(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"authorization_endpoint":%q,"token_endpoint":%q,"userinfo_endpoint":%q}`,
			srv.URL+"/authorize", srv.URL+"/token", srv.URL+"/userinfo")
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" ||
			r.Form.Get("code") != "good-code" ||
			r.Form.Get("client_id") != "test-client" ||
			r.Form.Get("client_secret") != "test-secret" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at-1","token_type":"Bearer"}`)
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"sub":"idp-user-1","email":"bob@example.com",`+
			`"preferred_username":"Bob.SSO","groups":["staff","llm-admins"]}`)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newSSOEnv(t *testing.T, idp *httptest.Server, adminGroup, requiredGroup string) *env {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		DatabaseURL:       filepath.Join(dir, "proxy.db"),
		SecretFile:        filepath.Join(dir, "secret"),
		LocalAdminName:    "local-admin",
		AdminPasswordFile: filepath.Join(dir, "admin-password"),
		CatalogTTL:        0,
		MaxBodyBytes:      256 * 1024,
		SessionTTL:        time.Hour,
		OIDCIssuer:        idp.URL,
		OIDCClientID:      "test-client",
		OIDCClientSecret:  "test-secret",
		OIDCRedirectURL:   "http://127.0.0.1/auth/callback",
		OIDCScopes:        "openid profile email",
		OIDCGroupsClaim:   "groups",
		OIDCAdminGroup:    adminGroup,
		OIDCRequiredGroup: requiredGroup,
	}
	secret, err := secrets.LoadOrCreate("", cfg.SecretFile)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(cfg, st, secret)
	if err := srv.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		proxy.Close()
		srv.Drain()
		st.Close()
	})
	return &env{dir: dir, st: st, srv: srv, proxy: proxy, secret: secret, cfg: cfg}
}

func browserClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// login performs the /auth/login redirect and returns the state parameter.
func login(t *testing.T, client *http.Client, proxyURL string) string {
	t.Helper()
	return loginVia(t, client, proxyURL, "/auth/login")
}

func loginVia(t *testing.T, client *http.Client, proxyURL, path string) string {
	t.Helper()
	resp, err := client.Get(proxyURL + path)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatalf("login redirect: %d", resp.StatusCode)
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasSuffix(location.Path, "/authorize") {
		t.Fatalf("wrong authorize redirect: %v", resp.Header.Get("Location"))
	}
	state := location.Query().Get("state")
	if state == "" {
		t.Fatal("no state in authorize URL")
	}
	return state
}

func TestSSOLoginFlow(t *testing.T) {
	idp := newFakeIdP(t)
	e := newSSOEnv(t, idp, "llm-admins", "")
	client := browserClient(t)

	login(t, client, e.proxy.URL)

	// A wrong state is never exchanged; the browser gets one automatic retry.
	resp, err := client.Get(e.proxy.URL + "/auth/callback?code=good-code&state=wrong")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/auth/login?retry=1" {
		t.Fatalf("bad state must redirect to a retried login: %d %s",
			resp.StatusCode, resp.Header.Get("Location"))
	}

	// The real callback establishes a session.
	state := login(t, client, e.proxy.URL)
	resp, err = client.Get(e.proxy.URL + "/auth/callback?code=good-code&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("callback: %d", resp.StatusCode)
	}

	// The session reaches self-service endpoints.
	resp, err = client.Get(e.proxy.URL + "/my/keys")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("session not accepted on /my/keys: %d", resp.StatusCode)
	}

	// Mutations with a matching Origin work; a foreign Origin is rejected.
	makeKey := func(origin string) int {
		req, _ := http.NewRequest("POST", e.proxy.URL+"/my/keys", strings.NewReader(`{"label":"ui"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := makeKey(e.proxy.URL); code != 201 {
		t.Fatalf("same-origin key creation failed: %d", code)
	}
	if code := makeKey("http://evil.example"); code != 403 {
		t.Fatalf("cross-origin mutation must be rejected: %d", code)
	}
	// The public host from the OIDC redirect URL is accepted too, for
	// deployments where a reverse proxy rewrites Host.
	if code := makeKey("http://127.0.0.1"); code != 201 {
		t.Fatalf("public-host origin must be accepted: %d", code)
	}

	// An admin session reaches the admin API (bob is in llm-admins).
	resp, err = client.Get(e.proxy.URL + "/admin/v1/providers")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("admin session should reach the admin API: %d", resp.StatusCode)
	}

	// First login provisioned the principal: sub as join key, group => admin.
	principal, err := e.st.GetPrincipalByExternalSub(context.Background(), "idp-user-1")
	if err != nil || principal == nil {
		t.Fatalf("principal not provisioned: %v", err)
	}
	if principal.Role != "admin" || principal.Name != "bob.sso" || principal.Email.String != "bob@example.com" {
		t.Fatalf("wrong principal: %+v", principal)
	}

	// Logout invalidates the browser session server-side: even a stolen copy
	// of the cookie value is dead afterwards, not just the jar's cleared one.
	proxyURL, _ := url.Parse(e.proxy.URL)
	var stolen string
	for _, c := range client.Jar.Cookies(proxyURL) {
		if c.Name == "llmproxy_session" {
			stolen = c.Value
		}
	}
	if stolen == "" {
		t.Fatal("no session cookie in the jar")
	}
	resp, err = client.Get(e.proxy.URL + "/auth/logout")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = client.Get(e.proxy.URL + "/my/keys")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("session must be gone after logout: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", e.proxy.URL+"/my/keys", nil)
	req.AddCookie(&http.Cookie{Name: "llmproxy_session", Value: stolen})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("stolen cookie must be dead after logout: %d", resp.StatusCode)
	}
}

func TestSSORequiredGroupGate(t *testing.T) {
	idp := newFakeIdP(t)
	e := newSSOEnv(t, idp, "", "group-bob-is-not-in")
	client := browserClient(t)

	state := login(t, client, e.proxy.URL)
	resp, err := client.Get(e.proxy.URL + "/auth/callback?code=good-code&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("missing required group must be rejected: %d", resp.StatusCode)
	}
	resp, err = client.Get(e.proxy.URL + "/my/keys")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("no session should exist: %d", resp.StatusCode)
	}
}

func TestSSOStateClobberRetry(t *testing.T) {
	idp := newFakeIdP(t)
	e := newSSOEnv(t, idp, "", "")
	client := browserClient(t)

	stateA := login(t, client, e.proxy.URL) // tab A
	login(t, client, e.proxy.URL)           // tab B overwrites the state cookie

	// Tab A's callback no longer matches and gets one automatic retry.
	resp, err := client.Get(e.proxy.URL + "/auth/callback?code=good-code&state=" + url.QueryEscape(stateA))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/auth/login?retry=1" {
		t.Fatalf("clobbered state must retry: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	// The retried login carries a marked state and completes normally.
	retryState := loginVia(t, client, e.proxy.URL, "/auth/login?retry=1")
	if !strings.HasSuffix(retryState, "-retry") {
		t.Fatalf("retry state must be marked: %q", retryState)
	}
	resp, err = client.Get(e.proxy.URL + "/auth/callback?code=good-code&state=" + url.QueryEscape(retryState))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/" {
		t.Fatalf("retried callback must establish the session: %d", resp.StatusCode)
	}
	resp, err = client.Get(e.proxy.URL + "/my/keys")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("session not accepted after retry: %d", resp.StatusCode)
	}

	// A marked state that still mismatches errors instead of looping.
	client2 := browserClient(t)
	marked := loginVia(t, client2, e.proxy.URL, "/auth/login?retry=1")
	login(t, client2, e.proxy.URL) // clobbered again
	resp, err = client2.Get(e.proxy.URL + "/auth/callback?code=good-code&state=" + url.QueryEscape(marked))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("a second mismatch must stop retrying: %d", resp.StatusCode)
	}
}

func TestSessionRevocation(t *testing.T) {
	idp := newFakeIdP(t)
	e := newSSOEnv(t, idp, "", "")
	client := browserClient(t)

	state := login(t, client, e.proxy.URL)
	resp, err := client.Get(e.proxy.URL + "/auth/callback?code=good-code&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("callback: %d", resp.StatusCode)
	}

	ctx := context.Background()
	localAdmin, err := e.st.GetPrincipalByName(ctx, "local-admin")
	if err != nil || localAdmin == nil {
		t.Fatalf("no local admin: %v", err)
	}
	adminKey := e.mintKey(t, localAdmin.ID, "boot")
	bob, err := e.st.GetPrincipalByExternalSub(ctx, "idp-user-1")
	if err != nil || bob == nil {
		t.Fatalf("bob not provisioned: %v", err)
	}
	bobKey := e.mintKey(t, bob.ID, "bob-key")

	rsp, body := e.request(t, "POST", "/admin/v1/principals/"+bob.ID+"/revoke-sessions", adminKey, nil)
	if rsp.StatusCode != 200 {
		t.Fatalf("revoke-sessions: %d %s", rsp.StatusCode, body)
	}
	if n, _ := decode(t, body)["deleted_sessions"].(float64); n < 1 {
		t.Fatalf("expected at least one deleted session: %s", body)
	}
	rsp, body = e.request(t, "POST", "/admin/v1/principals/nope/revoke-sessions", adminKey, nil)
	if rsp.StatusCode != 404 || errorCode(t, body) != "principal_not_found" {
		t.Fatalf("unknown principal: %d %s", rsp.StatusCode, body)
	}
	rsp, _ = e.request(t, "POST", "/admin/v1/principals/"+bob.ID+"/revoke-sessions", bobKey, nil)
	if rsp.StatusCode != 403 {
		t.Fatalf("member must not revoke sessions: %d", rsp.StatusCode)
	}

	// The browser session is gone; the API key is untouched.
	resp, err = client.Get(e.proxy.URL + "/my/keys")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("revoked session must be rejected: %d", resp.StatusCode)
	}
	rsp, _ = e.request(t, "GET", "/my/keys", bobKey, nil)
	if rsp.StatusCode != 200 {
		t.Fatalf("API key must survive session revocation: %d", rsp.StatusCode)
	}

	// A fresh login works immediately after revocation.
	state = login(t, client, e.proxy.URL)
	resp, err = client.Get(e.proxy.URL + "/auth/callback?code=good-code&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("re-login callback: %d", resp.StatusCode)
	}
	resp, err = client.Get(e.proxy.URL + "/my/keys")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("session after re-login: %d", resp.StatusCode)
	}
}

func TestHomePageServed(t *testing.T) {
	e := newEnv(t)
	resp, body := e.request(t, "GET", "/", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "llmproxy") {
		t.Fatalf("home page: %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("wrong content type: %s", resp.Header.Get("Content-Type"))
	}
}
