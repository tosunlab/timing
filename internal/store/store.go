package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound       = errors.New("session not found")
	ErrAmbiguous      = errors.New("session prefix is ambiguous")
	ErrAlreadyStopped = errors.New("session already stopped")
	ErrClosed         = errors.New("session is closed")
	ErrInvalid        = errors.New("invalid value")
)

// Append-only: a migration's index is its schema version, stored in PRAGMA user_version.
var migrations = []string{
	`CREATE TABLE sessions (
	  id         TEXT PRIMARY KEY,
	  project    TEXT NOT NULL,
	  agent      TEXT NOT NULL,
	  task       TEXT NOT NULL DEFAULT '',
	  started_at TEXT NOT NULL,
	  ended_at   TEXT,
	  CHECK (ended_at IS NULL OR ended_at >= started_at)
	);
	CREATE INDEX sessions_project_started ON sessions(project, started_at);
	CREATE TABLE token_usage (
	  id          INTEGER PRIMARY KEY,
	  session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	  model       TEXT NOT NULL DEFAULT '',
	  input       INTEGER NOT NULL DEFAULT 0 CHECK (input >= 0),
	  output      INTEGER NOT NULL DEFAULT 0 CHECK (output >= 0),
	  cache_read  INTEGER NOT NULL DEFAULT 0 CHECK (cache_read >= 0),
	  cache_write INTEGER NOT NULL DEFAULT 0 CHECK (cache_write >= 0),
	  recorded_at TEXT NOT NULL
	);
	CREATE INDEX token_usage_session ON token_usage(session_id);`,
	// SQLite allows many NULLs in a unique index, so manual usage rows (NULL source_id) never conflict.
	`ALTER TABLE sessions ADD COLUMN external_id TEXT NOT NULL DEFAULT '';
	CREATE INDEX sessions_external ON sessions(external_id, started_at);
	ALTER TABLE token_usage ADD COLUMN source_id TEXT;
	CREATE UNIQUE INDEX token_usage_source ON token_usage(source_id);`,
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	// Pragmas live in the DSN because they are per connection and database/sql pools connections.
	// _txlock=immediate takes the write lock at BEGIN, so busy_timeout can wait for it instead of
	// a read-then-write transaction failing with SQLITE_BUSY on upgrade.
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", abs, err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	return s.tx(func(tx *sql.Tx) error {
		var v int
		if err := tx.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
			return err
		}
		if v > len(migrations) {
			return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", v, len(migrations))
		}
		for i := v; i < len(migrations); i++ {
			if _, err := tx.Exec(migrations[i]); err != nil {
				return fmt.Errorf("migration %d: %w", i+1, err)
			}
			if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) tx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
