package cli

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/yusefmosiah/fase/internal/service"
)

// agenticSupervisor runs the supervisor as a regular adapter session (ADR-0041).
// The LLM has FASE MCP tools and handles all dispatch/attestation logic.
// The Go code just manages the session lifecycle.
type agenticSupervisor struct {
	svc     *service.Service
	cwd     string
	hub     *wsHub
	adapter string
	model   string

	mu     sync.Mutex
	paused bool
	hostCh chan string
}

func newAgenticSupervisor(svc *service.Service, cwd string, hub *wsHub, adapter, model string) *agenticSupervisor {
	if adapter == "" {
		adapter = "claude"
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	return &agenticSupervisor{
		svc:     svc,
		cwd:     cwd,
		hub:     hub,
		adapter: adapter,
		model:   model,
		hostCh:  make(chan string, 16),
	}
}

func (s *agenticSupervisor) pause()  { s.mu.Lock(); s.paused = true; s.mu.Unlock() }
func (s *agenticSupervisor) resume() { s.mu.Lock(); s.paused = false; s.mu.Unlock() }
func (s *agenticSupervisor) isPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

func (s *agenticSupervisor) send(msg string) {
	select {
	case s.hostCh <- msg:
	default:
	}
}

// run is the supervisor loop. Start session, wait for events, send next turn.
func (s *agenticSupervisor) run(ctx context.Context) {
	ch := s.svc.Events.Subscribe()
	defer s.svc.Events.Unsubscribe(ch)

	// First turn: cold-start with full supervisor hydration.
	hydration, err := s.svc.ProjectHydrate(ctx, service.ProjectHydrateRequest{Mode: "supervisor"})
	if err != nil {
		s.log("error", fmt.Sprintf("hydrate failed: %v", err))
		return
	}
	prompt := service.RenderProjectHydrateMarkdown(hydration)

	s.log("starting", fmt.Sprintf("adapter=%s model=%s", s.adapter, s.model))
	result, err := s.svc.Run(ctx, service.RunRequest{
		Adapter: s.adapter,
		CWD:     s.cwd,
		Prompt:  prompt,
		Model:   s.model,
		Label:   "supervisor",
	})
	if err != nil {
		s.log("error", fmt.Sprintf("failed to start: %v", err))
		return
	}
	sessionID := result.Session.SessionID
	s.log("started", fmt.Sprintf("session=%s job=%s", sessionID, result.Job.JobID))

	s.waitForJob(ctx, ch, result.Job.JobID)

	for {
		// Wait for something to happen: event or host message.
		msg := s.waitForSignal(ctx, ch)
		if ctx.Err() != nil {
			return
		}
		if s.isPaused() {
			continue
		}

		s.log("turn", fmt.Sprintf("session=%s", sessionID))

		sendResult, err := s.svc.Send(ctx, service.SendRequest{
			SessionID: sessionID,
			Adapter:   s.adapter,
			Prompt:    msg,
			Model:     s.model,
		})
		if err != nil {
			s.log("error", fmt.Sprintf("send failed: %v — restarting", err))
			s.restartAfterDelay(ctx, ch)
			return
		}

		s.waitForJob(ctx, ch, sendResult.Job.JobID)
	}
}

// waitForSignal blocks until an event or host message arrives.
// Returns a simple prompt — the LLM uses MCP tools to figure out what to do.
func (s *agenticSupervisor) waitForSignal(ctx context.Context, ch chan service.WorkEvent) string {
	for {
		select {
		case <-ctx.Done():
			return ""
		case msg := <-s.hostCh:
			return fmt.Sprintf("Message from host: %s", msg)
		case ev := <-ch:
			switch ev.Kind {
			case service.WorkEventCreated, service.WorkEventUpdated,
				service.WorkEventAttested, service.WorkEventReleased:
				// Debounce.
				time.Sleep(2 * time.Second)
				// Drain any burst.
				for {
					select {
					case <-ch:
					default:
						goto done
					}
				}
			done:
				return "Queue state changed. Check ready_work and act."
			}
		}
	}
}

// waitForJob polls until the supervisor's own turn job completes.
func (s *agenticSupervisor) waitForJob(ctx context.Context, ch chan service.WorkEvent, jobID string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			// Discard — events during the supervisor's own turn are
			// about the supervisor's job, not worker state changes.
			// The LLM will call ready_work on its next turn anyway.
		case <-ticker.C:
			status, err := s.svc.Status(ctx, jobID)
			if err != nil {
				continue
			}
			if status.Job.State.Terminal() {
				return
			}
		}
	}
}

func (s *agenticSupervisor) restartAfterDelay(ctx context.Context, ch chan service.WorkEvent) {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
		go s.run(ctx)
	}
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func (s *agenticSupervisor) log(event, message string) {
	s.hub.broadcast("supervisor_"+event, map[string]string{"message": message})
	fmt.Printf("supervisor: %s %s\n", event, message)
}
