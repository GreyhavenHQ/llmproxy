package server_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/monadical/llmproxy/internal/store"
)

// relayRequest performs a call against the transparent relay with
// Anthropic-style headers (x-api-key, anthropic-version) and a cookie the
// relay must strip.
func (e *env) relayRequest(t *testing.T, method, path, apiKey string, body []byte) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, e.proxy.URL+"/transparent/anthropic/"+e.relayToken+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Cookie", "llmproxy_session=browser-state")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func TestTransparentUnaryMessages(t *testing.T) {
	e := newEnv(t)
	body := []byte(`{"model":"claude-fake-1","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	resp, data := e.relayRequest(t, "POST", "/v1/messages", anthropicKey, body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	if string(data) != anthropicUnaryBody {
		t.Fatalf("response body altered by the relay:\n%s", data)
	}
	if got := resp.Header.Get("Request-Id"); got != "req_fake_anthropic" {
		t.Fatalf("upstream response header lost, Request-Id=%q", got)
	}

	// The upstream must have seen the caller's credentials and body untouched,
	// and no proxy-local state.
	up := e.anthropic.last(t)
	if up.Header.Get("x-api-key") != anthropicKey {
		t.Fatalf("x-api-key not forwarded, got %q", up.Header.Get("x-api-key"))
	}
	if up.Header.Get("anthropic-version") != "2023-06-01" {
		t.Fatal("anthropic-version not forwarded")
	}
	if up.Header.Get("Cookie") != "" {
		t.Fatal("cookie leaked upstream")
	}
	if !bytes.Equal(up.RawBody, body) {
		t.Fatalf("request body altered by the relay:\n%s", up.RawBody)
	}

	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.ProviderID == "transparent:anthropic" })
	if ev.Alias != "claude-fake-1" || ev.UpstreamName != "claude-fake-1" {
		t.Fatalf("model attribution wrong: %q", ev.Alias)
	}
	if ev.Endpoint != "v1/messages" || ev.Streamed || ev.Outcome != "ok" {
		t.Fatalf("event wrong: %+v", ev)
	}
	if ev.APIKeyID == "" {
		t.Fatal("relay token id missing from event")
	}
	got := e.quantities(t, ev.ID)
	want := map[string]float64{
		"input_tokens": 25, "output_tokens": 10,
		"cached_input_tokens": 100, "cache_creation_tokens": 50,
	}
	for unit, quantity := range want {
		if got[unit] != quantity {
			t.Fatalf("unit %s = %v, want %v (all: %v)", unit, got[unit], quantity, got)
		}
	}
}

func TestTransparentStreamingMessages(t *testing.T) {
	e := newEnv(t)
	body := []byte(`{"model":"claude-fake-1","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	resp, data := e.relayRequest(t, "POST", "/v1/messages", anthropicKey, body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	if string(data) != anthropicStreamBody {
		t.Fatalf("SSE stream altered by the relay:\n%s", data)
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool {
		return ev.ProviderID == "transparent:anthropic" && ev.Streamed
	})
	if ev.Alias != "claude-fake-1" || ev.Outcome != "ok" {
		t.Fatalf("event wrong: %+v", ev)
	}
	got := e.quantities(t, ev.ID)
	// input and cache tokens from message_start, output from the cumulative
	// message_delta (42 wins over message_start's 1).
	want := map[string]float64{
		"input_tokens": 25, "output_tokens": 42,
		"cached_input_tokens": 100, "cache_creation_tokens": 50,
	}
	for unit, quantity := range want {
		if got[unit] != quantity {
			t.Fatalf("unit %s = %v, want %v (all: %v)", unit, got[unit], quantity, got)
		}
	}
}

func TestTransparentCancellation(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	body := `{"model":"claude-fake-slow","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequestWithContext(ctx, "POST",
		e.proxy.URL+"/transparent/anthropic/"+e.relayToken+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", anthropicKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Read a couple of chunks, then hang up mid-stream.
	reader := bufio.NewReader(resp.Body)
	for i := 0; i < 4; i++ {
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}
	cancel()

	ev := e.waitUsage(t, func(ev store.UsageEvent) bool {
		return ev.ProviderID == "transparent:anthropic" && ev.Cancelled
	})
	if ev.Outcome != "cancelled" || !ev.Streamed {
		t.Fatalf("event wrong: %+v", ev)
	}
	// Partial usage from message_start still lands.
	got := e.quantities(t, ev.ID)
	if got["input_tokens"] != 25 {
		t.Fatalf("partial usage missing: %v", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !e.anthropic.cancelled.Load() {
		if time.Now().After(deadline) {
			t.Fatal("upstream request not cancelled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTransparentMetadataOnlyEndpoints(t *testing.T) {
	e := newEnv(t)
	resp, data := e.relayRequest(t, "POST", "/v1/messages/count_tokens", anthropicKey,
		[]byte(`{"model":"claude-fake-1","messages":[{"role":"user","content":"hi"}]}`))
	if resp.StatusCode != 200 || string(data) != `{"input_tokens":4325}` {
		t.Fatalf("count_tokens relay wrong: %d %s", resp.StatusCode, data)
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool {
		return ev.ProviderID == "transparent:anthropic" && ev.Endpoint == "v1/messages/count_tokens"
	})
	// count_tokens responses have no usage object; the event is metadata only.
	if got := e.quantities(t, ev.ID); len(got) != 0 {
		t.Fatalf("unexpected quantities: %v", got)
	}

	resp, data = e.relayRequest(t, "GET", "/v1/models", anthropicKey, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), "claude-fake-1") {
		t.Fatalf("models relay wrong: %d %s", resp.StatusCode, data)
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool {
		return ev.ProviderID == "transparent:anthropic" && ev.Endpoint == "v1/models"
	})
}

func TestTransparentProbesNotRecorded(t *testing.T) {
	e := newEnv(t)
	// HEAD is a connectivity probe (Claude Code sends one per session start);
	// it relays but must not land in the request log.
	resp, _ := e.relayRequest(t, "HEAD", "/v1/models", anthropicKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("HEAD not relayed: %d", resp.StatusCode)
	}
	// Recording is async; use a recorded request as the fence.
	e.relayRequest(t, "GET", "/v1/models", anthropicKey, nil)
	e.waitUsage(t, func(ev store.UsageEvent) bool {
		return ev.ProviderID == "transparent:anthropic" && ev.Endpoint == "v1/models"
	})
	events, err := e.st.ListUsageEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("HEAD probe was recorded: %+v", events)
	}
}

func TestTransparentUpstreamErrorPassthrough(t *testing.T) {
	e := newEnv(t)
	resp, data := e.relayRequest(t, "POST", "/v1/messages", "wrong-key",
		[]byte(`{"model":"claude-fake-1","messages":[]}`))
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !strings.Contains(string(data), "authentication_error") {
		t.Fatalf("upstream error body altered: %s", data)
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.ProviderID == "transparent:anthropic" })
	if ev.Outcome != "upstream_error" || !ev.StatusCode.Valid || ev.StatusCode.Int64 != 401 {
		t.Fatalf("event wrong: %+v", ev)
	}
}

func TestTransparentTokenBoundaries(t *testing.T) {
	e := newEnv(t)

	// An API key is not a relay token.
	req, _ := http.NewRequest("POST", e.proxy.URL+"/transparent/anthropic/"+e.memberKey+"/v1/messages",
		strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 404 || errorCode(t, data) != "unknown_relay_token" {
		t.Fatalf("API key accepted as relay token: %d %s", resp.StatusCode, data)
	}

	// A relay token is not an API key.
	resp2, data2 := e.request(t, "GET", "/v1/models", e.relayToken, nil)
	if resp2.StatusCode != 401 || errorCode(t, data2) != "invalid_api_key" {
		t.Fatalf("relay token accepted as API key: %d %s", resp2.StatusCode, data2)
	}

	// No usage rows for any of it.
	events, err := e.st.ListUsageEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("unexpected usage events: %+v", events)
	}
}

func TestRelayTokenLifecycle(t *testing.T) {
	e := newEnv(t)

	resp, data := e.request(t, "POST", "/my/relay-tokens", e.memberKey, map[string]any{"label": "laptop"})
	if resp.StatusCode != 201 {
		t.Fatalf("create failed: %d %s", resp.StatusCode, data)
	}
	created := decode(t, data)
	plaintext, _ := created["token"].(string)
	if !strings.HasPrefix(plaintext, "lpt_") {
		t.Fatalf("relay token has wrong prefix: %q", plaintext)
	}

	resp, data = e.request(t, "GET", "/my/relay-tokens", e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list failed: %d %s", resp.StatusCode, data)
	}
	if strings.Contains(string(data), plaintext) {
		t.Fatal("plaintext token leaked into the listing")
	}
	list := decode(t, data)
	tokens, _ := list["relay_tokens"].([]any)
	if len(tokens) != 2 { // seeded + created
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}

	// The fresh token works on the relay.
	req, _ := http.NewRequest("GET", e.proxy.URL+"/transparent/anthropic/"+plaintext+"/v1/models", nil)
	req.Header.Set("x-api-key", anthropicKey)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != 200 {
		t.Fatalf("fresh token rejected: %d", r2.StatusCode)
	}

	// Deletion revokes it.
	id, _ := created["id"].(string)
	resp, data = e.request(t, "DELETE", "/my/relay-tokens/"+id, e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete failed: %d %s", resp.StatusCode, data)
	}
	r3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body3, _ := io.ReadAll(r3.Body)
	r3.Body.Close()
	if r3.StatusCode != 404 || errorCode(t, body3) != "unknown_relay_token" {
		t.Fatalf("deleted token still accepted: %d %s", r3.StatusCode, body3)
	}

	// Another principal cannot delete my token.
	mine := decode(t, data)
	_ = mine
	resp, _ = e.request(t, "DELETE", "/my/relay-tokens/"+id, e.adminKey, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("cross-principal delete not rejected: %d", resp.StatusCode)
	}
}

func TestTransparentPricing(t *testing.T) {
	e := newEnv(t)
	feed := map[string]any{
		"version": "test-1",
		"entries": []map[string]any{
			{"model": "claude-fake-1", "unit": "input_tokens", "price_per_million": 3.0},
			{"model": "claude-fake-1", "unit": "output_tokens", "price_per_million": 15.0},
			{"model": "claude-fake-1", "unit": "cached_input_tokens", "price_per_million": 0.3},
			{"model": "claude-fake-1", "unit": "cache_creation_tokens", "price_per_million": 3.75},
		},
	}
	resp, data := e.request(t, "POST", "/admin/v1/pricing", e.adminKey, feed)
	if resp.StatusCode != 200 {
		t.Fatalf("pricing load failed: %d %s", resp.StatusCode, data)
	}

	resp, _ = e.relayRequest(t, "POST", "/v1/messages", anthropicKey,
		[]byte(`{"model":"claude-fake-1","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
	if resp.StatusCode != 200 {
		t.Fatalf("relay failed: %d", resp.StatusCode)
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.ProviderID == "transparent:anthropic" })
	if ev.Unpriced {
		t.Fatalf("event unpriced: %+v", ev)
	}
	want := (25*3.0 + 10*15.0 + 100*0.3 + 50*3.75) / 1_000_000
	if !ev.Cost.Valid || ev.Cost.Float64 < want*0.999 || ev.Cost.Float64 > want*1.001 {
		t.Fatalf("cost %v, want %v", ev.Cost, want)
	}
}
