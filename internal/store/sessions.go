package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/greyhavenhq/llmproxy/internal/secrets"
)

// SessionAuthResult identifies the principal behind a browser session.
type SessionAuthResult struct {
	SessionID     string
	PrincipalID   string
	PrincipalName string
	Role          string
	ExpiresAt     string
	LastUsedAt    sql.NullString
}

// CreateSession stores a browser session row and opportunistically purges
// expired rows. Logins are not admin events, so there is no audit here.
func (s *Store) CreateSession(ctx context.Context, principalID, tokenHash, expiresAt string) error {
	_, _ = s.db.ExecContext(ctx, s.q(`DELETE FROM session WHERE expires_at < ?`), Now())
	_, err := s.db.ExecContext(ctx,
		s.q(`INSERT INTO session (id, principal_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`),
		secrets.NewID(), principalID, tokenHash, Now(), expiresAt)
	return err
}

// AuthBySessionHash resolves a session token hash to its principal, or nil
// for an unknown, expired or deleted session.
func (s *Store) AuthBySessionHash(ctx context.Context, tokenHash string) (*SessionAuthResult, error) {
	row := s.db.QueryRowContext(ctx, s.q(`
		SELECT se.id, se.expires_at, se.last_used_at, p.id, p.name, p.role
		FROM session se JOIN principal p ON se.principal_id = p.id
		WHERE se.token_hash = ? AND se.expires_at > ?`), tokenHash, Now())
	var a SessionAuthResult
	if err := row.Scan(&a.SessionID, &a.ExpiresAt, &a.LastUsedAt, &a.PrincipalID, &a.PrincipalName, &a.Role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

func (s *Store) TouchSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		s.q(`UPDATE session SET last_used_at = ? WHERE id = ?`), Now(), id)
	return err
}

// ExtendSession moves the end time of a live session. An expired or deleted
// session stays dead.
func (s *Store) ExtendSession(ctx context.Context, id, expiresAt string) error {
	_, err := s.db.ExecContext(ctx,
		s.q(`UPDATE session SET expires_at = ? WHERE id = ? AND expires_at > ?`), expiresAt, id, Now())
	return err
}

// HasCredentials reports whether any session or API key exists, to tell a
// first start from a lost secret.
func (s *Store) HasCredentials(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM session) + (SELECT COUNT(*) FROM api_key)`).Scan(&n)
	return n > 0, err
}

// DeleteSessionByHash removes one session (logout). Deletion is the
// invalidation mechanism; there is no revoked flag.
func (s *Store) DeleteSessionByHash(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx,
		s.q(`DELETE FROM session WHERE token_hash = ?`), tokenHash)
	return err
}
