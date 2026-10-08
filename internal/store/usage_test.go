package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestUsageEventTTFTRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	if err := st.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	unary := &UsageEvent{TS: "2026-01-01T00:00:00Z", Alias: "m", Outcome: "ok", DurationMs: 50}
	streamed := &UsageEvent{TS: "2026-01-01T00:00:01Z", Alias: "m", Outcome: "ok", Streamed: true,
		DurationMs: 900, TTFTMs: sql.NullInt64{Int64: 120, Valid: true}}
	for _, ev := range []*UsageEvent{unary, streamed} {
		if err := st.InsertUsageEvent(ctx, ev, nil); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	events, err := st.ListUsageEvents(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].TTFTMs.Valid {
		t.Fatalf("unary ttft = %v, want null", events[0].TTFTMs)
	}
	if !events[1].TTFTMs.Valid || events[1].TTFTMs.Int64 != 120 {
		t.Fatalf("streamed ttft = %v, want 120", events[1].TTFTMs)
	}
	rows, err := st.ListRequests(ctx, UsageFilter{}, 10, 0)
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	if !rows[0].TTFTMs.Valid || rows[0].TTFTMs.Int64 != 120 || rows[1].TTFTMs.Valid {
		t.Fatalf("request log ttft = %v, %v", rows[0].TTFTMs, rows[1].TTFTMs)
	}
}
