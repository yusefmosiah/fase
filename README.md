# cagent

`cagent` is a local control-plane CLI for coding-agent CLIs.

It gives you one machine-readable interface for:
- starting jobs on vendor CLIs,
- persisting canonical jobs, sessions, turns, events, and artifacts,
- continuing same-vendor sessions when supported,
- exporting explicit cross-vendor transfers,
- launching new jobs from those transfers.

The implementation is driven by [cagent-spec-and-implementation-guide.md](/Users/wiz/cagent/cagent-spec-and-implementation-guide.md).

## Status

Current repo status:
- Milestones 0 through 4 are implemented.
- Milestone 5 is partial.
- The codebase is production-shaped for local use, but not yet feature-complete against the full spec.

Practical summary:
- Core runtime, SQLite persistence, canonical schemas, session inspection, transfers, and event translation are in place.
- Codex, Claude, Factory, Pi, Gemini, and OpenCode adapters all exist.
- A host-agent-facing `runtime` inventory command is available.
- Background execution, real process cancellation, live log follow, and filtered job/session listing are in place.
- `transfer` is the explicit failover/recovery path when native continuation is not possible.

## Intended Use

`cagent` is meant to be used as a bash-callable background subagent for existing coding agents.

That means:
- the primary public surface is the CLI plus `--json`,
- `run`, `send`, and `transfer run` queue background jobs and return IDs immediately,
- host agents should be able to inspect runtime availability before choosing an adapter,
- adapter-specific auth remains owned by each vendor CLI,
- `cagent` keeps durable session state and raw artifacts instead of trying to act like a janitor or hosted control plane.

The intended orchestration model is:
- `run` starts a fresh background subagent.
- `send` continues a native session on the same adapter.
- `transfer` is for explicit failover or provider switching when native continuation is not possible.
- `debrief` is a future debugging/recovery feature for asking a live agent to summarize its own world model.

## Spec Coverage

Reasonable headline: about 80-85% of the written spec is implemented.

That estimate is based on feature surface, not line count:
- Core data model: mostly implemented
- Core command surface: mostly implemented
- Adapter coverage: implemented for all adapters named in the current spec
- Live E2E validation: implemented for major paths
- Operational polish and lifecycle completeness: still incomplete

More concrete milestone view:

| Spec milestone | Status | Notes |
| --- | --- | --- |
| Milestone 0 | Complete | Repo, module, CLI scaffold, runtime paths, store bootstrap |
| Milestone 1 | Complete | `run`, `status`, `logs`, `list`, `cancel` shell, canonical job/session/event model |
| Milestone 2 | Complete | Codex and Claude adapters, fake CLIs, golden translation tests |
| Milestone 3 | Complete | Factory and Pi adapters, `send`, canonical session inspection |
| Milestone 4 | Complete | transfer export/run, Gemini adapter |
| Milestone 5 | Partial | OpenCode experimental adapter is implemented; `runtime` now covers the host-agent inventory role, but lifecycle polish is still missing |

## Implemented

Commands currently wired:
- `cagent run`
- `cagent status`
- `cagent logs`
- `cagent send`
- `cagent cancel`
- `cagent list`
- `cagent session`
- `cagent transfer export`
- `cagent transfer run`
- `cagent adapters`
- `cagent runtime`
- `cagent version`

Canonical persistence currently wired:
- sessions
- jobs
- turns
- events
- native session links
- artifacts
- transfer packets
- raw stdout/stderr/native payload artifacts

Adapters currently implemented:

| Adapter | Run | Send / resume | Transfer target | Notes |
| --- | --- | --- | --- | --- |
| Codex | Yes | Yes | Yes | Live tested |
| Claude | Yes | Yes | Yes | Live tested |
| Factory | Yes | No | Yes | Resume intentionally not claimed |
| Pi | Yes | Yes | Yes | Uses session-file metadata |
| Gemini | Yes | No | Yes | Run-only for now |
| OpenCode | Yes | Yes | Yes | Experimental, live tested |

Testing currently in repo:
- unit tests for core/store/service paths
- fixture-based translation tests
- fake CLI integration tests for all implemented adapters
- live smoke tests already exercised against real installed CLIs

## Not Yet Implemented

Important gaps versus the spec:
- the adapter contract does not yet include explicit `Cancel` or `ExportNativeSession` methods from the spec
- richer host-agent wait ergonomics such as `status --wait` are not implemented yet
- `tool.result`, approval, checkpoint, and richer structured event coverage are still incomplete for some vendors
- transfer bundle ergonomics can still improve, especially richer evidence references into native session state when available
- model-authored `debrief` does not exist yet

## Repository Layout

Main code locations:
- [cmd/cagent/main.go](/Users/wiz/cagent/cmd/cagent/main.go)
- [internal/cli/root.go](/Users/wiz/cagent/internal/cli/root.go)
- [internal/service/service.go](/Users/wiz/cagent/internal/service/service.go)
- [internal/store/store.go](/Users/wiz/cagent/internal/store/store.go)
- [internal/events/translate.go](/Users/wiz/cagent/internal/events/translate.go)
- [internal/handoff/render.go](/Users/wiz/cagent/internal/handoff/render.go)
- [internal/adapters](/Users/wiz/cagent/internal/adapters)

Fixtures and adapter test assets:
- [testdata/fixtures](/Users/wiz/cagent/testdata/fixtures)
- [testdata/golden](/Users/wiz/cagent/testdata/golden)
- [testdata/fake_clis](/Users/wiz/cagent/testdata/fake_clis)

## Build And Test

Requirements:
- Go 1.26.x
- vendor CLIs installed if you want live adapter runs

Build:

```bash
make build
```

Test:

```bash
make test
make lint
```

Run:

```bash
./bin/cagent run --adapter codex --cwd . --prompt "Reply with exactly OK."
./bin/cagent status <job-id>
./bin/cagent logs --follow <job-id>
./bin/cagent adapters --json
./bin/cagent runtime --json
```

## Configuration

Config is loaded from the default XDG-style path or `--config`.

Environment overrides:
- `CAGENT_CONFIG_DIR`
- `CAGENT_STATE_DIR`
- `CAGENT_CACHE_DIR`

Current default adapter config is defined in [internal/core/config.go](/Users/wiz/cagent/internal/core/config.go).

Example adapter traits for host-agent orchestration:

```toml
[adapters.codex]
binary = "codex"
enabled = true
summary = "primary code-editing adapter"
speed = "fast"
cost = "high"
tags = ["default", "tools"]
```

## Transfer Model

Normal orchestration should not rely on explicit transfers. The host agent is the orchestrator.

Use cases:
- normal flow: spawn a fresh subagent with `run`
- same-vendor follow-up: use `send`
- failover/provider outage/model switch: use `transfer`
- debugging or recovery when the source agent is still reachable: use `debrief` later

`transfer` should be treated as:
- a clearly labeled context transfer, not a native continuation
- host-authored metadata plus compact inline briefing
- evidence pointers to local files instead of replaying the full transcript inline

## Next Recommended Work

The highest-value remaining steps are:
1. Export richer transfer bundles with stronger evidence references into native session state when available.
2. Add a future `debrief` path for model-authored self-summary.
3. Improve event translation depth.
4. Add small orchestration ergonomics like `status --wait`.
