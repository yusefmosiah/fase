Date: 2026-03-20
Kind: Design document
Status: Implemented (v0)
Priority: 1
Requires: [cogent-spec-and-implementation-guide.md]
Owner: Runtime / Work System

## Narrative Summary (1-minute read)

The work runtime is the durable semantic layer of cogent. Work items are the
primary abstraction above jobs and sessions. Typed edges express structural
relationships between work items. A three-tier graph mutation model separates
execution updates from structural proposals from approval-gated changes. Workers
hydrate from compiled briefings that are deterministic projections of runtime
state. Runtime doc records support linkage, indexing, and review, but the repo
file at the declared path remains authoritative.

The core principle: prompts are compiled views over runtime state, not the
primary control plane. The briefing JSON passed to every worker is the output
of `CompileWorkerBriefing`, not a handwritten prompt.

## 1) Core Principle

### Prompts are compiled views, not the control plane

Prompts are not the source of truth. They are lossy, expensive, hard to diff,
hard to validate, and easy to drift from actual system state.

The happy path is:
1. a work item exists in the work graph with an objective and constraints,
2. a supervisor claims the work item and compiles a briefing from runtime state,
3. the briefing JSON is passed as the prompt to a worker,
4. the worker acts, publishing structured updates and notes back to the work item,
5. attestation and approval follow from durable evidence.

This is already how the system works. `CompileWorkerBriefing` in
`internal/service/service.go:1351` is the single compiler. The supervisor loop
in `internal/cli/supervisor_loop.go:220` claims, hydrates, serializes to JSON,
and passes the result directly as `--prompt` to `cogent run`.

## 2) Work As The Primary Abstraction

Jobs are execution-shaped. Work is orchestration-shaped.

One work item (`WorkItemRecord`) may involve:
- one or more research jobs,
- implementation jobs,
- attestation-producing jobs,
- review jobs,
- retries,
- child work items linked by `parent_of` edges,
- debrief and transfer artifacts.

That makes work the natural object for identity, continuity, attestation,
documentation, coordination, and recovery.

Jobs (`JobRecord`) and sessions (`SessionRecord`) are execution traces attached
to work items via `current_job_id` and `current_session_id`. A work item
survives job failure, session loss, and adapter changes. The invariant holds:
agents may always stop, the system may always resume.

### 2.1 Work Item Record

Defined in `internal/core/types.go:345`:

```
WorkItemRecord
  work_id               string           -- immutable identity
  title                 string           -- human-readable label
  objective             string           -- what needs to happen
  kind                  string           -- semantic hint (plan, task, implement, attest, review, ...)
  execution_state       WorkExecutionState -- 9-state machine
  approval_state        WorkApprovalState  -- 4-state machine
  lock_state            WorkLockState      -- unlocked | human_locked
  phase                 string           -- informational progress marker
  priority              int              -- dispatch ordering
  position              int              -- queue ordering within priority
  configuration_class   string           -- runtime config selection
  budget_class          string           -- cost governance
  required_capabilities []string         -- capability constraints
  required_model_traits []string         -- model selection hints
  preferred_adapters    []string         -- adapter preferences
  forbidden_adapters    []string         -- adapter exclusions
  preferred_models      []string         -- model preferences
  avoid_models          []string         -- model exclusions
  required_attestations []RequiredAttestation -- verification policy
  acceptance            map[string]any   -- acceptance criteria
  metadata              map[string]any   -- open extension
  head_commit_oid       string           -- git anchor for attestation freshness
  attestation_frozen_at *time.Time       -- monotonic attestation contract
  current_job_id        string           -- active execution trace
  current_session_id    string           -- active session
  claimed_by            string           -- lease holder
  claimed_until         *time.Time       -- lease expiry
  created_at            time.Time
  updated_at            time.Time
```

### 2.2 Work Item Kinds

Open-ended semantic hints, not a fixed ontology:

| Kind | Typical Use |
|------|-------------|
| `plan` | Architecture and design |
| `research` | Investigation and exploration |
| `implement` | Code changes |
| `attest` | Verification and review |
| `review` | Human or agent review |
| `red_team` | Adversarial testing |
| `doc` | Documentation work |
| `recovery` | State repair and retry |

Kinds influence worker contract rules. For example, `attest` kind workers get
a different set of contract rules and write commands, including the mandatory
`cogent work attest` instruction.

## 3) Execution State Machine

Execution state and approval state are deliberately separate. "Finished" and
"accepted" are not the same thing.

### 3.1 Execution States

Defined in `internal/core/types.go:287`:

```
ready ──(claim)──> claimed ──(start)──> in_progress
  ^                  │                     │
  │                  │(release)            ├──> awaiting_attestation
  │                  v                     │          │
  │                ready                   ├──> blocked
  │                                        │
  │                                        v
  │                              done / failed / cancelled
  │                                        │
  │                                        v
  │                                     archived

Terminal: done, failed, cancelled, archived
```

Full state set: `ready`, `claimed`, `in_progress`,
`awaiting_attestation`, `blocked`, `done`, `failed`, `cancelled`,
`archived`.

Transition guards (`internal/service/service.go:1988`):
- Cannot transition to `done` or `archived` if attestation policy has
  unresolved blocking attestations.
- Moving to `done` with an attestation policy auto-sets
  `approval_state = pending`.

### 3.2 Approval States

Defined in `internal/core/types.go:322`:

`none` -> `pending` -> `verified` | `rejected`

Approval requires all blocking attestations to have a passing result.
Checked by `requiredAttestationsResolved()` before `ApproveWork` succeeds.

### 3.3 Lock States

`unlocked` | `human_locked`

Human-locked items are excluded from the ready projection and cannot be
claimed by automated dispatch.

## 4) Typed Edges

The work graph is over work items, not over sessions or jobs. Edges are
first-class records (`WorkEdgeRecord` in `internal/core/types.go:377`).

### 4.1 Edge Types

| Edge Type | Semantics | Blocking? | Direction Convention |
|-----------|-----------|-----------|---------------------|
| `parent_of` | Hierarchy and scope grouping | No | from=parent, to=child |
| `blocks` | Hard prerequisite for readiness | Yes | from=prerequisite, to=dependent |
| `verifies` | Attestation/approval relationship | No | from=verifier, to=subject |
| `discovered_from` | Lineage without blocking | No | from=source, to=discovery |
| `supersedes` | Replacement for retries/rewrites | Yes (soft) | from=newer, to=older |
| `relates_to` | Weak informational relationship | No | bidirectional |

### 4.2 Parent-Child Constraint

Parent edges form an acyclic forest. A work item has at most one parent.
Children are parallel by default. If ordering matters, explicit `blocks` edges
must be used.

### 4.3 Edge Record

```
WorkEdgeRecord
  edge_id     string         -- immutable identity
  from_work_id string        -- source node
  to_work_id   string        -- target node
  edge_type    string        -- one of the typed edge types
  metadata     map[string]any -- open extension
  created_by   string        -- identity of creator
  created_at   time.Time
```

## 5) Ready Projection

`ready` is the most important projection in the graph. It is the machine
answer to "what can run now?"

Implemented in `internal/store/store.go:825` as `ListReadyWork`. A work item
is ready when all of the following hold:

1. `execution_state = 'ready'`, or `execution_state = 'claimed'` with an
   expired lease (claim reclamation).
2. Not currently held by a valid lease (`claimed_by` is empty, or
   `claimed_until` has passed).
3. Not `human_locked`.
4. No incoming `blocks` or `depends_on` edges from work items that are not
   in a terminal execution state (`done` or `cancelled`).
5. No incoming `supersedes` edges from work items that are not in a terminal
   failure state (`failed` or `cancelled`).

Results are ordered by `priority DESC, position ASC, updated_at DESC`.

## 6) Graph Mutation Model

Three tiers of operations with increasing governance cost.

### 6.1 Direct Mutations

Routine execution operations a worker can perform directly via
`cogent work update`:

- claim or release work,
- update execution state or phase,
- append a work update record,
- add a note,
- attach an artifact,
- mark blocked,
- mark done or failed.

These are execution-shaped. They modify work item fields and append records
but do not reshape the graph structure.

### 6.2 Proposal-Based Structural Edits

Changes that reshape the graph require explicit proposals
(`WorkProposalRecord`):

- split one work item into many,
- merge duplicate work,
- add or remove `blocks` edges,
- add or remove `verifies` edges,
- materially change acceptance criteria,
- supersede a plan subtree,
- promote discovered work into tracked work,
- reparent work,
- rewrite dependency structure.

Workers do not perform these directly. They are graph-governance operations.
The worker contract enforces `dependency_edits: proposal_only` and
`scope_expansion: proposal_only`.

### 6.3 Approval-Gated Changes

Some proposals require attestation or explicit approval before application:

- material scope expansion,
- deletion or supersession of accepted work,
- changes to approval or attestation policy,
- removal of verifier or reviewer requirements,
- root-objective reframing,
- changes with budget, security, or release implications.

The pattern: execution is direct, structure is proposed, governance is
explicit.

## 7) Worker Hydration

Workers hydrate from compiled briefings, not from bespoke prompts.

### 7.1 Compilation

`CompileWorkerBriefing(workID, mode)` in `internal/service/service.go:1351`
is the single compiler. It:

1. Loads the full work item plus children, updates, notes, jobs, proposals,
   attestations, approvals, promotions, artifacts, and docs via `Work()`.
2. Queries graph neighbors: parent, blocking inbound/outbound, children,
   verifiers, discovered, supersedes/superseded_by.
3. Truncates evidence to mode-dependent limits.
4. Generates a summary, open questions, and recommended next actions.
5. Builds worker contract rules (different for `attest` kind workers).
6. Assembles the complete briefing JSON.

The output schema is `cogent.worker_briefing.v1`, defined in
`schemas/worker-briefing.schema.json` and documented in
`docs/cogent-worker-briefing-schema.md`.

### 7.2 Hydration Modes

| Mode | Updates | Notes | Attestations | Artifacts | Jobs | Use Case |
|------|---------|-------|--------------|-----------|------|----------|
| `thin` | 3 | 3 | 3 | 5 | 3 | Fast orientation, simple work |
| `standard` | 10 | 10 | 10 | 20 | 10 | Default for most dispatches |
| `deep` | 25 | 25 | 25 | 50 | 25 | Complex, recovery-heavy work |

### 7.3 Briefing Sections

The briefing has 7 required sections:

1. **runtime** -- provenance (version, config path, state dir, claimant).
2. **assignment** -- the work item being hydrated (work_id, title, objective,
   kind, states, lease).
3. **requirements** -- acceptance criteria, capabilities, adapter preferences,
   mutation policy.
4. **graph_context** -- parent, blocking inbound/outbound, children, verifiers,
   discovered, supersession.
5. **evidence** -- recent updates, notes, attestations, artifacts, jobs,
   history matches.
6. **worker_contract** -- safe read/write commands and behavioral rules.
7. **hydration** -- compiled summary, open questions, recommended next actions.

### 7.4 Worker Contract

Workers receive explicit read and write commands:

Read commands:
- `cogent work show <work-id>`
- `cogent work notes <work-id>`
- `cogent artifacts list --work <work-id>`
- `cogent history search --query <text>`

Write commands:
- `cogent work update <work-id>` -- structured progress updates
- `cogent work note-add <work-id>` -- commentary and findings

For `attest` kind workers, an additional write command is provided:
- `cogent work attest <parent-work-id> --result [passed|failed] --message "<summary>"`

Workers are explicitly prohibited from:
- creating new work items, proposals, or child work,
- calling `cogent work complete`, `cogent work fail`, or `cogent work attest`
  (except attest-kind workers).

### 7.5 Dispatch Integration

The supervisor loop (`internal/cli/supervisor_loop.go:220`) performs:

1. `ClaimWork` with a 30-minute lease.
2. `HydrateWork` in "standard" mode.
3. `json.Marshal(briefing)` -- the compiled briefing IS the prompt.
4. Issue capability token (Ed25519-signed, if CA is available).
5. `spawnRun()` with `prompt = string(briefingJSON)`.

No additional prompt wrapping occurs. The briefing JSON is the prompt.

## 8) Project Hydration

For cold-starting sessions without a specific work item assignment,
`ProjectHydrate()` compiles a project-scoped briefing covering:

- convention notes from all work items with `note_type='convention'`,
- work graph summary (counts by state),
- active, ready, blocked, and recently completed work,
- pending attestations,
- project-level contract rules.

This replaces the MEMORY.md bootstrap approach with a deterministic
compilation from work state.

## 9) Documentation As Deterministic Projection

Tracked documentation is a work-linked runtime record, not an independent
authoritative store. The authoritative content lives in the repository file at
the declared repo-relative path.

### 9.1 Source Inputs

Projections are rendered from durable records:
- work items (objectives, state, acceptance criteria),
- work edges (structural relationships),
- work updates (progress timeline),
- work notes (findings, conventions, commentary),
- work proposals (pending structural changes),
- attestation records (verification evidence),
- artifacts (code, diffs, reports),
- history (canonical session/turn/event records).

### 9.2 DocContentRecord

The runtime stores tracked doc linkage plus imported/projected content:

```
DocContentRecord
  doc_id     string    -- identity
  work_id    string    -- source work item
  path       string    -- authoritative repo-relative path
  title      string    -- human-readable label
  body       string    -- imported or rendered content for indexing/review
  format     string    -- markdown, json, etc.
  version    int       -- runtime record version
  created_at time.Time
  updated_at time.Time
```

### 9.3 Bridge From Filesystem Docs

Filesystem Markdown was the bootstrap medium. It let the system discover its
conceptual shape before the runtime existed. The bridge is:

- work state is the durable semantic layer,
- artifacts hold human-readable intermediate material,
- Markdown docs become deterministic projections of current work state.

The filesystem-doc era was necessary. The work runtime era keeps durable
linkage and review metadata, but repo files still carry the authoritative
content contract. Imported/generated doc bodies can be refreshed from the repo
or regenerated from work state as needed.

### 9.4 Doc-Work Coupling

Docs are coupled to work items via the `DocContentRecord.work_id` field, and
every tracked doc declares a non-empty authoritative repo-relative `path`.
`work doc-set` is an import/bootstrap helper: it can create or refresh the
runtime record, but it does not replace the repo file as the source of truth.
When work state changes, projections can be re-rendered. When verification
detects repo/runtime drift or a missing repo file at the declared path, the
runtime record is stale and cannot satisfy docs-related verification on its own.

ADR-0002 (docs-before-execution) establishes:
- plans are committed docs,
- docs are safe to commit speculatively,
- asynchrony between docs and execution is expected,
- the work graph mirrors doc state,
- attestation verifies doc-code consistency.

## 10) Attestation

Attestation is the centerpiece. Work is not "done" because an agent says so --
it is done when durable evidence satisfies the attestation policy.

### 10.1 Required Attestations

Each work item declares a verification policy:

```
RequiredAttestation
  verifier_kind  string         -- "deterministic", "code_review", "security", etc.
  method         string         -- "test", "review", etc.
  blocking       bool           -- must pass before approval
  metadata       map[string]any
```

### 10.2 Attestation Record

```
AttestationRecord
  attestation_id            string
  subject_kind              string    -- "work", "job", "session", "artifact", "doc", "projection"
  subject_id                string
  result                    string    -- "passed", "failed", "inconclusive", "matches", "drifted"
  summary                   string
  artifact_id               string
  job_id, session_id        string
  method                    string    -- "deterministic", "self_report", "third_party_review", "human"
  verifier_kind             string
  verifier_identity         string
  confidence                float64
  blocking                  bool
  signer_pubkey             string    -- Ed25519 public key
  signature                 string    -- Ed25519 signature
  metadata                  map[string]any
  created_by                string
  created_at                time.Time
```

### 10.3 Attestation Freshness

An attestation only satisfies a policy slot if its `metadata.commit_oid`
matches the work item's current `head_commit_oid`. When new code lands,
prior attestations become stale.

### 10.4 Monotonic Attestation Contract

ADR-0036 proposes replacing synchronous `handleJobCompletion` with:
- predefined attestation slots frozen at dispatch,
- escalation-only after completion,
- attestation jobs as first-class work items in the dispatch queue,
- `awaiting_attestation` as an explicit execution state.

### 10.5 Verification

`VerifyWork()` in `internal/service/verify.go` performs a comprehensive audit:
validates capability tokens, checks CA trust root, validates Git commit OID,
and verifies Ed25519 signatures on attestation records.

## 11) Claims As Leases

Claims are time-bounded leases over work items.

`ClaimWorkItem` in `internal/store/store.go:916` sets `claimed_by` and
`claimed_until`. The ready projection automatically reclaims expired leases,
allowing a different worker to pick up the work.

If a worker dies, the work remains. Another worker hydrates from the same
work state and continues.

## 12) Dynamic Requirements

The runtime uses dynamic capability matching, not predefined worker profiles:

- work items declare `required_capabilities`, `preferred_adapters`,
  `forbidden_adapters`, `preferred_models`, `avoid_models`,
- adapters and models are selected by the supervisor based on work
  requirements and rotation policy,
- `pickAdapterModel()` respects work-level preferences first, then applies
  global round-robin rotation.

This keeps the runtime dynamic and prevents early overfitting to named
worker roles.

## 13) Companion Documents

| Document | Purpose |
|----------|---------|
| `docs/cogent-work-api-and-schema.md` | Concrete Go structs, table shapes, CLI surface, implementation plan |
| `docs/cogent-worker-briefing-schema.md` | Stable briefing contract, compilation rules, schema definition |
| `docs/cogent-v0-local-control-plane.md` | Product direction, board model, supervision guarantees |
| `docs/cogent-live-agent-protocol.md` | Multi-agent orchestration protocol across adapters |
| `docs/adr-0035-cryptographic-agent-identity.md` | Ed25519 CA, capability tokens, signed commits/attestations |
| `docs/adr-0036-monotonic-attestation-contract.md` | Attestation as first-class work, monotonic contract |
| `docs/adr-0034-verification-before-approval.md` | Verification ladder, attestation-driven approval |
| `schemas/worker-briefing.schema.json` | JSON Schema for the v1 briefing contract |
| `cogent-spec-and-implementation-guide.md` | Master specification and implementation guide |
