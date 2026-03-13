Date: 2026-03-10
Kind: Guide + spec
Status: Draft
Priority: 1
Requires: [cagent-spec-and-implementation-guide.md]
Owner: Runtime / Work System

## Narrative Summary (1-minute read)

`cagent` is evolving from a durable job runner into a durable work runtime for
coding agents.

The core idea is that `work`, not prompts, becomes the source of truth.
Coding-agent sessions are execution vehicles attached to work. Adapters are
transport backends. The runtime persists the state that matters:
- work items
- work updates
- notes and discoveries
- attestation and approval state
- artifacts and history
- derived projections of the current system state

This is the bridge from filesystem Markdown docs to a semantic work system.
Markdown was the bootstrap medium. The work runtime becomes the durable semantic
medium. Documentation can then become a deterministic projection of work state
instead of a separate hand-maintained truth universe.

## What Changed

1. Defined `work` as the primary abstraction above jobs and sessions.
2. Framed `cagent` as a single integrated runtime, not a federation of hidden
   actors.
3. Made worker hydration from work state the happy path, with prompting as a
   derived compatibility layer.
4. Separated canonical `history search` from future adapter-native history
   import.
5. Connected the work runtime directly to deterministic documentation
   generation.

## What To Do Next

1. Implement the minimum `work` data model.
2. Add worker-safe read/update commands for work state.
3. Attach jobs and sessions to work items.
4. Add attestation and discovery records as first-class objects.
5. Render deterministic doc views from work state and linked artifacts.

## 1) Product Direction

`cagent` is becoming a runtime for coding-agent work.

The runtime should unify:
- heterogeneous coding-agent interfaces,
- long-running and resumable execution,
- cross-adapter failover,
- durable work coordination,
- attestation and approval,
- documentation as a projection over current state.

This does not make `cagent` a hosted workflow SaaS or a general distributed
workflow engine. It remains a local machine runtime. The expansion is semantic,
not infrastructural.

## 2) Core Principle

### Prompting considered harmful

Prompts are not the source of truth.

They are:
- lossy,
- expensive,
- hard to diff,
- hard to validate,
- easy to drift from actual system state.

The happy path is:
1. a worker is launched,
2. the worker hydrates from durable work state,
3. the worker acts,
4. the worker publishes progress back into work state.

Prompts still exist, but as compiled views over runtime state, not as the
primary control plane.

## 3) Runtime Framing

`cagent` is one integrated runtime.

It is not best modeled as a collection of durable actors with hidden private
mailboxes. A more Go-shaped framing is:
- work items are durable structs in storage,
- worker sessions are transient processors,
- updates/notes/discoveries are durable messages,
- claims are leases,
- summaries/projections are materialized views,
- adapters are execution backends.

The runtime remembers. Workers consult and mutate. Adapters execute.

## 4) Work As The Primary Abstraction

Jobs are execution-shaped.
Work is orchestration-shaped.

One work item may involve:
- one or more research jobs,
- implementation jobs,
- attestation-producing jobs,
- review jobs,
- retries,
- child work items,
- debrief and transfer artifacts.

That makes `work` the natural object for:
- identity,
- continuity,
- attestation,
- documentation,
- coordination,
- recovery.

## 5) Core Work Objects

### 5.1 Work Item

A durable unit of responsibility.

Minimum fields:
- `work_id`
- `title`
- `objective`
- `kind`
- `status`
- `phase`
- `parent_work_id`
- `retry_of_work_id`
- `current_job_id`
- `current_session_id`
- `acceptance_criteria`
- `required_capabilities`
- `preferred_adapters`
- `forbidden_adapters`
- `configuration_class`
- `budget_class`
- `metadata`

### 5.2 Work Update

Structured progress emitted by workers.

Examples:
- phase started
- implementation completed
- attestation failed
- blocked on dependency
- ready for review

Minimum fields:
- `update_id`
- `work_id`
- `ts`
- `status`
- `phase`
- `message`
- `job_id`
- `session_id`
- `artifact_id`
- `metadata`

### 5.3 Work Note

Unstructured but durable commentary.

Examples:
- verifier feedback
- operator override
- review finding
- coordination reminder

Notes are comments. Updates are structured progress.

### 5.4 Work Discovery

A proposed new work item discovered during execution.

Discovered work should not immediately become accepted work. It first exists as
a proposal to be triaged in context of the larger system state.

### 5.5 Attestation Record

Explicit evidence-bearing claim that a result, artifact, document, or proposal
was checked.

Execution completion is not approval. Attestation and approval must be
first-class and attributable.

### 5.6 Claims

Leases over work items or native sessions.

If a worker dies, the work remains. Another worker can continue.

## 6) Execution State vs Approval State

These should remain separate.

Execution state:
- `queued`
- `in_progress`
- `blocked`
- `failed`
- `done`

Approval state:
- `pending`
- `verified`
- `rejected`
- `approved`

This matters because "finished" and "accepted" are not the same thing.

## 7) Work Graph Semantics

The graph should be over work, not over sessions or jobs.

Sessions and jobs are execution traces.
Work nodes are the durable semantic units.

The runtime needs:
- durable work nodes,
- explicit typed edges,
- deterministic projections such as `ready`, `blocked`, and `awaiting_approval`,
- agentic participation in shaping the graph through explicit API operations.

### 7.1 Nodes

The primary node type is the work item.

Useful initial kinds:
- `plan`
- `research`
- `implement`
- `verify`
- `review`
- `red_team`
- `doc`
- `recovery`

Kinds should stay open-ended. They are semantic hints, not a fixed ontology.

### 7.2 Edges

The initial edge set should stay small and explicit:

- `parent_of`
  - hierarchy and scope grouping only
- `blocks`
  - hard prerequisite for readiness
- `verifies`
  - approval/attestation relationship
- `discovered_from`
  - lineage without blocking
- `supersedes`
  - replacement lineage for retries or plan rewrites
- `relates_to`
  - weak informational relationship

Parent-child should not imply sequence.
Children are parallel by default. If order matters, use explicit `blocks`
edges.

### 7.3 Ready Semantics

A work item is ready when:
- it is not terminal,
- all incoming `blocks` edges are satisfied,
- it is not currently claimed,
- any parent-level policy permits execution,
- its required capabilities are satisfiable by at least one available worker.

`ready` is the most important projection in the graph. It is the machine answer
to "what can run now?"

### 7.4 Discovery Semantics

Discovered work should begin as a proposal, not as automatically accepted work.

That keeps agents from polluting the graph with speculative scope expansion.
It also lets a coherence or triage flow judge new work in context of:
- current objectives,
- existing graph state,
- duplicate or overlapping work,
- budget and risk,
- current documentation and attestation state.

## 8) Graph Mutation Model

The graph itself should remain deterministic.
Interaction with the graph should be agentic.
Governance of graph changes should itself be explicit work.

This produces three operation classes.

### 8.1 Direct Mutations

Cheap, routine mutations that a worker can perform directly:
- claim or release work,
- update status or phase,
- append a work update,
- add a note,
- attach an artifact,
- mark blocked,
- mark done,
- mark failed,
- create a discovered-work proposal stub,
- create child work when explicitly allowed by local policy.

These are execution-shaped operations.

### 8.2 Proposal-Based Structural Edits

Changes that reshape the graph should be explicit proposals:
- split one work item into many,
- merge duplicate work,
- add or remove `blocks` edges,
- add or remove `verifies` edges,
- materially change acceptance criteria,
- supersede a plan subtree,
- promote discovered work into tracked work,
- reparent work,
- rewrite dependency structure.

These are graph-governance operations, not ordinary execution updates.

### 8.3 Approval-Gated Changes

Some proposals should require attestation or explicit approval before
application:
- material scope expansion,
- deletion or supersession of accepted work,
- changes to approval or attestation policy,
- removal of verifier or reviewer requirements,
- root-objective reframing,
- changes with budget, security, or release implications.

Execution is direct.
Structure is proposed.
Governance is explicit.

## 9) Planning And Prep Should Be Proportional

Not every task needs a full spec, plan, guide, implementation plan, and review
packet before any work begins.

The amount of preamble should scale with task hardness.

### 9.1 Low-Hardness Work

Examples:
- small bugfixes,
- narrow doc edits,
- obvious local refactors,
- quick probes,
- targeted attestation.

Preferred approach:
- create or claim the work,
- hydrate thinly,
- execute,
- publish updates,
- verify if needed.

For this class of work, heavy ceremony is counterproductive.

### 9.2 Medium-Hardness Work

Examples:
- feature slices,
- interface changes,
- multi-file implementation work,
- migration steps,
- adapter additions.

Preferred approach:
- short plan,
- explicit acceptance criteria,
- maybe a checklist artifact,
- implementation,
- attestation,
- review if risk warrants it.

### 9.3 High-Hardness Work

Examples:
- architectural shifts,
- new subsystems,
- safety or security sensitive work,
- large migrations,
- graph or runtime model changes,
- release-critical coordination.

Preferred approach:
- explicit spec,
- clear plan,
- linked acceptance and attestation requirements,
- implementation phases,
- attestation and review,
- deterministic reporting or documentation projection.

The important rule is not "always do less prep" or "always do more prep."
It is:

- do the minimum preparation that makes the next irreversible step legible,
- and increase ceremony only when complexity, uncertainty, or risk justifies it.

## 10) Worker Hydration

Workers should hydrate from runtime state, not from giant bespoke prompts.

Hydration inputs:
- work item
- latest updates
- acceptance criteria
- required capabilities
- notes
- linked artifacts
- recent attestation findings
- child work status
- recent relevant history

Hydration modes:

### Thin hydration

Default path for ordinary work.

Includes:
- objective
- current phase
- current blockers
- key artifacts
- most recent notes and attestation result

### Deep hydration

For complex, recovery-heavy, or system-coherence work.

Includes:
- broader work graph state
- relevant recent history
- rolling summaries
- unresolved issues
- significant artifacts
- current frontier of active work

Deep state should always exist in the runtime even when only a compact view is
injected into a model context window.

## 11) Worker API

Workers should interact with the runtime directly through a work API exposed by
the `cagent` CLI.

Worker-safe read operations:
- `work show`
- `work notes`
- `work artifacts`
- `work children`
- `work status`
- `history search`

Worker-safe update operations:
- `work update`
- `work note add`
- `work block`
- `work complete`
- `work fail`
- `work discover`

Host- or supervisor-oriented operations:
- `work create`
- `work retry`
- `work claim`
- `work approve`
- `work reject`
- `work list`

The key point is that a running worker does not need hidden prompt injection to
stay current. It can read and write the work API directly.

## 11.1 Stable Worker Briefing

Workers should not start from bespoke handwritten prompts.

They should start from a compiled, versioned worker briefing generated from
runtime state. The stable contract is documented in
[docs/cagent-worker-briefing-schema.md](/Users/wiz/cagent/docs/cagent-worker-briefing-schema.md)
and the schema lives at
[schemas/worker-briefing.schema.json](/Users/wiz/cagent/schemas/worker-briefing.schema.json).

That briefing is the adapter-independent hydration contract. Natural-language
prompt rendering is downstream of it.

## 12) Dynamic Requirements, Not Predefined Worker Profiles

Avoid predefined worker profiles as a core concept.

Instead:
- work declares requirements,
- workers advertise capabilities,
- claim/assignment matches the two.

Examples:
- E2E browser attestation may require `browser`, `multimodal`, and `tool_use`.
- Cheap text research may require only `web` and `tool_use`.
- Recovery summarization may prefer high context and strong synthesis.

This keeps the runtime dynamic and prevents early overfitting to named worker
roles.

## 13) Canonical History vs Adapter Import

General case:
- `cagent history search` over canonical sessions, jobs, turns, events, and
  artifacts that `cagent` already owns.

Special case:
- adapter-native history import for sessions that were never launched through
  `cagent`.

The search path should be built on canonical history first. Native import is an
extension, not the foundation.

## 14) Documentation Bridge

Filesystem Markdown is useful, but deeply flawed as the final system of record.

Its failure modes:
- truth split across parallel notes,
- stale summaries,
- no direct tie to execution or attestation,
- weak semantics around lifecycle,
- hard-to-enforce canonicality.

But the filesystem-doc era was necessary. It let the system discover its own
conceptual shape.

The bridge is:
- work state becomes the durable semantic layer,
- artifacts hold human-readable intermediate material,
- Markdown docs become deterministic projections of current work state.

This means future docs can be rendered from:
- work graph state,
- accepted decisions,
- attestation records,
- current summaries,
- linked artifacts,
- active lifecycle bucket.

In other words:
- Markdown was the bootstrap medium,
- work becomes the semantic medium,
- generated docs become the readable surface.

## 15) Relationship To Existing Docs Systems

Many repositories already use Markdown docs as a lifecycle-organized knowledge
system with:
- an entrypoint,
- current truth,
- proposal space,
- execution evidence,
- archive/history.

`cagent work` should not replace that intent.

It should absorb the semantics behind it:
- "current truth" maps to accepted work state and approved artifacts,
- "proposal space" maps to draft work and discoveries,
- "execution evidence" maps to updates, logs, attestations, and state artifacts,
- "archive" maps to completed and superseded work history.

This lets documentation stay useful for humans while its semantic backbone
moves into the runtime.

## 16) Minimum Viable Command Surface

The first meaningful `work` layer likely needs:
- `work create`
- `work show`
- `work list`
- `work update`
- `work note add`
- `work notes`
- `work complete`
- `work fail`
- `work block`
- `work children`
- `work retry`
- `run --work`
- `send --work`
- `artifacts list --work`

Future additions:
- `work claim`
- `work claim-next`
- `work release`
- `work approve`
- `work reject`
- `work discover`
- `work ready`
- `work updates`
- `work projection`
- `work propose`
- `work proposal accept`
- `work proposal reject`

## 17) Bash-Orchestrated Shape

The shell remains the orchestration language.
The runtime owns the durable semantics.

Bash should keep:
- sequencing,
- fanout/fanin,
- branching,
- retries,
- policy.

`cagent` should own:
- work identity,
- updates,
- notes,
- discovery,
- attestation,
- history,
- claims,
- projections.

This preserves the current bash-friendly shape while giving the system a much
stronger substrate.

## 18) Product Statement

`cagent` is becoming a local runtime that turns coding agents from isolated
chat/process silos into one durable work system.

The central feature is not prompting.
The central feature is work.

Everything else is a way of executing, inspecting, verifying, recovering, and
documenting work.
