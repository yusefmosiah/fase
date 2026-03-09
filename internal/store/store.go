package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/yusefmosiah/cagent/internal/core"
)

var ErrNotFound = errors.New("record not found")

type Store struct {
	db   *sql.DB
	path string
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

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

func (s *Store) CreateSession(ctx context.Context, rec core.SessionRecord) error {
	return s.insertSession(ctx, s.db, rec)
}

func (s *Store) CreateSessionAndJob(ctx context.Context, session core.SessionRecord, job core.JobRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction for session/job create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.insertSession(ctx, tx, session); err != nil {
		return err
	}
	if err := s.insertJob(ctx, tx, job); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session/job create: %w", err)
	}

	return nil
}

func (s *Store) CreateJobAndUpdateSession(ctx context.Context, sessionID string, updatedAt time.Time, job core.JobRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction for job/session update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.insertJob(ctx, tx, job); err != nil {
		return err
	}
	if err := s.updateSessionLatestJob(ctx, tx, sessionID, job.JobID, updatedAt); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit job/session update: %w", err)
	}

	return nil
}

func (s *Store) insertSession(ctx context.Context, db execer, rec core.SessionRecord) error {
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal session metadata: %w", err)
	}

	tags, err := marshalJSON(rec.Tags)
	if err != nil {
		return fmt.Errorf("marshal session tags: %w", err)
	}

	_, err = db.ExecContext(
		ctx,
		`INSERT INTO sessions (
			session_id, label, status, origin_adapter, origin_job_id, cwd,
			created_at, updated_at, latest_job_id, parent_session_id, forked_from_turn_id,
			tags_json, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.SessionID,
		rec.Label,
		rec.Status,
		rec.OriginAdapter,
		rec.OriginJobID,
		rec.CWD,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
		rec.LatestJobID,
		rec.ParentSession,
		rec.ForkedFromTurn,
		string(tags),
		string(metadata),
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}

	return nil
}

func (s *Store) GetSession(ctx context.Context, sessionID string) (core.SessionRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT session_id, label, created_at, updated_at, status, origin_adapter,
		        origin_job_id, cwd, latest_job_id, parent_session_id, forked_from_turn_id,
		        tags_json, metadata_json
		   FROM sessions
		  WHERE session_id = ?`,
		sessionID,
	)

	rec, err := scanSession(row)
	if err != nil {
		return core.SessionRecord{}, err
	}

	return rec, nil
}

func (s *Store) CreateJob(ctx context.Context, rec core.JobRecord) error {
	return s.insertJob(ctx, s.db, rec)
}

func (s *Store) insertJob(ctx context.Context, db execer, rec core.JobRecord) error {
	summary, err := marshalJSON(rec.Summary)
	if err != nil {
		return fmt.Errorf("marshal job summary: %w", err)
	}

	_, err = db.ExecContext(
		ctx,
		`INSERT INTO jobs (
			job_id, session_id, adapter, state, label, native_session_id, cwd,
			created_at, updated_at, finished_at, summary_json, last_raw_artifact
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.JobID,
		rec.SessionID,
		rec.Adapter,
		rec.State,
		rec.Label,
		nullIfEmpty(rec.NativeSessionID),
		rec.CWD,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
		formatTimePtr(rec.FinishedAt),
		string(summary),
		nullIfEmpty(rec.LastRawArtifact),
	)
	if err != nil {
		return fmt.Errorf("insert job: %w", err)
	}

	return nil
}

func (s *Store) UpdateJob(ctx context.Context, rec core.JobRecord) error {
	summary, err := marshalJSON(rec.Summary)
	if err != nil {
		return fmt.Errorf("marshal job summary: %w", err)
	}

	result, err := s.db.ExecContext(
		ctx,
		`UPDATE jobs
		    SET state = ?,
		        label = ?,
		        native_session_id = ?,
		        updated_at = ?,
		        finished_at = ?,
		        summary_json = ?,
		        last_raw_artifact = ?
		  WHERE job_id = ?`,
		rec.State,
		rec.Label,
		nullIfEmpty(rec.NativeSessionID),
		rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
		formatTimePtr(rec.FinishedAt),
		string(summary),
		nullIfEmpty(rec.LastRawArtifact),
		rec.JobID,
	)
	if err != nil {
		return fmt.Errorf("update job: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated job rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: job %s", ErrNotFound, rec.JobID)
	}

	return nil
}

func (s *Store) GetJob(ctx context.Context, jobID string) (core.JobRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT job_id, session_id, adapter, state, label, native_session_id, cwd,
		        created_at, updated_at, finished_at, summary_json, last_raw_artifact
		   FROM jobs
		  WHERE job_id = ?`,
		jobID,
	)

	rec, err := scanJob(row)
	if err != nil {
		return core.JobRecord{}, err
	}

	return rec, nil
}

func (s *Store) ListJobs(ctx context.Context, limit int) ([]core.JobRecord, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT job_id, session_id, adapter, state, label, native_session_id, cwd,
		        created_at, updated_at, finished_at, summary_json, last_raw_artifact
		   FROM jobs
		  ORDER BY created_at DESC
		  LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var jobs []core.JobRecord
	for rows.Next() {
		rec, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}

	return jobs, nil
}

func (s *Store) AppendEvent(ctx context.Context, rec *core.EventRecord) error {
	if len(rec.Payload) == 0 {
		rec.Payload = json.RawMessage(`{}`)
	}

	for attempt := 0; attempt < 3; attempt++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin event transaction: %w", err)
		}

		row := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE job_id = ?`, rec.JobID)
		if err := row.Scan(&rec.Seq); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("query next event sequence: %w", err)
		}

		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO events (
				event_id, job_id, session_id, adapter, seq, ts, kind, phase,
				native_session_id, correlation_id, payload_json, raw_ref, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.EventID,
			rec.JobID,
			rec.SessionID,
			rec.Adapter,
			rec.Seq,
			rec.TS.UTC().Format(time.RFC3339Nano),
			rec.Kind,
			nullIfEmpty(rec.Phase),
			nullIfEmpty(rec.NativeSessionID),
			nullIfEmpty(rec.CorrelationID),
			string(rec.Payload),
			nullIfEmpty(rec.RawRef),
			rec.TS.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			_ = tx.Rollback()
			if retryableSQLiteErr(err) {
				continue
			}
			return fmt.Errorf("insert event: %w", err)
		}

		if err := tx.Commit(); err != nil {
			if retryableSQLiteErr(err) {
				continue
			}
			return fmt.Errorf("commit event insert: %w", err)
		}

		return nil
	}

	return fmt.Errorf("insert event: exhausted retries")
}

func (s *Store) ListEvents(ctx context.Context, jobID string, limit int) ([]core.EventRecord, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT event_id, seq, ts, job_id, session_id, adapter, kind, phase,
		        native_session_id, correlation_id, payload_json, raw_ref
		   FROM events
		  WHERE job_id = ?
		  ORDER BY seq ASC
		  LIMIT ?`,
		jobID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []core.EventRecord
	for rows.Next() {
		rec, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}

	return events, nil
}

func (s *Store) InsertArtifact(ctx context.Context, rec core.ArtifactRecord) error {
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal artifact metadata: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO artifacts (
			artifact_id, job_id, session_id, kind, path, created_at, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.ArtifactID,
		rec.JobID,
		rec.SessionID,
		rec.Kind,
		rec.Path,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		string(metadata),
	)
	if err != nil {
		return fmt.Errorf("insert artifact: %w", err)
	}

	return nil
}

func (s *Store) UpsertNativeSession(ctx context.Context, sessionID, adapter, nativeSessionID string, resumable bool) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO native_sessions (session_id, adapter, native_session_id, resumable)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(session_id, adapter, native_session_id)
		 DO UPDATE SET resumable = excluded.resumable`,
		sessionID,
		adapter,
		nativeSessionID,
		boolToInt(resumable),
	)
	if err != nil {
		return fmt.Errorf("upsert native session: %w", err)
	}

	return nil
}

func (s *Store) UpdateSessionLatestJob(ctx context.Context, sessionID, latestJobID string, updatedAt time.Time) error {
	return s.updateSessionLatestJob(ctx, s.db, sessionID, latestJobID, updatedAt)
}

func (s *Store) updateSessionLatestJob(ctx context.Context, db execer, sessionID, latestJobID string, updatedAt time.Time) error {
	result, err := db.ExecContext(
		ctx,
		`UPDATE sessions
		    SET latest_job_id = ?, updated_at = ?
		  WHERE session_id = ?`,
		latestJobID,
		updatedAt.UTC().Format(time.RFC3339Nano),
		sessionID,
	)
	if err != nil {
		return fmt.Errorf("update session latest job: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated session rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: session %s", ErrNotFound, sessionID)
	}

	return nil
}

func (s *Store) AttachArtifactToEvent(
	ctx context.Context,
	eventID string,
	jobID string,
	rawRef string,
	artifact core.ArtifactRecord,
) error {
	metadata, err := marshalJSON(artifact.Metadata)
	if err != nil {
		return fmt.Errorf("marshal artifact metadata: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin artifact attach transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `UPDATE events SET raw_ref = ? WHERE event_id = ?`, rawRef, eventID)
	if err != nil {
		return fmt.Errorf("update event raw_ref: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated event rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: event %s", ErrNotFound, eventID)
	}

	result, err = tx.ExecContext(
		ctx,
		`UPDATE jobs
		    SET last_raw_artifact = ?, updated_at = ?
		  WHERE job_id = ?`,
		rawRef,
		artifact.CreatedAt.UTC().Format(time.RFC3339Nano),
		jobID,
	)
	if err != nil {
		return fmt.Errorf("update job last_raw_artifact: %w", err)
	}
	rows, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated job rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: job %s", ErrNotFound, jobID)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO artifacts (
			artifact_id, job_id, session_id, kind, path, created_at, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		artifact.ArtifactID,
		artifact.JobID,
		artifact.SessionID,
		artifact.Kind,
		artifact.Path,
		artifact.CreatedAt.UTC().Format(time.RFC3339Nano),
		string(metadata),
	); err != nil {
		return fmt.Errorf("insert artifact: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit artifact attach: %w", err)
	}

	return nil
}

func (s *Store) bootstrap(ctx context.Context) error {
	statements := []string{
		`PRAGMA foreign_keys = ON;`,
		`PRAGMA busy_timeout = 5000;`,
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

func scanSession(scanner interface{ Scan(...any) error }) (core.SessionRecord, error) {
	var rec core.SessionRecord
	var createdAt string
	var updatedAt string
	var label sql.NullString
	var latestJobID sql.NullString
	var parentSession sql.NullString
	var forkedFromTurn sql.NullString
	var tagsJSON string
	var metadataJSON string

	if err := scanner.Scan(
		&rec.SessionID,
		&label,
		&createdAt,
		&updatedAt,
		&rec.Status,
		&rec.OriginAdapter,
		&rec.OriginJobID,
		&rec.CWD,
		&latestJobID,
		&parentSession,
		&forkedFromTurn,
		&tagsJSON,
		&metadataJSON,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.SessionRecord{}, ErrNotFound
		}
		return core.SessionRecord{}, fmt.Errorf("scan session: %w", err)
	}

	rec.Label = label.String
	rec.LatestJobID = latestJobID.String
	rec.ParentSession = stringPtr(parentSession)
	rec.ForkedFromTurn = stringPtr(forkedFromTurn)

	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return core.SessionRecord{}, fmt.Errorf("parse session created_at: %w", err)
	}
	updated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return core.SessionRecord{}, fmt.Errorf("parse session updated_at: %w", err)
	}
	rec.CreatedAt = created
	rec.UpdatedAt = updated

	if err := json.Unmarshal([]byte(tagsJSON), &rec.Tags); err != nil {
		return core.SessionRecord{}, fmt.Errorf("decode session tags: %w", err)
	}
	if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
		return core.SessionRecord{}, fmt.Errorf("decode session metadata: %w", err)
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}

	return rec, nil
}

func scanJob(scanner interface{ Scan(...any) error }) (core.JobRecord, error) {
	var rec core.JobRecord
	var createdAt string
	var updatedAt string
	var finishedAt sql.NullString
	var label sql.NullString
	var nativeSessionID sql.NullString
	var summaryJSON string
	var lastRaw sql.NullString

	if err := scanner.Scan(
		&rec.JobID,
		&rec.SessionID,
		&rec.Adapter,
		&rec.State,
		&label,
		&nativeSessionID,
		&rec.CWD,
		&createdAt,
		&updatedAt,
		&finishedAt,
		&summaryJSON,
		&lastRaw,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.JobRecord{}, ErrNotFound
		}
		return core.JobRecord{}, fmt.Errorf("scan job: %w", err)
	}

	rec.Label = label.String
	rec.NativeSessionID = nativeSessionID.String
	rec.LastRawArtifact = lastRaw.String

	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return core.JobRecord{}, fmt.Errorf("parse job created_at: %w", err)
	}
	updated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return core.JobRecord{}, fmt.Errorf("parse job updated_at: %w", err)
	}
	rec.CreatedAt = created
	rec.UpdatedAt = updated

	if finishedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, finishedAt.String)
		if err != nil {
			return core.JobRecord{}, fmt.Errorf("parse job finished_at: %w", err)
		}
		rec.FinishedAt = &parsed
	}

	if err := json.Unmarshal([]byte(summaryJSON), &rec.Summary); err != nil {
		return core.JobRecord{}, fmt.Errorf("decode job summary: %w", err)
	}
	if rec.Summary == nil {
		rec.Summary = map[string]any{}
	}

	return rec, nil
}

func scanEvent(scanner interface{ Scan(...any) error }) (core.EventRecord, error) {
	var rec core.EventRecord
	var ts string
	var phase sql.NullString
	var nativeSessionID sql.NullString
	var correlationID sql.NullString
	var rawRef sql.NullString
	var payloadJSON string

	if err := scanner.Scan(
		&rec.EventID,
		&rec.Seq,
		&ts,
		&rec.JobID,
		&rec.SessionID,
		&rec.Adapter,
		&rec.Kind,
		&phase,
		&nativeSessionID,
		&correlationID,
		&payloadJSON,
		&rawRef,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.EventRecord{}, ErrNotFound
		}
		return core.EventRecord{}, fmt.Errorf("scan event: %w", err)
	}

	parsedTS, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return core.EventRecord{}, fmt.Errorf("parse event ts: %w", err)
	}

	rec.TS = parsedTS
	rec.Phase = phase.String
	rec.NativeSessionID = nativeSessionID.String
	rec.CorrelationID = correlationID.String
	rec.RawRef = rawRef.String
	rec.Payload = json.RawMessage(payloadJSON)

	return rec, nil
}

func marshalJSON(value any) ([]byte, error) {
	if value == nil {
		return []byte("{}"), nil
	}

	switch typed := value.(type) {
	case []string:
		if typed == nil {
			return []byte("[]"), nil
		}
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	return encoded, nil
}

func formatTimePtr(value *time.Time) any {
	if value == nil {
		return nil
	}

	return value.UTC().Format(time.RFC3339Nano)
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func retryableSQLiteErr(err error) bool {
	if err == nil {
		return false
	}

	text := err.Error()
	return strings.Contains(text, "SQLITE_BUSY") || strings.Contains(text, "UNIQUE constraint failed: events.job_id, events.seq")
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
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
	latest_job_id TEXT,
	parent_session_id TEXT,
	forked_from_turn_id TEXT,
	tags_json TEXT NOT NULL DEFAULT '[]',
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
	summary_json TEXT NOT NULL DEFAULT '{}',
	last_raw_artifact TEXT
);

CREATE TABLE IF NOT EXISTS turns (
	turn_id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
	job_id TEXT NOT NULL REFERENCES jobs(job_id) ON DELETE CASCADE,
	adapter TEXT NOT NULL,
	started_at TEXT NOT NULL,
	completed_at TEXT,
	input_text TEXT NOT NULL,
	input_source TEXT NOT NULL,
	result_summary TEXT,
	status TEXT NOT NULL,
	native_session_id TEXT,
	stats_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS native_sessions (
	session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
	adapter TEXT NOT NULL,
	native_session_id TEXT NOT NULL,
	resumable INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (session_id, adapter, native_session_id)
);

CREATE TABLE IF NOT EXISTS handoffs (
	handoff_id TEXT PRIMARY KEY,
	job_id TEXT NOT NULL REFERENCES jobs(job_id) ON DELETE CASCADE,
	session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,
	packet_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS artifacts (
	artifact_id TEXT PRIMARY KEY,
	job_id TEXT NOT NULL REFERENCES jobs(job_id) ON DELETE CASCADE,
	session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
	kind TEXT NOT NULL,
	path TEXT NOT NULL,
	created_at TEXT NOT NULL,
	metadata_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS locks (
	lock_key TEXT PRIMARY KEY,
	adapter TEXT NOT NULL,
	native_session_id TEXT NOT NULL,
	job_id TEXT NOT NULL REFERENCES jobs(job_id) ON DELETE CASCADE,
	acquired_at TEXT NOT NULL,
	expires_at TEXT
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
CREATE UNIQUE INDEX IF NOT EXISTS idx_events_job_seq_unique ON events(job_id, seq);
CREATE INDEX IF NOT EXISTS idx_events_job_id_seq ON events(job_id, seq);
CREATE INDEX IF NOT EXISTS idx_events_session_id_ts ON events(session_id, ts);
CREATE INDEX IF NOT EXISTS idx_artifacts_job_id ON artifacts(job_id);
`
