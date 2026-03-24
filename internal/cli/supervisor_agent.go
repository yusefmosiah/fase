package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yusefmosiah/fase/internal/core"
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

	outcome := s.waitForJob(ctx, ch, result.Job.JobID)

	// Backoff state: tracks consecutive unproductive turns.
	consecutiveEmpty := 0
	productiveTurns := 0
	const maxBackoff = 5 * time.Minute
	const maxProductiveTurns = 10 // restart with fresh hydration after this many

	for {
		// If the last turn was unproductive (error, rate-limited, or very fast
		// with no tool calls), back off exponentially.
		if outcome.unproductive {
			consecutiveEmpty++
			// After 5 consecutive failures, restart with fresh session.
			if consecutiveEmpty >= 5 {
				s.log("error", fmt.Sprintf("5 consecutive failures (%s) — restarting with fresh session", outcome.reason))
				s.notifyHost(fmt.Sprintf("Supervisor restarting after 5 consecutive failures: %s", outcome.reason), "escalation")
				s.restartAfterDelay(ctx, ch)
				return
			}
			backoff := time.Duration(1<<min(consecutiveEmpty, 8)) * time.Second
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			s.log("backoff", fmt.Sprintf("unproductive turn (%s), waiting %s (streak: %d)",
				outcome.reason, backoff, consecutiveEmpty))
			select {
			case <-ctx.Done():
				return
			case msg := <-s.hostCh:
				// Host message breaks backoff immediately.
				consecutiveEmpty = 0
				s.log("turn", fmt.Sprintf("session=%s (host broke backoff)", sessionID))
				sendResult, err := s.svc.Send(ctx, service.SendRequest{
					SessionID: sessionID,
					Adapter:   s.adapter,
					Prompt:    fmt.Sprintf("Message from host: %s", msg),
					Model:     s.model,
				})
				if err != nil {
					s.log("error", fmt.Sprintf("send failed: %v — restarting", err))
					s.restartAfterDelay(ctx, ch)
					return
				}
				outcome = s.waitForJob(ctx, ch, sendResult.Job.JobID)
				continue
			case <-time.After(backoff):
				// After backoff, wait for a real signal (don't immediately retry).
			}
		} else {
			consecutiveEmpty = 0
			productiveTurns++
			// Proactive context management: restart with fresh hydration
			// every N productive turns to prevent context overflow.
			if productiveTurns >= maxProductiveTurns {
				s.log("context", fmt.Sprintf("rotating session after %d productive turns", productiveTurns))
				s.restartAfterDelay(ctx, ch)
				return
			}
		}

		// Collect pending events or wait for a signal.
		var msg string
		if len(outcome.events) > 0 {
			msg = formatEvents(outcome.events)
		} else {
			msg = s.waitForSignal(ctx, ch)
		}
		if ctx.Err() != nil {
			return
		}
		if s.isPaused() {
			outcome = jobOutcome{}
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

		outcome = s.waitForJob(ctx, ch, sendResult.Job.JobID)

		// Notify host after each productive turn.
		if !outcome.unproductive {
			if status, err := s.svc.Status(ctx, sendResult.Job.JobID); err == nil && status.Job.State == core.JobStateCompleted {
				s.notifyHost(fmt.Sprintf("Supervisor turn completed (job %s)", sendResult.Job.JobID), "status_update")
			}
		}
	}
}

// jobOutcome captures the result of waiting for a supervisor turn job.
type jobOutcome struct {
	events       []service.WorkEvent
	unproductive bool   // true if the turn was rate-limited, errored, or empty
	reason       string // human-readable reason for unproductive
}

// waitForSignal blocks until an event or host message arrives.
// syncWorkStateFromJob and refreshAttestationParentState now publish WorkEvents
// with correct Actor/Cause fields, so worker completion reliably wakes the supervisor.
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
		case service.WorkEventCheckRecorded:
			result := ev.Metadata["result"]
			checkID := ev.Metadata["check_id"]
			fmt.Fprintf(&b, "[check:%s] %s (%s) check_id=%s — use check_record_show to read the report\n",
				result, title, ev.WorkID, checkID)
		}
	}
	return b.String()
}

// waitForJob polls until the supervisor's own turn job completes.
// Returns a jobOutcome with collected events and whether the turn was unproductive.
func (s *agenticSupervisor) waitForJob(ctx context.Context, ch chan service.WorkEvent, jobID string) jobOutcome {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	startTime := time.Now()

	var collected []service.WorkEvent
	for {
		select {
		case <-ctx.Done():
			return jobOutcome{events: collected}
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
				return s.classifyOutcome(status, collected, startTime)
			}
		}
	}
}

// classifyOutcome determines if the completed turn was productive.
func (s *agenticSupervisor) classifyOutcome(status *service.StatusResult, events []service.WorkEvent, _ time.Time) jobOutcome {
	out := jobOutcome{events: events}

	// Only failed jobs are unproductive. Fast completion is fine —
	// a supervisor dispatching work in 5s is productive.
	if status.Job.State == core.JobStateFailed {
		out.unproductive = true
		out.reason = "job failed"
		return out
	}

	return out
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

// notifyHost sends a channel notification to the host via serve's API.
func (s *agenticSupervisor) notifyHost(message, msgType string) {
	body, _ := json.Marshal(map[string]any{
		"content": message,
		"meta":    map[string]string{"source": "supervisor", "type": msgType},
	})
	info, err := loadServeInfo()
	if err != nil {
		return
	}
	url := fmt.Sprintf("http://localhost:%d/api/channel/send", info.Port)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(body)))
	if err == nil {
		resp.Body.Close()
	}
}
