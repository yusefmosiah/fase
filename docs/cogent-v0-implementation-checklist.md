Date: 2026-03-13
Kind: Implementation checklist
Status: Active
Priority: 1
Requires: [docs/cogent-v0-local-control-plane.md, docs/cogent-work-runtime.md, docs/cogent-work-api-and-schema.md, docs/cogent-worker-briefing-schema.md]
Owner: Runtime / Local Control Plane

## Goal

Turn the v0 local control-plane spec into a reachable implementation order.

This is not another design document. It is the execution sequence that keeps
the runtime coherent while the work graph, board, and hydration surfaces are
brought up.

## Slice Order

### Slice 0: Naming + Store Substrate

- rename `VerificationRecord` to `AttestationRecord`
- rename `work verify` to `work attest`
- add attestation fields:
  - `confidence`
  - `blocking`
  - `method`
  - `verifier_kind`
  - `verifier_identity`
  - `supersedes_attestation_id`
- add work-item fields:
  - `lock_state`
  - `required_attestations`
  - `head_commit_oid`
- make ready/claim paths respect `lock_state=human_locked`
- migrate old verification rows into the new attestation table

Exit criteria:
- runtime compiles
- `work show` exposes attestations
- `work attest` records evidence
- locked work is not returned by `work ready` or `work claim-next`

### Slice 1: Approval Semantics

- stop auto-promoting passed attestations into approval
- add explicit `work approve` / `work reject`
- compute required-attestation resolution from:
  - work policy slots
  - current `head_commit_oid`
  - latest non-superseded attestation per slot
- set `approval_state=pending` when done work has blocking attestation policy

Exit criteria:
- approval state is no longer overloaded with verifier output
- approval fails when blocking slots are unresolved
- no-policy work can finish without approval

### Slice 2: Parent/Concurrency Guardrails

- enforce single-parent acyclic `parent` edges
- derive root identity by walking parent edges
- add service helper for root-aware concurrency accounting
- keep operator-set concurrency knobs out of automation for now

Exit criteria:
- parent edge creation rejects ambiguity/cycles
- root identity is deterministic

### Slice 3: Hydration

- implement deterministic `CompileWorkerBriefing(workID, mode)`
- add `cogent work hydrate <work-id>`
- compile from runtime state only
- include:
  - assignment
  - requirements
  - local graph context
  - recent evidence
  - worker contract
  - hydration summary

Exit criteria:
- hydration output validates against
  [schemas/worker-briefing.schema.json](schemas/worker-briefing.schema.json)
- repeated runs over unchanged state are stable

### Slice 4: Approval/Promotion Ledger

- add approval records anchored to Git state
- add promotion records for staging/production refs
- stop treating board `Approved` as a raw state bucket

Exit criteria:
- approval and promotion have durable provenance
- Git remains the content-history source of truth

### Slice 5: Local Web UI

- board projection over local runtime state
- card detail pane
- explicit mutation actions only
- approval/evidence surfaces

Exit criteria:
- local board is usable without external tracker ownership

## Immediate Next Moves

1. Land Slice 0 and Slice 1 together if the approval transition stays small.
2. Run service + CLI work-path tests after each slice.
3. Start the web UI only after hydration and approval semantics are stable.
