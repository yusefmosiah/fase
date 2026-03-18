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

var (
	ErrNotFound = errors.New("record not found")
	ErrBusy     = errors.New("record busy")
)

type Store struct {
	db          *sql.DB
	path        string
	privateDB   *sql.DB
	privatePath string
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

// OpenWithPrivate opens both the public and private databases.
func OpenWithPrivate(ctx context.Context, publicPath, privatePath string) (*Store, error) {
	store, err := Open(ctx, publicPath)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(privatePath), 0o755); err != nil {
		_ = store.db.Close()
		return nil, fmt.Errorf("create private store directory: %w", err)
	}

	pdb, err := sql.Open("sqlite", privatePath)
	if err != nil {
		_ = store.db.Close()
		return nil, fmt.Errorf("open private sqlite database: %w", err)
	}
	pdb.SetMaxOpenConns(1)
	pdb.SetMaxIdleConns(1)

	store.privateDB = pdb
	store.privatePath = privatePath

	if err := store.bootstrapPrivate(ctx); err != nil {
		_ = pdb.Close()
		_ = store.db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() error {
	if s.privateDB != nil {
		_ = s.privateDB.Close()
	}
	return s.db.Close()
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) HasPrivate() bool {
	return s.privateDB != nil
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
			job_id, session_id, work_id, adapter, state, label, native_session_id, cwd,
			created_at, updated_at, finished_at, summary_json, last_raw_artifact
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.JobID,
		rec.SessionID,
		nullIfEmpty(rec.WorkID),
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
		    SET work_id = ?,
		        state = ?,
		        label = ?,
		        native_session_id = ?,
		        updated_at = ?,
		        finished_at = ?,
		        summary_json = ?,
		        last_raw_artifact = ?
		  WHERE job_id = ?`,
		nullIfEmpty(rec.WorkID),
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
		`SELECT job_id, session_id, work_id, adapter, state, label, native_session_id, cwd,
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

	query := `SELECT job_id, session_id, work_id, adapter, state, label, native_session_id, cwd,
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

func (s *Store) CreateWorkItem(ctx context.Context, rec core.WorkItemRecord) error {
	required, err := marshalJSON(rec.RequiredCapabilities)
	if err != nil {
		return fmt.Errorf("marshal work required capabilities: %w", err)
	}
	requiredModelTraits, err := marshalJSON(rec.RequiredModelTraits)
	if err != nil {
		return fmt.Errorf("marshal work required model traits: %w", err)
	}
	preferred, err := marshalJSON(rec.PreferredAdapters)
	if err != nil {
		return fmt.Errorf("marshal work preferred adapters: %w", err)
	}
	forbidden, err := marshalJSON(rec.ForbiddenAdapters)
	if err != nil {
		return fmt.Errorf("marshal work forbidden adapters: %w", err)
	}
	preferredModels, err := marshalJSON(rec.PreferredModels)
	if err != nil {
		return fmt.Errorf("marshal work preferred models: %w", err)
	}
	avoidModels, err := marshalJSON(rec.AvoidModels)
	if err != nil {
		return fmt.Errorf("marshal work avoid models: %w", err)
	}
	requiredAttestations, err := marshalJSON(rec.RequiredAttestations)
	if err != nil {
		return fmt.Errorf("marshal work required attestations: %w", err)
	}
	acceptance, err := marshalJSON(rec.Acceptance)
	if err != nil {
		return fmt.Errorf("marshal work acceptance: %w", err)
	}
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal work metadata: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO work_items (
			work_id, title, objective, kind, execution_state, approval_state, lock_state, phase,
			priority, configuration_class, budget_class, required_capabilities_json, required_model_traits_json,
			preferred_adapters_json, forbidden_adapters_json, preferred_models_json, avoid_models_json,
			required_attestations_json, acceptance_json, metadata_json, head_commit_oid, attestation_frozen_at,
			current_job_id, current_session_id, claimed_by, claimed_until, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.WorkID,
		rec.Title,
		rec.Objective,
		rec.Kind,
		rec.ExecutionState,
		rec.ApprovalState,
		rec.LockState,
		nullIfEmpty(rec.Phase),
		rec.Priority,
		nullIfEmpty(rec.ConfigurationClass),
		nullIfEmpty(rec.BudgetClass),
		string(required),
		string(requiredModelTraits),
		string(preferred),
		string(forbidden),
		string(preferredModels),
		string(avoidModels),
		string(requiredAttestations),
		string(acceptance),
		string(metadata),
		nullIfEmpty(rec.HeadCommitOID),
		formatTimePtr(rec.AttestationFrozenAt),
		nullIfEmpty(rec.CurrentJobID),
		nullIfEmpty(rec.CurrentSessionID),
		nullIfEmpty(rec.ClaimedBy),
		formatTimePtr(rec.ClaimedUntil),
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert work item: %w", err)
	}

	return nil
}

func (s *Store) UpdateWorkItem(ctx context.Context, rec core.WorkItemRecord) error {
	required, err := marshalJSON(rec.RequiredCapabilities)
	if err != nil {
		return fmt.Errorf("marshal work required capabilities: %w", err)
	}
	requiredModelTraits, err := marshalJSON(rec.RequiredModelTraits)
	if err != nil {
		return fmt.Errorf("marshal work required model traits: %w", err)
	}
	preferred, err := marshalJSON(rec.PreferredAdapters)
	if err != nil {
		return fmt.Errorf("marshal work preferred adapters: %w", err)
	}
	forbidden, err := marshalJSON(rec.ForbiddenAdapters)
	if err != nil {
		return fmt.Errorf("marshal work forbidden adapters: %w", err)
	}
	preferredModels, err := marshalJSON(rec.PreferredModels)
	if err != nil {
		return fmt.Errorf("marshal work preferred models: %w", err)
	}
	avoidModels, err := marshalJSON(rec.AvoidModels)
	if err != nil {
		return fmt.Errorf("marshal work avoid models: %w", err)
	}
	requiredAttestations, err := marshalJSON(rec.RequiredAttestations)
	if err != nil {
		return fmt.Errorf("marshal work required attestations: %w", err)
	}
	acceptance, err := marshalJSON(rec.Acceptance)
	if err != nil {
		return fmt.Errorf("marshal work acceptance: %w", err)
	}
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal work metadata: %w", err)
	}

	result, err := s.db.ExecContext(
		ctx,
		`UPDATE work_items
		    SET title = ?,
		        objective = ?,
		        kind = ?,
		        execution_state = ?,
		        approval_state = ?,
		        lock_state = ?,
		        phase = ?,
		        priority = ?,
		        configuration_class = ?,
		        budget_class = ?,
		        required_capabilities_json = ?,
		        required_model_traits_json = ?,
		        preferred_adapters_json = ?,
		        forbidden_adapters_json = ?,
		        preferred_models_json = ?,
		        avoid_models_json = ?,
		        required_attestations_json = ?,
		        acceptance_json = ?,
		        metadata_json = ?,
		        head_commit_oid = ?,
		        attestation_frozen_at = ?,
		        current_job_id = ?,
		        current_session_id = ?,
		        claimed_by = ?,
		        claimed_until = ?,
		        updated_at = ?
		  WHERE work_id = ?`,
		rec.Title,
		rec.Objective,
		rec.Kind,
		rec.ExecutionState,
		rec.ApprovalState,
		rec.LockState,
		nullIfEmpty(rec.Phase),
		rec.Priority,
		nullIfEmpty(rec.ConfigurationClass),
		nullIfEmpty(rec.BudgetClass),
		string(required),
		string(requiredModelTraits),
		string(preferred),
		string(forbidden),
		string(preferredModels),
		string(avoidModels),
		string(requiredAttestations),
		string(acceptance),
		string(metadata),
		nullIfEmpty(rec.HeadCommitOID),
		formatTimePtr(rec.AttestationFrozenAt),
		nullIfEmpty(rec.CurrentJobID),
		nullIfEmpty(rec.CurrentSessionID),
		nullIfEmpty(rec.ClaimedBy),
		formatTimePtr(rec.ClaimedUntil),
		rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
		rec.WorkID,
	)
	if err != nil {
		return fmt.Errorf("update work item: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated work item rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: work %s", ErrNotFound, rec.WorkID)
	}
	return nil
}

func (s *Store) GetWorkItem(ctx context.Context, workID string) (core.WorkItemRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT work_id, title, objective, kind, execution_state, approval_state, lock_state, phase,
		        priority, configuration_class, budget_class, required_capabilities_json, required_model_traits_json,
		        preferred_adapters_json, forbidden_adapters_json, preferred_models_json, avoid_models_json,
		        required_attestations_json, acceptance_json, metadata_json, head_commit_oid, attestation_frozen_at,
		        current_job_id, current_session_id, claimed_by, claimed_until, created_at, updated_at
		   FROM work_items
		  WHERE work_id = ?`,
		workID,
	)
	return scanWorkItem(row)
}

func (s *Store) ListWorkItems(ctx context.Context, limit int, kind, executionState, approvalState string, includeArchived bool) ([]core.WorkItemRecord, error) {
	if limit <= 0 {
		limit = 50
	}

	var (
		clauses []string
		args    []any
	)
	if kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, kind)
	}
	if executionState != "" {
		clauses = append(clauses, "execution_state = ?")
		args = append(args, executionState)
	}
	if approvalState != "" {
		clauses = append(clauses, "approval_state = ?")
		args = append(args, approvalState)
	}
	if !includeArchived {
		clauses = append(clauses, "execution_state <> 'archived'")
	}

	query := `SELECT work_id, title, objective, kind, execution_state, approval_state, lock_state, phase,
		        priority, configuration_class, budget_class, required_capabilities_json, required_model_traits_json,
		        preferred_adapters_json, forbidden_adapters_json, preferred_models_json, avoid_models_json,
		        required_attestations_json, acceptance_json, metadata_json, head_commit_oid, attestation_frozen_at,
		        current_job_id, current_session_id, claimed_by, claimed_until, created_at, updated_at
		   FROM work_items`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query work items: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []core.WorkItemRecord
	for rows.Next() {
		rec, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate work items: %w", err)
	}
	return items, nil
}

func (s *Store) ListReadyWork(ctx context.Context, limit int, includeArchived bool) ([]core.WorkItemRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	queryWork := func(where string, args ...any) ([]core.WorkItemRecord, error) {
		rows, err := s.db.QueryContext(
			ctx,
			`SELECT wi.work_id, wi.title, wi.objective, wi.kind, wi.execution_state, wi.approval_state, wi.lock_state, wi.phase,
			        wi.priority, wi.configuration_class, wi.budget_class, wi.required_capabilities_json, wi.required_model_traits_json,
			        wi.preferred_adapters_json, wi.forbidden_adapters_json, wi.preferred_models_json, wi.avoid_models_json,
			        wi.required_attestations_json, wi.acceptance_json, wi.metadata_json, wi.head_commit_oid, wi.attestation_frozen_at,
			        wi.current_job_id, wi.current_session_id, wi.claimed_by, wi.claimed_until, wi.created_at, wi.updated_at
			   FROM work_items wi
			  WHERE `+where+`
			  ORDER BY wi.priority DESC, wi.updated_at DESC
			  LIMIT ?`,
			args...,
		)
		if err != nil {
			return nil, fmt.Errorf("query ready work: %w", err)
		}
		defer func() { _ = rows.Close() }()

		var items []core.WorkItemRecord
		for rows.Next() {
			rec, err := scanWorkItem(rows)
			if err != nil {
				return nil, err
			}
			items = append(items, rec)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate ready work: %w", err)
		}
		return items, nil
	}

	readyItems, err := queryWork(
		`(
		        wi.execution_state = 'ready'
		        OR (
		            wi.execution_state = 'claimed'
		            AND wi.claimed_by IS NOT NULL
		            AND wi.claimed_by <> ''
		            AND wi.claimed_until IS NOT NULL
		            AND wi.claimed_until <= ?
		        )
		    )
		    AND (
		        wi.claimed_by IS NULL
		        OR wi.claimed_by = ''
		        OR (wi.claimed_until IS NOT NULL AND wi.claimed_until <= ?)
		    )
		    AND wi.lock_state <> 'human_locked'
		    AND NOT EXISTS (
		        SELECT 1
		          FROM work_edges we
		          JOIN work_items dep ON dep.work_id = we.from_work_id
		         WHERE we.to_work_id = wi.work_id
		           AND we.edge_type IN ('blocks', 'depends_on')
		           AND dep.execution_state NOT IN ('done', 'cancelled')
		    )
		    AND NOT EXISTS (
		        SELECT 1
		          FROM work_edges we
		          JOIN work_items newer ON newer.work_id = we.from_work_id
		         WHERE we.to_work_id = wi.work_id
		           AND we.edge_type = 'supersedes'
		           AND newer.execution_state NOT IN ('failed', 'cancelled')
		    )`,
		now,
		now,
		limit,
	)
	if err != nil {
		return nil, err
	}
	if !includeArchived || len(readyItems) >= limit {
		return readyItems, nil
	}

	archivedItems, err := queryWork(`wi.execution_state = 'archived'`, limit-len(readyItems))
	if err != nil {
		return nil, err
	}

	return append(readyItems, archivedItems...), nil
}

func (s *Store) ClaimWorkItem(ctx context.Context, workID, claimant string, leaseUntil time.Time) (core.WorkItemRecord, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE work_items
		    SET claimed_by = ?,
		        claimed_until = ?,
		        execution_state = CASE
		            WHEN execution_state = 'ready' THEN 'claimed'
		            ELSE execution_state
		        END,
		        updated_at = ?
		  WHERE work_id = ?
		    AND execution_state NOT IN ('done', 'failed', 'cancelled', 'archived')
		    AND lock_state <> 'human_locked'
		    AND (
		        claimed_by IS NULL
		        OR claimed_by = ''
		        OR claimed_by = ?
		        OR (claimed_until IS NOT NULL AND claimed_until <= ?)
		    )`,
		claimant,
		leaseUntil.UTC().Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		workID,
		claimant,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("claim work item: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("check claimed work item rows: %w", err)
	}
	if rows == 0 {
		current, getErr := s.GetWorkItem(ctx, workID)
		if getErr != nil {
			return core.WorkItemRecord{}, getErr
		}
		if current.ExecutionState == core.WorkExecutionStateDone ||
			current.ExecutionState == core.WorkExecutionStateFailed ||
			current.ExecutionState == core.WorkExecutionStateCancelled ||
			current.ExecutionState == core.WorkExecutionStateArchived {
			return core.WorkItemRecord{}, ErrBusy
		}
		if current.ClaimedBy != "" && current.ClaimedBy != claimant {
			if current.ClaimedUntil == nil || current.ClaimedUntil.After(now) {
				return core.WorkItemRecord{}, ErrBusy
			}
		}
		return core.WorkItemRecord{}, ErrBusy
	}
	return s.GetWorkItem(ctx, workID)
}

func (s *Store) ReleaseWorkItemClaim(ctx context.Context, workID, claimant string, force bool) (core.WorkItemRecord, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE work_items
		    SET claimed_by = NULL,
		        claimed_until = NULL,
		        execution_state = CASE
		            WHEN execution_state = 'claimed' THEN 'ready'
		            WHEN execution_state = 'in_progress' THEN 'ready'
		            ELSE execution_state
		        END,
		        updated_at = ?
		  WHERE work_id = ?
		    AND (
		        claimed_by IS NULL
		        OR claimed_by = ''
		        OR claimed_by = ?
		        OR (claimed_until IS NOT NULL AND claimed_until <= ?)
		    )`,
		now.Format(time.RFC3339Nano),
		workID,
		claimant,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("release work item claim: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("check released work item rows: %w", err)
	}
	if rows == 0 {
		current, getErr := s.GetWorkItem(ctx, workID)
		if getErr != nil {
			return core.WorkItemRecord{}, getErr
		}
		if force {
			leaseExpired := current.ClaimedUntil != nil && !current.ClaimedUntil.After(now)
			if leaseExpired || current.ClaimedBy == "" || current.ClaimedBy == claimant {
				forceResult, forceErr := s.db.ExecContext(
					ctx,
					`UPDATE work_items
					    SET claimed_by = NULL,
					        claimed_until = NULL,
					        execution_state = CASE
					            WHEN execution_state = 'claimed' THEN 'ready'
					            WHEN execution_state = 'in_progress' THEN 'ready'
					            ELSE execution_state
					        END,
					        updated_at = ?
					  WHERE work_id = ?`,
					now.Format(time.RFC3339Nano),
					workID,
				)
				if forceErr != nil {
					return core.WorkItemRecord{}, fmt.Errorf("force release work item claim: %w", forceErr)
				}
				forceRows, _ := forceResult.RowsAffected()
				if forceRows > 0 {
					return s.GetWorkItem(ctx, workID)
				}
			}
		}
		if current.ClaimedBy != "" && current.ClaimedBy != claimant {
			if current.ClaimedUntil == nil || current.ClaimedUntil.After(now) {
				return core.WorkItemRecord{}, ErrBusy
			}
		}
		return core.WorkItemRecord{}, ErrBusy
	}
	return s.GetWorkItem(ctx, workID)
}

func (s *Store) RenewWorkItemLease(ctx context.Context, workID, claimant string, leaseUntil time.Time) (core.WorkItemRecord, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE work_items
		    SET claimed_until = ?,
		        updated_at = ?
		  WHERE work_id = ?
		    AND claimed_by = ?
		    AND claimed_by IS NOT NULL
		    AND claimed_by <> ''`,
		leaseUntil.UTC().Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		workID,
		claimant,
	)
	if err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("renew work item lease: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("check renewed work item rows: %w", err)
	}
	if rows == 0 {
		current, getErr := s.GetWorkItem(ctx, workID)
		if getErr != nil {
			return core.WorkItemRecord{}, getErr
		}
		if current.ClaimedBy == "" {
			return core.WorkItemRecord{}, fmt.Errorf("work item %s is not currently claimed", workID)
		}
		if current.ClaimedBy != claimant {
			return core.WorkItemRecord{}, ErrBusy
		}
		return core.WorkItemRecord{}, ErrBusy
	}
	return s.GetWorkItem(ctx, workID)
}

func (s *Store) ReleaseExpiredWorkClaims(ctx context.Context) ([]core.WorkItemRecord, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT work_id, title, objective, kind, execution_state, approval_state, lock_state, phase,
		        priority, configuration_class, budget_class, required_capabilities_json, required_model_traits_json,
		        preferred_adapters_json, forbidden_adapters_json, preferred_models_json, avoid_models_json,
		        required_attestations_json, acceptance_json, metadata_json, head_commit_oid, attestation_frozen_at,
		        current_job_id, current_session_id, claimed_by, claimed_until, created_at, updated_at
		   FROM work_items
		  WHERE execution_state IN ('claimed', 'in_progress')
		    AND claimed_until IS NOT NULL
		    AND claimed_until <= ?`,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("query expired work claims: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []core.WorkItemRecord
	for rows.Next() {
		rec, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired work claims: %w", err)
	}

	for i, item := range items {
		_, err := s.db.ExecContext(
			ctx,
			`UPDATE work_items
			    SET execution_state = 'ready',
			        claimed_by = NULL,
			        claimed_until = NULL,
			        updated_at = ?
			  WHERE work_id = ?
			    AND execution_state IN ('claimed', 'in_progress')
			    AND claimed_until <= ?`,
			now,
			item.WorkID,
			now,
		)
		if err != nil {
			return nil, fmt.Errorf("release expired claim %s: %w", item.WorkID, err)
		}
		items[i].ExecutionState = core.WorkExecutionStateReady
		items[i].ClaimedBy = ""
		items[i].ClaimedUntil = nil
	}

	return items, nil
}

func (s *Store) ListJobsByWork(ctx context.Context, workID string, limit int) ([]core.JobRecord, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT job_id, session_id, work_id, adapter, state, label, native_session_id, cwd,
		        created_at, updated_at, finished_at, summary_json, last_raw_artifact
		   FROM jobs
		  WHERE work_id = ?
		  ORDER BY created_at DESC
		  LIMIT ?`,
		workID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query jobs by work: %w", err)
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
		return nil, fmt.Errorf("iterate jobs by work: %w", err)
	}
	return jobs, nil
}

func (s *Store) CreateWorkEdge(ctx context.Context, rec core.WorkEdgeRecord) error {
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal work edge metadata: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO work_edges (edge_id, from_work_id, to_work_id, edge_type, metadata_json, created_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.EdgeID,
		rec.FromWorkID,
		rec.ToWorkID,
		rec.EdgeType,
		string(metadata),
		nullIfEmpty(rec.CreatedBy),
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert work edge: %w", err)
	}
	return nil
}

func (s *Store) ListWorkEdges(ctx context.Context, limit int, edgeType, fromWorkID, toWorkID string) ([]core.WorkEdgeRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	var (
		clauses []string
		args    []any
	)
	if edgeType != "" {
		clauses = append(clauses, "edge_type = ?")
		args = append(args, edgeType)
	}
	if fromWorkID != "" {
		clauses = append(clauses, "from_work_id = ?")
		args = append(args, fromWorkID)
	}
	if toWorkID != "" {
		clauses = append(clauses, "to_work_id = ?")
		args = append(args, toWorkID)
	}
	query := `SELECT edge_id, from_work_id, to_work_id, edge_type, metadata_json, created_by, created_at
		   FROM work_edges`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at ASC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query work edges: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var edges []core.WorkEdgeRecord
	for rows.Next() {
		rec, err := scanWorkEdge(rows)
		if err != nil {
			return nil, err
		}
		edges = append(edges, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate work edges: %w", err)
	}
	return edges, nil
}

func (s *Store) DeleteWorkEdge(ctx context.Context, edgeID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM work_edges WHERE edge_id = ?`, edgeID)
	if err != nil {
		return fmt.Errorf("delete work edge: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted work edge rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: work edge %s", ErrNotFound, edgeID)
	}
	return nil
}

func (s *Store) ListWorkChildren(ctx context.Context, workID string, limit int) ([]core.WorkItemRecord, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT wi.work_id, wi.title, wi.objective, wi.kind, wi.execution_state, wi.approval_state, wi.lock_state, wi.phase,
		        wi.priority, wi.configuration_class, wi.budget_class, wi.required_capabilities_json, wi.required_model_traits_json,
		        wi.preferred_adapters_json, wi.forbidden_adapters_json, wi.preferred_models_json, wi.avoid_models_json,
		        wi.required_attestations_json, wi.acceptance_json, wi.metadata_json, wi.head_commit_oid, wi.attestation_frozen_at,
		        wi.current_job_id, wi.current_session_id, wi.claimed_by, wi.claimed_until, wi.created_at, wi.updated_at
		   FROM work_edges we
		   JOIN work_items wi ON wi.work_id = we.to_work_id
		  WHERE we.from_work_id = ?
		    AND we.edge_type = 'parent_of'
		  ORDER BY wi.priority DESC, wi.created_at ASC
		  LIMIT ?`,
		workID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query work children: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []core.WorkItemRecord
	for rows.Next() {
		rec, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate work children: %w", err)
	}
	return items, nil
}

func (s *Store) CreateWorkUpdate(ctx context.Context, rec core.WorkUpdateRecord) error {
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal work update metadata: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO work_updates (
			update_id, work_id, execution_state, approval_state, phase, message,
			job_id, session_id, artifact_id, metadata_json, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.UpdateID,
		rec.WorkID,
		nullIfEmpty(string(rec.ExecutionState)),
		nullIfEmpty(string(rec.ApprovalState)),
		nullIfEmpty(rec.Phase),
		nullIfEmpty(rec.Message),
		nullIfEmpty(rec.JobID),
		nullIfEmpty(rec.SessionID),
		nullIfEmpty(rec.ArtifactID),
		string(metadata),
		nullIfEmpty(rec.CreatedBy),
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert work update: %w", err)
	}
	return nil
}

func (s *Store) ListWorkUpdates(ctx context.Context, workID string, limit int) ([]core.WorkUpdateRecord, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT update_id, work_id, execution_state, approval_state, phase, message,
		        job_id, session_id, artifact_id, metadata_json, created_by, created_at
		   FROM work_updates
		  WHERE work_id = ?
		  ORDER BY created_at DESC
		  LIMIT ?`,
		workID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query work updates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var updates []core.WorkUpdateRecord
	for rows.Next() {
		rec, err := scanWorkUpdate(rows)
		if err != nil {
			return nil, err
		}
		updates = append(updates, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate work updates: %w", err)
	}
	return updates, nil
}

func (s *Store) CreateWorkNote(ctx context.Context, rec core.WorkNoteRecord) error {
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal work note metadata: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO work_notes (note_id, work_id, note_type, body, metadata_json, created_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.NoteID,
		rec.WorkID,
		nullIfEmpty(rec.NoteType),
		rec.Body,
		string(metadata),
		nullIfEmpty(rec.CreatedBy),
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert work note: %w", err)
	}
	return nil
}

func (s *Store) ListWorkNotes(ctx context.Context, workID string, limit int) ([]core.WorkNoteRecord, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT note_id, work_id, note_type, body, metadata_json, created_by, created_at
		   FROM work_notes
		  WHERE work_id = ?
		  ORDER BY created_at DESC
		  LIMIT ?`,
		workID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query work notes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var notes []core.WorkNoteRecord
	for rows.Next() {
		rec, err := scanWorkNote(rows)
		if err != nil {
			return nil, err
		}
		notes = append(notes, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate work notes: %w", err)
	}
	return notes, nil
}

func (s *Store) CreateWorkProposal(ctx context.Context, rec core.WorkProposalRecord) error {
	patch, err := marshalJSON(rec.ProposedPatch)
	if err != nil {
		return fmt.Errorf("marshal work proposal patch: %w", err)
	}
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal work proposal metadata: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO work_proposals (
			proposal_id, proposal_type, state, target_work_id, source_work_id, rationale,
			proposed_patch_json, metadata_json, created_by, created_at, reviewed_by, reviewed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ProposalID,
		rec.ProposalType,
		rec.State,
		nullIfEmpty(rec.TargetWorkID),
		nullIfEmpty(rec.SourceWorkID),
		nullIfEmpty(rec.Rationale),
		string(patch),
		string(metadata),
		nullIfEmpty(rec.CreatedBy),
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		nullIfEmpty(rec.ReviewedBy),
		formatTimePtr(rec.ReviewedAt),
	)
	if err != nil {
		return fmt.Errorf("insert work proposal: %w", err)
	}
	return nil
}

func (s *Store) UpdateWorkProposal(ctx context.Context, rec core.WorkProposalRecord) error {
	patch, err := marshalJSON(rec.ProposedPatch)
	if err != nil {
		return fmt.Errorf("marshal work proposal patch: %w", err)
	}
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal work proposal metadata: %w", err)
	}

	result, err := s.db.ExecContext(
		ctx,
		`UPDATE work_proposals
		    SET proposal_type = ?,
		        state = ?,
		        target_work_id = ?,
		        source_work_id = ?,
		        rationale = ?,
		        proposed_patch_json = ?,
		        metadata_json = ?,
		        reviewed_by = ?,
		        reviewed_at = ?
		  WHERE proposal_id = ?`,
		rec.ProposalType,
		rec.State,
		nullIfEmpty(rec.TargetWorkID),
		nullIfEmpty(rec.SourceWorkID),
		nullIfEmpty(rec.Rationale),
		string(patch),
		string(metadata),
		nullIfEmpty(rec.ReviewedBy),
		formatTimePtr(rec.ReviewedAt),
		rec.ProposalID,
	)
	if err != nil {
		return fmt.Errorf("update work proposal: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated work proposal rows: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: proposal %s", ErrNotFound, rec.ProposalID)
	}
	return nil
}

func (s *Store) GetWorkProposal(ctx context.Context, proposalID string) (core.WorkProposalRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT proposal_id, proposal_type, state, target_work_id, source_work_id, rationale,
		        proposed_patch_json, metadata_json, created_by, created_at, reviewed_by, reviewed_at
		   FROM work_proposals
		  WHERE proposal_id = ?`,
		proposalID,
	)
	return scanWorkProposal(row)
}

func (s *Store) ListWorkProposals(ctx context.Context, limit int, state, targetWorkID, sourceWorkID string) ([]core.WorkProposalRecord, error) {
	if limit <= 0 {
		limit = 50
	}

	var (
		clauses []string
		args    []any
	)
	if state != "" {
		clauses = append(clauses, "state = ?")
		args = append(args, state)
	}
	if targetWorkID != "" {
		clauses = append(clauses, "target_work_id = ?")
		args = append(args, targetWorkID)
	}
	if sourceWorkID != "" {
		clauses = append(clauses, "source_work_id = ?")
		args = append(args, sourceWorkID)
	}

	query := `SELECT proposal_id, proposal_type, state, target_work_id, source_work_id, rationale,
		        proposed_patch_json, metadata_json, created_by, created_at, reviewed_by, reviewed_at
		   FROM work_proposals`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query work proposals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var proposals []core.WorkProposalRecord
	for rows.Next() {
		rec, err := scanWorkProposal(rows)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate work proposals: %w", err)
	}
	return proposals, nil
}

func (s *Store) CreateAttestationRecord(ctx context.Context, rec core.AttestationRecord) error {
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal attestation metadata: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO attestation_records (
			attestation_id, subject_kind, subject_id, result, summary, artifact_id,
			job_id, session_id, method, verifier_kind, verifier_identity,
			confidence, blocking, supersedes_attestation_id, signer_pubkey, signature,
			metadata_json, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.AttestationID,
		rec.SubjectKind,
		rec.SubjectID,
		rec.Result,
		nullIfEmpty(rec.Summary),
		nullIfEmpty(rec.ArtifactID),
		nullIfEmpty(rec.JobID),
		nullIfEmpty(rec.SessionID),
		nullIfEmpty(rec.Method),
		nullIfEmpty(rec.VerifierKind),
		nullIfEmpty(rec.VerifierIdentity),
		rec.Confidence,
		boolToInt(rec.Blocking),
		nullIfEmpty(rec.SupersedesAttestationID),
		nullIfEmpty(rec.SignerPubkey),
		nullIfEmpty(rec.Signature),
		string(metadata),
		nullIfEmpty(rec.CreatedBy),
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert attestation record: %w", err)
	}
	return nil
}

func (s *Store) ListAttestationRecords(ctx context.Context, subjectKind, subjectID string, limit int) ([]core.AttestationRecord, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT attestation_id, subject_kind, subject_id, result, summary, artifact_id,
		        job_id, session_id, method, verifier_kind, verifier_identity,
		        confidence, blocking, supersedes_attestation_id, signer_pubkey, signature,
		        metadata_json, created_by, created_at
		   FROM attestation_records
		  WHERE subject_kind = ?
		    AND subject_id = ?
		  ORDER BY created_at DESC
		  LIMIT ?`,
		subjectKind,
		subjectID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query attestation records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var records []core.AttestationRecord
	for rows.Next() {
		rec, err := scanAttestationRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attestation records: %w", err)
	}
	return records, nil
}

func (s *Store) CreateApprovalRecord(ctx context.Context, rec core.ApprovalRecord) error {
	attestationIDs, err := marshalJSON(rec.AttestationIDs)
	if err != nil {
		return fmt.Errorf("marshal approval attestation ids: %w", err)
	}
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal approval metadata: %w", err)
	}
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO approval_records (
			approval_id, work_id, approved_commit_oid, approved_ref, attestation_ids_json,
			status, supersedes_approval_id, approved_by, approved_at, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ApprovalID,
		rec.WorkID,
		nullIfEmpty(rec.ApprovedCommitOID),
		nullIfEmpty(rec.ApprovedRef),
		string(attestationIDs),
		rec.Status,
		nullIfEmpty(rec.SupersedesApprovalID),
		nullIfEmpty(rec.ApprovedBy),
		rec.ApprovedAt.UTC().Format(time.RFC3339Nano),
		string(metadata),
	)
	if err != nil {
		return fmt.Errorf("insert approval record: %w", err)
	}
	return nil
}

func (s *Store) ListApprovalRecords(ctx context.Context, workID string, limit int) ([]core.ApprovalRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT approval_id, work_id, approved_commit_oid, approved_ref, attestation_ids_json,
		        status, supersedes_approval_id, approved_by, approved_at, metadata_json
		   FROM approval_records
		  WHERE work_id = ?
		  ORDER BY approved_at DESC
		  LIMIT ?`,
		workID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query approval records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []core.ApprovalRecord
	for rows.Next() {
		rec, err := scanApprovalRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approval records: %w", err)
	}
	return records, nil
}

func (s *Store) CreatePromotionRecord(ctx context.Context, rec core.PromotionRecord) error {
	metadata, err := marshalJSON(rec.Metadata)
	if err != nil {
		return fmt.Errorf("marshal promotion metadata: %w", err)
	}
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO promotion_records (
			promotion_id, work_id, approval_id, environment, promoted_commit_oid,
			target_ref, status, promoted_by, promoted_at, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.PromotionID,
		rec.WorkID,
		nullIfEmpty(rec.ApprovalID),
		rec.Environment,
		nullIfEmpty(rec.PromotedCommitOID),
		nullIfEmpty(rec.TargetRef),
		rec.Status,
		nullIfEmpty(rec.PromotedBy),
		rec.PromotedAt.UTC().Format(time.RFC3339Nano),
		string(metadata),
	)
	if err != nil {
		return fmt.Errorf("insert promotion record: %w", err)
	}
	return nil
}

func (s *Store) ListPromotionRecords(ctx context.Context, workID string, limit int) ([]core.PromotionRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT promotion_id, work_id, approval_id, environment, promoted_commit_oid,
		        target_ref, status, promoted_by, promoted_at, metadata_json
		   FROM promotion_records
		  WHERE work_id = ?
		  ORDER BY promoted_at DESC
		  LIMIT ?`,
		workID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query promotion records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []core.PromotionRecord
	for rows.Next() {
		rec, err := scanPromotionRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate promotion records: %w", err)
	}
	return records, nil
}

func (s *Store) ListJobsBySession(ctx context.Context, sessionID string, limit int) ([]core.JobRecord, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT job_id, session_id, work_id, adapter, state, label, native_session_id, cwd,
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

func (s *Store) ListRecentEvents(ctx context.Context, limit int) ([]core.EventRecord, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT event_id, seq, ts, job_id, session_id, adapter, kind, phase, native_session_id,
		        correlation_id, payload_json, raw_ref
		   FROM events
		  ORDER BY ts DESC
		  LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent events: %w", err)
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
		return nil, fmt.Errorf("iterate recent events: %w", err)
	}
	return events, nil
}

func (s *Store) ListArtifactsByJob(ctx context.Context, jobID string, limit int) ([]core.ArtifactRecord, error) {
	return s.listArtifactsWhere(ctx, "job_id = ?", []any{jobID}, limit, "")
}

func (s *Store) ListRecentArtifacts(ctx context.Context, limit int) ([]core.ArtifactRecord, error) {
	return s.listArtifactsWhere(ctx, "", nil, limit, "")
}

func (s *Store) GetArtifact(ctx context.Context, artifactID string) (core.ArtifactRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT artifact_id, job_id, session_id, kind, path, created_at, metadata_json
		   FROM artifacts
		  WHERE artifact_id = ?`,
		artifactID,
	)
	return scanArtifact(row)
}

func (s *Store) ListArtifactsFiltered(ctx context.Context, jobID, sessionID, kind string, limit int) ([]core.ArtifactRecord, error) {
	var (
		clauses []string
		args    []any
	)
	if jobID != "" {
		clauses = append(clauses, "job_id = ?")
		args = append(args, jobID)
	}
	if sessionID != "" {
		clauses = append(clauses, "session_id = ?")
		args = append(args, sessionID)
	}
	return s.listArtifactsWhere(ctx, strings.Join(clauses, " AND "), args, limit, kind)
}

func (s *Store) listArtifactsWhere(ctx context.Context, where string, args []any, limit int, kind string) ([]core.ArtifactRecord, error) {
	if limit <= 0 {
		limit = 20
	}

	query := `SELECT artifact_id, job_id, session_id, kind, path, created_at, metadata_json
		   FROM artifacts`
	if where != "" || kind != "" {
		var clauses []string
		if where != "" {
			clauses = append(clauses, where)
		}
		if kind != "" {
			clauses = append(clauses, "kind = ?")
			args = append(args, kind)
		}
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
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
	return s.listArtifactsWhere(ctx, "session_id = ?", []any{sessionID}, limit, "")
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

func (s *Store) ListRecentTurns(ctx context.Context, limit int) ([]core.TurnRecord, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT turn_id, session_id, job_id, adapter, started_at, completed_at,
		        input_text, input_source, result_summary, status, native_session_id, stats_json
		   FROM turns
		  ORDER BY started_at DESC
		  LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent turns: %w", err)
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
		return nil, fmt.Errorf("iterate recent turns: %w", err)
	}

	return turns, nil
}

func (s *Store) FindActiveJobBySession(ctx context.Context, sessionID string) (*core.JobRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT job_id, session_id, work_id, adapter, state, label, native_session_id, cwd,
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

func (s *Store) CreateCatalogSnapshot(ctx context.Context, rec core.CatalogSnapshot) error {
	entriesJSON, err := marshalJSON(rec.Entries)
	if err != nil {
		return fmt.Errorf("marshal catalog entries: %w", err)
	}
	issuesJSON, err := marshalJSON(rec.Issues)
	if err != nil {
		return fmt.Errorf("marshal catalog issues: %w", err)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO catalog_snapshots (snapshot_id, created_at, entries_json, issues_json)
		 VALUES (?, ?, ?, ?)`,
		rec.SnapshotID,
		rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		string(entriesJSON),
		string(issuesJSON),
	)
	if err != nil {
		return fmt.Errorf("insert catalog snapshot: %w", err)
	}
	return nil
}

func (s *Store) LatestCatalogSnapshot(ctx context.Context) (core.CatalogSnapshot, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT snapshot_id, created_at, entries_json, issues_json
		   FROM catalog_snapshots
		  ORDER BY created_at DESC
		  LIMIT 1`,
	)
	return scanCatalogSnapshot(row)
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
		`ALTER TABLE jobs ADD COLUMN work_id TEXT REFERENCES work_items(work_id) ON DELETE SET NULL`,
		`ALTER TABLE work_items ADD COLUMN required_model_traits_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE work_items ADD COLUMN preferred_models_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE work_items ADD COLUMN avoid_models_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE work_items ADD COLUMN lock_state TEXT NOT NULL DEFAULT 'unlocked'`,
		`ALTER TABLE work_items ADD COLUMN required_attestations_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE work_items ADD COLUMN head_commit_oid TEXT`,
		`ALTER TABLE work_items ADD COLUMN attestation_frozen_at TEXT`,
		`ALTER TABLE attestation_records ADD COLUMN method TEXT`,
		`ALTER TABLE attestation_records ADD COLUMN verifier_kind TEXT`,
		`ALTER TABLE attestation_records ADD COLUMN verifier_identity TEXT`,
		`ALTER TABLE attestation_records ADD COLUMN confidence REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE attestation_records ADD COLUMN blocking INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE attestation_records ADD COLUMN supersedes_attestation_id TEXT`,
		`ALTER TABLE attestation_records ADD COLUMN signer_pubkey TEXT`,
		`ALTER TABLE attestation_records ADD COLUMN signature TEXT`,
		`INSERT OR IGNORE INTO attestation_records (
			attestation_id, subject_kind, subject_id, result, summary, artifact_id,
			job_id, session_id, metadata_json, created_by, created_at
		)
		SELECT verification_id, target_kind, target_id, result, summary, artifact_id,
		       job_id, session_id, metadata_json, created_by, created_at
		  FROM verification_records`,
		`UPDATE work_items SET approval_state = 'pending' WHERE approval_state = 'pending_verification'`,
		`UPDATE work_updates SET approval_state = 'pending' WHERE approval_state = 'pending_verification'`,
		`CREATE INDEX IF NOT EXISTS idx_attestation_records_subject_created_at ON attestation_records(subject_kind, subject_id, created_at DESC)`,
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
	var workID sql.NullString
	var label sql.NullString
	var nativeSessionID sql.NullString
	var summaryJSON string
	var lastRaw sql.NullString

	if err := scanner.Scan(
		&rec.JobID,
		&rec.SessionID,
		&workID,
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

	rec.WorkID = workID.String
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

func scanCatalogSnapshot(scanner interface{ Scan(...any) error }) (core.CatalogSnapshot, error) {
	var rec core.CatalogSnapshot
	var createdAt string
	var entriesJSON string
	var issuesJSON string

	if err := scanner.Scan(&rec.SnapshotID, &createdAt, &entriesJSON, &issuesJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.CatalogSnapshot{}, ErrNotFound
		}
		return core.CatalogSnapshot{}, fmt.Errorf("scan catalog snapshot: %w", err)
	}

	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return core.CatalogSnapshot{}, fmt.Errorf("parse catalog created_at: %w", err)
	}
	rec.CreatedAt = parsed
	if err := json.Unmarshal([]byte(entriesJSON), &rec.Entries); err != nil {
		return core.CatalogSnapshot{}, fmt.Errorf("decode catalog entries: %w", err)
	}
	if err := json.Unmarshal([]byte(issuesJSON), &rec.Issues); err != nil {
		return core.CatalogSnapshot{}, fmt.Errorf("decode catalog issues: %w", err)
	}
	if rec.Entries == nil {
		rec.Entries = []core.CatalogEntry{}
	}
	if rec.Issues == nil {
		rec.Issues = []core.CatalogIssue{}
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

func scanWorkItem(scanner interface{ Scan(...any) error }) (core.WorkItemRecord, error) {
	var rec core.WorkItemRecord
	var lockState sql.NullString
	var phase sql.NullString
	var currentJobID sql.NullString
	var currentSessionID sql.NullString
	var claimedBy sql.NullString
	var claimedUntil sql.NullString
	var createdAt string
	var updatedAt string
	var attestationFrozenAt sql.NullString
	var requiredJSON string
	var configurationClass sql.NullString
	var budgetClass sql.NullString
	var requiredModelTraitsJSON string
	var preferredJSON string
	var forbiddenJSON string
	var preferredModelsJSON string
	var avoidModelsJSON string
	var requiredAttestationsJSON string
	var acceptanceJSON string
	var metadataJSON string
	var headCommitOID sql.NullString

	if err := scanner.Scan(
		&rec.WorkID,
		&rec.Title,
		&rec.Objective,
		&rec.Kind,
		&rec.ExecutionState,
		&rec.ApprovalState,
		&lockState,
		&phase,
		&rec.Priority,
		&configurationClass,
		&budgetClass,
		&requiredJSON,
		&requiredModelTraitsJSON,
		&preferredJSON,
		&forbiddenJSON,
		&preferredModelsJSON,
		&avoidModelsJSON,
		&requiredAttestationsJSON,
		&acceptanceJSON,
		&metadataJSON,
		&headCommitOID,
		&attestationFrozenAt,
		&currentJobID,
		&currentSessionID,
		&claimedBy,
		&claimedUntil,
		&createdAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.WorkItemRecord{}, ErrNotFound
		}
		return core.WorkItemRecord{}, fmt.Errorf("scan work item: %w", err)
	}

	rec.LockState = core.WorkLockState(lockState.String)
	if rec.LockState == "" {
		rec.LockState = core.WorkLockStateUnlocked
	}
	rec.Phase = phase.String
	rec.ConfigurationClass = configurationClass.String
	rec.BudgetClass = budgetClass.String
	rec.HeadCommitOID = headCommitOID.String
	rec.CurrentJobID = currentJobID.String
	rec.CurrentSessionID = currentSessionID.String
	rec.ClaimedBy = claimedBy.String

	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("parse work created_at: %w", err)
	}
	updated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("parse work updated_at: %w", err)
	}
	rec.CreatedAt = created
	rec.UpdatedAt = updated
	if claimedUntil.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, claimedUntil.String)
		if err != nil {
			return core.WorkItemRecord{}, fmt.Errorf("parse work claimed_until: %w", err)
		}
		rec.ClaimedUntil = &parsed
	}
	if attestationFrozenAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, attestationFrozenAt.String)
		if err != nil {
			return core.WorkItemRecord{}, fmt.Errorf("parse work attestation_frozen_at: %w", err)
		}
		rec.AttestationFrozenAt = &parsed
	}

	if err := json.Unmarshal([]byte(requiredJSON), &rec.RequiredCapabilities); err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("decode work required capabilities: %w", err)
	}
	if err := json.Unmarshal([]byte(requiredModelTraitsJSON), &rec.RequiredModelTraits); err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("decode work required model traits: %w", err)
	}
	if err := json.Unmarshal([]byte(preferredJSON), &rec.PreferredAdapters); err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("decode work preferred adapters: %w", err)
	}
	if err := json.Unmarshal([]byte(forbiddenJSON), &rec.ForbiddenAdapters); err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("decode work forbidden adapters: %w", err)
	}
	if err := json.Unmarshal([]byte(preferredModelsJSON), &rec.PreferredModels); err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("decode work preferred models: %w", err)
	}
	if err := json.Unmarshal([]byte(avoidModelsJSON), &rec.AvoidModels); err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("decode work avoid models: %w", err)
	}
	if err := json.Unmarshal([]byte(requiredAttestationsJSON), &rec.RequiredAttestations); err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("decode work required attestations: %w", err)
	}
	if err := json.Unmarshal([]byte(acceptanceJSON), &rec.Acceptance); err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("decode work acceptance: %w", err)
	}
	if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
		return core.WorkItemRecord{}, fmt.Errorf("decode work metadata: %w", err)
	}
	if rec.RequiredCapabilities == nil {
		rec.RequiredCapabilities = []string{}
	}
	if rec.PreferredAdapters == nil {
		rec.PreferredAdapters = []string{}
	}
	if rec.ForbiddenAdapters == nil {
		rec.ForbiddenAdapters = []string{}
	}
	if rec.RequiredModelTraits == nil {
		rec.RequiredModelTraits = []string{}
	}
	if rec.PreferredModels == nil {
		rec.PreferredModels = []string{}
	}
	if rec.AvoidModels == nil {
		rec.AvoidModels = []string{}
	}
	if rec.RequiredAttestations == nil {
		rec.RequiredAttestations = []core.RequiredAttestation{}
	}
	if rec.Acceptance == nil {
		rec.Acceptance = map[string]any{}
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	return rec, nil
}

func scanWorkEdge(scanner interface{ Scan(...any) error }) (core.WorkEdgeRecord, error) {
	var rec core.WorkEdgeRecord
	var metadataJSON string
	var createdAt string

	if err := scanner.Scan(
		&rec.EdgeID,
		&rec.FromWorkID,
		&rec.ToWorkID,
		&rec.EdgeType,
		&metadataJSON,
		&rec.CreatedBy,
		&createdAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.WorkEdgeRecord{}, ErrNotFound
		}
		return core.WorkEdgeRecord{}, fmt.Errorf("scan work edge: %w", err)
	}

	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return core.WorkEdgeRecord{}, fmt.Errorf("parse work edge created_at: %w", err)
	}
	rec.CreatedAt = parsed
	if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
		return core.WorkEdgeRecord{}, fmt.Errorf("decode work edge metadata: %w", err)
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	return rec, nil
}

func scanWorkUpdate(scanner interface{ Scan(...any) error }) (core.WorkUpdateRecord, error) {
	var rec core.WorkUpdateRecord
	var executionState sql.NullString
	var approvalState sql.NullString
	var phase sql.NullString
	var message sql.NullString
	var jobID sql.NullString
	var sessionID sql.NullString
	var artifactID sql.NullString
	var metadataJSON string
	var createdBy sql.NullString
	var createdAt string

	if err := scanner.Scan(
		&rec.UpdateID,
		&rec.WorkID,
		&executionState,
		&approvalState,
		&phase,
		&message,
		&jobID,
		&sessionID,
		&artifactID,
		&metadataJSON,
		&createdBy,
		&createdAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.WorkUpdateRecord{}, ErrNotFound
		}
		return core.WorkUpdateRecord{}, fmt.Errorf("scan work update: %w", err)
	}

	rec.ExecutionState = core.WorkExecutionState(executionState.String)
	rec.ApprovalState = core.WorkApprovalState(approvalState.String)
	rec.Phase = phase.String
	rec.Message = message.String
	rec.JobID = jobID.String
	rec.SessionID = sessionID.String
	rec.ArtifactID = artifactID.String
	rec.CreatedBy = createdBy.String
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return core.WorkUpdateRecord{}, fmt.Errorf("parse work update created_at: %w", err)
	}
	rec.CreatedAt = parsed
	if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
		return core.WorkUpdateRecord{}, fmt.Errorf("decode work update metadata: %w", err)
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	return rec, nil
}

func scanWorkNote(scanner interface{ Scan(...any) error }) (core.WorkNoteRecord, error) {
	var rec core.WorkNoteRecord
	var noteType sql.NullString
	var metadataJSON string
	var createdBy sql.NullString
	var createdAt string

	if err := scanner.Scan(
		&rec.NoteID,
		&rec.WorkID,
		&noteType,
		&rec.Body,
		&metadataJSON,
		&createdBy,
		&createdAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.WorkNoteRecord{}, ErrNotFound
		}
		return core.WorkNoteRecord{}, fmt.Errorf("scan work note: %w", err)
	}

	rec.NoteType = noteType.String
	rec.CreatedBy = createdBy.String
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return core.WorkNoteRecord{}, fmt.Errorf("parse work note created_at: %w", err)
	}
	rec.CreatedAt = parsed
	if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
		return core.WorkNoteRecord{}, fmt.Errorf("decode work note metadata: %w", err)
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	return rec, nil
}

func scanWorkProposal(scanner interface{ Scan(...any) error }) (core.WorkProposalRecord, error) {
	var rec core.WorkProposalRecord
	var targetWorkID sql.NullString
	var sourceWorkID sql.NullString
	var rationale sql.NullString
	var patchJSON string
	var metadataJSON string
	var createdBy sql.NullString
	var createdAt string
	var reviewedBy sql.NullString
	var reviewedAt sql.NullString

	if err := scanner.Scan(
		&rec.ProposalID,
		&rec.ProposalType,
		&rec.State,
		&targetWorkID,
		&sourceWorkID,
		&rationale,
		&patchJSON,
		&metadataJSON,
		&createdBy,
		&createdAt,
		&reviewedBy,
		&reviewedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.WorkProposalRecord{}, ErrNotFound
		}
		return core.WorkProposalRecord{}, fmt.Errorf("scan work proposal: %w", err)
	}

	rec.TargetWorkID = targetWorkID.String
	rec.SourceWorkID = sourceWorkID.String
	rec.Rationale = rationale.String
	rec.CreatedBy = createdBy.String
	rec.ReviewedBy = reviewedBy.String
	parsedCreatedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return core.WorkProposalRecord{}, fmt.Errorf("parse work proposal created_at: %w", err)
	}
	rec.CreatedAt = parsedCreatedAt
	if reviewedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, reviewedAt.String)
		if err != nil {
			return core.WorkProposalRecord{}, fmt.Errorf("parse work proposal reviewed_at: %w", err)
		}
		rec.ReviewedAt = &parsed
	}
	if err := json.Unmarshal([]byte(patchJSON), &rec.ProposedPatch); err != nil {
		return core.WorkProposalRecord{}, fmt.Errorf("decode work proposal patch: %w", err)
	}
	if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
		return core.WorkProposalRecord{}, fmt.Errorf("decode work proposal metadata: %w", err)
	}
	if rec.ProposedPatch == nil {
		rec.ProposedPatch = map[string]any{}
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	return rec, nil
}

func scanAttestationRecord(scanner interface{ Scan(...any) error }) (core.AttestationRecord, error) {
	var rec core.AttestationRecord
	var summary sql.NullString
	var artifactID sql.NullString
	var jobID sql.NullString
	var sessionID sql.NullString
	var method sql.NullString
	var verifierKind sql.NullString
	var verifierIdentity sql.NullString
	var confidence sql.NullFloat64
	var blocking int
	var supersedesAttestationID sql.NullString
	var signerPubkey sql.NullString
	var signature sql.NullString
	var metadataJSON string
	var createdBy sql.NullString
	var createdAt string

	if err := scanner.Scan(
		&rec.AttestationID,
		&rec.SubjectKind,
		&rec.SubjectID,
		&rec.Result,
		&summary,
		&artifactID,
		&jobID,
		&sessionID,
		&method,
		&verifierKind,
		&verifierIdentity,
		&confidence,
		&blocking,
		&supersedesAttestationID,
		&signerPubkey,
		&signature,
		&metadataJSON,
		&createdBy,
		&createdAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.AttestationRecord{}, ErrNotFound
		}
		return core.AttestationRecord{}, fmt.Errorf("scan attestation record: %w", err)
	}

	rec.Summary = summary.String
	rec.ArtifactID = artifactID.String
	rec.JobID = jobID.String
	rec.SessionID = sessionID.String
	rec.Method = method.String
	rec.VerifierKind = verifierKind.String
	rec.VerifierIdentity = verifierIdentity.String
	rec.Confidence = confidence.Float64
	rec.Blocking = blocking != 0
	rec.SupersedesAttestationID = supersedesAttestationID.String
	rec.SignerPubkey = signerPubkey.String
	rec.Signature = signature.String
	rec.CreatedBy = createdBy.String
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return core.AttestationRecord{}, fmt.Errorf("parse attestation created_at: %w", err)
	}
	rec.CreatedAt = parsed
	if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
		return core.AttestationRecord{}, fmt.Errorf("decode attestation metadata: %w", err)
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	return rec, nil
}

func scanApprovalRecord(scanner interface{ Scan(...any) error }) (core.ApprovalRecord, error) {
	var rec core.ApprovalRecord
	var approvedCommitOID sql.NullString
	var approvedRef sql.NullString
	var attestationIDsJSON string
	var supersedesApprovalID sql.NullString
	var approvedBy sql.NullString
	var approvedAt string
	var metadataJSON string
	if err := scanner.Scan(
		&rec.ApprovalID,
		&rec.WorkID,
		&approvedCommitOID,
		&approvedRef,
		&attestationIDsJSON,
		&rec.Status,
		&supersedesApprovalID,
		&approvedBy,
		&approvedAt,
		&metadataJSON,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.ApprovalRecord{}, ErrNotFound
		}
		return core.ApprovalRecord{}, fmt.Errorf("scan approval record: %w", err)
	}
	rec.ApprovedCommitOID = approvedCommitOID.String
	rec.ApprovedRef = approvedRef.String
	rec.SupersedesApprovalID = supersedesApprovalID.String
	rec.ApprovedBy = approvedBy.String
	parsed, err := time.Parse(time.RFC3339Nano, approvedAt)
	if err != nil {
		return core.ApprovalRecord{}, fmt.Errorf("parse approval approved_at: %w", err)
	}
	rec.ApprovedAt = parsed
	if err := json.Unmarshal([]byte(attestationIDsJSON), &rec.AttestationIDs); err != nil {
		return core.ApprovalRecord{}, fmt.Errorf("decode approval attestation ids: %w", err)
	}
	if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
		return core.ApprovalRecord{}, fmt.Errorf("decode approval metadata: %w", err)
	}
	if rec.AttestationIDs == nil {
		rec.AttestationIDs = []string{}
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	return rec, nil
}

func scanPromotionRecord(scanner interface{ Scan(...any) error }) (core.PromotionRecord, error) {
	var rec core.PromotionRecord
	var approvalID sql.NullString
	var promotedCommitOID sql.NullString
	var targetRef sql.NullString
	var promotedBy sql.NullString
	var promotedAt string
	var metadataJSON string
	if err := scanner.Scan(
		&rec.PromotionID,
		&rec.WorkID,
		&approvalID,
		&rec.Environment,
		&promotedCommitOID,
		&targetRef,
		&rec.Status,
		&promotedBy,
		&promotedAt,
		&metadataJSON,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.PromotionRecord{}, ErrNotFound
		}
		return core.PromotionRecord{}, fmt.Errorf("scan promotion record: %w", err)
	}
	rec.ApprovalID = approvalID.String
	rec.PromotedCommitOID = promotedCommitOID.String
	rec.TargetRef = targetRef.String
	rec.PromotedBy = promotedBy.String
	parsed, err := time.Parse(time.RFC3339Nano, promotedAt)
	if err != nil {
		return core.PromotionRecord{}, fmt.Errorf("parse promotion promoted_at: %w", err)
	}
	rec.PromotedAt = parsed
	if err := json.Unmarshal([]byte(metadataJSON), &rec.Metadata); err != nil {
		return core.PromotionRecord{}, fmt.Errorf("decode promotion metadata: %w", err)
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
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
	return strings.Contains(text, "duplicate column name") || strings.Contains(text, "no such table")
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
	work_id TEXT REFERENCES work_items(work_id) ON DELETE SET NULL,
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

CREATE TABLE IF NOT EXISTS catalog_snapshots (
	snapshot_id TEXT PRIMARY KEY,
	created_at TEXT NOT NULL,
	entries_json TEXT NOT NULL DEFAULT '[]',
	issues_json TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS work_items (
	work_id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	objective TEXT NOT NULL,
	kind TEXT NOT NULL,
	execution_state TEXT NOT NULL,
	approval_state TEXT NOT NULL,
	lock_state TEXT NOT NULL DEFAULT 'unlocked',
	phase TEXT,
	priority INTEGER NOT NULL DEFAULT 0,
	configuration_class TEXT,
	budget_class TEXT,
	required_capabilities_json TEXT NOT NULL DEFAULT '[]',
	required_model_traits_json TEXT NOT NULL DEFAULT '[]',
	preferred_adapters_json TEXT NOT NULL DEFAULT '[]',
	forbidden_adapters_json TEXT NOT NULL DEFAULT '[]',
	preferred_models_json TEXT NOT NULL DEFAULT '[]',
	avoid_models_json TEXT NOT NULL DEFAULT '[]',
	required_attestations_json TEXT NOT NULL DEFAULT '[]',
	acceptance_json TEXT NOT NULL DEFAULT '{}',
	metadata_json TEXT NOT NULL DEFAULT '{}',
	head_commit_oid TEXT,
	current_job_id TEXT,
	current_session_id TEXT,
	claimed_by TEXT,
	claimed_until TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS work_edges (
	edge_id TEXT PRIMARY KEY,
	from_work_id TEXT NOT NULL REFERENCES work_items(work_id) ON DELETE CASCADE,
	to_work_id TEXT NOT NULL REFERENCES work_items(work_id) ON DELETE CASCADE,
	edge_type TEXT NOT NULL,
	metadata_json TEXT NOT NULL DEFAULT '{}',
	created_by TEXT,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS work_updates (
	update_id TEXT PRIMARY KEY,
	work_id TEXT NOT NULL REFERENCES work_items(work_id) ON DELETE CASCADE,
	execution_state TEXT,
	approval_state TEXT,
	phase TEXT,
	message TEXT,
	job_id TEXT,
	session_id TEXT,
	artifact_id TEXT,
	metadata_json TEXT NOT NULL DEFAULT '{}',
	created_by TEXT,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS work_notes (
	note_id TEXT PRIMARY KEY,
	work_id TEXT NOT NULL REFERENCES work_items(work_id) ON DELETE CASCADE,
	note_type TEXT,
	body TEXT NOT NULL,
	metadata_json TEXT NOT NULL DEFAULT '{}',
	created_by TEXT,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS work_proposals (
	proposal_id TEXT PRIMARY KEY,
	proposal_type TEXT NOT NULL,
	state TEXT NOT NULL,
	target_work_id TEXT,
	source_work_id TEXT,
	rationale TEXT,
	proposed_patch_json TEXT NOT NULL DEFAULT '{}',
	metadata_json TEXT NOT NULL DEFAULT '{}',
	created_by TEXT,
	created_at TEXT NOT NULL,
	reviewed_by TEXT,
	reviewed_at TEXT
);

CREATE TABLE IF NOT EXISTS attestation_records (
	attestation_id TEXT PRIMARY KEY,
	subject_kind TEXT NOT NULL,
	subject_id TEXT NOT NULL,
	result TEXT NOT NULL,
	summary TEXT,
	artifact_id TEXT,
	job_id TEXT,
	session_id TEXT,
	method TEXT,
	verifier_kind TEXT,
	verifier_identity TEXT,
	confidence REAL NOT NULL DEFAULT 0,
	blocking INTEGER NOT NULL DEFAULT 0,
	supersedes_attestation_id TEXT,
	signer_pubkey TEXT,
	signature TEXT,
	metadata_json TEXT NOT NULL DEFAULT '{}',
	created_by TEXT,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS approval_records (
	approval_id TEXT PRIMARY KEY,
	work_id TEXT NOT NULL REFERENCES work_items(work_id) ON DELETE CASCADE,
	approved_commit_oid TEXT,
	approved_ref TEXT,
	attestation_ids_json TEXT NOT NULL DEFAULT '[]',
	status TEXT NOT NULL,
	supersedes_approval_id TEXT,
	approved_by TEXT,
	approved_at TEXT NOT NULL,
	metadata_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS promotion_records (
	promotion_id TEXT PRIMARY KEY,
	work_id TEXT NOT NULL REFERENCES work_items(work_id) ON DELETE CASCADE,
	approval_id TEXT,
	environment TEXT NOT NULL,
	promoted_commit_oid TEXT,
	target_ref TEXT,
	status TEXT NOT NULL,
	promoted_by TEXT,
	promoted_at TEXT NOT NULL,
	metadata_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_jobs_session_id ON jobs(session_id);
CREATE INDEX IF NOT EXISTS idx_jobs_work_id ON jobs(work_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_events_job_seq_unique ON events(job_id, seq);
CREATE INDEX IF NOT EXISTS idx_events_session_id_ts ON events(session_id, ts);
CREATE INDEX IF NOT EXISTS idx_artifacts_job_id ON artifacts(job_id);
CREATE INDEX IF NOT EXISTS idx_catalog_snapshots_created_at ON catalog_snapshots(created_at);
CREATE INDEX IF NOT EXISTS idx_work_items_state ON work_items(execution_state, approval_state, updated_at);
CREATE INDEX IF NOT EXISTS idx_work_edges_to_type ON work_edges(to_work_id, edge_type);
CREATE INDEX IF NOT EXISTS idx_work_edges_from_type ON work_edges(from_work_id, edge_type);
CREATE INDEX IF NOT EXISTS idx_work_updates_work_id_created_at ON work_updates(work_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_work_notes_work_id_created_at ON work_notes(work_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_work_proposals_target_state_created_at ON work_proposals(target_work_id, state, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_attestation_records_subject_created_at ON attestation_records(subject_kind, subject_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_approval_records_work_id_approved_at ON approval_records(work_id, approved_at DESC);
CREATE INDEX IF NOT EXISTS idx_promotion_records_work_id_promoted_at ON promotion_records(work_id, promoted_at DESC);

CREATE TABLE IF NOT EXISTS doc_content (
    doc_id      TEXT PRIMARY KEY,
    work_id     TEXT NOT NULL REFERENCES work_items(work_id) ON DELETE CASCADE,
    path        TEXT NOT NULL DEFAULT '',
    title       TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL DEFAULT '',
    format      TEXT NOT NULL DEFAULT 'markdown',
    version     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE(work_id, path)
);

CREATE INDEX IF NOT EXISTS idx_doc_content_work_id ON doc_content(work_id);
`

// ── Doc Content ─────────────────────────────────────────────

func (s *Store) UpsertDocContent(ctx context.Context, rec core.DocContentRecord) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO doc_content (doc_id, work_id, path, title, body, format, version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(work_id, path) DO UPDATE SET
		   title = excluded.title,
		   body = excluded.body,
		   format = excluded.format,
		   version = version + 1,
		   updated_at = excluded.updated_at`,
		rec.DocID, rec.WorkID, rec.Path, rec.Title, rec.Body, rec.Format, 1, now, now)
	return err
}

func (s *Store) GetDocContent(ctx context.Context, workID string) ([]core.DocContentRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT doc_id, work_id, path, title, body, format, version, created_at, updated_at
		   FROM doc_content WHERE work_id = ? ORDER BY path`, workID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var docs []core.DocContentRecord
	for rows.Next() {
		var d core.DocContentRecord
		var createdAt, updatedAt string
		if err := rows.Scan(&d.DocID, &d.WorkID, &d.Path, &d.Title, &d.Body, &d.Format, &d.Version, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		d.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		d.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

func (s *Store) GetDocContentByPath(ctx context.Context, path string) (*core.DocContentRecord, error) {
	var d core.DocContentRecord
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT doc_id, work_id, path, title, body, format, version, created_at, updated_at
		   FROM doc_content WHERE path = ?`, path).
		Scan(&d.DocID, &d.WorkID, &d.Path, &d.Title, &d.Body, &d.Format, &d.Version, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	d.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	d.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &d, nil
}

// ── Private DB ──────────────────────────────────────────────

const privateSchema = `
CREATE TABLE IF NOT EXISTS private_notes (
    note_id         TEXT PRIMARY KEY,
    work_id         TEXT NOT NULL,
    note_type       TEXT NOT NULL DEFAULT 'private',
    text            TEXT NOT NULL,
    created_by      TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    supersedes_note_id TEXT
);

CREATE INDEX IF NOT EXISTS idx_private_notes_work_id ON private_notes(work_id, created_at DESC);
`

func (s *Store) bootstrapPrivate(ctx context.Context) error {
	statements := []string{
		`PRAGMA foreign_keys = ON;`,
		`PRAGMA busy_timeout = 5000;`,
		`PRAGMA journal_mode = WAL;`,
		privateSchema,
	}
	for _, stmt := range statements {
		if _, err := s.privateDB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply private sqlite bootstrap: %w", err)
		}
	}
	return nil
}

// AddPrivateNote stores a note in the private (gitignored) database.
func (s *Store) AddPrivateNote(ctx context.Context, noteID, workID, noteType, text, createdBy string) error {
	if s.privateDB == nil {
		return fmt.Errorf("private database not available")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.privateDB.ExecContext(ctx,
		`INSERT INTO private_notes (note_id, work_id, note_type, text, created_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		noteID, workID, noteType, text, createdBy, now)
	return err
}

// ListPrivateNotes returns private notes for a work item.
func (s *Store) ListPrivateNotes(ctx context.Context, workID string, limit int) ([]core.WorkNoteRecord, error) {
	if s.privateDB == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.privateDB.QueryContext(ctx,
		`SELECT note_id, work_id, note_type, text, created_by, created_at, supersedes_note_id
		   FROM private_notes WHERE work_id = ? ORDER BY created_at DESC LIMIT ?`,
		workID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var notes []core.WorkNoteRecord
	for rows.Next() {
		var n core.WorkNoteRecord
		var supersedes sql.NullString
		if err := rows.Scan(&n.NoteID, &n.WorkID, &n.NoteType, &n.Body, &n.CreatedBy, &n.CreatedAt, &supersedes); err != nil {
			return nil, err
		}
		// supersedes_note_id stored but not surfaced on WorkNoteRecord — ignore for now
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

// AllPrivateNotes returns all private notes across all work items.
func (s *Store) AllPrivateNotes(ctx context.Context) ([]core.WorkNoteRecord, error) {
	if s.privateDB == nil {
		return nil, nil
	}
	rows, err := s.privateDB.QueryContext(ctx,
		`SELECT note_id, work_id, note_type, text, created_by, created_at, supersedes_note_id
		   FROM private_notes ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var notes []core.WorkNoteRecord
	for rows.Next() {
		var n core.WorkNoteRecord
		var supersedes sql.NullString
		if err := rows.Scan(&n.NoteID, &n.WorkID, &n.NoteType, &n.Body, &n.CreatedBy, &n.CreatedAt, &supersedes); err != nil {
			return nil, err
		}
		// supersedes_note_id stored but not surfaced on WorkNoteRecord — ignore for now
		notes = append(notes, n)
	}
	return notes, rows.Err()
}
