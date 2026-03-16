Date: 2026-03-15
Kind: Architecture decision
Status: Accepted
Priority: 1
Requires: []

## ADR-0002: Documentation Before Execution

### Context

Development work follows a pattern: think → plan → implement → verify. The documentation of intent (planning) and the execution of that intent (implementation) are often entangled — docs are written after the fact, or plans exist only in conversation context that expires.

### Decision

Documentation commits before execution commits. A plan is a committed doc. Implementation follows.

### Rules

1. **Plans are committed docs.** Before executing a multi-step change, document the plan as a committed file (ADR, spec, checklist). The doc commit is separate from and precedes the implementation commit.

2. **Docs are safe to commit speculatively.** A proposed ADR can be committed without implementing it. Multiple proposed ADRs can coexist and be co-designed. The cost of a doc is low; the cost of undocumented intent is high.

3. **Asynchrony between docs and execution is expected.** A doc may describe work that won't happen for weeks. That's fine — the doc captures intent at the moment it's clear, decoupled from when resources are available to execute.

4. **The work graph mirrors the doc state.** Each committed doc has a corresponding work item. The work item's execution state reflects whether the doc's intent has been implemented (draft → ready → running → done). The doc and work item evolve together.

5. **Attestation verifies doc-code consistency.** When work is completed, an attestation records whether the doc accurately describes the implemented state. Drift between docs and code is a verification failure.

### Why

- Docs capture intent when it's freshest, not when implementation is done
- Multiple plans can be co-designed before committing to implementation order
- Agents can read docs to understand intent without needing conversation context
- The work graph provides execution tracking on top of the doc layer
- Version control of docs provides an audit trail of evolving design decisions

### Anti-patterns

- Writing docs after implementation (loses the intent/rationale)
- Keeping plans in conversation context only (expires, can't be shared)
- Blocking doc commits on implementation readiness (artificially couples them)
- Treating docs as secondary artifacts (they are the primary expression of intent)
