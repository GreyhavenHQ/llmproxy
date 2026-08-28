package server

import (
	"net/http"
	"sort"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/apierr"
	"github.com/greyhavenhq/llmproxy/internal/secrets"
	"github.com/greyhavenhq/llmproxy/internal/store"
)

func keyView(k *store.APIKey) map[string]any {
	view := map[string]any{
		"id":           k.ID,
		"key_suffix":   k.Suffix,
		"label":        k.Label,
		"created_at":   k.CreatedAt,
		"last_used_at": nil,
	}
	if k.LastUsedAt.Valid {
		view["last_used_at"] = k.LastUsedAt.String
	}
	return view
}

func (s *Server) handleMyKeyCreate(w http.ResponseWriter, r *http.Request, auth *Auth) {
	var body struct {
		Label string `json:"label"`
	}
	if perr := readJSONBody(r, 1<<20, &body); perr != nil {
		writeProxyError(w, perr)
		return
	}
	if len(body.Label) > 120 {
		writeProxyError(w, apierr.New(400, "invalid_label", "label must be at most 120 characters"))
		return
	}
	plaintext := secrets.GenerateAPIKey()
	key, err := s.store.CreateAPIKey(r.Context(), auth.PrincipalID,
		secrets.HashAPIKey(s.secret, plaintext), secrets.KeySuffix(plaintext), body.Label, nil)
	if err != nil {
		writeProxyError(w, apierr.New(500, "internal_error", "failed to create key"))
		return
	}
	view := keyView(key)
	view["key"] = plaintext // shown exactly once; only the keyed hash is stored
	writeJSON(w, 201, view)
}

func (s *Server) handleMyKeysList(w http.ResponseWriter, r *http.Request, auth *Auth) {
	keys, err := s.store.ListAPIKeys(r.Context(), auth.PrincipalID, "", 500, 0)
	if err != nil {
		writeProxyError(w, apierr.New(500, "internal_error", "failed to list keys"))
		return
	}
	views := make([]map[string]any, 0, len(keys))
	for i := range keys {
		views = append(views, keyView(&keys[i]))
	}
	writeJSON(w, 200, map[string]any{"keys": views})
}

func (s *Server) handleMyKeyRevoke(w http.ResponseWriter, r *http.Request, auth *Auth) {
	key, err := s.store.GetAPIKey(r.Context(), r.PathValue("id"), auth.PrincipalID)
	if err != nil {
		writeProxyError(w, apierr.New(500, "internal_error", "failed to load key"))
		return
	}
	if key == nil {
		writeProxyError(w, apierr.New(404, "key_not_found", "no such key on your principal"))
		return
	}
	if err := s.store.DeleteAPIKey(r.Context(), key.ID, nil); err != nil {
		writeProxyError(w, apierr.New(500, "internal_error", "failed to delete key"))
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": key.ID})
}

// parseTimeParam accepts common ISO 8601 forms and converts to the store's
// timestamp format. Empty input stays empty (filter disabled).
func parseTimeParam(value, name string) (string, *apierr.ProxyError) {
	if value == "" {
		return "", nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC().Format("2006-01-02T15:04:05.000000Z"), nil
		}
	}
	return "", apierr.Newf(400, "invalid_date", "'%s' must be an ISO 8601 datetime", name)
}

func summaryViews(rows []store.UsageSummaryRow, names map[string]string) []map[string]any {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Alias != rows[j].Alias {
			return rows[i].Alias < rows[j].Alias
		}
		return rows[i].Endpoint < rows[j].Endpoint
	})
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		view := map[string]any{
			"model":     r.Alias,
			"endpoint":  r.Endpoint,
			"requests":  r.Requests,
			"cancelled": r.Cancelled,
			"cost":      nil,
			"units":     r.Units,
		}
		if r.Cost.Valid {
			view["cost"] = r.Cost.Float64
		}
		if names != nil {
			name, ok := names[r.PrincipalID]
			if !ok {
				name = "unknown"
			}
			view["principal"] = name
		}
		out = append(out, view)
	}
	return out
}

// ---------- usage time series ----------

// storeTimeLayout is the store's canonical timestamp format; parseTimeParam
// emits it and usage_event.ts is written in it.
const storeTimeLayout = "2006-01-02T15:04:05.000000Z"

var bucketGranularities = map[string]bool{"hour": true, "day": true, "week": true, "month": true}

// maxBuckets bounds a series so a wide range at a fine granularity cannot ask
// the browser to draw tens of thousands of bars.
const maxBuckets = 1000

// truncateBucket snaps t down to the start of its bucket. Weeks start Monday.
func truncateBucket(t time.Time, granularity string) time.Time {
	t = t.UTC()
	switch granularity {
	case "hour":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
	case "week":
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		offset := (int(day.Weekday()) + 6) % 7 // Monday = 0
		return day.AddDate(0, 0, -offset)
	case "month":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
}

func nextBucket(t time.Time, granularity string) time.Time {
	switch granularity {
	case "hour":
		return t.Add(time.Hour)
	case "week":
		return t.AddDate(0, 0, 7)
	case "month":
		return t.AddDate(0, 1, 0)
	default:
		return t.AddDate(0, 0, 1)
	}
}

// parseBucketKey turns a store bucket key back into its UTC start time.
func parseBucketKey(key string) (time.Time, bool) {
	layout := "2006-01-02"
	if len(key) > 10 {
		layout = "2006-01-02T15"
	}
	t, err := time.Parse(layout, key)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

type seriesBucket struct {
	Start     time.Time
	Requests  int64
	OK        int64
	Cancelled int64
	Failed    int64
	Unpriced  int64
	Cost      float64
	Priced    bool
	Units     map[string]float64
}

// handleUsageSeries answers every series endpoint (self-service, admin and
// the shared stats view): usage aggregated into equal time buckets,
// gap-filled so every bucket in the range is present (empty ones included)
// and the client can plot it directly. The caller fixes the identity scope in
// the filter; since/until come from the query here.
func (s *Server) handleUsageSeries(w http.ResponseWriter, r *http.Request, filter store.UsageFilter) {
	granularity := r.URL.Query().Get("bucket")
	if granularity == "" {
		granularity = "day"
	}
	if !bucketGranularities[granularity] {
		writeProxyError(w, apierr.New(400, "invalid_bucket", "bucket must be hour, day, week or month"))
		return
	}
	since, perr := parseTimeParam(r.URL.Query().Get("since"), "since")
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	until, perr := parseTimeParam(r.URL.Query().Get("until"), "until")
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	filter.Since, filter.Until = since, until
	rows, err := s.store.UsageSeries(r.Context(), filter, granularity == "hour")
	if err != nil {
		internalErr(w, "failed to summarise usage")
		return
	}

	// Roll the store's hour/day rows up into the requested granularity.
	byStart := make(map[time.Time]*seriesBucket, len(rows))
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
			bucket = &seriesBucket{Start: start, Units: make(map[string]float64)}
			byStart[start] = bucket
		}
		bucket.Requests += row.Requests
		bucket.OK += row.OK
		bucket.Cancelled += row.Cancelled
		bucket.Failed += row.Failed
		bucket.Unpriced += row.Unpriced
		if row.Cost.Valid {
			bucket.Cost += row.Cost.Float64
			bucket.Priced = true
		}
		for unit, quantity := range row.Units {
			bucket.Units[unit] += quantity
		}
	}

	first, last, perr := seriesBounds(since, until, earliest, granularity)
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	views := make([]map[string]any, 0, len(byStart))
	for at := first; !at.After(last); at = nextBucket(at, granularity) {
		if len(views) >= maxBuckets {
			writeProxyError(w, apierr.Newf(400, "range_too_large",
				"a %s series over this range needs more than %d buckets; narrow it or use a coarser bucket",
				granularity, maxBuckets))
			return
		}
		view := map[string]any{
			"start":             at.Format(time.RFC3339),
			"requests":          int64(0),
			"ok":                int64(0),
			"cancelled":         int64(0),
			"failed":            int64(0),
			"unpriced_requests": int64(0),
			"cost":              nil,
			"units":             map[string]float64{},
		}
		if bucket, ok := byStart[at]; ok {
			view["requests"] = bucket.Requests
			view["ok"] = bucket.OK
			view["cancelled"] = bucket.Cancelled
			view["failed"] = bucket.Failed
			view["unpriced_requests"] = bucket.Unpriced
			view["units"] = bucket.Units
			if bucket.Priced {
				view["cost"] = bucket.Cost
			}
		}
		views = append(views, view)
	}
	writeJSON(w, 200, map[string]any{"bucket": granularity, "series": views})
}

// seriesBounds picks the first and last bucket to emit: the requested window
// when given, otherwise from the earliest recorded event to now.
func seriesBounds(since, until string, earliest time.Time, granularity string) (time.Time, time.Time, *apierr.ProxyError) {
	first := earliest
	if since != "" {
		at, err := time.Parse(storeTimeLayout, since)
		if err != nil {
			return time.Time{}, time.Time{}, apierr.New(400, "invalid_date", "'since' must be an ISO 8601 datetime")
		}
		first = at
	}
	last := time.Now().UTC()
	if until != "" {
		at, err := time.Parse(storeTimeLayout, until)
		if err != nil {
			return time.Time{}, time.Time{}, apierr.New(400, "invalid_date", "'until' must be an ISO 8601 datetime")
		}
		// until is exclusive, so the last bucket is the one before it.
		last = at.Add(-time.Nanosecond)
	}
	if first.IsZero() || first.After(last) {
		// No data and no explicit window: emit nothing rather than a
		// bar-per-bucket back to the epoch.
		return time.Unix(1, 0).UTC(), time.Unix(0, 0).UTC(), nil
	}
	return truncateBucket(first, granularity), truncateBucket(last, granularity), nil
}

func (s *Server) handleMyUsageSeries(w http.ResponseWriter, r *http.Request, auth *Auth) {
	s.handleUsageSeries(w, r, store.UsageFilter{PrincipalID: auth.PrincipalID})
}

func (s *Server) handleMyUsage(w http.ResponseWriter, r *http.Request, auth *Auth) {
	since, perr := parseTimeParam(r.URL.Query().Get("since"), "since")
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	until, perr := parseTimeParam(r.URL.Query().Get("until"), "until")
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	rows, err := s.store.UsageSummary(r.Context(), auth.PrincipalID, since, until)
	if err != nil {
		writeProxyError(w, apierr.New(500, "internal_error", "failed to summarise usage"))
		return
	}
	writeJSON(w, 200, map[string]any{"usage": summaryViews(rows, nil)})
}
