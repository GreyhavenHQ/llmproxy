package server_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/monadical/llmproxy/internal/store"
)

func TestClientCapturedOnIngress(t *testing.T) {
	e := newEnv(t)
	req, err := http.NewRequest("POST", e.proxy.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.memberKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/2.0.13 (external, cli)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("chat completion failed: %d", resp.StatusCode)
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "alpha" })
	if ev.Client != "claude-cli/2.0.13 (external, cli)" {
		t.Fatalf("client = %q, want the User-Agent", ev.Client)
	}
}

func TestClientCapturedOnTransparentRelay(t *testing.T) {
	e := newEnv(t)
	req, err := http.NewRequest("POST", e.proxy.URL+"/transparent/anthropic/"+e.relayToken+"/v1/messages",
		strings.NewReader(`{"model":"claude-fake-1","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", anthropicKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/2.1.0 (external, cli)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("relay request failed: %d", resp.StatusCode)
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.ProviderID == "transparent:anthropic" })
	if ev.Client != "claude-cli/2.1.0 (external, cli)" {
		t.Fatalf("client = %q, want the User-Agent", ev.Client)
	}
}

func TestClientTruncatedAndUTF8Safe(t *testing.T) {
	e := newEnv(t)
	longUA := strings.Repeat("x", 300) + "é"
	req, err := http.NewRequest("POST", e.proxy.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.memberKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", longUA)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "alpha" })
	if len(ev.Client) > 256 {
		t.Fatalf("client not truncated: %d bytes", len(ev.Client))
	}
	if ev.Client != strings.Repeat("x", 256) {
		t.Fatalf("client = %q, want 256 x's", ev.Client)
	}
}

// TestStatsOpenToMembers: the shared stats endpoints admit any authenticated
// user and show the whole proxy's usage, not just the caller's.
func TestStatsOpenToMembers(t *testing.T) {
	e := newEnv(t)
	// Admin traffic, then a member reads the global stats.
	resp, _ := e.request(t, "POST", "/v1/chat/completions", e.adminKey,
		map[string]any{"model": "alpha", "messages": []map[string]any{{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 200 {
		t.Fatalf("chat completion failed: %d", resp.StatusCode)
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "alpha" })

	resp, data := e.request(t, "GET", "/stats/summary", e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("member /stats/summary = %d: %s", resp.StatusCode, data)
	}
	body := decode(t, data)
	rows, _ := body["usage"].([]any)
	if len(rows) == 0 {
		t.Fatal("member sees no usage rows in the global summary")
	}
	row := rows[0].(map[string]any)
	if row["principal"] != "local-admin" {
		t.Fatalf("expected the admin's traffic in the member's view, got %v", row["principal"])
	}
	for _, field := range []string{"provider", "model", "endpoint", "client"} {
		if _, ok := row[field]; !ok {
			t.Fatalf("summary row missing %q: %v", field, row)
		}
	}

	resp, _ = e.request(t, "GET", "/stats/series?bucket=day", e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("member /stats/series = %d", resp.StatusCode)
	}
	resp, data = e.request(t, "GET", "/stats/requests", e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("member /stats/requests = %d", resp.StatusCode)
	}
	reqBody := decode(t, data)
	reqRows, _ := reqBody["requests"].([]any)
	if len(reqRows) == 0 {
		t.Fatal("member sees no rows in the request log")
	}
	if _, ok := reqRows[0].(map[string]any)["client"]; !ok {
		t.Fatal("request log row missing the client field")
	}

	// No auth at all stays rejected.
	resp, _ = e.request(t, "GET", "/stats/summary", "", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated /stats/summary = %d, want 401", resp.StatusCode)
	}
}

func TestStatsFilters(t *testing.T) {
	e := newEnv(t)
	seedTwoRequests(t, e)

	rowsFor := func(query string) []any {
		resp, data := e.request(t, "GET", "/stats/summary"+query, e.memberKey, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("/stats/summary%s = %d: %s", query, resp.StatusCode, data)
		}
		rows, _ := decode(t, data)["usage"].([]any)
		return rows
	}

	if rows := rowsFor("?provider=transparent:anthropic"); len(rows) != 1 {
		t.Fatalf("provider filter: %d rows, want 1", len(rows))
	}
	if rows := rowsFor("?provider=fake"); len(rows) != 1 {
		t.Fatalf("provider name filter: %d rows, want 1", len(rows))
	} else if rows[0].(map[string]any)["model"] != "alpha" {
		t.Fatalf("provider name filter returned %v", rows[0])
	}
	// Client filter is a prefix match: the bare product token matches any version.
	if rows := rowsFor("?client=claude-cli"); len(rows) != 1 {
		t.Fatalf("client prefix filter: %d rows, want 1", len(rows))
	}
	if rows := rowsFor("?client=claude-cli&provider=fake"); len(rows) != 0 {
		t.Fatalf("combined filters: %d rows, want 0", len(rows))
	}
	if rows := rowsFor("?model=alpha"); len(rows) != 1 {
		t.Fatalf("model filter: %d rows, want 1", len(rows))
	}
	if rows := rowsFor("?model=claude-fake-1&provider=fake"); len(rows) != 0 {
		t.Fatalf("model+provider filters: %d rows, want 0", len(rows))
	}
	// principal filter still resolves names and 404s on unknowns; the relay
	// event belongs to bob (his relay token), the /v1 one to the admin.
	if rows := rowsFor("?principal=local-admin"); len(rows) != 1 {
		t.Fatalf("principal filter: %d rows, want 1", len(rows))
	}
	if rows := rowsFor("?principal=bob"); len(rows) != 1 {
		t.Fatalf("principal filter (bob): %d rows, want 1", len(rows))
	}
	// The key filter lives in the shared WHERE clause, so every aggregate
	// honours it. Only the /v1 event was minted under an API key.
	keys, err := e.st.ListAPIKeys(context.Background(), "", "local-admin", 10, 0)
	if err != nil || len(keys) != 1 {
		t.Fatalf("ListAPIKeys = %v, %v", keys, err)
	}
	if rows := rowsFor("?key=" + keys[0].ID); len(rows) != 1 {
		t.Fatalf("key filter: %d rows, want 1", len(rows))
	}
	if rows := rowsFor("?key=" + keys[0].ID + "&provider=transparent:anthropic"); len(rows) != 0 {
		t.Fatalf("key+provider filters: %d rows, want 0", len(rows))
	}
	resp, data := e.request(t, "GET", "/stats/summary?principal=nobody", e.memberKey, nil)
	if resp.StatusCode != 404 || errorCode(t, data) != "principal_not_found" {
		t.Fatalf("unknown principal = %d %s", resp.StatusCode, data)
	}
}

// TestStatsSummaryExcludesFailures: a failed request's model and provider are
// whatever the caller asked for, so failures stay out of the breakdown; the
// series still counts them in its failed split.
func TestStatsSummaryExcludesFailures(t *testing.T) {
	e := newEnv(t)
	// "broken" routes to an upstream that answers 429: outcome upstream_error.
	resp, _ := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "broken", "messages": []map[string]any{{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 429 {
		t.Fatalf("broken model = %d, want passthrough 429", resp.StatusCode)
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "broken" })

	resp, data := e.request(t, "GET", "/stats/summary", e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/stats/summary = %d", resp.StatusCode)
	}
	if rows, _ := decode(t, data)["usage"].([]any); len(rows) != 0 {
		t.Fatalf("summary includes a failed request: %v", rows)
	}

	resp, data = e.request(t, "GET", "/stats/series?bucket=day", e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/stats/series = %d", resp.StatusCode)
	}
	failed := 0.0
	for _, b := range decode(t, data)["series"].([]any) {
		failed += b.(map[string]any)["failed"].(float64)
	}
	if failed != 1 {
		t.Fatalf("series failed = %v, want 1", failed)
	}
}

// TestAnthropicCacheFoldedIntoInput: Anthropic reports cache reads and
// writes outside input_tokens; the read-side aggregates fold them in so
// totals are comparable with OpenAI-style reporting (where cached tokens are
// already part of the prompt count). The fake upstream reports input 25,
// cache_read 100, cache_creation 50.
func TestAnthropicCacheFoldedIntoInput(t *testing.T) {
	e := newEnv(t)
	req, err := http.NewRequest("POST", e.proxy.URL+"/transparent/anthropic/"+e.relayToken+"/v1/messages",
		strings.NewReader(`{"model":"claude-fake-1","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", anthropicKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("relay request failed: %d", res.StatusCode)
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool {
		return ev.ProviderID == "transparent:anthropic" && ev.Outcome == "ok"
	})
	// The stored per-event quantities stay raw as reported.
	raw := e.quantities(t, ev.ID)
	if raw["input_tokens"] != 25 || raw["cached_input_tokens"] != 100 || raw["cache_creation_tokens"] != 50 {
		t.Fatalf("raw quantities changed: %v", raw)
	}

	check := func(path string, units map[string]any) {
		if got := units["input_tokens"].(float64); got != 175 {
			t.Fatalf("%s input_tokens = %v, want 175 (25 + 100 cache read + 50 cache write)", path, got)
		}
		if got := units["cached_input_tokens"].(float64); got != 100 {
			t.Fatalf("%s cached_input_tokens = %v, want 100", path, got)
		}
	}
	resp, data := e.request(t, "GET", "/stats/summary", e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/stats/summary = %d", resp.StatusCode)
	}
	rows, _ := decode(t, data)["usage"].([]any)
	if len(rows) != 1 {
		t.Fatalf("summary rows = %d, want 1", len(rows))
	}
	check("/stats/summary", rows[0].(map[string]any)["units"].(map[string]any))

	resp, data = e.request(t, "GET", "/stats/series?bucket=day", e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/stats/series = %d", resp.StatusCode)
	}
	buckets, _ := decode(t, data)["series"].([]any)
	var withUsage map[string]any
	for _, b := range buckets {
		bucket := b.(map[string]any)
		if bucket["requests"].(float64) > 0 {
			withUsage = bucket["units"].(map[string]any)
		}
	}
	if withUsage == nil {
		t.Fatal("no series bucket with usage")
	}
	check("/stats/series", withUsage)
}

// TestStatsSummaryExcludesModellessEvents: the relay's count_tokens calls
// succeed but report no model and no usage, so they stay out of the by-model
// breakdown while still counting in the series.
func TestStatsSummaryExcludesModellessEvents(t *testing.T) {
	e := newEnv(t)
	req, err := http.NewRequest("POST",
		e.proxy.URL+"/transparent/anthropic/"+e.relayToken+"/v1/messages/count_tokens",
		strings.NewReader(`{"model":"claude-fake-1","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", anthropicKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("count_tokens = %d", resp.StatusCode)
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Endpoint == "v1/messages/count_tokens" })
	if ev.Alias != "" || ev.Outcome != "ok" {
		t.Fatalf("unexpected count_tokens event: alias=%q outcome=%q", ev.Alias, ev.Outcome)
	}

	res, data := e.request(t, "GET", "/stats/summary", e.memberKey, nil)
	if res.StatusCode != 200 {
		t.Fatalf("/stats/summary = %d", res.StatusCode)
	}
	if rows, _ := decode(t, data)["usage"].([]any); len(rows) != 0 {
		t.Fatalf("summary includes a model-less event: %v", rows)
	}

	res, data = e.request(t, "GET", "/stats/series?bucket=day", e.memberKey, nil)
	if res.StatusCode != 200 {
		t.Fatalf("/stats/series = %d", res.StatusCode)
	}
	ok := 0.0
	for _, b := range decode(t, data)["series"].([]any) {
		ok += b.(map[string]any)["ok"].(float64)
	}
	if ok != 1 {
		t.Fatalf("series ok = %v, want 1 (count_tokens still counts as a request)", ok)
	}
}

// seedTwoRequests records one /v1 event (admin key, provider "fake") and one
// relay event (bob's relay token, sentinel provider), with distinct clients.
func seedTwoRequests(t *testing.T, e *env) {
	t.Helper()
	req, _ := http.NewRequest("POST", e.proxy.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+e.adminKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "openai-python/1.51.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("chat completion failed: %v", err)
	}
	resp.Body.Close()
	req, _ = http.NewRequest("POST", e.proxy.URL+"/transparent/anthropic/"+e.relayToken+"/v1/messages",
		strings.NewReader(`{"model":"claude-fake-1","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", anthropicKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/2.0.13 (external, cli)")
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("relay request failed: %v", err)
	}
	resp.Body.Close()
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "alpha" })
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.ProviderID == "transparent:anthropic" })
}

// TestRequestExplorerFilters covers the request explorer surface: the facets
// endpoint that feeds its dropdowns, the API key filter, the time window and
// offset paging with a stable total.
func TestRequestExplorerFilters(t *testing.T) {
	e := newEnv(t)
	seedTwoRequests(t, e)

	get := func(path string) map[string]any {
		t.Helper()
		resp, data := e.request(t, "GET", path, e.memberKey, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s = %d: %s", path, resp.StatusCode, data)
		}
		return decode(t, data)
	}
	rowsFor := func(query string) ([]any, float64) {
		t.Helper()
		body := get("/stats/requests" + query)
		rows, _ := body["requests"].([]any)
		total, _ := body["total"].(float64)
		return rows, total
	}

	// Facets list the distinct values in the window, including the keys every
	// authenticated user may filter by.
	facets := get("/stats/requests/facets")
	var adminKeyID string
	keys, _ := facets["keys"].([]any)
	for _, k := range keys {
		key := k.(map[string]any)
		if key["label"] == "test-admin" {
			adminKeyID, _ = key["id"].(string)
		}
	}
	if adminKeyID == "" {
		t.Fatalf("facets keys missing test-admin: %v", keys)
	}
	if principals, _ := facets["principals"].([]any); len(principals) != 2 {
		t.Fatalf("facets principals = %v, want local-admin and bob", principals)
	}
	if clients, _ := facets["clients"].([]any); len(clients) != 2 {
		t.Fatalf("facets clients = %v, want 2", clients)
	}

	// Unfiltered: both events, total matches.
	rows, total := rowsFor("")
	if len(rows) != 2 || total != 2 {
		t.Fatalf("unfiltered = %d rows, total %v, want 2/2", len(rows), total)
	}

	// Key filter narrows to the one event minted under that key, and the row
	// carries the key's label back for display.
	rows, total = rowsFor("?key=" + adminKeyID)
	if len(rows) != 1 || total != 1 {
		t.Fatalf("key filter = %d rows, total %v, want 1/1", len(rows), total)
	}
	row := rows[0].(map[string]any)
	if row["model"] != "alpha" || row["key_label"] != "test-admin" {
		t.Fatalf("key filter row = %v", row)
	}
	if row["provider"] != "fake" {
		t.Fatalf("row provider = %v, want fake", row["provider"])
	}
	if _, total := rowsFor("?key=no-such-key"); total != 0 {
		t.Fatalf("unknown key total = %v, want 0", total)
	}

	// The relay event stores a relay token id, which is not an API key: no
	// label, no suffix, and it never matches a key filter.
	rows, _ = rowsFor("?provider=transparent:anthropic")
	if len(rows) != 1 {
		t.Fatalf("relay filter = %d rows, want 1", len(rows))
	}
	if relay := rows[0].(map[string]any); relay["key_label"] != "" || relay["key_suffix"] != "" {
		t.Fatalf("relay row carries a key: %v", relay)
	}

	// since/until bound the window on the stored UTC timestamp.
	if _, total := rowsFor("?since=2999-01-01T00:00:00Z"); total != 0 {
		t.Fatalf("future since total = %v, want 0", total)
	}
	if _, total := rowsFor("?until=2000-01-01T00:00:00Z"); total != 0 {
		t.Fatalf("past until total = %v, want 0", total)
	}
	if _, total := rowsFor("?since=2000-01-01T00:00:00Z&until=2999-01-01T00:00:00Z"); total != 2 {
		t.Fatalf("wide window total = %v, want 2", total)
	}

	// Offset walks pages; the total stays the size of the whole filtered set.
	first, total := rowsFor("?limit=1&offset=0")
	if len(first) != 1 || total != 2 {
		t.Fatalf("page 1 = %d rows, total %v, want 1/2", len(first), total)
	}
	second, total := rowsFor("?limit=1&offset=1")
	if len(second) != 1 || total != 2 {
		t.Fatalf("page 2 = %d rows, total %v, want 1/2", len(second), total)
	}
	if first[0].(map[string]any)["id"] == second[0].(map[string]any)["id"] {
		t.Fatalf("page 2 repeats page 1: %v", first[0])
	}
	if rows, _ := rowsFor("?limit=1&offset=5"); len(rows) != 0 {
		t.Fatalf("offset past the end = %d rows, want 0", len(rows))
	}

	// Quantities follow the paged rows, not a re-derived newest-N set.
	if units, _ := first[0].(map[string]any)["units"].(map[string]any); len(units) == 0 {
		t.Fatalf("paged row lost its quantities: %v", first[0])
	}
}

// TestInitIdempotent: a second Init on an already-migrated database is a
// no-op; the client column upgrade must tolerate both fresh and old schemas.
func TestInitIdempotent(t *testing.T) {
	e := newEnv(t)
	if err := e.st.Init(context.Background()); err != nil {
		t.Fatalf("second Init failed: %v", err)
	}
}
