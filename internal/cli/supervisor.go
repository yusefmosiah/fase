package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/yusefmosiah/cagent/internal/core"
	"github.com/yusefmosiah/cagent/internal/service"
)

// rotationEntry pairs an adapter name with the model to use for that adapter.
type rotationEntry struct {
	adapter string
	model   string
}

// workRotation is the round-robin pool for dispatch and attestation.
// Order matters: attestation uses offset +1 from the work entry.
var workRotation = []rotationEntry{
	{adapter: "claude", model: "claude-sonnet-4-6"},
	{adapter: "codex", model: "gpt-5.4-mini"},
	{adapter: "opencode", model: "zai-coding-plan/glm-5-turbo"},
}

// globalRotationIdx is incremented each time we dispatch without prior history.
var globalRotationIdx int64

// rotationIndexForAdapter returns the index of adapter in workRotation, or -1.
func rotationIndexForAdapter(adapter string) int {
	for i, e := range workRotation {
		if e.adapter == adapter {
			return i
		}
	}
	return -1
}

// modelForAdapter returns the model string for a known rotation adapter, or "".
func modelForAdapter(adapter string) string {
	for _, e := range workRotation {
		if e.adapter == adapter {
			return e.model
		}
	}
	return ""
}

// pickAdapterModel selects adapter+model for a work item.
// Priority: (1) item.PreferredAdapters, (2) rotation offset from job history,
// (3) global round-robin counter.
// jobs should be ordered newest-first so jobs[0] is the most recent attempt.
// rotation is the effective pool to draw from (budget-filtered); nil falls back to workRotation.
func pickAdapterModel(item core.WorkItemRecord, jobs []core.JobRecord, rotation []rotationEntry) (adapter, model string) {
	pool := rotation
	if len(pool) == 0 {
		pool = workRotation
	}
	if len(item.PreferredAdapters) > 0 {
		a := item.PreferredAdapters[0]
		m := modelForAdapter(a)
		// If the preferred adapter isn't in the pool (budget exhausted), still honour
		// the explicit preference — work items that pin an adapter know what they want.
		return a, m
	}
	if len(jobs) > 0 {
		// Find the most recent job that used a known rotation adapter and
		// advance to the next slot (ensures retries differ from last attempt).
		for _, job := range jobs {
			lastIdx := rotationIndexForEntry(job.Adapter, pool)
			if lastIdx >= 0 {
				next := pool[(lastIdx+1)%len(pool)]
				return next.adapter, next.model
			}
		}
	}
	// No usable history: global round-robin over the effective pool.
	idx := int(atomic.AddInt64(&globalRotationIdx, 1)-1) % len(pool)
	return pool[idx].adapter, pool[idx].model
}

// rotationIndexForEntry returns the index of adapter in the given pool, or -1.
func rotationIndexForEntry(adapter string, pool []rotationEntry) int {
	for i, e := range pool {
		if e.adapter == adapter {
			return i
		}
	}
	return -1
}

// attestAdapterModel returns the adapter+model to use for attestation, offset
// by one slot from the work adapter so a different model independently reviews.
func attestAdapterModel(workAdapter string) (adapter, model string) {
	idx := rotationIndexForAdapter(workAdapter)
	if idx < 0 {
		// Unknown adapter — pick the first rotation entry that differs.
		for _, e := range workRotation {
			if e.adapter != workAdapter {
				return e.adapter, e.model
			}
		}
		return workRotation[0].adapter, workRotation[0].model
	}
	next := workRotation[(idx+1)%len(workRotation)]
	return next.adapter, next.model
}

type supervisorOptions struct {
	interval       time.Duration
	maxConcurrent  int
	defaultAdapter string
	cwd            string
	dryRun         bool
}

// inFlightJob tracks a dispatched job by its real cagent job ID, not a process PID.
type inFlightJob struct {
	workID       string
	jobID        string // real cagent job_id from `run --json` output
	adapter      string
	model        string // model used for this job (for attestation offset)
	started      time.Time
	leaseNext    time.Time // when to renew the lease
	worktreePath string    // absolute path to git worktree (empty if not using worktrees)
	branchName   string    // git branch name for this job's worktree
}

type supervisorCycleReport struct {
	Cycle      int              `json:"cycle"`
	Timestamp  string           `json:"timestamp"`
	Ready      int              `json:"ready"`
	InFlight   int              `json:"in_flight"`
	Dispatched []dispatchEntry  `json:"dispatched,omitempty"`
	Completed  []completedEntry `json:"completed,omitempty"`
	DryRun     bool             `json:"dry_run,omitempty"`
}

type dispatchEntry struct {
	WorkID  string `json:"work_id"`
	Title   string `json:"title"`
	Adapter string `json:"adapter"`
	JobID   string `json:"job_id,omitempty"`
}

type completedEntry struct {
	WorkID string `json:"work_id"`
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

func newSupervisorCommand(root *rootOptions) *cobra.Command {
	opts := &supervisorOptions{
		interval:       30 * time.Second,
		maxConcurrent:  1,
		defaultAdapter: "codex",
		cwd:            ".",
	}

	cmd := &cobra.Command{
		Use:   "supervisor",
		Short: "Run an autonomous dispatch loop that claims and executes ready work",
		Long: `The supervisor polls for ready work items, claims them, hydrates their
briefing, and spawns "cagent run" processes to execute each item. It tracks
real job IDs and polls their status for completion.

The core loop:
  1. Reconcile expired leases
  2. Poll in-flight jobs via "cagent status" and mark work done/failed
  3. Renew leases on still-running jobs
  4. If in-flight < max-concurrent, claim next work and spawn a run
  5. Sleep for the poll interval`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSupervisor(cmd, root, opts)
		},
	}

	cmd.Flags().DurationVar(&opts.interval, "interval", 30*time.Second, "poll interval between cycles")
	cmd.Flags().IntVar(&opts.maxConcurrent, "max-concurrent", 1, "max simultaneous jobs")
	cmd.Flags().StringVar(&opts.defaultAdapter, "default-adapter", "codex", "fallback adapter when work has no preference")
	cmd.Flags().StringVar(&opts.cwd, "cwd", ".", "working directory for spawned jobs")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "show what would be dispatched without doing it")

	return cmd
}

func runSupervisor(cmd *cobra.Command, root *rootOptions, opts *supervisorOptions) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var mu sync.Mutex
	inFlight := make(map[string]*inFlightJob) // keyed by workID
	stopping := false
	cycle := 0

	selfBin, err := os.Executable()
	if err != nil {
		selfBin = "cagent"
	}

	cwd := opts.cwd
	if cwd == "" || cwd == "." {
		cwd, _ = os.Getwd()
	}

	// Load config for budget-aware rotation. Errors are non-fatal; we fall back
	// to the hard-coded workRotation with no budget limits.
	cfg, _ := core.LoadConfig(root.configPath)

	// Update the package-level workRotation from config if entries are provided
	// so that attestation dispatch in serve.go also sees the configured pool.
	if len(cfg.Rotation.Entries) > 0 {
		workRotation = rotationFromConfig(cfg)
	}

	// Budget tracker persists daily run counts to <stateDir>/usage.json.
	stateDir := filepath.Join(cwd, ".cagent")
	budget := newDailyUsage(stateDir)
	limits := buildLimitsMap(cfg)

	jsonOutput := root.jsonOutput
	leaseDuration := 30 * time.Minute
	leaseRenewInterval := 10 * time.Minute // renew well before expiry

	emit := func(report supervisorCycleReport) {
		if jsonOutput {
			_ = writeJSON(cmd.OutOrStdout(), report)
		} else {
			parts := []string{
				fmt.Sprintf("[cycle %d]", report.Cycle),
				fmt.Sprintf("ready=%d", report.Ready),
				fmt.Sprintf("in_flight=%d", report.InFlight),
			}
			for _, d := range report.Dispatched {
				if opts.dryRun {
					parts = append(parts, fmt.Sprintf("would-dispatch %s (%s via %s)", d.WorkID, d.Title, d.Adapter))
				} else {
					parts = append(parts, fmt.Sprintf("dispatched %s job=%s (%s via %s)", d.WorkID, d.JobID, d.Title, d.Adapter))
				}
			}
			for _, c := range report.Completed {
				parts = append(parts, fmt.Sprintf("completed %s job=%s status=%s", c.WorkID, c.JobID, c.Status))
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.Join(parts, " "))
		}
	}

	for {
		cycle++

		select {
		case <-sigCh:
			stopping = true
		default:
		}

		if stopping {
			mu.Lock()
			remaining := len(inFlight)
			// On graceful shutdown, cancel all in-flight jobs rather than
			// waiting indefinitely. Each `cagent run` spawns a detached
			// background worker (via service.spawnDetachedWorker with
			// Setpgid: true), so the worker survives `cagent run` exiting.
			// We must explicitly cancel via the service layer, which sends
			// escalating signals (SIGINT → SIGTERM → SIGKILL) to the
			// worker's process group.
			if remaining > 0 {
				if !jsonOutput {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "supervisor: stopping, cancelling %d in-flight job(s)\n", remaining)
				}
				shutdownSvc, svcErr := service.Open(ctx, root.configPath)
				if svcErr == nil {
					for workID, flight := range inFlight {
						if !jsonOutput {
							_, _ = fmt.Fprintf(cmd.OutOrStdout(), "supervisor: cancelling job %s (work %s)\n", flight.jobID, workID)
						}
						if _, cancelErr := shutdownSvc.Cancel(ctx, flight.jobID); cancelErr != nil {
							if !jsonOutput {
								_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: failed to cancel job %s: %v\n", flight.jobID, cancelErr)
							}
						}
						// Mark the work item as failed so it can be retried later
						_, _ = shutdownSvc.UpdateWork(ctx, service.WorkUpdateRequest{
							WorkID:         workID,
							ExecutionState: core.WorkExecutionStateFailed,
							Message:        fmt.Sprintf("supervisor: job %s cancelled during shutdown", flight.jobID),
							CreatedBy:      "supervisor",
						})
					}
					_ = shutdownSvc.Close()
				} else if !jsonOutput {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: failed to open service for shutdown cleanup: %v\n", svcErr)
				}
			}
			mu.Unlock()
			if !jsonOutput {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "supervisor: shutdown complete")
			}
			return nil
		}

		report := supervisorCycleReport{
			Cycle:     cycle,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			DryRun:    opts.dryRun,
		}

		svc, err := service.Open(ctx, root.configPath)
		if err != nil {
			if !jsonOutput {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: failed to open service: %v\n", err)
			}
			sleepOrStop(ctx, opts.interval, sigCh, &stopping)
			continue
		}

		// 0. Auto-init: if no active work on first cycle, bootstrap
		if cycle == 1 {
			readyWork, listErr := svc.ReadyWork(ctx, 1, false)
			if listErr == nil && len(readyWork) == 0 {
				if !jsonOutput {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "supervisor: empty work graph, bootstrapping %s\n", cwd)
				}
				if bootstrapErr := bootstrapRepo(ctx, svc, cwd); bootstrapErr != nil {
					if !jsonOutput {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: bootstrap failed: %v\n", bootstrapErr)
					}
				}
			}
		}

		// 1. Reconcile expired leases
		if _, reconcileErr := svc.ReconcileOnStartup(ctx); reconcileErr != nil {
			if !jsonOutput {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: reconcile: %v\n", reconcileErr)
			}
		}

		// 2. Check in-flight jobs by querying job status directly (no subprocess)
		mu.Lock()
		var completed []completedEntry
		for workID, flight := range inFlight {
			statusResult, pollErr := svc.Status(ctx, flight.jobID)
			if pollErr != nil {
				if !jsonOutput {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: failed to poll job %s: %v\n", flight.jobID, pollErr)
				}
				continue
			}
			jobState := string(statusResult.Job.State)

			if isTerminal(jobState) {
				status := "done"
				if jobState == "failed" || jobState == "cancelled" {
					status = jobState
				}

				completed = append(completed, completedEntry{
					WorkID: workID,
					JobID:  flight.jobID,
					Status: status,
				})
				delete(inFlight, workID)
			} else if isJobStalled(filepath.Join(cwd, ".cagent", "raw", "stdout", flight.jobID), 10*time.Minute) {
				// No new events for 10 minutes — job is stuck
				_, updateErr := svc.UpdateWork(ctx, service.WorkUpdateRequest{
					WorkID:         workID,
					ExecutionState: core.WorkExecutionStateFailed,
					Message:        fmt.Sprintf("supervisor: job %s stalled (no output for 10m, started %s ago)", flight.jobID, time.Since(flight.started).Truncate(time.Second)),
					CreatedBy:      "supervisor",
				})
				if updateErr != nil && !jsonOutput {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: failed to timeout work %s: %v\n", workID, updateErr)
				}
				completed = append(completed, completedEntry{
					WorkID: workID,
					JobID:  flight.jobID,
					Status: "timeout",
				})
				delete(inFlight, workID)
			} else {
				// 3. Renew lease if needed
				if time.Now().After(flight.leaseNext) {
					_, renewErr := svc.RenewWorkLease(ctx, service.WorkRenewLeaseRequest{
						WorkID:        workID,
						Claimant:      "supervisor",
						LeaseDuration: leaseDuration,
					})
					if renewErr != nil && !jsonOutput {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: failed to renew lease for %s: %v\n", workID, renewErr)
					} else {
						flight.leaseNext = time.Now().Add(leaseRenewInterval)
					}
				}
			}
		}
		inFlightCount := len(inFlight)
		mu.Unlock()
		report.Completed = completed
		report.InFlight = inFlightCount

		// 4. List ready work
		readyItems, err := svc.ReadyWork(ctx, opts.maxConcurrent*2, false)
		if err != nil {
			if !jsonOutput {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: failed to list ready work: %v\n", err)
			}
			_ = svc.Close()
			sleepOrStop(ctx, opts.interval, sigCh, &stopping)
			continue
		}
		report.Ready = len(readyItems)

		// 5. Dispatch new work
		if !stopping {
			for _, item := range readyItems {
				mu.Lock()
				currentInFlight := len(inFlight)
				_, alreadyTracked := inFlight[item.WorkID]
				mu.Unlock()

				if alreadyTracked || currentInFlight >= opts.maxConcurrent {
					if currentInFlight >= opts.maxConcurrent {
						break
					}
					continue
				}

				// Look up job history to inform rotation-based adapter selection.
				var jobHistory []core.JobRecord
				if workDetail, wErr := svc.Work(ctx, item.WorkID); wErr == nil {
					jobHistory = workDetail.Jobs
				}
				// Apply budget filter: skip adapters that have hit their daily limit.
				effectivePool := budgetFilter(workRotation, limits, budget)
				adapter, model := pickAdapterModel(item, jobHistory, effectivePool)

				if opts.dryRun {
					report.Dispatched = append(report.Dispatched, dispatchEntry{
						WorkID:  item.WorkID,
						Title:   item.Title,
						Adapter: adapter,
					})
					continue
				}

				// Claim
				claimed, claimErr := svc.ClaimWork(ctx, service.WorkClaimRequest{
					WorkID:        item.WorkID,
					Claimant:      "supervisor",
					LeaseDuration: leaseDuration,
				})
				if claimErr != nil {
					if !jsonOutput {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: failed to claim %s: %v\n", item.WorkID, claimErr)
					}
					continue
				}

				// Hydrate
				briefing, hydrateErr := svc.HydrateWork(ctx, service.WorkHydrateRequest{
					WorkID:   claimed.WorkID,
					Mode:     "standard",
					Claimant: "supervisor",
				})
				if hydrateErr != nil {
					if !jsonOutput {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: failed to hydrate %s: %v\n", claimed.WorkID, hydrateErr)
					}
					_, _ = svc.ReleaseWork(ctx, service.WorkReleaseRequest{
						WorkID:   claimed.WorkID,
						Claimant: "supervisor",
					})
					continue
				}

				briefingJSON, _ := json.Marshal(briefing)

				// Spawn `cagent run --json` and capture the job ID from stdout
				jobID, spawnErr := spawnRun(selfBin, root.configPath, adapter, model, cwd, string(briefingJSON))
				if spawnErr != nil {
					if !jsonOutput {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: failed to spawn job for %s: %v\n", claimed.WorkID, spawnErr)
					}
					_, _ = svc.ReleaseWork(ctx, service.WorkReleaseRequest{
						WorkID:   claimed.WorkID,
						Claimant: "supervisor",
					})
					continue
				}

				// Record the dispatch in the daily budget tracker.
				budget.recordRun(adapter, model)

				mu.Lock()
				inFlight[claimed.WorkID] = &inFlightJob{
					workID:    claimed.WorkID,
					jobID:     jobID,
					adapter:   adapter,
					model:     model,
					started:   time.Now(),
					leaseNext: time.Now().Add(leaseRenewInterval),
				}
				mu.Unlock()

				report.InFlight = currentInFlight + 1
				report.Dispatched = append(report.Dispatched, dispatchEntry{
					WorkID:  claimed.WorkID,
					Title:   claimed.Title,
					Adapter: adapter,
					JobID:   jobID,
				})
			}
		}

		_ = svc.Close()
		emit(report)

		// Write supervisor state for `cagent status` and mind-graph
		writeSupState(cwd, cycle, inFlight, report)

		if opts.dryRun {
			return nil
		}

		sleepOrStop(ctx, opts.interval, sigCh, &stopping)
	}
}

// spawnRun launches `cagent run --json` and extracts the real job_id from the output.
// The run command queues background work and returns immediately with job metadata.
//
// Process hierarchy and orphan prevention:
//
//	supervisor (this process)
//	  └─ cagent run --json   (short-lived, returns job ID and exits)
//	       └─ __run-job       (detached worker, Setpgid=true, survives parent)
//
// The `cagent run` subprocess is synchronous (we wait for its output). It spawns
// a detached background worker via service.spawnDetachedWorker which sets
// Setpgid: true, placing the worker in its own process group. This means:
//
//  1. The worker intentionally survives `cagent run` exiting.
//  2. If the supervisor is killed (SIGKILL), workers will NOT be automatically
//     cleaned up because they are in separate process groups. This is by design
//     for crash resilience — workers can finish their work even if the supervisor
//     dies. On restart, the supervisor reconciles via lease expiry.
//  3. On graceful shutdown (SIGINT/SIGTERM), the supervisor explicitly cancels
//     each in-flight job via svc.Cancel(), which sends escalating signals
//     (SIGINT → SIGTERM → SIGKILL) to the worker's process group.
//
// We still set Setpgid on the `cagent run` command itself so it doesn't receive
// signals meant for the supervisor's terminal session (e.g., Ctrl+C) before we
// get a chance to do orderly cleanup.
func spawnRun(bin, configPath, adapter, model, cwd, prompt string) (string, error) {
	args := []string{"run", "--json", "--adapter", adapter, "--cwd", cwd, "--prompt", prompt}
	if model != "" {
		args = append(args, "--model", model)
	}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}

	runCmd := exec.Command(bin, args...)
	runCmd.Dir = cwd // ensure cagent resolves .cagent/ from the target repo
	runCmd.Stderr = nil
	runCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := runCmd.Output()
	if err != nil {
		return "", fmt.Errorf("cagent run failed: %w", err)
	}

	// Parse the JSON output to get the job ID
	var result struct {
		Job struct {
			JobID string `json:"job_id"`
		} `json:"job"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return "", fmt.Errorf("failed to parse run output: %w", err)
	}
	if result.Job.JobID == "" {
		return "", fmt.Errorf("run returned no job_id")
	}
	return result.Job.JobID, nil
}

// pollJobStatus calls `cagent status --json <job-id>` and returns the job state.
func pollJobStatus(ctx context.Context, bin, configPath, jobID, cwd string) (string, error) {
	args := []string{"status", "--json", jobID}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}

	statusCmd := exec.CommandContext(ctx, bin, args...)
	if cwd != "" {
		statusCmd.Dir = cwd
	}
	statusCmd.Stderr = nil
	out, err := statusCmd.Output()
	if err != nil {
		return "", fmt.Errorf("cagent status failed: %w", err)
	}

	var result struct {
		Job struct {
			State string `json:"state"`
		} `json:"job"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return "", fmt.Errorf("failed to parse status output: %w", err)
	}
	return result.Job.State, nil
}

func isTerminal(state string) bool {
	switch state {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// isJobStalled checks if a job has produced no new output for the given duration.
// It looks at the modification time of the newest JSONL file in the job's raw stdout dir.
func isJobStalled(jobRawDir string, threshold time.Duration) bool {
	entries, err := os.ReadDir(jobRawDir)
	if err != nil {
		return false // can't determine, assume not stalled
	}
	var newest time.Time
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return false // no files yet, job might just be starting
	}
	return time.Since(newest) > threshold
}

type supervisorState struct {
	PID       int                   `json:"pid"`
	Cycle     int                   `json:"cycle"`
	Timestamp string                `json:"timestamp"`
	Uptime    string                `json:"uptime,omitempty"`
	InFlight  []inFlightState       `json:"in_flight"`
	Ready     int                   `json:"ready"`
	Report    supervisorCycleReport `json:"last_report"`
}

type inFlightState struct {
	WorkID  string `json:"work_id"`
	JobID   string `json:"job_id"`
	Adapter string `json:"adapter"`
	Elapsed string `json:"elapsed"`
}

var supervisorStart = time.Now()

func writeSupState(cwd string, cycle int, inFlight map[string]*inFlightJob, report supervisorCycleReport) {
	stateDir := filepath.Join(cwd, ".cagent")
	_ = os.MkdirAll(stateDir, 0o755)

	flights := make([]inFlightState, 0, len(inFlight))
	for _, f := range inFlight {
		flights = append(flights, inFlightState{
			WorkID:  f.workID,
			JobID:   f.jobID,
			Adapter: f.adapter,
			Elapsed: time.Since(f.started).Truncate(time.Second).String(),
		})
	}

	state := supervisorState{
		PID:       os.Getpid(),
		Cycle:     cycle,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Uptime:    time.Since(supervisorStart).Truncate(time.Second).String(),
		InFlight:  flights,
		Ready:     report.Ready,
		Report:    report,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(stateDir, "supervisor.json"), data, 0o644)
}

func sleepOrStop(ctx context.Context, d time.Duration, sigCh chan os.Signal, stopping *bool) {
	select {
	case <-time.After(d):
	case <-sigCh:
		*stopping = true
	case <-ctx.Done():
		*stopping = true
	}
}

// bootstrapRepo creates a root "bootstrap" work item from repo metadata.
// It deeply walks the doc tree, reads index files and frontmatter, and
// generates an objective that teaches the agent to mirror the project's
// own documentation ontology as the work graph.
func bootstrapRepo(ctx context.Context, svc *service.Service, cwd string) error {
	repoName := filepath.Base(cwd)
	adrInfo := discoverADRNumbering(cwd)
	adrExample := "ADR-0001"
	if adrInfo.highest > 0 {
		adrExample = fmt.Sprintf("ADR-%04d", adrInfo.highest+1)
	}

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("Repository: %s\nPath: %s\n", repoName, cwd))

	// Read README
	for _, name := range []string{"README.md", "README", "readme.md"} {
		data, err := os.ReadFile(filepath.Join(cwd, name))
		if err == nil {
			excerpt := string(data)
			if len(excerpt) > 3000 {
				excerpt = excerpt[:3000] + "\n...(truncated)"
			}
			buf.WriteString(fmt.Sprintf("\n## %s\n%s\n", name, excerpt))
			break
		}
	}

	// Read doc indexes (ATLAS.md, NARRATIVE_INDEX.md, etc.)
	for _, idx := range []string{
		"docs/ATLAS.md", "docs/README.md", "docs/INDEX.md",
		"docs/architecture/NARRATIVE_INDEX.md", "ARCHITECTURE.md",
	} {
		data, err := os.ReadFile(filepath.Join(cwd, idx))
		if err == nil {
			excerpt := string(data)
			if len(excerpt) > 3000 {
				excerpt = excerpt[:3000] + "\n...(truncated)"
			}
			buf.WriteString(fmt.Sprintf("\n## %s\n%s\n", idx, excerpt))
		}
	}

	// Deep walk docs/ tree — list all docs with their paths
	docsDir := filepath.Join(cwd, "docs")
	if _, err := os.Stat(docsDir); err == nil {
		buf.WriteString("\n## Full docs/ tree\n")
		docCount := 0
		_ = filepath.Walk(docsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".md") {
				return nil
			}
			rel, _ := filepath.Rel(cwd, path)
			// Read first 3 lines for frontmatter/title
			header := readFileHeader(path, 5)
			buf.WriteString(fmt.Sprintf("- %s", rel))
			if header != "" {
				buf.WriteString(fmt.Sprintf("  [%s]", header))
			}
			buf.WriteString("\n")
			docCount++
			return nil
		})
		buf.WriteString(fmt.Sprintf("\n(%d docs total)\n", docCount))
	}

	// Top-level code structure
	if entries, err := os.ReadDir(cwd); err == nil {
		buf.WriteString("\n## Top-level structure\n")
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			marker := ""
			if e.IsDir() {
				marker = "/"
			}
			buf.WriteString(fmt.Sprintf("- %s%s\n", e.Name(), marker))
		}
	}

	if adrInfo.highest > 0 {
		buf.WriteString("\n## ADR numbering\n")
		buf.WriteString(fmt.Sprintf(
			"Highest discovered ADR: ADR-%04d\nContinue numbering from: ADR-%04d\n",
			adrInfo.highest, adrInfo.highest+1,
		))
		if len(adrInfo.samples) > 0 {
			buf.WriteString("Discovered ADR references:\n")
			for _, sample := range adrInfo.samples {
				buf.WriteString(fmt.Sprintf("- %s\n", sample))
			}
		}
	}

	objective := fmt.Sprintf(`Bootstrap the %s repository into the cagent work graph by ingesting its documentation.

## Approach

1. DISCOVER THE DOC ONTOLOGY: Read the doc index files (ATLAS.md, README, etc.) to understand how this project organizes its documentation. Look for categories, taxonomies, and hierarchies that the project itself defines. Do NOT impose an external structure — mirror the project's own organization.

2. RESPECT EXISTING ADR NUMBERING: If the repository already uses ADR numbering, continue from the highest discovered ADR number rather than restarting at ADR-0001. The repo context below includes the highest discovered ADR, if any.

3. CREATE WORK ITEMS FROM DOCS: Each significant document (ADR, design doc, spec, guide) should become a work item. The work item's state should reflect the document's status:
   - Documents in "theory" or "proposed" or "draft" directories → kind=plan, state=ready
   - Documents in "practice" or "accepted" or "implemented" directories → kind=implement, state=done
   - Test reports, audits, load tests → these are attestation evidence, not work items. Record them as attestations on the relevant work item using: cagent work attest <work-id> --result passed --summary "..." --verifier-kind deterministic --method test
   - Guides and runbooks → attach as notes on the relevant work item
   - Archived/deprecated docs → skip (historical context, not active work)

4. CREATE EDGES FROM DEPENDENCIES: If documents reference each other (e.g., "Requires: ADR-0014" or "Dependencies: ..."), create blocking edges between the corresponding work items.

5. READ KEY DOCUMENTS: For each work item you create, read the actual document content and set the objective to a meaningful summary of what the document describes. Include the document path in the objective so it can be found.

6. RECORD ATTESTATION EVIDENCE: Test reports and validation results should be recorded as attestations on the work items they validate. Use the report's findings as the attestation summary.

## Commands

Create work items:
  cagent work create --title "%s: Per-User VM Lifecycle" --objective "..." --kind plan --priority 2

Record attestations (for test reports):
  cagent work attest <work-id> --result passed --summary "Load test: 62 concurrent VMs, KSM saving 1.7GB" --verifier-kind deterministic --method test

Add notes (for guides, snapshots, findings):
  cagent work note-add <work-id> --type finding --text "Implementation guide at docs/theory/guides/adr-0014-implementation.md"

Mark implemented items as done via attestation (bootstrap only — normal workers must NOT call this):
  cagent work attest <work-id> --result passed --summary "Implemented and operational per practice/decisions/"

Create dependency edges between work items:
  cagent work proposal create --type add_edge --target <blocked-work-id> --rationale "ADR-0024 requires ADR-0014" --patch '{"edge_type":"blocks","source_work_id":"<blocker-work-id>"}'

## Repository Context

%s`, repoName, adrExample, buf.String())

	_, err := svc.CreateWork(ctx, service.WorkCreateRequest{
		Title:     fmt.Sprintf("Bootstrap %s", repoName),
		Objective: objective,
		Kind:      "plan",
		Priority:  1,
	})
	return err
}

type adrNumberingInfo struct {
	highest int
	samples []string
}

var adrNumberPattern = regexp.MustCompile(`(?i)\bADR-(\d+)\b`)

func discoverADRNumbering(cwd string) adrNumberingInfo {
	docsDir := filepath.Join(cwd, "docs")
	info := adrNumberingInfo{}
	if _, err := os.Stat(docsDir); err != nil {
		return info
	}

	seen := map[int]string{}
	_ = filepath.Walk(docsDir, func(path string, fileInfo os.FileInfo, err error) error {
		if err != nil || fileInfo.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".md") {
			return nil
		}
		rel, relErr := filepath.Rel(cwd, path)
		if relErr != nil {
			rel = path
		}
		header := readFileHeader(path, 20)
		if header == "" {
			return nil
		}
		matches := adrNumberPattern.FindAllStringSubmatch(header, -1)
		if len(matches) == 0 {
			return nil
		}
		seenNumbers := map[int]struct{}{}
		for _, match := range matches {
			n, convErr := strconv.Atoi(match[1])
			if convErr != nil || n <= 0 {
				continue
			}
			if n > info.highest {
				info.highest = n
			}
			if _, ok := seenNumbers[n]; ok {
				continue
			}
			seenNumbers[n] = struct{}{}
			if _, ok := seen[n]; !ok {
				snippet := readFileHeader(path, 5)
				if snippet != "" {
					seen[n] = fmt.Sprintf("%s [%s]", rel, snippet)
				} else {
					seen[n] = rel
				}
			}
		}
		return nil
	})

	if len(seen) == 0 {
		return info
	}
	keys := make([]int, 0, len(seen))
	for n := range seen {
		keys = append(keys, n)
	}
	sort.Ints(keys)
	for _, n := range keys {
		info.samples = append(info.samples, seen[n])
	}
	return info
}

// readFileHeader reads the first N lines of a file and returns a compact summary.
func readFileHeader(path string, maxLines int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.SplitN(string(data), "\n", maxLines+1)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	// Extract title or frontmatter hints
	var parts []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "---" {
			continue
		}
		// Frontmatter fields
		if strings.HasPrefix(line, "Date:") || strings.HasPrefix(line, "Status:") ||
			strings.HasPrefix(line, "Kind:") || strings.HasPrefix(line, "Priority:") {
			parts = append(parts, line)
			continue
		}
		// Markdown title (H1 or H2 — ADR numbers appear in both)
		if strings.HasPrefix(line, "## ") {
			parts = append(parts, line[3:])
			continue
		}
		if strings.HasPrefix(line, "# ") {
			parts = append(parts, line[2:])
			continue
		}
	}
	return strings.Join(parts, " | ")
}

// gitIsRepo returns true if cwd is inside a git repository.
func gitIsRepo(cwd string) bool {
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--git-dir")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// createWorktree creates a git worktree at .cagent/worktrees/<workID> on a new
// branch cagent/work_<workID>. Returns the worktree path and branch name.
func createWorktree(cwd, workID string) (worktreePath, branchName string, err error) {
	branchName = "cagent/work_" + workID
	worktreePath = filepath.Join(cwd, ".cagent", "worktrees", workID)

	if err = os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", "", fmt.Errorf("create worktrees dir: %w", err)
	}

	cmd := exec.Command("git", "-C", cwd, "worktree", "add", "-b", branchName, worktreePath)
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return "", "", fmt.Errorf("git worktree add: %w: %s", runErr, strings.TrimSpace(string(out)))
	}
	return worktreePath, branchName, nil
}

// removeWorktree removes a git worktree and prunes stale entries.
func removeWorktree(cwd, worktreePath string) error {
	cmd := exec.Command("git", "-C", cwd, "worktree", "remove", "--force", worktreePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w: %s", err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("git", "-C", cwd, "worktree", "prune").Run()
	return nil
}

// mergeWorktree merges branchName into the current branch of cwd with --no-ff.
// Returns (conflicted=true, nil) when there are merge conflicts so the caller
// can spawn a resolver rather than treating the error as fatal.
func mergeWorktree(cwd, branchName string) (conflicted bool, err error) {
	cmd := exec.Command("git", "-C", cwd, "merge", "--no-ff", branchName,
		"-m", "Merge "+branchName)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		outStr := string(out)
		if strings.Contains(outStr, "CONFLICT") || strings.Contains(outStr, "conflict") {
			// Leave the conflicted state; caller must resolve or abort.
			return true, nil
		}
		return false, fmt.Errorf("git merge: %w: %s", runErr, strings.TrimSpace(outStr))
	}
	return false, nil
}

// deleteBranch deletes a local branch (best-effort, ignores errors).
func deleteBranch(cwd, branchName string) {
	_ = exec.Command("git", "-C", cwd, "branch", "-d", branchName).Run()
}

// spawnMergeResolver launches a cagent run job that resolves merge conflicts.
// conflictFiles is the list of paths that have conflict markers.
func spawnMergeResolver(selfBin, configPath, cwd, workID, branchName string) error {
	prompt := fmt.Sprintf(`You are a merge-conflict resolver.
A git merge of branch %s into the main working directory failed with conflicts.

Working directory: %s
Work item ID: %s

Steps:
1. Run: git status   — identify conflicted files
2. For each conflicted file: open it, resolve the conflict markers (<<<<<<<, =======, >>>>>>>)
3. Run: git add <resolved-files>
4. Run: git commit -m "Resolve merge conflicts from %s"
5. Add a note: cagent work note-add %s --type finding --text "Merge conflicts resolved automatically"

Only resolve conflicts; do not make other changes.`, branchName, cwd, workID, branchName, workID)

	args := []string{"run", "--json", "--adapter", "claude", "--cwd", cwd, "--prompt", prompt}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	cmd := exec.Command(selfBin, args...)
	cmd.Dir = cwd
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}
