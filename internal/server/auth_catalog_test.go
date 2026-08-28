package server_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestAuthErrors(t *testing.T) {
	e := newEnv(t)

	resp, body := e.request(t, "GET", "/my/keys", "", nil)
	if resp.StatusCode != 401 || errorCode(t, body) != "missing_api_key" {
		t.Fatalf("want 401 missing_api_key, got %d %s", resp.StatusCode, body)
	}
	if decode(t, body)["llmproxy"].(map[string]any)["source"] != "proxy" {
		t.Fatal("proxy errors must be marked source=proxy")
	}

	resp, body = e.request(t, "GET", "/my/keys", "lp_not_a_real_key", nil)
	if resp.StatusCode != 401 || errorCode(t, body) != "invalid_api_key" {
		t.Fatalf("want 401 invalid_api_key, got %d %s", resp.StatusCode, body)
	}

	// The model list is public: no key required, JSON either way.
	resp, body = e.request(t, "GET", "/v1/models", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"object":"list"`) {
		t.Fatalf("/v1/models must be public: %d %s", resp.StatusCode, body)
	}

	// x-api-key header also works.
	req, _ := http.NewRequest("GET", e.proxy.URL+"/my/keys", nil)
	req.Header.Set("x-api-key", e.memberKey)
	xresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	xresp.Body.Close()
	if xresp.StatusCode != 200 {
		t.Fatalf("x-api-key auth failed: %d", xresp.StatusCode)
	}
}

func TestSelfServiceKeyLifecycle(t *testing.T) {
	e := newEnv(t)

	resp, body := e.request(t, "POST", "/my/keys", e.memberKey, map[string]string{"label": "laptop"})
	if resp.StatusCode != 201 {
		t.Fatalf("create key: %d %s", resp.StatusCode, body)
	}
	created := decode(t, body)
	newKey, _ := created["key"].(string)
	if !strings.HasPrefix(newKey, "lp_") {
		t.Fatalf("expected lp_ key, got %q", newKey)
	}

	// The plaintext is never listed again; only the display suffix is.
	resp, body = e.request(t, "GET", "/my/keys", e.memberKey, nil)
	if resp.StatusCode != 200 || strings.Contains(string(body), newKey) {
		t.Fatalf("plaintext key leaked in listing: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"key_suffix":"`+newKey[len(newKey)-4:]+`"`) {
		t.Fatalf("listing must carry the last-4 display suffix: %s", body)
	}

	// The new key authenticates.
	resp, _ = e.request(t, "GET", "/my/keys", newKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("new key rejected: %d", resp.StatusCode)
	}

	// Deletion is the revocation mechanism: immediate, and the key is gone.
	resp, body = e.request(t, "DELETE", "/my/keys/"+created["id"].(string), e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
	resp, body = e.request(t, "GET", "/my/keys", newKey, nil)
	if resp.StatusCode != 401 || errorCode(t, body) != "invalid_api_key" {
		t.Fatalf("want 401 invalid_api_key, got %d %s", resp.StatusCode, body)
	}
	_, body = e.request(t, "GET", "/my/keys", e.memberKey, nil)
	if strings.Contains(string(body), created["id"].(string)) {
		t.Fatalf("deleted key must not be listed: %s", body)
	}
}

func TestMemberCannotUseAdminAPI(t *testing.T) {
	e := newEnv(t)
	resp, body := e.request(t, "GET", "/admin/v1/providers", e.memberKey, nil)
	if resp.StatusCode != 403 || errorCode(t, body) != "admin_required" {
		t.Fatalf("want 403 admin_required, got %d %s", resp.StatusCode, body)
	}
}

func modelIDs(t *testing.T, body []byte) map[string]bool {
	t.Helper()
	out := make(map[string]bool)
	for _, m := range decode(t, body)["data"].([]any) {
		out[m.(map[string]any)["id"].(string)] = true
	}
	return out
}

func TestModelListFiltering(t *testing.T) {
	e := newEnv(t)

	resp, body := e.request(t, "GET", "/v1/models", e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list models: %d %s", resp.StatusCode, body)
	}
	ids := modelIDs(t, body)
	if !ids["alpha"] || !ids["embed-only"] || ids["hidden"] {
		t.Fatalf("wrong visibility: %v", ids)
	}

	_, body = e.request(t, "GET", "/v1/models?endpoint=chat", e.memberKey, nil)
	ids = modelIDs(t, body)
	if !ids["alpha"] || ids["embed-only"] {
		t.Fatalf("endpoint filter failed: %v", ids)
	}
}

// The public model list carries the curated metadata a caller needs to pick a
// model: what it can do, and whether the name is an alias for another one.
func TestModelListMetadata(t *testing.T) {
	e := newEnv(t)

	// vision is a declarative capability: admins set it, routing ignores it.
	resp, body := e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "seer", "provider": "fake", "upstream_name": "m-alpha",
		"capabilities": []string{"chat", "chat_stream", "vision"},
	})
	if resp.StatusCode != 201 {
		t.Fatalf("bind a vision model: %d %s", resp.StatusCode, body)
	}
	resp, body = e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "smart", "target": "seer",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("bind the alias: %d %s", resp.StatusCode, body)
	}

	_, body = e.request(t, "GET", "/v1/models", "", nil)
	models := make(map[string]map[string]any)
	for _, m := range decode(t, body)["data"].([]any) {
		entry := m.(map[string]any)
		models[entry["id"].(string)] = entry
	}

	seer := models["seer"]
	if seer == nil {
		t.Fatalf("model missing from the list: %s", body)
	}
	if caps := capabilityList(t, seer); !caps["chat"] || !caps["vision"] || caps["completions"] {
		t.Fatalf("wrong capabilities: %v", caps)
	}
	if seer["alias_of"] != nil {
		t.Fatalf("a direct model has no alias target: %v", seer["alias_of"])
	}

	// An alias reports its target and serves its target's capabilities.
	smart := models["smart"]
	if smart == nil {
		t.Fatalf("alias missing from the list: %s", body)
	}
	if smart["alias_of"] != "seer" {
		t.Fatalf("alias_of = %v, want seer", smart["alias_of"])
	}
	if caps := capabilityList(t, smart); !caps["vision"] {
		t.Fatalf("alias did not inherit capabilities: %v", caps)
	}
}

func capabilityList(t *testing.T, entry map[string]any) map[string]bool {
	t.Helper()
	raw, ok := entry["capabilities"].([]any)
	if !ok {
		t.Fatalf("capabilities missing on %v", entry["id"])
	}
	out := make(map[string]bool)
	for _, c := range raw {
		out[c.(string)] = true
	}
	return out
}

func TestCatalogRejections(t *testing.T) {
	e := newEnv(t)
	chat := func(model string) (*http.Response, []byte) {
		return e.request(t, "POST", "/v1/chat/completions", e.memberKey,
			map[string]any{"model": model, "messages": []any{}})
	}

	resp, body := chat("nope")
	if resp.StatusCode != 404 || errorCode(t, body) != "model_not_found" {
		t.Fatalf("unknown model: %d %s", resp.StatusCode, body)
	}

	// Wrong endpoint fails at the proxy, naming the supported capabilities.
	resp, body = chat("embed-only")
	if resp.StatusCode != 400 || errorCode(t, body) != "endpoint_not_supported" {
		t.Fatalf("wrong endpoint: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "embeddings") {
		t.Fatalf("error must name supported capabilities: %s", body)
	}

	// 'slow' has chat but not completions.
	resp, body = e.request(t, "POST", "/v1/completions", e.memberKey,
		map[string]any{"model": "slow", "prompt": "hi"})
	if resp.StatusCode != 400 || errorCode(t, body) != "endpoint_not_supported" {
		t.Fatalf("completions capability: %d %s", resp.StatusCode, body)
	}
}
