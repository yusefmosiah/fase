package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/yusefmosiah/fase/internal/core"
	"github.com/yusefmosiah/fase/internal/service"
)

type RecoveryEngine struct{}

func newRecoveryEngine() *RecoveryEngine {
	return &RecoveryEngine{}
}

func (r *RecoveryEngine) handlePrematureExit(ctx context.Context, svc *service.Service, loop *eventDrivenLoop, flight *inFlightJob, ev ProcessEvent, cwd string) bool {
	commits := gitCommitsSince(flight.workID, flight.started, cwd)
	if len(commits) == 0 {
		_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
			WorkID:         flight.workID,
			ExecutionState: core.WorkExecutionStateFailed,
			Message:        fmt.Sprintf("supervisor: job %s process exited cleanly but no committed work found", flight.jobID),
			CreatedBy:      "supervisor",
		})
		_, _ = svc.AddWorkNote(ctx, service.WorkNoteRequest{
			WorkID:    flight.workID,
			NoteType:  "finding",
			Body:      fmt.Sprintf("[fase:message from=\"supervisor\" type=\"info\"] Recovery: Worker (pid %d, exit code %d) exited cleanly but job %s was not completed. No new commits found. Marking as failed for retry.\n[/fase:message]", ev.PID, ev.ExitCode, flight.jobID),
			CreatedBy: "supervisor",
		})
		if loop != nil {
			loop.health.recordFailure(flight.adapter, flight.model)
		}
		return false
	}

	latestCommit := commits[0]
	_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
		WorkID:         flight.workID,
		ExecutionState: core.WorkExecutionStateDone,
		ApprovalState:  core.WorkApprovalStatePending,
		ForceDone:      true,
		Message:        fmt.Sprintf("supervisor: job %s premature exit with %d committed change(s). Latest: %s", flight.jobID, len(commits), latestCommit),
		CreatedBy:      "supervisor",
		Metadata: map[string]any{
			"head_commit_oid": latestCommit,
			"recovery_reason": "premature_exit",
		},
	})

	_, _ = svc.AddWorkNote(ctx, service.WorkNoteRequest{
		WorkID:    flight.workID,
		NoteType:  "finding",
		Body:      fmt.Sprintf("[fase:message from=\"supervisor\" type=\"info\"] Recovery: Worker (pid %d, exit code %d) exited cleanly but job %s was not completed. %d commit(s) found on branch. Preserving committed work. Transitioning to awaiting_attestation.\n[/fase:message]", ev.PID, ev.ExitCode, flight.jobID, len(commits)),
		CreatedBy: "supervisor",
	})

	if loop != nil {
		loop.health.recordSuccess(flight.adapter, flight.model, 0)
	}
	return true
}

func (r *RecoveryEngine) handleCrash(ctx context.Context, svc *service.Service, loop *eventDrivenLoop, flight *inFlightJob, ev ProcessEvent, cwd string) bool {
	commits := gitCommitsSince(flight.workID, flight.started, cwd)

	if len(commits) > 0 {
		latestCommit := commits[0]
		_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
			WorkID:         flight.workID,
			ExecutionState: core.WorkExecutionStateDone,
			ApprovalState:  core.WorkApprovalStatePending,
			ForceDone:      true,
			Message:        fmt.Sprintf("supervisor: job %s adapter crash (exit code %d) with %d committed change(s). Latest: %s", flight.jobID, ev.ExitCode, len(commits), latestCommit),
			CreatedBy:      "supervisor",
			Metadata: map[string]any{
				"head_commit_oid": latestCommit,
				"recovery_reason": "adapter_crash",
			},
		})

		_, _ = svc.AddWorkNote(ctx, service.WorkNoteRequest{
			WorkID:    flight.workID,
			NoteType:  "finding",
			Body:      fmt.Sprintf("[fase:message from=\"supervisor\" type=\"info\"] Recovery: Worker (pid %d, exit code %d) crashed but %d commit(s) found on branch. Preserving committed work. Transitioning to awaiting_attestation.\n[/fase:message]", ev.PID, ev.ExitCode, len(commits)),
			CreatedBy: "supervisor",
		})
		if loop != nil {
			loop.health.recordSuccess(flight.adapter, flight.model, 0)
		}
		return true
	}

	_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
		WorkID:         flight.workID,
		ExecutionState: core.WorkExecutionStateFailed,
		Message:        fmt.Sprintf("supervisor: job %s adapter crashed (exit code %d, pid %d). No committed work. Marking as failed for retry.", flight.jobID, ev.ExitCode, ev.PID),
		CreatedBy:      "supervisor",
	})

	_, _ = svc.AddWorkNote(ctx, service.WorkNoteRequest{
		WorkID:    flight.workID,
		NoteType:  "finding",
		Body:      fmt.Sprintf("[fase:message from=\"supervisor\" type=\"info\"] Recovery: Worker (pid %d, exit code %d) crashed with no committed work. Marking as failed for retry with a different adapter.\n[/fase:message]", ev.PID, ev.ExitCode),
		CreatedBy: "supervisor",
	})
	if loop != nil {
		loop.health.recordFailure(flight.adapter, flight.model)
	}
	return false
}

func (r *RecoveryEngine) recordStallRecovery(ctx context.Context, svc *service.Service, loop *eventDrivenLoop, workID string, flight *inFlightJob, cwd string) bool {
	commits := gitCommitsSince(workID, flight.started, cwd)

	if len(commits) > 0 {
		latestCommit := commits[0]
		_, _ = svc.UpdateWork(ctx, service.WorkUpdateRequest{
			WorkID:         workID,
			ExecutionState: core.WorkExecutionStateDone,
			ApprovalState:  core.WorkApprovalStatePending,
			ForceDone:      true,
			Message:        fmt.Sprintf("supervisor: job %s stalled but %d commit(s) found. Latest: %s", flight.jobID, len(commits), latestCommit),
			CreatedBy:      "supervisor",
			Metadata: map[string]any{
				"head_commit_oid": latestCommit,
				"recovery_reason": "stall_with_commits",
			},
		})

		_, _ = svc.AddWorkNote(ctx, service.WorkNoteRequest{
			WorkID:    workID,
			NoteType:  "finding",
			Body:      fmt.Sprintf("[fase:message from=\"supervisor\" type=\"info\"] Recovery: Job %s stalled (no output for 10m) but %d commit(s) found. Preserving committed work. Transitioning to awaiting_attestation.\n[/fase:message]", flight.jobID, len(commits)),
			CreatedBy: "supervisor",
		})
		if loop != nil {
			loop.health.recordSuccess(flight.adapter, flight.model, 0)
		}
		return true
	}

	_, _ = svc.AddWorkNote(ctx, service.WorkNoteRequest{
		WorkID:    workID,
		NoteType:  "finding",
		Body:      fmt.Sprintf("[fase:message from=\"supervisor\" type=\"info\"] Recovery: Job %s stalled (no output for 10m) and no committed work was found. Marking as failed for retry.\n[/fase:message]", flight.jobID),
		CreatedBy: "supervisor",
	})
	if loop != nil {
		loop.health.recordFailure(flight.adapter, flight.model)
	}
	return false
}

func gitCommitsSince(workID string, started time.Time, cwd string) []string {
	args := []string{"-C", cwd, "log", "--format=%H", "--max-count=10"}
	if strings.TrimSpace(workID) != "" {
		email := strings.TrimSpace(workID) + "@fase.local"
		args = append(args, "--author="+email, "--committer="+email)
	}
	if !started.IsZero() {
		args = append(args, "--since="+started.UTC().Format(time.RFC3339))
	}

	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	commits := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			commits = append(commits, line)
		}
	}
	return commits
}
