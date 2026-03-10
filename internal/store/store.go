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
	return s.ListJobsFiltered(ctx, limit, "", "", "")
}

func (s *Store) ListJobsFiltered(ctx context.Context, limit int, adapter, state, sessionID string) ([]core.JobRecord, error) {
	if limit <= 0 {
		limit = 50
	}

	var (
		clauses []string
		args    []any
	)
	if adapter != "" {
		clauses = append(clauses, "adapter = ?")
		args = append(args, adapter)
	}
	if state != "" {
		clauses = append(clauses, "state = ?")
		args = append(args, state)
	}
	if sessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, sessionID)
	}

	query := `SELECT job_id, session_id, adapter, state, label, native_session_id, cwd,
		        created_at, updated_at, finished_at, summary_json, last_raw_artifact
		   FROM jobs`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(
		ctx,
		query,
		args...,
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

func (s *Store) ListSessions(ctx context.Context, limit int, adapter, status string) ([]core.SessionRecord, error) {
	if limit <= 0 {
		limit = 50
	}

	var (
		clauses []string
		args    []any
	)
	if adapter != "" {
		clauses = append(clauses, "origin_adapter = ?")
		args = append(args, adapter)
	}
	if status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, status)
	}

	query := `SELECT session_id, label, created_at, updated_at, status, origin_adapter,
		        origin_job_id, cwd, latest_job_id, parent_session_id, forked_from_turn_id,
		        tags_json, metadata_json
		   FROM sessions`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sessions []core.SessionRecord
	for rows.Next() {
		rec, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}

	return sessions, nil
}

func (s *Store) ListJobsBySession(ctx context.Context, sessionID string, limit int) ([]core.JobRecord, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT job_id, session_id, adapter, state, label, native_session_id, cwd,
		        created_at, updated_at, finished_at, summary_json, last_raw_artifact
		   FROM jobs
		  WHERE session_id = ?
		  ORDER BY created_at DESC
		  LIMIT ?`,
		sessionID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query jobs by session: %w", err)
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
		return nil, fmt.Errorf("iterate jobs by session: %w", err)
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
	return s.ListEventsAfter(ctx, jobID, 0, limit)
}

func (s *Store) ListEventsAfter(ctx context.Context, jobID string, afterSeq int64, limit int) ([]core.EventRecord, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT event_id, seq, ts, job_id, session_id, adapter, kind, phase,
		        native_session_id, correlation_id, payload_json, raw_ref
		   FROM events
		  WHERE job_id = ?
		    AND seq > ?
		  ORDER BY seq ASC
		  LIMIT ?`,
		jobID,
		afterSeq,
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

func (s *Store) ListEventsBySession(ctx context.Context, sessionID string, limit int) ([]core.EventRecord, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT event_id, seq, ts, job_id, session_id, adapter, kind, phase,
		        native_session_id, correlation_id, payload_json, raw_ref
		   FROM events
		  WHERE session_id = ?
		  ORDER BY ts DESC, seq DESC
		  LIMIT ?`,
		sessionID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query events by session: %w", err)
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
		return nil, fmt.Errorf("iterate events by session: %w", err)
	}

	return events, nil
}

func (s *Store) ListArtifactsByJob(ctx context.Context, jobID string, limit int) ([]core.ArtifactRecord, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT artifact_id, job_id, session_id, kind, path, created_at, metadata_json
		   FROM artifacts
		  WHERE job_id = ?
		  ORDER BY created_at DESC
		  LIMIT ?`,
		jobID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var artifacts []core.ArtifactRecord
	for rows.Next() {
		rec, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifacts: %w", err)
	}

	return artifacts, nil
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

func (s *Store) ListArtifactsBySession(ctx context.Context, sessionID string, limit int) ([]core.ArtifactRecord, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT artifact_id, job_id, session_id, kind, path, created_at, metadata_json
		   FROM artifacts
		  WHERE session_id = ?
		  ORDER BY created_at DESC
		  LIMIT ?`,
		sessionID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query artifacts by session: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var artifacts []core.ArtifactRecord
	for rows.Next() {
		rec, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifacts by session: %w", err)
	}

	return artifacts, nil
}

func (s *Store) UpsertNativeSession(ctx context.Context, rec core.NativeSessionRecord) error {
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal native session metadata: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO native_sessions (session_id, adapter, native_session_id, resumable, metadata_json)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, adapter, native_session_id)
		 DO UPDATE SET resumable = excluded.resumable, metadata_json = excluded.metadata_json`,
		rec.SessionID,
		rec.Adapter,
		rec.NativeSessionID,
		boolToInt(rec.Resumable),
		string(metadata),
	)
	if err != nil {
		return fmt.Errorf("upsert native session: %w", err)
	}

	return nil
}

func (s *Store) ListNativeSessions(ctx context.Context, sessionID string) ([]core.NativeSessionRecord, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT ns.session_id, ns.adapter, ns.native_session_id, ns.resumable, ns.metadata_json,
		        l.job_id, l.acquired_at, l.expires_at
		   FROM native_sessions ns
		   LEFT JOIN locks l
		     ON l.lock_key = ('native:' || ns.adapter || ':' || ns.native_session_id)
		  WHERE ns.session_id = ?
		  ORDER BY ns.adapter ASC, ns.native_session_id ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query native sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sessions []core.NativeSessionRecord
	for rows.Next() {
		rec, err := scanNativeSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate native sessions: %w", err)
	}

	return sessions, nil
}

func (s *Store) CreateTurn(ctx context.Context, rec core.TurnRecord) error {
	stats, err := marshalJSON(rec.Stats)
	if err != nil {
		return fmt.Errorf("marshal turn stats: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO turns (
			turn_id, session_id, job_id, adapter, started_at, completed_at,
			input_text, input_source, result_summary, status, native_session_id, stats_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.TurnID,
		rec.SessionID,
		rec.JobID,
		rec.Adapter,
		rec.StartedAt.UTC().Format(time.RFC3339Nano),
		formatTimePtr(rec.CompletedAt),
		rec.InputText,
		rec.InputSource,
		nullIfEmpty(rec.ResultSummary),
		rec.Status,
		nullIfEmpty(rec.NativeSessionID),
		string(stats),
	)
	if err != nil {
		return fmt.Errorf("insert turn: %w", err)
	}

	return nil
}

func (s *Store) UpdateTurn(ctx context.Context, rec core.TurnRecord) error {
	stats, err := marshalJSON(rec.Stats)
	if err != nil {
		return fmt.Errorf("marshal turn stats: %w", err)
	}

	result, err := s.db.ExecContext(
		ctx,
		`UPDATE turns
		    SET completed_at = ?,
		        result_summary = ?,
		        status = ?,
		        native_session_id = ?,
		        stats_json = ?
		  WHERE turn_id = ?`,
		formatTimePtr(rec.CompletedAt),
		nullIfEmpty(rec.ResultSummary),
		rec.Status,
		nullIfEmpty(rec.NativeSessionID),
		string(stats),
		rec.TurnID,
	)
	if err != nil {
		return fmt.Errorf("update turn: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated turn rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: turn %s", ErrNotFound, rec.TurnID)
	}

	return nil
}

func (s *Store) GetTurn(ctx context.Context, turnID string) (core.TurnRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT turn_id, session_id, job_id, adapter, started_at, completed_at,
		        input_text, input_source, result_summary, status, native_session_id, stats_json
		   FROM turns
		  WHERE turn_id = ?`,
		turnID,
	)

	return scanTurn(row)
}

func (s *Store) ListTurnsBySession(ctx context.Context, sessionID string, limit int) ([]core.TurnRecord, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT turn_id, session_id, job_id, adapter, started_at, completed_at,
		        input_text, input_source, result_summary, status, native_session_id, stats_json
		   FROM turns
		  WHERE session_id = ?
		  ORDER BY started_at DESC
		  LIMIT ?`,
		sessionID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var turns []core.TurnRecord
	for rows.Next() {
		rec, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		turns = append(turns, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate turns: %w", err)
	}

	return turns, nil
}

func (s *Store) FindActiveJobBySession(ctx context.Context, sessionID string) (*core.JobRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT job_id, session_id, adapter, state, label, native_session_id, cwd,
		        created_at, updated_at, finished_at, summary_json, last_raw_artifact
		   FROM jobs
		  WHERE session_id = ?
		    AND state NOT IN ('completed', 'failed', 'cancelled', 'blocked')
		  ORDER BY created_at DESC
		  LIMIT 1`,
		sessionID,
	)

	rec, err := scanJob(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}

	return &rec, nil
}

func (s *Store) AcquireLock(ctx context.Context, rec core.LockRecord) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO locks (lock_key, adapter, native_session_id, job_id, acquired_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		rec.LockKey,
		rec.Adapter,
		rec.NativeSessionID,
		rec.JobID,
		rec.AcquiredAt.UTC().Format(time.RFC3339Nano),
		formatTimePtr(rec.ExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("insert lock: %w", err)
	}

	return nil
}

func (s *Store) GetLock(ctx context.Context, lockKey string) (core.LockRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT lock_key, adapter, native_session_id, job_id, acquired_at, expires_at
		   FROM locks
		  WHERE lock_key = ?`,
		lockKey,
	)

	return scanLock(row)
}

func (s *Store) UpsertJobRuntime(ctx context.Context, rec core.JobRuntimeRecord) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO job_runtime (
			job_id, supervisor_pid, vendor_pid, detached,
			started_at, updated_at, cancel_requested_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET
			supervisor_pid = excluded.supervisor_pid,
			vendor_pid = excluded.vendor_pid,
			detached = excluded.detached,
			updated_at = excluded.updated_at,
			cancel_requested_at = excluded.cancel_requested_at,
			completed_at = excluded.completed_at`,
		rec.JobID,
		nullIfZero(rec.SupervisorPID),
		nullIfZero(rec.VendorPID),
		boolToInt(rec.Detached),
		rec.StartedAt.UTC().Format(time.RFC3339Nano),
		rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
		formatTimePtr(rec.CancelRequestedAt),
		formatTimePtr(rec.CompletedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert job runtime: %w", err)
	}
	return nil
}

func (s *Store) GetJobRuntime(ctx context.Context, jobID string) (core.JobRuntimeRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT job_id, supervisor_pid, vendor_pid, detached, started_at, updated_at, cancel_requested_at, completed_at
		   FROM job_runtime
		  WHERE job_id = ?`,
		jobID,
	)
	return scanJobRuntime(row)
}

func (s *Store) ReleaseLock(ctx context.Context, lockKey, jobID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM locks WHERE lock_key = ? AND job_id = ?`, lockKey, jobID)
	if err != nil {
		return fmt.Errorf("delete lock: %w", err)
	}
	return nil
}

func (s *Store) CreateTransfer(ctx context.Context, rec core.TransferRecord) error {
	packet, err := marshalJSON(rec.Packet)
	if err != nil {
		return fmt.Errorf("marshal transfer packet: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO handoffs (handoff_id, job_id, session_id, created_at, packet_json)
		 VALUES (?, ?, ?, ?, ?)`,
		rec.TransferID,
		rec.JobID,
		rec.SessionID,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		string(packet),
	)
	if err != nil {
		return fmt.Errorf("insert transfer: %w", err)
	}

	return nil
}

func (s *Store) GetTransfer(ctx context.Context, transferID string) (core.TransferRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT handoff_id, job_id, session_id, created_at, packet_json
		   FROM handoffs
		  WHERE handoff_id = ?`,
		transferID,
	)

	return scanTransfer(row)
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

	migrations := []string{
		`ALTER TABLE native_sessions ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}'`,
	}
	for _, stmt := range migrations {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil && !ignorableSQLiteMigrationErr(err) {
			return fmt.Errorf("apply sqlite migration: %w", err)
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

func scanArtifact(scanner interface{ Scan(...any) error }) (core.ArtifactRecord, error) {
	var rec core.ArtifactRecord
	var createdAt string
	var metadataJSON string

	if err := scanner.Scan(
		&rec.ArtifactID,
		&rec.JobID,
		&rec.SessionID,
		&rec.Kind,
		&rec.Path,
		&createdAt,
		&metadataJSON,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.ArtifactRecord{}, ErrNotFound
		}
		return core.ArtifactRecord{}, fmt.Errorf("scan artifact: %w", err)
	}

	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return core.ArtifactRecord{}, fmt.Errorf("parse artifact created_at: %w", err)
	}
	rec.CreatedAt = parsed
	if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
		return core.ArtifactRecord{}, fmt.Errorf("decode artifact metadata: %w", err)
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}

	return rec, nil
}

func scanTransfer(scanner interface{ Scan(...any) error }) (core.TransferRecord, error) {
	var rec core.TransferRecord
	var createdAt string
	var packetJSON string

	if err := scanner.Scan(&rec.TransferID, &rec.JobID, &rec.SessionID, &createdAt, &packetJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.TransferRecord{}, ErrNotFound
		}
		return core.TransferRecord{}, fmt.Errorf("scan transfer: %w", err)
	}

	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return core.TransferRecord{}, fmt.Errorf("parse transfer created_at: %w", err)
	}
	rec.CreatedAt = parsed
	if err := json.Unmarshal([]byte(packetJSON), &rec.Packet); err != nil {
		return core.TransferRecord{}, fmt.Errorf("decode transfer packet: %w", err)
	}

	return rec, nil
}

func scanTurn(scanner interface{ Scan(...any) error }) (core.TurnRecord, error) {
	var rec core.TurnRecord
	var startedAt string
	var completedAt sql.NullString
	var resultSummary sql.NullString
	var nativeSessionID sql.NullString
	var statsJSON string

	if err := scanner.Scan(
		&rec.TurnID,
		&rec.SessionID,
		&rec.JobID,
		&rec.Adapter,
		&startedAt,
		&completedAt,
		&rec.InputText,
		&rec.InputSource,
		&resultSummary,
		&rec.Status,
		&nativeSessionID,
		&statsJSON,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.TurnRecord{}, ErrNotFound
		}
		return core.TurnRecord{}, fmt.Errorf("scan turn: %w", err)
	}

	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return core.TurnRecord{}, fmt.Errorf("parse turn started_at: %w", err)
	}
	rec.StartedAt = started
	rec.ResultSummary = resultSummary.String
	rec.NativeSessionID = nativeSessionID.String

	if completedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, completedAt.String)
		if err != nil {
			return core.TurnRecord{}, fmt.Errorf("parse turn completed_at: %w", err)
		}
		rec.CompletedAt = &parsed
	}

	if err := json.Unmarshal([]byte(statsJSON), &rec.Stats); err != nil {
		return core.TurnRecord{}, fmt.Errorf("decode turn stats: %w", err)
	}
	if rec.Stats == nil {
		rec.Stats = map[string]any{}
	}

	return rec, nil
}

func scanNativeSession(scanner interface{ Scan(...any) error }) (core.NativeSessionRecord, error) {
	var rec core.NativeSessionRecord
	var resumable int
	var metadataJSON string
	var lockedByJobID sql.NullString
	var lockedAt sql.NullString
	var lockExpiresAt sql.NullString

	if err := scanner.Scan(
		&rec.SessionID,
		&rec.Adapter,
		&rec.NativeSessionID,
		&resumable,
		&metadataJSON,
		&lockedByJobID,
		&lockedAt,
		&lockExpiresAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.NativeSessionRecord{}, ErrNotFound
		}
		return core.NativeSessionRecord{}, fmt.Errorf("scan native session: %w", err)
	}

	rec.Resumable = resumable != 0
	rec.LockedByJobID = lockedByJobID.String
	if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
		return core.NativeSessionRecord{}, fmt.Errorf("decode native session metadata: %w", err)
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	if lockedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, lockedAt.String)
		if err != nil {
			return core.NativeSessionRecord{}, fmt.Errorf("parse native session locked_at: %w", err)
		}
		rec.LockedAt = &parsed
	}
	if lockExpiresAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, lockExpiresAt.String)
		if err != nil {
			return core.NativeSessionRecord{}, fmt.Errorf("parse native session lock_expires_at: %w", err)
		}
		rec.LockExpiresAt = &parsed
	}

	return rec, nil
}

func scanLock(scanner interface{ Scan(...any) error }) (core.LockRecord, error) {
	var rec core.LockRecord
	var acquiredAt string
	var expiresAt sql.NullString

	if err := scanner.Scan(
		&rec.LockKey,
		&rec.Adapter,
		&rec.NativeSessionID,
		&rec.JobID,
		&acquiredAt,
		&expiresAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.LockRecord{}, ErrNotFound
		}
		return core.LockRecord{}, fmt.Errorf("scan lock: %w", err)
	}

	parsed, err := time.Parse(time.RFC3339Nano, acquiredAt)
	if err != nil {
		return core.LockRecord{}, fmt.Errorf("parse lock acquired_at: %w", err)
	}
	rec.AcquiredAt = parsed
	if expiresAt.Valid {
		parsedExpiresAt, err := time.Parse(time.RFC3339Nano, expiresAt.String)
		if err != nil {
			return core.LockRecord{}, fmt.Errorf("parse lock expires_at: %w", err)
		}
		rec.ExpiresAt = &parsedExpiresAt
	}

	return rec, nil
}

func scanJobRuntime(scanner interface{ Scan(...any) error }) (core.JobRuntimeRecord, error) {
	var rec core.JobRuntimeRecord
	var supervisorPID sql.NullInt64
	var vendorPID sql.NullInt64
	var detached int
	var startedAt string
	var updatedAt string
	var cancelRequestedAt sql.NullString
	var completedAt sql.NullString

	if err := scanner.Scan(
		&rec.JobID,
		&supervisorPID,
		&vendorPID,
		&detached,
		&startedAt,
		&updatedAt,
		&cancelRequestedAt,
		&completedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.JobRuntimeRecord{}, ErrNotFound
		}
		return core.JobRuntimeRecord{}, fmt.Errorf("scan job runtime: %w", err)
	}

	rec.SupervisorPID = int(supervisorPID.Int64)
	rec.VendorPID = int(vendorPID.Int64)
	rec.Detached = detached != 0

	parsedStartedAt, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return core.JobRuntimeRecord{}, fmt.Errorf("parse job runtime started_at: %w", err)
	}
	parsedUpdatedAt, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return core.JobRuntimeRecord{}, fmt.Errorf("parse job runtime updated_at: %w", err)
	}
	rec.StartedAt = parsedStartedAt
	rec.UpdatedAt = parsedUpdatedAt

	if cancelRequestedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, cancelRequestedAt.String)
		if err != nil {
			return core.JobRuntimeRecord{}, fmt.Errorf("parse job runtime cancel_requested_at: %w", err)
		}
		rec.CancelRequestedAt = &parsed
	}
	if completedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, completedAt.String)
		if err != nil {
			return core.JobRuntimeRecord{}, fmt.Errorf("parse job runtime completed_at: %w", err)
		}
		rec.CompletedAt = &parsed
	}

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

func nullIfZero(value int) any {
	if value == 0 {
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

func ignorableSQLiteMigrationErr(err error) bool {
	if err == nil {
		return false
	}

	text := err.Error()
	return strings.Contains(text, "duplicate column name")
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
	metadata_json TEXT NOT NULL DEFAULT '{}',
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

CREATE TABLE IF NOT EXISTS job_runtime (
	job_id TEXT PRIMARY KEY REFERENCES jobs(job_id) ON DELETE CASCADE,
	supervisor_pid INTEGER,
	vendor_pid INTEGER,
	detached INTEGER NOT NULL DEFAULT 0,
	started_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	cancel_requested_at TEXT,
	completed_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_jobs_session_id ON jobs(session_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_events_job_seq_unique ON events(job_id, seq);
CREATE INDEX IF NOT EXISTS idx_events_job_id_seq ON events(job_id, seq);
CREATE INDEX IF NOT EXISTS idx_events_session_id_ts ON events(session_id, ts);
CREATE INDEX IF NOT EXISTS idx_artifacts_job_id ON artifacts(job_id);
`
