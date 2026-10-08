package server_test

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/server"
	"github.com/greyhavenhq/llmproxy/internal/store"
)

const perfWindow = "?since=2026-01-01T00:00:00Z&until=2026-01-01T03:00:00Z&bucket=hour"

func seedPerformance(t *testing.T, e *env) {
	t.Helper()
	type row struct {
		ts         string
		outcome    string
		cancelled  bool
		streamed   bool
		durationMs int64
		ttftMs     int64
		output     float64
	}
	rows := []row{
		{"2026-01-01T00:10:00.000000Z", "ok", false, true, 2000, 500, 300},
		{"2026-01-01T00:20:00.000000Z", "ok", false, true, 4000, 1000, 900},
		{"2026-01-01T00:30:00.000000Z", "upstream_error", false, true, 100, 0, 0},
		{"2026-01-01T00:35:00.000000Z", "cancelled", true, true, 9000, 100, 50},
		{"2026-01-01T00:40:00.000000Z", "ok", false, false, 1000, 0, 100},
		{"2026-01-01T00:50:00.000000Z", "ok", false, true, 1300, 300, 0},
	}
	for _, r := range rows {
		ev := &store.UsageEvent{
			TS: r.ts, PrincipalID: "p", Alias: "alpha", UpstreamName: "m-alpha", Endpoint: "chat",
			Outcome: r.outcome, Cancelled: r.cancelled, Streamed: r.streamed, DurationMs: r.durationMs,
		}
		if r.ttftMs > 0 {
			ev.TTFTMs = sql.NullInt64{Int64: r.ttftMs, Valid: true}
		}
		var qs []store.UsageQuantity
		if r.output > 0 {
			qs = append(qs, store.UsageQuantity{Unit: "output_tokens", Quantity: r.output, Measurement: "upstream_reported"})
		}
		if err := e.st.InsertUsageEvent(context.Background(), ev, qs); err != nil {
			t.Fatal(err)
		}
	}
}

func figureOf(t *testing.T, m map[string]any, name string) [3]any {
	t.Helper()
	f, ok := m[name].(map[string]any)
	if !ok {
		t.Fatalf("%s missing in %v", name, m)
	}
	return [3]any{f["mean"], f["p50"], f["p95"]}
}

func TestStatsPerformance(t *testing.T) {
	e := newEnv(t)
	seedPerformance(t, e)
	resp, data := e.request(t, "GET", "/stats/performance"+perfWindow, e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/stats/performance = %d %s", resp.StatusCode, data)
	}
	body := decode(t, data)
	series, _ := body["series"].([]any)
	if len(series) != 3 {
		t.Fatalf("series has %d buckets, want 3", len(series))
	}
	first := series[0].(map[string]any)

	// Failed and cancelled rows stay out of every latency figure.
	if got := figureOf(t, first, "duration_ms"); got != [3]any{2075.0, 1300.0, 4000.0} {
		t.Fatalf("duration = %v, want 2075/1300/4000", got)
	}
	// The unary row has no ttft and is not counted as zero.
	if got := figureOf(t, first, "ttft_ms"); got != [3]any{600.0, 500.0, 1000.0} {
		t.Fatalf("ttft = %v, want 600/500/1000", got)
	}
	// Rows without output tokens or ttft are skipped; speed excludes the ttft.
	if got := figureOf(t, first, "tokens_per_second"); got != [3]any{250.0, 200.0, 300.0} {
		t.Fatalf("tokens/s = %v, want 250/200/300", got)
	}

	empty := series[1].(map[string]any)
	for _, name := range []string{"duration_ms", "ttft_ms", "tokens_per_second"} {
		if got := figureOf(t, empty, name); got != [3]any{nil, nil, nil} {
			t.Fatalf("empty bucket %s = %v, want nulls", name, got)
		}
	}

	summary := body["summary"].(map[string]any)
	if got := figureOf(t, summary, "duration_ms"); got != [3]any{2075.0, 1300.0, 4000.0} {
		t.Fatalf("summary duration = %v", got)
	}
	conc := summary["concurrency"].(map[string]any)
	if conc["peak"] != 1.0 {
		t.Fatalf("summary peak concurrency = %v, want 1", conc["peak"])
	}

	models, _ := body["models"].([]any)
	if len(models) != 1 || models[0].(map[string]any)["model"] != "alpha" {
		t.Fatalf("models = %v, want one alpha row", models)
	}
}

func TestStatsPerformanceRefusesOverCap(t *testing.T) {
	e := newEnv(t)
	seedPerformance(t, e)
	defer server.SetPerformanceEventCap(3)()
	resp, data := e.request(t, "GET", "/stats/performance"+perfWindow, e.memberKey, nil)
	if resp.StatusCode != 400 || errorCode(t, data) != "range_too_large" {
		t.Fatalf("over cap = %d %s, want 400 range_too_large", resp.StatusCode, data)
	}
}

// Parallel streams overlap by at least the ttft delay, so the sweep over
// ts - duration sees them all in flight at once.
func TestStatsPerformancePeakConcurrency(t *testing.T) {
	e := newEnv(t)
	resp, body := e.request(t, "POST", "/admin/v1/models", e.adminKey, map[string]any{
		"alias": "ttft", "provider": "fake", "upstream_name": "m-ttft", "capabilities": []string{"chat", "chat_stream"},
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create model: %d %s", resp.StatusCode, body)
	}
	const parallel = 3
	statuses := make(chan int, parallel)
	var wg sync.WaitGroup
	for range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", e.proxy.URL+"/v1/chat/completions",
				bytes.NewReader([]byte(`{"model":"ttft","stream":true,"messages":[]}`)))
			req.Header.Set("Authorization", "Bearer "+e.memberKey)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				statuses <- 0
				return
			}
			_, _ = bytes.NewBuffer(nil).ReadFrom(resp.Body)
			resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != 200 {
			t.Fatalf("stream = %d, want 200", status)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		events, err := e.st.ListUsageEvents(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, ev := range events {
			if ev.Alias == "ttft" {
				n++
			}
		}
		if n == parallel {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d ttft events recorded, want %d", n, parallel)
		}
		time.Sleep(20 * time.Millisecond)
	}
	resp, data := e.request(t, "GET", "/stats/performance?model=ttft&bucket=hour", e.memberKey, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("/stats/performance = %d %s", resp.StatusCode, data)
	}
	conc := decode(t, data)["summary"].(map[string]any)["concurrency"].(map[string]any)
	if conc["peak"] != float64(parallel) {
		t.Fatalf("peak concurrency = %v, want %d", conc["peak"], parallel)
	}
}
