Date: 2026-03-11
Kind: Test plan + seed spec
Status: Draft
Priority: 1
Requires: [docs/fase-work-runtime.md, docs/fase-work-api-and-schema.md, docs/fase-worker-briefing-schema.md]
Owner: Runtime / Dogfood

## Narrative Summary (1-minute read)

This seed starts a hands-off end-to-end dogfood test where `fase` workers
build, verify, review, red-team, and report on a tiny local web desktop app.

After the initial planner bootstrap, the host should mostly observe through
work projections, logs, and artifacts. The point is not only to build the app.
The point is to test whether `fase` can operate as a durable work runtime for
other coding-agent workers.

## What Changed

1. Defined a minimal seed project for an all-`fase` worker test.
2. Fixed the worker governance rules for a hands-off run.
3. Required Playwright screenshots and video artifacts for verification.
4. Specified adapter/model routing preferences with low-cost defaults.

## What To Do Next

1. Create the root work item from this seed.
2. Launch one planner worker that reads this file and the local `fase` skill.
3. Observe progress from `work projection` and emitted artifacts.
4. Intervene only for explicit approval, recovery, or runtime-debug reasons.

## 1) Objective

Build a tiny local web desktop app and take it through the full work lifecycle
using only `fase` workers:

- plan/spec
- implement
- verify with Playwright
- review
- red-team
- final release/debrief reporting

The app itself should be intentionally small. The work-runtime behavior is the
main thing under test.

## 2) Product Target

Build a local browser-based "web desktop" with:

- a desktop surface
- at least one launchable window/app
- a taskbar or dock
- minimal polish sufficient for screenshots and interaction tests

It does not need to be ambitious. It does need to be coherent, runnable, and
testable.

## 3) Runtime Rule

After the first planner worker starts, substantive work should be done through
`fase` workers only.

That means:
- workers use the `fase work` API
- workers create child work, proposals, discoveries, notes, and updates
- workers launch other workers through `fase run` or `fase send`
- the host mainly observes

The host may still:
- inspect status/logs/projections/artifacts
- review screenshots and videos
- accept/reject explicit structural proposals
- recover from provider/runtime failures

## 4) Worker Governance

All workers should follow these rules:

1. Hydrate from work state, not from bespoke prompts.
2. Read the local `fase` skill and work-runtime docs when needed.
3. Publish a structured `work update` at each phase boundary.
4. Use notes for findings, review comments, and operator-visible context.
5. Use proposals for structural graph changes.
6. Do not silently approve your own implementation work.
7. Attach or emit artifacts for anything that should be inspected later.
8. Prefer spawning another worker over keeping too much work in one session.
9. Stay inside the repo and declared target paths; do not scan unrelated parts of the filesystem.
10. Treat phase boundaries as hard stops: if your assigned work is scaffold, stop at scaffold and hand off.

## 5) Cost And Adapter Policy

Default lane:
- `opencode`
- low-cost or free models first

Preferred low-cost routing:
- `opencode/minimax-m2.5-free`
- `opencode/mimo-v2-flash-free`
- `opencode/gpt-5-nano`

Deeper but slower fallback:
- `zai-coding-plan/glm-5`

Stronger comparison/review lanes:
- `codex/gpt-5.4`
- `claude/claude-haiku-4-5`

Policy:
- the root planning/coherence thread may use the strongest available reasoning model
- cheap models should do most implementation and ordinary verification loops
- prefer a range of fast free OpenCode models instead of leaning on one slow default
- use `glm-5` when the task mostly waits on long-running scripts/tests and benefits from stronger planning/verification
- stronger models should be used sparingly for planning, review, recovery, or comparison
- if a listed model is uncertain, workers should use `catalog probe` or recent
  usage history before choosing it

## 6) Required Graph Shape

The initial planner should create at least:

- root `plan` work
- `implement` work for scaffold
- `implement` work for core UI/features
- `verify` work for Playwright E2E
- `review` work for code review
- `red_team` work for misuse/security pass
- `doc` work for final release report

The planner may refine this graph, but should keep it legible.

Phase boundary rule:

- `Scaffold Project Structure` stops after a runnable scaffold and dependency/tooling setup.
- `Implement Core UI` owns the desktop surface, windows, and dock/taskbar behavior.
- `Verify Playwright E2E` owns screenshots, video, and browser validation.
- Review, red-team, and release reporting remain separate work.

## 7) Required Artifacts

The workflow must emit inspectable artifacts, especially:

- Playwright screenshots
- Playwright video if available
- test report output
- review findings report
- red-team/security report
- final debrief or release report

The host should be able to assess progress largely through these artifacts and
work projections.

## 8) Success Criteria

The run succeeds if:

1. The app builds and runs locally.
2. Playwright E2E passes.
3. Visual verification artifacts exist.
4. Review and red-team passes are recorded as work results.
5. Final work reaches verified or approved end states.
6. The host did not need to manually steer ordinary implementation steps.

## 9) Failure/Recovery Rules

If a worker or provider fails:

- prefer `send` for same-adapter continuation
- prefer `transfer` for explicit cross-adapter recovery
- record a note or update explaining the recovery

If the graph needs to change materially:

- use proposals
- keep the root objective stable unless explicitly approved otherwise

## 10) Runtime Scoping Rule

All worker-issued `fase` commands must operate on the same isolated runtime
that launched the run.

Preferred rule:

- use bare `fase` from the inherited environment
- if a wrapper path is provided, use that wrapper
- verify graph mutations against runtime state before narrating success

Do not assume that a repo-local `./bin/fase` invocation is sufficient unless
the inherited `FASE_*` environment is present.

## 11) Minimal Observer Workflow

The host should primarily observe with:

```bash
./bin/fase work projection checklist <root-work-id>
./bin/fase work projection status <root-work-id>
./bin/fase work show <root-work-id>
./bin/fase work ready --json
./bin/fase artifacts list --work <work-id>
./bin/fase logs --follow <job-id>
```

## 12) First Planner Task

The first worker should:

1. read this seed
2. read the `fase` skill
3. inspect runtime/catalog state
4. create the initial work graph
5. publish the checklist/status through work state
6. start delegating to worker jobs

The first worker should act as planner/coordinator, not as the only implementer.
