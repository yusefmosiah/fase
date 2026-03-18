package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/yusefmosiah/cagent/internal/core"
	"github.com/yusefmosiah/cagent/internal/service"
	"github.com/yusefmosiah/cagent/internal/web"
)

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
		Short: "Run the cagent service: web UI, API, and housekeeping",
		Long: `Starts the cagent service: mind-graph web UI, HTTP API, and background
housekeeping (lease reconciliation, stall detection).

By default, no work is auto-dispatched. Use --auto to enable autonomous
claiming and execution of ready work items.

Examples:
  cagent serve                          # UI + API + housekeeping
  cagent serve --auto                   # also auto-dispatch ready work
  cagent serve --host 0.0.0.0           # accessible via Tailscale/LAN
  cagent serve --no-browser             # don't open browser on start`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd, root, port, host, auto, noUI, noBrowser, maxConcurrent, defaultAdapter, devAssets)
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

func runServe(cmd *cobra.Command, root *rootOptions, port int, host string, auto, noUI, noBrowser bool, maxConcurrent int, defaultAdapter, devAssets string) error {
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

	// Set up HTTP handlers
	mux := http.NewServeMux()
	registerAPIHandlers(mux, svc, cwd)

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
		runHousekeeping(ctx, svc, cwd)
	}()

	// Only auto-dispatch when --auto is set
	if auto {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runInProcessSupervisor(ctx, svc, cwd, root, maxConcurrent, defaultAdapter)
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
	fmt.Fprintf(cmd.OutOrStdout(), "cagent %s: %s (pid %d)\n", mode, url, os.Getpid())

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

	fmt.Fprintln(cmd.OutOrStdout(), "\ncagent serve: shutting down...")
	cancel() // stops housekeeping and supervisor goroutines

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)

	wg.Wait()
	fmt.Fprintln(cmd.OutOrStdout(), "cagent serve: stopped")
	return nil
}

// runHousekeeping runs periodic maintenance without dispatching work:
// - Reconcile expired leases (orphaned claims)
// - Detect stalled jobs (no output for 10 minutes)
// - Dispatch verification for completed jobs (from cagent dispatch)
func runHousekeeping(ctx context.Context, svc *service.Service, cwd string) {
	selfBin, _ := os.Executable()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Track which work items we've already dispatched verification for
	verified := make(map[string]bool)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Reconcile expired leases
			_, _ = svc.ReconcileOnStartup(ctx)

			// Check for stalled jobs and completed jobs needing verification
			rawDir := filepath.Join(cwd, ".cagent", "raw", "stdout")
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
					continue
				}

				// If job completed and work is still in_progress, dispatch attestation
				if isTerminal(jobState) && !verified[workID] {
					workResult, err := svc.Work(ctx, workID)
					if err != nil {
						continue
					}
					if workResult.Work.ExecutionState == core.WorkExecutionStateInProgress ||
						workResult.Work.ExecutionState == core.WorkExecutionStateClaimed {
						verified[workID] = true
						flight := &inFlightJob{
							workID:  workID,
							jobID:   jobID,
							adapter: "dispatch",
						}
						// Get config path from the binary's default
						configPath := ""
						go handleJobCompletion(ctx, svc, selfBin, configPath, cwd, workID, flight, "codex")
					}
				}
			}
		}
	}
}

func registerAPIHandlers(mux *http.ServeMux, svc *service.Service, cwd string) {
	// Work items list
	mux.HandleFunc("/api/work/items", func(w http.ResponseWriter, r *http.Request) {
		items, err := svc.ListWork(r.Context(), service.WorkListRequest{Limit: 500})
		if err != nil {
			writeJSONHTTP(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSONHTTP(w, 200, items)
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
		supPath := filepath.Join(cwd, ".cagent", "supervisor.json")
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

	// Full diff
	mux.HandleFunc("/api/diff", func(w http.ResponseWriter, r *http.Request) {
		out, _ := exec.CommandContext(r.Context(), "git", "diff", "--patch").Output()
		writeJSONHTTP(w, 200, map[string]any{"diff": string(out)})
	})

	// Bash log
	mux.HandleFunc("/api/bash-log", func(w http.ResponseWriter, r *http.Request) {
		rawDir := filepath.Join(cwd, ".cagent", "raw", "stdout")
		dirs, _ := os.ReadDir(rawDir)

		// Find latest job
		var latestDir string
		for i := len(dirs) - 1; i >= 0; i-- {
			if strings.HasPrefix(dirs[i].Name(), "job_") {
				latestDir = filepath.Join(rawDir, dirs[i].Name())
				break
			}
		}

		if latestDir == "" {
			writeJSONHTTP(w, 200, map[string]any{"commands": []any{}, "job_id": ""})
			return
		}

		files, _ := os.ReadDir(latestDir)
		var commands []map[string]any
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			data, _ := os.ReadFile(filepath.Join(latestDir, f.Name()))
			for _, line := range strings.Split(string(data), "\n") {
				if line == "" {
					continue
				}
				var ev map[string]any
				if json.Unmarshal([]byte(line), &ev) != nil {
					continue
				}
				if ev["type"] == "item.completed" {
					item, _ := ev["item"].(map[string]any)
					if item == nil {
						continue
					}
					if item["type"] == "command_execution" {
						cmd, _ := item["command"].(string)
						if strings.HasPrefix(cmd, "/bin/zsh -lc ") {
							cmd = cmd[13:]
							if (strings.HasPrefix(cmd, "'") && strings.HasSuffix(cmd, "'")) ||
								(strings.HasPrefix(cmd, "\"") && strings.HasSuffix(cmd, "\"")) {
								cmd = cmd[1 : len(cmd)-1]
							}
						}
						exitCode, _ := item["exit_code"].(float64)
						output, _ := item["aggregated_output"].(string)
						if len(output) > 500 {
							output = output[:500]
						}
						commands = append(commands, map[string]any{
							"command":        cmd,
							"exit_code":      int(exitCode),
							"output_preview": output,
						})
					} else if item["type"] == "agent_message" {
						text, _ := item["text"].(string)
						if len(text) > 300 {
							text = text[:300]
						}
						if text != "" {
							commands = append(commands, map[string]any{"comment": text})
						}
					}
				}
			}
		}

		writeJSONHTTP(w, 200, map[string]any{
			"commands": commands,
			"job_id":   filepath.Base(latestDir),
		})
	})
}

func writeJSONHTTP(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// handleJobCompletion dispatches an attestation run after a worker completes.
// The attestor independently reviews: did files change? do changes match the
// objective? did the worker report verification results? does it build?
// The attestor marks the work item done (pass) or failed (retry with context).
//
// Three-layer model:
//   - Verification: workers verify their own work during their run (tests, build checks)
//     and report findings as notes on the work item.
//   - Attestation: an independent agent reviews after the worker exits. Checks the diff,
//     reads worker findings, runs its own checks, and gates the state transition.
//   - Retry: if attestation fails, the failure reason feeds into the next worker's briefing.
func handleJobCompletion(ctx context.Context, svc *service.Service, selfBin, configPath, cwd, workID string, flight *inFlightJob, defaultAdapter string) {
	workResult, err := svc.Work(ctx, workID)
	if err != nil {
		_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
			WorkID:         workID,
			ExecutionState: core.WorkExecutionStateFailed,
			Message:        fmt.Sprintf("attestation: could not fetch work state: %v", err),
			CreatedBy:      "attestation",
		})
		return
	}

	work := workResult.Work

	// Collect worker's verification findings (notes) for the attestor
	var workerFindings string
	for _, note := range workResult.Notes {
		if note.NoteType == "finding" || note.NoteType == "verification" {
			workerFindings += fmt.Sprintf("- [%s] %s\n", note.NoteType, note.Body)
		}
	}
	if workerFindings == "" {
		workerFindings = "(worker reported no verification findings)"
	}

	attestPrompt := fmt.Sprintf(`You are an attestation agent. A worker just finished work item %s.
Your job is to independently verify the work was done correctly.

## Work item
Title: %s
Objective: %s
Worker adapter: %s
Worker job: %s

## Worker's verification findings
%s

## Attestation procedure
1. Run: git diff --stat
2. If NO files changed (only .cagent/cagent.db or nothing):
   The worker failed silently. Record and fail:
   cagent work note-add %s --type finding --text "attestation: no code changes produced by worker"
   cagent work fail %s --message "attestation: no code changes produced"
   Stop.

3. If files changed, review the diff:
   - Run: git diff
   - Do the changes address the objective?
   - Check build: run the appropriate build command (go build ./... or similar)
   - Check for obvious errors or regressions

4. If the work is correct and complete:
   cagent work note-add %s --type finding --text "attestation: passed — N files changed, builds clean, changes match objective"
   cagent work complete %s --message "attestation: passed"

5. If the work is incorrect or incomplete:
   cagent work note-add %s --type finding --text "attestation: failed — <specific reason with details>"
   cagent work fail %s --message "attestation: <brief reason>"

Be thorough but concise. The failure message will be injected into the retry briefing.`,
		workID, work.Title, work.Objective, flight.adapter, flight.jobID,
		workerFindings,
		workID, workID, workID, workID, workID, workID)

	// Dispatch attestation via opencode/glm-5-turbo
	attestAdapter := "opencode"
	attestModel := "zai-coding-plan/glm-5-turbo"

	args := []string{"run", "--json", "--adapter", attestAdapter, "--cwd", cwd,
		"--model", attestModel, "--work", workID, "--prompt", attestPrompt}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}

	runCmd := exec.Command(selfBin, args...)
	runCmd.Dir = cwd
	runCmd.Stderr = nil
	runCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, spawnErr := runCmd.Output()

	if spawnErr != nil {
		_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
			WorkID:         workID,
			ExecutionState: core.WorkExecutionStateFailed,
			Message:        fmt.Sprintf("attestation: dispatch failed: %v", spawnErr),
			CreatedBy:      "attestation",
		})
		return
	}

	// Extract attestation job ID for tracking
	var result struct {
		Job struct {
			JobID string `json:"job_id"`
		} `json:"job"`
	}
	attestJobID := "(unknown)"
	if json.Unmarshal(out, &result) == nil && result.Job.JobID != "" {
		attestJobID = result.Job.JobID
	}

	// Don't mark done — the attestor will do it via cagent work complete/fail
	_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
		WorkID:    workID,
		Message:   fmt.Sprintf("attestation: dispatched %s via %s/%s", attestJobID, attestAdapter, attestModel),
		CreatedBy: "attestation",
	})
}

// runInProcessSupervisor runs the autonomous dispatch loop using the shared Service instance.
// Only active when --auto is set.
func runInProcessSupervisor(ctx context.Context, svc *service.Service, cwd string, root *rootOptions, maxConcurrent int, defaultAdapter string) {
	selfBin, err := os.Executable()
	if err != nil {
		selfBin = "cagent"
	}

	var mu sync.Mutex
	inFlight := make(map[string]*inFlightJob)
	cycle := 0
	leaseDuration := 30 * time.Minute
	leaseRenewInterval := 10 * time.Minute

	for {
		select {
		case <-ctx.Done():
			// Graceful shutdown: cancel in-flight jobs
			mu.Lock()
			for workID, flight := range inFlight {
				_, _ = svc.Cancel(ctx, flight.jobID)
				_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
					WorkID:         workID,
					ExecutionState: core.WorkExecutionStateFailed,
					Message:        "supervisor: cancelled during shutdown",
					CreatedBy:      "supervisor",
				})
			}
			mu.Unlock()
			return
		default:
		}

		cycle++

		// Auto-init
		if cycle == 1 {
			readyWork, _ := svc.ReadyWork(ctx, 1)
			if len(readyWork) == 0 {
				_ = bootstrapRepo(ctx, svc, cwd)
			}
		}

		// Reconcile
		_, _ = svc.ReconcileOnStartup(ctx)

		// Check in-flight
		mu.Lock()
		for workID, flight := range inFlight {
			statusResult, err := svc.Status(ctx, flight.jobID)
			if err != nil {
				continue
			}
			jobState := string(statusResult.Job.State)

			if isTerminal(jobState) {
				if jobState == "failed" || jobState == "cancelled" {
					_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
						WorkID:         workID,
						ExecutionState: core.WorkExecutionStateFailed,
						Message:        fmt.Sprintf("supervisor: job %s %s", flight.jobID, jobState),
						CreatedBy:      "supervisor",
					})
				} else {
					handleJobCompletion(ctx, svc, selfBin, root.configPath, cwd, workID, flight, defaultAdapter)
				}
				delete(inFlight, workID)
			} else if isJobStalled(filepath.Join(cwd, ".cagent", "raw", "stdout", flight.jobID), 10*time.Minute) {
				_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
					WorkID:         workID,
					ExecutionState: core.WorkExecutionStateFailed,
					Message:        fmt.Sprintf("supervisor: job %s stalled (no output for 10m)", flight.jobID),
					CreatedBy:      "supervisor",
				})
				delete(inFlight, workID)
			} else if time.Now().After(flight.leaseNext) {
				_, _ = svc.RenewWorkLease(ctx, service.WorkRenewLeaseRequest{
					WorkID:        workID,
					Claimant:      "supervisor",
					LeaseDuration: leaseDuration,
				})
				flight.leaseNext = time.Now().Add(leaseRenewInterval)
			}
		}
		inFlightCount := len(inFlight)
		mu.Unlock()

		// Dispatch
		if inFlightCount < maxConcurrent {
			readyItems, _ := svc.ReadyWork(ctx, maxConcurrent*2)
			for _, item := range readyItems {
				mu.Lock()
				if len(inFlight) >= maxConcurrent {
					mu.Unlock()
					break
				}
				if _, tracked := inFlight[item.WorkID]; tracked {
					mu.Unlock()
					continue
				}
				mu.Unlock()

				adapter := pickAdapter(item, defaultAdapter)

				claimed, err := svc.ClaimWork(ctx, service.WorkClaimRequest{
					WorkID:        item.WorkID,
					Claimant:      "supervisor",
					LeaseDuration: leaseDuration,
				})
				if err != nil {
					continue
				}

				briefing, err := svc.HydrateWork(ctx, service.WorkHydrateRequest{
					WorkID:   claimed.WorkID,
					Mode:     "standard",
					Claimant: "supervisor",
				})
				if err != nil {
					_, _ = svc.ReleaseWork(ctx, service.WorkReleaseRequest{
						WorkID:   claimed.WorkID,
						Claimant: "supervisor",
					})
					continue
				}

				briefingJSON, _ := json.Marshal(briefing)
				jobID, err := spawnRun(selfBin, root.configPath, adapter, cwd, string(briefingJSON))
				if err != nil {
					_, _ = svc.ReleaseWork(ctx, service.WorkReleaseRequest{
						WorkID:   claimed.WorkID,
						Claimant: "supervisor",
					})
					continue
				}

				mu.Lock()
				inFlight[claimed.WorkID] = &inFlightJob{
					workID:    claimed.WorkID,
					jobID:     jobID,
					adapter:   adapter,
					started:   time.Now(),
					leaseNext: time.Now().Add(leaseRenewInterval),
				}
				mu.Unlock()
			}
		}

		// Write state file
		writeSupState(cwd, cycle, inFlight, supervisorCycleReport{
			Cycle:     cycle,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			InFlight:  len(inFlight),
			Ready:     0,
		})

		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}
