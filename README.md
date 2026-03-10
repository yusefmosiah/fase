# cagent

`cagent` is a local control-plane CLI for coding-agent CLIs.

It gives you one machine-readable interface for:
- starting jobs on vendor CLIs,
- persisting canonical jobs, sessions, turns, events, and artifacts,
- continuing same-vendor sessions when supported,
- asking a live session to produce a model-authored debrief,
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
- A provider/model catalog is available, including auth mode, billing class, and best-effort pricing when known.
- Background execution, real process cancellation, live log follow, and filtered job/session listing are in place.
- `transfer` is the explicit failover/recovery path when native continuation is not possible.
- `debrief` is available for model-authored "land the plane" exports on still-live sessions.
- `status` now exposes normalized token usage and vendor-reported or estimated cost when enough signal exists.

## Intended Use

`cagent` is meant to be used as a bash-callable background subagent for existing coding agents.

That means:
- the primary public surface is the CLI plus `--json`,
- `run`, `send`, `debrief`, and `transfer run` queue background jobs and return IDs immediately,
- host agents should be able to inspect runtime availability before choosing an adapter,
- adapter-specific auth remains owned by each vendor CLI,
- `cagent` keeps durable session state and raw artifacts instead of trying to act like a janitor or hosted control plane.

The intended orchestration model is:
- `run` starts a fresh background subagent.
- `send` continues a native session on the same adapter.
- `debrief` asks a still-live agent to export its current world model as a durable artifact.
- `transfer` is for explicit failover or provider switching when native continuation is not possible.

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
- `cagent debrief`
- `cagent artifacts list`
- `cagent artifacts show`
- `cagent cancel`
- `cagent list`
- `cagent session`
- `cagent transfer export`
- `cagent transfer run`
- `cagent adapters`
- `cagent catalog sync`
- `cagent catalog show`
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
- staged orchestration E2E tests, including recursive `cagent` workflows
- env-gated live low-cost smoke tests
- live smoke tests already exercised against real installed CLIs

## Not Yet Implemented

Important gaps versus the spec:
- the adapter contract does not yet include explicit `Cancel` or `ExportNativeSession` methods from the spec
- `tool.result`, approval, checkpoint, and richer structured event coverage are still incomplete for some vendors
- transfer bundle ergonomics can still improve, especially richer evidence references into native session state when available

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
./bin/cagent status --wait <job-id>
./bin/cagent logs --follow <job-id>
./bin/cagent artifacts list --job <job-id>
./bin/cagent adapters --json
./bin/cagent catalog sync --json
./bin/cagent catalog show --json
./bin/cagent runtime --json
```

## Catalog

`catalog` is the host-agent-facing discovery layer for provider/model inventory.

Use:
- `runtime` to answer "what adapter CLIs are installed and runnable?"
- `catalog` to answer "what providers, models, and auth modes are available through them?"

Current first-pass behavior:
- OpenCode, Pi, and Factory enumerate models from local CLI surfaces.
- Claude and Codex primarily report auth mode plus selected/provider context.
- Gemini currently reports auth mode conservatively from local environment and config signals.
- Pricing is best-effort, provenance-carrying, and not authoritative; auth mode and billing class remain the primary routing signals.

## Usage And Cost Reporting

`status --json` exposes normalized job-level usage and cost data when the adapter emits enough signal.

Rules:
- Prefer vendor-reported cost when present.
- Otherwise estimate cost only when provider, model, and pricing are known.
- Treat estimated cost as a routing/debugging hint, not a billing ledger.

The staged release matrix for contract, fake, live low-cost, recovery, and recursive orchestration lanes is documented in [docs/release-test-matrix.md](/Users/wiz/cagent/docs/release-test-matrix.md).

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
- debugging or recovery when the source agent is still reachable: use `debrief`
- failover/provider outage/model switch: use `transfer`

`transfer` should be treated as:
- a clearly labeled context transfer, not a native continuation
- host-authored metadata plus compact inline briefing
- evidence pointers to local files instead of replaying the full transcript inline

`debrief` should be treated as:
- an explicit debugging/recovery workflow, not normal orchestration
- a same-vendor continuation against the live source session
- a durable markdown artifact that captures the agent's self-reported world model

## Next Recommended Work

The highest-value remaining steps are:
1. Expand the low-cost live matrix beyond smoke tests into full multi-agent workflows.
2. Export richer transfer bundles with stronger evidence references into native session state when available.
3. Improve event translation depth.
4. Add richer catalog coverage for vendors with weaker model-enumeration surfaces.
