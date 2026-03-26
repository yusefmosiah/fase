# Root Cause Analysis: Three Critical Bugs

## BUG 1 — SQLite DB Corruption

### Root Cause: `__run-job` subprocess opens its own SQLite writer connection

**Evidence:**

1. **`__run-job` opens a direct DB connection** — `internal/cli/root.go:399-412`:
   ```go
   svc, err := service.Open(context.Background(), root.configPath)
   ```
   This opens `*sql.DB` → `store.Open()` → `sql.Open("sqlite", path)`, creating a **second writer process** on the same `.cogent/cogent.db` file.

2. **Serve is also writing concurrently** — `runServe()` in `serve.go` opens `service.Open()` and writes to the same DB (work item updates, job state changes, events, etc.).

3. **SQLite WAL mode allows concurrent readers but NOT concurrent writers.** The `busy_timeout = 30000` PRAGMA handles brief contention, but with two processes actively writing (e.g., subprocess appending events while serve updates work items), WAL gets corrupted.

4. **Corrupt DB files exist as evidence**: `cogent-corrupt3.db` and `cogent-corrupt4.db` in `~/cogent-evals/osint/.cogent/`.

5. **The proxy command's own docs confirm this is a known pattern**: `cogent mcp proxy`'s Long description says:
   > "This avoids the WAL split problem where a separate DB connection sees stale data."

6. **`cogent mcp stdio` has the same problem** — `internal/cli/mcp.go:27-30`:
   ```go
   svc, err := service.Open(context.Background(), root.configPath)
   ```
   This is why `cogent mcp proxy` was created as a workaround, but `__run-job` was never updated.

7. **The store correctly sets `MaxOpenConns(1)` and WAL mode** (`store.go:47-51`), but this only controls the connection pool within a single process. Cross-process writes are not serialized.

### Additional Risk: Private DB

The private DB (`cogent-private.db`) is also opened by both serve and `__run-job` simultaneously. Since `__run-job` calls `OpenWithPrivate()`, it creates its own WAL readers/writers for both DBs.

### Fix

**Option A (recommended)**: Make `__run-job` route all DB writes through serve's HTTP API, similar to how `cogent work update`, `cogent work note-add`, etc. work via `connectServe()`. Add HTTP endpoints for the operations `ExecuteDetachedJob` needs (UpsertJobRuntime, UpdateJob, AppendEvent, etc.). The subprocess would be read-only against the DB and write through serve.

**Option B (simpler, lower confidence)**: Open the DB in read-only mode from `__run-job` and add an HTTP API for the writes it needs. This prevents the second writer entirely.

**Option C (defense in depth)**: Add `_journal_mode = WAL` verification on every store.Open() and add a startup integrity_check. If corruption is detected, automatically recover from the last good WAL checkpoint.

---

## BUG 2 — Serve Crash Loop (Silent Panics)

### Root Cause: No panic recovery in goroutines; auto-restart masks the error

**Evidence:**

1. **No `defer recover()` in any goroutine** — `serve.go:279-300`. The three goroutines (housekeeping, change watcher, supervisor) all run without panic recovery:
   ```go
   go func() {
       defer wg.Done()
       runHousekeeping(ctx, svc, cwd, hub, sup, mcpServer)
   }()
   ```
   If `runHousekeeping` panics (e.g., nil pointer dereference on a job status), the entire process crashes with no error message.

2. **Signal handler prints "shutting down... stopped"** but a panic bypasses this path entirely. The process just dies. Any auto-restart wrapper sees exit code 2 (cobra default for errors) or signal-based exit.

3. **Supervisor goroutine has no panic recovery** — `serve.go:293-304`:
   ```go
   go func() {
       defer wg.Done()
       restartDelay := 10 * time.Second
       for {
           sup.run(ctx)  // if this panics, process dies
           ...
       }
   }()
   ```

4. **Memory leak from worktrees** — `createWorktree()` in `serve.go` creates worktrees but there's no cleanup in `runHousekeeping`. Over many dispatch cycles, orphaned worktrees accumulate. The `mergeWorktree` function does cleanup, but only on success path. Failed dispatches leave orphaned worktrees.

5. **Raw log file accumulation** — `runHousekeeping` reads every file in `.cogent/raw/stdout/job_*/` every 30 seconds. With many completed jobs, this is O(n) I/O per tick, though it doesn't directly leak memory.

### Fix

1. **Add panic recovery to all goroutines in `runServe`**:
   ```go
   go func() {
       defer wg.Done()
       defer func() {
           if r := recover(); r != nil {
               fmt.Fprintf(os.Stderr, "HOUSEKEEPING PANIC: %v\n", r)
               debug.PrintStack()
           }
       }()
       runHousekeeping(ctx, svc, cwd, hub, sup, mcpServer)
   }()
   ```

2. **Add worktree cleanup to `runHousekeeping`**: Iterate `.cogent/worktrees/` and remove any worktree whose corresponding work item is in a terminal state (done/failed/cancelled).

3. **Add a stale-serve.json guard**: Before writing `serve.json`, check if the PID in the existing file is alive. If so, refuse to start (preventing multiple serve instances).

---

## BUG 3 — MCP Proxy Hangs

### Root Cause: stdout mutex deadlock between SSE reading and WebSocket notifications

**Evidence:**

1. **The deadlock pattern** — `internal/cli/mcp.go:115-130`:
   ```go
   proxyStdoutMu.Lock()
   if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
       relaySSEToStdout(resp.Body)  // blocks until body EOF
   } else {
       io.Copy(os.Stdout, resp.Body)
   }
   proxyStdoutMu.Unlock()
   ```

   The mutex is held **while reading the entire HTTP response body**. If the SSE stream doesn't terminate (server hangs or sends keep-alive events without ending), this blocks forever.

2. **WebSocket goroutine needs the same mutex** — `mcp.go:161-165`:
   ```go
   proxyStdoutMu.Lock()
   fmt.Fprintln(os.Stdout, string(notifJSON))
   proxyStdoutMu.Unlock()
   ```

   If the main goroutine holds `proxyStdoutMu` (waiting for SSE EOF), the WebSocket goroutine blocks trying to send `channel_message` notifications. This is a **deadlock**.

3. **No HTTP client timeout** — `mcp.go:98`:
   ```go
   client := &http.Client{}
   ```
   No timeout on requests. If serve hangs processing an MCP request, the proxy hangs forever.

4. **SSE parsing doesn't handle all MCP response formats** — `relaySSEToStdout` only handles `data: ` lines. The go-sdk MCP `StreamableHTTPHandler` may send:
   - Direct JSON responses (non-SSE) for simple tool calls → handled by the else branch
   - SSE with `data: ` lines → handled
   - SSE with empty lines between events → handled (scanner skips them)
   - SSE with `event:` type lines → **not handled, but harmless** (just ignored)
   - SSE that never terminates → **NOT handled** (this is the hang)

5. **No context propagation to response body reading** — The request context (`ctx`) is passed to `http.NewRequestWithContext`, but once the response starts, `relaySSEToStdout` doesn't check `ctx.Done()`. If the context is cancelled (e.g., SIGINT), the proxy doesn't notice until the SSE stream ends.

### Fix

1. **Don't hold the mutex during I/O**. Use a buffered channel:
   ```go
   outCh := make(chan []byte, 64)
   go func() {
       relaySSEToStdout(resp.Body, outCh)
       close(outCh)
   }()
   for data := range outCh {
       proxyStdoutMu.Lock()
       fmt.Fprintln(os.Stdout, string(data))
       proxyStdoutMu.Unlock()
   }
   ```

2. **Add HTTP client timeout**:
   ```go
   client := &http.Client{Timeout: 120 * time.Second}
   ```

3. **Check context cancellation in SSE loop**:
   ```go
   func relaySSEToStdout(body io.Reader, out chan<- []byte) {
       scanner := bufio.NewScanner(body)
       for scanner.Scan() {
           if ctx.Err() != nil {
               return
           }
           line := scanner.Text()
           if strings.HasPrefix(line, "data: ") {
               data := line[6:]
               if data != "" && data != "[DONE]" {
                   out <- []byte(data + "\n")
               }
           }
       }
   }
   ```

4. **Add response body deadline** — After sending a request, set a deadline on the response body based on the tool being called (e.g., 30s for simple tools, 120s for long-running tools).

---

## Summary Table

| Bug | Root Cause | Severity | Fix Complexity |
|-----|-----------|----------|---------------|
| DB Corruption | `__run-job` opens 2nd writer on same SQLite DB | Critical | Medium — add HTTP API for writes |
| Serve Crash | No panic recovery in goroutines | High | Low — add defer/recover |
| MCP Proxy Hang | stdout mutex held during blocking SSE read + no timeout | High | Medium — decouple I/O from mutex |

## Open Questions

1. **Are there other CLI commands that open direct DB connections while serve is running?** The `cogent run`, `cogent status`, `cogent list`, etc. commands all open `service.Open()`. Most are read-only, but `cogent run` also writes. These should be audited.

2. **What triggers the serve crash — is it always a panic or sometimes an OOM?** Need to check dmesg/syslog for OOM killer events on the OSINT machine.

3. **Is the OSINT Go service's SQLite DB (`articles.db` or similar) in the same directory?** If so, file-level contention could contribute. Checked — the OSINT project only has `cogent.db` and `cogent-private.db` in `.cogent/`.
