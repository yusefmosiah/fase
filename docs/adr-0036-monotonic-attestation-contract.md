Date: 2026-03-18
Kind: Architecture decision
Status: Proposed
Priority: 1

## ADR-0036: Monotonic Attestation Contract

### Context

The current attestation implementation is a synchronous side-effect in
`handleJobCompletion` (`internal/cli/serve.go`). When a worker completes,
`dispatchAttestorAndWait` spawns attestation jobs and blocks the supervisor
goroutine until each attestor finishes. This causes several problems:

1. **Orphan jobs.** Attestation jobs are created via `fase run --work
   <work-id>` which links a *job* to a work item, but the attestation job is
   not a *work item* itself. The work graph has no visibility into which
   attestors are running, what they found, or how they relate to each other.

2. **Stuck claimed state.** The `WaitStatus` poll inside `dispatchAttestorAndWait`
   holds a goroutine for up to 30 minutes. If the supervisor process restarts
   mid-wait, the work item stays `in_progress` with no active worker, and the
   attestation jobs become truly orphaned — no cleanup path exists.

3. **Invisible review contracts.** A work item's `required_attestations` JSON
   states what review is needed, but the actual attestation jobs are invisible
   in the DAG. Tooling (UI, `fase work show`, graph queries) cannot answer
   "what attestors are reviewing this?" or "which slot is blocking completion?"

4. **No contract enforcement.** Nothing prevents a supervisor restart,
   a config change, or a buggy code path from silently weakening or skipping
   required attestations after dispatch. The review contract is not persisted
   as a durable invariant.

5. **No escalation path.** If a worker touches auth code, there is no
   structured way to add a security review slot after the worker completes
   without rebuilding review logic ad hoc.

This ADR replaces `handleJobCompletion` with a **monotonic attestation
contract**: attestation requirements are predefined, frozen at dispatch,
escalation-only after completion, and fulfilled through first-class work items
in the normal dispatch queue.

### Decision

1. **Attestation slots are predefined at work item creation.** Defaults come
   from the supervisor config keyed on `kind` and `configuration_class`.
   Per-item overrides are set at creation time.

2. **The contract is frozen at dispatch.** When the first worker job is
   dispatched (`claimed → in_progress`), `attestation_frozen_at` is set and
   the slot list becomes immutable to weakening operations.

3. **After worker completion, escalation is append-only.** The supervisor
   may add slots (with reason + timestamp). No existing slot may be removed
   or softened.

4. **Each slot becomes a child work item (`kind=attest`) on worker
   completion.** These children go through the normal dispatch queue. No
   special-cased synchronous wait logic in the supervisor.

5. **A new execution state `awaiting_attestation` replaces the in-progress
   wait.** The parent stays in `awaiting_attestation` until all blocking
   attestation children reach `done`. If any blocking child fails, the parent
   fails. The `handleJobCompletion` function is removed.

6. **Attestation nonce is set on the parent at worker completion and
   inherited by child work items.** The nonce is the temporal information
   boundary: the worker cannot have known it.

### Design

#### 1. State Machine

```
ready → claimed → in_progress → awaiting_attestation → done
                                         │
                                         └──→ failed
         │                 │
         └── failed ←──────┘  (can fail at any point)
```

Transitions:

| From               | To                    | Trigger                                           |
|--------------------|-----------------------|---------------------------------------------------|
| `ready`            | `claimed`             | Supervisor claims work item (sets lease)          |
| `claimed`          | `in_progress`         | Worker job starts; sets `attestation_frozen_at`   |
| `in_progress`      | `awaiting_attestation`| Worker job reaches terminal state (pass or fail-silent); nonce set; attest children created |
| `awaiting_attestation` | `done`            | All blocking attest children reach `done`         |
| `awaiting_attestation` | `failed`          | Any blocking attest child reaches `failed`        |
| `awaiting_attestation` | `in_progress`     | Retry: supervisor re-dispatches worker (new job)  |
| `in_progress`      | `failed`              | Worker job explicitly fails                       |

**Why `awaiting_attestation` rather than a condition on `in_progress`:**

- The dispatch loop must not claim-and-dispatch a worker job for a work item
  that is already pending attestation. A distinct state is an O(1) guard
  with no risk of races.
- UI and graph tooling can render the state meaningfully: "3 attestors
  reviewing" vs. "worker running."
- `in_progress` semantically means "a worker is executing." That is false
  while attestors are reviewing. Conflating them would obscure audit trails.
- The `Terminal()` predicate on `WorkExecutionState` stays clean: `done`,
  `failed`, `cancelled`, `archived` are terminal; `awaiting_attestation` is
  not (attestation can still fail and retry).

**`awaiting_attestation` is not claimable by regular workers.** The dispatch
query's `WHERE execution_state IN ('ready', 'claimed')` clause already
excludes it. No other change to claim logic is needed.

#### 2. Schema Changes

##### 2a. `work_items` table — add `attestation_frozen_at`

```sql
ALTER TABLE work_items ADD COLUMN attestation_frozen_at TEXT;
```

Set to the current timestamp when `in_progress` is first written (dispatch
time). NULL means the contract has not been frozen yet (work never started).
The service layer enforces: once non-NULL, `required_attestations_json` may
only be appended to, never modified in place.

##### 2b. `RequiredAttestation` struct — add escalation fields

```go
type RequiredAttestation struct {
    VerifierKind    string         `json:"verifier_kind,omitempty"`
    Method          string         `json:"method,omitempty"`
    Blocking        bool           `json:"blocking,omitempty"`
    Metadata        map[string]any `json:"metadata,omitempty"`
    // Escalation fields — nil on original slots, set on escalated slots
    EscalatedAt     *time.Time     `json:"escalated_at,omitempty"`
    EscalationBy    string         `json:"escalation_by,omitempty"`
    EscalationReason string        `json:"escalation_reason,omitempty"`
}
```

Original slots (set at creation) have nil `EscalatedAt`. Escalated slots
(added after freeze) have all three escalation fields set. This lets
verification tooling distinguish original requirements from supervisor
escalations.

##### 2c. Execution state constant

```go
WorkExecutionStateAwaitingAttestation WorkExecutionState = "awaiting_attestation"
```

Update `Terminal()` to leave `awaiting_attestation` non-terminal. Update
the `ready` work query to exclude it (it is already excluded by the
`execution_state IN ('ready', 'claimed')` filter, but make the exclusion
explicit in a comment).

##### 2d. `work_items` table — `execution_state` constraint

Add `awaiting_attestation` to any CHECK constraint on `execution_state`.
(Currently unconstrained in SQLite; add it to documentation/migration notes.)

#### 3. Attestation Defaults in Supervisor Config

Add an `[attestation]` section to `config.toml`:

```toml
# Default attestation slots applied at work item creation.
# Rules are evaluated in order; first matching rule wins per work item.
# kind and configuration_class support "*" as wildcard.

[[attestation.defaults]]
kind                = "implement"
configuration_class = "*"
slots = [
  { verifier_kind = "attestation", method = "automated_review", blocking = true },
]

[[attestation.defaults]]
kind                = "implement"
configuration_class = "security"
slots = [
  { verifier_kind = "attestation", method = "automated_review",   blocking = true },
  { verifier_kind = "security",    method = "security_review",    blocking = true },
]

[[attestation.defaults]]
kind                = "plan"
configuration_class = "*"
slots = [
  { verifier_kind = "attestation", method = "plan_review", blocking = true },
]

[[attestation.defaults]]
kind                = "attest"
configuration_class = "*"
slots = []  # attestation work items require no further attestation
```

Matching rules:
1. If a work item's `kind` and `configuration_class` match a rule exactly,
   use that rule's slots.
2. If `configuration_class = "*"` matches, use that rule as a fallback for
   the given kind.
3. If no rule matches, the work item gets zero slots (unreviewed; suitable
   for internal kinds like `attest`).
4. If a work item is created with explicit `required_attestations`, those
   take precedence over config defaults entirely. Config defaults apply
   only when the field is empty at creation time.

Go struct additions to `Config`:

```go
type AttestationDefaultsConfig struct {
    Defaults []AttestationRule `toml:"defaults"`
}

type AttestationRule struct {
    Kind               string                `toml:"kind"`
    ConfigurationClass string                `toml:"configuration_class"`
    Slots              []RequiredAttestation `toml:"slots"`
}
```

And in `Config`:

```go
type Config struct {
    // ... existing fields ...
    Attestation AttestationDefaultsConfig `toml:"attestation"`
}
```

The `CreateWork` service call (or the CLI `fase work create` path) resolves
defaults: if `required_attestations` is empty, look up the matching rule and
apply its slots.

#### 4. Worker Completion → Attestation Child Creation

When a worker job transitions a work item to "worker complete" (the point
currently handled by `handleJobCompletion`), the supervisor's normal job
monitoring loop (in `runInProcessSupervisor` / `serve.go`) detects job
terminal state and calls a new function `spawnAttestationChildren`:

```
spawnAttestationChildren(ctx, svc, workID, workerJobID, workerAdapter):
  1. Fetch work item and current attestation records.
  2. Generate attestation nonce: nonce = core.GenerateID("nonce")
  3. Store nonce in work.Metadata["attestation_nonce"] via UpdateWork.
  4. For each slot i in work.RequiredAttestations:
       a. If a child work item for this slot already exists (check
          work_edges for kind=attest children with matching slot index
          in metadata), skip (idempotent re-entry after crash).
       b. Create a child work item:
            kind           = "attest"
            title          = fmt.Sprintf("Attest slot %d: %s/%s — %s", i,
                               slot.VerifierKind, slot.Method, work.Title)
            objective      = attestor prompt (see §5)
            parent work_id = workID (via "blocks" edge: child blocks parent
                             becoming done)
            metadata       = {
                               "parent_work_id": workID,
                               "slot_index":     i,
                               "attestation_nonce": nonce,
                               "worker_job_id":  workerJobID,
                               "worker_adapter": workerAdapter,
                             }
            preferred_adapters = [rotate(workerAdapter, i)]
            required_attestations = []  (attest items need no attestation)
  5. Transition parent to awaiting_attestation via UpdateWork.
```

**Idempotency:** If the supervisor crashes between step 4a and 5, on restart
it will re-enter `spawnAttestationChildren`. The "already exists" check at
step 4a prevents duplicate children. The `awaiting_attestation` transition is
a no-op if already in that state.

**No slots case:** If `work.RequiredAttestations` is empty, `kind=attest` is
dispatched with a single default slot (same behavior as today's
`attestAdapterModel` path). The parent still transitions to
`awaiting_attestation` so the state machine is uniform. Alternatively: work
items with zero required_attestations could skip attestation entirely and go
directly to `done`. This is a policy choice; the recommended default is
**one implicit slot** to preserve the three-layer verification model.

#### 5. Attestation Child Work Item Objective

The objective field of each `kind=attest` child doubles as the attestor's
prompt (the worker briefing system injects it). The supervisor builds it
from parent metadata at child creation time:

```
You are an attestation agent reviewing work item {{parent_work_id}}.

## Work item
Title: {{parent_title}}
Objective: {{parent_objective}}
Worker adapter: {{worker_adapter}}
Worker job: {{worker_job_id}}
Slot: {{slot_index}} ({{verifier_kind}}/{{method}})

## Worker's verification findings
{{worker_findings}}

## Attestation procedure
1. Run: git diff --stat HEAD~1
2. If no meaningful files changed: attest failure.
3. Review the diff. Does it address the objective? Does it build?
4. Record your finding:
   fase work attest {{parent_work_id}} \
     --nonce {{attestation_nonce}} \
     --result [passed|failed] \
     --summary "<your finding>" \
     --verifier-kind {{verifier_kind}} \
     --method {{method}}

You MUST run exactly one fase work attest command.
After attesting, mark this work item done:
   fase work update {{self_work_id}} --state done
```

The attestor calls `fase work attest` on the **parent** work ID (not its
own), then marks its own work item done. The `AttestWork` service method
already handles the parent state transition logic.

#### 6. Parent State Transition on Attestation

The existing `AttestWork` service method (`service.go:1907`) already
implements the transition logic: when all blocking slots have passing
attestations, it transitions the parent to `done`. When any blocking slot
is attested `failed`, it transitions the parent to `failed`. This logic is
unchanged. The only change is that it is now triggered by a child work item
calling `fase work attest` rather than by an orphan job.

The `requiredAttestationsResolved` and `UnsatisfiedAttestationSlotIndices`
helpers are unchanged.

#### 7. Escalation Protocol

After the worker completes (parent is `awaiting_attestation`) but before
all attestation children are done, the supervisor may escalate:

```
EscalateAttestation(ctx, svc, workID, slots []RequiredAttestation, reason, by string):
  1. Fetch current work item. Verify attestation_frozen_at is set.
  2. Append each new slot to required_attestations_json with escalation fields set.
  3. Update work item.
  4. For each new slot, call spawnAttestationChildren logic for that slot only.
```

Invariants enforced by the service:
- `EscalateAttestation` is the only write path to `required_attestations`
  after `attestation_frozen_at` is set. Direct writes are rejected.
- No existing slot may be removed or modified. Only appends are allowed.
- Escalation creates new child work items immediately.

Escalation trigger example (for future implementation): the supervisor scans
the worker's diff after `awaiting_attestation`. If the diff touches
`internal/auth/` or similar sensitive paths, it calls `EscalateAttestation`
with a security review slot. This is policy; the contract mechanism is
neutral to the trigger.

#### 8. Migration from handleJobCompletion

**Phase 0 (this ADR, no behavior change):**
- Add `WorkExecutionStateAwaitingAttestation` constant.
- Add `attestation_frozen_at` column (migration).
- Add `EscalatedAt`/`EscalationBy`/`EscalationReason` to `RequiredAttestation`.
- Add `[attestation]` config section parsing (no-op if empty).
- Set `attestation_frozen_at` on `in_progress` transition (pure audit).

**Phase 1 (behavioral cutover):**
- Implement `spawnAttestationChildren`.
- Replace `handleJobCompletion` / `dispatchAttestorAndWait` with a call to
  `spawnAttestationChildren` + parent transition to `awaiting_attestation`.
- The supervisor's dispatch loop already handles `kind=attest` work items
  because it dispatches all `ready` work items regardless of kind. No new
  special-case dispatch logic is required.
- Remove the `WaitStatus` synchronous poll. Dispatch is now fire-and-forget
  from the supervisor's perspective.

**Phase 2 (config-driven defaults):**
- Implement config rule resolution in `CreateWork`.
- Add `EscalateAttestation` service method.
- Wire up diff-scanning escalation trigger in supervisor.

**Migration for existing work items** already in `in_progress` during the
Phase 1 cutover: the supervisor restart will re-enter job monitoring.
`handleJobCompletion` will no longer exist; `spawnAttestationChildren` will
run instead. If attestation children already exist (from a prior run's
`handleJobCompletion`), the idempotency check at step 4a skips them. If they
don't exist (fresh cutover), new children are created. The `awaiting_attestation`
state transition overwrites `in_progress` cleanly.

#### 9. ADR-0035 Integration Points

ADR-0035 defines WHO the attestor is (cryptographic identity). This ADR
defines WHAT review each work item faces. They compose at two points:

**Capability tokens for attestation children.** When the supervisor dispatches
a `kind=attest` work item, it issues a capability token with:
```json
{
  "role": "attestor",
  "capabilities": ["work:attest", "work:note-add", "work:update"]
}
```
The `work:attest` capability is scoped to the parent work ID stored in the
child's metadata. The attestor cannot attest an arbitrary work item — only
the one specified in its token.

**Nonce ordering.** The nonce is set on the parent at worker-completion time
and inherited by children at creation time. This preserves the temporal
ordering guarantee from ADR-0026: the worker cannot have known the nonce.
The nonce is now visible in the work graph (child metadata) rather than only
in the parent's metadata and the attestor's prompt.

**Signed attestation records (ADR-0035 Phase 3).** Attestation children have
a known `job_id` and `agent_pubkey` from dispatch. The `AttestationRecord`'s
`SignerPubkey` and `Signature` fields can be populated by the attestor using
its ephemeral key. The parent work item's verification tooling (`verify.go`)
can check:
1. The attestation record's `SignerPubkey` matches the dispatched agent's key.
2. The `Signature` is valid over the canonical record.
3. The record's `job_id` matches the child work item's `current_job_id`.

This chain — child work item → job → agent keypair → signed attestation —
is not possible in the current orphan-job model because there is no child
work item to anchor the key binding.

#### 10. Consequences

**Positive:**
- Attestation is visible in the work graph. `fase work show <id>` shows
  pending attestation children and their states.
- No synchronous goroutine blocks. The supervisor dispatch loop is
  event-driven; a 30-minute attestation does not pin a goroutine.
- Crash-safe. On restart, `spawnAttestationChildren` is idempotent.
- Contract is durable and auditable. `attestation_frozen_at` + append-only
  escalation give a full audit trail in the DB.
- ADR-0035 Phase 3 (signed attestations) can be fully verified because each
  attestation is bound to a child work item and its dispatched agent identity.

**Negative / costs:**
- **More work items in the graph.** Every implemented work item grows 1–N
  child `kind=attest` entries. Queries that walk the full graph become
  noisier. The UI must filter or collapse attest children by default.
- **`kind=attest` must be excluded from user-visible dispatch counts.**
  The supervisor work queue will show attest items in flight; dashboards
  should distinguish them from substantive work.
- **`awaiting_attestation` adds a state to every state machine diagram,
  test fixture, and state assertion in the codebase.** This is a one-time
  cost but non-trivial (grep `WorkExecutionState` for the blast radius).
- **The attestation child's objective is the attestor prompt.** This is
  a slight semantic overload of `objective` — it now doubles as an
  instruction template. The worker briefing system already injects
  `objective` as the primary instruction, so this is consistent, but
  it means attest objectives are longer and more procedural than typical
  work item objectives.

### Open Questions

1. **Zero-slot work items.** Should work items with `required_attestations = []`
   after config resolution skip attestation and go directly to `done`? Or
   should the supervisor inject a minimum single-slot default for all
   non-attest kinds? Recommend: one implicit slot minimum for `implement` and
   `plan` kinds; zero is allowed for `attest`, `note`, and internal kinds.

2. **Re-attestation on retry.** When an attestation fails and the parent
   retries (`awaiting_attestation → in_progress`), what happens to the failed
   attestation children? Options: (a) mark them `cancelled` and create new
   children on next worker completion; (b) leave them in `failed` as history
   and create new children. Recommend option (a) — cancelled children are
   historical artifacts, new children represent the current review cycle.

3. **Parallel vs. sequential attestation dispatch.** The current implementation
   dispatches slots sequentially (each attestor sees the prior record). With
   first-class work items, all children are `ready` simultaneously and the
   dispatch loop may run them in parallel. Sequential visibility was a
   deliberate choice. To preserve it: add `depends_on` edges between sibling
   attest children (slot N+1 depends on slot N). This is optional; parallel
   is simpler and may be acceptable.

4. **Attestation slot matching after escalation.** The `hasPassingAttestationForSlot`
   function matches by index position in `required_attestations`. After
   escalation appends new slots, indices are stable (append-only). Existing
   passing attestations retain their index. This is safe as long as no
   reordering occurs — the append-only constraint ensures this.

### References

- ADR-0026: Attestation nonce (temporal ordering, anti-self-attest)
- ADR-0033: fase serve (supervisor architecture, job monitoring loop)
- ADR-0034: Verification before approval (three-layer model)
- ADR-0035: Cryptographic agent identity (WHO; this ADR is WHAT)
- `internal/cli/serve.go:handleJobCompletion` — function being replaced
- `internal/service/service.go:AttestWork` — unchanged; called by attestor
- `internal/service/service.go:UnsatisfiedAttestationSlotIndices` — unchanged
