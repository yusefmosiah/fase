# Cogent Overnight Autonomous Run Report — 2026-03-22

## Summary

First sustained autonomous run of Cogent managing a real project (OSINT intelligence feed aggregator). The supervisor ran for ~15 hours with periodic host oversight via hourly cron checks. The system produced a working Go service with 19,486 lines across 64 files and 12 test packages — all passing.

## Timeline

| Time | Event |
|------|-------|
| 21:00 Mar 21 | OSINT project started. SPEC.md written. serve --auto launched. |
| 21:30 | Supervisor bootstrap: decomposed spec into 9 work items (created duplicates — known issue) |
| 22:00 | First workers dispatched. Scaffold, model, store created. |
| 23:00 | 4 test regressions from concurrent workers. Supervisor self-healed — dispatched fix, tests green. |
| 00:00 Mar 22 | Housekeeping race condition fixed. Stale workers killed. |
| 02:00 | Context overflow ($17MB tool output). Fixed with output caps + history trim. |
| 03:00 | Supervisor stall pattern identified — workers complete but events don't wake supervisor. |
| 05:00 | Test regressions fixed again by supervisor autonomously. Concurrent-on-main validated. |
| 07:00 | Duplicate cleanup. 10 duplicates archived manually. |
| 09:00 | Worker briefing context overflow fixed. |
| 13:00 | 2-minute poll timer added — supervisor now self-cycles without host nudges. |
| 14:00 | Poll timer working: 40 turns autonomously. Queue emptied (spec complete). |
| 15:00 | Supervisor created advanced feature work items: LLM analysis, correlation, trend, graph, alerts. |
| 17:00 | Supervisor goroutine auto-restart implemented. Silent death fixed. |
| 18:00 | 13,415 lines. Correlation + trend packages built. |
| 19:00 | 14,474 lines. Analysis package built. 10 test packages. |
| 21:00 | 19,486 lines. All advanced features attempted. Graph store, alert engine, enrichment done. |

## What Was Built

### OSINT Aggregator (19,486 lines Go, 64 files, 12 packages)

```
internal/
  alert/       — configurable alert engine with pattern matching
  analysis/    — LLM-powered intelligence analysis
  api/         — REST API (articles, sources, health dashboard)
  correlation/ — cross-source event correlation
  enrichment/  — article enrichment pipeline (summaries, entities)
  graph/       — SQLite-backed entity relationship graph store
  llm/         — LLM client abstraction for analysis
  model/       — unified Article/Event schema
  scheduler/   — periodic source pull with health tracking
  source/      — RSS/API source adapters (Reuters, BBC, HN, FRED, etc.)
  store/       — SQLite FTS5 store with dedup
  trend/       — trending topic detection across time windows
```

All 12 test packages pass. Build clean. Real HTTP fetching in tests.

## Successes

### 1. Autonomous code generation at scale
The system produced ~20K lines of working, tested Go code across 12 packages with no human code authorship. Workers used glm-5-turbo, claude-haiku, and gpt-5.4-mini via the native adapter.

### 2. Self-healing test regressions
When concurrent workers caused test failures, the supervisor detected the issue, created a fix work item, dispatched a worker, and restored green tests — twice — without human intervention.

### 3. Multi-provider rotation
Workers rotated across z.ai (GLM), Bedrock (Claude), ChatGPT (GPT), and Claude Code adapters. All providers worked for code generation tasks.

### 4. Advanced feature generation
After the spec was complete, the supervisor autonomously created and dispatched work items for LLM intelligence analysis, entity extraction, graph databases, cross-source correlation, trend detection, and alerting — going well beyond the original spec.

### 5. Infrastructure improvements discovered and fixed in production
The overnight run surfaced and fixed 8+ Cogent bugs that wouldn't have been found in unit testing: context overflow, event loss, housekeeping races, stale workers, session lock conflicts.

## Failures

### 1. Web UI never built (CRITICAL)
The most important user-facing feature was marked `awaiting_attestation` but never actually created — no `web/` directory exists. The supervisor skipped past it to build more backend features. **Root cause**: the supervisor optimized for throughput (more items done) rather than verifying completeness. It saw the item move to `awaiting_attestation` and moved on without checking that the deliverable existed.

**Fix needed**: attestation workers must verify that files/artifacts actually exist before passing. The briefing should require listing created files as evidence.

### 2. Persistent duplicate creation
The supervisor creates 3-4 copies of every work item. The bootstrap created 2x, and the advanced feature creation created 4x. Required manual cleanup 3 times. **Root cause**: the native adapter's tool-use loop calls `work_create` multiple times — possibly due to retries, or the supervisor's instructions cause it to create items in a loop.

**Fix needed**: dedup in `work_create` (reject items with identical titles in same project), or rate-limit work creation.

### 3. Supervisor stall pattern (~$100 waste in first overnight, fixed mid-run)
The supervisor waited for EventBus events that never arrived after workers completed. Required hourly host nudges for ~8 hours. **Root cause**: `syncWorkStateFromJob` and `refreshAttestationParentState` don't publish events to the EventBus.

**Fixed mid-run**: added 2-minute poll timer to `waitForSignal`. Supervisor now self-cycles regardless of events.

### 4. Context overflow ($17MB accumulated tool output)
Supervisor session history grew to 17MB from accumulated tool results, hitting OpenAI's 10MB string limit. **Root cause**: bash and read_file tools returned unlimited output, and session history grew unboundedly.

**Fixed mid-run**: tool output capped (50KB bash, 100KB read_file), history trimmed at 2MB, session rotation every 10 productive turns.

### 5. Supervisor goroutine silent death
The supervisor goroutine would exit without logging, leaving serve running but no supervisor dispatching. Required manual restart on each hourly check.

**Fixed mid-run**: auto-restart wrapper with 10s delay.

### 6. Failed items show as failed despite working code
Several work items (graph store, alert system, enrichment) show `failed` status but the code exists and tests pass. **Root cause**: workers completed the code but either didn't call `work_update --state done` (old briefing bug) or timed out before the update.

### 7. No git commits from workers
Workers created 64 files and 19K+ lines but never committed. All changes are unstaged. **Root cause**: workers aren't instructed to commit, and the supervisor doesn't enforce it.

**Fix needed**: worker briefing should include `git add && git commit` as a required step, or the supervisor should commit after attestation.

## Cogent Bugs Found and Fixed During Run

| Bug | Fix | Commit |
|-----|-----|--------|
| Housekeeping kills current job from stale job failure | Check `currentJobID` before failing work item | `b7546ec` |
| Native adapter has no Cogent tools | `SetService` injection via `resolveAdapter` | `c3a1913` |
| Tool output unbounded (17MB) | Cap bash 50KB, read_file 100KB, loop 100KB | `785a6f5` |
| Session history unbounded | Trim at 2MB, rotate every 10 turns | `38c1e51` |
| Supervisor waits for events that never come | 2-minute poll timer | `2732e35` |
| Supervisor goroutine dies silently | Auto-restart wrapper | `11f9263` |
| Native adapter session lock conflict | Use canonical session ID as lock key | `cf008b5` |
| Native adapter ContinueRun not supported | Implement with disk-persisted history | `b0cae37` |

## Cost Estimate

| Component | Estimated Cost |
|-----------|---------------|
| Supervisor (gpt-5.4-mini, ~100 turns) | ~$5 |
| Workers (glm-5-turbo, ~30 dispatches) | ~$10 |
| Workers (claude-haiku, ~15 dispatches) | ~$8 |
| Workers (gpt-5.4-mini/codex, ~10 dispatches) | ~$5 |
| Workers (claude-sonnet, ~5 dispatches) | ~$10 |
| **Total estimated** | **~$38** |

## Recommendations

### Immediate
1. **Attestation must verify artifacts exist** — check that files listed in the objective were actually created
2. **Dedup work_create** — reject duplicate titles
3. **Workers must git commit** — either in the briefing contract or as a post-attestation step
4. **Publish EventBus events from syncWorkStateFromJob** — eliminates the need for poll timer

### Architecture
5. **Worktree isolation** — concurrent workers on main worked but required self-healing. Worktrees would prevent conflicts entirely.
6. **Session context summarization** — instead of hard trim at 2MB, use a cheap LLM call to summarize old turns
7. **Cost tracking per session** — track actual API spend, not just turn count
8. **Supervisor state persistence** — the supervisor loses all context on restart. A persistent state file (`.cogent/supervisor-state.md`) would survive restarts.

### Process
9. **Spec completeness gates** — don't create advanced features until all spec items are verified complete (not just "done" status)
10. **Periodic quality checks** — the supervisor should run `go test ./...` every N turns and create fix items on failure
