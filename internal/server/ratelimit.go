package server

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/catalog"
	"github.com/greyhavenhq/llmproxy/internal/metrics"
	"github.com/greyhavenhq/llmproxy/internal/store"
)

// RateLimitReading holds the parsed values from one upstream response.
type RateLimitReading struct {
	ProviderID   string
	ProviderName string

	LimitRequests     *int64
	RemainingRequests *int64
	LimitTokens       *int64
	RemainingTokens   *int64
	ResetRequestsAt   string
	ResetTokensAt     string
}

// readRateLimitHeaders extracts rate-limit values from upstream response
// headers using the provider's configured header mapping. Returns nil when
// the provider has no mapping or the response carries none of the headers.
func readRateLimitHeaders(headers http.Header, mapping map[string]string) *RateLimitReading {
	if len(mapping) == 0 {
		return nil
	}
	r := &RateLimitReading{}
	any := false
	if h, ok := mapping["limit_requests"]; ok {
		if v, err := strconv.ParseInt(headers.Get(h), 10, 64); err == nil {
			r.LimitRequests = &v
			any = true
		}
	}
	if h, ok := mapping["remaining_requests"]; ok {
		if v, err := strconv.ParseInt(headers.Get(h), 10, 64); err == nil {
			r.RemainingRequests = &v
			any = true
		}
	}
	if h, ok := mapping["limit_tokens"]; ok {
		if v, err := strconv.ParseInt(headers.Get(h), 10, 64); err == nil {
			r.LimitTokens = &v
			any = true
		}
	}
	if h, ok := mapping["remaining_tokens"]; ok {
		if v, err := strconv.ParseInt(headers.Get(h), 10, 64); err == nil {
			r.RemainingTokens = &v
			any = true
		}
	}
	if h, ok := mapping["reset_requests"]; ok {
		if raw := headers.Get(h); raw != "" {
			if parsed := parseResetTime(raw); parsed != "" {
				r.ResetRequestsAt = parsed
				any = true
			}
		}
	}
	if h, ok := mapping["reset_tokens"]; ok {
		if raw := headers.Get(h); raw != "" {
			if parsed := parseResetTime(raw); parsed != "" {
				r.ResetTokensAt = parsed
				any = true
			}
		}
	}
	if !any {
		return nil
	}
	return r
}

// parseResetTime accepts integer seconds, Go-style duration, or RFC 3339.
// Returns an absolute UTC RFC 3339 timestamp, or "" on failure.
func parseResetTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return time.Now().Add(d).UTC().Format(time.RFC3339)
	}
	if secs, err := strconv.ParseFloat(raw, 64); err == nil {
		return time.Now().Add(time.Duration(secs * float64(time.Second))).UTC().Format(time.RFC3339)
	}
	return ""
}

// setRateLimitResponseHeaders writes x-llmproxy-ratelimit-* headers on the
// response for every observed value.
func setRateLimitResponseHeaders(w http.ResponseWriter, r *RateLimitReading) {
	if r == nil {
		return
	}
	if r.LimitRequests != nil {
		w.Header().Set("x-llmproxy-ratelimit-requests-limit", strconv.FormatInt(*r.LimitRequests, 10))
	}
	if r.RemainingRequests != nil {
		w.Header().Set("x-llmproxy-ratelimit-requests-remaining", strconv.FormatInt(*r.RemainingRequests, 10))
	}
	if r.LimitTokens != nil {
		w.Header().Set("x-llmproxy-ratelimit-tokens-limit", strconv.FormatInt(*r.LimitTokens, 10))
	}
	if r.RemainingTokens != nil {
		w.Header().Set("x-llmproxy-ratelimit-tokens-remaining", strconv.FormatInt(*r.RemainingTokens, 10))
	}
}

// rateLimitCell is the in-memory accumulator for one provider's current minute.
type rateLimitCell struct {
	bucket       string
	observedAt   time.Time
	observations int64

	limitRequests     *int64
	remainingRequests *int64
	minRemRequests    *int64
	limitTokens       *int64
	remainingTokens   *int64
	minRemTokens      *int64
	resetRequestsAt   string
	resetTokensAt     string
}

func (c *rateLimitCell) observe(r *RateLimitReading, now time.Time) {
	c.observedAt = now
	c.observations++
	if r.LimitRequests != nil {
		c.limitRequests = r.LimitRequests
	}
	if r.RemainingRequests != nil {
		c.remainingRequests = r.RemainingRequests
		if c.minRemRequests == nil || *r.RemainingRequests < *c.minRemRequests {
			v := *r.RemainingRequests
			c.minRemRequests = &v
		}
	}
	if r.LimitTokens != nil {
		c.limitTokens = r.LimitTokens
	}
	if r.RemainingTokens != nil {
		c.remainingTokens = r.RemainingTokens
		if c.minRemTokens == nil || *r.RemainingTokens < *c.minRemTokens {
			v := *r.RemainingTokens
			c.minRemTokens = &v
		}
	}
	if r.ResetRequestsAt != "" {
		c.resetRequestsAt = r.ResetRequestsAt
	}
	if r.ResetTokensAt != "" {
		c.resetTokensAt = r.ResetTokensAt
	}
}

func (c *rateLimitCell) toSample(providerID string) *store.RateLimitSample {
	s := &store.RateLimitSample{
		ProviderID:   providerID,
		Bucket:       c.bucket,
		ObservedAt:   c.observedAt.UTC().Format(store.TimeFormat),
		Observations: c.observations,
	}
	if c.limitRequests != nil {
		s.LimitRequests = sql.NullInt64{Int64: *c.limitRequests, Valid: true}
	}
	if c.remainingRequests != nil {
		s.RemainingRequests = sql.NullInt64{Int64: *c.remainingRequests, Valid: true}
	}
	if c.minRemRequests != nil {
		s.MinRemainingRequests = sql.NullInt64{Int64: *c.minRemRequests, Valid: true}
	}
	if c.limitTokens != nil {
		s.LimitTokens = sql.NullInt64{Int64: *c.limitTokens, Valid: true}
	}
	if c.remainingTokens != nil {
		s.RemainingTokens = sql.NullInt64{Int64: *c.remainingTokens, Valid: true}
	}
	if c.minRemTokens != nil {
		s.MinRemainingTokens = sql.NullInt64{Int64: *c.minRemTokens, Valid: true}
	}
	if c.resetRequestsAt != "" {
		s.ResetRequestsAt = sql.NullString{String: c.resetRequestsAt, Valid: true}
	}
	if c.resetTokensAt != "" {
		s.ResetTokensAt = sql.NullString{String: c.resetTokensAt, Valid: true}
	}
	return s
}

// RateLimitSnapshot is the current reading for one provider, served by the
// stats endpoint.
type RateLimitSnapshot struct {
	ProviderID   string
	ProviderName string
	ObservedAt   time.Time
	Observations int64

	LimitRequests     *int64
	RemainingRequests *int64
	MinRemRequests    *int64
	LimitTokens       *int64
	RemainingTokens   *int64
	MinRemTokens      *int64
	ResetRequestsAt   string
	ResetTokensAt     string
}

// RateLimitTracker accumulates per-provider readings in memory and flushes
// finished minute buckets to the store.
type RateLimitTracker struct {
	mu    sync.Mutex
	cells map[string]*rateLimitCell // keyed by provider ID
	names map[string]string         // provider ID -> name
}

func NewRateLimitTracker() *RateLimitTracker {
	return &RateLimitTracker{
		cells: make(map[string]*rateLimitCell),
		names: make(map[string]string),
	}
}

func minuteBucket(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04")
}

// Observe records one upstream reading. If the minute rolled over, the
// finished bucket is returned for persistence; otherwise nil.
func (t *RateLimitTracker) Observe(r *RateLimitReading, now time.Time) *store.RateLimitSample {
	t.mu.Lock()
	defer t.mu.Unlock()

	bucket := minuteBucket(now)
	t.names[r.ProviderID] = r.ProviderName

	cell, ok := t.cells[r.ProviderID]
	if !ok {
		cell = &rateLimitCell{bucket: bucket}
		t.cells[r.ProviderID] = cell
		cell.observe(r, now)
		return nil
	}

	if cell.bucket == bucket {
		cell.observe(r, now)
		return nil
	}

	// Minute rolled over: snapshot the finished bucket, start a new one.
	finished := cell.toSample(r.ProviderID)
	newCell := &rateLimitCell{bucket: bucket}
	newCell.observe(r, now)
	t.cells[r.ProviderID] = newCell
	return finished
}

// Snapshots returns the current in-memory reading for every tracked provider.
func (t *RateLimitTracker) Snapshots() []RateLimitSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]RateLimitSnapshot, 0, len(t.cells))
	for providerID, cell := range t.cells {
		out = append(out, RateLimitSnapshot{
			ProviderID:        providerID,
			ProviderName:      t.names[providerID],
			ObservedAt:        cell.observedAt,
			Observations:      cell.observations,
			LimitRequests:     cell.limitRequests,
			RemainingRequests: cell.remainingRequests,
			MinRemRequests:    cell.minRemRequests,
			LimitTokens:       cell.limitTokens,
			RemainingTokens:   cell.remainingTokens,
			MinRemTokens:      cell.minRemTokens,
			ResetRequestsAt:   cell.resetRequestsAt,
			ResetTokensAt:     cell.resetTokensAt,
		})
	}
	return out
}

// Drain flushes every open bucket and returns the samples for persistence.
func (t *RateLimitTracker) Drain() []*store.RateLimitSample {
	t.mu.Lock()
	defer t.mu.Unlock()

	var out []*store.RateLimitSample
	for providerID, cell := range t.cells {
		out = append(out, cell.toSample(providerID))
	}
	t.cells = make(map[string]*rateLimitCell)
	return out
}

// observeRateLimit is the single call site that reads upstream rate-limit
// headers, updates the in-memory tracker, sets response headers, updates
// Prometheus gauges, and flushes finished buckets to the store.
// observeRateLimit records the upstream's rate-limit telemetry and returns the reading, or nil.
func (s *Server) observeRateLimit(resp *http.Response, route *catalog.Route) *RateLimitReading {
	reading := readRateLimitHeaders(resp.Header, route.RateLimitHeaders)
	if reading == nil {
		return nil
	}
	reading.ProviderID = route.ProviderID
	reading.ProviderName = route.ProviderName

	setRateLimitGauges(s.metrics, route.ProviderName, reading)

	finished := s.rateLimits.Observe(reading, time.Now())
	if finished != nil {
		s.recordAsync(func(ctx context.Context) {
			if err := s.store.InsertRateLimitSample(ctx, finished); err != nil {
				slog.Error("failed to persist rate-limit sample", "provider", finished.ProviderID, "error", err)
			}
		})
	}
	return reading
}

func (s *Server) flushRateLimitSample(sample *store.RateLimitSample) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.InsertRateLimitSample(ctx, sample); err != nil {
		slog.Error("failed to persist rate-limit sample", "provider", sample.ProviderID, "error", err)
	}
}

func setRateLimitGauges(m *metrics.Metrics, provider string, r *RateLimitReading) {
	if r.RemainingRequests != nil {
		m.Set("llmproxy_provider_rate_limit_remaining",
			[][2]string{{"provider", provider}, {"unit", "requests"}},
			float64(*r.RemainingRequests))
	}
	if r.LimitRequests != nil {
		m.Set("llmproxy_provider_rate_limit_limit",
			[][2]string{{"provider", provider}, {"unit", "requests"}},
			float64(*r.LimitRequests))
	}
	if r.RemainingTokens != nil {
		m.Set("llmproxy_provider_rate_limit_remaining",
			[][2]string{{"provider", provider}, {"unit", "tokens"}},
			float64(*r.RemainingTokens))
	}
	if r.LimitTokens != nil {
		m.Set("llmproxy_provider_rate_limit_limit",
			[][2]string{{"provider", provider}, {"unit", "tokens"}},
			float64(*r.LimitTokens))
	}
}
