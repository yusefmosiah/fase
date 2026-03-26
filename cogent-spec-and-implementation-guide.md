# Cogent Spec And Implementation Guide

Date: 2026-03-09
Kind: Spec + implementation guide
Status: Draft
Requires: []

## Narrative Summary (1-minute read)

`cogent` is a standalone open source CLI for running external coding-agent CLIs
as durable background jobs behind one local machine-readable contract.

It is designed to be called from `bash` by:
- humans,
- other coding agents,
- automation systems,
- shell scripts,
- higher-level runtimes that want to outsource software work to vendor CLIs.

`cogent` is not itself a coding agent and it is not a general distributed
workflow platform. It is a portable local control plane over coding-agent CLIs.

The critical semantic rule is:
- same-vendor continuation is `resume`
- cross-vendor failover is `transfer`
- model-authored self-summary is `debrief`

The critical inventory rule is:
- `runtime` reports local executable and adapter readiness
- `catalog` reports discovered providers, models, and auth/billing mode
- subscription-backed CLIs and usage-priced API credentials must not be conflated

`cogent` must preserve native vendor identities and raw artifacts, while also
normalizing sessions, jobs, turns, and events into one canonical local model.

When vendor streams expose token usage or cost, `cogent` should normalize and
persist that too. When cost is not vendor-reported, any estimate must be tied to
provider/model pricing provenance and clearly labeled as best-effort.

The implementation language should be Go.

## What Changed

1. Named the project `cogent`.
2. Declared the product as a standalone general-purpose CLI, not an embedded
   host-specific subsystem.
3. Declared the CLI contract and JSON output as the primary public API.
4. Declared same-vendor `resume` versus cross-vendor `transfer` as a hard rule.
5. Defined canonical job, session, turn, transfer, and event schemas.
6. Defined the adapter contract and runtime model.
7. Included implementation guidance for these adapters:
   - Codex
   - Claude Code
   - Factory Droid
   - Pi
   - Gemini CLI
   - OpenCode
8. Added implementation order, testing strategy, and packaging guidance.
9. Added a staged low-cost test matrix and a future provider/model catalog.
10. Added normalized token-usage and best-effort cost-reporting guidance.

## What To Do Next

1. Create a dedicated `cogent` repository.
2. Implement the core runtime, SQLite store, and CLI shell.
3. Implement Tier 1 adapters first:
   - Codex
   - Claude Code
   - Factory Droid
   - Pi
4. Implement fake adapter fixtures and golden event translation tests.
5. Implement Tier 2 adapters:
   - Gemini CLI
6. Implement the OpenCode adapter as experimental.
7. Publish a stable `--json` contract before adding daemon/server mode.
8. Add Nix packaging and reproducible integration tests.

## Product Statement

`cogent` is a local job runner and normalization layer for coding-agent CLIs.

It must let callers do the following through one binary:
- start work on a chosen adapter,
- inspect status,
- stream or read logs,
- continue same-vendor sessions,
- export cross-vendor transfers,
- launch new work from those transfers,
- inspect capabilities and health,
- inspect discovered providers, models, and auth mode,
- preserve all raw vendor data needed for debugging.

`cogent` must not:
- erase vendor-specific behavior,
- pretend all CLIs share one native session model,
- require a daemon in v0,
- require host integration code to understand each vendor separately.

## Goals

- Provide one portable CLI for controlling coding-agent jobs.
- Make the CLI safe and pleasant to invoke from `bash`.
- Preserve native vendor session IDs, artifacts, and raw event streams.
- Normalize vendor behavior into stable local schemas.
- Support durable long-running background work.
- Support same-vendor continuation where a vendor supports it.
- Support explicit cross-vendor transfer for failover and recovery.
- Expose discovered provider/model inventory for host-agent routing.
- Distinguish subscription-backed CLI access from usage-priced API access.
- Support human-readable output and machine-readable `--json`.
- Be easy to package as a single installable binary.
- Be feasible for another Codex to implement end-to-end with minimal ambiguity.

## Non-Goals

- Not a replacement for vendor CLIs.
- Not a replacement for vendor SDKs.
- Not a cross-machine scheduler in v0.
- Not a remote SaaS service.
- Not a universal abstraction over all possible model-provider APIs.
- Not a semantic memory system in v0.
- Not a guarantee that every adapter supports native `resume`.

## Core Terms

### Adapter

A vendor-specific implementation that knows how to:
- discover the vendor CLI,
- diagnose auth and environment issues,
- start a run,
- continue a native session when supported,
- parse output into canonical events,
- export native session metadata,
- report capability flags.

### Job

A durable `cogent` execution record.
One job corresponds to one launched vendor run attempt.

### Canonical Session

A `cogent`-owned logical thread of work across one or more jobs.
It may have one or many native vendor sessions attached over time.

### Native Session

A vendor-owned session/thread/conversation identity.
Examples:
- Codex session ID
- Claude Code session ID
- Pi session file / session path
- Factory session ID

### Turn

One user or caller input plus the assistant work it triggered.
For some vendors, turns are explicit.
For others, they must be reconstructed.

### Transfer

A structured host-authored context packet for continuing work on a different
adapter or in a fresh native session.
Transfer is not resume.
Transfer must remain clearly labeled as context transfer, not native continuity.

### Debrief

A future model-authored "land the plane" export that asks a live source agent
to summarize its world model for debugging or recovery.
Debrief is optional and not required for transfer.

### Runtime

Local execution readiness and installed adapter inventory.
Examples:
- binary availability
- detected adapter version
- capability flags
- configured traits

### Catalog

A discovered inventory of providers, models, and auth/billing mode that a host
agent can use for routing.

Catalog is not the same as runtime.
Runtime answers "what CLIs are locally usable right now?"
Catalog answers "what models and access modes are available through those CLIs?"

At minimum, catalog entries must distinguish:
- subscription-backed access
- usage-priced API credentials
- unknown or mixed mode

## Hard Rules

1. Never call cross-vendor transfer `resume`.
2. Never drop the native session ID if one exists.
3. Never drop raw vendor stdout/stderr or raw event payloads.
4. Never permit concurrent sends into the same native session.
5. Every machine-facing command must support `--json`.
6. Every persisted event must be append-only.
7. Every canonical event must include `job_id`, `adapter`, `ts`, and `seq`.
8. Every adapter must declare capabilities explicitly.
9. If native `resume` semantics are not verified, mark them unsupported.
10. The first stable API is the CLI contract, not the internal Go packages.
11. Never collapse subscription access and API-key access into one generic "price".

## High-Level Architecture

```text
caller (human / bash / agent / CI)
  -> cogent CLI
    -> job service
      -> adapter manager
        -> vendor adapter
          -> vendor CLI process
    -> local store (sqlite + artifacts)
    -> canonical event stream
```

### Runtime Layers

1. CLI layer
   - argument parsing
   - output rendering
   - exit codes

2. Service layer
   - job lifecycle
   - session and transfer logic
   - store access
   - locks and cancellation

3. Adapter layer
   - vendor command construction
   - environment diagnosis
   - event translation
   - native session discovery

4. Persistence layer
   - SQLite metadata
   - JSONL artifacts
   - raw stdout/stderr
   - transfer exports

## Implementation Language

Use Go.

Rationale:
- fast compile and test iteration,
- good subprocess handling,
- strong standard library for CLI, JSON, HTTP, and context cancellation,
- easy distribution as a single binary,
- good fit for orchestration glue rather than low-level systems code.

Recommended stack:
- CLI: `cobra` or `kong`
- config: `koanf` or small custom TOML loader
- DB: `modernc.org/sqlite` first, switch only if needed
- logging: `log/slog`
- tests: standard `testing` plus golden-file fixtures

## Repository Layout

```text
cogent/
  cmd/cogent/
  internal/cli/
  internal/core/
  internal/store/
  internal/events/
  internal/transfer/
  internal/adapters/
    codex/
    claude/
    factory/
    pi/
    gemini/
    opencode/
  testdata/
    fixtures/
    golden/
    fake_clis/
  docs/
  schemas/
  nix/
```

## Filesystem Layout

Default config and state:

```text
$XDG_CONFIG_HOME/cogent/config.toml
$XDG_STATE_HOME/cogent/cogent.db
$XDG_STATE_HOME/cogent/jobs/
$XDG_STATE_HOME/cogent/raw/
$XDG_STATE_HOME/cogent/transfers/
$XDG_CACHE_HOME/cogent/
```

Fallbacks:
- macOS/Linux fallback to `~/.config`, `~/.local/state`, `~/.cache`
- allow override via:
  - `COGENT_CONFIG_DIR`
  - `COGENT_STATE_DIR`
  - `COGENT_CACHE_DIR`

## Command Surface

Every command must support:
- human-readable output by default
- `--json` for machine-readable output

### Core Commands

```text
cogent run
cogent status
cogent logs
cogent send
cogent debrief
cogent artifacts list
cogent artifacts show
cogent cancel
cogent list
cogent session
cogent history search
cogent catalog sync
cogent catalog show
cogent catalog probe
cogent transfer export
cogent transfer run
cogent adapters
```

### `cogent run`

Starts a new job.

Required:
- `--adapter`
- `--cwd`
- one of:
  - `--prompt`
  - `--prompt-file`
  - `--stdin`

Optional:
- `--label`
- `--model`
- `--profile`
- `--config`
- `--env-file`
- `--artifact-dir`
- `--session`
- `--json`

Behavior:
- create canonical session if none supplied
- create job row
- queue a background worker
- capture raw stdout/stderr
- translate canonical events
- return immediately with job and session ids

### `cogent status`

Returns the latest job state and summary.

Flags:
- `--wait`
- `--interval`
- `--timeout`

### `cogent logs`

Streams canonical events or raw output.

Flags:
- `--follow`
- `--raw`
- `--json`

### `cogent send`

Sends follow-up input to a native resumable session.

Rules:
- only valid if adapter declares `native_resume=true`
- must acquire a session lock
- must fail clearly if a run is already active on that session

### `cogent debrief`

Queues a same-vendor continuation that asks the live source agent to produce a
structured "land the plane" report.

Rules:
- only valid if adapter declares `native_resume=true`
- must remain optional and separate from `transfer`
- must write a durable debrief artifact on success
- must return immediately with the queued job id and planned artifact path

### `cogent cancel`

Cancels a running job.

Default behavior:
- send SIGINT
- wait grace period
- send SIGTERM
- then SIGKILL if required
- persist canonical cancellation events

### `cogent list`

Lists jobs or sessions with filters.

### `cogent session`

Shows canonical session state, linked native sessions, recent turns, and
available continuation actions.

### `cogent history search`

Searches canonical local `cogent` history across jobs, turns, events, and
persisted artifacts.

Rules:
- must work without any adapter-specific importer
- must treat canonical `cogent` history as the default/general case
- should support adapter, model, cwd, session, and record-kind filters
- should search artifact content for small text artifacts like debriefs and transfers
- should return machine-readable matches with snippets and stable ids
- adapter-native history import is a separate special case for sessions not created by `cogent`

### `cogent artifacts list`

Lists persisted artifacts for a job or session.

Rules:
- must support filtering by artifact kind
- must support machine-readable output

### `cogent artifacts show`

Returns artifact metadata and content for one persisted artifact.

### `cogent catalog sync`

Discovers provider, model, and auth-mode information from installed adapters.

Rules:
- may use adapter-specific CLI inspection, config inspection, or environment inspection
- must record provenance and refresh timestamp
- must not require live web scraping in v0
- may leave pricing unknown

### `cogent catalog show`

Returns the current discovered provider/model catalog.

Rules:
- must clearly distinguish runtime readiness from catalog availability
- must include auth mode per entry when known
- should include recent canonical job history so host agents can prefer recently successful models
- should include pricing metadata only when provenance is clear

### `cogent catalog probe`

Runs best-effort entitlement probes against discovered catalog entries and records
whether they appear runnable for the current local account/configuration.

Rules:
- must be explicit and operator-invoked, not part of normal `catalog sync`
- must support filtering by adapter, provider, and model
- must persist probe timestamp and last probe outcome on the catalog entry
- must distinguish at minimum `runnable`, `unsupported_by_plan`, `failed`, and `hung_or_unstable`
- must not treat discovered inventory as implied entitlement

### `cogent transfer export`

Exports a structured transfer packet.

### `cogent transfer run`

Starts a new job from a transfer packet on a target adapter.

### `cogent adapters`

Lists installed/available adapters and their capability flags.

## Exit Codes

```text
0 success
1 generic runtime error
2 invalid invocation
3 adapter not available
4 auth/config missing
5 unsupported operation
6 job not found
7 session locked
8 vendor process failed
9 timeout
10 schema/translation error
```

## Canonical Job State Machine

```text
created
queued
starting
running
waiting_input
completed
failed
cancelled
blocked
```

Rules:
- `waiting_input` means the adapter supports more input and the current run is
  not active.
- `blocked` means the adapter surfaced a structured block or approval dead-end.

## Canonical Event Schema

Each event must contain:

```json
{
  "event_id": "evt_01...",
  "seq": 42,
  "ts": "2026-03-09T12:34:56Z",
  "job_id": "job_01...",
  "session_id": "ses_01...",
  "adapter": "codex",
  "kind": "tool.result",
  "phase": "execution",
  "native_session_id": "vendor-native-id-or-null",
  "correlation_id": "optional",
  "payload": {},
  "raw_ref": "raw/stdout/job_01.../00042.jsonl"
}
```

Canonical `kind` values:
- `job.created`
- `job.started`
- `job.state_changed`
- `job.completed`
- `job.failed`
- `job.cancelled`
- `process.spawned`
- `process.stdout`
- `process.stderr`
- `assistant.delta`
- `assistant.message`
- `user.message`
- `tool.call`
- `tool.result`
- `tool.error`
- `approval.requested`
- `approval.resolved`
- `checkpoint.created`
- `session.discovered`
- `session.resumed`
- `transfer.exported`
- `transfer.started`
- `diagnostic`

Rules:
- `payload` is adapter-specific but must still be valid JSON.
- `raw_ref` points to a persisted raw artifact, not the event payload itself.
- canonical parsers may emit both high-level events and raw passthrough events.

## Canonical Session Schema

```json
{
  "session_id": "ses_01...",
  "label": "optional human label",
  "created_at": "2026-03-09T12:00:00Z",
  "updated_at": "2026-03-09T12:34:56Z",
  "status": "active",
  "origin_adapter": "codex",
  "origin_job_id": "job_01...",
  "cwd": "/absolute/path",
  "native_sessions": [
    {
      "adapter": "codex",
      "native_session_id": "1234-5678",
      "resumable": true
    }
  ],
  "parent_session_id": null,
  "forked_from_turn_id": null,
  "latest_job_id": "job_01...",
  "latest_turn_id": "turn_01...",
  "tags": [],
  "metadata": {}
}
```

Important rule:
- canonical session identity is local to `cogent`
- native session identity remains vendor-owned

## Canonical Turn Schema

```json
{
  "turn_id": "turn_01...",
  "session_id": "ses_01...",
  "job_id": "job_01...",
  "adapter": "claude",
  "started_at": "2026-03-09T12:01:00Z",
  "completed_at": "2026-03-09T12:05:00Z",
  "input_text": "Continue by fixing tests",
  "input_source": "prompt|prompt_file|stdin|transfer",
  "result_summary": "brief summary",
  "status": "completed",
  "native_session_id": "vendor-native-id-or-null",
  "stats": {
    "tool_calls": 3,
    "assistant_messages": 2
  }
}
```

## Transfer Packet Schema

Cross-vendor failover must use this concept, not native resume.

```json
{
  "transfer_id": "xfer_01...",
  "exported_at": "2026-03-09T12:40:00Z",
  "mode": "recovery|operator_override|cost|capability|manual",
  "reason": "anthropic outage during a long-running session",
  "disclaimer": "This is a context transfer, not native session continuation.",
  "source": {
    "adapter": "codex",
    "model": "gpt-5-codex",
    "job_id": "job_01...",
    "session_id": "ses_01...",
    "native_session_id": "native-123",
    "cwd": "/abs/path/repo"
  },
  "objective": "Build the app and fix remaining tests",
  "summary": "Compact host-authored working brief",
  "unresolved": [
    "Failing tests in foo/bar",
    "Need review of migration path"
  ],
  "recent_turns_inline": [],
  "recent_events_inline": [],
  "important_files": [
    "/abs/path/src/main.go",
    "/abs/path/pkg/store/store.go"
  ],
  "evidence_refs": [
    {
      "kind": "recent_turns_json",
      "path": "/abs/path/.local/state/cogent/transfers/xfer_01/recent_turns.json"
    },
    {
      "kind": "recent_events_jsonl",
      "path": "/abs/path/.local/state/cogent/transfers/xfer_01/recent_events.jsonl"
    },
    {
      "kind": "session_export",
      "path": "/abs/path/.local/state/vendor/session.jsonl"
    }
  ],
  "artifacts": [],
  "constraints": [
    "Do not rewrite auth model",
    "Keep CLI flags backward compatible"
  ],
  "recommended_next_steps": [
    "Run tests",
    "Patch failing files",
    "Summarize diff"
  ]
}
```

## Adapter Capability Matrix

Every adapter must declare:

```json
{
  "adapter": "codex",
  "version": "detected version or null",
  "available": true,
  "capabilities": {
    "headless_run": true,
    "stream_json": true,
    "native_resume": true,
    "native_fork": true,
    "structured_output": true,
    "interactive_mode": true,
    "rpc_mode": true,
    "mcp": true,
    "checkpointing": false,
    "session_export": true
  }
}
```

Capability names:
- `headless_run`
- `stream_json`
- `native_resume`
- `native_fork`
- `structured_output`
- `interactive_mode`
- `rpc_mode`
- `mcp`
- `checkpointing`
- `session_export`

## Go Adapter Contract

```go
type Adapter interface {
    Name() string
    Detect(ctx context.Context) (Diagnosis, error)
    Capabilities() Capabilities
    StartRun(ctx context.Context, req StartRunRequest) (RunHandle, error)
    ContinueRun(ctx context.Context, req ContinueRunRequest) (RunHandle, error)
    Cancel(ctx context.Context, running RunningJob) error
    ExportNativeSession(ctx context.Context, job JobRecord) (*NativeSessionExport, error)
}
```

`RunHandle` should provide:
- process metadata
- raw stdout reader
- raw stderr reader
- optional native event reader
- optional native session discovery hook

The adapter must not:
- write directly to SQLite
- decide canonical IDs
- decide transfer semantics
- render human CLI output

Those belong to the service layer.

## Persistence Model

SQLite tables:
- `sessions`
- `jobs`
- `turns`
- `events`
- `native_sessions`
- `transfers`
- `artifacts`
- `locks`
- `catalog_snapshots`

Raw artifact directories:
- `raw/stdout/<job_id>/`
- `raw/stderr/<job_id>/`
- `raw/native/<job_id>/`

Use append-only inserts for events.

## Locking Rules

One active turn per native session.

Lock key:
- adapter name
- native session ID

Failure mode:
- return exit code `7`
- emit `diagnostic` event explaining lock conflict

## Catalog Discovery Model

Catalog entries should look roughly like:

```json
{
  "adapter": "opencode",
  "provider": "openrouter",
  "model": "glm-5",
  "access_mode": "subscription|api_key|unknown|mixed",
  "source": "cli|config|env|manual",
  "available": true,
  "selected": true,
  "pricing": {
    "kind": "token|subscription|unknown",
    "input_per_million": null,
    "output_per_million": null,
    "currency": "USD",
    "provenance": "manual",
    "observed_at": "2026-03-09T12:00:00Z"
  }
}
```

Rules:
- `access_mode=subscription` means the operator is consuming an included-plan workflow through a vendor CLI.
- `access_mode=api_key` means usage is tied to metered API credentials.
- `pricing.kind=unknown` is acceptable and preferable to guessed prices.
- token-based pricing only needs to be precise when the access mode is `api_key`.
- host agents should be able to route without pricing when only model identity and access mode are known.

v0 discovery sources:
- adapter CLI help and inspection commands
- adapter-specific config files
- environment variables
- optional operator-supplied overrides in `config.toml`

v0 non-goals:
- guaranteed complete provider/model enumeration for every CLI
- scraping vendor web pages at runtime
- auto-computing spend for subscription-backed plans

## Adapter Support Tiers

### Tier 1

Implement first:
- Codex
- Claude Code
- Factory Droid
- Pi

### Tier 2

Implement after Tier 1:
- Gemini CLI

### Tier 3

Implement as experimental:
- OpenCode

Reason:
- OpenCode's inspected repo is archived and points to Crush as the current
  successor, so adapter behavior should be treated as unstable.

## Adapter Specifications

### Codex Adapter

Status:
- Tier 1

Docs:
- https://developers.openai.com/codex/cli
- https://developers.openai.com/codex/noninteractive
- https://developers.openai.com/codex/app-server
- https://github.com/openai/codex

Native surfaces relevant to `cogent`:
- `codex exec --json`
- `codex exec resume --json`
- `codex resume`
- `codex app-server`
- `codex mcp-server`

v0 implementation choice:
- use `codex exec --json`
- use `codex exec resume --json` for continuation
- do not use app-server in v0

Rationale:
- CLI JSONL is simpler to ship first
- app-server can be added later as a transport optimization

Must support:
- `run`
- `send`
- session discovery
- native resume

Should support later:
- native fork
- app-server-backed transport

Notes:
- preserve Codex session/thread ID exactly as emitted or discovered
- preserve the last assistant message artifact when available
- map JSONL events to canonical events without losing raw detail

### Claude Code Adapter

Status:
- Tier 1

Docs:
- https://code.claude.com/docs/en/headless
- https://code.claude.com/docs/en/cli-reference
- https://code.claude.com/docs/en/sub-agents
- https://platform.claude.com/docs/en/agent-sdk/overview

Native surfaces relevant to `cogent`:
- `claude -p`
- `--output-format json`
- `--output-format stream-json`
- `--resume`
- `--continue`

v0 implementation choice:
- use headless CLI mode
- use `stream-json` when available for event translation
- use `--resume` or `--continue` for same-vendor continuation when a stable
  session identifier is available

Must support:
- `run`
- `send` when session identity is recoverable

Should support later:
- Agent SDK transport as a second backend

Notes:
- treat filesystem-based project config as vendor-local behavior, not canonical
  `cogent` configuration
- preserve Claude session ID if surfaced
- if session ID is not reliably recoverable in a given mode, mark
  `native_resume=false` for that submode

### Factory Droid Adapter

Status:
- Tier 1

Docs:
- https://docs.factory.ai/reference/cli-reference
- https://docs.factory.ai/cli/getting-started/overview

Native surfaces relevant to `cogent`:
- `droid`
- `droid exec`
- `-o json`
- `-o stream-json`
- `-o stream-jsonrpc`

v0 implementation choice:
- use `droid exec`
- prefer `stream-json` for canonical translation
- fall back to `json` for shorter runs if needed

Must support:
- `run`
- structured event translation

May support:
- `send`, if stable session continuation is verified in CLI docs or behavior

Rule:
- do not claim `native_resume` until it is tested against the current CLI and
  documented in adapter fixtures

### Pi Adapter

Status:
- Tier 1

Docs:
- https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent
- https://shittycodingagent.ai

Native surfaces relevant to `cogent`:
- interactive mode
- print / JSON mode
- RPC mode
- SDK mode
- session JSONL files with tree structure

v0 implementation choice:
- use CLI print / JSON mode for direct runs
- use session file inspection for native session linkage
- add RPC mode only after CLI mode is stable

Must support:
- `run`
- native session discovery
- session export

Should support:
- `send`
- branch-aware native session export

Notes:
- Pi session files are part of the public mental model; preserve file paths as
  native session metadata
- do not flatten Pi branch semantics into fake linear resume

### Gemini CLI Adapter

Status:
- Tier 2

Docs:
- https://github.com/google-gemini/gemini-cli
- https://geminicli.com/docs/

Observed surfaces from current README:
- `-p`
- `--output-format json`
- `--output-format stream-json`
- checkpointing
- MCP support

v0 implementation choice:
- use `gemini -p`
- use `--output-format stream-json`
- implement `run`
- store checkpoint metadata if surfaced

Rule:
- do not implement `send` until concrete same-session continuation flags are
  verified in the current public docs or through controlled adapter fixtures

### OpenCode Adapter

Status:
- Tier 3 experimental

Docs:
- https://github.com/opencode-ai/opencode

Important status note:
- the inspected OpenCode repository is archived and points to Crush as the
  ongoing successor

Observed surfaces from archived README:
- TUI-first
- session management
- SQLite persistence
- tool integration

Spec rule:
- include an adapter design, but do not promise parity with other Tier 1
  adapters

v0 implementation choice:
- one-shot CLI integration only if a stable non-interactive path is confirmed
- otherwise implement as unavailable

Rationale:
- `cogent` should document the adapter shape without pretending the archived
  upstream is stable enough for first-wave support

## Adapter Implementation Order

1. Codex
2. Claude Code
3. Factory Droid
4. Pi
5. Gemini CLI
6. OpenCode

## Runtime Behavior

### Job Launch

1. Validate flags.
2. Load config.
3. Resolve adapter.
4. Run `Detect`.
5. Create session and job rows.
6. Spawn process.
7. Persist `process.spawned`.
8. Stream raw output into artifact files.
9. Translate canonical events as lines arrive.
10. Finalize job state.

### Native Session Discovery

Discovery may happen:
- from a CLI-emitted event,
- from a parsed final JSON payload,
- from an output file,
- from session files on disk,
- from a follow-up vendor inspection call.

Once discovered:
- persist `session.discovered`
- link `native_sessions`
- expose it in `status` and `session`

### Continuation

`send` must:
- resolve canonical session
- resolve linked native session
- acquire native session lock
- spawn a new job attached to the same canonical session
- call the adapter continuation path

### Transfer

`transfer export` must:
- gather recent turns
- include important file references
- include current unresolved goals
- include relevant artifacts and diagnostics
- include explicit transfer metadata such as source adapter, source model if known, and reason
- avoid dumping massive raw transcript blobs inline by default
- prefer bundle files and path references when context is large

`transfer run` must:
- validate target adapter
- create new canonical session or forked child session
- store the transfer artifact
- render the transfer into a clearly labeled prompt packet appropriate for the target adapter
- disclose that this is context transfer, not native continuation

### Debrief

`debrief` must:
- ask a still-live source agent to summarize its own world model
- produce a structured artifact for debugging or recovery
- remain optional and separate from transfer

## Output Contract

Human-readable mode:
- concise summaries
- tables for list/status
- tail-like logs for `logs --follow`

Machine-readable mode:
- one JSON object for non-streaming commands
- newline-delimited JSON events for streaming commands

Examples:

```bash
cogent status job_01ABC --json
cogent logs job_01ABC --follow --json
cogent adapters --json
```

## Config File

Suggested `config.toml`:

```toml
[store]
state_dir = "~/.local/state/cogent"

[defaults]
json = false

[adapters.codex]
binary = "codex"
enabled = true

[adapters.claude]
binary = "claude"
enabled = true

[adapters.factory]
binary = "droid"
enabled = true

[adapters.pi]
binary = "pi"
enabled = true

[adapters.gemini]
binary = "gemini"
enabled = true

[adapters.opencode]
binary = "opencode"
enabled = false
```

## Testing Strategy

### Unit Tests

Cover:
- ID generation
- state transitions
- lock acquisition
- config parsing
- transfer rendering
- event translation helpers

### Fixture Tests

For each adapter:
- store real captured stdout/stderr or JSONL fixtures
- replay them through the parser
- assert canonical events with golden files

### Fake CLI Tests

Build fake vendor binaries under `testdata/fake_clis`.

Each fake CLI should simulate:
- normal success
- structured error
- partial stream
- session discovery
- continuation
- hang and cancellation

### Live Smoke Tests

Run only when explicitly enabled by environment variables.

Goals:
- verify installed vendor binaries
- verify auth presence
- verify a no-op or trivial prompt roundtrip

### Low-Cost Live Matrix

The default live test lane should use the cheapest practical model available on
each adapter.

Goals:
- maximize coverage without large spend
- validate model selection as a routing feature
- discover where weaker models are already sufficient
- reserve stronger models for sparse comparison runs

Suggested dimensions:
- adapter
- model tier
- task shape
- workflow shape
- failure mode

### Recursive `cogent` Tests

`cogent` must be tested as a subagent runtime that can be orchestrated by a
coding agent which itself has the `cogent` skill loaded.

Important scenario:
1. planner `cogent` writes or refines a spec
2. implementation `cogent` runs one phase of work
3. verifier `cogent` compiles, tests, and retries until green or blocked
4. review `cogent` performs code review
5. red-team `cogent` performs adversarial and security testing
6. report `cogent` emits a release or security summary

This should first be exercised on low-cost models, then selectively compared
against stronger models.

### Learning-Oriented Test Lanes

The goal is not only regression prevention, but rapid discovery of latent
capabilities.

Examples:
- whether low-cost models can produce useful debriefs
- whether transfer bundles let a weaker target model recover stronger-source work
- whether recursive `cogent` orchestration stabilizes into a useful workflow
- which adapters are best for planning, coding, verification, review, or red-team tasks

### Compatibility Gates

Each adapter must ship with:
- capability expectations,
- known limitations,
- version detection behavior,
- at least one live smoke test recipe.

## Implementation Milestones

### Milestone 0

- create repo
- scaffold CLI
- scaffold SQLite store
- implement IDs, config, and state dirs

### Milestone 1

- canonical schemas
- `run`, `status`, `logs`, `cancel`, `list`
- raw artifact persistence

### Milestone 2

- Codex adapter
- Claude adapter
- fake CLI framework
- golden parser tests

### Milestone 3

- Factory adapter
- Pi adapter
- `send`
- canonical session inspection

### Milestone 4

- transfer export
- transfer run
- Gemini adapter

### Milestone 5

- OpenCode experimental adapter
- packaging and release automation

### Milestone 6

- provider/model catalog
- auth-mode detection
- low-cost live matrix
- recursive `cogent` orchestration tests

## Known Risks

- vendor CLIs change output formats
- vendor CLIs may expose only partial session identity in some modes
- archived or unstable upstreams may break adapter assumptions
- auth and subscription modes vary widely across vendors
- TUI-first CLIs may not have clean headless continuation paths
- model catalogs may be incomplete or only partially discoverable
- pricing may be missing or ambiguous for subscription-backed access

Mitigation:
- capability flags
- conservative support claims
- raw artifact preservation
- fixture-based parsers
- live smoke tests against real installed CLIs

## Documentation Links

### Codex

- https://developers.openai.com/codex/cli
- https://developers.openai.com/codex/noninteractive
- https://developers.openai.com/codex/app-server
- https://github.com/openai/codex

### Claude Code

- https://code.claude.com/docs/en/headless
- https://code.claude.com/docs/en/cli-reference
- https://code.claude.com/docs/en/sub-agents
- https://platform.claude.com/docs/en/agent-sdk/overview

### Factory Droid

- https://docs.factory.ai/reference/cli-reference
- https://docs.factory.ai/cli/getting-started/overview

### Pi

- https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent
- https://shittycodingagent.ai

### Gemini CLI

- https://github.com/google-gemini/gemini-cli
- https://geminicli.com/docs/

### OpenCode

- https://github.com/opencode-ai/opencode

## Final Implementation Guidance For The Next Codex

Build the smallest correct slice first.

Do this in order:
1. implement canonical schemas and the SQLite store
2. implement raw artifact capture
3. implement `run`, `status`, `logs`, `cancel`, `list`
4. implement Codex
5. implement Claude Code
6. freeze JSON contracts with golden tests
7. implement `send`
8. implement transfers
9. add the remaining adapters in the declared order
10. implement catalog discovery and low-cost live orchestration tests

Do not:
- build daemon mode first
- build remote orchestration first
- claim unsupported resume behavior
- compress away raw vendor output
- couple the product to any one host runtime

If a vendor surface is unclear:
- preserve raw output,
- mark the capability unsupported,
- ship the adapter in conservative mode first,
- add stronger support only after live fixtures prove it.
