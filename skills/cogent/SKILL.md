---
name: cogent
description: Use Cogent as a bash-callable subagent runtime for coding-agent CLIs. Prefer this when you need to choose among available adapters, launch work through one stable JSON CLI, continue same-vendor sessions, or hand work off across vendors.
---

# Cogent

## When to use

Use `cogent` when you want a local control plane over installed coding-agent CLIs instead of talking to one vendor CLI directly.

Typical cases:
- choose among Codex, Claude, Pi, Gemini, Factory, or OpenCode at runtime,
- inspect discovered provider/model/auth-mode inventory before routing work,
- launch a task through one stable `--json` interface,
- participate in a durable `work` graph and coordinate through the work API,
- continue a same-vendor session through `send`,
- ask a still-live session to land the plane with `debrief`,
- export an explicit transfer bundle and launch it on another adapter when failover is required,
- inspect durable local session and artifact state.
- search canonical local history before falling back to vendor-native state.

## Core workflow

1. Inspect the local runtime first:

```bash
cogent runtime --json
cogent catalog sync --json
cogent catalog show --json
cogent catalog probe --json --adapter opencode --provider openai
cogent history search --json --query "deployment rollback"
```

2. Choose an adapter using:
- `enabled`
- `available`
- capability flags
- configured traits like `speed`, `cost`, `summary`, and `tags`
- best-effort provider/model pricing when discovered locally

3. Start work:

```bash
cogent run --json --adapter codex --cwd /path/to/repo --prompt "Fix the failing tests."
```

`run` always queues background work and returns immediately.

4. Use returned IDs for follow-up operations:

```bash
cogent status --json --wait <job-id>
cogent logs --json <job-id>
cogent session --json <session-id>
cogent artifacts list --json --job <job-id>
```

For work-runtime usage:

```bash
cogent work list --json                    # list all work items
cogent work show <work-id>                 # show details + docs + attestations
cogent work ready --json                   # list actionable work
cogent work create --title "..." --objective "..." --kind implement
cogent inbox "quick idea"                  # shorthand for work create --kind idea
cogent work update <work-id> --message "Started implementation"
cogent work complete <work-id> --message "Done"
cogent work note-add <work-id> --type finding --text "..."
cogent work private-note <work-id> --text "SSH creds..." --type credential  # gitignored DB
cogent work doc-set --file docs/adr-001.md                                 # auto-creates work item from doc
cogent work doc-set <work-id> --file docs/adr-001.md --title "ADR-001"     # attach doc to existing work item
cogent work attest <work-id> --result passed --summary "Tests pass" --verifier-kind deterministic --method test
cogent work claim <work-id> --claimant worker-a
cogent work release <work-id> --claimant worker-a
cogent work renew-lease <work-id> --claimant worker-a --lease 15m          # heartbeat
cogent work children <work-id> --json
cogent work discover <work-id> --title "..." --objective "..." --kind verify --rationale "..."
cogent work proposal create --type add_edge --target <work-id> --rationale "..." --patch '{"edge_type":"blocks","source_work_id":"<id>"}'
cogent work approve <work-id> --message "Approved"
cogent work reject <work-id> --message "Needs rework"
cogent work lock <work-id>                 # human lock (prevents agent claim)
cogent work unlock <work-id>
cogent reconcile --json                    # release orphaned work with expired leases
cogent artifacts attach --work <work-id> --path ./report.md --kind report
```

Supervisor (autonomous dispatch loop):

```bash
cogent supervisor --default-adapter codex --max-concurrent 1  # run forever, dispatch work
cogent supervisor --dry-run --json                            # show what would dispatch
cogent supervisor --cwd /path/to/repo                        # target a specific repo
```

The supervisor auto-bootstraps empty repos: if no work exists, it creates a bootstrap
work item from the repo's docs/README, dispatches an agent to analyze and create the
work graph. Each repo gets its own `.cogent/` directory (per-repo state isolation).

Split databases:
- `.cogent/cogent.db` — public work graph (tracked in git)
- `.cogent/cogent-private.db` — private notes, credentials (gitignored, never committed)

Doc-work coupling (IMPORTANT):
- Every tracked doc MUST have a corresponding work item and authoritative repo-relative path. Use `work doc-set` to guarantee this.
- `work doc-set --file <path>` without a work-id auto-creates a linked work item from the doc import
- `work doc-set <work-id> --file <path>` refreshes the tracked runtime record for that work item
- `work doc-set` is an import/bootstrap helper; the repo file at the declared path remains authoritative
- `work show` returns tracked docs in the response, including repo-path linkage for review
- The mind-graph UI renders docs in the detail panel
- Principle: documentation commits before execution commits (ADR-0002)

Child-work policy:
- create child work directly only for:
  - unexpected local work discovered during execution,
  - fanout work that can run in parallel with distinct bounded outputs,
  - sequential context isolation where the next step benefits from a fresh bounded context.
- create a child only when you can stay ignorant of implementation details and still name the required result, artifact, or attestation bundle up front.
- if the proposed child does not have a clear cheap verifier or attestation target, do not create it directly; create a proposal instead.
- do not create children just to offload thinking or explore vaguely. If scope may expand or verification is unclear, use `cogent work proposal create`.

5. Continue same-vendor work:

```bash
cogent send --json --session <session-id> --prompt "Continue from the last result."
```

`send` always queues a new background job against the existing native session.

6. Ask for a debrief when you need the live agent's own world model:

```bash
cogent debrief --json --session <session-id> --reason "prepare a debugging summary"
```

`debrief` queues a continuation job and produces a markdown artifact when it finishes.

7. Transfer only when native continuation is impossible or undesirable:

```bash
cogent transfer export --json --job <job-id> --reason "provider outage" --mode recovery
cogent transfer run --json --transfer <transfer-id-or-path> --adapter gemini --cwd /path/to/repo
```

`transfer run` should also return immediately with a queued background job.

## Operating rules

- NEVER use `git add .` or `git add -A`. ALWAYS stage specific files: `git add <file1> <file2>`. Agents that stage the whole working tree risk committing unrelated dirty files (test artifacts, benchmark output, credentials).
- Prefer `runtime --json` as the machine-facing inventory command.
- Prefer `catalog show --json` when choosing among providers/models and auth modes.
- Prefer `catalog probe --json` when listed models may not match actual plan entitlement.
- Prefer models with recent successful `catalog show` history over merely listed-but-unused ones.
- Prefer `status --json` when you need normalized token usage or cost for a completed job.
- Treat `cogent` as machine-facing first. Use `--json` unless a human-readable summary is explicitly better.
- Treat `work` as the source of truth; use prompts only as compiled hydration views over work state.
- If the host gives you an isolated runtime via inherited `Cogent_*` env vars or a runtime-specific wrapper on `PATH`, use bare `cogent` so all graph mutations land in the same runtime.
- Treat `run`, `send`, and `transfer run` as launch operations, not blocking operations.
- Treat `debrief` as a debugging/recovery workflow, not a normal orchestration step.
- Use `status --wait` when you want `cogent` to own the polling loop.
- Use `artifacts show` to read a debrief or transfer artifact by id.
- Use `history search` to query prior canonical jobs, turns, events, and artifact content.
- Do not assume every adapter supports `send`; inspect capability flags first.
- Same-vendor continuation is `send`. Cross-vendor failover is `transfer`.
- Do not expect `cogent` to perform vendor auth flows for you.
- Persisted session history and raw artifacts are part of the intended model.
- Prefer fresh `run` jobs for normal orchestration; use `transfer` for failover/recovery.
- Treat `catalog show` as discovered inventory and `catalog probe` as a best-effort entitlement/runability check.
- Treat adapter-native history import as a future special case when the session was not created by `cogent`.
- Publish structured updates at phase boundaries, and use notes for findings, feedback, and recovery context.
- Use proposals for structural graph edits instead of silently rewriting the work graph.
- Use direct child creation only for unexpected work, fanout work, or sequential context isolation with bounded and easily verified outputs.
- If a possible child cannot be verified cheaply or clearly, propose it instead of creating it.
- Do not self-approve implementation work; verification and review are separate work.
- Before claiming a planning phase succeeded, verify the expected child work actually exists with `cogent work children` or `cogent work show`.
- Stay within the repo and declared target paths; do not run broad home-directory scans like `find /Users/...` when the target path is already known.
- Treat assigned work scope as a hard boundary. If you own scaffold work, finish scaffold, update work state, and stop instead of drifting into core UI implementation.
- Prefer explicit `cogent work update`, `cogent work note-add`, and `cogent work complete` calls over burying progress in tool transcripts.

## Adapter selection heuristics

- Prefer adapters with `enabled=true` and `available=true`.
- Treat estimated cost as a routing/debugging hint, not authoritative billing.
- Prefer adapters with `native_resume=true` when you expect iterative follow-up turns.
- Prefer adapters with `session_export=true` when durable native-session linkage matters.
