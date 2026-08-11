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
	// Bring an existing database created before a column or change existed up
	// to the current schema. Versioned and tracked, not best-effort; see
	// migrate.go.
	return s.migrate(ctx)
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
