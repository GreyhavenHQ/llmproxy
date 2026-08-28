package server_test

import (
	"testing"
)

// hide flips the hidden flag on a model through the admin API.
func hide(t *testing.T, e *env, alias string, hidden bool) {
	t.Helper()
	resp, body := e.request(t, "PATCH", "/admin/v1/models/"+alias, e.adminKey,
		map[string]any{"hidden": hidden})
	if resp.StatusCode != 200 {
		t.Fatalf("patch hidden on %s: %d %s", alias, resp.StatusCode, body)
	}
	if decode(t, body)["hidden"] != hidden {
		t.Fatalf("patch response did not echo hidden=%v: %s", hidden, body)
	}
}

// A hidden model drops out of the caller-facing list and comes back when
// unhidden. Hiding is decluttering, not access control.
func TestHiddenModelLeavesPublicList(t *testing.T) {
	e := newEnv(t)

	if ids := modelIDs(t, mustList(t, e, "/v1/models", "")); !ids["alpha"] {
		t.Fatalf("alpha missing before hiding: %v", ids)
	}

	hide(t, e, "alpha", true)
	ids := modelIDs(t, mustList(t, e, "/v1/models", ""))
	if ids["alpha"] {
		t.Fatalf("hidden model still listed: %v", ids)
	}
	if !ids["slow"] {
		t.Fatalf("hiding one model removed others: %v", ids)
	}

	hide(t, e, "alpha", false)
	if ids := modelIDs(t, mustList(t, e, "/v1/models", "")); !ids["alpha"] {
		t.Fatalf("unhidden model did not come back: %v", ids)
	}
}

// include_hidden is for the usage catalog: any authenticated caller can see
// hidden models there, flagged, because they can still carry cost.
func TestIncludeHiddenNeedsAuth(t *testing.T) {
	e := newEnv(t)
	hide(t, e, "alpha", true)

	resp, body := e.request(t, "GET", "/v1/models?include_hidden=1", "", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("include_hidden must require auth: %d %s", resp.StatusCode, body)
	}

	body = mustList(t, e, "/v1/models?include_hidden=1", e.memberKey)
	entries := modelEntries(t, body)
	alpha := entries["alpha"]
	if alpha == nil {
		t.Fatalf("include_hidden did not return the hidden model: %s", body)
	}
	if alpha["hidden"] != true {
		t.Fatalf("hidden model not flagged: %v", alpha)
	}
	if entries["slow"]["hidden"] != false {
		t.Fatalf("visible model flagged hidden: %v", entries["slow"])
	}
}

// Hidden is a listing flag only: the model keeps serving requests by name.
func TestHiddenModelStaysCallable(t *testing.T) {
	e := newEnv(t)
	hide(t, e, "alpha", true)

	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 200 {
		t.Fatalf("hidden model must stay callable: %d %s", resp.StatusCode, body)
	}
}

// The flag lives on the row, so hiding a target says nothing about the
// aliases pointing at it, and an alias can be hidden on its own.
func TestHiddenIsPerRow(t *testing.T) {
	e := newEnv(t)
	resp, body := e.request(t, "POST", "/admin/v1/models", e.adminKey,
		map[string]any{"alias": "quick", "target": "alpha"})
	if resp.StatusCode != 201 {
		t.Fatalf("create alias: %d %s", resp.StatusCode, body)
	}

	hide(t, e, "alpha", true)
	ids := modelIDs(t, mustList(t, e, "/v1/models", ""))
	if ids["alpha"] || !ids["quick"] {
		t.Fatalf("hiding a target must not hide its aliases: %v", ids)
	}

	hide(t, e, "quick", true)
	if ids := modelIDs(t, mustList(t, e, "/v1/models", "")); ids["quick"] {
		t.Fatalf("hidden alias still listed: %v", ids)
	}
}

// The admin catalog is the full picture: hidden rows are always there, with
// the flag on every row.
func TestAdminModelListAlwaysShowsHidden(t *testing.T) {
	e := newEnv(t)
	hide(t, e, "alpha", true)

	resp, body := e.request(t, "GET", "/admin/v1/models", e.adminKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("admin list: %d %s", resp.StatusCode, body)
	}
	seen := make(map[string]any)
	for _, m := range decode(t, body)["models"].([]any) {
		entry := m.(map[string]any)
		seen[entry["alias"].(string)] = entry["hidden"]
	}
	if seen["alpha"] != true {
		t.Fatalf("admin list must show the hidden model, flagged: %s", body)
	}
	if seen["slow"] != false {
		t.Fatalf("visible model flagged hidden in the admin list: %s", body)
	}
}

// mustList performs a model-list request and fails on anything but 200.
func mustList(t *testing.T, e *env, path, key string) []byte {
	t.Helper()
	resp, body := e.request(t, "GET", path, key, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
	}
	return body
}

func modelEntries(t *testing.T, body []byte) map[string]map[string]any {
	t.Helper()
	out := make(map[string]map[string]any)
	for _, m := range decode(t, body)["data"].([]any) {
		entry := m.(map[string]any)
		out[entry["id"].(string)] = entry
	}
	return out
}
