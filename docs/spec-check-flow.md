# Check Flow Spec

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
- Reads the checker's report
- Decides: mark done (ship) or back to doing (fix)
- On done: merge worktree to main, email fires with report as proof
- On back-to-doing: sends failure context to original worker session
- After 3 failed checks on same item: considers whether this is a spec problem, escalates to human with recommendation

## States

| State | Meaning | Who transitions |
|-------|---------|-----------------|
| `ready` | Available for dispatch | Supervisor |
| `doing` | Worker is implementing | Worker (on claim) |
| `checking` | Worker finished, checker is verifying | Worker signals, system dispatches checker |
| `done` | Supervisor approved, merged, emailed | Supervisor |
| `failed` | Unrecoverable failure | Supervisor |

No `awaiting_attestation`. No `report` state — the report is data on the check record, not a state.

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

## Email (on done only)

Subject: `[FASE] done: <work title>`

Body: the check report rendered as HTML.
- What was built (diff stat)
- Test results (pass/fail counts)
- Screenshots inline (if any)
- Checker's notes

Attachments: screenshots as PNGs.

No email on failure. Failures are internal iteration.

## Spec Escalation

After 3 failed checks on the same work item, supervisor emails the human:

Subject: `[FASE] spec question: <work title>`

Body: "This item has failed verification 3 times. Here's what keeps going wrong: [checker reports]. The spec may need to change. Recommendation: [supervisor's suggestion]."

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
