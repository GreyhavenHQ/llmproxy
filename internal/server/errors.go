package server

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/apierr"
	"github.com/greyhavenhq/llmproxy/internal/store"
)

// Error analytics: classify what failed at capture time, aggregate it for the
// errors dashboard. Only classification tokens are ever kept; provider error
// messages can echo request content, so they never leave the response path.

// errorKindPattern is the accepted shape of an upstream error code: a short
// lowercase identifier. Free text never matches, so a message-shaped value is
// dropped whole rather than partially preserved.
var errorKindPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,63}$`)

// errorKindToken normalises a candidate token: lowercased, then accepted only
// if it looks like an identifier. Anything else becomes "".
func errorKindToken(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if !errorKindPattern.MatchString(v) {
		return ""
	}
	return v
}

// errorKindFromBody pulls a classification token out of an upstream error
// body. Both wire formats nest it the same way: OpenAI-compatible errors
// carry {"error":{"type":..., "code":...}}, Anthropic {"error":{"type":...}}.
// The more specific code wins over the type; numeric codes stringify. The
// message field is deliberately never read.
func errorKindFromBody(body []byte) string {
	var doc struct {
		Error struct {
			Type string          `json:"type"`
			Code json.RawMessage `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return ""
	}
	var code string
	if len(doc.Error.Code) > 0 {
		if json.Unmarshal(doc.Error.Code, &code) != nil {
			var n json.Number
			if json.Unmarshal(doc.Error.Code, &n) == nil {
				code = n.String()
			}
		}
	}
	if kind := errorKindToken(code); kind != "" {
		return kind
	}
	return errorKindToken(doc.Error.Type)
}

// outcomeFilters are the accepted values of the stats endpoints' outcome
// parameter: the stored outcomes plus "failed", which matches every non-ok
// outcome.
var outcomeFilters = map[string]bool{
	"ok": true, "upstream_error": true, "unreachable": true, "cancelled": true, "failed": true,
}

// handleStatsErrors answers the errors dashboard in one call: a gap-filled
// outcome series and the full-dimensional error breakdown, both under the
// shared stats filters. Team-visible like the rest of /stats.
func (s *Server) handleStatsErrors(w http.ResponseWriter, r *http.Request, auth *Auth) {
	filter, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	granularity := r.URL.Query().Get("bucket")
	if granularity == "" {
		granularity = "day"
	}
	if !bucketGranularities[granularity] {
		writeProxyError(w, apierr.New(400, "invalid_bucket", "bucket must be hour, day, week or month"))
		return
	}

	rows, err := s.store.ErrorSeries(r.Context(), filter, granularity == "hour")
	if err != nil {
		internalErr(w, "failed to summarise errors")
		return
	}
	// Roll the store's hour/day rows up into the requested granularity and
	// gap-fill, the same shape handleUsageSeries emits.
	byStart := make(map[time.Time]*store.ErrorSeriesRow, len(rows))
	var earliest time.Time
	for _, row := range rows {
		at, ok := parseBucketKey(row.Bucket)
		if !ok {
			continue
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
		start := truncateBucket(at, granularity)
		bucket, ok := byStart[start]
		if !ok {
			bucket = &store.ErrorSeriesRow{}
			byStart[start] = bucket
		}
		bucket.Requests += row.Requests
		bucket.OK += row.OK
		bucket.UpstreamError += row.UpstreamError
		bucket.Unreachable += row.Unreachable
		bucket.Cancelled += row.Cancelled
	}
	first, last, perr := seriesBounds(filter.Since, filter.Until, earliest, granularity)
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	series := make([]map[string]any, 0, len(byStart))
	for at := first; !at.After(last); at = nextBucket(at, granularity) {
		if len(series) >= maxBuckets {
			writeProxyError(w, apierr.Newf(400, "range_too_large",
				"a %s series over this range needs more than %d buckets; narrow it or use a coarser bucket",
				granularity, maxBuckets))
			return
		}
		view := map[string]any{
			"start": at.Format(time.RFC3339), "requests": int64(0), "ok": int64(0),
			"upstream_error": int64(0), "unreachable": int64(0), "cancelled": int64(0),
		}
		if b, ok := byStart[at]; ok {
			view["requests"] = b.Requests
			view["ok"] = b.OK
			view["upstream_error"] = b.UpstreamError
			view["unreachable"] = b.Unreachable
			view["cancelled"] = b.Cancelled
		}
		series = append(series, view)
	}

	breakdown, err := s.store.ErrorBreakdown(r.Context(), filter)
	if err != nil {
		internalErr(w, "failed to break down errors")
		return
	}
	cells := make([]map[string]any, 0, len(breakdown))
	for _, row := range breakdown {
		cell := map[string]any{
			"provider":    row.Provider,
			"model":       row.Alias,
			"endpoint":    row.Endpoint,
			"client":      row.Client,
			"tags":        row.Tags,
			"outcome":     row.Outcome,
			"error_kind":  row.ErrorKind,
			"status_code": nil,
			"requests":    row.Requests,
			"avg_ms":      row.AvgMs,
			"last_seen":   row.LastSeen,
			"bands":       row.Bands[:],
		}
		if row.StatusCode.Valid {
			cell["status_code"] = row.StatusCode.Int64
		}
		cells = append(cells, cell)
	}
	writeJSON(w, 200, map[string]any{
		"bucket": granularity, "series": series, "breakdown": cells,
	})
}
