Date: 2026-03-10
Kind: Spec + implementation plan
Status: Draft
Priority: 1
Requires: [docs/cogent-work-runtime.md]
Owner: Runtime / Work System

## Narrative Summary (1-minute read)

This doc turns the `cogent work` idea into a concrete implementation target.

The goal is not to build a general workflow DSL. The goal is to add one durable
work graph to `cogent` with:
- explicit work items,
- typed edges,
- structured progress updates,
- notes,
- proposals for graph edits,
- attestation records,
- ready/blocking projections,
- and a CLI/API surface that both hosts and workers can use.

The first version should stay intentionally small. It should be enough to make
work durable, queryable, and governable without trying to encode every possible
orchestration pattern.

## What Changed

1. Defined the minimum durable tables and record shapes for `work`.
2. Defined explicit edge types and state transitions.
3. Split direct execution mutations from proposal-based graph edits.
4. Defined a worker-safe CLI surface and a host/supervisor CLI surface.
5. Added a phased implementation plan from storage to projections.

## What To Do Next

1. Add core `work` records and storage tables.
2. Wire `run --work` and `send --work` to attach jobs and sessions to work.
3. Implement worker-safe read/update commands.
4. Add `work proposal` and attestation records.
5. Add `work ready` and basic deterministic projections.

## 1) Design Goals

The first `work` layer should:
- make work items durable and queryable,
- let workers hydrate from work state instead of large bespoke prompts,
- let workers publish structured progress,
- let hosts and workers discover actionable unblocked work,
- preserve graph lineage across retries, discoveries, and attestations,
- keep execution state separate from approval state,
- stay bash-friendly and CLI-first.

It should not:
- become a YAML workflow engine,
- embed a scheduler DSL,
- require remote infrastructure,
- force one rigid ontology of worker roles,
- replace ordinary shell control flow for sequencing and policy.

## 2) Core Records

### 2.1 Work Item

One durable semantic unit of work.

Suggested Go shape:

```go
type WorkItem struct {
    WorkID               string
    Title                string
    Objective            string
    Kind                 string
    ExecutionState       string
    ApprovalState        string
    Phase                string
    Priority             int
    RequiredCapabilities []string
    PreferredAdapters    []string
    ForbiddenAdapters    []string
    AcceptanceJSON       json.RawMessage
    RequiredAttestations json.RawMessage
    MetadataJSON         json.RawMessage
    CurrentJobID         string
    CurrentSessionID     string
    ClaimedBy            string
    ClaimedUntil         *time.Time
    CreatedAt            time.Time
    UpdatedAt            time.Time
}
```

Notes:
- `ExecutionState` and `ApprovalState` are separate on purpose.
- `ClaimedBy` should identify the worker/session/host holding the lease.
- `AcceptanceJSON` and `RequiredAttestations` should stay flexible at first.

### 2.2 Work Edge

Typed relationship between two work items.

```go
type WorkEdge struct {
    EdgeID        string
    FromWorkID    string
    ToWorkID      string
    EdgeType      string
    MetadataJSON  json.RawMessage
    CreatedBy     string
    CreatedAt     time.Time
}
```

Initial `EdgeType` set:
- `parent_of`
- `blocks`
- `verifies`
- `discovered_from`
- `supersedes`
- `relates_to`

### 2.3 Work Update

Append-only structured progress event.

```go
type WorkUpdate struct {
    UpdateID      string
    WorkID        string
    ExecutionState string
    ApprovalState  string
    Phase         string
    Message       string
    JobID         string
    SessionID     string
    ArtifactID    string
    MetadataJSON  json.RawMessage
    CreatedBy     string
    CreatedAt     time.Time
}
```

### 2.4 Work Note

Durable comment or feedback message.

```go
type WorkNote struct {
    NoteID        string
    WorkID        string
    NoteType      string
    Body          string
    MetadataJSON  json.RawMessage
    CreatedBy     string
    CreatedAt     time.Time
}
```

### 2.5 Work Proposal

A proposed structural graph change.

```go
type WorkProposal struct {
    ProposalID         string
    ProposalType       string
    State              string
    TargetWorkID       string
    SourceWorkID       string
    Rationale          string
    ProposedPatchJSON  json.RawMessage
    MetadataJSON       json.RawMessage
    CreatedBy          string
    CreatedAt          time.Time
    ReviewedBy         string
    ReviewedAt         *time.Time
}
```

Initial `ProposalType` set:
- `create_child`
- `promote_discovery`
- `add_edge`
- `remove_edge`
- `supersede_work`
- `change_acceptance`
- `reparent_work`

Initial proposal states:
- `proposed`
- `triaged`
- `accepted`
- `rejected`
- `withdrawn`

### 2.6 Attestation Record

Explicit evidence-bearing claim about a work item, proposal, artifact, or other
subject.

```go
type AttestationRecord struct {
    AttestationID            string
    SubjectKind              string
    SubjectID                string
    VerifierKind             string
    VerifierIdentity         string
    Method                   string
    Result                   string
    Summary                  string
    Confidence               float64
    Blocking                 bool
    JobID                    string
    SessionID                string
    ArtifactIDsJSON          json.RawMessage
    MetadataJSON             json.RawMessage
    SupersedesAttestationID  string
    CreatedBy                string
    CreatedAt                time.Time
}
```

`SubjectKind`:
- `work`
- `proposal`
- `artifact`
- `doc`
- `projection`

`Result`:
- `passed`
- `failed`
- `inconclusive`
- `matches`
- `drifted`
- `approved`
- `rejected`
- `superseded`

## 3) State Model

### 3.1 Execution State

Initial execution-state set:
- `proposed`
- `ready`
- `claimed`
- `in_progress`
- `blocked`
- `done`
- `failed`
- `cancelled`

Suggested rules:
- new accepted work defaults to `ready`
- claimed work should have a lease expiry
- `done` means execution finished, not that the result is accepted

### 3.2 Approval State

Initial approval-state set:
- `none`
- `pending`
- `verified`
- `rejected`
- `approved`

Suggested rules:
- implementation work often moves to `pending` when execution is
  done
- required attestations may gate readiness for approval
- `approved` should be explicit, not implied by `done`

### 3.3 Phase

`Phase` stays open-ended text at first.

Examples:
- `research`
- `implementation`
- `attesting`
- `review`
- `red_team`
- `handoff`

Do not over-model this in v1.

## 4) Edge Semantics

### 4.1 `parent_of`

Hierarchy only.

Meaning:
- `A parent_of B` means `B` is part of `A`
- it does not imply `B` is blocked on sibling work
- parent rollups can summarize child state

### 4.2 `blocks`

Hard prerequisite.

Meaning:
- `A blocks B` means `B` is not ready until `A` reaches a satisfying terminal
  condition
- default satisfying condition should be `ExecutionState=done`
- policy may later require approval, but not in v1

### 4.3 `verifies`

Approval relationship.

Meaning:
- `V verifies W` means `V` exists to attest to the quality of `W`
- completion of `V` may emit attestations that influence `W.ApprovalState`

### 4.4 `discovered_from`

Lineage only.

Meaning:
- `D discovered_from W` means `D` was found while doing `W`
- it does not block by default

### 4.5 `supersedes`

Replacement lineage.

Meaning:
- `B supersedes A` means `B` is the replacement path for `A`
- useful for retries, rewrites, or plan replacement

### 4.6 `relates_to`

Weak informational edge for navigation and search.

## 5) Ready Projection

`work ready` should answer "what can run now?"

A work item is ready if:
- `ExecutionState` is `ready`, or it is `claimed` with an expired lease
- it is not currently leased by another worker
- all incoming `blocks` edges are satisfied
- it is not superseded by newer active work
- its required capabilities match at least one available runtime option

The first implementation can compute this on demand in SQL/service code.
It does not need a materialized cache yet.

## 6) Mutation Policy

### 6.1 Direct Mutations

Workers may directly:
- claim/release work
- append updates
- add notes
- attach artifacts
- set `blocked`
- set `done`
- set `failed`
- create discovery proposals

Workers may also create child work directly only when local policy allows it.

### 6.2 Proposal-Based Structural Edits

Workers should use `work proposal` for:
- splitting work
- merging work
- adding/removing dependencies
- changing acceptance materially
- reparenting work
- promoting discoveries into tracked work
- superseding plans or active work

### 6.3 Approval-Gated Changes

Some proposal classes should require explicit approval:
- root-objective changes
- scope expansion
- deletion/supersession of accepted work
- attestation-policy changes
- security or budget-sensitive graph edits

Approval policy can remain simple in v1:
- host or supervisor decides
- runtime records the decision durably

## 7) CLI Surface

### 7.1 Worker-Safe Read Commands

- `cogent work show <work-id>`
- `cogent work notes <work-id>`
- `cogent work updates <work-id>`
- `cogent work children <work-id>`
- `cogent work artifacts <work-id>`
- `cogent work ready`

### 7.2 Worker-Safe Update Commands

- `cogent work claim <work-id>`
- `cogent work claim-next`
- `cogent work release <work-id>`
- `cogent work update <work-id> --state ... --phase ... --message ...`
- `cogent work note add <work-id> --type ... --text ...`
- `cogent work block <work-id> --message ...`
- `cogent work complete <work-id> --message ...`
- `cogent work fail <work-id> --message ...`
- `cogent work discover <work-id> --title ... --objective ...`

### 7.3 Host / Supervisor Commands

- `cogent work create`
- `cogent work list`
- `cogent work retry <work-id>`
- `cogent work proposal list`
- `cogent work proposal show <proposal-id>`
- `cogent work proposal accept <proposal-id>`
- `cogent work proposal reject <proposal-id>`
- `cogent work attest <work-id> ...`

### 7.4 Existing Command Integration

- `cogent run --work <work-id> ...`
- `cogent send --work <work-id> ...`
- `cogent artifacts list --work <work-id>`
- `cogent history search --work <work-id>` later, optional

### 7.5 Worker Briefing Contract

Workers should hydrate from a versioned compiled briefing, not a bespoke launch
prompt.

The stable contract is defined in
[docs/cogent-worker-briefing-schema.md](docs/cogent-worker-briefing-schema.md)
with the canonical JSON schema in
[schemas/worker-briefing.schema.json](schemas/worker-briefing.schema.json).

Lease semantics for v1:
- `work claim` moves `ready -> claimed`
- `work release` moves `claimed -> ready`
- expired claims should no longer block `work ready`
- terminal or blocked execution outcomes should clear the lease automatically

## 8) Storage Plan

Initial new tables:
- `work_items`
- `work_edges`
- `work_updates`
- `work_notes`
- `work_proposals`
- `attestation_records`

Minimal attachment changes to existing runtime tables:
- `jobs.work_id`
- `sessions.root_work_id` optional
- maybe `artifacts.work_id` for direct lookup

Suggested indexes:
- `work_items (execution_state, approval_state, updated_at)`
- `work_edges (to_work_id, edge_type)`
- `work_edges (from_work_id, edge_type)`
- `work_updates (work_id, created_at DESC)`
- `work_notes (work_id, created_at DESC)`
- `work_proposals (target_work_id, state, created_at DESC)`
- `attestation_records (subject_kind, subject_id, created_at DESC)`

## 9) Phased Implementation

### Phase 1: Core Work Records

Implement:
- `work_items`
- `jobs.work_id`
- `run --work`
- `work create/show/list`

Goal:
- attach work identity to real jobs immediately

### Phase 2: Updates And Notes

Implement:
- `work_updates`
- `work_notes`
- `work update`
- `work notes`
- `work complete/block/fail`

Goal:
- let workers publish durable progress

### Phase 3: Graph Edges And Ready

Implement:
- `work_edges`
- edge creation for `parent_of`, `blocks`, `verifies`, `discovered_from`
- `work children`
- `work ready`

Goal:
- make orchestration queryable

### Phase 4: Proposals And Attestations

Implement:
- `work_proposals`
- `attestation_records`
- `work discover`
- `work proposal accept/reject`
- `work attest`

Goal:
- make graph evolution explicit and governable

### Phase 5: Projections And Docs

Implement:
- checklist view
- status/frontier view
- deterministic doc projections

Goal:
- bridge work state back into readable docs and reports

## 10) Example Bash Shapes

Sequential plan-implement-verify:

```bash
root=$(cogent --json work create \
  --title "Add work graph" \
  --kind plan \
  --objective "Design and implement the first work runtime layer" | jq -r .work.work_id)

impl=$(cogent --json work create \
  --parent "$root" \
  --kind implement \
  --title "Store and CLI" \
  --objective "Add schema and read/update commands" | jq -r .work.work_id)

job=$(cogent --json run \
  --work "$impl" \
  --adapter codex \
  --cwd /repo \
  --prompt "Hydrate from work and implement the feature." | jq -r .job.job_id)

cogent --json status --wait "$job"
```

Discovery during execution:

```bash
cogent --json work discover work_impl \
  --title "Add verifier gate" \
  --objective "Verification should become first-class work"
```

Progress update from a worker:

```bash
cogent --json work update work_impl \
  --state in_progress \
  --phase implementation \
  --message "Store schema added; wiring CLI commands now."
```

## 11) Acceptance For This Spec

The spec is good enough to implement when:
- the initial tables and fields are unambiguous,
- the first edge types have clear semantics,
- `ready` is concretely defined,
- worker-safe versus supervisor commands are clearly separated,
- and the phased plan lets us land useful slices without a giant branch.
