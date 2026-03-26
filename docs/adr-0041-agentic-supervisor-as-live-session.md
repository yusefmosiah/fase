# ADR-0041: Agentic Supervisor — Just Another Adapter Session

**Status:** Accepted (revised 2026-03-21)
**Date:** 2026-03-21

## Context

The supervisor should not be special Go infrastructure. It should be a regular Cogent session running on any adapter (claude, codex, opencode), using Cogent MCP tools to manage the work queue. The supervisor is just another agent — it happens to dispatch and review instead of writing code.

The deterministic supervisor (1500+ lines of Go dispatch logic) has already been deleted. This ADR specifies its replacement: a single adapter session with a supervisor prompt.

## Decision

### Supervisor = Regular Adapter Session

`cogent serve --auto` launches the supervisor as a regular `svc.Run` call on a configurable adapter. The supervisor is not special infrastructure — it's a work item dispatched to an adapter, same as any worker. The adapter process connects to Cogent's MCP server (already exposed at `/mcp` by serve) and uses Cogent tools to manage the queue.

There is no Go goroutine making dispatch decisions. The LLM decides everything.

### How It Works

1. `cogent serve --auto` calls `svc.Run` with:
   - **adapter**: configurable (default `claude`)
   - **model**: configurable (default `claude-sonnet-4-6`)
   - **prompt**: `project_hydrate --mode supervisor` output — role, queue state, dispatch protocol, MCP tool contract
2. The adapter process (e.g., `claude --print`) starts with Cogent MCP tools available via `.mcp.json` or the `/mcp` endpoint.
3. The supervisor LLM reads the queue state from its prompt and uses MCP tools:
   - `ready_work` → see what's dispatchable
   - `work_show` → inspect a specific item
   - `work_claim` → claim it
   - Dispatch workers via `cogent dispatch` or by calling the dispatch API
   - `work_attest` → review and attest completed work
4. When the supervisor turn completes, serve checks EventBus for state changes since the turn started. If anything changed, it re-runs the supervisor with fresh `project_hydrate` output. If nothing is ready, it waits for EventBus events before re-running.

### Supervisor Lifecycle

```
serve --auto starts
  │
  ├─ generate supervisor prompt (project_hydrate --mode supervisor)
  ├─ svc.Run(adapter, model, prompt)  ← first turn, creates session
  │    └─ LLM uses MCP tools: ready_work, dispatch, attest, etc.
  ├─ turn completes
  ├─ wait for EventBus events (work completed, created, failed, etc.)
  ├─ svc.Send(session, eventSummary)  ← continue same session
  │    └─ LLM has full context from prior turns
  └─ loop: wait for events → send → repeat
```

The supervisor is a **long-running session**. The first turn (`svc.Run`) creates the session with the full supervisor hydration prompt. Subsequent turns use `svc.Send` on the same session, passing event summaries as the prompt. The LLM accumulates context across turns — it remembers what it dispatched, what's pending, what failed. This is the same session continuation protocol used by workers via `cogent send`.

### Supervisor Prompt (project_hydrate --mode supervisor)

The initial prompt (first turn only) includes:
1. **Role**: "You are the Cogent supervisor. Dispatch ready work, monitor workers, attest completed work. Never write code directly."
2. **Queue state**: Ready items (with priority, preferred adapters/models), active work, pending attestations, recent completions.
3. **Dispatch protocol**: Step-by-step instructions for claiming, hydrating, dispatching, and attesting.
4. **Concurrency rules**: One code-writer at a time. Plan/research/attest can run concurrently.
5. **MCP tools available**: The contract — which tools exist and how to use them.
6. **Conventions**: Project-specific rules from convention notes.

Target: ~4K tokens. The supervisor calls `work_show` for details on specific items.

Subsequent turns receive an event summary: "Work item X completed. 2 items now ready. Check queue and act." The LLM already has the role, protocol, and conventions from its session history.

### Go Infrastructure

The serve-side Go code is minimal (~40 lines):

```go
func runSupervisorLoop(ctx, svc, adapter, model) {
    // First turn: cold-start with full hydration
    prompt := projectHydrate(mode: "supervisor")
    result := svc.Run(adapter, model, prompt)
    sessionID := result.Session.SessionID

    for {
        events := waitForEvents(ctx, svc.Events)
        summary := formatEventSummary(events)
        svc.Send(sessionID, summary)  // continue same session
    }
}
```

No dispatch logic. No adapter selection. No rotation pools. No health tracking. The LLM handles all of that. The Go code just manages the session lifecycle and relays events.

### Configuration

Flags: `cogent serve --auto --supervisor-adapter claude --supervisor-model claude-sonnet-4-6`

### Pause / Resume

`cogent supervisor pause` sets a flag. The loop checks the flag before re-running. While paused, the current turn (if any) completes but no new turns start.

## Consequences

- **No special infrastructure**: Supervisor is just another adapter session.
- **Adapter-agnostic**: Works with any adapter that supports `svc.Run` (all of them).
- **Stateless**: Each turn gets fresh hydration. No session state to corrupt or debug.
- **Observable**: Supervisor's MCP tool calls are logged as regular job events. Visible in web UI.
- **Flexible**: Changing dispatch strategy = editing the supervisor prompt.
- **Cost**: ~$0.01-0.05 per dispatch cycle with sonnet.
- **Latency**: 2-5s per dispatch decision vs <1ms for Go loop. Acceptable — dispatch is not latency-critical.
