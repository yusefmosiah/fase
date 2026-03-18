package core

import (
	"encoding/json"
	"time"
)

type JobState string

const (
	JobStateCreated      JobState = "created"
	JobStateQueued       JobState = "queued"
	JobStateStarting     JobState = "starting"
	JobStateRunning      JobState = "running"
	JobStateWaitingInput JobState = "waiting_input"
	JobStateCompleted    JobState = "completed"
	JobStateFailed       JobState = "failed"
	JobStateCancelled    JobState = "cancelled"
	JobStateBlocked      JobState = "blocked"
)

func (s JobState) Terminal() bool {
	switch s {
	case JobStateCompleted, JobStateFailed, JobStateCancelled, JobStateBlocked:
		return true
	default:
		return false
	}
}

type SessionRecord struct {
	SessionID      string         `json:"session_id"`
	Label          string         `json:"label,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	Status         string         `json:"status"`
	OriginAdapter  string         `json:"origin_adapter"`
	OriginJobID    string         `json:"origin_job_id"`
	CWD            string         `json:"cwd"`
	LatestJobID    string         `json:"latest_job_id,omitempty"`
	ParentSession  *string        `json:"parent_session_id,omitempty"`
	ForkedFromTurn *string        `json:"forked_from_turn_id,omitempty"`
	Tags           []string       `json:"tags,omitempty"`
	Metadata       map[string]any `json:"metadata"`
}

type NativeSessionRecord struct {
	SessionID       string         `json:"session_id"`
	Adapter         string         `json:"adapter"`
	NativeSessionID string         `json:"native_session_id"`
	Resumable       bool           `json:"resumable"`
	Metadata        map[string]any `json:"metadata"`
	LockedByJobID   string         `json:"locked_by_job_id,omitempty"`
	LockedAt        *time.Time     `json:"locked_at,omitempty"`
	LockExpiresAt   *time.Time     `json:"lock_expires_at,omitempty"`
}

type JobRecord struct {
	JobID           string         `json:"job_id"`
	SessionID       string         `json:"session_id"`
	WorkID          string         `json:"work_id,omitempty"`
	Adapter         string         `json:"adapter"`
	State           JobState       `json:"state"`
	Label           string         `json:"label,omitempty"`
	NativeSessionID string         `json:"native_session_id,omitempty"`
	CWD             string         `json:"cwd"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	FinishedAt      *time.Time     `json:"finished_at,omitempty"`
	Summary         map[string]any `json:"summary"`
	LastRawArtifact string         `json:"last_raw_artifact,omitempty"`
}

type TurnRecord struct {
	TurnID          string         `json:"turn_id"`
	SessionID       string         `json:"session_id"`
	JobID           string         `json:"job_id"`
	Adapter         string         `json:"adapter"`
	StartedAt       time.Time      `json:"started_at"`
	CompletedAt     *time.Time     `json:"completed_at,omitempty"`
	InputText       string         `json:"input_text"`
	InputSource     string         `json:"input_source"`
	ResultSummary   string         `json:"result_summary,omitempty"`
	Status          string         `json:"status"`
	NativeSessionID string         `json:"native_session_id,omitempty"`
	Stats           map[string]any `json:"stats"`
}

type EventRecord struct {
	EventID         string          `json:"event_id"`
	Seq             int64           `json:"seq"`
	TS              time.Time       `json:"ts"`
	JobID           string          `json:"job_id"`
	SessionID       string          `json:"session_id"`
	Adapter         string          `json:"adapter"`
	Kind            string          `json:"kind"`
	Phase           string          `json:"phase,omitempty"`
	NativeSessionID string          `json:"native_session_id,omitempty"`
	CorrelationID   string          `json:"correlation_id,omitempty"`
	Payload         json.RawMessage `json:"payload"`
	RawRef          string          `json:"raw_ref,omitempty"`
}

type ArtifactRecord struct {
	ArtifactID string         `json:"artifact_id"`
	JobID      string         `json:"job_id"`
	SessionID  string         `json:"session_id"`
	Kind       string         `json:"kind"`
	Path       string         `json:"path"`
	CreatedAt  time.Time      `json:"created_at"`
	Metadata   map[string]any `json:"metadata"`
}

type ModelPricing struct {
	Currency                string     `json:"currency,omitempty"`
	InputUSDPerMTok         float64    `json:"input_usd_per_mtok,omitempty"`
	OutputUSDPerMTok        float64    `json:"output_usd_per_mtok,omitempty"`
	CachedInputUSDPerMTok   float64    `json:"cached_input_usd_per_mtok,omitempty"`
	CacheReadUSDPerMTok     float64    `json:"cache_read_usd_per_mtok,omitempty"`
	CacheCreationUSDPerMTok float64    `json:"cache_creation_usd_per_mtok,omitempty"`
	Source                  string     `json:"source,omitempty"`
	SourceURL               string     `json:"source_url,omitempty"`
	ObservedAt              *time.Time `json:"observed_at,omitempty"`
}

type UsageReport struct {
	Provider                 string  `json:"provider,omitempty"`
	Model                    string  `json:"model,omitempty"`
	InputTokens              int64   `json:"input_tokens,omitempty"`
	OutputTokens             int64   `json:"output_tokens,omitempty"`
	TotalTokens              int64   `json:"total_tokens,omitempty"`
	CachedInputTokens        int64   `json:"cached_input_tokens,omitempty"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens,omitempty"`
	CostUSD                  float64 `json:"cost_usd,omitempty"`
	Source                   string  `json:"source,omitempty"`
}

type CostEstimate struct {
	Currency             string     `json:"currency,omitempty"`
	InputCostUSD         float64    `json:"input_cost_usd,omitempty"`
	OutputCostUSD        float64    `json:"output_cost_usd,omitempty"`
	CachedInputCostUSD   float64    `json:"cached_input_cost_usd,omitempty"`
	CacheReadCostUSD     float64    `json:"cache_read_cost_usd,omitempty"`
	CacheCreationCostUSD float64    `json:"cache_creation_cost_usd,omitempty"`
	TotalCostUSD         float64    `json:"total_cost_usd,omitempty"`
	Estimated            bool       `json:"estimated"`
	Source               string     `json:"source,omitempty"`
	SourceURL            string     `json:"source_url,omitempty"`
	ObservedAt           *time.Time `json:"observed_at,omitempty"`
}

type CatalogProvenance struct {
	Source     string    `json:"source"`
	Command    string    `json:"command,omitempty"`
	Path       string    `json:"path,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

type CatalogEntry struct {
	Adapter      string            `json:"adapter"`
	Provider     string            `json:"provider,omitempty"`
	Model        string            `json:"model,omitempty"`
	Traits       []string          `json:"traits,omitempty"`
	Selected     bool              `json:"selected"`
	Available    bool              `json:"available"`
	AuthMethod   string            `json:"auth_method,omitempty"`
	BillingClass string            `json:"billing_class,omitempty"`
	Source       string            `json:"source,omitempty"`
	Provenance   CatalogProvenance `json:"provenance"`
	Pricing      *ModelPricing     `json:"pricing,omitempty"`
	ProbeStatus  string            `json:"probe_status,omitempty"`
	ProbeMessage string            `json:"probe_message,omitempty"`
	ProbeJobID   string            `json:"probe_job_id,omitempty"`
	ProbeAt      *time.Time        `json:"probe_at,omitempty"`
	History      *CatalogHistory   `json:"history,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
}

type CatalogHistory struct {
	RecentJobs        int        `json:"recent_jobs,omitempty"`
	RecentSuccesses   int        `json:"recent_successes,omitempty"`
	RecentFailures    int        `json:"recent_failures,omitempty"`
	RecentCancelled   int        `json:"recent_cancelled,omitempty"`
	LastJobID         string     `json:"last_job_id,omitempty"`
	LastSessionID     string     `json:"last_session_id,omitempty"`
	LastUsedAt        *time.Time `json:"last_used_at,omitempty"`
	LastSucceededAt   *time.Time `json:"last_succeeded_at,omitempty"`
	LastFailedAt      *time.Time `json:"last_failed_at,omitempty"`
	TotalInputTokens  int64      `json:"total_input_tokens,omitempty"`
	TotalOutputTokens int64      `json:"total_output_tokens,omitempty"`
}

type CatalogIssue struct {
	Adapter  string `json:"adapter"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type CatalogSnapshot struct {
	SnapshotID string         `json:"snapshot_id"`
	CreatedAt  time.Time      `json:"created_at"`
	Entries    []CatalogEntry `json:"entries"`
	Issues     []CatalogIssue `json:"issues,omitempty"`
}

type TransferSource struct {
	Adapter         string `json:"adapter"`
	Model           string `json:"model,omitempty"`
	JobID           string `json:"job_id"`
	SessionID       string `json:"session_id"`
	NativeSessionID string `json:"native_session_id,omitempty"`
	CWD             string `json:"cwd,omitempty"`
}

type TransferArtifact struct {
	Kind     string         `json:"kind"`
	Path     string         `json:"path"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type TransferPacket struct {
	TransferID           string             `json:"transfer_id"`
	ExportedAt           time.Time          `json:"exported_at"`
	Mode                 string             `json:"mode"`
	Reason               string             `json:"reason,omitempty"`
	Disclaimer           string             `json:"disclaimer"`
	Source               TransferSource     `json:"source"`
	Objective            string             `json:"objective"`
	Summary              string             `json:"summary"`
	Unresolved           []string           `json:"unresolved"`
	ImportantFiles       []string           `json:"important_files"`
	RecentTurnsInline    []TurnRecord       `json:"recent_turns_inline,omitempty"`
	RecentEventsInline   []EventRecord      `json:"recent_events_inline,omitempty"`
	EvidenceRefs         []TransferArtifact `json:"evidence_refs"`
	Artifacts            []TransferArtifact `json:"artifacts"`
	Constraints          []string           `json:"constraints"`
	RecommendedNextSteps []string           `json:"recommended_next_steps"`
}

type TransferRecord struct {
	TransferID string         `json:"transfer_id"`
	JobID      string         `json:"job_id"`
	SessionID  string         `json:"session_id"`
	CreatedAt  time.Time      `json:"created_at"`
	Packet     TransferPacket `json:"packet"`
}

type LockRecord struct {
	LockKey         string     `json:"lock_key"`
	Adapter         string     `json:"adapter"`
	NativeSessionID string     `json:"native_session_id"`
	JobID           string     `json:"job_id"`
	AcquiredAt      time.Time  `json:"acquired_at"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
}

type JobRuntimeRecord struct {
	JobID             string     `json:"job_id"`
	SupervisorPID     int        `json:"supervisor_pid,omitempty"`
	VendorPID         int        `json:"vendor_pid,omitempty"`
	Detached          bool       `json:"detached"`
	StartedAt         time.Time  `json:"started_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

type HistoryMatch struct {
	Kind      string          `json:"kind"`
	ID        string          `json:"id"`
	WorkID    string          `json:"work_id,omitempty"`
	SessionID string          `json:"session_id"`
	JobID     string          `json:"job_id,omitempty"`
	Adapter   string          `json:"adapter"`
	Model     string          `json:"model,omitempty"`
	CWD       string          `json:"cwd,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Title     string          `json:"title,omitempty"`
	Snippet   string          `json:"snippet,omitempty"`
	Path      string          `json:"path,omitempty"`
	Score     int             `json:"score,omitempty"`
	Source    string          `json:"source,omitempty"`
	Artifact  *ArtifactRecord `json:"artifact,omitempty"`
}

type WorkExecutionState string

const (
	WorkExecutionStateReady               WorkExecutionState = "ready"
	WorkExecutionStateClaimed             WorkExecutionState = "claimed"
	WorkExecutionStateInProgress          WorkExecutionState = "in_progress"
	WorkExecutionStateAwaitingAttestation WorkExecutionState = "awaiting_attestation"
	WorkExecutionStateBlocked             WorkExecutionState = "blocked"
	WorkExecutionStateDone                WorkExecutionState = "done"
	WorkExecutionStateFailed              WorkExecutionState = "failed"
	WorkExecutionStateCancelled           WorkExecutionState = "cancelled"
	WorkExecutionStateArchived            WorkExecutionState = "archived"
)

func (s WorkExecutionState) Terminal() bool {
	switch s {
	case WorkExecutionStateDone, WorkExecutionStateFailed, WorkExecutionStateCancelled:
		return true
	default:
		return false
	}
}

type WorkApprovalState string

const (
	WorkApprovalStateNone     WorkApprovalState = "none"
	WorkApprovalStatePending  WorkApprovalState = "pending"
	WorkApprovalStateVerified WorkApprovalState = "verified"
	WorkApprovalStateRejected WorkApprovalState = "rejected"
)

type WorkLockState string

const (
	WorkLockStateUnlocked    WorkLockState = "unlocked"
	WorkLockStateHumanLocked WorkLockState = "human_locked"
)

type RequiredAttestation struct {
	VerifierKind string         `json:"verifier_kind,omitempty"`
	Method       string         `json:"method,omitempty"`
	Blocking     bool           `json:"blocking,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type WorkItemRecord struct {
	WorkID               string                `json:"work_id"`
	Title                string                `json:"title"`
	Objective            string                `json:"objective"`
	Kind                 string                `json:"kind"`
	ExecutionState       WorkExecutionState    `json:"execution_state"`
	ApprovalState        WorkApprovalState     `json:"approval_state"`
	LockState            WorkLockState         `json:"lock_state"`
	Phase                string                `json:"phase,omitempty"`
	Priority             int                   `json:"priority,omitempty"`
	Position             int                   `json:"position,omitempty"`
	ConfigurationClass   string                `json:"configuration_class,omitempty"`
	BudgetClass          string                `json:"budget_class,omitempty"`
	RequiredCapabilities []string              `json:"required_capabilities,omitempty"`
	RequiredModelTraits  []string              `json:"required_model_traits,omitempty"`
	PreferredAdapters    []string              `json:"preferred_adapters,omitempty"`
	ForbiddenAdapters    []string              `json:"forbidden_adapters,omitempty"`
	PreferredModels      []string              `json:"preferred_models,omitempty"`
	AvoidModels          []string              `json:"avoid_models,omitempty"`
	RequiredAttestations []RequiredAttestation `json:"required_attestations,omitempty"`
	Acceptance           map[string]any        `json:"acceptance,omitempty"`
	Metadata             map[string]any        `json:"metadata,omitempty"`
	HeadCommitOID        string                `json:"head_commit_oid,omitempty"`
	AttestationFrozenAt  *time.Time            `json:"attestation_frozen_at,omitempty"`
	CurrentJobID         string                `json:"current_job_id,omitempty"`
	CurrentSessionID     string                `json:"current_session_id,omitempty"`
	ClaimedBy            string                `json:"claimed_by,omitempty"`
	ClaimedUntil         *time.Time            `json:"claimed_until,omitempty"`
	CreatedAt            time.Time             `json:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
}

type WorkEdgeRecord struct {
	EdgeID     string         `json:"edge_id"`
	FromWorkID string         `json:"from_work_id"`
	ToWorkID   string         `json:"to_work_id"`
	EdgeType   string         `json:"edge_type"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedBy  string         `json:"created_by,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

type WorkUpdateRecord struct {
	UpdateID       string             `json:"update_id"`
	WorkID         string             `json:"work_id"`
	ExecutionState WorkExecutionState `json:"execution_state,omitempty"`
	ApprovalState  WorkApprovalState  `json:"approval_state,omitempty"`
	Phase          string             `json:"phase,omitempty"`
	Message        string             `json:"message,omitempty"`
	JobID          string             `json:"job_id,omitempty"`
	SessionID      string             `json:"session_id,omitempty"`
	ArtifactID     string             `json:"artifact_id,omitempty"`
	Metadata       map[string]any     `json:"metadata,omitempty"`
	CreatedBy      string             `json:"created_by,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
}

type WorkNoteRecord struct {
	NoteID    string         `json:"note_id"`
	WorkID    string         `json:"work_id"`
	NoteType  string         `json:"note_type,omitempty"`
	Body      string         `json:"body"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedBy string         `json:"created_by,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type DocContentRecord struct {
	DocID     string    `json:"doc_id"`
	WorkID    string    `json:"work_id"`
	Path      string    `json:"path"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Format    string    `json:"format"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type WorkProposalRecord struct {
	ProposalID    string         `json:"proposal_id"`
	ProposalType  string         `json:"proposal_type"`
	State         string         `json:"state"`
	TargetWorkID  string         `json:"target_work_id,omitempty"`
	SourceWorkID  string         `json:"source_work_id,omitempty"`
	Rationale     string         `json:"rationale,omitempty"`
	ProposedPatch map[string]any `json:"proposed_patch,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	CreatedBy     string         `json:"created_by,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	ReviewedBy    string         `json:"reviewed_by,omitempty"`
	ReviewedAt    *time.Time     `json:"reviewed_at,omitempty"`
}

type AttestationRecord struct {
	AttestationID           string         `json:"attestation_id"`
	SubjectKind             string         `json:"subject_kind"`
	SubjectID               string         `json:"subject_id"`
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
	SignerPubkey            string         `json:"signer_pubkey,omitempty"`
	Signature               string         `json:"signature,omitempty"`
	Metadata                map[string]any `json:"metadata,omitempty"`
	CreatedBy               string         `json:"created_by,omitempty"`
	CreatedAt               time.Time      `json:"created_at"`
}

type ApprovalRecord struct {
	ApprovalID           string         `json:"approval_id"`
	WorkID               string         `json:"work_id"`
	ApprovedCommitOID    string         `json:"approved_commit_oid,omitempty"`
	ApprovedRef          string         `json:"approved_ref,omitempty"`
	AttestationIDs       []string       `json:"attestation_ids,omitempty"`
	Status               string         `json:"status"`
	SupersedesApprovalID string         `json:"supersedes_approval_id,omitempty"`
	ApprovedBy           string         `json:"approved_by,omitempty"`
	ApprovedAt           time.Time      `json:"approved_at"`
	Metadata             map[string]any `json:"metadata,omitempty"`
}

type PromotionRecord struct {
	PromotionID       string         `json:"promotion_id"`
	WorkID            string         `json:"work_id"`
	ApprovalID        string         `json:"approval_id,omitempty"`
	Environment       string         `json:"environment"`
	PromotedCommitOID string         `json:"promoted_commit_oid,omitempty"`
	TargetRef         string         `json:"target_ref,omitempty"`
	Status            string         `json:"status"`
	PromotedBy        string         `json:"promoted_by,omitempty"`
	PromotedAt        time.Time      `json:"promoted_at"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}
