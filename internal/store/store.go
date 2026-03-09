package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string
}

func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	store := &Store{db: db, path: path}
	if err := store.bootstrap(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) bootstrap(ctx context.Context) error {
	statements := []string{
		`PRAGMA foreign_keys = ON;`,
		`PRAGMA journal_mode = WAL;`,
		schema,
	}

	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply sqlite bootstrap statement: %w", err)
		}
	}

	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	session_id TEXT PRIMARY KEY,
	label TEXT,
	status TEXT NOT NULL,
	origin_adapter TEXT NOT NULL,
	origin_job_id TEXT NOT NULL,
	cwd TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	metadata_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS jobs (
	job_id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
	adapter TEXT NOT NULL,
	state TEXT NOT NULL,
	label TEXT,
	native_session_id TEXT,
	cwd TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	finished_at TEXT,
	summary_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS events (
	event_id TEXT PRIMARY KEY,
	job_id TEXT NOT NULL REFERENCES jobs(job_id) ON DELETE CASCADE,
	session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
	adapter TEXT NOT NULL,
	seq INTEGER NOT NULL,
	ts TEXT NOT NULL,
	kind TEXT NOT NULL,
	phase TEXT,
	native_session_id TEXT,
	correlation_id TEXT,
	payload_json TEXT NOT NULL DEFAULT '{}',
	raw_ref TEXT,
	created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_jobs_session_id ON jobs(session_id);
CREATE INDEX IF NOT EXISTS idx_events_job_id_seq ON events(job_id, seq);
CREATE INDEX IF NOT EXISTS idx_events_session_id_ts ON events(session_id, ts);
`
