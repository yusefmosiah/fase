# Second Opinion RCA: `work_01KMF0VNA1QGHSSZ6JP35CZTGB`

## Scope

Reviewed:

- [internal/cli/serve.go](/Users/wiz/cogent/internal/cli/serve.go)
- [internal/cli/mcp.go](/Users/wiz/cogent/internal/cli/mcp.go)
- [internal/cli/root.go](/Users/wiz/cogent/internal/cli/root.go)
- [internal/service/service.go](/Users/wiz/cogent/internal/service/service.go)
- [internal/store/store.go](/Users/wiz/cogent/internal/store/store.go)
- OSINT state under `~/cogent-evals/osint/.cogent/`
- Prior RCA in [docs/rca-three-bugs.md](/Users/wiz/cogent/docs/rca-three-bugs.md)

## Findings

### 1. DB corruption: the real single-writer violation is broader than `__run-job`

I agree with the earlier RCA that detached workers bypass the "serve is the writer" design. `__run-job` opens a fresh service and store in its own process at [internal/cli/root.go:2426](/Users/wiz/cogent/internal/cli/root.go:2426) and [internal/cli/root.go:2438](/Users/wiz/cogent/internal/cli/root.go:2438), then runs `ExecuteDetachedJob`, which updates job runtime and executes the lifecycle directly at [internal/service/service.go:4735](/Users/wiz/cogent/internal/service/service.go:4735). That means worker subprocesses do write the same SQLite files independently of serve.

But the bigger miss in the first RCA is that `serve` itself also permits multiple long-lived writer processes on the same state dir:

- `runServe` binds the requested port, but if it is occupied it silently falls back to port `0` instead of refusing to start at [internal/cli/serve.go:239](/Users/wiz/cogent/internal/cli/serve.go:239) through [internal/cli/serve.go:248](/Users/wiz/cogent/internal/cli/serve.go:248).
- The replacement instance then overwrites `.cogent/serve.json` unconditionally at [internal/cli/serve.go:251](/Users/wiz/cogent/internal/cli/serve.go:251) through [internal/cli/serve.go:265](/Users/wiz/cogent/internal/cli/serve.go:265).

That behavior is visible in the OSINT repo right now:

- `~/cogent-evals/osint/.cogent/serve.json` was last written at `2026-03-24 00:23:14` with dead PID `22475` on port `60641`.
- A different `cogent serve --auto --port 4244` process is still alive as PID `74443`, started at `Tue Mar 24 00:13:01 2026`, with cwd `/Users/wiz/cogent-evals/osint`.

So the OSINT evidence shows two separate serve lifecycles touching the same repo state. That is a stronger confirmed breach of the single-writer assumption than the detached worker path alone.

I also reproduced the duplicate-serve behavior locally with the current code: starting two `serve` processes on the same requested port left both alive, with the second one auto-moving to an ephemeral port while both opened the same SQLite files.

Important correction to the first RCA: the store is already in WAL mode at [internal/store/store.go:2628](/Users/wiz/cogent/internal/store/store.go:2628) through [internal/store/store.go:2633](/Users/wiz/cogent/internal/store/store.go:2633), and the pinned `modernc.org/sqlite` changelog explicitly says the driver added support for concurrent access by multiple goroutines and processes. So "a second connection exists" is not, by itself, a sufficient root cause. The codebase bug is that Cogent violates its own single-writer architecture in multiple places and allows overlapping long-lived writers with no leadership lock.

My conclusion: the corruption root cause is architectural writer fan-out, with duplicate serve instances being the strongest confirmed trigger in the OSINT evidence and detached workers being an additional contributing path.

### 2. Serve "crash-loop with no error" is more consistent with external SIGTERM or duplicate-serve churn than with a hidden panic

The earlier RCA focuses on unhandled panics in goroutines. That is a real hardening gap: the housekeeping, change-watcher, and supervisor goroutines are launched without recovery at [internal/cli/serve.go:345](/Users/wiz/cogent/internal/cli/serve.go:345) through [internal/cli/serve.go:378](/Users/wiz/cogent/internal/cli/serve.go:378).

But I do not think that is the best primary explanation for the observed symptom.

Why:

- The exact `"cogent serve: shutting down..."` and `"cogent serve: stopped"` strings are only printed on the signal-handling path at [internal/cli/serve.go:409](/Users/wiz/cogent/internal/cli/serve.go:409) through [internal/cli/serve.go:422](/Users/wiz/cogent/internal/cli/serve.go:422).
- A panic in one of those goroutines would not flow through that shutdown banner.
- The OSINT repo currently shows a live older serve process and a newer stale `serve.json`, which means serve lifecycle/discovery is already confused by overlapping instances.

That makes the observed "stops with no error" symptom look more like:

- an external launcher or operator sending SIGTERM and immediately starting another serve, or
- a second serve instance being started, taking over discovery, and then dying, while the original serve keeps running.

I would still add panic recovery, but only as defense in depth. The stronger root cause for the silent-loop symptom is missing serve-instance exclusivity and the resulting lifecycle confusion.

### 3. MCP proxy hangs: the proxy is not a real Streamable HTTP client

The first RCA correctly flags two concrete bugs in the proxy:

- it uses an `http.Client{}` with no timeout at [internal/cli/mcp.go:96](/Users/wiz/cogent/internal/cli/mcp.go:96) through [internal/cli/mcp.go:97](/Users/wiz/cogent/internal/cli/mcp.go:97)
- it holds `proxyStdoutMu` while reading the full response body at [internal/cli/mcp.go:141](/Users/wiz/cogent/internal/cli/mcp.go:141) through [internal/cli/mcp.go:154](/Users/wiz/cogent/internal/cli/mcp.go:154)

Those are real problems.

But the deeper issue is that `cogent mcp proxy` is not implementing the go-sdk transport model that the server is using:

- The serve endpoint is a `StreamableHTTPHandler` at [internal/cli/serve.go:280](/Users/wiz/cogent/internal/cli/serve.go:280) through [internal/cli/serve.go:284](/Users/wiz/cogent/internal/cli/serve.go:284).
- The SDK transport manages session streams and hanging HTTP requests, not an app-specific `/ws` side channel. See the upstream transport implementation at `/Users/wiz/go/pkg/mod/github.com/modelcontextprotocol/go-sdk@v1.4.1/mcp/streamable.go:687` and `/Users/wiz/go/pkg/mod/github.com/modelcontextprotocol/go-sdk@v1.4.1/mcp/streamable.go:776`.
- Our proxy instead invents a separate `/ws` connection at [internal/cli/mcp.go:99](/Users/wiz/cogent/internal/cli/mcp.go:99) through [internal/cli/mcp.go:105](/Users/wiz/cogent/internal/cli/mcp.go:105) and [internal/cli/mcp.go:192](/Users/wiz/cogent/internal/cli/mcp.go:192) through [internal/cli/mcp.go:309](/Users/wiz/cogent/internal/cli/mcp.go:309).

That mismatch matters because:

- any blocked or slow MCP HTTP request becomes an infinite proxy hang, since there is no timeout and no independent response pump
- stdout is serialized for the entire HTTP body lifetime, so WebSocket notifications are back-pressured behind stalled tool calls
- MCP `notifications/claude/channel` emitted by `mcpserver.SendChannelEvent` are written to the MCP server writer at [internal/mcpserver/server.go:65](/Users/wiz/cogent/internal/mcpserver/server.go:65) through [internal/mcpserver/server.go:81](/Users/wiz/cogent/internal/mcpserver/server.go:81), while the proxy only listens for `channel_message` broadcasts from `/ws`, so the two notification paths are not actually the same transport

My conclusion: the proxy hang is not just a mutex bug. It is a transport-layer design bug, with the mutex and missing timeout turning backend stalls into permanent user-visible hangs.

## Comparison With The First RCA

### I agree with

- `__run-job` does open its own store and does write outside the serve process.
- The proxy's stdout mutex and missing timeout are real bugs.
- Panic recovery on serve goroutines would improve resilience.

### I disagree with or would narrow

- "SQLite WAL allows concurrent readers but not concurrent writers" is too broad for this root cause. SQLite serializes writers, and the pinned driver advertises multi-process support.
- "Serve crash-loop" being primarily an unhandled panic is not supported by the visible `"shutting down... stopped"` symptom. That banner is on the signal path.
- "Add a stale serve.json guard" is incomplete as phrased. The client already rejects stale PIDs when reading `serve.json`; the missing guard is on serve startup, where duplicate instances are currently allowed.

### What the first RCA missed

- The explicit duplicate-serve path: requested-port collision silently creates a second serve instance on a random port and overwrites discovery.
- The OSINT evidence already shows that this happened in practice.
- The MCP proxy is diverging from the SDK transport model, not just mishandling an SSE loop.

## Recommended Fix Order

1. Enforce one serve instance per state dir.
   Refuse startup when an explicit port is already in use by another live serve for the same repo. Do not silently fall back to a random port in that case.

2. Move detached worker writes behind serve or add a real leader lock.
   If serve is the architecture, detached workers should report mutations through serve instead of opening their own stores.

3. Replace the hand-rolled proxy with the SDK client transport, or match its model.
   At minimum: add request timeouts, stop holding the stdout mutex during body reads, and stop substituting `/ws` for the MCP transport's own session stream.

4. Make discovery durable and self-describing.
   Log state dir, pid, and bound port on startup, and refuse to overwrite `serve.json` when another live serve already owns that state dir.

5. Add panic recovery around background goroutines.
   This is still worth doing, but it is not the first fix I would make for these incidents.

## Verification Notes

- Reproduced duplicate serve startup locally with the current code. First process stayed on the requested port; second process silently rebound to an ephemeral port while both remained alive.
- `PRAGMA journal_mode;` on the OSINT DB returns `wal`.
- `PRAGMA integrity_check;` reports corruption for `~/cogent-evals/osint/.cogent/cogent-corrupt3.db`, `~/cogent-evals/osint/.cogent/cogent-corrupt4.db`, and the current `~/cogent-evals/osint/.cogent/cogent.db`.

## Open Questions

- Whether the corruption requires duplicate serves specifically, or whether detached workers alone are sufficient on macOS with the current workload.
- Which external launcher or workflow is spawning the second OSINT serve instance at `00:23`.
- Whether the proxy hangs are usually caused by DB lock/corruption stalls underneath, or by transport mismatch alone.
