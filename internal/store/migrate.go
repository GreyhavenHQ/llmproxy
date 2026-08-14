package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// Schema evolution is versioned, not best-effort. Init runs the base Schema
// (CREATE TABLE IF NOT EXISTS, which brings a fresh database fully up to date
// and creates any wholly-missing table on an existing one) and then applies
// every pending migration below in order, recording each in schema_migration
// inside the same transaction. A migration runs at most once per database and
// a failure surfaces as a real error instead of being ignored.
//
// The base Schema constant stays the single readable description of the
// current schema, so every migration here is written to be idempotent against
// it: on a fresh database the column or change already exists and the
// migration records itself as a no-op; on an older database restored from a
// dump it performs the change. Adding a column later therefore means two
// edits: add it to Schema (for fresh installs) and append a migration here
// (to carry existing installs forward).

const schemaMigrationDDL = `
CREATE TABLE IF NOT EXISTS schema_migration (
    version TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
)`

type migration struct {
	version string
	apply   func(ctx context.Context, tx *sql.Tx, s *Store) error
}

// addColumn is the common case: add a column if the table does not already
// have it. Idempotent so it is a no-op on a fresh database whose base Schema
// already declares the column.
func addColumn(version, table, column, definition string) migration {
	return migration{version: version, apply: func(ctx context.Context, tx *sql.Tx, s *Store) error {
		exists, err := s.columnExists(ctx, tx, table, column)
		if err != nil || exists {
			return err
		}
		_, err = tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition)
		return err
	}}
}

// migrations is the ordered, append-only history. Never reorder or edit an
// existing entry; only append. Versions are recorded, so an edited entry that
// already ran on some database would silently diverge.
var migrations = []migration{
	addColumn("001_api_key_key_suffix", "api_key", "key_suffix", "TEXT NOT NULL DEFAULT ''"),
	{version: "002_delete_revoked_api_keys", apply: func(ctx context.Context, tx *sql.Tx, s *Store) error {
		// Revoked keys are deleted outright now; drop any left over from when
		// the column existed. Guarded so a database that never had the column
		// (every fresh install) is a clean no-op rather than an error.
		exists, err := s.columnExists(ctx, tx, "api_key", "revoked_at")
		if err != nil || !exists {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM api_key WHERE revoked_at IS NOT NULL")
		return err
	}},
	addColumn("003_model_binding_target_id", "model_binding", "target_id", "TEXT REFERENCES model_binding(id)"),
	addColumn("004_principal_sessions_revoked_before", "principal", "sessions_revoked_before", "TEXT"),
	addColumn("005_usage_event_client", "usage_event", "client", "TEXT NOT NULL DEFAULT ''"),
	addColumn("006_usage_event_tags", "usage_event", "tags", "TEXT NOT NULL DEFAULT ''"),
	addColumn("007_usage_event_error_kind", "usage_event", "error_kind", "TEXT NOT NULL DEFAULT ''"),
	{version: "008_drop_provider_timeout_write", apply: func(ctx context.Context, tx *sql.Tx, s *Store) error {
		// The setting was stored but never enforced anywhere; dropped rather
		// than left as a lie in the API. Guarded so a fresh database, whose
		// base Schema no longer declares it, is a clean no-op.
		exists, err := s.columnExists(ctx, tx, "provider", "timeout_write")
		if err != nil || !exists {
			return err
		}
		_, err = tx.ExecContext(ctx, "ALTER TABLE provider DROP COLUMN timeout_write")
		return err
	}},
}

// migrate applies every pending migration in order. Each runs in its own
// transaction together with its schema_migration bookkeeping row, so a change
// and the record of it commit atomically.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaMigrationDDL); err != nil {
		return err
	}
	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := m.apply(ctx, tx, s); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx,
			s.q(`INSERT INTO schema_migration (version, applied_at) VALUES (?, ?)`),
			m.version, Now()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: record: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %s: commit: %w", m.version, err)
		}
		slog.Info("applied schema migration", "version", m.version)
	}
	return nil
}

func (s *Store) appliedMigrations(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migration`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// columnExists reports whether table has column, in the current transaction so
// it reflects changes made earlier in the same migration. The table name comes
// from a trusted migration constant, never user input.
func (s *Store) columnExists(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	var n int
	if s.dialect == "postgres" {
		err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM information_schema.columns
			 WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
			table, column).Scan(&n)
		return n > 0, err
	}
	err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('`+table+`') WHERE name = ?`, column).Scan(&n)
	return n > 0, err
}
