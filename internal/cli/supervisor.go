package cli

import (
	"context"
	"encoding/json"
	"fmt"
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
)

type supervisorOptions struct {
	interval       time.Duration
	maxConcurrent  int
	defaultAdapter string
	cwd            string
	dryRun         bool
}

// inFlightJob tracks a dispatched job by its real cagent job ID, not a process PID.
type inFlightJob struct {
	workID    string
	jobID     string // real cagent job_id from `run --json` output
	adapter   string
	started   time.Time
	leaseNext time.Time // when to renew the lease
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
			readyWork, listErr := svc.ReadyWork(ctx, 1)
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
				newState := core.WorkExecutionStateDone
				if jobState == "failed" || jobState == "cancelled" {
					status = jobState
					newState = core.WorkExecutionStateFailed
				}

				_, updateErr := svc.UpdateWork(ctx, service.WorkUpdateRequest{
					WorkID:         workID,
					ExecutionState: newState,
					Message:        fmt.Sprintf("supervisor: job %s %s", flight.jobID, status),
					CreatedBy:      "supervisor",
				})
				if updateErr != nil && !jsonOutput {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: failed to update work %s: %v\n", workID, updateErr)
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
		readyItems, err := svc.ReadyWork(ctx, opts.maxConcurrent*2)
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

				adapter := pickAdapter(item, opts.defaultAdapter)

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
				jobID, spawnErr := spawnRun(selfBin, root.configPath, adapter, cwd, string(briefingJSON))
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

				mu.Lock()
				inFlight[claimed.WorkID] = &inFlightJob{
					workID:    claimed.WorkID,
					jobID:     jobID,
					adapter:   adapter,
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
func spawnRun(bin, configPath, adapter, cwd, prompt string) (string, error) {
	args := []string{"run", "--json", "--adapter", adapter, "--cwd", cwd, "--prompt", prompt}
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

func pickAdapter(item core.WorkItemRecord, defaultAdapter string) string {
	if len(item.PreferredAdapters) > 0 {
		return item.PreferredAdapters[0]
	}
	return defaultAdapter
}

type supervisorState struct {
	PID       int                      `json:"pid"`
	Cycle     int                      `json:"cycle"`
	Timestamp string                   `json:"timestamp"`
	Uptime    string                   `json:"uptime,omitempty"`
	InFlight  []inFlightState          `json:"in_flight"`
	Ready     int                      `json:"ready"`
	Report    supervisorCycleReport    `json:"last_report"`
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

	objective := fmt.Sprintf(`Bootstrap the %s repository into the cagent work graph by ingesting its documentation.

## Approach

1. DISCOVER THE DOC ONTOLOGY: Read the doc index files (ATLAS.md, README, etc.) to understand how this project organizes its documentation. Look for categories, taxonomies, and hierarchies that the project itself defines. Do NOT impose an external structure — mirror the project's own organization.

2. CREATE WORK ITEMS FROM DOCS: Each significant document (ADR, design doc, spec, guide) should become a work item. The work item's state should reflect the document's status:
   - Documents in "theory" or "proposed" or "draft" directories → kind=plan, state=ready
   - Documents in "practice" or "accepted" or "implemented" directories → kind=implement, state=done
   - Test reports, audits, load tests → these are attestation evidence, not work items. Record them as attestations on the relevant work item using: cagent work attest <work-id> --result passed --summary "..." --verifier-kind deterministic --method test
   - Guides and runbooks → attach as notes on the relevant work item
   - Archived/deprecated docs → skip (historical context, not active work)

3. CREATE EDGES FROM DEPENDENCIES: If documents reference each other (e.g., "Requires: ADR-0014" or "Dependencies: ..."), create blocking edges between the corresponding work items.

4. READ KEY DOCUMENTS: For each work item you create, read the actual document content and set the objective to a meaningful summary of what the document describes. Include the document path in the objective so it can be found.

5. RECORD ATTESTATION EVIDENCE: Test reports and validation results should be recorded as attestations on the work items they validate. Use the report's findings as the attestation summary.

## Commands

Create work items:
  cagent work create --title "ADR-0014: Per-User VM Lifecycle" --objective "..." --kind plan --priority 2

Record attestations (for test reports):
  cagent work attest <work-id> --result passed --summary "Load test: 62 concurrent VMs, KSM saving 1.7GB" --verifier-kind deterministic --method test

Add notes (for guides, snapshots, findings):
  cagent work note-add <work-id> --type finding --text "Implementation guide at docs/theory/guides/adr-0014-implementation.md"

Update state for implemented items:
  cagent work complete <work-id> --message "Implemented and operational per practice/decisions/"

Create dependency edges between work items:
  cagent work proposal create --type add_edge --target <blocked-work-id> --rationale "ADR-0024 requires ADR-0014" --patch '{"edge_type":"blocks","source_work_id":"<blocker-work-id>"}'

## Repository Context

%s`, repoName, buf.String())

	_, err := svc.CreateWork(ctx, service.WorkCreateRequest{
		Title:     fmt.Sprintf("Bootstrap %s", repoName),
		Objective: objective,
		Kind:      "plan",
		Priority:  1,
	})
	return err
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
		// Markdown title
		if strings.HasPrefix(line, "# ") {
			parts = append(parts, line[2:])
			continue
		}
	}
	return strings.Join(parts, " | ")
}
