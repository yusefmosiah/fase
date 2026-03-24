# Deep Review: Simplify FASE Around a Singular Worker Contract

**Work ID:** `work_01KMF31JMH9SCZRH8E9PM72DKZ`
**Date:** 2026-03-24

---

## (A) Map of the Current System As-Is

### 1. Communication Paths

FASE has **7 distinct communication paths** where there should be **1 pattern**:

| Path | Mechanism | Entry Point | Notes |
|------|-----------|-------------|-------|
| Worker -> Host | MCP tool `notify_host` | `mcpserver/tools_channel.go:19` | JSON-RPC notification via stdout |
| Worker -> Service | MCP tools `work_update`, `work_note_add`, etc. | `mcpserver/server.go:277-408` | State mutations |
| Supervisor -> Host | HTTP POST `/api/channel/send` | `supervisor_agent.go:348-363` | Same endpoint as CLI notify |
| Host -> Supervisor | HTTP POST `/api/supervisor/send` | `serve.go:1662-1684` | Pushes to `hostCh` (cap 16, drops silently) |
| Host -> Worker | MCP tool `session_send` | `mcpserver/server.go:436-451` | Via Service.Send() |
| Supervisor -> Worker | `Service.Send()` directly | `supervisor_agent.go:124,169` | Same as Host->Worker |
| CLI notify | HTTP POST `/api/channel/send` | `cli/check.go:151-179` | Duplicates Worker->Host path |

**Core problem:** A worker uses MCP tools (stdout). A supervisor uses HTTP. The CLI uses HTTP. The host uses MCP. There is no unified "send message" abstraction — each actor has its own bespoke path.

### 2. Naming Inconsistencies

| Concept | Worker calls it | Supervisor calls it | CLI calls it | Service calls it |
|---------|----------------|-------------------|-------------|-----------------|
| "Tell the orchestrator something" | `notify_host` | `notifyHost()` | `fase notify` | `SendChannelEvent()` |
| "Send work to agent" | N/A | `svc.Send()` | `POST /api/dispatch` | `Run()` / `queueContinuation()` |
| "Receive messages" | MCP stdin | `hostCh` channel | N/A | EventBus.Subscribe() |

**A worker shouldn't know if it's talking to a supervisor or host.** The tool is called `notify_host` but the supervisor intercepts it. Rename to just `notify` or `report`.

### 3. Three API Surfaces — Divergence Map

| Operation | MCP | CLI | HTTP | Gap |
|-----------|-----|-----|------|-----|
| work list | Y | Y | Y | - |
| work show | Y | Y | Y | - |
| work create | Y | Y | Y | - |
| work claim | Y | Y | via update | HTTP missing dedicated endpoint |
| work update | Y | Y | Y | - |
| ready work | Y | Y | Y | - |
| work notes | Y | Y | - | HTTP missing |
| work note-add | Y | Y | - | HTTP missing |
| work attest | Y | Y | Y | - |
| check record create | Y | Y | Y | - |
| check record list | Y | - | Y | CLI missing |
| check record show | Y | - | Y | CLI missing |
| notify_host | Y | Y (`fase notify`) | Y (`/api/channel/send`) | - |
| session_send | Y | - | Y (`/api/supervisor/send`) | CLI missing |
| send_escalation_email | Y | - | - | CLI+HTTP missing |
| work block | - | Y | via update | MCP missing |
| work archive | - | Y | via update | MCP missing |
| work retry | - | Y | via update | MCP missing |
| work edge add/rm/ls | - | Y | Y | MCP missing |
| proposals | - | Y | Y | MCP missing |
| dispatch | - | - | Y | MCP+CLI missing |
| supervisor pause/resume | - | - | Y | MCP+CLI missing |

**How to guarantee sync:** All three surfaces should be thin wrappers over `Service` methods. Currently, `serve.go` contains ~200 lines of dispatch logic that belongs in `Service`. The HTTP handler should be `svc.Dispatch()`, not inline logic.

### 4. Work Item State Machine

**Defined states (10):**
```
ready, claimed, in_progress, checking, awaiting_attestation,
blocked, done, failed, cancelled, archived
```

**Spec states (5):** `ready -> doing -> checking -> done | failed`

**Actual transitions observed in code:**

```
                    ┌──────────────────────────────────────────┐
                    │                                          │
  create ──> ready ──> claimed ──> in_progress ──> checking ──> done
                │         │            │              │          │
                │         │            │              └──> failed│
                │         │            └──> failed               │
                │         └──> failed                            │
                └──> blocked                                     │
                └──> cancelled                                   │
                └──> archived <──────────────────────────────────┘
```

**Dead/redundant states:**
- `claimed`: Never set as execution_state by claim logic. `ClaimWork()` sets `ClaimedBy`/`ClaimedUntil` fields but leaves execution_state unchanged. Then `markWorkQueued()` sets it to `claimed` — but this is a second, redundant transition.
- `awaiting_attestation`: Spec says removed, but still used at `service.go:3217` when work has attestation children and state was `in_progress`.
- `in_progress`: Must be manually set by worker via `work_update`. Never auto-set.

**Proposal:** Collapse to 6 states: `ready`, `doing`, `checking`, `done`, `failed`, `blocked`. Drop `claimed` (use fields), `in_progress` (merge with `doing`), `awaiting_attestation` (merge with `checking`), `cancelled`/`archived` (use `done` + metadata).

### 5. Supervisor Mechanisms

The supervisor has **5 interacting mechanisms:**

| Mechanism | Location | Purpose |
|-----------|----------|---------|
| EventBus subscription | `supervisor_agent.go:65` | Primary signal for work state changes |
| Event debouncing | `supervisor_agent.go:216` | 2s window to batch burst events |
| Poll timer | `supervisor_agent.go:286` | 5s backup polling for job status |
| Exponential backoff | `supervisor_agent.go:93-150` | 2^n seconds (cap 5min) on failed jobs |
| Context rotation | `supervisor_agent.go:145-149` | Fresh session after 10 productive turns |

**Interaction complexity:**
- EventBus can drop events silently (cap 64 channel, `events.go:158-159`)
- Poll timer exists as backup for dropped events
- Backoff resets on host message (good)
- 5 consecutive failures -> restart with fresh session
- 10 productive turns -> restart with fresh session (context overflow prevention)

**Can this be simplified?** The EventBus + poll timer dual path is necessary given the unreliable channel. But the debouncing logic (2s window) adds complexity that could be removed — just process events as they arrive. Context rotation at 10 turns is arbitrary and could be tuned or removed if sessions handle context well.

### 6. Worker Briefing Contract

**12 rules for regular workers, 9 for attestation workers.**

**Actually enforced by code (3):**
1. State transitions to `done` require resolved attestations (`guardDoneTransition()`)
2. Attestation children auto-spawn and block parent completion
3. `work_attest` call required for attest-kind workers (enforced by state machine)

**Aspirational only (9):**
1. Git commit before exit — not validated
2. `fase work update` to `checking`/`failed` — not validated (worker can just exit)
3. `notify_host` before exit — not validated
4. Record notes — not validated
5. Run verification — not validated
6. Web UI must have e2e tests — not validated
7. Don't create child work — not validated
8. Don't call `work attest` (for non-attest workers) — not validated
9. Include specific info in notifications — not validated

**Minimal contract that works:**
1. Set execution_state to `checking` or `failed` before exit
2. For attest workers: call `work attest` with result
3. Everything else is guidance

### 7. Dispatch Path — Failure Points

```
ReadyWork() -> HTTP POST /api/dispatch -> HydrateWork() -> ClaimWork()
  -> createWorktree() -> svc.Run() -> markWorkQueued() -> launchDetachedWorker()
```

**Where it fails:**
1. **Worktree creation** (non-fatal, falls back to main CWD — but worker may corrupt shared state)
2. **Claim race** (two dispatches claim same work — mitigated by DB lock but timing window exists)
3. **launchDetachedWorker** (fatal to job, no parent monitoring of child process)
4. **Worker crash** (job stays in `Queued` forever — no heartbeat, no timeout)

**Why workers don't report back:**
- Worker is a detached background process with no supervision of its PID
- MCP tool calls can fail silently (no ack mechanism)
- No heartbeat from worker to service
- No timeout for stalled jobs in Queued state
- Event channel can drop (cap 64), supervisor may miss state change (recovered by 5s poll)

---

## (B) Simplified Contract Proposal

### The Singular Contract

Every FASE agent (worker, supervisor, checker) follows ONE pattern:

```
1. RECEIVE work via briefing (prompt with work_id, instructions, context)
2. DO the work (code, review, check — whatever the kind requires)
3. REPORT via `fase report <work_id> --state <next_state> --message "<summary>"`
4. EXIT
```

That's it. Three verbs: **receive, do, report.**

### Unified Naming

| Current (inconsistent) | Proposed (unified) |
|------------------------|-------------------|
| `notify_host` MCP tool | `report` MCP tool |
| `notifyHost()` supervisor method | `report()` |
| `fase notify` CLI | `fase report` CLI |
| `work_update` for state changes | `report` (combines state + message + notification) |
| `session_send` | `message` (send message to running session) |
| `SendChannelEvent()` | `Report()` service method |

### Unified State Machine (6 states)

```
ready -> doing -> checking -> done
           |         |
           v         v
         failed    failed
           |
           v
        blocked -> ready (unblock)
```

- `ready`: Work can be claimed and dispatched
- `doing`: Worker is executing (set automatically on dispatch, not manually)
- `checking`: Checker is verifying (set by worker reporting success)
- `done`: Work passed verification
- `failed`: Work or check failed
- `blocked`: Waiting on dependency

Drop: `claimed` (use ClaimedBy field), `in_progress` (rename to `doing`), `awaiting_attestation` (merge with `checking`), `cancelled`/`archived` (use `done` + metadata flag).

### Unified Report Contract

The `report` tool/CLI combines three current operations into one atomic action:

```json
{
  "work_id": "work_...",
  "state": "checking",           // required: next state
  "message": "Implemented X",    // required: summary
  "notes": [                     // optional: findings
    {"type": "finding", "body": "..."}
  ]
}
```

This replaces: `work_update` + `notify_host` + `work_note_add` as separate calls.

### API Surface Guarantee

All operations defined once in `Service`, exposed identically on all three surfaces:

```
Service.Report(ctx, ReportRequest) -> exposed as:
  - MCP tool: report
  - CLI: fase report <work_id> --state X --message Y
  - HTTP: POST /api/work/report
```

New operations added to `Service` get auto-generated handlers via a registry pattern (or at minimum, a compile-time check that all Service methods have corresponding handlers).

---

## (C) Specific Refactoring Steps (Ordered by Impact)

### Phase 1: State Machine Cleanup (High impact, low risk)

1. **Merge `in_progress` into `doing`** — rename constant, update all references. Pure mechanical.
2. **Remove `awaiting_attestation`** — replace with `checking` everywhere. Update `service.go:3217`.
3. **Auto-set `doing` on dispatch** — when `markWorkQueued()` runs, set state to `doing` instead of `claimed`.
4. **Remove `claimed` execution state** — claim is tracked via `ClaimedBy`/`ClaimedUntil` fields, not state.
5. **Audit `cancelled`/`archived`** — if unused, remove. If used, keep but document.

### Phase 2: Unified Report (High impact, medium risk)

6. **Create `Service.Report()` method** — atomic operation that: updates state, adds message, publishes event, sends notification. One call replaces three.
7. **Rename `notify_host` MCP tool to `report`** — update `tools_channel.go`. Keep `notify_host` as alias for one release.
8. **Update `fase notify` CLI to `fase report`** — update `cli/check.go`. Keep `notify` as alias.
9. **Update worker briefing** — simplify rules from 12 to 3: do work, report state, exit.

### Phase 3: API Surface Consolidation (Medium impact, medium risk)

10. **Move dispatch logic from `serve.go` into `Service.Dispatch()`** — the ~200 lines of inline dispatch logic in the HTTP handler belong in the service layer.
11. **Add missing HTTP endpoints** for notes, edges — or explicitly document they're CLI-only.
12. **Add missing MCP tools** for block, archive, retry, edges — or explicitly document they're CLI-only.
13. **Add integration test** that asserts all Service methods have MCP+CLI+HTTP handlers.

### Phase 4: Supervisor Simplification (Medium impact, higher risk)

14. **Remove event debouncing** — process events immediately. The 2s window adds complexity for marginal benefit.
15. **Add worker heartbeat** — workers ping every 30s. If no heartbeat for 2min, mark job failed. Fixes "workers don't report back."
16. **Add stalled job detection** — periodic sweep for jobs in `Queued`/`Running` state beyond lease timeout.
17. **Make context rotation configurable** — move the hardcoded "10 turns" to config.

### Phase 5: Briefing Simplification (Lower impact, low risk)

18. **Reduce worker rules to 3** — (1) do the work, (2) `fase report` with state, (3) exit. All other guidance moves to project conventions discoverable via `project hydrate`.
19. **Remove static `docs/checker-briefing.md`** — checker briefing is already auto-generated from `CompileWorkerBriefing()` for kind="attest". The static doc is stale.
20. **Unify checker and worker briefing generation** — single `CompileBriefing(kind)` function, no special cases.

---

## Appendix: Risk Assessment

| Refactoring | Risk | Mitigation |
|-------------|------|------------|
| State rename | Low | Mechanical find-replace, tests cover transitions |
| Report unification | Medium | Keep old tool names as aliases for one release |
| Dispatch extraction | Medium | Extract without changing behavior, test thoroughly |
| Supervisor changes | Higher | Feature-flag new behavior, A/B test |
| Briefing simplification | Low | Workers already ignore most rules |
