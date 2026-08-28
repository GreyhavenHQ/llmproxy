package server_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/store"
)

func TestRegisterDiscoverBindInferUnregister(t *testing.T) {
	e := newEnv(t)

	// Register a provider carrying its upstream API key.
	resp, body := e.request(t, "POST", "/admin/v1/providers", e.adminKey, map[string]any{
		"name": "fake2", "base_url": e.upstream.srv.URL + "/v1", "api_key": upstreamKey,
	})
	if resp.StatusCode != 201 || decode(t, body)["has_credential"] != true {
		t.Fatalf("register: %d %s", resp.StatusCode, body)
	}

	// Duplicate registration is rejected.
	resp, _ = e.request(t, "POST", "/admin/v1/providers", e.adminKey, map[string]any{
		"name": "fake2", "base_url": "http://other.test/v1",
	})
	if resp.StatusCode != 409 {
		t.Fatalf("duplicate provider must 409, got %d", resp.StatusCode)
	}

	// Discover upstream models (read-only).
	resp, body = e.request(t, "GET", "/admin/v1/providers/fake2/discover", e.adminKey, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "m-alpha") {
		t.Fatalf("discover: %d %s", resp.StatusCode, body)
	}

	// Bind under a curated alias; bindings serve as soon as they exist.
	resp, body = e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "alpha2", "provider": "fake2", "upstream_name": "m-alpha",
		"capabilities": []string{"chat", "chat_stream"}, "origin": "discovered",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("bind: %d %s", resp.StatusCode, body)
	}

	// Alias uniqueness is global.
	resp, _ = e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "alpha2", "provider": "fake2", "upstream_name": "m-embed",
	})
	if resp.StatusCode != 409 {
		t.Fatalf("duplicate alias must 409, got %d", resp.StatusCode)
	}

	// Dry-run resolution shows exactly what would serve the call.
	resp, body = e.request(t, "GET", "/admin/v1/resolve?model=alpha2", e.adminKey, nil)
	resolved := decode(t, body)
	if resp.StatusCode != 200 || resolved["provider"] != "fake2" || resolved["upstream_name"] != "m-alpha" {
		t.Fatalf("resolve: %d %s", resp.StatusCode, body)
	}

	// A member can now call it, and the headers say who served it.
	resp, body = e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha2", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 200 || resp.Header.Get("x-llmproxy-provider") != "fake2" {
		t.Fatalf("inference through new provider: %d %s", resp.StatusCode, body)
	}

	// Edit the binding: retarget the upstream model and capabilities.
	resp, body = e.request(t, "PATCH", "/admin/v1/models/alpha2", e.adminKey, map[string]any{
		"upstream_name": "m-embed", "capabilities": []string{"embeddings"},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("model edit failed: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha2", "messages": []any{}})
	if resp.StatusCode != 400 { // now embeddings-only
		t.Fatalf("edited capability set not enforced: %d", resp.StatusCode)
	}

	// Disabling the provider takes its models out of the list and off the wire.
	resp, _ = e.request(t, "PATCH", "/admin/v1/providers/fake2", e.adminKey, map[string]any{"enabled": false})
	if resp.StatusCode != 200 {
		t.Fatalf("disable provider: %d", resp.StatusCode)
	}
	_, body = e.request(t, "GET", "/v1/models", e.memberKey, nil)
	if modelIDs(t, body)["alpha2"] {
		t.Fatal("model on disabled provider still listed")
	}
	resp, _ = e.request(t, "POST", "/v1/embeddings", e.memberKey,
		map[string]any{"model": "alpha2", "input": "hi"})
	if resp.StatusCode != 404 {
		t.Fatalf("model on disabled provider must 404, got %d", resp.StatusCode)
	}

	// Unregister the provider entirely (bindings cascade).
	resp, _ = e.request(t, "DELETE", "/admin/v1/providers/fake2", e.adminKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("unregister failed: %d", resp.StatusCode)
	}
	_, body = e.request(t, "GET", "/admin/v1/models?provider=fake2", e.adminKey, nil)
	if models := decode(t, body)["models"].([]any); len(models) != 0 {
		t.Fatalf("bindings did not cascade: %v", models)
	}

	// The admin trail recorded metadata-only events.
	_, body = e.request(t, "GET", "/admin/v1/events", e.adminKey, nil)
	trail := string(body)
	for _, action := range []string{"provider.create", "model.create", "provider.delete"} {
		if !strings.Contains(trail, action) {
			t.Fatalf("missing admin event %q in %s", action, trail)
		}
	}
}

func TestServicePrincipalsAndAdminKeys(t *testing.T) {
	e := newEnv(t)

	resp, body := e.request(t, "POST", "/admin/v1/principals", e.adminKey,
		map[string]any{"name": "batch-service", "kind": "service"})
	if resp.StatusCode != 201 {
		t.Fatalf("create principal: %d %s", resp.StatusCode, body)
	}

	resp, body = e.request(t, "POST", "/admin/v1/keys", e.adminKey,
		map[string]any{"principal": "batch-service", "label": "prod"})
	if resp.StatusCode != 201 {
		t.Fatalf("mint service key: %d %s", resp.StatusCode, body)
	}
	minted := decode(t, body)
	serviceKey := minted["key"].(string)

	resp, _ = e.request(t, "GET", "/my/keys", serviceKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("service key rejected: %d", resp.StatusCode)
	}

	resp, _ = e.request(t, "DELETE", "/admin/v1/keys/"+minted["id"].(string), e.adminKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete service key: %d", resp.StatusCode)
	}
	resp, body = e.request(t, "GET", "/my/keys", serviceKey, nil)
	if resp.StatusCode != 401 || errorCode(t, body) != "invalid_api_key" {
		t.Fatalf("want 401 invalid_api_key, got %d %s", resp.StatusCode, body)
	}
}

func TestPricingFeedAndCosting(t *testing.T) {
	e := newEnv(t)

	resp, body := e.request(t, "POST", "/admin/v1/pricing", e.adminKey, map[string]any{
		"version": "test-1",
		"entries": []map[string]any{
			{"model": "alpha", "unit": "input_tokens", "price_per_million": 1.0},
			{"model": "alpha", "unit": "output_tokens", "price_per_million": 2.0},
			{"model": "alpha", "unit": "cached_input_tokens", "price_per_million": 0.5},
		},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("load pricing: %d %s", resp.StatusCode, body)
	}
	loaded := decode(t, body)
	if loaded["version"] != "test-1" || loaded["count"] != float64(3) {
		t.Fatalf("pricing status wrong: %s", body)
	}

	resp, _ = e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 200 {
		t.Fatal("chat failed")
	}

	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Cost.Valid })
	want := 7*1e-6 + 5*2e-6 + 2*0.5e-6
	if math.Abs(ev.Cost.Float64-want) > 1e-12 || ev.Unpriced {
		t.Fatalf("wrong cost: %v (want %v), unpriced=%v", ev.Cost.Float64, want, ev.Unpriced)
	}

	// Per-principal usage summary shows per-unit quantities.
	resp, body = e.request(t, "GET", "/my/usage", e.memberKey, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "input_tokens") {
		t.Fatalf("my usage: %d %s", resp.StatusCode, body)
	}
}

// Prices belong to the model: they are read and written through the binding,
// and editing one model leaves every other price entry alone.
func TestModelPricingThroughTheBinding(t *testing.T) {
	e := newEnv(t)

	// A feed priced on upstream names: "alpha" inherits m-alpha's prices.
	resp, body := e.request(t, "POST", "/admin/v1/pricing", e.adminKey, map[string]any{
		"version": "upstream-1",
		"entries": []map[string]any{
			{"model": "m-alpha", "unit": "input_tokens", "price_per_million": 1.0},
			{"model": "m-embed", "unit": "input_tokens", "price_per_million": 9.0},
		},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("load pricing: %d %s", resp.StatusCode, body)
	}
	if m := modelByAlias(t, e, "alpha"); m["pricing_inherited"] != true ||
		m["pricing"].(map[string]any)["input_tokens"] != 1.0 {
		t.Fatalf("inherited price not surfaced on the model: %v", m)
	}

	// Setting prices on the alias overrides the inherited ones.
	resp, body = e.request(t, "PATCH", "/admin/v1/models/alpha", e.adminKey, map[string]any{
		"pricing": map[string]any{"input_tokens": 3.0, "output_tokens": 4.0},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("price the model: %d %s", resp.StatusCode, body)
	}
	updated := decode(t, body)
	if updated["pricing_inherited"] != false ||
		updated["pricing"].(map[string]any)["output_tokens"] != 4.0 {
		t.Fatalf("model pricing not applied: %s", body)
	}

	// Another model's entries survived the edit.
	if m := modelByAlias(t, e, "embed-only"); m["pricing"].(map[string]any)["input_tokens"] != 9.0 {
		t.Fatalf("editing one model dropped another's prices: %v", m)
	}

	// The new prices are what the data plane costs with; the unpriced cached
	// unit still flags the event rather than costing zero.
	resp, _ = e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 200 {
		t.Fatal("chat failed")
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Cost.Valid })
	want := 7*3e-6 + 5*4e-6
	if math.Abs(ev.Cost.Float64-want) > 1e-12 || !ev.Unpriced {
		t.Fatalf("wrong cost: %v (want %v), unpriced=%v", ev.Cost.Float64, want, ev.Unpriced)
	}

	// An unknown unit is rejected rather than silently dropped.
	resp, _ = e.request(t, "PATCH", "/admin/v1/models/alpha", e.adminKey, map[string]any{
		"pricing": map[string]any{"gpu_seconds": 1.0},
	})
	if resp.StatusCode != 400 {
		t.Fatalf("unknown price unit must 400, got %d", resp.StatusCode)
	}
}

// A model is one row that can be renamed and repointed, not something you
// delete and rebuild: the prices follow the name.
func TestModelRenameAndAliasDefault(t *testing.T) {
	e := newEnv(t)

	// No alias given: the model serves under the upstream's own name.
	resp, body := e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"provider": "fake", "upstream_name": "m-alpha",
		"capabilities": []string{"chat", "chat_stream"},
		"pricing":      map[string]any{"input_tokens": 2.0},
	})
	if resp.StatusCode != 201 || decode(t, body)["alias"] != "m-alpha" {
		t.Fatalf("alias should default to the upstream name: %d %s", resp.StatusCode, body)
	}

	// Rename it. The prices move with it, under one new feed version.
	resp, body = e.request(t, "PATCH", "/admin/v1/models/m-alpha", e.adminKey,
		map[string]any{"alias": "acme/smart"})
	renamed := decode(t, body)
	if resp.StatusCode != 200 || renamed["alias"] != "acme/smart" ||
		renamed["pricing"].(map[string]any)["input_tokens"] != 2.0 {
		t.Fatalf("rename lost the prices: %d %s", resp.StatusCode, body)
	}
	_, body = e.request(t, "GET", "/admin/v1/pricing", e.adminKey, nil)
	if strings.Contains(string(body), `"m-alpha"`) {
		t.Fatalf("prices still keyed on the old alias: %s", body)
	}

	// The new name serves and the old one is gone.
	resp, _ = e.request(t, "POST", "/v1/chat/completions", e.memberKey, map[string]any{
		"model": "acme/smart", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("renamed alias does not serve: %d", resp.StatusCode)
	}
	resp, body = e.request(t, "POST", "/v1/chat/completions", e.memberKey, map[string]any{
		"model": "m-alpha", "messages": []any{},
	})
	if resp.StatusCode != 404 || errorCode(t, body) != "model_not_found" {
		t.Fatalf("old alias still resolves: %d %s", resp.StatusCode, body)
	}

	// Renaming onto a name in use is rejected; aliases stay unique.
	resp, _ = e.request(t, "PATCH", "/admin/v1/models/acme%2Fsmart", e.adminKey,
		map[string]any{"alias": "alpha"})
	if resp.StatusCode != 409 {
		t.Fatalf("rename onto a bound alias must 409, got %d", resp.StatusCode)
	}
}

// An alias is a name for another model: one hop, everything inherited, and it
// follows the target when the target moves.
func TestModelAliasInheritsItsTarget(t *testing.T) {
	e := newEnv(t)

	resp, body := e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "z-ai/glm-5.2", "provider": "fake", "upstream_name": "m-alpha",
		"capabilities": []string{"chat", "chat_stream", "completions"},
		"pricing":      map[string]any{"input_tokens": 1.0, "output_tokens": 3.0},
	})
	if resp.StatusCode != 201 {
		t.Fatalf("bind the real model: %d %s", resp.StatusCode, body)
	}

	// The alias carries nothing of its own but its name.
	resp, body = e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "acme/smart", "target": "z-ai/glm-5.2",
	})
	view := decode(t, body)
	if resp.StatusCode != 201 || view["target"] != "z-ai/glm-5.2" ||
		view["provider"] != "fake" || view["upstream_name"] != "m-alpha" {
		t.Fatalf("alias did not inherit its target: %d %s", resp.StatusCode, body)
	}
	if caps := view["capabilities"].([]any); len(caps) != 3 {
		t.Fatalf("capabilities not inherited: %v", caps)
	}
	if view["pricing"].(map[string]any)["output_tokens"] != 3.0 || view["pricing_inherited"] != true {
		t.Fatalf("prices not inherited: %s", body)
	}

	// It serves, and the upstream sees the target's model name.
	resp, body = e.request(t, "POST", "/v1/chat/completions", e.memberKey, map[string]any{
		"model": "acme/smart", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode != 200 || resp.Header.Get("x-llmproxy-model") != "m-alpha" {
		t.Fatalf("alias does not serve: %d %s", resp.StatusCode, body)
	}

	// Usage is recorded against the name the caller used, costed through the
	// target's prices.
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "acme/smart" })
	want := 7*1e-6 + 5*3e-6
	if math.Abs(ev.Cost.Float64-want) > 1e-12 {
		t.Fatalf("alias not costed through its target: %v (want %v)", ev.Cost.Float64, want)
	}

	// Retargeting the real model moves the alias with it.
	resp, _ = e.request(t, "PATCH", "/admin/v1/models/z-ai%2Fglm-5.2", e.adminKey,
		map[string]any{"upstream_name": "m-embed", "capabilities": []string{"embeddings"}})
	if resp.StatusCode != 200 {
		t.Fatalf("retarget the real model: %d", resp.StatusCode)
	}
	moved := modelByAlias(t, e, "acme/smart")
	if moved["upstream_name"] != "m-embed" ||
		len(moved["capabilities"].([]any)) != 1 {
		t.Fatalf("alias did not follow its target: %v", moved)
	}

	// One hop: an alias cannot point at an alias.
	resp, body = e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "acme/smarter", "target": "acme/smart",
	})
	if resp.StatusCode != 400 || errorCode(t, body) != "invalid_target" {
		t.Fatalf("want 400 invalid_target for a two-hop alias, got %d %s", resp.StatusCode, body)
	}

	// Editing what an alias inherits is refused rather than silently ignored.
	resp, body = e.request(t, "PATCH", "/admin/v1/models/acme%2Fsmart", e.adminKey,
		map[string]any{"upstream_name": "m-alpha"})
	if resp.StatusCode != 400 || errorCode(t, body) != "invalid_target" {
		t.Fatalf("want 400 invalid_target editing an alias's route, got %d %s", resp.StatusCode, body)
	}

	// The target cannot be deleted out from under the alias.
	resp, body = e.request(t, "DELETE", "/admin/v1/models/z-ai%2Fglm-5.2", e.adminKey, nil)
	if resp.StatusCode != 409 || errorCode(t, body) != "model_in_use" {
		t.Fatalf("want 409 model_in_use, got %d %s", resp.StatusCode, body)
	}

	// Both names are servable and listed.
	_, body = e.request(t, "GET", "/v1/models", e.memberKey, nil)
	ids := modelIDs(t, body)
	if !ids["acme/smart"] || !ids["z-ai/glm-5.2"] {
		t.Fatalf("alias missing from the model list: %s", body)
	}

	// Deleting the alias frees the target.
	resp, _ = e.request(t, "DELETE", "/admin/v1/models/acme%2Fsmart", e.adminKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete alias: %d", resp.StatusCode)
	}
	resp, _ = e.request(t, "DELETE", "/admin/v1/models/z-ai%2Fglm-5.2", e.adminKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete target after its alias: %d", resp.StatusCode)
	}
}

// An alias can carry its own price, which is how an internal name charges
// differently from the model it points at.
func TestModelAliasCanOverridePrice(t *testing.T) {
	e := newEnv(t)
	e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "vendor/model", "provider": "fake", "upstream_name": "m-alpha",
		"pricing": map[string]any{"input_tokens": 1.0, "output_tokens": 1.0},
	})
	e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "team/model", "target": "vendor/model",
		"pricing": map[string]any{"input_tokens": 10.0},
	})
	view := modelByAlias(t, e, "team/model")
	prices := view["pricing"].(map[string]any)
	if prices["input_tokens"] != 10.0 || prices["output_tokens"] != 1.0 {
		t.Fatalf("override should win for one unit and inherit the other: %v", prices)
	}
}

func modelByAlias(t *testing.T, e *env, alias string) map[string]any {
	t.Helper()
	_, body := e.request(t, "GET", "/admin/v1/models", e.adminKey, nil)
	for _, m := range decode(t, body)["models"].([]any) {
		view := m.(map[string]any)
		if view["alias"] == alias {
			return view
		}
	}
	t.Fatalf("no model %q in %s", alias, body)
	return nil
}

func TestUsageSeriesBuckets(t *testing.T) {
	e := newEnv(t)

	resp, _ := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 200 {
		t.Fatal("chat failed")
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "alpha" })

	// A bounded window is gap-filled: every day in it gets a bucket, whether
	// or not anything happened that day.
	since := time.Now().UTC().AddDate(0, 0, -6).Format("2006-01-02")
	resp, body := e.request(t, "GET", "/my/usage/series?bucket=day&since="+since, e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("series: %d %s", resp.StatusCode, body)
	}
	doc := decode(t, body)
	series := doc["series"].([]any)
	if doc["bucket"] != "day" || len(series) != 7 {
		t.Fatalf("want 7 day buckets, got %d: %s", len(series), body)
	}
	last := series[len(series)-1].(map[string]any)
	// input is normalised to the non-cached input: prompt 7 minus cached 2.
	if last["requests"] != 1.0 || last["units"].(map[string]any)["input_tokens"] != 5.0 {
		t.Fatalf("today's bucket missing the request: %v", last)
	}
	if last["ok"] != 1.0 || last["failed"] != 0.0 || last["cancelled"] != 0.0 {
		t.Fatalf("outcome split wrong: %v", last)
	}
	if last["cost"] != nil {
		t.Fatalf("an unpriced bucket must report null cost, not zero: %v", last)
	}

	// Coarser buckets roll the same events up into one.
	resp, body = e.request(t, "GET", "/my/usage/series?bucket=month&since="+since, e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("month series: %d %s", resp.StatusCode, body)
	}
	total := 0.0
	for _, b := range decode(t, body)["series"].([]any) {
		total += b.(map[string]any)["requests"].(float64)
	}
	if total != 1 {
		t.Fatalf("month rollup lost requests: %s", body)
	}

	// An upstream error lands in the failed segment, not in ok.
	resp, _ = e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "broken", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 429 {
		t.Fatalf("expected the fake upstream to rate-limit, got %d", resp.StatusCode)
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Outcome == "upstream_error" })
	resp, body = e.request(t, "GET", "/my/usage/series?bucket=day&since="+since, e.memberKey, nil)
	today := decode(t, body)["series"].([]any)
	last = today[len(today)-1].(map[string]any)
	if resp.StatusCode != 200 || last["failed"] != 1.0 || last["ok"] != 1.0 {
		t.Fatalf("failed request not split out: %v", last)
	}

	// Admin sees the same shape across every principal.
	resp, body = e.request(t, "GET", "/admin/v1/usage/series?bucket=day", e.adminKey, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "input_tokens") {
		t.Fatalf("admin series: %d %s", resp.StatusCode, body)
	}

	resp, body = e.request(t, "GET", "/my/usage/series?bucket=fortnight", e.memberKey, nil)
	if resp.StatusCode != 400 || errorCode(t, body) != "invalid_bucket" {
		t.Fatalf("want 400 invalid_bucket, got %d %s", resp.StatusCode, body)
	}
}

// Timeouts, the concurrency cap and TLS verification are editable in place;
// zero clears the cap back to unlimited, and a rejected patch changes nothing.
func TestProviderPatchOperationalSettings(t *testing.T) {
	e := newEnv(t)
	resp, body := e.request(t, "PATCH", "/admin/v1/providers/fake", e.adminKey, map[string]any{
		"timeout_connect": 5, "timeout_read": 120, "max_concurrency": 8, "verify_tls": false,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("patch: %d %s", resp.StatusCode, body)
	}
	_, body = e.request(t, "GET", "/admin/v1/providers/fake", e.adminKey, nil)
	view := decode(t, body)
	if view["timeout_connect"].(float64) != 5 || view["timeout_read"].(float64) != 120 ||
		view["max_concurrency"].(float64) != 8 || view["verify_tls"].(bool) != false {
		t.Fatalf("patched values did not persist: %s", body)
	}
	if _, present := view["timeout_write"]; present {
		t.Fatalf("timeout_write still in the provider view: %s", body)
	}

	// Zero clears the cap; the view reports unlimited as null.
	resp, _ = e.request(t, "PATCH", "/admin/v1/providers/fake", e.adminKey,
		map[string]any{"max_concurrency": 0})
	if resp.StatusCode != 200 {
		t.Fatalf("clear cap: %d", resp.StatusCode)
	}
	_, body = e.request(t, "GET", "/admin/v1/providers/fake", e.adminKey, nil)
	if decode(t, body)["max_concurrency"] != nil {
		t.Fatalf("cleared cap still set: %s", body)
	}

	// A non-positive timeout is rejected, and the stored value stays put.
	resp, body = e.request(t, "PATCH", "/admin/v1/providers/fake", e.adminKey,
		map[string]any{"timeout_read": 0})
	if resp.StatusCode != 400 || errorCode(t, body) != "invalid_timeout" {
		t.Fatalf("zero timeout accepted: %d %s", resp.StatusCode, body)
	}
	_, body = e.request(t, "GET", "/admin/v1/providers/fake", e.adminKey, nil)
	if decode(t, body)["timeout_read"].(float64) != 120 {
		t.Fatalf("timeout_read changed by a rejected patch: %s", body)
	}

	// The proxy still serves through the edited provider.
	resp, _ = e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 200 {
		t.Fatalf("inference after provider edit: %d", resp.StatusCode)
	}
}
