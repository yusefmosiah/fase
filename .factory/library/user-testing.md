# User Testing

Testing surface, required testing skills/tools, and resource cost classification.

---

## Validation Surface

**Primary surface:** CLI (terminal commands)

The cogent binary is a CLI tool. Validation involves:
1. Running CLI commands (`cogent --help`, `cogent version`, `cogent work list`, `cogent check create --help`, `cogent work attest --help`)
2. Running build/test validators (`go build ./...`, `go test`, `go vet ./...`)
3. Grepping source code for stale references or structural assertions
4. Reading specific code sections to verify structural changes

**Tools:** Shell command execution (no browser, no TUI framework). All validation is done via direct command execution and output inspection.

**Setup required:** `make install` to build and install the binary to `~/.local/bin/cogent`.

**No auth/login required.** CLI commands work without authentication for local operations.

## Validation Concurrency

**Surface: CLI/shell**
- Each validator instance: ~50 MB (Go binary execution + shell)
- Machine: 16 GB RAM, 8 CPU cores, ~10 GB available
- Max concurrent validators: **5**
- Rationale: CLI execution is extremely lightweight. 5 concurrent shell processes consume <250 MB total. Well within 70% headroom (7 GB).
