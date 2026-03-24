package cli

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusefmosiah/fase/internal/adapters"
	"github.com/yusefmosiah/fase/internal/core"
	"github.com/yusefmosiah/fase/internal/mcpserver"
	"github.com/yusefmosiah/fase/internal/service"
	"github.com/yusefmosiah/fase/internal/web"
)

// wsHub manages WebSocket connections and broadcasts events to all connected clients.
type wsHub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
	drops   atomic.Int64
}

func newWSHub() *wsHub {
	return &wsHub{clients: make(map[chan []byte]struct{})}
}

func (h *wsHub) subscribe() chan []byte {
	ch := make(chan []byte, 16)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *wsHub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// broadcast sends a typed event to all connected WebSocket clients.
func (h *wsHub) broadcast(eventType string, data any) {
	msg, err := json.Marshal(map[string]any{"type": eventType, "data": data})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
			h.drops.Add(1)
		}
	}
}

func (h *wsHub) Drops() int64 {
	return h.drops.Load()
}

// wsUpgrade performs the WebSocket HTTP upgrade handshake.
func wsUpgrade(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, nil, fmt.Errorf("missing websocket key")
	}
	h := sha1.New()
	h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return nil, nil, fmt.Errorf("hijacking not supported")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		conn.Close()
		return nil, nil, err
	}
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, rw, nil
}

// wsWriteTextFrame writes a single WebSocket text frame (server→client, unmasked).
func wsWriteTextFrame(rw *bufio.ReadWriter, data []byte) error {
	n := len(data)
	header := []byte{0x81} // FIN=1, opcode=text
	switch {
	case n < 126:
		header = append(header, byte(n))
	case n <= 65535:
		header = append(header, 126, byte(n>>8), byte(n))
	default:
		header = append(header, 127,
			byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32),
			byte(n>>24), byte(n>>16), byte(n>>8), byte(n),
		)
	}
	if _, err := rw.Write(header); err != nil {
		return err
	}
	if _, err := rw.Write(data); err != nil {
		return err
	}
	return rw.Flush()
}

// wsServeClient handles one WebSocket connection: sends hub messages until
// the client disconnects or the context is cancelled.
func wsServeClient(ctx context.Context, hub *wsHub, conn net.Conn, rw *bufio.ReadWriter) {
	defer conn.Close()
	ch := hub.subscribe()
	defer hub.unsubscribe(ch)

	// Detect client disconnect by reading (and discarding) incoming frames.
	disconnected := make(chan struct{})
	go func() {
		defer close(disconnected)
		buf := make([]byte, 512)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-disconnected:
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := wsWriteTextFrame(rw, msg); err != nil {
				return
			}
		}
	}
}

func newServeCommand(root *rootOptions) *cobra.Command {
	var port int
	var host string
	var auto bool
	var noUI bool
	var noBrowser bool
	var devAssets string
	var supAdapter string
	var supModel string
	var envFile string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the fase service: web UI, API, and housekeeping",
		Long: `Starts the fase service: mind-graph web UI, HTTP API, and background
housekeeping (lease reconciliation, stall detection).

By default, no work is auto-dispatched. Use --auto to enable autonomous
claiming and execution of ready work items.

Examples:
  fase serve                          # UI + API + housekeeping
  fase serve --auto                   # also auto-dispatch ready work
  fase serve --host 0.0.0.0           # accessible via Tailscale/LAN
  fase serve --no-browser             # don't open browser on start`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd, root, port, host, auto, noUI, noBrowser, devAssets, supAdapter, supModel, envFile)
		},
	}

	cmd.Flags().IntVar(&port, "port", 4242, "HTTP server port")
	cmd.Flags().StringVar(&host, "host", "localhost", "HTTP bind host")
	cmd.Flags().BoolVar(&auto, "auto", false, "auto-dispatch ready work items")
	cmd.Flags().BoolVar(&noUI, "no-ui", false, "skip web UI, run housekeeping only")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", true, "don't auto-open browser")
	cmd.Flags().StringVar(&devAssets, "dev-assets", "", "serve UI from filesystem instead of embedded (for development)")
	cmd.Flags().StringVar(&supAdapter, "supervisor-adapter", "claude", "adapter for the supervisor session (used with --auto)")
	cmd.Flags().StringVar(&supModel, "supervisor-model", "claude-sonnet-4-6", "model for the supervisor session (used with --auto)")
	cmd.Flags().StringVar(&envFile, "env", "", "path to .env file for API keys (default: .env in cwd)")

	return cmd
}

func runServe(cmd *cobra.Command, root *rootOptions, port int, host string, auto, noUI, noBrowser bool, devAssets, supAdapter, supModel, envFile string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load .env — native adapter needs API keys.
	loadDotEnv(envFile)

	// Open service once — shared by all goroutines
	svc, err := service.Open(ctx, root.configPath)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer func() { _ = svc.Close() }()

	cwd, _ := os.Getwd()

	// Find a free port
	listenAddr := net.JoinHostPort(host, fmt.Sprint(port))
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		// Try next port
		listener, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port

	// Write serve.json for CLI discovery
	serveInfo := map[string]any{
		"pid":  os.Getpid(),
		"port": actualPort,
		"cwd":  cwd,
		"auto": auto,
	}
	if envFile != "" {
		absEnv, _ := filepath.Abs(envFile)
		serveInfo["env_file"] = absEnv
	}
	serveJSON, _ := json.MarshalIndent(serveInfo, "", "  ")
	servePath := filepath.Join(svc.Paths.StateDir, "serve.json")
	_ = os.WriteFile(servePath, serveJSON, 0o644)
	defer os.Remove(servePath)

	// WebSocket hub — shared across all goroutines
	hub := newWSHub()

	// Agentic supervisor (ADR-0041) — created before API handlers so
	// pause/resume endpoints have a reference. Goroutine started below.
	var sup *agenticSupervisor
	if auto {
		sup = newAgenticSupervisor(svc, cwd, hub, supAdapter, supModel)
	}

	mux := http.NewServeMux()
	registerAPIHandlers(mux, svc, cwd, hub, sup)

	// MCP endpoint — same work graph tools as `fase mcp http`
	mcpServer := mcpserver.New(svc)
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return mcpServer.MCP
	}, nil))

	// Channel send — push notifications to connected Claude Code session
	mux.HandleFunc("/api/channel/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			Content string            `json:"content"`
			Meta    map[string]string `json:"meta,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		if req.Content == "" {
			writeJSONHTTP(w, 400, map[string]string{"error": "content must not be empty"})
			return
		}
		if err := mcpServer.SendChannelEvent(req.Content, req.Meta); err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		// Broadcast to WebSocket — proxy relays as claude/channel notification.
		broadcastData := map[string]any{"content": req.Content}
		if len(req.Meta) > 0 {
			broadcastData["meta"] = req.Meta
		}
		hub.broadcast("channel_message", broadcastData)
		writeJSONHTTP(w, 200, map[string]string{"status": "sent"})
	})

	// WebSocket endpoint
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := wsUpgrade(w, r)
		if err != nil {
			return
		}
		go wsServeClient(ctx, hub, conn, rw)
	})

	if !noUI {
		// Serve mind-graph UI
		if devAssets != "" {
			// Development: serve from filesystem
			mux.Handle("/", http.FileServer(http.Dir(devAssets)))
		} else {
			// Production: serve from embedded assets
			subFS, err := fs.Sub(web.Assets, "dist")
			if err != nil {
				return fmt.Errorf("embedded assets: %w", err)
			}
			mux.Handle("/", http.FileServer(http.FS(subFS)))
		}
	}

	server := &http.Server{Handler: mux}

	var wg sync.WaitGroup

	// Always run housekeeping (reconcile leases, detect stalls)
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHousekeeping(ctx, svc, cwd, hub, sup, mcpServer)
	}()

	// Always run change watcher — detects work/job/attestation changes and pushes via WebSocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		runChangeWatcher(ctx, svc, hub)
	}()

	// Start agentic supervisor goroutine (ADR-0041)
	// Auto-restarts on exit (context overflow, errors) with backoff.
	if sup != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			restartDelay := 10 * time.Second
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
		}()
	}

	// Start HTTP server
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.Serve(listener); err != http.ErrServerClosed {
			fmt.Fprintf(cmd.ErrOrStderr(), "HTTP server error: %v\n", err)
		}
	}()

	displayHost := host
	if displayHost == "0.0.0.0" || displayHost == "::" || displayHost == "" {
		displayHost = "localhost"
	}
	url := "http://" + net.JoinHostPort(displayHost, fmt.Sprint(actualPort))
	mode := "serve"
	if auto {
		mode = "serve --auto"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "fase %s: %s (pid %d)\n", mode, url, os.Getpid())

	// Auto-open browser
	if !noUI && !noBrowser {
		go func() {
			time.Sleep(500 * time.Millisecond)
			_ = exec.Command("open", url).Start() // macOS
		}()
	}

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Fprintln(cmd.OutOrStdout(), "\nfase serve: shutting down...")
	cancel() // stops housekeeping and supervisor goroutines

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)

	wg.Wait()
	fmt.Fprintln(cmd.OutOrStdout(), "fase serve: stopped")
	return nil
}

// runHousekeeping runs periodic maintenance without dispatching work:
// - Reconcile expired leases (orphaned claims)
// - Detect stalled jobs (no output for 10 minutes)
// - Dispatch verification for completed jobs (from fase dispatch)
// loadDotEnv reads a .env file and sets environment variables.
// Existing vars are NOT overwritten. No dependency on external packages.
// If path is empty, reads ".env" from cwd.
func loadDotEnv(paths ...string) {
	path := ".env"
	if len(paths) > 0 && paths[0] != "" {
		path = paths[0]
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// Strip surrounding quotes.
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

// createWorktree creates a git worktree for isolated worker execution.
// Returns the worktree path. Branch name: fase/work/<work-id>.
func createWorktree(repoRoot, workID string) (string, error) {
	worktreeDir := filepath.Join(repoRoot, ".fase", "worktrees", workID)
	branch := "fase/work/" + workID

	// Clean up stale branch/worktree from previous failed dispatch.
	rmCmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
	rmCmd.Dir = repoRoot
	_ = rmCmd.Run()
	delCmd := exec.Command("git", "branch", "-D", branch)
	delCmd.Dir = repoRoot
	_ = delCmd.Run()

	cmd := exec.Command("git", "worktree", "add", "-b", branch, worktreeDir)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return worktreeDir, nil
}

// mergeWorktree merges a worktree branch back to main and cleans up.
func mergeWorktree(repoRoot, workID string) error {
	branch := "fase/work/" + workID
	worktreeDir := filepath.Join(repoRoot, ".fase", "worktrees", workID)

	// Merge branch into current HEAD (main).
	mergeCmd := exec.Command("git", "merge", "--no-ff", "-m", fmt.Sprintf("fase: merge %s", workID), branch)
	mergeCmd.Dir = repoRoot
	if out, err := mergeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git merge %s: %s: %w", branch, strings.TrimSpace(string(out)), err)
	}

	// Remove worktree.
	rmCmd := exec.Command("git", "worktree", "remove", worktreeDir)
	rmCmd.Dir = repoRoot
	_ = rmCmd.Run() // best-effort

	// Delete branch.
	delCmd := exec.Command("git", "branch", "-d", branch)
	delCmd.Dir = repoRoot
	_ = delCmd.Run() // best-effort

	return nil
}

// cleanupWorktree removes a worktree without merging (for failed work).
func cleanupWorktree(repoRoot, workID string) {
	worktreeDir := filepath.Join(repoRoot, ".fase", "worktrees", workID)
	branch := "fase/work/" + workID

	rmCmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
	rmCmd.Dir = repoRoot
	_ = rmCmd.Run()

	delCmd := exec.Command("git", "branch", "-D", branch)
	delCmd.Dir = repoRoot
	_ = delCmd.Run()
}

func runHousekeeping(ctx context.Context, svc *service.Service, cwd string, hub *wsHub, sup *agenticSupervisor, mcpServer *mcpserver.Server) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Reconcile expired leases (safe every tick)
			_, _ = svc.ReconcileExpiredLeases(ctx)

			// Check for stalled jobs and completed jobs needing verification
			rawDir := filepath.Join(cwd, ".fase", "raw", "stdout")
			entries, err := os.ReadDir(rawDir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), "job_") {
					continue
				}
				jobID := entry.Name()
				jobDir := filepath.Join(rawDir, jobID)

				statusResult, err := svc.Status(ctx, jobID)
				if err != nil {
					continue
				}
				jobState := string(statusResult.Job.State)
				workID := statusResult.Job.WorkID

				if workID == "" {
					continue
				}

				if isJobStalled(jobDir, 30*time.Minute) && !isTerminal(jobState) {
					// Only process the stalled job if it's still the current job.
					// A newer dispatch may have replaced it.
					workResult, wErr := svc.Work(ctx, workID)
					if wErr != nil {
						continue
					}
					if workResult.Work.CurrentJobID != "" && workResult.Work.CurrentJobID != jobID {
						continue // newer job is active, skip stale one
					}

					// Publish stall event instead of auto-killing.
					// Supervisor will receive this and decide: check logs, steer worker, or kill+retry.
					svc.Events.Publish(service.WorkEvent{
						Kind:     service.WorkEventUpdated,
						WorkID:   workID,
						Title:    workResult.Work.Title,
						State:    string(workResult.Work.ExecutionState),
						JobID:    jobID,
						Adapter:  statusResult.Job.Adapter,
						Actor:    service.ActorHousekeeping,
						Cause:    service.CauseHousekeepingStall,
						Metadata: map[string]string{
							"reason": fmt.Sprintf("no output for 30 minutes"),
						},
					})
					hub.broadcast("work_updated", map[string]string{"work_id": workID})

					// If no supervisor is running (one-off dispatch), send channel notification to host
					if sup == nil && mcpServer != nil {
						_ = mcpServer.SendChannelEvent(
							fmt.Sprintf("⚠️ Stall detected: job %s has no output for 30 minutes. Work: %s (%s)", jobID, workResult.Work.Title, workID),
							map[string]string{"type": "stall_warning", "work_id": workID, "job_id": jobID},
						)
					}
					continue
				}
			}

			// Detect orphaned workers: in_progress work whose worker process is dead.
			// This catches jobs dispatched manually (not tracked in supervisor's in-flight map).
			inProgress, _ := svc.ListWork(ctx, service.WorkListRequest{
				Limit:          50,
				ExecutionState: string(core.WorkExecutionStateInProgress),
			})
			for _, item := range inProgress {
				if item.CurrentJobID == "" {
					continue
				}
				rt, rtErr := svc.GetJobRuntime(ctx, item.CurrentJobID)
				if rtErr != nil || rt.SupervisorPID == 0 || rt.CompletedAt != nil {
					continue
				}
				if !isProcessAlive(rt.SupervisorPID) {
					// Publish orphan event for supervisor to handle
					svc.Events.Publish(service.WorkEvent{
						Kind:     service.WorkEventUpdated,
						WorkID:   item.WorkID,
						Title:    item.Title,
						State:    string(item.ExecutionState),
						JobID:    item.CurrentJobID,
						Actor:    service.ActorHousekeeping,
						Cause:    service.CauseHousekeepingOrphan,
						Metadata: map[string]string{
							"reason": fmt.Sprintf("worker process (pid %d) is dead", rt.SupervisorPID),
						},
					})
					hub.broadcast("work_updated", map[string]string{"work_id": item.WorkID})

					// If no supervisor is running, send channel notification
					if sup == nil && mcpServer != nil {
						_ = mcpServer.SendChannelEvent(
							fmt.Sprintf("⚠️ Orphan worker detected: process %d for job %s is dead. Work: %s (%s)", rt.SupervisorPID, item.CurrentJobID, item.Title, item.WorkID),
							map[string]string{"type": "orphan_warning", "work_id": item.WorkID, "job_id": item.CurrentJobID},
						)
					}
				}
			}
		}
	}
}

func runChangeWatcher(ctx context.Context, svc *service.Service, hub *wsHub) {
	ch := svc.Events.Subscribe()
	defer svc.Events.Unsubscribe(ch)

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			event := map[string]string{
				"work_id": ev.WorkID,
				"state":   ev.State,
			}
			hub.broadcast("work_updated", event)

			if ev.PrevState != "" && ev.PrevState != ev.State {
				switch ev.State {
				case "claimed", "in_progress":
					hub.broadcast("job_started", event)
				case "done":
					hub.broadcast("job_completed", event)
				}
			}

			if ev.Kind == service.WorkEventAttested {
				hub.broadcast("attestation_added", map[string]string{
					"work_id": ev.WorkID,
				})
			}
		}
	}
}

func registerAPIHandlers(mux *http.ServeMux, svc *service.Service, cwd string, hub *wsHub, sup *agenticSupervisor) {
	// Work items list (legacy)
	mux.HandleFunc("/api/work/items", func(w http.ResponseWriter, r *http.Request) {
		includeArchived := r.URL.Query().Get("include_archived") == "1" || strings.EqualFold(r.URL.Query().Get("include_archived"), "true")
		items, err := svc.ListWork(r.Context(), service.WorkListRequest{Limit: 500, IncludeArchived: includeArchived})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, items)
	})

	// Work items list with full filters (CLI rewire target)
	mux.HandleFunc("/api/work/list", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 50
		if v := q.Get("limit"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		includeArchived := q.Get("include_archived") == "1" || strings.EqualFold(q.Get("include_archived"), "true")
		items, err := svc.ListWork(r.Context(), service.WorkListRequest{
			Limit:           limit,
			Kind:            q.Get("kind"),
			ExecutionState:  q.Get("state"),
			ApprovalState:   q.Get("approval_state"),
			IncludeArchived: includeArchived,
		})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, items)
	})

	// Ready work items
	mux.HandleFunc("/api/work/ready", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		includeArchived := r.URL.Query().Get("include_archived") == "1"
		_, _ = svc.ReconcileExpiredLeases(r.Context())
		items, err := svc.ReadyWork(r.Context(), limit, includeArchived)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, items)
	})

	// Project hydrate
	mux.HandleFunc("/api/project/hydrate", func(w http.ResponseWriter, r *http.Request) {
		mode := r.URL.Query().Get("mode")
		if mode == "" {
			mode = "standard"
		}
		format := r.URL.Query().Get("format")
		if format == "" {
			format = "markdown"
		}
		result, err := svc.ProjectHydrate(r.Context(), service.ProjectHydrateRequest{
			Mode:   mode,
			Format: format,
		})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if format == "markdown" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(200)
			fmt.Fprint(w, service.RenderProjectHydrateMarkdown(result))
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// Create work item
	mux.HandleFunc("/api/work/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req service.WorkCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		work, err := svc.CreateWork(r.Context(), req)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, work)
	})

	// Claim next ready work item
	mux.HandleFunc("/api/work/claim-next", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req service.WorkClaimNextRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		_, _ = svc.ReconcileExpiredLeases(r.Context())
		work, err := svc.ClaimNextWork(r.Context(), req)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, work)
	})

	// Proposal endpoints
	mux.HandleFunc("/api/proposal/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req service.WorkProposalCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		proposal, err := svc.CreateWorkProposal(r.Context(), req)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, proposal)
	})

	mux.HandleFunc("/api/proposal/list", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 50
		if v := q.Get("limit"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		proposals, err := svc.ListWorkProposals(r.Context(), service.WorkProposalListRequest{
			Limit:        limit,
			State:        q.Get("state"),
			TargetWorkID: q.Get("target"),
			SourceWorkID: q.Get("source"),
		})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, proposals)
	})

	mux.HandleFunc("/api/proposal/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/proposal/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			writeJSONHTTP(w, 404, map[string]string{"error": "missing proposal id"})
			return
		}
		proposalID := parts[0]
		if len(parts) == 1 {
			// GET /api/proposal/{id}
			proposal, err := svc.GetWorkProposal(r.Context(), proposalID)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, proposal)
			return
		}
		switch parts[1] {
		case "accept":
			proposal, created, err := svc.ReviewWorkProposal(r.Context(), proposalID, "accept")
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, map[string]any{"proposal": proposal, "created": created})
		case "reject":
			proposal, _, err := svc.ReviewWorkProposal(r.Context(), proposalID, "reject")
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, proposal)
		default:
			writeJSONHTTP(w, 404, map[string]string{"error": "unknown endpoint"})
		}
	})

	// Edge endpoints (extend existing /api/work/edges)
	mux.HandleFunc("/api/work/edges/add", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			From     string `json:"from"`
			To       string `json:"to"`
			EdgeType string `json:"edge_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		if req.EdgeType == "" {
			req.EdgeType = "blocks"
		}
		edge := core.WorkEdgeRecord{
			EdgeID:     core.GenerateID("wedge"),
			FromWorkID: req.From,
			ToWorkID:   req.To,
			EdgeType:   req.EdgeType,
			CreatedBy:  "cli",
			CreatedAt:  time.Now().UTC(),
		}
		if err := svc.CreateEdge(r.Context(), edge); err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, edge)
	})

	mux.HandleFunc("/api/work/edges/rm", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		edges, err := svc.ListEdges(r.Context(), 100, "", req.From, req.To)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if len(edges) == 0 {
			writeJSONHTTP(w, 404, map[string]string{"error": fmt.Sprintf("no edge found from %s to %s", req.From, req.To)})
			return
		}
		for _, e := range edges {
			if err := svc.DeleteEdge(r.Context(), e.EdgeID); err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
		}
		writeJSONHTTP(w, 200, map[string]any{"removed": len(edges)})
	})

	// Dispatch endpoint — claims work, hydrates, spawns worker
	mux.HandleFunc("/api/dispatch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			WorkID  string `json:"work_id"`
			Adapter string `json:"adapter"`
			Model   string `json:"model"`
			Force   bool   `json:"force"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}

		// Concurrency guard
		if !req.Force {
			inProgress, _ := svc.ListWork(r.Context(), service.WorkListRequest{
				Limit:          10,
				ExecutionState: string(core.WorkExecutionStateInProgress),
			})
			if len(inProgress) > 0 {
				writeJSONHTTP(w, 409, map[string]string{"error": fmt.Sprintf("concurrency guard: %d work item(s) already in progress (use force to override)", len(inProgress))})
				return
			}
		}

		// Pick work item
		workID := req.WorkID
		var item *service.WorkShowResult
		if workID == "" {
			readyItems, err := svc.ReadyWork(r.Context(), 1, false)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			if len(readyItems) == 0 {
				writeJSONHTTP(w, 200, map[string]string{"message": "no ready work items"})
				return
			}
			workID = readyItems[0].WorkID
		}
		result, err := svc.Work(r.Context(), workID)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if result.Work.ExecutionState != "ready" && req.WorkID != "" {
			writeJSONHTTP(w, 400, map[string]string{"error": fmt.Sprintf("work %s is %s, not ready", workID, result.Work.ExecutionState)})
			return
		}
		item = result

		// Pick adapter+model
		pickedAdapter, pickedModel := pickAdapterModel(item.Work, item.Jobs, nil)
		adapter := req.Adapter
		if adapter == "" {
			adapter = pickedAdapter
		}
		model := req.Model
		if model == "" {
			model = pickedModel
		}

		// Hydrate briefing
		briefing, err := svc.HydrateWork(r.Context(), service.WorkHydrateRequest{
			WorkID:   workID,
			Mode:     "standard",
			Claimant: "dispatch",
		})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": fmt.Sprintf("hydrate work: %v", err)})
			return
		}
		briefingJSON := []byte(service.RenderWorkerBriefingMarkdown(briefing))

		// Claim and run
		_, _ = svc.ClaimWork(r.Context(), service.WorkClaimRequest{
			WorkID:        workID,
			Claimant:      "dispatch",
			LeaseDuration: 30 * time.Minute,
		})

		// Create worktree for isolated worker execution.
		workerCWD := cwd
		worktreePath := ""
		if item.Work.Kind == "implement" {
			wt, wtErr := createWorktree(cwd, workID)
			if wtErr != nil {
				// Log but don't fail — fall back to main.
				fmt.Fprintf(os.Stderr, "worktree create failed for %s: %v (falling back to main)\n", workID, wtErr)
			} else {
				workerCWD = wt
				worktreePath = wt
			}
		}

		runResult, runErr := svc.Run(r.Context(), service.RunRequest{
			Adapter: adapter,
			CWD:     workerCWD,
			Prompt:  string(briefingJSON),
			Model:   model,
			WorkID:  workID,
		})

		if runErr != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": runErr.Error()})
			return
		}

		resp := map[string]any{
			"work_id":  workID,
			"title":    item.Work.Title,
			"adapter":  adapter,
			"model":    model,
			"worktree": worktreePath,
		}
		if runResult != nil {
			resp["job_id"] = runResult.Job.JobID
		}
		writeJSONHTTP(w, 200, resp)
	})

	// Attestation signing
	mux.HandleFunc("/api/attestation/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/attestation/"), "/")
		if len(parts) < 2 || parts[1] != "sign" {
			writeJSONHTTP(w, 404, map[string]string{"error": "unknown endpoint"})
			return
		}
		attestID := parts[0]
		var req struct {
			Signature string `json:"signature"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		if err := svc.SignAttestationRecord(r.Context(), attestID, req.Signature); err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, map[string]string{"status": "ok"})
	})

	// Recent job runs across all work items
	mux.HandleFunc("/api/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		limit := 50
		if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
			if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		runs, err := recentRuns(r.Context(), svc, limit)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, runs)
	})

	mux.HandleFunc("/api/runs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		jobID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/runs/"), "/")
		if jobID == "" {
			writeJSONHTTP(w, 404, map[string]string{"error": "missing job id"})
			return
		}
		result, err := runDetail(r.Context(), svc, jobID)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// Work edges list (for DAG view)
	mux.HandleFunc("/api/work/edges", func(w http.ResponseWriter, r *http.Request) {
		edges, err := svc.ListEdges(r.Context(), 500, "", "", "")
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, edges)
	})

	// Work item show
	mux.HandleFunc("/api/work/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/work/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			writeJSONHTTP(w, 404, map[string]string{"error": "missing work id"})
			return
		}
		workID := parts[0]

		if len(parts) == 1 {
			result, err := svc.Work(r.Context(), workID)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, result)
			return
		}

		switch parts[1] {
		case "hydrate":
			mode := r.URL.Query().Get("mode")
			if mode == "" {
				mode = "standard"
			}
			result, err := svc.HydrateWork(r.Context(), service.WorkHydrateRequest{
				WorkID: workID,
				Mode:   mode,
			})
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, result)
		case "notes":
			result, err := svc.Work(r.Context(), workID)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, result.Notes)
		case "update":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req service.WorkUpdateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
				return
			}
			req.WorkID = workID
			work, err := svc.UpdateWork(r.Context(), req)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, work)
		case "note-add":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req service.WorkNoteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
				return
			}
			req.WorkID = workID
			note, err := svc.AddWorkNote(r.Context(), req)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, note)
		case "attest":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req service.WorkAttestRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
				return
			}
			req.WorkID = workID
			// Auto-lookup nonce if not provided
			if strings.TrimSpace(req.Nonce) == "" {
				if workRec, workErr := svc.Work(r.Context(), workID); workErr == nil && workRec != nil {
					req.Nonce = attestationNonceFromWorkShow(workRec)
				}
			}
			record, work, err := svc.AttestWork(r.Context(), req)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, map[string]any{"attestation": record, "work": work})
		case "check":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req service.WorkCheckRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
				return
			}
			req.WorkID = workID
			result, err := svc.WorkCheck(r.Context(), req)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, result)
		case "block":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req struct {
				Message string `json:"message"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			work, err := svc.UpdateWork(r.Context(), service.WorkUpdateRequest{
				WorkID:         workID,
				ExecutionState: core.WorkExecutionStateBlocked,
				Message:        req.Message,
				CreatedBy:      "cli",
			})
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, work)
		case "archive":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req struct {
				Message string `json:"message"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			work, err := svc.UpdateWork(r.Context(), service.WorkUpdateRequest{
				WorkID:         workID,
				ExecutionState: core.WorkExecutionStateArchived,
				Message:        req.Message,
				CreatedBy:      "cli",
			})
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, work)
		case "retry":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			result, err := svc.Work(r.Context(), workID)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			if !result.Work.ExecutionState.Terminal() {
				writeJSONHTTP(w, 400, map[string]string{"error": fmt.Sprintf("work %s is %s, not in a terminal state", workID, result.Work.ExecutionState)})
				return
			}
			work, err := svc.UpdateWork(r.Context(), service.WorkUpdateRequest{
				WorkID:         workID,
				ExecutionState: core.WorkExecutionStateReady,
				Message:        "retried from " + string(result.Work.ExecutionState),
				CreatedBy:      "cli",
			})
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, work)
		case "lock":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			work, err := svc.SetWorkLock(r.Context(), workID, core.WorkLockStateHumanLocked, "cli", "human lock applied")
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, work)
		case "unlock":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			work, err := svc.SetWorkLock(r.Context(), workID, core.WorkLockStateUnlocked, "cli", "human lock released")
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, work)
		case "approve":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req struct {
				Message string `json:"message"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			work, err := svc.ApproveWork(r.Context(), workID, "cli", req.Message)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, work)
		case "reject":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req struct {
				Message string `json:"message"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			work, err := svc.RejectWork(r.Context(), workID, "cli", req.Message)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, work)
		case "promote":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req service.WorkPromoteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
				return
			}
			req.WorkID = workID
			record, work, err := svc.PromoteWork(r.Context(), req)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, map[string]any{"promotion": record, "work": work})
		case "private-note":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req struct {
				NoteType string `json:"note_type"`
				Body     string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
				return
			}
			note, err := svc.AddPrivateNote(r.Context(), workID, req.NoteType, req.Body, "cli")
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, note)
		case "doc-set":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req struct {
				Path   string `json:"path"`
				Title  string `json:"title"`
				Body   string `json:"body"`
				Format string `json:"format"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
				return
			}
			doc, resolvedWorkID, err := svc.SetDocContent(r.Context(), workID, req.Path, req.Title, req.Body, req.Format)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, map[string]any{"doc": doc, "work_id": resolvedWorkID})
		case "children":
			result, err := svc.Work(r.Context(), workID)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, result.Children)
		case "discover":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req struct {
				Title     string `json:"title"`
				Objective string `json:"objective"`
				Kind      string `json:"kind"`
				Rationale string `json:"rationale"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
				return
			}
			proposal, err := svc.DiscoverWork(r.Context(), workID, req.Title, req.Objective, req.Kind, req.Rationale)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, proposal)
		case "claim":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req service.WorkClaimRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
				return
			}
			req.WorkID = workID
			work, err := svc.ClaimWork(r.Context(), req)
			if err != nil {
				if errors.Is(err, service.ErrBusy) {
					writeJSONHTTP(w, 409, map[string]string{"error": err.Error()})
				} else {
					writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				}
				return
			}
			writeJSONHTTP(w, 200, work)
		case "release":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req service.WorkReleaseRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
				return
			}
			req.WorkID = workID
			work, err := svc.ReleaseWork(r.Context(), req)
			if err != nil {
				if errors.Is(err, service.ErrBusy) {
					writeJSONHTTP(w, 409, map[string]string{"error": err.Error()})
				} else {
					writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				}
				return
			}
			writeJSONHTTP(w, 200, work)
		case "renew-lease":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			var req service.WorkRenewLeaseRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
				return
			}
			req.WorkID = workID
			work, err := svc.RenewWorkLease(r.Context(), req)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, work)
		case "verify":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			result, err := svc.VerifyWork(r.Context(), workID)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, result)
		default:
			writeJSONHTTP(w, 404, map[string]string{"error": "unknown endpoint"})
		}
	})

	// Supervisor status
	mux.HandleFunc("/api/supervisor/status", func(w http.ResponseWriter, r *http.Request) {
		supPath := filepath.Join(cwd, ".fase", "supervisor.json")
		supData, _ := os.ReadFile(supPath)
		var sup any
		if len(supData) > 0 {
			_ = json.Unmarshal(supData, &sup)
		}

		// Git diff stat (tracked changes + untracked files)
		diffStat := ""
		if out, err := exec.CommandContext(r.Context(), "git", "diff", "--stat").Output(); err == nil {
			diffStat = string(out)
		}
		if out, err := exec.CommandContext(r.Context(), "git", "ls-files", "--others", "--exclude-standard").Output(); err == nil {
			untracked := strings.TrimSpace(string(out))
			if untracked != "" {
				for _, f := range strings.Split(untracked, "\n") {
					diffStat += " " + f + " (new)\n"
				}
			}
		}

		writeJSONHTTP(w, 200, map[string]any{
			"supervisor": sup,
			"diff_stat":  diffStat,
		})
	})

	// Check record endpoints — same service methods as MCP tools and native adapter
	mux.HandleFunc("/api/check/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			WorkID       string           `json:"work_id"`
			Result       string           `json:"result"`
			CheckerModel string           `json:"checker_model"`
			WorkerModel  string           `json:"worker_model"`
			Report       core.CheckReport `json:"report"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		record, err := svc.CreateCheckRecord(r.Context(), service.CheckRecordCreateRequest{
			WorkID:       req.WorkID,
			Result:       req.Result,
			CheckerModel: req.CheckerModel,
			WorkerModel:  req.WorkerModel,
			Report:       req.Report,
			CreatedBy:    "cli",
		})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, record)
	})

	mux.HandleFunc("/api/check/list", func(w http.ResponseWriter, r *http.Request) {
		workID := r.URL.Query().Get("work_id")
		if workID == "" {
			writeJSONHTTP(w, 400, map[string]string{"error": "work_id required"})
			return
		}
		records, err := svc.ListCheckRecords(r.Context(), workID, 20)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, records)
	})

	mux.HandleFunc("/api/check/show", func(w http.ResponseWriter, r *http.Request) {
		checkID := r.URL.Query().Get("check_id")
		if checkID == "" {
			writeJSONHTTP(w, 400, map[string]string{"error": "check_id required"})
			return
		}
		record, err := svc.GetCheckRecord(r.Context(), checkID)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, record)
	})

	// Git status — tracked changes + untracked files
	mux.HandleFunc("/api/git/status", func(w http.ResponseWriter, r *http.Request) {
		result := map[string]any{}

		if out, err := exec.CommandContext(r.Context(), "git", "diff", "--stat").Output(); err == nil {
			result["diff_stat"] = strings.TrimSpace(string(out))
		}
		if out, err := exec.CommandContext(r.Context(), "git", "status", "--short").Output(); err == nil {
			result["status"] = strings.TrimSpace(string(out))
		}
		if out, err := exec.CommandContext(r.Context(), "git", "ls-files", "--others", "--exclude-standard").Output(); err == nil {
			untracked := strings.TrimSpace(string(out))
			if untracked != "" {
				result["untracked"] = strings.Split(untracked, "\n")
			}
		}
		writeJSONHTTP(w, 200, result)
	})

	// Supervisor pause / resume (ADR-0041)
	mux.HandleFunc("/api/supervisor/pause", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		if sup == nil {
			writeJSONHTTP(w, 409, map[string]string{"error": "supervisor not running (start with --auto)"})
			return
		}
		sup.pause()
		writeJSONHTTP(w, 200, map[string]string{"status": "paused"})
	})

	mux.HandleFunc("/api/supervisor/resume", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		if sup == nil {
			writeJSONHTTP(w, 409, map[string]string{"error": "supervisor not running (start with --auto)"})
			return
		}
		sup.resume()
		writeJSONHTTP(w, 200, map[string]string{"status": "resumed"})
	})

	mux.HandleFunc("/api/supervisor/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		if sup == nil {
			writeJSONHTTP(w, 409, map[string]string{"error": "supervisor not running (start with --auto)"})
			return
		}
		var req struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		if req.Message == "" {
			writeJSONHTTP(w, 400, map[string]string{"error": "message must not be empty"})
			return
		}
		sup.send(req.Message)
		writeJSONHTTP(w, 200, map[string]string{"status": "sent"})
	})

	// Full diff
	mux.HandleFunc("/api/diff", func(w http.ResponseWriter, r *http.Request) {
		out, _ := exec.CommandContext(r.Context(), "git", "diff", "--patch").Output()
		writeJSONHTTP(w, 200, map[string]any{"diff": string(out)})
	})

	// Bash log
	mux.HandleFunc("/api/bash-log", func(w http.ResponseWriter, r *http.Request) {
		commands, jobID, err := bashLogForRequest(r.Context(), svc, r.URL.Query().Get("job"))
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, map[string]any{
			"commands": commands,
			"job_id":   jobID,
		})
	})

	// --- Phase B: route remaining CLI commands through serve ---

	// POST /api/job/run — queue a new background job
	mux.HandleFunc("/api/job/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req service.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		result, err := svc.Run(r.Context(), req)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// POST /api/job/send — queue a continuation on a resumable session
	mux.HandleFunc("/api/job/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req service.SendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		result, err := svc.Send(r.Context(), req)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// GET /api/job/list — list jobs
	mux.HandleFunc("/api/job/list", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 50
		if v := q.Get("limit"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		jobs, err := svc.ListJobs(r.Context(), service.ListJobsRequest{
			Limit:     limit,
			Adapter:   q.Get("adapter"),
			State:     q.Get("state"),
			SessionID: q.Get("session"),
		})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if jobs == nil {
			jobs = []core.JobRecord{}
		}
		writeJSONHTTP(w, 200, jobs)
	})

	// GET/POST /api/job/{id}/... — per-job operations
	mux.HandleFunc("/api/job/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/job/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			writeJSONHTTP(w, 404, map[string]string{"error": "missing job id"})
			return
		}
		jobID := parts[0]
		action := ""
		if len(parts) > 1 {
			action = parts[1]
		}
		switch action {
		case "status":
			result, err := svc.Status(r.Context(), jobID)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, result)
		case "logs":
			q := r.URL.Query()
			limit := 200
			if v := q.Get("limit"); v != "" {
				if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
					limit = parsed
				}
			}
			events, err := svc.Logs(r.Context(), jobID, limit)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			if events == nil {
				events = []core.EventRecord{}
			}
			writeJSONHTTP(w, 200, events)
		case "logs-after":
			q := r.URL.Query()
			limit := 200
			if v := q.Get("limit"); v != "" {
				if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
					limit = parsed
				}
			}
			var afterSeq int64
			if v := q.Get("after"); v != "" {
				afterSeq, _ = strconv.ParseInt(v, 10, 64)
			}
			events, err := svc.LogsAfter(r.Context(), jobID, afterSeq, limit)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			if events == nil {
				events = []core.EventRecord{}
			}
			writeJSONHTTP(w, 200, events)
		case "logs-raw":
			q := r.URL.Query()
			limit := 200
			if v := q.Get("limit"); v != "" {
				if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
					limit = parsed
				}
			}
			logs, err := svc.RawLogs(r.Context(), jobID, limit)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			if logs == nil {
				logs = []service.RawLogEntry{}
			}
			writeJSONHTTP(w, 200, logs)
		case "logs-raw-after":
			q := r.URL.Query()
			limit := 200
			if v := q.Get("limit"); v != "" {
				if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
					limit = parsed
				}
			}
			var afterSeq int64
			if v := q.Get("after"); v != "" {
				afterSeq, _ = strconv.ParseInt(v, 10, 64)
			}
			rawLogs, events, err := svc.RawLogsAfter(r.Context(), jobID, afterSeq, limit)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, map[string]any{"logs": rawLogs, "events": events})
		case "cancel":
			if r.Method != http.MethodPost {
				writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
				return
			}
			job, err := svc.Cancel(r.Context(), jobID)
			if err != nil {
				writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSONHTTP(w, 200, job)
		default:
			writeJSONHTTP(w, 404, map[string]string{"error": "unknown job endpoint"})
		}
	})

	// POST /api/debrief — queue a model-authored session debrief
	mux.HandleFunc("/api/debrief", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req service.DebriefRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		result, err := svc.Debrief(r.Context(), req)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// GET /api/session/list — list sessions
	mux.HandleFunc("/api/session/list", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 50
		if v := q.Get("limit"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		sessions, err := svc.ListSessions(r.Context(), service.ListSessionsRequest{
			Limit:   limit,
			Adapter: q.Get("adapter"),
			Status:  q.Get("status"),
		})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if sessions == nil {
			sessions = []core.SessionRecord{}
		}
		writeJSONHTTP(w, 200, sessions)
	})

	// GET /api/session/{id} — inspect a canonical session
	mux.HandleFunc("/api/session/", func(w http.ResponseWriter, r *http.Request) {
		sessionID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/session/"), "/")
		if sessionID == "" {
			writeJSONHTTP(w, 404, map[string]string{"error": "missing session id"})
			return
		}
		result, err := svc.Session(r.Context(), sessionID)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// GET /api/artifact/list — list artifacts
	mux.HandleFunc("/api/artifact/list", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 20
		if v := q.Get("limit"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		artifacts, err := svc.ListArtifacts(r.Context(), service.ArtifactsRequest{
			JobID:     q.Get("job"),
			SessionID: q.Get("session"),
			WorkID:    q.Get("work"),
			Kind:      q.Get("kind"),
			Limit:     limit,
		})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if artifacts == nil {
			artifacts = []core.ArtifactRecord{}
		}
		writeJSONHTTP(w, 200, artifacts)
	})

	// POST /api/artifact/attach — attach a file as a persisted artifact
	mux.HandleFunc("/api/artifact/attach", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req service.AttachArtifactRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		artifact, err := svc.AttachArtifact(r.Context(), req)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, artifact)
	})

	// GET /api/artifact/{id} — show one artifact and its content
	mux.HandleFunc("/api/artifact/", func(w http.ResponseWriter, r *http.Request) {
		artifactID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/artifact/"), "/")
		if artifactID == "" {
			writeJSONHTTP(w, 404, map[string]string{"error": "missing artifact id"})
			return
		}
		result, err := svc.ReadArtifact(r.Context(), artifactID)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// GET /api/history/search — search canonical local fase history
	mux.HandleFunc("/api/history/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 20
		if v := q.Get("limit"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		scanLimit := 500
		if v := q.Get("scan_limit"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				scanLimit = parsed
			}
		}
		result, err := svc.SearchHistory(r.Context(), service.HistorySearchRequest{
			Query:     q.Get("query"),
			Adapter:   q.Get("adapter"),
			Model:     q.Get("model"),
			CWD:       q.Get("cwd"),
			SessionID: q.Get("session"),
			Kinds:     splitCSV(q.Get("kinds")),
			Limit:     limit,
			ScanLimit: scanLimit,
		})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// POST /api/internal/run-job — execute a detached job in the serve process
	// This eliminates the concurrent-writer problem: __run-job subprocess POSTs here
	// and serve executes the job using its existing DB connection.
	mux.HandleFunc("/api/internal/run-job", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			JobID  string `json:"job_id"`
			TurnID string `json:"turn_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		if req.JobID == "" || req.TurnID == "" {
			writeJSONHTTP(w, 400, map[string]string{"error": "job_id and turn_id are required"})
			return
		}
		// Return 202 immediately — execution happens in a goroutine using serve's DB connection.
		writeJSONHTTP(w, 202, map[string]string{"status": "accepted"})
		go func() {
			if err := svc.ExecuteDetachedJob(context.Background(), req.JobID, req.TurnID); err != nil {
				fmt.Fprintf(os.Stderr, "run-job %s error: %v\n", req.JobID, err)
			}
		}()
	})

	// GET /api/adapters — list adapter availability and capability flags
	mux.HandleFunc("/api/adapters", func(w http.ResponseWriter, r *http.Request) {
		catalog := adapters.CatalogFromConfig(svc.Config)
		writeJSONHTTP(w, 200, catalog)
	})

	// GET /api/runtime — show the current host-agent runtime inventory
	mux.HandleFunc("/api/runtime", func(w http.ResponseWriter, r *http.Request) {
		result, err := svc.Runtime(r.Context(), r.URL.Query().Get("adapter"))
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// POST /api/catalog/sync — refresh the discovered provider/model catalog
	mux.HandleFunc("/api/catalog/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		result, err := svc.SyncCatalog(r.Context())
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// GET /api/catalog/show — show the latest discovered provider/model catalog
	mux.HandleFunc("/api/catalog/show", func(w http.ResponseWriter, r *http.Request) {
		result, err := svc.Catalog(r.Context())
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// POST /api/catalog/probe — run short entitlement probes against catalog entries
	mux.HandleFunc("/api/catalog/probe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req service.ProbeCatalogRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		result, err := svc.ProbeCatalog(r.Context(), req)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// POST /api/transfer/export — export a structured transfer bundle
	mux.HandleFunc("/api/transfer/export", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req service.TransferExportRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		result, err := svc.ExportTransfer(r.Context(), req)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// POST /api/transfer/run — queue a job from a transfer bundle
	mux.HandleFunc("/api/transfer/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req service.TransferRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		result, err := svc.RunTransfer(r.Context(), req)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// POST /api/reconcile — release orphaned work items with expired leases
	mux.HandleFunc("/api/reconcile", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		ids, err := svc.ReconcileOnStartup(r.Context())
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if ids == nil {
			ids = []string{}
		}
		writeJSONHTTP(w, 200, map[string]any{"reconciled_work_ids": ids, "count": len(ids)})
	})

	// GET /api/dashboard — show live supervisor and work graph status
	mux.HandleFunc("/api/dashboard", func(w http.ResponseWriter, r *http.Request) {
		supPath := filepath.Join(cwd, ".fase", "supervisor.json")
		supData, _ := os.ReadFile(supPath)

		allWork, err := svc.ListWork(r.Context(), service.WorkListRequest{Limit: 500})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		states := map[string]int{}
		for _, wk := range allWork {
			states[string(wk.ExecutionState)]++
		}
		result := map[string]any{
			"work_states": states,
			"total_items": len(allWork),
			"work":        allWork,
		}
		if len(supData) > 0 {
			var sup map[string]any
			if json.Unmarshal(supData, &sup) == nil {
				result["supervisor"] = sup
			}
		}
		writeJSONHTTP(w, 200, result)
	})

	// POST /api/bootstrap/inspect — assess whether paths are work-graph-native
	mux.HandleFunc("/api/bootstrap/inspect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req service.BootstrapInspectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		result, err := svc.InspectBootstrap(r.Context(), req)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})

	// POST /api/bootstrap/create — create a root work item from discovered entrypoints
	mux.HandleFunc("/api/bootstrap/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		var req service.BootstrapCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONHTTP(w, 400, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		result, err := svc.BootstrapCreate(r.Context(), req)
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, result)
	})
}

func writeJSONHTTP(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

type runHistoryItem struct {
	JobID              string     `json:"job_id"`
	WorkID             string     `json:"work_id,omitempty"`
	WorkTitle          string     `json:"work_title,omitempty"`
	Adapter            string     `json:"adapter"`
	Model              string     `json:"model,omitempty"`
	JobState           string     `json:"job_state"`
	Status             string     `json:"status"`
	AttestationResult  string     `json:"attestation_result,omitempty"`
	AttestationSummary string     `json:"attestation_summary,omitempty"`
	DurationMS         int64      `json:"duration_ms"`
	FilesChanged       *int       `json:"files_changed,omitempty"`
	LinesAdded         *int       `json:"lines_added,omitempty"`
	LinesRemoved       *int       `json:"lines_removed,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	FinishedAt         *time.Time `json:"finished_at,omitempty"`
}

type runDetailResponse struct {
	Run          runHistoryItem           `json:"run"`
	Work         core.WorkItemRecord      `json:"work"`
	Objective    string                   `json:"objective"`
	Updates      []core.WorkUpdateRecord  `json:"updates,omitempty"`
	Notes        []core.WorkNoteRecord    `json:"notes,omitempty"`
	Attestation  *core.AttestationRecord  `json:"attestation,omitempty"`
	Attestations []core.AttestationRecord `json:"attestations,omitempty"`
	BashLog      []bashLogCommand         `json:"bash_log,omitempty"`
}

type runDiffStat struct {
	filesChanged int
	linesAdded   int
	linesRemoved int
	known        bool
}

var runDiffStatPattern = regexp.MustCompile(`(?m)(\d+)\s+files?\s+changed(?:,\s+(\d+)\s+insertions?\(\+\))?(?:,\s+(\d+)\s+deletions?\(-\))?`)

type bashLogCommand struct {
	Command       string `json:"command,omitempty"`
	ExitCode      int    `json:"exit_code,omitempty"`
	OutputPreview string `json:"output_preview,omitempty"`
	Comment       string `json:"comment,omitempty"`
}

type bashLogState struct {
	commands []bashLogCommand
	pending  map[string]int
}

func collectBashLogCommands(rawDir string) ([]bashLogCommand, string) {
	dirs, err := os.ReadDir(rawDir)
	if err != nil {
		return []bashLogCommand{}, ""
	}

	// Find the newest job directory by sorted ReadDir order.
	var latestDir string
	for i := len(dirs) - 1; i >= 0; i-- {
		if strings.HasPrefix(dirs[i].Name(), "job_") {
			latestDir = filepath.Join(rawDir, dirs[i].Name())
			break
		}
	}
	if latestDir == "" {
		return []bashLogCommand{}, ""
	}

	state := &bashLogState{
		pending: map[string]int{},
	}

	files, err := os.ReadDir(latestDir)
	if err != nil {
		return []bashLogCommand{}, filepath.Base(latestDir)
	}

	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".jsonl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(latestDir, f.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}
			state.ingest(ev)
		}
	}

	return state.commands, filepath.Base(latestDir)
}

func bashLogForRequest(ctx context.Context, svc *service.Service, requestedJobID string) ([]bashLogCommand, string, error) {
	jobID := strings.TrimSpace(requestedJobID)
	if jobID == "" || jobID == "latest" {
		jobs, err := svc.ListJobs(ctx, service.ListJobsRequest{Limit: 1})
		if err != nil {
			return nil, "", err
		}
		if len(jobs) == 0 {
			return []bashLogCommand{}, "", nil
		}
		jobID = jobs[0].JobID
	}

	logs, err := svc.RawLogs(ctx, jobID, 200)
	if err != nil {
		return nil, "", err
	}
	return collectBashLogCommandsFromEntries(logs), jobID, nil
}

func collectBashLogCommandsFromEntries(entries []service.RawLogEntry) []bashLogCommand {
	state := &bashLogState{
		pending: map[string]int{},
	}

	for _, entry := range entries {
		for _, line := range strings.Split(entry.Content, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}
			state.ingest(ev)
		}
	}

	return state.commands
}

func recentRuns(ctx context.Context, svc *service.Service, limit int) ([]runHistoryItem, error) {
	if limit <= 0 {
		limit = 50
	}

	jobs, err := svc.ListJobs(ctx, service.ListJobsRequest{Limit: limit * 2})
	if err != nil {
		return nil, err
	}

	entries := make([]runHistoryItem, 0, limit)
	workCache := map[string]*service.WorkShowResult{}
	statsCache := map[string]runDiffStat{}

	for _, job := range jobs {
		if job.WorkID == "" {
			continue
		}

		work, err := cachedWorkShow(ctx, svc, workCache, job.WorkID)
		if err != nil {
			continue
		}

		stats, ok := statsCache[job.JobID]
		if !ok {
			stats = runDiffStatForJob(ctx, svc, job.JobID)
			statsCache[job.JobID] = stats
		}

		entries = append(entries, buildRunHistoryItem(job, work, stats))
		if len(entries) >= limit {
			break
		}
	}

	return entries, nil
}

func runDetail(ctx context.Context, svc *service.Service, jobID string) (*runDetailResponse, error) {
	status, err := svc.Status(ctx, jobID)
	if err != nil {
		return nil, err
	}

	work, err := svc.Work(ctx, status.Job.WorkID)
	if err != nil {
		return nil, err
	}

	runItem := buildRunHistoryItem(status.Job, work, runDiffStatForJob(ctx, svc, jobID))
	updates := filterRunUpdates(work.Updates, status.Job)
	notes := filterRunNotes(work.Notes, status.Job)
	attestation := latestAttestationForRun(work.Attestations)
	bashLog, _, err := bashLogForRequest(ctx, svc, jobID)
	if err != nil {
		bashLog = []bashLogCommand{}
	}

	return &runDetailResponse{
		Run:          runItem,
		Work:         work.Work,
		Objective:    work.Work.Objective,
		Updates:      updates,
		Notes:        notes,
		Attestation:  attestation,
		Attestations: work.Attestations,
		BashLog:      bashLog,
	}, nil
}

func cachedWorkShow(ctx context.Context, svc *service.Service, cache map[string]*service.WorkShowResult, workID string) (*service.WorkShowResult, error) {
	if cached := cache[workID]; cached != nil {
		return cached, nil
	}
	work, err := svc.Work(ctx, workID)
	if err != nil {
		return nil, err
	}
	cache[workID] = work
	return work, nil
}

func buildRunHistoryItem(job core.JobRecord, work *service.WorkShowResult, stats runDiffStat) runHistoryItem {
	attestation := latestAttestationForRun(work.Attestations)
	status := string(job.State)
	result := ""
	summary := ""
	if attestation != nil {
		result = attestation.Result
		summary = attestation.Summary
		status = attestation.Result
	}

	return runHistoryItem{
		JobID:              job.JobID,
		WorkID:             job.WorkID,
		WorkTitle:          work.Work.Title,
		Adapter:            job.Adapter,
		Model:              summaryStringValue(job.Summary, "model"),
		JobState:           string(job.State),
		Status:             status,
		AttestationResult:  result,
		AttestationSummary: summary,
		DurationMS:         jobDuration(job).Milliseconds(),
		FilesChanged:       stats.intPtrFilesChanged(),
		LinesAdded:         stats.intPtrLinesAdded(),
		LinesRemoved:       stats.intPtrLinesRemoved(),
		CreatedAt:          job.CreatedAt,
		UpdatedAt:          job.UpdatedAt,
		FinishedAt:         job.FinishedAt,
	}
}

func runDiffStatForJob(ctx context.Context, svc *service.Service, jobID string) runDiffStat {
	logs, err := svc.RawLogs(ctx, jobID, 200)
	if err != nil {
		return runDiffStat{}
	}
	return parseRunDiffStatFromLogs(collectBashLogCommandsFromEntries(logs))
}

func parseRunDiffStatFromLogs(commands []bashLogCommand) runDiffStat {
	for _, command := range commands {
		output := strings.TrimSpace(command.OutputPreview)
		if output == "" {
			continue
		}
		if stat, ok := parseRunDiffStat(output); ok {
			return stat
		}
	}
	return runDiffStat{}
}

func parseRunDiffStat(output string) (runDiffStat, bool) {
	matches := runDiffStatPattern.FindStringSubmatch(output)
	if len(matches) == 0 {
		return runDiffStat{}, false
	}

	filesChanged, _ := strconv.Atoi(matches[1])
	linesAdded := 0
	linesRemoved := 0
	if len(matches) > 2 && matches[2] != "" {
		linesAdded, _ = strconv.Atoi(matches[2])
	}
	if len(matches) > 3 && matches[3] != "" {
		linesRemoved, _ = strconv.Atoi(matches[3])
	}

	return runDiffStat{
		filesChanged: filesChanged,
		linesAdded:   linesAdded,
		linesRemoved: linesRemoved,
		known:        true,
	}, true
}

func (s runDiffStat) intPtrFilesChanged() *int {
	if !s.known {
		return nil
	}
	value := s.filesChanged
	return &value
}

func (s runDiffStat) intPtrLinesAdded() *int {
	if !s.known {
		return nil
	}
	value := s.linesAdded
	return &value
}

func (s runDiffStat) intPtrLinesRemoved() *int {
	if !s.known {
		return nil
	}
	value := s.linesRemoved
	return &value
}

func latestAttestationForRun(attestations []core.AttestationRecord) *core.AttestationRecord {
	if len(attestations) == 0 {
		return nil
	}
	latest := attestations[0]
	for _, attestation := range attestations[1:] {
		if attestation.CreatedAt.After(latest.CreatedAt) {
			latest = attestation
		}
	}
	return &latest
}

func filterRunUpdates(updates []core.WorkUpdateRecord, job core.JobRecord) []core.WorkUpdateRecord {
	filtered := make([]core.WorkUpdateRecord, 0, len(updates))
	start := job.CreatedAt.Add(-1 * time.Second)
	end := time.Now().UTC()
	if job.FinishedAt != nil {
		end = job.FinishedAt.Add(1 * time.Second)
	}
	for _, update := range updates {
		if update.JobID != "" && update.JobID != job.JobID {
			continue
		}
		if update.JobID == "" && (update.CreatedAt.Before(start) || update.CreatedAt.After(end)) {
			continue
		}
		filtered = append(filtered, update)
	}
	return filtered
}

func filterRunNotes(notes []core.WorkNoteRecord, job core.JobRecord) []core.WorkNoteRecord {
	filtered := make([]core.WorkNoteRecord, 0, len(notes))
	start := job.CreatedAt.Add(-1 * time.Second)
	end := time.Now().UTC()
	if job.FinishedAt != nil {
		end = job.FinishedAt.Add(1 * time.Second)
	}
	for _, note := range notes {
		if note.CreatedAt.Before(start) || note.CreatedAt.After(end) {
			continue
		}
		filtered = append(filtered, note)
	}
	return filtered
}

func jobDuration(job core.JobRecord) time.Duration {
	end := job.UpdatedAt
	if job.FinishedAt != nil {
		end = *job.FinishedAt
	}
	if end.Before(job.CreatedAt) {
		return 0
	}
	return end.Sub(job.CreatedAt)
}

func summaryStringValue(summary map[string]any, key string) string {
	if summary == nil {
		return ""
	}
	value, ok := summary[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func (s *bashLogState) ingest(ev map[string]any) {
	if ev == nil {
		return
	}

	if isBashResultEvent(ev) {
		s.updateFromResult(ev)
		return
	}

	if command, id, exitCode, output, ok := extractBashCommand(ev); ok {
		s.addCommand(id, command, exitCode, output)
		return
	}

	if comment := extractBashComment(ev); comment != "" {
		s.addComment(comment)
	}
}

func (s *bashLogState) addComment(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(text) > 300 {
		text = text[:300]
	}
	s.commands = append(s.commands, bashLogCommand{Comment: text})
}

func (s *bashLogState) addCommand(id, command string, exitCode *int, output string) {
	command = normalizeBashCommand(command)
	if command == "" {
		return
	}

	entry := bashLogCommand{Command: command}
	if exitCode != nil {
		entry.ExitCode = *exitCode
	}
	if output != "" {
		entry.OutputPreview = truncateText(output, 500)
	}

	index := len(s.commands)
	s.commands = append(s.commands, entry)
	if id != "" {
		s.pending[id] = index
	}
}

func (s *bashLogState) updateFromResult(ev map[string]any) {
	id := firstString(ev, "tool_use_id", "toolUseId", "id", "call_id")
	if part, ok := ev["part"].(map[string]any); ok && id == "" {
		id = firstString(part, "tool_use_id", "toolUseId", "id", "call_id")
	}

	exitCode, hasExitCode := extractIntField(ev, "exit_code")
	if !hasExitCode {
		if part, ok := ev["part"].(map[string]any); ok {
			exitCode, hasExitCode = extractIntField(part, "exit_code")
		}
	}

	output := extractBashOutput(ev)
	if output == "" {
		if part, ok := ev["part"].(map[string]any); ok {
			output = extractBashOutput(part)
		}
	}

	index := -1
	if id != "" {
		if pendingIndex, ok := s.pending[id]; ok {
			index = pendingIndex
		}
	}
	if index < 0 {
		for i := len(s.commands) - 1; i >= 0; i-- {
			if s.commands[i].Command != "" {
				index = i
				break
			}
		}
	}
	if index < 0 {
		return
	}

	if hasExitCode {
		s.commands[index].ExitCode = exitCode
	}
	if output != "" {
		s.commands[index].OutputPreview = truncateText(output, 500)
	}
}

func isBashResultEvent(ev map[string]any) bool {
	eventType := strings.ToLower(firstString(ev, "type", "event"))
	if strings.Contains(eventType, "tool_result") || strings.Contains(eventType, "command_execution_result") {
		return true
	}
	if part, ok := ev["part"].(map[string]any); ok {
		partType := strings.ToLower(firstString(part, "type"))
		return strings.Contains(partType, "tool_result") || strings.Contains(partType, "command_execution_result")
	}
	return false
}

func extractBashCommand(ev map[string]any) (command, id string, exitCode *int, output string, ok bool) {
	for _, candidate := range bashLogCandidateMaps(ev) {
		if candidate == nil {
			continue
		}

		if id == "" {
			id = firstString(candidate, "tool_use_id", "toolUseId", "id", "call_id")
		}

		if command == "" {
			command = extractCommandString(candidate)
		}
		if command == "" {
			continue
		}

		if code, hasCode := extractIntField(candidate, "exit_code"); hasCode {
			exitCode = &code
		}
		if output == "" {
			output = extractBashOutput(candidate)
		}
		ok = true
		break
	}
	return
}

func bashLogCandidateMaps(ev map[string]any) []map[string]any {
	maps := []map[string]any{ev}
	for _, key := range []string{"item", "part", "message", "completion", "result"} {
		if nested, ok := ev[key].(map[string]any); ok {
			maps = append(maps, nested)
		}
	}
	return maps
}

func extractCommandString(m map[string]any) string {
	if command := firstString(m, "command", "cmd", "shell", "script"); command != "" {
		return command
	}

	if input, ok := m["input"].(map[string]any); ok {
		if command := firstString(input, "command", "cmd", "shell", "script", "text"); command != "" {
			return command
		}
		if args, ok := input["args"].([]any); ok {
			var parts []string
			for _, arg := range args {
				if text, ok := arg.(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}

	if args, ok := m["args"].([]any); ok {
		var parts []string
		for _, arg := range args {
			if text, ok := arg.(string); ok && text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}

	return ""
}

func extractBashComment(ev map[string]any) string {
	for _, candidate := range bashLogCandidateMaps(ev) {
		if candidate == nil {
			continue
		}

		if strings.ToLower(firstString(candidate, "type")) == "agent_message" {
			if text := extractText(candidate["text"], candidate["content"], candidate["message"]); text != "" {
				return text
			}
		}

		if role := strings.ToLower(firstString(candidate, "role")); role == "assistant" {
			if text := extractText(candidate["content"], candidate["text"], candidate["message"]); text != "" {
				return text
			}
		}

		if strings.Contains(strings.ToLower(firstString(candidate, "type")), "assistant") {
			if text := extractText(candidate["content"], candidate["text"], candidate["message"]); text != "" {
				return text
			}
		}

		if strings.ToLower(firstString(candidate, "type")) == "text" {
			if text := extractText(candidate["part"], candidate["text"], candidate["content"]); text != "" {
				return text
			}
		}

		if text := extractText(candidate["finalText"], candidate["final_text"], candidate["result"]); text != "" {
			if strings.ToLower(firstString(candidate, "type")) == "completion" {
				return text
			}
		}
	}

	return ""
}

func extractBashOutput(m map[string]any) string {
	if output := extractText(
		m["aggregated_output"],
		m["stdout"],
		m["output"],
		m["content"],
		m["text"],
	); output != "" {
		return output
	}

	if result, ok := m["result"].(map[string]any); ok {
		if output := extractText(result["content"], result["stdout"], result["output"], result["text"]); output != "" {
			return output
		}
	}

	if part, ok := m["part"].(map[string]any); ok {
		if output := extractText(part["content"], part["stdout"], part["output"], part["text"]); output != "" {
			return output
		}
	}

	return ""
}

func extractText(values ...any) string {
	var parts []string
	for _, value := range values {
		text := extractTextValue(value)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func extractTextValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, key := range []string{"text", "content", "message", "finalText", "final_text", "stdout", "output", "result"} {
			if text := extractTextValue(typed[key]); text != "" {
				return text
			}
		}
		return ""
	case []any:
		var parts []string
		for _, item := range typed {
			if text := extractTextValue(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

func firstString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func intValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		var parsed int64
		_, err := fmt.Sscan(strings.TrimSpace(typed), &parsed)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func extractIntField(m map[string]any, key string) (int, bool) {
	value, ok := intValue(m[key])
	return int(value), ok
}

func normalizeBashCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}

	for _, prefix := range []string{"/bin/zsh -lc ", "/bin/bash -lc ", "zsh -lc ", "bash -lc "} {
		if strings.HasPrefix(command, prefix) {
			command = strings.TrimSpace(command[len(prefix):])
			break
		}
	}

	if len(command) >= 2 {
		if (strings.HasPrefix(command, "'") && strings.HasSuffix(command, "'")) ||
			(strings.HasPrefix(command, "\"") && strings.HasSuffix(command, "\"")) {
			command = command[1 : len(command)-1]
		}
	}

	return strings.TrimSpace(command)
}

func truncateText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit]
}

// runInProcessSupervisor is a placeholder for the agentic supervisor (ADR-0041).
// The deterministic supervisor loop has been removed.
