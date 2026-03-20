Date: 2026-03-20
Kind: ADR
Status: Accepted
Priority: 1
Supersedes: None (extends current supervisor_loop.go)
Owner: Runtime / Supervisor

# ADR-0037: Event-Driven Agentic Supervisor

## Context

The current supervisor (`internal/cli/supervisor_loop.go`) is a polling loop that
runs every 30 seconds (configurable via `--interval` in standalone mode, hardcoded
in serve mode). Each cycle executes a 6-step algorithm: (0) bootstrap empty
repos, (1) reconcile expired leases, (2-4) poll in-flight jobs via subprocess
`fase status --json` + PID check + JSONL mtime stall detection (10-minute
threshold), (5) dispatch ready work. This design was appropriate when the system
had no event infrastructure.

The live agent protocol (ADR: `fase-live-agent-protocol.md`) introduced an
EventBus (`internal/service/events.go`) that publishes structured events on every
work graph mutation. The EventBus has 3 active subscribers: the native conductor
adapter (forwards events as steers to active workers), the WebSocket change
watcher (broadcasts to UI), and the MCP server (forwards to Claude Code channel
protocol). Five adapter implementations span two tiers: (1) subprocess-based
`adapterapi.Adapter` used by the supervisor's `spawnRun` path, and (2)
`adapterapi.LiveAgentAdapter` used by the native conductor pattern with
persistent sessions and per-session event channels. The co-agent messaging
format enables transparent inter-agent communication.

**Important detail:** In serve mode, two polling loops run concurrently:
`runHousekeeping` (30s ticker for lease reconciliation and stall detection) and
`runInProcessSupervisor` (30s sleep for the full dispatch cycle). Both check
for stalls independently — a redundancy the event-driven design should collapse.

The polling loop has three structural limitations:

1. **Latency floor.** A completed job waits up to 30 seconds before the
   supervisor notices and dispatches the next item. For a 10-item serial DAG
   with 2-minute tasks, this adds 5 minutes (25%) of dead time.

2. **Blind recovery.** Crash detection relies on `syscall.Kill(pid, 0)` and
   JSONL mtime checks (10-minute stall threshold), both polled. A worker that
   exits cleanly but commits code without updating the work graph (the "Codex
   premature exit" case) requires the host agent to manually detect, commit,
   and attest. Job status polling shells out to `fase status --json <job-id>`
   as a subprocess (`supervisor.go:482`) rather than querying the DB directly,
   adding latency overhead per poll cycle.

3. **Static routing.** Adapter/model selection uses round-robin rotation with
   work-level preferences. It does not consider adapter health, recent failure
   rates, rate-limit state, or work characteristics beyond `preferred_adapters`.

## Decision

Replace the polling sleep with an event-driven reactor that subscribes to the
EventBus and adapter lifecycle events. The reactor runs the same dispatch
algorithm on-demand rather than on a timer. A background heartbeat (60s) ensures
liveness for edge cases where events are lost. The two concurrent polling loops
in serve mode (`runHousekeeping` + `runInProcessSupervisor`) are collapsed into
a single event-driven loop.

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Event-Driven Supervisor                   │
│                                                             │
│  ┌──────────┐   ┌──────────┐   ┌──────────┐   ┌─────────┐ │
│  │ EventBus │   │ Adapter  │   │ Process  │   │  Timer  │ │
│  │Subscriber│   │ Events   │   │ Watcher  │   │ (60s)   │ │
│  └────┬─────┘   └────┬─────┘   └────┬─────┘   └────┬────┘ │
│       │              │              │              │       │
│       v              v              v              v       │
│  ┌─────────────────────────────────────────────────────┐   │
│  │              Unified Event Channel                   │   │
│  │  (WorkEvent | AdapterEvent | ProcessEvent | Tick)    │   │
│  └──────────────────────┬──────────────────────────────┘   │
│                         │                                   │
│                         v                                   │
│  ┌─────────────────────────────────────────────────────┐   │
│  │                   Reactor Loop                       │   │
│  │                                                     │   │
│  │  match event:                                       │   │
│  │    WorkEvent(attested, done, failed) → tryDispatch  │   │
│  │    WorkEvent(created)                → tryDispatch  │   │
│  │    AdapterEvent(turn.completed)      → reconcileJob │   │
│  │    AdapterEvent(turn.failed)         → handleFail   │   │
│  │    AdapterEvent(session.closed)      → handleCrash  │   │
│  │    ProcessEvent(exited)              → handleExit   │   │
│  │    Tick                              → fullCycle    │   │
│  └──────────────────────┬──────────────────────────────┘   │
│                         │                                   │
│                         v                                   │
│  ┌──────────────┐  ┌──────────┐  ┌──────────────────────┐  │
│  │   Dispatch   │  │ Recovery │  │    Routing Engine     │  │
│  │   Engine     │  │  Engine  │  │  (adapter selection)  │  │
│  └──────────────┘  └──────────┘  └──────────────────────┘  │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │              Co-Agent Message Bus                    │   │
│  │  [fase:message from="supervisor" type="..."]        │   │
│  └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

### 1. Event Sources

The reactor multiplexes four event sources into a single channel:

#### 1a. EventBus (existing)

Publishes 5 event kinds on every work graph mutation:
`work_created`, `work_updated`, `work_claimed`, `work_released`,
`work_attested`. A 6th kind (`work_lease_renewed`) is defined but never
published — `RenewWorkLease` (service.go:1943) creates a work update record
but does not call `s.Events.publish()`. The supervisor subscribes via
`svc.Events.Subscribe()`.

The publish mechanism is non-blocking: `select { case ch <- ev: default: }`
(events.go:57). Slow consumers are silently dropped. Each subscriber gets a
buffered channel of size 64. Current subscribers:
- `conductorSession` (native/live.go:133) — forwards events as steers to active workers
- `runChangeWatcher` (serve.go:451) — broadcasts to WebSocket UI
- `mcpserver.runEventForwarder` (mcpserver/server.go:91) — forwards to Claude Code channel

**New events needed:**

| Event | Trigger | Purpose |
|-------|---------|---------|
| `work_completed` | Worker calls `fase work update --state done` | Distinguish completion from generic update |
| `work_failed` | Worker or supervisor marks failed | Trigger retry/recovery without waiting for poll |
| `job_state_changed` | Job transitions to terminal state | Decouple job lifecycle from work lifecycle |

These are refinements of `work_updated` — the existing event fires but doesn't
carry enough semantic information. Adding `Kind` specificity to `WorkEvent` (or
a `Reason` field) is sufficient; no new publish sites needed.

#### 1b. Adapter Lifecycle Events (new integration)

Live adapters already emit events on `LiveSession.Events()`:
`turn.completed`, `turn.failed`, `session.closed`, `error`.

Currently only the native conductor subscribes. The event-driven supervisor
subscribes to adapter events for all in-flight live sessions, giving it
immediate notification of:
- Turn completion (job finished)
- Turn failure (job failed with structured error)
- Session closure (adapter crashed or exited)
- Transport errors (connection lost)

For subprocess-based adapters (current `spawnRun` model), the supervisor
does not have a `LiveSession` handle. Two paths:

**Path A (incremental):** Keep subprocess dispatch for non-live adapters. Add
a `ProcessWatcher` goroutine (see 1c) that uses `os.Process.Wait()` instead
of polling `Kill(pid, 0)`.

**Path B (target):** Migrate all dispatch to use the `LiveAgentAdapter`
interface. The supervisor starts a `LiveSession`, calls `StartTurn()`, and
receives events on the session's event channel. This unifies the event model
but requires the supervisor to manage session lifecycles directly.

**Recommendation:** Path A first, Path B as follow-up. Path B is the
architecturally clean end state, but Path A delivers 80% of the latency
improvement with minimal change.

#### 1c. Process Watcher (replaces PID polling)

Replace `isProcessAlive(pid)` polling with `os.Process.Wait()` in a dedicated
goroutine per worker process:

```go
type ProcessEvent struct {
    WorkID    string
    JobID     string
    PID       int
    ExitCode  int
    ExitError error
}

func watchProcess(pid int, workID, jobID string, ch chan<- ProcessEvent) {
    proc, err := os.FindProcess(pid)
    if err != nil {
        ch <- ProcessEvent{WorkID: workID, JobID: jobID, PID: pid, ExitCode: -1, ExitError: err}
        return
    }
    state, err := proc.Wait()
    ch <- ProcessEvent{
        WorkID:    workID,
        JobID:     jobID,
        PID:       pid,
        ExitCode:  state.ExitCode(),
        ExitError: err,
    }
}
```

This eliminates the 30s detection delay for crashed workers. The goroutine
blocks on `Wait()` and sends exactly one event when the process exits.

**Caveat:** `os.Process.Wait()` only works for child processes. Since workers
are spawned via a two-level process hierarchy (`supervisor` → `fase run --json`
→ `__run-job` detached), the process watcher has a subtlety: the `fase run`
parent exits quickly after returning the job ID, and the actual `__run-job`
worker is re-parented to init with `Setpgid: true` and detached I/O. The
process watcher can only wait on the `fase run` subprocess (child of
supervisor), not on the detached `__run-job` worker. When `fase run` exits
cleanly, the watcher learns the job was launched but not its completion status.

```
supervisor (this process)
  └─ fase run --json   (short-lived, returns job ID and exits)
       └─ __run-job       (detached worker, Setpgid=true, survives parent)
```

Three options:

- **Option 1:** Change `spawnRun` to not detach. The supervisor goroutine
  calls `cmd.Wait()` directly on the worker. Worker death = process exit.
  Simplest change but supervisor death kills all workers.

- **Option 2:** Keep detached workers. Use pidfd (Linux) or kqueue (macOS)
  to watch non-child PIDs. Go's `os.Process.Wait()` doesn't support this, but
  platform-specific syscalls do.

- **Option 3 (recommended):** Eliminate the two-level process hierarchy
  entirely. Instead of `spawnRun` → `fase run --json` → `launchDetachedWorker`,
  have the supervisor directly call `launchDetachedWorker` (service.go:3810)
  or better, migrate to the `LiveAgentAdapter` interface where the supervisor
  manages the session lifecycle and receives events directly. For the interim,
  use `fsnotify` on the JSONL output directory (`.fase/raw/stdout/<jobID>/`)
  as a liveness signal. No output for 10 minutes = stall (matching the current
  `isJobStalled` threshold). Combined with job state polling on EventBus
  events, this covers the gap. Note: this adds an `fsnotify` dependency that
  does not currently exist in the codebase.

#### 1d. Heartbeat Timer (safety net)

A 60-second ticker triggers a full cycle as a catch-all. This handles:
- Events lost due to buffer overflow (non-blocking publish)
- Lease renewal (still time-based)
- Edge cases where no event fires (e.g., external process modifies DB)

The heartbeat is intentionally longer than the current 30s because event-driven
reactions handle the hot path. It only covers cold-path recovery.

### 2. Reactor Loop

The reactor replaces the `for { runOneCycle(); sleep(30s) }` pattern:

```go
func (l *eventDrivenLoop) run(ctx context.Context, svc *service.Service) {
    workCh := svc.Events.Subscribe()
    defer svc.Events.Unsubscribe(workCh)

    ticker := time.NewTicker(60 * time.Second)
    defer ticker.Stop()

    // First cycle: full reconciliation (same as current cycle 1).
    l.fullCycle(ctx, svc)

    for {
        select {
        case <-ctx.Done():
            l.cancelInFlight(ctx, svc)
            return

        case ev := <-workCh:
            l.handleWorkEvent(ctx, svc, ev)

        case pev := <-l.processCh:
            l.handleProcessExit(ctx, svc, pev)

        case <-ticker.C:
            l.heartbeat(ctx, svc)
        }
    }
}
```

#### Event Handlers

**`handleWorkEvent`:**

| Event Kind | Action |
|-----------|--------|
| `work_created` | Check if ready, try dispatch |
| `work_updated` (to `done`/`failed`) | Clean up in-flight, try dispatch next |
| `work_attested` | Check if attestation unblocks downstream work, try dispatch |
| `work_released` | Item returned to pool — try dispatch |
| `work_lease_renewed` | Update internal lease tracker |

**`handleProcessExit`:**

| Exit Code | Action |
|-----------|--------|
| 0 (clean exit) | Poll job state; if completed → normal completion flow |
| Non-zero | Mark work failed; check for partial results (recovery engine) |
| Signal death | Mark work failed; check git status for uncommitted work |

**`heartbeat`:**

- Reconcile expired leases
- Check stalled jobs (JSONL mtime fallback)
- Renew leases for jobs approaching expiry
- Try dispatch if capacity available

### 3. Recovery Engine

The recovery engine handles cases the host agent currently manages manually.

#### 3a. Premature Exit with Committed Code

**Scenario:** Codex exits (exit code 0 or non-zero) but has committed code
to the working branch.

**Detection:**
```
ProcessEvent(exitCode=0) + job state != "completed"
  → git log --oneline HEAD...<work-branch> shows new commits
  → commits exist that aren't reflected in work state
```

**Recovery action:**
1. Check `git log` for commits since the work item's `head_commit_oid`.
2. If new commits exist:
   a. Update work item's `head_commit_oid` to latest commit.
   b. Transition work to `awaiting_attestation`.
   c. Log recovery action as a work note.
   d. Dispatch attestation job for the new commits.
3. If no new commits: mark work as failed for retry.

#### 3b. Adapter Crash Mid-Turn

**Scenario:** Adapter process crashes (SIGSEGV, OOM, transport disconnect).

**Detection:** `ProcessEvent` with non-zero exit code, or
`AdapterEvent(session.closed)` with active turn.

**Recovery action:**
1. Check git for partial commits.
2. If clean (no partial work): mark failed, retry with different adapter.
3. If partial commits exist: same as 3a — preserve work, attest what's there.
4. Record adapter crash in routing engine's health tracker.

#### 3c. Stalled Job Recovery

**Scenario:** Job produces no output for extended period.

**Detection:** `fsnotify` on output directory, or heartbeat mtime check.

**Recovery action:**
1. Attempt interrupt via `LiveSession.Interrupt()` if live session exists.
2. Wait 30s for graceful shutdown.
3. If still running: kill process group.
4. Check git for committed work (same as 3a).
5. Mark work failed with "stalled" reason.

#### 3d. Attestation Failure Recovery

**Scenario:** Attestation job reports failure (tests don't pass, review
finds issues).

**Detection:** `WorkEvent(attested)` with `result=failed`.

**Recovery action:**
1. Read attestation summary for failure details.
2. Create a recovery work item (kind=`recovery`) with:
   - Original work item as context (via `discovered_from` edge).
   - Attestation failure summary in objective.
   - Preference for different adapter than original (rotation offset).
3. Dispatch recovery item.

**Note:** This is currently handled by the host agent reviewing attestations.
Automating it requires a policy decision: which failures are auto-recoverable
vs. which need human review. Start with deterministic failures (build fails,
test fails) as auto-recoverable. Non-deterministic failures (code review
findings) require human triage.

### 4. Routing Engine

Replace the current 3-tier routing algorithm with a scoring-based router.

#### 4a. Current Routing (to be replaced)

The existing `pickAdapterModel` (supervisor.go:68) uses a 3-tier priority:
1. **Work preference**: If `PreferredAdapters[0]` is set, use that adapter.
   Budget-exhausted preferences are still honored (line 76-78: "work items
   that pin an adapter know what they want").
2. **Job history offset**: Find the most recent job's adapter in the rotation
   pool, advance to `(lastIdx+1) % len(pool)` — ensures retries use a
   different adapter.
3. **Global round-robin**: Atomic counter `globalRotationIdx` incremented per
   dispatch.

The default rotation pool is: claude (claude-sonnet-4-6), codex (gpt-5.4-mini),
opencode (zai-coding-plan/glm-5-turbo). Overridden by `config.toml
[rotation]`. Budget filtering (`budget.go:118`) removes entries exceeding
`max_runs_per_day` (fail-open: if all exhausted, returns full pool).

**Bug:** `ForbiddenAdapters` is checked at ready-list time by
`workHasAvailableRuntime` (store.go:5023) but NOT checked by `pickAdapterModel`
during dispatch. A work item whose `forbidden_adapters` only excludes one
adapter could still be dispatched to it if `pickAdapterModel` selects it,
because the forbidden check gates whether the item appears in the ready list,
not which adapter is chosen. The scoring router should check forbidden
adapters at dispatch time.

**No health tracking or circuit breaking exists.** Failed adapters stay in
the rotation pool. The only filtering is budget-based (daily run counts) and
preference-based.

#### 4b. Adapter Health Tracking

```go
type AdapterHealth struct {
    Adapter        string
    Model          string
    RecentFailures int       // failures in last hour
    LastFailure    time.Time
    LastSuccess    time.Time
    AvgDuration    time.Duration // rolling average of last 10 jobs
    RateLimited    bool
    RateLimitUntil time.Time
}
```

Updated on every job completion/failure. Persisted in `.fase/adapter_health.json`
across supervisor restarts.

#### 4c. Work-Aware Routing

Score each adapter/model pair for a given work item:

```
score(adapter, work) =
    base_score(adapter)                          // from rotation config
  + preference_bonus(adapter, work)              // work.preferred_adapters
  - failure_penalty(adapter, work.kind)          // recent failures for this kind
  - rate_limit_penalty(adapter)                  // currently rate-limited
  + kind_affinity(adapter, work.kind)            // learned: codex good at tests
  - cost_weight(adapter, work.budget_class)      // budget-aware
```

**Kind affinity** is a simple lookup table derived from conventions:

| Work Kind | Preferred | Reason |
|-----------|-----------|--------|
| `implement` | claude | Complex implementation |
| `attest` | codex, opencode | Different adapter than implementer |
| `research`, `plan` | opencode | Bulk read-only, cheap |
| `review` | opencode | Analysis, cheap |
| `recovery` | claude | Needs judgment |

This replaces the hardcoded `workRotation` slice. The rotation still exists
as the default ordering, but scoring adjusts it per-dispatch.

#### 4d. Circuit Breaker

If an adapter fails 3 consecutive times within an hour, it enters a
circuit-breaker state:
- **Open:** No dispatch for 5 minutes.
- **Half-open:** Allow one dispatch. If it succeeds, close. If it fails,
  re-open for 10 minutes.

This prevents burning budget on a broken adapter while allowing recovery.

### 5. Co-Agent Messaging

The supervisor uses the co-agent messaging format for all communications
visible to workers or the host agent.

#### Outbound Messages (supervisor → worker via steering)

```
[fase:message from="supervisor" type="info"]
Work graph update: work_01ABC attestation passed. Your blocking dependency
is resolved — you may proceed with implementation.
[/fase:message]

[fase:message from="supervisor" type="request"]
Job stall detected (no output for 8 minutes). Are you blocked? If so,
describe the blocker and I will attempt recovery.
[/fase:message]
```

#### Status Messages (supervisor → WebSocket → UI)

```json
{
  "type": "supervisor_event",
  "data": {
    "action": "dispatch",
    "work_id": "work_01ABC",
    "adapter": "codex",
    "model": "gpt-5.4-mini",
    "reason": "highest-scored adapter for attest kind (score: 0.82)"
  }
}
```

#### Recovery Messages (supervisor → work notes)

```
[fase:message from="supervisor" type="info"]
Recovery: Worker exited (pid 12345, exit code 1) but 2 commits found on
branch. Preserving committed work. Transitioning to awaiting_attestation.
Attestation job will verify committed changes.
[/fase:message]
```

These are recorded as work notes (type=`finding`) for auditability.

### 6. EventBus Enhancements

The current EventBus needs minor extensions:

#### 6a. Richer Event Payloads

```go
type WorkEvent struct {
    Kind      WorkEventKind
    WorkID    string
    Title     string
    State     string
    PrevState string
    // New fields:
    JobID     string            // associated job, if any
    Adapter   string            // adapter that produced this event
    Metadata  map[string]string // extensible key-value pairs
}
```

The `JobID` and `Adapter` fields let the reactor correlate events to in-flight
jobs without querying the database or shelling out to `fase status`.

#### 6b. Publish `work_lease_renewed`

The `WorkEventLeaseRenew` constant (events.go:14) is dead code — defined but
never published. `RenewWorkLease` (service.go:1943) should call
`s.Events.publish(WorkEvent{Kind: WorkEventLeaseRenew, ...})`. This enables
the supervisor to track lease activity without periodic DB queries.

#### 6c. Buffer Size and Delivery Guarantee

Current publish is fire-and-forget: `select { case ch <- ev: default: }`
(events.go:57). Slow consumers are silently dropped. Buffer size is 64 per
subscriber. With 3 existing subscribers (conductor, change watcher, MCP), a
burst of 20 work mutations would fill 60% of each subscriber's buffer.

**Recommendation:** Increase supervisor's subscription buffer to 256. The
heartbeat catches anything missed. The supervisor must never be a bottleneck
for work mutations — blocking publish would be worse than dropping events.

### 7. Migration Path

#### Phase 1: Event-Triggered Dispatch (low risk)

Change `runInProcessSupervisor` to subscribe to EventBus and react to
`work_updated`, `work_attested`, `work_released` events by calling
`tryDispatch()` immediately. Collapse the redundant `runHousekeeping` ticker
into the heartbeat. Keep the 30s heartbeat (not 60s initially — use 30s to
match existing lease renewal interval of 10 minutes within the 30-minute
lease duration) for lease renewal and stall detection. This is a ~50 line
change.

The key insight: `work_updated` fires when a worker calls `fase work update
--state done`, and `work_attested` fires when attestation completes. Both are
already published by `UpdateWork` (service.go:2062) and `AttestWork`
(service.go:2413). The supervisor just needs to subscribe and react.

**Impact:** Dispatch latency drops from 0-30s to near-zero for the common
case (work completed → next item dispatched). Eliminates one of the two
concurrent polling loops in serve mode.

#### Phase 2: Eliminate Subprocess Polling (medium risk)

Replace `pollJobStatus` (which shells out to `fase status --json <job-id>`)
with direct DB queries for job state. Add `os.Process.Wait()` goroutines for
the `fase run` child process (not the detached `__run-job` worker). For
detached workers, add `fsnotify` on the JSONL output directory
(`.fase/raw/stdout/<jobID>/`) as a liveness signal with 10-minute stall
threshold matching `isJobStalled`.

This phase adds an `fsnotify` dependency. The alternative (skip fsnotify,
keep mtime polling in heartbeat) is simpler but retains the 10-minute stall
detection latency for detached workers.

**Impact:** Crash detection drops from 0-30s to near-zero for child processes.
Eliminates subprocess spawning overhead from `pollJobStatus` (~1 subprocess
per in-flight job per 30s cycle).

#### Phase 3: Recovery Engine (medium risk)

Implement git-aware recovery for premature exit (3a) and attestation failure
recovery for deterministic failures (3d).

**Impact:** Reduces host agent manual recovery workload.

#### Phase 4: Routing Engine (low risk)

Replace round-robin with scoring-based routing. Add health tracking and
circuit breaker.

**Impact:** Better adapter utilization, fewer wasted retries.

#### Phase 5: Live Session Integration (high risk)

Migrate dispatch from subprocess `spawnRun` (which spawns `fase run --json`
as a child, which in turn calls `launchDetachedWorker` to fork a detached
`__run-job`) to `LiveAgentAdapter.StartSession` + `StartTurn`. The supervisor
manages session lifecycles directly and receives adapter events on the session
channel. This eliminates the two-level process hierarchy entirely.

All 4 live adapters (native conductor, codex JSON-RPC, pi JSONL, opencode
REST+SSE) already implement the `LiveAgentAdapter` interface. The 2 remaining
subprocess-only adapters (gemini, factory) would either get live
implementations or continue on the subprocess path.

**Impact:** Full event-driven lifecycle — no PID polling, no mtime checks,
no subprocess spawning for status, structured error reporting from adapters.

### 8. Two-Tier Adapter Architecture

The codebase has two distinct dispatch paths that the event-driven supervisor
must bridge:

| Tier | Interface | Used By | Events | Session Lifecycle |
|------|-----------|---------|--------|-------------------|
| **Subprocess** | `adapterapi.Adapter` | `supervisor_loop.go` via `spawnRun` | None — fire-and-forget | `fase run` → `__run-job` (detached) |
| **Live** | `adapterapi.LiveAgentAdapter` | `native conductor` via `StartSession` | Per-session `Events()` channel | Supervisor manages directly |

The subprocess tier (claude, codex, opencode, pi, gemini, factory adapters in
`adapterapi.Adapter`) spawns CLI processes, returns `*RunHandle` with
stdout/stderr pipes, and does NOT emit events on any EventBus. Job completion
is detected via polling `fase status --json`.

The live tier (native, codex-live, opencode-live, pi-live adapters in
`adapterapi.LiveAgentAdapter`) maintains persistent sessions and emits
structured events (`turn.completed`, `turn.failed`, `session.closed`, etc.) on
a per-session channel. The native conductor is the only adapter that currently
bridges live sessions with the EventBus — it subscribes to EventBus events and
forwards them as steers to active workers via `conductorSession.conductorLoop`
(native/live.go:257).

The event-driven supervisor must handle both tiers during the migration:
subprocess adapters via process watchers and fsnotify (Phases 1-2), live
adapters via session event channels (Phase 5).

### 9. Reconciliation and Startup Behavior

The current `ReconcileOnStartup` (service.go:1738) is a blunt reset:
1. Releases all expired leases
2. **Fails all orphan jobs** — marks `JobStateRunning` → `JobStateFailed`
3. **Releases all stale claims** — resets `claimed`/`in_progress` → `ready`

This means any work in-flight when the supervisor restarts is failed and
returned to the ready pool. The event-driven supervisor should preserve this
behavior for startup safety, but the recovery engine (Phase 3) should add a
more nuanced approach: check git for committed work on failed-orphaned items
before resetting them.

The `ReconcileExpiredLeases` (service.go:1715) runs every cycle and is
responsible for releasing claims where `claimed_until <= now`. This remains
necessary as a heartbeat function but should be triggered less frequently
(every 60s) since event-driven reactions handle the hot path.

### 10. Interface Changes

#### New: `eventDrivenLoop` (replaces or wraps `supervisorLoop`)

```go
type eventDrivenLoop struct {
    supervisorLoop                      // embed existing loop for state/config
    processCh    chan ProcessEvent       // from process watchers
    health       *AdapterHealthTracker  // routing engine state
    recovery     *RecoveryEngine        // recovery policy + actions
}

func (l *eventDrivenLoop) run(ctx context.Context, svc *service.Service)
func (l *eventDrivenLoop) handleWorkEvent(ctx context.Context, svc *service.Service, ev service.WorkEvent)
func (l *eventDrivenLoop) handleProcessExit(ctx context.Context, svc *service.Service, ev ProcessEvent)
func (l *eventDrivenLoop) heartbeat(ctx context.Context, svc *service.Service)
func (l *eventDrivenLoop) tryDispatch(ctx context.Context, svc *service.Service)
```

#### Modified: `WorkEvent` (extended payload)
Add `JobID`, `Adapter`, `Metadata` fields. Backward compatible — existing
subscribers ignore new fields.

#### New: `AdapterHealthTracker`

Tracks per-adapter success/failure rates, average durations, rate-limit state.
Persisted to `.fase/adapter_health.json`.

#### New: `RecoveryEngine`

Policy-driven recovery for premature exits, stalls, and attestation failures.
Configured via `config.toml` recovery section.

### 11. Non-Goals

- **Distributed supervisor.** Single-process supervisor is sufficient for the
  current single-repo, single-machine deployment model.
- **Event sourcing.** The work graph is the source of truth, not the event
  stream. Events are notifications, not the log of record.
- **Custom event handlers.** The reactor is a fixed algorithm, not a plugin
  system. Extensibility comes from the work graph and routing config, not
  from user-defined event handlers.
- **Replacing the EventBus.** The current simple fan-out bus is adequate.
  No need for a message broker, persistent queue, or pub/sub system.

### 12. Risks

| Risk | Mitigation |
|------|------------|
| Event storms (many rapid mutations) | Debounce dispatch attempts; at most one dispatch evaluation per 500ms |
| Missed events (buffer overflow) | Heartbeat catches anything missed; increase supervisor buffer to 256 |
| Recovery engine makes wrong call | Start with deterministic-only auto-recovery; human review for judgment calls |
| Process watcher goroutine leak | Track watchers in map; clean up on supervisor shutdown |
| Backward compatibility | `supervisorLoop` is embedded, not replaced; all existing behavior preserved |
| fsnotify dependency (Phase 2) | New external dependency; gate behind build tag if needed; fallback to mtime polling |
| Two-tier adapter migration (Phase 5) | Subprocess and live adapters coexist during migration; no flag day |
| ForbiddenAdapters routing gap | Scoring router checks forbidden adapters at dispatch time (fixes existing bug) |

## Consequences

**Positive:**
- Dispatch latency drops from 0-30s to sub-second for the common case.
- Crash/stall detection becomes near-instant instead of polling-delayed.
- Automated recovery reduces host agent toil for common failure modes.
- Smarter routing reduces wasted budget on broken adapters.
- Foundation for Phase 5 (live session integration) unifies the execution model.

**Negative:**
- More goroutines (process watchers, event subscription).
- Recovery engine adds decision logic that can be wrong.
- Debugging event-driven flow is harder than debugging a sequential loop.

**Neutral:**
- The heartbeat means the system is hybrid event+poll, not pure event-driven.
  This is intentional — pure event-driven is fragile when events can be lost.
