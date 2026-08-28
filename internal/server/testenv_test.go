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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/config"
	"github.com/greyhavenhq/llmproxy/internal/secrets"
	"github.com/greyhavenhq/llmproxy/internal/server"
	"github.com/greyhavenhq/llmproxy/internal/store"
)

const upstreamKey = "upstream-secret-key"

type recordedRequest struct {
	Path   string
	Header http.Header
	Fields map[string]json.RawMessage
}

type fakeUpstream struct {
	srv       *httptest.Server
	mu        sync.Mutex
	requests  []recordedRequest
	cancelled atomic.Bool
}

// record captures the request body's top-level fields for later assertions.
func (f *fakeUpstream) record(r *http.Request) map[string]json.RawMessage {
	raw, _ := io.ReadAll(r.Body)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{Path: r.URL.Path, Header: r.Header.Clone(), Fields: fields})
	f.mu.Unlock()
	return fields
}

func sseChunk(model string, delta map[string]any, finish any, usage map[string]any, emptyChoices bool) []byte {
	payload := map[string]any{
		"id":      "chatcmpl-fake",
		"object":  "chat.completion.chunk",
		"created": 1700000000,
		"model":   model,
	}
	if emptyChoices {
		payload["choices"] = []any{}
	} else {
		payload["choices"] = []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}
	}
	if usage != nil {
		payload["usage"] = usage
	}
	data, _ := json.Marshal(payload)
	return append(append([]byte("data: "), data...), '\n', '\n')
}

var fakeUsage = map[string]any{
	"prompt_tokens":         7,
	"completion_tokens":     5,
	"total_tokens":          12,
	"prompt_tokens_details": map[string]any{"cached_tokens": 2},
}

// ---------- fake Anthropic upstream (transparent relay tests) ----------

const anthropicKey = "sk-ant-fake-upstream-key"

// anthropicUnaryBody is served byte-for-byte so tests can assert the relay
// changes nothing.
const anthropicUnaryBody = `{"id":"msg_fake","type":"message","role":"assistant","model":"claude-fake-1",` +
	`"content":[{"type":"text","text":"hello from fake anthropic"}],"stop_reason":"end_turn",` +
	`"usage":{"input_tokens":25,"output_tokens":10,"cache_creation_input_tokens":50,"cache_read_input_tokens":100}}`

const anthropicStreamBody = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_fake","type":"message","role":"assistant",` +
	`"model":"claude-fake-1","content":[],` +
	`"usage":{"input_tokens":25,"output_tokens":1,"cache_creation_input_tokens":50,"cache_read_input_tokens":100}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

type anthropicRequest struct {
	Method  string
	Path    string
	Header  http.Header
	RawBody []byte
}

type fakeAnthropic struct {
	srv       *httptest.Server
	mu        sync.Mutex
	requests  []anthropicRequest
	cancelled atomic.Bool
}

func (f *fakeAnthropic) record(r *http.Request) []byte {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, anthropicRequest{
		Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), RawBody: raw})
	f.mu.Unlock()
	return raw
}

func (f *fakeAnthropic) last(t *testing.T) anthropicRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("fake anthropic received no requests")
	}
	return f.requests[len(f.requests)-1]
}

func newFakeAnthropic(t *testing.T) *fakeAnthropic {
	f := &fakeAnthropic{}
	mux := http.NewServeMux()

	authError := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}

	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		raw := f.record(r)
		if r.Header.Get("x-api-key") != anthropicKey {
			authError(w)
			return
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		stream := false
		if rawStream, ok := fields["stream"]; ok {
			_ = json.Unmarshal(rawStream, &stream)
		}
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Request-Id", "req_fake_anthropic")
			fmt.Fprint(w, anthropicUnaryBody)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		var model string
		_ = json.Unmarshal(fields["model"], &model)
		if model == "claude-fake-slow" {
			fmt.Fprint(w, "event: message_start\n"+
				`data: {"type":"message_start","message":{"model":"claude-fake-slow","usage":{"input_tokens":25,"output_tokens":1}}}`+"\n\n")
			flusher.Flush()
			for i := 0; ; i++ {
				fmt.Fprintf(w, "event: content_block_delta\n"+
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"tok%d "}}`+"\n\n", i)
				flusher.Flush()
				select {
				case <-r.Context().Done():
					f.cancelled.Store(true)
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
		}
		fmt.Fprint(w, anthropicStreamBody)
		flusher.Flush()
	})

	mux.HandleFunc("POST /v1/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if r.Header.Get("x-api-key") != anthropicKey {
			authError(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"input_tokens":4325}`)
	})

	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if r.Header.Get("x-api-key") != anthropicKey {
			authError(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"claude-fake-1","type":"model"}],"has_more":false}`)
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	f := &fakeUpstream{}
	mux := http.NewServeMux()

	authorized := func(r *http.Request) bool {
		return r.Header.Get("Authorization") == "Bearer "+upstreamKey
	}
	authError := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"bad upstream key","type":"invalid_request_error"}}`)
	}

	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			authError(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"m-alpha","object":"model"},{"id":"m-embed","object":"model"}]}`)
	})

	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		fields := f.record(r)
		if !authorized(r) {
			authError(w)
			return
		}
		var model string
		_ = json.Unmarshal(fields["model"], &model)
		if model == "m-error" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			return
		}
		var stream bool
		if rawStream, ok := fields["stream"]; ok {
			_ = json.Unmarshal(rawStream, &stream)
		}
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			body, _ := json.Marshal(map[string]any{
				"id": "chatcmpl-fake", "object": "chat.completion", "created": 1700000000, "model": model,
				"choices": []any{map[string]any{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": "hello from the fake upstream"},
					"finish_reason": "stop",
				}},
				"usage": fakeUsage,
			})
			_, _ = w.Write(body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		if model == "m-slow" {
			for i := 0; ; i++ {
				_, _ = w.Write(sseChunk(model, map[string]any{"content": fmt.Sprintf("tok%d ", i)}, nil, nil, false))
				flusher.Flush()
				select {
				case <-r.Context().Done():
					f.cancelled.Store(true)
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
		}
		_, _ = w.Write(sseChunk(model, map[string]any{"role": "assistant", "content": ""}, nil, nil, false))
		for i := 0; i < 3; i++ {
			_, _ = w.Write(sseChunk(model, map[string]any{"content": fmt.Sprintf("word%d ", i)}, nil, nil, false))
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = w.Write(sseChunk(model, map[string]any{}, "stop", nil, false))
		_, _ = w.Write(sseChunk(model, nil, nil, fakeUsage, true))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})

	mux.HandleFunc("POST /v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if !authorized(r) {
			authError(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],`+
			`"model":"m-embed","usage":{"prompt_tokens":9,"total_tokens":9}}`)
	})

	mux.HandleFunc("POST /v1/completions", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if !authorized(r) {
			authError(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cmpl-fake","object":"text_completion","created":1700000000,`+
			`"choices":[{"text":"legacy ok","index":0,"logprobs":null,"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeUpstream) last(t *testing.T) recordedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("fake upstream received no requests")
	}
	return f.requests[len(f.requests)-1]
}

type env struct {
	dir        string
	st         *store.Store
	srv        *server.Server
	proxy      *httptest.Server
	upstream   *fakeUpstream
	anthropic  *fakeAnthropic
	secret     []byte
	adminKey   string
	memberKey  string
	relayToken string // member's relay token for the transparent relay
	cfg        config.Config
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	upstream := newFakeUpstream(t)
	anthropic := newFakeAnthropic(t)
	cfg := config.Config{
		DatabaseURL:       filepath.Join(dir, "proxy.db"),
		SecretFile:        filepath.Join(dir, "secret"),
		LocalAdminName:    "local-admin",
		AdminPasswordFile: filepath.Join(dir, "admin-password"),
		CatalogTTL:        0,
		MaxBodyBytes:      256 * 1024,
		MaxEmbeddingBatch: 4,
		SessionTTL:        time.Hour,

		TransparentAnthropicBaseURL: anthropic.srv.URL,
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
	proxy := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		proxy.Close()
		srv.Drain()
		st.Close()
	})

	e := &env{dir: dir, st: st, srv: srv, proxy: proxy, upstream: upstream, anthropic: anthropic, secret: secret, cfg: cfg}
	e.seed(t)
	return e
}

func (e *env) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	encrypted, err := secrets.EncryptCredential(e.secret, upstreamKey)
	if err != nil {
		t.Fatal(err)
	}
	provider := &store.Provider{
		Name: "fake", WireFormat: "openai", BaseURL: e.upstream.srv.URL + "/v1",
		CredentialCiphertext: sql.NullString{String: encrypted, Valid: true},
		VerifyTLS:            true, TimeoutConnect: 5, TimeoutRead: 30, Enabled: true,
	}
	if err := e.st.CreateProvider(ctx, provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	bindings := []store.ModelBinding{
		{Alias: "alpha", UpstreamName: "m-alpha", CapabilitySet: "chat,chat_stream,completions"},
		{Alias: "slow", UpstreamName: "m-slow", CapabilitySet: "chat,chat_stream"},
		{Alias: "broken", UpstreamName: "m-error", CapabilitySet: "chat,chat_stream"},
		{Alias: "embed-only", UpstreamName: "m-embed", CapabilitySet: "embeddings"},
	}
	for i := range bindings {
		bindings[i].ProviderID = provider.ID
		bindings[i].Origin = "declared"
		if err := e.st.CreateBinding(ctx, &bindings[i], nil); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := e.st.GetOrCreatePrincipal(ctx, "local-admin", "user", "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	member, err := e.st.GetOrCreatePrincipal(ctx, "bob", "user", "member", nil)
	if err != nil {
		t.Fatal(err)
	}
	e.adminKey = e.mintKey(t, admin.ID, "test-admin")
	e.memberKey = e.mintKey(t, member.ID, "test-member")
	e.relayToken = e.mintRelayToken(t, member.ID, "test-relay")
}

func (e *env) mintRelayToken(t *testing.T, principalID, label string) string {
	t.Helper()
	plaintext := secrets.GenerateRelayToken()
	if _, err := e.st.CreateRelayToken(context.Background(), principalID,
		secrets.HashAPIKey(e.secret, plaintext), secrets.KeySuffix(plaintext), label, nil); err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func (e *env) mintKey(t *testing.T, principalID, label string) string {
	t.Helper()
	plaintext := secrets.GenerateAPIKey()
	if _, err := e.st.CreateAPIKey(context.Background(), principalID,
		secrets.HashAPIKey(e.secret, plaintext), secrets.KeySuffix(plaintext), label, nil); err != nil {
		t.Fatal(err)
	}
	return plaintext
}

// request performs an HTTP call against the proxy and returns status + body.
func (e *env) request(t *testing.T, method, path, key string, body any) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(b)
	default:
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, e.proxy.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if reader != nil {
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

func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("invalid JSON %q: %v", data, err)
	}
	return out
}

func errorCode(t *testing.T, data []byte) string {
	t.Helper()
	body := decode(t, data)
	errObj, _ := body["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	return code
}

// waitUsage polls until a usage event matching pred exists (recording is async).
func (e *env) waitUsage(t *testing.T, pred func(store.UsageEvent) bool) store.UsageEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, err := e.st.ListUsageEvents(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range events {
			if pred(ev) {
				return ev
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no matching usage event within 5s")
	return store.UsageEvent{}
}

func (e *env) quantities(t *testing.T, eventID string) map[string]float64 {
	t.Helper()
	rows, err := e.st.ListQuantities(context.Background(), eventID)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]float64, len(rows))
	for _, q := range rows {
		out[q.Unit] = q.Quantity
	}
	return out
}
