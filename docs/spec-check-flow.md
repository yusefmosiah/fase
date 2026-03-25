# Check Flow Spec

> **Contract Note**: The canonical work execution states are defined in `internal/core/types.go` (see `WorkExecutionState` constants). This document describes the original design intent; the runtime code is the authoritative source. See the README for the precedence rule.

> **Briefing Note**: The checker briefing is generated at runtime from `internal/service/service.go`. There is no separate static checker-briefing contract file.

## Flow

```
ready → doing → checking → report → done
```

## Roles

**Worker** — does the work. Any model.
- Writes code in a worktree
- Commits: `fase(<work-id>): <summary>`
- Signals: `fase work update <id> --execution-state checking`
- Does NOT decide if the work is good

**Checker** — produces evidence. Different model from worker.
- Runs in the same worktree (read-only — doesn't modify code)
- Runs `go test ./...`
- Runs Playwright if the work involves UI
- Collects: test output, screenshots, diff stat, build status
- Writes a structured report to the check record
- Does NOT decide pass/fail — just reports what it sees

**Supervisor** — makes decisions.
- Reads the canonical review bundle via `fase work show`
- Decides: mark done (ship) or back to doing (fix)
- On done: merge worktree to main, email/reporting reuses the canonical proof bundle
- On back-to-doing: sends failure context to original worker session
- After 3 failed checks on same item: considers whether this is a spec problem, escalates to human with recommendation

## States

> **Historical note**: This table shows the original design from the spec. The canonical states are defined in `internal/core/types.go`. The code includes `checking` (the canonical handoff state) and may include deprecated aliases for backward compatibility. Always reference `internal/core/types.go` for the authoritative state list.

| State | Meaning | Who transitions |
|-------|---------|-----------------|
| `ready` | Available for dispatch | Supervisor |
| `doing` | Worker is implementing | Worker (on claim) |
| `checking` | Worker finished, checker is verifying | Worker signals, system dispatches checker |
| `done` | Supervisor approved, merged, emailed | Supervisor |
| `failed` | Unrecoverable failure | Supervisor |

The original spec design called for removing `awaiting_attestation` in favor of `checking`. The code implements `checking` as the canonical verification handoff state.

## Check Record

```go
type CheckRecord struct {
    CheckID     string
    WorkID      string
    CheckerModel string    // who ran the check
    WorkerModel  string    // who did the work
    Result      string    // "pass" or "fail"
    Report      CheckReport
    CreatedAt   time.Time
}

type CheckReport struct {
    BuildOK       bool
    TestsPassed   int
    TestsFailed   int
    TestOutput    string        // truncated to 50KB
    DiffStat      string        // files changed, insertions, deletions
    Screenshots   []string      // paths in .fase/artifacts/<work-id>/
    Videos        []string      // paths
    CheckerNotes  string        // free-form observations from the checker
}
```

## Artifact Storage

```
.fase/artifacts/<work-id>/
  go-test-output.txt
  diff-stat.txt
  screenshots/
    01-dashboard.png
    02-articles.png
  videos/
    test-run.webm
  checker-notes.md
```

Artifacts persist even after worktree cleanup. They're the proof.

## Completion Reporting

Subject: `[FASE] done: <work title>`

Body: the latest passing check report rendered alongside canonical proof-bundle references for the same work item.
- Work ID plus execution/approval state
- Check and attestation identifiers
- Artifact and doc identifiers/paths from the canonical review bundle
- What was built (diff stat)
- Test results (pass/fail counts)
- Screenshots inline (if any)
- Checker's notes

Attachments: screenshots as PNGs.

No email on failure. Failures are internal iteration.

## Spec Escalation

After 3 failed checks on the same work item, supervisor emails the human:

Subject: `[FASE] spec question: <work title>`

Body: "This item has failed verification 3 times. Here's what keeps going wrong: [checker reports]." The escalation also carries canonical proof-bundle references back to the same work/check/attestation/artifact/doc records used by `fase work show`, so operators do not have to reconcile a prose-only summary.

This is the ONLY failure email the human receives.

## Stats (future)

Every check is a data point:
- `worker_model`: who implemented
- `checker_model`: who checked
- `result`: pass/fail
- `iterations`: how many check cycles
- `duration`: time from doing to done

Over time: model quality matrix from real work, not benchmarks.

## Implementation Priority

1. Rename `awaiting_attestation` → `checking` in states
2. Auto-dispatch checker when worker signals `checking`
3. Checker briefing: run tests, collect artifacts, write report
4. Store check record with structured report
5. Supervisor reads report, decides done or back-to-doing
6. Email on done with report
7. Artifact storage in `.fase/artifacts/`
8. Spec escalation after 3 failures
9. Stats collection
