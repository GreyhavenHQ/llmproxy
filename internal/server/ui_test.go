package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/greyhavenhq/llmproxy/internal/store"
)

// The UI is a compiled React app embedded in the binary; these tests cover
// serving it and the JSON endpoints it depends on (/auth/me, password login,
// admin-session access to /admin/v1).

func TestUIServedFromBinary(t *testing.T) {
	e := newEnv(t)

	resp, body := e.request(t, "GET", "/", "", nil)
	page := string(body)
	if resp.StatusCode != 200 || !strings.Contains(page, `id="root"`) {
		t.Fatalf("index.html not served: %d %s", resp.StatusCode, page)
	}
	// The hashed bundle referenced by the page must itself be embedded.
	start := strings.Index(page, "/assets/")
	if start < 0 {
		t.Fatalf("no asset reference in index.html: %s", page)
	}
	asset := page[start:]
	asset = asset[:strings.IndexByte(asset, '"')]
	resp, _ = e.request(t, "GET", asset, "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("bundle %s not served: %d", asset, resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("hashed assets should be cacheable: %q", cc)
	}

	// Unknown paths fall back to the SPA, API routes do not.
	resp, body = e.request(t, "GET", "/some/client/route", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `id="root"`) {
		t.Fatalf("SPA fallback: %d", resp.StatusCode)
	}
	resp, body = e.request(t, "GET", "/v1/models", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"object":"list"`) {
		t.Fatalf("API routes must not be shadowed by the SPA: %d %s", resp.StatusCode, body)
	}
}

// /models is both a page of the SPA and the root-level alias of GET
// /v1/models. A browser reload (Accept: text/html) must get the app back;
// API clients keep getting JSON, and /v1/models is JSON no matter what.
func TestModelsPageSurvivesReload(t *testing.T) {
	e := newEnv(t)

	browserGet := func(path string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest("GET", e.proxy.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return resp, string(body)
	}

	resp, page := browserGet("/models")
	if resp.StatusCode != 200 || !strings.Contains(page, `id="root"`) {
		t.Fatalf("browser reload of /models should serve the SPA: %d %s", resp.StatusCode, page)
	}

	// An API client (no text/html in Accept) still reaches the alias; the
	// model list is public, so no key is needed.
	resp, body := e.request(t, "GET", "/models", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"object":"list"`) {
		t.Fatalf("API client on /models: %d %s", resp.StatusCode, body)
	}

	// /v1/models stays JSON even for a text/html Accept.
	resp, page = browserGet("/v1/models")
	if resp.StatusCode != 200 || strings.Contains(page, `id="root"`) {
		t.Fatalf("/v1/models must never serve the SPA: %d %s", resp.StatusCode, page)
	}
}

// A client calling an unknown path or an unsupported OpenAI endpoint must
// get an explanatory JSON error, not a bare 405 or the SPA.
func TestUnknownEndpointsReturnJSON(t *testing.T) {
	e := newEnv(t)

	// A base URL without /v1 works: the OpenAI routes are aliased at the
	// root, LiteLLM-style. An empty body reaches the real handler.
	resp, body := e.request(t, "POST", "/chat/completions", e.memberKey, map[string]any{})
	if resp.StatusCode != 400 || errorCode(t, body) != "model_required" {
		t.Fatalf("root chat alias should reach the handler: %d %s", resp.StatusCode, body)
	}

	// Unsupported OpenAI Responses API, with and without /v1.
	for _, path := range []string{"/v1/responses", "/responses"} {
		resp, body = e.request(t, "POST", path, e.memberKey, map[string]any{})
		if resp.StatusCode != 404 || !strings.Contains(string(body), "chat/completions") {
			t.Fatalf("%s hint: %d %s", path, resp.StatusCode, body)
		}
	}

	// Wrong method on a known path gets JSON too.
	resp, body = e.request(t, "GET", "/v1/chat/completions", e.memberKey, nil)
	if resp.StatusCode != 404 || errorCode(t, body) != "unknown_endpoint" {
		t.Fatalf("wrong method: %d %s", resp.StatusCode, body)
	}

	// Unknown GET API paths return JSON, not the SPA's HTML.
	resp, body = e.request(t, "GET", "/v1/nope", e.memberKey, nil)
	if resp.StatusCode != 404 || strings.Contains(string(body), "<html") {
		t.Fatalf("API 404 must be JSON: %d %s", resp.StatusCode, body)
	}
}

func TestAuthMe(t *testing.T) {
	e := newEnv(t)

	_, body := e.request(t, "GET", "/auth/me", "", nil)
	me := decode(t, body)
	if me["authenticated"] != false || me["password_enabled"] != true || me["sso_enabled"] != false {
		t.Fatalf("unauthenticated /auth/me: %v", me)
	}

	_, body = e.request(t, "GET", "/auth/me", e.memberKey, nil)
	me = decode(t, body)
	if me["authenticated"] != true || me["name"] != "bob" || me["role"] != "member" {
		t.Fatalf("member /auth/me: %v", me)
	}
}

func adminPassword(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "admin-password"))
	if err != nil {
		t.Fatalf("bootstrap should have generated the admin password file: %v", err)
	}
	return strings.TrimSpace(string(data))
}

// jsonReq sends a JSON request through the browser client with an Origin.
func jsonReq(t *testing.T, client *http.Client, method, url, origin string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(data))
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	_ = json.Unmarshal(data, &decoded)
	return resp, decoded
}

func TestAdminPasswordLoginAndAdminSession(t *testing.T) {
	e := newEnv(t)
	client := browserClient(t)
	password := adminPassword(t, e.dir)

	// Wrong password: distinct code, no session.
	resp, body := jsonReq(t, client, "POST", e.proxy.URL+"/auth/password", e.proxy.URL,
		map[string]any{"password": "nope"})
	if resp.StatusCode != 401 {
		t.Fatalf("wrong password: %d %v", resp.StatusCode, body)
	}
	if resp, _ := client.Get(e.proxy.URL + "/my/keys"); resp.StatusCode != 401 {
		t.Fatal("failed login must not create a session")
	}

	// Cross-origin login is rejected.
	resp, _ = jsonReq(t, client, "POST", e.proxy.URL+"/auth/password", "https://evil.example.com",
		map[string]any{"password": password})
	if resp.StatusCode != 403 {
		t.Fatalf("cross-origin password login must 403: %d", resp.StatusCode)
	}

	// Correct password: session established with the admin role.
	resp, body = jsonReq(t, client, "POST", e.proxy.URL+"/auth/password", e.proxy.URL,
		map[string]any{"password": password})
	if resp.StatusCode != 200 || body["role"] != "admin" {
		t.Fatalf("password login: %d %v", resp.StatusCode, body)
	}
	if _, me := jsonReq(t, client, "GET", e.proxy.URL+"/auth/me", "", nil); me["authenticated"] != true || me["role"] != "admin" {
		t.Fatalf("session /auth/me: %v", me)
	}

	// The admin session drives the admin API: register a provider, bind a
	// model, and the alias serves traffic.
	resp, body = jsonReq(t, client, "POST", e.proxy.URL+"/admin/v1/providers", e.proxy.URL, map[string]any{
		"name": "fake2", "wire_format": "openai",
		"base_url": e.upstream.srv.URL + "/v1", "api_key": upstreamKey,
	})
	if resp.StatusCode != 201 {
		t.Fatalf("provider create via session: %d %v", resp.StatusCode, body)
	}
	resp, body = jsonReq(t, client, "POST", e.proxy.URL+"/admin/v1/models", e.proxy.URL, map[string]any{
		"alias": "beta", "provider": "fake2", "upstream_name": "m-alpha",
		"capabilities": []string{"chat", "chat_stream"},
	})
	if resp.StatusCode != 201 {
		t.Fatalf("model create via session: %d %v", resp.StatusCode, body)
	}
	chat, chatBody := e.request(t, "POST", "/v1/chat/completions", e.memberKey, map[string]any{
		"model":    "beta",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if chat.StatusCode != 200 {
		t.Fatalf("chat through the UI-created provider/model failed: %d %s", chat.StatusCode, chatBody)
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "beta" })

	// The request log shows the member's call: who, model, tokens; no content.
	_, body = jsonReq(t, client, "GET", e.proxy.URL+"/admin/v1/requests", "", nil)
	requests, _ := body["requests"].([]any)
	if len(requests) == 0 {
		t.Fatalf("request log empty: %v", body)
	}
	entry, _ := requests[0].(map[string]any)
	units, _ := entry["units"].(map[string]any)
	// input is normalised to the non-cached input: prompt 7 minus cached 2.
	if entry["principal"] != "bob" || entry["model"] != "beta" ||
		units["input_tokens"] != float64(5) || units["output_tokens"] != float64(5) ||
		units["cached_input_tokens"] != float64(2) {
		t.Fatalf("request log entry wrong: %v", entry)
	}

	// Session mutations still require a matching Origin.
	resp, _ = jsonReq(t, client, "DELETE", e.proxy.URL+"/admin/v1/models/beta", "https://evil.example.com", nil)
	if resp.StatusCode != 403 {
		t.Fatalf("cross-origin admin mutation must 403: %d", resp.StatusCode)
	}

	// Member keys stay locked out of the admin API.
	resp, _ = e.request(t, "GET", "/admin/v1/providers", e.memberKey, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("member on admin API must 403: %d", resp.StatusCode)
	}
}

func TestPasswordLoginRemainsWithSSO(t *testing.T) {
	idp := newFakeIdP(t)
	e := newSSOEnv(t, idp, "", "") // no admin group: SSO users are members

	// Both login methods are advertised.
	_, body := e.request(t, "GET", "/auth/me", "", nil)
	me := decode(t, body)
	if me["sso_enabled"] != true || me["password_enabled"] != true {
		t.Fatalf("/auth/me in SSO mode: %v", me)
	}

	// Password login works as break-glass admin.
	client := browserClient(t)
	resp, loginBody := jsonReq(t, client, "POST", e.proxy.URL+"/auth/password", e.proxy.URL,
		map[string]any{"password": adminPassword(t, e.dir)})
	if resp.StatusCode != 200 || loginBody["role"] != "admin" {
		t.Fatalf("break-glass login: %d %v", resp.StatusCode, loginBody)
	}
	resp, _ = jsonReq(t, client, "GET", e.proxy.URL+"/admin/v1/providers", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("break-glass admin session on admin API: %d", resp.StatusCode)
	}

	// An SSO member session stays locked out of the admin API.
	member := browserClient(t)
	state := login(t, member, e.proxy.URL)
	cb, err := member.Get(e.proxy.URL + "/auth/callback?code=good-code&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatal(err)
	}
	cb.Body.Close()
	resp, _ = jsonReq(t, member, "GET", e.proxy.URL+"/admin/v1/providers", "", nil)
	if resp.StatusCode != 403 {
		t.Fatalf("SSO member on admin API must 403: %d", resp.StatusCode)
	}
	resp, _ = jsonReq(t, member, "GET", e.proxy.URL+"/my/keys", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("SSO member session on self-service: %d", resp.StatusCode)
	}
}
