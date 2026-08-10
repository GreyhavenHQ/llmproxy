// Package store is the authoritative persistence layer: SQLite (pure Go
// driver, no CGO) by default, Postgres via a postgres:// URL. All state that
// matters lives here; caches elsewhere are derived and short-lived.
package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	dialect string // "sqlite" | "postgres"
}

func Open(databaseURL string) (*Store, error) {
	var db *sql.DB
	var err error
	dialect := "sqlite"
	switch {
	case strings.HasPrefix(databaseURL, "postgres://"), strings.HasPrefix(databaseURL, "postgresql://"):
		dialect = "postgres"
		db, err = sql.Open("pgx", databaseURL)
		if err == nil {
			db.SetMaxOpenConns(20)
		}
	default:
		path := strings.TrimPrefix(databaseURL, "sqlite://")
		dsn := "file:" + path +
			"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=synchronous(NORMAL)"
		db, err = sql.Open("sqlite", dsn)
		if err == nil {
			db.SetMaxOpenConns(8)
		}
	}
	if err != nil {
		return nil, err
	}
	return &Store{db: db, dialect: dialect}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Init(ctx context.Context) error {
	for _, stmt := range strings.Split(Schema, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	// Best-effort upgrades for databases created before a column existed.
	// CREATE TABLE IF NOT EXISTS does not alter existing tables, so additive
	// changes are applied here; the errors ("duplicate column", "no such
	// column") are the normal case on fresh or already-upgraded databases.
	for _, stmt := range []string{
		`ALTER TABLE api_key ADD COLUMN key_suffix TEXT NOT NULL DEFAULT ''`,
		`DELETE FROM api_key WHERE revoked_at IS NOT NULL`, // revoked keys are now deleted outright
		`ALTER TABLE model_binding ADD COLUMN target_id TEXT REFERENCES model_binding(id)`,
		`ALTER TABLE principal ADD COLUMN sessions_revoked_before TEXT`,
		`ALTER TABLE usage_event ADD COLUMN client TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE usage_event ADD COLUMN tags TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = s.db.ExecContext(ctx, stmt)
	}
	return nil
}

// q rewrites ? placeholders to $n for Postgres.
func (s *Store) q(query string) string {
	if s.dialect != "postgres" {
		return query
	}
	var b strings.Builder
	n := 0
	for _, ch := range query {
		if ch == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
		} else {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// TimeFormat is the canonical timestamp format: UTC, fixed width,
// lexicographically sortable, identical across both backends (columns are TEXT).
const TimeFormat = "2006-01-02T15:04:05.000000Z"

func Now() string {
	return time.Now().UTC().Format(TimeFormat)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
