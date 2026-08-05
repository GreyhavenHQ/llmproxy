package server_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monadical/llmproxy/internal/store"
)

func TestUnaryChatRoundtrip(t *testing.T) {
	e := newEnv(t)
	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 200 {
		t.Fatalf("chat: %d %s", resp.StatusCode, body)
	}
	out := decode(t, body)
	content := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]
	if content != "hello from the fake upstream" {
		t.Fatalf("wrong body: %v", content)
	}

	// Resolved-provider headers (story C4).
	if resp.Header.Get("x-llmproxy-provider") != "fake" || resp.Header.Get("x-llmproxy-model") != "m-alpha" {
		t.Fatalf("missing resolution headers: %v", resp.Header)
	}

	// Credential forwarding regression (a known gateway bug class) and model rewrite.
	seen := e.upstream.last(t)
	if seen.Header.Get("Authorization") != "Bearer "+upstreamKey {
		t.Fatalf("upstream credential not forwarded: %q", seen.Header.Get("Authorization"))
	}
	var upstreamModel string
	_ = json.Unmarshal(seen.Fields["model"], &upstreamModel)
	if upstreamModel != "m-alpha" {
		t.Fatalf("model not rewritten: %q", upstreamModel)
	}

	// Usage accounted per unit; no pricing feed loaded means unpriced, never zero.
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Endpoint == "chat" && ev.Outcome == "ok" })
	units := e.quantities(t, ev.ID)
	if units["input_tokens"] != 7 || units["output_tokens"] != 5 || units["cached_input_tokens"] != 2 {
		t.Fatalf("wrong quantities: %v", units)
	}
	if !ev.Unpriced || ev.Cost.Valid {
		t.Fatalf("expected unpriced with null cost, got unpriced=%v cost=%v", ev.Unpriced, ev.Cost)
	}
}

func TestVisionContentPassesThroughUntouched(t *testing.T) {
	e := newEnv(t)
	// Raw JSON so the bytes the upstream receives can be compared exactly.
	messages := `[{"role":"user","content":[{"type":"text","text":"what is in this image?"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="}}]}]`
	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha", "messages": json.RawMessage(messages)})
	if resp.StatusCode != 200 {
		t.Fatalf("vision chat: %d %s", resp.StatusCode, body)
	}
	seen := e.upstream.last(t)
	if !bytes.Equal(seen.Fields["messages"], []byte(messages)) {
		t.Fatalf("messages were rewritten:\nsent: %s\ngot:  %s", messages, seen.Fields["messages"])
	}
}

func TestLegacyCompletions(t *testing.T) {
	e := newEnv(t)
	resp, body := e.request(t, "POST", "/v1/completions", e.memberKey,
		map[string]any{"model": "alpha", "prompt": "say hi"})
	if resp.StatusCode != 200 {
		t.Fatalf("completions: %d %s", resp.StatusCode, body)
	}
	text := decode(t, body)["choices"].([]any)[0].(map[string]any)["text"]
	if text != "legacy ok" {
		t.Fatalf("wrong text: %v", text)
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Endpoint == "completions" })
	units := e.quantities(t, ev.ID)
	if units["input_tokens"] != 3 || units["output_tokens"] != 2 {
		t.Fatalf("wrong quantities: %v", units)
	}
}

func TestUpstreamErrorPassthrough(t *testing.T) {
	e := newEnv(t)
	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "broken", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.StatusCode != 429 {
		t.Fatalf("want 429 passthrough, got %d %s", resp.StatusCode, body)
	}
	msg := decode(t, body)["error"].(map[string]any)["message"]
	if msg != "rate limited" {
		t.Fatalf("upstream error body not preserved: %s", body)
	}
	if resp.Header.Get("x-llmproxy-error-source") != "upstream" {
		t.Fatal("upstream errors must be marked source=upstream")
	}
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Outcome == "upstream_error" })
	if !ev.StatusCode.Valid || ev.StatusCode.Int64 != 429 {
		t.Fatalf("status not recorded: %v", ev.StatusCode)
	}
}

func TestOversizedBodyRejected(t *testing.T) {
	e := newEnv(t)
	huge := bytes.Repeat([]byte("x"), int(e.cfg.MaxBodyBytes)+1)
	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey, huge)
	if resp.StatusCode != 413 || errorCode(t, body) != "request_too_large" {
		t.Fatalf("want 413 request_too_large, got %d %s", resp.StatusCode, body)
	}
}

func TestSSERoundtripAndUsage(t *testing.T) {
	e := newEnv(t)
	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{
			"model":    "alpha",
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			"stream":   true,
		})
	if resp.StatusCode != 200 {
		t.Fatalf("stream: %d %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("wrong content type: %q", ct)
	}
	if resp.Header.Get("x-llmproxy-provider") != "fake" {
		t.Fatal("missing provider header on stream")
	}
	text := string(body)
	if strings.Count(text, "word") != 3 || !strings.Contains(text, "data: [DONE]") {
		t.Fatalf("stream content wrong:\n%s", text)
	}

	// include_usage was injected on the upstream request.
	seen := e.upstream.last(t)
	var opts map[string]any
	_ = json.Unmarshal(seen.Fields["stream_options"], &opts)
	if opts["include_usage"] != true {
		t.Fatalf("include_usage not injected: %s", seen.Fields["stream_options"])
	}

	// Usage extracted from the final chunk and recorded.
	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Streamed && ev.Outcome == "ok" })
	if ev.Cancelled {
		t.Fatal("clean stream must not be flagged cancelled")
	}
	units := e.quantities(t, ev.ID)
	if units["input_tokens"] != 7 || units["output_tokens"] != 5 {
		t.Fatalf("wrong stream quantities: %v", units)
	}
}

func TestStreamChunksForwardedVerbatim(t *testing.T) {
	e := newEnv(t)
	_, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha", "messages": []any{}, "stream": true})

	var events [][]byte
	for _, block := range bytes.Split(body, []byte("\n\n")) {
		if bytes.HasPrefix(block, []byte("data: ")) {
			events = append(events, block[len("data: "):])
		}
	}
	if len(events) < 3 || !bytes.Equal(events[len(events)-1], []byte("[DONE]")) {
		t.Fatalf("unexpected SSE framing: %d events", len(events))
	}
	for _, event := range events[:len(events)-1] {
		parsed := decode(t, event)
		if parsed["object"] != "chat.completion.chunk" || parsed["id"] != "chatcmpl-fake" {
			t.Fatalf("chunk was rewritten: %s", event)
		}
	}
}
