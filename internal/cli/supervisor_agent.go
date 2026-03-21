package cli

import (
	"context"
	"fmt"
	"strings"
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

	pending := s.waitForJob(ctx, ch, result.Job.JobID)

	for {
		// If events arrived during waitForJob, use them immediately.
		// Otherwise block for the next signal.
		var msg string
		if len(pending) > 0 {
			msg = formatEvents(pending)
			pending = nil
		} else {
			msg = s.waitForSignal(ctx, ch)
		}
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

		pending = s.waitForJob(ctx, ch, sendResult.Job.JobID)
	}
}

// waitForSignal blocks until an event or host message arrives.
// Collects events during debounce window and formats them into the prompt.
func (s *agenticSupervisor) waitForSignal(ctx context.Context, ch chan service.WorkEvent) string {
	var events []service.WorkEvent
	for {
		select {
		case <-ctx.Done():
			return ""
		case msg := <-s.hostCh:
			return fmt.Sprintf("Message from host: %s", msg)
		case ev := <-ch:
			if !ev.RequiresSupervisorAttention() {
				continue
			}
			events = append(events, ev)
			// Debounce: collect burst events within 2s.
			timer := time.NewTimer(2 * time.Second)
		drain:
			for {
				select {
				case e := <-ch:
					if e.RequiresSupervisorAttention() {
						events = append(events, e)
					}
				case msg := <-s.hostCh:
					timer.Stop()
					return fmt.Sprintf("Message from host: %s\n\n%s", msg, formatEvents(events))
				case <-timer.C:
					break drain
				}
			}
			return formatEvents(events)
		}
	}
}

func formatEvents(events []service.WorkEvent) string {
	var b strings.Builder
	for _, ev := range events {
		title := ev.Title
		if title == "" {
			title = ev.WorkID
		}
		switch ev.Kind {
		case service.WorkEventCreated:
			fmt.Fprintf(&b, "[created] %s (%s)\n", title, ev.WorkID)
		case service.WorkEventUpdated:
			fmt.Fprintf(&b, "[%s→%s] %s (%s)", ev.PrevState, ev.State, title, ev.WorkID)
			if msg := ev.Metadata["message"]; msg != "" {
				if len(msg) > 200 {
					fmt.Fprintf(&b, ": %s...", msg[:200])
				} else {
					fmt.Fprintf(&b, ": %s", msg)
				}
			}
			if ev.JobID != "" {
				fmt.Fprintf(&b, " [job %s]", ev.JobID)
			}
			b.WriteString("\n")
		case service.WorkEventAttested:
			result := ev.Metadata["result"]
			summary := ev.Metadata["summary"]
			fmt.Fprintf(&b, "[attested:%s] %s (%s)", result, title, ev.WorkID)
			if summary != "" {
				if len(summary) > 200 {
					fmt.Fprintf(&b, ": %s...", summary[:200])
				} else {
					fmt.Fprintf(&b, ": %s", summary)
				}
			}
			b.WriteString("\n")
		case service.WorkEventReleased:
			fmt.Fprintf(&b, "[released] %s (%s)\n", title, ev.WorkID)
		}
	}
	return b.String()
}

// waitForJob polls until the supervisor's own turn job completes.
// Collects events that arrive during the wait so they aren't lost.
func (s *agenticSupervisor) waitForJob(ctx context.Context, ch chan service.WorkEvent, jobID string) []service.WorkEvent {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var collected []service.WorkEvent
	for {
		select {
		case <-ctx.Done():
			return collected
		case ev := <-ch:
			if ev.RequiresSupervisorAttention() {
				collected = append(collected, ev)
			}
		case <-ticker.C:
			status, err := s.svc.Status(ctx, jobID)
			if err != nil {
				continue
			}
			if status.Job.State.Terminal() {
				return collected
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
