# Mission: Cogent Rename + Simplify Verification Architecture

## Background

Cogent ("Fully Automated Software Engineering") is being renamed to **Cogent** — a cogent abstraction level for managing the complexity of full-stack agentic engineering. The name change reflects reality: we're not at fully automated SE yet, but we have a powerful tool for orchestrating multi-agent work.

## Repository

- **Repo:** github.com/yusefmosiah/cogent
- **Language:** Go (module: github.com/yusefmosiah/cogent)
- **Build:** `go build ./...` / `make install`
- **Tests:** `go test ./internal/store/... ./internal/notify/... ./internal/core/...` (service integration tests require running adapters)
- **Binary:** `cogent` CLI installed to `~/.local/bin/cogent`

## Current Architecture (what exists)

### Core Components
- **Work graph** (SQLite): tracks work items, dependency edges, check records, attestations
- **Native adapter** (`internal/adapters/native/`): in-process Go adapter supporting multiple LLM providers (GLM, GPT, Claude via Bedrock, Gemini)
- **Coagent system** (`internal/adapters/native/tools_coagent.go`): agents can spawn sub-agents on different models
- **Service** (`internal/service/service.go`): ~9500-line core with work lifecycle, check records, notifications
- **Serve** (`internal/cli/serve.go`): HTTP server, WebSocket hub, housekeeping, supervisor loop
- **Mind-graph UI** (`mind-graph/`): Poincaré disk hyperbolic visualization of work graph

### Key Specs
- `docs/cogent-v0-local-control-plane.md` — product direction
- `docs/cogent-work-runtime.md` — work runtime design
- `docs/design-hourly-digest-and-checker-mining.md` — email digest design (approved, not yet fully implemented)

### Non-native Adapters (deprecated, do not maintain)
Claude, Codex, Factory, Pi, Gemini, OpenCode adapters exist as subprocess wrappers. They are being deprecated in favor of the native adapter. Do not invest in them.

## Mission Objectives

### 1. Rename cogent → cogent (throughout codebase)

- Go module: `github.com/yusefmosiah/cogent` → `github.com/yusefmosiah/cogent`
- Binary: `cogent` → `cogent`
- CLI commands: `cogent work ...` → `cogent work ...`
- State directory: `.cogent/` → `.cogent/`
- All imports, references, docs, README, SKILL.md files
- Git commit prefixes: `cogent(...)` → `cogent(...)`
- Worker/checker briefings reference `cogent` CLI commands — update all
- Migration: on first run, if `.cogent/` exists and `.cogent/` doesn't, rename it

### 2. Peer coagent message channels (replace synchronous API)

**Problem:** Current coagent API (`send_turn`) blocks the caller until the coagent finishes. Workers can't have back-and-forth conversations with checker coagents.

**Solution:** Replace with async peer messaging:

**New file:** `internal/adapters/native/channel.go`
```go
type ChannelManager struct { ... }  // keyed by work_id
type AgentChannel struct { ... }    // pub/sub message queue
type ChannelMessage struct {
    From      string    // session ID
    Role      string    // "worker", "checker", "supervisor"
    Content   string
    Timestamp time.Time
}
```

**Rewrite:** `internal/adapters/native/tools_coagent.go`
| New Tool | Replaces | Behavior |
|----------|----------|----------|
| `spawn_agent` | `spawn_session` | Start peer on specified model+role. Non-blocking. |
| `post_message` | `send_turn` | Post to work channel. Returns immediately. |
| `read_messages` | (new) | Read since last cursor. Non-blocking. |
| `wait_for_message` | (new) | Block until message arrives (with timeout). |
| `close_agent` | `close_session` | Shut down peer agent. |

Remove: `steer_session`, `list_sessions`

**Worker-checker flow:**
1. Worker implements, commits
2. Worker posts "please verify" to channel, spawns checker agent
3. Checker runs build/tests, creates check record, posts result to channel
4. Worker reads result. If fail: fixes, posts "re-check please". If pass: signals done.
5. Loop up to 3 times. Supervisor gets notified on each failure via check record events. After 3 failures supervisor intervenes.

### 3. Remove attestation children machinery

**File:** `internal/service/service.go`

Remove from `finishJob`: the `spawnAttestationChildren` call and the ~15 helper functions around it (attestationChildRuntime, attestationChildTitle, attestationChildObjective, attestationWorkerFindings, refreshParentAfterAttestationChild, refreshAttestationParentState, childMatchesCurrentAttempt, attestationNonce, currentAttemptEpoch, defaultRequiredAttestations, attestationPreferredAdapters, entryMatchesWorkRole, alternateAdapter).

Remove auto-dispatch of checker on `checking` state in `UpdateWork`.

Simplify `guardDoneTransition`: only require a passing check record.

Keep: `AttestWork` (manual CLI), `CreateCheckRecord` (unchanged).

### 4. Simplify state machine

**File:** `internal/core/types.go`

Map `checking` → `in_progress` in `Canonical()`. Effective states: `ready`, `in_progress`, `done`, `failed` (plus `blocked`, `cancelled`, `archived`).

**File:** `internal/service/service.go`

Worker briefing: completion signal changes from `--execution-state checking` to `--execution-state done`.

### 5. Skill-based worker/checker contracts

**New:** `skills/cogent/worker/SKILL.md` — worker protocol with self-check via peer channels
**New:** `skills/cogent/checker/SKILL.md` — checker verification protocol

**File:** `internal/service/service.go`
- `CompileWorkerBriefing`: load contract from skill file (hardcoded fallback)
- `buildCheckerBriefing`: load from skill file (hardcoded fallback)
- Add `loadSkillFile(name string) string`

### 6. Re-disable MCP tools

**File:** `internal/mcpserver/server.go`

A previous Factory mission re-enabled MCP tools. Disable `registerTools` and `registerChannelTools` in `New()`. MCP server must register ZERO tools. Keep channel notification relay.

### 7. Email digest (hourly batching)

**New:** `internal/notify/digest.go` — DigestCollector with Collect/Flush
**File:** `internal/service/service.go` — replace per-event email calls with digestCollector.Collect()
**File:** `internal/cli/serve.go` — wire Flush into housekeeping (hourly)

Escalations (3+ failures) bypass digest and send immediately.

The design doc at `docs/design-hourly-digest-and-checker-mining.md` has the approved architecture.

### 8. Cleanup

- Remove `dispatchChecker` (supervisor no longer auto-dispatches)
- Remove `buildCheckerBriefing` hardcoded template (replaced by skill)
- Remove `WorkCheck` alias (use `CreateCheckRecord`)

## Execution Order

1. Rename cogent → cogent (foundational, touches everything)
2. Peer coagent channels (enables worker self-check)
3. Remove attestation children (simplify service.go)
4. Simplify state machine (depends on #3)
5. Skill-based contracts (depends on #2 for channel tool references)
6. Re-disable MCP tools (independent)
7. Email digest (independent)
8. Cleanup (last, depends on #3-5)

## Runtime Configuration

Model preferences are in `.cogent/supervisor-brief.md` (will become `.cogent/supervisor-brief.md`):
```
supervisor_adapter: native
supervisor_model: zai/glm-5-turbo
checker_pool: codex/gpt-5.4-mini, claude/claude-haiku-4-5, codex/gpt-5.4
```

## Known Issues

- SQLite DB corruption was fixed with auto-recovery (store.go `openAndCheck`/`recoverAndOpen`)
- Supervisor idle churn fixed with seen-set event deduplication + ActorSupervisor tagging
- Stale job directories in `.cogent/raw/stdout/` cause false stall warnings for completed work
- `service.go` is ~9500 lines — consider splitting into multiple files during the rename

## Constraints

- `go build ./...` must pass at every commit
- `go test ./internal/store/... ./internal/notify/... ./internal/core/...` must pass
- `cogent check create` CLI must work standalone (no supervisor required)
- Don't break the mind-graph UI
- Don't invest in non-native adapter code
