package server_test

import (
	"encoding/json"
	"testing"

	"github.com/greyhavenhq/llmproxy/internal/store"
)

func TestEmbeddingsRoundtrip(t *testing.T) {
	e := newEnv(t)
	resp, body := e.request(t, "POST", "/v1/embeddings", e.memberKey,
		map[string]any{"model": "embed-only", "input": "hello world"})
	if resp.StatusCode != 200 {
		t.Fatalf("embeddings: %d %s", resp.StatusCode, body)
	}
	out := decode(t, body)
	if out["object"] != "list" {
		t.Fatalf("wrong body: %s", body)
	}
	if resp.Header.Get("x-llmproxy-provider") != "fake" || resp.Header.Get("x-llmproxy-model") != "m-embed" {
		t.Fatalf("missing resolution headers: %v", resp.Header)
	}

	seen := e.upstream.last(t)
	if seen.Path != "/v1/embeddings" {
		t.Fatalf("wrong upstream path: %s", seen.Path)
	}
	if seen.Header.Get("Authorization") != "Bearer "+upstreamKey {
		t.Fatal("credential not forwarded on embeddings")
	}
	var upstreamModel string
	_ = json.Unmarshal(seen.Fields["model"], &upstreamModel)
	if upstreamModel != "m-embed" {
		t.Fatalf("model not rewritten: %q", upstreamModel)
	}

	ev := e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Endpoint == "embeddings" })
	units := e.quantities(t, ev.ID)
	if units["input_tokens"] != 9 {
		t.Fatalf("wrong embedding quantities: %v", units)
	}
	if _, ok := units["output_tokens"]; ok {
		t.Fatal("embeddings must not record output tokens")
	}
}

func TestEmbeddingsBatchCap(t *testing.T) {
	e := newEnv(t) // cap is 4 in the test config

	resp, _ := e.request(t, "POST", "/v1/embeddings", e.memberKey,
		map[string]any{"model": "embed-only", "input": []string{"a", "b", "c"}})
	if resp.StatusCode != 200 {
		t.Fatalf("batch of 3 should pass: %d", resp.StatusCode)
	}

	resp, body := e.request(t, "POST", "/v1/embeddings", e.memberKey,
		map[string]any{"model": "embed-only", "input": []string{"a", "b", "c", "d", "e"}})
	if resp.StatusCode != 400 || errorCode(t, body) != "embedding_batch_too_large" {
		t.Fatalf("batch of 5 should be capped: %d %s", resp.StatusCode, body)
	}
}

func TestChatModelRejectsEmbeddings(t *testing.T) {
	e := newEnv(t)
	resp, body := e.request(t, "POST", "/v1/embeddings", e.memberKey,
		map[string]any{"model": "alpha", "input": "hi"})
	if resp.StatusCode != 400 || errorCode(t, body) != "endpoint_not_supported" {
		t.Fatalf("chat model must reject embeddings: %d %s", resp.StatusCode, body)
	}
}
