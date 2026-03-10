---
name: cagent
description: Use cagent as a bash-callable subagent runtime for coding-agent CLIs. Prefer this when you need to choose among available adapters, launch work through one stable JSON CLI, continue same-vendor sessions, or hand work off across vendors.
---

# cagent

## When to use

Use `cagent` when you want a local control plane over installed coding-agent CLIs instead of talking to one vendor CLI directly.

Typical cases:
- choose among Codex, Claude, Pi, Gemini, Factory, or OpenCode at runtime,
- inspect discovered provider/model/auth-mode inventory before routing work,
- launch a task through one stable `--json` interface,
- continue a same-vendor session through `send`,
- ask a still-live session to land the plane with `debrief`,
- export an explicit transfer bundle and launch it on another adapter when failover is required,
- inspect durable local session and artifact state.
- search canonical local history before falling back to vendor-native state.

## Core workflow

1. Inspect the local runtime first:

```bash
cagent runtime --json
cagent catalog sync --json
cagent catalog show --json
cagent catalog probe --json --adapter opencode --provider openai
cagent history search --json --query "deployment rollback"
```

2. Choose an adapter using:
- `enabled`
- `available`
- capability flags
- configured traits like `speed`, `cost`, `summary`, and `tags`
- best-effort provider/model pricing when discovered locally

3. Start work:

```bash
cagent run --json --adapter codex --cwd /path/to/repo --prompt "Fix the failing tests."
```

`run` always queues background work and returns immediately.

4. Use returned IDs for follow-up operations:

```bash
cagent status --json --wait <job-id>
cagent logs --json <job-id>
cagent session --json <session-id>
cagent artifacts list --json --job <job-id>
```

5. Continue same-vendor work:

```bash
cagent send --json --session <session-id> --prompt "Continue from the last result."
```

`send` always queues a new background job against the existing native session.

6. Ask for a debrief when you need the live agent's own world model:

```bash
cagent debrief --json --session <session-id> --reason "prepare a debugging summary"
```

`debrief` queues a continuation job and produces a markdown artifact when it finishes.

7. Transfer only when native continuation is impossible or undesirable:

```bash
cagent transfer export --json --job <job-id> --reason "provider outage" --mode recovery
cagent transfer run --json --transfer <transfer-id-or-path> --adapter gemini --cwd /path/to/repo
```

`transfer run` should also return immediately with a queued background job.

## Operating rules

- Prefer `runtime --json` as the machine-facing inventory command.
- Prefer `catalog show --json` when choosing among providers/models and auth modes.
- Prefer `catalog probe --json` when listed models may not match actual plan entitlement.
- Prefer models with recent successful `catalog show` history over merely listed-but-unused ones.
- Prefer `status --json` when you need normalized token usage or cost for a completed job.
- Treat `cagent` as machine-facing first. Use `--json` unless a human-readable summary is explicitly better.
- Treat `run`, `send`, and `transfer run` as launch operations, not blocking operations.
- Treat `debrief` as a debugging/recovery workflow, not a normal orchestration step.
- Use `status --wait` when you want `cagent` to own the polling loop.
- Use `artifacts show` to read a debrief or transfer artifact by id.
- Use `history search` to query prior canonical jobs, turns, events, and artifact content.
- Do not assume every adapter supports `send`; inspect capability flags first.
- Same-vendor continuation is `send`. Cross-vendor failover is `transfer`.
- Do not expect `cagent` to perform vendor auth flows for you.
- Persisted session history and raw artifacts are part of the intended model.
- Prefer fresh `run` jobs for normal orchestration; use `transfer` for failover/recovery.
- Treat `catalog show` as discovered inventory and `catalog probe` as a best-effort entitlement/runability check.
- Treat adapter-native history import as a future special case when the session was not created by `cagent`.

## Adapter selection heuristics

- Prefer adapters with `enabled=true` and `available=true`.
- Treat estimated cost as a routing/debugging hint, not authoritative billing.
- Prefer adapters with `native_resume=true` when you expect iterative follow-up turns.
- Prefer adapters with `session_export=true` when durable native-session linkage matters.
