Date: 2026-03-16
Kind: Architecture decision
Status: Proposed
Priority: 1

## ADR-0003: cogent as SDLC Framework

### Summary

cogent covers Planning and Implementation strongly, Testing and Review
moderately, and Deployment and Monitoring weakly. The key gaps:

- No `cogent verify` (run tests + record attestation in one step)
- No `cogent review` (generate review attestation from diff)
- No `cogent project readme` (generate README from work graph)
- Supervisor marks done on exit code, not on attestation satisfaction
- Promotion is record-keeping only, no CI trigger

### Lifecycle Coverage

| Phase | Coverage | Key Gap |
|-------|----------|---------|
| Planning | Strong | No milestone abstraction |
| Design | Moderate | No design-phase gate |
| Implementation | Strong | No branch/PR management |
| Testing | Moderate | No cogent verify command |
| Review | Moderate | No diff-based review |
| Deployment | Weak | Promotion is record-only |
| Monitoring | Moderate | No alerting |
| Maintenance | Weak | No staleness detection |

### Priority Implementation

1. `cogent verify` — run tests + record attestation (bridges Testing gap)
2. Supervisor verification loop — dispatch verification after execution, check attestations before marking done
3. `cogent project readme` — template-based README generation with cogent:human fenced blocks
4. Pre-commit chain: readme + atlas + verify --pre-commit

### README Generation Design

Template with fenced human blocks:
- cogent:human — preserved verbatim across regenerations
- cogent:status — work graph state
- cogent:work-graph — top-level items checklist
- cogent:recent — git log + completion events

Triggers: pre-commit (full), on work complete (status section only)

### Verification Framework

Minimum viable: after job completion, check required_attestations.
If unsatisfied, dispatch verification child work. When verification
completes, check parent attestations again. If all pass, set
approval_state to pending. Human approves via mind-graph or CLI.

See full report in cogent work graph for detailed analysis.
