package native

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yusefmosiah/fase/internal/adapterapi"
)

type nativeSessionConfig struct {
	id       string
	provider Provider
	client   LLMClient
	registry *ToolRegistry
	steerCh  <-chan adapterapi.SteerEvent
	svc      any
	resumed  bool
	manager  *coAgentManager
}

type nativeSession struct {
	id       string
	provider Provider
	client   LLMClient
	registry *ToolRegistry

	history []Message
	tools   []ToolDef

	eventCh chan adapterapi.Event
	steerQ  chan adapterapi.SteerEvent
	svc     any
	manager *coAgentManager

	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	activeTurn  string
	turnCancel  context.CancelFunc
	turnDone    chan struct{}
	closed      bool
	closeOnce   sync.Once
	turnSeq     atomic.Int64
	previousID  string
	steerBridge sync.WaitGroup
}

func newNativeSession(ctx context.Context, cfg nativeSessionConfig) *nativeSession {
	sctx, cancel := context.WithCancel(ctx)
	s := &nativeSession{
		id:       cfg.id,
		provider: cfg.provider,
		client:   cfg.client,
		registry: cfg.registry,
		tools:    cfg.registry.CoreDefinitions(), // core tools upfront, rest on demand
		eventCh:  make(chan adapterapi.Event, 256),
		steerQ:   make(chan adapterapi.SteerEvent, 64),
		svc:      cfg.svc,
		manager:  cfg.manager,
		ctx:      sctx,
		cancel:   cancel,
	}

	kind := adapterapi.EventKindSessionStarted
	if cfg.resumed {
		kind = adapterapi.EventKindSessionResumed
	}
	s.emit(adapterapi.Event{Kind: kind, SessionID: s.id})

	if cfg.steerCh != nil {
		s.steerBridge.Add(1)
		go s.forwardSteers(cfg.steerCh)
	}

	return s
}

func (s *nativeSession) SessionID() string { return s.id }

func (s *nativeSession) ActiveTurnID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeTurn
}

func (s *nativeSession) StartTurn(ctx context.Context, input []adapterapi.Input) (string, error) {
	userMessage := messageFromInputs(input)
	if len(userMessage.Content) == 0 {
		return "", fmt.Errorf("native session: empty input")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", fmt.Errorf("native session: session closed")
	}
	if s.activeTurn != "" {
		return "", fmt.Errorf("native session: turn %s already active", s.activeTurn)
	}

	turnID := fmt.Sprintf("nturn-%d", s.turnSeq.Add(1))
	s.history = append(s.history, userMessage)
	turnCtx, turnCancel := context.WithCancel(s.ctx)
	if ctx != nil {
		go func(parent context.Context, child context.Context, cancel context.CancelFunc) {
			select {
			case <-parent.Done():
				cancel()
			case <-child.Done():
			}
		}(ctx, turnCtx, turnCancel)
	}
	done := make(chan struct{})
	s.activeTurn = turnID
	s.turnCancel = turnCancel
	s.turnDone = done

	s.emit(adapterapi.Event{
		Kind:      adapterapi.EventKindTurnStarted,
		SessionID: s.id,
		TurnID:    turnID,
	})

	go s.runTurn(turnCtx, turnID, done)
	return turnID, nil
}

func (s *nativeSession) Steer(_ context.Context, expectedTurnID string, input []adapterapi.Input) error {
	message := strings.TrimSpace(joinInputs(input))
	if message == "" {
		return fmt.Errorf("native session: empty steer input")
	}

	s.mu.Lock()
	activeTurn := s.activeTurn
	closed := s.closed
	s.mu.Unlock()

	if closed {
		return fmt.Errorf("native session: session closed")
	}
	if activeTurn == "" {
		return fmt.Errorf("native session: no active turn")
	}
	if expectedTurnID != "" && expectedTurnID != activeTurn {
		return fmt.Errorf("native session: turn mismatch expected=%s active=%s", expectedTurnID, activeTurn)
	}

	select {
	case s.steerQ <- adapterapi.SteerEvent{Message: message}:
		return nil
	case <-s.ctx.Done():
		return fmt.Errorf("native session: session closed")
	}
}

func (s *nativeSession) Interrupt(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeTurn == "" || s.turnCancel == nil {
		return fmt.Errorf("native session: no active turn")
	}
	s.turnCancel()
	return nil
}

func (s *nativeSession) Events() <-chan adapterapi.Event { return s.eventCh }

func (s *nativeSession) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()

		s.mu.Lock()
		turnDone := s.turnDone
		turnCancel := s.turnCancel
		s.closed = true
		s.mu.Unlock()

		if turnCancel != nil {
			turnCancel()
		}
		if turnDone != nil {
			<-turnDone
		}

		if s.manager != nil {
			_ = s.manager.closeAll()
		}

		s.steerBridge.Wait()
		s.emit(adapterapi.Event{Kind: adapterapi.EventKindSessionClosed, SessionID: s.id})
		close(s.eventCh)
	})
	return nil
}

func (s *nativeSession) runTurn(ctx context.Context, turnID string, done chan struct{}) {
	defer close(done)

	if err := s.runToolLoop(ctx, turnID); err != nil {
		if ctx.Err() != nil {
			s.emit(adapterapi.Event{
				Kind:      adapterapi.EventKindTurnInterrupted,
				SessionID: s.id,
				TurnID:    turnID,
			})
		} else {
			s.emit(adapterapi.Event{
				Kind:      adapterapi.EventKindTurnFailed,
				SessionID: s.id,
				TurnID:    turnID,
				Text:      err.Error(),
			})
		}
	}

	s.mu.Lock()
	if s.activeTurn == turnID {
		s.activeTurn = ""
	}
	s.turnCancel = nil
	s.turnDone = nil
	s.mu.Unlock()
}

func (s *nativeSession) forwardSteers(steerCh <-chan adapterapi.SteerEvent) {
	defer s.steerBridge.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case steer, ok := <-steerCh:
			if !ok {
				return
			}
			select {
			case s.steerQ <- steer:
			case <-s.ctx.Done():
				return
			}
		}
	}
}

func (s *nativeSession) emit(ev adapterapi.Event) {
	select {
	case s.eventCh <- ev:
	default:
	}
}

func messageFromInputs(input []adapterapi.Input) Message {
	msg := Message{Role: "user"}
	for _, item := range input {
		if text := strings.TrimSpace(item.Text); text != "" {
			msg.Content = append(msg.Content, ContentBlock{
				Type: "text",
				Text: item.Text,
			})
		}
	}
	return msg
}

func joinInputs(input []adapterapi.Input) string {
	parts := make([]string, 0, len(input))
	for _, item := range input {
		if text := strings.TrimSpace(item.Text); text != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}
