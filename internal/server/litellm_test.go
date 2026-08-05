package server_test

import (
	"strings"
	"testing"
)

// Exercises the LiteLLM management-API compatibility surface with the exact
// payload shapes the existing registration tooling sends.

func TestLiteLLMManagementCompat(t *testing.T) {
	e := newEnv(t)

	// POST /model/new, LiteLLM deployment shape (extra fields are ignored).
	payload := map[string]any{
		"model_name":         "monadical/zdr-kimi",
		"provider":           "openai",
		"litellm_model_name": "moonshotai/kimi-k2",
		"litellm_params": map[string]any{
			"model":               "moonshotai/kimi-k2",
			"custom_llm_provider": "openai",
			"api_base":            e.upstream.srv.URL + "/v1",
			"api_key":             upstreamKey,
		},
		"model_info": map[string]any{"supports_vision": true},
	}
	resp, body := e.request(t, "POST", "/model/new", e.adminKey, payload)
	if resp.StatusCode != 200 {
		t.Fatalf("/model/new: %d %s", resp.StatusCode, body)
	}
	created := decode(t, body)
	info, _ := created["model_info"].(map[string]any)
	deploymentID, _ := info["id"].(string)
	if created["model_name"] != "monadical/zdr-kimi" || deploymentID == "" {
		t.Fatalf("/model/new view: %v", created)
	}
	if strings.Contains(string(body), upstreamKey) {
		t.Fatal("/model/new must not echo the upstream key")
	}

	// Idempotent: the same deployment again is a 200, not a conflict.
	resp, body = e.request(t, "POST", "/model/new", e.adminKey, payload)
	if resp.StatusCode != 200 {
		t.Fatalf("re-register: %d %s", resp.StatusCode, body)
	}

	// A different mapping under the same model_name is rejected (aliases are
	// unique; llmproxy does not do LiteLLM-style multi-deployment balancing).
	conflicting := map[string]any{
		"model_name": "monadical/zdr-kimi",
		"litellm_params": map[string]any{
			"model":    "some-other-model",
			"api_base": e.upstream.srv.URL + "/v1",
		},
	}
	resp, body = e.request(t, "POST", "/model/new", e.adminKey, conflicting)
	if resp.StatusCode != 409 || errorCode(t, body) != "alias_exists" {
		t.Fatalf("conflicting deployment: %d %s", resp.StatusCode, body)
	}

	// A deployment on an already-known api_base reuses that provider (the
	// seeded "fake" provider points at the same upstream).
	_, body = e.request(t, "GET", "/admin/v1/providers", e.adminKey, nil)
	if got := strings.Count(string(body), `"base_url"`); got != 1 {
		t.Fatalf("same api_base must reuse the provider, got: %s", body)
	}

	// A new api_base creates a provider named after its host.
	resp, body = e.request(t, "POST", "/model/new", e.adminKey, map[string]any{
		"model_name": "kimi-embed",
		"litellm_params": map[string]any{
			"model":    "m-embed",
			"api_base": e.upstream.srv.URL + "/other/v1",
		},
		"model_info": map[string]any{"mode": "embedding"},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("embedding register: %d %s", resp.StatusCode, body)
	}
	_, body = e.request(t, "GET", "/admin/v1/providers", e.adminKey, nil)
	if got := strings.Count(string(body), `"base_url"`); got != 2 {
		t.Fatalf("new api_base must create a provider, got: %s", body)
	}
	if !strings.Contains(string(body), `"name":"127.0.0.1-`) {
		t.Fatalf("derived provider name missing: %s", body)
	}

	// GET /model/info lists deployments in LiteLLM shape; the tooling matches
	// on model_name + litellm_params.model and reads model_info.id.
	resp, body = e.request(t, "GET", "/model/info", e.adminKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/model/info: %d %s", resp.StatusCode, body)
	}
	listing := decode(t, body)
	deployments, _ := listing["data"].([]any)
	found := false
	for _, d := range deployments {
		dep, _ := d.(map[string]any)
		params, _ := dep["litellm_params"].(map[string]any)
		depInfo, _ := dep["model_info"].(map[string]any)
		if dep["model_name"] == "monadical/zdr-kimi" && params["model"] == "moonshotai/kimi-k2" {
			if depInfo["id"] != deploymentID {
				t.Fatalf("deployment id mismatch: %v vs %v", depInfo["id"], deploymentID)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("registered deployment missing from /model/info: %s", body)
	}

	// The registered alias serves chat, including at the root path (LiteLLM
	// serves the OpenAI routes without /v1 too).
	resp, body = e.request(t, "POST", "/chat/completions", e.memberKey, map[string]any{
		"model":    "monadical/zdr-kimi",
		"messages": []map[string]any{{"role": "user", "content": "hello"}},
	})
	if resp.StatusCode != 200 || !strings.Contains(string(body), "hello from the fake upstream") {
		t.Fatalf("root chat via registered model: %d %s", resp.StatusCode, body)
	}
	if e.upstream.last(t).Path != "/v1/chat/completions" {
		t.Fatalf("upstream path: %s", e.upstream.last(t).Path)
	}

	// POST /model/delete by deployment id; the alias stops resolving.
	resp, body = e.request(t, "POST", "/model/delete", e.adminKey, map[string]any{"id": deploymentID})
	if resp.StatusCode != 200 {
		t.Fatalf("/model/delete: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.request(t, "POST", "/v1/chat/completions", e.memberKey, map[string]any{
		"model":    "monadical/zdr-kimi",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode != 404 {
		t.Fatalf("deleted deployment should 404: %d", resp.StatusCode)
	}
	resp, body = e.request(t, "POST", "/model/delete", e.adminKey, map[string]any{"id": deploymentID})
	if resp.StatusCode != 404 || errorCode(t, body) != "model_not_found" {
		t.Fatalf("double delete: %d %s", resp.StatusCode, body)
	}

	// Management endpoints require the admin role.
	resp, _ = e.request(t, "GET", "/model/info", e.memberKey, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("member on /model/info: %d", resp.StatusCode)
	}
}

// The root-level OpenAI aliases serve the same handlers as /v1.
func TestRootOpenAIAliases(t *testing.T) {
	e := newEnv(t)

	resp, body := e.request(t, "GET", "/models", e.memberKey, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"alpha"`) {
		t.Fatalf("GET /models: %d %s", resp.StatusCode, body)
	}
	resp, body = e.request(t, "POST", "/embeddings", e.memberKey, map[string]any{
		"model": "embed-only", "input": "hello",
	})
	if resp.StatusCode != 200 || !strings.Contains(string(body), "embedding") {
		t.Fatalf("POST /embeddings: %d %s", resp.StatusCode, body)
	}
}
