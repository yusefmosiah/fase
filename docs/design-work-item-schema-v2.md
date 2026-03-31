# Design: Cogent Work Item Schema v2

Generated: 2026-03-31
Status: READY FOR IMPLEMENTATION
Principle: "cogent is a function where requirements go in and work comes out"

## Core Insight

Work-focused, not worker-focused. The work item defines WHAT needs to be true.
The runtime decides HOW (which models, which adapters, which tools). Multi-model
verification is a requirement on the work item, not a separate task in the queue.

## Schema: 3 Tables

### work_items

```sql
CREATE TABLE IF NOT EXISTS work_items (
    work_id    TEXT PRIMARY KEY,
    title      TEXT NOT NULL,
    objective  TEXT NOT NULL,          -- natural language description of the work
    kind       TEXT NOT NULL,          -- implement, idea, investigate, etc.
    priority   INTEGER NOT NULL DEFAULT 0,
    parent_id  TEXT REFERENCES work_items(work_id),  -- replaces work_edges for hierarchy
    metadata   TEXT NOT NULL DEFAULT '{}',            -- extensible JSON for future needs
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
```

**Removed** (31 → 10 columns):
- `execution_state` — derived from events log
- `approval_state` — derived from whether requirements are satisfied
- `lock_state` — removed entirely (no human locks in v2)
- `phase` — just a tag, move to metadata if needed
- `position` — UI concern, move to metadata
- `configuration_class`, `budget_class` — unused
- `required_capabilities_json` — model infers from objective
- `required_model_traits_json` — model infers
- `preferred_adapters_json`, `forbidden_adapters_json` — model decides
- `preferred_models_json`, `avoid_models_json` — model decides
- `required_attestations_json` — replaced by requirements table
- `required_docs_json` — replaced by requirements table
- `acceptance_json` — replaced by requirements table
- `head_commit_oid` — move to event log (commit events)
- `attestation_frozen_at` — no longer needed
- `current_job_id`, `current_session_id` — derived from events
- `claimed_by`, `claimed_until` — leases removed
- `attempt_epoch` — derived from event count

**Added**: `parent_id` replaces `work_edges` table for simple hierarchy. If you
need non-hierarchical edges later, add them. Don't build a graph database
for a queue with occasional parent-child relationships.

### requirements

```sql
CREATE TABLE IF NOT EXISTS requirements (
    requirement_id TEXT PRIMARY KEY,
    work_id        TEXT NOT NULL REFERENCES work_items(work_id) ON DELETE CASCADE,
    description    TEXT NOT NULL,          -- natural language: "tests pass", "docs updated"
    verification   TEXT NOT NULL,          -- "deterministic", "agent", "human"
    created_at     TEXT NOT NULL
);
```

Requirements are predicates. A requirement is satisfied when there's an event
in the log with `kind = 'requirement_satisfied'` referencing this requirement_id
and `content` containing the verification evidence.

**verification types:**
- `deterministic`: checkable by code (tests pass, lint clean, doc file exists)
- `agent`: checkable by LLM (coherent with architecture, well-written)
- `human`: requires human judgment (product intent, taste)

The runtime evaluates deterministic requirements automatically. Agent requirements
are evaluated by spawning a peer agent (co-agent tool). Human requirements wait
for a human event in the log.

### events

```sql
CREATE TABLE IF NOT EXISTS events (
    event_id   TEXT PRIMARY KEY,
    work_id    TEXT NOT NULL REFERENCES work_items(work_id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    author     TEXT NOT NULL,             -- agent id, "human", "system"
    content    TEXT NOT NULL DEFAULT '',   -- natural language or JSON
    metadata   TEXT NOT NULL DEFAULT '{}', -- structured data (commit hash, file paths, etc.)
    created_at TEXT NOT NULL
);

CREATE INDEX idx_events_work_id ON events(work_id);
CREATE INDEX idx_events_kind ON events(kind);
```

**Event kinds:**
- `created` — work item created
- `started` — agent began working on this
- `note` — informational append (replaces work_notes, work_updates)
- `message` — inter-agent communication
- `commit` — code committed (metadata: commit hash, branch)
- `requirement_satisfied` — a requirement predicate was verified true
- `requirement_failed` — a requirement predicate was verified false
- `completed` — agent declares work done
- `failed` — agent declares work failed
- `doc_updated` — documentation was updated (metadata: file path)

**Status derivation** (computed, never stored):
```
func DeriveStatus(events []Event) Status {
    has := map[string]bool{}
    for _, e := range events {
        has[e.Kind] = true
    }
    if has["failed"]    { return Failed }
    if has["completed"] { return Done }     // BUT: check requirements
    if has["started"]   { return InProgress }
    return Ready
}
```

A work item with `completed` event but unsatisfied requirements is NOT done.
It's `CompletedPendingVerification`. The agent thinks it's done. The requirements
disagree. This is the gap between "I finished" and "the work is actually finished."

## What Gets Deleted

### Tables removed entirely (21 → 3):
- `work_edges` — replaced by `parent_id` on work_items
- `work_updates` — events with kind="note"
- `work_notes` — events with kind="note"
- `work_proposals` — events with kind="message" + structured content
- `attestation_records` — events with kind="requirement_satisfied"
- `approval_records` — derived from requirements satisfaction
- `promotion_records` — events with kind="note"
- `check_records` — events with kind="requirement_satisfied"
- `doc_content` — events with kind="doc_updated" + file path reference

### Tables kept but separate concern (not part of work schema):
- `sessions` — runtime concern (adapter session tracking)
- `jobs` — runtime concern (execution tracking)
- `turns` — runtime concern (LLM conversation turns)
- `native_sessions` — runtime concern
- `artifacts` — runtime concern (file attachments)
- `events` (old) — replaced by new events table
- `job_runtime` — runtime concern
- `catalog_snapshots` — runtime concern
- `locks` — removed (no locking in v2)
- `handoffs` — runtime concern
- `private_notes` — kept in private DB, unchanged

The runtime tables (sessions, jobs, turns, etc.) stay as-is. They track HOW
work is being executed, not WHAT the work is. The work schema redesign doesn't
touch the adapter layer's internal state.

## Migration Strategy

### Phase 1: New tables alongside old
- Create `work_items_v2`, `requirements`, `events_v2` tables
- Write a migration function that copies existing work_items into v2 format
- Convert `required_attestations_json` entries into requirements rows
- Convert existing `work_updates`, `work_notes`, `attestation_records` into events

### Phase 2: Dual-write
- New code writes to both old and new tables
- Read from new tables
- Verify consistency

### Phase 3: Cut over
- Remove old table writes
- Drop old tables
- Rename `work_items_v2` → `work_items`, `events_v2` → `events`

### Phase 4: Retention
- Add event retention policy (prune events older than N days, configurable)
- Cap events per work item (keep last 1000)

## Service Layer Changes

### Remove (~467 lines):
- `ReconcileExpiredLeases()` — no leases
- `ReconcileOnStartup()` — no leases
- `ClaimWork()` — no leases
- `ClaimNextWork()` — replace with "get next ready work item"
- `ReleaseWork()` — no leases
- `RenewWorkLease()` — no leases
- All store methods for claim/release/renew

### Simplify:
- `ListWork()` — query work_items, derive status from events
- `GetWork()` — query work_item + requirements + events
- `UpdateWork()` — update title/objective/priority/metadata only
- `CompleteWork()` — append "completed" event, check requirements

### Add:
- `AddRequirement(workID, description, verification)` — insert requirement
- `SatisfyRequirement(workID, reqID, evidence, author)` — append event
- `AppendEvent(workID, kind, author, content)` — generic event append
- `DeriveStatus(workID)` — compute status from event log
- `IsWorkDone(workID)` — completed AND all requirements satisfied

## Store Layer Changes

### store.go rewrite scope:
- Current: 4175 lines, 97 methods
- Keep: runtime methods (sessions, jobs, turns, artifacts, catalog) — ~2500 lines
- Rewrite: work methods — ~1200 lines → ~400 lines
- Delete: lease methods, overlay methods, approval methods — ~500 lines
- Net: ~4175 → ~3400 lines

### New store methods (~400 lines):
```go
// Work items
func (s *Store) CreateWorkItem(item WorkItem) error
func (s *Store) GetWorkItem(workID string) (*WorkItem, error)
func (s *Store) ListWorkItems(filter WorkFilter) ([]WorkItem, error)
func (s *Store) UpdateWorkItem(workID string, updates WorkUpdate) error
func (s *Store) DeleteWorkItem(workID string) error

// Requirements
func (s *Store) AddRequirement(workID string, req Requirement) error
func (s *Store) ListRequirements(workID string) ([]Requirement, error)
func (s *Store) DeleteRequirement(reqID string) error

// Events (append-only)
func (s *Store) AppendEvent(event Event) error
func (s *Store) ListEvents(workID string, filter EventFilter) ([]Event, error)
func (s *Store) PruneEvents(olderThan time.Time) (int, error)

// Derived state
func (s *Store) DeriveWorkStatus(workID string) (WorkStatus, error)
func (s *Store) GetReadyWork(limit int) ([]WorkItem, error)
func (s *Store) IsWorkComplete(workID string) (bool, error)
```

## Core Types Changes

### types.go rewrite:
```go
type WorkItem struct {
    WorkID    string    `json:"work_id"`
    Title     string    `json:"title"`
    Objective string    `json:"objective"`
    Kind      string    `json:"kind"`
    Priority  int       `json:"priority"`
    ParentID  *string   `json:"parent_id,omitempty"`
    Metadata  JSONMap   `json:"metadata"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type Requirement struct {
    RequirementID string `json:"requirement_id"`
    WorkID        string `json:"work_id"`
    Description   string `json:"description"`
    Verification  string `json:"verification"` // "deterministic", "agent", "human"
    CreatedAt     time.Time `json:"created_at"`
}

type Event struct {
    EventID   string    `json:"event_id"`
    WorkID    string    `json:"work_id"`
    Kind      string    `json:"kind"`
    Author    string    `json:"author"`
    Content   string    `json:"content"`
    Metadata  JSONMap   `json:"metadata"`
    CreatedAt time.Time `json:"created_at"`
}

type WorkStatus string
const (
    StatusReady                    WorkStatus = "ready"
    StatusInProgress               WorkStatus = "in_progress"
    StatusCompletedPendingVerify   WorkStatus = "completed_pending_verification"
    StatusDone                     WorkStatus = "done"
    StatusFailed                   WorkStatus = "failed"
)
```

**Removed types:**
- `WorkExecutionState` (7 states → 5 derived statuses)
- `WorkApprovalState` — derived from requirements
- `WorkLockState` — removed
- `AttestationRecord` — events
- `RequiredAttestation` — requirements
- `ApprovalRecord` — events
- `PromotionRecord` — events

## CLI Changes

### Commands that stay (same interface, new backend):
- `cogent work create` — creates work item
- `cogent work show` — shows item + requirements + event log
- `cogent work list` — lists items with derived status
- `cogent work ready` — lists items where status == ready
- `cogent work note-add` — appends event with kind="note"
- `cogent work doc-set` — appends event with kind="doc_updated"

### Commands that change:
- `cogent work attest` → `cogent work satisfy <work-id> <requirement-id> --evidence "..."`
- `cogent work claim` → removed (no leases)
- `cogent work release` → removed
- `cogent work renew-lease` → removed
- `cogent work verify` → `cogent work check <work-id>` (evaluate all requirements)
- `cogent work approve/reject` → removed (derived from requirements)
- `cogent work lock/unlock` → removed
- `cogent work promote` → removed

### New commands:
- `cogent work require <work-id> --description "tests pass" --verification deterministic`
- `cogent work satisfy <work-id> <requirement-id> --evidence "all tests passing"`
- `cogent work check <work-id>` — evaluate all requirements, report status
- `cogent work log <work-id>` — show event log (append-only history)

## Supervisor Changes

The agentic supervisor (supervisor_agent.go) currently reads execution_state,
approval_state, and lease information to make dispatch decisions. With v2:

- **Dispatch**: query `GetReadyWork()` instead of filtering by execution_state + lease
- **Completion**: check `IsWorkComplete()` instead of approval_state
- **No lease management**: supervisor doesn't need to reconcile anything
- **Requirements**: supervisor can add requirements to work items before dispatch
  (e.g., "verified by separate model") and the runtime satisfies them inline

## What This Does NOT Change

- Native adapter (LLM calls, tool loop) — untouched
- Co-agent tools (spawn_agent, post_message) — untouched
- Session/job/turn tracking — untouched
- Mind-graph UI — needs to read from new schema but same API shape
- Skills — untouched
- Private notes — untouched (separate DB)

## Data Flow Diagram

```
                          ┌─────────────┐
                          │  work_items  │
                          │  (10 cols)   │
                          └──────┬──────┘
                                 │
                    ┌────────────┼────────────┐
                    │            │            │
              ┌─────┴─────┐ ┌───┴───┐ ┌─────┴─────┐
              │requirements│ │events │ │  runtime   │
              │(predicates)│ │ (log) │ │(sessions,  │
              └───────────┘ └───────┘ │ jobs, etc) │
                    │            │     └───────────┘
                    │            │
                    └─────┬──────┘
                          │
                   DeriveStatus()
                          │
                    ┌─────┴─────┐
                    │  ready?   │──→ supervisor dispatches
                    │  done?    │──→ supervisor moves on
                    │  failed?  │──→ supervisor retries/escalates
                    └───────────┘
```

## Implementation Order

1. Define new Go types in `internal/core/types.go`
2. Add new tables + store methods in `internal/store/store.go`
3. Write migration function (old schema → new)
4. Update `internal/service/service_work.go` (remove lease, add requirements)
5. Update CLI commands in `internal/cli/work_*.go`
6. Update supervisor in `internal/cli/supervisor_agent.go`
7. Update briefing hydration in `internal/service/service_briefing.go`
8. Run migration on existing .cogent/cogent.db
9. Delete old types, old store methods, old service methods
10. Add retention policy for events

## Test Strategy

- Unit tests for DeriveStatus() with various event sequences
- Unit tests for IsWorkComplete() with satisfied/unsatisfied requirements
- Integration tests for create → require → start → satisfy → complete flow
- Migration test: load existing DB, migrate, verify data integrity
- CLI tests: verify new command surface
- Supervisor test: verify dispatch with new schema

## Success Criteria

- `cogent work list` shows derived status, not stored state
- `cogent work require` + `cogent work satisfy` replaces attestation system
- No lease columns anywhere in the schema
- No routing-hint columns (model infers from objective)
- Events are append-only, never mutated
- Retention policy keeps DB under 5MB for normal usage
- All existing work items migrated without data loss
