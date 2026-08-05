package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/monadical/llmproxy/internal/catalog"
	"github.com/monadical/llmproxy/internal/store"
)

// clientMaxLen caps the stored User-Agent: enough to identify any real
// client, short enough that the column stays a metadata field.
const clientMaxLen = 256

// clientFrom is the caller's User-Agent header, truncated and forced to valid
// UTF-8 (Postgres TEXT rejects broken sequences). Stored verbatim; grouping
// by product token happens at read time.
func clientFrom(r *http.Request) string {
	ua := strings.TrimSpace(r.UserAgent())
	if len(ua) > clientMaxLen {
		ua = ua[:clientMaxLen]
	}
	return strings.ToValidUTF8(ua, "")
}

// usageOutcome carries everything the accounting path needs: identifiers,
// numbers and flags only. Structurally incapable of holding content.
type usageOutcome struct {
	StatusCode int
	Outcome    string // ok | upstream_error | unreachable | cancelled
	Cancelled  bool
	Streamed   bool
	Usage      map[string]any
	DurationMs int64
}

// extractUsage pulls the usage object out of a unary JSON response.
func extractUsage(body []byte) map[string]any {
	var doc struct {
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil
	}
	return doc.Usage
}

var dataPrefix = []byte("data:")
var doneMarker = []byte("[DONE]")
var usageMarker = []byte(`"usage"`)

// parseSSEUsage extracts a usage object from one SSE line, if present. The
// cheap marker check avoids JSON-parsing content chunks.
func parseSSEUsage(line []byte) map[string]any {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, dataPrefix) {
		return nil
	}
	data := bytes.TrimSpace(line[len(dataPrefix):])
	if bytes.Equal(data, doneMarker) || !bytes.Contains(data, usageMarker) {
		return nil
	}
	var doc struct {
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	return doc.Usage
}

// mergeUsage merges max-wins: some upstreams emit usage before the terminal chunk.
func mergeUsage(old, new map[string]any) map[string]any {
	if new == nil {
		return old
	}
	if old == nil {
		return new
	}
	merged := make(map[string]any, len(old)+len(new))
	for k, v := range old {
		merged[k] = v
	}
	for k, v := range new {
		newNum, newOK := v.(float64)
		oldNum, oldOK := merged[k].(float64)
		if newOK && oldOK {
			merged[k] = max(oldNum, newNum)
		} else {
			merged[k] = v
		}
	}
	return merged
}

type unitQuantity struct {
	Unit     string
	Quantity float64
}

func quantitiesFromUsage(usage map[string]any) []unitQuantity {
	if usage == nil {
		return nil
	}
	var out []unitQuantity
	if v, ok := usage["prompt_tokens"].(float64); ok {
		out = append(out, unitQuantity{"input_tokens", v})
	}
	if v, ok := usage["completion_tokens"].(float64); ok {
		out = append(out, unitQuantity{"output_tokens", v})
	}
	if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		if v, ok := details["cached_tokens"].(float64); ok && v > 0 {
			out = append(out, unitQuantity{"cached_input_tokens", v})
		}
	}
	return out
}

// anthropicQuantities maps the Anthropic Messages usage object to units. The
// two cache figures are recorded only when non-zero: Anthropic reports them
// on every response, and an all-zero row per request is noise.
func anthropicQuantities(usage map[string]any) []unitQuantity {
	if usage == nil {
		return nil
	}
	var out []unitQuantity
	if v, ok := usage["input_tokens"].(float64); ok {
		out = append(out, unitQuantity{"input_tokens", v})
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		out = append(out, unitQuantity{"output_tokens", v})
	}
	if v, ok := usage["cache_read_input_tokens"].(float64); ok && v > 0 {
		out = append(out, unitQuantity{"cached_input_tokens", v})
	}
	if v, ok := usage["cache_creation_input_tokens"].(float64); ok && v > 0 {
		out = append(out, unitQuantity{"cache_creation_tokens", v})
	}
	return out
}

// priceAndInsert prices the quantities against the model names (in order of
// specificity), fills the event's cost fields, emits metrics and persists.
// The event arrives fully populated except for Cost/Unpriced.
func (s *Server) priceAndInsert(ctx context.Context, ev *store.UsageEvent, providerName string,
	quantities []unitQuantity, models ...string) {
	idx := s.pricing.Load()
	var rows []store.UsageQuantity
	cost := 0.0
	pricedAny, unpricedAny := false, false
	for _, uq := range quantities {
		price, priced := idx.Lookup(uq.Unit, models...)
		row := store.UsageQuantity{
			Unit:        uq.Unit,
			Quantity:    uq.Quantity,
			Priced:      priced,
			Measurement: "upstream_reported",
		}
		if priced {
			row.UnitPrice = sql.NullFloat64{Float64: price, Valid: true}
			cost += price * uq.Quantity
			pricedAny = true
		} else {
			unpricedAny = true
		}
		rows = append(rows, row)
		s.metrics.ObserveUnits(providerName, ev.Alias, uq.Unit, uq.Quantity, priced)
	}
	ev.Unpriced = unpricedAny
	if pricedAny {
		ev.Cost = sql.NullFloat64{Float64: cost, Valid: true}
	}
	s.metrics.ObserveRequest(ev.Endpoint, providerName, ev.Alias, ev.Outcome, ev.DurationMs)
	if err := s.store.InsertUsageEvent(ctx, ev, rows); err != nil {
		// Never let accounting failures break or block the data plane.
		slog.Error("failed to record usage event", "error", err)
	}
}

func (s *Server) recordUsage(ctx context.Context, auth *Auth, route *catalog.Route, endpoint string, rec usageOutcome) {
	ev := &store.UsageEvent{
		PrincipalID:  auth.PrincipalID,
		APIKeyID:     auth.KeyID,
		ProviderID:   route.ProviderID,
		Alias:        route.Alias,
		UpstreamName: route.UpstreamName,
		Endpoint:     endpoint,
		Client:       auth.Client,
		Outcome:      rec.Outcome,
		Cancelled:    rec.Cancelled,
		Streamed:     rec.Streamed,
		DurationMs:   rec.DurationMs,
	}
	if rec.StatusCode != 0 {
		ev.StatusCode = sql.NullInt64{Int64: int64(rec.StatusCode), Valid: true}
	}
	s.priceAndInsert(ctx, ev, route.ProviderName, quantitiesFromUsage(rec.Usage),
		route.Alias, route.TargetAlias, route.UpstreamName)
}

// recordAsync runs an accounting function off the request path, detached from
// the request context so client cancellation cannot lose the record.
func (s *Server) recordAsync(record func(context.Context)) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		record(ctx)
	}()
}

func (s *Server) recordUsageAsync(auth *Auth, route *catalog.Route, endpoint string, rec usageOutcome) {
	s.recordAsync(func(ctx context.Context) { s.recordUsage(ctx, auth, route, endpoint, rec) })
}
