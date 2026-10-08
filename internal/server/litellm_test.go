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
		"model_name":         "acme/zdr-kimi",
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
	if created["model_name"] != "acme/zdr-kimi" || deploymentID == "" {
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
		if dep["model_name"] == "acme/zdr-kimi" && params["model"] == "moonshotai/kimi-k2" {
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
		"model":    "acme/zdr-kimi",
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
		"model":    "acme/zdr-kimi",
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

// A second deployment under one model_name becomes another target of that alias.
func TestLiteLLMDeploymentsShareModelName(t *testing.T) {
	e := newEnv(t)
	other := strings.Replace(e.upstream.srv.URL, "127.0.0.1", "localhost", 1)
	deploy := func(model, base string, weight int) (int, map[string]any) {
		t.Helper()
		params := map[string]any{"model": model, "api_base": base, "api_key": upstreamKey}
		if weight > 0 {
			params["weight"] = weight
		}
		resp, body := e.request(t, "POST", "/model/new", e.adminKey, map[string]any{
			"model_name": "shared", "litellm_params": params,
		})
		return resp.StatusCode, decode(t, body)
	}
	idOf := func(view map[string]any) string {
		info, _ := view["model_info"].(map[string]any)
		id, _ := info["id"].(string)
		return id
	}
	resolve := func() map[string]any {
		t.Helper()
		resp, body := e.request(t, "GET", "/admin/v1/resolve?model=shared", e.adminKey, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("resolve: %d %s", resp.StatusCode, body)
		}
		return decode(t, body)
	}
	listing := func() []map[string]any {
		t.Helper()
		_, body := e.request(t, "GET", "/model/info", e.adminKey, nil)
		var out []map[string]any
		for _, d := range decode(t, body)["data"].([]any) {
			dep := d.(map[string]any)
			if strings.Contains(dep["model_name"].(string), "@") {
				t.Fatalf("a target created for a deployment is listed on its own: %v", dep)
			}
			if dep["model_name"] == "shared" {
				out = append(out, dep)
			}
		}
		return out
	}

	code, first := deploy("m1", e.upstream.srv.URL+"/v1", 0)
	if code != 200 {
		t.Fatalf("first deployment: %d %v", code, first)
	}
	code, second := deploy("m2", other+"/v1", 3)
	if code != 200 || second["model_name"] != "shared" || idOf(second) == "" || idOf(second) == idOf(first) {
		t.Fatalf("second deployment: %d %v", code, second)
	}
	if code, again := deploy("m2", other+"/v1", 3); code != 200 || idOf(again) != idOf(second) {
		t.Fatalf("re-register second: %d %v", code, again)
	}
	code, firstAgain := deploy("m1", e.upstream.srv.URL+"/v1", 0)
	if code != 200 {
		t.Fatalf("re-register first: %d %v", code, firstAgain)
	}

	plan := resolve()
	targets, _ := plan["targets"].([]any)
	if plan["strategy"] != "weighted" || len(targets) != 2 {
		t.Fatalf("plan after two deployments: %v", plan)
	}
	if w := targets[1].(map[string]any)["weight"]; w != float64(3) {
		t.Fatalf("weight from litellm_params: %v", w)
	}

	deps := listing()
	if len(deps) != 2 {
		t.Fatalf("/model/info deployments: %v", deps)
	}
	models := map[string]string{}
	for _, d := range deps {
		models[d["litellm_params"].(map[string]any)["model"].(string)] = idOf(d)
	}
	if models["m2"] != idOf(second) || models["m1"] == "" || models["m1"] != idOf(firstAgain) {
		t.Fatalf("/model/info ids: %v", models)
	}

	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey, map[string]any{
		"model": "shared", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("chat through shared: %d %s", resp.StatusCode, body)
	}

	resp, body = e.request(t, "POST", "/model/delete", e.adminKey, map[string]any{"id": idOf(second)})
	if resp.StatusCode != 200 || decode(t, body)["model_name"] != "shared" {
		t.Fatalf("delete one deployment: %d %s", resp.StatusCode, body)
	}
	plan = resolve()
	if plan["strategy"] != nil || plan["upstream_name"] != "m1" {
		t.Fatalf("plan after delete: %v", plan)
	}
	deps = listing()
	if len(deps) != 1 || deps[0]["litellm_params"].(map[string]any)["model"] != "m1" {
		t.Fatalf("/model/info after delete: %v", deps)
	}

	resp, body = e.request(t, "POST", "/model/delete", e.adminKey, map[string]any{"id": idOf(deps[0])})
	if resp.StatusCode != 200 {
		t.Fatalf("delete last deployment: %d %s", resp.StatusCode, body)
	}
	if deps = listing(); len(deps) != 0 {
		t.Fatalf("/model/info after deleting all: %v", deps)
	}
	_, body = e.request(t, "GET", "/admin/v1/models", e.adminKey, nil)
	if strings.Contains(string(body), "shared") {
		t.Fatalf("models left behind: %s", body)
	}
}
