Date: 2026-03-20
Kind: Protocol spec
Status: Draft
Priority: 1
Requires: [docs/cagent-work-runtime.md, docs/host-agents.md]
Owner: Runtime / Agent Adapters

## Summary

`cagent` standardizes a **live agent protocol** for multi-agent orchestration
across heterogeneous coding agent adapters.

The protocol is discovered through sequential implementation, not designed
up front. Each adapter teaches us what the right abstraction is. The spec
captures what we've verified, not what we hope will work.

Build order: Claude Code → Codex → Pi → OpenCode → native Go.

## 1) Design Position

`cagent` is not normalizing every possible coding-agent CLI.

It is defining a live-session orchestration standard for adapters that can
support real-time cooperative work. The protocol emerges from concrete
adapter integrations, not from abstract specification.

Principles:
- no degraded fallback mode,
- no buffered fake messaging pretending to be live,
- no protocol exceptions for fire-and-forget subprocesses,
- the abstraction follows the implementations, not the other way around.

## 2) Goals

The protocol exists to support:
- real-time supervision,
- co-agent messaging during active work,
- structured observation of agent execution,
- interruption and recovery,
- adapter-agnostic orchestration semantics,
- transparent communication across all adapters.

## 3) Non-Goals

This protocol does not attempt to:
- support every vendor CLI,
- hide meaningful capability differences behind fake equivalence,
- define the internal prompting strategy of a model,
- replace `cagent`'s canonical job/session/work graph,
- prematurely abstract before understanding each adapter's strengths.

## 4) Adapter Landscape

### 4.1 Transport Matrix

Each adapter has two independent concerns: **control transport** (how cagent
drives the agent) and **tool bridge** (how the agent calls cagent mid-turn).

```
                    Control Transport          Tool Bridge
                    (cagent → agent)          (agent → cagent)
                    ─────────────────         ────────────────
Claude Code         MCP channels              MCP tools
Codex               JSON-RPC (app-server)     MCP tools or CLI (TBD)
Pi                  JSONL stdio               CLI commands
OpenCode            HTTP REST + SSE           MCP tools or CLI (TBD)
Native Go           in-process                direct service calls
```

### 4.2 Capability Matrix

| Capability | Claude Code | Codex | Pi | OpenCode | Native Go |
|---|---|---|---|---|---|
| **Transport** | MCP stdio/HTTP | JSON-RPC stdio/WS | JSONL stdio | HTTP REST + SSE | In-process |
| **Sessions** | MCP connection | Thread/Turn/Item | Tree-structured | Full CRUD + fork | Direct |
| **Steering** | Channel push | `turn/steer` | `steer` + `follow_up` | `noReply` injection | Direct |
| **Interrupt** | None programmatic | `turn/interrupt` | `abort` | `POST /abort` | Direct |
| **Events out** | Channel notifications | JSON-RPC notifications | JSONL on stdout | SSE endpoints | EventBus |
| **MCP client** | Yes | Yes | No (by design) | Yes | N/A |
| **Go SDK** | N/A | None | None | Official | N/A |

### 4.3 Build Order Rationale

1. **Claude Code** — already working. MCP server built, channels enabled,
   EventBus wired. Claude lacks a native live session API, so MCP + channels
   *is* our control transport. Proves the tool bridge via MCP.

2. **Codex** — richest external protocol. App-server has native session/turn/
   steer/interrupt. Near 1:1 mapping to our protocol objects. JSON-RPC is
   the control transport. Tool bridge: MCP or CLI, evaluate during build.

3. **Pi** — best steering model (`steer` vs `follow_up` with configurable
   delivery modes). Clean JSONL protocol over stdio. No MCP by design, so
   CLI is the tool bridge. Tests whether CLI tool bridge is sufficient.

4. **OpenCode** — Go SDK exists, HTTP REST is straightforward. Weakest
   steering (`noReply` workaround). Unreliable abort. Tests whether HTTP
   transport maps cleanly to our protocol.

5. **Native Go** — reference implementation. Direct service calls, EventBus
   subscription, full control. Learnings from all 4 adapters inform design.

## 5) Core Objects

### 5.1 Session

A session is a persistent conversational execution context.

A session MUST have:
- `session_id`
- adapter identity
- creation timestamp
- resumability semantics

A session MAY outlive any individual turn.

### 5.2 Turn

A turn is a single active work cycle within a session.

A turn MUST have:
- `turn_id`
- `session_id`
- status
- start timestamp

A turn is the unit of:
- active execution,
- steering,
- interruption,
- completion.

### 5.3 Event

An event is a structured notification emitted while a session or turn is
active. Events are the runtime observation surface for `cagent`.

Internally, `cagent` publishes events via the service-level `EventBus`.
Each adapter subscribes to the EventBus and translates events into its
native push mechanism:
- Claude: MCP channel notifications
- Codex: `turn/steer` with cagent messages
- Pi: `steer` command on stdin
- OpenCode: `prompt_async` + `noReply`
- Native Go: direct channel read

### 5.4 Tool Call

A tool call is an outbound invocation from the running agent into `cagent`.

Tool calls are first-class protocol events, not unstructured text.

The tool bridge varies per adapter:
- MCP tools (Claude, Codex, OpenCode — where supported)
- CLI commands (Pi, and as fallback)
- Direct service calls (native Go)

Whether MCP or CLI works better for Codex and OpenCode is an open question
to be evaluated during implementation.

## 6) Transport Model

The protocol is **transport-neutral** but assumes bidirectional communication.

Each adapter maps onto a different concrete transport:
- MCP over stdio (Claude Code)
- JSON-RPC 2.0 over stdio or WebSocket (Codex app-server)
- JSONL over stdio (Pi)
- HTTP REST + SSE (OpenCode)
- In-process Go interface calls (native)

A conforming transport MUST support:
- request/response RPCs (or equivalent),
- asynchronous notifications or event streaming,
- ordered delivery within a single connection,
- correlation of active session and active turn.

## 7) Required Capabilities

A conforming adapter MUST implement all of the following.

### 7.1 Persistent session identity

The adapter MUST expose a stable native session identifier that `cagent` can
map onto its canonical session.

### 7.2 Explicit turn lifecycle

The adapter MUST expose turn start and turn completion semantics.

### 7.3 Mid-turn steering

The adapter MUST allow `cagent` to inject a message into the currently active
turn without starting a new session.

This is the defining capability of the protocol. Implementation varies:
- Codex: `turn/steer` (first-class)
- Pi: `steer` command (first-class, configurable delivery)
- OpenCode: `noReply` context injection (workaround)
- Claude: MCP channel notification (custom)

### 7.4 Interrupt / cancel

The adapter MUST allow interruption of the active turn where the underlying
platform supports it.

Known limitations:
- Claude Code: no programmatic interrupt
- OpenCode: `abort` is unreliable

### 7.5 Structured event stream

The adapter MUST emit machine-readable events during execution.

### 7.6 Same-session continuation

The adapter MUST support continuing an existing session after a completed turn.

### 7.7 Tool callback path

The running agent MUST be able to call `cagent` tools during execution via
MCP, CLI, or direct service calls.

## 8) Canonical RPC Surface

A conforming adapter MUST present semantics equivalent to the following.
The exact wire representation varies per adapter transport.

### 8.1 `session/start`

Create a new live session.

Codex: `thread/start` → Pi: implicit on first `prompt` → OpenCode:
`POST /session` → Claude: MCP connection init → Native: `StartSession()`

### 8.2 `session/resume`

Reconnect to or continue an existing session.

Codex: `thread/resume` → Pi: load session file → OpenCode:
`GET /session/:id` → Claude: MCP reconnection → Native: `ResumeSession()`

### 8.3 `turn/start`

Start a new turn in an existing session.

Codex: `turn/start` → Pi: `prompt` → OpenCode: `POST /session/:id/prompt_async`
→ Claude: MCP tool call or channel → Native: `StartTurn()`

### 8.4 `turn/steer`

Inject additional input into the currently active turn.

Codex: `turn/steer` → Pi: `steer` or `follow_up` → OpenCode:
`POST /session/:id/prompt_async` with `noReply` → Claude: channel notification
→ Native: `Steer()`

### 8.5 `turn/interrupt`

Interrupt the active turn.

Codex: `turn/interrupt` → Pi: `abort` → OpenCode: `POST /session/:id/abort`
→ Claude: not available → Native: context cancellation

### 8.6 `session/close`

Close the live connection. MUST NOT imply deletion of durable state.

## 9) Event Stream

A conforming adapter MUST emit a structured event stream.

### 9.1 Required events

- Session lifecycle: started, resumed, closed
- Turn lifecycle: started, completed, failed, interrupted
- Assistant output: delta, message
- Tool calls: started, completed, failed

### 9.2 Internal event flow

All work graph mutations flow through the service-level `EventBus`:

```
  CreateWork ──┐
  UpdateWork ──┤
  ClaimWork  ──┼──▶ svc.Events.Publish(WorkEvent{...})
  AttestWork ──┤         │
  ReleaseWork ─┘         ▼
                    EventBus
                   ┌────┼────┐────┐
                   ▼    ▼    ▼    ▼
                Claude Codex  Pi  OpenCode
                chan   steer  steer noReply
```

Each adapter subscribes and translates to its native push mechanism.

### 9.3 Optional rich events

- `diff.updated`
- `plan.updated`
- `usage.reported`
- `artifact.created`

## 10) Co-Agent Messaging Format

All inter-agent messages visible to the model MUST use a transport-agnostic
structured format.

### 10.1 Message

```text
[cagent:message from="agent-id" type="info|result|request"]
Message body.
[/cagent:message]
```

### 10.2 Request / Response

```text
[cagent:request from="agent-id" request_id="req_123"]
Question body.
[/cagent:request]

[cagent:response to="agent-id" request_id="req_123"]
Answer body.
[/cagent:response]
```

Adapters MUST preserve message structure so that:
- the receiving model can recognize it,
- `cagent` can log or parse it,
- transport differences do not change the agent-visible format.

## 11) Concurrency and Race Safety

### 11.1 Turn identity check

Mid-turn steering MUST target a specific active turn where the adapter
supports it (Codex `expectedTurnId`, Pi implicit single-turn).

### 11.2 Delivery result

A steering request MUST resolve as one of:
- delivered,
- rejected (turn mismatch, no active turn),
- transport failure.

Silent best-effort delivery is not acceptable.

## 12) Error Model

Adapters MUST surface structured errors.

Minimum error classes:
- invalid request
- unknown session
- no active turn
- turn mismatch
- interrupt failed
- transport disconnected
- adapter unavailable

## 13) Security and Trust Boundary

The adapter is not the system of record.

`cagent` remains authoritative for:
- canonical session identity mapping,
- work graph state,
- durable event persistence,
- approval and attestation,
- policy.

The adapter is authoritative only for:
- live transport behavior,
- native session/turn identifiers,
- native event observation,
- native interrupt/steer mechanics.

## 14) Recommended Go Interface

```go
type LiveAgentAdapter interface {
    Name() string
    StartSession(ctx context.Context, req StartSessionRequest) (LiveSession, error)
    ResumeSession(ctx context.Context, nativeSessionID string) (LiveSession, error)
}

type LiveSession interface {
    SessionID() string
    ActiveTurnID() string

    StartTurn(ctx context.Context, input []Input) (string, error)
    Steer(ctx context.Context, expectedTurnID string, input []Input) error
    Interrupt(ctx context.Context) error

    Events() <-chan Event
    Close() error
}
```

This interface will evolve as we implement each adapter. The first adapter
(Claude Code) may not need all methods. The interface grows to accommodate
real needs, not speculative ones.

## 15) Implementation Plan

### Phase 1: Claude Code adapter (current)

Status: **MCP server built, channels enabled, EventBus wired.**

What exists:
- 10 MCP tools exposing the work graph
- `claude/channel` capability for push events
- Service-level EventBus with instant fan-out
- Event forwarder: EventBus → channel notifications
- `.mcp.json` for auto-discovery

What's next:
- Verify channel events reach Claude Code session
- Build the adapter wrapper implementing `LiveAgentAdapter`
- Test co-agent messaging through MCP tools

### Phase 2: Codex adapter

Approach:
- Spawn `codex app-server --listen stdio://` as subprocess
- Speak JSON-RPC 2.0 for control (thread/turn/steer/interrupt)
- Evaluate MCP vs CLI for tool bridge during build
- Map Codex thread → cagent session, Codex turn → cagent turn

Key questions to answer:
- Does Codex app-server reliably support MCP server connections?
- How does Codex approval flow interact with cagent attestation?
- What's the right mapping for Codex's rich item model?

### Phase 3: Pi adapter

Approach:
- Spawn `pi --mode rpc` as subprocess
- Speak JSONL for control (prompt/steer/abort)
- CLI commands for tool bridge (no MCP)
- Leverage Pi's `steer` vs `follow_up` distinction

Key questions to answer:
- Is CLI tool bridge sufficient for full work graph interaction?
- How does Pi's session file model map to cagent sessions?
- Can we build a Pi extension for richer integration?

### Phase 4: OpenCode adapter

Approach:
- Use official `opencode-sdk-go` for control
- HTTP REST + SSE for transport
- Evaluate MCP vs CLI for tool bridge
- Work around unreliable abort

Key questions to answer:
- Is `noReply` injection sufficient for real steering?
- How unreliable is abort in practice?
- Does the Go SDK cover everything we need?

### Phase 5: Native Go adapter

Approach:
- In-process, direct service calls
- EventBus subscription for events
- Reference implementation for protocol semantics
- Informed by learnings from all 4 external adapters

## 16) Research Rule

The protocol should be made **smaller before it is made broader**.

Prefer:
- concrete over abstract,
- verified over speculative,
- fewer adapters done well over many done poorly,
- learning from implementation over designing in advance.

## 17) Open Questions

These will be resolved through implementation:

1. **MCP vs CLI tool bridge**: For Codex and OpenCode, which works better
   in practice? Build both, measure, pick one.

2. **Steering fidelity**: How reliably does each adapter deliver mid-turn
   messages? Is `noReply` (OpenCode) good enough?

3. **Event completeness**: Which adapters emit enough events for meaningful
   supervision? What's the minimum viable event set?

4. **Session persistence**: How does each adapter's session model map to
   cagent's canonical session? Where are the impedance mismatches?

5. **Abstraction shape**: What does the `LiveAgentAdapter` interface actually
   look like after implementing all 5 adapters? The current interface is
   a starting guess.
