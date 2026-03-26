# Design: Hourly Email Digest + Checker Note Mining

## Context

Currently:
- One email fires per completed work item → noisy (dozens/session)
- Checker notes (test results, code quality, architecture concerns) sit in check records, unread systematically
- No quality trend visibility across runs

This design addresses both problems as one integrated feature: an hourly intelligence digest that includes LLM-synthesized insights mined from checker notes.

---

## Architecture Decision: Option A — Goroutine in Serve

**Recommendation: A background `DigestWorker` goroutine started by `serve`, using the in-process native LLM client for synthesis.**

### Why not B (supervisor does it)?

The supervisor is an event-driven agentic session — it reacts to work events. It's not scheduled. Adding hourly synthesis to its cycle means either:
- Injecting clock awareness into the supervisor's briefing (ugly)
- Running synthesis on every cycle (expensive, wrong cadence)
- Adding a "it's been an hour" check to the supervisor prompt (fragile)

The supervisor's job is work dispatch and check decisions, not meta-analysis. Mixing concerns would make its briefing longer and its cycles less focused.

### Why not C (dedicated worker dispatched hourly)?

Dispatching a subprocess (codex/claude) just to do this:
- Adds work-queue overhead for meta-work that doesn't produce code
- Requires a scheduler to dispatch it (which has to live somewhere anyway)
- Creates a subprocess with file-system + DB access that's only reading

The native adapter is already in-process. For synthesis, we call Anthropic/Bedrock directly without spawning a subprocess.

### Why Option A works well

- Serve is already the long-running process
- Direct DB access — no HTTP round-trip
- The synthesis LLM call is a single Anthropic/Bedrock request (< 5s typical)
- Stateless between runs — just queries "last N hours"
- Failures are non-fatal: if synthesis times out, send digest without LLM narrative
- Natural place to add a config flag: `DIGEST_ENABLED=true`

---

## Component Design

### `internal/digest/` — New Package

```go
// DigestWorker accumulates work events over a time window and sends hourly digests.
type DigestWorker struct {
    store    DigestStore
    config   Config
}

type Config struct {
    Interval        time.Duration // default: 1 hour
    LookbackPeriod  time.Duration // default: 1 hour (same as interval)
    MiningWindow    int           // check records to mine for patterns (default: 100)
    SynthesisModel  string        // e.g., "bedrock/claude-sonnet-4-6"
    ResendAPIKey    string
    EmailTo         string
    Enabled         bool
}

func (w *DigestWorker) Start(ctx context.Context) // starts the hourly ticker goroutine
func (w *DigestWorker) RunOnce(ctx context.Context) error // runs a single digest cycle (also for testing)
```

### Store Interface (new methods needed)

```go
type DigestStore interface {
    // Work items that transitioned to done or failed in [since, now)
    ListWorkItemsUpdatedSince(ctx context.Context, since time.Time, states []string) ([]core.WorkItemRecord, error)
    // Check records created in [since, now)
    ListCheckRecordsSince(ctx context.Context, since time.Time, limit int) ([]core.CheckRecord, error)
    // Rolling window of recent check records for pattern mining
    ListRecentCheckRecords(ctx context.Context, limit int) ([]core.CheckRecord, error)
    // Open work items (titles + IDs) for untracked-bug detection
    ListWorkItemsByState(ctx context.Context, state string, limit int) ([]core.WorkItemRecord, error)
}
```

These are additive store methods (new SQL queries on existing tables — no schema changes).

---

## Digest Cycle (hourly)

```
tick (every hour)
  │
  ├─ Query: work items done/failed since last tick
  ├─ Query: check records since last tick
  ├─ Query: last 100 check records (mining window)
  ├─ Query: open work items (for untracked-bug detection)
  │
  ├─ Build DigestData struct
  │   ├─ completed: []WorkSummary
  │   ├─ failed: []WorkSummary
  │   ├─ checkStats: pass rate, counts
  │   └─ checkerNotes: []NoteEntry
  │
  ├─ LLM synthesis (30s timeout, non-blocking)
  │   Input: structured JSON of all DigestData
  │   Output: DigestInsights (see below)
  │
  └─ Send email (fire-and-forget)
      Subject: "[Cogent] digest: 18:00–19:00 · 4 done · 2 failed"
      Body: BuildDigestEmail(data, insights)
```

If there were zero completions and zero check records in the window: **skip** — no email sent.

---

## LLM Synthesis

### Input Prompt (structured)

```
You are analyzing Cogent work queue activity for the past hour.

## Completed Work Items (${N} total)
${list of titles, check results, and diff stats}

## Failed Work Items
${list with reason}

## Checker Notes (last ${M} checks)
${each check: work title, result pass/fail, checker notes text}

## Currently Open Work Items (for untracked bug detection)
${titles only}

Produce a JSON synthesis with this structure:
{
  "executive_summary": "2-3 sentence narrative of what happened",
  "quality_trend": "improving|stable|declining",
  "quality_score": 0.0-1.0,  // pass rate
  "recurring_failures": [
    {"pattern": "description", "work_items": ["title1", "title2"], "frequency": 3}
  ],
  "spec_problems": [
    {"description": "what the spec might be wrong about", "evidence": "checker note excerpts"}
  ],
  "potential_new_work_items": [
    {"title": "suggested title", "reason": "what checker found"}
  ],
  "highlight": "one sentence: the most important thing that happened"
}
```

The synthesis model should be `bedrock/claude-sonnet-4-6` or `claude-sonnet-4-6` (multimodal not needed here, but reasoning quality matters).

If synthesis fails/times out: send digest without the intelligence section. The metrics (counts, pass rates) are still valuable.

---

## Digest Email Structure

```
Subject: [Cogent] digest 18:00–19:00 · 4 done · 1 failed

┌─────────────────────────────────────────────────────┐
│ Cogent Hourly Digest                                   │
│ 2026-03-24 18:00 – 19:00                            │
├─────────────────────────────────────────────────────┤
│ HIGHLIGHT (LLM one-liner)                           │
├─────────────────────────────────────────────────────┤
│ STATUS                                              │
│  ✓ 4 completed  ✗ 1 failed  ⟳ 3 in progress       │
│  Check pass rate: 87% (13/15 checks passed)         │
│  Quality trend: ↑ improving                         │
├─────────────────────────────────────────────────────┤
│ COMPLETED ITEMS                                     │
│  ✓ Fix stall detection (2 checks)                   │
│  ✓ Wire failure email notification (1 check)        │
│  ✓ Email: screenshots + artifacts                   │
│  ✓ Fix duplicate work creation                      │
├─────────────────────────────────────────────────────┤
│ FAILED ITEMS                                        │
│  ✗ Test worktree isolation – checker: missing deps  │
├─────────────────────────────────────────────────────┤
│ INTELLIGENCE (LLM synthesis)                        │
│                                                     │
│ Executive Summary:                                  │
│  "Email notifications are fully wired. Stall        │
│   detection is now process-liveness based.          │
│   Test isolation remains blocked on deps."          │
│                                                     │
│ Patterns Found:                                     │
│  • Worktree tests consistently fail missing         │
│    dependency setup — may be a spec issue           │
│                                                     │
│ Potential New Work Items:                           │
│  • "Add worktree dependency bootstrap" (found by    │
│    checker on work_01KMCEFK3)                       │
├─────────────────────────────────────────────────────┤
│ [screenshots inline if any completed item has them] │
└─────────────────────────────────────────────────────┘
```

---

## Suppressing Individual Completion Emails

When `DIGEST_ENABLED=true` (env var):

| Event | Current behavior | New behavior |
|-------|-----------------|--------------|
| Work → done | `sendWorkNotification` fires | **Skip** |
| Work → failed | `sendWorkFailureNotification` fires | **Skip** |
| 3+ check failures | `SendSpecEscalationEmail` fires | **Keep** (urgent, real-time) |

Spec escalation stays real-time because it requires human action. The digest cannot substitute for it — the human needs to respond before the next dispatch cycle.

Implementation: check `os.Getenv("DIGEST_ENABLED") == "true"` in `UpdateWork` before calling the individual notification functions.

---

## Checker Note Mining — Detail

The mining pass examines the `CheckerNotes` field from `CheckReport` across a rolling window (default: last 100 records). The LLM prompt includes all notes with their context (work title, result, model).

### What to mine for

1. **Recurring failures** — same work item failing repeatedly, or same error type appearing in multiple items. Signals: "Playwright not installed", "import cycle", "missing RESEND_API_KEY in test env"

2. **Spec problems** — 3+ failures on same item where checker notes describe impossible or contradictory requirements. Already triggers escalation, but digest should surface earlier (at 2 failures).

3. **Quality trend** — compute rolling pass rate. Compare last 20 checks vs. previous 20. Present as direction + score.

4. **Untracked bugs** — checker notes that say "I also noticed X bug" or "there's an issue with Y" where Y is not an open work item. These are free discoveries that should become work items.

5. **Model performance** — which checker models are passing vs. failing most. Which worker models are producing work that passes first-try vs. needs multiple iterations. (Long-term signal; useful once you have 50+ records.)

### Not mined in v1

- Videos (no structured analysis)
- Screenshots (LLM can see them but synthesis cost is high for bulk review)
- Test output (too verbose for synthesis; pass/fail counts are enough)

---

## Integration with Serve

In `cmd/cogent/main.go` (or wherever `serve` initializes):

```go
if os.Getenv("DIGEST_ENABLED") == "true" {
    d := digest.NewDigestWorker(digest.Config{
        Interval:       time.Hour,
        MiningWindow:   100,
        SynthesisModel: "bedrock/claude-sonnet-4-6",
        ResendAPIKey:   os.Getenv("RESEND_API_KEY"),
        EmailTo:        os.Getenv("EMAIL_TO"),
        Enabled:        true,
    }, svc.Store())
    go d.Start(ctx)
}
```

No changes to supervisor briefing or check flow. The digest worker is a completely independent goroutine.

---

## Implementation Sequence

1. **Store methods** — add `ListWorkItemsUpdatedSince`, `ListCheckRecordsSince`, `ListRecentCheckRecords`
2. **`internal/digest/` package** — `DigestWorker`, `DigestData`, `DigestInsights` structs
3. **`internal/notify/email_builder.go`** — add `BuildDigestEmail`
4. **Suppress individual emails** — add `DIGEST_ENABLED` gate in `UpdateWork`
5. **Wire into serve** — start DigestWorker goroutine on startup
6. **LLM synthesis** — call native Anthropic/Bedrock client directly (not via subprocess)

---

## Open Questions / Risks

**Q: What if serve restarts mid-hour?**
The lookback query covers the full period since the last digest. On restart, the next tick will cover everything since the last email, not just since the restart. This requires tracking "last digest sent at" — either in a small state file (`.cogent/digest-last-run.txt`) or in the DB (a new `system_state` KV table). Simplest: env var `DIGEST_LAST_RUN` or a file.

**Q: What if the native LLM client isn't configured (no Bedrock/Anthropic keys)?**
Synthesis gracefully fails; digest is sent with structured data only (no narrative section). Log a warning. Don't skip the digest.

**Q: Batching screenshots in digest**
At hourly cadence with 10+ completions, attaching all screenshots would make the email enormous. Limit to: the 3 most recent completions with screenshots, or screenshots only for items that had failures/concerns noted.

**Q: Should the supervisor receive the digest insights?**
Yes, optionally. After the digest runs, if `potential_new_work_items` is non-empty, the DigestWorker could inject those as notes on the parent work item or create proposals. But this crosses into "creating work from meta-analysis" which the spec says only happens through supervisor/human review. For v1: include in digest email only, let human create the items.

**Q: Frequency — is 1 hour right?**
For an active session with 10–20 work items completing per hour, yes. For quieter periods, the digest naturally skips (no completions = no email). Make interval configurable via `DIGEST_INTERVAL` env var.

---

## Summary

| Concern | Decision |
|---------|----------|
| Where does it live? | Background goroutine in serve |
| How scheduled? | Internal ticker, 1h default |
| LLM synthesis? | Native adapter, direct API call, 30s timeout |
| Fallback if LLM fails? | Send structured digest without narrative |
| Individual emails | Suppressed when `DIGEST_ENABLED=true` |
| Spec escalations | Always real-time (not batched) |
| Checker note mining | Part of digest synthesis, rolling 100-record window |
| New work item creation | Not automated — surfaced in digest for human review |
| Screenshots in digest | Limited to 3 most recent completions with UI changes |
| Schema changes | None — new SQL queries on existing tables |
