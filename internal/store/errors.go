package store

import (
	"context"
	"sort"
)

// Error analytics: the queries behind /stats/errors. Everything here reads
// usage_event metadata only; there is no content anywhere to aggregate.

// durationBandsSQL counts rows into fixed time-to-outcome bands. The
// boundaries sit around common client timeouts (60s, 120s), so a cluster of
// cancellations in one band reads as "the callers' timeout fired", and a
// cluster of upstream errors in the first band as "the provider rejects
// instantly".
const durationBandsSQL = `
	SUM(CASE WHEN e.duration_ms < 1000 THEN 1 ELSE 0 END),
	SUM(CASE WHEN e.duration_ms >= 1000 AND e.duration_ms < 5000 THEN 1 ELSE 0 END),
	SUM(CASE WHEN e.duration_ms >= 5000 AND e.duration_ms < 15000 THEN 1 ELSE 0 END),
	SUM(CASE WHEN e.duration_ms >= 15000 AND e.duration_ms < 30000 THEN 1 ELSE 0 END),
	SUM(CASE WHEN e.duration_ms >= 30000 AND e.duration_ms < 60000 THEN 1 ELSE 0 END),
	SUM(CASE WHEN e.duration_ms >= 60000 AND e.duration_ms < 120000 THEN 1 ELSE 0 END),
	SUM(CASE WHEN e.duration_ms >= 120000 THEN 1 ELSE 0 END)`

// ErrorSeries buckets request counts by outcome, hourly or daily. Same
// timestamp-prefix bucketing as UsageSeries, without the units and cost
// joins: the errors chart needs counts only.
func (s *Store) ErrorSeries(ctx context.Context, f UsageFilter, hourly bool) ([]ErrorSeriesRow, error) {
	bucket := "SUBSTR(e.ts, 1, 10)"
	if hourly {
		bucket = "SUBSTR(e.ts, 1, 13)"
	}
	where, args := usageWhere(f)
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT `+bucket+`, COUNT(*),
			SUM(CASE WHEN e.outcome = 'ok' THEN 1 ELSE 0 END),
			SUM(CASE WHEN e.outcome = 'upstream_error' THEN 1 ELSE 0 END),
			SUM(CASE WHEN e.outcome = 'unreachable' THEN 1 ELSE 0 END),
			SUM(CASE WHEN e.outcome = 'cancelled' THEN 1 ELSE 0 END)
		FROM usage_event e LEFT JOIN provider p ON e.provider_id = p.id`+where+`
		GROUP BY `+bucket), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ErrorSeriesRow
	for rows.Next() {
		var r ErrorSeriesRow
		if err := rows.Scan(&r.Bucket, &r.Requests, &r.OK, &r.UpstreamError,
			&r.Unreachable, &r.Cancelled); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket < out[j].Bucket })
	return out, nil
}

// ErrorBreakdown aggregates the filter window by (provider, model, endpoint,
// client, tags, outcome, error_kind, status_code), each cell carrying its
// request count, average duration, latest timestamp and duration-band counts.
// One query feeds every roll-up the errors dashboard shows; ok rows are kept
// so a dimension's error rate has its denominator in the same response.
func (s *Store) ErrorBreakdown(ctx context.Context, f UsageFilter) ([]ErrorBreakdownRow, error) {
	where, args := usageWhere(f)
	const dims = providerNameSQL + `, e.alias, e.endpoint, e.client, e.tags,
		e.outcome, e.error_kind, e.status_code`
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT `+dims+`, COUNT(*), AVG(e.duration_ms), MAX(e.ts), `+durationBandsSQL+`
		FROM usage_event e LEFT JOIN provider p ON e.provider_id = p.id`+where+`
		GROUP BY `+dims), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ErrorBreakdownRow
	for rows.Next() {
		var r ErrorBreakdownRow
		if err := rows.Scan(&r.Provider, &r.Alias, &r.Endpoint, &r.Client, &r.Tags,
			&r.Outcome, &r.ErrorKind, &r.StatusCode, &r.Requests, &r.AvgMs, &r.LastSeen,
			&r.Bands[0], &r.Bands[1], &r.Bands[2], &r.Bands[3], &r.Bands[4],
			&r.Bands[5], &r.Bands[6]); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
