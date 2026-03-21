Date: 2026-03-20
Kind: ADR
Status: Proposed
Priority: 1
Supersedes: None (extends ADR-0037: Event-Driven Supervisor)
Owner: Runtime / Supervisor

# ADR-0038: Agentic Supervisor — App Agent Layer

## Context

ADR-0037 replaces the polling loop with an event-driven reactor. The reactor
is deterministic: given a `WorkEvent`, it follows a fixed algorithm to
dispatch, recover, and route. This handles the happy path and a well-enumerated
set of failure modes. It does not handle the long tail of anomalies that
require reasoning: contradictory graph state, policy gaps, novel failure modes,
multi-step recovery sequences, and communication with the host about situations
the deterministic code cannot resolve.

The architecture mirrors choiros-rs, which separates three levels of agency:

```
conductor    (host)       — approves, archives, sets policy
  └─ app-agent (supervisor) — orchestrates, reasons, escalates
       └─ workers           — execute assigned work items
```

Currently, the supervisor is a pure deterministic loop. When it encounters
something it cannot handle (unfamiliar graph topology, ambiguous attestation
failure, policy violation not covered by any rule), it either: (a) silently
ignores the anomaly, (b) marks work failed without explanation, or (c) requires
the host agent to intervene manually. This manual intervention breaks the
autonomous operation goal and requires the host to understand the system's
internal state deeply.

The agentic supervisor adds a reasoning layer that wraps the deterministic loop.
The deterministic loop stays as the execution engine. The agent wraps it with
judgment about *what to execute*, *when to escalate*, and *how to recover*.

**Key insight from prior work:** The deterministic loop is good at mechanics —
claiming work, spawning processes, renewing leases. The agent is good at
judgment — deciding whether a recovery is appropriate, whether an escalation is
warranted, whether the current queue order serves the project goals. Keep them
separate and compose them.

## Decision

Introduce an agentic supervisor process that operates as the `app-agent` tier
in the conductor → app-agent → workers hierarchy. It is invoked by the
deterministic event-driven loop on every cycle and on anomaly detection. It
has a capability token with the `overseer` role — broad write access to the
work graph, but excluding the host-only operations (`work:approve`,
`work:archive`). It communicates with the host exclusively through escalation
work items in the graph. It improves over time via a policy learning loop:
each resolved escalation produces a policy update to the hydration brief, so
the same class of anomaly is handled autonomously next time.

### Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         fase serve                                       │
│                                                                          │
│  ┌────────────────────────────────────────────────────────────────────┐  │
│  │               Event-Driven Supervisor Loop (ADR-0037)              │  │
│  │                                                                    │  │
│  │  WorkEvent / ProcessEvent / AdapterEvent / Tick                   │  │
│  │          │                                                         │  │
│  │          v                                                         │  │
│  │  ┌────────────────┐   anomaly?    ┌─────────────────────────────┐  │  │
│  │  │ Reactor (deter-│──────────────►│      Agentic Supervisor     │  │  │
│  │  │ ministic path) │               │   (reasoning / judgment)    │  │  │
│  │  │                │◄──────────────│                             │  │  │
│  │  │  • dispatch    │   action set  │  • classify anomaly         │  │  │
│  │  │  • recover     │               │  • choose action            │  │  │
│  │  │  • route       │               │  • execute or escalate      │  │  │
│  │  │  • heartbeat   │               │  • record rationale         │  │  │
│  │  └────────────────┘               └──────────────┬──────────────┘  │  │
│  └──────────────────────────────────────────────────│─────────────────┘  │
│                                                     │                    │
│                              ┌──────────────────────┘                    │
│                              │                                            │
│                    ┌─────────▼──────────┐                                │
│                    │    Work Graph      │                                 │
│                    │                   │                                 │
│                    │  • escalation      │◄────── host resolves            │
│                    │    work items      │        + policy update          │
│                    │  • policy notes    │                                 │
│                    │  • audit trail     │                                 │
│                    └────────────────────┘                                │
└─────────────────────────────────────────────────────────────────────────┘
```

### 1. Invocation Model

The agentic supervisor is not a separate process. It runs in-process within
`fase serve` as a stateful goroutine that the deterministic reactor calls when
reasoning is needed. This keeps the system simple: one process, one work
graph connection, one capability token, one source of truth.

**Invocation triggers (deterministic loop → agent):**

| Trigger | Condition | Example |
|---------|-----------|---------|
| Anomaly detection | Reactor cannot classify an event | `work_updated` with state transition that violates known DAG invariants |
| Recovery ambiguity | Multiple recovery paths available | Worker committed partial code *and* attestation failed — preserve or retry? |
| Stall without output | Job stalled but no explanation in notes | Agent asks worker for status; if no response, decides recovery |
| Queue re-evaluation | New high-priority work created | Decide whether to interrupt in-flight jobs or queue behind |
| Heartbeat (periodic) | Every N cycles (configurable) | Review graph health, pending escalations, policy drift |
| Explicit request | Host adds a `supervisor:review` note | Host asks supervisor to re-evaluate current queue ordering |

**What the agent does NOT decide (left to deterministic loop):**
- Standard dispatch (ready work → available adapter)
- Lease renewal timing
- Budget enforcement
- Stall detection thresholds

The boundary is: deterministic for everything with a clear rule; agentic for
everything that requires judgment.

### 2. Capability Model

The supervisor agent receives an `overseer` role capability token. This role
is a superset of `worker` and `planner`, excluding host-only operations.

**Role → Capability mapping:**

| Role      | Capabilities |
|-----------|-------------|
| `worker`  | `work:update`, `work:note-add` |
| `attestor`| `work:attest`, `work:note-add` |
| `reviewer`| `work:approve`, `work:reject`, `work:note-add` |
| `planner` | `work:create`, `work:edge-add`, `work:note-add` |
| `overseer`| `work:update`, `work:note-add`, `work:attest`, `work:edge-add`, `work:edge-rm`, `work:create` |
| `host`    | All above + `work:approve`, `work:archive` |

**Rationale for capability boundaries:**

- `work:attest` — the supervisor can attest deterministic checks (build, test,
  lint) without a separate agent invocation. Human-judgment attestations still
  require a separate attestor worker, preserving the separation between
  implementor and attestor.

- `work:edge-add` / `work:edge-rm` — the supervisor can reorder work (insert
  hotfix ahead of queued items, remove stale blocking edges, add discovered
  dependencies). Edge management is the primary mechanism for priority
  manipulation.

- `work:create` — the supervisor can create escalation items and priority fix
  items. It cannot create arbitrary work items without limit; the
  `child_creation: proposal_only` policy on non-escalation work items still
  applies unless the supervisor's hydration brief grants an exception.

- NOT `work:approve` — approval requires host judgment. The supervisor cannot
  unilaterally approve work items. It can *recommend* approval via a note on
  the work item and via an escalation.

- NOT `work:archive` — archive is irreversible and host-only. The supervisor
  must never archive items; only the host can collapse completed work into
  the graph history.

**Token issuance:** The supervisor token is issued at `fase serve` startup,
not per-invocation. It has a 24-hour expiry (matching the serve session) and is
renewed on lease renewal. The token is held in memory, not written to disk —
the supervisor is a trusted in-process component, not an external agent.

**Token scoping:** Unlike worker tokens (scoped to a single work item and job),
the overseer token is scoped to the project. This reflects the supervisor's
role as the orchestrator. All writes made with the overseer token are recorded
in the audit trail with the token's public key, making the supervisor's actions
attributable and auditable.

### 3. Communication with the Host

The supervisor communicates with the host exclusively through the work graph.
It does not send messages out-of-band (no Slack, no email, no separate log
channel). This keeps the communication model consistent and auditable.

#### 3a. Escalation Work Items

When the supervisor encounters a situation it cannot resolve, it creates an
escalation work item:

```
kind: escalation
title: "[Supervisor] <short description>"
objective: |
  <Structured description of the situation, what the supervisor tried,
   why it is escalating, and what the host needs to decide.>
priority: 0  (highest — blocks progress)
blocking: [affected work item IDs]
```

The escalation work item blocks downstream progress by inserting a blocking
edge from it to the affected work items. The graph will not dispatch any
blocked work until the escalation is resolved. This ensures the host sees the
escalation before the system continues.

**Escalation structure:**

```
[supervisor:escalation]
Situation: <what happened>
Tried: <what recovery was attempted>
Options:
  A. <option 1 description> — consequence: <what happens>
  B. <option 2 description> — consequence: <what happens>
  C. Defer / ask me later — consequence: work remains blocked
Recommended: <A|B|C> because <reason>
[/supervisor:escalation]
```

The structured format makes the escalation machine-readable for future policy
extraction (§3b). Options are always enumerated with consequences. The
supervisor recommends but does not decide.

**Host resolution:** The host resolves an escalation by:
1. Updating the escalation item to `done` with a note explaining the decision.
2. Optionally updating the supervisor's hydration brief with a policy note
   (see §3b).
3. Unblocking the affected work items (the blocking edge is removed when the
   escalation transitions to `done`).

The host's resolution is the authoritative record. The supervisor reads it
on the next invocation and adjusts behavior accordingly.

#### 3b. Policy Notes on Hydration Brief

After resolving an escalation, the host can append a policy note to the
supervisor's hydration brief. The brief is stored as a convention note on a
designated policy anchor work item (created at project bootstrap) and emitted
by `fase project hydrate` as part of the project conventions.

**Policy note format:**

```
Supervisor policy: <trigger condition>
Action: <what the supervisor should do>
Rationale: <why the host decided this>
Added: <date>
```

**Example:**

```
Supervisor policy: Worker exits with code 0, job state != completed, no commits on branch
Action: Mark work failed; retry with different adapter; do not escalate
Rationale: This is a Codex spurious exit — the adapter finishes without producing output. Retry always resolves it.
Added: 2026-03-18
```

Once a policy note exists, the supervisor handles the trigger autonomously
without escalating. The learning loop closes:

```
anomaly → escalation → host resolves → policy note → autonomous next time
```

#### 3c. Direct Notes on Work Items

For non-escalation observations — state transitions, recovery actions taken,
routing decisions — the supervisor adds notes directly to the affected work
items:

```
[supervisor:action type="recovery"]
Worker process exited (pid 12345, exit code 1).
Found 2 commits on branch since work was claimed.
Action: Preserved committed work; transitioned to awaiting_attestation.
Attestation job dispatched: job_01DEF...
[/supervisor:action]
```

These notes provide the audit trail for the supervisor's autonomous decisions.
They are visible to the host in the work item history and to future supervisor
invocations as context.

### 4. Reasoning Interface

The agentic supervisor is invoked as a structured reasoning call:

```go
type SupervisorInput struct {
    TriggerKind   string                 // "anomaly" | "recovery_ambiguity" | "heartbeat" | ...
    TriggerEvent  interface{}            // the event or situation that triggered invocation
    GraphContext  SupervisorGraphContext // relevant work items, edges, in-flight jobs
    PolicyBrief  string                 // current policy notes from hydration brief
    InvocationID string                 // for deduplication and tracing
}

type SupervisorOutput struct {
    Actions     []SupervisorAction // ordered list of actions to execute
    Notes       []NoteToAdd        // notes to add to work items
    Escalation  *EscalationItem    // non-nil if escalation is warranted
    Rationale   string             // why the supervisor made these decisions
}

type SupervisorAction struct {
    Kind     string // "work:update" | "edge:add" | "edge:rm" | "work:create" | "dispatch:reorder"
    WorkID   string
    Payload  interface{}
    Priority int    // execution order; lower = first
}
```

The supervisor is invoked with a context window containing:
1. The trigger event and its context.
2. The graph context: affected work items, their states, their notes, their
   edges (blocking and blocked-by).
3. The policy brief: current supervisor policies extracted from the hydration
   brief.
4. Recent supervisor history: last 5 decisions for this work area (from the
   supervisor's own notes on those items).

The supervisor returns a structured action set. The deterministic loop executes
the actions using the overseer capability token. This separation means the
agent reasons without side effects, and the deterministic loop applies the
results — making the system testable and auditable.

**Model selection:** The supervisor uses a low-cost reasoning model. Default:
`claude-sonnet-4-6` or `opencode/glm-5-turbo`. The choice is configured in
`config.toml` under `[supervisor]`:

```toml
[supervisor]
model = "claude-sonnet-4-6"        # or "opencode/glm-5-turbo"
adapter = "claude"                  # adapter to use for supervisor calls
max_tokens = 4096
temperature = 0                     # deterministic for reproducibility
invoke_on_anomaly = true
invoke_on_heartbeat = true
heartbeat_interval_cycles = 10      # invoke every 10 reactor cycles
```

The supervisor does NOT use a coding model — it reasons about work graph state,
not code. A fast, cheap reasoning model is appropriate.

### 5. Graph Management Capabilities

The supervisor's most powerful capability is work graph manipulation: it can
restructure the execution order to serve the project's goals without waiting
for host intervention.

#### 5a. Priority Insertion (hotfix ahead of queue)

When a blocking bug or urgent fix is discovered mid-sprint, the supervisor can:
1. Create a new work item for the fix (kind=`implement`, priority=0).
2. Add a blocking edge from it to the items it must precede.
3. Update the items that depended on the broken component to depend on the fix.

This reorders the queue so the fix is dispatched next, ahead of queued items.
The host sees the graph change and can approve or override.

#### 5b. Stale Edge Removal

Work items sometimes have blocking edges that no longer apply — a dependency
that was satisfied by a different implementation path, or a blocking item
that was force-completed. The supervisor can identify and remove stale edges
based on the current state of the graph.

**Safety:** The supervisor does not remove edges that the host explicitly
created (marked with a `host:manual` metadata flag). It only removes edges
it created itself or edges it can prove are stale from the graph state.

#### 5c. Dependency Discovery

When a worker's attestation reveals an unexpected dependency (e.g., a test
failure that requires a change to a package the worker didn't touch), the
supervisor can insert a discovered dependency edge between the failed work item
and the newly-discovered prerequisite. This is recorded as a `discovered_from`
edge with the attestation item as the evidence source.

#### 5d. Work Item Reconciliation

For stuck work items (claimed but no progress, orphaned jobs, expired leases
with partial work), the supervisor evaluates the full context — git state,
job history, notes — and decides:
- **Resume**: re-claim and dispatch with the same adapter and additional context.
- **Retry**: mark failed, return to ready pool, dispatch with different adapter.
- **Escalate**: the situation is ambiguous enough to require host judgment.
- **Attest partial**: committed work exists and is self-consistent; attest it as-is.

### 6. Signed Actions and Audit Trail

All supervisor actions are executed with the overseer capability token and
recorded in the audit trail (ADR-0035). Every write command carries the
supervisor's public key in the request, and the capability enforcement layer
verifies the token before executing the write.

This means:
- Work state changes made by the supervisor are distinguishable from those made
  by workers or the host.
- Edge additions and removals are attributed to the supervisor.
- Escalations are signed — the host can verify the escalation was created by
  the trusted supervisor, not by a worker attempting to inject a false anomaly.

**Audit query:** `fase work history --actor supervisor` shows all write
operations performed by the supervisor across the work graph.

### 7. Learning Loop

The learning loop is the long-run value proposition of the agentic supervisor.
Each anomaly class that requires an escalation becomes a policy case, and
each resolved escalation is an opportunity to teach the supervisor to handle
the same class autonomously.

```
                  ┌─────────────────────────────────────────┐
                  │           Learning Loop                  │
                  │                                          │
  anomaly ───────►│  1. supervisor classifies & escalates    │
                  │                                          │
  host            │  2. host reviews, adds resolution note   │
  resolves ──────►│     to escalation item                   │
                  │                                          │
                  │  3. host appends policy note to          │
                  │     hydration brief (optional but        │
                  │     encouraged)                          │
                  │                                          │
  next cycle ────►│  4. supervisor reads updated brief       │
                  │     on next invocation                   │
                  │                                          │
  same anomaly ──►│  5. supervisor handles autonomously      │
  next time       │     without escalating                   │
                  └─────────────────────────────────────────┘
```

**Policy drift prevention:** The supervisor's policy brief is versioned (via
the work graph's note history). If a policy note causes unexpected behavior,
the host can remove it and add a corrected version. The supervisor always uses
the most recent policy brief.

**Escalation analytics:** Over time, the escalation history reveals which
anomaly classes are most common and which policies are most valuable. The
supervisor emits structured escalation events that can be queried to understand
what the host spends time resolving.

### 8. Implementation Phases

#### Phase 1: Escalation Infrastructure (no agent yet)

Implement the escalation work item protocol without any agent reasoning. The
deterministic loop creates escalation items for pre-defined anomaly patterns
(e.g., stuck job with no output after 15 minutes and no commits on branch).

This validates the communication protocol before the reasoning layer is added.
The host can resolve escalations and update the policy brief. This phase also
establishes the overseer capability token and its enforcement.

**Deliverables:**
- `escalation` work item kind
- Blocking edge auto-insertion on escalation creation
- Auto-resolution: blocking edge removed when escalation transitions to `done`
- Overseer token issuance at `fase serve` startup (requires ADR-0035 phases 1-3)
- `fase work attest` with overseer token (supervisor-driven deterministic checks)

#### Phase 2: Structured Anomaly Triage

Add deterministic anomaly classification to the reactor. On each event, after
the deterministic path runs, check for known anomaly patterns:

| Pattern | Anomaly Class | Default Action |
|---------|--------------|----------------|
| Job stalled, no output, no commits | `stall_no_work` | Escalate after 15m |
| Worker exit 0, job incomplete, no commits | `spurious_exit` | Retry (different adapter) |
| Attestation failed (build/test) | `attest_deterministic_fail` | Create recovery item |
| Attestation failed (review finding) | `attest_judgment_fail` | Escalate |
| Graph cycle detected | `dag_cycle` | Escalate immediately |
| All adapters failing | `adapter_circuit_open` | Escalate |
| Escalation unresolved > 24h | `escalation_stale` | Escalate with urgency bump |

Each anomaly class has a policy-driven action. The initial policies are
hardcoded. Policy notes from the hydration brief override the hardcoded
defaults. This is the policy evaluation engine — no LLM yet.

**Deliverables:**
- `AnomalyClassifier` in supervisor code
- Policy evaluation engine (precedence: hydration brief > hardcoded defaults)
- Escalation creation with structured `[supervisor:escalation]` format
- Recovery item creation for `attest_deterministic_fail`

#### Phase 3: Agent Reasoning Integration

Integrate the LLM reasoning call for anomalies that fall outside the
classification taxonomy. When `AnomalyClassifier` returns `unknown`, invoke
the supervisor agent model with the structured `SupervisorInput`.

The agent call is gated: it only fires for genuinely unclassified anomalies.
Known anomaly classes go through the deterministic policy path (Phase 2).
This minimizes API cost — the vast majority of cycles use no LLM calls.

**Deliverables:**
- `AgenticSupervisor` struct with `Invoke(ctx, input) (SupervisorOutput, error)`
- Adapter integration (uses the existing `claude` or `opencode` adapter)
- Action execution: deterministic loop executes the returned action set
- Fallback: if agent invocation fails, escalate with the original anomaly context
- Config: `config.toml [supervisor]` section

#### Phase 4: Priority and Queue Management

Enable the supervisor to reorder work via edge management (§5). Add the
`dispatch:reorder` action kind to `SupervisorOutput`. Implement the priority
insertion pattern (§5a) and stale edge removal (§5b).

**Deliverables:**
- Priority insertion: supervisor creates fix items and inserts them ahead of queue
- Stale edge detection and removal
- `host:manual` edge flag to protect host-created edges
- Queue re-evaluation trigger (new high-priority work created)

#### Phase 5: Learning Loop Tooling

Add tooling to support the learning loop (§7). Extract policy cases from
resolved escalations. Provide a `fase supervisor policy` command for the host
to review, add, and remove policy notes.

**Deliverables:**
- `fase supervisor policy list` — show current policy notes
- `fase supervisor policy add <trigger> <action>` — add policy note
- `fase supervisor escalations` — show unresolved and recently-resolved escalations
- Escalation analytics: `fase supervisor stats` shows anomaly class frequency

### 9. Interaction with ADR-0035 (Cryptographic Agent Identity)

The agentic supervisor depends on ADR-0035 phases 1-3 for its capability token
and signed actions. The dependency is hard: without the overseer token, the
supervisor cannot be distinguished from a worker in the audit trail.

**New role addition:** ADR-0035's capability table (§2) needs an `overseer`
row. The supervisor CA issues overseer tokens at serve startup using the same
token format as worker tokens, but with the expanded capability set and a
longer expiry. The overseer token is issued to the `supervisor` actor (not
scoped to a work item or job).

**Capability token for supervisor invocations:** When the agentic supervisor
makes a write call (e.g., creating an escalation item), the call carries the
overseer token. The capability enforcement layer verifies the token before
executing. This means the supervisor cannot act outside its defined capability
set — the same enforcement that applies to workers applies to the supervisor.

### 10. Non-Goals

- **Fully autonomous operation.** The supervisor cannot approve or archive
  work. The host agent remains in the loop for all state-advancing decisions.
  The supervisor's autonomy is bounded to reasoning within an already-approved
  scope.

- **Code generation.** The supervisor reasons about the work graph, not code.
  It does not write code, produce diffs, or modify files. Workers do that.

- **Replacing the deterministic loop.** The event-driven reactor (ADR-0037)
  is not replaced. The agent is invoked *by* the reactor, not instead of it.
  The reactor handles the hot path; the agent handles the edge cases.

- **Multi-model consensus.** The supervisor uses a single model. Multi-model
  consensus for supervisor decisions is out of scope — the learning loop (host
  resolution → policy update) is the mechanism for improving decision quality
  over time.

- **External event sources.** The supervisor does not subscribe to external
  systems (CI/CD webhooks, deployment pipelines, Slack). It reasons only about
  work graph state. Integration with external systems goes through the host.

### 11. Risks

| Risk | Mitigation |
|------|------------|
| Supervisor creates too many escalations (alert fatigue) | Phase 2 hardcodes deterministic resolution for common patterns; LLM invoked only for unknown anomalies |
| Agent makes wrong call (bad edge removal, wrong recovery) | All actions are recorded in audit trail with rationale; host can reverse via `work:update` or edge restoration; `host:manual` flag protects host-created edges |
| Overseer token stolen / misused | Token held in memory only (never written to disk); 24h expiry; all actions attributed in audit trail; host can revoke by restarting `fase serve` |
| Policy brief grows too large (context window limit) | Policy notes are versioned; stale/superseded notes are archived by host; brief is designed for consumption in a single context window |
| Agent invocation latency blocks reactor | Agent calls are async; reactor does not block on agent response; if agent times out, the anomaly is escalated using the deterministic path |
| Circular escalation (escalation about an escalation) | Escalations of kind `escalation` are never blocked by other escalations; detection: if escalation graph depth > 2, emit a warning and go straight to host |
| LLM costs for heartbeat invocations | Heartbeat invocations use a thin context window and cheap model; configurable interval; disabled by default in cost-sensitive deployments |

## Consequences

**Positive:**
- Anomaly handling moves from manual host intervention to autonomous resolution
  for known patterns, and structured escalation for unknown ones.
- The host communicates intent through policy notes rather than by being
  online to handle every edge case.
- All supervisor actions are auditable — the overseer token and signed actions
  give the host visibility into what the supervisor did and why.
- The learning loop compounds: each resolved escalation shrinks the set of
  patterns that require future escalation.

**Negative:**
- A new reasoning component adds LLM API calls and associated latency/cost.
- Policy notes require discipline to maintain — stale notes can cause incorrect
  supervisor behavior.
- The overseer token has broad write access; a compromised supervisor (e.g.,
  prompt injection via a malicious work item note) could damage the graph.

**Neutral:**
- The conductor → app-agent → workers hierarchy aligns with choiros-rs, which
  is a positive for future distribution work, but adds conceptual layers that
  need to be understood by operators.
- Phase 1 (escalation infrastructure) provides immediate value without any LLM
  integration, making it independently deployable.
