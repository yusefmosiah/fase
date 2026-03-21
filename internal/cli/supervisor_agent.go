package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/yusefmosiah/fase/internal/service"
)

// agenticSupervisor implements the agentic supervisor (ADR-0041).
// The supervisor is a long-running adapter session. Each turn, the LLM uses
// FASE MCP tools to inspect the queue, dispatch workers, and attest completed
// work. Between turns, the loop waits for EventBus events and sends event
// summaries as the next turn's prompt.
type agenticSupervisor struct {
	svc     *service.Service
	cwd     string
	hub     *wsHub
	adapter string
	model   string

	mu      sync.Mutex
	paused  bool
	hostCh  chan string // host messages injected via API
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

// send injects a host message into the supervisor session. The message will
// be included in the next turn's prompt, triggering the supervisor to act.
func (s *agenticSupervisor) send(msg string) {
	select {
	case s.hostCh <- msg:
	default:
		// Drop if channel is full — host can retry.
	}
}

// run is the main supervisor loop.
func (s *agenticSupervisor) run(ctx context.Context) {
	ch := s.svc.Events.Subscribe()
	defer s.svc.Events.Unsubscribe(ch)

	// First turn: cold-start with full supervisor hydration.
	hydration, err := s.svc.ProjectHydrate(ctx, service.ProjectHydrateRequest{Mode: "supervisor"})
	if err != nil {
		s.log("error", fmt.Sprintf("failed to hydrate: %v", err))
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
		s.log("error", fmt.Sprintf("failed to start supervisor session: %v", err))
		return
	}
	sessionID := result.Session.SessionID
	s.log("started", fmt.Sprintf("session=%s job=%s", sessionID, result.Job.JobID))

	// Wait for the first turn's job to complete, then enter the event loop.
	// pendingInput carries events collected during waitForJob so they aren't lost.
	pendingInput := s.waitForJob(ctx, ch, result.Job.JobID)

	for {
		// Collect events and host messages until something interesting happens.
		// If pendingInput has events from waitForJob, use those immediately
		// instead of blocking for new ones.
		var input turnInput
		if len(pendingInput.events) > 0 || len(pendingInput.hostMessages) > 0 {
			input = pendingInput
			pendingInput = turnInput{}
		} else {
			input = s.collectInput(ctx, ch)
		}
		if ctx.Err() != nil {
			return
		}
		if s.isPaused() {
			pendingInput = turnInput{} // discard while paused
			continue
		}

		// Send the next turn with events + host messages.
		summary := formatTurnPrompt(input)
		s.log("turn", fmt.Sprintf("session=%s events=%d host_msgs=%d", sessionID, len(input.events), len(input.hostMessages)))

		sendResult, err := s.svc.Send(ctx, service.SendRequest{
			SessionID: sessionID,
			Adapter:   s.adapter,
			Prompt:    summary,
			Model:     s.model,
		})
		if err != nil {
			s.log("error", fmt.Sprintf("send failed: %v — restarting session", err))
			// Session may have died. Restart with fresh hydration.
			s.restartAfterDelay(ctx, ch)
			return
		}

		pendingInput = s.waitForJob(ctx, ch, sendResult.Job.JobID)
	}
}

// waitForJob collects EventBus events until the given job reaches a terminal
// state. Returns any relevant events collected during the wait so they can
// be passed to the next supervisor turn (avoiding event loss).
func (s *agenticSupervisor) waitForJob(ctx context.Context, ch chan service.WorkEvent, jobID string) turnInput {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var collected turnInput
	for {
		select {
		case <-ctx.Done():
			return collected
		case ev := <-ch:
			if isRelevantEvent(ev) {
				collected.events = append(collected.events, ev)
			}
		case msg := <-s.hostCh:
			collected.hostMessages = append(collected.hostMessages, msg)
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

// turnInput holds events and host messages collected between turns.
type turnInput struct {
	events       []service.WorkEvent
	hostMessages []string
}

// collectInput waits for at least one relevant event or host message, then
// debounces and collects any additional input within 2 seconds.
func (s *agenticSupervisor) collectInput(ctx context.Context, ch chan service.WorkEvent) turnInput {
	var input turnInput

	// Wait for the first signal.
	for {
		select {
		case <-ctx.Done():
			return input
		case ev := <-ch:
			if isRelevantEvent(ev) {
				input.events = append(input.events, ev)
				goto debounce
			}
		case msg := <-s.hostCh:
			input.hostMessages = append(input.hostMessages, msg)
			goto debounce
		}
	}

debounce:
	// Wait 2s, collecting any additional input.
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return input
		case ev := <-ch:
			if isRelevantEvent(ev) {
				input.events = append(input.events, ev)
			}
		case msg := <-s.hostCh:
			input.hostMessages = append(input.hostMessages, msg)
		case <-timer.C:
			return input
		}
	}
}

func (s *agenticSupervisor) restartAfterDelay(ctx context.Context, ch chan service.WorkEvent) {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
		// Restart the supervisor loop.
		go s.run(ctx)
	}
	// Drain events during the delay.
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

func isRelevantEvent(ev service.WorkEvent) bool {
	switch ev.Kind {
	case service.WorkEventCreated, service.WorkEventUpdated,
		service.WorkEventAttested, service.WorkEventReleased:
		return true
	}
	return false
}

func formatTurnPrompt(input turnInput) string {
	var b strings.Builder

	if len(input.hostMessages) > 0 {
		b.WriteString("Message from host:\n\n")
		for _, msg := range input.hostMessages {
			b.WriteString(msg)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(input.events) > 0 {
		b.WriteString("Events since your last turn:\n\n")
		for _, ev := range input.events {
			title := ev.Title
			if title == "" {
				title = ev.WorkID
			}
			switch ev.Kind {
			case service.WorkEventCreated:
				fmt.Fprintf(&b, "- NEW: %s (%s) created\n", title, ev.WorkID)
			case service.WorkEventUpdated:
				if ev.PrevState != "" && ev.PrevState != ev.State {
					fmt.Fprintf(&b, "- %s (%s): %s → %s\n", title, ev.WorkID, ev.PrevState, ev.State)
				} else {
					fmt.Fprintf(&b, "- %s (%s) updated (state: %s)\n", title, ev.WorkID, ev.State)
				}
			case service.WorkEventAttested:
				fmt.Fprintf(&b, "- ATTESTED: %s (%s)\n", title, ev.WorkID)
			case service.WorkEventReleased:
				fmt.Fprintf(&b, "- RELEASED: %s (%s) — available for dispatch\n", title, ev.WorkID)
			default:
				fmt.Fprintf(&b, "- %s: %s (%s)\n", ev.Kind, title, ev.WorkID)
			}
		}
	}

	if len(input.events) == 0 && len(input.hostMessages) == 0 {
		b.WriteString("No new events. Check the queue for any work that needs attention.")
	}

	b.WriteString("\nCheck the queue and take appropriate action (dispatch, attest, or wait).")
	return b.String()
}
