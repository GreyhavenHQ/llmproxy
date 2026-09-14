package store

import (
	"context"
	"database/sql"

	"github.com/greyhavenhq/llmproxy/internal/secrets"
)

func (s *Store) InsertRateLimitSample(ctx context.Context, sample *RateLimitSample) error {
	sample.ID = secrets.NewID()
	_, err := s.db.ExecContext(ctx, s.q(`
		INSERT INTO provider_rate_limit_sample
			(id, provider_id, bucket, observed_at, observations,
			 limit_requests, remaining_requests, min_remaining_requests,
			 limit_tokens, remaining_tokens, min_remaining_tokens,
			 reset_requests_at, reset_tokens_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		sample.ID, sample.ProviderID, sample.Bucket, sample.ObservedAt,
		sample.Observations,
		sample.LimitRequests, sample.RemainingRequests, sample.MinRemainingRequests,
		sample.LimitTokens, sample.RemainingTokens, sample.MinRemainingTokens,
		sample.ResetRequestsAt, sample.ResetTokensAt)
	return err
}

func (s *Store) LatestRateLimitSamples(ctx context.Context) ([]RateLimitSample, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT s.id, s.provider_id, s.bucket, s.observed_at, s.observations,
			s.limit_requests, s.remaining_requests, s.min_remaining_requests,
			s.limit_tokens, s.remaining_tokens, s.min_remaining_tokens,
			s.reset_requests_at, s.reset_tokens_at
		FROM provider_rate_limit_sample s
		INNER JOIN (
			SELECT provider_id, MAX(bucket) AS max_bucket
			FROM provider_rate_limit_sample
			GROUP BY provider_id
		) latest ON s.provider_id = latest.provider_id AND s.bucket = latest.max_bucket
		ORDER BY s.provider_id`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRateLimitSamples(rows)
}

func (s *Store) RateLimitSeries(ctx context.Context, providerID, since, until string) ([]RateLimitSample, error) {
	where := ` WHERE provider_id = ?`
	args := []any{providerID}
	if since != "" {
		where += ` AND bucket >= ?`
		args = append(args, since)
	}
	if until != "" {
		where += ` AND bucket <= ?`
		args = append(args, until)
	}
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT id, provider_id, bucket, observed_at, observations,
			limit_requests, remaining_requests, min_remaining_requests,
			limit_tokens, remaining_tokens, min_remaining_tokens,
			reset_requests_at, reset_tokens_at
		FROM provider_rate_limit_sample`+where+` ORDER BY bucket`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRateLimitSamples(rows)
}

func (s *Store) PruneRateLimitSamples(ctx context.Context, before string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		s.q(`DELETE FROM provider_rate_limit_sample WHERE bucket < ?`), before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanRateLimitSamples(rows *sql.Rows) ([]RateLimitSample, error) {
	var out []RateLimitSample
	for rows.Next() {
		var s RateLimitSample
		if err := rows.Scan(&s.ID, &s.ProviderID, &s.Bucket, &s.ObservedAt,
			&s.Observations,
			&s.LimitRequests, &s.RemainingRequests, &s.MinRemainingRequests,
			&s.LimitTokens, &s.RemainingTokens, &s.MinRemainingTokens,
			&s.ResetRequestsAt, &s.ResetTokensAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
