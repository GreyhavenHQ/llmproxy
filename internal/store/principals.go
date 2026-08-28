package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/greyhavenhq/llmproxy/internal/secrets"
)

const principalColumns = `id, name, kind, role, external_sub, email, created_at, sessions_revoked_before`

func scanPrincipal(row interface{ Scan(...any) error }) (*Principal, error) {
	var p Principal
	err := row.Scan(&p.ID, &p.Name, &p.Kind, &p.Role, &p.ExternalSub, &p.Email, &p.CreatedAt, &p.SessionsRevokedBefore)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

func (s *Store) GetPrincipalByName(ctx context.Context, name string) (*Principal, error) {
	return scanPrincipal(s.db.QueryRowContext(ctx,
		s.q(`SELECT `+principalColumns+` FROM principal WHERE name = ?`), name))
}

func (s *Store) GetPrincipalByID(ctx context.Context, id string) (*Principal, error) {
	return scanPrincipal(s.db.QueryRowContext(ctx,
		s.q(`SELECT `+principalColumns+` FROM principal WHERE id = ?`), id))
}

func (s *Store) GetPrincipalByExternalSub(ctx context.Context, sub string) (*Principal, error) {
	return scanPrincipal(s.db.QueryRowContext(ctx,
		s.q(`SELECT `+principalColumns+` FROM principal WHERE external_sub = ?`), sub))
}

func (s *Store) GetOrCreatePrincipal(ctx context.Context, name, kind, role string, audit *Audit) (*Principal, error) {
	if existing, err := s.GetPrincipalByName(ctx, name); err != nil || existing != nil {
		return existing, err
	}
	p := &Principal{ID: secrets.NewID(), Name: name, Kind: kind, Role: role, CreatedAt: Now()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		s.q(`INSERT INTO principal (id, name, kind, role, created_at) VALUES (?, ?, ?, ?, ?)`),
		p.ID, p.Name, p.Kind, p.Role, p.CreatedAt); err != nil {
		return nil, err
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

func (s *Store) ListPrincipals(ctx context.Context, limit, offset int) ([]Principal, error) {
	rows, err := s.db.QueryContext(ctx,
		s.q(`SELECT `+principalColumns+` FROM principal ORDER BY name LIMIT ? OFFSET ?`),
		limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Principal
	for rows.Next() {
		p, err := scanPrincipal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) CreateAPIKey(ctx context.Context, principalID, keyHash, suffix, label string, audit *Audit) (*APIKey, error) {
	k := &APIKey{ID: secrets.NewID(), PrincipalID: principalID, Suffix: suffix, Label: label, CreatedAt: Now()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		s.q(`INSERT INTO api_key (id, principal_id, key_hash, key_suffix, label, created_at) VALUES (?, ?, ?, ?, ?, ?)`),
		k.ID, k.PrincipalID, keyHash, k.Suffix, k.Label, k.CreatedAt); err != nil {
		return nil, err
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return nil, err
	}
	return k, tx.Commit()
}

func (s *Store) AuthByKeyHash(ctx context.Context, keyHash string) (*AuthResult, error) {
	row := s.db.QueryRowContext(ctx, s.q(`
		SELECT k.id, k.last_used_at, p.id, p.name, p.role
		FROM api_key k JOIN principal p ON k.principal_id = p.id
		WHERE k.key_hash = ?`), keyHash)
	var a AuthResult
	if err := row.Scan(&a.KeyID, &a.LastUsedAt, &a.PrincipalID, &a.PrincipalName, &a.Role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

func (s *Store) TouchAPIKey(ctx context.Context, keyID string) error {
	_, err := s.db.ExecContext(ctx,
		s.q(`UPDATE api_key SET last_used_at = ? WHERE id = ?`), Now(), keyID)
	return err
}

// ListAPIKeys lists keys, optionally filtered by principal id or name.
func (s *Store) ListAPIKeys(ctx context.Context, principalID, principalName string, limit, offset int) ([]APIKey, error) {
	query := `
		SELECT k.id, k.principal_id, k.key_suffix, k.label, k.created_at, k.last_used_at, p.name
		FROM api_key k JOIN principal p ON k.principal_id = p.id`
	var args []any
	switch {
	case principalID != "":
		query += ` WHERE k.principal_id = ?`
		args = append(args, principalID)
	case principalName != "":
		query += ` WHERE p.name = ?`
		args = append(args, principalName)
	}
	query += ` ORDER BY k.created_at LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.PrincipalID, &k.Suffix, &k.Label, &k.CreatedAt, &k.LastUsedAt, &k.PrincipalName); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetAPIKey fetches one key; if ownerID is non-empty the key must belong to it.
func (s *Store) GetAPIKey(ctx context.Context, id, ownerID string) (*APIKey, error) {
	query := `SELECT id, principal_id, key_suffix, label, created_at, last_used_at FROM api_key WHERE id = ?`
	args := []any{id}
	if ownerID != "" {
		query += ` AND principal_id = ?`
		args = append(args, ownerID)
	}
	row := s.db.QueryRowContext(ctx, s.q(query), args...)
	var k APIKey
	if err := row.Scan(&k.ID, &k.PrincipalID, &k.Suffix, &k.Label, &k.CreatedAt, &k.LastUsedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &k, nil
}

// DeleteAPIKey removes the key outright; the next auth attempt with it gets
// invalid_api_key. Usage events keep the key id as an opaque reference.
func (s *Store) DeleteAPIKey(ctx context.Context, id string, audit *Audit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		s.q(`DELETE FROM api_key WHERE id = ?`), id); err != nil {
		return err
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokePrincipalSessions deletes every stored session of the principal and
// returns how many were removed; API keys are unaffected. It also sets the
// watermark that sweeps signed-cookie sessions from before the session table
// existed (those cannot be deleted); the watermark stops mattering once that
// format has drained, one SessionTTL after the upgrade.
func (s *Store) RevokePrincipalSessions(ctx context.Context, id string, audit *Audit) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		s.q(`UPDATE principal SET sessions_revoked_before = ? WHERE id = ?`), Now(), id); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		s.q(`DELETE FROM session WHERE principal_id = ?`), id)
	if err != nil {
		return 0, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return 0, err
	}
	return deleted, tx.Commit()
}

func (s *Store) auditTx(ctx context.Context, tx *sql.Tx, audit *Audit) error {
	if audit == nil {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		s.q(`INSERT INTO admin_event (id, ts, actor_principal_id, action, target_kind, target_ref) VALUES (?, ?, ?, ?, ?, ?)`),
		secrets.NewID(), Now(), audit.Actor, audit.Action, audit.TargetKind, audit.TargetRef)
	return err
}

func (s *Store) ListAdminEvents(ctx context.Context, limit, offset int) ([]AdminEvent, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT ts, actor_principal_id, action, target_kind, target_ref
		FROM admin_event ORDER BY ts DESC LIMIT ? OFFSET ?`), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminEvent
	for rows.Next() {
		var e AdminEvent
		if err := rows.Scan(&e.TS, &e.ActorPrincipalID, &e.Action, &e.TargetKind, &e.TargetRef); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
