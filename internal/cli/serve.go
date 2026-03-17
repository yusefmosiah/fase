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
	var noUI bool
	var noBrowser bool
	var maxConcurrent int
	var defaultAdapter string
	var devAssets string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run supervisor + web UI + API in one process",
		Long: `Starts a unified cagent service that runs the supervisor loop,
serves the mind-graph web UI, and provides HTTP API endpoints.
Eliminates DB contention by using a single shared Service instance.

Equivalent to running supervisor + Vite dev server, but in one process
with no Node.js dependency.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd, root, port, noUI, noBrowser, maxConcurrent, defaultAdapter, devAssets)
		},
	}

	cmd.Flags().IntVar(&port, "port", 4242, "HTTP server port")
	cmd.Flags().BoolVar(&noUI, "no-ui", false, "run supervisor only, no web UI (alias for supervisor)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't auto-open browser")
	cmd.Flags().IntVar(&maxConcurrent, "max-concurrent", 1, "max simultaneous jobs")
	cmd.Flags().StringVar(&defaultAdapter, "default-adapter", "codex", "fallback adapter")
	cmd.Flags().StringVar(&devAssets, "dev-assets", "", "serve UI from filesystem instead of embedded (for development)")

	return cmd
}

func runServe(cmd *cobra.Command, root *rootOptions, port int, noUI, noBrowser bool, maxConcurrent int, defaultAdapter, devAssets string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Open service once — shared by supervisor and API handlers
	svc, err := service.Open(ctx, root.configPath)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer func() { _ = svc.Close() }()

	cwd, _ := os.Getwd()

	// Find a free port
	listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		// Try next port
		listener, err = net.Listen("tcp", "localhost:0")
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

	// Start supervisor as goroutine
	var wg sync.WaitGroup
	supervisorDone := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(supervisorDone)
		runInProcessSupervisor(ctx, svc, cwd, root, maxConcurrent, defaultAdapter)
	}()

	// Start HTTP server
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.Serve(listener); err != http.ErrServerClosed {
			fmt.Fprintf(cmd.ErrOrStderr(), "HTTP server error: %v\n", err)
		}
	}()

	url := fmt.Sprintf("http://localhost:%d", actualPort)
	fmt.Fprintf(cmd.OutOrStdout(), "cagent serve: %s (pid %d)\n", url, os.Getpid())

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
	cancel() // stops the supervisor goroutine

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)

	wg.Wait()
	fmt.Fprintln(cmd.OutOrStdout(), "cagent serve: stopped")
	return nil
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

// handleJobCompletion decides whether to mark work done or dispatch verification.
// The verification loop:
//  1. Worker completes → check required attestations
//  2. If attestations unsatisfied → dispatch verifier (different adapter/model)
//  3. Verifier reviews: diff, test output, attestation artifacts
//  4. Verifier attests: passed or failed
//  5. If failed → re-dispatch worker with feedback (iterate)
//  6. If passed → mark work done, set approval_state to pending
func handleJobCompletion(ctx context.Context, svc *service.Service, selfBin, configPath, cwd, workID string, flight *inFlightJob, defaultAdapter string) {
	// Get work item with its required attestations
	workResult, err := svc.Work(ctx, workID)
	if err != nil {
		_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
			WorkID:         workID,
			ExecutionState: core.WorkExecutionStateDone,
			Message:        fmt.Sprintf("supervisor: job %s completed (could not check attestations: %v)", flight.jobID, err),
			CreatedBy:      "supervisor",
		})
		return
	}

	work := workResult.Work
	attestations := workResult.Attestations

	// If no required attestations, mark done immediately (backward compatible)
	if len(work.RequiredAttestations) == 0 {
		_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
			WorkID:         workID,
			ExecutionState: core.WorkExecutionStateDone,
			Message:        fmt.Sprintf("supervisor: job %s completed (no attestation policy)", flight.jobID),
			CreatedBy:      "supervisor",
		})
		return
	}

	// Check which attestation slots are unsatisfied
	unsatisfied := findUnsatisfiedAttestations(work.RequiredAttestations, attestations)
	if len(unsatisfied) == 0 {
		// All attestations satisfied — done, pending approval
		_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
			WorkID:         workID,
			ExecutionState: core.WorkExecutionStateDone,
			Message:        fmt.Sprintf("supervisor: job %s completed, all %d attestations satisfied", flight.jobID, len(work.RequiredAttestations)),
			CreatedBy:      "supervisor",
		})
		// TODO: set approval_state to pending when the service supports it in UpdateWork
		return
	}

	// Attestations unsatisfied — dispatch verification job
	// Use a different adapter than the worker for independent review
	verifierAdapter := pickVerifierAdapter(flight.adapter, defaultAdapter)

	verifyPrompt := fmt.Sprintf(`You are a verification agent reviewing work on: %s

The implementation agent completed its work. Your job is to verify the results
and record attestations. Review:
1. The git diff (run: git diff)
2. Any test output or artifacts
3. The work item's objective and acceptance criteria

For each required attestation that is not yet satisfied, either:
- Record a passing attestation if the work meets the requirement:
  cagent work attest %s --result passed --summary "..." --verifier-kind <kind> --method third_party_review
- Record a failing attestation with specific feedback:
  cagent work attest %s --result failed --summary "..." --verifier-kind <kind> --method third_party_review

Required attestations still needed: %s

Work objective: %s`,
		work.Title, workID, workID,
		formatUnsatisfied(unsatisfied),
		work.Objective)

	// Dispatch verification job
	_, spawnErr := spawnRun(selfBin, configPath, verifierAdapter, cwd, verifyPrompt)
	if spawnErr != nil {
		// Can't dispatch verifier — mark done anyway
		_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
			WorkID:         workID,
			ExecutionState: core.WorkExecutionStateDone,
			Message:        fmt.Sprintf("supervisor: job %s completed, verification dispatch failed: %v", flight.jobID, spawnErr),
			CreatedBy:      "supervisor",
		})
		return
	}

	// Mark as still in progress — the verification job will update attestations
	// The next supervisor cycle will re-check this work item
	_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
		WorkID:  workID,
		Message: fmt.Sprintf("supervisor: dispatched verification to %s (%d attestations unsatisfied)", verifierAdapter, len(unsatisfied)),
		CreatedBy: "supervisor",
	})
}

func findUnsatisfiedAttestations(required []core.RequiredAttestation, actual []core.AttestationRecord) []core.RequiredAttestation {
	var unsatisfied []core.RequiredAttestation
	for _, req := range required {
		if !req.Blocking {
			continue
		}
		satisfied := false
		for _, att := range actual {
			if att.VerifierKind == req.VerifierKind && att.Result == "passed" {
				satisfied = true
				break
			}
		}
		if !satisfied {
			unsatisfied = append(unsatisfied, req)
		}
	}
	return unsatisfied
}

func formatUnsatisfied(reqs []core.RequiredAttestation) string {
	var parts []string
	for _, r := range reqs {
		parts = append(parts, r.VerifierKind)
	}
	return strings.Join(parts, ", ")
}

func pickVerifierAdapter(workerAdapter, defaultAdapter string) string {
	// Use a different adapter than the worker for independent review
	// If worker used codex, verify with claude (and vice versa)
	switch workerAdapter {
	case "codex":
		return "claude"
	case "claude":
		return "codex"
	default:
		if defaultAdapter != workerAdapter {
			return defaultAdapter
		}
		return "claude"
	}
}

// runInProcessSupervisor runs the supervisor loop using the shared Service instance.
// Unlike the subprocess-based supervisor, this doesn't shell out for status checks.
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
					Message:        fmt.Sprintf("supervisor: cancelled during shutdown"),
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
					// Job completed — check if verification is needed
					handleJobCompletion(ctx, svc, selfBin, root.configPath, cwd, workID, flight, defaultAdapter)
				}
				delete(inFlight, workID)
			} else if isJobStalled(filepath.Join(cwd, ".cagent", "raw", "stdout", flight.jobID), 10*time.Minute) {
				// No new events for 10 minutes — job is stuck
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
			Ready:     0, // we'd need to count but this is good enough
		})

		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}
