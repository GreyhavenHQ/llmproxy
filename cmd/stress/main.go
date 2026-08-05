// Stress test the proxy end to end over real sockets.
//
// Boots a fake OpenAI-compatible upstream and the proxy in one process, seeds
// a provider/model/key, then fires concurrent unary and streaming chat
// completions. Reports throughput, latency percentiles, memory, and whether
// usage accounting kept up.
//
// Usage: go run ./cmd/stress -requests 2000 -concurrency 100 -stream-ratio 0.5
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/monadical/llmproxy/internal/config"
	"github.com/monadical/llmproxy/internal/secrets"
	"github.com/monadical/llmproxy/internal/server"
	"github.com/monadical/llmproxy/internal/store"
)

const chunkCount = 20

func makeUpstream() http.Handler {
	var chunks [][]byte
	for i := 0; i < chunkCount; i++ {
		payload, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-stress", "object": "chat.completion.chunk", "created": 1700000000,
			"model":   "m-stress",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": fmt.Sprintf("tok%d ", i)}, "finish_reason": nil}},
		})
		chunks = append(chunks, append(append([]byte("data: "), payload...), '\n', '\n'))
	}
	usageChunk := []byte(`data: {"id":"chatcmpl-stress","object":"chat.completion.chunk","created":1700000000,` +
		`"model":"m-stress","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":20,"total_tokens":40}}` + "\n\n")
	unaryBody, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-stress", "object": "chat.completion", "created": 1700000000, "model": "m-stress",
		"choices": []any{map[string]any{"index": 0,
			"message": map[string]any{"role": "assistant", "content": "ok ok ok ok ok ok ok ok ok ok"}, "finish_reason": "stop"}},
		"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 20, "total_tokens": 40},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var fields map[string]json.RawMessage
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &fields)
		stream := false
		if raw, ok := fields["stream"]; ok {
			_ = json.Unmarshal(raw, &stream)
		}
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(unaryBody)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range chunks {
			_, _ = w.Write(chunk)
		}
		_, _ = w.Write(usageChunk)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	return mux
}

func percentiles(samples []time.Duration) string {
	if len(samples) == 0 {
		return "n/a"
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	pct := func(p float64) time.Duration {
		idx := int(float64(len(samples)) * p)
		if idx >= len(samples) {
			idx = len(samples) - 1
		}
		return samples[idx]
	}
	return fmt.Sprintf("p50=%.1fms p95=%.1fms p99=%.1fms max=%.1fms",
		float64(pct(0.50).Microseconds())/1000, float64(pct(0.95).Microseconds())/1000,
		float64(pct(0.99).Microseconds())/1000, float64(samples[len(samples)-1].Microseconds())/1000)
}

func heapMiB() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

func main() {
	// The access log would flood stdout at stress rates.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	requests := flag.Int("requests", 500, "total requests")
	concurrency := flag.Int("concurrency", 50, "concurrent workers")
	streamRatio := flag.Float64("stream-ratio", 0.5, "fraction of streamed requests")
	flag.Parse()

	dir, err := os.MkdirTemp("", "llmproxy-stress-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	upstream := httptest.NewServer(makeUpstream())
	defer upstream.Close()

	cfg := config.Config{
		DatabaseURL:    filepath.Join(dir, "stress.db"),
		SecretFile:     filepath.Join(dir, "secret"),
		LocalAdminName: "local-admin",
		CatalogTTL:     5 * time.Second,
		MaxBodyBytes:   10 << 20,
	}
	secret, err := secrets.LoadOrCreate("", cfg.SecretFile)
	if err != nil {
		panic(err)
	}
	st, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		panic(err)
	}
	defer st.Close()
	srv := server.New(cfg, st, secret)
	ctx := context.Background()
	if err := srv.Bootstrap(ctx); err != nil {
		panic(err)
	}
	proxy := httptest.NewServer(srv.Handler())
	defer proxy.Close()

	provider := &store.Provider{
		Name: "stress", WireFormat: "openai", BaseURL: upstream.URL + "/v1",
		VerifyTLS: true, TimeoutConnect: 5, TimeoutRead: 60, TimeoutWrite: 30, Enabled: true,
	}
	if err := st.CreateProvider(ctx, provider, nil, nil); err != nil {
		panic(err)
	}
	binding := &store.ModelBinding{
		Alias: "stress", ProviderID: provider.ID, UpstreamName: "m-stress",
		CapabilitySet: "chat,chat_stream", Origin: "declared",
	}
	if err := st.CreateBinding(ctx, binding, nil); err != nil {
		panic(err)
	}
	principal, err := st.GetOrCreatePrincipal(ctx, "stress-user", "user", "member", nil)
	if err != nil {
		panic(err)
	}
	apiKey := secrets.GenerateAPIKey()
	if _, err := st.CreateAPIKey(ctx, principal.ID, secrets.HashAPIKey(secret, apiKey), secrets.KeySuffix(apiKey), "stress", nil); err != nil {
		panic(err)
	}

	heapBefore := heapMiB()
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: *concurrency * 2}}
	url := proxy.URL + "/v1/chat/completions"
	streamEvery := 0
	if *streamRatio > 0 {
		streamEvery = int(1 / *streamRatio)
		if streamEvery < 1 {
			streamEvery = 1
		}
	}

	var mu sync.Mutex
	var unaryLatencies, streamLatencies []time.Duration
	var errors []string
	work := make(chan int, *requests)
	for i := 0; i < *requests; i++ {
		work <- i
	}
	close(work)

	wallStart := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				streamed := streamEvery > 0 && i%streamEvery == 0
				payload, _ := json.Marshal(map[string]any{
					"model":    "stress",
					"messages": []any{map[string]any{"role": "user", "content": fmt.Sprintf("request %d", i)}},
					"stream":   streamed,
				})
				started := time.Now()
				req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
				req.Header.Set("Authorization", "Bearer "+apiKey)
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					mu.Lock()
					errors = append(errors, err.Error())
					mu.Unlock()
					continue
				}
				_, readErr := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				elapsed := time.Since(started)
				mu.Lock()
				switch {
				case resp.StatusCode != 200:
					errors = append(errors, fmt.Sprintf("status %d", resp.StatusCode))
				case readErr != nil:
					errors = append(errors, readErr.Error())
				case streamed:
					streamLatencies = append(streamLatencies, elapsed)
				default:
					unaryLatencies = append(unaryLatencies, elapsed)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	wall := time.Since(wallStart)

	// Give async usage recording a moment, then check accounting kept up.
	var recorded int64
	for i := 0; i < 100; i++ {
		recorded, _ = st.CountUsageEvents(ctx)
		if recorded >= int64(*requests) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	done := len(unaryLatencies) + len(streamLatencies)
	fmt.Printf("requests:      %d (concurrency %d)\n", *requests, *concurrency)
	fmt.Printf("completed:     %d  errors: %d\n", done, len(errors))
	fmt.Printf("wall time:     %.2fs  throughput: %.1f req/s\n", wall.Seconds(), float64(done)/wall.Seconds())
	fmt.Printf("unary   (%d): %s\n", len(unaryLatencies), percentiles(unaryLatencies))
	fmt.Printf("stream  (%d): %s\n", len(streamLatencies), percentiles(streamLatencies))
	fmt.Printf("usage events recorded: %d/%d\n", recorded, *requests)
	fmt.Printf("heap: %.1f MiB -> %.1f MiB\n", heapBefore, heapMiB())
	if len(errors) > 0 {
		fmt.Println("first errors:", errors[:min(5, len(errors))])
		os.Exit(1)
	}
	srv.Drain()
}
