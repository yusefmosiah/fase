Date: 2026-03-13
Kind: Product spec + implementation target
Status: Draft v0
Priority: 1
Requires: [docs/fase-work-runtime.md, docs/fase-work-api-and-schema.md, docs/fase-worker-briefing-schema.md]
Owner: Runtime / Local Control Plane

## Narrative Summary (1-minute read)

`fase` v0 should become a local work-control plane with a web UI.

The source of truth is the SQLite-backed work graph plus Git-backed repository
state. Markdown docs, the web board, and worker briefings are all projections
of that state. They are not independent stores.

Attestation is the centerpiece. Work should not be considered complete merely
because an agent says so. The system records durable attestations from tests,
scripts, agent reviewers, computer-use verifiers, humans, and other evaluation
lanes. Attestations are orthogonal to work phases — any agent doing any work
may also verify, attest, or defer to the next agent. Human approval consumes
summarized attestation bundles and linked artifacts rather than raw transcripts.

Agents may always stop. The system may always resume. This is the core
invariant. Work hydration compiles a deterministic briefing from the work graph
so any agent — same adapter or different — can pick up where the last left off.

Remote work is explicitly deferred to v1. v0 is local-only.

## What Changed (v0 revision 2)

1. Defined the v0 product as a local board + runtime control surface.
2. Made attestation an orthogonal system, not a terminal phase or a task kind.
   Any agent may verify and attest during any work.
3. Removed stage ordering. Work items carry an optional `phase` label for human
   orientation. The runtime does not enforce phase transitions. Dependency
   edges between work items are explicit, not implied by stage position.
4. Defined human locks and bounded concurrency as explicit runtime semantics.
5. Anchored provenance to Git rather than inventing a second content-history
   system.
6. Made docs a projection of the work graph, not a hand-maintained parallel
   store. SQLite is the source of truth; markdown is a rendering.
7. Defined work hydration as two modes: cold (deterministic, no LLM) and
   debrief (resume original agent for self-report). Recovery is just
   re-assignment — the recovery agent investigates agentically using runtime
   read commands.
8. Defined the reachability attestation pattern: impl guides are attestations
   against specs, not governed tasks. Specs and reachability attestations
   iterate in a loop until both cohere, then implementation begins.
9. Deferred remote execution and remote workspace ownership to v1.

## What To Do Next

1. Rename `VerificationRecord` to `AttestationRecord`; add missing fields
   (confidence, blocking, method, verifier_kind, verifier_identity,
   supersedes_attestation_id). Do this before more data accumulates.
2. Implement `work hydrate` — deterministic briefing compilation from work
   graph state, with optional debrief mode.
3. Add heartbeat renewal to the lease model. Without it, lease duration is a
   hard timeout on worker execution.
4. Add human lock state to work items.
5. Add approval ledger table and promotion records.
6. Add Git anchors to work items, jobs, and approvals.
7. Build a narrow web UI that renders and mutates the local runtime.

## 1) Product Direction

`fase` v0 is a local runtime for governed agent work.

The target operator experience is:
- capture ideas and work quickly from the terminal
- define or bootstrap work locally
- inspect a board view of work items
- drill into card details and evidence
- lock/unlock items
- run agents against ready work
- review attestation results
- approve or reject work based on evidence

This is not a hosted SaaS control plane.
This is not a tracker-centric product.
This is not remote-first orchestration.

## 2) Scope And Non-Goals

### 2.1 In Scope For v0

- local work graph state (SQLite)
- local board and card-detail UI (projection)
- deterministic projections from runtime state (markdown, JSON, web)
- explicit work-item mutations via command vocabulary
- human locks
- dependency edges between work items
- bounded concurrency (operator-controlled)
- attestation recording across many verifier kinds
- work hydration (cold + debrief)
- quick capture / inbox workflow
- Git-linked provenance for approvals and promotions

### 2.2 Explicitly Out Of Scope For v0

- remote workers
- SSH-managed workspaces
- remote execution hosts
- remote-first issue tracker ownership
- full automatic concurrency optimization
- generalized workflow DSL
- replacing Git history with a custom ledger
- enforced stage ordering

Remote work is a v1 feature.

## 3) Source Of Truth

v0 has three durable truth layers:

1. `fase` work graph (SQLite)
   - work items
   - edges
   - updates
   - notes
   - attestations
   - approvals
   - promotions
2. repository filesystem state
   - code
   - generated reports
   - screenshots/videos
   - other artifacts
3. Git object history
   - commits
   - trees
   - branches/worktrees
   - refs/tags

Markdown docs, the web board, and worker briefings are projections of these
layers. They must not silently become independent state stores.

Design work — including interactive sessions, research, and spec drafting — is
captured in the work graph as work items, notes, attestations, and artifacts.
Rendered markdown is a projection of that state (see section 15).

## 4) Core User Model

The operator should be able to think in these terms:

- `work item`
  - a unit of work at any granularity: idea, bug, feature, task
- `attestation`
  - a durable claim with evidence about some subject
- `approval`
  - a human or policy decision over attested work
- `dependency`
  - an explicit edge: this work blocks or requires that work

The point is not merely to run agents.
The point is to govern work, evidence, and acceptance.

## 5) Board Model

The board is a projection of runtime state.

Suggested v0 columns:
- `Inbox`
- `Ready`
- `Running`
- `Blocked`
- `Human Lock`
- `Awaiting Approval`
- `Approved`
- `Done`

Column placement rules (derived from runtime state, not drag-only UI):

- `Inbox` — execution_state=ready, kind=idea, no parent edge
- `Ready` — execution_state=ready (not inbox-qualifying)
- `Running` — execution_state=claimed or in_progress
- `Blocked` — execution_state=blocked
- `Human Lock` — lock_state=human_locked (regardless of execution state)
- `Awaiting Approval` — execution_state=done, all blocking attestation policy
  slots satisfied, approval_state != verified
- `Approved` — approval_state=verified (approval ledger surface, not a pile)
- `Done` — execution_state=done with no attestation policy, or
  execution_state=done with approval_state=verified and promoted=true, or
  execution_state=cancelled/failed (terminal)

Work items without an attestation policy skip `Awaiting Approval` and go
directly from execution_state=done to `Done`. Promoted items move from
`Approved` to `Done`. Failed and cancelled items appear in `Done` with
distinct visual treatment.

Additional notes:
- moving into `Human Lock` creates a durable lock mutation
- human lock takes visual priority: a locked running item shows in `Human Lock`

### 5.1 Card Summary

Each card should show at minimum:
- title
- work kind
- current phase (optional, human-oriented label)
- execution state
- approval state
- lock state
- dependency/blocker counts
- attestation summary badges
- linked branch/worktree status when present

### 5.2 Card Detail

Each card detail should show:
- full objective
- acceptance criteria
- child work items
- notes and updates
- jobs and sessions
- artifacts
- attestations
- approval history
- Git anchors

### 5.3 Visible Actions

The UI should expose explicit actions only.

These actions form the command vocabulary. They are the only way to mutate
state. In v1, this same vocabulary becomes the remote broker protocol.

Minimum actions:
- claim
- release
- lock
- unlock
- block
- unblock
- record note/update
- attach artifact
- record attestation
- request approval
- approve
- reject
- spawn run
- hydrate

## 6) Work Model For v0

The work graph is the execution and dependency truth.

Work items have a `kind` that describes what the work is. Kinds do not imply
ordering or lifecycle position — that is determined by execution state,
dependency edges, and attestations.

Useful v0 work kinds:
- `idea`
- `bug`
- `feature`
- `task`
- `research`
- `review`
- `security`

Work items may carry an optional `phase` label (e.g., "designing", "spiking",
"waiting on review") for human orientation. The runtime does not enforce phase
transitions. Phases are informational, not governed.

### 6.1 Attestation Policy

Each work item may carry a `required_attestations` field: a list of verifier
kinds that must have a passing result before the work can move to `Awaiting
Approval`. This is the machine-readable definition of "good enough."

Example:

    required_attestations:
      - verifier_kind: deterministic
        method: test
        blocking: true
      - verifier_kind: code_review
        blocking: true
      - verifier_kind: security
        blocking: false

The runtime uses this to compute:
- whether a work item's required attestations are resolved
- when to move a card to `Awaiting Approval` on the board
- what "required attestations resolved" means in the approval flow (section 12)

Attestation freshness rule: an attestation satisfies a policy slot only if its
`metadata.commit_oid` matches the work item's current `head_commit_oid`. When
new code lands (head moves), prior attestations become stale and must be re-run
against the new head. If a work item has no Git anchor (e.g., a pure research
item), attestations are evaluated without a commit constraint.

When multiple attestations of the same verifier kind exist for the same commit,
the latest non-superseded attestation wins. Superseded attestations are
retained for provenance but do not satisfy policy slots.

If `required_attestations` is empty or absent, the work item has no
attestation gate — it can be approved based solely on human judgment.

### 6.2 Attestable Actions

Verification and documentation updates are not separate task kinds. They are
attestable actions available to any worker at any time.

Any agent doing implementation work may also:
- run tests and record a passing/failing attestation
- update docs and record a docs-consistency attestation
- review neighboring code and record a review attestation
- defer any of these to the next agent

The work graph tracks whether required attestations are satisfied, not whether
a verification task was completed. "Does this work have the attestations its
policy requires?" is the question, not "did we schedule a test task?"

### 6.3 Quick Capture

Low-friction capture is essential. The inbox pattern should be:

    fase inbox "thing I just thought of"
    fase inbox "bug: X breaks when Y" --kind bug

This is shorthand for `work create --kind idea`. Items land in the Inbox
column. Triage promotes, links, details, or cancels them.

## 7) Dependency Edges

Work items are related through explicit edges, not implicit stage position.

Edge types:
- `parent` — structural containment (tree-constrained, see below)
- `blocks` — A must complete before B can start
- `verifier` — A attests to the quality of B
- `discovery` — A was discovered during work on B
- `supersedes` — A replaces B

Structural constraints on `parent` edges:
- A work item has at most one inbound `parent` edge (single parent).
- `parent` edges must form an acyclic forest (no cycles).
- The runtime rejects `parent` edge creation that would violate either rule.
- Work items with no inbound `parent` edge are root items. Root identity is
  used for concurrency accounting (section 10).

The runtime enforces `blocks` edges: a work item with unresolved blocking
inbound edges cannot become ready. All other edge types are informational.

A feature that needs design-before-implementation creates an explicit `blocks`
edge between the design work item and the implementation work item. This is a
deliberate choice by the planner or human, not a default stage ladder.

## 8) Notes And Compaction

Notes accumulate over the life of a work item. They are evidence from a point
in time, not permanent truth.

Notes carry `created_at` and an optional `supersedes_note_id`. When a note
supersedes another, the superseded note is retained for provenance but demoted
in hydration ranking.

Staleness is managed through three mechanisms:

1. **Recency ranking**: the briefing compiler sorts notes by recency and
   truncates. Old notes naturally fall off the hydration window.
2. **Compaction**: when notes exceed a threshold or when explicitly requested,
   an agent reviews accumulated notes and produces a compacted summary note
   that supersedes the originals. This is an attestable action — the compacted
   note is evidence that someone reviewed and condensed the history.
3. **State-scoped relevance**: notes from earlier execution states (e.g., notes
   from when the work was `ready` are less relevant once it's `in_progress`)
   are ranked below notes from the current state.

Compaction is not deletion. Superseded notes remain queryable for audit and
provenance. But hydration surfaces the compacted summary, not the raw
accumulation.

The rule: **notes accumulate, compaction summarizes, hydration truncates.**

## 9) Human Locks

Human locks are first-class runtime state.

Minimum lock states:
- `unlocked`
- `human_locked`

Rules:
- locked work is never auto-claimed
- a locked item remains visible on the board in the `Human Lock` column
- downstream work blocked by the locked item remains not-ready
- lock/unlock actions must be attributable and durable

The kanban column is a projection of the lock.
The lock is not a UI-only affordance.

## 10) Concurrency Model

v0 should keep concurrency policy intentionally simple.

A **root work item** is any work item with no parent edge. These are the
top-level units of concurrent work — typically features, but also standalone
bugs, research, or ideas that have been promoted to active work.

The human sets:
- maximum active root items (the concurrency cap)
- allowed adapters and/or routing mode

A root item is **active** when any work item in its subtree (itself or any
descendant via parent edges) has execution_state `claimed` or `in_progress`.

The runtime enforces:
- the concurrency cap: `ClaimNextWork` will not claim work whose root item
  would exceed the maximum active root items count
- dependency edges between work items
- human locks
- only-ready work dispatch

Child work items inherit their root identity by walking parent edges to the
root. Work items with no parent are their own root. This is a graph traversal,
not a stored field — it stays consistent as edges are added or removed.

v0 should not attempt automatic token-budget optimization beyond simple
operator-controlled limits and routing policy.

## 11) Attestations

Attestations are first-class and orthogonal to work phases.

Any agent, tool, script, or human may record an attestation about a subject.
This includes attestations produced as side-effects during other work — an
agent implementing a feature may also run tests and attest the results.

Subject kinds:
- `work_item`
- `job`
- `session`
- `artifact`
- `doc`
- `projection`
- `graph_edge`
- `release_candidate`

Result semantics:
- `passed`
- `failed`
- `inconclusive`
- `matches`
- `drifted`
- `approved`
- `rejected`
- `superseded`

Attestation fields:
- `attestation_id`
- `subject_kind`
- `subject_id`
- `verifier_kind`
- `verifier_identity`
- `method`
- `result`
- `summary`
- `confidence`
- `blocking`
- `job_id`
- `session_id`
- `artifact_ids`
- `metadata`
- `created_by`
- `created_at`
- `supersedes_attestation_id`

Verifier kinds:
- `deterministic` — tests, lints, type checks, schema validators, link
  checkers. Safe, no hallucination risk, run freely.
- `browser_e2e` — Playwright, Cypress, etc.
- `computer_use` — vision-model flow verification
- `observability` — performance, latency, resource usage
- `load_stress` — load and stress testing
- `security` — adversarial, penetration, threat model
- `code_review` — structural review by agent or human
- `llm_as_judge` — LLM evaluation of artifacts or behavior
- `human_review` — human inspection and judgment
- `formal` — proof-backed verification
- `worker_debrief` — self-report from the agent that did the work
- `recovery_review` — third-party review of another agent's session log
- `reachability_analysis` — attestation that a viable implementation path
  exists from current state to specified end state
- `docs_consistency` — structural or semantic docs verification

The method field distinguishes how the attestation was produced:
- `deterministic` — script/tool output, fully reproducible
- `self_report` — the working agent's own assessment
- `third_party_review` — independent assessment by a different agent
- `human` — human judgment

The system should treat raw artifacts and verifier judgments as separate things.
A Playwright video is evidence. A vision-model judgment over that video is an
attestation.

Attestations are how the system accumulates durable evidence and later computes
agent reliability statistics.

### 10.1 Reachability Attestations

Implementation guides are not governed tasks. They are reachability
attestations against specs.

The workflow:
1. A spec defines the desired end state (work item with objective and
   acceptance criteria).
2. An agent or human produces a reachability analysis: does a viable path exist
   from the current state to the end state? What are the steps? What are the
   gaps?
3. This analysis is recorded as an attestation with `verifier_kind:
   reachability_analysis`.
4. If the analysis finds gaps, the spec is updated. The analysis is re-run.
5. This loop continues until the spec and the reachability attestation cohere.
6. Implementation begins.

The reachability attestation is not a plan to follow — the implementing agent
may take a different path. It is evidence that a path exists, which de-risks
the work before committing agent resources.

### 10.2 Deterministic Docs Consistency

Docs consistency checks that do not require an LLM:
- structural: required frontmatter fields present, valid values
- referential: internal links resolve, `Requires:` targets exist
- freshness: referenced file paths still exist in the codebase
- schema: examples validate against declared schemas
- acyclicity: dependency graph has no cycles
- drift: regenerated projections match committed snapshots

These are scriptable, produce attestations with `method: deterministic`, and
can run as pre-commit hooks or CI checks.

Semantic consistency (do the docs accurately describe the code?) requires an
LLM reviewer. That reviewer must be read-only — it produces attestations, never
writes docs directly. A human or governing agent decides what to do with a
`drifted` attestation.

## 12) Approval And Promotion

Approval is not identical to execution completion.

v0 should keep these states distinct:
- execution finished
- required attestations resolved (per the work item's attestation policy,
  section 6.1)
- human approved
- promoted

The runtime computes "required attestations resolved" by checking whether
every entry in the work item's `required_attestations` with `blocking: true`
has a corresponding passing attestation. When all blocking attestations are
satisfied, the card moves to `Awaiting Approval`.

Approval consumes attestation bundles and linked artifacts.
Promotion consumes approvals.

This allows:
- a feature to be implemented but unapproved
- a feature to be approved but not yet promoted
- a promoted item to later be superseded or reverted

### 11.1 Approval Ledger

The `Approved` board column is an ordered approval ledger surface.

It is not a blockchain.
It is not a second Git.
It is a chronological list of approval events whose subjects are anchored to
Git objects and runtime attestations.

Approval-event fields:
- `approval_id`
- `work_id`
- `approved_commit_oid`
- `attestation_ids`
- `approved_by`
- `approved_at`
- `status`
- `supersedes_approval_id`

Reverts and rebases should create new approval/promotion events rather than
erasing prior provenance.

## 13) Work Hydration

Work hydration compiles a deterministic briefing from the work graph so that
any agent can pick up work — whether as a fresh assignment, a continuation, or
a recovery.

### 12.1 Cold Hydration

Always available. No LLM required. Deterministic compilation from SQLite.

Inputs:
- work item record (objective, acceptance, kind, phase, state)
- graph context (parent, blockers, children, attestations)
- recent updates, notes (bounded, sorted by recency)
- recent artifacts (bounded)
- recent jobs (bounded)
- relevant history matches (bounded)

Output: JSON conforming to `schemas/worker-briefing.schema.json`.

Three density modes control truncation cutoffs:
- `thin` — minimal evidence, fast orientation
- `standard` — bounded evidence, default
- `deep` — higher cutoffs, more context

### 12.2 Debrief Hydration

Optional enrichment. Resumes the original agent's session and asks it to write
a structured handoff report before recompiling the briefing.

Requires: the original adapter is available and the session is resumable.

The debrief prompt is partly consistent (progress, unfinished, blockers,
findings, recommendations, files) and partly contextual (adapted to acceptance
criteria, existing attestations, work kind, blocking edges).

The debrief report is stored as an artifact and recorded as an attestation
with `method: self_report`, `verifier_kind: worker_debrief`.

### 12.3 Recovery

Recovery is not a hydration mode. It is re-assignment.

When the original adapter is unavailable (rate-limited, crashed, incompatible),
the work is simply assigned to a new agent. The briefing's `briefing_kind:
recovery` signals that the previous worker didn't finish.

The recovery agent investigates agentically using runtime read commands:
- `fase work show <id>` — work item with neighborhood
- `fase logs <job-id>` — session logs for a specific job
- `fase session <session-id>` — session detail and job list
- `fase artifacts list --work <id>` — artifacts linked to work
- `fase history search` — cross-reference with canonical history

How deeply it investigates is its decision. It may glance at the last few
turns and continue. It may spawn subagents to review the full session. It may
cross-reference with other sources. The adapter is a black box — different
adapters can work differently.

### 12.4 The Resume Invariant

Agents may always stop. The system may always resume.

This requires:
- **Lease renewal (heartbeat)**: long-running workers can extend their lease.
  Without this, lease duration is a hard timeout.
- **Partial progress capture**: the agent's work-so-far is recoverable through
  turns, events, and artifacts already stored in the work graph.
- **No implicit task coupling**: if an agent stops mid-verification, that just
  means no attestation was recorded yet. The work item's attestation count is
  the signal, not a task completion state.

### 12.5 CLI

    fase work hydrate <work-id>                    # cold, standard density
    fase work hydrate <work-id> --debrief          # resume original agent
    fase work hydrate <work-id> --mode deep        # more evidence
    fase work hydrate <work-id> --mode thin        # minimal, fast

## 14) Git Integration

Git already provides the content-history ledger.
`fase` should integrate with Git, not reimplement it.

At minimum, `fase` records should be able to point at:
- `base_commit_oid`
- `head_commit_oid`
- `branch_ref`
- `worktree_path`
- `tree_oid` when useful

Approvals and promotions should reference Git state rather than duplicate Git's
hash chain.

Good v0 Git integration:
- one branch/worktree per active feature when practical
- commit anchors on jobs, artifacts, and approvals
- durable refs or tags for promoted staging/prod heads

## 15) Docs As Projections

SQLite is the source of truth. All documentation is a projection of the work
graph.

Design happens through interactive sessions — conversations, research,
prototyping. The outputs of those sessions are captured in the work graph as
work items, notes, updates, attestations, and artifacts. Markdown documents are
projected views of that state, not independent stores.

This means there is no separate category of "authored docs" that lives outside
the work graph. A design spec like this one is the projected state of a design
work item. A review finding is an attestation. A decision rationale is a note.
The work graph holds the structured data; projections render it for humans.

Generated views cannot drift because they are compiled from the database on
demand.

Projection commands:
- `fase work projection <work-id> --format md` — renders a work item as
  markdown (objective, acceptance, notes, attestations, history)
- `fase project atlas` — renders an index of all work from the graph
- `fase work hydrate <work-id>` — compiles a worker briefing as JSON

Projected markdown can be committed to Git as snapshots for provenance, but
these are generated artifacts, not source-of-truth documents.

## 16) Stats And Provenance

The system should eventually compute agent quality from attested outcomes
rather than self-reported success.

Useful future stats:
- accepted work rate
- verifier disagreement rate
- reopen rate
- drift rate after approval
- cost/time per approved feature
- reliability by adapter/model/verifier kind

v0 only needs to store enough provenance to make these future stats possible.

## 17) Web UI Contract For v0

The first web UI should remain intentionally narrow.

Required views:
- board
- work detail
- artifact viewer/list
- attestation list
- approval history

Required backend capabilities:
- list work items
- show one work item and its neighborhood
- show projections
- mutate lock/state/note/update fields
- record attestations
- trigger runs
- inspect jobs/sessions/artifacts
- hydrate work items

The UI should be able to render deterministic text projections directly from
the runtime where helpful rather than re-implementing all summarization logic
in the frontend.

## 18) Supervision Model (v0 Local, v1-Ready)

v0 supervision is local-only but designed to extend to remote workers in v1.

Six guarantees:

1. **Desired state vs observed state**: every work item has durable intended
   state. The runtime can tell whether reality matches it.
2. **Lease + heartbeat**: a worker owns work via a lease. If heartbeat stops,
   the lease expires and the work becomes recoverable.
3. **Idempotent launch/cancel**: starting the same logical run twice does not
   create semantic duplication. Cancelling is safe to retry.
4. **Startup reconciliation**: on restart, `fase` inspects store state,
   detects orphaned/running/unknown jobs, and marks or reattaches them.
5. **Bounded event capture**: stdout/stderr/tool events/artifacts are durably
   captured enough to explain what happened, without requiring a live parent
   process.
6. **Explicit terminal outcomes**: done, failed, cancelled, lost, superseded.

`fase` supervises work, leases, attestations, and approvals.
`fase` does not supervise machines, VMs, or OS processes.

In v1, the command vocabulary (section 5.3) becomes the remote broker protocol.
The local implementation calls the service directly. The remote implementation
calls vsock (same-host) or HTTP+mTLS (cross-host). The vocabulary is the same.

## 19) Initial Implementation Plan

Phase 1:
- rename VerificationRecord to AttestationRecord with full field set
- add human lock state to work items
- add heartbeat renewal to lease model
- add `lost` and `superseded` terminal outcomes
- add startup reconciliation

Phase 2:
- implement `work hydrate` (cold compilation from work graph)
- implement debrief hydration mode
- implement `inbox` shorthand command
- add Git anchors to relevant records

Phase 3:
- add approval ledger table and promotion records
- expose projection-friendly APIs/CLI JSON
- build the local board and card detail UI

Phase 4:
- support approval and promotion flows over attested work
- add deterministic docs-consistency verifier
- committed snapshot generation

## 20) v1 Boundary

v1 may add:
- remote workers via brokered execution API (work-level verbs, not VM-level)
- vsock transport for same-host guest-to-host communication
- HTTP+mTLS for cross-host worker pools
- remote runtime inventory
- external tracker mirroring/integration
- richer automatic scheduling and budget controls
- broker-enforced capability scope, identity, and concurrency policy

Those are not required to prove the v0 product.

The minimum remote-aware design principle:
- machines are supervised by the host (systemd, hypervisor)
- work is supervised by `fase`

## 21) Done Criteria For This Spec

This spec is fulfilled when:
- a local board exists
- the board is driven by runtime state, not ad hoc frontend state
- human locks are durable
- dependency edges are enforced
- attestations are recorded and inspectable
- approvals point to evidence and Git state
- work hydration produces deterministic briefings
- agents can stop and resume via the work graph
- remote work is not required for ordinary use
