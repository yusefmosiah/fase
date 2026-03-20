# ADR-0039: Native Go Adapter — Direct LLM Client with Tool-Use Loop

**Status:** Accepted
**Date:** 2026-03-20

## Context

FASE orchestrates coding agents through adapters. Current adapters (codex, claude, opencode, pi) shell out to vendor CLI binaries. The "native" adapter was implemented as a conductor/meta-adapter that dispatches to these external adapters — it makes no LLM calls itself.

This is wrong. The native adapter should be a first-class LLM client: direct HTTP API calls, tool-use loop, co-agent spawning via the live adapter protocol. It must be referentially transparent with external adapters — the conductor/supervisor orchestrates adapters (including native) identically.

## Decision

### Single Client, Two Providers

Both z.ai coding plan and AWS Bedrock Claude support the Anthropic Messages API format. The native adapter uses one HTTP client with provider-specific configuration:

| Provider | Base URL | Auth | Models |
|----------|----------|------|--------|
| z.ai coding plan | `https://api.z.ai/api/anthropic` | `Bearer $ZAI_API_KEY` | glm-4.7, glm-4.7-flash, glm-5-turbo, glm-5 |
| AWS Bedrock | `https://bedrock-runtime.$AWS_REGION.amazonaws.com` | `Bearer $AWS_BEARER_TOKEN_BEDROCK` | claude-haiku-4-5, claude-sonnet-4-6, claude-opus-4-6 |

Bedrock differences: model ID goes in URL path (`/model/{modelId}/invoke`), requires `anthropic_version: "bedrock-2023-05-31"`, tool_use IDs prefixed `toolu_bdrk_`.

### Architecture

```
┌─────────────────────────────────────────────┐
│              Native LiveSession             │
│                                             │
│  ┌─────────┐    ┌──────────┐    ┌────────┐  │
│  │ Messages│───▸│ Provider │───▸│  HTTP  │  │
│  │ History │    │ Client   │    │ Stream │  │
│  └─────────┘    └──────────┘    └────────┘  │
│       │                              │      │
│       ▼                              ▼      │
│  ┌──────────────────────────────────────┐   │
│  │          Tool-Use Loop               │   │
│  │                                      │   │
│  │  LLM response ──▸ tool_use?          │   │
│  │    yes: execute tool, append result  │   │
│  │         loop back to LLM call        │   │
│  │    no:  emit output, turn complete   │   │
│  └──────────────────────────────────────┘   │
│       │                                     │
│       ▼                                     │
│  ┌──────────────────────────────────────┐   │
│  │            Tool Registry             │   │
│  │                                      │   │
│  │  fase tools:  work_list, work_create │   │
│  │    work_update, work_attest, ...     │   │
│  │  coding tools: read_file, write_file │   │
│  │    bash, glob, grep                  │   │
│  │  co-agent:    spawn_session, send,   │   │
│  │    steer, close                      │   │
│  └──────────────────────────────────────┘   │
│       │                                     │
│       ▼                                     │
│  ┌──────────┐                               │
│  │  Event   │──▸ adapterapi.Event channel   │
│  │  Emitter │                               │
│  └──────────┘                               │
└─────────────────────────────────────────────┘
```

### Tool-Use Loop

The core loop runs in a goroutine per turn:

```
StartTurn(input) → turnID
  │
  ▼
  append user message to history
  │
  ▼
  ┌─── loop ────────────────────────┐
  │  call LLM (stream response)     │
  │    │                            │
  │    ├─ text block → emit delta   │
  │    ├─ tool_use block → collect  │
  │    │                            │
  │  check stop_reason:             │
  │    end_turn → TurnCompleted     │
  │    tool_use → execute tools ──┐ │
  │    max_tokens → TurnFailed    │ │
  │                               │ │
  │  append assistant msg         │ │
  │  append tool results ◀────────┘ │
  │  continue loop                  │
  └─────────────────────────────────┘
```

### Tool Categories

**1. FASE work graph tools** (direct service calls, no subprocess):
- `work_list`, `work_show`, `work_create`, `work_update`
- `work_attest`, `work_note_add`, `work_claim`
- `ready_work`, `project_hydrate`

**2. Coding tools** (filesystem + shell):
- `read_file` — read file contents
- `write_file` — create or overwrite file
- `edit_file` — apply targeted edit (old_string → new_string)
- `glob` — find files by pattern
- `grep` — search file contents
- `bash` — execute shell command
- `git_status`, `git_diff`, `git_commit` — git operations

**3. Co-agent tools** (live adapter protocol over channels):
- `spawn_session` — start a new LiveSession on any adapter
- `send_turn` — send input to a co-agent session
- `steer_session` — inject mid-turn input
- `close_session` — shut down a co-agent
- `list_sessions` — enumerate active co-agent sessions

Co-agent tools allow the native adapter's LLM to delegate work to other adapters (including other native sessions). This is how conductor/worker patterns compose — the LLM decides when to delegate, not the adapter infrastructure.

### Steering

The Anthropic Messages API doesn't support mid-turn injection. Steering is handled by:

1. Steer messages are queued in a channel
2. After each tool execution (before the next LLM call), pending steers are drained
3. Steer content is prepended to the next user message as `[fase:steer]` tagged blocks
4. If no tool execution is happening (pure text generation), steers wait until the current response completes, then start a new turn

### Session Management

```go
type nativeSession struct {
    id       string
    provider Provider          // z.ai or bedrock
    history  []Message         // conversation history
    tools    []ToolDefinition  // registered tools
    eventCh  chan Event        // output events
    steerQ   chan SteerEvent   // pending steers
    svc      *service.Service  // direct service access
    coAgents map[string]LiveAgentAdapter  // for co-agent spawning
}
```

- **No resume**: Conversation history is in-memory only. Resume would require persisting potentially large histories. If needed later, serialize to disk.
- **Session = one conversation thread**: Multiple turns share history within a session.
- **Concurrency**: One active turn at a time per session. StartTurn while a turn is active returns an error.

### Provider Client

```go
type Provider struct {
    Name       string  // "zai" or "bedrock"
    BaseURL    string
    AuthHeader string  // "Bearer <key>"
    ModelID    string
    // Bedrock-specific
    AnthropicVersion string  // "bedrock-2023-05-31" or ""
    ModelInPath      bool    // true for bedrock
}
```

Model string parsing: `"bedrock/claude-haiku-4-5"` → Provider=bedrock, Model=anthropic.claude-haiku-4-5-20251001-v1:0. `"zai/glm-4.7"` → Provider=zai, Model=glm-4.7.

### Configuration

```toml
[adapters.native]
enabled = true
summary = "Direct LLM API client"

[adapters.native.providers.zai]
base_url = "https://api.z.ai/api/anthropic"
api_key_env = "ZAI_API_KEY"
models = ["glm-4.7", "glm-4.7-flash", "glm-5-turbo"]

[adapters.native.providers.bedrock]
base_url = "https://bedrock-runtime.us-east-1.amazonaws.com"
api_key_env = "AWS_BEARER_TOKEN_BEDROCK"
anthropic_version = "bedrock-2023-05-31"
models = ["claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-6"]
```

### What This Replaces

The current `internal/adapters/native/live.go` (conductor pattern) is **deleted**. The conductor/worker pattern is an orchestration concern handled by the supervisor, not an adapter concern. Adapters are LLM clients. Period.

The echo session moves to a test helper.

## Consequences

- **No vendor CLI dependency**: Native adapter runs anywhere with API keys
- **Type-safe tool calls**: Direct `service.Method()` calls, not subprocess/MCP bridging
- **Co-agent composability**: LLM decides when to delegate, can spawn any adapter
- **Two cheap providers**: z.ai glm-4.7 and Bedrock claude-haiku-4-5 for eval/bulk work
- **Same interface contract**: `LiveAgentAdapter`/`LiveSession` — conductor/supervisor treats it identically to codex, claude, opencode
- **Streaming**: Real-time output deltas via SSE parsing
- **Future extensibility**: Adding a provider = new Provider struct, no code changes to the loop

## Implementation Plan

1. **Provider client**: HTTP client with Anthropic Messages API, SSE streaming, provider-specific URL/auth
2. **Tool registry**: Define tools as Go structs, map to service calls and shell commands
3. **Tool-use loop**: Core goroutine that calls LLM, executes tools, loops
4. **LiveSession implementation**: Wire tool loop into StartTurn/Steer/Interrupt/Events/Close
5. **LiveAdapter**: Factory that creates sessions with provider + tool registry
6. **Co-agent tools**: Spawn/send/steer/close other LiveSessions
7. **Integration**: Register in adapter registry, wire into supervisor dispatch
8. **Eval**: Run multi-step tasks through the supervisor using native adapter
