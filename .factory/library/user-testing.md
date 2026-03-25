# User Testing

Testing-surface findings and validator guidance for the readiness mission.

**What belongs here:** authoritative user-facing surfaces, validation tools, runtime setup, concurrency guidance, and known limitations.
**What does NOT belong here:** implementation details that belong in architecture or environment notes.

---

## Validation Surface

Authoritative surfaces for this mission:

- CLI
- local `fase serve` HTTP/API surface
- browser UI via `agent-browser`

### Dry-run readiness results

- `make build`: passed during planning dry run
- `make lint`: passed during planning dry run
- focused readiness-area tests: passed during planning dry run
- local `fase serve` was started successfully on port `5380`
- API smoke succeeded against:
  - `/api/runtime`
  - `/api/dashboard`
  - `/api/work/ready`
  - `/api/git/status`
- browser validation succeeded using `agent-browser` against the served UI, including interactive snapshot and annotated screenshot capture

### Known limitations at mission start

- `make test` is red at baseline and is reserved as an end-of-mission gate unless a feature explicitly targets full-suite stabilization earlier
- Final readiness baseline should still aim to make `make test` pass before mission completion

## Validation Concurrency

- Machine observed during planning: 8 logical CPUs, 16 GiB RAM
- Focused validation cost: medium
- Full repo `make test` cost: heavy

Recommended limits:

- Focused validation: up to **2** concurrent lanes
- Full `make test`: **1** concurrent lane (serialize)

## Tooling Guidance

- For UI/browser validation, use `agent-browser` and capture an annotated screenshot
- For runtime validation, prefer one temporary local `fase serve` instance at a time inside the mission port range `5380-5389`

## Flow Validator Guidance: contract-freeze

### Isolation Rules

Contract-freeze assertions test work-item lifecycle and attestation/review contract behavior. All assertions share the same work-graph state and should run in a single validator to avoid interference.

### Resources and Boundaries

- Use the already-running `fase serve` on port `5380`
- Do NOT start additional serve instances
- Work items created for testing should use distinct identifiers to avoid collision
- Clean up any test work items after validation (or leave them for synthesis inspection)

### Assertions to Test

- **VAL-CONTRACT-003**: Review contract freezes and can only become stricter after first live execution. Verify that once a work item begins first live execution, its review contract becomes durable and cannot be weakened. Stricter changes must flow through an explicit audited escalation path.
- **VAL-CONTRACT-004**: Terminal success is gated by one frozen completion contract. Verify that terminal success cannot be recorded until all blocking review requirements are satisfied, and exactly one canonical path owns final success authorization.
- **VAL-CONTRACT-005**: Code, docs, and persisted work-graph state share one explicit precedence rule. Verify that README, ADR/spec docs, and CLI help text describe one explicit precedence rule for resolving conflicts between runtime code, committed docs, and persisted work-graph state.

### Testing Approach

1. Use CLI commands to create and manipulate work items with attestation requirements
2. Use HTTP API to inspect work item state and attestation records
3. Verify frozen contract behavior by attempting to weaken review requirements after first dispatch
4. Verify escalation path by attempting stricter changes and checking for audited escalation records
5. Verify precedence rule by inspecting committed docs and comparing to runtime help text

## Flow Validator Guidance: supervisor-wake-causality

### Isolation Rules

Supervisor wake assertions share the same runtime event stream and work graph, so validate them in one serialized lane to avoid cross-talk from shared state or overlapping supervisor turns.

### Resources and Boundaries

- Use the already-running `fase serve` on port `5380`
- Do NOT start additional serve instances
- Prefer CLI and HTTP/API assertions for wake/provenance traces; only use browser UI if an assertion unexpectedly requires it
- Keep all test work under a unique, milestone-scoped prefix so provenance traces stay easy to attribute

### Assertions to Test

- **VAL-SUPERVISOR-001**: Only actionable events wake the supervisor.
- **VAL-SUPERVISOR-002**: Supervisor-originated mutations never self-wake.
- **VAL-SUPERVISOR-003**: Wake-relevant events carry trustworthy provenance across CLI, HTTP, and MCP transport boundaries.
- **VAL-SUPERVISOR-004**: External worker, checker, attestation, host, and housekeeping signals wake exactly when needed.
- **VAL-SUPERVISOR-005**: Idle suppression, burst batching, and recovery avoid churn without losing context.
- **VAL-SUPERVISOR-006**: Self-wake suppression never hides later legitimate external events.

### Testing Approach

1. Use CLI commands to create or mutate work items and trigger supervisor-relevant events
2. Use HTTP API snapshots and event traces to confirm provenance and wake behavior
3. Verify supervisor turn counts and event logs to prove no self-wake loops or missed actionable wakeups
4. Keep the entire milestone in a single validator run so event ordering remains deterministic

## Flow Validator Guidance: lifecycle-normalization

### Isolation Rules

Lifecycle normalization assertions test canonical lifecycle vocabulary, deprecated state handling, dispatchability, claim/lease semantics, job-to-work mapping, attestation children, and retry/reset behavior. All assertions share the same work-graph state and should run in a single serialized validator to avoid interference from concurrent state mutations.

### Resources and Boundaries

- Use the already-running `fase serve` on port `5380`
- Do NOT start additional serve instances
- Use CLI and HTTP/API for primary testing; browser UI only if explicitly required by an assertion
- Create test work items with unique milestone-scoped identifiers (e.g., prefix with "lifecycle-norm-test-")
- Clean up test work items after validation or leave them for synthesis inspection

### Assertions to Test

- **VAL-CONTRACT-001**: Canonical lifecycle vocabulary is singular. Verify that all runtime surfaces (CLI JSON, HTTP, MCP, work detail) expose only one canonical lifecycle vocabulary with one meaning per state.
- **VAL-CONTRACT-002**: Deprecated lifecycle names are normalized or rejected. Verify that deprecated lifecycle names are either rejected on write or normalized to canonical states on read, and never survive as separate active states in normal runtime output.
- **VAL-LIFECYCLE-001**: Ready listing returns only genuinely dispatchable work. Verify that ready/dispatchable listing contains only work currently eligible for dispatch under the explicit availability contract.
- **VAL-LIFECYCLE-002**: Claim, lease, and release semantics match the canonical lifecycle. Verify that claiming, renewing, releasing, and expiry manipulate ownership consistently without creating illegal lifecycle transitions or bypassing review gates.
- **VAL-LIFECYCLE-003**: Job states map deterministically to canonical work states. Verify that queued, running, completed, failed, cancelled, and retry/reset job outcomes normalize into one deterministic work-state contract with no ambiguous dependency on legacy state names.
- **VAL-LIFECYCLE-004**: Attestation child creation and parent aggregation are first-class and idempotent. Verify that worker completion creates exactly the required child set once, links it durably in the work graph, and parent aggregation resolves deterministically from child outcomes.
- **VAL-LIFECYCLE-005**: Retry/reset re-enters the canonical path without stale state leakage. Verify that retrying or resetting work returns it to the single canonical dispatch path without stale leases, obsolete review artifacts, deprecated active states, or stale attempt-linkage fields that make the new run look already reviewed.

### Testing Approach

1. Use CLI commands to create work items with various lifecycle states and attestation requirements
2. Use HTTP API to inspect work item state, job mappings, attestation records, and attempt epochs
3. Verify canonical vocabulary by checking CLI JSON output, HTTP responses, and work detail views for absence of deprecated state names
4. Verify dispatchability by testing ready listings under various dependency, supersession, and review-gate scenarios
5. Verify claim/lease/release behavior with state and claimant field read-back after each operation
6. Verify job-to-work mapping by running jobs through various outcomes and checking resulting work states
7. Verify attestation children by creating parent work with review policy and checking child creation and aggregation
8. Verify retry/reset by resetting work items and confirming clean re-entry state with no stale linkage
9. Keep the entire milestone in a single validator run to ensure deterministic state progression

## Flow Validator Guidance: verification-unification

### Isolation Rules

Verification unification assertions test the canonical verification lifecycle, check submission contract across surfaces, evidence requirements, completion gating, reviewer evidence bundle, and verify terminology. All assertions share the same work-graph state and should run in a single serialized validator to avoid interference from concurrent check submissions and state mutations.

### Resources and Boundaries

- Use the already-running `fase serve` on port `5380`
- Do NOT start additional serve instances
- Use CLI, HTTP/API, and browser UI (via `agent-browser`) for comprehensive surface testing
- Create test work items with unique milestone-scoped identifiers (e.g., prefix with "verify-unif-test-")
- Clean up test work items and check records after validation or leave them for synthesis inspection

### Assertions to Test

- **VAL-VERIFY-001**: The verification lifecycle has one canonical handoff. Verify that all surviving user-facing surfaces use one verification lifecycle and one meaning for the worker-to-verification handoff, with `checking` as the single canonical handoff state.
- **VAL-VERIFY-002**: Check submission has one semantic contract across surviving surfaces. Verify that every surviving check-submission surface is either the canonical path or a documented exact alias to it, producing the same record shape, validation rules, and post-submit behavior.
- **VAL-VERIFY-003**: Passing checks require real evidence and durable artifacts. Verify that a passing check is rejected unless required evidence exists (successful build, no failed tests, valid persisted artifact paths) and that accepted check evidence remains reviewable after ephemeral worktree state is gone.
- **VAL-VERIFY-004**: Checks are evidence-only and blocking policy gates final success. Verify that checks collect evidence but do not independently satisfy blocking review policy, failed checks reopen the implementation loop, and unresolved blocking verification prevents final success.
- **VAL-VERIFY-005**: Reviewers consume one canonical evidence bundle. Verify that there is one canonical reviewer flow exposing current work state, checks, attestations, artifacts, linked docs, and approvals without requiring guesswork across duplicate surfaces.
- **VAL-VERIFY-006**: "Verify" terminology is unambiguous across audit and completion flows. Verify that any surviving verify-named surface is either the canonical completion-review bundle or is explicitly limited to cryptographic/audit verification.
- **VAL-CROSS-001**: Supervisor-to-verification flow runs end-to-end on canonical states. Verify that a supervisor-dispatched work item moves through the normalized lifecycle into the canonical verification path with no dependency on legacy active states and with preserved work/job context across worker and verifier steps.

### Testing Approach

1. Use CLI commands to create work items, submit checks through different surfaces, and manipulate verification states
2. Use HTTP API to inspect work item state, check records, attestation records, and verification artifacts
3. Verify canonical verification lifecycle by checking that `checking` is the handoff state across CLI JSON, HTTP responses, and browser UI work detail views
4. Verify check submission contract parity by submitting checks through CLI, HTTP, and any other surviving surfaces and comparing record shapes, validation rules, and post-submit state behavior
5. Verify evidence requirements by attempting to submit passing checks without required artifacts and confirming rejection
6. Verify completion gating by creating work items with blocking review policy, submitting passing checks, and confirming that final success remains blocked until the canonical policy resolves
7. Verify canonical evidence bundle by accessing the reviewer flow through CLI, HTTP, and browser UI and confirming all evidence (checks, attestations, artifacts, docs) is visible in one unified view
8. Verify verify terminology by inspecting CLI help, HTTP endpoint names, and browser UI labels for unambiguous usage of "verify"
9. Verify end-to-end supervisor-to-verification flow by dispatching work through the supervisor and tracing the full lifecycle from ready → claimed → working → checking → done with canonical state names
10. Keep the entire milestone in a single validator run to ensure deterministic state progression and cross-assertion coherence

## Flow Validator Guidance: docs-as-verification

### Isolation Rules

Docs-as-verification assertions test documentation integration with the work verification system: doc-linking, source-of-truth semantics, verification gating, doc policy, and proof bundle integration. All assertions share the same work-graph state and should run in a single serialized validator to avoid interference from concurrent doc mutations and state changes.

### Resources and Boundaries

- Use the already-running `fase serve` on port `5380`
- Do NOT start additional serve instances
- Use CLI and HTTP/API for primary testing
- Create test work items with unique milestone-scoped identifiers (e.g., prefix with "docs-verify-test-")
- Create test documents in temporary locations (e.g., `/tmp/fase-doc-test/`) to avoid polluting the repo
- Clean up test work items, doc records, and temp files after validation or leave them for synthesis inspection

### Assertions to Test

- **VAL-DOCS-001**: Tracked docs are first-class work-linked records. Verify that tracked docs always link to a work item and declare a non-empty authoritative repo-relative path, and storing a doc without an explicit work item resolves or creates the linked work.
- **VAL-DOCS-002**: Repo docs remain the source of truth. Verify that runtime doc records support linkage and review but verification treats the repo file at the declared path as authoritative.
- **VAL-DOCS-003**: Missing or stale required docs are verification failures. Verify that missing required docs, stale docs, or doc/code mismatch create explicit verification failure or unresolved-review outcomes and can block completion when policy requires docs.
- **VAL-DOCS-004**: Verification docs and briefings match runtime behavior or are removed. Verify that any committed doc claiming to mirror runtime verification behavior stays aligned with the shipped contract.
- **VAL-DOCS-005**: Writable doc-ingest surfaces are import helpers, not authoritative doc stores. Verify that doc-set and similar surfaces are explicitly treated as import/bootstrap helpers, and a document cannot satisfy verification unless the repo file exists at the declared path.
- **VAL-DOCS-006**: Doc requirements are machine-readable verification policy. Verify that completion dependencies on docs are represented in runtime state strongly enough for verification to deterministically fail on absence, staleness, or mismatch.
- **VAL-CROSS-002**: Documentation participates in the same completion gate as code and verification evidence. Verify that for work requiring docs, code changes, review evidence, and repo doc alignment are all evaluated as part of one completion contract.
- **VAL-CROSS-003**: Final reporting and notifications reuse the canonical proof bundle. Verify that completion or escalation reporting is rendered from canonical work-graph facts including linked docs.

### Testing Approach

1. Use CLI `fase work doc-set` to create and link docs to work items
2. Use HTTP API to inspect work item state and doc records
3. Verify work-doc linking by creating docs with and without explicit work IDs and checking auto-creation behavior
4. Verify repo-file authority by creating work with required docs, submitting doc-set records, and testing whether verification passes without actual repo files
5. Verify missing/stale doc failures by creating work with required docs but missing or outdated repo files and confirming verification blocks or fails
6. Verify machine-readable policy by inspecting work specs for RequiredDocs fields and testing whether verification deterministically checks them
7. Verify completion gating by creating work with both code checks and required docs, satisfying one but not the other, and confirming completion remains blocked
8. Verify proof bundle integration by accessing work detail/review surfaces and confirming linked docs are visible alongside checks and attestations
9. Keep the entire milestone in a single validator run to ensure deterministic state progression

## Flow Validator Guidance: usage-and-surface-cleanup

### Isolation Rules

Usage-and-surface-cleanup assertions test normalized usage/cost reporting, machine surface alignment (CLI/HTTP/MCP/native), and usage observability across work flows. All assertions share the same work-graph state and runtime surface exposure, so run them in a single serialized validator to ensure consistent state and avoid interference from concurrent job runs or surface mutations.

### Resources and Boundaries

- Use the already-running `fase serve` on port `5380`
- Do NOT start additional serve instances
- Use CLI, HTTP/API, and MCP surfaces for comprehensive testing
- Create test work items with unique milestone-scoped identifiers (e.g., prefix with "usage-surface-test-")
- Test jobs may incur real token usage if using live LLM providers - prefer fake/mock adapters where possible
- Clean up test work items and jobs after validation or leave them for synthesis inspection

### Assertions to Test

- **VAL-USAGE-001**: Normalized usage reporting exposes one canonical machine shape. Verify that status surfaces expose one stable normalized usage object with canonical fields, derived totals are consistent, and no-usage cases omit the field rather than guessing.
- **VAL-USAGE-002**: Multi-model usage aggregation is deterministic. Verify that jobs spanning multiple models expose deterministic aggregate usage plus sorted per-model entries whose totals match the aggregate.
- **VAL-USAGE-003**: Cost selection is deterministic and provenance-carrying. Verify that vendor-reported cost wins when present, estimated cost appears only when known with explicit labeling, and machine-facing status doesn't ambiguously select between competing cost fields.
- **VAL-USAGE-004**: Catalog and history rollups derive from canonical job usage. Verify that usage totals in catalog/history surfaces derive from the same normalized per-job usage contract used by status, with explicit inclusion rules and no-usage omission.
- **VAL-SURFACE-001**: Surviving machine surfaces are thin wrappers or intentionally removed. Verify that CLI JSON and HTTP semantics stay aligned, legacy divergent surfaces are removed or exact aliases, and runtime/catalog/status/work/check/report surfaces don't silently substitute for one another.
- **VAL-SURFACE-002**: MCP and reporting surfaces expose only the documented simplified contract. Verify that MCP tools and report/notification surfaces expose only the documented minimal contract with no hidden duplicate tools or mismatched envelopes.
- **VAL-SURFACE-003**: Native tool exposure does not form a divergent control plane. Verify that native adapter tools expose only the documented simplified contract and don't introduce extra authoritative mutation or review paths beyond CLI/HTTP/MCP surfaces.
- **VAL-CROSS-004**: Usage and cost observability remain attached to the verified work flow. Verify that worker and verifier jobs expose normalized usage/cost data tied back to the validated work item with clear vendor-reported vs estimated distinction.
- **VAL-CROSS-005**: Usage and cost attribution survive retries and verifier fan-out. Verify that across retries and worker/verifier fan-out, usage remains attributable to specific job IDs and attempts, with final work/report surfaces distinguishing accepted from superseded attempts.

### Testing Approach

1. Create test work items that will spawn jobs with observable usage/cost data
2. Use CLI commands to check job status and inspect usage reporting format
3. Use HTTP API to inspect the same jobs and verify CLI/HTTP parity
4. If MCP is available, test MCP tool exposure and verify contract alignment
5. Create multi-model jobs if possible to test aggregation logic
6. Create jobs with vendor-reported cost and jobs with only estimated cost to test cost selection
7. Test retry/reset scenarios to verify usage attribution across attempts
8. Verify catalog and history rollups match per-job usage data
9. Keep the entire milestone in a single validator run to ensure deterministic usage accumulation and surface parity
