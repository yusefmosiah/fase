# FASE

`fase` is the command-line entrypoint for FASE, the Fully Automated Software Engineering local work-control plane for governed agent work.

It gives you:
- a **work graph** (SQLite) that tracks work items, dependency edges, attestations, approvals, and promotions,
- a **command vocabulary** for mutating work state: create, claim, release, block, attest, approve, promote, hydrate,
- **6 adapter CLIs** (Codex, Claude, Factory, Pi, Gemini, OpenCode) for dispatching work to coding agents,
- **work hydration** that compiles deterministic briefings so any agent can pick up where any other left off,
- a **mind-graph UI** (Poincaré disk hyperbolic visualization) for observing causal relations in the work graph,
- durable session persistence, transfers, debriefs, and canonical history search.

The core invariant: **agents may always stop, the system may always resume.**

Attestation is the centerpiece. Work is not "done" because an agent says so — it's done when durable evidence from tests, scripts, agent reviewers, and human review satisfies the attestation policy. Docs are projections of the work graph, not independent stores.

Key specs:
- [docs/fase-v0-local-control-plane.md](docs/fase-v0-local-control-plane.md) — product direction and v0 scope
- [docs/fase-work-runtime.md](docs/fase-work-runtime.md) — work runtime design
- [docs/fase-work-api-and-schema.md](docs/fase-work-api-and-schema.md) — work CLI/API schema
- [docs/fase-worker-briefing-schema.md](docs/fase-worker-briefing-schema.md) — hydration contract
- [schemas/worker-briefing.schema.json](schemas/worker-briefing.schema.json) — briefing JSON schema

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

`fase` is a local runtime for governed agent work. The operator experience:
- capture ideas and work from the terminal (`work create`, `inbox`)
- define or bootstrap work locally
- inspect the work graph via CLI or mind-graph UI
- dispatch work to agents (`run`, `send`)
- review attestation results
- approve or reject work based on evidence
- promote approved work

The primary surface is the CLI with `--json`. Jobs queue in the background and return IDs immediately. The work graph (not the agent) is the source of truth.

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
- `fase run`
- `fase status`
- `fase logs`
- `fase send`
- `fase debrief`
- `fase artifacts list`
- `fase artifacts show`
- `fase history search`
- `fase cancel`
- `fase list`
- `fase session`
- `fase transfer export`
- `fase transfer run`
- `fase adapters`
- `fase catalog sync`
- `fase catalog show`
- `fase catalog probe`
- `fase runtime`
- `fase version`

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
- staged orchestration E2E tests, including recursive `fase` workflows
- env-gated live low-cost smoke tests
- live smoke tests already exercised against real installed CLIs

## Not Yet Implemented

Important gaps versus the spec:
- the adapter contract does not yet include explicit `Cancel` or `ExportNativeSession` methods from the spec
- `tool.result`, approval, checkpoint, and richer structured event coverage are still incomplete for some vendors
- transfer bundle ergonomics can still improve, especially richer evidence references into native session state when available

## Repository Layout

Main code locations:
- [cmd/fase/main.go](/Users/wiz/cagent/cmd/fase/main.go)
- [internal/cli/root.go](/Users/wiz/cagent/internal/cli/root.go)
- [internal/service/service.go](/Users/wiz/cagent/internal/service/service.go)
- [internal/store/store.go](/Users/wiz/cagent/internal/store/store.go)
- [internal/events/translate.go](/Users/wiz/cagent/internal/events/translate.go)
- [internal/transfer/render.go](/Users/wiz/cagent/internal/transfer/render.go)
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
./bin/fase run --adapter codex --cwd . --prompt "Reply with exactly OK."
./bin/fase status --wait <job-id>
./bin/fase logs --follow <job-id>
./bin/fase artifacts list --job <job-id>
./bin/fase history search --query "provider outage" --adapter claude
./bin/fase work create --title "Implement X" --objective "Do the work"
./bin/fase work claim-next --claimant worker-a
./bin/fase work release <work-id> --claimant worker-a
./bin/fase adapters --json
./bin/fase catalog sync --json
./bin/fase catalog show --json
./bin/fase catalog probe --json --adapter opencode --provider openai
./bin/fase runtime --json
```

## Catalog

`catalog` is the host-agent-facing discovery layer for provider/model inventory and recent local usage history.

Use:
- `runtime` to answer "what adapter CLIs are installed and runnable?"
- `catalog` to answer "what providers, models, and auth modes are available through them?"
- `catalog probe` to answer "which discovered models are actually runnable for this local account/config right now?"

Current first-pass behavior:
- OpenCode, Pi, and Factory enumerate models from local CLI surfaces.
- Claude and Codex primarily report auth mode plus selected/provider context.
- Gemini currently reports auth mode conservatively from local environment and config signals.
- `catalog show` reports discovered inventory, while `catalog probe` adds best-effort entitlement status like `runnable`, `unsupported_by_plan`, and `hung_or_unstable`.
- `catalog show` also annotates entries with recent local canonical job history so routing can prefer models that actually worked recently.
- Pricing is best-effort, provenance-carrying, and not authoritative; auth mode and billing class remain the primary routing signals.

## Usage And Cost Reporting

`status --json` exposes normalized job-level usage and cost data when the adapter emits enough signal.

Rules:
- Prefer vendor-reported cost when present.
- Otherwise estimate cost only when provider, model, and pricing are known.
- Treat estimated cost as a routing/debugging hint, not a billing ledger.

The staged release matrix for contract, fake, live low-cost, recovery, and recursive orchestration lanes is documented in [docs/release-test-matrix.md](/Users/wiz/cagent/docs/release-test-matrix.md).

## History

`history search` is the lightweight canonical search path over `fase`'s own local store.

It searches existing jobs, turns, events, and persisted artifacts without requiring a separate index or importer.

Use it for:
- recalling prior work launched through `fase`,
- finding debrief/transfer artifacts by content,
- mining recent canonical sessions before deciding whether adapter-native import is needed.

Adapter-native history import remains a future special case for sessions that were never created by `fase`.

## Configuration

Config is loaded from the default XDG-style path or `--config`.

Environment overrides:
- `FASE_CONFIG_DIR`
- `FASE_STATE_DIR`
- `FASE_CACHE_DIR`

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

## Mind Graph UI

`mind-graph/hyperbolic-proto.html` is a Poincaré disk visualization of the work graph.

Features:
- Hyperbolic geometry: exponential compression at the periphery, everything visible at all zoom levels
- Force simulation: attention shells (urgency → center), parent-child springs, blocking tension, pairwise repulsion
- Möbius focus transforms: click to center a node, esc to ascend
- Text LOD: full label → abbreviated → initials → ink-stroke as nodes approach the boundary
- Loads live data from the Vite API (`npm run dev` in `mind-graph/`) or falls back to mock data

Run standalone:
```bash
open mind-graph/hyperbolic-proto.html
```

Run with live data:
```bash
cd mind-graph && npm run dev
# then open http://localhost:5173/hyperbolic-proto.html
```

## Next Recommended Work

1. Implement `work hydrate` fully — deterministic briefing compilation with debrief mode.
2. Add parent/child edges to existing work items (the graph is currently flat).
3. Build the mind-graph into a proper component with operator controls (lock, approve, attest).
4. Add blocking edge discovery and visualization from the work graph.
5. Expand live adapter testing beyond smoke tests into multi-agent workflows.
