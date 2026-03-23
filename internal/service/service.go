package service

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yusefmosiah/fase/internal/adapterapi"
	"github.com/yusefmosiah/fase/internal/adapters"
	catalogpkg "github.com/yusefmosiah/fase/internal/catalog"
	"github.com/yusefmosiah/fase/internal/core"
	debriefpkg "github.com/yusefmosiah/fase/internal/debrief"
	"github.com/yusefmosiah/fase/internal/notify"
	"github.com/yusefmosiah/fase/internal/events"
	"github.com/yusefmosiah/fase/internal/pricing"
	"github.com/yusefmosiah/fase/internal/store"
	transferpkg "github.com/yusefmosiah/fase/internal/transfer"
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

type Service struct {
	Paths         core.Paths
	Config        core.Config
	ConfigPath    string
	ConfigPresent bool
	store         *store.Store
	Events        EventBus
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
	Job            core.JobRecord             `json:"job"`
	Session        core.SessionRecord         `json:"session"`
	NativeSessions []core.NativeSessionRecord `json:"native_sessions"`
	Events         []core.EventRecord         `json:"events"`
	Usage          *core.UsageReport          `json:"usage,omitempty"`
	UsageByModel   []core.UsageReport         `json:"usage_by_model,omitempty"`
	Cost           *core.CostEstimate         `json:"cost,omitempty"`
	VendorCost     *core.CostEstimate         `json:"vendor_cost,omitempty"`
	EstimatedCost  *core.CostEstimate         `json:"estimated_cost,omitempty"`
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
	Acceptance           map[string]any             `json:"acceptance,omitempty"`
	Metadata             map[string]any             `json:"metadata,omitempty"`
	HeadCommitOID        string                     `json:"head_commit_oid,omitempty"`
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

type WorkCheckRequest struct {
	WorkID       string           `json:"work_id,omitempty"`
	Result       string           `json:"result"` // "pass" or "fail"
	CheckerModel string           `json:"checker_model,omitempty"`
	WorkerModel  string           `json:"worker_model,omitempty"`
	Report       core.CheckReport `json:"report"`
	CreatedBy    string           `json:"created_by,omitempty"`
}

type WorkCheckResult struct {
	CheckRecord core.CheckRecord    `json:"check_record"`
	Work        core.WorkItemRecord `json:"work"`
}

type WorkShowResult struct {
	Work         core.WorkItemRecord       `json:"work"`
	Children     []core.WorkItemRecord     `json:"children,omitempty"`
	Updates      []core.WorkUpdateRecord   `json:"updates,omitempty"`
	Notes        []core.WorkNoteRecord     `json:"notes,omitempty"`
	Jobs         []core.JobRecord          `json:"jobs,omitempty"`
	Proposals    []core.WorkProposalRecord `json:"proposals,omitempty"`
	Attestations []core.AttestationRecord  `json:"attestations,omitempty"`
	Approvals    []core.ApprovalRecord     `json:"approvals,omitempty"`
	Promotions   []core.PromotionRecord    `json:"promotions,omitempty"`
	Artifacts    []core.ArtifactRecord     `json:"artifacts,omitempty"`
	Docs         []core.DocContentRecord   `json:"docs,omitempty"`
}

type WorkClaimRequest struct {
	WorkID        string        `json:"work_id"`
	Claimant      string        `json:"claimant,omitempty"`
	LeaseDuration time.Duration `json:"lease_duration,omitempty"`
}

type WorkClaimNextRequest struct {
	Claimant      string        `json:"claimant,omitempty"`
	LeaseDuration time.Duration `json:"lease_duration,omitempty"`
	Limit         int           `json:"limit,omitempty"`
}

type WorkReleaseRequest struct {
	WorkID    string `json:"work_id,omitempty"`
	Claimant  string `json:"claimant,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
	Force     bool   `json:"force,omitempty"`
}

type WorkRenewLeaseRequest struct {
	WorkID        string        `json:"work_id,omitempty"`
	Claimant      string        `json:"claimant,omitempty"`
	LeaseDuration time.Duration `json:"lease_duration,omitempty"`
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
// `run --work` that need to share the database with a running `fase serve` instance.
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
		Paths:         paths,
		Config:        cfg,
		ConfigPath:    resolvedConfigPath,
		ConfigPresent: configPresent,
		store:         db,
	}, nil
}

// sendWorkNotification sends an email when work items transition to done.
// It renders the checker's CheckReport as HTML with inline screenshots.
func (s *Service) sendWorkNotification(ctx context.Context, work core.WorkItemRecord, message string) {
	apiKey := os.Getenv("RESEND_API_KEY")
	to := os.Getenv("EMAIL_TO")
	if apiKey == "" || to == "" {
		return
	}

	subject := fmt.Sprintf("[FASE] done: %s", work.Title)

	// Try to find the latest passing check record to render as proof.
	checkRecords, err := s.store.ListCheckRecords(ctx, work.WorkID, 10)
	var html string
	var attachments []notify.ResendEmailAttachment

	if err == nil {
		// Find the most recent passing check record.
		for _, cr := range checkRecords {
			if cr.Result == "pass" {
				html = notify.BuildCheckReportEmail(&work, cr)
				attachments = s.collectCheckArtifacts(ctx, work.WorkID, cr)
				break
			}
		}
	}

	if html == "" {
		// Fallback: basic completion email without check report.
		attestations, _ := s.store.ListAttestationRecords(ctx, "work", work.WorkID, 10)
		html = notify.BuildWorkCompletionEmail(&work, message, attestations, true)
		attachments = s.collectPlaywrightAttachments(ctx, work.WorkID)
	}

	notify.SendEmail(ctx, apiKey, to, subject, html, attachments)
}

// collectCheckArtifacts collects screenshots from the check report's artifact paths
// and from .fase/artifacts/<work-id>/screenshots/ in the project root.
func (s *Service) collectCheckArtifacts(ctx context.Context, workID string, cr core.CheckRecord) []notify.ResendEmailAttachment {
	var attachments []notify.ResendEmailAttachment

	// Collect screenshots referenced directly in the check report.
	for _, screenshotPath := range cr.Report.Screenshots {
		data, err := os.ReadFile(screenshotPath)
		if err != nil {
			continue
		}
		attachments = append(attachments, notify.ResendEmailAttachment{
			Filename:    filepath.Base(screenshotPath),
			Content:     base64.StdEncoding.EncodeToString(data),
			ContentType: "image/png",
		})
	}
	if len(attachments) > 0 {
		return attachments
	}

	// Fallback: look in .fase/artifacts/<work-id>/screenshots/ under the project root.
	projectRoot := s.findProjectRoot(ctx, workID)
	if projectRoot != "" {
		screenshotDir := filepath.Join(projectRoot, ".fase", "artifacts", workID, "screenshots")
		if found := collectScreenshots(screenshotDir); len(found) > 0 {
			return found
		}
	}

	// Final fallback: Playwright test-results directories.
	return s.collectPlaywrightAttachments(ctx, workID)
}

// findProjectRoot finds the git root from the job CWD for a work item.
func (s *Service) findProjectRoot(ctx context.Context, workID string) string {
	jobs, err := s.store.ListJobsByWork(ctx, workID, 10)
	if err != nil || len(jobs) == 0 {
		return ""
	}
	cwd := verifyRepoPath(jobs)
	if cwd == "" || cwd == "." {
		return ""
	}
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// collectPlaywrightAttachments looks up the job CWD for the work item and
// returns any PNG screenshots found in mind-graph/test-results/.
func (s *Service) collectPlaywrightAttachments(ctx context.Context, workID string) []notify.ResendEmailAttachment {
	jobs, err := s.store.ListJobsByWork(ctx, workID, 10)
	if err != nil || len(jobs) == 0 {
		return nil
	}
	cwd := verifyRepoPath(jobs)
	if cwd == "" || cwd == "." {
		return nil
	}
	// Check multiple possible locations for Playwright artifacts.
	for _, subdir := range []string{"test-results", "tests/test-results", "mind-graph/test-results"} {
		dir := filepath.Join(cwd, subdir)
		if attachments := collectScreenshots(dir); len(attachments) > 0 {
			return attachments
		}
	}
	return nil
}

// collectScreenshots walks dir recursively and returns PNG files as base64 attachments.
func collectScreenshots(dir string) []notify.ResendEmailAttachment {
	var attachments []notify.ResendEmailAttachment
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".png") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		attachments = append(attachments, notify.ResendEmailAttachment{
			Filename:    name,
			Content:     base64.StdEncoding.EncodeToString(data),
			ContentType: "image/png",
		})
		return nil
	})
	return attachments
}

func (s *Service) Close() error {
	return s.store.Close()
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
func (s *Service) CreateCheckRecord(ctx context.Context, req CheckRecordCreateRequest) (core.CheckRecord, error) {
	if req.WorkID == "" {
		return core.CheckRecord{}, fmt.Errorf("%w: work_id must not be empty", ErrInvalidInput)
	}
	if req.Result != "pass" && req.Result != "fail" {
		return core.CheckRecord{}, fmt.Errorf("%w: result must be 'pass' or 'fail'", ErrInvalidInput)
	}
	rec := core.CheckRecord{
		CheckID:      core.GenerateID("chk"),
		WorkID:       req.WorkID,
		CheckerModel: req.CheckerModel,
		WorkerModel:  req.WorkerModel,
		Result:       req.Result,
		Report:       req.Report,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.store.CreateCheckRecord(ctx, rec); err != nil {
		return core.CheckRecord{}, err
	}
	s.Events.Publish(WorkEvent{
		Kind:   WorkEventCheckRecorded,
		WorkID: req.WorkID,
		State:  req.Result,
		Actor:  actorFromCreatedBy(req.CreatedBy),
		Cause:  CauseCheckRecorded,
		Metadata: map[string]string{
			"check_id": rec.CheckID,
			"result":   req.Result,
		},
	})
	return rec, nil
}

func (s *Service) GetCheckRecord(ctx context.Context, checkID string) (core.CheckRecord, error) {
	rec, err := s.store.GetCheckRecord(ctx, checkID)
	if err != nil {
		return core.CheckRecord{}, normalizeStoreError("check_record", checkID, err)
	}
	return rec, nil
}

func (s *Service) ListCheckRecords(ctx context.Context, workID string, limit int) ([]core.CheckRecord, error) {
	return s.store.ListCheckRecords(ctx, workID, limit)
}

// CreateCheckRecordDirect is an acyclic bridge for the native adapter's in-process tool registration.
// It accepts only core and primitive types so the native adapter can define a matching interface
// without importing the service package (which would create an import cycle).
func (s *Service) CreateCheckRecordDirect(ctx context.Context, workID, result, checkerModel, workerModel string, report core.CheckReport) (core.CheckRecord, error) {
	return s.CreateCheckRecord(ctx, CheckRecordCreateRequest{
		WorkID:       workID,
		Result:       result,
		CheckerModel: checkerModel,
		WorkerModel:  workerModel,
		Report:       report,
		CreatedBy:    "worker",
	})
}

// SendSpecEscalationEmail emails the human when a work item has failed checks 3+ times.
func (s *Service) SendSpecEscalationEmail(ctx context.Context, workID, summary, recommendation string) {
	apiKey := os.Getenv("RESEND_API_KEY")
	to := os.Getenv("EMAIL_TO")
	if apiKey == "" || to == "" {
		return
	}
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return
	}
	checkRecords, _ := s.store.ListCheckRecords(ctx, workID, 10)
	subject := fmt.Sprintf("[FASE] spec question: %s", work.Title)
	html := notify.BuildSpecEscalationEmail(&work, checkRecords, summary, recommendation)
	notify.SendEmail(ctx, apiKey, to, subject, html, nil)
}

// Edge operations — direct, no proposal ceremony.

func (s *Service) CreateEdge(ctx context.Context, rec core.WorkEdgeRecord) error {
	// Validate both work items exist
	if _, err := s.store.GetWorkItem(ctx, rec.FromWorkID); err != nil {
		return normalizeStoreError("work", rec.FromWorkID, err)
	}
	if _, err := s.store.GetWorkItem(ctx, rec.ToWorkID); err != nil {
		return normalizeStoreError("work", rec.ToWorkID, err)
	}
	return s.store.CreateWorkEdge(ctx, rec)
}

func (s *Service) DeleteEdge(ctx context.Context, edgeID string) error {
	return s.store.DeleteWorkEdge(ctx, edgeID)
}

func (s *Service) ListEdges(ctx context.Context, limit int, edgeType, fromWorkID, toWorkID string) ([]core.WorkEdgeRecord, error) {
	return s.store.ListWorkEdges(ctx, limit, edgeType, fromWorkID, toWorkID)
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
		if err := s.markWorkQueued(ctx, req.WorkID, job, session); err != nil {
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

	vendorCost := vendorCostFromSummary(job)
	estimatedCost := estimatedCostFromSummary(job)
	selectedCost := vendorCost
	if selectedCost == nil {
		selectedCost = estimatedCost
	}

	return &StatusResult{
		Job:            job,
		Session:        session,
		NativeSessions: nativeSessions,
		Events:         events,
		Usage:          usageFromSummary(job.Summary),
		UsageByModel:   modelUsageFromSummary(job.Summary),
		Cost:           selectedCost,
		VendorCost:     vendorCost,
		EstimatedCost:  estimatedCost,
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

func (s *Service) CreateWork(ctx context.Context, req WorkCreateRequest) (*core.WorkItemRecord, error) {
	title := strings.TrimSpace(req.Title)
	objective := strings.TrimSpace(req.Objective)
	if title == "" {
		return nil, fmt.Errorf("%w: title must not be empty", ErrInvalidInput)
	}
	if objective == "" {
		return nil, fmt.Errorf("%w: objective must not be empty", ErrInvalidInput)
	}

	// DEDUP CHECK: Prevent MCP tool call retries from creating duplicate work items
	// Check for recent duplicate work items with identical title and objective
	// within the last 5 seconds (MCP retry window).
	recentWork, _ := s.store.ListWorkItems(ctx, 100, "", "", "", true)
	for _, existing := range recentWork {
		if existing.Title == title && existing.Objective == objective {
			if time.Since(existing.CreatedAt) < 5*time.Second {
				// Return the existing work item instead of creating a duplicate
				// This prevents MCP retries from creating multiple copies
				s.Events.Publish(WorkEvent{
					Kind:   WorkEventCreated,
					WorkID: existing.WorkID,
					Title:  existing.Title,
					State:  string(existing.ExecutionState),
					Actor:  ActorService,
					Cause:  CauseWorkCreated,
					Metadata: map[string]string{
						"duplicate_suppressed": "true",
					},
				})
				return &existing, nil
			}
		}
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "task"
	}
	now := time.Now().UTC()
	lockState := req.LockState
	if lockState == "" {
		lockState = core.WorkLockStateUnlocked
	}
	work := core.WorkItemRecord{
		WorkID:               core.GenerateID("work"),
		Title:                title,
		Objective:            objective,
		Kind:                 kind,
		ExecutionState:       core.WorkExecutionStateReady,
		ApprovalState:        core.WorkApprovalStateNone,
		LockState:            lockState,
		Priority:             req.Priority,
		ConfigurationClass:   strings.TrimSpace(req.ConfigurationClass),
		BudgetClass:          strings.TrimSpace(req.BudgetClass),
		RequiredCapabilities: cloneSlice(req.RequiredCapabilities),
		RequiredModelTraits:  cloneSlice(req.RequiredModelTraits),
		PreferredAdapters:    cloneSlice(req.PreferredAdapters),
		ForbiddenAdapters:    cloneSlice(req.ForbiddenAdapters),
		PreferredModels:      cloneSlice(req.PreferredModels),
		AvoidModels:          cloneSlice(req.AvoidModels),
		Acceptance:           cloneMap(req.Acceptance),
		Metadata:             cloneMap(req.Metadata),
		HeadCommitOID:        strings.TrimSpace(req.HeadCommitOID),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	work.RequiredAttestations = defaultRequiredAttestations(work, req.RequiredAttestations, s.Config)
	if req.Position > 0 {
		work.Position = req.Position
		if err := s.store.CreateWorkItem(ctx, work); err != nil {
			return nil, err
		}
	} else {
		if err := s.store.CreateWorkItemWithAutoPosition(ctx, work); err != nil {
			return nil, err
		}
	}
	if req.ParentWorkID != "" {
		if _, err := s.store.GetWorkItem(ctx, req.ParentWorkID); err != nil {
			return nil, normalizeStoreError("work", req.ParentWorkID, err)
		}
		if err := s.attachParentEdge(ctx, req.ParentWorkID, work.WorkID, "service", now, map[string]any{}, false); err != nil {
			return nil, err
		}
	}
	s.Events.Publish(WorkEvent{
		Kind:   WorkEventCreated,
		WorkID: work.WorkID,
		Title:  work.Title,
		State:  string(work.ExecutionState),
		Actor:  ActorService,
		Cause:  CauseWorkCreated,
	})
	return &work, nil
}

// MoveWork repositions a work item to newPosition, shifting other items to keep positions contiguous.
// newPosition is 1-based (1 = front of queue). If newPosition <= 0 it is treated as 1.
func (s *Service) MoveWork(ctx context.Context, workID string, newPosition int) (*core.WorkItemRecord, error) {
	if newPosition <= 0 {
		newPosition = 1
	}
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	if work.Position == newPosition {
		return &work, nil
	}
	if err := s.store.MoveWorkPosition(ctx, workID, newPosition); err != nil {
		return nil, err
	}
	work.Position = newPosition
	return &work, nil
}

// InsertWorkBefore moves workID to just before beforeWorkID in the queue.
func (s *Service) InsertWorkBefore(ctx context.Context, workID, beforeWorkID string) (*core.WorkItemRecord, error) {
	before, err := s.store.GetWorkItem(ctx, beforeWorkID)
	if err != nil {
		return nil, normalizeStoreError("work", beforeWorkID, err)
	}
	return s.MoveWork(ctx, workID, before.Position)
}

// MoveToFront moves a work item to position 1 (front of the dispatch queue).
func (s *Service) MoveToFront(ctx context.Context, workID string) (*core.WorkItemRecord, error) {
	return s.MoveWork(ctx, workID, 1)
}

// ReorderQueue assigns sequential positions 1..N to the given work IDs in order.
// Any work items not in the list retain their existing positions (shifted to follow the reordered items).
func (s *Service) ReorderQueue(ctx context.Context, workIDs []string) error {
	return s.store.ReorderWorkPositions(ctx, workIDs)
}

func (s *Service) Work(ctx context.Context, workID string) (*WorkShowResult, error) {
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	children, err := s.store.ListWorkChildren(ctx, workID, 100)
	if err != nil {
		return nil, err
	}
	updates, err := s.store.ListWorkUpdates(ctx, workID, 50)
	if err != nil {
		return nil, err
	}
	notes, err := s.store.ListWorkNotes(ctx, workID, 50)
	if err != nil {
		return nil, err
	}
	jobs, err := s.store.ListJobsByWork(ctx, workID, 20)
	if err != nil {
		return nil, err
	}
	targetProposals, err := s.store.ListWorkProposals(ctx, 50, "", workID, "")
	if err != nil {
		return nil, err
	}
	sourceProposals, err := s.store.ListWorkProposals(ctx, 50, "", "", workID)
	if err != nil {
		return nil, err
	}
	proposals := targetProposals
	seenProposals := map[string]bool{}
	for _, proposal := range proposals {
		seenProposals[proposal.ProposalID] = true
	}
	for _, proposal := range sourceProposals {
		if seenProposals[proposal.ProposalID] {
			continue
		}
		proposals = append(proposals, proposal)
	}
	attestations, err := s.store.ListAttestationRecords(ctx, "work", workID, 50)
	if err != nil {
		return nil, err
	}
	approvals, err := s.store.ListApprovalRecords(ctx, workID, 50)
	if err != nil {
		return nil, err
	}
	promotions, err := s.store.ListPromotionRecords(ctx, workID, 50)
	if err != nil {
		return nil, err
	}
	artifacts, err := s.listArtifactsForWork(ctx, workID, 50)
	if err != nil {
		return nil, err
	}
	docs, _ := s.store.GetDocContent(ctx, workID)

	return &WorkShowResult{
		Work:         work,
		Children:     children,
		Updates:      updates,
		Notes:        notes,
		Jobs:         jobs,
		Proposals:    proposals,
		Attestations: attestations,
		Approvals:    approvals,
		Promotions:   promotions,
		Artifacts:    artifacts,
		Docs:         docs,
	}, nil
}

// CompileWorkerBriefing deterministically compiles a worker briefing from
// runtime state. This is the adapter-independent hydration contract — all
// adapters consume the same compiled briefing.
func (s *Service) CompileWorkerBriefing(ctx context.Context, workID, mode string) (WorkHydrateResult, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "standard"
	}
	if mode != "thin" && mode != "standard" && mode != "deep" && mode != "supervisor" {
		return nil, fmt.Errorf("%w: hydrate mode must be thin, standard, deep, or supervisor", ErrInvalidInput)
	}
	result, err := s.Work(ctx, workID)
	if err != nil {
		return nil, err
	}

	parent, _ := s.firstRelatedWork(ctx, workID, "parent_of", false)
	blockingInbound, _ := s.relatedWork(ctx, workID, "blocks", false, 25)
	blockingOutbound, _ := s.relatedWork(ctx, workID, "blocks", true, 25)
	children, _ := s.relatedWork(ctx, workID, "parent_of", true, 25)
	verifierNodes, _ := s.relatedWork(ctx, workID, "verifier", false, 25)
	discoveredNodes, _ := s.relatedWork(ctx, workID, "discovered_from", false, 25)
	supersedes, _ := s.relatedWork(ctx, workID, "supersedes", true, 25)
	supersededBy, _ := s.relatedWork(ctx, workID, "supersedes", false, 25)

	updateLimit, noteLimit, attestationLimit, artifactLimit, jobLimit := hydrationLimits(mode)
	updates := result.Updates
	if len(updates) > updateLimit {
		updates = updates[:updateLimit]
	}
	notes := result.Notes
	if len(notes) > noteLimit {
		notes = notes[:noteLimit]
	}
	attestations := result.Attestations
	if len(attestations) > attestationLimit {
		attestations = attestations[:attestationLimit]
	}
	artifacts := result.Artifacts
	if len(artifacts) > artifactLimit {
		artifacts = artifacts[:artifactLimit]
	}
	jobs := result.Jobs
	if len(jobs) > jobLimit {
		jobs = jobs[:jobLimit]
	}

	summary := fmt.Sprintf("%s: %s", result.Work.Kind, result.Work.Objective)
	if len(updates) > 0 && strings.TrimSpace(updates[0].Message) != "" {
		summary = summary + " Latest update: " + strings.TrimSpace(updates[0].Message)
	}
	openQuestions := []string{}
	if len(blockingInbound) > 0 {
		openQuestions = append(openQuestions, fmt.Sprintf("%d blocking dependencies remain unresolved.", len(blockingInbound)))
	}
	if len(attestations) == 0 && len(result.Work.RequiredAttestations) > 0 {
		openQuestions = append(openQuestions, "Required attestations have not been recorded yet.")
	}
	nextActions := []string{
		"Inspect the current work item objective and acceptance before making changes.",
		"Review the most recent updates, notes, and attestations.",
		"Publish a structured work update before handing off or stopping.",
	}
	nextActions = append(nextActions, delegationNextAction(result.Work))

	writeCommands := []string{
		"fase work update <work-id>",
		"fase work note-add <work-id>",
	}
	updateDoneCmd := fmt.Sprintf("fase work update %s --execution-state checking --message \"<summary of what you did>\"", workID)
	gitCommitCmd := fmt.Sprintf("git add -A && git commit -m \"fase(%s): <summary>\"", workID)
	updateFailCmd := fmt.Sprintf("fase work update %s --execution-state failed --message \"<what went wrong>\"", workID)
	contractRules := []string{
		"Do the work, add notes as you go, then commit and update state before exiting.",
		fmt.Sprintf("REQUIRED before exit: %s", gitCommitCmd),
		fmt.Sprintf("REQUIRED on success: %s", updateDoneCmd),
		fmt.Sprintf("REQUIRED on failure: %s", updateFailCmd),
		"You MUST call one of the above before exiting. The supervisor cannot see your work otherwise.",
		"Record notes for findings, risks, and open questions.",
		"Run verification (tests, builds) and report results as notes.",
		"If the work involves a web UI: you MUST add e2e tests (default: Playwright) covering all interactive features (buttons, drag, resize, navigation). Backend tests alone are insufficient — they cannot catch broken UI behavior.",
		"Do NOT create new work items, proposals, or child work. Only do what was assigned.",
		"Do NOT call fase work attest — an independent agent handles attestation.",
		delegationNextAction(result.Work),
	}

	if result.Work.Kind == "attest" {
		parentWorkID := "<parent-work-id>"
		if parent != nil {
			parentWorkID = parent.WorkID
		}
		attestCmd := fmt.Sprintf("fase work attest %s --result [passed|failed] --message \"<summary>\"", parentWorkID)
		writeCommands = append(writeCommands, attestCmd)
		attestInstruction := fmt.Sprintf(
			"REQUIRED: After completing your review, you MUST call: fase work attest %s --result passed|failed --message \"<your finding summary>\"",
			parentWorkID,
		)
		nextActions = append(nextActions, attestInstruction)
		contractRules = []string{
			"Review the parent work item thoroughly: inspect the code, diff, tests, notes, and evidence.",
			"Record notes for your findings before attesting.",
			"If the work involves a web UI: run Playwright e2e tests with 'cd mind-graph && npx playwright test'. Screenshots and videos are saved to mind-graph/test-results/ and will be attached to the attestation email automatically.",
			"If no Playwright tests exist for web UI work, FAIL the attestation — backend-only tests are insufficient for web UI work.",
			fmt.Sprintf("REQUIRED: You MUST call 'fase work attest %s --result passed|failed --message \"<your finding summary>\"' to submit your attestation result.", parentWorkID),
			"Use --result passed if the work meets its objective; use --result failed if it does not.",
			"Do NOT create new work items, proposals, or child work. Only do what was assigned.",
			"Do NOT call fase work complete or fase work fail.",
			delegationNextAction(result.Work),
		}
	}

	runtimeSection := map[string]any{
		"runtime_version": "dev",
		"config_path":     s.ConfigPath,
		"state_dir":       s.Paths.StateDir,
	}
	if claimant := firstNonEmpty(result.Work.ClaimedBy); claimant != "" {
		runtimeSection["claimant"] = claimant
	}

	assignmentSection := map[string]any{
		"work_id":         result.Work.WorkID,
		"title":           result.Work.Title,
		"objective":       result.Work.Objective,
		"kind":            result.Work.Kind,
		"execution_state": result.Work.ExecutionState,
		"approval_state":  result.Work.ApprovalState,
		"priority":        result.Work.Priority,
		"metadata":        cloneMap(result.Work.Metadata),
	}
	if result.Work.Phase != "" {
		assignmentSection["phase"] = result.Work.Phase
	}
	if result.Work.CurrentJobID != "" {
		assignmentSection["current_job_id"] = result.Work.CurrentJobID
	}
	if result.Work.CurrentSessionID != "" {
		assignmentSection["current_session_id"] = result.Work.CurrentSessionID
	}
	if result.Work.ClaimedBy != "" {
		assignmentSection["claimed_by"] = result.Work.ClaimedBy
	}
	if result.Work.ClaimedUntil != nil {
		assignmentSection["claimed_until"] = result.Work.ClaimedUntil.UTC().Format(time.RFC3339Nano)
	}

	return WorkHydrateResult{
		"schema_version": "fase.worker_briefing.v1",
		"briefing_kind":  "assignment",
		"generated_at":   time.Now().UTC().Format(time.RFC3339Nano),
		"runtime":        runtimeSection,
		"assignment":     assignmentSection,
		"requirements": map[string]any{
			"acceptance":            cloneMap(result.Work.Acceptance),
			"required_capabilities": cloneSlice(result.Work.RequiredCapabilities),
			"preferred_adapters":    cloneSlice(result.Work.PreferredAdapters),
			"forbidden_adapters":    cloneSlice(result.Work.ForbiddenAdapters),
			"policy": map[string]any{
				"child_creation":      "proposal_only",
				"dependency_edits":    "proposal_only",
				"scope_expansion":     "proposal_only",
				"verification_policy": "attestation_driven",
			},
		},
		"graph_context": map[string]any{
			"parent":            workRefOrNil(parent),
			"blocking_inbound":  workRefs(blockingInbound),
			"blocking_outbound": workRefs(blockingOutbound),
			"children":          workRefs(children),
			"verifier_nodes":    workRefs(verifierNodes),
			"discovered_nodes":  workRefs(discoveredNodes),
			"supersession": map[string]any{
				"supersedes":      workRefs(supersedes),
				"supersededed_by": workRefs(supersededBy),
			},
		},
		"evidence": map[string]any{
			"latest_updates":      updateRefs(updates),
			"latest_notes":        noteRefs(notes),
			"latest_attestations": attestationRefs(attestations),
			"artifacts":           artifactRefs(artifacts),
			"recent_jobs":         jobRefs(jobs),
			"history_matches":     []map[string]any{},
		},
		"worker_contract": map[string]any{
			"read_commands": []string{
				"fase work show <work-id>",
				"fase work notes <work-id>",
				"fase artifacts list --work <work-id>",
				"fase history search --query <text>",
			},
			"write_commands": writeCommands,
			"rules":          contractRules,
		},
		"hydration": map[string]any{
			"mode":                     mode,
			"summary":                  summary,
			"open_questions":           openQuestions,
			"recommended_next_actions": nextActions,
		},
	}, nil
}

// ProjectHydrate compiles a project-scoped briefing for cold-starting any session.
// Unlike work-scoped hydration, this covers the entire project: conventions, graph summary,
// active/blocked/ready work, and recent activity. Designed to replace the MEMORY.md bootstrap hack.
func (s *Service) ProjectHydrate(ctx context.Context, req ProjectHydrateRequest) (ProjectHydrateResult, error) {
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "standard"
	}
	if mode != "thin" && mode != "standard" && mode != "deep" && mode != "supervisor" {
		return nil, fmt.Errorf("%w: hydrate mode must be thin, standard, deep, or supervisor", ErrInvalidInput)
	}

	// Conventions — the core of project hydration.
	conventionLimit := 50
	if mode == "thin" {
		conventionLimit = 20
	} else if mode == "deep" {
		conventionLimit = 200
	}
	conventions, err := s.store.ListConventionNotes(ctx, conventionLimit)
	if err != nil {
		return nil, fmt.Errorf("list conventions: %w", err)
	}

	// Work queue summary — counts by execution state.
	allWork, err := s.ListWork(ctx, WorkListRequest{Limit: 500, IncludeArchived: false})
	if err != nil {
		return nil, fmt.Errorf("list work: %w", err)
	}
	stateCounts := map[core.WorkExecutionState]int{}
	var recentCompleted []map[string]any
	var activeWork []map[string]any
	var readyWork []map[string]any
	var blockedWork []map[string]any
	completedLimit := 5
	if mode == "deep" {
		completedLimit = 15
	}
	for _, w := range allWork {
		stateCounts[w.ExecutionState]++
		switch w.ExecutionState {
		case core.WorkExecutionStateDone:
			if len(recentCompleted) < completedLimit {
				recentCompleted = append(recentCompleted, map[string]any{
					"work_id": w.WorkID,
					"title":   w.Title,
					"kind":    w.Kind,
				})
			}
		case core.WorkExecutionStateInProgress, core.WorkExecutionStateClaimed:
			activeWork = append(activeWork, map[string]any{
				"work_id":    w.WorkID,
				"title":      w.Title,
				"kind":       w.Kind,
				"claimed_by": w.ClaimedBy,
			})
		case core.WorkExecutionStateReady:
			entry := map[string]any{
				"work_id":  w.WorkID,
				"title":    w.Title,
				"kind":     w.Kind,
				"priority": w.Priority,
			}
			if len(w.PreferredAdapters) > 0 {
				entry["preferred_adapters"] = w.PreferredAdapters
			}
			if len(w.PreferredModels) > 0 {
				entry["preferred_models"] = w.PreferredModels
			}
			readyWork = append(readyWork, entry)
		case core.WorkExecutionStateBlocked:
			blockedWork = append(blockedWork, map[string]any{
				"work_id": w.WorkID,
				"title":   w.Title,
				"kind":    w.Kind,
			})
		}
	}

	// Pending attestations — work awaiting review.
	var pendingAttestations []map[string]any
	for _, w := range allWork {
		if w.ExecutionState == core.WorkExecutionStateAwaitingAttestation {
			pendingAttestations = append(pendingAttestations, map[string]any{
				"work_id":               w.WorkID,
				"title":                 w.Title,
				"required_attestations": w.RequiredAttestations,
			})
		}
	}

	// Compile conventions into a deduplicated list (newest wins on duplicate body).
	conventionEntries := make([]map[string]any, 0, len(conventions))
	seen := map[string]bool{}
	for _, note := range conventions {
		body := strings.TrimSpace(note.Body)
		if seen[body] {
			continue
		}
		seen[body] = true
		entry := map[string]any{
			"body":       body,
			"created_at": note.CreatedAt.UTC().Format(time.RFC3339),
		}
		if note.WorkID != "" {
			entry["source_work_id"] = note.WorkID
		}
		conventionEntries = append(conventionEntries, entry)
	}

	effectiveMode := mode
	if mode == "supervisor" {
		effectiveMode = "standard"
	}

	// Load project spec (SPEC.md) if present — gives supervisor and workers
	// project-specific context beyond conventions.
	var projectSpec string
	cwd, _ := os.Getwd()
	for _, specName := range []string{"SPEC.md", "spec.md", "SPEC", "README.md"} {
		if data, err := os.ReadFile(filepath.Join(cwd, specName)); err == nil {
			projectSpec = strings.TrimSpace(string(data))
			if len(projectSpec) > 4000 {
				projectSpec = projectSpec[:4000] + "\n\n[truncated — read full file with read_file tool]"
			}
			break
		}
	}

	result := ProjectHydrateResult{
		"schema_version": "fase.project_briefing.v1",
		"briefing_kind":  "project",
		"generated_at":   time.Now().UTC().Format(time.RFC3339Nano),
		"mode":           mode,
		"runtime": map[string]any{
			"config_path": s.ConfigPath,
			"state_dir":   s.Paths.StateDir,
		},
		"conventions": conventionEntries,
		"queue_summary": map[string]any{
			"total_items":  len(allWork),
			"state_counts": stateCounts,
		},
		"active_work":          activeWork,
		"ready_work":           readyWork,
		"blocked_work":         blockedWork,
		"recent_completed":     recentCompleted,
		"pending_attestations": pendingAttestations,
	}
	_ = effectiveMode // reserved for future per-mode tuning

	contract := map[string]any{
		"read_commands": []string{
			"fase work show <work-id>",
			"fase work notes <work-id>",
			"fase work hydrate <work-id>",
			"fase work list",
			"fase work ready",
			"fase project hydrate",
		},
		"write_commands": []string{
			"fase work create",
			"fase work update <work-id>",
			"fase work note-add <work-id>",
			"fase work attest <work-id>",
			"fase dispatch [work-id]",
		},
		"rules": []string{
			"Build: run 'make install' before running fase commands. Always use 'fase' (on PATH), never './fase'.",
			"CLI routes through fase serve — serve must be running for all commands.",
			"All persistent state belongs in the FASE work queue (notes, updates, conventions).",
			"Do not use Claude memory system — all state in FASE work queue.",
			"Do not create memory files, CLAUDE.md, or .claude hidden state files.",
			"One code-writer per environment, unlimited readers — plan/research/attest tasks can run concurrently.",
			"Host agent role: delegate and review, never write code directly.",
		},
		"available_adapters": []string{
			"native (zai/glm-5-turbo, zai/glm-5, zai/glm-4.7, zai/glm-4.7-flash, bedrock/claude-haiku-4-5, bedrock/claude-sonnet-4-6, bedrock/claude-opus-4-6, chatgpt/gpt-5.4, chatgpt/gpt-5.4-mini) — in-process Go adapter",
			"claude (claude-sonnet-4-6, claude-haiku-4-5) — Claude Code subprocess",
			"codex (gpt-5.4, gpt-5.4-mini) — Codex subprocess",
			"opencode (zai-coding-plan/glm-5-turbo) — OpenCode subprocess",
		},
		"model_capabilities": []string{
			"GLM models (glm-5-turbo, glm-5, glm-4.7, glm-4.7-flash): text-only, no multimodal. Cannot run Playwright or verify screenshots.",
			"Claude models (haiku, sonnet, opus): multimodal. Can run Playwright and verify visual output.",
			"GPT models (gpt-5.4, gpt-5.4-mini): multimodal. Can run Playwright and verify visual output.",
			"Native adapter: web search via Exa/Tavily/Brave/Serper (rate-limited, uses project API keys).",
			"External adapters (claude, codex): have their own built-in web search (no rate limits). Prefer external adapters for research-heavy tasks.",
		},
	}
	result["contract"] = contract
	if projectSpec != "" {
		result["project_spec"] = projectSpec
	}

	if mode == "supervisor" {
		result["supervisor_role"] = supervisorRolePrompt()
		result["dispatch_protocol"] = supervisorDispatchProtocol()
	}

	return result, nil
}

func RenderProjectHydrateMarkdown(r ProjectHydrateResult) string {
	var b strings.Builder

	b.WriteString("# Project Briefing\n\n")

	if gen, ok := r["generated_at"].(string); ok {
		fmt.Fprintf(&b, "Generated: %s\n", gen)
	}
	if mode, ok := r["mode"].(string); ok {
		fmt.Fprintf(&b, "Mode: %s\n\n", mode)
	}

	if conventions := toSlice(r["conventions"]); len(conventions) > 0 {
		b.WriteString("## Project Conventions\n\n")
		for _, c := range conventions {
			if entry, ok := c.(map[string]any); ok {
				if body, ok := entry["body"].(string); ok {
					for _, line := range strings.Split(body, "\n") {
						if strings.TrimSpace(line) == "" {
							continue
						}
						b.WriteString("- " + line + "\n")
					}
				}
			}
		}
		b.WriteString("\n")
	}

	if summary, ok := r["queue_summary"].(map[string]any); ok {
		b.WriteString("## Work Queue Summary\n\n")
		if total, ok := summary["total_items"].(int); ok {
			fmt.Fprintf(&b, "Total items: %d\n", total)
		}
		if counts, ok := summary["state_counts"].(map[any]any); ok {
			for k, v := range counts {
				fmt.Fprintf(&b, "  %v: %d\n", k, v)
			}
		}
		b.WriteString("\n")
	}

	renderWorkList := func(title string, key string) {
		items := toSlice(r[key])
		if len(items) == 0 {
			return
		}
		b.WriteString("## " + title + "\n\n")
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				wtitle := "(untitled)"
				if t, ok := m["title"].(string); ok {
					wtitle = t
				}
				id := ""
				if wid, ok := m["work_id"].(string); ok {
					id = wid
				}
				kind := ""
				if k, ok := m["kind"].(string); ok {
					kind = k
				}
				fmt.Fprintf(&b, "- **%s** `%s` [%s]", wtitle, id, kind)
				if claimed, ok := m["claimed_by"].(string); ok && claimed != "" {
					fmt.Fprintf(&b, " claimed by %s", claimed)
				}
				if pri, ok := m["priority"].(int); ok && pri != 0 {
					fmt.Fprintf(&b, " priority=%d", pri)
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	renderWorkList("Active Work", "active_work")
	renderWorkList("Ready Work", "ready_work")
	renderWorkList("Blocked Work", "blocked_work")
	renderWorkList("Recently Completed", "recent_completed")

	if atts := toSlice(r["pending_attestations"]); len(atts) > 0 {
		b.WriteString("## Pending Attestations\n\n")
		for _, a := range atts {
			if m, ok := a.(map[string]any); ok {
				wtitle := "(untitled)"
				if t, ok := m["title"].(string); ok {
					wtitle = t
				}
				if wid, ok := m["work_id"].(string); ok {
					fmt.Fprintf(&b, "- **%s** `%s`", wtitle, wid)
				}
				if ra, ok := m["required_attestations"].([]any); ok {
					fmt.Fprintf(&b, " requires: %v", ra)
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	if contract, ok := r["contract"].(map[string]any); ok {
		b.WriteString("## Contract\n\n")
		if cmds := toSlice(contract["read_commands"]); len(cmds) > 0 {
			b.WriteString("Read commands:\n")
			for _, c := range cmds {
				if s, ok := c.(string); ok {
					fmt.Fprintf(&b, "  - `%s`\n", s)
				}
			}
		}
		if cmds := toSlice(contract["write_commands"]); len(cmds) > 0 {
			b.WriteString("\nWrite commands:\n")
			for _, c := range cmds {
				if s, ok := c.(string); ok {
					fmt.Fprintf(&b, "  - `%s`\n", s)
				}
			}
		}
		if rules := toSlice(contract["rules"]); len(rules) > 0 {
			b.WriteString("\nRules:\n")
			for _, rule := range rules {
				if s, ok := rule.(string); ok {
					fmt.Fprintf(&b, "  - %s\n", s)
				}
			}
		}
		if adapters := toSlice(contract["available_adapters"]); len(adapters) > 0 {
			b.WriteString("\nAvailable adapters:\n")
			for _, a := range adapters {
				if s, ok := a.(string); ok {
					fmt.Fprintf(&b, "  - %s\n", s)
				}
			}
		}
		if caps := toSlice(contract["model_capabilities"]); len(caps) > 0 {
			b.WriteString("\nModel capabilities:\n")
			for _, c := range caps {
				if s, ok := c.(string); ok {
					fmt.Fprintf(&b, "  - %s\n", s)
				}
			}
		}
		b.WriteString("\n")
	}

	if spec, ok := r["project_spec"].(string); ok && spec != "" {
		b.WriteString("## Project Spec\n\n")
		b.WriteString(spec)
		b.WriteString("\n\n")
	}

	if role, ok := r["supervisor_role"].(string); ok {
		b.WriteString("## Supervisor Role\n\n")
		b.WriteString(role)
		b.WriteString("\n\n")
	}

	if proto, ok := r["dispatch_protocol"].(map[string]any); ok {
		renderProtoSection := func(title, key string) {
			if steps := toSlice(proto[key]); len(steps) > 0 {
				b.WriteString("### " + title + "\n\n")
				for _, step := range steps {
					if s, ok := step.(string); ok {
						b.WriteString(s + "\n")
					}
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("## Dispatch Protocol\n\n")
		renderProtoSection("Dispatch Flow", "dispatch_flow")
		renderProtoSection("Attestation Flow", "attestation_flow")
		renderProtoSection("Error Handling", "error_handling")
		renderProtoSection("Concurrency Rules", "concurrency_rules")
		renderProtoSection("Work Creation Rules", "work_creation_rules")
	}

	return b.String()
}

func supervisorRolePrompt() string {
	return `You are the FASE supervisor. Your job is to manage the work queue:
1. Dispatch ready work items to worker agents by choosing the right adapter and model.
2. Monitor worker progress via events.
3. Review and attest completed work.
4. Ensure one code-writer runs at a time per the FASE concurrency model.

You are NOT a worker — you never write code directly. You delegate to worker agents
via the dispatch system and review their output.`
}

func supervisorDispatchProtocol() map[string]any {
	return map[string]any{
		"dispatch_flow": []string{
			"1. Check ready_work for dispatchable items (highest priority first).",
			"2. Check active_work — if any item is in_progress or claimed, wait for it to complete.",
			"3. For the next ready item, select adapter+model based on preferred_adapters/preferred_models, or round-robin.",
			"4. Claim the work item (fase work claim <work-id>).",
			"5. Hydrate the worker briefing (fase work hydrate <work-id>).",
			"6. Dispatch: spawn a worker session on the chosen adapter with the briefing as prompt.",
			"7. Monitor events for completion or failure.",
		},
		"attestation_flow": []string{
			"1. When a work item reaches awaiting_attestation, review the worker's output.",
			"2. Check the diff, test results, and any findings noted by the worker.",
			"3. For web UI work: verify that e2e tests (Playwright) exist and pass. Fail attestation if no e2e tests — backend tests alone are insufficient.",
			"4. If the work meets the objective, attest it (fase work attest <work-id> --verdict approve).",
			"5. If the work needs revision, update it back to ready with feedback.",
		},
		"error_handling": []string{
			"If a worker fails, the item returns to ready state — it will be redispatched.",
			"If a worker stalls (no output for 10 minutes), housekeeping marks it failed.",
			"If an adapter is unavailable, try the next adapter in rotation.",
		},
		"concurrency_rules": []string{
			"One code-writing worker at a time per environment.",
			"Plan, research, and attest tasks can run concurrently.",
			"Use force dispatch only when you are certain there is no conflict.",
		},
		"work_creation_rules": []string{
			"When creating work items, include DETAILED objectives that a worker can execute independently.",
			"Title: concise but specific (e.g., 'Fix SSE streaming in AnthropicClient' not 'Fix bug').",
			"Objective: include (1) what to implement, (2) which files to create/modify, (3) acceptance criteria (tests to pass, build to be clean), (4) relevant context (ADR references, related work IDs).",
			"Always set kind (implement/plan/attest), priority, and preferred_adapters if the task needs a specific adapter.",
			"A worker reading only the objective should be able to complete the task without asking questions.",
			"Do NOT create throwaway/test work items. Only create real work that advances the project.",
		},
	}
}

func toSlice(v any) []any {
	if v == nil {
		return nil
	}
	if s, ok := v.([]any); ok {
		return s
	}
	val := reflect.ValueOf(v)
	if val.Kind() == reflect.Slice {
		result := make([]any, val.Len())
		for i := range val.Len() {
			result[i] = val.Index(i).Interface()
		}
		return result
	}
	return nil
}

func (s *Service) HydrateWork(ctx context.Context, req WorkHydrateRequest) (WorkHydrateResult, error) {
	if req.Debrief {
		return nil, fmt.Errorf("%w: debrief hydration is not implemented yet", ErrUnsupported)
	}
	briefing, err := s.CompileWorkerBriefing(ctx, req.WorkID, req.Mode)
	if err != nil {
		return nil, err
	}
	if claimant := firstNonEmpty(req.Claimant); claimant != "" {
		if runtimeSection, ok := briefing["runtime"].(map[string]any); ok {
			runtimeSection["claimant"] = claimant
		}
	}
	return briefing, nil
}

// ReconcileExpiredLeases releases work items whose lease has expired.
// Safe to call every supervisor cycle.
func (s *Service) ReconcileExpiredLeases(ctx context.Context) ([]string, error) {
	released, err := s.store.ReleaseExpiredWorkClaims(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconcile expired claims: %w", err)
	}
	now := time.Now().UTC()
	ids := make([]string, 0, len(released))
	for _, item := range released {
		ids = append(ids, item.WorkID)
		_ = s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
			UpdateID:       core.GenerateID("wup"),
			WorkID:         item.WorkID,
			ExecutionState: core.WorkExecutionStateReady,
			Message:        "Lease expired — released by reconciliation",
			CreatedBy:      "reconciler",
			CreatedAt:      now,
		})
	}
	return ids, nil
}

// ReconcileOnStartup does a full reset: expires leases, fails orphan jobs,
// and releases stale claims. Call ONLY on supervisor startup (cycle 1).
func (s *Service) ReconcileOnStartup(ctx context.Context) ([]string, error) {
	ids, err := s.ReconcileExpiredLeases(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()

	// Fail orphan jobs: any job still marked "running" has no live supervisor
	// watching it. The invariant is that all active runs must be tracked in the
	// supervisor's in-flight map. On startup, nothing is in-flight yet, so any
	// "running" job in the DB is orphaned. Mark them failed so their work items
	// can be retried.
	orphans, err := s.store.ListJobsFiltered(ctx, 200, "", string(core.JobStateRunning), "")
	if err == nil {
		for _, job := range orphans {
			job.State = core.JobStateFailed
			job.FinishedAt = &now
			job.UpdatedAt = now
			_ = s.store.UpdateJob(ctx, job)
			if job.WorkID != "" {
				_, _ = s.UpdateWork(ctx, WorkUpdateRequest{
					WorkID:         job.WorkID,
					ExecutionState: core.WorkExecutionStateFailed,
					Message:        fmt.Sprintf("orphan job %s failed during reconciliation", job.JobID),
					CreatedBy:      "reconciler",
				})
			}
			ids = append(ids, job.JobID)
		}
	}

	// Release stale claims: on startup, no work should be claimed or in_progress
	// since no supervisor is tracking anything yet. Reset them to ready.
	staleStates := []string{
		string(core.WorkExecutionStateClaimed),
		string(core.WorkExecutionStateInProgress),
	}
	for _, state := range staleStates {
		stale, listErr := s.store.ListWorkItems(ctx, 200, "", state, "", false)
		if listErr != nil {
			continue
		}
		for _, item := range stale {
			_, _ = s.UpdateWork(ctx, WorkUpdateRequest{
				WorkID:         item.WorkID,
				ExecutionState: core.WorkExecutionStateReady,
				Message:        fmt.Sprintf("stale %s state released during reconciliation", state),
				CreatedBy:      "reconciler",
			})
			ids = append(ids, item.WorkID)
		}
	}

	return ids, nil
}

func (s *Service) ListWork(ctx context.Context, req WorkListRequest) ([]core.WorkItemRecord, error) {
	return s.store.ListWorkItems(ctx, req.Limit, req.Kind, req.ExecutionState, req.ApprovalState, req.IncludeArchived)
}

func (s *Service) ReadyWork(ctx context.Context, limit int, includeArchived bool) ([]core.WorkItemRecord, error) {
	items, err := s.store.ListReadyWork(ctx, limit*4, includeArchived)
	if err != nil {
		return nil, err
	}
	// Filter out work items whose required_model_traits can't be satisfied by any
	// available catalog model. Work items with explicit preferred models/adapters
	// that are in the catalog are considered satisfiable regardless of trait tags.
	// Auto-syncs catalog if no snapshot exists so the filter is always applied.
	if snapshot, snapErr := s.catalogSnapshotOrSync(ctx); snapErr == nil {
		items = filterWorkByModelTraits(items, snapshot.Entries)
	}
	// ADR-0040: supervisor owns dispatch decisions. Runtime filtering
	// is no longer applied here — the supervisor scores and selects
	// adapters/models based on work item preferences and health.
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// filterWorkByModelTraits removes work items whose required_model_traits cannot
// be satisfied by any available catalog entry. Work items with preferred
// models or adapters available in the catalog are considered satisfiable even
// if the catalog entries lack explicit trait tags.
func filterWorkByModelTraits(items []core.WorkItemRecord, entries []core.CatalogEntry) []core.WorkItemRecord {
	if len(entries) == 0 {
		return items
	}
	availableTraits := map[string]struct{}{}
	availableModels := map[string]struct{}{}
	availableAdapters := map[string]struct{}{}
	for _, e := range entries {
		if !e.Available {
			continue
		}
		for _, t := range e.Traits {
			availableTraits[t] = struct{}{}
		}
		if e.Model != "" {
			availableModels[e.Model] = struct{}{}
		}
		if e.Adapter != "" {
			availableAdapters[e.Adapter] = struct{}{}
		}
	}
	var result []core.WorkItemRecord
	for _, item := range items {
		if canSatisfyModelTraits(item, availableTraits, availableModels, availableAdapters) {
			result = append(result, item)
		}
	}
	return result
}

func canSatisfyModelTraits(item core.WorkItemRecord, availableTraits, availableModels, availableAdapters map[string]struct{}) bool {
	if len(item.RequiredModelTraits) == 0 {
		// If the item specifies preferred adapters and none are available in the
		// catalog, exclude it — no available adapter can satisfy the request.
		if len(item.PreferredAdapters) > 0 {
			for _, a := range item.PreferredAdapters {
				if _, ok := availableAdapters[a]; ok {
					return true
				}
			}
			return false
		}
		return true
	}
	for _, m := range item.PreferredModels {
		if _, ok := availableModels[m]; ok {
			return true
		}
	}
	for _, a := range item.PreferredAdapters {
		if _, ok := availableAdapters[a]; ok {
			return true
		}
	}
	for _, t := range item.RequiredModelTraits {
		if _, ok := availableTraits[t]; !ok {
			return false
		}
	}
	return true
}

func (s *Service) ClaimWork(ctx context.Context, req WorkClaimRequest) (*core.WorkItemRecord, error) {
	workID := strings.TrimSpace(req.WorkID)
	claimant := strings.TrimSpace(req.Claimant)
	if workID == "" {
		return nil, fmt.Errorf("%w: work id must not be empty", ErrInvalidInput)
	}
	if claimant == "" {
		return nil, fmt.Errorf("%w: claimant must not be empty", ErrInvalidInput)
	}
	leaseDuration := req.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = 15 * time.Minute
	}
	before, fetchErr := s.store.GetWorkItem(ctx, workID)
	prevState := ""
	if fetchErr == nil {
		prevState = string(before.ExecutionState)
	}
	work, err := s.store.ClaimWorkItem(ctx, workID, claimant, time.Now().UTC().Add(leaseDuration))
	if err != nil {
		return nil, normalizeWorkClaimError(workID, err)
	}
	now := time.Now().UTC()
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:       core.GenerateID("wup"),
		WorkID:         work.WorkID,
		ExecutionState: work.ExecutionState,
		Message:        fmt.Sprintf("claimed by %s", claimant),
		Metadata: map[string]any{
			"claimed_by":    claimant,
			"claimed_until": timeStringPtr(work.ClaimedUntil),
			"lease_seconds": int(leaseDuration.Seconds()),
		},
		CreatedBy: claimant,
		CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	s.Events.Publish(WorkEvent{
		Kind:      WorkEventClaimed,
		WorkID:    work.WorkID,
		Title:     work.Title,
		State:     string(work.ExecutionState),
		PrevState: prevState,
		Actor:     actorFromClaimant(claimant),
		Cause:     CauseClaimChanged,
	})
	return &work, nil
}

func (s *Service) ClaimNextWork(ctx context.Context, req WorkClaimNextRequest) (*core.WorkItemRecord, error) {
	claimant := strings.TrimSpace(req.Claimant)
	if claimant == "" {
		return nil, fmt.Errorf("%w: claimant must not be empty", ErrInvalidInput)
	}
	leaseDuration := req.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = 15 * time.Minute
	}
	searchLimit := req.Limit
	if searchLimit <= 0 {
		searchLimit = 25
	}
	candidates, err := s.ReadyWork(ctx, searchLimit, false)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		work, claimErr := s.ClaimWork(ctx, WorkClaimRequest{
			WorkID:        candidate.WorkID,
			Claimant:      claimant,
			LeaseDuration: leaseDuration,
		})
		if claimErr == nil {
			return work, nil
		}
		if errors.Is(claimErr, ErrBusy) {
			continue
		}
		return nil, claimErr
	}
	return nil, fmt.Errorf("%w: no claimable work", ErrNotFound)
}

func (s *Service) ReleaseWork(ctx context.Context, req WorkReleaseRequest) (*core.WorkItemRecord, error) {
	workID := strings.TrimSpace(req.WorkID)
	claimant := strings.TrimSpace(req.Claimant)
	if workID == "" {
		return nil, fmt.Errorf("%w: work id must not be empty", ErrInvalidInput)
	}
	if claimant == "" {
		return nil, fmt.Errorf("%w: claimant must not be empty", ErrInvalidInput)
	}
	before, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	work, err := s.store.ReleaseWorkItemClaim(ctx, workID, claimant, req.Force)
	if err != nil {
		return nil, normalizeWorkClaimError(workID, err)
	}
	if before.ClaimedBy != "" {
		if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
			UpdateID:       core.GenerateID("wup"),
			WorkID:         work.WorkID,
			ExecutionState: work.ExecutionState,
			Message:        fmt.Sprintf("claim released by %s", claimant),
			Metadata: map[string]any{
				"previous_claimed_by":    before.ClaimedBy,
				"previous_claimed_until": timeStringPtr(before.ClaimedUntil),
			},
			CreatedBy: req.CreatedBy,
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
	}
	s.Events.Publish(WorkEvent{
		Kind:      WorkEventReleased,
		WorkID:    work.WorkID,
		Title:     work.Title,
		State:     string(work.ExecutionState),
		PrevState: string(before.ExecutionState),
		Actor:     actorFromClaimant(claimant),
		Cause:     CauseClaimChanged,
	})
	return &work, nil
}

func (s *Service) RenewWorkLease(ctx context.Context, req WorkRenewLeaseRequest) (*core.WorkItemRecord, error) {
	workID := strings.TrimSpace(req.WorkID)
	claimant := strings.TrimSpace(req.Claimant)
	if workID == "" {
		return nil, fmt.Errorf("%w: work id must not be empty", ErrInvalidInput)
	}
	if claimant == "" {
		return nil, fmt.Errorf("%w: claimant must not be empty", ErrInvalidInput)
	}
	leaseDuration := req.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = 15 * time.Minute
	}
	work, err := s.store.RenewWorkItemLease(ctx, workID, claimant, time.Now().UTC().Add(leaseDuration))
	if err != nil {
		if strings.Contains(err.Error(), "is not currently claimed") {
			return nil, fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
		}
		return nil, normalizeWorkClaimError(workID, err)
	}
	now := time.Now().UTC()
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:       core.GenerateID("wup"),
		WorkID:         work.WorkID,
		ExecutionState: work.ExecutionState,
		Message:        fmt.Sprintf("lease renewed by %s", claimant),
		Metadata: map[string]any{
			"claimed_by":    claimant,
			"claimed_until": timeStringPtr(work.ClaimedUntil),
			"lease_seconds": int(leaseDuration.Seconds()),
		},
		CreatedBy: claimant,
		CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	s.Events.Publish(WorkEvent{
		Kind:   WorkEventLeaseRenew,
		WorkID: work.WorkID,
		Title:  work.Title,
		State:  string(work.ExecutionState),
		Actor:  actorFromClaimant(claimant),
		Cause:  CauseLeaseReconcile,
	})
	return &work, nil
}

func (s *Service) UpdateWork(ctx context.Context, req WorkUpdateRequest) (*core.WorkItemRecord, error) {
	work, err := s.store.GetWorkItem(ctx, req.WorkID)
	if err != nil {
		return nil, normalizeStoreError("work", req.WorkID, err)
	}
	prevState := string(work.ExecutionState)
	now := time.Now().UTC()
	if req.ExecutionState != "" {
		if !req.ExecutionState.Valid() {
			return nil, fmt.Errorf("%w: invalid execution state %q", ErrInvalidInput, req.ExecutionState)
		}
		// Guard: cannot transition to done or archived via UpdateWork if
		// attestation is unresolved. Terminal-success transitions require
		// satisfied attestations; failed/cancelled are exempt.
		if req.ExecutionState == core.WorkExecutionStateDone || req.ExecutionState == core.WorkExecutionStateArchived {
			if req.ForceDone {
				emitForceDoneWarning(req.WorkID, req.CreatedBy)
			} else {
				if err := s.guardDoneTransition(ctx, work); err != nil {
					return nil, err
				}
			}
		}
		work.ExecutionState = req.ExecutionState
	}
	if req.ApprovalState != "" {
		work.ApprovalState = req.ApprovalState
	}
	if req.LockState != "" {
		work.LockState = req.LockState
	}
	if req.Phase != "" {
		work.Phase = req.Phase
	}
	if req.ExecutionState == core.WorkExecutionStateReady ||
		req.ExecutionState == core.WorkExecutionStateBlocked ||
		req.ExecutionState == core.WorkExecutionStateDone ||
		req.ExecutionState == core.WorkExecutionStateFailed ||
		req.ExecutionState == core.WorkExecutionStateCancelled ||
		req.ExecutionState == core.WorkExecutionStateArchived {
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
	}
	if req.JobID != "" {
		work.CurrentJobID = req.JobID
	}
	if req.SessionID != "" {
		work.CurrentSessionID = req.SessionID
	}
	if req.Metadata != nil {
		if work.Metadata == nil {
			work.Metadata = map[string]any{}
		}
		for k, v := range req.Metadata {
			work.Metadata[k] = v
		}
	}
	if req.LockState == core.WorkLockStateHumanLocked {
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
	}
	if req.ExecutionState == core.WorkExecutionStateDone && req.ApprovalState == "" && shouldSetPendingApproval(work) {
		work.ApprovalState = core.WorkApprovalStatePending
	}
	work.UpdatedAt = now
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, err
	}
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:       core.GenerateID("wup"),
		WorkID:         work.WorkID,
		ExecutionState: req.ExecutionState,
		ApprovalState:  req.ApprovalState,
		Phase:          req.Phase,
		Message:        req.Message,
		JobID:          req.JobID,
		SessionID:      req.SessionID,
		ArtifactID:     req.ArtifactID,
		Metadata:       cloneMap(req.Metadata),
		CreatedBy:      req.CreatedBy,
		CreatedAt:      now,
	}); err != nil {
		return nil, err
	}
	ev := WorkEvent{
		Kind:      WorkEventUpdated,
		WorkID:    work.WorkID,
		Title:     work.Title,
		State:     string(work.ExecutionState),
		PrevState: prevState,
		JobID:     work.CurrentJobID,
		Actor:     actorFromCreatedBy(req.CreatedBy),
	}
	if work.ExecutionState.Terminal() {
		ev.Cause = CauseWorkerTerminal
	} else {
		ev.Cause = CauseWorkerProgress
	}
	if req.Message != "" {
		ev.Metadata = map[string]string{"message": req.Message}
	}
	s.Events.Publish(ev)

	// Send email notification only on first transition to done.
	// Deduplicate: only send if previous state was NOT done.
	if string(work.ExecutionState) == "done" && prevState != "done" {
		s.sendWorkNotification(context.Background(), work, req.Message)
	}

	// Auto-dispatch checker when worker signals checking state.
	if req.ExecutionState == core.WorkExecutionStateChecking {
		go s.dispatchChecker(context.Background(), work)
	}

	return &work, nil
}

func (s *Service) SetWorkLock(ctx context.Context, workID string, lockState core.WorkLockState, createdBy, message string) (*core.WorkItemRecord, error) {
	if lockState == "" {
		return nil, fmt.Errorf("%w: lock state must not be empty", ErrInvalidInput)
	}
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	work.LockState = lockState
	if lockState == core.WorkLockStateHumanLocked {
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
	}
	work.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, err
	}
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:  core.GenerateID("wup"),
		WorkID:    work.WorkID,
		Message:   message,
		Metadata:  map[string]any{"lock_state": string(lockState)},
		CreatedBy: createdBy,
		CreatedAt: work.UpdatedAt,
	}); err != nil {
		return nil, err
	}
	return &work, nil
}

// checkerModels is the ordered pool of adapter+model pairs used for checker dispatch.
// Checkers intentionally use a different model from the worker to provide independent verification.
var checkerModels = []struct{ adapter, model string }{
	{"claude", "claude-opus-4-6"},
	{"claude", "claude-sonnet-4-6"},
	{"native", "bedrock/claude-sonnet-4-6"},
}

// dispatchChecker spawns a checker job for the given work item.
// It finds the worktree CWD from the work item's last job, picks a model
// different from the worker, and runs the checker briefing.
func (s *Service) dispatchChecker(ctx context.Context, work core.WorkItemRecord) {
	jobs, err := s.store.ListJobsByWork(ctx, work.WorkID, 5)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatchChecker: list jobs for %s: %v\n", work.WorkID, err)
		return
	}

	// Use the last job's CWD as the worktree path.
	// Fallback: repo root derived from state dir (strip ".fase" suffix).
	cwd := filepath.Dir(s.Paths.StateDir)
	workerAdapter := ""
	workerModel := ""
	if len(jobs) > 0 {
		lastJob := jobs[0]
		if lastJob.CWD != "" {
			cwd = lastJob.CWD
		}
		workerAdapter = lastJob.Adapter
		if m, ok := lastJob.Summary["model"].(string); ok {
			workerModel = m
		}
	}

	// Pick a checker adapter+model that differs from the worker's last model.
	checkerAdapter := checkerModels[0].adapter
	checkerModel := checkerModels[0].model
	for _, cm := range checkerModels {
		if cm.adapter != workerAdapter && cm.model != workerModel {
			checkerAdapter = cm.adapter
			checkerModel = cm.model
			break
		}
	}

	briefing := s.buildCheckerBriefing(work)

	_, runErr := s.Run(ctx, RunRequest{
		Adapter: checkerAdapter,
		CWD:     cwd,
		Prompt:  briefing,
		Model:   checkerModel,
		WorkID:  work.WorkID,
		Label:   "checker",
	})
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "dispatchChecker: run for %s: %v\n", work.WorkID, runErr)
	}
}

// buildCheckerBriefing produces a prompt for the checker agent.
func (s *Service) buildCheckerBriefing(work core.WorkItemRecord) string {
	return fmt.Sprintf(`# Checker Assignment

Work ID: %s
Title: %s

You are a checker. Your job is to verify the worker's output independently.

## Your Tasks

1. Run the test suite: go test ./...
2. Check that the build is clean: go build ./...
3. Review the diff: git diff main...HEAD --stat
4. Note any issues, warnings, or test failures

## Rules

- You are read-only. Do NOT modify code.
- Record your findings as work notes: fase work note-add %s --note-type finding --body "<your findings>"
- After completing your review, update the work state:
  - If tests pass and build is clean: fase work update %s --execution-state done --message "<summary>"
  - If there are failures: fase work update %s --execution-state failed --message "<what failed>"
- Do NOT call fase work attest.
- Do NOT create new work items.

## Work Objective

%s
`, work.WorkID, work.Title, work.WorkID, work.WorkID, work.WorkID, work.Objective)
}

func (s *Service) ApproveWork(ctx context.Context, workID, createdBy, message string) (*core.WorkItemRecord, error) {
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	attestations, err := s.store.ListAttestationRecords(ctx, "work", workID, 200)
	if err != nil {
		return nil, err
	}
	if !requiredAttestationsResolved(work, attestations) {
		return nil, fmt.Errorf("%w: blocking attestation policy unresolved", ErrInvalidInput)
	}
	previousApprovals, err := s.store.ListApprovalRecords(ctx, workID, 1)
	if err != nil {
		return nil, err
	}
	work.ApprovalState = core.WorkApprovalStateVerified
	work.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, err
	}
	approval := core.ApprovalRecord{
		ApprovalID:        core.GenerateID("approval"),
		WorkID:            work.WorkID,
		ApprovedCommitOID: work.HeadCommitOID,
		AttestationIDs:    attestationIDs(attestations),
		Status:            "approved",
		ApprovedBy:        createdBy,
		ApprovedAt:        work.UpdatedAt,
		Metadata:          map[string]any{"message": message},
	}
	if len(previousApprovals) > 0 {
		approval.SupersedesApprovalID = previousApprovals[0].ApprovalID
	}
	if err := s.store.CreateApprovalRecord(ctx, approval); err != nil {
		return nil, err
	}
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:      core.GenerateID("wup"),
		WorkID:        work.WorkID,
		ApprovalState: work.ApprovalState,
		Message:       message,
		CreatedBy:     createdBy,
		CreatedAt:     work.UpdatedAt,
		Metadata:      map[string]any{"approval_action": "approve"},
	}); err != nil {
		return nil, err
	}
	return &work, nil
}

func (s *Service) RejectWork(ctx context.Context, workID, createdBy, message string) (*core.WorkItemRecord, error) {
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	attestations, err := s.store.ListAttestationRecords(ctx, "work", workID, 200)
	if err != nil {
		return nil, err
	}
	previousApprovals, err := s.store.ListApprovalRecords(ctx, workID, 1)
	if err != nil {
		return nil, err
	}
	work.ApprovalState = core.WorkApprovalStateRejected
	work.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, err
	}
	approval := core.ApprovalRecord{
		ApprovalID:        core.GenerateID("approval"),
		WorkID:            work.WorkID,
		ApprovedCommitOID: work.HeadCommitOID,
		AttestationIDs:    attestationIDs(attestations),
		Status:            "rejected",
		ApprovedBy:        createdBy,
		ApprovedAt:        work.UpdatedAt,
		Metadata:          map[string]any{"message": message},
	}
	if len(previousApprovals) > 0 {
		approval.SupersedesApprovalID = previousApprovals[0].ApprovalID
	}
	if err := s.store.CreateApprovalRecord(ctx, approval); err != nil {
		return nil, err
	}
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:      core.GenerateID("wup"),
		WorkID:        work.WorkID,
		ApprovalState: work.ApprovalState,
		Message:       message,
		CreatedBy:     createdBy,
		CreatedAt:     work.UpdatedAt,
		Metadata:      map[string]any{"approval_action": "reject"},
	}); err != nil {
		return nil, err
	}
	return &work, nil
}

func (s *Service) PromoteWork(ctx context.Context, req WorkPromoteRequest) (*core.PromotionRecord, *core.WorkItemRecord, error) {
	work, err := s.store.GetWorkItem(ctx, req.WorkID)
	if err != nil {
		return nil, nil, normalizeStoreError("work", req.WorkID, err)
	}
	if work.ApprovalState != core.WorkApprovalStateVerified {
		return nil, nil, fmt.Errorf("%w: work must be approved before promotion", ErrInvalidInput)
	}
	approvals, err := s.store.ListApprovalRecords(ctx, req.WorkID, 1)
	if err != nil {
		return nil, nil, err
	}
	if len(approvals) == 0 || approvals[0].Status != "approved" {
		return nil, nil, fmt.Errorf("%w: missing approval record for promotion", ErrInvalidInput)
	}
	now := time.Now().UTC()
	promotion := core.PromotionRecord{
		PromotionID:       core.GenerateID("promote"),
		WorkID:            work.WorkID,
		ApprovalID:        approvals[0].ApprovalID,
		Environment:       strings.TrimSpace(req.Environment),
		PromotedCommitOID: work.HeadCommitOID,
		TargetRef:         strings.TrimSpace(req.TargetRef),
		Status:            "promoted",
		PromotedBy:        req.CreatedBy,
		PromotedAt:        now,
		Metadata:          map[string]any{"message": req.Message},
	}
	if promotion.Environment == "" {
		promotion.Environment = "staging"
	}
	if err := s.store.CreatePromotionRecord(ctx, promotion); err != nil {
		return nil, nil, err
	}
	if work.Metadata == nil {
		work.Metadata = map[string]any{}
	}
	work.Metadata["promoted"] = true
	work.Metadata["promoted_environment"] = promotion.Environment
	work.UpdatedAt = now
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, nil, err
	}
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:      core.GenerateID("wup"),
		WorkID:        work.WorkID,
		ApprovalState: work.ApprovalState,
		Message:       firstNonEmpty(req.Message, "promoted"),
		CreatedBy:     req.CreatedBy,
		CreatedAt:     now,
		Metadata: map[string]any{
			"promotion_id": promotion.PromotionID,
			"environment":  promotion.Environment,
			"target_ref":   promotion.TargetRef,
		},
	}); err != nil {
		return nil, nil, err
	}
	return &promotion, &work, nil
}

func (s *Service) AttestWork(ctx context.Context, req WorkAttestRequest) (*core.AttestationRecord, *core.WorkItemRecord, error) {
	work, err := s.store.GetWorkItem(ctx, req.WorkID)
	if err != nil {
		return nil, nil, normalizeStoreError("work", req.WorkID, err)
	}
	attestationTarget := work
	if strings.EqualFold(work.Kind, "attest") {
		parentID := ""
		if work.Metadata != nil {
			if rawParentID, ok := work.Metadata["parent_work_id"].(string); ok {
				parentID = strings.TrimSpace(rawParentID)
			}
		}
		if parentID != "" {
			if parent, parentErr := s.store.GetWorkItem(ctx, parentID); parentErr == nil {
				attestationTarget = parent
			}
		}
	}
	prevState := string(work.ExecutionState)
	// Nonce validation: if the work item has an attestation nonce,
	// the caller must provide it. The nonce is generated after the worker
	// exits, so workers cannot attest their own work.
	if storedNonce, ok := attestationTarget.Metadata["attestation_nonce"].(string); ok && storedNonce != "" {
		if req.Nonce == "" || req.Nonce != storedNonce {
			return nil, nil, fmt.Errorf("attestation nonce mismatch: work item requires valid nonce (generated post-completion)")
		}
	}
	now := time.Now().UTC()
	metadata := cloneMap(req.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	if attestationTarget.HeadCommitOID != "" {
		if _, ok := metadata["commit_oid"]; !ok {
			metadata["commit_oid"] = attestationTarget.HeadCommitOID
		}
	}

	unsatisfiedSlots := []core.RequiredAttestation(nil)
	if len(attestationTarget.RequiredAttestations) > 0 {
		attestations, fetchErr := s.store.ListAttestationRecords(ctx, "work", attestationTarget.WorkID, 200)
		if fetchErr != nil {
			return nil, nil, fetchErr
		}
		unsatisfiedSlots = unsatisfiedAttestationSlots(attestationTarget, attestations)
	}

	verifierKind := strings.TrimSpace(req.VerifierKind)
	method := strings.TrimSpace(req.Method)
	if len(unsatisfiedSlots) == 1 {
		if verifierKind == "" {
			verifierKind = strings.TrimSpace(unsatisfiedSlots[0].VerifierKind)
		}
		if method == "" {
			method = strings.TrimSpace(unsatisfiedSlots[0].Method)
		}
	}
	if verifierKind == "" || method == "" {
		return nil, nil, fmt.Errorf("%w: attestation requires non-empty verifier_kind and method; got verifier_kind=%q method=%q", ErrInvalidInput, verifierKind, method)
	}
	if len(unsatisfiedSlots) > 0 && !attestationSubmissionMatchesAnySlot(verifierKind, method, unsatisfiedSlots) {
		return nil, nil, fmt.Errorf("%w: attestation verifier_kind/method must match one unsatisfied required attestation slot; expected one of %s, got verifier_kind=%q method=%q", ErrInvalidInput, formatAttestationSlots(unsatisfiedSlots), verifierKind, method)
	}

	record := core.AttestationRecord{
		AttestationID:           core.GenerateID("attest"),
		SubjectKind:             "work",
		SubjectID:               attestationTarget.WorkID,
		Result:                  req.Result,
		Summary:                 req.Summary,
		ArtifactID:              req.ArtifactID,
		JobID:                   req.JobID,
		SessionID:               req.SessionID,
		Method:                  method,
		VerifierKind:            verifierKind,
		VerifierIdentity:        strings.TrimSpace(req.VerifierIdentity),
		Confidence:              req.Confidence,
		Blocking:                req.Blocking,
		SupersedesAttestationID: strings.TrimSpace(req.SupersedesAttestationID),
		SignerPubkey:            strings.TrimSpace(req.SignerPubkey),
		Metadata:                metadata,
		CreatedBy:               req.CreatedBy,
		CreatedAt:               now,
	}
	if record.VerifierKind == "" {
		record.VerifierKind = "manual"
	}
	if err := s.store.CreateAttestationRecord(ctx, record); err != nil {
		return nil, nil, err
	}

	children, childErr := s.store.ListWorkChildren(ctx, work.WorkID, 200)
	hasAttestationChildren := childErr == nil
	if hasAttestationChildren {
		hasAttestationChildren = false
		for _, child := range children {
			if child.Kind == "attest" {
				hasAttestationChildren = true
				break
			}
		}
	}

	// Attestation is transactional: recording the attestation also transitions
	// the work item's execution state. If this work item owns attestation child
	// work items, we keep the parent in awaiting_attestation until those children
	// complete; otherwise we preserve the legacy direct-attestation behavior.
	switch req.Result {
	case "passed":
		if !hasAttestationChildren {
			shouldSetDone := true
			if len(work.RequiredAttestations) > 0 {
				allAttestations, fetchErr := s.store.ListAttestationRecords(ctx, "work", req.WorkID, 200)
				if fetchErr == nil {
					shouldSetDone = requiredAttestationsResolved(work, allAttestations)
				}
			}
			if shouldSetDone {
				work.ExecutionState = core.WorkExecutionStateDone
				work.ClaimedBy = ""
				work.ClaimedUntil = nil
				if shouldSetPendingApproval(work) {
					work.ApprovalState = core.WorkApprovalStatePending
				}
			}
		} else if work.ExecutionState == core.WorkExecutionStateInProgress {
			work.ExecutionState = core.WorkExecutionStateAwaitingAttestation
		}
		if hasAttestationChildren {
			if err := s.refreshAttestationParentState(ctx, work.WorkID); err != nil {
				return nil, nil, err
			}
		}
	case "failed":
		work.ExecutionState = core.WorkExecutionStateFailed
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
		if hasAttestationChildren {
			if err := s.refreshAttestationParentState(ctx, work.WorkID); err != nil {
				return nil, nil, err
			}
		}
	}
	work.UpdatedAt = now
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, nil, err
	}

	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:       core.GenerateID("wup"),
		WorkID:         work.WorkID,
		ExecutionState: work.ExecutionState,
		ApprovalState:  work.ApprovalState,
		Message:        req.Summary,
		JobID:          req.JobID,
		SessionID:      req.SessionID,
		ArtifactID:     req.ArtifactID,
		CreatedBy:      req.CreatedBy,
		CreatedAt:      now,
		Metadata: map[string]any{
			"attestation_result":   req.Result,
			"attestation_method":   record.Method,
			"attestation_verifier": record.VerifierKind,
		},
	}); err != nil {
		return nil, nil, err
	}
	s.Events.Publish(WorkEvent{
		Kind:      WorkEventAttested,
		WorkID:    work.WorkID,
		Title:     work.Title,
		State:     string(work.ExecutionState),
		PrevState: prevState,
		Actor:     ActorService,
		Cause:     CauseAttestationRecorded,
		Metadata: map[string]string{
			"result":  req.Result,
			"summary": req.Summary,
		},
	})
	if strings.EqualFold(work.Kind, "attest") && attestationTarget.WorkID != work.WorkID {
		if err := s.refreshAttestationParentState(ctx, attestationTarget.WorkID); err != nil {
			return nil, nil, err
		}
	}
	return &record, &work, nil
}

// SignAttestationRecord updates an attestation record with a cryptographic signature.
func (s *Service) SignAttestationRecord(ctx context.Context, attestationID, signature string) error {
	return s.store.UpdateAttestationSignature(ctx, attestationID, signature)
}

// WorkCheck stores a check record and transitions the work state.
// Result "pass" moves to awaiting_attestation; "fail" returns to ready for rework.
func (s *Service) WorkCheck(ctx context.Context, req WorkCheckRequest) (*WorkCheckResult, error) {
	if req.WorkID == "" {
		return nil, fmt.Errorf("%w: work_id is required", ErrInvalidInput)
	}
	if req.Result != "pass" && req.Result != "fail" {
		return nil, fmt.Errorf("%w: result must be 'pass' or 'fail'", ErrInvalidInput)
	}

	work, err := s.store.GetWorkItem(ctx, req.WorkID)
	if err != nil {
		return nil, normalizeStoreError("work", req.WorkID, err)
	}

	// Truncate test output to 50KB
	const maxTestOutput = 50 * 1024
	if len(req.Report.TestOutput) > maxTestOutput {
		req.Report.TestOutput = req.Report.TestOutput[:maxTestOutput] + "\n[truncated]"
	}

	// Save artifacts to .fase/artifacts/<work-id>/
	artifactDir := filepath.Join(s.Paths.StateDir, "artifacts", req.WorkID)
	if err := os.MkdirAll(artifactDir, 0o755); err == nil {
		if req.Report.TestOutput != "" {
			_ = os.WriteFile(filepath.Join(artifactDir, "go-test-output.txt"), []byte(req.Report.TestOutput), 0o644)
		}
		if req.Report.DiffStat != "" {
			_ = os.WriteFile(filepath.Join(artifactDir, "diff-stat.txt"), []byte(req.Report.DiffStat), 0o644)
		}
		if req.Report.CheckerNotes != "" {
			_ = os.WriteFile(filepath.Join(artifactDir, "checker-notes.md"), []byte(req.Report.CheckerNotes), 0o644)
		}
	}

	now := time.Now().UTC()
	rec := core.CheckRecord{
		CheckID:      core.GenerateID("chk"),
		WorkID:       req.WorkID,
		CheckerModel: req.CheckerModel,
		WorkerModel:  req.WorkerModel,
		Result:       req.Result,
		Report:       req.Report,
		CreatedAt:    now,
	}
	if err := s.store.CreateCheckRecord(ctx, rec); err != nil {
		return nil, fmt.Errorf("store check record: %w", err)
	}

	// Transition work state
	var nextState core.WorkExecutionState
	var message string
	if req.Result == "pass" {
		nextState = core.WorkExecutionStateAwaitingAttestation
		message = fmt.Sprintf("check passed (tests: %d passed, %d failed) — awaiting attestation",
			req.Report.TestsPassed, req.Report.TestsFailed)
	} else {
		nextState = core.WorkExecutionStateReady
		notes := req.Report.CheckerNotes
		if notes == "" {
			notes = fmt.Sprintf("tests: %d passed, %d failed", req.Report.TestsPassed, req.Report.TestsFailed)
		}
		message = "check failed — returning to ready for rework: " + notes
	}

	updatedWork, err := s.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         req.WorkID,
		ExecutionState: nextState,
		Message:        message,
		CreatedBy:      firstNonEmpty(req.CreatedBy, "checker"),
	})
	if err != nil {
		return nil, fmt.Errorf("update work after check: %w", err)
	}
	_ = work

	return &WorkCheckResult{
		CheckRecord: rec,
		Work:        *updatedWork,
	}, nil
}

func (s *Service) AddWorkNote(ctx context.Context, req WorkNoteRequest) (*core.WorkNoteRecord, error) {
	if strings.TrimSpace(req.Body) == "" {
		return nil, fmt.Errorf("%w: note body must not be empty", ErrInvalidInput)
	}
	if _, err := s.store.GetWorkItem(ctx, req.WorkID); err != nil {
		return nil, normalizeStoreError("work", req.WorkID, err)
	}
	note := core.WorkNoteRecord{
		NoteID:    core.GenerateID("wnote"),
		WorkID:    req.WorkID,
		NoteType:  req.NoteType,
		Body:      req.Body,
		Metadata:  cloneMap(req.Metadata),
		CreatedBy: req.CreatedBy,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateWorkNote(ctx, note); err != nil {
		return nil, err
	}
	return &note, nil
}

func (s *Service) AddPrivateNote(ctx context.Context, workID, noteType, text, createdBy string) (*core.WorkNoteRecord, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: note text must not be empty", ErrInvalidInput)
	}
	if _, err := s.store.GetWorkItem(ctx, workID); err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	noteID := core.GenerateID("pnote")
	if err := s.store.AddPrivateNote(ctx, noteID, workID, noteType, text, createdBy); err != nil {
		return nil, err
	}
	return &core.WorkNoteRecord{
		NoteID:    noteID,
		WorkID:    workID,
		NoteType:  noteType,
		Body:      text,
		CreatedBy: createdBy,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (s *Service) ListPrivateNotes(ctx context.Context, workID string) ([]core.WorkNoteRecord, error) {
	return s.store.ListPrivateNotes(ctx, workID, 50)
}

// SetDocContent stores doc content and auto-creates a work item if workID is empty.
// This guarantees every doc has a corresponding work item.
func (s *Service) SetDocContent(ctx context.Context, workID, path, title, body, format string) (*core.DocContentRecord, string, error) {
	if format == "" {
		format = "markdown"
	}

	// Auto-create work item if none specified
	createdWork := false
	if workID == "" {
		// Check if a work item already exists for this doc path
		existing, err := s.store.GetDocContentByPath(ctx, path)
		if err == nil && existing != nil {
			workID = existing.WorkID
		} else {
			// Infer kind from path
			kind := "doc"
			if strings.Contains(path, "/decisions/") || strings.Contains(path, "adr-") {
				kind = "plan"
			} else if strings.Contains(path, "/guides/") {
				kind = "implement"
			} else if strings.Contains(path, "/reports/") || strings.Contains(path, "/snapshots/") {
				kind = "review"
			}

			// Infer title from content if not provided
			if title == "" {
				title = inferTitleFromMarkdown(body)
			}
			if title == "" {
				title = filepath.Base(path)
			}

			// Extract first paragraph as objective
			objective := path + ": " + extractFirstParagraph(body)

			work, err := s.CreateWork(ctx, WorkCreateRequest{
				Title:     title,
				Objective: objective,
				Kind:      kind,
			})
			if err != nil {
				return nil, "", fmt.Errorf("auto-create work item for doc: %w", err)
			}
			workID = work.WorkID
			createdWork = true
		}
	} else {
		if _, err := s.store.GetWorkItem(ctx, workID); err != nil {
			return nil, "", normalizeStoreError("work", workID, err)
		}
	}

	rec := core.DocContentRecord{
		DocID:  core.GenerateID("doc"),
		WorkID: workID,
		Path:   path,
		Title:  title,
		Body:   body,
		Format: format,
	}
	if err := s.store.UpsertDocContent(ctx, rec); err != nil {
		return nil, workID, err
	}
	_ = createdWork // could return this to caller
	return &rec, workID, nil
}

func inferTitleFromMarkdown(body string) string {
	for _, line := range strings.SplitN(body, "\n", 30) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
		if strings.HasPrefix(line, "## ") {
			return strings.TrimPrefix(line, "## ")
		}
	}
	return ""
}

func extractFirstParagraph(body string) string {
	lines := strings.Split(body, "\n")
	var para []string
	inContent := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "---" {
			if inContent && len(para) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "Date:") ||
			strings.HasPrefix(trimmed, "Kind:") || strings.HasPrefix(trimmed, "Status:") ||
			strings.HasPrefix(trimmed, "Priority:") || strings.HasPrefix(trimmed, "Requires:") {
			continue
		}
		inContent = true
		para = append(para, trimmed)
	}
	result := strings.Join(para, " ")
	if len(result) > 300 {
		result = result[:300] + "..."
	}
	return result
}

func (s *Service) GetDocContent(ctx context.Context, workID string) ([]core.DocContentRecord, error) {
	return s.store.GetDocContent(ctx, workID)
}

func (s *Service) DiscoverWork(ctx context.Context, sourceWorkID, title, objective, kind, rationale string) (*core.WorkProposalRecord, error) {
	if _, err := s.store.GetWorkItem(ctx, sourceWorkID); err != nil {
		return nil, normalizeStoreError("work", sourceWorkID, err)
	}
	if strings.TrimSpace(title) == "" || strings.TrimSpace(objective) == "" {
		return nil, fmt.Errorf("%w: title and objective must not be empty", ErrInvalidInput)
	}
	if strings.TrimSpace(kind) == "" {
		kind = "task"
	}
	proposal := core.WorkProposalRecord{
		ProposalID:   core.GenerateID("wprop"),
		ProposalType: "promote_discovery",
		State:        "proposed",
		SourceWorkID: sourceWorkID,
		Rationale:    strings.TrimSpace(rationale),
		ProposedPatch: map[string]any{
			"title":     strings.TrimSpace(title),
			"objective": strings.TrimSpace(objective),
			"kind":      strings.TrimSpace(kind),
		},
		Metadata:  map[string]any{"discovered": true},
		CreatedBy: "service",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateWorkProposal(ctx, proposal); err != nil {
		return nil, err
	}
	return &proposal, nil
}

func (s *Service) CreateWorkProposal(ctx context.Context, req WorkProposalCreateRequest) (*core.WorkProposalRecord, error) {
	proposalType := strings.TrimSpace(req.ProposalType)
	if proposalType == "" {
		return nil, fmt.Errorf("%w: proposal type must not be empty", ErrInvalidInput)
	}
	if req.TargetWorkID != "" {
		if _, err := s.store.GetWorkItem(ctx, req.TargetWorkID); err != nil {
			return nil, normalizeStoreError("work", req.TargetWorkID, err)
		}
	}
	if req.SourceWorkID != "" {
		if _, err := s.store.GetWorkItem(ctx, req.SourceWorkID); err != nil {
			return nil, normalizeStoreError("work", req.SourceWorkID, err)
		}
	}
	proposal := core.WorkProposalRecord{
		ProposalID:    core.GenerateID("wprop"),
		ProposalType:  proposalType,
		State:         "proposed",
		TargetWorkID:  req.TargetWorkID,
		SourceWorkID:  req.SourceWorkID,
		Rationale:     strings.TrimSpace(req.Rationale),
		ProposedPatch: cloneMap(req.Patch),
		Metadata:      cloneMap(req.Metadata),
		CreatedBy:     req.CreatedBy,
		CreatedAt:     time.Now().UTC(),
	}
	if err := s.store.CreateWorkProposal(ctx, proposal); err != nil {
		return nil, err
	}
	return &proposal, nil
}

func (s *Service) ListWorkProposals(ctx context.Context, req WorkProposalListRequest) ([]core.WorkProposalRecord, error) {
	return s.store.ListWorkProposals(ctx, req.Limit, req.State, req.TargetWorkID, req.SourceWorkID)
}

func (s *Service) GetWorkProposal(ctx context.Context, proposalID string) (*core.WorkProposalRecord, error) {
	proposal, err := s.store.GetWorkProposal(ctx, proposalID)
	if err != nil {
		return nil, normalizeStoreError("proposal", proposalID, err)
	}
	return &proposal, nil
}

func (s *Service) ReviewWorkProposal(ctx context.Context, proposalID, decision string) (*core.WorkProposalRecord, *core.WorkItemRecord, error) {
	proposal, err := s.store.GetWorkProposal(ctx, proposalID)
	if err != nil {
		return nil, nil, normalizeStoreError("proposal", proposalID, err)
	}
	now := time.Now().UTC()
	switch decision {
	case "accept":
		proposal.State = "accepted"
		proposal.ReviewedBy = "service"
		proposal.ReviewedAt = &now
		var created *core.WorkItemRecord
		switch proposal.ProposalType {
		case "promote_discovery":
			item, err := s.createWorkFromPatch(ctx, proposal, now)
			if err != nil {
				return nil, nil, err
			}
			if proposal.SourceWorkID != "" {
				if err := s.store.CreateWorkEdge(ctx, core.WorkEdgeRecord{
					EdgeID:     core.GenerateID("wedge"),
					FromWorkID: item.WorkID,
					ToWorkID:   proposal.SourceWorkID,
					EdgeType:   "discovered_from",
					CreatedBy:  "service",
					CreatedAt:  now,
					Metadata:   map[string]any{},
				}); err != nil {
					return nil, nil, err
				}
			}
			proposal.TargetWorkID = item.WorkID
			created = item
		case "create_child":
			parentID := proposal.TargetWorkID
			if parentID == "" {
				parentID = proposal.SourceWorkID
			}
			if parentID == "" {
				return nil, nil, fmt.Errorf("%w: create_child requires target or source work id", ErrInvalidInput)
			}
			item, err := s.createWorkFromPatch(ctx, proposal, now)
			if err != nil {
				return nil, nil, err
			}
			if err := s.attachParentEdge(ctx, parentID, item.WorkID, "service", now, map[string]any{}, false); err != nil {
				return nil, nil, err
			}
			proposal.TargetWorkID = item.WorkID
			created = item
		case "add_edge":
			if err := s.applyAddEdgeProposal(ctx, proposal, now); err != nil {
				return nil, nil, err
			}
		case "remove_edge":
			if err := s.applyRemoveEdgeProposal(ctx, proposal); err != nil {
				return nil, nil, err
			}
		case "change_acceptance":
			if err := s.applyChangeAcceptanceProposal(ctx, proposal, now); err != nil {
				return nil, nil, err
			}
		case "reparent_work":
			if err := s.applyReparentProposal(ctx, proposal, now); err != nil {
				return nil, nil, err
			}
		case "supersede_work":
			item, err := s.applySupersedeProposal(ctx, proposal, now)
			if err != nil {
				return nil, nil, err
			}
			created = item
		}
		if err := s.store.UpdateWorkProposal(ctx, proposal); err != nil {
			return nil, nil, err
		}
		return &proposal, created, nil
	case "reject":
		proposal.State = "rejected"
		proposal.ReviewedBy = "service"
		proposal.ReviewedAt = &now
		if err := s.store.UpdateWorkProposal(ctx, proposal); err != nil {
			return nil, nil, err
		}
		return &proposal, nil, nil
	default:
		return nil, nil, fmt.Errorf("%w: decision must be accept or reject", ErrInvalidInput)
	}
}

func (s *Service) createWorkFromPatch(ctx context.Context, proposal core.WorkProposalRecord, now time.Time) (*core.WorkItemRecord, error) {
	title := summaryString(proposal.ProposedPatch, "title")
	objective := summaryString(proposal.ProposedPatch, "objective")
	kind := summaryString(proposal.ProposedPatch, "kind")
	if kind == "" {
		kind = "task"
	}
	if title == "" || objective == "" {
		return nil, fmt.Errorf("%w: proposal patch requires title and objective", ErrInvalidInput)
	}
	item := core.WorkItemRecord{
		WorkID:             core.GenerateID("work"),
		Title:              title,
		Objective:          objective,
		Kind:               kind,
		ExecutionState:     core.WorkExecutionStateReady,
		ApprovalState:      core.WorkApprovalStateNone,
		LockState:          core.WorkLockStateUnlocked,
		ConfigurationClass: summaryString(proposal.ProposedPatch, "configuration_class"),
		BudgetClass:        summaryString(proposal.ProposedPatch, "budget_class"),
		Metadata:           map[string]any{"proposal_id": proposal.ProposalID},
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	item.RequiredAttestations = defaultRequiredAttestations(item, nil, s.Config)
	if err := s.store.CreateWorkItem(ctx, item); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Service) applyAddEdgeProposal(ctx context.Context, proposal core.WorkProposalRecord, now time.Time) error {
	fromWorkID := summaryString(proposal.ProposedPatch, "from_work_id")
	toWorkID := summaryString(proposal.ProposedPatch, "to_work_id")
	edgeType := summaryString(proposal.ProposedPatch, "edge_type")
	if fromWorkID == "" || toWorkID == "" || edgeType == "" {
		return fmt.Errorf("%w: add_edge requires from_work_id, to_work_id, and edge_type", ErrInvalidInput)
	}
	if _, err := s.store.GetWorkItem(ctx, fromWorkID); err != nil {
		return normalizeStoreError("work", fromWorkID, err)
	}
	if _, err := s.store.GetWorkItem(ctx, toWorkID); err != nil {
		return normalizeStoreError("work", toWorkID, err)
	}
	if edgeType == "parent_of" {
		return s.attachParentEdge(ctx, fromWorkID, toWorkID, "service", now, cloneMap(proposal.Metadata), false)
	}
	return s.store.CreateWorkEdge(ctx, core.WorkEdgeRecord{
		EdgeID:     core.GenerateID("wedge"),
		FromWorkID: fromWorkID,
		ToWorkID:   toWorkID,
		EdgeType:   edgeType,
		CreatedBy:  "service",
		CreatedAt:  now,
		Metadata:   cloneMap(proposal.Metadata),
	})
}

func (s *Service) applyRemoveEdgeProposal(ctx context.Context, proposal core.WorkProposalRecord) error {
	edgeID := summaryString(proposal.ProposedPatch, "edge_id")
	if edgeID != "" {
		return s.store.DeleteWorkEdge(ctx, edgeID)
	}
	fromWorkID := summaryString(proposal.ProposedPatch, "from_work_id")
	toWorkID := summaryString(proposal.ProposedPatch, "to_work_id")
	edgeType := summaryString(proposal.ProposedPatch, "edge_type")
	edges, err := s.store.ListWorkEdges(ctx, 100, edgeType, fromWorkID, toWorkID)
	if err != nil {
		return err
	}
	if len(edges) == 0 {
		return fmt.Errorf("%w: no matching edge found", ErrNotFound)
	}
	for _, edge := range edges {
		if err := s.store.DeleteWorkEdge(ctx, edge.EdgeID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyChangeAcceptanceProposal(ctx context.Context, proposal core.WorkProposalRecord, now time.Time) error {
	targetID := proposal.TargetWorkID
	if targetID == "" {
		return fmt.Errorf("%w: change_acceptance requires target work id", ErrInvalidInput)
	}
	work, err := s.store.GetWorkItem(ctx, targetID)
	if err != nil {
		return normalizeStoreError("work", targetID, err)
	}
	for key, value := range proposal.ProposedPatch {
		work.Acceptance[key] = value
	}
	work.UpdatedAt = now
	return s.store.UpdateWorkItem(ctx, work)
}

func (s *Service) applyReparentProposal(ctx context.Context, proposal core.WorkProposalRecord, now time.Time) error {
	targetID := proposal.TargetWorkID
	newParentID := summaryString(proposal.ProposedPatch, "parent_work_id")
	if targetID == "" || newParentID == "" {
		return fmt.Errorf("%w: reparent_work requires target work id and parent_work_id", ErrInvalidInput)
	}
	if _, err := s.store.GetWorkItem(ctx, targetID); err != nil {
		return normalizeStoreError("work", targetID, err)
	}
	if _, err := s.store.GetWorkItem(ctx, newParentID); err != nil {
		return normalizeStoreError("work", newParentID, err)
	}
	if err := s.validateParentEdge(ctx, newParentID, targetID, true); err != nil {
		return err
	}
	existing, err := s.store.ListWorkEdges(ctx, 100, "parent_of", "", targetID)
	if err != nil {
		return err
	}
	for _, edge := range existing {
		if err := s.store.DeleteWorkEdge(ctx, edge.EdgeID); err != nil {
			return err
		}
	}
	return s.attachParentEdge(ctx, newParentID, targetID, "service", now, map[string]any{}, true)
}

func (s *Service) applySupersedeProposal(ctx context.Context, proposal core.WorkProposalRecord, now time.Time) (*core.WorkItemRecord, error) {
	targetID := proposal.TargetWorkID
	if targetID == "" {
		return nil, fmt.Errorf("%w: supersede_work requires target work id", ErrInvalidInput)
	}
	if _, err := s.store.GetWorkItem(ctx, targetID); err != nil {
		return nil, normalizeStoreError("work", targetID, err)
	}
	replacementID := summaryString(proposal.ProposedPatch, "existing_work_id")
	var created *core.WorkItemRecord
	if replacementID == "" {
		item, err := s.createWorkFromPatch(ctx, proposal, now)
		if err != nil {
			return nil, err
		}
		created = item
		replacementID = item.WorkID
	} else {
		if _, err := s.store.GetWorkItem(ctx, replacementID); err != nil {
			return nil, normalizeStoreError("work", replacementID, err)
		}
	}
	if err := s.store.CreateWorkEdge(ctx, core.WorkEdgeRecord{
		EdgeID:     core.GenerateID("wedge"),
		FromWorkID: replacementID,
		ToWorkID:   targetID,
		EdgeType:   "supersedes",
		CreatedBy:  "service",
		CreatedAt:  now,
		Metadata:   map[string]any{},
	}); err != nil {
		return nil, err
	}
	return created, nil
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

	jobByID := make(map[string]core.JobRecord, len(jobs))
	var filteredJobs []core.JobRecord
	for _, job := range jobs {
		if !historyJobMatches(job, req) {
			continue
		}
		jobByID[job.JobID] = job
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
		matches = append(matches, core.HistoryMatch{
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
		})
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
			matches = append(matches, core.HistoryMatch{
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
			})
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
			matches = append(matches, core.HistoryMatch{
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
			})
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
			matches = append(matches, core.HistoryMatch{
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
			})
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

func (s *Service) cancelReleaseWorkClaim(ctx context.Context, jobID, workID string) {
	if workID == "" {
		return
	}
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return
	}
	if work.ClaimedBy == "" && work.ClaimedUntil == nil {
		return
	}
	now := time.Now().UTC()
	leaseExpired := work.ClaimedUntil != nil && !work.ClaimedUntil.After(now)
	workState := core.WorkExecutionStateFailed
	if leaseExpired || work.ClaimedBy == "" {
		workState = core.WorkExecutionStateReady
	}
	_, _ = s.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         workID,
		ExecutionState: workState,
		Message:        fmt.Sprintf("cancelled: job %s", jobID),
		CreatedBy:      "cancel",
	})
}

func (s *Service) Cancel(ctx context.Context, jobID string) (*core.JobRecord, error) {
	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, normalizeStoreError("job", jobID, err)
	}

	if job.State.Terminal() {
		s.cancelReleaseWorkClaim(ctx, jobID, job.WorkID)
		return &job, nil
	}

	now := time.Now().UTC()
	if err := s.upsertJobRuntime(ctx, job.JobID, func(rec *core.JobRuntimeRecord) {
		rec.CancelRequestedAt = &now
	}); err != nil {
		return nil, err
	}

	var runtimeRec *core.JobRuntimeRecord
	waitForRuntimeUntil := time.Now().Add(5 * time.Second)
	for runtimeRec == nil && time.Now().Before(waitForRuntimeUntil) {
		rec, runtimeErr := s.store.GetJobRuntime(ctx, job.JobID)
		if runtimeErr == nil {
			runtimeRec = &rec
			break
		}
		if runtimeErr != nil && !errors.Is(runtimeErr, store.ErrNotFound) {
			return nil, runtimeErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	signals := []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL}
	delays := []time.Duration{1500 * time.Millisecond, 1500 * time.Millisecond, 1500 * time.Millisecond}
	for idx, sig := range signals {
		if runtimeRec == nil {
			rec, runtimeErr := s.store.GetJobRuntime(ctx, job.JobID)
			if runtimeErr == nil {
				runtimeRec = &rec
			} else if runtimeErr != nil && !errors.Is(runtimeErr, store.ErrNotFound) {
				return nil, runtimeErr
			}
		}
		if runtimeRec != nil {
			if runtimeRec.VendorPID != 0 {
				_ = signalProcessGroup(runtimeRec.VendorPID, sig)
			} else if runtimeRec.SupervisorPID != 0 {
				_ = signalProcessGroup(runtimeRec.SupervisorPID, sig)
			}
		}
		waitUntil := time.Now().Add(delays[idx])
		for time.Now().Before(waitUntil) {
			current, err := s.store.GetJob(ctx, jobID)
			if err == nil && current.State.Terminal() {
				s.cancelReleaseWorkClaim(ctx, jobID, current.WorkID)
				return &current, nil
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	current, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}

	s.cancelReleaseWorkClaim(ctx, jobID, current.WorkID)

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
	var translatedError string
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
			if hint.Kind == "diagnostic" && translatedError == "" {
				translatedError = diagnosticMessage(hint.Payload)
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
	if translatedError != "" && lastAssistant == "" {
		return translatedError, fmt.Errorf("%w: %s", ErrVendorProcess, translatedError)
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = s.detachedWorkerEnv(exePath)
	adapterapi.PrepareCommand(cmd)

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start detached worker: %w", err)
	}

	return cmd.Process.Pid, nil
}

func (s *Service) detachedWorkerEnv(exePath string) []string {
	envMap := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		envMap[key] = value
	}

	envMap["FASE_EXECUTABLE"] = exePath
	if s.ConfigPath != "" {
		envMap["FASE_CONFIG_DIR"] = filepath.Dir(s.ConfigPath)
	}
	if s.Paths.StateDir != "" {
		envMap["FASE_STATE_DIR"] = s.Paths.StateDir
	}
	if s.Paths.CacheDir != "" {
		envMap["FASE_CACHE_DIR"] = s.Paths.CacheDir
	}
	if exeDir := filepath.Dir(exePath); exeDir != "" {
		if pathValue, ok := envMap["PATH"]; ok && pathValue != "" {
			envMap["PATH"] = exeDir + string(os.PathListSeparator) + pathValue
		} else {
			envMap["PATH"] = exeDir
		}
	}

	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+envMap[key])
	}
	return env
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
		WorkID:          req.WorkID,
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
	if req.WorkID != "" {
		if err := s.markWorkQueued(ctx, req.WorkID, job, session); err != nil {
			return nil, err
		}
	}

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
	path, err := osExecutable()
	if err == nil && path != "" {
		lower := strings.ToLower(path)
		if !strings.HasSuffix(lower, ".test") && !strings.Contains(lower, string(filepath.Separator)+"go-build"+string(filepath.Separator)) {
			return path, nil
		}
	}
	if explicit := os.Getenv("FASE_EXECUTABLE"); explicit != "" {
		return explicit, nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve fase executable: %w", err)
	}
	return path, nil
}

// nativeServiceInjector is implemented by adapters that need a service reference.
type nativeServiceInjector interface {
	SetService(svc any)
}

func (s *Service) resolveAdapter(ctx context.Context, name string) (adapterapi.Adapter, adapters.Diagnosis, error) {
	adapter, descriptor, ok := adapters.Resolve(ctx, s.Config, name)
	if ok {
		// Inject service into adapters that need it (native adapter for FASE tools).
		if injector, ok := adapter.(nativeServiceInjector); ok {
			injector.SetService(s)
		}
	}
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

func cloneSlice(src []string) []string {
	if len(src) == 0 {
		return []string{}
	}
	dst := make([]string, len(src))
	copy(dst, src)
	return dst
}

func cloneRequiredAttestations(src []core.RequiredAttestation) []core.RequiredAttestation {
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

func defaultRequiredAttestations(work core.WorkItemRecord, explicit []core.RequiredAttestation, _ core.Config) []core.RequiredAttestation {
	if len(explicit) > 0 {
		return cloneRequiredAttestations(explicit)
	}
	if strings.EqualFold(work.Kind, "attest") {
		return []core.RequiredAttestation{}
	}
	return []core.RequiredAttestation{
		{
			VerifierKind: "attestation",
			Method:       "automated_review",
			Blocking:     true,
		},
	}
}

func (s *Service) spawnAttestationChildren(ctx context.Context, parent core.WorkItemRecord, job core.JobRecord) error {
	if strings.EqualFold(parent.Kind, "attest") {
		return nil
	}

	slots := defaultRequiredAttestations(parent, parent.RequiredAttestations, s.Config)
	if len(slots) == 0 {
		return nil
	}

	workDetail, err := s.Work(ctx, parent.WorkID)
	if err != nil {
		return err
	}

	nonce := ""
	if parent.Metadata != nil {
		if existing, ok := parent.Metadata["attestation_nonce"].(string); ok {
			nonce = strings.TrimSpace(existing)
		}
	}
	if nonce == "" {
		nonce = core.GenerateID("nonce")
	}

	now := time.Now().UTC()
	if parent.Metadata == nil {
		parent.Metadata = map[string]any{}
	}
	parent.Metadata["attestation_nonce"] = nonce
	if parent.AttestationFrozenAt == nil {
		parent.AttestationFrozenAt = &now
	}
	parent.ExecutionState = core.WorkExecutionStateAwaitingAttestation
	parent.ClaimedBy = ""
	parent.ClaimedUntil = nil
	parent.UpdatedAt = now
	if err := s.store.UpdateWorkItem(ctx, parent); err != nil {
		return err
	}

	existing := make(map[int]struct{}, len(workDetail.Children))
	for _, child := range workDetail.Children {
		if !strings.EqualFold(child.Kind, "attest") {
			continue
		}
		childNonce := ""
		if child.Metadata != nil {
			childNonce, _ = child.Metadata["attestation_nonce"].(string)
		}
		if strings.TrimSpace(childNonce) != nonce {
			continue
		}
		if slotIdx, ok := metadataInt(child.Metadata, "slot_index"); ok {
			existing[slotIdx] = struct{}{}
		}
	}

	workerFindings := attestationWorkerFindings(workDetail)
	workerModel := summaryString(job.Summary, "model")

	for slotIdx, slot := range slots {
		if _, ok := existing[slotIdx]; ok {
			continue
		}

		childAdapter, childModel := s.attestationChildRuntime(parent, job.Adapter, slotIdx)
		child := core.WorkItemRecord{
			WorkID:               core.GenerateID("work"),
			Title:                attestationChildTitle(parent, slotIdx, slot),
			Objective:            attestationChildObjective(parent, job, slotIdx, slot, nonce, workerFindings),
			Kind:                 "attest",
			ExecutionState:       core.WorkExecutionStateReady,
			ApprovalState:        core.WorkApprovalStateNone,
			LockState:            core.WorkLockStateUnlocked,
			Priority:             parent.Priority,
			ConfigurationClass:   parent.ConfigurationClass,
			BudgetClass:          parent.BudgetClass,
			PreferredAdapters:    childAdapter,
			PreferredModels:      nonEmptySlice(childModel),
			RequiredAttestations: []core.RequiredAttestation{},
			Metadata: map[string]any{
				"parent_work_id":    parent.WorkID,
				"slot_index":        slotIdx,
				"attestation_nonce": nonce,
				"worker_job_id":     job.JobID,
				"worker_adapter":    job.Adapter,
				"worker_model":      workerModel,
				"blocking":          slot.Blocking,
			},
			CreatedAt: now,
			UpdatedAt: now,
		}
		created, createErr := s.CreateWork(ctx, WorkCreateRequest{
			Title:                child.Title,
			Objective:            child.Objective,
			Kind:                 child.Kind,
			LockState:            child.LockState,
			Priority:             child.Priority,
			ConfigurationClass:   child.ConfigurationClass,
			BudgetClass:          child.BudgetClass,
			PreferredAdapters:    child.PreferredAdapters,
			PreferredModels:      child.PreferredModels,
			RequiredAttestations: child.RequiredAttestations,
			Metadata:             child.Metadata,
		})
		if createErr != nil {
			_, _ = s.UpdateWork(ctx, WorkUpdateRequest{
				WorkID:         parent.WorkID,
				ExecutionState: core.WorkExecutionStateFailed,
				Message:        fmt.Sprintf("attestation child creation failed for slot %d: %v", slotIdx, createErr),
				CreatedBy:      "service",
			})
			return createErr
		}
		if err := s.attachParentEdge(ctx, parent.WorkID, created.WorkID, "service", now, map[string]any{
			"edge_kind":  "attestation",
			"slot_index": slotIdx,
		}, false); err != nil {
			_, _ = s.UpdateWork(ctx, WorkUpdateRequest{
				WorkID:         parent.WorkID,
				ExecutionState: core.WorkExecutionStateFailed,
				Message:        fmt.Sprintf("attestation edge creation failed for slot %d: %v", slotIdx, err),
				CreatedBy:      "service",
			})
			return err
		}
		if err := s.store.CreateWorkEdge(ctx, core.WorkEdgeRecord{
			EdgeID:     core.GenerateID("wedge"),
			FromWorkID: parent.WorkID,
			ToWorkID:   created.WorkID,
			EdgeType:   "depends_on",
			CreatedBy:  "service",
			CreatedAt:  now,
			Metadata: map[string]any{
				"slot_index":        slotIdx,
				"attestation_nonce": nonce,
			},
		}); err != nil {
			_, _ = s.UpdateWork(ctx, WorkUpdateRequest{
				WorkID:         parent.WorkID,
				ExecutionState: core.WorkExecutionStateFailed,
				Message:        fmt.Sprintf("attestation dependency creation failed for slot %d: %v", slotIdx, err),
				CreatedBy:      "service",
			})
			return err
		}
	}

	_, _ = s.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         parent.WorkID,
		ExecutionState: core.WorkExecutionStateAwaitingAttestation,
		Message:        fmt.Sprintf("spawned %d attestation child work item(s)", len(slots)),
		CreatedBy:      "service",
		Metadata: map[string]any{
			"attestation_nonce": nonce,
		},
	})

	return nil
}

func (s *Service) attestationChildRuntime(parent core.WorkItemRecord, workerAdapter string, slotIndex int) ([]string, string) {
	adapters := attestationPreferredAdapters(s.Config, parent, slotIndex)
	if len(adapters) > 0 {
		return adapters[:1], ""
	}
	if adapter := alternateAdapter(workerAdapter, s.Config); adapter != "" {
		return []string{adapter}, ""
	}
	return []string{}, ""
}

func alternateAdapter(workerAdapter string, cfg core.Config) string {
	workerAdapter = strings.TrimSpace(workerAdapter)
	for _, candidate := range []string{"claude", "codex", "factory", "gemini", "native", "opencode", "pi"} {
		if candidate == workerAdapter {
			continue
		}
		if adapterCfg, ok := cfg.Adapters.ByName(candidate); ok && adapterCfg.Enabled {
			return candidate
		}
	}
	return ""
}

func attestationPreferredAdapters(cfg core.Config, work core.WorkItemRecord, slotIndex int) []string {
	if len(cfg.Rotation.Entries) == 0 {
		return nil
	}
	matches := make([]string, 0, len(cfg.Rotation.Entries))
	for _, entry := range cfg.Rotation.Entries {
		if entryMatchesWorkRole(entry, work) {
			matches = append(matches, entry.Adapter)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	return []string{matches[slotIndex%len(matches)]}
}

func entryMatchesWorkRole(entry core.RotationEntry, work core.WorkItemRecord) bool {
	if len(entry.Roles) == 0 {
		return true
	}
	kind := strings.ToLower(strings.TrimSpace(work.Kind))
	class := strings.ToLower(strings.TrimSpace(work.ConfigurationClass))
	for _, role := range entry.Roles {
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "*" || role == kind || (class != "" && role == class) {
			return true
		}
	}
	return false
}

func attestationWorkerFindings(workDetail *WorkShowResult) string {
	if workDetail == nil {
		return "(worker reported no verification findings)"
	}
	var findings strings.Builder
	for _, note := range workDetail.Notes {
		if note.NoteType == "finding" || note.NoteType == "verification" {
			findings.WriteString(fmt.Sprintf("- [%s] %s\n", note.NoteType, note.Body))
		}
	}
	if findings.Len() == 0 {
		return "(worker reported no verification findings)"
	}
	return findings.String()
}

func attestationChildTitle(parent core.WorkItemRecord, slotIndex int, slot core.RequiredAttestation) string {
	verifier := strings.TrimSpace(slot.VerifierKind)
	if verifier == "" {
		verifier = "attestation"
	}
	method := strings.TrimSpace(slot.Method)
	if method == "" {
		method = "review"
	}
	return fmt.Sprintf("Attest slot %d: %s/%s for %s", slotIndex, verifier, method, parent.Title)
}

func attestationChildObjective(parent core.WorkItemRecord, job core.JobRecord, slotIndex int, slot core.RequiredAttestation, nonce, workerFindings string) string {
	workerModel := summaryString(job.Summary, "model")
	return fmt.Sprintf(`You are an attestation agent reviewing work item %s.

## Work item
Title: %s
Objective: %s
Worker adapter: %s
Worker model: %s
Worker job: %s
Slot: %d (%s/%s)

## Worker's verification findings
%s

## Attestation procedure
1. Inspect the parent work item and its diff.
2. Decide whether the work matches the objective and evidence.
3. Record exactly one attestation on the parent:
   fase work attest %s --nonce %s --result [passed|failed] --summary "<your finding>" --verifier-kind %s --method %s

Do not record more than one attestation. Do not spawn extra work.`, parent.WorkID, parent.Title, parent.Objective, job.Adapter, workerModel, job.JobID, slotIndex, slot.VerifierKind, slot.Method, workerFindings, parent.WorkID, nonce, slot.VerifierKind, slot.Method)
}

func metadataInt(metadata map[string]any, key string) (int, bool) {
	if metadata == nil {
		return 0, false
	}
	value, ok := metadata[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func nonEmptySlice(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	return []string{value}
}

func shouldSetPendingApproval(work core.WorkItemRecord) bool {
	if work.ExecutionState != core.WorkExecutionStateDone {
		return false
	}
	if work.ApprovalState == core.WorkApprovalStateVerified || work.ApprovalState == core.WorkApprovalStateRejected {
		return false
	}
	for _, slot := range work.RequiredAttestations {
		if slot.Blocking {
			return true
		}
	}
	return false
}

func requiredAttestationsResolved(work core.WorkItemRecord, attestations []core.AttestationRecord) bool {
	superseded := supersededAttestationIDs(attestations)
	for _, slot := range work.RequiredAttestations {
		if !slot.Blocking {
			continue
		}
		if !hasPassingAttestationForSlot(work, slot, attestations, superseded) {
			return false
		}
	}
	return true
}

func unsatisfiedAttestationSlots(work core.WorkItemRecord, attestations []core.AttestationRecord) []core.RequiredAttestation {
	superseded := supersededAttestationIDs(attestations)
	var result []core.RequiredAttestation
	for _, slot := range work.RequiredAttestations {
		if hasPassingAttestationForSlot(work, slot, attestations, superseded) {
			continue
		}
		result = append(result, slot)
	}
	return result
}

// UnsatisfiedAttestationSlotIndices returns the indices (into RequiredAttestations)
// of blocking slots that do not yet have a passing, non-superseded attestation.
// Used by the supervisor to determine how many more attestors to dispatch.
func UnsatisfiedAttestationSlotIndices(work core.WorkItemRecord, attestations []core.AttestationRecord) []int {
	superseded := supersededAttestationIDs(attestations)
	var result []int
	for i, slot := range work.RequiredAttestations {
		if !slot.Blocking {
			continue
		}
		if !hasPassingAttestationForSlot(work, slot, attestations, superseded) {
			result = append(result, i)
		}
	}
	return result
}

func attestationSubmissionMatchesAnySlot(verifierKind, method string, slots []core.RequiredAttestation) bool {
	for _, slot := range slots {
		if attestationSubmissionMatchesSlot(verifierKind, method, slot) {
			return true
		}
	}
	return false
}

func attestationSubmissionMatchesSlot(verifierKind, method string, slot core.RequiredAttestation) bool {
	if slot.VerifierKind != "" && verifierKind != "" && verifierKind != slot.VerifierKind {
		return false
	}
	if slot.Method != "" && method != "" && method != slot.Method {
		return false
	}
	if slot.VerifierKind != "" && verifierKind == "" {
		return false
	}
	if slot.Method != "" && method == "" {
		return false
	}
	return true
}

func formatAttestationSlots(slots []core.RequiredAttestation) string {
	if len(slots) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(slots))
	for _, slot := range slots {
		verifier := strings.TrimSpace(slot.VerifierKind)
		if verifier == "" {
			verifier = "*"
		}
		method := strings.TrimSpace(slot.Method)
		if method == "" {
			method = "*"
		}
		parts = append(parts, fmt.Sprintf("%s/%s", verifier, method))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func supersededAttestationIDs(attestations []core.AttestationRecord) map[string]bool {
	superseded := make(map[string]bool, len(attestations))
	for _, attestation := range attestations {
		if attestation.SupersedesAttestationID != "" {
			superseded[attestation.SupersedesAttestationID] = true
		}
	}
	return superseded
}

func hasPassingAttestationForSlot(work core.WorkItemRecord, slot core.RequiredAttestation, attestations []core.AttestationRecord, superseded map[string]bool) bool {
	for _, attestation := range attestations {
		if attestation.Result != "passed" {
			continue
		}
		if superseded[attestation.AttestationID] {
			continue
		}
		if slot.VerifierKind != "" && attestation.VerifierKind != slot.VerifierKind {
			continue
		}
		if slot.Method != "" && attestation.Method != slot.Method {
			continue
		}
		if work.HeadCommitOID != "" {
			commitOID, _ := attestation.Metadata["commit_oid"].(string)
			if commitOID != work.HeadCommitOID {
				continue
			}
		}
		return true
	}
	return false
}

func (s *Service) attachParentEdge(ctx context.Context, parentID, childID, createdBy string, createdAt time.Time, metadata map[string]any, allowReplace bool) error {
	if err := s.validateParentEdge(ctx, parentID, childID, allowReplace); err != nil {
		return err
	}
	return s.store.CreateWorkEdge(ctx, core.WorkEdgeRecord{
		EdgeID:     core.GenerateID("wedge"),
		FromWorkID: parentID,
		ToWorkID:   childID,
		EdgeType:   "parent_of",
		CreatedBy:  createdBy,
		CreatedAt:  createdAt,
		Metadata:   metadata,
	})
}

func (s *Service) validateParentEdge(ctx context.Context, parentID, childID string, allowReplace bool) error {
	if parentID == "" || childID == "" {
		return fmt.Errorf("%w: parent and child work ids must not be empty", ErrInvalidInput)
	}
	if parentID == childID {
		return fmt.Errorf("%w: parent edge cannot target the same work item", ErrInvalidInput)
	}
	existingParents, err := s.store.ListWorkEdges(ctx, 2, "parent_of", "", childID)
	if err != nil {
		return err
	}
	if len(existingParents) > 0 {
		if !allowReplace {
			return fmt.Errorf("%w: work item %s already has a parent", ErrInvalidInput, childID)
		}
		for _, edge := range existingParents {
			if edge.FromWorkID == parentID {
				return nil
			}
		}
	}
	current := parentID
	seen := map[string]bool{}
	for current != "" {
		if current == childID {
			return fmt.Errorf("%w: parent edge would create a cycle", ErrInvalidInput)
		}
		if seen[current] {
			return fmt.Errorf("%w: parent lineage already contains a cycle", ErrInvalidInput)
		}
		seen[current] = true
		edges, err := s.store.ListWorkEdges(ctx, 2, "parent_of", "", current)
		if err != nil {
			return err
		}
		if len(edges) == 0 {
			break
		}
		current = edges[0].FromWorkID
	}
	return nil
}

// RootWorkID walks parent edges from the given work item to find the root.
// Returns the workID of the root (the work item with no parent), or the
// input workID if it has no parent edge.
func (s *Service) RootWorkID(ctx context.Context, workID string) (string, error) {
	current := workID
	seen := map[string]bool{current: true}
	for {
		edges, err := s.store.ListWorkEdges(ctx, 2, "parent_of", "", current)
		if err != nil {
			return workID, err
		}
		if len(edges) == 0 {
			return current, nil
		}
		parentID := edges[0].FromWorkID
		if seen[parentID] {
			return current, nil
		}
		seen[parentID] = true
		current = parentID
	}
}

// ActiveRootWorkIDs returns the set of root work IDs that have at least one
// active (claimed or in_progress) work item in their subtree.
func (s *Service) ActiveRootWorkIDs(ctx context.Context) (map[string]bool, error) {
	items, err := s.store.ListWorkItems(ctx, 10000, "", "", "", false)
	if err != nil {
		return nil, err
	}
	activeRoots := map[string]bool{}
	for _, item := range items {
		if item.ExecutionState != core.WorkExecutionStateClaimed && item.ExecutionState != core.WorkExecutionStateInProgress {
			continue
		}
		rootID, rootErr := s.RootWorkID(ctx, item.WorkID)
		if rootErr != nil {
			continue
		}
		activeRoots[rootID] = true
	}
	return activeRoots, nil
}

// CountActiveRoots returns the number of distinct root work items that have
// active work in their subtree. Used for concurrency cap enforcement.
func (s *Service) CountActiveRoots(ctx context.Context) (int, error) {
	activeRoots, err := s.ActiveRootWorkIDs(ctx)
	if err != nil {
		return 0, err
	}
	return len(activeRoots), nil
}

// RenderWorkerBriefingMarkdown converts a worker briefing to a compact markdown
// document. Much more token-efficient than JSON for LLM consumption.
func RenderWorkerBriefingMarkdown(r WorkHydrateResult) string {
	var b strings.Builder

	// Assignment
	if a, ok := r["assignment"].(map[string]any); ok {
		b.WriteString("# Assignment\n\n")
		if title, _ := a["title"].(string); title != "" {
			fmt.Fprintf(&b, "**%s**\n", title)
		}
		if wid, _ := a["work_id"].(string); wid != "" {
			fmt.Fprintf(&b, "Work ID: `%s`\n", wid)
		}
		if kind, _ := a["kind"].(string); kind != "" {
			fmt.Fprintf(&b, "Kind: %s\n", kind)
		}
		if obj, _ := a["objective"].(string); obj != "" {
			fmt.Fprintf(&b, "\n%s\n", obj)
		}
	}

	// Notes (conventions, findings from prior work)
	if ev, ok := r["evidence"].(map[string]any); ok {
		if notes := toSlice(ev["latest_notes"]); len(notes) > 0 {
			b.WriteString("\n## Notes\n\n")
			for _, n := range notes {
				if note, ok := n.(map[string]any); ok {
					ntype, _ := note["note_type"].(string)
					body, _ := note["body"].(string)
					if body != "" {
						fmt.Fprintf(&b, "**[%s]** %s\n\n", ntype, body)
					}
				}
			}
		}
	}

	// Contract
	if wc, ok := r["worker_contract"].(map[string]any); ok {
		if rules := toSlice(wc["rules"]); len(rules) > 0 {
			b.WriteString("\n## Rules\n\n")
			for _, rule := range rules {
				if s, ok := rule.(string); ok {
					fmt.Fprintf(&b, "- %s\n", s)
				}
			}
		}
	}

	return b.String()
}

func hydrationLimits(mode string) (updates, notes, attestations, artifacts, jobs int) {
	switch mode {
	case "thin":
		// Minimal: just assignment + contract. No history.
		return 0, 3, 0, 0, 0
	case "deep":
		// Full context: prior runs, artifacts, attestations. For debugging/review.
		return 20, 20, 20, 25, 15
	default:
		// Standard: notes for context, no prior run artifacts or job history.
		return 3, 5, 0, 0, 0
	}
}

func delegationNextAction(work core.WorkItemRecord) string {
	return "Create child work directly only for unexpected work, fanout work, or sequential context isolation when success can be judged from bounded results."
}

func attestationIDs(attestations []core.AttestationRecord) []string {
	ids := make([]string, 0, len(attestations))
	for _, attestation := range attestations {
		ids = append(ids, attestation.AttestationID)
	}
	return ids
}

func workRef(item core.WorkItemRecord) map[string]any {
	return map[string]any{
		"work_id":         item.WorkID,
		"title":           item.Title,
		"kind":            item.Kind,
		"execution_state": item.ExecutionState,
		"approval_state":  item.ApprovalState,
	}
}

func workRefOrNil(item *core.WorkItemRecord) any {
	if item == nil {
		return nil
	}
	return workRef(*item)
}

func workRefs(items []core.WorkItemRecord) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, workRef(item))
	}
	return result
}

func updateRefs(items []core.WorkUpdateRecord) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{
			"update_id":       item.UpdateID,
			"created_at":      item.CreatedAt.Format(time.RFC3339Nano),
			"phase":           item.Phase,
			"execution_state": item.ExecutionState,
			"approval_state":  item.ApprovalState,
			"message":         item.Message,
			"job_id":          item.JobID,
			"session_id":      item.SessionID,
			"artifact_id":     item.ArtifactID,
		})
	}
	return result
}

func noteRefs(items []core.WorkNoteRecord) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{
			"note_id":    item.NoteID,
			"created_at": item.CreatedAt.Format(time.RFC3339Nano),
			"note_type":  item.NoteType,
			"body":       item.Body,
		})
	}
	return result
}

func attestationRefs(items []core.AttestationRecord) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{
			"attestation_id": item.AttestationID,
			"created_at":     item.CreatedAt.Format(time.RFC3339Nano),
			"result":         item.Result,
			"summary":        item.Summary,
			"artifact_id":    item.ArtifactID,
			"verifier_kind":  item.VerifierKind,
			"method":         item.Method,
		})
	}
	return result
}

func artifactRefs(items []core.ArtifactRecord) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{
			"artifact_id": item.ArtifactID,
			"kind":        item.Kind,
			"path":        item.Path,
			"job_id":      item.JobID,
			"session_id":  item.SessionID,
		})
	}
	return result
}

func jobRefs(items []core.JobRecord) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{
			"job_id":            item.JobID,
			"state":             item.State,
			"adapter":           item.Adapter,
			"native_session_id": item.NativeSessionID,
			"summary_message":   summaryString(item.Summary, "message"),
		})
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *Service) firstRelatedWork(ctx context.Context, workID, edgeType string, outbound bool) (*core.WorkItemRecord, error) {
	items, err := s.relatedWork(ctx, workID, edgeType, outbound, 1)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &items[0], nil
}

func (s *Service) relatedWork(ctx context.Context, workID, edgeType string, outbound bool, limit int) ([]core.WorkItemRecord, error) {
	var fromWorkID, toWorkID string
	if outbound {
		fromWorkID = workID
	} else {
		toWorkID = workID
	}
	edges, err := s.store.ListWorkEdges(ctx, limit, edgeType, fromWorkID, toWorkID)
	if err != nil {
		return nil, err
	}
	items := make([]core.WorkItemRecord, 0, len(edges))
	for _, edge := range edges {
		relatedID := edge.FromWorkID
		if outbound {
			relatedID = edge.ToWorkID
		}
		if relatedID == "" {
			continue
		}
		item, err := s.store.GetWorkItem(ctx, relatedID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) listArtifactsForWork(ctx context.Context, workID string, limit int) ([]core.ArtifactRecord, error) {
	jobs, err := s.store.ListJobsByWork(ctx, workID, limit)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return []core.ArtifactRecord{}, nil
	}
	artifacts := make([]core.ArtifactRecord, 0, limit)
	seen := map[string]bool{}
	for _, job := range jobs {
		jobArtifacts, err := s.store.ListArtifactsByJob(ctx, job.JobID, limit)
		if err != nil {
			return nil, err
		}
		for _, artifact := range jobArtifacts {
			if seen[artifact.ArtifactID] {
				continue
			}
			seen[artifact.ArtifactID] = true
			artifacts = append(artifacts, artifact)
			if len(artifacts) >= limit {
				return artifacts, nil
			}
		}
	}
	return artifacts, nil
}

func (s *Service) workHasAvailableAdapter(work core.WorkItemRecord, diags []adapters.Diagnosis) bool {
	for _, diag := range diags {
		if !diag.Enabled || !diag.Available || !diag.Implemented {
			continue
		}
		if containsString(work.ForbiddenAdapters, diag.Adapter) {
			continue
		}
		if len(work.PreferredAdapters) > 0 && !containsString(work.PreferredAdapters, diag.Adapter) {
			continue
		}
		if s.adapterSatisfiesWork(work, diag) {
			return true
		}
	}
	return false
}

func (s *Service) workHasAvailableRuntime(work core.WorkItemRecord, entries []core.CatalogEntry, diags []adapters.Diagnosis, haveCatalog bool) bool {
	if haveCatalog {
		for _, entry := range entries {
			if !entry.Available {
				continue
			}
			if containsString(work.ForbiddenAdapters, entry.Adapter) {
				continue
			}
			if len(work.PreferredAdapters) > 0 && !containsString(work.PreferredAdapters, entry.Adapter) {
				continue
			}
			if len(work.PreferredModels) > 0 && !containsString(work.PreferredModels, entry.Model) {
				continue
			}
			if containsString(work.AvoidModels, entry.Model) {
				continue
			}
			if catalogEntrySatisfiesWork(work, entry) {
				return true
			}
		}
		return false
	}
	return s.workHasAvailableAdapter(work, diags)
}

func (s *Service) adapterSatisfiesWork(work core.WorkItemRecord, diag adapters.Diagnosis) bool {
	cfg, _ := s.Config.Adapters.ByName(diag.Adapter)
	tagSet := map[string]bool{}
	for _, tag := range cfg.Tags {
		tagSet[strings.ToLower(strings.TrimSpace(tag))] = true
	}
	for _, required := range work.RequiredCapabilities {
		required = strings.ToLower(strings.TrimSpace(required))
		if required == "" {
			continue
		}
		if tagSet[required] {
			continue
		}
		switch required {
		case "headless_run":
			if !diag.Capabilities.HeadlessRun {
				return false
			}
		case "stream_json":
			if !diag.Capabilities.StreamJSON {
				return false
			}
		case "native_resume", "resume":
			if !diag.Capabilities.NativeResume {
				return false
			}
		case "native_fork", "fork":
			if !diag.Capabilities.NativeFork {
				return false
			}
		case "structured_output":
			if !diag.Capabilities.StructuredOutput {
				return false
			}
		case "interactive_mode":
			if !diag.Capabilities.InteractiveMode {
				return false
			}
		case "rpc_mode":
			if !diag.Capabilities.RPCMode {
				return false
			}
		case "mcp":
			if !diag.Capabilities.MCP {
				return false
			}
		case "checkpointing":
			if !diag.Capabilities.Checkpointing {
				return false
			}
		case "session_export":
			if !diag.Capabilities.SessionExport {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func catalogEntrySatisfiesWork(work core.WorkItemRecord, entry core.CatalogEntry) bool {
	for _, required := range work.RequiredModelTraits {
		required = strings.ToLower(strings.TrimSpace(required))
		if required == "" {
			continue
		}
		if !containsString(entry.Traits, required) {
			return false
		}
	}
	return true
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
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

func (s *Service) catalogHistory(ctx context.Context, limit int) (map[string]core.CatalogHistory, error) {
	jobs, err := s.store.ListJobs(ctx, limit)
	if err != nil {
		return nil, err
	}

	history := make(map[string]core.CatalogHistory)
	for _, job := range jobs {
		usage := usageFromSummary(job.Summary)
		provider, model := pricingLookupContext(job, usage)
		keys := []string{catalogHistoryKey(job.Adapter, provider, model)}
		if model != "" {
			keys = append(keys, catalogHistoryKey(job.Adapter, provider, ""))
		}

		for _, key := range keys {
			hist := history[key]
			if hist.RecentJobs == 0 {
				hist.LastJobID = job.JobID
				hist.LastSessionID = job.SessionID
				lastUsedAt := job.UpdatedAt
				hist.LastUsedAt = &lastUsedAt
			}
			hist.RecentJobs++
			switch job.State {
			case core.JobStateCompleted:
				hist.RecentSuccesses++
				if hist.LastSucceededAt == nil {
					lastSucceededAt := job.UpdatedAt
					hist.LastSucceededAt = &lastSucceededAt
				}
			case core.JobStateFailed, core.JobStateBlocked:
				hist.RecentFailures++
				if hist.LastFailedAt == nil {
					lastFailedAt := job.UpdatedAt
					hist.LastFailedAt = &lastFailedAt
				}
			case core.JobStateCancelled:
				hist.RecentCancelled++
			}
			if usage != nil {
				hist.TotalInputTokens += usage.InputTokens + usage.CachedInputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
				hist.TotalOutputTokens += usage.OutputTokens
			}
			history[key] = hist
		}
	}

	return history, nil
}

func catalogEntryLess(a, b core.CatalogEntry) bool {
	if probeRank(a.ProbeStatus) != probeRank(b.ProbeStatus) {
		return probeRank(a.ProbeStatus) < probeRank(b.ProbeStatus)
	}
	if historySuccesses(a.History) != historySuccesses(b.History) {
		return historySuccesses(a.History) > historySuccesses(b.History)
	}
	if cmp := compareTimes(historySucceededAt(a.History), historySucceededAt(b.History)); cmp != 0 {
		return cmp > 0
	}
	if cmp := compareTimes(historyUsedAt(a.History), historyUsedAt(b.History)); cmp != 0 {
		return cmp > 0
	}
	if a.Selected != b.Selected {
		return a.Selected
	}
	if a.Available != b.Available {
		return a.Available
	}
	if a.Adapter != b.Adapter {
		return a.Adapter < b.Adapter
	}
	if a.Provider != b.Provider {
		return a.Provider < b.Provider
	}
	return a.Model < b.Model
}

func probeRank(status string) int {
	switch status {
	case "runnable":
		return 0
	case "":
		return 1
	case "unsupported_by_plan":
		return 2
	case "hung_or_unstable":
		return 3
	default:
		return 4
	}
}

func historySuccesses(history *core.CatalogHistory) int {
	if history == nil {
		return 0
	}
	return history.RecentSuccesses
}

func historySucceededAt(history *core.CatalogHistory) *time.Time {
	if history == nil {
		return nil
	}
	return history.LastSucceededAt
}

func historyUsedAt(history *core.CatalogHistory) *time.Time {
	if history == nil {
		return nil
	}
	return history.LastUsedAt
}

func compareTimes(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	case a.After(*b):
		return 1
	case a.Before(*b):
		return -1
	default:
		return 0
	}
}

func historyJobMatches(job core.JobRecord, req HistorySearchRequest) bool {
	if req.Adapter != "" && job.Adapter != req.Adapter {
		return false
	}
	if req.SessionID != "" && job.SessionID != req.SessionID {
		return false
	}
	if req.CWD != "" && job.CWD != req.CWD {
		return false
	}
	if req.Model != "" && !strings.EqualFold(summaryString(job.Summary, "model"), req.Model) {
		return false
	}
	return true
}

func stringifySummary(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(data)
	}
}

func makeHistoryMatch(kind, query, text string) (string, bool) {
	if text == "" {
		return "", false
	}
	lowerText := strings.ToLower(text)
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	if lowerQuery == "" {
		return "", false
	}
	idx := strings.Index(lowerText, lowerQuery)
	if idx == -1 {
		return "", false
	}
	start := idx - 80
	if start < 0 {
		start = 0
	}
	end := idx + len(query) + 160
	if end > len(text) {
		end = len(text)
	}
	snippet := strings.TrimSpace(text[start:end])
	snippet = strings.ReplaceAll(snippet, "\n", " ")
	snippet = strings.Join(strings.Fields(snippet), " ")
	return snippet, true
}

func historyScore(query, text string) int {
	lowerText := strings.ToLower(text)
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	if lowerQuery == "" {
		return 0
	}
	count := strings.Count(lowerText, lowerQuery)
	if count == 0 {
		return 0
	}
	score := count * 10
	if idx := strings.Index(lowerText, lowerQuery); idx >= 0 {
		score += max(0, 1000-idx)
	}
	return score
}

func shouldSearchArtifactContent(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > 256*1024 {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".txt", ".json", ".jsonl", ".log", ".yaml", ".yml", ".toml", ".xml", ".csv":
		return true
	}
	return ext == ""
}

func (s *Service) probeCatalogEntry(ctx context.Context, entry core.CatalogEntry, req ProbeCatalogRequest, timeout time.Duration) (core.CatalogEntry, *core.CatalogIssue) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = "Reply with exactly OK and nothing else."
	}
	cwd := req.CWD
	if cwd == "" {
		cwd = "."
	}
	model := probeModelArg(entry)
	startedAt := time.Now().UTC()
	entry.ProbeAt = &startedAt
	entry.ProbeStatus = "launching"
	entry.ProbeMessage = ""
	entry.ProbeJobID = ""

	runResult, runErr := s.Run(probeCtx, RunRequest{
		Adapter:      entry.Adapter,
		Model:        model,
		CWD:          cwd,
		Prompt:       prompt,
		PromptSource: "catalog_probe",
		Label:        "catalog probe",
	})
	if runErr != nil {
		entry.ProbeStatus = "launch_error"
		entry.ProbeMessage = runErr.Error()
		return entry, &core.CatalogIssue{
			Adapter:  entry.Adapter,
			Severity: "warning",
			Message:  fmt.Sprintf("catalog probe launch failed for %s/%s: %v", entry.Provider, entry.Model, runErr),
		}
	}

	entry.ProbeJobID = runResult.Job.JobID
	status, waitErr := s.WaitStatus(probeCtx, runResult.Job.JobID, 250*time.Millisecond, timeout)
	if waitErr != nil {
		entry.ProbeStatus = "hung_or_unstable"
		entry.ProbeMessage = waitErr.Error()
		return entry, nil
	}

	classification, message := classifyProbeOutcome(status)
	entry.ProbeStatus = classification
	entry.ProbeMessage = message
	return entry, nil
}

func probeModelArg(entry core.CatalogEntry) string {
	if entry.Model == "" {
		return ""
	}
	switch entry.Adapter {
	case "opencode":
		if entry.Provider != "" {
			return entry.Provider + "/" + entry.Model
		}
	}
	return entry.Model
}

func classifyProbeOutcome(status *StatusResult) (string, string) {
	if status == nil {
		return "provider_error", ""
	}
	message := summaryString(status.Job.Summary, "message")
	eventsText := strings.ToLower(message)
	for _, event := range status.Events {
		eventsText += " " + strings.ToLower(string(event.Payload))
	}

	switch {
	case strings.Contains(eventsText, "not supported when using codex with a chatgpt account"),
		strings.Contains(eventsText, "not supported"),
		strings.Contains(eventsText, "unsupported"),
		strings.Contains(eventsText, "plan"):
		if message == "" {
			message = "unsupported by current account or plan"
		}
		return "unsupported_by_plan", message
	case status.Job.State == core.JobStateFailed:
		if message == "" {
			message = "provider-side failure"
		}
		return "provider_error", message
	}

	trimmed := strings.TrimSpace(message)
	if status.Job.State == core.JobStateCompleted && trimmed == "OK" {
		return "runnable", message
	}
	if status.Job.State == core.JobStateCompleted {
		if message == "" {
			message = "completed without the expected probe response"
		}
		return "hung_or_unstable", message
	}
	if status.Job.State == core.JobStateCancelled {
		if message == "" {
			message = "probe cancelled"
		}
		return "hung_or_unstable", message
	}
	if message == "" {
		message = string(status.Job.State)
	}
	return "provider_error", message
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
	if usageByModel := modelUsageFromPayload(payload); len(usageByModel) > 0 {
		job.Summary["usage_by_model"] = modelUsageMaps(usageByModel)
	}

	if vendor := costFromPayload(payload); vendor != nil {
		job.Summary["vendor_cost"] = costMap(*vendor)
	}
	if estimated := s.estimateCostForJob(*job); estimated != nil {
		job.Summary["estimated_cost"] = costMap(*estimated)
	}
	if preferred := preferredCostFromSummary(*job); preferred != nil {
		job.Summary["cost"] = costMap(*preferred)
	} else {
		delete(job.Summary, "cost")
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
	merged.InputTokens = max(merged.InputTokens, incoming.InputTokens)
	merged.OutputTokens = max(merged.OutputTokens, incoming.OutputTokens)
	merged.TotalTokens = max(merged.TotalTokens, incoming.TotalTokens)
	merged.CachedInputTokens = max(merged.CachedInputTokens, incoming.CachedInputTokens)
	merged.CacheReadInputTokens = max(merged.CacheReadInputTokens, incoming.CacheReadInputTokens)
	merged.CacheCreationInputTokens = max(merged.CacheCreationInputTokens, incoming.CacheCreationInputTokens)
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

func modelUsageFromPayload(payload map[string]any) []core.UsageReport {
	raw, ok := payload["model_usage"].([]any)
	if !ok {
		return nil
	}

	models := make([]core.UsageReport, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		report := core.UsageReport{
			Provider:                 summaryString(entry, "provider"),
			Model:                    summaryString(entry, "model"),
			InputTokens:              summaryInt64(entry, "input_tokens"),
			OutputTokens:             summaryInt64(entry, "output_tokens"),
			TotalTokens:              summaryInt64(entry, "total_tokens"),
			CachedInputTokens:        summaryInt64(entry, "cached_input_tokens"),
			CacheReadInputTokens:     summaryInt64(entry, "cache_read_input_tokens"),
			CacheCreationInputTokens: summaryInt64(entry, "cache_creation_input_tokens"),
			CostUSD:                  summaryFloat64(entry, "cost_usd"),
			Source:                   "vendor_report",
		}
		if report.TotalTokens == 0 {
			report.TotalTokens = report.InputTokens + report.OutputTokens + report.CachedInputTokens + report.CacheReadInputTokens + report.CacheCreationInputTokens
		}
		if report.Model == "" {
			continue
		}
		models = append(models, report)
	}
	if len(models) == 0 {
		return nil
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].Model < models[j].Model
	})
	return models
}

func modelUsageFromSummary(summary map[string]any) []core.UsageReport {
	if summary == nil {
		return nil
	}
	raw, ok := summary["usage_by_model"].([]any)
	if !ok {
		return nil
	}
	models := make([]core.UsageReport, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		model := core.UsageReport{
			Provider:                 summaryString(entry, "provider"),
			Model:                    summaryString(entry, "model"),
			InputTokens:              summaryInt64(entry, "input_tokens"),
			OutputTokens:             summaryInt64(entry, "output_tokens"),
			TotalTokens:              summaryInt64(entry, "total_tokens"),
			CachedInputTokens:        summaryInt64(entry, "cached_input_tokens"),
			CacheReadInputTokens:     summaryInt64(entry, "cache_read_input_tokens"),
			CacheCreationInputTokens: summaryInt64(entry, "cache_creation_input_tokens"),
			CostUSD:                  summaryFloat64(entry, "cost_usd"),
			Source:                   summaryString(entry, "source"),
		}
		if model.TotalTokens == 0 {
			model.TotalTokens = model.InputTokens + model.OutputTokens + model.CachedInputTokens + model.CacheReadInputTokens + model.CacheCreationInputTokens
		}
		if model.Model == "" {
			continue
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].Model < models[j].Model
	})
	return models
}

func modelUsageMaps(models []core.UsageReport) []map[string]any {
	if len(models) == 0 {
		return nil
	}
	result := make([]map[string]any, 0, len(models))
	for _, model := range models {
		result = append(result, map[string]any{
			"provider":                    model.Provider,
			"model":                       model.Model,
			"input_tokens":                model.InputTokens,
			"output_tokens":               model.OutputTokens,
			"total_tokens":                model.TotalTokens,
			"cached_input_tokens":         model.CachedInputTokens,
			"cache_read_input_tokens":     model.CacheReadInputTokens,
			"cache_creation_input_tokens": model.CacheCreationInputTokens,
			"cost_usd":                    model.CostUSD,
			"source":                      model.Source,
		})
	}
	return result
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

func preferredCostFromSummary(job core.JobRecord) *core.CostEstimate {
	if vendor := vendorCostFromSummary(job); vendor != nil {
		return vendor
	}
	return estimatedCostFromSummary(job)
}

func vendorCostFromSummary(job core.JobRecord) *core.CostEstimate {
	if cost := summaryCost(job.Summary, "vendor_cost"); cost != nil {
		return cost
	}
	if cost := summaryCost(job.Summary, "cost"); cost != nil && !cost.Estimated {
		return cost
	}
	return nil
}

func estimatedCostFromSummary(job core.JobRecord) *core.CostEstimate {
	if cost := summaryCost(job.Summary, "estimated_cost"); cost != nil {
		return cost
	}
	if cost := summaryCost(job.Summary, "cost"); cost != nil && cost.Estimated {
		return cost
	}
	return nil
}

func (s *Service) estimateCostForJob(job core.JobRecord) *core.CostEstimate {
	if models := modelUsageFromSummary(job.Summary); len(models) > 0 {
		total := &core.CostEstimate{
			Currency:  "USD",
			Estimated: true,
		}
		for _, modelUsage := range models {
			usage := core.UsageReport{
				Provider:                 modelUsage.Provider,
				Model:                    modelUsage.Model,
				InputTokens:              modelUsage.InputTokens,
				OutputTokens:             modelUsage.OutputTokens,
				TotalTokens:              modelUsage.TotalTokens,
				CachedInputTokens:        modelUsage.CachedInputTokens,
				CacheReadInputTokens:     modelUsage.CacheReadInputTokens,
				CacheCreationInputTokens: modelUsage.CacheCreationInputTokens,
				Source:                   modelUsage.Source,
			}
			provider, model := pricingLookupContext(job, &usage)
			if provider == "" || model == "" {
				continue
			}
			usage.Provider = provider
			usage.Model = model
			estimate := pricing.Estimate(usage, pricing.Resolve(s.Config, provider, model))
			if estimate == nil {
				continue
			}
			total.InputCostUSD += estimate.InputCostUSD
			total.OutputCostUSD += estimate.OutputCostUSD
			total.CachedInputCostUSD += estimate.CachedInputCostUSD
			total.CacheReadCostUSD += estimate.CacheReadCostUSD
			total.CacheCreationCostUSD += estimate.CacheCreationCostUSD
			total.TotalCostUSD += estimate.TotalCostUSD
			if total.Source == "" {
				total.Source = estimate.Source
			}
			if total.SourceURL == "" {
				total.SourceURL = estimate.SourceURL
			}
			if total.ObservedAt == nil {
				total.ObservedAt = estimate.ObservedAt
			}
		}
		if total.TotalCostUSD > 0 {
			return total
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

func summaryCost(summary map[string]any, key string) *core.CostEstimate {
	if summary == nil {
		return nil
	}
	raw, ok := summary[key].(map[string]any)
	if !ok {
		return nil
	}
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
	if cost.TotalCostUSD <= 0 {
		return nil
	}
	return cost
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

func (s *Service) markWorkQueued(ctx context.Context, workID string, job core.JobRecord, session core.SessionRecord) error {
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	work.CurrentJobID = job.JobID
	work.CurrentSessionID = session.SessionID
	work.ExecutionState = core.WorkExecutionStateClaimed
	work.UpdatedAt = now
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return err
	}
	return s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:       core.GenerateID("wup"),
		WorkID:         workID,
		ExecutionState: core.WorkExecutionStateClaimed,
		Message:        "job queued",
		JobID:          job.JobID,
		SessionID:      session.SessionID,
		CreatedBy:      "service",
		CreatedAt:      now,
		Metadata:       map[string]any{"job_state": string(job.State)},
	})
}

func (s *Service) syncWorkStateFromJob(ctx context.Context, job core.JobRecord, payload map[string]any) error {
	if job.WorkID == "" {
		return nil
	}
	work, err := s.store.GetWorkItem(ctx, job.WorkID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	now := time.Now().UTC()
	prevState := string(work.ExecutionState)
	work.CurrentJobID = job.JobID
	work.CurrentSessionID = job.SessionID
	work.UpdatedAt = now

	var (
		workState core.WorkExecutionState
		message   string
	)
	switch job.State {
	case core.JobStateQueued, core.JobStateCreated:
		workState = core.WorkExecutionStateClaimed
	case core.JobStateStarting, core.JobStateRunning, core.JobStateWaitingInput:
		workState = core.WorkExecutionStateInProgress
		if work.Kind != "attest" && work.AttestationFrozenAt == nil {
			frozenAt := now
			work.AttestationFrozenAt = &frozenAt
		}
	case core.JobStateCompleted:
		workState = core.WorkExecutionStateDone
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
		if shouldSetPendingApproval(work) {
			work.ApprovalState = core.WorkApprovalStatePending
		}
	case core.JobStateFailed:
		workState = core.WorkExecutionStateFailed
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
	case core.JobStateCancelled:
		workState = core.WorkExecutionStateCancelled
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
	case core.JobStateBlocked:
		workState = core.WorkExecutionStateBlocked
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
	default:
		workState = work.ExecutionState
	}
	work.ExecutionState = workState
	if workState == core.WorkExecutionStateAwaitingAttestation {
		if work.AttestationFrozenAt == nil {
			frozenAt := now
			work.AttestationFrozenAt = &frozenAt
		}
	}
	if payload != nil {
		message = summaryString(payload, "message")
	}
	if message == "" {
		message = summaryString(job.Summary, "message")
	}
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return err
	}
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:       core.GenerateID("wup"),
		WorkID:         work.WorkID,
		ExecutionState: work.ExecutionState,
		ApprovalState:  work.ApprovalState,
		Message:        message,
		JobID:          job.JobID,
		SessionID:      job.SessionID,
		CreatedBy:      "service",
		CreatedAt:      now,
		Metadata:       map[string]any{"job_state": string(job.State)},
	}); err != nil {
		return err
	}
	ev := WorkEvent{
		Kind:      WorkEventUpdated,
		WorkID:    work.WorkID,
		Title:     work.Title,
		State:     string(work.ExecutionState),
		PrevState: prevState,
		JobID:     job.JobID,
		Actor:     ActorService,
		Cause:     CauseJobLifecycle,
	}
	s.Events.Publish(ev)
	return nil
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
	if err := s.syncWorkStateFromJob(ctx, *job, payload); err != nil {
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
	if err := s.store.UpdateJob(ctx, *job); err != nil {
		return err
	}
	if err := s.syncWorkStateFromJob(ctx, *job, job.Summary); err != nil {
		return err
	}
	work, err := s.store.GetWorkItem(ctx, job.WorkID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if strings.EqualFold(work.Kind, "attest") {
		if err := s.refreshParentAfterAttestationChild(ctx, work); err != nil {
			return err
		}
	}
	if state == core.JobStateCompleted && work.Kind != "attest" {
		if err := s.spawnAttestationChildren(ctx, work, *job); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) refreshParentAfterAttestationChild(ctx context.Context, child core.WorkItemRecord) error {
	parentID := ""
	if child.Metadata != nil {
		parentID, _ = child.Metadata["parent_work_id"].(string)
	}
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil
	}
	return s.refreshAttestationParentState(ctx, parentID)
}

// forceDoneWarningEvent is the structured log payload emitted to stderr when
// the force-done escape hatch is used.
type forceDoneWarningEvent struct {
	Level     string `json:"level"`
	Kind      string `json:"kind"`
	WorkID    string `json:"work_id"`
	Actor     string `json:"actor,omitempty"`
	Timestamp string `json:"timestamp"`
	Message   string `json:"message"`
}

// emitForceDoneWarning writes a structured warning to stderr when --force
// bypasses guardDoneTransition. Errors are intentionally swallowed; this is
// a best-effort audit trail only.
func emitForceDoneWarning(workID, actor string) {
	event := forceDoneWarningEvent{
		Level:     "warn",
		Kind:      "force_done_override",
		WorkID:    workID,
		Actor:     actor,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Message:   "guardDoneTransition bypassed via --force; attestation requirements not verified",
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, string(data))
}

// guardDoneTransition returns an error if the work item cannot transition to a
// terminal-success state (done, archived) because it has unresolved attestation
// requirements or pending attestation children.
func (s *Service) guardDoneTransition(ctx context.Context, work core.WorkItemRecord) error {
	// Check for attestation children that aren't done
	children, err := s.store.ListWorkChildren(ctx, work.WorkID, 100)
	if err == nil {
		for _, child := range children {
			if child.Kind == "attest" && child.ExecutionState != core.WorkExecutionStateDone {
				return fmt.Errorf("%w: work item %s has pending attestation child %s (state: %s)",
					ErrInvalidInput, work.WorkID, child.WorkID, child.ExecutionState)
			}
		}
	}
	// Check required attestations (legacy path without children)
	if len(work.RequiredAttestations) > 0 {
		attestations, fetchErr := s.store.ListAttestationRecords(ctx, "work", work.WorkID, 200)
		if fetchErr == nil && !requiredAttestationsResolved(work, attestations) {
			return fmt.Errorf("%w: work item %s has unresolved required attestations",
				ErrInvalidInput, work.WorkID)
		}
	}
	return nil
}

func (s *Service) refreshAttestationParentState(ctx context.Context, parentID string) error {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil
	}

	parent, err := s.store.GetWorkItem(ctx, parentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	prevParentState := string(parent.ExecutionState)

	children, err := s.store.ListWorkChildren(ctx, parentID, 200)
	if err != nil {
		return err
	}
	attestationChildren := make([]core.WorkItemRecord, 0, len(children))
	for _, candidate := range children {
		if strings.EqualFold(candidate.Kind, "attest") {
			attestationChildren = append(attestationChildren, candidate)
		}
	}
	if len(attestationChildren) == 0 {
		return nil
	}

	for _, attChild := range attestationChildren {
		switch attChild.ExecutionState {
		case core.WorkExecutionStateFailed, core.WorkExecutionStateCancelled:
			parent.ExecutionState = core.WorkExecutionStateFailed
			parent.ClaimedBy = ""
			parent.ClaimedUntil = nil
			parent.UpdatedAt = time.Now().UTC()
			if err := s.store.UpdateWorkItem(ctx, parent); err != nil {
				return err
			}
			s.Events.Publish(WorkEvent{
				Kind:      WorkEventUpdated,
				WorkID:    parent.WorkID,
				Title:     parent.Title,
				State:     string(parent.ExecutionState),
				PrevState: prevParentState,
				Actor:     ActorService,
				Cause:     CauseParentTransition,
			})
			return nil
		case core.WorkExecutionStateDone:
		default:
			return nil
		}
	}

	attestations, err := s.store.ListAttestationRecords(ctx, "work", parentID, 200)
	if err != nil {
		return err
	}
	if !requiredAttestationsResolved(parent, attestations) {
		return nil
	}

	parent.ExecutionState = core.WorkExecutionStateDone
	parent.ClaimedBy = ""
	parent.ClaimedUntil = nil
	parent.UpdatedAt = time.Now().UTC()
	if shouldSetPendingApproval(parent) {
		parent.ApprovalState = core.WorkApprovalStatePending
	}
	if err := s.store.UpdateWorkItem(ctx, parent); err != nil {
		return err
	}
	s.Events.Publish(WorkEvent{
		Kind:      WorkEventUpdated,
		WorkID:    parent.WorkID,
		Title:     parent.Title,
		State:     string(parent.ExecutionState),
		PrevState: prevParentState,
		Actor:     ActorService,
		Cause:     CauseParentTransition,
	})
	return nil
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
		EvidenceRefs:         []core.TransferArtifact{},
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
		packet.EvidenceRefs = []core.TransferArtifact{}
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
		core.TransferArtifact{Kind: "recent_turns_json", Path: turnsPath},
		core.TransferArtifact{Kind: "recent_events_jsonl", Path: eventsPath},
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

func inferArtifactKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return "markdown"
	case ".json":
		return "json"
	case ".jsonl":
		return "jsonl"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "image"
	case ".mp4", ".mov", ".webm":
		return "video"
	case ".txt", ".log":
		return "text"
	default:
		return "file"
	}
}

func normalizeWorkClaimError(workID string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return normalizeStoreError("work", workID, err)
	}
	if errors.Is(err, store.ErrBusy) {
		return fmt.Errorf("%w: work %s is claimed by another worker", ErrBusy, workID)
	}
	return err
}

func timeStringPtr(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
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
