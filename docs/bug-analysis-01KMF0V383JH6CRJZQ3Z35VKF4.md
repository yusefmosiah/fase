# Bug Analysis: SQLite Corruption, Serve Crashes, MCP Proxy Hangs

Work ID: `work_01KMF0V383JH6CRJZQ3Z35VKF4`
Date: 2025-06-27

---

## BUG 1 — SQLite DB Corruption

### Root Cause: Detached workers open their own DB connections (concurrent writers)

**Evidence:**

1. **`__run-job` opens its own SQLite connection** (`internal/cli/root.go:2437`):
   ```go
   svc, err := service.Open(context.Background(), root.configPath)
   ```
   Every detached worker spawned by `launchDetachedWorker` (`internal/service/service.go:4770`) runs `fase __run-job --job <id> --turn <id>`, which calls `service.Open()` and opens a **second** `*sql.DB` connection to the same `fase.db` file.

2. **`launchDetachedWorker` does NOT set `cmd.Dir`** (`internal/service/service.go:4791-4797`):
   ```go
   cmd := exec.Command(exePath, args...)
   cmd.Stdout = devNull
   cmd.Stderr = devNull
   cmd.Stdin = devNull
   // NO cmd.Dir = ...
   ```
   The worker inherits serve's CWD (the main repo root), so `ResolvePathsForRepo()` resolves to the **same** `.fase/fase.db` as serve.

3. **WAL mode is properly configured** (`internal/store/store.go:2632`):
   ```go
   `PRAGMA journal_mode = WAL;`,
   `PRAGMA busy_timeout = 30000;`,
   ```
   WAL allows concurrent readers but **not** safe concurrent writers without application-level coordination. The `busy_timeout = 30000` (30s) handles short contention but under sustained concurrent writes (e.g., multiple workers updating jobs, events, and work items simultaneously), WAL can still produce corruption.

4. **`fase mcp stdio` also opens its own DB** (`internal/cli/mcp.go:34`):
   ```go
   svc, err := service.Open(context.Background(), root.configPath)
   ```
   When Claude Code uses `fase mcp stdio` directly (not via proxy), it creates yet another writer.

5. **CLI fallback creates additional writers** (`internal/cli/root.go:993`):
   ```go
   if c, serveErr := connectServe(); serveErr == nil {
       // route through serve
   }
   svc, err := service.Open(context.Background(), root.configPath)
   // fallback: direct DB access
   ```
   If serve is briefly unreachable, CLI commands fall back to direct DB writes.

6. **Physical evidence confirms corruption** — 4+ `fase-corrupt*.db` files in the OSINT project:
   ```
   ~/fase-evals/osint/.fase/fase-corrupt3.db  (1.9MB, Mar 23)
   ~/fase-evals/osint/.fase/fase-corrupt4.db  (7.7MB, Mar 23)
   ```
   Worktree directories also have their own `fase-corrupt*.db` files, confirming that workers in worktrees also experience corruption.

7. **The OSINT project's own `osint.db` and `graph.db` are separate files** — they do NOT conflict with fase's SQLite.

8. **serve does NOT check for existing `serve.json` before starting** (`internal/cli/serve.go:264`):
   ```go
   _ = os.WriteFile(servePath, serveJSON, 0o644)
   ```
   If serve crashes and restarts quickly (or if a stale `serve.json` from a previous run isn't cleaned up due to a crash), two serve instances could write to the same DB. The `defer os.Remove(servePath)` only runs on clean shutdown.

### Why WAL isn't enough

WAL mode serializes writes through the WAL file. With `MaxOpenConns(1)`, each process serializes its own writes. But when **multiple processes** each hold `MaxOpenConns(1)` connections, they compete for the database lock. The `busy_timeout` retries for 30s, but:
- Under high write throughput (events, job updates, work item state changes), the 30s timeout can be exceeded
- `modernc.org/sqlite` (pure Go SQLite) may have different locking behavior than C SQLite
- WAL checkpoint operations under concurrent write pressure can cause corruption

### Specific Fixes

**Fix 1a: Route `__run-job` through serve's HTTP API instead of direct DB access**

The `__run-job` command should call serve's API instead of opening its own DB. This eliminates the most frequent source of concurrent writers.

```go
// In newInternalRunJobCommand:
func(cmd *cobra.Command, args []string) error {
    // Instead of service.Open(), use serve's HTTP API
    c, err := connectServe()
    if err != nil {
        return err  // require serve to be running
    }
    // POST to /api/job/execute or similar
}
```

**Fix 1b: Add stale serve.json detection on startup**

```go
// In runServe, before writing serve.json:
if data, err := os.ReadFile(servePath); err == nil {
    var existing serveInfo
    if json.Unmarshal(data, &existing) == nil && existing.PID > 0 {
        if syscall.Kill(existing.PID, 0) == nil {
            return fmt.Errorf("fase serve is already running (pid %d)", existing.PID)
        }
    }
}
```

**Fix 1c: Set `_SQLITE_JOURNAL_MODE=WAL` and `_SQLITE_BUSY_TIMEOUT` as URI params**

For belt-and-suspenders, embed WAL mode in the DSN:
```go
db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=30000")
```

**Fix 1d: Add WAL checkpoint on serve startup**

```go
// In bootstrap(), after PRAGMA statements:
s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE);")
```

---

## BUG 2 — Serve Crash Loop

### Root Cause: No panic recovery + silent supervisor restart

**Evidence:**

1. **No panic recovery in `runServe`** (`internal/cli/serve.go`):
   The function has no `defer func() { recover() }()`. Any panic in any goroutine crashes the process.

2. **The supervisor goroutine has auto-restart with no error logging** (`internal/cli/serve.go:340-354`):
   ```go
   for {
       sup.run(ctx)
       if ctx.Err() != nil {
           return // serve shutting down
       }
       sup.log("restart", fmt.Sprintf("supervisor exited — restarting in %s", restartDelay))
       select {
       case <-ctx.Done():
           return
       case <-time.After(restartDelay):
       }
   }
   ```
   When `sup.run(ctx)` returns due to an error (not a panic), it logs "supervisor exited" but the actual error from `sup.run()` is **discarded**. The restart loop has no error logging.

3. **`restartAfterDelay` launches a competing goroutine** (`internal/cli/supervisor_agent.go`):
   ```go
   func (s *agenticSupervisor) restartAfterDelay(ctx context.Context, ch chan service.WorkEvent) {
       timer := time.NewTimer(10 * time.Second)
       defer timer.Stop()
       select {
       case <-ctx.Done():
       case <-timer.C:
           go s.run(ctx)  // <-- launches NEW goroutine
       }
   }
   ```
   When called from within `s.run(ctx)`, this launches `go s.run(ctx)` in a new goroutine. Then `s.run(ctx)` returns, which causes the serve.go restart loop to call `sup.run(ctx)` again. Now **two** `run()` calls execute concurrently, both trying to start supervisor sessions, dispatch work, etc.

4. **"shutting down... stopped" without error** — This is the normal SIGINT/SIGTERM path. If the user (or a terminal multiplexer) sends SIGINT, serve shuts down cleanly. The "no error" part is because `runServe` returns `nil` after clean shutdown. This isn't a bug per se — it's expected behavior that's confusing because the real error (why the signal was sent) isn't logged.

5. **Housekeeping goroutine can panic** (`internal/cli/serve.go:runHousekeeping`):
   The housekeeping goroutine reads directories, queries the DB, and publishes events. Any nil pointer dereference or unexpected DB error could panic this goroutine, crashing the entire process.

### Specific Fixes

**Fix 2a: Add panic recovery to `runServe`**
```go
func runServe(...) error {
    defer func() {
        if r := recover(); r != nil {
            fmt.Fprintf(cmd.ErrOrStderr(), "FATAL: serve panicked: %v\n", r)
            debug.PrintStack()
        }
    }()
    // ... existing code
}
```

**Fix 2b: Log supervisor restart errors**
```go
for {
    err := sup.run(ctx)
    if ctx.Err() != nil {
        return
    }
    sup.log("restart", fmt.Sprintf("supervisor exited: %v — restarting in %s", err, restartDelay))
    // ...
}
```
This requires `sup.run(ctx)` to return an error instead of `func()`.

**Fix 2c: Fix `restartAfterDelay` to not launch a competing goroutine**

The `restartAfterDelay` should signal the serve.go restart loop to restart, not launch its own goroutine. One approach: have `run()` return a sentinel error that the serve.go loop interprets as "restart immediately":

```go
var errRestartImmediately = errors.New("restart immediately")

func (s *agenticSupervisor) restartAfterDelay(...) {
    // Just return — let the serve.go loop handle restart
    return  // run() returns errRestartImmediately
}
```

**Fix 2d: Add per-goroutine panic recovery**

Wrap each goroutine's function body:
```go
go func() {
    defer func() {
        if r := recover(); r != nil {
            sup.log("panic", fmt.Sprintf("housekeeping panicked: %v", r))
        }
    }()
    runHousekeeping(ctx, svc, cwd, hub, sup, mcpServer)
}()
```

---

## BUG 3 — MCP Proxy Hangs

### Root Cause: Scanner token limit + no response timeout + mutex contention

**Evidence:**

1. **bufio.Scanner default max token size is 64KB** (`internal/cli/mcp.go:relaySSEToStdout`):
   ```go
   func relaySSEToStdout(body io.Reader) {
       scanner := bufio.NewScanner(body)
       for scanner.Scan() {
           // ...
       }
   }
   ```
   If any SSE `data:` line exceeds 64KB (e.g., a large tool result from `fase work hydrate` or `fase project hydrate`), the scanner fails with `token too long` and stops reading. The error is **silently swallowed** — the function just returns without logging or propagating the error.

2. **No HTTP client timeout** (`internal/cli/mcp.go:runMCPProxy`):
   ```go
   client := &http.Client{}
   ```
   If serve's `/mcp` endpoint takes a long time (e.g., a tool call that runs `bash` for 30+ seconds), the proxy blocks indefinitely. The SSE response stream keeps the connection open.

3. **Mutex held during entire SSE relay** (`internal/cli/mcp.go:runMCPProxy`):
   ```go
   proxyStdoutMu.Lock()
   if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
       relaySSEToStdout(resp.Body)  // blocks until SSE stream ends
   }
   proxyStdoutMu.Unlock()
   ```
   While the SSE stream is being relayed, the WebSocket goroutine cannot write channel notifications to stdout. This doesn't cause a deadlock (the WebSocket goroutine just blocks), but it delays notifications.

4. **The real hang scenario**: When Claude Code calls an MCP tool that triggers a long-running operation (like `bash`), serve sends the initial SSE events (tool start), then waits for the operation to complete before sending the final SSE event. The proxy's `relaySSEToStdout` blocks reading from the HTTP response body, which blocks the main goroutine from reading the next stdin line. Claude Code is waiting for the tool response, and the proxy is waiting for serve to send it. If serve crashes or stalls, the proxy hangs forever.

5. **No session ID validation on reconnect**: If the proxy restarts (e.g., serve restarts), the `sessionID` is cleared (it's a local variable), so subsequent requests create a new session. This is correct behavior but means state can be lost.

### Specific Fixes

**Fix 3a: Increase scanner buffer size**
```go
func relaySSEToStdout(body io.Reader) {
    scanner := bufio.NewScanner(body)
    scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024) // 10MB
    // ...
}
```

**Fix 3b: Add HTTP client timeout**
```go
client := &http.Client{
    Timeout: 5 * time.Minute,  // overall request timeout
}
```
Note: For SSE streams, the timeout should be on the initial connection, not the response body reading. Use `http.Client{}` with a custom transport that sets `DialContext` timeout, and use `context.WithTimeout` for the request.

**Fix 3c: Log scanner errors**
```go
func relaySSEToStdout(body io.Reader) error {
    scanner := bufio.NewScanner(body)
    scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024)
    for scanner.Scan() {
        // ...
    }
    if err := scanner.Err(); err != nil {
        fmt.Fprintf(os.Stderr, "mcp proxy: SSE scan error: %v\n", err)
        return err
    }
    return nil
}
```

**Fix 3d: Release mutex during SSE relay, re-acquire for final write**
```go
// Don't hold mutex during entire SSE relay
// Instead, relay SSE lines individually with short mutex holds
```
Actually, the current approach is fine for correctness — the mutex prevents interleaved output. The issue is that long SSE streams block notifications. A better fix is to use an unbuffered channel to sequence writes:

```go
type stdoutWriter struct {
    ch chan []byte
}

func (w *stdoutWriter) Write(data []byte) {
    w.ch <- data
}

func stdoutMux(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case data := <-ch:
            os.Stdout.Write(data)
        }
    }
}
```

---

## Summary Table

| Bug | Root Cause | Severity | Fix Complexity |
|-----|-----------|----------|---------------|
| DB Corruption | `__run-job` opens 2nd writer to same DB | Critical | Medium — route through serve API |
| Serve Crash | No panic recovery + supervisor goroutine leak | High | Low — add defer/recover |
| MCP Proxy Hang | Scanner buffer limit + no timeout | Medium | Low — increase buffer + add timeout |
