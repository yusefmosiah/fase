# Design: Supervisor Operational API

**Work ID**: `work_01KME1FCF4C6GRMEWS7PB35YN6`
**Date**: 2026-03-23
**Status**: Draft — awaiting attestation

## Problem

The supervisor (ADR-0041) has 10 MCP tools, all work-queue focused:
`project_hydrate`, `work_list`, `work_show`, `work_notes`, `work_update`,
`work_create`, `work_note_add`, `work_attest`, `work_claim`, `ready_work`.

It has **zero operational awareness**. Concrete gaps from overnight runs:

| Gap | Incident | Cost |
|-----|----------|------|
| Worktree failures invisible | `serve.go:1028` prints to stderr, supervisor never sees it | Workers fall back to main, causing concurrent-on-main corruption |
| No log reading | Supervisor can't inspect worker output to diagnose stalls | Stalled jobs sit for 30min until housekeeping detects |
| No health check | Supervisor can't see serve uptime, active processes, DB state | Silent supervisor death went unnoticed for hours |
| No error stream | Dispatch errors return HTTP 500, supervisor never retries | Failed dispatches leave work items claimed but un-run |
| No worktree status | Supervisor doesn't know which worktrees exist or are stale | Manual cleanup needed 3 times in overnight run |
| No cost awareness | Supervisor burned $100 on noise loop with no budget check | Rate-limit/backoff was retrofitted mid-run |

## Decision: MCP Tools (not CLI skills)

**MCP tools are the right pattern.** Rationale:

1. **The supervisor is an LLM session.** It calls tools, not CLI commands. Skills are harness-invoked (e.g., `/commit` triggers a prompt expansion in Claude Code). The supervisor doesn't use Claude Code's harness — it's a raw adapter session.

2. **Tools are callable on-demand.** The supervisor decides when to check health, read logs, or repair — the same way it decides when to call `ready_work`. This fits the agentic model: the LLM observes events, decides what operational checks to run, and acts.

3. **Skills are user-initiated.** They're triggered by `/slash-commands` from a human. The supervisor has no human.

4. **Consistency.** All 10 existing supervisor capabilities are MCP tools. Adding operational capabilities as tools keeps a single interface.

5. **The HTTP API already exists for most of this.** The serve process exposes `/api/supervisor/status`, `/api/git/status`, `/api/diff`, `/api/bash-log`, `/api/runs`. We just need MCP tool wrappers that call the service layer.

## Proposed Tools

### Category 1: Health & Status

#### `serve_health`

Returns operational health of the cogent serve process.

```go
type serveHealthInput struct{}

type ServeHealthResult struct {
    Uptime          string            `json:"uptime"`           // "2h34m"
    ActiveJobs      int               `json:"active_jobs"`      // running job count
    ActiveWorktrees []WorktreeStatus  `json:"active_worktrees"` // list with work_id, path, branch, age
    StaleBranches   []string          `json:"stale_branches"`   // cogent/work/* branches with no worktree
    EventBusStats   EventBusStats     `json:"event_bus_stats"`  // published, drops, subscriber count
    RecentErrors    []RecentError     `json:"recent_errors"`    // last 10 errors from error ring buffer
    DBSize          int64             `json:"db_size_bytes"`    // .cogent/cogent.db file size
    WorkerProcesses []WorkerProcess   `json:"worker_processes"` // PID, job_id, work_id, alive, age
}

type WorktreeStatus struct {
    WorkID string `json:"work_id"`
    Path   string `json:"path"`
    Branch string `json:"branch"`
    Age    string `json:"age"`
}

type WorkerProcess struct {
    PID    int    `json:"pid"`
    JobID  string `json:"job_id"`
    WorkID string `json:"work_id"`
    Alive  bool   `json:"alive"`
    Age    string `json:"age"`
}

type RecentError struct {
    Time    string `json:"time"`
    Source  string `json:"source"`  // "dispatch", "worktree", "adapter", "housekeeping"
    Message string `json:"message"`
}
```

**Implementation**: New `Service.Health()` method aggregating:
- `time.Since(startTime)` for uptime (store start time on serve init)
- `ListJobs(state=running)` for active jobs
- `git worktree list --porcelain` for worktrees
- `git branch --list 'cogent/work/*'` cross-referenced with worktrees for stale branches
- `EventBus.Stats()` (already exists)
- New error ring buffer (`internal/service/errors.go`, capacity 50)
- `os.Stat(.cogent/cogent.db)` for DB size
- `GetJobRuntime` + `isProcessAlive` for worker processes

#### `worker_logs`

Read recent output from a worker job.

```go
type workerLogsInput struct {
    JobID    string `json:"job_id" jsonschema:"required,job ID to read logs for"`
    TailN    int    `json:"tail_n,omitempty" jsonschema:"last N lines (default 50, max 200)"`
}
```

**Returns**: Last N lines from `.cogent/raw/stdout/<job_id>/*.jsonl`, parsed to extract text content (tool calls, assistant messages, errors). Truncated to 10KB to prevent context overflow.

**Implementation**: New `Service.WorkerLogs(jobID, tailN)` that reads the raw stdout JSONLs, extracts `content` fields, and returns the tail.

#### `dispatch_errors`

Read the dispatch error log for recent failures.

```go
type dispatchErrorsInput struct {
    Limit int `json:"limit,omitempty" jsonschema:"max errors to return (default 10)"`
}
```

**Returns**: Recent dispatch failures from the error ring buffer, filtered to source="dispatch". Includes: work_id, adapter, error message, timestamp.

**Implementation**: Reads from the same error ring buffer as `serve_health`.

### Category 2: Git & Worktree Operations

#### `worktree_status`

List all Cogent worktrees with their state.

```go
type worktreeStatusInput struct{}

type WorktreeStatusResult struct {
    Worktrees []WorktreeDetail `json:"worktrees"`
}

type WorktreeDetail struct {
    WorkID     string `json:"work_id"`
    Path       string `json:"path"`
    Branch     string `json:"branch"`
    Exists     bool   `json:"exists"`      // directory exists
    HasCommits bool   `json:"has_commits"` // branch has commits ahead of main
    CommitCount int   `json:"commit_count"`
    Age        string `json:"age"`
    WorkState  string `json:"work_state"`  // execution state of associated work item
}
```

**Implementation**: `git worktree list --porcelain` + `git rev-list --count main..<branch>` per worktree + work item state lookup.

#### `worktree_cleanup`

Remove a stale worktree and its branch.

```go
type worktreeCleanupInput struct {
    WorkID string `json:"work_id" jsonschema:"required,work ID whose worktree to remove"`
    Force  bool   `json:"force,omitempty" jsonschema:"force removal even if uncommitted changes"`
}
```

**Returns**: Success/failure message.

**Implementation**: Calls existing `cleanupWorktree(repoRoot, workID)` from `serve.go`, but now accessible to the supervisor LLM.

#### `worktree_merge`

Merge a worktree branch back to main.

```go
type worktreeMergeInput struct {
    WorkID string `json:"work_id" jsonschema:"required,work ID whose worktree to merge"`
}
```

**Returns**: Merge result (success, conflict details).

**Implementation**: Calls existing `mergeWorktree(repoRoot, workID)` from `serve.go`.

#### `git_status`

Get git status for main repo or a specific worktree.

```go
type gitStatusInput struct {
    WorkID string `json:"work_id,omitempty" jsonschema:"work ID to check worktree status (omit for main repo)"`
}
```

**Returns**: `git status --short`, `git diff --stat`, untracked files. Same as existing `/api/git/status` but worktree-aware.

### Category 3: Error Propagation & Self-Repair

#### `job_kill`

Kill a running worker process.

```go
type jobKillInput struct {
    JobID  string `json:"job_id" jsonschema:"required,job to kill"`
    Reason string `json:"reason" jsonschema:"required,why the job is being killed"`
}
```

**Implementation**: `GetJobRuntime(jobID)` → `syscall.Kill(-pid, SIGTERM)` (process group kill). Updates job state to failed. Records reason in error ring buffer.

#### `dispatch_work`

Dispatch a work item with operational error reporting (replaces the current pattern of calling `cogent dispatch` via bash).

```go
type dispatchWorkInput struct {
    WorkID  string `json:"work_id" jsonschema:"required,work item to dispatch"`
    Adapter string `json:"adapter,omitempty" jsonschema:"override adapter"`
    Model   string `json:"model,omitempty" jsonschema:"override model"`
}

type DispatchResult struct {
    WorkID       string `json:"work_id"`
    JobID        string `json:"job_id"`
    Adapter      string `json:"adapter"`
    Model        string `json:"model"`
    WorktreePath string `json:"worktree_path"`    // "" if worktree creation failed
    WorktreeError string `json:"worktree_error"`   // non-empty if worktree failed (NEW)
    Error        string `json:"error,omitempty"`   // dispatch-level error
}
```

**Key difference from current**: The `worktree_error` field makes worktree failures visible. Currently `serve.go:1028` prints to stderr and the dispatch response includes `"worktree": ""` with no indication of failure. The supervisor sees an empty worktree path but can't distinguish "worktree not requested" from "worktree creation failed."

**Implementation**: Wraps the existing `/api/dispatch` handler logic but returns structured errors instead of swallowing them.

### Category 4: Cost & Budget

#### `cost_report`

Get cost tracking for the current serve session.

```go
type costReportInput struct{}

type CostReport struct {
    SessionTotal    float64          `json:"session_total_usd"`
    SupervisorCost  float64          `json:"supervisor_cost_usd"`
    WorkerCost      float64          `json:"worker_cost_usd"`
    BudgetLimit     float64          `json:"budget_limit_usd"`      // 0 = unlimited
    BudgetRemaining float64          `json:"budget_remaining_usd"`
    TopWorkItems    []WorkItemCost   `json:"top_work_items"`        // top 5 by cost
}

type WorkItemCost struct {
    WorkID string  `json:"work_id"`
    Title  string  `json:"title"`
    Cost   float64 `json:"cost_usd"`
    Jobs   int     `json:"job_count"`
}
```

**Implementation**: Aggregate `estimated_cost.total_cost_usd` from job summaries grouped by work_id.

## Error Propagation Design

### Current state

```
Worktree fails → stderr → invisible
Dispatch fails → HTTP 500 → supervisor retries blind
Worker stalls → 30min housekeeping → event → supervisor wakes
Worker dies   → housekeeping orphan check → event → supervisor wakes
Rate limit    → fast completion → classifyOutcome detects → backoff
```

### Proposed state

```
Worktree fails → error ring buffer + dispatch_work returns worktree_error
Dispatch fails → error ring buffer + dispatch_work returns error
Worker stalls → housekeeping event (unchanged, this works)
Worker dies   → housekeeping event (unchanged, this works)
Rate limit    → classifyOutcome (unchanged, this works)
Infrastructure → serve_health shows recent errors
```

### Error Ring Buffer

New `internal/service/errors.go`:

```go
type ErrorRing struct {
    mu      sync.Mutex
    entries []ErrorEntry
    cap     int
    pos     int
}

type ErrorEntry struct {
    Time    time.Time
    Source  string // "dispatch", "worktree", "adapter", "housekeeping", "serve"
    WorkID  string // optional
    JobID   string // optional
    Message string
}

func (r *ErrorRing) Add(source, message string, workID, jobID string)
func (r *ErrorRing) Recent(limit int) []ErrorEntry
func (r *ErrorRing) RecentBySource(source string, limit int) []ErrorEntry
```

The error ring lives on `*Service` and is written to by dispatch, worktree creation, adapter launch, and housekeeping. The `serve_health` and `dispatch_errors` tools read from it.

## Comparison: MCP Tools vs Other Patterns

| Pattern | Pros | Cons | Verdict |
|---------|------|------|---------|
| **MCP tools** (proposed) | Same interface as work-queue tools; LLM decides when to check; composable | Adds ~8 tools to MCP surface | **Use this** |
| CLI skills | User-invocable; harness integration | Supervisor has no harness; can't call `/health` | Wrong fit |
| Event stream subscription | Push model; no polling | Supervisor already subscribes to EventBus; adding a second stream adds complexity | EventBus is sufficient for reactive signals |
| Polling HTTP endpoints | Already exist (/api/supervisor/status etc.) | Supervisor would need bash tool to curl; fragile; prompt injection risk | Don't do this |
| Dedicated health goroutine | Go code monitors and auto-repairs | Contradicts ADR-0041 (LLM decides, not Go code) | Only for fatal infrastructure |

## Choiros-rs Hypervisor Mapping

The choiros-rs hypervisor monitors sandbox health and intervenes:
- **Sandbox heartbeat** → Cogent equivalent: `isProcessAlive()` in housekeeping (already exists)
- **Resource limits** → Cogent equivalent: cost budget in `cost_report` tool (proposed)
- **Sandbox restart** → Cogent equivalent: `job_kill` + `dispatch_work` (kill stalled worker, redispatch)
- **Health dashboard** → Cogent equivalent: `serve_health` tool (proposed)
- **Intervention protocol** → Cogent equivalent: supervisor LLM reads health, decides action

Key difference: choiros-rs hypervisor is deterministic Go/Rust code. Cogent supervisor is an LLM. The operational tools give the LLM the same information the hypervisor would have, but the LLM decides the intervention. This is consistent with ADR-0041's philosophy.

One area where choiros-rs is better: **fatal infrastructure recovery** (e.g., serve process crash, DB corruption). An LLM can't fix its own process dying. For that, Cogent should have a **watchdog** — a separate process (systemd, launchd, or a Go goroutine in the parent) that restarts serve if it crashes. This is outside the MCP tool scope but worth noting.

## How CI/CD Systems Auto-Remediate

| System | Pattern | Cogent Analog |
|--------|---------|-------------|
| GitHub Actions | Retry failed steps with `continue-on-error` | `dispatch_work` with retry count |
| Kubernetes | Pod restart on liveness probe failure | `job_kill` + `dispatch_work` on stall detection |
| Argo CD | Sync retry with backoff | Supervisor backoff (already exists in `classifyOutcome`) |
| Buildkite | Automatic retry with agent selection | Supervisor picks different adapter on retry |
| Datadog Watchdog | Anomaly detection → alert | `serve_health` recent_errors → supervisor decides |

The common pattern: **detect** (health check) → **decide** (retry? different agent? escalate?) → **act** (redispatch/kill/alert). Cogent already has decent detect (housekeeping events) and act (dispatch), but the supervisor lacks the **observe** step — it can't inspect what went wrong. The proposed tools fill this gap.

## Implementation Priority

| Tool | Priority | Effort | Rationale |
|------|----------|--------|-----------|
| `serve_health` | P0 | Medium | Supervisor needs this every turn for situational awareness |
| `dispatch_work` (with errors) | P0 | Low | Replace bash-based dispatch; make worktree errors visible |
| `worker_logs` | P0 | Medium | Critical for diagnosing stalls and failures |
| `worktree_status` | P1 | Low | Needed before worktree cleanup/merge |
| `worktree_cleanup` | P1 | Low | Already implemented in Go, just needs MCP wrapper |
| `job_kill` | P1 | Low | Supervisor needs to act on stall events |
| `git_status` | P2 | Low | Nice-to-have; already exposed via HTTP |
| `worktree_merge` | P2 | Low | Post-attestation workflow |
| `cost_report` | P2 | Medium | Important for budget control, not urgent |
| `dispatch_errors` | P2 | Low | Subset of serve_health; may not need standalone tool |

## Supervisor Prompt Integration

The supervisor's initial hydration should include the operational tools in its contract:

```markdown
## Operational Tools (infrastructure awareness)

- `serve_health` — check system health (call at start of each turn, and when diagnosing issues)
- `dispatch_work {work_id, adapter?, model?}` — dispatch with error reporting
- `worker_logs {job_id, tail_n?}` — read worker output (use when stall/failure detected)
- `worktree_status` — list all worktrees
- `worktree_cleanup {work_id, force?}` — remove stale worktree
- `worktree_merge {work_id}` — merge worktree to main
- `job_kill {job_id, reason}` — kill stalled/broken worker
- `git_status {work_id?}` — check git state
- `cost_report` — check spend and budget

## Operational Protocol

1. **Each turn**: call `serve_health` first. Check for recent errors, dead workers, stale worktrees.
2. **On stall event**: call `worker_logs` to diagnose. If unrecoverable, `job_kill` + redispatch.
3. **On dispatch**: use `dispatch_work` (not bash). Check `worktree_error` in response.
4. **On worktree error**: `worktree_cleanup` the stale one, then redispatch.
5. **After attestation passes**: `worktree_merge` to bring changes to main.
6. **Budget check**: call `cost_report` every 5 turns. If approaching limit, pause non-critical work.
```

## Open Questions

1. **Should `serve_health` be called automatically at turn start (injected by Go code) or on-demand (supervisor decides)?** Recommendation: on-demand. The supervisor should learn to check health when things look wrong, not waste tokens on health checks when everything is fine.

2. **Should `dispatch_work` replace the current bash-based `cogent dispatch`?** Recommendation: yes. The MCP tool gives structured error reporting. The bash approach loses error context.

3. **Should there be a `serve_restart` tool for the supervisor to restart the serve process itself?** Recommendation: no. Self-restart is dangerous — if the supervisor's judgment is impaired (e.g., by a context overflow), letting it restart serve could cause loops. Fatal recovery should be external (watchdog).

4. **Error ring buffer capacity?** 50 entries seems right. Errors older than the most recent 50 are unlikely to be actionable.

5. **Should the supervisor call `serve_health` proactively or only in response to problems?** The postmortem suggests proactive checking wastes money when things are fine. But the overnight report shows that problems go unnoticed without it. Compromise: check health at session start and after every 3 productive turns (not every turn).
