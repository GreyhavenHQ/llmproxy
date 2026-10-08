package store

import "context"

// PerformanceEvents returns the timing rows of the filter window, at most
// limit of them. The bool is true when the window holds more than limit.
func (s *Store) PerformanceEvents(ctx context.Context, f UsageFilter, limit int) ([]PerformanceEvent, bool, error) {
	where, args := usageWhere(f)
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT e.ts, e.duration_ms, e.ttft_ms,
			(SELECT SUM(q.quantity) FROM usage_quantity q
				WHERE q.usage_event_id = e.id AND q.unit = 'output_tokens'),
			e.outcome, e.cancelled, e.streamed, e.alias, `+providerNameSQL+`
		FROM usage_event e LEFT JOIN provider p ON e.provider_id = p.id`+where+`
		LIMIT ?`), append(args, limit+1)...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []PerformanceEvent
	for rows.Next() {
		var r PerformanceEvent
		var cancelled, streamed int64
		if err := rows.Scan(&r.TS, &r.DurationMs, &r.TTFTMs, &r.OutputTokens,
			&r.Outcome, &cancelled, &streamed, &r.Alias, &r.Provider); err != nil {
			return nil, false, err
		}
		r.Cancelled = cancelled != 0
		r.Streamed = streamed != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		return nil, true, nil
	}
	return out, false, nil
}
