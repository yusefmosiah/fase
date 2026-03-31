package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
	"github.com/yusefmosiah/cogent/internal/adapters"
	catalogpkg "github.com/yusefmosiah/cogent/internal/catalog"
	"github.com/yusefmosiah/cogent/internal/core"
	debriefpkg "github.com/yusefmosiah/cogent/internal/debrief"
	"github.com/yusefmosiah/cogent/internal/notify"
	"github.com/yusefmosiah/cogent/internal/pricing"
	"github.com/yusefmosiah/cogent/internal/store"
	transferpkg "github.com/yusefmosiah/cogent/internal/transfer"
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

var osExecutable = os.Executable

var checkerUIEvidencePattern = regexp.MustCompile(`(?i)(\bmind-graph/|\bplaywright\.config(?:\.[[:alnum:]]+)?\b|\bindex\.html\b|\.tsx\b|\.jsx\b|\.css\b|\.html\b|\bfrontend\b|\bfront-end\b|\bweb ui\b|\buser interface\b|\bbrowser ui\b)`)

func workNeedsUIVerification(work core.WorkItemRecord) bool {
	if workHasUITag(work.Metadata) {
		return true
	}
	haystack := strings.Join([]string{work.Title, work.Objective}, "\n")
	return checkerUIEvidencePattern.MatchString(haystack)
}

func workHasUITag(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	tags := metadataStringSlice(metadata["tags"])
	for _, tag := range tags {
		switch strings.ToLower(strings.TrimSpace(tag)) {
		case "ui", "frontend", "front-end", "web ui", "web-ui", "webui", "browser", "browser ui", "browser-ui":
			return true
		}
	}
	for _, key := range []string{"ui", "frontend", "front_end", "web_ui", "browser_ui"} {
		switch v := metadata[key].(type) {
		case bool:
			if v {
				return true
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(v), "true") || strings.EqualFold(strings.TrimSpace(v), "yes") {
				return true
			}
		}
	}
	return false
}

func metadataStringSlice(value any) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	default:
		return nil
	}
}

type Service struct {
	Paths           core.Paths
	Config          core.Config
	ConfigPath      string
	ConfigPresent   bool
	store           *store.Store
	Events          EventBus
	DigestCollector *notify.DigestCollector
}

type RunRequest struct {
	Adapter         string
	CWD             string
	Prompt          string
	PromptSource    string
	Label           string
	Model           string
	Profile         string
	ArtifactDir     string
	SessionID       string
	WorkID          string
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
	WorkID       string
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
	Job              core.JobRecord             `json:"job"`
	Session          core.SessionRecord         `json:"session"`
	NativeSessions   []core.NativeSessionRecord `json:"native_sessions"`
	Events           []core.EventRecord         `json:"events"`
	Usage            *core.UsageReport          `json:"usage,omitempty"`
	UsageByModel     []core.UsageReport         `json:"usage_by_model,omitempty"`
	Cost             *core.CostEstimate         `json:"cost,omitempty"`
	VendorCost       *core.CostEstimate         `json:"vendor_cost,omitempty"`
	EstimatedCost    *core.CostEstimate         `json:"estimated_cost,omitempty"`
	UsageAttribution *core.UsageAttribution     `json:"usage_attribution,omitempty"`
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

type ProbeCatalogRequest struct {
	Adapter     string
	Provider    string
	Model       string
	CWD         string
	Prompt      string
	Timeout     time.Duration
	Concurrency int
	Limit       int
}

type RawLogEntry struct {
	Stream  string `json:"stream"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ArtifactsRequest struct {
	JobID     string
	SessionID string
	WorkID    string
	Kind      string
	Limit     int
}

type ArtifactResult struct {
	Artifact core.ArtifactRecord `json:"artifact"`
	Content  string              `json:"content,omitempty"`
}

type AttachArtifactRequest struct {
	JobID     string
	SessionID string
	WorkID    string
	Path      string
	Kind      string
	Copy      bool
	Metadata  map[string]any
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

type HistorySearchRequest struct {
	Query     string
	Adapter   string
	Model     string
	CWD       string
	SessionID string
	Kinds     []string
	Limit     int
	ScanLimit int
}

type HistorySearchResult struct {
	Matches []core.HistoryMatch `json:"matches"`
}

type WorkCreateRequest struct {
	Title                string                     `json:"title"`
	Objective            string                     `json:"objective"`
	Kind                 string                     `json:"kind"`
	ParentWorkID         string                     `json:"parent_work_id,omitempty"`
	LockState            core.WorkLockState         `json:"lock_state,omitempty"`
	Priority             int                        `json:"priority,omitempty"`
	Position             int                        `json:"position,omitempty"`
	ConfigurationClass   string                     `json:"configuration_class,omitempty"`
	BudgetClass          string                     `json:"budget_class,omitempty"`
	RequiredCapabilities []string                   `json:"required_capabilities,omitempty"`
	RequiredModelTraits  []string                   `json:"required_model_traits,omitempty"`
	PreferredAdapters    []string                   `json:"preferred_adapters,omitempty"`
	ForbiddenAdapters    []string                   `json:"forbidden_adapters,omitempty"`
	PreferredModels      []string                   `json:"preferred_models,omitempty"`
	AvoidModels          []string                   `json:"avoid_models,omitempty"`
	RequiredAttestations []core.RequiredAttestation `json:"required_attestations,omitempty"`
	RequiredDocs         []string                   `json:"required_docs,omitempty"`
	Acceptance           map[string]any             `json:"acceptance,omitempty"`
	Metadata             map[string]any             `json:"metadata,omitempty"`
	HeadCommitOID        string                     `json:"head_commit_oid,omitempty"`
	CreatedBy            string                     `json:"created_by,omitempty"`
}

type WorkListRequest struct {
	Limit           int
	Kind            string
	ExecutionState  string
	ApprovalState   string
	IncludeArchived bool
}

type WorkUpdateRequest struct {
	WorkID         string                  `json:"work_id,omitempty"`
	ExecutionState core.WorkExecutionState `json:"execution_state,omitempty"`
	ApprovalState  core.WorkApprovalState  `json:"approval_state,omitempty"`
	LockState      core.WorkLockState      `json:"lock_state,omitempty"`
	Phase          string                  `json:"phase,omitempty"`
	Message        string                  `json:"message,omitempty"`
	JobID          string                  `json:"job_id,omitempty"`
	SessionID      string                  `json:"session_id,omitempty"`
	ArtifactID     string                  `json:"artifact_id,omitempty"`
	Metadata       map[string]any          `json:"metadata,omitempty"`
	CreatedBy      string                  `json:"created_by,omitempty"`
	ForceDone      bool                    `json:"force_done,omitempty"`
}

type WorkNoteRequest struct {
	WorkID    string         `json:"work_id,omitempty"`
	NoteType  string         `json:"note_type,omitempty"`
	Body      string         `json:"body"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedBy string         `json:"created_by,omitempty"`
}

type WorkProposalCreateRequest struct {
	ProposalType string         `json:"proposal_type"`
	TargetWorkID string         `json:"target_work_id,omitempty"`
	SourceWorkID string         `json:"source_work_id,omitempty"`
	Rationale    string         `json:"rationale,omitempty"`
	Patch        map[string]any `json:"patch,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	CreatedBy    string         `json:"created_by,omitempty"`
}

type WorkProposalListRequest struct {
	Limit        int
	State        string
	TargetWorkID string
	SourceWorkID string
}

type WorkAttestRequest struct {
	WorkID                  string         `json:"work_id,omitempty"`
	Result                  string         `json:"result"`
	Summary                 string         `json:"summary,omitempty"`
	ArtifactID              string         `json:"artifact_id,omitempty"`
	JobID                   string         `json:"job_id,omitempty"`
	SessionID               string         `json:"session_id,omitempty"`
	Method                  string         `json:"method,omitempty"`
	VerifierKind            string         `json:"verifier_kind,omitempty"`
	VerifierIdentity        string         `json:"verifier_identity,omitempty"`
	Confidence              float64        `json:"confidence,omitempty"`
	Blocking                bool           `json:"blocking,omitempty"`
	SupersedesAttestationID string         `json:"supersedes_attestation_id,omitempty"`
	Metadata                map[string]any `json:"metadata,omitempty"`
	CreatedBy               string         `json:"created_by,omitempty"`
	Nonce                   string         `json:"nonce,omitempty"`
	SignerPubkey            string         `json:"signer_pubkey,omitempty"`
}

type WorkShowResult struct {
	Work         core.WorkItemRecord       `json:"work"`
	Children     []core.WorkItemRecord     `json:"children,omitempty"`
	Updates      []core.WorkUpdateRecord   `json:"updates,omitempty"`
	Notes        []core.WorkNoteRecord     `json:"notes,omitempty"`
	Jobs         []core.JobRecord          `json:"jobs,omitempty"`
	Proposals    []core.WorkProposalRecord `json:"proposals,omitempty"`
	CheckRecords []core.CheckRecord        `json:"check_records,omitempty"`
	Attestations []core.AttestationRecord  `json:"attestations,omitempty"`
	Approvals    []core.ApprovalRecord     `json:"approvals,omitempty"`
	Promotions   []core.PromotionRecord    `json:"promotions,omitempty"`
	Artifacts    []core.ArtifactRecord     `json:"artifacts,omitempty"`
	Docs         []core.DocContentRecord   `json:"docs,omitempty"`
}


// WorkResetRequest resets a work item to start a new attempt epoch.
// This clears stale state and begins a fresh attempt while preserving history.
type WorkResetRequest struct {
	WorkID      string `json:"work_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	ClearClaims bool   `json:"clear_claims,omitempty"`
}

type WorkHydrateRequest struct {
	WorkID   string
	Mode     string
	Debrief  bool
	Claimant string
}

type WorkHydrateResult map[string]any

type ProjectHydrateRequest struct {
	Mode   string // thin, standard, deep
	Format string // json, markdown (default: markdown)
}

type ProjectHydrateResult map[string]any

type WorkPromoteRequest struct {
	WorkID      string `json:"work_id,omitempty"`
	Environment string `json:"environment,omitempty"`
	TargetRef   string `json:"target_ref,omitempty"`
	Message     string `json:"message,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
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
	WorkID       string
}

func Open(ctx context.Context, configPath string) (*Service, error) {
	return openWithStateDirOverride(ctx, configPath, "")
}

// OpenWithStateDir opens the service using the given configPath for adapter/config
// settings but overrides the database stateDir. This is used by commands like
// `run --work` that need to share the database with a running `cogent serve` instance.
func OpenWithStateDir(ctx context.Context, configPath, stateDir string) (*Service, error) {
	return openWithStateDirOverride(ctx, configPath, stateDir)
}

func openWithStateDirOverride(ctx context.Context, configPath, stateDirOverride string) (*Service, error) {
	paths, err := core.ResolvePathsForRepo()
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

	if stateDirOverride != "" {
		paths = paths.WithStateDir(stateDirOverride)
	} else if cfg.Store.StateDir != "" {
		stateDir, err := core.ExpandPath(cfg.Store.StateDir)
		if err != nil {
			return nil, fmt.Errorf("expand state dir: %w", err)
		}
		paths = paths.WithStateDir(stateDir)
	}

	if err := core.EnsurePaths(paths); err != nil {
		return nil, fmt.Errorf("ensure runtime paths: %w", err)
	}

	db, err := store.OpenWithPrivate(ctx, paths.DBPath, paths.PrivateDBPath)
	if err != nil {
		return nil, err
	}

	return &Service{
		Paths:           paths,
		Config:          cfg,
		ConfigPath:      resolvedConfigPath,
		ConfigPresent:   configPresent,
		store:           db,
		DigestCollector: notify.NewDigestCollector(os.Getenv("RESEND_API_KEY"), os.Getenv("EMAIL_TO")),
	}, nil
}

// sendWorkNotification collects a "done" event into the digest.
func (s *Service) Close() error {
	return s.store.Close()
}

// CheckpointWAL forces a WAL checkpoint on the database to ensure durability.
func (s *Service) CheckpointWAL() {
	s.store.CheckpointWAL()
}

// ── Check Records ────────────────────────────────────────────────────────────

type CheckRecordCreateRequest struct {
	WorkID       string
	CheckerModel string
	WorkerModel  string
	Result       string // "pass" or "fail"
	Report       core.CheckReport
	CreatedBy    string
}

// CreateCheckRecord stores a checker's report and publishes an event to wake the supervisor.
// sendAttestationNotification collects an attestation event into the digest.

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
	if req.WorkID != "" {
		if _, err := s.store.GetWorkItem(ctx, req.WorkID); err != nil {
			return nil, normalizeStoreError("work", req.WorkID, err)
		}
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
		WorkID:    req.WorkID,
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
	if req.WorkID != "" {
		if err := s.markWorkQueued(ctx, req.WorkID, &job, session); err != nil {
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
		// For adapters that support ContinueRun without a native session
		// (e.g., native adapter starts a fresh session each continuation),
		// create a synthetic target from the session's origin adapter.
		adapterName := req.Adapter
		if adapterName == "" {
			adapterName = session.OriginAdapter
		}
		_, _, resolveErr := s.resolveAdapter(ctx, adapterName)
		if resolveErr != nil {
			return nil, err // original error
		}
		target = core.NativeSessionRecord{
			NativeSessionID: req.SessionID, // use canonical session ID as lock key
			Adapter:         adapterName,
			Resumable:       true,
		}
	}

	_, descriptor, err := s.resolveAdapter(ctx, target.Adapter)
	if err != nil {
		return nil, err
	}
	// Allow continuation for adapters with NativeResume OR adapters that
	// handle ContinueRun by starting fresh (like native adapter).
	if !descriptor.Capabilities.NativeResume && target.Adapter != "native" {
		return nil, fmt.Errorf("%w: adapter %q does not support continuation", ErrUnsupported, target.Adapter)
	}
	if req.WorkID != "" {
		if _, err := s.store.GetWorkItem(ctx, req.WorkID); err != nil {
			return nil, normalizeStoreError("work", req.WorkID, err)
		}
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
		WorkID: req.WorkID,
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
	if err := s.annotateCatalogSnapshot(ctx, &snapshot); err != nil {
		return nil, err
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
	if err := s.annotateCatalogSnapshot(ctx, &snapshot); err != nil {
		return nil, err
	}
	return &CatalogResult{Snapshot: snapshot}, nil
}

func (s *Service) ProbeCatalog(ctx context.Context, req ProbeCatalogRequest) (*CatalogResult, error) {
	snapshot, err := s.catalogSnapshotOrSync(ctx)
	if err != nil {
		return nil, err
	}

	entries := filterCatalogEntries(snapshot.Entries, req)
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: no catalog entries matched probe filters", ErrNotFound)
	}
	if req.Limit > 0 && len(entries) > req.Limit {
		entries = entries[:req.Limit]
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	concurrency := req.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}

	type result struct {
		key   string
		entry core.CatalogEntry
		issue *core.CatalogIssue
	}
	workCh := make(chan core.CatalogEntry)
	resultCh := make(chan result, len(entries))
	var wg sync.WaitGroup
	for idx := 0; idx < concurrency; idx++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for entry := range workCh {
				probed, issue := s.probeCatalogEntry(ctx, entry, req, timeout)
				resultCh <- result{key: catalogEntryKey(entry), entry: probed, issue: issue}
			}
		}()
	}
	go func() {
		for _, entry := range entries {
			workCh <- entry
		}
		close(workCh)
		wg.Wait()
		close(resultCh)
	}()

	updated := snapshot
	updated.SnapshotID = core.GenerateID("cat")
	updated.CreatedAt = time.Now().UTC()
	updated.Entries = append([]core.CatalogEntry(nil), snapshot.Entries...)
	updated.Issues = append([]core.CatalogIssue(nil), snapshot.Issues...)

	index := map[string]int{}
	for idx, entry := range updated.Entries {
		index[catalogEntryKey(entry)] = idx
	}
	for item := range resultCh {
		if idx, ok := index[item.key]; ok {
			updated.Entries[idx] = item.entry
		}
		if item.issue != nil {
			updated.Issues = append(updated.Issues, *item.issue)
		}
	}
	if err := s.annotateCatalogSnapshot(ctx, &updated); err != nil {
		return nil, err
	}
	if err := s.store.CreateCatalogSnapshot(ctx, updated); err != nil {
		return nil, err
	}
	return &CatalogResult{Snapshot: updated}, nil
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

	contract := s.canonicalJobUsage(ctx, job, map[string]core.WorkItemRecord{})
	if contract == nil {
		contract = &jobUsageContract{}
	}

	return &StatusResult{
		Job:              job,
		Session:          session,
		NativeSessions:   nativeSessions,
		Events:           events,
		Usage:            contract.usage,
		UsageByModel:     contract.usageByModel,
		Cost:             contract.cost,
		VendorCost:       contract.vendorCost,
		EstimatedCost:    contract.estimatedCost,
		UsageAttribution: contract.attribution,
	}, nil
}

func (s *Service) GetJobRuntime(ctx context.Context, jobID string) (core.JobRuntimeRecord, error) {
	return s.store.GetJobRuntime(ctx, jobID)
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

func (s *Service) SearchHistory(ctx context.Context, req HistorySearchRequest) (*HistorySearchResult, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, fmt.Errorf("%w: query must not be empty", ErrInvalidInput)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	scanLimit := req.ScanLimit
	if scanLimit <= 0 {
		scanLimit = 500
	}
	if scanLimit < limit {
		scanLimit = limit
	}

	allowedKinds := map[string]bool{}
	if len(req.Kinds) > 0 {
		for _, kind := range req.Kinds {
			trimmed := strings.ToLower(strings.TrimSpace(kind))
			if trimmed != "" {
				allowedKinds[trimmed] = true
			}
		}
	}

	jobs, err := s.store.ListJobs(ctx, scanLimit)
	if err != nil {
		return nil, err
	}

	workCache := make(map[string]core.WorkItemRecord)
	jobByID := make(map[string]core.JobRecord, len(jobs))
	jobUsageByID := make(map[string]*jobUsageContract, len(jobs))
	var filteredJobs []core.JobRecord
	for _, job := range jobs {
		contract := s.canonicalJobUsage(ctx, job, workCache)
		if !historyJobMatches(job, contract, req) {
			continue
		}
		jobByID[job.JobID] = job
		jobUsageByID[job.JobID] = contract
		filteredJobs = append(filteredJobs, job)
	}

	matches := make([]core.HistoryMatch, 0, limit*2)
	for _, job := range filteredJobs {
		if len(allowedKinds) > 0 && !allowedKinds["job"] {
			continue
		}
		text := strings.Join([]string{job.Label, job.CWD, stringifySummary(job.Summary)}, "\n")
		match, ok := makeHistoryMatch("job", query, text)
		if !ok {
			continue
		}
		matchRecord := core.HistoryMatch{
			Kind:      "job",
			ID:        job.JobID,
			WorkID:    job.WorkID,
			SessionID: job.SessionID,
			JobID:     job.JobID,
			Adapter:   job.Adapter,
			Model:     summaryString(job.Summary, "model"),
			CWD:       job.CWD,
			Timestamp: job.UpdatedAt,
			Title:     job.Label,
			Snippet:   match,
			Score:     historyScore(query, text),
			Source:    "canonical",
		}
		applyUsageContract(&matchRecord, jobUsageByID[job.JobID])
		matches = append(matches, matchRecord)
	}

	if len(allowedKinds) == 0 || allowedKinds["turn"] {
		turns, err := s.store.ListRecentTurns(ctx, scanLimit)
		if err != nil {
			return nil, err
		}
		for _, turn := range turns {
			job, ok := jobByID[turn.JobID]
			if !ok {
				continue
			}
			text := strings.Join([]string{turn.InputText, turn.ResultSummary}, "\n")
			match, ok := makeHistoryMatch("turn", query, text)
			if !ok {
				continue
			}
			matchRecord := core.HistoryMatch{
				Kind:      "turn",
				ID:        turn.TurnID,
				WorkID:    job.WorkID,
				SessionID: turn.SessionID,
				JobID:     turn.JobID,
				Adapter:   turn.Adapter,
				Model:     summaryString(job.Summary, "model"),
				CWD:       job.CWD,
				Timestamp: turn.StartedAt,
				Title:     turn.InputSource,
				Snippet:   match,
				Score:     historyScore(query, text),
				Source:    "canonical",
			}
			applyUsageContract(&matchRecord, jobUsageByID[job.JobID])
			matches = append(matches, matchRecord)
		}
	}

	if len(allowedKinds) == 0 || allowedKinds["event"] {
		events, err := s.store.ListRecentEvents(ctx, scanLimit)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			job, ok := jobByID[event.JobID]
			if !ok {
				continue
			}
			text := event.Kind + "\n" + string(event.Payload)
			match, ok := makeHistoryMatch("event", query, text)
			if !ok {
				continue
			}
			matchRecord := core.HistoryMatch{
				Kind:      "event",
				ID:        event.EventID,
				WorkID:    job.WorkID,
				SessionID: event.SessionID,
				JobID:     event.JobID,
				Adapter:   event.Adapter,
				Model:     summaryString(job.Summary, "model"),
				CWD:       job.CWD,
				Timestamp: event.TS,
				Title:     event.Kind,
				Snippet:   match,
				Score:     historyScore(query, text),
				Source:    "canonical",
			}
			applyUsageContract(&matchRecord, jobUsageByID[job.JobID])
			matches = append(matches, matchRecord)
		}
	}

	if len(allowedKinds) == 0 || allowedKinds["artifact"] {
		artifacts, err := s.store.ListRecentArtifacts(ctx, scanLimit)
		if err != nil {
			return nil, err
		}
		for _, artifact := range artifacts {
			job, ok := jobByID[artifact.JobID]
			if !ok {
				continue
			}
			text := strings.Join([]string{artifact.Kind, artifact.Path, stringifySummary(artifact.Metadata)}, "\n")
			contentMatch := ""
			contentText := ""
			if shouldSearchArtifactContent(artifact.Path) {
				if data, err := os.ReadFile(artifact.Path); err == nil {
					contentText = string(data)
					text += "\n" + contentText
					if snippet, ok := makeHistoryMatch("artifact", query, contentText); ok {
						contentMatch = snippet
					}
				}
			}
			match, ok := makeHistoryMatch("artifact", query, text)
			if !ok {
				continue
			}
			if contentMatch != "" {
				match = contentMatch
			}
			copyArtifact := artifact
			matchRecord := core.HistoryMatch{
				Kind:      "artifact",
				ID:        artifact.ArtifactID,
				WorkID:    job.WorkID,
				SessionID: artifact.SessionID,
				JobID:     artifact.JobID,
				Adapter:   job.Adapter,
				Model:     summaryString(job.Summary, "model"),
				CWD:       job.CWD,
				Timestamp: artifact.CreatedAt,
				Title:     artifact.Kind,
				Snippet:   match,
				Path:      artifact.Path,
				Score:     historyScore(query, text),
				Source:    "canonical",
				Artifact:  &copyArtifact,
			}
			applyUsageContract(&matchRecord, jobUsageByID[job.JobID])
			matches = append(matches, matchRecord)
		}
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Timestamp.After(matches[j].Timestamp)
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return &HistorySearchResult{Matches: matches}, nil
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
	count := 0
	if req.JobID != "" {
		count++
	}
	if req.SessionID != "" {
		count++
	}
	if req.WorkID != "" {
		count++
	}
	if count != 1 {
		return nil, fmt.Errorf("%w: specify exactly one of job_id, session_id, or work_id", ErrInvalidInput)
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
	if req.WorkID != "" {
		if _, err := s.store.GetWorkItem(ctx, req.WorkID); err != nil {
			return nil, normalizeStoreError("work", req.WorkID, err)
		}
		return s.listArtifactsForWork(ctx, req.WorkID, req.Limit)
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

func (s *Service) AttachArtifact(ctx context.Context, req AttachArtifactRequest) (*core.ArtifactRecord, error) {
	targetCount := 0
	if req.JobID != "" {
		targetCount++
	}
	if req.SessionID != "" {
		targetCount++
	}
	if req.WorkID != "" {
		targetCount++
	}
	if targetCount != 1 {
		return nil, fmt.Errorf("%w: specify exactly one of job_id, session_id, or work_id", ErrInvalidInput)
	}

	sourcePath := strings.TrimSpace(req.Path)
	if sourcePath == "" {
		return nil, fmt.Errorf("%w: path must not be empty", ErrInvalidInput)
	}
	absoluteSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve path %q: %v", ErrInvalidInput, sourcePath, err)
	}
	info, err := os.Stat(absoluteSource)
	if err != nil {
		return nil, fmt.Errorf("%w: stat path %q: %v", ErrInvalidInput, absoluteSource, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%w: path %q is a directory", ErrInvalidInput, absoluteSource)
	}

	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = inferArtifactKind(absoluteSource)
	}

	jobID := strings.TrimSpace(req.JobID)
	sessionID := strings.TrimSpace(req.SessionID)
	if jobID != "" {
		job, err := s.store.GetJob(ctx, jobID)
		if err != nil {
			return nil, normalizeStoreError("job", jobID, err)
		}
		if sessionID == "" {
			sessionID = job.SessionID
		}
	}
	if sessionID != "" {
		if _, err := s.store.GetSession(ctx, sessionID); err != nil {
			return nil, normalizeStoreError("session", sessionID, err)
		}
	}
	if req.WorkID != "" {
		work, err := s.store.GetWorkItem(ctx, req.WorkID)
		if err != nil {
			return nil, normalizeStoreError("work", req.WorkID, err)
		}
		if work.CurrentJobID == "" || work.CurrentSessionID == "" {
			return nil, fmt.Errorf("%w: work %s has no current job/session to attach against", ErrInvalidInput, req.WorkID)
		}
		jobID = work.CurrentJobID
		sessionID = work.CurrentSessionID
	}

	storedPath := absoluteSource
	if req.Copy {
		artifactID := core.GenerateID("art")
		targetDir := filepath.Join(s.Paths.StateDir, "artifacts", "attached", artifactID)
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return nil, fmt.Errorf("create attachment directory: %w", err)
		}
		targetPath := filepath.Join(targetDir, filepath.Base(absoluteSource))
		data, err := os.ReadFile(absoluteSource)
		if err != nil {
			return nil, fmt.Errorf("read attachment source: %w", err)
		}
		if err := os.WriteFile(targetPath, data, 0o644); err != nil {
			return nil, fmt.Errorf("write attachment copy: %w", err)
		}
		artifact := core.ArtifactRecord{
			ArtifactID: artifactID,
			JobID:      jobID,
			SessionID:  sessionID,
			Kind:       kind,
			Path:       targetPath,
			CreatedAt:  time.Now().UTC(),
			Metadata:   cloneMap(req.Metadata),
		}
		artifact.Metadata["attached_from"] = absoluteSource
		artifact.Metadata["copied"] = true
		if req.WorkID != "" {
			artifact.Metadata["work_id"] = req.WorkID
		}
		if err := s.store.InsertArtifact(ctx, artifact); err != nil {
			return nil, err
		}
		return &artifact, nil
	}

	artifact := core.ArtifactRecord{
		ArtifactID: core.GenerateID("art"),
		JobID:      jobID,
		SessionID:  sessionID,
		Kind:       kind,
		Path:       storedPath,
		CreatedAt:  time.Now().UTC(),
		Metadata:   cloneMap(req.Metadata),
	}
	artifact.Metadata["attached_from"] = absoluteSource
	artifact.Metadata["copied"] = false
	if req.WorkID != "" {
		artifact.Metadata["work_id"] = req.WorkID
	}
	if err := s.store.InsertArtifact(ctx, artifact); err != nil {
		return nil, err
	}
	return &artifact, nil
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

func cloneSlice(src []string) []string {
	if len(src) == 0 {
		return []string{}
	}
	dst := make([]string, len(src))
	copy(dst, src)
	return dst
}

func copyRequiredAttestations(src []core.RequiredAttestation) []core.RequiredAttestation {
	if len(src) == 0 {
		return []core.RequiredAttestation{}
	}
	dst := make([]core.RequiredAttestation, len(src))
	for i, slot := range src {
		dst[i] = core.RequiredAttestation{
			VerifierKind: slot.VerifierKind,
			Method:       slot.Method,
			Blocking:     slot.Blocking,
			Metadata:     cloneMap(slot.Metadata),
		}
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

func diagnosticMessage(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if message := summaryString(payload, "message"); message != "" {
		return message
	}
	if message, _ := payload["error"].(string); message != "" {
		return message
	}
	if errValue, ok := payload["error"].(map[string]any); ok {
		if data, ok := errValue["data"].(map[string]any); ok {
			if message := summaryString(data, "message"); message != "" {
				return message
			}
		}
	}
	return ""
}

func (s *Service) annotateCatalogSnapshot(ctx context.Context, snapshot *core.CatalogSnapshot) error {
	history, err := s.catalogHistory(ctx, 500)
	if err != nil {
		return err
	}
	for idx := range snapshot.Entries {
		entry := &snapshot.Entries[idx]
		entry.Pricing = pricing.Resolve(s.Config, entry.Provider, entry.Model)
		entry.Traits = inferCatalogTraits(*entry, s.Config)
		entry.History = nil
		if hist, ok := history[catalogEntryKey(*entry)]; ok {
			historyCopy := hist
			entry.History = &historyCopy
		}
	}
	sort.SliceStable(snapshot.Entries, func(i, j int) bool {
		return catalogEntryLess(snapshot.Entries[i], snapshot.Entries[j])
	})
	return nil
}

func inferCatalogTraits(entry core.CatalogEntry, cfg core.Config) []string {
	traits := map[string]bool{}
	add := func(values ...string) {
		for _, value := range values {
			value = strings.ToLower(strings.TrimSpace(value))
			if value == "" {
				continue
			}
			traits[value] = true
		}
	}

	add(entry.Adapter, entry.Provider, entry.BillingClass, entry.AuthMethod)
	if adapterCfg, ok := cfg.Adapters.ByName(entry.Adapter); ok {
		add(adapterCfg.Tags...)
		if adapterCfg.Speed != "" {
			add("speed:" + adapterCfg.Speed)
		}
		if adapterCfg.Cost != "" {
			add("cost:" + adapterCfg.Cost)
		}
	}

	model := strings.ToLower(entry.Model)
	switch {
	case strings.Contains(model, "gpt-5.4"):
		add("reasoning:high", "planning", "review", "premium", "implementation:strong")
	case strings.Contains(model, "glm-5"):
		add("planning", "verification", "reasoning:high", "speed:slow", "long_run")
	case strings.Contains(model, "haiku"):
		add("speed:fast", "reasoning:light", "review", "cheap")
	case strings.Contains(model, "minimax"):
		add("implementation", "speed:fast", "cheap", "reasoning:light")
	case strings.Contains(model, "mimo"):
		add("implementation", "speed:fast", "cheap", "reasoning:light")
	case strings.Contains(model, "nano"):
		add("cheap", "speed:fast", "reasoning:light")
	case strings.Contains(model, "sonnet"):
		add("planning", "review", "implementation:strong", "reasoning:high")
	case strings.Contains(model, "opus"):
		add("planning", "review", "reasoning:high", "premium")
	}

	if entry.BillingClass == "subscription" {
		add("account-backed")
	}
	if entry.BillingClass == "metered_api" || entry.BillingClass == "cloud_project" {
		add("api-metered")
	}

	result := make([]string, 0, len(traits))
	for trait := range traits {
		result = append(result, trait)
	}
	sort.Strings(result)
	return result
}

func (s *Service) catalogSnapshotOrSync(ctx context.Context) (core.CatalogSnapshot, error) {
	snapshot, err := s.store.LatestCatalogSnapshot(ctx)
	if err == nil {
		if annotateErr := s.annotateCatalogSnapshot(ctx, &snapshot); annotateErr != nil {
			return core.CatalogSnapshot{}, annotateErr
		}
		return snapshot, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return core.CatalogSnapshot{}, err
	}
	result, err := s.SyncCatalog(ctx)
	if err != nil {
		return core.CatalogSnapshot{}, err
	}
	return result.Snapshot, nil
}

func filterCatalogEntries(entries []core.CatalogEntry, req ProbeCatalogRequest) []core.CatalogEntry {
	filtered := make([]core.CatalogEntry, 0, len(entries))
	for _, entry := range entries {
		if req.Adapter != "" && entry.Adapter != req.Adapter {
			continue
		}
		if req.Provider != "" && entry.Provider != req.Provider {
			continue
		}
		if req.Model != "" && entry.Model != req.Model {
			continue
		}
		if entry.Model == "" || !entry.Available {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func catalogEntryKey(entry core.CatalogEntry) string {
	return catalogHistoryKey(entry.Adapter, entry.Provider, entry.Model)
}

func catalogHistoryKey(adapter, provider, model string) string {
	return strings.ToLower(strings.Join([]string{adapter, provider, model}, "|"))
}

// a best-effort audit trail only.
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

	// When raw data will be written as an artifact file, don't duplicate
	// the payload in the events table — it's the dominant source of db bloat.
	// The event row keeps metadata (kind, seq, job_id) for ordering; the
	// actual content lives in the artifact file referenced by raw_ref.
	dbPayload := encoded
	if len(rawData) > 0 && rawStream != "" {
		dbPayload = json.RawMessage(`{}`)
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
		Payload:         dbPayload,
	}

	if err := s.store.AppendEvent(ctx, event); err != nil {
		return nil, err
	}
	// Restore full payload on the in-memory record for callers.
	event.Payload = encoded

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
