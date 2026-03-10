package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yusefmosiah/cagent/internal/adapterapi"
	"github.com/yusefmosiah/cagent/internal/adapters"
	catalogpkg "github.com/yusefmosiah/cagent/internal/catalog"
	"github.com/yusefmosiah/cagent/internal/core"
	debriefpkg "github.com/yusefmosiah/cagent/internal/debrief"
	"github.com/yusefmosiah/cagent/internal/events"
	transferpkg "github.com/yusefmosiah/cagent/internal/handoff"
	"github.com/yusefmosiah/cagent/internal/pricing"
	"github.com/yusefmosiah/cagent/internal/store"
)

var (
	ErrNotFound           = errors.New("not found")
	ErrUnsupported        = errors.New("unsupported operation")
	ErrAdapterUnavailable = errors.New("adapter not available")
	ErrInvalidInput       = errors.New("invalid input")
	ErrBusy               = errors.New("resource busy")
	ErrSessionLocked      = errors.New("session locked")
	ErrVendorProcess      = errors.New("vendor process failed")
	ErrTimeout            = errors.New("timeout")
)

type Service struct {
	Paths         core.Paths
	Config        core.Config
	ConfigPath    string
	ConfigPresent bool
	store         *store.Store
}

type RunRequest struct {
	Adapter         string
	CWD             string
	Prompt          string
	PromptSource    string
	Label           string
	Model           string
	Profile         string
	EnvFile         string
	ArtifactDir     string
	SessionID       string
	ParentSessionID string
	TransferID      string
}

type SendRequest struct {
	SessionID    string
	Adapter      string
	Prompt       string
	PromptSource string
	Model        string
	Profile      string
}

type DebriefRequest struct {
	SessionID  string
	Adapter    string
	Model      string
	Profile    string
	OutputPath string
	Reason     string
}

type RunResult struct {
	Job     core.JobRecord     `json:"job"`
	Session core.SessionRecord `json:"session"`
	Message string             `json:"message,omitempty"`
}

type DebriefResult struct {
	Job     core.JobRecord     `json:"job"`
	Session core.SessionRecord `json:"session"`
	Path    string             `json:"path"`
	Message string             `json:"message,omitempty"`
}

type TransferExportRequest struct {
	JobID      string
	SessionID  string
	OutputPath string
	Reason     string
	Mode       string
}

type TransferExportResult struct {
	Transfer core.TransferRecord `json:"transfer"`
	Path     string              `json:"path"`
}

type TransferRunRequest struct {
	TransferRef string
	Adapter     string
	CWD         string
	Model       string
	Profile     string
	Label       string
}

type StatusResult struct {
	Job            core.JobRecord             `json:"job"`
	Session        core.SessionRecord         `json:"session"`
	NativeSessions []core.NativeSessionRecord `json:"native_sessions"`
	Events         []core.EventRecord         `json:"events"`
	Usage          *core.UsageReport          `json:"usage,omitempty"`
	Cost           *core.CostEstimate         `json:"cost,omitempty"`
}

type SessionAction struct {
	Action          string `json:"action"`
	Adapter         string `json:"adapter"`
	NativeSessionID string `json:"native_session_id"`
	Available       bool   `json:"available"`
	Reason          string `json:"reason,omitempty"`
}

type SessionResult struct {
	Session        core.SessionRecord         `json:"session"`
	NativeSessions []core.NativeSessionRecord `json:"native_sessions"`
	Turns          []core.TurnRecord          `json:"turns"`
	RecentJobs     []core.JobRecord           `json:"recent_jobs"`
	Actions        []SessionAction            `json:"actions"`
}

type RuntimeAdapter struct {
	Adapter      string                  `json:"adapter"`
	Binary       string                  `json:"binary"`
	Version      *string                 `json:"version,omitempty"`
	Enabled      bool                    `json:"enabled"`
	Available    bool                    `json:"available"`
	Implemented  bool                    `json:"implemented"`
	Capabilities adapterapi.Capabilities `json:"capabilities"`
	Summary      string                  `json:"summary,omitempty"`
	Speed        string                  `json:"speed,omitempty"`
	Cost         string                  `json:"cost,omitempty"`
	Tags         []string                `json:"tags,omitempty"`
}

type RuntimeResult struct {
	ConfigPath    string              `json:"config_path"`
	ConfigPresent bool                `json:"config_present"`
	Paths         core.Paths          `json:"paths"`
	Defaults      core.DefaultsConfig `json:"defaults"`
	Adapters      []RuntimeAdapter    `json:"adapters"`
}

type CatalogResult struct {
	Snapshot core.CatalogSnapshot `json:"snapshot"`
}

type RawLogEntry struct {
	Stream  string `json:"stream"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ArtifactsRequest struct {
	JobID     string
	SessionID string
	Kind      string
	Limit     int
}

type ArtifactResult struct {
	Artifact core.ArtifactRecord `json:"artifact"`
	Content  string              `json:"content,omitempty"`
}

type ListJobsRequest struct {
	Limit     int
	Adapter   string
	State     string
	SessionID string
}

type ListSessionsRequest struct {
	Limit   int
	Adapter string
	Status  string
}

type lineItem struct {
	stream string
	line   string
}

type startExecutionOptions struct {
	Prompt            string
	PromptSource      string
	Model             string
	Profile           string
	Continue          bool
	NativeSessionID   string
	NativeSessionMeta map[string]any
}

type continuationRequest struct {
	Prompt       string
	PromptSource string
	Model        string
	Profile      string
	Summary      map[string]any
}

func Open(ctx context.Context, configPath string) (*Service, error) {
	paths, err := core.ResolvePaths()
	if err != nil {
		return nil, fmt.Errorf("resolve runtime paths: %w", err)
	}

	resolvedConfigPath := paths.ConfigPath
	if configPath != "" {
		resolvedConfigPath, err = core.ExpandPath(configPath)
		if err != nil {
			return nil, fmt.Errorf("expand config path: %w", err)
		}
	}
	_, statErr := os.Stat(resolvedConfigPath)
	configPresent := statErr == nil

	cfg, err := core.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	if cfg.Store.StateDir != "" {
		stateDir, err := core.ExpandPath(cfg.Store.StateDir)
		if err != nil {
			return nil, fmt.Errorf("expand state dir: %w", err)
		}
		paths = paths.WithStateDir(stateDir)
	}

	if err := core.EnsurePaths(paths); err != nil {
		return nil, fmt.Errorf("ensure runtime paths: %w", err)
	}

	db, err := store.Open(ctx, paths.DBPath)
	if err != nil {
		return nil, err
	}

	return &Service{
		Paths:         paths,
		Config:        cfg,
		ConfigPath:    resolvedConfigPath,
		ConfigPresent: configPresent,
		store:         db,
	}, nil
}

func (s *Service) Close() error {
	return s.store.Close()
}

func (s *Service) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	cwd, err := filepath.Abs(req.CWD)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve cwd: %v", ErrInvalidInput, err)
	}
	if stat, err := os.Stat(cwd); err != nil || !stat.IsDir() {
		return nil, fmt.Errorf("%w: cwd must be an existing directory", ErrInvalidInput)
	}
	if req.Prompt == "" {
		return nil, fmt.Errorf("%w: prompt must not be empty", ErrInvalidInput)
	}

	if _, _, err := s.resolveAdapter(ctx, req.Adapter); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	jobID := core.GenerateID("job")
	sessionID := req.SessionID
	var session core.SessionRecord

	if sessionID == "" {
		var parentSession *string
		if req.ParentSessionID != "" {
			parent := req.ParentSessionID
			parentSession = &parent
		}
		metadata := map[string]any{}
		if req.TransferID != "" {
			metadata["source_transfer_id"] = req.TransferID
		}
		sessionID = core.GenerateID("ses")
		session = core.SessionRecord{
			SessionID:     sessionID,
			Label:         req.Label,
			CreatedAt:     now,
			UpdatedAt:     now,
			Status:        "active",
			OriginAdapter: req.Adapter,
			OriginJobID:   jobID,
			CWD:           cwd,
			LatestJobID:   jobID,
			ParentSession: parentSession,
			Tags:          []string{},
			Metadata:      metadata,
		}
	} else {
		session, err = s.store.GetSession(ctx, sessionID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("%w: session %s", ErrNotFound, sessionID)
			}
			return nil, err
		}
		session.LatestJobID = jobID
		session.UpdatedAt = now
	}

	job := core.JobRecord{
		JobID:     jobID,
		SessionID: sessionID,
		Adapter:   req.Adapter,
		State:     core.JobStateCreated,
		Label:     req.Label,
		CWD:       cwd,
		CreatedAt: now,
		UpdatedAt: now,
		Summary: map[string]any{
			"prompt_source": req.PromptSource,
		},
	}
	if req.Model != "" {
		job.Summary["model"] = req.Model
	}
	if req.Profile != "" {
		job.Summary["profile"] = req.Profile
	}
	if req.TransferID != "" {
		job.Summary["transfer_id"] = req.TransferID
	}
	if req.SessionID == "" {
		if err := s.store.CreateSessionAndJob(ctx, session, job); err != nil {
			return nil, err
		}
	} else {
		if err := s.store.CreateJobAndUpdateSession(ctx, sessionID, now, job); err != nil {
			return nil, err
		}
	}

	result := &RunResult{
		Job:     job,
		Session: session,
	}

	turn := core.TurnRecord{
		TurnID:      core.GenerateID("turn"),
		SessionID:   session.SessionID,
		JobID:       job.JobID,
		Adapter:     job.Adapter,
		StartedAt:   now,
		InputText:   req.Prompt,
		InputSource: req.PromptSource,
		Status:      string(core.JobStateCreated),
		Stats:       map[string]any{},
	}

	if err := s.prepareJobLifecycle(ctx, &job, &turn, startExecutionOptions{
		Prompt:       req.Prompt,
		PromptSource: req.PromptSource,
		Model:        req.Model,
		Profile:      req.Profile,
	}); err != nil {
		return result, err
	}
	result.Message, err = s.queuePreparedJob(ctx, &job, &turn)
	result.Job = job
	return result, err
}

func (s *Service) Send(ctx context.Context, req SendRequest) (*RunResult, error) {
	if req.SessionID == "" {
		return nil, fmt.Errorf("%w: session must not be empty", ErrInvalidInput)
	}
	if req.Prompt == "" {
		return nil, fmt.Errorf("%w: prompt must not be empty", ErrInvalidInput)
	}

	session, err := s.store.GetSession(ctx, req.SessionID)
	if err != nil {
		return nil, normalizeStoreError("session", req.SessionID, err)
	}

	active, err := s.store.FindActiveJobBySession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return nil, fmt.Errorf("%w: session %s already has active job %s", ErrSessionLocked, req.SessionID, active.JobID)
	}

	target, err := s.resolveContinuationTarget(ctx, session, req.Adapter)
	if err != nil {
		return nil, err
	}

	_, descriptor, err := s.resolveAdapter(ctx, target.Adapter)
	if err != nil {
		return nil, err
	}
	if !descriptor.Capabilities.NativeResume {
		return nil, fmt.Errorf("%w: adapter %q does not support continuation", ErrUnsupported, target.Adapter)
	}

	return s.queueContinuation(ctx, session, target, continuationRequest{
		Prompt:       req.Prompt,
		PromptSource: req.PromptSource,
		Model:        req.Model,
		Profile:      req.Profile,
		Summary: map[string]any{
			"prompt_source": req.PromptSource,
			"continued":     true,
		},
	})
}

func (s *Service) Debrief(ctx context.Context, req DebriefRequest) (*DebriefResult, error) {
	if req.SessionID == "" {
		return nil, fmt.Errorf("%w: session must not be empty", ErrInvalidInput)
	}

	session, err := s.store.GetSession(ctx, req.SessionID)
	if err != nil {
		return nil, normalizeStoreError("session", req.SessionID, err)
	}

	active, err := s.store.FindActiveJobBySession(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return nil, fmt.Errorf("%w: session %s already has active job %s", ErrSessionLocked, req.SessionID, active.JobID)
	}

	target, err := s.resolveContinuationTarget(ctx, session, req.Adapter)
	if err != nil {
		return nil, err
	}

	outputPath, err := s.resolveDebriefOutputPath(req.OutputPath, session.SessionID, "")
	if err != nil {
		return nil, err
	}
	prompt := debriefpkg.RenderPrompt(session, target.Adapter, req.Reason)

	runResult, err := s.queueContinuation(ctx, session, target, continuationRequest{
		Prompt:       prompt,
		PromptSource: "debrief",
		Model:        req.Model,
		Profile:      req.Profile,
		Summary: map[string]any{
			"prompt_source":      "debrief",
			"continued":          true,
			"debrief":            true,
			"debrief_reason":     normalizeDebriefReason(req.Reason),
			"debrief_path":       outputPath,
			"debrief_format":     "markdown",
			"debrief_requested":  true,
			"debrief_source_job": session.LatestJobID,
		},
	})
	if runResult == nil {
		return nil, err
	}

	path, _ := runResult.Job.Summary["debrief_path"].(string)
	if path == "" {
		path = outputPath
	}
	return &DebriefResult{
		Job:     runResult.Job,
		Session: runResult.Session,
		Path:    path,
		Message: runResult.Message,
	}, err
}

func (s *Service) Session(ctx context.Context, sessionID string) (*SessionResult, error) {
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, normalizeStoreError("session", sessionID, err)
	}

	nativeSessions, err := s.store.ListNativeSessions(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	turns, err := s.store.ListTurnsBySession(ctx, sessionID, 20)
	if err != nil {
		return nil, err
	}

	recentJobs, err := s.store.ListJobsBySession(ctx, sessionID, 10)
	if err != nil {
		return nil, err
	}

	active, err := s.store.FindActiveJobBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	actions := make([]SessionAction, 0, len(nativeSessions))
	for _, native := range nativeSessions {
		available := native.Resumable && active == nil && native.LockedByJobID == ""
		reason := ""
		switch {
		case !native.Resumable:
			reason = "adapter does not declare native continuation"
		case active != nil:
			reason = fmt.Sprintf("active job %s is still running", active.JobID)
		case native.LockedByJobID != "":
			reason = fmt.Sprintf("native session lock held by job %s", native.LockedByJobID)
		}
		actions = append(actions,
			SessionAction{
				Action:          "send",
				Adapter:         native.Adapter,
				NativeSessionID: native.NativeSessionID,
				Available:       available,
				Reason:          reason,
			},
			SessionAction{
				Action:          "debrief",
				Adapter:         native.Adapter,
				NativeSessionID: native.NativeSessionID,
				Available:       available,
				Reason:          reason,
			},
		)
	}

	return &SessionResult{
		Session:        session,
		NativeSessions: nativeSessions,
		Turns:          turns,
		RecentJobs:     recentJobs,
		Actions:        actions,
	}, nil
}

func (s *Service) Runtime(ctx context.Context, adapterName string) (*RuntimeResult, error) {
	catalog := adapters.CatalogFromConfig(s.Config)
	entries := make([]RuntimeAdapter, 0, len(catalog))
	for _, entry := range catalog {
		if adapterName != "" && entry.Adapter != adapterName {
			continue
		}

		cfg, ok := s.Config.Adapters.ByName(entry.Adapter)
		if !ok {
			continue
		}

		entries = append(entries, RuntimeAdapter{
			Adapter:      entry.Adapter,
			Binary:       entry.Binary,
			Version:      entry.Version,
			Enabled:      entry.Enabled,
			Available:    entry.Available,
			Implemented:  entry.Implemented,
			Capabilities: entry.Capabilities,
			Summary:      cfg.Summary,
			Speed:        cfg.Speed,
			Cost:         cfg.Cost,
			Tags:         append([]string(nil), cfg.Tags...),
		})
	}
	if adapterName != "" && len(entries) == 0 {
		return nil, fmt.Errorf("%w: unknown adapter %q", ErrInvalidInput, adapterName)
	}

	return &RuntimeResult{
		ConfigPath:    s.ConfigPath,
		ConfigPresent: s.ConfigPresent,
		Paths:         s.Paths,
		Defaults:      s.Config.Defaults,
		Adapters:      entries,
	}, nil
}

func (s *Service) SyncCatalog(ctx context.Context) (*CatalogResult, error) {
	snapshot := catalogpkg.Snapshot(ctx, s.Config, nil)
	for idx := range snapshot.Entries {
		entry := &snapshot.Entries[idx]
		entry.Pricing = pricing.Resolve(s.Config, entry.Provider, entry.Model)
	}
	if err := s.store.CreateCatalogSnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	return &CatalogResult{Snapshot: snapshot}, nil
}

func (s *Service) Catalog(ctx context.Context) (*CatalogResult, error) {
	snapshot, err := s.store.LatestCatalogSnapshot(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("%w: catalog snapshot", ErrNotFound)
		}
		return nil, err
	}
	return &CatalogResult{Snapshot: snapshot}, nil
}

func (s *Service) ExportTransfer(ctx context.Context, req TransferExportRequest) (*TransferExportResult, error) {
	if (req.JobID == "" && req.SessionID == "") || (req.JobID != "" && req.SessionID != "") {
		return nil, fmt.Errorf("%w: specify exactly one of job_id or session_id", ErrInvalidInput)
	}

	var (
		job     core.JobRecord
		session core.SessionRecord
		err     error
	)
	switch {
	case req.JobID != "":
		job, err = s.store.GetJob(ctx, req.JobID)
		if err != nil {
			return nil, normalizeStoreError("job", req.JobID, err)
		}
		session, err = s.store.GetSession(ctx, job.SessionID)
		if err != nil {
			return nil, normalizeStoreError("session", job.SessionID, err)
		}
	default:
		session, err = s.store.GetSession(ctx, req.SessionID)
		if err != nil {
			return nil, normalizeStoreError("session", req.SessionID, err)
		}
		if session.LatestJobID == "" {
			return nil, fmt.Errorf("%w: session %s has no jobs to export", ErrNotFound, session.SessionID)
		}
		job, err = s.store.GetJob(ctx, session.LatestJobID)
		if err != nil {
			return nil, normalizeStoreError("job", session.LatestJobID, err)
		}
	}

	turns, err := s.store.ListTurnsBySession(ctx, session.SessionID, 5)
	if err != nil {
		return nil, err
	}
	events, err := s.store.ListEventsBySession(ctx, session.SessionID, 20)
	if err != nil {
		return nil, err
	}
	artifacts, err := s.store.ListArtifactsBySession(ctx, session.SessionID, 10)
	if err != nil {
		return nil, err
	}

	packet := s.buildTransferPacket(job, session, turns, events, artifacts, req.Reason, req.Mode)
	packet, path, err := s.writeTransferBundle(packet, req.OutputPath, turns, events)
	if err != nil {
		return nil, err
	}
	record := core.TransferRecord{
		TransferID: packet.TransferID,
		JobID:      job.JobID,
		SessionID:  session.SessionID,
		CreatedAt:  packet.ExportedAt,
		Packet:     packet,
	}
	if err := s.store.CreateTransfer(ctx, record); err != nil {
		return nil, err
	}
	if err := s.store.InsertArtifact(ctx, core.ArtifactRecord{
		ArtifactID: core.GenerateID("art"),
		JobID:      job.JobID,
		SessionID:  session.SessionID,
		Kind:       "transfer",
		Path:       path,
		CreatedAt:  packet.ExportedAt,
		Metadata: map[string]any{
			"transfer_id": packet.TransferID,
			"mode":        packet.Mode,
		},
	}); err != nil {
		return nil, err
	}
	if _, err := s.emitEvent(ctx, job, "transfer.exported", "transfer", map[string]any{
		"transfer_id": packet.TransferID,
		"path":        path,
		"mode":        packet.Mode,
		"reason":      packet.Reason,
	}, "", nil); err != nil {
		return nil, err
	}

	return &TransferExportResult{
		Transfer: record,
		Path:     path,
	}, nil
}

func (s *Service) RunTransfer(ctx context.Context, req TransferRunRequest) (*RunResult, error) {
	if req.TransferRef == "" {
		return nil, fmt.Errorf("%w: transfer must not be empty", ErrInvalidInput)
	}
	if req.Adapter == "" {
		return nil, fmt.Errorf("%w: adapter must not be empty", ErrInvalidInput)
	}
	if _, _, err := s.resolveAdapter(ctx, req.Adapter); err != nil {
		return nil, err
	}

	record, err := s.loadTransfer(ctx, req.TransferRef)
	if err != nil {
		return nil, err
	}

	cwd := req.CWD
	if cwd == "" {
		if session, sessionErr := s.store.GetSession(ctx, record.Packet.Source.SessionID); sessionErr == nil {
			cwd = session.CWD
		}
	}
	if cwd == "" {
		return nil, fmt.Errorf("%w: cwd is required when the source session is not available locally", ErrInvalidInput)
	}

	prompt := transferpkg.RenderPrompt(req.Adapter, record.Packet)
	result, runErr := s.Run(ctx, RunRequest{
		Adapter:         req.Adapter,
		CWD:             cwd,
		Prompt:          prompt,
		PromptSource:    "transfer",
		Label:           req.Label,
		Model:           req.Model,
		Profile:         req.Profile,
		ParentSessionID: record.Packet.Source.SessionID,
		TransferID:      record.Packet.TransferID,
	})
	if result != nil {
		if _, err := s.emitEvent(ctx, result.Job, "transfer.started", "transfer", map[string]any{
			"transfer_id":    record.Packet.TransferID,
			"source_adapter": record.Packet.Source.Adapter,
			"mode":           record.Packet.Mode,
			"reason":         record.Packet.Reason,
		}, "", nil); err != nil {
			return result, err
		}
	}

	return result, runErr
}

func (s *Service) Status(ctx context.Context, jobID string) (*StatusResult, error) {
	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, normalizeStoreError("job", jobID, err)
	}

	session, err := s.store.GetSession(ctx, job.SessionID)
	if err != nil {
		return nil, normalizeStoreError("session", job.SessionID, err)
	}

	nativeSessions, err := s.store.ListNativeSessions(ctx, job.SessionID)
	if err != nil {
		return nil, err
	}

	events, err := s.store.ListEvents(ctx, jobID, 50)
	if err != nil {
		return nil, err
	}

	return &StatusResult{
		Job:            job,
		Session:        session,
		NativeSessions: nativeSessions,
		Events:         events,
		Usage:          usageFromSummary(job.Summary),
		Cost:           s.costFromSummary(job),
	}, nil
}

func (s *Service) WaitStatus(ctx context.Context, jobID string, interval, timeout time.Duration) (*StatusResult, error) {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	var (
		timer  <-chan time.Time
		status *StatusResult
		err    error
	)
	if timeout > 0 {
		timeoutTimer := time.NewTimer(timeout)
		defer timeoutTimer.Stop()
		timer = timeoutTimer.C
	}

	for {
		status, err = s.Status(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if status.Job.State.Terminal() {
			return status, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer:
			return status, fmt.Errorf("%w: job %s did not reach a terminal state within %s", ErrTimeout, jobID, timeout)
		case <-time.After(interval):
		}
	}
}

func (s *Service) ListJobs(ctx context.Context, req ListJobsRequest) ([]core.JobRecord, error) {
	return s.store.ListJobsFiltered(ctx, req.Limit, req.Adapter, req.State, req.SessionID)
}

func (s *Service) ListSessions(ctx context.Context, req ListSessionsRequest) ([]core.SessionRecord, error) {
	return s.store.ListSessions(ctx, req.Limit, req.Adapter, req.Status)
}

func (s *Service) Logs(ctx context.Context, jobID string, limit int) ([]core.EventRecord, error) {
	if _, err := s.store.GetJob(ctx, jobID); err != nil {
		return nil, normalizeStoreError("job", jobID, err)
	}
	return s.store.ListEvents(ctx, jobID, limit)
}

func (s *Service) LogsAfter(ctx context.Context, jobID string, afterSeq int64, limit int) ([]core.EventRecord, error) {
	if _, err := s.store.GetJob(ctx, jobID); err != nil {
		return nil, normalizeStoreError("job", jobID, err)
	}
	return s.store.ListEventsAfter(ctx, jobID, afterSeq, limit)
}

func (s *Service) RawLogs(ctx context.Context, jobID string, limit int) ([]RawLogEntry, error) {
	events, err := s.Logs(ctx, jobID, limit)
	if err != nil {
		return nil, err
	}
	return s.rawLogsFromEvents(events)
}

func (s *Service) ListArtifacts(ctx context.Context, req ArtifactsRequest) ([]core.ArtifactRecord, error) {
	if (req.JobID == "" && req.SessionID == "") || (req.JobID != "" && req.SessionID != "") {
		return nil, fmt.Errorf("%w: specify exactly one of job_id or session_id", ErrInvalidInput)
	}
	if req.JobID != "" {
		if _, err := s.store.GetJob(ctx, req.JobID); err != nil {
			return nil, normalizeStoreError("job", req.JobID, err)
		}
	}
	if req.SessionID != "" {
		if _, err := s.store.GetSession(ctx, req.SessionID); err != nil {
			return nil, normalizeStoreError("session", req.SessionID, err)
		}
	}
	return s.store.ListArtifactsFiltered(ctx, req.JobID, req.SessionID, req.Kind, req.Limit)
}

func (s *Service) ReadArtifact(ctx context.Context, artifactID string) (*ArtifactResult, error) {
	artifact, err := s.store.GetArtifact(ctx, artifactID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("%w: artifact %s", ErrNotFound, artifactID)
		}
		return nil, err
	}

	path := artifact.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.Paths.StateDir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read artifact %q: %w", path, err)
	}
	artifact.Path = path
	return &ArtifactResult{
		Artifact: artifact,
		Content:  string(data),
	}, nil
}

func (s *Service) RawLogsAfter(ctx context.Context, jobID string, afterSeq int64, limit int) ([]RawLogEntry, []core.EventRecord, error) {
	events, err := s.LogsAfter(ctx, jobID, afterSeq, limit)
	if err != nil {
		return nil, nil, err
	}
	logs, err := s.rawLogsFromEvents(events)
	return logs, events, err
}

func (s *Service) rawLogsFromEvents(events []core.EventRecord) ([]RawLogEntry, error) {
	var logs []RawLogEntry
	for _, event := range events {
		if event.RawRef == "" {
			continue
		}

		path := filepath.Join(s.Paths.StateDir, event.RawRef)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read raw artifact %q: %w", path, err)
		}

		logs = append(logs, RawLogEntry{
			Stream:  streamFromRawRef(event.RawRef),
			Path:    path,
			Content: string(data),
		})
	}

	return logs, nil
}

func (s *Service) Cancel(ctx context.Context, jobID string) (*core.JobRecord, error) {
	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, normalizeStoreError("job", jobID, err)
	}

	if job.State.Terminal() {
		return &job, nil
	}

	now := time.Now().UTC()
	if err := s.upsertJobRuntime(ctx, job.JobID, func(rec *core.JobRuntimeRecord) {
		rec.CancelRequestedAt = &now
	}); err != nil {
		return nil, err
	}

	runtimeRec, runtimeErr := s.store.GetJobRuntime(ctx, job.JobID)
	if runtimeErr != nil && !errors.Is(runtimeErr, store.ErrNotFound) {
		return nil, runtimeErr
	}

	signals := []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL}
	delays := []time.Duration{1500 * time.Millisecond, 1500 * time.Millisecond, 0}
	for idx, sig := range signals {
		if runtimeErr == nil {
			if runtimeRec.VendorPID != 0 {
				_ = signalProcessGroup(runtimeRec.VendorPID, sig)
			} else if runtimeRec.SupervisorPID != 0 {
				_ = signalProcessGroup(runtimeRec.SupervisorPID, sig)
			}
		}

		if delays[idx] == 0 {
			break
		}
		waitUntil := time.Now().Add(delays[idx])
		for time.Now().Before(waitUntil) {
			current, err := s.store.GetJob(ctx, jobID)
			if err == nil && current.State.Terminal() {
				return &current, nil
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	current, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if current.State.Terminal() {
		return &current, nil
	}

	return &current, fmt.Errorf("%w: job %s did not exit after cancellation signals", ErrBusy, jobID)
}

func (s *Service) queuePreparedJob(ctx context.Context, job *core.JobRecord, turn *core.TurnRecord) (string, error) {
	if err := s.transitionJob(ctx, job, core.JobStateQueued, map[string]any{"message": "job queued for background execution"}); err != nil {
		return "", err
	}
	turn.Status = string(job.State)
	if err := s.store.UpdateTurn(ctx, *turn); err != nil {
		return "", err
	}

	pid, err := s.launchDetachedWorker(job.JobID, turn.TurnID)
	if err != nil {
		message := fmt.Sprintf("failed to launch background worker: %v", err)
		if failErr := s.failPreparedJobLifecycle(ctx, job, turn, message); failErr != nil {
			return "", failErr
		}
		return message, fmt.Errorf("%w: %s", ErrBusy, message)
	}
	if err := s.upsertJobRuntime(ctx, job.JobID, func(rec *core.JobRuntimeRecord) {
		rec.Detached = true
		rec.SupervisorPID = pid
	}); err != nil {
		return "", err
	}

	message := fmt.Sprintf("job launched as background worker pid %d", pid)
	job.Summary["message"] = message
	if err := s.store.UpdateJob(ctx, *job); err != nil {
		return "", err
	}
	return message, nil
}

func (s *Service) prepareJobLifecycle(
	ctx context.Context,
	job *core.JobRecord,
	turn *core.TurnRecord,
	opts startExecutionOptions,
) error {
	if err := s.store.CreateTurn(ctx, *turn); err != nil {
		return err
	}

	if _, err := s.emitEvent(ctx, *job, "job.created", "lifecycle", map[string]any{
		"cwd":           job.CWD,
		"label":         job.Label,
		"prompt_source": opts.PromptSource,
		"continued":     opts.Continue,
	}, "", nil); err != nil {
		return err
	}

	rawPrompt, _ := json.Marshal(map[string]any{
		"prompt":    opts.Prompt,
		"source":    opts.PromptSource,
		"continued": opts.Continue,
	})
	if _, err := s.emitEvent(ctx, *job, "user.message", "input", map[string]any{
		"text":   opts.Prompt,
		"source": opts.PromptSource,
	}, "native", rawPrompt); err != nil {
		return err
	}

	return nil
}

func (s *Service) startPreparedJobLifecycle(
	ctx context.Context,
	adapter adapterapi.Adapter,
	descriptor adapters.Diagnosis,
	job *core.JobRecord,
	turn *core.TurnRecord,
	opts startExecutionOptions,
) (string, error) {
	if err := s.transitionJob(ctx, job, core.JobStateStarting, map[string]any{"message": "job starting"}); err != nil {
		return "", err
	}
	turn.Status = string(job.State)
	if err := s.store.UpdateTurn(ctx, *turn); err != nil {
		return "", err
	}

	var (
		message string
		runErr  error
	)
	cancelRequested := false
	switch {
	case !descriptor.Available:
		message = fmt.Sprintf("adapter %q binary %q is not available on PATH", job.Adapter, descriptor.Binary)
		runErr = fmt.Errorf("%w: %s", ErrAdapterUnavailable, message)
	case !descriptor.Implemented:
		message = fmt.Sprintf("adapter %q is detected but not implemented in this build yet", job.Adapter)
		runErr = fmt.Errorf("%w: %s", ErrUnsupported, message)
	default:
		message, runErr = s.executeAdapter(ctx, adapter, job, opts)
	}
	cancelRequested = s.isCancelRequested(ctx, job.JobID)
	if runErr != nil && !cancelRequested {
		if _, err := s.emitEvent(ctx, *job, "diagnostic", "translation", map[string]any{
			"message": message,
		}, "", nil); err != nil {
			return message, err
		}
		if _, err := s.emitEvent(ctx, *job, "process.stderr", "execution", map[string]any{
			"message": message,
		}, "stderr", []byte(message+"\n")); err != nil {
			return message, err
		}
	}

	job.Summary["message"] = message
	if err := s.store.UpdateJob(ctx, *job); err != nil {
		return message, err
	}

	terminalState := core.JobStateCompleted
	terminalEvent := "job.completed"
	if cancelRequested {
		terminalState = core.JobStateCancelled
		terminalEvent = "job.cancelled"
		if message == "" {
			message = "job cancelled"
		}
	} else if runErr != nil {
		terminalState = core.JobStateFailed
		terminalEvent = "job.failed"
	}
	if err := s.finishJob(ctx, job, terminalState); err != nil {
		return message, err
	}
	if terminalState == core.JobStateCompleted {
		if err := s.persistDebrief(ctx, job, message); err != nil {
			return message, err
		}
	}
	if _, err := s.emitEvent(ctx, *job, terminalEvent, "lifecycle", map[string]any{
		"message": message,
	}, "", nil); err != nil {
		return message, err
	}

	turn.CompletedAt = job.FinishedAt
	turn.ResultSummary = message
	turn.Status = string(job.State)
	turn.NativeSessionID = job.NativeSessionID
	if err := s.store.UpdateTurn(ctx, *turn); err != nil {
		return message, err
	}
	if err := s.upsertJobRuntime(ctx, job.JobID, func(rec *core.JobRuntimeRecord) {
		completedAt := time.Now().UTC()
		rec.VendorPID = 0
		rec.CompletedAt = &completedAt
	}); err != nil {
		return message, err
	}

	return message, runErr
}

func (s *Service) failPreparedJobLifecycle(ctx context.Context, job *core.JobRecord, turn *core.TurnRecord, message string) error {
	job.Summary["message"] = message
	if err := s.store.UpdateJob(ctx, *job); err != nil {
		return err
	}
	if _, err := s.emitEvent(ctx, *job, "diagnostic", "execution", map[string]any{
		"message": message,
	}, "", nil); err != nil {
		return err
	}
	if err := s.finishJob(ctx, job, core.JobStateFailed); err != nil {
		return err
	}
	if _, err := s.emitEvent(ctx, *job, "job.failed", "lifecycle", map[string]any{
		"message": message,
	}, "", nil); err != nil {
		return err
	}
	turn.CompletedAt = job.FinishedAt
	turn.ResultSummary = message
	turn.Status = string(job.State)
	return s.store.UpdateTurn(ctx, *turn)
}

func (s *Service) executeAdapter(
	ctx context.Context,
	adapter adapterapi.Adapter,
	job *core.JobRecord,
	opts startExecutionOptions,
) (string, error) {
	var (
		handle *adapterapi.RunHandle
		err    error
	)

	switch {
	case opts.Continue:
		handle, err = adapter.ContinueRun(ctx, adapterapi.ContinueRunRequest{
			CanonicalSessionID: job.SessionID,
			CWD:                job.CWD,
			Prompt:             opts.Prompt,
			Model:              opts.Model,
			Profile:            opts.Profile,
			NativeSessionID:    opts.NativeSessionID,
			NativeSessionMeta:  opts.NativeSessionMeta,
		})
	default:
		handle, err = adapter.StartRun(ctx, adapterapi.StartRunRequest{
			CanonicalSessionID: job.SessionID,
			CWD:                job.CWD,
			Prompt:             opts.Prompt,
			Model:              opts.Model,
			Profile:            opts.Profile,
		})
	}
	if err != nil {
		return err.Error(), err
	}
	defer func() {
		if handle.Cleanup != nil {
			_ = handle.Cleanup()
		}
	}()

	if err := s.upsertJobRuntime(ctx, job.JobID, func(rec *core.JobRuntimeRecord) {
		rec.Detached = true
		rec.SupervisorPID = os.Getpid()
		rec.VendorPID = handle.Cmd.Process.Pid
	}); err != nil {
		return "", err
	}

	if _, err := s.emitEvent(ctx, *job, "process.spawned", "execution", map[string]any{
		"argv": handle.Cmd.Args,
		"pid":  handle.Cmd.Process.Pid,
	}, "", nil); err != nil {
		return "", err
	}
	if _, err := s.emitEvent(ctx, *job, "job.started", "lifecycle", map[string]any{
		"message": "job entered running state",
	}, "", nil); err != nil {
		return "", err
	}
	if err := s.transitionJob(ctx, job, core.JobStateRunning, map[string]any{"message": "job running"}); err != nil {
		return "", err
	}

	lineCh := make(chan lineItem, 64)
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go s.scanStream(handle.Stdout, "stdout", lineCh, errCh, &wg)
	go s.scanStream(handle.Stderr, "stderr", lineCh, errCh, &wg)
	go func() {
		wg.Wait()
		close(lineCh)
		close(errCh)
	}()

	var lastAssistant string
	for item := range lineCh {
		if _, err := s.emitEvent(ctx, *job, "process."+item.stream, "execution", map[string]any{
			"line": item.line,
		}, item.stream, []byte(item.line+"\n")); err != nil {
			return lastAssistant, err
		}

		hints := events.TranslateLine(job.Adapter, item.stream, item.line)
		for _, hint := range hints {
			emitHint := true
			if hint.NativeSessionID != "" {
				if job.NativeSessionID == "" {
					job.NativeSessionID = hint.NativeSessionID
					if err := s.store.UpdateJob(ctx, *job); err != nil {
						return lastAssistant, err
					}
					if err := s.store.UpsertNativeSession(ctx, core.NativeSessionRecord{
						SessionID:       job.SessionID,
						Adapter:         job.Adapter,
						NativeSessionID: hint.NativeSessionID,
						Resumable:       adapter.Capabilities().NativeResume,
						Metadata:        cloneMap(handle.NativeSessionMeta),
					}); err != nil {
						return lastAssistant, err
					}
				} else if hint.Kind == "session.discovered" && job.NativeSessionID == hint.NativeSessionID {
					emitHint = false
				}
			}
			if text, ok := hint.Payload["text"].(string); ok && text != "" && hint.Kind == "assistant.message" {
				if text == lastAssistant {
					emitHint = false
				}
				lastAssistant = text
			}
			if emitHint {
				event, err := s.emitEvent(ctx, *job, hint.Kind, hint.Phase, hint.Payload, "", nil)
				if err != nil {
					return lastAssistant, err
				}
				if hint.NativeSessionID != "" {
					event.NativeSessionID = hint.NativeSessionID
				}
			}
			if hint.Kind == "usage.reported" {
				if err := s.applyUsageHint(ctx, job, hint.Payload); err != nil {
					return lastAssistant, err
				}
			}
		}
	}

	for scanErr := range errCh {
		if scanErr != nil {
			return lastAssistant, scanErr
		}
	}

	waitErr := handle.Cmd.Wait()
	if lastMessage, err := s.readLastMessage(handle.LastMessagePath); err == nil && lastMessage != "" && lastMessage != lastAssistant {
		if _, emitErr := s.emitEvent(ctx, *job, "assistant.message", "translation", map[string]any{
			"text":   lastMessage,
			"source": "last_message_file",
		}, "", nil); emitErr != nil {
			return lastAssistant, emitErr
		}
		lastAssistant = lastMessage
	}

	if waitErr != nil {
		if _, err := s.emitEvent(ctx, *job, "diagnostic", "execution", map[string]any{
			"message": waitErr.Error(),
		}, "", nil); err != nil {
			return lastAssistant, err
		}
		return lastAssistant, fmt.Errorf("%w: %v", ErrVendorProcess, waitErr)
	}

	if lastAssistant == "" {
		lastAssistant = "adapter completed without a translated assistant message"
	}

	return lastAssistant, nil
}

func (s *Service) ExecuteDetachedJob(ctx context.Context, jobID, turnID string) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return normalizeStoreError("job", jobID, err)
	}
	turn, err := s.store.GetTurn(ctx, turnID)
	if err != nil {
		return normalizeStoreError("turn", turnID, err)
	}
	defer s.releaseContinuationLock(context.Background(), job)

	adapter, descriptor, err := s.resolveAdapter(ctx, job.Adapter)
	if err != nil {
		return err
	}

	if err := s.upsertJobRuntime(ctx, job.JobID, func(rec *core.JobRuntimeRecord) {
		rec.Detached = true
		rec.SupervisorPID = os.Getpid()
	}); err != nil {
		return err
	}

	opts, err := s.executionOptionsForJob(ctx, job, turn)
	if err != nil {
		return err
	}

	_, runErr := s.startPreparedJobLifecycle(ctx, adapter, descriptor, &job, &turn, opts)
	return runErr
}

func (s *Service) launchDetachedWorker(jobID, turnID string) (int, error) {
	exePath, err := detachedExecutablePath()
	if err != nil {
		return 0, err
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	args := []string{
		"--config", s.ConfigPath,
		"__run-job",
		"--job", jobID,
		"--turn", turnID,
	}
	cmd := exec.Command(exePath, args...)
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.Stdin = devNull
	cmd.Env = os.Environ()
	adapterapi.PrepareCommand(cmd)

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start detached worker: %w", err)
	}

	return cmd.Process.Pid, nil
}

func (s *Service) releaseContinuationLock(ctx context.Context, job core.JobRecord) {
	continued, _ := job.Summary["continued"].(bool)
	if !continued || job.NativeSessionID == "" {
		return
	}
	_ = s.store.ReleaseLock(ctx, lockKey(job.Adapter, job.NativeSessionID), job.JobID)
}

func (s *Service) queueContinuation(
	ctx context.Context,
	session core.SessionRecord,
	target core.NativeSessionRecord,
	req continuationRequest,
) (*RunResult, error) {
	now := time.Now().UTC()
	job := core.JobRecord{
		JobID:           core.GenerateID("job"),
		SessionID:       session.SessionID,
		Adapter:         target.Adapter,
		State:           core.JobStateCreated,
		CWD:             session.CWD,
		CreatedAt:       now,
		UpdatedAt:       now,
		NativeSessionID: target.NativeSessionID,
		Summary:         cloneMap(req.Summary),
	}
	if job.Summary == nil {
		job.Summary = map[string]any{}
	}
	if req.PromptSource != "" {
		job.Summary["prompt_source"] = req.PromptSource
	}
	if req.Model != "" {
		job.Summary["model"] = req.Model
	}
	if req.Profile != "" {
		job.Summary["profile"] = req.Profile
	}
	if debriefRequested, _ := job.Summary["debrief"].(bool); debriefRequested {
		path, err := s.resolveDebriefOutputPath(summaryString(job.Summary, "debrief_path"), session.SessionID, job.JobID)
		if err != nil {
			return nil, err
		}
		job.Summary["debrief_path"] = path
	}
	if err := s.store.CreateJobAndUpdateSession(ctx, session.SessionID, now, job); err != nil {
		return nil, err
	}
	session.LatestJobID = job.JobID
	session.UpdatedAt = now

	lock := core.LockRecord{
		LockKey:         lockKey(target.Adapter, target.NativeSessionID),
		Adapter:         target.Adapter,
		NativeSessionID: target.NativeSessionID,
		JobID:           job.JobID,
		AcquiredAt:      now,
	}
	if err := s.store.AcquireLock(ctx, lock); err != nil {
		message := fmt.Sprintf("native session %s is already in use", target.NativeSessionID)
		job.Summary["message"] = message
		_ = s.finishJob(ctx, &job, core.JobStateBlocked)
		return &RunResult{
			Job:     job,
			Session: session,
			Message: message,
		}, fmt.Errorf("%w: %s", ErrSessionLocked, message)
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_ = s.store.ReleaseLock(context.Background(), lock.LockKey, lock.JobID)
		}
	}()

	turn := core.TurnRecord{
		TurnID:          core.GenerateID("turn"),
		SessionID:       session.SessionID,
		JobID:           job.JobID,
		Adapter:         job.Adapter,
		StartedAt:       now,
		InputText:       req.Prompt,
		InputSource:     req.PromptSource,
		Status:          string(core.JobStateCreated),
		NativeSessionID: target.NativeSessionID,
		Stats:           map[string]any{},
	}

	if err := s.prepareJobLifecycle(ctx, &job, &turn, startExecutionOptions{
		Prompt:            req.Prompt,
		PromptSource:      req.PromptSource,
		Model:             req.Model,
		Profile:           req.Profile,
		Continue:          true,
		NativeSessionID:   target.NativeSessionID,
		NativeSessionMeta: target.Metadata,
	}); err != nil {
		return nil, err
	}
	message, runErr := s.queuePreparedJob(ctx, &job, &turn)
	if runErr == nil {
		lockHeld = false
	}

	return &RunResult{
		Job:     job,
		Session: session,
		Message: message,
	}, runErr
}

func (s *Service) executionOptionsForJob(ctx context.Context, job core.JobRecord, turn core.TurnRecord) (startExecutionOptions, error) {
	opts := startExecutionOptions{
		Prompt:       turn.InputText,
		PromptSource: turn.InputSource,
		Model:        summaryString(job.Summary, "model"),
		Profile:      summaryString(job.Summary, "profile"),
	}

	continued, _ := job.Summary["continued"].(bool)
	if !continued {
		return opts, nil
	}

	opts.Continue = true
	opts.NativeSessionID = job.NativeSessionID
	if job.NativeSessionID == "" {
		return opts, nil
	}

	metadata, err := s.nativeSessionMetadata(ctx, job.SessionID, job.Adapter, job.NativeSessionID)
	if err != nil {
		return opts, err
	}
	opts.NativeSessionMeta = metadata
	return opts, nil
}

func (s *Service) nativeSessionMetadata(ctx context.Context, sessionID, adapter, nativeSessionID string) (map[string]any, error) {
	if sessionID == "" || adapter == "" || nativeSessionID == "" {
		return nil, nil
	}

	nativeSessions, err := s.store.ListNativeSessions(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for _, native := range nativeSessions {
		if native.Adapter == adapter && native.NativeSessionID == nativeSessionID {
			return cloneMap(native.Metadata), nil
		}
	}
	return nil, nil
}

func (s *Service) resolveDebriefOutputPath(outputPath, sessionID, jobID string) (string, error) {
	path := strings.TrimSpace(outputPath)
	if path == "" {
		name := "latest.md"
		if jobID != "" {
			name = jobID + ".md"
		}
		path = filepath.Join(s.Paths.DebriefsDir, sessionID, name)
	} else {
		expanded, err := core.ExpandPath(path)
		if err != nil {
			return "", fmt.Errorf("%w: expand debrief output path: %v", ErrInvalidInput, err)
		}
		path = expanded
	}

	if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("%w: resolve debrief output path: %v", ErrInvalidInput, err)
		}
		path = absolute
	}

	return path, nil
}

func (s *Service) persistDebrief(ctx context.Context, job *core.JobRecord, message string) error {
	requested, _ := job.Summary["debrief"].(bool)
	if !requested {
		return nil
	}

	path, err := s.resolveDebriefOutputPath(summaryString(job.Summary, "debrief_path"), job.SessionID, job.JobID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create debrief directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(message)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write debrief: %w", err)
	}

	artifact := core.ArtifactRecord{
		ArtifactID: core.GenerateID("art"),
		JobID:      job.JobID,
		SessionID:  job.SessionID,
		Kind:       "debrief",
		Path:       path,
		CreatedAt:  time.Now().UTC(),
		Metadata: map[string]any{
			"adapter": job.Adapter,
			"format":  "markdown",
			"reason":  summaryString(job.Summary, "debrief_reason"),
		},
	}
	if err := s.store.InsertArtifact(ctx, artifact); err != nil {
		return err
	}

	job.Summary["debrief_path"] = path
	job.Summary["debrief_format"] = "markdown"
	if err := s.store.UpdateJob(ctx, *job); err != nil {
		return err
	}
	if _, err := s.emitEvent(ctx, *job, "debrief.exported", "debrief", map[string]any{
		"path":   path,
		"format": "markdown",
		"reason": summaryString(job.Summary, "debrief_reason"),
	}, "", nil); err != nil {
		return err
	}
	return nil
}

func detachedExecutablePath() (string, error) {
	if explicit := os.Getenv("CAGENT_EXECUTABLE"); explicit != "" {
		return explicit, nil
	}
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve cagent executable: %w", err)
	}
	return path, nil
}

func (s *Service) resolveAdapter(ctx context.Context, name string) (adapterapi.Adapter, adapters.Diagnosis, error) {
	adapter, descriptor, ok := adapters.Resolve(ctx, s.Config, name)
	if !ok {
		for _, entry := range adapters.CatalogFromConfig(s.Config) {
			if entry.Adapter == name {
				if !entry.Enabled {
					return nil, entry, fmt.Errorf("%w: adapter %q is disabled in config", ErrUnsupported, name)
				}
				return nil, entry, nil
			}
		}
		return nil, adapters.Diagnosis{}, fmt.Errorf("%w: unknown adapter %q", ErrInvalidInput, name)
	}
	if !descriptor.Enabled {
		return nil, descriptor, fmt.Errorf("%w: adapter %q is disabled in config", ErrUnsupported, name)
	}
	return adapter, descriptor, nil
}

func (s *Service) resolveContinuationTarget(ctx context.Context, session core.SessionRecord, adapterName string) (core.NativeSessionRecord, error) {
	nativeSessions, err := s.store.ListNativeSessions(ctx, session.SessionID)
	if err != nil {
		return core.NativeSessionRecord{}, err
	}

	var candidates []core.NativeSessionRecord
	for _, native := range nativeSessions {
		if !native.Resumable {
			continue
		}
		if adapterName != "" && native.Adapter != adapterName {
			continue
		}
		candidates = append(candidates, native)
	}
	if len(candidates) == 0 {
		if adapterName != "" {
			return core.NativeSessionRecord{}, fmt.Errorf("%w: no resumable native session linked for adapter %q", ErrUnsupported, adapterName)
		}
		return core.NativeSessionRecord{}, fmt.Errorf("%w: session %s has no resumable native sessions", ErrUnsupported, session.SessionID)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}

	if session.LatestJobID != "" {
		job, err := s.store.GetJob(ctx, session.LatestJobID)
		if err == nil {
			for _, candidate := range candidates {
				if candidate.Adapter == job.Adapter && candidate.NativeSessionID == job.NativeSessionID {
					return candidate, nil
				}
			}
		}
	}

	return core.NativeSessionRecord{}, fmt.Errorf("%w: session %s has multiple resumable native sessions; specify --adapter", ErrInvalidInput, session.SessionID)
}

func lockKey(adapter, nativeSessionID string) string {
	return "native:" + adapter + ":" + nativeSessionID
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}

	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func summaryString(summary map[string]any, key string) string {
	if summary == nil {
		return ""
	}
	value, _ := summary[key].(string)
	return value
}

func (s *Service) applyUsageHint(ctx context.Context, job *core.JobRecord, payload map[string]any) error {
	if job.Summary == nil {
		job.Summary = map[string]any{}
	}

	usage := usageFromPayload(payload)
	if usage == nil {
		return nil
	}
	if usage.Model != "" && summaryString(job.Summary, "model") == "" {
		job.Summary["model"] = usage.Model
	}
	if usage.Provider != "" && summaryString(job.Summary, "provider") == "" {
		job.Summary["provider"] = usage.Provider
	}

	merged := mergeUsageReports(usageFromSummary(job.Summary), *usage)
	if merged != nil {
		job.Summary["usage"] = map[string]any{
			"provider":                    merged.Provider,
			"model":                       merged.Model,
			"input_tokens":                merged.InputTokens,
			"output_tokens":               merged.OutputTokens,
			"total_tokens":                merged.TotalTokens,
			"cached_input_tokens":         merged.CachedInputTokens,
			"cache_read_input_tokens":     merged.CacheReadInputTokens,
			"cache_creation_input_tokens": merged.CacheCreationInputTokens,
			"source":                      merged.Source,
		}
	}

	if cost := costFromPayload(payload); cost != nil {
		job.Summary["cost"] = costMap(*cost)
	} else if estimated := s.costFromSummary(*job); estimated != nil {
		job.Summary["cost"] = costMap(*estimated)
	}

	return s.store.UpdateJob(ctx, *job)
}

func usageFromPayload(payload map[string]any) *core.UsageReport {
	usage := &core.UsageReport{
		Provider:                 summaryString(payload, "provider"),
		Model:                    summaryString(payload, "model"),
		InputTokens:              summaryInt64(payload, "input_tokens"),
		OutputTokens:             summaryInt64(payload, "output_tokens"),
		TotalTokens:              summaryInt64(payload, "total_tokens"),
		CachedInputTokens:        summaryInt64(payload, "cached_input_tokens"),
		CacheReadInputTokens:     summaryInt64(payload, "cache_read_input_tokens"),
		CacheCreationInputTokens: summaryInt64(payload, "cache_creation_input_tokens"),
		Source:                   "vendor_report",
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 && usage.CachedInputTokens == 0 && usage.CacheReadInputTokens == 0 && usage.CacheCreationInputTokens == 0 {
		return nil
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.CachedInputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	}
	return usage
}

func usageFromSummary(summary map[string]any) *core.UsageReport {
	if summary == nil {
		return nil
	}
	raw, ok := summary["usage"].(map[string]any)
	if !ok {
		return nil
	}
	usage := &core.UsageReport{
		Provider:                 summaryString(raw, "provider"),
		Model:                    summaryString(raw, "model"),
		InputTokens:              summaryInt64(raw, "input_tokens"),
		OutputTokens:             summaryInt64(raw, "output_tokens"),
		TotalTokens:              summaryInt64(raw, "total_tokens"),
		CachedInputTokens:        summaryInt64(raw, "cached_input_tokens"),
		CacheReadInputTokens:     summaryInt64(raw, "cache_read_input_tokens"),
		CacheCreationInputTokens: summaryInt64(raw, "cache_creation_input_tokens"),
		Source:                   summaryString(raw, "source"),
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 && usage.CachedInputTokens == 0 && usage.CacheReadInputTokens == 0 && usage.CacheCreationInputTokens == 0 {
		return nil
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.CachedInputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	}
	return usage
}

func mergeUsageReports(existing *core.UsageReport, incoming core.UsageReport) *core.UsageReport {
	if existing == nil {
		copy := incoming
		return &copy
	}
	merged := *existing
	merged.InputTokens = max64(merged.InputTokens, incoming.InputTokens)
	merged.OutputTokens = max64(merged.OutputTokens, incoming.OutputTokens)
	merged.TotalTokens = max64(merged.TotalTokens, incoming.TotalTokens)
	merged.CachedInputTokens = max64(merged.CachedInputTokens, incoming.CachedInputTokens)
	merged.CacheReadInputTokens = max64(merged.CacheReadInputTokens, incoming.CacheReadInputTokens)
	merged.CacheCreationInputTokens = max64(merged.CacheCreationInputTokens, incoming.CacheCreationInputTokens)
	if merged.Model == "" {
		merged.Model = incoming.Model
	}
	if merged.Provider == "" {
		merged.Provider = incoming.Provider
	}
	if incoming.Source != "" {
		merged.Source = incoming.Source
	}
	return &merged
}

func costFromPayload(payload map[string]any) *core.CostEstimate {
	total := summaryFloat64(payload, "cost_usd")
	if total == 0 {
		return nil
	}
	return &core.CostEstimate{
		Currency:     "USD",
		TotalCostUSD: total,
		Estimated:    false,
		Source:       "vendor_report",
	}
}

func (s *Service) costFromSummary(job core.JobRecord) *core.CostEstimate {
	if job.Summary != nil {
		if raw, ok := job.Summary["cost"].(map[string]any); ok {
			cost := &core.CostEstimate{
				Currency:             summaryString(raw, "currency"),
				InputCostUSD:         summaryFloat64(raw, "input_cost_usd"),
				OutputCostUSD:        summaryFloat64(raw, "output_cost_usd"),
				CachedInputCostUSD:   summaryFloat64(raw, "cached_input_cost_usd"),
				CacheReadCostUSD:     summaryFloat64(raw, "cache_read_cost_usd"),
				CacheCreationCostUSD: summaryFloat64(raw, "cache_creation_cost_usd"),
				TotalCostUSD:         summaryFloat64(raw, "total_cost_usd"),
				Estimated:            summaryBool(raw, "estimated"),
				Source:               summaryString(raw, "source"),
				SourceURL:            summaryString(raw, "source_url"),
			}
			if cost.Currency == "" {
				cost.Currency = "USD"
			}
			if cost.TotalCostUSD > 0 {
				return cost
			}
		}
	}

	usage := usageFromSummary(job.Summary)
	if usage == nil {
		return nil
	}
	provider, model := pricingLookupContext(job, usage)
	if provider == "" || model == "" {
		return nil
	}
	usage.Provider = provider
	usage.Model = model
	return pricing.Estimate(*usage, pricing.Resolve(s.Config, provider, model))
}

func pricingLookupContext(job core.JobRecord, usage *core.UsageReport) (string, string) {
	provider := ""
	model := ""
	if usage != nil {
		provider = usage.Provider
		model = usage.Model
	}
	if provider == "" {
		provider = summaryString(job.Summary, "provider")
	}
	if model == "" {
		model = summaryString(job.Summary, "model")
	}
	if strings.Contains(model, "/") {
		parts := strings.SplitN(model, "/", 2)
		if provider == "" {
			provider = parts[0]
		}
		model = parts[1]
	}
	if provider == "" {
		switch job.Adapter {
		case "codex":
			provider = "openai"
		case "claude":
			provider = "anthropic"
		case "gemini":
			provider = "google"
		}
	}
	return strings.ToLower(provider), strings.ToLower(model)
}

func costMap(cost core.CostEstimate) map[string]any {
	return map[string]any{
		"currency":                cost.Currency,
		"input_cost_usd":          cost.InputCostUSD,
		"output_cost_usd":         cost.OutputCostUSD,
		"cached_input_cost_usd":   cost.CachedInputCostUSD,
		"cache_read_cost_usd":     cost.CacheReadCostUSD,
		"cache_creation_cost_usd": cost.CacheCreationCostUSD,
		"total_cost_usd":          cost.TotalCostUSD,
		"estimated":               cost.Estimated,
		"source":                  cost.Source,
		"source_url":              cost.SourceURL,
	}
}

func summaryInt64(summary map[string]any, key string) int64 {
	if summary == nil {
		return 0
	}
	value := summary[key]
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func summaryFloat64(summary map[string]any, key string) float64 {
	if summary == nil {
		return 0
	}
	value := summary[key]
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

func summaryBool(summary map[string]any, key string) bool {
	if summary == nil {
		return false
	}
	value, _ := summary[key].(bool)
	return value
}

func max64(left, right int64) int64 {
	if right > left {
		return right
	}
	return left
}

func (s *Service) upsertJobRuntime(ctx context.Context, jobID string, mutate func(*core.JobRuntimeRecord)) error {
	now := time.Now().UTC()
	rec, err := s.store.GetJobRuntime(ctx, jobID)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		rec = core.JobRuntimeRecord{
			JobID:     jobID,
			StartedAt: now,
		}
	default:
		return err
	}

	mutate(&rec)
	if rec.StartedAt.IsZero() {
		rec.StartedAt = now
	}
	rec.UpdatedAt = now
	return s.store.UpsertJobRuntime(ctx, rec)
}

func (s *Service) isCancelRequested(ctx context.Context, jobID string) bool {
	rec, err := s.store.GetJobRuntime(ctx, jobID)
	if err != nil {
		return false
	}
	return rec.CancelRequestedAt != nil
}

func signalProcessGroup(pid int, sig syscall.Signal) error {
	if pid == 0 {
		return nil
	}
	if err := syscall.Kill(-pid, sig); err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err := syscall.Kill(pid, sig); err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("signal pid %d with %s", pid, sig)
}

func (s *Service) transitionJob(ctx context.Context, job *core.JobRecord, state core.JobState, payload map[string]any) error {
	job.State = state
	job.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateJob(ctx, *job); err != nil {
		return err
	}
	_, err := s.emitEvent(ctx, *job, "job.state_changed", "lifecycle", map[string]any{
		"state":   state,
		"message": payload["message"],
	}, "", nil)
	return err
}

func (s *Service) finishJob(ctx context.Context, job *core.JobRecord, state core.JobState) error {
	now := time.Now().UTC()
	job.State = state
	job.UpdatedAt = now
	job.FinishedAt = &now
	return s.store.UpdateJob(ctx, *job)
}

func (s *Service) emitEvent(
	ctx context.Context,
	job core.JobRecord,
	kind string,
	phase string,
	payload any,
	rawStream string,
	rawData []byte,
) (*core.EventRecord, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal event payload: %w", err)
	}

	event := &core.EventRecord{
		EventID:         core.GenerateID("evt"),
		TS:              time.Now().UTC(),
		JobID:           job.JobID,
		SessionID:       job.SessionID,
		Adapter:         job.Adapter,
		Kind:            kind,
		Phase:           phase,
		NativeSessionID: job.NativeSessionID,
		Payload:         encoded,
	}

	if err := s.store.AppendEvent(ctx, event); err != nil {
		return nil, err
	}

	if len(rawData) > 0 && rawStream != "" {
		rawRef, err := s.writeRawArtifact(job, rawStream, event.Seq, rawData)
		if err != nil {
			return nil, err
		}

		artifact := core.ArtifactRecord{
			ArtifactID: core.GenerateID("art"),
			JobID:      job.JobID,
			SessionID:  job.SessionID,
			Kind:       rawStream,
			Path:       rawRef,
			CreatedAt:  time.Now().UTC(),
			Metadata: map[string]any{
				"seq": event.Seq,
			},
		}

		if err := s.store.AttachArtifactToEvent(ctx, event.EventID, job.JobID, rawRef, artifact); err != nil {
			_ = os.Remove(filepath.Join(s.Paths.StateDir, rawRef))
			return nil, err
		}

		event.RawRef = rawRef
	}

	return event, nil
}

func (s *Service) scanStream(
	reader io.Reader,
	stream string,
	lineCh chan<- lineItem,
	errCh chan<- error,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		lineCh <- lineItem{stream: stream, line: scanner.Text()}
	}
	errCh <- scanner.Err()
}

func (s *Service) writeRawArtifact(job core.JobRecord, stream string, seq int64, data []byte) (string, error) {
	dir := filepath.Join(s.Paths.RawDir, stream, job.JobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create raw artifact dir: %w", err)
	}

	name := fmt.Sprintf("%05d.jsonl", seq)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write raw artifact: %w", err)
	}

	return filepath.ToSlash(filepath.Join("raw", stream, job.JobID, name)), nil
}

func (s *Service) readLastMessage(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

func (s *Service) buildTransferPacket(
	job core.JobRecord,
	session core.SessionRecord,
	turns []core.TurnRecord,
	events []core.EventRecord,
	artifacts []core.ArtifactRecord,
	reason string,
	mode string,
) core.TransferPacket {
	mode = normalizeTransferMode(mode)
	packet := core.TransferPacket{
		TransferID: core.GenerateID("xfer"),
		ExportedAt: time.Now().UTC(),
		Mode:       mode,
		Reason:     strings.TrimSpace(reason),
		Disclaimer: "This is a context transfer, not native session continuation.",
		Source: core.TransferSource{
			Adapter:         job.Adapter,
			Model:           summaryString(job.Summary, "model"),
			JobID:           job.JobID,
			SessionID:       session.SessionID,
			NativeSessionID: job.NativeSessionID,
			CWD:             session.CWD,
		},
		Objective:            latestObjective(turns),
		Summary:              summarizeTurns(turns),
		Unresolved:           collectUnresolved(job, events),
		ImportantFiles:       collectImportantFiles(session.CWD, events, artifacts),
		RecentTurnsInline:    condenseTurns(turns, 3),
		RecentEventsInline:   condenseEvents(events, 6),
		EvidenceRefs:         []core.TransferEvidenceRef{},
		Artifacts:            toTransferArtifacts(s.Paths.StateDir, artifacts),
		Constraints:          []string{"Keep CLI flags and JSON output backward compatible.", fmt.Sprintf("Work within %s.", session.CWD)},
		RecommendedNextSteps: recommendNextSteps(job, turns),
	}
	if packet.Objective == "" {
		packet.Objective = "Continue the latest session objective."
	}
	if packet.Summary == "" {
		packet.Summary = "No prior turn summary was captured."
	}
	if packet.Reason == "" {
		packet.Reason = defaultTransferReason(job)
	}
	if len(packet.Unresolved) == 0 && job.State != core.JobStateCompleted {
		packet.Unresolved = []string{fmt.Sprintf("Latest job ended in state %s.", job.State)}
	}
	if packet.Unresolved == nil {
		packet.Unresolved = []string{}
	}
	if packet.ImportantFiles == nil {
		packet.ImportantFiles = []string{}
	}
	if packet.RecentTurnsInline == nil {
		packet.RecentTurnsInline = []core.TurnRecord{}
	}
	if packet.RecentEventsInline == nil {
		packet.RecentEventsInline = []core.EventRecord{}
	}
	if packet.Artifacts == nil {
		packet.Artifacts = []core.TransferArtifact{}
	}
	if packet.EvidenceRefs == nil {
		packet.EvidenceRefs = []core.TransferEvidenceRef{}
	}
	return packet
}

func (s *Service) writeTransferBundle(packet core.TransferPacket, outputPath string, turns []core.TurnRecord, events []core.EventRecord) (core.TransferPacket, string, error) {
	path := outputPath
	if path == "" {
		path = filepath.Join(s.Paths.TransfersDir, packet.TransferID, "transfer.json")
	} else {
		expanded, err := core.ExpandPath(outputPath)
		if err != nil {
			return packet, "", fmt.Errorf("%w: expand transfer output path: %v", ErrInvalidInput, err)
		}
		path = expanded
	}
	if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return packet, "", fmt.Errorf("%w: resolve transfer output path: %v", ErrInvalidInput, err)
		}
		path = absolute
	}

	bundleDir := filepath.Dir(path)
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return packet, "", fmt.Errorf("create transfer directory: %w", err)
	}

	turnsPath := filepath.Join(bundleDir, "recent_turns.json")
	eventsPath := filepath.Join(bundleDir, "recent_events.jsonl")
	if err := writeIndentedJSON(turnsPath, turns); err != nil {
		return packet, "", err
	}
	if err := writeJSONL(eventsPath, condenseEvents(events, 20)); err != nil {
		return packet, "", err
	}
	packet.EvidenceRefs = append(packet.EvidenceRefs,
		core.TransferEvidenceRef{Kind: "recent_turns_json", Path: turnsPath},
		core.TransferEvidenceRef{Kind: "recent_events_jsonl", Path: eventsPath},
	)

	encoded, err := json.MarshalIndent(packet, "", "  ")
	if err != nil {
		return packet, "", fmt.Errorf("marshal transfer packet: %w", err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		return packet, "", fmt.Errorf("write transfer packet: %w", err)
	}

	return packet, path, nil
}

func (s *Service) loadTransfer(ctx context.Context, ref string) (core.TransferRecord, error) {
	if stat, err := os.Stat(ref); err == nil && !stat.IsDir() {
		data, err := os.ReadFile(ref)
		if err != nil {
			return core.TransferRecord{}, fmt.Errorf("read transfer file: %w", err)
		}
		var packet core.TransferPacket
		if err := json.Unmarshal(data, &packet); err != nil {
			return core.TransferRecord{}, fmt.Errorf("%w: decode transfer file: %v", ErrInvalidInput, err)
		}
		return core.TransferRecord{
			TransferID: packet.TransferID,
			JobID:      packet.Source.JobID,
			SessionID:  packet.Source.SessionID,
			CreatedAt:  packet.ExportedAt,
			Packet:     packet,
		}, nil
	}

	record, err := s.store.GetTransfer(ctx, ref)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return core.TransferRecord{}, fmt.Errorf("%w: transfer %s", ErrNotFound, ref)
		}
		return core.TransferRecord{}, err
	}
	return record, nil
}

func defaultTransferReason(job core.JobRecord) string {
	if job.State != core.JobStateCompleted {
		return fmt.Sprintf("source job ended in state %s", job.State)
	}
	return "operator-requested context transfer"
}

func normalizeDebriefReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "operator-requested debrief"
	}
	return reason
}

func normalizeTransferMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "", "manual":
		return "manual"
	case "recovery", "operator_override", "cost", "capability":
		return mode
	default:
		return "manual"
	}
}

func latestObjective(turns []core.TurnRecord) string {
	for _, turn := range turns {
		if strings.TrimSpace(turn.InputText) != "" {
			return strings.TrimSpace(turn.InputText)
		}
	}
	return ""
}

func summarizeTurns(turns []core.TurnRecord) string {
	var parts []string
	for _, turn := range turns {
		if text := strings.TrimSpace(turn.ResultSummary); text != "" {
			parts = append(parts, text)
		}
		if len(parts) == 3 {
			break
		}
	}
	return strings.Join(parts, "\n")
}

func condenseTurns(turns []core.TurnRecord, limit int) []core.TurnRecord {
	if limit > 0 && len(turns) > limit {
		turns = turns[:limit]
	}
	condensed := make([]core.TurnRecord, 0, len(turns))
	for _, turn := range turns {
		turn.InputText = truncateString(turn.InputText, 800)
		turn.ResultSummary = truncateString(turn.ResultSummary, 400)
		condensed = append(condensed, turn)
	}
	return condensed
}

func collectUnresolved(job core.JobRecord, events []core.EventRecord) []string {
	var unresolved []string
	if job.State != core.JobStateCompleted {
		unresolved = append(unresolved, fmt.Sprintf("Latest job state is %s.", job.State))
	}
	for _, event := range events {
		if event.Kind != "diagnostic" && event.Kind != "job.failed" && event.Kind != "job.cancelled" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		if message, ok := payload["message"].(string); ok && strings.TrimSpace(message) != "" {
			unresolved = append(unresolved, strings.TrimSpace(message))
		}
	}
	return dedupeStrings(unresolved, 6)
}

func collectImportantFiles(cwd string, events []core.EventRecord, artifacts []core.ArtifactRecord) []string {
	var files []string
	seen := map[string]struct{}{}
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || !filepath.IsAbs(path) {
			return
		}
		stat, err := os.Stat(path)
		if err != nil || stat.IsDir() {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		files = append(files, path)
	}

	for _, artifact := range artifacts {
		add(artifact.Path)
	}
	for _, event := range events {
		var decoded any
		if err := json.Unmarshal(event.Payload, &decoded); err != nil {
			continue
		}
		walkStrings(decoded, func(value string) {
			add(value)
			if cwd != "" && !filepath.IsAbs(value) {
				add(filepath.Join(cwd, value))
			}
		})
	}

	if len(files) > 12 {
		files = files[:12]
	}
	return files
}

func condenseEvents(events []core.EventRecord, limit int) []core.EventRecord {
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}

	condensed := make([]core.EventRecord, 0, len(events))
	for _, event := range events {
		var payload any
		if err := json.Unmarshal(event.Payload, &payload); err == nil {
			payload = truncateNestedStrings(payload, 400)
			if encoded, err := json.Marshal(payload); err == nil {
				event.Payload = encoded
			}
		}
		condensed = append(condensed, event)
	}
	return condensed
}

func truncateNestedStrings(value any, max int) any {
	switch typed := value.(type) {
	case string:
		if max > 0 && len(typed) > max {
			return typed[:max] + "...(truncated)"
		}
		return typed
	case []any:
		for i := range typed {
			typed[i] = truncateNestedStrings(typed[i], max)
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = truncateNestedStrings(item, max)
		}
		return typed
	default:
		return value
	}
}

func toTransferArtifacts(stateDir string, artifacts []core.ArtifactRecord) []core.TransferArtifact {
	result := make([]core.TransferArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		path := artifact.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(stateDir, path)
		}
		result = append(result, core.TransferArtifact{
			Kind:     artifact.Kind,
			Path:     path,
			Metadata: artifact.Metadata,
		})
	}
	return result
}

func writeIndentedJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeJSONL(path string, values []core.EventRecord) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	encoder := json.NewEncoder(file)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func truncateString(text string, max int) string {
	if max > 0 && len(text) > max {
		return text[:max] + "...(truncated)"
	}
	return text
}

func recommendNextSteps(job core.JobRecord, turns []core.TurnRecord) []string {
	steps := []string{"Review the most recent summary and unresolved items.", "Inspect the important files before making changes.", "Run verification before finishing."}
	if job.State != core.JobStateCompleted {
		steps[0] = fmt.Sprintf("Investigate why the latest job ended in state %s.", job.State)
	}
	if len(turns) > 0 && turns[0].ResultSummary != "" {
		steps = append([]string{"Use the last turn summary as the starting context."}, steps...)
	}
	return dedupeStrings(steps, 5)
}

func walkStrings(value any, visit func(string)) {
	switch typed := value.(type) {
	case string:
		visit(typed)
	case []any:
		for _, item := range typed {
			walkStrings(item, visit)
		}
	case map[string]any:
		for _, item := range typed {
			walkStrings(item, visit)
		}
	}
}

func dedupeStrings(values []string, limit int) []string {
	seen := map[string]struct{}{}
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if limit > 0 && len(result) == limit {
			break
		}
	}
	return result
}

func normalizeStoreError(kind, id string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %s %s", ErrNotFound, kind, id)
	}
	return err
}

func streamFromRawRef(rawRef string) string {
	parts := strings.Split(filepath.ToSlash(rawRef), "/")
	for _, candidate := range []string{"stdout", "stderr", "native"} {
		for _, part := range parts {
			if part == candidate {
				return candidate
			}
		}
		if filepath.ToSlash(rawRef) == candidate || filepath.Clean(rawRef) == candidate {
			return candidate
		}
	}

	return "raw"
}
