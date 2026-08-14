package server_test

import (
	"testing"

	"github.com/monadical/llmproxy/internal/store"
)

// The upstream's error type lands in error_kind as a token; the message stays
// on the response path only.
func TestErrorKindCapturedOnUpstreamError(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "broken", "messages": []map[string]any{{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 429 {
		t.Fatalf("broken model = %d, want passthrough 429", resp.StatusCode)
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "broken" })
	if ev.Outcome != "upstream_error" || ev.ErrorKind != "rate_limit_error" {
		t.Fatalf("outcome=%q error_kind=%q, want upstream_error/rate_limit_error", ev.Outcome, ev.ErrorKind)
	}
}

// A provider that cannot be reached records the transport class the proxy
// already names in its client-facing error.
func TestErrorKindCapturedOnUnreachable(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.request(t, "POST", "/admin/v1/providers", e.adminKey, map[string]any{
		"name": "ghost", "base_url": "http://127.0.0.1:9/v1", "timeout_connect": 0.5,
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create ghost provider: %d", resp.StatusCode)
	}
	resp, _ = e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "ghost-model", "provider": "ghost", "upstream_name": "x",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create ghost model: %d", resp.StatusCode)
	}
	resp, _ = e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "ghost-model", "messages": []any{}})
	if resp.StatusCode != 502 {
		t.Fatalf("ghost model = %d, want 502", resp.StatusCode)
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "ghost-model" })
	if ev.Outcome != "unreachable" || ev.ErrorKind == "" {
		t.Fatalf("outcome=%q error_kind=%q, want unreachable with a transport class", ev.Outcome, ev.ErrorKind)
	}
}

// /stats/errors returns the outcome series and the error breakdown in one
// response; ok rows stay in the breakdown so rates have their denominator.
func TestStatsErrors(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha", "messages": []map[string]any{{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 200 {
		t.Fatalf("alpha = %d", resp.StatusCode)
	}
	resp, _ = e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "broken", "messages": []map[string]any{{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 429 {
		t.Fatalf("broken = %d", resp.StatusCode)
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "broken" })
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "alpha" })

	resp, data := e.request(t, "GET", "/stats/errors?bucket=day", e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/stats/errors = %d %s", resp.StatusCode, data)
	}
	doc := decode(t, data)

	okTotal, failedTotal := 0.0, 0.0
	for _, b := range doc["series"].([]any) {
		bucket := b.(map[string]any)
		okTotal += bucket["ok"].(float64)
		failedTotal += bucket["upstream_error"].(float64)
	}
	if okTotal != 1 || failedTotal != 1 {
		t.Fatalf("series ok=%v upstream_error=%v, want 1 and 1", okTotal, failedTotal)
	}

	cells := doc["breakdown"].([]any)
	var okCell, errCell map[string]any
	for _, c := range cells {
		cell := c.(map[string]any)
		switch cell["outcome"] {
		case "ok":
			okCell = cell
		case "upstream_error":
			errCell = cell
		}
	}
	if okCell == nil || errCell == nil {
		t.Fatalf("breakdown missing ok or error cell: %v", cells)
	}
	if errCell["error_kind"] != "rate_limit_error" || errCell["status_code"].(float64) != 429 {
		t.Fatalf("error cell = %v", errCell)
	}
	bands := errCell["bands"].([]any)
	total := 0.0
	for _, b := range bands {
		total += b.(float64)
	}
	if len(bands) != 7 || total != 1 {
		t.Fatalf("bands = %v, want 7 bands summing to 1", bands)
	}
}

// The outcome filter narrows the request log; an unknown value is a 400, not
// an empty result.
func TestRequestsOutcomeFilter(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha", "messages": []map[string]any{{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 200 {
		t.Fatalf("alpha = %d", resp.StatusCode)
	}
	resp, _ = e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "broken", "messages": []map[string]any{{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 429 {
		t.Fatalf("broken = %d", resp.StatusCode)
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "broken" })
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "alpha" })

	rows := func(query string) []any {
		resp, data := e.request(t, "GET", "/stats/requests"+query, e.memberKey, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("/stats/requests%s = %d %s", query, resp.StatusCode, data)
		}
		return decode(t, data)["requests"].([]any)
	}
	if got := rows("?outcome=failed"); len(got) != 1 {
		t.Fatalf("outcome=failed: %d rows, want 1", len(got))
	} else if row := got[0].(map[string]any); row["error_kind"] != "rate_limit_error" {
		t.Fatalf("failed row error_kind = %v, want rate_limit_error", row["error_kind"])
	}
	if got := rows("?outcome=ok"); len(got) != 1 {
		t.Fatalf("outcome=ok: %d rows, want 1", len(got))
	}
	resp, data := e.request(t, "GET", "/stats/requests?outcome=nope", e.memberKey, nil)
	if resp.StatusCode != 400 || errorCode(t, data) != "invalid_outcome" {
		t.Fatalf("invalid outcome = %d %s", resp.StatusCode, data)
	}
}
