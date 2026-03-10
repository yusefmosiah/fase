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

type TransferEvidenceRef struct {
	Kind     string         `json:"kind"`
	Path     string         `json:"path"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type TransferPacket struct {
	TransferID           string                `json:"transfer_id"`
	ExportedAt           time.Time             `json:"exported_at"`
	Mode                 string                `json:"mode"`
	Reason               string                `json:"reason,omitempty"`
	Disclaimer           string                `json:"disclaimer"`
	Source               TransferSource        `json:"source"`
	Objective            string                `json:"objective"`
	Summary              string                `json:"summary"`
	Unresolved           []string              `json:"unresolved"`
	ImportantFiles       []string              `json:"important_files"`
	RecentTurnsInline    []TurnRecord          `json:"recent_turns_inline,omitempty"`
	RecentEventsInline   []EventRecord         `json:"recent_events_inline,omitempty"`
	EvidenceRefs         []TransferEvidenceRef `json:"evidence_refs"`
	Artifacts            []TransferArtifact    `json:"artifacts"`
	Constraints          []string              `json:"constraints"`
	RecommendedNextSteps []string              `json:"recommended_next_steps"`
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
