package server_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/monadical/llmproxy/internal/store"
)

// Story F2: a client abort cancels the upstream request and partial usage is
// recorded as cancelled. Everything runs over real sockets (httptest servers).
func TestClientAbortCancelsUpstreamAndRecordsPartial(t *testing.T) {
	e := newEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payload := []byte(`{"model":"slow","messages":[{"role":"user","content":"go"}],"stream":true}`)
	req, err := http.NewRequestWithContext(ctx, "POST",
		e.proxy.URL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.memberKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream setup failed: %d", resp.StatusCode)
	}

	buf := make([]byte, 4096)
	reads := 0
	for reads < 3 {
		if _, err := resp.Body.Read(buf); err != nil {
			t.Fatalf("stream died early: %v", err)
		}
		reads++
	}
	cancel() // abort the client connection mid-stream

	deadline := time.Now().Add(5 * time.Second)
	for !e.upstream.cancelled.Load() {
		if time.Now().After(deadline) {
			t.Fatal("upstream request was not cancelled")
		}
		time.Sleep(20 * time.Millisecond)
	}

	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Cancelled })
	if ev.Outcome != "cancelled" || !ev.Streamed {
		t.Fatalf("wrong cancelled event: outcome=%s streamed=%v", ev.Outcome, ev.Streamed)
	}
}

func TestUnreachableProviderIsAProxyError(t *testing.T) {
	e := newEnv(t)

	resp, body := e.request(t, "POST", "/admin/v1/providers", e.adminKey, map[string]any{
		"name": "ghost", "base_url": "http://127.0.0.1:9/v1", "timeout_connect": 0.5,
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create ghost provider: %d %s", resp.StatusCode, body)
	}
	resp, body = e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "ghost-model", "provider": "ghost", "upstream_name": "x",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create ghost model: %d %s", resp.StatusCode, body)
	}

	resp, body = e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "ghost-model", "messages": []any{}})
	if resp.StatusCode != 502 || errorCode(t, body) != "provider_unreachable" {
		t.Fatalf("want 502 provider_unreachable, got %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("x-llmproxy-error-source") != "proxy" {
		t.Fatal("unreachable must be marked source=proxy")
	}
}
