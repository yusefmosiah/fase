package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/yusefmosiah/fase/internal/core"
	"github.com/yusefmosiah/fase/internal/service"
)

type ProcessEvent struct {
	WorkID   string
	JobID    string
	PID      int
	ExitCode int
	ExitErr  error
}

type eventDrivenLoop struct {
	supervisorLoop

	processCh    chan ProcessEvent
	health       *AdapterHealthTracker
	recovery     *RecoveryEngine
	dispatchMu   sync.Mutex
	lastDispatch time.Time
}

const (
	maxRetries           = 10
	retryBackoffBase     = 2 * time.Minute
	retryBackoffMax      = 60 * time.Minute
	retryBackoffCapCount = 5
)

func newEventDrivenLoop(maxConcurrent int, cwd, selfBin, configPath string) *eventDrivenLoop {
	return &eventDrivenLoop{
		supervisorLoop: supervisorLoop{
			maxConcurrent:      maxConcurrent,
			leaseDuration:      30 * time.Minute,
			leaseRenewInterval: 10 * time.Minute,
			cwd:                cwd,
			selfBin:            selfBin,
			configPath:         configPath,
			inFlight:           make(map[string]*inFlightJob),
		},
		processCh: make(chan ProcessEvent, 64),
		health:    newAdapterHealthTracker(filepath.Join(cwd, ".fase")),
		recovery:  newRecoveryEngine(),
	}
}

const dispatchDebounce = 500 * time.Millisecond

func (l *eventDrivenLoop) run(ctx context.Context, svc *service.Service) {
	workCh := svc.Events.SubscribeWithBuffer(256)
	defer svc.Events.Unsubscribe(workCh)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	l.fullCycle(ctx, svc)

	for {
		select {
		case <-ctx.Done():
			l.cancelInFlight(ctx, svc)
			return

		case ev, ok := <-workCh:
			if !ok {
				return
			}
			l.handleWorkEvent(ctx, svc, ev)

		case pev := <-l.processCh:
			l.handleProcessExit(ctx, svc, pev)

		case <-ticker.C:
			l.heartbeat(ctx, svc)
		}
	}
}

func (l *eventDrivenLoop) fullCycle(ctx context.Context, svc *service.Service) {
	l.cycle++
	if l.cycle == 1 {
		if allWork, err := svc.ListWork(ctx, service.WorkListRequest{Limit: 1, IncludeArchived: true}); err == nil && len(allWork) == 0 {
			_ = bootstrapRepo(ctx, svc, l.cwd)
		}
	}

	if l.cycle == 1 {
		_, _ = svc.ReconcileOnStartup(ctx)
	} else {
		_, _ = svc.ReconcileExpiredLeases(ctx)
	}

	l.pollInFlight(ctx, svc)
	l.tryDispatch(ctx, svc)
}

func (l *eventDrivenLoop) handleWorkEvent(ctx context.Context, svc *service.Service, ev service.WorkEvent) {
	switch ev.Kind {
	case service.WorkEventCreated:
		l.tryDispatch(ctx, svc)

	case service.WorkEventUpdated:
		switch ev.State {
		case string(core.WorkExecutionStateDone),
			string(core.WorkExecutionStateFailed),
			string(core.WorkExecutionStateCancelled):
			l.completeInFlight(ev.WorkID, ev.State, svc)
			l.tryDispatch(ctx, svc)
		case string(core.WorkExecutionStateReady):
			l.tryDispatch(ctx, svc)
		}

	case service.WorkEventReleased:
		l.completeInFlight(ev.WorkID, "released", svc)
		l.tryDispatch(ctx, svc)

	case service.WorkEventAttested:
		l.tryDispatch(ctx, svc)

	case service.WorkEventLeaseRenew:
		l.mu.Lock()
		if flight, ok := l.inFlight[ev.WorkID]; ok {
			flight.leaseNext = time.Now().Add(l.leaseRenewInterval)
		}
		l.mu.Unlock()
	}
}

func (l *eventDrivenLoop) handleProcessExit(ctx context.Context, svc *service.Service, ev ProcessEvent) {
	l.mu.Lock()
	flight, tracked := l.inFlight[ev.WorkID]
	if tracked {
		delete(l.inFlight, ev.WorkID)
	}
	l.mu.Unlock()

	if !tracked {
		return
	}

	removeCredentialFile(flight.tokenPath)
	removeSSHKeyFile(flight.sshKeyPath)
	if l.ca != nil && flight.agentEmail != "" {
		l.ca.removeAllowedSigner(flight.agentEmail)
	}

	statusResult, pollErr := svc.Status(ctx, flight.jobID)
	if pollErr == nil {
		jobState := string(statusResult.Job.State)
		if isTerminal(jobState) {
			l.finishJob(flight, ev.WorkID, jobState, svc)
			return
		}
	}

	if terminalState, ok := waitForTerminalJobState(ctx, svc, flight.jobID, 5*time.Second); ok {
		l.finishJob(flight, ev.WorkID, terminalState, svc)
		return
	}

	if workResult, err := svc.Work(ctx, flight.workID); err == nil {
		switch workResult.Work.ExecutionState {
		case core.WorkExecutionStateDone,
			core.WorkExecutionStateFailed,
			core.WorkExecutionStateCancelled:
			l.finishJob(flight, ev.WorkID, string(workResult.Work.ExecutionState), svc)
			return
		}
	}

	if ev.ExitCode == 0 {
		if l.recovery.handlePrematureExit(ctx, svc, l, flight, ev, l.cwd) {
			return
		}
	} else {
		l.recovery.handleCrash(ctx, svc, l, flight, ev, l.cwd)
	}
	l.tryDispatch(ctx, svc)
}

func (l *eventDrivenLoop) heartbeat(ctx context.Context, svc *service.Service) {
	_, _ = svc.ReconcileExpiredLeases(ctx)

	l.pollInFlight(ctx, svc)
	l.tryDispatch(ctx, svc)
}

func (l *eventDrivenLoop) pollInFlight(ctx context.Context, svc *service.Service) {
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
			_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
				WorkID:         workID,
				ExecutionState: core.WorkExecutionStateFailed,
				Message:        fmt.Sprintf("supervisor: job %s stalled (no output for 10m, started %s ago)", flight.jobID, time.Since(flight.started).Truncate(time.Second)),
				CreatedBy:      "supervisor",
			})
			l.recovery.recordStallRecovery(ctx, svc, l, workID, flight, l.cwd)
			completed = append(completed, completedJob{workID, flight, "stalled"})
			delete(l.inFlight, workID)

		case time.Now().After(flight.leaseNext):
			_, _ = svc.RenewWorkLease(ctx, service.WorkRenewLeaseRequest{
				WorkID:        workID,
				Claimant:      "supervisor",
				LeaseDuration: l.leaseDuration,
			})
			flight.leaseNext = time.Now().Add(l.leaseRenewInterval)
		}
	}
	l.mu.Unlock()

	for _, c := range completed {
		l.finishJob(c.flight, c.workID, c.state, svc)
	}
}

func (l *eventDrivenLoop) completeInFlight(workID, state string, svc *service.Service) {
	l.mu.Lock()
	flight, ok := l.inFlight[workID]
	if ok {
		delete(l.inFlight, workID)
	}
	l.mu.Unlock()

	if !ok {
		return
	}

	l.finishJob(flight, workID, state, svc)
}

func (l *eventDrivenLoop) finishJob(flight *inFlightJob, workID, state string, svc *service.Service) {
	removeCredentialFile(flight.tokenPath)
	removeSSHKeyFile(flight.sshKeyPath)
	if l.ca != nil && flight.agentEmail != "" {
		l.ca.removeAllowedSigner(flight.agentEmail)
	}

	if flight.worktreePath != "" && flight.branchName != "" {
		if state == "completed" {
			if conflicted, _ := mergeWorktree(l.cwd, flight.branchName); conflicted {
				_ = spawnMergeResolver(l.selfBin, l.configPath, l.cwd, flight.workID, flight.branchName)
			}
		}
		_ = removeWorktree(l.cwd, flight.worktreePath)
		deleteBranch(l.cwd, flight.branchName)
	}

	switch state {
	case "completed":
		l.health.recordSuccess(flight.adapter, flight.model, time.Since(flight.started))
	case "failed", "cancelled", "stalled", "process_dead":
		l.health.recordFailure(flight.adapter, flight.model)
	}

	if l.onJobCompleted != nil {
		l.onJobCompleted(workID, flight.jobID, state)
	}
}

func (l *eventDrivenLoop) tryDispatch(ctx context.Context, svc *service.Service) {
	l.dispatchMu.Lock()
	defer l.dispatchMu.Unlock()

	if time.Since(l.lastDispatch) < dispatchDebounce {
		return
	}

	select {
	case <-ctx.Done():
		return
	default:
	}
	if l.paused.Load() {
		return
	}

	l.mu.Lock()
	currentInFlight := len(l.inFlight)
	l.mu.Unlock()

	availableSlots := l.maxConcurrent - currentInFlight
	if availableSlots <= 0 {
		return
	}
	readyItems, _ := svc.ReadyWork(ctx, availableSlots*2, false)
	if len(readyItems) == 0 {
		return
	}

	for _, item := range readyItems {
		l.mu.Lock()
		currentInFlight = len(l.inFlight)
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

		retryCount := 0
		var lastFailedAt time.Time
		for _, job := range jobHistory {
			if job.State == core.JobStateFailed || job.State == core.JobStateCancelled {
				retryCount++
				if job.FinishedAt != nil && job.FinishedAt.After(lastFailedAt) {
					lastFailedAt = *job.FinishedAt
				}
			}
		}
		if retryCount >= maxRetries {
			_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
				WorkID:         item.WorkID,
				ExecutionState: core.WorkExecutionStateFailed,
				Message:        fmt.Sprintf("supervisor: work item exhausted %d retries, marking as permanently failed", maxRetries),
				CreatedBy:      "supervisor",
				Metadata:       map[string]any{"failure_reason": "max_retries_exceeded"},
			})
			_, _ = svc.AddWorkNote(ctx, service.WorkNoteRequest{
				WorkID:    item.WorkID,
				NoteType:  "finding",
				Body:      fmt.Sprintf("[fase:message from=\"supervisor\" type=\"info\"] Work item has failed %d times (max retries). Marking as permanently failed. Human review required.\n[/fase:message]", maxRetries),
				CreatedBy: "supervisor",
			})
			continue
		}
		if retryCount > 0 && !lastFailedAt.IsZero() {
			backoffCount := retryCount
			if backoffCount > retryBackoffCapCount {
				backoffCount = retryBackoffCapCount
			}
			backoff := retryBackoffBase
			for i := 1; i < backoffCount; i++ {
				backoff *= 2
				if backoff > retryBackoffMax {
					backoff = retryBackoffMax
					break
				}
			}
			if time.Since(lastFailedAt) < backoff {
				continue
			}
		}
		effectivePool := workRotation
		if l.budget != nil {
			effectivePool = budgetFilter(workRotation, l.limits, l.budget)
		}

		adapter, model := l.scoreAndSelect(item, jobHistory, effectivePool)

		if l.dryRun {
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

		jobCWD := l.cwd
		var worktreePath, branchName string
		if l.maxConcurrent > 1 {
			if wtPath, wtBranch, wtErr := createWorktree(l.cwd, claimed.WorkID); wtErr == nil {
				worktreePath = wtPath
				branchName = wtBranch
				jobCWD = wtPath
			} else {
				fmt.Fprintf(os.Stderr, "supervisor: worktree creation failed for %s, using shared CWD: %v\n", claimed.WorkID, wtErr)
			}
		}

		jobID, spawnErr := spawnRun(l.selfBin, l.configPath, adapter, model, jobCWD, string(briefingJSON), extraEnv)
		if spawnErr != nil {
			if cred != nil {
				removeCredentialFile(cred.tokenPath)
				removeSSHKeyFile(cred.sshKeyPath)
			}
			_, _ = svc.ReleaseWork(ctx, service.WorkReleaseRequest{WorkID: claimed.WorkID, Claimant: "supervisor"})
			l.health.recordFailure(adapter, model)
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
			for _, env := range cred.gitEnv {
				if strings.HasPrefix(env, "GIT_COMMITTER_EMAIL=") {
					agentEmail = strings.TrimPrefix(env, "GIT_COMMITTER_EMAIL=")
					break
				}
			}
		}

		flight := &inFlightJob{
			workID:       claimed.WorkID,
			jobID:        jobID,
			adapter:      adapter,
			model:        model,
			started:      time.Now(),
			leaseNext:    time.Now().Add(l.leaseRenewInterval),
			workerPID:    workerPID,
			worktreePath: worktreePath,
			branchName:   branchName,
			tokenPath:    tokenPath,
			sshKeyPath:   sshKeyPath,
			agentEmail:   agentEmail,
		}

		l.mu.Lock()
		l.inFlight[claimed.WorkID] = flight
		l.mu.Unlock()

		l.health.recordDispatch(adapter, model)

		if workerPID > 0 {
			go watchProcess(workerPID, claimed.WorkID, jobID, l.processCh)
		}

		l.lastDispatch = time.Now()

		if l.onJobStarted != nil {
			l.onJobStarted(claimed.WorkID, jobID, adapter)
		}
	}
}

func (l *eventDrivenLoop) scoreAndSelect(item core.WorkItemRecord, jobs []core.JobRecord, pool []rotationEntry) (adapter, model string) {
	if len(pool) == 0 {
		pool = workRotation
	}

	forbidden := make(map[string]struct{}, len(item.ForbiddenAdapters))
	for _, a := range item.ForbiddenAdapters {
		forbidden[a] = struct{}{}
	}
	avoidModels := make(map[string]struct{}, len(item.AvoidModels))
	for _, m := range item.AvoidModels {
		avoidModels[m] = struct{}{}
	}

	isAllowed := func(adapter, model string) bool {
		if _, ok := forbidden[adapter]; ok {
			return false
		}
		if model != "" {
			if _, ok := avoidModels[model]; ok {
				return false
			}
		}
		return true
	}

	if len(item.PreferredAdapters) > 0 {
		for _, pref := range item.PreferredAdapters {
			for _, e := range pool {
				if e.adapter != pref || !isAllowed(e.adapter, e.model) {
					continue
				}
				if l.health.isCircuitOpen(e.adapter) {
					continue
				}
				if len(item.PreferredModels) == 0 {
					return e.adapter, e.model
				}
				for _, prefModel := range item.PreferredModels {
					if e.model == prefModel {
						return e.adapter, e.model
					}
				}
			}
		}
	}

	type scored struct {
		entry rotationEntry
		score float64
	}

	candidates := make([]scored, 0, len(pool))
	for _, e := range pool {
		if !isAllowed(e.adapter, e.model) {
			continue
		}
		s := l.health.score(e.adapter, e.model, item)
		if l.health.isCircuitOpen(e.adapter) {
			s -= 100
		}
		if len(item.PreferredModels) > 0 {
			for _, prefModel := range item.PreferredModels {
				if e.model == prefModel {
					s += 0.2
					break
				}
			}
		}
		if len(item.PreferredAdapters) > 0 {
			for _, pref := range item.PreferredAdapters {
				if e.adapter == pref {
					s += 0.15
					break
				}
			}
		}
		if l.defaultAdapter != "" && len(jobs) == 0 && len(item.PreferredAdapters) == 0 && len(item.PreferredModels) == 0 && e.adapter == l.defaultAdapter {
			s += 0.05
		}
		candidates = append(candidates, scored{e, s})
	}

	if len(candidates) == 0 {
		return pickAdapterModelWithFallback(item, jobs, pool, l.defaultAdapter)
	}

	if len(jobs) > 0 {
		for _, job := range jobs {
			for i, c := range candidates {
				if c.entry.adapter == job.Adapter {
					candidates[i].score -= 0.3
				}
			}
		}
	}

	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.score > best.score {
			best = c
		}
	}

	return best.entry.adapter, best.entry.model
}

func watchProcess(pid int, workID, jobID string, ch chan<- ProcessEvent) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		if !isProcessAlive(pid) {
			ch <- ProcessEvent{WorkID: workID, JobID: jobID, PID: pid, ExitCode: -1}
			return
		}
		<-ticker.C
	}
}

func waitForTerminalJobState(ctx context.Context, svc *service.Service, jobID string, maxWait time.Duration) (string, bool) {
	deadline := time.Now().Add(maxWait)
	for {
		statusResult, err := svc.Status(ctx, jobID)
		if err == nil {
			state := string(statusResult.Job.State)
			if isTerminal(state) {
				return state, true
			}
		}

		if time.Now().After(deadline) {
			return "", false
		}

		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func runEventDrivenSupervisor(cmd *cobra.Command, root *rootOptions, opts *supervisorOptions) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		cancel()
	}()

	selfBin, err := os.Executable()
	if err != nil {
		selfBin = "fase"
	}

	cwd := opts.cwd
	if cwd == "" || cwd == "." {
		cwd, _ = os.Getwd()
	}

	cfg, _ := core.LoadConfig(root.configPath)
	if len(cfg.Rotation.Entries) > 0 {
		workRotation = rotationFromConfig(cfg)
	}

	stateDir := core.ResolveRepoStateDirFrom(cwd)
	if stateDir == "" {
		stateDir = filepath.Join(cwd, ".fase")
	}

	loop := newEventDrivenLoop(opts.maxConcurrent, cwd, selfBin, root.configPath)
	loop.dryRun = opts.dryRun
	loop.defaultAdapter = opts.defaultAdapter
	loop.budget = newDailyUsage(stateDir)
	loop.limits = buildLimitsMap(cfg)

	ca, caErr := loadOrCreateCA(stateDir)
	if caErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "supervisor: capability CA init failed (tokens disabled): %v\n", caErr)
	}
	if ca != nil {
		sweepStaleTokenFiles(stateDir, 2*time.Hour)
	}
	loop.ca = ca

	svc, svcErr := service.Open(ctx, root.configPath)
	if svcErr != nil {
		return fmt.Errorf("open service: %w", svcErr)
	}
	defer svc.Close()

	fmt.Fprintf(cmd.OutOrStdout(), "supervisor: event-driven mode started (pid %d)\n", os.Getpid())
	loop.run(ctx, svc)
	fmt.Fprintln(cmd.OutOrStdout(), "supervisor: shutdown complete")
	return nil
}
