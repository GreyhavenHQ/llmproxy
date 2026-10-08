package server_test

import (
	"context"
	"testing"
)

func TestAliasWithSeveralTargetsThroughAdminAPI(t *testing.T) {
	e := newEnv(t)
	a := e.addTarget(t, "pa", e.upstream.srv.URL, "")
	b := e.addTarget(t, "pb", e.upstream.srv.URL, "")

	resp, body := e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "ha", "strategy": "failover", "targets": []any{a.Alias},
	})
	if resp.StatusCode != 400 || errorCode(t, body) != "invalid_strategy" {
		t.Fatalf("strategy with one target: %d %s", resp.StatusCode, body)
	}
	resp, body = e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "ha", "strategy": "fastest", "targets": []any{a.Alias, b.Alias},
	})
	if resp.StatusCode != 400 || errorCode(t, body) != "invalid_strategy" {
		t.Fatalf("unknown strategy: %d %s", resp.StatusCode, body)
	}
	resp, body = e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "ha", "targets": []any{a.Alias, a.Alias},
	})
	if resp.StatusCode != 400 || errorCode(t, body) != "invalid_target" {
		t.Fatalf("duplicate target: %d %s", resp.StatusCode, body)
	}
	resp, body = e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "ha", "target": a.Alias, "targets": []any{b.Alias},
	})
	if resp.StatusCode != 400 || errorCode(t, body) != "invalid_target" {
		t.Fatalf("target and targets: %d %s", resp.StatusCode, body)
	}

	resp, body = e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "ha", "targets": []any{b.Alias, map[string]any{"alias": a.Alias, "weight": 3}},
	})
	view := decode(t, body)
	if resp.StatusCode != 201 || view["strategy"] != "failover" || view["target"] != nil {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	targets := view["targets"].([]any)
	if len(targets) != 2 || targets[0].(map[string]any)["alias"] != b.Alias ||
		targets[1].(map[string]any)["weight"] != 3.0 ||
		targets[0].(map[string]any)["provider"] != "pb" || targets[1].(map[string]any)["provider"] != "pa" {
		t.Fatalf("targets = %v", targets)
	}
	if caps := view["capabilities"].([]any); len(caps) != 2 {
		t.Fatalf("capabilities = %v", caps)
	}

	resp, body = e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "two-hop", "targets": []any{a.Alias, "ha"},
	})
	if resp.StatusCode != 400 || errorCode(t, body) != "invalid_target" {
		t.Fatalf("alias as a target: %d %s", resp.StatusCode, body)
	}

	resp, body = e.request(t, "PATCH", "/admin/v1/models/ha", e.adminKey, map[string]any{
		"strategy": "weighted", "targets": []any{map[string]any{"alias": a.Alias, "weight": 3}, b.Alias},
	})
	view = decode(t, body)
	if resp.StatusCode != 200 || view["strategy"] != "weighted" ||
		view["targets"].([]any)[0].(map[string]any)["alias"] != a.Alias {
		t.Fatalf("reorder: %d %s", resp.StatusCode, body)
	}
	resp, body = e.request(t, "PATCH", "/admin/v1/models/ha", e.adminKey, map[string]any{"strategy": "round_robin"})
	if resp.StatusCode != 200 || decode(t, body)["strategy"] != "round_robin" {
		t.Fatalf("strategy only: %d %s", resp.StatusCode, body)
	}
	resp, body = e.request(t, "PATCH", "/admin/v1/models/ha", e.adminKey, map[string]any{"upstream_name": "x"})
	if resp.StatusCode != 400 || errorCode(t, body) != "invalid_target" {
		t.Fatalf("route edit on an alias: %d %s", resp.StatusCode, body)
	}

	resp, body = e.request(t, "GET", "/admin/v1/resolve?model=ha", e.adminKey, nil)
	view = decode(t, body)
	if resp.StatusCode != 200 || view["strategy"] != "round_robin" {
		t.Fatalf("resolve: %d %s", resp.StatusCode, body)
	}
	cands := view["targets"].([]any)
	if len(cands) != 2 || cands[0].(map[string]any)["provider"] != "pa" ||
		cands[0].(map[string]any)["cooling_down"] != false {
		t.Fatalf("resolve targets = %v", cands)
	}

	resp, body = e.request(t, "PATCH", "/admin/v1/models/ha", e.adminKey, map[string]any{"targets": []any{b.Alias}})
	view = decode(t, body)
	if resp.StatusCode != 200 || view["strategy"] != nil || view["target"] != b.Alias || view["provider"] != "pb" {
		t.Fatalf("back to one target: %d %s", resp.StatusCode, body)
	}
	resp, body = e.request(t, "PATCH", "/admin/v1/models/ha", e.adminKey, map[string]any{"strategy": "failover"})
	if resp.StatusCode != 400 || errorCode(t, body) != "invalid_strategy" {
		t.Fatalf("strategy on one target: %d %s", resp.StatusCode, body)
	}

	resp, body = e.request(t, "PATCH", "/admin/v1/models/ha", e.adminKey, map[string]any{"target": a.Alias})
	if resp.StatusCode != 200 || decode(t, body)["target"] != a.Alias {
		t.Fatalf("legacy target: %d %s", resp.StatusCode, body)
	}

	events, err := e.st.ListAdminEvents(context.Background(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, ev := range events {
		if ev.Action == "model.update" && ev.TargetRef == "ha" {
			updates++
		}
	}
	if updates != 4 {
		t.Fatalf("model.update events for ha = %d, want 4", updates)
	}
}
