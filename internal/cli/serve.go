package cli

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
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
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusefmosiah/fase/internal/core"
	"github.com/yusefmosiah/fase/internal/mcpserver"
	"github.com/yusefmosiah/fase/internal/service"
	"github.com/yusefmosiah/fase/internal/web"
)

// wsHub manages WebSocket connections and broadcasts events to all connected clients.
type wsHub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
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
		default: // drop if client is slow
		}
	}
}

func supervisorAvailableSlots(inFlightCount, maxConcurrent int, spawnedCompletionJob bool) int {
	availableSlots := maxConcurrent - inFlightCount
	if spawnedCompletionJob {
		availableSlots--
	}
	if availableSlots < 0 {
		return 0
	}
	return availableSlots
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
	var maxConcurrent int
	var defaultAdapter string
	var devAssets string

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
			return runServe(cmd, root, port, host, auto, noUI, noBrowser, maxConcurrent, devAssets)
		},
	}

	cmd.Flags().IntVar(&port, "port", 4242, "HTTP server port")
	cmd.Flags().StringVar(&host, "host", "localhost", "HTTP bind host")
	cmd.Flags().BoolVar(&auto, "auto", false, "auto-dispatch ready work items")
	cmd.Flags().BoolVar(&noUI, "no-ui", false, "skip web UI, run housekeeping only")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't auto-open browser")
	cmd.Flags().IntVar(&maxConcurrent, "max-concurrent", 1, "max simultaneous jobs (with --auto)")
	cmd.Flags().StringVar(&defaultAdapter, "default-adapter", "codex", "fallback adapter (with --auto)")
	cmd.Flags().StringVar(&devAssets, "dev-assets", "", "serve UI from filesystem instead of embedded (for development)")

	return cmd
}

func runServe(cmd *cobra.Command, root *rootOptions, port int, host string, auto, noUI, noBrowser bool, maxConcurrent int, devAssets string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	serveJSON, _ := json.MarshalIndent(serveInfo, "", "  ")
	servePath := filepath.Join(svc.Paths.StateDir, "serve.json")
	_ = os.WriteFile(servePath, serveJSON, 0o644)
	defer os.Remove(servePath)

	// WebSocket hub — shared across all goroutines
	hub := newWSHub()

	// Set up HTTP handlers (supervisor loop is nil until --auto starts it)
	var supLoop *supervisorLoop
	mux := http.NewServeMux()
	registerAPIHandlers(mux, svc, cwd, hub, &supLoop)

	// MCP endpoint — same work graph tools as `fase mcp http`
	mcpServer := mcpserver.New(svc)
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return mcpServer.MCP
	}, nil))

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
		runHousekeeping(ctx, svc, cwd, hub)
	}()

	// Always run change watcher — detects work/job/attestation changes and pushes via WebSocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		runChangeWatcher(ctx, svc, hub)
	}()

	// Only auto-dispatch when --auto is set
	if auto {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runInProcessSupervisor(ctx, svc, cwd, root, maxConcurrent, hub, &supLoop)
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
func runHousekeeping(ctx context.Context, svc *service.Service, cwd string, hub *wsHub) {
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

				if isJobStalled(jobDir, 10*time.Minute) && !isTerminal(jobState) {
					_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
						WorkID:         workID,
						ExecutionState: core.WorkExecutionStateFailed,
						Message:        fmt.Sprintf("housekeeping: job %s stalled (no output for 10m)", jobID),
						CreatedBy:      "housekeeping",
					})
					hub.broadcast("work_updated", map[string]string{"work_id": workID})
					continue
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

func registerAPIHandlers(mux *http.ServeMux, svc *service.Service, cwd string, hub *wsHub, supLoopPtr **supervisorLoop) {
	// Work items list
	mux.HandleFunc("/api/work/items", func(w http.ResponseWriter, r *http.Request) {
		includeArchived := r.URL.Query().Get("include_archived") == "1" || strings.EqualFold(r.URL.Query().Get("include_archived"), "true")
		items, err := svc.ListWork(r.Context(), service.WorkListRequest{Limit: 500, IncludeArchived: includeArchived})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, items)
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

		// Git diff stat
		diffStat := ""
		if out, err := exec.CommandContext(r.Context(), "git", "diff", "--stat").Output(); err == nil {
			diffStat = string(out)
		}

		writeJSONHTTP(w, 200, map[string]any{
			"supervisor": sup,
			"diff_stat":  diffStat,
		})
	})

	// Supervisor pause / resume
	mux.HandleFunc("/api/supervisor/pause", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		loop := *supLoopPtr
		if loop == nil {
			writeJSONHTTP(w, 409, map[string]string{"error": "supervisor not running (start with --auto)"})
			return
		}
		loop.Pause()
		hub.broadcast("supervisor_paused", map[string]bool{"paused": true})
		writeJSONHTTP(w, 200, map[string]any{"paused": true})
	})

	mux.HandleFunc("/api/supervisor/resume", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONHTTP(w, 405, map[string]string{"error": "method not allowed"})
			return
		}
		loop := *supLoopPtr
		if loop == nil {
			writeJSONHTTP(w, 409, map[string]string{"error": "supervisor not running (start with --auto)"})
			return
		}
		loop.Resume()
		hub.broadcast("supervisor_resumed", map[string]bool{"paused": false})
		writeJSONHTTP(w, 200, map[string]any{"paused": false})
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

// runInProcessSupervisor runs the autonomous dispatch loop using the shared Service instance.
// Only active when --auto is set. Delegates to supervisorLoop for the core 5-step algorithm.
// loopOut is set to the loop pointer before the first cycle so HTTP handlers can call Pause/Resume.
func runInProcessSupervisor(ctx context.Context, svc *service.Service, cwd string, root *rootOptions, maxConcurrent int, hub *wsHub, loopOut **supervisorLoop) {
	selfBin, err := os.Executable()
	if err != nil {
		selfBin = "fase"
	}

	stateDir := core.ResolveRepoStateDirFrom(cwd)
	if stateDir == "" {
		stateDir = filepath.Join(cwd, ".fase")
	}
	ca, caErr := loadOrCreateCA(stateDir)
	if caErr != nil {
		fmt.Fprintf(os.Stderr, "supervisor: capability CA init failed (tokens disabled): %v\n", caErr)
	}
	if ca != nil {
		sweepStaleTokenFiles(stateDir, 2*time.Hour)
	}

	// Load config for budget-aware rotation and apply to the package-level pool.
	cfg, _ := core.LoadConfig(root.configPath)
	if len(cfg.Rotation.Entries) > 0 {
		workRotation = rotationFromConfig(cfg)
	}

	loop := newSupervisorLoop(maxConcurrent, cwd, selfBin, root.configPath)
	loop.ca = ca
	if loopOut != nil {
		*loopOut = loop
	}
	loop.budget = newDailyUsage(stateDir)
	loop.limits = buildLimitsMap(cfg)
	loop.onJobStarted = func(workID, jobID, adapter string) {
		hub.broadcast("job_started", map[string]string{"work_id": workID, "job_id": jobID, "adapter": adapter})
	}
	loop.onJobCompleted = func(workID, jobID, state string) {
		if state == "failed" || state == "cancelled" || state == "stalled" {
			hub.broadcast("work_updated", map[string]string{
				"work_id": workID, "job_id": jobID, "state": state,
				"message": fmt.Sprintf("supervisor: job %s %s", jobID, state),
			})
		} else {
			hub.broadcast("job_completed", map[string]string{"work_id": workID, "job_id": jobID})
			hub.broadcast("work_updated", map[string]string{"work_id": workID, "job_id": jobID, "state": "completed"})
		}
	}

	for {
		select {
		case <-ctx.Done():
			loop.cancelInFlight(ctx, svc)
			return
		default:
		}

		report := loop.runOneCycle(ctx, svc)
		writeSupState(cwd, report.Cycle, loop.IsPaused(), loop.snapshotInFlight(), report)

		select {
		case <-ctx.Done():
			loop.cancelInFlight(ctx, svc)
			return
		case <-time.After(30 * time.Second):
		}
	}
}
