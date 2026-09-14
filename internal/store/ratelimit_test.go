package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/greyhavenhq/llmproxy/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRateLimitSampleInsertAndLatest(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	p := &store.Provider{
		Name: "test-provider", WireFormat: "openai", BaseURL: "https://example.com",
		VerifyTLS: true, TimeoutConnect: 5, TimeoutRead: 30, Enabled: true,
	}
	if err := st.CreateProvider(ctx, p, nil, nil); err != nil {
		t.Fatal(err)
	}

	s1 := &store.RateLimitSample{
		ProviderID:        p.ID,
		Bucket:            "2026-09-14T10:00",
		ObservedAt:        "2026-09-14T10:00:45.000000Z",
		Observations:      12,
		LimitRequests:     sql.NullInt64{Int64: 1000, Valid: true},
		RemainingRequests: sql.NullInt64{Int64: 900, Valid: true},
		MinRemainingRequests: sql.NullInt64{Int64: 850, Valid: true},
		LimitTokens:       sql.NullInt64{Int64: 2000000, Valid: true},
		RemainingTokens:   sql.NullInt64{Int64: 1500000, Valid: true},
		MinRemainingTokens: sql.NullInt64{Int64: 1400000, Valid: true},
	}
	if err := st.InsertRateLimitSample(ctx, s1); err != nil {
		t.Fatal(err)
	}
	if s1.ID == "" {
		t.Fatal("expected ID to be assigned")
	}

	s2 := &store.RateLimitSample{
		ProviderID:        p.ID,
		Bucket:            "2026-09-14T10:01",
		ObservedAt:        "2026-09-14T10:01:30.000000Z",
		Observations:      5,
		LimitRequests:     sql.NullInt64{Int64: 1000, Valid: true},
		RemainingRequests: sql.NullInt64{Int64: 800, Valid: true},
		MinRemainingRequests: sql.NullInt64{Int64: 780, Valid: true},
	}
	if err := st.InsertRateLimitSample(ctx, s2); err != nil {
		t.Fatal(err)
	}

	latest, err := st.LatestRateLimitSamples(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 {
		t.Fatalf("expected 1 latest sample, got %d", len(latest))
	}
	if latest[0].Bucket != "2026-09-14T10:01" {
		t.Fatalf("expected latest bucket 2026-09-14T10:01, got %s", latest[0].Bucket)
	}
	if latest[0].RemainingRequests.Int64 != 800 {
		t.Fatalf("expected remaining_requests 800, got %d", latest[0].RemainingRequests.Int64)
	}
}

func TestRateLimitSeries(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	p := &store.Provider{
		Name: "series-provider", WireFormat: "openai", BaseURL: "https://example.com",
		VerifyTLS: true, TimeoutConnect: 5, TimeoutRead: 30, Enabled: true,
	}
	if err := st.CreateProvider(ctx, p, nil, nil); err != nil {
		t.Fatal(err)
	}

	buckets := []string{"2026-09-14T08:00", "2026-09-14T09:00", "2026-09-14T10:00"}
	for _, b := range buckets {
		s := &store.RateLimitSample{
			ProviderID:   p.ID,
			Bucket:       b,
			ObservedAt:   b + ":30.000000Z",
			Observations: 1,
			LimitRequests: sql.NullInt64{Int64: 1000, Valid: true},
		}
		if err := st.InsertRateLimitSample(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	series, err := st.RateLimitSeries(ctx, p.ID, "2026-09-14T08:30", "2026-09-14T09:30")
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("expected 1 row in range, got %d", len(series))
	}
	if series[0].Bucket != "2026-09-14T09:00" {
		t.Fatalf("expected bucket 2026-09-14T09:00, got %s", series[0].Bucket)
	}

	all, err := st.RateLimitSeries(ctx, p.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 rows total, got %d", len(all))
	}
}

func TestPruneRateLimitSamples(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	p := &store.Provider{
		Name: "prune-provider", WireFormat: "openai", BaseURL: "https://example.com",
		VerifyTLS: true, TimeoutConnect: 5, TimeoutRead: 30, Enabled: true,
	}
	if err := st.CreateProvider(ctx, p, nil, nil); err != nil {
		t.Fatal(err)
	}

	for _, b := range []string{"2026-09-13T10:00", "2026-09-14T10:00", "2026-09-15T10:00"} {
		s := &store.RateLimitSample{
			ProviderID: p.ID, Bucket: b, ObservedAt: b + ":00.000000Z", Observations: 1,
		}
		if err := st.InsertRateLimitSample(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	pruned, err := st.PruneRateLimitSamples(ctx, "2026-09-14T10:00")
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("expected 1 pruned, got %d", pruned)
	}

	remaining, err := st.RateLimitSeries(ctx, p.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 {
		t.Fatalf("expected 2 remaining, got %d", len(remaining))
	}
}

func TestProviderRateLimitHeadersRoundtrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	headers := "limit_requests:x-ratelimit-limit-requests,remaining_requests:x-ratelimit-remaining-requests"
	p := &store.Provider{
		Name: "rl-provider", WireFormat: "openai", BaseURL: "https://example.com",
		VerifyTLS: true, TimeoutConnect: 5, TimeoutRead: 30, Enabled: true,
		RateLimitHeaders: headers,
	}
	if err := st.CreateProvider(ctx, p, nil, nil); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetProviderByName(ctx, "rl-provider")
	if err != nil {
		t.Fatal(err)
	}
	if got.RateLimitHeaders != headers {
		t.Fatalf("expected rate_limit_headers %q, got %q", headers, got.RateLimitHeaders)
	}

	got.RateLimitHeaders = "limit_tokens:x-ratelimit-limit-tokens"
	if err := st.UpdateProvider(ctx, got, nil); err != nil {
		t.Fatal(err)
	}
	updated, err := st.GetProviderByName(ctx, "rl-provider")
	if err != nil {
		t.Fatal(err)
	}
	if updated.RateLimitHeaders != "limit_tokens:x-ratelimit-limit-tokens" {
		t.Fatalf("expected updated headers, got %q", updated.RateLimitHeaders)
	}
}

func TestDeleteProviderCascadesRateLimitSamples(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	p := &store.Provider{
		Name: "del-provider", WireFormat: "openai", BaseURL: "https://example.com",
		VerifyTLS: true, TimeoutConnect: 5, TimeoutRead: 30, Enabled: true,
	}
	if err := st.CreateProvider(ctx, p, nil, nil); err != nil {
		t.Fatal(err)
	}

	s := &store.RateLimitSample{
		ProviderID: p.ID, Bucket: "2026-09-14T10:00", ObservedAt: "2026-09-14T10:00:00.000000Z", Observations: 1,
	}
	if err := st.InsertRateLimitSample(ctx, s); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteProvider(ctx, p.ID, nil); err != nil {
		t.Fatal(err)
	}

	remaining, err := st.LatestRateLimitSamples(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected 0 samples after provider delete, got %d", len(remaining))
	}
}
