# Cogent Overnight Session Report — 2026-03-20

**Duration**: ~13 hours (06:12 UTC – 19:00 UTC)
**Mode**: Autonomous host agent with hourly cron checks, supervisor auto-dispatch
**Adapters used**: codex (gpt-5.4), claude (sonnet-4.6), opencode (glm-5-turbo)

## Executive Summary

Built the entire live agent protocol stack from zero to working: 5 live adapters, event-driven agentic supervisor, renamed the project to Cogent, ran eval with cheap models, and iterated twice on findings. 111 files changed, +9,406 / -1,719 lines across 12 commits.

## Commits (chronological)

| Commit | Description | Lines |
|--------|-------------|-------|
| `c6dbc3d` | Fix ready work ordering: priority DESC | +1/-1 |
| `58710f0` | Live agent protocol spec | +501 |
| `d41a636` | LiveAgentAdapter interface + Codex adapter | +1,038 |
| `c62f7fd` | Pi adapter (JSONL stdio + CLI tool bridge) | +1,146 |
| `80e44db` | OpenCode adapter (HTTP REST + SSE) | +1,132 |
| `9fa5f5d` | Native Go adapter (conductor/worker + channels) | +1,183 |
| `caad063` | Gitignore .cagent/keys | +29/-1 |
| `64b9bbc` | Rename cagent → Cogent | +1,235/-890 |
| `47b6ab6` | Fix dispatch CWD, concurrency guard, PID liveness | +384/-10 |
| `6a3c175` | ADR-0037: event-driven supervisor architecture | +1,106/-556 |
| `40c7e5e` | Cogent eval test projects (Go/Python/Node) | +489 |
| `8389e4f` | Event-driven supervisor + recovery + routing engines | +1,687/-285 |

## What Was Built

### 1. Live Agent Protocol (5 adapters)

Defined the `LiveAgentAdapter` / `LiveSession` Go interfaces in `internal/adapterapi/live.go` and implemented 5 adapters:

| Adapter | Transport | Tool Bridge | Tests | Key Learning |
|---------|-----------|-------------|-------|--------------|
| **Claude Code** | MCP channels | MCP tools | existing | MCP + channels *is* the control transport |
| **Codex** | JSON-RPC 2.0 over stdio | MCP via .mcp.json | 5 integration | Auto-approve server requests needed; 4MB scanner buffer; sync.Once for race-safe channel close |
| **Pi** | JSONL over stdio | CLI commands | 5 integration + unit | Fire-and-forget JSONL (not request/response); steer vs follow_up gives genuine delivery control |
| **OpenCode** | HTTP REST + SSE | MCP (preferred) | fake SSE harness | noReply steering functional but a workaround; abort is best-effort; Go SDK covers control plane |
| **Native Go** | In-process | Direct service calls | 9 unit | Conductor/worker architecture; goroutine-per-agent; channel-based steering/events/results |

### 2. Event-Driven Agentic Supervisor

Replaced the 30s polling loop with an event-driven reactor:

- **`supervisor_event_driven.go`**: Subscribes to EventBus, reacts to work graph mutations, process exit events, and heartbeat timer
- **`supervisor_recovery.go`**: Git-aware premature exit detection, adapter crash handling, stall recovery with commit preservation
- **`supervisor_routing.go`**: Scoring-based adapter selection, kind affinity, health tracking, circuit breaker (3 failures/1h = 5min open)
- **ADR-0037**: Full architecture doc covering reactor design, 4 event sources, recovery engine, routing, phased migration

### 3. Cogent Rename

Full rename from `cagent` to `Cogent` (Fully Automated Software Engineering):
- Go module: `github.com/yusefmosiah/cogent`
- Binary: `cmd/cogent/main.go` with `cagent` symlink for backward compat
- 81 files: package imports, CLI commands, docs, MCP server, config paths
- State directory: `.cogent/` (with `.cagent/` legacy fallback)

### 4. Eval with Cheap Models

Three greenfield projects validated the full protocol stack end-to-end:

| Project | Adapter/Model | Result |
|---------|---------------|--------|
| `eval/fib-go` | codex/gpt-5.4-mini | Pass |
| `eval/fizzbuzz-python` | opencode/glm-5-turbo | Pass |
| `eval/md2html-node` | claude/haiku-4.5 | Pass |

All three cheap models produced working code with tests.

### 5. Iteration from Eval Findings

3 bugs found and fixed:
1. **CWD inheritance**: dispatch.go now resolves CWD to git repo root
2. **Concurrency guard**: `--force` flag prevents max-concurrent=1 violation when mixing manual+auto dispatch
3. **PID liveness**: serve.go housekeeping detects orphaned workers

## Work Graph Status

| State | Count |
|-------|-------|
| Done | 40 |
| Failed | 4 |
| Ready | 2 |
| Claimed | 2 |
| Awaiting attestation | 2 |
| **Total** | **50** |

**20 work items completed during this session** (from 33 done → 40 done, plus items created and completed within the session).

## Architecture Decisions

1. **Interface emerges from implementation**: LiveAgentAdapter was defined alongside the Codex adapter, not before it. The interface grew as each adapter revealed needs.

2. **Build order validated**: Claude (already working) → Codex (richest protocol, most to learn) → Pi (best steering, tests CLI bridge) → OpenCode (Go SDK, HTTP transport) → native Go (reference impl). Each adapter's learnings compounded.

3. **MCP vs CLI tool bridge**: MCP is better where supported (Codex, OpenCode). CLI is sufficient but inferior for structured ops (Pi). Confirmed through implementation, not speculation.

4. **EventBus as nervous system**: All work graph mutations publish events. All consumers subscribe. Zero polling for state changes. The event-driven supervisor is the natural consumer.

5. **DB migration issue**: The Cogent rename created `.cogent/` with an empty DB, splitting state from `.cagent/cagent.db`. Fixed with SQLite `.backup` command. This is exactly the kind of recovery the agentic supervisor should handle automatically.

## Operational Observations

- **Worker reliability**: codex and claude workers are reliable for implementation tasks. Workers repeatedly crash on lower-priority housekeeping items (UI/JS tasks, planning tasks) — likely model capability limitations.
- **Stale claims**: The most common failure mode is workers dying without releasing their claim. Lease reconciliation catches this but adds latency. The event-driven supervisor's process watcher addresses this.
- **Supervisor stability**: The supervisor itself is stable. The issue is always worker processes exiting unexpectedly.
- **Auto-bootstrap confusion**: After the rename, the supervisor auto-bootstrapped because it detected a "new" repo (`.cogent/` existed but was empty). This created unnecessary work items. Mitigated by DB migration.

## What's Left

Low-priority backlog items the supervisor is working through:
- Go native coding agent adapter (z.ai API) — planning
- ADR: Agentic supervisor app agent layer — planning (partially superseded by ADR-0037)
- 2 eval attestation items
- WebSocket live updates (UI)
- Project hydrate convention emission

None of these block the core deliverables.

## Cost Estimate

Rough estimate based on model usage:
- **codex (gpt-5.4)**: Primary implementation adapter, ~15 dispatches
- **claude (sonnet-4.6)**: Secondary implementation, ~8 dispatches
- **opencode (glm-5-turbo)**: Verification and cheap eval, ~5 dispatches
- **Host agent (opus-4.6)**: 14 hourly checks, work graph management, commits, attestations

The cheap model eval confirmed that glm-5-turbo, gpt-5.4-mini, and claude haiku all produce working code for straightforward tasks.
