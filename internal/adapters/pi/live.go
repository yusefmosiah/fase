package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

type DeliveryMode string

const (
	DeliverySteer    DeliveryMode = "steer"
	DeliveryFollowUp DeliveryMode = "follow_up"
)

type LiveAdapter struct {
	binary string
}

func NewLiveAdapter(binary string) *LiveAdapter {
	return &LiveAdapter{binary: binary}
}

func (a *LiveAdapter) Name() string { return "pi" }

func (a *LiveAdapter) StartSession(ctx context.Context, req adapterapi.StartSessionRequest) (adapterapi.LiveSession, error) {
	t, err := a.spawnTransport(ctx, req.CWD, req.Model, "")
	if err != nil {
		return nil, err
	}

	state, err := t.getState(ctx)
	if err != nil {
		_ = t.close()
		return nil, fmt.Errorf("pi get_state: %w", err)
	}

	if state.SessionID == "" {
		_ = t.close()
		return nil, fmt.Errorf("pi session: empty session id after startup")
	}

	return newSession(ctx, t, state.SessionID, state.SessionFile, req.SteerCh), nil
}

func (a *LiveAdapter) ResumeSession(ctx context.Context, nativeSessionID string, req adapterapi.StartSessionRequest) (adapterapi.LiveSession, error) {
	t, err := a.spawnTransport(ctx, req.CWD, req.Model, nativeSessionID)
	if err != nil {
		return nil, err
	}

	state, err := t.getState(ctx)
	if err != nil {
		_ = t.close()
		return nil, fmt.Errorf("pi get_state: %w", err)
	}

	return newSession(ctx, t, state.SessionID, state.SessionFile, req.SteerCh), nil
}

func (a *LiveAdapter) spawnTransport(ctx context.Context, cwd, model, sessionPath string) (*transport, error) {
	args := []string{"--mode", "rpc"}
	if sessionPath != "" {
		args = append(args, "--session", sessionPath)
	} else {
		args = append(args, "--no-session")
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.CommandContext(ctx, a.binary, args...)
	cmd.Dir = cwd
	adapterapi.PrepareCommand(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pi rpc stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pi rpc stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pi rpc start: %w", err)
	}

	t := &transport{
		cmd:       cmd,
		w:         stdin,
		enc:       json.NewEncoder(stdin),
		pending:   make(map[string]chan rpcResponse),
		eventCh:   make(chan json.RawMessage, 256),
		nextReqID: atomic.Int64{},
		done:      make(chan struct{}),
	}

	go t.readLoop(bufio.NewScanner(stdout))

	return t, nil
}

// --- JSONL RPC Transport ---

type rpcResponse struct {
	ID       *string         `json:"id,omitempty"`
	Type     string          `json:"type"`
	Command  string          `json:"command,omitempty"`
	Success  bool            `json:"success"`
	Data     json.RawMessage `json:"data,omitempty"`
	Error    string          `json:"error,omitempty"`
	RawEvent json.RawMessage `json:"-"`
}

type piState struct {
	SessionID   string `json:"sessionId"`
	SessionFile string `json:"sessionFile"`
	IsStreaming bool   `json:"isStreaming"`
}

type transport struct {
	cmd *exec.Cmd

	w   interface{ Write(p []byte) (int, error) }
	enc *json.Encoder
	mu  sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan rpcResponse
	nextReqID atomic.Int64

	eventCh chan json.RawMessage

	closeOnce sync.Once
	done      chan struct{}
}

func (t *transport) readLoop(scanner *bufio.Scanner) {
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	defer close(t.done)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}

		typeVal, ok := raw["type"]
		if !ok {
			continue
		}
		var typeStr string
		if err := json.Unmarshal(typeVal, &typeStr); err != nil {
			continue
		}

		switch typeStr {
		case "response":
			var resp rpcResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				continue
			}
			if resp.ID != nil {
				t.pendingMu.Lock()
				ch, ok := t.pending[*resp.ID]
				if ok {
					delete(t.pending, *resp.ID)
				}
				t.pendingMu.Unlock()
				if ok {
					select {
					case ch <- resp:
					default:
					}
				}
			}

		default:
			select {
			case t.eventCh <- line:
			default:
			}
		}
	}
}

func (t *transport) nextID() string {
	return fmt.Sprintf("fase-%d", t.nextReqID.Add(1))
}

func (t *transport) call(ctx context.Context, cmdType string, params map[string]any) (json.RawMessage, error) {
	id := t.nextID()
	params["type"] = cmdType
	params["id"] = id

	req := params

	replyCh := make(chan rpcResponse, 1)
	t.pendingMu.Lock()
	t.pending[id] = replyCh
	t.pendingMu.Unlock()

	t.mu.Lock()
	encErr := t.enc.Encode(req)
	t.mu.Unlock()
	if encErr != nil {
		t.pendingMu.Lock()
		delete(t.pending, id)
		t.pendingMu.Unlock()
		return nil, fmt.Errorf("encode rpc request: %w", encErr)
	}

	select {
	case <-ctx.Done():
		t.pendingMu.Lock()
		delete(t.pending, id)
		t.pendingMu.Unlock()
		return nil, ctx.Err()
	case <-t.done:
		return nil, fmt.Errorf("transport closed")
	case resp := <-replyCh:
		if !resp.Success {
			return nil, fmt.Errorf("rpc error %s: %s", resp.Command, resp.Error)
		}
		return resp.Data, nil
	}
}

func (t *transport) send(ctx context.Context, cmdType string, params map[string]any) error {
	id := t.nextID()
	params["type"] = cmdType
	params["id"] = id

	req := params

	replyCh := make(chan rpcResponse, 1)
	t.pendingMu.Lock()
	t.pending[id] = replyCh
	t.pendingMu.Unlock()

	t.mu.Lock()
	encErr := t.enc.Encode(req)
	t.mu.Unlock()
	if encErr != nil {
		t.pendingMu.Lock()
		delete(t.pending, id)
		t.pendingMu.Unlock()
		return fmt.Errorf("encode rpc request: %w", encErr)
	}

	select {
	case <-ctx.Done():
		t.pendingMu.Lock()
		delete(t.pending, id)
		t.pendingMu.Unlock()
		return ctx.Err()
	case <-t.done:
		return fmt.Errorf("transport closed")
	case resp := <-replyCh:
		if !resp.Success {
			return fmt.Errorf("rpc error %s: %s", resp.Command, resp.Error)
		}
		return nil
	}
}

func (t *transport) getState(ctx context.Context) (*piState, error) {
	data, err := t.call(ctx, "get_state", map[string]any{})
	if err != nil {
		return nil, err
	}
	var state piState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal get_state: %w", err)
	}
	return &state, nil
}

func (t *transport) prompt(ctx context.Context, message string, streamingBehavior string) error {
	params := map[string]any{
		"message": message,
	}
	if streamingBehavior != "" {
		params["streamingBehavior"] = streamingBehavior
	}
	return t.send(ctx, "prompt", params)
}

func (t *transport) steer(ctx context.Context, message string) error {
	return t.send(ctx, "steer", map[string]any{
		"message": message,
	})
}

func (t *transport) followUp(ctx context.Context, message string) error {
	return t.send(ctx, "follow_up", map[string]any{
		"message": message,
	})
}

func (t *transport) abort(ctx context.Context) error {
	return t.send(ctx, "abort", map[string]any{})
}

func (t *transport) setSteeringMode(ctx context.Context, mode string) error {
	return t.send(ctx, "set_steering_mode", map[string]any{
		"mode": mode,
	})
}

func (t *transport) setFollowUpMode(ctx context.Context, mode string) error {
	return t.send(ctx, "set_follow_up_mode", map[string]any{
		"mode": mode,
	})
}

func (t *transport) close() error {
	var err error
	t.closeOnce.Do(func() {
		if t.cmd.Process != nil {
			err = t.cmd.Process.Kill()
			_ = t.cmd.Wait()
		}
	})
	return err
}

// --- Session ---

type piSession struct {
	t           *transport
	sessionID   string
	sessionFile string

	activeMu   sync.Mutex
	activeTurn string
	turnSeq    int

	eventCh      chan adapterapi.Event
	eventChClose sync.Once
	steerCh      <-chan adapterapi.SteerEvent

	deliveryMode DeliveryMode

	ctx    context.Context
	cancel context.CancelFunc
}

func newSession(ctx context.Context, t *transport, sessionID, sessionFile string, steerCh <-chan adapterapi.SteerEvent) *piSession {
	sctx, cancel := context.WithCancel(ctx)
	s := &piSession{
		t:            t,
		sessionID:    sessionID,
		sessionFile:  sessionFile,
		eventCh:      make(chan adapterapi.Event, 128),
		steerCh:      steerCh,
		deliveryMode: DeliverySteer,
		ctx:          sctx,
		cancel:       cancel,
	}

	s.eventCh <- adapterapi.Event{
		Kind:      adapterapi.EventKindSessionStarted,
		SessionID: sessionID,
	}

	go s.dispatchLoop()
	if steerCh != nil {
		go s.steerLoop()
	}

	return s
}

func (s *piSession) SessionID() string { return s.sessionID }

func (s *piSession) ActiveTurnID() string {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	return s.activeTurn
}

func (s *piSession) SetDeliveryMode(mode DeliveryMode) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	s.deliveryMode = mode
}

func (s *piSession) StartTurn(ctx context.Context, input []adapterapi.Input) (string, error) {
	var text string
	for _, inp := range input {
		if inp.Text != "" {
			text = inp.Text
			break
		}
	}
	if text == "" {
		return "", fmt.Errorf("pi startTurn: empty input")
	}

	behavior := "steer"
	s.activeMu.Lock()
	if s.deliveryMode == DeliveryFollowUp {
		behavior = "followUp"
	}
	s.activeMu.Unlock()

	if err := s.t.prompt(ctx, text, behavior); err != nil {
		return "", fmt.Errorf("pi prompt: %w", err)
	}

	s.activeMu.Lock()
	s.turnSeq++
	turnID := fmt.Sprintf("turn-%d", s.turnSeq)
	s.activeTurn = turnID
	s.activeMu.Unlock()

	return turnID, nil
}

func (s *piSession) Steer(ctx context.Context, expectedTurnID string, input []adapterapi.Input) error {
	s.activeMu.Lock()
	if s.activeTurn == "" {
		s.activeMu.Unlock()
		return fmt.Errorf("pi steer: no active turn")
	}
	if expectedTurnID != "" && expectedTurnID != s.activeTurn {
		s.activeMu.Unlock()
		return fmt.Errorf("pi steer: turn mismatch expected=%s active=%s", expectedTurnID, s.activeTurn)
	}
	s.activeMu.Unlock()

	var text string
	for _, inp := range input {
		if inp.Text != "" {
			text = inp.Text
			break
		}
	}

	s.activeMu.Lock()
	mode := s.deliveryMode
	s.activeMu.Unlock()

	switch mode {
	case DeliveryFollowUp:
		return s.t.followUp(ctx, text)
	default:
		return s.t.steer(ctx, text)
	}
}

func (s *piSession) Interrupt(ctx context.Context) error {
	return s.t.abort(ctx)
}

func (s *piSession) Events() <-chan adapterapi.Event {
	return s.eventCh
}

func (s *piSession) Close() error {
	s.cancel()
	return s.t.close()
}

func (s *piSession) dispatchLoop() {
	defer s.eventChClose.Do(func() { close(s.eventCh) })
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.t.done:
			s.emit(adapterapi.Event{
				Kind:      adapterapi.EventKindSessionClosed,
				SessionID: s.sessionID,
			})
			return
		case rawLine, ok := <-s.t.eventCh:
			if !ok {
				return
			}
			s.handleEvent(rawLine)
		}
	}
}

func (s *piSession) handleEvent(rawLine json.RawMessage) {
	var base struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(rawLine, &base); err != nil {
		return
	}

	switch base.Type {
	case "agent_start":
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindTurnStarted,
			SessionID: s.sessionID,
			TurnID:    s.ActiveTurnID(),
		})

	case "agent_end":
		s.activeMu.Lock()
		s.activeTurn = ""
		s.activeMu.Unlock()
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindTurnCompleted,
			SessionID: s.sessionID,
		})

	case "turn_start":
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindTurnStarted,
			SessionID: s.sessionID,
			TurnID:    s.ActiveTurnID(),
		})

	case "turn_end":
		var p struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		if err := json.Unmarshal(rawLine, &p); err == nil && p.Message.Role == "user" {
			s.activeMu.Lock()
			s.activeTurn = ""
			s.activeMu.Unlock()
		}
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindTurnCompleted,
			SessionID: s.sessionID,
		})

	case "message_update":
		var p struct {
			AssistantMessageEvent struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			} `json:"assistantMessageEvent"`
		}
		if err := json.Unmarshal(rawLine, &p); err != nil {
			return
		}
		switch p.AssistantMessageEvent.Type {
		case "text_delta":
			s.emit(adapterapi.Event{
				Kind:      adapterapi.EventKindOutputDelta,
				SessionID: s.sessionID,
				TurnID:    s.ActiveTurnID(),
				Text:      p.AssistantMessageEvent.Delta,
			})
		case "thinking_delta":
			s.emit(adapterapi.Event{
				Kind:      adapterapi.EventKindOutputDelta,
				SessionID: s.sessionID,
				TurnID:    s.ActiveTurnID(),
				Text:      p.AssistantMessageEvent.Delta,
			})
		}

	case "tool_execution_start":
		var p struct {
			ToolName string `json:"toolName"`
			Args     any    `json:"args"`
		}
		if err := json.Unmarshal(rawLine, &p); err != nil {
			return
		}
		argsJSON, _ := json.Marshal(p.Args)
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindOutputDelta,
			SessionID: s.sessionID,
			TurnID:    s.ActiveTurnID(),
			Text:      fmt.Sprintf("[tool:%s args=%s]", p.ToolName, string(argsJSON)),
		})

	case "tool_execution_end":
		var p struct {
			ToolName string `json:"toolName"`
			Result   any    `json:"result"`
			IsError  bool   `json:"isError"`
		}
		if err := json.Unmarshal(rawLine, &p); err != nil {
			return
		}
		if p.IsError {
			s.emit(adapterapi.Event{
				Kind:      adapterapi.EventKindError,
				SessionID: s.sessionID,
				TurnID:    s.ActiveTurnID(),
				Text:      fmt.Sprintf("tool %s error: %v", p.ToolName, p.Result),
			})
		}

	case "error":
		var p struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rawLine, &p); err != nil {
			return
		}
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindError,
			SessionID: s.sessionID,
			Text:      p.Error,
		})
	}
}

func (s *piSession) steerLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case ev, ok := <-s.steerCh:
			if !ok {
				return
			}
			s.activeMu.Lock()
			turnID := s.activeTurn
			mode := s.deliveryMode
			s.activeMu.Unlock()

			if turnID == "" {
				continue
			}

			msg := fmt.Sprintf("[fase:message from=\"supervisor\" type=\"info\"]\n%s\n[/fase:message]", ev.Message)
			switch mode {
			case DeliveryFollowUp:
				_ = s.t.followUp(s.ctx, msg)
			default:
				_ = s.t.steer(s.ctx, msg)
			}
		}
	}
}

func (s *piSession) emit(ev adapterapi.Event) {
	select {
	case <-s.ctx.Done():
		return
	default:
	}
	select {
	case s.eventCh <- ev:
	default:
	}
}
