package server_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/secrets"
	"github.com/greyhavenhq/llmproxy/internal/store"
)

type scriptedUpstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	auth []string
}

func newScripted(t *testing.T, handler http.HandlerFunc) *scriptedUpstream {
	t.Helper()
	u := &scriptedUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.auth = append(u.auth, r.Header.Get("Authorization"))
		u.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *scriptedUpstream) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.auth...)
}

func statusHandler(status int, body string, headers map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
}

func (e *env) upstreamCalls() int {
	e.upstream.mu.Lock()
	defer e.upstream.mu.Unlock()
	return len(e.upstream.requests)
}

// addTarget creates a provider at baseURL with one direct model on it.
func (e *env) addTarget(t *testing.T, name, baseURL, rateLimitHeaders string) *store.ModelBinding {
	t.Helper()
	ctx := context.Background()
	encrypted, err := secrets.EncryptCredential(e.secret, upstreamKey)
	if err != nil {
		t.Fatal(err)
	}
	p := &store.Provider{Name: name, WireFormat: "openai", BaseURL: baseURL,
		CredentialCiphertext: sql.NullString{String: encrypted, Valid: true},
		VerifyTLS:            true, TimeoutConnect: 5, TimeoutRead: 30, Enabled: true,
		RateLimitHeaders: rateLimitHeaders}
	if err := e.st.CreateProvider(ctx, p, nil, nil); err != nil {
		t.Fatal(err)
	}
	b := &store.ModelBinding{Alias: name + "-m", ProviderID: p.ID, UpstreamName: "up-" + name,
		CapabilitySet: "chat,chat_stream", Origin: "declared"}
	if err := e.st.CreateBinding(ctx, b, nil); err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *env) addHA(t *testing.T, targets ...*store.ModelBinding) {
	t.Helper()
	ctx := context.Background()
	alpha, err := e.st.GetBindingByAlias(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	ha := &store.ModelBinding{Alias: "ha", Origin: "declared", Strategy: "failover"}
	for _, b := range append(targets, alpha) {
		ha.Targets = append(ha.Targets, store.BindingTarget{ID: b.ID})
	}
	if err := e.st.CreateBinding(ctx, ha, nil); err != nil {
		t.Fatal(err)
	}
}

func haChat(stream bool, content string) map[string]any {
	return map[string]any{"model": "ha", "stream": stream,
		"messages": []any{map[string]any{"role": "user", "content": content}}}
}

func TestFailoverOnRefusedConnection(t *testing.T) {
	e := newEnv(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	e.addHA(t, e.addTarget(t, "dead", deadURL+"/v1", ""))

	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey, haChat(false, "hi"))
	if resp.StatusCode != 200 {
		t.Fatalf("chat: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("x-llmproxy-provider") != "fake" || resp.Header.Get("x-llmproxy-model") != "m-alpha" ||
		resp.Header.Get("x-llmproxy-attempts") != "2" {
		t.Fatalf("headers: %v", resp.Header)
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool {
		return ev.Alias == "ha" && ev.FailedOver && ev.Outcome == "unreachable" && ev.ErrorKind == "connection_error"
	})
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "ha" && !ev.FailedOver && ev.Outcome == "ok" })

	mresp, err := http.Get(e.proxy.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	metrics, _ := io.ReadAll(mresp.Body)
	mresp.Body.Close()
	if !strings.Contains(string(metrics), `llmproxy_failover_total{model="ha",provider="dead",reason="unreachable"} 1`) {
		t.Fatalf("failover metric missing:\n%s", metrics)
	}

	_, logBody := e.request(t, "GET", "/stats/requests?model=ha", e.adminKey, nil)
	flags := map[string]bool{}
	for _, row := range decode(t, logBody)["requests"].([]any) {
		entry := row.(map[string]any)
		flags[entry["outcome"].(string)] = entry["failed_over"] == true
	}
	if !flags["unreachable"] || flags["ok"] {
		t.Fatalf("failed_over flags in the request log: %v %s", flags, logBody)
	}

	if resp, body := e.request(t, "POST", "/admin/v1/models", e.adminKey,
		map[string]any{"alias": "one", "target": "alpha"}); resp.StatusCode != 201 {
		t.Fatalf("create one: %d %s", resp.StatusCode, body)
	}
	_, modelsBody := e.request(t, "GET", "/v1/models", e.memberKey, nil)
	for _, m := range decode(t, modelsBody)["data"].([]any) {
		entry := m.(map[string]any)
		switch entry["id"] {
		case "ha":
			targets, _ := entry["targets"].([]any)
			if entry["strategy"] != "failover" || len(targets) != 2 || targets[0] != "dead-m" || targets[1] != "alpha" {
				t.Fatalf("ha in /v1/models: %v", entry)
			}
		case "one":
			if entry["alias_of"] != "alpha" || entry["targets"] != nil || entry["strategy"] != nil {
				t.Fatalf("one in /v1/models: %v", entry)
			}
		}
	}
}

func TestFailoverOnRetryableStatus(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			e := newEnv(t)
			failing := newScripted(t, statusHandler(status, `{"error":{"message":"busy","type":"overloaded"}}`,
				map[string]string{"x-ratelimit-remaining-requests": "5"}))
			e.addHA(t, e.addTarget(t, "flaky", failing.srv.URL+"/v1",
				"remaining_requests:x-ratelimit-remaining-requests"))

			resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey, haChat(false, "hi"))
			if resp.StatusCode != 200 || resp.Header.Get("x-llmproxy-attempts") != "2" {
				t.Fatalf("chat: %d %v %s", resp.StatusCode, resp.Header, body)
			}
			if got := resp.Header.Get("x-llmproxy-ratelimit-requests-remaining"); got != "" {
				t.Fatalf("rate-limit header leaked from failed attempt: %q", got)
			}
			if calls := failing.calls(); len(calls) != 1 || calls[0] != "Bearer "+upstreamKey {
				t.Fatalf("failed attempt credential: %v", calls)
			}
			if e.upstream.last(t).Header.Get("Authorization") != "Bearer "+upstreamKey {
				t.Fatal("second attempt credential missing")
			}
			e.waitUsage(t, func(ev store.UsageEvent) bool {
				return ev.FailedOver && ev.Outcome == "upstream_error" && ev.StatusCode.Int64 == int64(status) &&
					ev.ErrorKind == "overloaded"
			})
		})
	}
}

func TestNoFailoverOnClientError(t *testing.T) {
	e := newEnv(t)
	bad := newScripted(t, statusHandler(400, `{"error":{"message":"bad","type":"invalid_request_error"}}`, nil))
	e.addHA(t, e.addTarget(t, "bad", bad.srv.URL+"/v1", ""))

	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey, haChat(false, "hi"))
	if resp.StatusCode != 400 || resp.Header.Get("x-llmproxy-attempts") != "1" {
		t.Fatalf("chat: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	if n := e.upstreamCalls(); n != 0 {
		t.Fatalf("second target called %d times", n)
	}
}

func TestLastTargetResponsePassesThrough(t *testing.T) {
	e := newEnv(t)
	first := newScripted(t, statusHandler(503, `{"error":{"message":"first"}}`, nil))
	second := newScripted(t, statusHandler(503, `{"error":{"message":"second"}}`, nil))
	ctx := context.Background()
	a, b := e.addTarget(t, "first", first.srv.URL+"/v1", ""), e.addTarget(t, "second", second.srv.URL+"/v1", "")
	ha := &store.ModelBinding{Alias: "ha", Origin: "declared", Strategy: "failover",
		Targets: []store.BindingTarget{{ID: a.ID}, {ID: b.ID}}}
	if err := e.st.CreateBinding(ctx, ha, nil); err != nil {
		t.Fatal(err)
	}

	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey, haChat(false, "hi"))
	if resp.StatusCode != 503 || !strings.Contains(string(body), "second") {
		t.Fatalf("chat: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("x-llmproxy-attempts") != "2" || resp.Header.Get("x-llmproxy-provider") != "second" {
		t.Fatalf("headers: %v", resp.Header)
	}
}

func TestStreamFailoverBeforeFirstByte(t *testing.T) {
	e := newEnv(t)
	failing := newScripted(t, statusHandler(503, `{"error":{"message":"busy"}}`, nil))
	e.addHA(t, e.addTarget(t, "flaky", failing.srv.URL+"/v1", ""))

	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey, haChat(true, "hi"))
	if resp.StatusCode != 200 || !strings.Contains(string(body), "word2") {
		t.Fatalf("stream: %d %s", resp.StatusCode, body)
	}
	if calls := failing.calls(); len(calls) != 1 || calls[0] != "Bearer "+upstreamKey {
		t.Fatalf("failed attempt credential: %v", calls)
	}
	if e.upstream.last(t).Header.Get("Authorization") != "Bearer "+upstreamKey {
		t.Fatal("second attempt credential missing")
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "ha" && ev.Streamed && ev.Outcome == "ok" })
}

func TestNoFailoverAfterFirstStreamByte(t *testing.T) {
	e := newEnv(t)
	breaking := newScripted(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write(sseChunk("up-breaking", map[string]any{"content": "partial"}, nil, nil, false))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})
	e.addHA(t, e.addTarget(t, "breaking", breaking.srv.URL+"/v1", ""))

	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey, haChat(true, "hi"))
	if resp.StatusCode != 200 || !strings.Contains(string(body), "partial") {
		t.Fatalf("stream: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("x-llmproxy-attempts") != "1" {
		t.Fatalf("attempts: %q", resp.Header.Get("x-llmproxy-attempts"))
	}
	if n := e.upstreamCalls(); n != 0 {
		t.Fatalf("second target called %d times after the first byte", n)
	}
}

func TestClientCancelStopsFailover(t *testing.T) {
	e := newEnv(t)
	hanging := newScripted(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	e.addHA(t, e.addTarget(t, "hanging", hanging.srv.URL+"/v1", ""))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", e.proxy.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"ha","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+e.memberKey)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("expected the client to time out")
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "ha" && ev.Cancelled })
	time.Sleep(200 * time.Millisecond)
	if n := e.upstreamCalls(); n != 0 {
		t.Fatalf("second target called %d times after cancel", n)
	}
}

func TestFailoverMarkerNeverReachesDisk(t *testing.T) {
	e := newEnv(t)
	echo := newScripted(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		fmt.Fprintf(w, `{"error":{"message":%q,"type":%q}}`, raw, raw)
	})
	e.addHA(t, e.addTarget(t, "echo", echo.srv.URL+"/v1", ""))

	resp, body := e.request(t, "POST", "/v1/chat/completions", e.memberKey,
		haChat(false, "SECRETMARKER-failover content"))
	if resp.StatusCode != 200 {
		t.Fatalf("chat: %d %s", resp.StatusCode, body)
	}
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.FailedOver })
	e.waitUsage(t, func(ev store.UsageEvent) bool { return ev.Alias == "ha" && ev.Outcome == "ok" })
	assertNoMarkerOnDisk(t, e)
}
