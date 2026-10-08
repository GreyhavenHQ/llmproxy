package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/greyhavenhq/llmproxy/internal/secrets"
)

const providerColumns = `id, name, wire_format, base_url, credential_ciphertext, verify_tls,
	ca_pem, timeout_connect, timeout_read, max_concurrency, enabled, created_at, rate_limit_headers`

func scanProvider(row interface{ Scan(...any) error }) (*Provider, error) {
	var p Provider
	var verifyTLS, enabled int64
	err := row.Scan(&p.ID, &p.Name, &p.WireFormat, &p.BaseURL, &p.CredentialCiphertext,
		&verifyTLS, &p.CAPEM, &p.TimeoutConnect, &p.TimeoutRead,
		&p.MaxConcurrency, &enabled, &p.CreatedAt, &p.RateLimitHeaders)
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
			ca_pem, timeout_connect, timeout_read, max_concurrency, enabled, created_at, rate_limit_headers)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		p.ID, p.Name, p.WireFormat, p.BaseURL, p.CredentialCiphertext, boolInt(p.VerifyTLS),
		p.CAPEM, p.TimeoutConnect, p.TimeoutRead, p.MaxConcurrency,
		boolInt(p.Enabled), p.CreatedAt, p.RateLimitHeaders); err != nil {
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
			verify_tls = ?, timeout_connect = ?, timeout_read = ?, max_concurrency = ?,
			rate_limit_headers = ?
		WHERE id = ?`),
		p.BaseURL, p.CredentialCiphertext, boolInt(p.Enabled),
		boolInt(p.VerifyTLS), p.TimeoutConnect, p.TimeoutRead, p.MaxConcurrency,
		p.RateLimitHeaders, p.ID); err != nil {
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
	if err := s.dropProviderTargetsTx(ctx, tx, providerID); err != nil {
		return err
	}
	for _, stmt := range []string{
		// Aliases of this provider's models go with them; leaving them would
		// strand rows pointing at nothing.
		`DELETE FROM model_binding WHERE target_id IN (SELECT id FROM model_binding WHERE provider_id = ?)`,
		`DELETE FROM model_binding WHERE provider_id = ?`,
		`DELETE FROM provider_endpoint WHERE provider_id = ?`,
		`DELETE FROM provider_rate_limit_sample WHERE provider_id = ?`,
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
	b.origin, b.discovered_at, b.created_at, b.target_id, COALESCE(t.alias, ''), p.name,
	b.hidden, b.strategy`

func scanBinding(row interface{ Scan(...any) error }) (*ModelBinding, error) {
	var b ModelBinding
	// hidden is read off the row itself, not through the join: unlike routing,
	// it is not inherited from an alias's target.
	var hidden int64
	err := row.Scan(&b.ID, &b.Alias, &b.ProviderID, &b.UpstreamName,
		&b.CapabilitySet, &b.Origin, &b.DiscoveredAt, &b.CreatedAt,
		&b.TargetID, &b.TargetAlias, &b.ProviderName, &hidden, &b.Strategy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	b.Hidden = hidden != 0
	if b.TargetID.Valid {
		b.Targets = []BindingTarget{{ID: b.TargetID.String, Alias: b.TargetAlias, Weight: 1}}
	}
	return &b, nil
}

// targetsWhere selects bindings with a listed target matching cond, which
// reads columns of the target binding t2 and its provider p2.
func targetsWhere(cond string) string {
	return `EXISTS (SELECT 1 FROM model_binding_target bt
		JOIN model_binding t2 ON t2.id = bt.target_id
		JOIN provider p2 ON p2.id = t2.provider_id
		WHERE bt.binding_id = b.id AND ` + cond + `)`
}

// attachTargets fills Targets for aliases stored in the target table.
func (s *Store) attachTargets(ctx context.Context, bindings []*ModelBinding) error {
	multi := map[string]*ModelBinding{}
	var ids []any
	for _, b := range bindings {
		if b != nil && b.Strategy != "" {
			multi[b.ID] = b
			b.Targets = nil
			ids = append(ids, b.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT bt.binding_id, bt.target_id, t.alias, bt.weight
		FROM model_binding_target bt JOIN model_binding t ON t.id = bt.target_id
		WHERE bt.binding_id IN (`+placeholders(len(ids))+`)
		ORDER BY bt.binding_id, bt.position`), ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bindingID string
		var t BindingTarget
		if err := rows.Scan(&bindingID, &t.ID, &t.Alias, &t.Weight); err != nil {
			return err
		}
		multi[bindingID].Targets = append(multi[bindingID].Targets, t)
	}
	return rows.Err()
}

func (s *Store) getBinding(ctx context.Context, where string, arg any) (*ModelBinding, error) {
	row := s.db.QueryRowContext(ctx, s.q(`SELECT `+bindingColumns+bindingSource+where), arg)
	b, err := scanBinding(row)
	if err != nil || b == nil {
		return nil, err
	}
	return b, s.attachTargets(ctx, []*ModelBinding{b})
}

func (s *Store) GetBindingByID(ctx context.Context, id string) (*ModelBinding, error) {
	return s.getBinding(ctx, ` WHERE b.id = ?`, id)
}

func (s *Store) GetBindingByAlias(ctx context.Context, alias string) (*ModelBinding, error) {
	return s.getBinding(ctx, ` WHERE b.alias = ?`, alias)
}

func (s *Store) listBindings(ctx context.Context, query string, args ...any) ([]ModelBinding, error) {
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	var out []ModelBinding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ptrs := make([]*ModelBinding, len(out))
	for i := range out {
		ptrs[i] = &out[i]
	}
	return out, s.attachTargets(ctx, ptrs)
}

func (s *Store) ListBindings(ctx context.Context, providerName string, limit, offset int) ([]ModelBinding, error) {
	query := `SELECT ` + bindingColumns + bindingSource
	var args []any
	if providerName != "" {
		query += ` WHERE (p.name = ? OR ` + targetsWhere(`p2.name = ?`) + `)`
		args = append(args, providerName, providerName)
	}
	query += ` ORDER BY b.alias LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	return s.listBindings(ctx, query, args...)
}

// ListServableBindings returns bindings on enabled providers, for the
// caller-facing model list. An alias is servable when its target's provider
// is enabled, which the resolved join already accounts for, or with several
// targets when any target's provider is enabled. Hidden rows are
// left out unless includeHidden asks for them; they stay servable either way,
// hidden is a listing flag only.
func (s *Store) ListServableBindings(ctx context.Context, includeHidden bool) ([]ModelBinding, error) {
	where := ` WHERE ((b.strategy = '' AND p.enabled = 1) OR ` + targetsWhere(`p2.enabled = 1`) + `)`
	if !includeHidden {
		where += ` AND b.hidden = 0`
	}
	return s.listBindings(ctx, `SELECT `+bindingColumns+bindingSource+where+` ORDER BY b.alias`)
}

// ListBindingsTargeting returns the aliases pointing at a binding, so a
// delete can refuse to strand them.
func (s *Store) ListBindingsTargeting(ctx context.Context, bindingID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		s.q(`SELECT alias FROM model_binding WHERE target_id = ?
			UNION SELECT b.alias FROM model_binding_target bt JOIN model_binding b ON b.id = bt.binding_id
			WHERE bt.target_id = ? ORDER BY 1`), bindingID, bindingID)
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

// prepareTargets picks the storage form for an alias: one target goes in
// target_id, several go in model_binding_target with a strategy. An alias
// borrows its first target's provider id to satisfy the reference.
func (s *Store) prepareTargets(ctx context.Context, tx *sql.Tx, b *ModelBinding) error {
	switch {
	case len(b.Targets) == 1:
		b.TargetID = sql.NullString{String: b.Targets[0].ID, Valid: true}
		b.Strategy = ""
	case len(b.Targets) > 1:
		b.TargetID = sql.NullString{}
		if b.Strategy == "" {
			b.Strategy = "failover"
		}
		for i := range b.Targets {
			if b.Targets[i].Weight < 1 {
				b.Targets[i].Weight = 1
			}
		}
	default:
		b.Strategy = ""
	}
	first := b.TargetID.String
	if len(b.Targets) > 1 {
		first = b.Targets[0].ID
	}
	if first == "" {
		return nil
	}
	return tx.QueryRowContext(ctx, s.q(`SELECT provider_id FROM model_binding WHERE id = ?`), first).
		Scan(&b.ProviderID)
}

// ownRouting returns the columns a row stores for itself. An alias keeps none
// of its own and reads everything through the join or the target table.
func ownRouting(b *ModelBinding) (providerID, upstreamName, capabilitySet string) {
	if b.TargetID.Valid || b.Strategy != "" {
		return b.ProviderID, "", ""
	}
	return b.ProviderID, b.UpstreamName, b.CapabilitySet
}

func (s *Store) writeTargetsTx(ctx context.Context, tx *sql.Tx, b *ModelBinding) error {
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM model_binding_target WHERE binding_id = ?`), b.ID); err != nil {
		return err
	}
	if b.Strategy == "" {
		return nil
	}
	for i, t := range b.Targets {
		if _, err := tx.ExecContext(ctx, s.q(`
			INSERT INTO model_binding_target (binding_id, target_id, position, weight) VALUES (?, ?, ?, ?)`),
			b.ID, t.ID, i, t.Weight); err != nil {
			return err
		}
	}
	return nil
}

// dropProviderTargetsTx removes a provider's models from aliases with several
// targets. An alias left with one target becomes a plain alias, with none it
// is deleted.
func (s *Store) dropProviderTargetsTx(ctx context.Context, tx *sql.Tx, providerID string) error {
	rows, err := tx.QueryContext(ctx, s.q(`
		SELECT DISTINCT bt.binding_id FROM model_binding_target bt
		JOIN model_binding t ON t.id = bt.target_id WHERE t.provider_id = ?`), providerID)
	if err != nil {
		return err
	}
	var affected []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		affected = append(affected, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM model_binding_target
		WHERE target_id IN (SELECT id FROM model_binding WHERE provider_id = ?)`), providerID); err != nil {
		return err
	}
	for _, id := range affected {
		var remaining []string
		rows, err := tx.QueryContext(ctx, s.q(`
			SELECT target_id FROM model_binding_target WHERE binding_id = ? ORDER BY position`), id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var t string
			if err := rows.Scan(&t); err != nil {
				rows.Close()
				return err
			}
			remaining = append(remaining, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var stmts []string
		var args [][]any
		switch len(remaining) {
		case 0:
			stmts = []string{`DELETE FROM model_binding WHERE id = ?`}
			args = [][]any{{id}}
		case 1:
			stmts = []string{
				`DELETE FROM model_binding_target WHERE binding_id = ?`,
				`UPDATE model_binding SET strategy = '', target_id = ?,
					provider_id = (SELECT provider_id FROM model_binding WHERE id = ?) WHERE id = ?`,
			}
			args = [][]any{{id}, {remaining[0], remaining[0], id}}
		default:
			stmts = []string{`UPDATE model_binding SET
				provider_id = (SELECT provider_id FROM model_binding WHERE id = ?) WHERE id = ?`}
			args = [][]any{{remaining[0], id}}
		}
		for i, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, s.q(stmt), args[i]...); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) CreateBinding(ctx context.Context, b *ModelBinding, audit *Audit) error {
	b.ID = secrets.NewID()
	b.CreatedAt = Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.prepareTargets(ctx, tx, b); err != nil {
		return err
	}
	providerID, upstreamName, capabilitySet := ownRouting(b)
	if _, err := tx.ExecContext(ctx, s.q(`
		INSERT INTO model_binding (id, alias, provider_id, upstream_name, capability_set, origin, discovered_at, created_at, target_id, hidden, strategy)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		b.ID, b.Alias, providerID, upstreamName,
		capabilitySet, b.Origin, b.DiscoveredAt, b.CreatedAt, b.TargetID, boolInt(b.Hidden), b.Strategy); err != nil {
		return err
	}
	if err := s.writeTargetsTx(ctx, tx, b); err != nil {
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
	if err := s.prepareTargets(ctx, tx, b); err != nil {
		return err
	}
	providerID, upstreamName, capabilitySet := ownRouting(b)
	if _, err := tx.ExecContext(ctx, s.q(`
		UPDATE model_binding SET alias = ?, provider_id = ?, upstream_name = ?, capability_set = ?,
			target_id = ?, hidden = ?, strategy = ? WHERE id = ?`),
		b.Alias, providerID, upstreamName, capabilitySet, b.TargetID, boolInt(b.Hidden), b.Strategy, b.ID); err != nil {
		return err
	}
	if err := s.writeTargetsTx(ctx, tx, b); err != nil {
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
	for _, stmt := range []string{
		`DELETE FROM model_binding_target WHERE binding_id = ?`,
		`DELETE FROM model_binding WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, s.q(stmt), bindingID); err != nil {
			return err
		}
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

// ResolveAlias loads an alias and its servable targets in order: the binding
// itself when it routes through one hop or less, each listed target on an
// enabled provider otherwise. A nil binding means there is nothing to serve.
func (s *Store) ResolveAlias(ctx context.Context, alias string) (*ModelBinding, []ResolvedTarget, error) {
	binding, err := s.GetBindingByAlias(ctx, alias)
	if err != nil || binding == nil {
		return nil, nil, err
	}
	var out []ResolvedTarget
	if binding.Strategy == "" {
		t, err := s.resolveTarget(ctx, binding, 1)
		if err != nil || t == nil {
			return nil, nil, err
		}
		return binding, []ResolvedTarget{*t}, nil
	}
	for _, bt := range binding.Targets {
		target, err := s.GetBindingByID(ctx, bt.ID)
		if err != nil {
			return nil, nil, err
		}
		if target == nil {
			continue
		}
		t, err := s.resolveTarget(ctx, target, bt.Weight)
		if err != nil {
			return nil, nil, err
		}
		if t != nil {
			out = append(out, *t)
		}
	}
	if len(out) == 0 {
		return nil, nil, nil
	}
	return binding, out, nil
}

func (s *Store) resolveTarget(ctx context.Context, b *ModelBinding, weight int) (*ResolvedTarget, error) {
	row := s.db.QueryRowContext(ctx,
		s.q(`SELECT `+providerColumns+` FROM provider WHERE id = ? AND enabled = 1`), b.ProviderID)
	provider, err := scanProvider(row)
	if err != nil || provider == nil {
		return nil, err
	}
	overrides, err := s.ListEndpointOverrides(ctx, provider.ID)
	if err != nil {
		return nil, err
	}
	return &ResolvedTarget{Binding: b, Provider: provider, Overrides: overrides, Weight: weight}, nil
}
