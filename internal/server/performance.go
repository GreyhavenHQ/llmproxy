package server

import (
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/apierr"
	"github.com/greyhavenhq/llmproxy/internal/store"
)

// performanceEventCap bounds the rows one /stats/performance call loads.
var performanceEventCap = 500000

// figure is the mean, p50 and p95 of one measure; nil when nothing qualified.
type figure struct {
	Mean *float64 `json:"mean"`
	P50  *float64 `json:"p50"`
	P95  *float64 `json:"p95"`
}

// percentile picks the nearest-rank value from sorted, non-empty values.
func percentile(sorted []float64, p float64) float64 {
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

func newFigure(values []float64) figure {
	if len(values) == 0 {
		return figure{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	mean, p50, p95 := sum/float64(len(sorted)), percentile(sorted, 50), percentile(sorted, 95)
	return figure{Mean: &mean, P50: &p50, P95: &p95}
}

type interval struct{ start, end time.Time }

// segment is a stretch of time with a constant number of requests in flight.
type segment struct {
	start, end time.Time
	count      int
}

// concurrencySegments sweeps the start and end points into sorted segments.
// An end sorts before a start at the same instant, so touching intervals do
// not overlap.
func concurrencySegments(spans []interval) []segment {
	type point struct {
		at    time.Time
		delta int
	}
	points := make([]point, 0, 2*len(spans))
	for _, s := range spans {
		if !s.end.After(s.start) {
			continue
		}
		points = append(points, point{s.start, 1}, point{s.end, -1})
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].at.Equal(points[j].at) {
			return points[i].delta < points[j].delta
		}
		return points[i].at.Before(points[j].at)
	})
	var segs []segment
	count := 0
	for i, p := range points {
		count += p.delta
		if count > 0 && i+1 < len(points) && points[i+1].at.After(p.at) {
			segs = append(segs, segment{p.at, points[i+1].at, count})
		}
	}
	return segs
}

// concurrency returns the average and the peak in-flight count over
// [from, to) from segments sorted by start.
func concurrency(segs []segment, from, to time.Time) (float64, int) {
	if !to.After(from) {
		return 0, 0
	}
	i := sort.Search(len(segs), func(i int) bool { return segs[i].end.After(from) })
	var busy time.Duration
	peak := 0
	for ; i < len(segs) && segs[i].start.Before(to); i++ {
		start, end := segs[i].start, segs[i].end
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		busy += time.Duration(segs[i].count) * end.Sub(start)
		if segs[i].count > peak {
			peak = segs[i].count
		}
	}
	return float64(busy) / float64(to.Sub(from)), peak
}

// perfSamples collects the latency measures of the requests that qualify:
// ok and not cancelled.
type perfSamples struct {
	requests                 int64
	measured                 int64
	duration, ttft, tokensPS []float64
}

func (p *perfSamples) add(ev store.PerformanceEvent) {
	p.requests++
	if ev.Outcome != "ok" || ev.Cancelled {
		return
	}
	p.measured++
	p.duration = append(p.duration, float64(ev.DurationMs))
	if !ev.TTFTMs.Valid {
		return
	}
	p.ttft = append(p.ttft, float64(ev.TTFTMs.Int64))
	generating := ev.DurationMs - ev.TTFTMs.Int64
	if ev.Streamed && ev.OutputTokens.Valid && generating > 0 {
		p.tokensPS = append(p.tokensPS, ev.OutputTokens.Float64/(float64(generating)/1000))
	}
}

func (p *perfSamples) view() map[string]any {
	return map[string]any{
		"requests":          p.requests,
		"measured":          p.measured,
		"duration_ms":       newFigure(p.duration),
		"ttft_ms":           newFigure(p.ttft),
		"tokens_per_second": newFigure(p.tokensPS),
	}
}

// handleStatsPerformance answers the performance view: latency, time to first
// token, output speed and concurrency under the shared stats filters. Latency
// figures land in the bucket where the request ended; concurrency splits a
// request across the buckets it spans.
func (s *Server) handleStatsPerformance(w http.ResponseWriter, r *http.Request, auth *Auth) {
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
	events, tooMany, err := s.store.PerformanceEvents(r.Context(), filter, performanceEventCap)
	if err != nil {
		internalErr(w, "failed to read performance data")
		return
	}
	if tooMany {
		writeProxyError(w, apierr.Newf(400, "range_too_large",
			"this range holds more than %d requests; narrow it", performanceEventCap))
		return
	}

	var summary perfSamples
	byStart := make(map[time.Time]*perfSamples)
	type modelKey struct{ provider, alias string }
	byModel := make(map[modelKey]*perfSamples)
	spans := make([]interval, 0, len(events))
	var earliest time.Time
	for _, ev := range events {
		end, err := time.Parse(storeTimeLayout, ev.TS)
		if err != nil {
			continue
		}
		if earliest.IsZero() || end.Before(earliest) {
			earliest = end
		}
		spans = append(spans, interval{end.Add(-time.Duration(ev.DurationMs) * time.Millisecond), end})
		summary.add(ev)
		start := truncateBucket(end, granularity)
		if byStart[start] == nil {
			byStart[start] = &perfSamples{}
		}
		byStart[start].add(ev)
		key := modelKey{ev.Provider, ev.Alias}
		if byModel[key] == nil {
			byModel[key] = &perfSamples{}
		}
		byModel[key].add(ev)
	}

	first, last, perr := seriesBounds(filter.Since, filter.Until, earliest, granularity)
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	segs := concurrencySegments(spans)
	now := time.Now().UTC()
	var busy, elapsed float64
	peak := 0
	series := make([]map[string]any, 0, len(byStart))
	for at := first; !at.After(last); at = nextBucket(at, granularity) {
		if len(series) >= maxBuckets {
			writeProxyError(w, apierr.Newf(400, "range_too_large",
				"a %s series over this range needs more than %d buckets; narrow it or use a coarser bucket",
				granularity, maxBuckets))
			return
		}
		end := nextBucket(at, granularity)
		if end.After(now) {
			end = now
		}
		avg, bucketPeak := concurrency(segs, at, end)
		if end.After(at) {
			busy += avg * float64(end.Sub(at))
			elapsed += float64(end.Sub(at))
		}
		peak = max(peak, bucketPeak)
		samples := byStart[at]
		if samples == nil {
			samples = &perfSamples{}
		}
		view := samples.view()
		view["start"] = at.Format(time.RFC3339)
		view["concurrency"] = map[string]any{"average": avg, "peak": bucketPeak}
		series = append(series, view)
	}

	summaryView := summary.view()
	average := 0.0
	if elapsed > 0 {
		average = busy / elapsed
	}
	summaryView["concurrency"] = map[string]any{"average": average, "peak": peak}

	models := make([]map[string]any, 0, len(byModel))
	for key, samples := range byModel {
		view := samples.view()
		view["provider"] = key.provider
		view["model"] = key.alias
		models = append(models, view)
	}
	sort.Slice(models, func(i, j int) bool {
		a, b := models[i]["requests"].(int64), models[j]["requests"].(int64)
		if a != b {
			return a > b
		}
		return models[i]["model"].(string) < models[j]["model"].(string)
	})
	writeJSON(w, 200, map[string]any{
		"bucket": granularity, "summary": summaryView, "series": series, "models": models,
	})
}
