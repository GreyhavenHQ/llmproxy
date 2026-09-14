package server_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/config"
	"github.com/greyhavenhq/llmproxy/internal/secrets"
	"github.com/greyhavenhq/llmproxy/internal/server"
	"github.com/greyhavenhq/llmproxy/internal/store"
)

type rlEnv struct {
	proxy *httptest.Server
	key   string
	srv   *server.Server
	st    *store.Store
}

func newRateLimitEnv(t *testing.T) *rlEnv {
	t.Helper()
	dir := t.TempDir()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-limit-requests", "1000")
		w.Header().Set("x-ratelimit-remaining-requests", "943")
		w.Header().Set("x-ratelimit-limit-tokens", "2000000")
		w.Header().Set("x-ratelimit-remaining-tokens", "1811204")

		raw, _ := io.ReadAll(r.Body)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		var stream bool
		if s, ok := fields["stream"]; ok {
			_ = json.Unmarshal(s, &stream)
		}

		if !stream {
			w.Header().Set("Content-Type", "application/json")
			body, _ := json.Marshal(map[string]any{
				"id": "chatcmpl-rl", "object": "chat.completion", "model": "m-rl",
				"choices": []any{map[string]any{
					"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
			})
			w.Write(body)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		payload, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-rl", "object": "chat.completion.chunk", "model": "m-rl",
			"choices": []any{}, "usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
		})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Config{
		DatabaseURL:       filepath.Join(dir, "proxy.db"),
		SecretFile:        filepath.Join(dir, "secret"),
		LocalAdminName:    "local-admin",
		AdminPasswordFile: filepath.Join(dir, "admin-password"),
		CatalogTTL:        0,
		MaxBodyBytes:      256 * 1024,
		SessionTTL:        time.Hour,
	}
	secret, err := secrets.LoadOrCreate("", cfg.SecretFile)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(cfg, st, secret)
	if err := srv.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	encrypted, err := secrets.EncryptCredential(secret, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	rlHeaders := "limit_requests:x-ratelimit-limit-requests,remaining_requests:x-ratelimit-remaining-requests," +
		"limit_tokens:x-ratelimit-limit-tokens,remaining_tokens:x-ratelimit-remaining-tokens"
	provider := &store.Provider{
		Name: "rl-upstream", WireFormat: "openai", BaseURL: upstream.URL + "/v1",
		CredentialCiphertext: sql.NullString{String: encrypted, Valid: true},
		VerifyTLS: true, TimeoutConnect: 5, TimeoutRead: 30, Enabled: true,
		RateLimitHeaders: rlHeaders,
	}
	if err := st.CreateProvider(ctx, provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	binding := store.ModelBinding{
		Alias: "rl-model", ProviderID: provider.ID, UpstreamName: "m-rl",
		CapabilitySet: "chat,chat_stream", Origin: "declared",
	}
	if err := st.CreateBinding(ctx, &binding, nil); err != nil {
		t.Fatal(err)
	}
	member, err := st.GetOrCreatePrincipal(ctx, "local-admin", "user", "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := secrets.GenerateAPIKey()
	if _, err := st.CreateAPIKey(ctx, member.ID,
		secrets.HashAPIKey(secret, plaintext), secrets.KeySuffix(plaintext), "test", nil); err != nil {
		t.Fatal(err)
	}

	proxy := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		proxy.Close()
		srv.Drain()
		st.Close()
	})
	return &rlEnv{proxy: proxy, key: plaintext, srv: srv, st: st}
}

func (e *rlEnv) chat(t *testing.T, stream bool) *http.Response {
	t.Helper()
	body := map[string]any{
		"model": "rl-model", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	if stream {
		body["stream"] = true
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.proxy.URL+"/v1/chat/completions", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+e.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp
}

func TestRateLimitHeadersCapturedUnary(t *testing.T) {
	e := newRateLimitEnv(t)
	resp := e.chat(t, false)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("x-llmproxy-ratelimit-requests-remaining"); got != "943" {
		t.Errorf("expected requests-remaining 943, got %q", got)
	}
	if got := resp.Header.Get("x-llmproxy-ratelimit-tokens-limit"); got != "2000000" {
		t.Errorf("expected tokens-limit 2000000, got %q", got)
	}
}

func TestRateLimitHeadersCapturedStream(t *testing.T) {
	e := newRateLimitEnv(t)
	resp := e.chat(t, true)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("x-llmproxy-ratelimit-requests-remaining"); got != "943" {
		t.Errorf("expected requests-remaining 943, got %q", got)
	}
	if got := resp.Header.Get("x-llmproxy-ratelimit-tokens-remaining"); got != "1811204" {
		t.Errorf("expected tokens-remaining 1811204, got %q", got)
	}
}

func TestRateLimitNoHeadersWithoutConfig(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		map[string]any{"model": "alpha", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if resp.Header.Get("x-llmproxy-ratelimit-requests-remaining") != "" {
		t.Error("rate-limit response headers set on provider without config")
	}
}

func TestRateLimitGaugesExported(t *testing.T) {
	e := newRateLimitEnv(t)
	e.chat(t, false)

	resp, err := http.Get(e.proxy.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	metrics := string(body)

	for _, want := range []string{
		`llmproxy_provider_rate_limit_remaining{provider="rl-upstream",unit="requests"} 943`,
		`llmproxy_provider_rate_limit_limit{provider="rl-upstream",unit="requests"} 1000`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("expected metric line %q in:\n%s", want, metrics)
		}
	}
}
