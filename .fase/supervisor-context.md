# Supervisor Context (auto-generated)

This file contains a compressed summary of previous conversation turns.
It is automatically updated when history compression occurs.

## Context Summary

**Work ID:** `work_01KMKSSPV3EN6W672GT0KZE7B2`
**Kind:** implement — Final cleanup: delete `.factory/`, remove dead code

### What was asked
1. Delete `.factory/` directory entirely (droid validation logs, no runtime value)
2. Add `.factory/` to `.gitignore` (already present)
3. Remove `dispatchChecker` from `service.go` (supervisor no longer auto-dispatches checkers)
4. Remove `buildCheckerBriefing` hardcoded template (replaced by skill file)
5. Remove `WorkCheck` alias (everything uses `CreateCheckRecord`)
6. Build must pass

### Files modified

**`internal/service/service.go`** — Removed 5 blocks:
- `uiCheckerModels` var (lines 54-58) — model pool for checker dispatch
- `dispatchChecker` call site in `UpdateWork` (lines 3264-3267) — auto-dispatch when worker signals checking state
- `checkerModels` var, `dispatchChecker` method, `checkerDispatchCWD` method (lines 3369-3458) — entire checker dispatch infrastructure
- `buildCheckerBriefing` method (lines 3459-3550) — hardcoded checker briefing template
- `WorkCheck` method (lines 3932-3943) — legacy alias for `CreateCheckRecord`

**`internal/cli/serve.go`** (line 1326) — Changed `svc.WorkCheck(...)` call to `svc.CreateCheckRecord(r.Context(), service.CheckRecordCreateRequest{...})` with field mapping: `WorkID`, `CreatedBy`, `Verdict`, `Summary`, `Details`, `Artifacts`.

**`internal/service/service_test.go`** — Removed 4 dead test functions:
- `TestWorkCheckUsesCreateCheckRecordAlias` (lines 2560-2615)
- `TestBuildCheckerBriefingIncludesUIInstructionsForUIWork` (lines 2757-2778)
- `TestBuildCheckerBriefingOmitsUIInstructionsForNonUIWork` (lines 2779-2804)
- `TestCheckerDispatchCWDUsesMainRepoRootForWorktreeJobs` (lines 3854-3910)

**`.factory/`** — Deleted entirely (contained `library/` and `validation/` subdirs with droid validation logs)

### Key decisions
- `WorkCheckRequest` struct (line 408) was **kept** — it's the wire/transport type with JSON tags used by `serve.go`
- `.gitignore` already had `.factory/` — no change needed
- `workNeedsUIVerification` function was preserved (careful to not accidentally remove it when removing `uiCheckerModels`)

### Errors encountered
- **Python string replacement failures:** Initial attempts using python3 heredoc scripts failed silently — the file appeared modified in-memory but changes weren't persisted to disk. Root cause unclear (possibly heredoc quoting issues with Go template strings containing backticks).
- **awk `func workNeedsUIVerification` accidental deletion:** First awk pass incorrectly included line 59 (the `workNeedsUIVerification` function declaration) in the skip range for `uiCheckerModels`. Fixed by restoring via `git checkout` and adjusting awk range to stop at line 58.
- **awk brace-matching failure for test removal:** Using brace-depth tracking in awk to find test function boundaries failed — it incorrectly split `TestCheckerBriefingContainsNoLegacyCompletionGuidance` (which references `buildCheckerBriefing` in name only but isn't dead code). Fixed by using `sed` with hardcoded line ranges found via `grep -n "^func Test"`.

### Current state
- `go build ./...` — **passes**
- `go test ./internal/service/... ./internal/cli/...` — **passes**
- Full test suite (`go test ./...`) has 2 pre-existing flaky failures in `cmd/fase/orchestration_e2e_test.go` (unrelated to changes)
- Commit made: `fase(work_01KMKSSPV3EN6W672GT0KZE7B2): delete .factory/, remove dead code (dispatchChecker, buildCheckerBriefing, WorkCheck alias)`

### What remains
- Nothing — all 5 tasks completed and committed
