Date: 2026-03-15
Kind: Architecture decision
Status: Proposed
Priority: 2
Requires: []

## ADR-0001: Dolt-Versioned Work Graph with Doc Content

### Context

cagent's public work graph is currently SQLite tracked in git. This has three problems:

1. **No meaningful diffs** — git sees the DB as an opaque binary blob
2. **No concurrent agent isolation** — single-writer lock, no branching
3. **Docs and work items are separate** — docs are files on disk, work items are DB rows, they drift independently

### Decision

Migrate the public work graph from SQLite to Dolt (embedded) in four phases. Keep the private DB as SQLite.

### Phases

**Phase 1: Doc Content in SQLite (immediate)**
- Add `doc_content` table linking work items to their source doc text
- Store full markdown doc bodies alongside work items
- Mind-graph detail panel renders actual doc content
- No new dependencies — pure schema change
- Exit criteria: focused work item shows its full doc in the UI

**Phase 2: Dolt Prototype (next)**
- Prototype `github.com/dolthub/driver` embedded on a branch
- Benchmark startup time, point-read latency, write latency, binary size
- Validate that MySQL dialect migration is tractable
- Exit criteria: same test suite passes against Dolt, with measured performance numbers

**Phase 3: Dolt Migration (if Phase 2 validates)**
- Migrate public DB from SQLite to Dolt
- Branch-per-agent workflow: supervisor creates a branch per dispatched job
- `DOLT_COMMIT` after every work+doc transaction
- Merge agent branches back to main on job completion
- Conflict resolution: orchestrator-decides policy
- Exit criteria: concurrent agents produce mergeable, auditable work

**Phase 4: Global Knowledge Base (choir-on-choir)**
- Dolt push/pull to DoltHub for ADR-0027 promotion pipeline
- Per-user cagent → global DoltDB via subgraph promotion
- Merge-based publishing with review/approval
- Exit criteria: work promoted from local cagent appears in global KB

### Tradeoffs

**What we gain:**
- Version history with cell-level diffs (not binary blob)
- Branch-per-agent isolation with 3-way merge
- Doc content versioned alongside work graph (single source of truth)
- Historical queries (`AS OF`, `dolt_history_*`)
- Push/pull to DoltHub for remote collaboration
- Full-text search on doc content

**What we lose:**
- Binary size: 12MB → est. 40-70MB
- Build simplicity: pure Go → requires cgo + C toolchain
- Startup time: near-instant → additional query engine init
- Storage format: single file → directory tree

**Mitigation:** Phase 2 prototype measures actual costs before committing.

### Architecture

```
.cagent/
  workgraph/           # Dolt database (Phase 3+)
    .dolt/             # Dolt version control
  cagent.db            # SQLite public DB (Phase 1-2, deprecated Phase 3)
  cagent-private.db    # SQLite private DB (always, never versioned)
```

Private DB stays SQLite permanently — no reason to version-control secrets.

### References

- Dolt: https://github.com/dolthub/dolt
- Embedded Go driver: https://github.com/dolthub/driver
- ChoirOS ADR-0027: Publishing and Global Knowledge Base
- Dolt latency benchmarks: ~1.35x slower than MySQL on reads, ~1.3x faster on writes
- At cagent's scale (100s of work items), performance difference is imperceptible
