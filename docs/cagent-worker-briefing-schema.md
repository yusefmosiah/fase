Date: 2026-03-11
Kind: Spec
Status: Draft
Priority: 1
Requires: [docs/cagent-work-runtime.md, docs/cagent-work-api-and-schema.md]
Owner: Runtime / Work System

## Narrative Summary (1-minute read)

This doc defines the stable compiled worker briefing contract for `cagent`.

Workers should not start from a handwritten orchestration prompt. They should
start from a versioned, deterministic briefing compiled from the work graph,
recent evidence, and runtime policy. Adapters may render that briefing into
natural language or structured input, but the source contract stays the same.

The stable schema lives in
[schemas/worker-briefing.schema.json](/Users/wiz/cagent/schemas/worker-briefing.schema.json).

## What Changed

1. Defined a versioned worker-briefing schema.
2. Split the briefing into assignment, requirements, graph context, evidence,
   worker contract, and hydration layers.
3. Made compilation deterministic and adapter-independent.
4. Defined what belongs in the briefing versus what stays as on-demand runtime
   lookup.

## What To Do Next

1. Implement `CompileWorkerBriefing(workID, mode)` in the service layer.
2. Add `cagent work hydrate <work-id>`.
3. Make all worker adapters consume the same compiled briefing contract.
4. Add projection/rendering tests to keep the contract stable.

## 1) Purpose

The worker briefing is the runtime-native replacement for a bespoke launch
prompt.

It exists to answer:

1. What work is this worker responsible for?
2. What constraints and acceptance criteria govern the work?
3. What graph context matters right now?
4. What evidence already exists?
5. How should the worker interact with the runtime?
6. What is the recommended next action frontier?

This is the stable hydration contract between `cagent` runtime state and any
execution adapter.

## 2) Contract Shape

The canonical format is JSON conforming to
[schemas/worker-briefing.schema.json](/Users/wiz/cagent/schemas/worker-briefing.schema.json).

Top-level sections:

1. `schema_version`
2. `briefing_kind`
3. `generated_at`
4. `runtime`
5. `assignment`
6. `requirements`
7. `graph_context`
8. `evidence`
9. `worker_contract`
10. `hydration`

These sections are mandatory in v1. The runtime may add optional fields only
through a schema version bump.

## 3) Section Semantics

### 3.1 `runtime`

Identity of the runtime that compiled the briefing.

Purpose:
- provenance
- debugging
- stable linkage to local state

It should include:
- runtime version
- config path
- state dir
- current host adapter/model when known
- claimant identity when the worker holds a lease

### 3.2 `assignment`

The canonical work item being hydrated.

This is the minimal durable identity:
- work id
- title
- objective
- kind
- phase
- execution state
- approval state
- current linked job/session
- lease holder and expiry when present

This section should be enough to say "what am I doing right now?" before any
graph traversal.

### 3.3 `requirements`

What constrains acceptable execution.

This includes:
- acceptance criteria
- capability requirements
- adapter preferences and exclusions
- configuration/budget classes
- mutation policy for graph changes

This replaces the "important instructions hidden in docs" problem with a
machine-readable contract.

### 3.4 `graph_context`

The local semantic neighborhood of the assigned work.

V1 includes:
- parent
- inbound blockers
- outbound blockers
- children
- verifier nodes
- discovered nodes
- supersession lineage

This is intentionally local, not the full graph dump. The runtime still owns
the larger graph, and workers can query it on demand.

### 3.5 `evidence`

Recent facts relevant to acting correctly.

This includes:
- latest structured updates
- latest notes
- latest attestation results
- linked artifacts
- recent jobs
- relevant history matches

The rule is recency plus relevance, not transcript replay.

### 3.6 `worker_contract`

The explicit runtime-facing API contract for the worker.

This should list:
- safe read commands
- safe write commands
- behavioral rules

Examples of rules:
- publish progress with structured updates
- add notes for findings and feedback
- create proposals for structural graph edits
- do not silently mutate dependencies or acceptance

### 3.7 `hydration`

The compiled operational summary.

This is the natural-language-heavy section. It should contain:
- a concise summary
- open questions
- recommended next actions
- selected hydration mode: `thin`, `standard`, or `deep`

This is the adapter-facing synthesis layer, but it is still derived from the
same deterministic state inputs.

## 4) Compilation Rules

The runtime should compile briefings deterministically from the same work state.

### 4.1 Stable Inputs

Compilation may read:
- `work_items`
- `work_edges`
- `work_updates`
- `work_notes`
- `work_proposals`
- `attestation_records`
- linked artifacts
- linked jobs/sessions
- relevant canonical history matches
- runtime config and catalog facts

Compilation must not depend on:
- arbitrary handwritten operator prose outside the work system
- current terminal scrollback
- adapter-specific transcript formatting
- undocumented hidden prompt fragments

### 4.2 Deterministic Ordering

V1 ordering rules:
- updates, notes, attestations, jobs, artifacts, and history matches sorted by
  recency descending
- graph references sorted by priority desc, then update time desc, then id
- arrays truncated by deterministic cutoffs

### 4.3 Deterministic Truncation

Recommended v1 defaults:
- latest updates: 10
- latest notes: 10
- latest attestations: 10
- artifacts: 20
- recent jobs: 10
- history matches: 10
- children: 25
- blockers/verifiers/discoveries/supersession refs: 25 each

If more material exists, the worker should fetch it through the work/history API
instead of receiving an unbounded initial payload.

### 4.4 Hydration Modes

`thin`
- assignment
- requirements
- local graph refs
- minimal evidence
- short summary

`standard`
- default mode
- all top-level sections with bounded evidence

`deep`
- same schema, but with higher evidence cutoffs and a denser summary/open
  questions/action set

The schema does not change across modes. Only the density of the compiled
content changes.

## 5) What Stays Out Of The Briefing

The worker briefing is not:
- the full transcript
- the whole work graph
- a replay of every artifact body
- a second database

Large or numerous objects should remain path- and id-addressable through the
runtime APIs.

The boundary is:
- the briefing gives fast initial coherence
- the runtime stays the source of truth

## 6) Adapter Contract

Every adapter should consume the same worker briefing contract.

Adapters may differ in how they render it:
- plain text compiled prompt
- structured system plus task messages
- file-backed manifest plus compact prompt
- richer harness-specific initialization

But they should all derive from the same JSON briefing, not from divergent
bespoke templates.

That means:
- one runtime compiler
- many adapter renderers

## 7) Prompt Rendering Rule

Natural-language prompt rendering is downstream of the worker briefing.

The happy path is:
1. compile the briefing
2. render an adapter-specific worker prompt from the briefing
3. let the worker query the work API for more detail as needed

Not:
1. handwrite a giant prompt
2. hope it captures runtime state accurately

## 8) Example Shape

```json
{
  "schema_version": "cagent.worker_briefing.v1",
  "briefing_kind": "assignment",
  "generated_at": "2026-03-11T07:00:00Z",
  "runtime": {
    "runtime_version": "dev",
    "config_path": "/Users/wiz/.config/cagent/config.toml",
    "state_dir": "/Users/wiz/.local/state/cagent",
    "claimant": "worker-opencode-1"
  },
  "assignment": {
    "work_id": "work_123",
    "title": "Implement history search",
    "objective": "Add durable history search across canonical sessions",
    "kind": "implement",
    "phase": "implementation",
    "execution_state": "claimed",
    "approval_state": "pending"
  },
  "requirements": {
    "acceptance": {
      "tests_required": true
    },
    "required_capabilities": ["headless_run"],
    "preferred_adapters": ["codex", "opencode"],
    "forbidden_adapters": [],
    "policy": {
      "child_creation": "allowed",
      "dependency_edits": "proposal_only",
      "scope_expansion": "approval_required",
      "attestation_policy": "required"
    }
  }
}
```

## 9) Why This Replaces Docs As Source Of Truth

Traditional Markdown docs tried to hold:
- intent
- current state
- evidence
- open questions
- next steps

The work runtime now owns those semantically:
- intent in `assignment` and graph shape
- current state in work execution, approval, and update records
- evidence in attestations, artifacts, jobs, and history
- open questions and next steps in compiled hydration

Markdown becomes a projection again, not the database.

## 10) Implementation Target

The first implementation should add:

1. `CompileWorkerBriefing(workID, mode)` in the service layer
2. a `WorkerBriefing` Go type mirroring the schema
3. `cagent work hydrate <work-id> [--mode thin|standard|deep]`
4. adapter renderers that consume `WorkerBriefing`
5. tests that pin the JSON shape and deterministic ordering rules
