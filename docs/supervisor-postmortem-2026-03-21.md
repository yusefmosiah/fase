# Supervisor Postmortem: 2026-03-21 Overnight Session

## Summary

First overnight run of the agentic supervisor (ADR-0041). The supervisor dispatched all queued work (ADR-0039 phases 1-5, two eval projects, TD1 refactor) successfully in ~3 hours, then spent ~8 hours in two degenerate loops burning $100+ in wasted API calls and exhausting rate limits.

**Productive**: ~$52, all work completed and attested.
**Wasted**: ~$100, supervisor responding "Noise — ignoring" to housekeeping events 200+ times, then 700+ rate-limited retries.

## Timeline

| Time (UTC) | Phase | Turns | Cost | What happened |
|------------|-------|-------|------|---------------|
| 03:36–06:34 | Productive | ~25 | ~$12 | Dispatched 7 work items, ran attestations, all completed |
| 06:34–08:12 | Noise loop | ~200 | ~$100 | Housekeeping events woke supervisor every 30s, supervisor said "ignoring" each time |
| 08:12–14:27 | Rate-limited | ~700 | $0 | Claude returned "hit your limit", loop kept retrying |

## Bugs

### Bug 1: Housekeeping → EventBus feedback loop (critical, ~$100 cost)

**What**: The housekeeping goroutine runs every 30 seconds. When it detects stale/orphaned jobs, it calls `svc.UpdateWork()` which publishes `WorkEventUpdated` to the EventBus. The supervisor subscribes to EventBus and wakes on every `WorkEventUpdated`. The supervisor's own turn completion also generates events. Result: supervisor wakes every 30 seconds even when there's nothing to do.

**Where**: `runHousekeeping()` in `serve.go` → `svc.UpdateWork()` → `EventBus.publish()` → supervisor `waitForSignal()` wakes.

**The supervisor correctly identified these as noise** ("Same stale pair. Ignoring. Queue idle, awaiting host direction.") — but it still cost $0.50 per turn to reach that conclusion.

**Fix options**:
1. Housekeeping should not publish events to EventBus (use internal-only state updates)
2. Add a `source` field to WorkEvent so supervisor can filter by source (ignore "housekeeping")
3. The supervisor's `waitForSignal` should filter events from its own session's jobs

### Bug 2: No rate-limit detection or backoff (critical, burned quota)

**What**: When claude returns "You've hit your limit", the Go loop treats this as a successful turn completion. The turn generates a `job completed` event, which wakes the supervisor for the next turn, which hits the rate limit again. 700+ iterations over 6 hours.

**Where**: `waitForJob()` in `supervisor_agent.go` polls `svc.Status()` which sees `job.State.Terminal()` = true (completed). The loop doesn't inspect the job result for errors.

**Fix options**:
1. Detect rate-limit / error responses in the supervisor loop (check job result text)
2. Exponential backoff: if last N turns produced no tool calls or had errors, increase wait time
3. Auto-switch provider: on rate limit, restart supervisor session on a different adapter (codex, opencode, native)
4. Circuit breaker: after N consecutive empty/error turns, pause supervisor and notify host

### Bug 3: Workers don't update work item state (moderate, caused stalls)

**What**: Workers write code and exit without calling `cogent work update <id> --state done`. The supervisor waits for state-change events that never arrive. Supervisor stalls until host manually nudges it.

**Where**: Worker briefing contract didn't include explicit state update instructions. Fixed mid-session by adding REQUIRED commands to `CompileWorkerBriefing()` (commit `caabd99`). Workers dispatched before the fix still had the old briefing.

**Status**: Partially fixed. New workers get the updated briefing. Need to verify workers actually follow it.

### Bug 4: Self-event amplification (moderate)

**What**: When the supervisor calls MCP tools (work_update, work_claim, dispatch), those calls mutate DB state, which publishes events, which wake the supervisor for another turn. The supervisor acts, generates events, wakes itself.

**Where**: EventBus in `service.go` doesn't distinguish event sources. All mutations publish events indiscriminately.

**Fix options**:
1. Add `source` or `actor` field to WorkEvent
2. Supervisor filters events where actor = "supervisor" or actor = its own session ID
3. Only wake on events from external actors (workers, host, housekeeping-escalations)

### Bug 5: No idle/sleep mode (minor)

**What**: When the queue is empty, the supervisor should sleep until new work is created. Instead it keeps responding to housekeeping noise. The supervisor even says "Queue idle, awaiting host direction" — but has no mechanism to actually idle.

**Fix**: After N consecutive "nothing to do" turns, enter exponential backoff or sleep mode. Only wake on `WorkEventCreated` (new work) or host message.

## Architecture Issues

### The supervisor-as-adapter model works but needs guardrails

The core idea is sound: supervisor is just another adapter session. It dispatched all work correctly. But the Go loop (`supervisor_agent.go`) is too simple — it wakes on ANY event and sends a turn. It needs:

1. **Event classification**: not all events require supervisor attention
2. **Rate limiting**: the supervisor itself needs rate limiting independent of the LLM provider
3. **Provider failover**: when one provider is rate-limited, switch to another
4. **Backoff**: exponential backoff on empty/error turns
5. **Idle detection**: recognize "nothing to do" state and sleep

### Housekeeping should be decoupled from EventBus

Housekeeping is an internal maintenance loop. Its state changes should not wake the supervisor. Options:
- Housekeeping uses a separate `internalUpdate()` path that doesn't publish events
- Events get a `source` field and supervisor filters

### Cost control

Need a cost tracking mechanism:
- Track cumulative cost per supervisor session
- Alert/pause at configurable thresholds
- Daily budget caps

## What Worked

1. **Dispatch logic**: supervisor correctly identified priorities, picked adapters, dispatched all work
2. **Attestation**: supervisor ran attestation workers and reviewed results
3. **Session continuity**: 922 turns on a single claude session via svc.Send — the long-running session model works
4. **Worker output**: all code compiles, all tests pass, eval projects are functional
5. **Host messaging**: `cogent supervisor send` successfully injected instructions mid-session

## Metrics

- Total jobs created: 1137 (922 supervisor + ~215 worker/attestation)
- Total cost: ~$152
- Productive cost: ~$52 (34%)
- Wasted cost: ~$100 (66%)
- Work items completed: 8 (Phases 1-5, 2 evals, TD1)
- Net code: +7486 lines, -1594 lines (52 files)
