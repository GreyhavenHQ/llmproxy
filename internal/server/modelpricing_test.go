package server_test

import (
	"testing"
)

// include_pricing puts the prices the proxy bills at on the caller-facing
// list, so a client can show what a request cost without an admin key.
func TestModelListIncludesPricing(t *testing.T) {
	e := newEnv(t)

	resp, body := e.request(t, "PATCH", "/admin/v1/models/alpha", e.adminKey, map[string]any{
		"pricing": map[string]any{"input_tokens": 0.4, "output_tokens": 1.2},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("price the model: %d %s", resp.StatusCode, body)
	}

	// Off by default: the list keeps its OpenAI shape unless asked.
	if _, ok := modelEntries(t, mustList(t, e, "/v1/models", ""))["alpha"]["pricing"]; ok {
		t.Fatalf("pricing must not appear without include_pricing")
	}

	entries := modelEntries(t, mustList(t, e, "/v1/models?include_pricing=1", e.memberKey))
	prices, ok := entries["alpha"]["pricing"].(map[string]any)
	if !ok {
		t.Fatalf("no pricing on the entry: %v", entries["alpha"])
	}
	if prices["input_tokens"] != 0.4 || prices["output_tokens"] != 1.2 {
		t.Fatalf("wrong prices: %v", prices)
	}
	if entries["alpha"]["pricing_inherited"] != false {
		t.Fatalf("prices set on the alias are not inherited: %v", entries["alpha"])
	}

	// An unpriced model carries an empty set, never a zero price.
	unpriced, ok := entries["slow"]["pricing"].(map[string]any)
	if !ok || len(unpriced) != 0 {
		t.Fatalf("unpriced model must list no prices: %v", entries["slow"])
	}
}

// Prices are operator data, so the parameter costs an identity, like
// include_hidden.
func TestIncludePricingNeedsAuth(t *testing.T) {
	e := newEnv(t)

	resp, body := e.request(t, "GET", "/v1/models?include_pricing=1", "", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("include_pricing must require auth: %d %s", resp.StatusCode, body)
	}
}

// A name for another model lists the prices in force for it, flagged as
// inherited, the same resolution the data plane bills with.
func TestModelListPricingFollowsInheritance(t *testing.T) {
	e := newEnv(t)

	resp, body := e.request(t, "PATCH", "/admin/v1/models/alpha", e.adminKey, map[string]any{
		"pricing": map[string]any{"input_tokens": 0.4},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("price the target: %d %s", resp.StatusCode, body)
	}
	resp, body = e.request(t, "POST", "/admin/v1/models", e.adminKey,
		map[string]any{"alias": "quick", "target": "alpha"})
	if resp.StatusCode != 201 {
		t.Fatalf("create alias: %d %s", resp.StatusCode, body)
	}

	entry := modelEntries(t, mustList(t, e, "/v1/models?include_pricing=1", e.memberKey))["quick"]
	if entry["pricing"].(map[string]any)["input_tokens"] != 0.4 ||
		entry["pricing_inherited"] != true {
		t.Fatalf("alias did not inherit the target's price: %v", entry)
	}
}
