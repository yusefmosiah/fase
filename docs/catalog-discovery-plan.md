# Catalog Discovery Plan

Date: 2026-03-10
Status: Research complete, ready for implementation

## Purpose

`cogent` needs a release-quality inventory feature that tells a host agent:
- which coding-agent CLIs are installed and usable,
- which providers and models are available through them,
- whether access is subscription-backed or usage-priced,
- which model is currently selected by default when known,
- where each fact came from.

This is separate from `runtime`.

- `runtime` answers: "what adapters are locally installed and runnable?"
- `catalog` answers: "what providers, models, and access modes can the host route work to?"

## Main Decisions

1. Pricing is not a v0 requirement.
2. Auth mode and billing class are the important routing signals.
3. We should store provenance for every catalog fact.
4. We should prefer local CLI/config/env inspection over web lookups at runtime.
5. We should not pretend every adapter can enumerate all remote models.
6. We should explicitly favor reusable user-account auth across harnesses when vendors permit it.

## Strategic Goal

For shipped `cogent` usage, the most valuable auth path is not "any available key",
but "an account the user already has and is allowed to reuse across harnesses."

Right now that means:
- OpenAI account reuse is strategically important.
- Anthropic subscription reuse is lower priority for shipped-product scenarios.

Implications:
- Codex should treat ChatGPT-backed login as a first-class catalog signal.
- OpenCode should treat OpenAI OAuth / ChatGPT-backed access as a first-class catalog signal.
- Pi should not be modeled as supporting OpenAI account reuse unless it gains an official OpenAI account auth flow; today it looks API-key based.
- `cogent` should not invent a synthetic shared-login layer when the underlying harness only supports API keys.

## Data Model

The catalog should distinguish at least these concepts:

- `auth_method`
  - How the CLI authenticates.
  - Examples: `chatgpt`, `claude_ai`, `api_key`, `oauth`, `vertex`, `bedrock`, `local_model`, `unknown`

- `billing_class`
  - How usage is paid for.
  - Values:
    - `subscription`
    - `metered_api`
    - `cloud_project`
    - `local`
    - `unknown`

- `source`
  - Where the fact came from.
  - Values:
    - `cli`
    - `config`
    - `env`
    - `manual`

Suggested catalog entry shape:

```json
{
  "adapter": "opencode",
  "provider": "zai",
  "model": "glm-5",
  "selected": true,
  "available": true,
  "auth_method": "api_key",
  "billing_class": "metered_api",
  "source": "cli",
  "provenance": {
    "command": "opencode auth list",
    "observed_at": "2026-03-10T12:00:00Z"
  }
}
```

## Researched Findings

### Codex

Official sources:
- [OpenAI Codex overview](https://developers.openai.com/codex/)
- [OpenAI help: ChatGPT included access and API migration](https://help.openai.com/en/articles/11096431-openai-codex-cli-getting-started)

Local CLI findings:
- `codex login status` prints `Logged in using ChatGPT`
- `codex login --help` exposes `--with-api-key`
- `codex --help` and `codex exec --help` expose:
  - `--model`
  - `--oss`
  - `--local-provider`

Implications:
- Codex clearly supports multiple auth modes:
  - ChatGPT account-backed access
  - API key access
  - local OSS provider mode
- For product routing, ChatGPT-backed access should be marked as the preferred account-reuse mode.
- Codex does not appear to expose a first-class "list models" command.
- v0 Codex cataloging should focus on:
  - auth mode
  - selected model
  - provider class (`openai`, `oss/lmstudio`, `oss/ollama`)
- v0 should not promise full remote model enumeration for Codex.

### Claude Code

Official sources:
- [Claude Code authentication](https://docs.anthropic.com/en/docs/claude-code/authentication)
- [Claude Code overview and pricing modes](https://docs.anthropic.com/en/docs/claude-code/overview)
- [Claude Code CLI reference](https://docs.anthropic.com/en/docs/claude-code/cli-reference)

Local CLI findings:
- `claude auth status` returns JSON with:
  - `authMethod`
  - `apiProvider`
  - `subscriptionType`
- `claude setup-token --help` says it requires a Claude subscription
- `claude --help` exposes:
  - `--model`
  - `--max-budget-usd`
  - `--betas` with note `API key users only`

Implications:
- Claude has the best auth-mode surface of the current adapters.
- We can confidently distinguish:
  - subscription-backed `claude.ai` access
  - first-party API usage
  - cloud-provider modes documented by Anthropic
- Claude does not appear to expose a model catalog command.
- v0 Claude cataloging should prioritize:
  - auth status JSON
  - selected model
  - provider mode
- Full model enumeration should be out of scope unless we later add a curated alias list.

### Gemini CLI

Official sources:
- [Gemini CLI authentication](https://geminicli.com/docs/cli/authentication/)
- [Gemini CLI quota and pricing](https://geminicli.com/docs/quota-and-pricing/)
- [Gemini CLI models](https://geminicli.com/docs/cli/configuration/#model)

Local CLI findings:
- `gemini --help` exposes:
  - `--model`
  - `--resume`
  - `--output-format`
- No local `list-models` command was found.

Implications:
- Gemini auth mode materially changes billing/quota behavior.
- Official docs distinguish:
  - Google login / included quota paths
  - Gemini API key usage
  - Vertex AI / cloud-project usage
- Since the local CLI does not expose a model catalog surface, v0 Gemini cataloging should use:
  - env/config inspection
  - selected model if configured
  - auth-mode detection
- v0 should not promise complete model enumeration for Gemini.

### OpenCode

Official sources:
- [OpenCode home](https://dev.opencode.ai/)
- [OpenCode providers docs](https://opencode.ai/docs/providers/)
- [OpenCode configuration docs](https://opencode.ai/docs/config/)

Local CLI findings:
- `opencode models [provider]` lists models
- `opencode models --help` exposes `--refresh` and notes refresh from `models.dev`
- `opencode auth list` shows provider credentials and method labels such as `oauth` and `api`
- `opencode auth login --help` exposes `--provider` and `--method`
- Local `opencode models` already returns a large provider/model list

Implications:
- OpenCode has the richest catalog surface.
- We can discover:
  - provider IDs
  - model IDs
  - credential method per provider
- OpenCode also appears to support OpenAI-backed account reuse directly:
  - the public site explicitly says "Log in with OpenAI to use your ChatGPT Plus or Pro account"
- This should be the first adapter with full provider/model enumeration in `cogent`.
- OpenCode is also the best low-cost testbed because provider choice is explicit and `glm-5` is available.

### Pi

Official sources:
- [Pi repo](https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent)
- [Shitty Coding Agent site](https://shittycodingagent.ai)

Local CLI findings:
- `pi --help` exposes:
  - `--provider`
  - `--model`
  - `--list-models`
  - `--api-key`
- `pi --list-models` returns provider/model rows
- `pi --help` documents many provider-specific env vars

Implications:
- Pi is much more catalogable than expected.
- We can discover:
  - provider list
  - model list
  - selected provider/model
  - likely auth mode from env/config
- Pi currently looks API-key and token based, not account-login based, for OpenAI.
- Pi should be implemented early in the catalog because its surfaces are straightforward.

### Factory Droid

Official sources:
- [Factory CLI overview](https://docs.factory.ai/cli/getting-started/overview)
- [Factory CLI reference](https://docs.factory.ai/reference/cli-reference)

Local CLI findings:
- `droid exec --help` exposes:
  - `--model`
  - `--session-id`
  - `--list-tools`
- `droid exec --help` includes an explicit "Available Models" section
- `droid exec --help` documents `FACTORY_API_KEY`
- `--mission` is a real multi-agent orchestration mode

Implications:
- Factory appears to be API-key-backed in the local CLI.
- The available model list is directly parseable from help text.
- Factory should be cataloged with:
  - enumerated available models from help
  - selected/default model from help or config
  - billing class `metered_api`

## Implementation Scope

### v0 Catalog Commands

Public commands:
- `cogent catalog sync`
- `cogent catalog show`

Behavior:
- `catalog sync`
  - runs adapter-specific discoverers
  - stores a snapshot with provenance and timestamp
- `catalog show`
  - renders the latest snapshot
  - supports `--json`

### v0 Discovery Coverage

High-confidence adapters:
- OpenCode
- Pi
- Factory
- Claude

Medium-confidence adapters:
- Codex
- Gemini

For medium-confidence adapters, v0 should prefer:
- auth mode
- selected/default model
- provider class

and explicitly leave full model enumeration unknown when not exposed.

## Adapter-Specific Discovery Plan

### Codex

Discovery order:
1. `codex login status`
2. `~/.codex/config.toml`
3. selected model from config or runtime overrides
4. `--oss` / local provider inference from config

Expected output:
- one selected entry for OpenAI account/API mode
- optionally one selected local-provider entry for OSS mode
- if ChatGPT-backed auth is active, mark it as preferred for reusable account-backed routing

### Claude

Discovery order:
1. `claude auth status`
2. config/settings inspection if needed
3. selected model from config or operator defaults

Expected output:
- one selected provider/auth entry
- selected model if known
- subscription type when provided

### Gemini

Discovery order:
1. env inspection for API-key and Vertex markers
2. Gemini config/settings inspection
3. selected model from config

Expected output:
- auth mode
- billing class
- selected model if known
- no promised full model list in v0

### OpenCode

Discovery order:
1. `opencode auth list`
2. `opencode models`
3. config inspection for selected provider/model

Expected output:
- one entry per discovered provider/model pair
- one or more selected entries if config indicates defaults
- credential method per provider
- if OpenAI OAuth is configured, mark it as preferred for reusable account-backed routing

### Pi

Discovery order:
1. `pi --list-models`
2. config/session-dir inspection
3. provider/model env inspection

Expected output:
- enumerated provider/model entries
- selected/default provider and model
- auth method inferred from env/config

### Factory

Discovery order:
1. `droid exec --help`
2. config/settings inspection
3. env inspection for `FACTORY_API_KEY`

Expected output:
- enumerated model entries parsed from help
- selected/default model
- `metered_api` billing class

## Storage Plan

Add a new persisted snapshot concept rather than mutating runtime state.

Suggested table:
- `catalog_snapshots`

Suggested stored fields:
- `snapshot_id`
- `created_at`
- `entries_json`
- `sources_json`

Rationale:
- append-only snapshots are simpler to debug
- host agents may want to compare stale vs refreshed state

## Testing Plan

### Contract Tests

- JSON schema for `catalog show`
- exit codes and empty-state handling

### Fixture Tests

Capture and parse representative outputs for:
- `codex login status`
- `claude auth status`
- `opencode auth list`
- `opencode models`
- `pi --list-models`
- `droid exec --help`

### Live Local Tests

On a machine with real CLIs installed:
- confirm parser behavior against current outputs
- verify unknown fields degrade safely

### Low-Cost Live Routing Tests

Default low-cost models:
- OpenCode: `glm-5`
- Factory: `glm-5`
- Pi: default low-cost provider/model from local setup
- Codex/Claude/Gemini: explicit cheaper model where available

Goals:
- prove the catalog can drive cheap-model routing
- verify host-agent selection behavior
- surface latent capabilities in lower-cost models

### Recursive `cogent` Tests

The capstone scenario should validate `cogent in cogent`:
1. planner `cogent`
2. implementation `cogent`
3. verification loop `cogent`
4. review `cogent`
5. red-team `cogent`
6. report `cogent`

Run this first on low-cost models.
Use higher-end models only as sparse comparison baselines.

## Recommended Build Order

1. Add catalog schema and snapshot storage.
2. Add `catalog sync` and `catalog show`.
3. Implement OpenCode, Pi, and Factory discoverers first.
4. Implement Claude auth discovery.
5. Implement conservative Codex and Gemini discovery.
6. Add fixture tests for all parsers.
7. Add low-cost live routing tests.
8. Add recursive `cogent` orchestration tests.

## Non-Goals For This Slice

- authoritative pricing across all vendors
- web scraping vendor sites at runtime
- full remote model enumeration when the CLI does not expose it
- guaranteed spend calculation for subscription-backed access
