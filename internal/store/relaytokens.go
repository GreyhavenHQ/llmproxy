package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/greyhavenhq/llmproxy/internal/secrets"
)

func (s *Store) CreateRelayToken(ctx context.Context, principalID, tokenHash, suffix, label string, audit *Audit) (*RelayToken, error) {
	rt := &RelayToken{ID: secrets.NewID(), PrincipalID: principalID, Suffix: suffix, Label: label, CreatedAt: Now()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		s.q(`INSERT INTO relay_token (id, principal_id, token_hash, token_suffix, label, created_at) VALUES (?, ?, ?, ?, ?, ?)`),
		rt.ID, rt.PrincipalID, tokenHash, rt.Suffix, rt.Label, rt.CreatedAt); err != nil {
		return nil, err
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return nil, err
	}
	return rt, tx.Commit()
}

// RelayAuthResult identifies the principal a relay token attributes usage to.
type RelayAuthResult struct {
	TokenID       string
	PrincipalID   string
	PrincipalName string
	LastUsedAt    sql.NullString
}

func (s *Store) AuthByRelayTokenHash(ctx context.Context, tokenHash string) (*RelayAuthResult, error) {
	row := s.db.QueryRowContext(ctx, s.q(`
		SELECT t.id, t.last_used_at, p.id, p.name
		FROM relay_token t JOIN principal p ON t.principal_id = p.id
		WHERE t.token_hash = ?`), tokenHash)
	var a RelayAuthResult
	if err := row.Scan(&a.TokenID, &a.LastUsedAt, &a.PrincipalID, &a.PrincipalName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

func (s *Store) TouchRelayToken(ctx context.Context, tokenID string) error {
	_, err := s.db.ExecContext(ctx,
		s.q(`UPDATE relay_token SET last_used_at = ? WHERE id = ?`), Now(), tokenID)
	return err
}

// ListRelayTokens lists tokens, optionally filtered by principal id.
func (s *Store) ListRelayTokens(ctx context.Context, principalID string, limit, offset int) ([]RelayToken, error) {
	query := `
		SELECT t.id, t.principal_id, t.token_suffix, t.label, t.created_at, t.last_used_at, p.name
		FROM relay_token t JOIN principal p ON t.principal_id = p.id`
	var args []any
	if principalID != "" {
		query += ` WHERE t.principal_id = ?`
		args = append(args, principalID)
	}
	query += ` ORDER BY t.created_at LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelayToken
	for rows.Next() {
		var rt RelayToken
		if err := rows.Scan(&rt.ID, &rt.PrincipalID, &rt.Suffix, &rt.Label, &rt.CreatedAt, &rt.LastUsedAt, &rt.PrincipalName); err != nil {
			return nil, err
		}
		out = append(out, rt)
	}
	return out, rows.Err()
}

// GetRelayToken fetches one token; if ownerID is non-empty it must belong to it.
func (s *Store) GetRelayToken(ctx context.Context, id, ownerID string) (*RelayToken, error) {
	query := `SELECT id, principal_id, token_suffix, label, created_at, last_used_at FROM relay_token WHERE id = ?`
	args := []any{id}
	if ownerID != "" {
		query += ` AND principal_id = ?`
		args = append(args, ownerID)
	}
	row := s.db.QueryRowContext(ctx, s.q(query), args...)
	var rt RelayToken
	if err := row.Scan(&rt.ID, &rt.PrincipalID, &rt.Suffix, &rt.Label, &rt.CreatedAt, &rt.LastUsedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &rt, nil
}

// DeleteRelayToken removes the token outright; the next relay request with it
// gets unknown_relay_token. Usage events keep the token id as an opaque ref.
func (s *Store) DeleteRelayToken(ctx context.Context, id string, audit *Audit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		s.q(`DELETE FROM relay_token WHERE id = ?`), id); err != nil {
		return err
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}
