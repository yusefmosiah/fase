# cagent for Host Agents

`cagent` is designed to be called by another coding agent as a local subprocess.

Use it when the host agent wants:
- one stable JSON CLI instead of vendor-specific command lines,
- durable local sessions and artifacts,
- provider/model/auth-mode discovery for routing,
- same-vendor continuation through `send`,
- a model-authored debrief from a still-live session,
- explicit cross-vendor failover through `transfer`.

## Recommended host workflow

1. Query runtime inventory:

```bash
cagent runtime --json
cagent catalog sync --json
cagent catalog show --json
```

2. Choose an adapter based on:
- `enabled`
- `available`
- capability flags
- operator-provided traits like `speed`, `cost`, and `tags`
- best-effort pricing and per-job usage/cost reporting when available

3. Launch work:

```bash
cagent run --json --adapter codex --cwd /repo --prompt "Fix the failing tests."
```

`run` queues background work and returns immediately with a job id and session id.

4. Poll or inspect:

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

`send` also queues background work and returns a new job id immediately.

6. Ask the live source agent to land the plane when needed:

```bash
cagent debrief --json --session <session-id> --reason "prepare a recovery summary"
```

`debrief` queues a continuation job and writes a durable markdown artifact when it completes.

7. Transfer to another adapter only when native continuation is not possible:

```bash
cagent transfer export --json --job <job-id> --reason "anthropic outage" --mode recovery
cagent transfer run --json --transfer <transfer-id-or-path> --adapter codex --cwd /repo
```

`transfer run` should follow the same background-job contract as `run`.

## Important behavior

- `cagent` does not perform vendor auth flows.
- `cagent` preserves native session IDs and raw vendor output.
- `runtime --json` is the preferred machine-facing inventory command.
- `catalog show --json` is the preferred machine-facing provider/model inventory command.
- Use `status`, `logs --follow`, `session`, and `cancel` as the control surface after launch.
- Use `status --wait` when the host wants a blocking wait without writing its own polling loop.
- Use `status --json` when routing or debugging based on token usage or cost.
- Use `artifacts list/show` to inspect transfer and debrief outputs directly.
- Use `debrief` when you want the current agent to summarize itself before recovery or debugging.
- Treat `transfer` as explicit failover/recovery, not as the normal orchestration path.
- The transfer prompt should explicitly disclose the source adapter and reason for transfer.
