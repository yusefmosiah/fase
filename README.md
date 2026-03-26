# Cogent

`cogent` is the command-line entrypoint for Cogent, the Fully Automated Software Engineering local work-control plane for governed agent work.

It gives you:
- a **work graph** (SQLite) that tracks work items, dependency edges, attestations, approvals, and promotions,
- a **canonical lifecycle** with one state machine: `ready → claimed → in_progress → checking → blocked → done / failed / cancelled / archived`,
- a **command vocabulary** for mutating work state: create, claim, release, block, attest, approve, promote, hydrate,
- a **verification model** where checks produce evidence and blocking attestation policy gates completion,
- **6 adapter CLIs** (Codex, Claude, Factory, Pi, Gemini, OpenCode) for dispatching work to coding agents,
- **work hydration** that compiles deterministic briefings so any agent can pick up where any other left off,
- a **mind-graph UI** (Poincaré disk hyperbolic visualization) for observing causal relations in the work graph,
- a **unified usage/surface model** with normalized cost reporting across all adapters and machine-facing surfaces (CLI `--json`, HTTP API, MCP tools),
- durable session persistence, transfers, debriefs, and canonical history search.

The core invariant: **agents may always stop, the system may always resume.**

Attestation is the centerpiece. Work is not "done" because an agent says so — it's done when durable evidence from tests, scripts, agent reviewers, and human review satisfies the attestation policy. Docs are projections of the work graph, not independent stores.

### Contract Precedence

When runtime code, committed documentation, and persisted work-graph state disagree, **runtime code is the canonical source of truth**. This applies to:

- **Work execution states**: The authoritative state values are defined in `internal/core/types.go` (see `WorkExecutionState` constants). Historical or proposal docs that list different states are either updated to match or marked as superseded.
- **CLI/API contracts**: The runtime implementation defines the authoritative surface; docs are derivatives.
- **Attestation semantics**: The code's `AttestationRecord` structure and guard logic define the contract.

Workers should reference `cogent work update --execution-state` with the values from `internal/core/types.go` for valid states. Any doc that contradicts the code's state definitions should be treated as historical.

### Canonical Lifecycle

Every work item follows one state machine defined in `internal/core/types.go`:

| State | Meaning |
| --- | --- |
| `ready` | Available for assignment |
| `claimed` | Assigned to a worker, not yet started |
| `in_progress` | Worker is actively executing |
| `checking` | Evidence collection / verification in progress |
| `blocked` | Waiting on a dependency or external input |
| `done` | Completed and verified |
| `failed` | Terminal failure |
| `cancelled` | Explicitly cancelled |
| `archived` | Retained for history, no longer active |

The deprecated alias `awaiting_attestation` is normalized to `checking` on read and rejected on new writes.

### Verification Model

Checks are evidence, not approval. The pipeline:

1. **Evidence collection** — workers and automated checks produce `AttestationRecord` entries against a work item.
2. **Blocking attestation policy** — each work item declares `required_attestations` slots. Slots marked `blocking: true` must be satisfied before the item can transition to `done`.
3. **Attestation freeze** — once the attestation requirement set is frozen (`attestation_frozen_at`), no new required slots can be added. Escalations after freeze are recorded with explicit provenance (`escalated_at`, `escalation_by`, `escalation_reason`).
4. **Docs as verification** — repository docs are first-class verification evidence. `required_docs` on a work item declares which documents must exist and be current. Doc drift is a check failure, not a separate concern.
5. **Completion gating** — the supervisor/service layer enforces that all blocking attestation slots are satisfied before allowing `done`. The work graph, not the agent, owns completion authority.

### Usage and Surface Model

All machine-facing surfaces (CLI `--json`, HTTP API, MCP tools) expose the same normalized usage and cost contract:

- **Vendor-reported cost** is preferred when present.
- **Estimated cost** is derived only when provider, model, and pricing are all known.
- Estimated cost is a routing/debugging hint, not a billing ledger.
- Usage attribution tracks per-model token counts for multi-model jobs.

Key specs:
- [docs/cogent-v0-local-control-plane.md](docs/cogent-v0-local-control-plane.md) — product direction and v0 scope
- [docs/cogent-work-runtime.md](docs/cogent-work-runtime.md) — work runtime design
- [docs/cogent-work-api-and-schema.md](docs/cogent-work-api-and-schema.md) — work CLI/API schema
- [docs/cogent-worker-briefing-schema.md](docs/cogent-worker-briefing-schema.md) — hydration contract
- [schemas/worker-briefing.schema.json](schemas/worker-briefing.schema.json) — briefing JSON schema

## Status

The control plane has been hardened through a 6-milestone readiness mission (contract-freeze, supervisor-wake-causality, lifecycle-normalization, verification-unification, docs-as-verification, usage-and-surface-cleanup) with all 40 validation assertions passing.

Current repo status:
- Milestones 0 through 4 are implemented.
- Milestone 5 is partial.
- The readiness mission aligned contracts, lifecycle, verification, and surfaces across the codebase.

Practical summary:
- Core runtime, SQLite persistence, canonical schemas, session inspection, transfers, and event translation are in place.
- One canonical lifecycle state machine governs all work items (see Canonical Lifecycle above).
- Checks produce evidence; blocking attestation policy gates completion (see Verification Model above).
- Supervisor wake causality is hardened: no self-wake loops, trustworthy event provenance, `RequiresSupervisorAttention()` as the canonical wake gate.
- Codex, Claude, Factory, Pi, Gemini, and OpenCode adapters all exist.
- A host-agent-facing `runtime` inventory command is available.
- A provider/model catalog is available, including auth mode, billing class, and best-effort pricing when known.
- Background execution, real process cancellation, live log follow, and filtered job/session listing are in place.
- `transfer` is the explicit failover/recovery path when native continuation is not possible.
- `debrief` is available for model-authored "land the plane" exports on still-live sessions.
- `status` now exposes normalized token usage and vendor-reported or estimated cost when enough signal exists.
- All machine-facing surfaces (CLI `--json`, HTTP API, MCP tools) share one unified usage/cost contract.

## Intended Use

`cogent` is a local runtime for governed agent work. The operator experience:
- capture ideas and work from the terminal (`work create`, `inbox`)
- define or bootstrap work locally
- inspect the work graph via CLI or mind-graph UI
- dispatch work to agents (`run`, `send`)
- review attestation results
- approve or reject work based on evidence
- promote approved work

The primary surface is the CLI with `--json`. Jobs queue in the background and return IDs immediately. The work graph (not the agent) is the source of truth.

## Spec Coverage

Reasonable headline: about 85-90% of the written spec is implemented, with the control plane fully hardened.

That estimate is based on feature surface, not line count:
- Core data model: implemented
- Core command surface: implemented
- Adapter coverage: implemented for all adapters named in the current spec
- Lifecycle and verification: unified and aligned across all layers
- Live E2E validation: implemented for major paths
- Operational polish: supervisor causality and wake gating hardened

More concrete milestone view:

| Spec milestone | Status | Notes |
| --- | --- | --- |
| Milestone 0 | Complete | Repo, module, CLI scaffold, runtime paths, store bootstrap |
| Milestone 1 | Complete | `run`, `status`, `logs`, `list`, `cancel` shell, canonical job/session/event model |
| Milestone 2 | Complete | Codex and Claude adapters, fake CLIs, golden translation tests |
| Milestone 3 | Complete | Factory and Pi adapters, `send`, canonical session inspection |
| Milestone 4 | Complete | transfer export/run, Gemini adapter |
| Milestone 5 | Partial | OpenCode experimental adapter is implemented; `runtime` covers the host-agent inventory role |

Readiness mission milestones (all complete):

| Readiness milestone | Status | Notes |
| --- | --- | --- |
| contract-freeze | Complete | Canonical lifecycle, attestation, and docs-as-truth policies locked |
| supervisor-wake-causality | Complete | Self-wake loops eliminated, event provenance trustworthy |
| lifecycle-normalization | Complete | One state machine across CLI, service, store, supervisor, docs |
| verification-unification | Complete | Single check→attestation→completion pipeline |
| docs-as-verification | Complete | Docs are first-class verification evidence |
| usage-and-surface-cleanup | Complete | Unified usage/cost across CLI, HTTP, MCP surfaces |

## Implemented

Commands currently wired:
- `cogent run`
- `cogent serve`
- `cogent status`
- `cogent logs`
- `cogent send`
- `cogent debrief`
- `cogent artifacts list`
- `cogent artifacts show`
- `cogent history search`
- `cogent cancel`
- `cogent list`
- `cogent session`
- `cogent transfer export`
- `cogent transfer run`
- `cogent adapters`
- `cogent catalog sync`
- `cogent catalog show`
- `cogent catalog probe`
- `cogent runtime`
- `cogent version`

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
- staged orchestration E2E tests, including recursive `cogent` workflows
- env-gated live low-cost smoke tests
- live smoke tests already exercised against real installed CLIs

## Not Yet Implemented

Remaining gaps versus the spec:
- the adapter contract does not yet include explicit `Cancel` or `ExportNativeSession` methods from the spec
- `tool.result`, approval, checkpoint, and richer structured event coverage are still incomplete for some vendors
- transfer bundle ergonomics can still improve, especially richer evidence references into native session state when available
- `work hydrate` is not yet fully implemented (deterministic briefing compilation with debrief mode)

## Repository Layout

Main code locations:
- [cmd/cogent/main.go](cmd/cogent/main.go)
- [internal/cli/root.go](internal/cli/root.go)
- [internal/service/service.go](internal/service/service.go)
- [internal/store/store.go](internal/store/store.go)
- [internal/events/translate.go](internal/events/translate.go)
- [internal/transfer/render.go](internal/transfer/render.go)
- [internal/adapters](internal/adapters)

Fixtures and adapter test assets:
- [testdata/fixtures](testdata/fixtures)
- [testdata/golden](testdata/golden)
- [testdata/fake_clis](testdata/fake_clis)

## Build And Test

Requirements:
- Go 1.25+
- vendor CLIs installed if you want live adapter runs

Build:

```bash
make build        # produces build/cogent
make install      # installs to ~/.local/bin/cogent
```

Test:

```bash
make test         # go test ./internal/...
make lint         # go vet ./...
```

Serve (UI + API + supervisor):

```bash
cogent serve                        # UI + API + housekeeping on default port
cogent serve --auto                 # also auto-dispatch ready work
cogent serve --host 0.0.0.0        # accessible via Tailscale/LAN
cogent serve --no-browser           # don't open browser on start
```

Run:

```bash
cogent run --adapter codex --cwd . --prompt "Reply with exactly OK."
cogent status --wait <job-id>
cogent logs --follow <job-id>
cogent artifacts list --job <job-id>
cogent history search --query "provider outage" --adapter claude
cogent work create --title "Implement X" --objective "Do the work"
cogent work claim-next --claimant worker-a
cogent work release <work-id> --claimant worker-a
cogent adapters --json
cogent catalog sync --json
cogent catalog show --json
cogent catalog probe --json --adapter opencode --provider openai
cogent runtime --json
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

`status --json` exposes normalized job-level usage and cost data when the adapter emits enough signal. See the Usage and Surface Model section above for the contract details.

The staged release matrix for contract, fake, live low-cost, recovery, and recursive orchestration lanes is documented in [docs/release-test-matrix.md](docs/release-test-matrix.md).

## History

`history search` is the lightweight canonical search path over `cogent`'s own local store.

It searches existing jobs, turns, events, and persisted artifacts without requiring a separate index or importer.

Use it for:
- recalling prior work launched through `cogent`,
- finding debrief/transfer artifacts by content,
- mining recent canonical sessions before deciding whether adapter-native import is needed.

Adapter-native history import remains a future special case for sessions that were never created by `cogent`.

## Configuration

Config is loaded from the default XDG-style path or `--config`.

Environment overrides:
- `COGENT_CONFIG_DIR`
- `COGENT_STATE_DIR`
- `COGENT_CACHE_DIR`

Current default adapter config is defined in [internal/core/config.go](internal/core/config.go).

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
6. Implement adapter-level `Cancel` and `ExportNativeSession` per the spec.
