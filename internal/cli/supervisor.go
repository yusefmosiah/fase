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
			mu.Unlock()
			if remaining == 0 {
				if !jsonOutput {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "supervisor: shutdown complete")
				}
				return nil
			}
			if !jsonOutput {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "supervisor: stopping, waiting for %d in-flight job(s)\n", remaining)
			}
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

		// 2. Check in-flight jobs by polling their real job status
		mu.Lock()
		var completed []completedEntry
		for workID, flight := range inFlight {
			jobState, pollErr := pollJobStatus(ctx, selfBin, root.configPath, flight.jobID, cwd)
			if pollErr != nil {
				if !jsonOutput {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: failed to poll job %s: %v\n", flight.jobID, pollErr)
				}
				continue
			}

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

		if opts.dryRun {
			return nil
		}

		sleepOrStop(ctx, opts.interval, sigCh, &stopping)
	}
}

// spawnRun launches `cagent run --json` and extracts the real job_id from the output.
// The run command queues background work and returns immediately with job metadata.
func spawnRun(bin, configPath, adapter, cwd, prompt string) (string, error) {
	args := []string{"run", "--json", "--adapter", adapter, "--cwd", cwd, "--prompt", prompt}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}

	runCmd := exec.Command(bin, args...)
	runCmd.Dir = cwd // ensure cagent resolves .cagent/ from the target repo
	runCmd.Stderr = nil
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

func pickAdapter(item core.WorkItemRecord, defaultAdapter string) string {
	if len(item.PreferredAdapters) > 0 {
		return item.PreferredAdapters[0]
	}
	return defaultAdapter
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
// The objective includes enough context for an agent to analyze the repo
// and create child work items.
func bootstrapRepo(ctx context.Context, svc *service.Service, cwd string) error {
	repoName := filepath.Base(cwd)

	// Gather repo context
	var context strings.Builder
	context.WriteString(fmt.Sprintf("Repository: %s\nPath: %s\n", repoName, cwd))

	// Read README if present
	for _, name := range []string{"README.md", "README", "README.txt", "readme.md"} {
		readmePath := filepath.Join(cwd, name)
		data, err := os.ReadFile(readmePath)
		if err == nil {
			excerpt := string(data)
			if len(excerpt) > 2000 {
				excerpt = excerpt[:2000] + "\n...(truncated)"
			}
			context.WriteString(fmt.Sprintf("\n## %s\n%s\n", name, excerpt))
			break
		}
	}

	// List docs/ if present
	docsDir := filepath.Join(cwd, "docs")
	if entries, err := os.ReadDir(docsDir); err == nil {
		context.WriteString("\n## docs/\n")
		for _, e := range entries {
			if !e.IsDir() {
				context.WriteString(fmt.Sprintf("- %s\n", e.Name()))
			}
		}
	}

	// List top-level structure
	if entries, err := os.ReadDir(cwd); err == nil {
		context.WriteString("\n## Top-level files/dirs\n")
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			marker := ""
			if e.IsDir() {
				marker = "/"
			}
			context.WriteString(fmt.Sprintf("- %s%s\n", e.Name(), marker))
		}
	}

	objective := fmt.Sprintf(`Bootstrap the %s repository into the cagent work graph.

Analyze the codebase, documentation, and project structure. Then:
1. Create root work items for major components or features
2. Create child work items for actionable tasks
3. Set up blocking edges where dependencies exist
4. Add required attestation policies where appropriate
5. Record notes with key findings about the codebase

Use cagent CLI commands to create the work graph:
- cagent work create --title "..." --objective "..." --kind <kind>
- cagent work discover <parent-id> --title "..." --objective "..."
- cagent work note-add <id> --type finding --text "..."
- cagent work update <id> --message "..."

Context:
%s`, repoName, context.String())

	_, err := svc.CreateWork(ctx, service.WorkCreateRequest{
		Title:     fmt.Sprintf("Bootstrap %s", repoName),
		Objective: objective,
		Kind:      "plan",
		Priority:  1,
	})
	return err
}
