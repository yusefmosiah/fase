package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yusefmosiah/fase/internal/core"
	"github.com/yusefmosiah/fase/internal/service"
)

// supervisorLoop encapsulates the shared 5-step dispatch state machine.
// Both runSupervisor (standalone) and runInProcessSupervisor (serve --auto)
// compose this type — one implementation, two entry points.
//
// The caller is responsible for service lifecycle (open/close), sleep/wait,
// and shutdown signal handling. supervisorLoop only runs the algorithm.
type supervisorLoop struct {
	maxConcurrent      int
	leaseDuration      time.Duration
	leaseRenewInterval time.Duration
	cwd                string
	selfBin            string
	configPath         string
	defaultAdapter     string
	dryRun             bool
	budget             *dailyUsage    // nil = no budget limits
	limits             map[string]int // adapter/model -> max runs/day
	ca                 *supervisorCA  // nil = no capability tokens issued

	// Optional event hooks (nil = no-op).
	onJobStarted   func(workID, jobID, adapter string) // called after successful dispatch
	onJobCompleted func(workID, jobID, state string)   // called when job reaches terminal state

	// paused prevents dispatch (step 5) while still allowing monitoring,
	// lease renewal, and completion detection (steps 2-4).
	paused atomic.Bool

	// Internal state — guarded by mu.
	mu       sync.Mutex
	inFlight map[string]*inFlightJob
	cycle    int
}

// Pause stops new work dispatch. In-flight jobs continue to be monitored.
func (l *supervisorLoop) Pause() { l.paused.Store(true) }

// Resume re-enables work dispatch.
func (l *supervisorLoop) Resume() { l.paused.Store(false) }

// IsPaused reports whether dispatch is currently paused.
func (l *supervisorLoop) IsPaused() bool { return l.paused.Load() }

func newSupervisorLoop(maxConcurrent int, cwd, selfBin, configPath string) *supervisorLoop {
	return &supervisorLoop{
		maxConcurrent:      maxConcurrent,
		leaseDuration:      30 * time.Minute,
		leaseRenewInterval: 10 * time.Minute,
		cwd:                cwd,
		selfBin:            selfBin,
		configPath:         configPath,
		inFlight:           make(map[string]*inFlightJob),
	}
}

// runOneCycle executes one iteration of the 5-step supervisor algorithm:
//  1. Bootstrap (first cycle only, when graph is empty)
//  2. Reconcile expired leases (full reset on first cycle)
//  3. Poll in-flight jobs; handle completions and stalls
//  4. Renew leases on still-running jobs
//  5. Dispatch ready work to available capacity
//
// svc must already be open; the caller manages its lifecycle.
// Returns the cycle report. State file writing is left to the caller.
func (l *supervisorLoop) runOneCycle(ctx context.Context, svc *service.Service) supervisorCycleReport {
	l.cycle++
	report := supervisorCycleReport{
		Cycle:     l.cycle,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		DryRun:    l.dryRun,
	}

	// Step 0: Bootstrap on first cycle if the work graph is empty.
	if l.cycle == 1 {
		if allWork, err := svc.ListWork(ctx, service.WorkListRequest{Limit: 1, IncludeArchived: true}); err == nil && len(allWork) == 0 {
			_ = bootstrapRepo(ctx, svc, l.cwd)
		}
	}

	// Step 1: Reconcile — full startup reset on cycle 1, lease expiry every cycle.
	if l.cycle == 1 {
		_, _ = svc.ReconcileOnStartup(ctx)
	} else {
		_, _ = svc.ReconcileExpiredLeases(ctx)
	}

	// Steps 2-4: Poll in-flight jobs; renew leases on still-running ones.
	type completedJob struct {
		workID string
		flight *inFlightJob
		state  string
	}
	var completed []completedJob

	l.mu.Lock()
	for workID, flight := range l.inFlight {
		statusResult, pollErr := svc.Status(ctx, flight.jobID)
		if pollErr != nil {
			continue
		}
		jobState := string(statusResult.Job.State)

		switch {
		case isTerminal(jobState):
			completed = append(completed, completedJob{workID, flight, jobState})
			delete(l.inFlight, workID)

		case !isProcessAlive(flight.workerPID):
			_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
				WorkID:         workID,
				ExecutionState: core.WorkExecutionStateFailed,
				Message:        fmt.Sprintf("supervisor: job %s worker process (pid %d) exited unexpectedly", flight.jobID, flight.workerPID),
				CreatedBy:      "supervisor",
			})
			completed = append(completed, completedJob{workID, flight, "process_dead"})
			delete(l.inFlight, workID)

		case isJobStalled(filepath.Join(l.cwd, ".fase", "raw", "stdout", flight.jobID), 10*time.Minute):
			// Mark stalled work as failed so it can be retried.
			_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
				WorkID:         workID,
				ExecutionState: core.WorkExecutionStateFailed,
				Message:        fmt.Sprintf("supervisor: job %s stalled (no output for 10m, started %s ago)", flight.jobID, time.Since(flight.started).Truncate(time.Second)),
				CreatedBy:      "supervisor",
			})
			completed = append(completed, completedJob{workID, flight, "stalled"})
			delete(l.inFlight, workID)

		case time.Now().After(flight.leaseNext):
			// Step 4: Renew lease.
			_, _ = svc.RenewWorkLease(ctx, service.WorkRenewLeaseRequest{
				WorkID:        workID,
				Claimant:      "supervisor",
				LeaseDuration: l.leaseDuration,
			})
			flight.leaseNext = time.Now().Add(l.leaseRenewInterval)
		}
	}
	inFlightCount := len(l.inFlight)
	l.mu.Unlock()

	for _, c := range completed {
		report.Completed = append(report.Completed, completedEntry{
			WorkID: c.workID,
			JobID:  c.flight.jobID,
			Status: c.state,
		})
		removeCredentialFile(c.flight.tokenPath)
		removeSSHKeyFile(c.flight.sshKeyPath)
		if l.ca != nil && c.flight.agentEmail != "" {
			l.ca.removeAllowedSigner(c.flight.agentEmail)
		}
		if l.onJobCompleted != nil {
			l.onJobCompleted(c.workID, c.flight.jobID, c.state)
		}
	}
	report.InFlight = inFlightCount

	// Fetch ready work.
	availableSlots := l.maxConcurrent - inFlightCount
	if availableSlots <= 0 {
		availableSlots = 0
	}
	if availableSlots == 0 {
		return report
	}
	readyItems, _ := svc.ReadyWork(ctx, availableSlots*2, false)
	report.Ready = len(readyItems)

	// Step 5: Dispatch ready work to available capacity.
	// Skip dispatch if context is cancelled (graceful shutdown) or paused.
	select {
	case <-ctx.Done():
		return report
	default:
	}
	if l.paused.Load() {
		report.Paused = true
		return report
	}

	for _, item := range readyItems {
		l.mu.Lock()
		currentInFlight := len(l.inFlight)
		_, alreadyTracked := l.inFlight[item.WorkID]
		l.mu.Unlock()

		if alreadyTracked {
			continue
		}
		if currentInFlight >= l.maxConcurrent {
			break
		}

		var jobHistory []core.JobRecord
		if workDetail, wErr := svc.Work(ctx, item.WorkID); wErr == nil {
			jobHistory = workDetail.Jobs
		}
		effectivePool := workRotation
		if l.budget != nil {
			effectivePool = budgetFilter(workRotation, l.limits, l.budget)
		}
		adapter, model := pickAdapterModelWithFallback(item, jobHistory, effectivePool, l.defaultAdapter)

		if l.dryRun {
			report.Dispatched = append(report.Dispatched, dispatchEntry{
				WorkID:  item.WorkID,
				Title:   item.Title,
				Adapter: adapter,
			})
			continue
		}

		claimed, claimErr := svc.ClaimWork(ctx, service.WorkClaimRequest{
			WorkID:        item.WorkID,
			Claimant:      "supervisor",
			LeaseDuration: l.leaseDuration,
		})
		if claimErr != nil {
			continue
		}

		briefing, hydrateErr := svc.HydrateWork(ctx, service.WorkHydrateRequest{
			WorkID:   claimed.WorkID,
			Mode:     "standard",
			Claimant: "supervisor",
		})
		if hydrateErr != nil {
			_, _ = svc.ReleaseWork(ctx, service.WorkReleaseRequest{WorkID: claimed.WorkID, Claimant: "supervisor"})
			continue
		}

		briefingJSON, _ := json.Marshal(briefing)

		var extraEnv []string
		var cred *dispatchCredential
		if l.ca != nil {
			cred = l.ca.issueAndWriteCredential(
				filepath.Join(l.cwd, ".fase"),
				claimed.WorkID, "worker", adapter, model,
			)
			if cred != nil && cred.tokenPath != "" {
				extraEnv = append(extraEnv, core.EnvAgentToken+"="+cred.tokenPath)
				extraEnv = append(extraEnv, cred.gitEnv...)
			}
		}

		jobID, spawnErr := spawnRun(l.selfBin, l.configPath, adapter, model, l.cwd, string(briefingJSON), extraEnv)
		if spawnErr != nil {
			if cred != nil {
				removeCredentialFile(cred.tokenPath)
				removeSSHKeyFile(cred.sshKeyPath)
			}
			_, _ = svc.ReleaseWork(ctx, service.WorkReleaseRequest{WorkID: claimed.WorkID, Claimant: "supervisor"})
			continue
		}

		workerPID := 0
		if runtimeRec, rtErr := svc.GetJobRuntime(ctx, jobID); rtErr == nil && runtimeRec.SupervisorPID > 0 {
			workerPID = runtimeRec.SupervisorPID
		}

		if l.budget != nil {
			l.budget.recordRun(adapter, model)
		}

		var tokenPath, sshKeyPath, agentEmail string
		if cred != nil {
			tokenPath = cred.tokenPath
			sshKeyPath = cred.sshKeyPath
			// Extract email from gitEnv for allowed_signers cleanup.
			for _, env := range cred.gitEnv {
				if strings.HasPrefix(env, "GIT_COMMITTER_EMAIL=") {
					agentEmail = strings.TrimPrefix(env, "GIT_COMMITTER_EMAIL=")
					break
				}
			}
		}

		l.mu.Lock()
		l.inFlight[claimed.WorkID] = &inFlightJob{
			workID:     claimed.WorkID,
			jobID:      jobID,
			adapter:    adapter,
			model:      model,
			started:    time.Now(),
			leaseNext:  time.Now().Add(l.leaseRenewInterval),
			workerPID:  workerPID,
			tokenPath:  tokenPath,
			sshKeyPath: sshKeyPath,
			agentEmail: agentEmail,
		}
		l.mu.Unlock()

		report.InFlight++
		report.Dispatched = append(report.Dispatched, dispatchEntry{
			WorkID:  claimed.WorkID,
			Title:   claimed.Title,
			Adapter: adapter,
			JobID:   jobID,
		})
		if l.onJobStarted != nil {
			l.onJobStarted(claimed.WorkID, jobID, adapter)
		}
	}

	return report
}

// cancelInFlight cancels all tracked in-flight jobs and marks work as failed.
// svc must be open; the caller is responsible for its lifecycle.
func (l *supervisorLoop) cancelInFlight(ctx context.Context, svc *service.Service) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for workID, flight := range l.inFlight {
		_, _ = svc.Cancel(ctx, flight.jobID)
		_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
			WorkID:         workID,
			ExecutionState: core.WorkExecutionStateFailed,
			Message:        fmt.Sprintf("supervisor: job %s cancelled during shutdown", flight.jobID),
			CreatedBy:      "supervisor",
		})
		removeCredentialFile(flight.tokenPath)
		removeSSHKeyFile(flight.sshKeyPath)
		if l.ca != nil && flight.agentEmail != "" {
			l.ca.removeAllowedSigner(flight.agentEmail)
		}
		if flight.worktreePath != "" {
			_ = removeWorktree(l.cwd, flight.worktreePath)
			deleteBranch(l.cwd, flight.branchName)
		}
	}
}

// snapshotInFlight returns a shallow copy of the current in-flight map.
// Safe to call without holding the lock; used for state file writes.
func (l *supervisorLoop) snapshotInFlight() map[string]*inFlightJob {
	l.mu.Lock()
	defer l.mu.Unlock()
	snap := make(map[string]*inFlightJob, len(l.inFlight))
	for k, v := range l.inFlight {
		snap[k] = v
	}
	return snap
}

// inFlightLen returns the number of currently tracked in-flight jobs.
func (l *supervisorLoop) inFlightLen() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.inFlight)
}
