package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/greyhavenhq/llmproxy/internal/secrets"
)

const providerColumns = `id, name, wire_format, base_url, credential_ciphertext, verify_tls,
	ca_pem, timeout_connect, timeout_read, max_concurrency, enabled, created_at`

func scanProvider(row interface{ Scan(...any) error }) (*Provider, error) {
	var p Provider
	var verifyTLS, enabled int64
	err := row.Scan(&p.ID, &p.Name, &p.WireFormat, &p.BaseURL, &p.CredentialCiphertext,
		&verifyTLS, &p.CAPEM, &p.TimeoutConnect, &p.TimeoutRead,
		&p.MaxConcurrency, &enabled, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	p.VerifyTLS = verifyTLS != 0
	p.Enabled = enabled != 0
	return &p, nil
}

func (s *Store) GetProviderByName(ctx context.Context, name string) (*Provider, error) {
	row := s.db.QueryRowContext(ctx,
		s.q(`SELECT `+providerColumns+` FROM provider WHERE name = ?`), name)
	return scanProvider(row)
}

func (s *Store) ListProviders(ctx context.Context, limit, offset int) ([]Provider, error) {
	rows, err := s.db.QueryContext(ctx,
		s.q(`SELECT `+providerColumns+` FROM provider ORDER BY name LIMIT ? OFFSET ?`), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) CreateProvider(ctx context.Context, p *Provider, overrides map[string]string, audit *Audit) error {
	p.ID = secrets.NewID()
	p.CreatedAt = Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.q(`
		INSERT INTO provider (id, name, wire_format, base_url, credential_ciphertext, verify_tls,
			ca_pem, timeout_connect, timeout_read, max_concurrency, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		p.ID, p.Name, p.WireFormat, p.BaseURL, p.CredentialCiphertext, boolInt(p.VerifyTLS),
		p.CAPEM, p.TimeoutConnect, p.TimeoutRead, p.MaxConcurrency,
		boolInt(p.Enabled), p.CreatedAt); err != nil {
		return err
	}
	for endpoint, url := range overrides {
		if _, err := tx.ExecContext(ctx, s.q(`
			INSERT INTO provider_endpoint (id, provider_id, endpoint, url_override, enabled)
			VALUES (?, ?, ?, ?, 1)`),
			secrets.NewID(), p.ID, endpoint, url); err != nil {
			return err
		}
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateProvider(ctx context.Context, p *Provider, audit *Audit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.q(`
		UPDATE provider SET base_url = ?, credential_ciphertext = ?, enabled = ?,
			verify_tls = ?, timeout_connect = ?, timeout_read = ?, max_concurrency = ?
		WHERE id = ?`),
		p.BaseURL, p.CredentialCiphertext, boolInt(p.Enabled),
		boolInt(p.VerifyTLS), p.TimeoutConnect, p.TimeoutRead, p.MaxConcurrency, p.ID); err != nil {
		return err
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteProvider(ctx context.Context, providerID string, audit *Audit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		// Aliases of this provider's models go with them; leaving them would
		// strand rows pointing at nothing.
		`DELETE FROM model_binding WHERE target_id IN (SELECT id FROM model_binding WHERE provider_id = ?)`,
		`DELETE FROM model_binding WHERE provider_id = ?`,
		`DELETE FROM provider_endpoint WHERE provider_id = ?`,
		`DELETE FROM provider WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, s.q(stmt), providerID); err != nil {
			return err
		}
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListEndpointOverrides(ctx context.Context, providerID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT endpoint, url_override FROM provider_endpoint
		WHERE provider_id = ? AND enabled = 1 AND url_override IS NOT NULL`), providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var endpoint, url string
		if err := rows.Scan(&endpoint, &url); err != nil {
			return nil, err
		}
		out[endpoint] = url
	}
	return out, rows.Err()
}

// bindingSource resolves the one permitted hop in SQL: an alias row (target_id
// set) reads its provider, upstream name and capabilities off its target, a
// direct row off itself. Every read goes through this, so callers never have
// to know which kind of row they are holding.
const bindingSource = ` FROM model_binding b
	LEFT JOIN model_binding t ON b.target_id = t.id
	JOIN provider p ON p.id = COALESCE(t.provider_id, b.provider_id)`

const bindingColumns = `b.id, b.alias, COALESCE(t.provider_id, b.provider_id),
	COALESCE(t.upstream_name, b.upstream_name),
	COALESCE(t.capability_set, b.capability_set),
	b.origin, b.discovered_at, b.created_at, b.target_id, COALESCE(t.alias, ''), p.name`

func scanBinding(row interface{ Scan(...any) error }) (*ModelBinding, error) {
	var b ModelBinding
	err := row.Scan(&b.ID, &b.Alias, &b.ProviderID, &b.UpstreamName,
		&b.CapabilitySet, &b.Origin, &b.DiscoveredAt, &b.CreatedAt,
		&b.TargetID, &b.TargetAlias, &b.ProviderName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
}

func (s *Store) GetBindingByID(ctx context.Context, id string) (*ModelBinding, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT `+bindingColumns+bindingSource+` WHERE b.id = ?`), id)
	return scanBinding(row)
}

func (s *Store) GetBindingByAlias(ctx context.Context, alias string) (*ModelBinding, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT `+bindingColumns+bindingSource+` WHERE b.alias = ?`), alias)
	return scanBinding(row)
}

func (s *Store) ListBindings(ctx context.Context, providerName string, limit, offset int) ([]ModelBinding, error) {
	query := `SELECT ` + bindingColumns + bindingSource
	var args []any
	if providerName != "" {
		query += ` WHERE p.name = ?`
		args = append(args, providerName)
	}
	query += ` ORDER BY b.alias LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelBinding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ListServableBindings returns bindings on enabled providers, for the
// caller-facing model list. An alias is servable when its target's provider
// is enabled, which the resolved join already accounts for.
func (s *Store) ListServableBindings(ctx context.Context) ([]ModelBinding, error) {
	rows, err := s.db.QueryContext(ctx,
		s.q(`SELECT `+bindingColumns+bindingSource+` WHERE p.enabled = 1 ORDER BY b.alias`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelBinding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ListBindingsTargeting returns the aliases pointing at a binding, so a
// delete can refuse to strand them.
func (s *Store) ListBindingsTargeting(ctx context.Context, bindingID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		s.q(`SELECT alias FROM model_binding WHERE target_id = ? ORDER BY alias`), bindingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, err
		}
		out = append(out, alias)
	}
	return out, rows.Err()
}

// ownRouting returns the columns a row stores for itself. An alias keeps none
// of its own: it borrows the target's provider id only to satisfy the
// reference, and reads everything through the join.
func ownRouting(b *ModelBinding) (providerID, upstreamName, capabilitySet string) {
	if b.TargetID.Valid {
		return b.ProviderID, "", ""
	}
	return b.ProviderID, b.UpstreamName, b.CapabilitySet
}

func (s *Store) CreateBinding(ctx context.Context, b *ModelBinding, audit *Audit) error {
	b.ID = secrets.NewID()
	b.CreatedAt = Now()
	providerID, upstreamName, capabilitySet := ownRouting(b)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.q(`
		INSERT INTO model_binding (id, alias, provider_id, upstream_name, capability_set, origin, discovered_at, created_at, target_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		b.ID, b.Alias, providerID, upstreamName,
		capabilitySet, b.Origin, b.DiscoveredAt, b.CreatedAt, b.TargetID); err != nil {
		return err
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateBinding(ctx context.Context, b *ModelBinding, audit *Audit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	providerID, upstreamName, capabilitySet := ownRouting(b)
	if _, err := tx.ExecContext(ctx, s.q(`
		UPDATE model_binding SET alias = ?, provider_id = ?, upstream_name = ?, capability_set = ?,
			target_id = ? WHERE id = ?`),
		b.Alias, providerID, upstreamName, capabilitySet, b.TargetID, b.ID); err != nil {
		return err
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteBinding(ctx context.Context, bindingID string, audit *Audit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		s.q(`DELETE FROM model_binding WHERE id = ?`), bindingID); err != nil {
		return err
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

// ResolveAlias loads the routing row for an alias on an enabled provider, or
// nil when there is none.
func (s *Store) ResolveAlias(ctx context.Context, alias string) (*ModelBinding, *Provider, map[string]string, error) {
	binding, err := s.GetBindingByAlias(ctx, alias)
	if err != nil || binding == nil {
		return nil, nil, nil, err
	}
	row := s.db.QueryRowContext(ctx,
		s.q(`SELECT `+providerColumns+` FROM provider WHERE id = ? AND enabled = 1`),
		binding.ProviderID)
	provider, err := scanProvider(row)
	if err != nil || provider == nil {
		return nil, nil, nil, err
	}
	overrides, err := s.ListEndpointOverrides(ctx, provider.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	return binding, provider, overrides, nil
}
