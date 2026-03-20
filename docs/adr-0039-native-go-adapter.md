# ADR-0039: Native Go Adapter — Direct LLM Client with Tool-Use Loop

**Status:** Accepted
**Date:** 2026-03-20

## Context

FASE orchestrates coding agents through adapters. Current adapters (codex, claude, opencode, pi) shell out to vendor CLI binaries. The "native" adapter was implemented as a conductor/meta-adapter that dispatches to these external adapters — it makes no LLM calls itself.

This is wrong. The native adapter should be a first-class LLM client: direct HTTP API calls, tool-use loop, co-agent spawning via the live adapter protocol. It must be referentially transparent with external adapters — the conductor/supervisor orchestrates adapters (including native) identically.

## Decision

### Three Providers, Two API Formats

The native adapter supports three LLM providers with two distinct API formats:

| Provider | API Format | Base URL | Auth | Models |
|----------|-----------|----------|------|--------|
| z.ai coding plan | Anthropic Messages | `https://api.z.ai/api/anthropic` | `Bearer $ZAI_API_KEY` | glm-4.7, glm-4.7-flash, glm-5-turbo, glm-5 |
| AWS Bedrock | Anthropic Messages | `https://bedrock-runtime.$AWS_REGION.amazonaws.com` | `Bearer $AWS_BEARER_TOKEN_BEDROCK` | claude-haiku-4-5, claude-sonnet-4-6, claude-opus-4-6 |
| ChatGPT (via Codex OAuth) | OpenAI Responses | `https://chatgpt.com/backend-api/codex/responses` | `Bearer <access_token from auth.json>` | gpt-5.4-mini, gpt-5.4-codex, o3, o4-mini |

Bedrock differences: model ID goes in URL path (`/model/{modelId}/invoke`), requires `anthropic_version: "bedrock-2023-05-31"`, tool_use IDs prefixed `toolu_bdrk_`.

### ChatGPT OAuth Provider

The ChatGPT provider uses Codex's OAuth flow to bill API usage to a ChatGPT subscription (Plus/Pro/Max) instead of metered API credits. This is how OpenCode and Pi already access OpenAI models.

**Auth flow:**
1. One-time: user runs `fase login chatgpt` which shells out to `codex login` (opens browser for ChatGPT sign-in)
2. Codex writes `~/.codex/auth.json` with access/refresh/id tokens
3. Native adapter reads `auth.json`, uses `access_token` as `Bearer` token
4. Token refresh: either shell out to `codex` to refresh, or use `https://auth.openai.com/oauth/token` directly with the refresh token

**`auth.json` structure:**
```json
{
  "auth_mode": "chatgpt",
  "tokens": {
    "access_token": "...",
    "refresh_token": "...",
    "id_token": "..."
  }
}
```

**API format — OpenAI Responses API (`POST /v1/responses`):**

Unlike the Anthropic Messages API, the Responses API uses a different request/response format:

```json
{
  "model": "gpt-5.4-mini",
  "instructions": "system prompt here",
  "input": [
    {"role": "user", "content": "user message"}
  ],
  "tools": [
    {
      "type": "function",
      "name": "read_file",
      "description": "Read file contents",
      "parameters": {
        "type": "object",
        "properties": {
          "path": {"type": "string"}
        },
        "required": ["path"]
      }
    }
  ],
  "stream": true
}
```

**Tool calls in response** appear as Items:
```json
{
  "type": "function_call",
  "call_id": "call_abc123",
  "name": "read_file",
  "arguments": {"path": "/foo/bar.go"}
}
```

**Tool results** are sent back as Items in the next request's `input`:
```json
{
  "type": "function_call_output",
  "call_id": "call_abc123",
  "output": "file contents here"
}
```

**Key differences from Anthropic Messages API:**
- `input` (not `messages`), `instructions` (not `system`)
- `output` array of typed Items (not `content` blocks)
- Tool calls are `function_call` Items with `call_id` (not `tool_use` blocks with `id`)
- Results are `function_call_output` Items (not `tool_result` blocks)
- Stateful: supports `previous_response_id` for multi-turn without resending history
- The endpoint at `chatgpt.com/backend-api/codex/responses` mirrors the standard Responses API format

**Implementation:** The native adapter needs two API client implementations sharing a common tool-use loop:
1. `AnthropicClient` — for z.ai and Bedrock (Messages API format)
2. `OpenAIClient` — for ChatGPT/Codex (Responses API format)

Both implement a common `LLMClient` interface that abstracts the request/response format differences away from the tool-use loop.

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

### LLM Client Interface

```go
// LLMClient abstracts Anthropic Messages API vs OpenAI Responses API.
type LLMClient interface {
    // Call sends a request and streams response events.
    // Returns tool calls (if any) and the stop reason.
    Call(ctx context.Context, req LLMRequest) (*LLMResponse, error)
}

type LLMRequest struct {
    System   string
    Messages []Message       // conversation history
    Tools    []ToolDef        // tool definitions
    Stream   bool
}

type LLMResponse struct {
    TextBlocks []string        // text output
    ToolCalls  []ToolCall       // tool invocations
    StopReason string          // "end_turn", "tool_use", "max_tokens"
    Usage      Usage
    // For streaming: events are emitted via callback during Call()
    OnDelta    func(text string)
}
```

Two implementations:
- `anthropicClient` — for z.ai and Bedrock (Messages API, `POST /v1/messages`)
- `openaiClient` — for ChatGPT/Codex (Responses API, `POST /v1/responses`)

### Provider Configuration

```go
type Provider struct {
    Name       string  // "zai", "bedrock", or "chatgpt"
    APIFormat  string  // "anthropic" or "openai"
    BaseURL    string
    AuthFunc   func() (string, error)  // returns "Bearer <token>"
    ModelID    string
    // Anthropic-specific
    AnthropicVersion string  // "bedrock-2023-05-31" for bedrock, "" for z.ai
    ModelInPath      bool    // true for bedrock
}
```

Model string parsing:
- `"bedrock/claude-haiku-4-5"` → Provider=bedrock, APIFormat=anthropic
- `"zai/glm-4.7"` → Provider=zai, APIFormat=anthropic
- `"chatgpt/gpt-5.4-mini"` → Provider=chatgpt, APIFormat=openai

The `AuthFunc` closure handles the different auth patterns:
- z.ai/bedrock: read env var, return static bearer token
- chatgpt: read `~/.codex/auth.json`, refresh if expired, return access token

### Configuration

```toml
[adapters.native]
enabled = true
summary = "Direct LLM API client"

[adapters.native.providers.zai]
api_format = "anthropic"
base_url = "https://api.z.ai/api/anthropic"
api_key_env = "ZAI_API_KEY"
models = ["glm-4.7", "glm-4.7-flash", "glm-5-turbo"]

[adapters.native.providers.bedrock]
api_format = "anthropic"
base_url = "https://bedrock-runtime.us-east-1.amazonaws.com"
api_key_env = "AWS_BEARER_TOKEN_BEDROCK"
anthropic_version = "bedrock-2023-05-31"
models = ["claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-6"]

[adapters.native.providers.chatgpt]
api_format = "openai"
base_url = "https://chatgpt.com/backend-api/codex/responses"
auth_method = "codex_oauth"  # reads ~/.codex/auth.json
models = ["gpt-5.4-mini", "gpt-5.4-codex", "o3", "o4-mini"]
```

### Login Command

```bash
fase login chatgpt    # shells out to `codex login`, stores auth.json
fase login status     # shows active auth methods and token freshness
```

### What This Replaces

The current `internal/adapters/native/live.go` (conductor pattern) is **deleted**. The conductor/worker pattern is an orchestration concern handled by the supervisor, not an adapter concern. Adapters are LLM clients. Period.

The echo session moves to a test helper.

## Consequences

- **No vendor CLI dependency**: Native adapter runs anywhere with API keys
- **Type-safe tool calls**: Direct `service.Method()` calls, not subprocess/MCP bridging
- **Co-agent composability**: LLM decides when to delegate, can spawn any adapter
- **Three providers**: z.ai glm-4.7, Bedrock claude-haiku-4-5, ChatGPT gpt-5.4-mini for eval/bulk work
- **ChatGPT subscription billing**: Use existing Plus/Pro subscription via Codex OAuth — no API credits needed
- **Same interface contract**: `LiveAgentAdapter`/`LiveSession` — conductor/supervisor treats it identically to codex, claude, opencode
- **Streaming**: Real-time output deltas via SSE parsing
- **Future extensibility**: Adding a provider = new Provider struct, no code changes to the loop

## Implementation Plan

### Phase 1: LLM Clients
1. **LLMClient interface**: Common abstraction over Anthropic Messages + OpenAI Responses
2. **Anthropic client**: HTTP + SSE streaming, supports z.ai and Bedrock via provider config
3. **OpenAI client**: HTTP + SSE streaming, supports ChatGPT/Codex Responses API
4. **Auth**: Env var readers for z.ai/Bedrock, `auth.json` reader + refresh for ChatGPT

### Phase 2: Tool System
5. **Tool registry**: Define tools as Go structs with JSON schema parameters
6. **Coding tools**: read_file, write_file, edit_file, glob, grep, bash, git ops
7. **FASE tools**: work_list, work_create, work_update, work_attest, etc. (direct service calls)

### Phase 3: Core Loop + LiveSession
8. **Tool-use loop**: Provider-agnostic goroutine that calls LLM, executes tools, loops
9. **LiveSession**: Wire tool loop into StartTurn/Steer/Interrupt/Events/Close
10. **LiveAdapter**: Factory that creates sessions with provider + tool registry

### Phase 4: Integration
11. **Co-agent tools**: Spawn/send/steer/close other LiveSessions via channels
12. **Adapter registry**: Register native adapter, wire into supervisor dispatch
13. **Login command**: `fase login chatgpt` flow

### Phase 5: Eval
14. **Multi-step eval tasks** through the supervisor using native adapter with cheap models

## References

- [Codex Auth Documentation](https://developers.openai.com/codex/auth/)
- [Codex App Server (JSON-RPC)](https://developers.openai.com/codex/app-server)
- [OpenAI Responses API](https://platform.openai.com/docs/guides/function-calling)
- [openai-oauth: ChatGPT OAuth proxy](https://github.com/EvanZhouDev/openai-oauth)
- [AWS Bedrock Claude](https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages.html)
