package native

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yusefmosiah/fase/internal/adapterapi"
	"github.com/yusefmosiah/fase/internal/core"
	"github.com/yusefmosiah/fase/internal/service"
)

// LiveAdapter is the native Go LiveAgentAdapter — the reference implementation
// for the fase live agent protocol.
//
// Architecture: conductor/app-agent/worker pattern using Go concurrency
// primitives (goroutines, channels). The conductor session routes turns to
// co-agent workers running in goroutines. External adapters (Claude, Codex,
// Pi, OpenCode) are transparent co-agents dispatched by the conductor based
// on the "adapter/model" model string.
//
// Transport: in-process. No subprocess. No binary. No external protocol.
// EventBus: subscribed directly to svc.Events for work graph notifications.
// Tool bridge: direct service calls.
// Model config: parsed from "adapter/model" string (no BAML).
type LiveAdapter struct {
	svc      *service.Service
	coAgents map[string]adapterapi.LiveAgentAdapter
}

// NewLiveAdapter creates a LiveAdapter with access to the fase service and
// a set of named external co-agent adapters for turn routing.
//
// coAgents maps adapter name → LiveAgentAdapter (e.g. "claude" → claude.NewLiveAdapter(...)).
// Pass nil svc to run without EventBus integration (useful in tests).
func NewLiveAdapter(svc *service.Service, coAgents map[string]adapterapi.LiveAgentAdapter) *LiveAdapter {
	if coAgents == nil {
		coAgents = make(map[string]adapterapi.LiveAgentAdapter)
	}
	return &LiveAdapter{svc: svc, coAgents: coAgents}
}

// Name returns the adapter identifier.
func (a *LiveAdapter) Name() string { return "native" }

// StartSession creates a new conductor session.
func (a *LiveAdapter) StartSession(ctx context.Context, req adapterapi.StartSessionRequest) (adapterapi.LiveSession, error) {
	id := core.GenerateID("nsess")
	return newConductorSession(ctx, id, req, a.svc, a.coAgents, false), nil
}

// ResumeSession reconnects to an existing conductor session by ID.
// Since the conductor is in-process, resume creates a new conductor goroutine
// with the provided session ID for continuity.
func (a *LiveAdapter) ResumeSession(ctx context.Context, nativeSessionID string, req adapterapi.StartSessionRequest) (adapterapi.LiveSession, error) {
	return newConductorSession(ctx, nativeSessionID, req, a.svc, a.coAgents, true), nil
}

// -----------------------------------------------------------------------
// Conductor Session
// -----------------------------------------------------------------------

// conductorSession implements adapterapi.LiveSession using the Choir
// conductor/app-agent/worker pattern.
//
// The conductor goroutine manages:
//   - routing turns to co-agent workers (external adapter sessions)
//   - forwarding work graph events (EventBus) to active workers as steers
//   - relaying external steer events from the SteerCh to active workers
//   - aggregating all worker events into the session event channel
type conductorSession struct {
	id      string
	cwd     string
	model   string
	profile string

	steerCh      <-chan adapterapi.SteerEvent
	eventCh      chan adapterapi.Event
	eventChClose sync.Once
	workEventCh  chan service.WorkEvent
	eventDrops   atomic.Int64

	workersMu sync.Mutex
	workers   map[string]*coAgentWorker

	turnSeq atomic.Int64

	activeMu   sync.Mutex
	activeTurn string

	svc      *service.Service
	coAgents map[string]adapterapi.LiveAgentAdapter

	ctx    context.Context
	cancel context.CancelFunc
}

func newConductorSession(
	ctx context.Context,
	id string,
	req adapterapi.StartSessionRequest,
	svc *service.Service,
	coAgents map[string]adapterapi.LiveAgentAdapter,
	resumed bool,
) *conductorSession {
	sctx, cancel := context.WithCancel(ctx)
	s := &conductorSession{
		id:       id,
		cwd:      req.CWD,
		model:    req.Model,
		profile:  req.Profile,
		steerCh:  req.SteerCh,
		eventCh:  make(chan adapterapi.Event, 256),
		workers:  make(map[string]*coAgentWorker),
		svc:      svc,
		coAgents: coAgents,
		ctx:      sctx,
		cancel:   cancel,
	}

	if svc != nil {
		s.workEventCh = svc.Events.Subscribe()
	}

	kind := adapterapi.EventKindSessionStarted
	if resumed {
		kind = adapterapi.EventKindSessionResumed
	}
	s.eventCh <- adapterapi.Event{Kind: kind, SessionID: id}

	go s.conductorLoop()
	return s
}

// SessionID returns the conductor session ID.
func (s *conductorSession) SessionID() string { return s.id }

// ActiveTurnID returns the current active conductor turn ID.
func (s *conductorSession) ActiveTurnID() string {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	return s.activeTurn
}

// StartTurn routes a turn to the appropriate co-agent worker.
// The conductor picks or creates a worker based on the model configuration,
// begins a turn in the worker, and proxies its events to the conductor event channel.
func (s *conductorSession) StartTurn(ctx context.Context, input []adapterapi.Input) (string, error) {
	turnID := fmt.Sprintf("cturn-%d", s.turnSeq.Add(1))

	s.activeMu.Lock()
	s.activeTurn = turnID
	s.activeMu.Unlock()

	s.emit(adapterapi.Event{
		Kind:      adapterapi.EventKindTurnStarted,
		SessionID: s.id,
		TurnID:    turnID,
	})

	worker, err := s.getOrCreateWorker(ctx)
	if err != nil {
		s.activeMu.Lock()
		if s.activeTurn == turnID {
			s.activeTurn = ""
		}
		s.activeMu.Unlock()
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindTurnFailed,
			SessionID: s.id,
			TurnID:    turnID,
			Text:      fmt.Sprintf("conductor: get worker: %v", err),
		})
		return "", fmt.Errorf("native conductor: %w", err)
	}

	go s.runTurn(turnID, worker, input)
	return turnID, nil
}

// Steer injects input into the active turn via the current worker.
func (s *conductorSession) Steer(ctx context.Context, expectedTurnID string, input []adapterapi.Input) error {
	s.activeMu.Lock()
	activeTurn := s.activeTurn
	s.activeMu.Unlock()

	if activeTurn == "" {
		return fmt.Errorf("native conductor: no active turn")
	}
	if expectedTurnID != "" && expectedTurnID != activeTurn {
		return fmt.Errorf("native conductor: turn mismatch expected=%s active=%s", expectedTurnID, activeTurn)
	}

	worker := s.activeWorker()
	if worker == nil {
		return fmt.Errorf("native conductor: no active worker")
	}

	workerTurnID := worker.activeTurnID()
	return worker.session.Steer(ctx, workerTurnID, input)
}

// Interrupt cancels the active turn via the current worker.
func (s *conductorSession) Interrupt(ctx context.Context) error {
	worker := s.activeWorker()
	if worker == nil {
		return fmt.Errorf("native conductor: no active worker to interrupt")
	}
	return worker.session.Interrupt(ctx)
}

// Events returns the conductor event channel.
func (s *conductorSession) Events() <-chan adapterapi.Event { return s.eventCh }

// Close shuts down the conductor session and all co-agent workers.
func (s *conductorSession) Close() error {
	s.cancel()

	s.workersMu.Lock()
	workers := make([]*coAgentWorker, 0, len(s.workers))
	for _, w := range s.workers {
		workers = append(workers, w)
	}
	s.workersMu.Unlock()

	for _, w := range workers {
		if w.session != nil {
			_ = w.session.Close()
		}
	}

	if s.workEventCh != nil && s.svc != nil {
		s.svc.Events.Unsubscribe(s.workEventCh)
		s.workEventCh = nil
	}

	return nil
}

// -----------------------------------------------------------------------
// Conductor goroutine
// -----------------------------------------------------------------------

// conductorLoop is the main conductor goroutine. It handles EventBus work
// graph events and external steer events, forwarding both to the active worker.
func (s *conductorSession) conductorLoop() {
	defer s.eventChClose.Do(func() { close(s.eventCh) })

	// Local copies so we can nil them when closed without data races.
	workCh := s.workEventCh
	steerCh := s.steerCh

	for {
		select {
		case <-s.ctx.Done():
			s.emit(adapterapi.Event{
				Kind:      adapterapi.EventKindSessionClosed,
				SessionID: s.id,
			})
			return

		case ev, ok := <-workCh:
			if !ok {
				workCh = nil
				continue
			}
			s.forwardWorkEvent(ev)

		case steer, ok := <-steerCh:
			if !ok {
				steerCh = nil
				continue
			}
			s.forwardSteer(steer)
		}
	}
}

// forwardWorkEvent translates a service WorkEvent into a co-agent steer message.
// Only steers if a turn is active and the event is relevant to this session.
// Filters out noise: lease renewals and events for unrelated work items.
func (s *conductorSession) forwardWorkEvent(ev service.WorkEvent) {
	if ev.Kind == service.WorkEventLeaseRenew {
		return
	}

	s.activeMu.Lock()
	activeTurn := s.activeTurn
	s.activeMu.Unlock()
	if activeTurn == "" {
		return
	}

	worker := s.activeWorker()
	if worker == nil {
		return
	}
	workerTurnID := worker.activeTurnID()
	if workerTurnID == "" {
		return
	}

	msg := formatWorkEvent(ev)
	_ = worker.session.Steer(s.ctx, workerTurnID, []adapterapi.Input{adapterapi.TextInput(msg)})
}

// forwardSteer relays an external SteerEvent to the active worker.
func (s *conductorSession) forwardSteer(steer adapterapi.SteerEvent) {
	worker := s.activeWorker()
	if worker == nil {
		return
	}
	workerTurnID := worker.activeTurnID()
	if workerTurnID == "" {
		return
	}

	msg := fmt.Sprintf("[fase:message from=\"supervisor\" type=\"info\"]\n%s\n[/fase:message]", steer.Message)
	_ = worker.session.Steer(s.ctx, workerTurnID, []adapterapi.Input{adapterapi.TextInput(msg)})
}

// -----------------------------------------------------------------------
// Turn goroutine
// -----------------------------------------------------------------------

// runTurn executes a conductor turn by delegating to a co-agent worker.
// It proxies events from the worker event channel to the conductor event channel.
// Runs in its own goroutine.
func (s *conductorSession) runTurn(conductorTurnID string, worker *coAgentWorker, input []adapterapi.Input) {
	subTurnID, err := worker.session.StartTurn(s.ctx, input)
	if err != nil {
		s.activeMu.Lock()
		if s.activeTurn == conductorTurnID {
			s.activeTurn = ""
		}
		s.activeMu.Unlock()
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindTurnFailed,
			SessionID: s.id,
			TurnID:    conductorTurnID,
			Text:      err.Error(),
		})
		return
	}

	worker.setActiveTurnID(subTurnID)

	// Proxy events from the worker until the turn ends or the context is cancelled.
	for {
		select {
		case <-s.ctx.Done():
			return

		case ev, ok := <-worker.session.Events():
			if !ok {
				// Worker session closed: report as turn failure.
				s.activeMu.Lock()
				if s.activeTurn == conductorTurnID {
					s.activeTurn = ""
				}
				s.activeMu.Unlock()
				s.emit(adapterapi.Event{
					Kind:      adapterapi.EventKindTurnFailed,
					SessionID: s.id,
					TurnID:    conductorTurnID,
					Text:      "worker session closed unexpectedly",
				})
				return
			}

			if s.handleWorkerEvent(ev, conductorTurnID, worker) {
				return
			}
		}
	}
}

// handleWorkerEvent translates a worker event into a conductor event.
// Returns true when the turn has ended and runTurn should return.
func (s *conductorSession) handleWorkerEvent(ev adapterapi.Event, conductorTurnID string, worker *coAgentWorker) bool {
	switch ev.Kind {
	case adapterapi.EventKindOutputDelta:
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindOutputDelta,
			SessionID: s.id,
			TurnID:    conductorTurnID,
			Text:      ev.Text,
		})

	case adapterapi.EventKindTurnStarted:
		// Worker started its own turn; record the sub-turn ID.
		worker.setActiveTurnID(ev.TurnID)
		// The conductor's turn.started was already emitted in StartTurn.

	case adapterapi.EventKindTurnCompleted:
		worker.setActiveTurnID("")
		s.activeMu.Lock()
		if s.activeTurn == conductorTurnID {
			s.activeTurn = ""
		}
		s.activeMu.Unlock()
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindTurnCompleted,
			SessionID: s.id,
			TurnID:    conductorTurnID,
		})
		return true

	case adapterapi.EventKindTurnFailed:
		worker.setActiveTurnID("")
		s.activeMu.Lock()
		if s.activeTurn == conductorTurnID {
			s.activeTurn = ""
		}
		s.activeMu.Unlock()
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindTurnFailed,
			SessionID: s.id,
			TurnID:    conductorTurnID,
			Text:      ev.Text,
		})
		return true

	case adapterapi.EventKindTurnInterrupted:
		worker.setActiveTurnID("")
		s.activeMu.Lock()
		if s.activeTurn == conductorTurnID {
			s.activeTurn = ""
		}
		s.activeMu.Unlock()
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindTurnInterrupted,
			SessionID: s.id,
			TurnID:    conductorTurnID,
		})
		return true

	case adapterapi.EventKindError:
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindError,
			SessionID: s.id,
			TurnID:    conductorTurnID,
			Text:      ev.Text,
		})

	case adapterapi.EventKindSessionClosed:
		// Worker session ended mid-turn.
		s.activeMu.Lock()
		if s.activeTurn == conductorTurnID {
			s.activeTurn = ""
		}
		s.activeMu.Unlock()
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindTurnFailed,
			SessionID: s.id,
			TurnID:    conductorTurnID,
			Text:      "worker session closed mid-turn",
		})
		return true
	}
	return false
}

// -----------------------------------------------------------------------
// Worker management
// -----------------------------------------------------------------------

// getOrCreateWorker returns an existing worker or creates a new one.
// Currently the conductor uses a single worker per session (reused across turns).
// This can be extended to a pool for concurrent multi-agent dispatch.
func (s *conductorSession) getOrCreateWorker(ctx context.Context) (*coAgentWorker, error) {
	s.workersMu.Lock()
	for _, w := range s.workers {
		s.workersMu.Unlock()
		return w, nil
	}
	s.workersMu.Unlock()
	return s.createWorker(ctx)
}

// createWorker spawns a new co-agent worker based on the configured model.
// An empty model creates a native echo worker (useful for testing and no-op routing).
func (s *conductorSession) createWorker(ctx context.Context) (*coAgentWorker, error) {
	adapterName, modelRef := parseModel(s.model)

	if adapterName == "" {
		// No external adapter: use the native echo worker.
		w := newEchoWorker()
		s.workersMu.Lock()
		s.workers[w.id] = w
		s.workersMu.Unlock()
		return w, nil
	}

	liveAdapter, ok := s.coAgents[adapterName]
	if !ok {
		return nil, fmt.Errorf("co-agent adapter %q not found in registry", adapterName)
	}

	subReq := adapterapi.StartSessionRequest{
		CWD:     s.cwd,
		Model:   modelRef,
		Profile: s.profile,
	}

	subSess, err := liveAdapter.StartSession(ctx, subReq)
	if err != nil {
		return nil, fmt.Errorf("start co-agent session (%s): %w", adapterName, err)
	}

	// Drain the session.started event so runTurn sees only turn events.
	select {
	case <-subSess.Events():
	case <-ctx.Done():
		_ = subSess.Close()
		return nil, ctx.Err()
	}

	workerID := core.GenerateID("wkr")
	w := &coAgentWorker{
		id:      workerID,
		adapter: adapterName,
		session: subSess,
	}

	s.workersMu.Lock()
	s.workers[w.id] = w
	s.workersMu.Unlock()

	return w, nil
}

// activeWorker returns the first available worker, or nil.
func (s *conductorSession) activeWorker() *coAgentWorker {
	s.workersMu.Lock()
	defer s.workersMu.Unlock()
	for _, w := range s.workers {
		return w
	}
	return nil
}

// emit sends an event to the conductor event channel without blocking.
// Events are dropped if the context is cancelled or the buffer is full.
func (s *conductorSession) emit(ev adapterapi.Event) {
	select {
	case <-s.ctx.Done():
		return
	default:
	}
	select {
	case s.eventCh <- ev:
	default:
		s.eventDrops.Add(1)
	}
}

func (s *conductorSession) EventDrops() int64 {
	return s.eventDrops.Load()
}

// -----------------------------------------------------------------------
// Co-Agent Worker
// -----------------------------------------------------------------------

// coAgentWorker wraps an external LiveSession as a conductor worker.
// It is safe for concurrent use.
type coAgentWorker struct {
	id      string
	adapter string
	session adapterapi.LiveSession

	activeTurnMu  sync.Mutex
	currentTurnID string
}

func (w *coAgentWorker) activeTurnID() string {
	w.activeTurnMu.Lock()
	defer w.activeTurnMu.Unlock()
	return w.currentTurnID
}

func (w *coAgentWorker) setActiveTurnID(id string) {
	w.activeTurnMu.Lock()
	w.currentTurnID = id
	w.activeTurnMu.Unlock()
}

// newEchoWorker creates a native worker backed by an echoSession.
// The echo worker immediately completes turns by reflecting input as output.
// It serves as the no-op baseline and for unit tests.
func newEchoWorker() *coAgentWorker {
	sess := newEchoSession()
	// Drain the session.started event that echoSession pre-emits.
	<-sess.eventCh
	return &coAgentWorker{
		id:      core.GenerateID("wkr"),
		adapter: "native",
		session: sess,
	}
}

// -----------------------------------------------------------------------
// Echo Session (native worker backend)
// -----------------------------------------------------------------------

// echoSession is a minimal LiveSession used as the native worker's backing session.
// It reflects input text as output deltas and completes turns immediately.
// No external process, no network, no model calls.
type echoSession struct {
	sessionID string
	eventCh   chan adapterapi.Event

	turnSeq atomic.Int64

	activeMu   sync.Mutex
	activeTurn string

	closeOnce sync.Once
}

func newEchoSession() *echoSession {
	id := core.GenerateID("echo")
	e := &echoSession{
		sessionID: id,
		eventCh:   make(chan adapterapi.Event, 64),
	}
	e.eventCh <- adapterapi.Event{
		Kind:      adapterapi.EventKindSessionStarted,
		SessionID: id,
	}
	return e
}

func (e *echoSession) SessionID() string { return e.sessionID }

func (e *echoSession) ActiveTurnID() string {
	e.activeMu.Lock()
	defer e.activeMu.Unlock()
	return e.activeTurn
}

func (e *echoSession) StartTurn(_ context.Context, input []adapterapi.Input) (string, error) {
	turnID := fmt.Sprintf("eturn-%d", e.turnSeq.Add(1))

	e.activeMu.Lock()
	e.activeTurn = turnID
	e.activeMu.Unlock()

	go func() {
		e.eventCh <- adapterapi.Event{
			Kind:      adapterapi.EventKindTurnStarted,
			SessionID: e.sessionID,
			TurnID:    turnID,
		}
		for _, inp := range input {
			if inp.Text != "" {
				e.eventCh <- adapterapi.Event{
					Kind:      adapterapi.EventKindOutputDelta,
					SessionID: e.sessionID,
					TurnID:    turnID,
					Text:      inp.Text,
				}
			}
		}
		e.activeMu.Lock()
		if e.activeTurn == turnID {
			e.activeTurn = ""
		}
		e.activeMu.Unlock()
		e.eventCh <- adapterapi.Event{
			Kind:      adapterapi.EventKindTurnCompleted,
			SessionID: e.sessionID,
			TurnID:    turnID,
		}
	}()

	return turnID, nil
}

func (e *echoSession) Steer(_ context.Context, _ string, _ []adapterapi.Input) error {
	return nil
}

func (e *echoSession) Interrupt(_ context.Context) error {
	e.activeMu.Lock()
	turnID := e.activeTurn
	e.activeTurn = ""
	e.activeMu.Unlock()

	if turnID != "" {
		select {
		case e.eventCh <- adapterapi.Event{
			Kind:      adapterapi.EventKindTurnInterrupted,
			SessionID: e.sessionID,
			TurnID:    turnID,
		}:
		default:
		}
	}
	return nil
}

func (e *echoSession) Events() <-chan adapterapi.Event { return e.eventCh }

func (e *echoSession) Close() error {
	e.closeOnce.Do(func() { close(e.eventCh) })
	return nil
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// parseModel splits "adapter/model" into adapter name and model reference.
// "claude/claude-opus-4-6"   → ("claude", "claude-opus-4-6")
// "opencode/anthropic/opus"  → ("opencode", "anthropic/opus")
// "codex"                    → ("codex", "")
// ""                         → ("", "")   → native echo worker
func parseModel(model string) (adapterName, modelRef string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", ""
	}
	adapterName, modelRef, _ = strings.Cut(model, "/")
	return adapterName, modelRef
}

// ParseModelForTest is exported for use in tests.
func ParseModelForTest(model string) (string, string) { return parseModel(model) }

// formatWorkEvent formats a service WorkEvent as a co-agent steer message.
func formatWorkEvent(ev service.WorkEvent) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Work %s:\n  work_id: %s", ev.Kind, ev.WorkID)
	if ev.Title != "" {
		fmt.Fprintf(&sb, "\n  title: %s", ev.Title)
	}
	fmt.Fprintf(&sb, "\n  state: %s", ev.State)
	if ev.PrevState != "" && ev.PrevState != ev.State {
		fmt.Fprintf(&sb, "\n  prev_state: %s", ev.PrevState)
	}
	return fmt.Sprintf("[fase:message from=\"work-graph\" type=\"info\"]\n%s\n[/fase:message]", sb.String())
}
