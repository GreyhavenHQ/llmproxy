package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
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

// Tag bounds. Eight pairs is more dimensions than a dashboard can show, and
// 256 bytes keeps the column a metadata field like client.
const (
	tagsHeader   = "x-llmproxy-tags"
	tagsMaxPairs = 8
	tagsMaxLen   = 256
	// tagsRawMaxLen bounds how much header the parser looks at. The output
	// can never exceed tagsMaxLen, so anything past a few KB only buys the
	// sender CPU time on the request goroutine.
	tagsRawMaxLen = 4096
)

// tagPair is the accepted shape of one "key:value" pair: lowercase
// alphanumerics, dots, dashes and underscores, starting alphanumeric.
var tagPair = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*:[a-z0-9][a-z0-9._-]*$`)

// tagKey is the part before the colon. Pairs are validated before this runs,
// so the separator is always there.
func tagKey(pair string) string {
	key, _, _ := strings.Cut(pair, ":")
	return key
}

// tagsFrom is the caller's x-llmproxy-tags header, normalised into a
// canonical "k:v,k:v" string so SQL grouping is stable: pairs trimmed and
// lowercased, malformed pairs dropped, one value per key (first wins),
// sorted by key, then capped in count and bytes. Sorting before the caps is
// what makes the result independent of the order the caller sent. Telemetry
// never fails a request, so nothing here reports an error. Header metadata
// only, never content.
func tagsFrom(r *http.Request) string {
	raw := r.Header.Get(tagsHeader)
	if raw == "" {
		return ""
	}
	if len(raw) > tagsRawMaxLen {
		raw = raw[:tagsRawMaxLen]
	}
	seen := make(map[string]bool, tagsMaxPairs)
	var pairs []string
	for _, part := range strings.Split(raw, ",") {
		pair := strings.ToLower(strings.TrimSpace(part))
		if !tagPair.MatchString(pair) {
			continue
		}
		key := tagKey(pair)
		if seen[key] {
			continue
		}
		seen[key] = true
		pairs = append(pairs, pair)
	}
	// By key, not by whole pair: ':' sorts after digits, so "app:z" and
	// "app2:a" would otherwise come back in the surprising order. Keys are
	// unique by now, so the comparison is total.
	sort.Slice(pairs, func(i, j int) bool { return tagKey(pairs[i]) < tagKey(pairs[j]) })
	if len(pairs) > tagsMaxPairs {
		pairs = pairs[:tagsMaxPairs]
	}
	// Drop whole pairs rather than truncate one into a different value; a
	// pair too long to fit does not stop the shorter ones behind it.
	out := ""
	for _, pair := range pairs {
		next := pair
		if out != "" {
			next = out + "," + pair
		}
		if len(next) > tagsMaxLen {
			continue
		}
		out = next
	}
	// The regexp already excludes non-ASCII, so this is belt and braces
	// against a future looser pattern; Postgres TEXT rejects broken UTF-8.
	return strings.ToValidUTF8(out, "")
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
		Tags:         auth.Tags,
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
