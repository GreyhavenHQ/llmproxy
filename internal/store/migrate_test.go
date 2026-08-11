package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// A fresh database: the base Schema already declares every current column, so
// every migration records itself as a no-op, and a second Init changes
// nothing. No swallowed errors, each version recorded exactly once.
func TestMigrateFreshIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	if err := st.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := st.Init(ctx); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	for _, m := range migrations {
		var n int
		if err := st.db.QueryRow(`SELECT count(*) FROM schema_migration WHERE version = ?`, m.version).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", m.version, err)
		}
		if n != 1 {
			t.Fatalf("version %s recorded %d times, want 1", m.version, n)
		}
	}
}

// An older database, as if restored from a pre-tags/-client dump: usage_event
// lacks the newer columns and api_key still has a revoked_at column with a
// revoked row. Init must add the columns and drop the revoked row.
func TestMigrateUpgradesLegacyDump(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	legacy := []string{
		`CREATE TABLE usage_event (
			id TEXT PRIMARY KEY, ts TEXT NOT NULL, principal_id TEXT NOT NULL,
			api_key_id TEXT NOT NULL, provider_id TEXT NOT NULL, alias TEXT NOT NULL,
			upstream_name TEXT NOT NULL, endpoint TEXT NOT NULL, status_code INTEGER,
			outcome TEXT NOT NULL, cancelled INTEGER NOT NULL DEFAULT 0,
			streamed INTEGER NOT NULL DEFAULT 0, cost DOUBLE PRECISION,
			unpriced INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE api_key (
			id TEXT PRIMARY KEY, principal_id TEXT NOT NULL, key_hash TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
			last_used_at TEXT, revoked_at TEXT)`,
		`INSERT INTO api_key (id, principal_id, key_hash, created_at, revoked_at) VALUES ('k1','p','h1','t', NULL)`,
		`INSERT INTO api_key (id, principal_id, key_hash, created_at, revoked_at) VALUES ('k2','p','h2','t', 't')`,
	}
	for _, stmt := range legacy {
		if _, err := st.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed legacy: %v", err)
		}
	}

	if err := st.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}

	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, col := range []string{"client", "tags"} {
		ok, err := st.columnExists(ctx, tx, "usage_event", col)
		if err != nil {
			t.Fatalf("columnExists %s: %v", col, err)
		}
		if !ok {
			t.Fatalf("usage_event.%s was not added", col)
		}
	}

	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM api_key`).Scan(&n); err != nil {
		t.Fatalf("count api_key: %v", err)
	}
	if n != 1 {
		t.Fatalf("api_key count = %d, want 1 (revoked row deleted)", n)
	}
}
