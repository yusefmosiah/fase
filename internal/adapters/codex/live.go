package codex

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

// LiveAdapter implements adapterapi.LiveAgentAdapter for the Codex app-server.
//
// Transport: JSON-RPC 2.0 over the app-server's stdio transport.
// Tool bridge: MCP via .mcp.json auto-discovery in the session CWD.
// Approval policy: "never" — fully autonomous execution.
type LiveAdapter struct {
	binary string
}

// NewLiveAdapter creates a LiveAdapter using the given codex binary.
func NewLiveAdapter(binary string) *LiveAdapter {
	return &LiveAdapter{binary: binary}
}

// Name returns the adapter identifier.
func (a *LiveAdapter) Name() string { return "codex" }

// StartSession spawns a new codex app-server process and starts a new thread.
func (a *LiveAdapter) StartSession(ctx context.Context, req adapterapi.StartSessionRequest) (adapterapi.LiveSession, error) {
	t, err := a.spawnTransport(ctx, req.CWD)
	if err != nil {
		return nil, err
	}

	if err := t.initialize(ctx); err != nil {
		_ = t.close()
		return nil, fmt.Errorf("codex initialize: %w", err)
	}

	threadID, err := t.threadStart(ctx, req.CWD, req.Model)
	if err != nil {
		_ = t.close()
		return nil, fmt.Errorf("codex thread/start: %w", err)
	}

	return newSession(ctx, t, threadID, req.SteerCh), nil
}

// ResumeSession spawns a new codex app-server process and resumes an existing thread.
func (a *LiveAdapter) ResumeSession(ctx context.Context, nativeSessionID string, req adapterapi.StartSessionRequest) (adapterapi.LiveSession, error) {
	t, err := a.spawnTransport(ctx, req.CWD)
	if err != nil {
		return nil, err
	}

	if err := t.initialize(ctx); err != nil {
		_ = t.close()
		return nil, fmt.Errorf("codex initialize: %w", err)
	}

	if err := t.threadResume(ctx, nativeSessionID, req.CWD, req.Model); err != nil {
		_ = t.close()
		return nil, fmt.Errorf("codex thread/resume: %w", err)
	}

	return newSession(ctx, t, nativeSessionID, req.SteerCh), nil
}

// spawnTransport starts the codex app-server subprocess and returns a connected transport.
func (a *LiveAdapter) spawnTransport(ctx context.Context, cwd string) (*transport, error) {
	args := []string{"app-server", "--listen", "stdio://"}
	cmd := exec.CommandContext(ctx, a.binary, args...)
	cmd.Dir = cwd
	adapterapi.PrepareCommand(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("codex app-server stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex app-server stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex app-server start: %w", err)
	}

	t := &transport{
		cmd:     cmd,
		w:       stdin,
		enc:     json.NewEncoder(stdin),
		pending: make(map[int64]chan rpcMessage),
		notifCh: make(chan rpcMessage, 128),
		done:    make(chan struct{}),
	}

	go t.readLoop(bufio.NewScanner(stdout))

	return t, nil
}

// --- JSON-RPC 2.0 Transport ---

// rpcRequest is an outbound JSON-RPC 2.0 request.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// rpcMessage is an inbound JSON-RPC 2.0 message (response or notification).
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.Number    `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcError is the JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// transport manages a JSON-RPC 2.0 connection to the codex app-server subprocess.
type transport struct {
	cmd *exec.Cmd

	// write side
	w   interface{ Write(p []byte) (int, error) }
	enc *json.Encoder
	mu  sync.Mutex // serializes writes

	// pending RPC correlation
	pendingMu sync.Mutex
	pending   map[int64]chan rpcMessage
	nextID    atomic.Int64

	// inbound notifications
	notifCh chan rpcMessage

	// lifecycle
	closeOnce sync.Once
	done      chan struct{}
}

// readLoop continuously reads lines from the subprocess stdout and dispatches
// them to either a pending request's reply channel or the notification channel.
func (t *transport) readLoop(scanner *bufio.Scanner) {
	// 4 MB buffer to handle large agent messages and diffs.
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	defer close(t.done)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue // skip malformed messages
		}

		switch {
		case msg.ID != nil && msg.Method != "":
			// Server request: Codex is asking us to approve something.
			// We auto-respond to keep the agent unblocked.
			go t.autoRespond(msg)

		case msg.ID != nil:
			// Response to one of our pending requests.
			id, err := msg.ID.Int64()
			if err != nil {
				continue
			}
			t.pendingMu.Lock()
			ch, ok := t.pending[id]
			if ok {
				delete(t.pending, id)
			}
			t.pendingMu.Unlock()
			if ok {
				select {
				case ch <- msg:
				default:
				}
			}

		case msg.Method != "":
			// Pure notification (no id).
			select {
			case t.notifCh <- msg:
			default:
				// drop if consumer is slow
			}
		}
	}
}

// autoRespond sends an approval response to a server-initiated request.
// Codex sends these when approval policy is not "never" or for auth token
// refresh. We auto-approve to keep the agent unblocked.
func (t *transport) autoRespond(msg rpcMessage) {
	id, err := msg.ID.Int64()
	if err != nil {
		return
	}

	var result any
	switch msg.Method {
	case "item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"applyPatchApproval",
		"execCommandApproval",
		"item/permissions/requestApproval":
		result = map[string]any{"decision": "approved_for_session"}
	default:
		// Unknown server request: return null result rather than blocking.
		result = nil
	}

	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
	t.mu.Lock()
	_ = t.enc.Encode(resp)
	t.mu.Unlock()
}

// call sends a JSON-RPC 2.0 request and waits for the response.
func (t *transport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := t.nextID.Add(1)
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	replyCh := make(chan rpcMessage, 1)
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
	case msg := <-replyCh:
		if msg.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", msg.Error.Code, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

// notify sends a JSON-RPC 2.0 notification (no ID, no response expected).
func (t *transport) notify(method string, params any) error {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.enc.Encode(msg)
}

// close terminates the subprocess and cleans up.
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

// --- Protocol Methods ---

// initialize sends the JSON-RPC initialize request.
func (t *transport) initialize(ctx context.Context) error {
	params := map[string]any{
		"clientInfo": map[string]any{
			"name":    "cogent",
			"title":   nil, // nullable per ClientInfo schema
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": false,
		},
	}
	_, err := t.call(ctx, "initialize", params)
	if err != nil {
		return err
	}
	// Send initialized notification to complete handshake.
	return t.notify("initialized", map[string]any{})
}

// threadStart starts a new Codex thread and returns the thread ID.
func (t *transport) threadStart(ctx context.Context, cwd, model string) (string, error) {
	params := map[string]any{
		"cwd":            cwd,
		"approvalPolicy": "never",
	}
	if model != "" {
		params["model"] = model
	}

	result, err := t.call(ctx, "thread/start", params)
	if err != nil {
		return "", err
	}

	// thread/start response returns the thread object directly.
	var thread struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result, &thread); err != nil {
		return "", fmt.Errorf("unmarshal thread/start result: %w", err)
	}
	if thread.ID == "" {
		// Some versions wrap in a "thread" field.
		var wrapped struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err2 := json.Unmarshal(result, &wrapped); err2 == nil && wrapped.Thread.ID != "" {
			return wrapped.Thread.ID, nil
		}
		return "", fmt.Errorf("thread/start: empty thread id in response: %s", string(result))
	}
	return thread.ID, nil
}

// threadResume resumes an existing Codex thread.
func (t *transport) threadResume(ctx context.Context, threadID, cwd, model string) error {
	params := map[string]any{
		"threadId":       threadID,
		"cwd":            cwd,
		"approvalPolicy": "never",
	}
	if model != "" {
		params["model"] = model
	}
	_, err := t.call(ctx, "thread/resume", params)
	return err
}

// turnStart sends a turn/start request and returns the turn ID.
func (t *transport) turnStart(ctx context.Context, threadID string, inputs []adapterapi.Input) (string, error) {
	userInputs := make([]map[string]string, len(inputs))
	for i, inp := range inputs {
		userInputs[i] = map[string]string{"type": "text", "text": inp.Text}
	}

	params := map[string]any{
		"threadId": threadID,
		"input":    userInputs,
	}

	result, err := t.call(ctx, "turn/start", params)
	if err != nil {
		return "", err
	}

	// turn/start returns the turn object.
	var turn struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result, &turn); err != nil {
		return "", fmt.Errorf("unmarshal turn/start result: %w", err)
	}
	if turn.ID == "" {
		var wrapped struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err2 := json.Unmarshal(result, &wrapped); err2 == nil && wrapped.Turn.ID != "" {
			return wrapped.Turn.ID, nil
		}
		return "", fmt.Errorf("turn/start: empty turn id in response: %s", string(result))
	}
	return turn.ID, nil
}

// turnSteer sends a turn/steer request with the given inputs.
func (t *transport) turnSteer(ctx context.Context, threadID, expectedTurnID string, inputs []adapterapi.Input) error {
	userInputs := make([]map[string]string, len(inputs))
	for i, inp := range inputs {
		userInputs[i] = map[string]string{"type": "text", "text": inp.Text}
	}

	params := map[string]any{
		"threadId":       threadID,
		"expectedTurnId": expectedTurnID,
		"input":          userInputs,
	}

	_, err := t.call(ctx, "turn/steer", params)
	return err
}

// turnInterrupt sends a turn/interrupt request.
func (t *transport) turnInterrupt(ctx context.Context, threadID, turnID string) error {
	params := map[string]any{
		"threadId": threadID,
		"turnId":   turnID,
	}
	_, err := t.call(ctx, "turn/interrupt", params)
	return err
}

// --- Session ---

// codexSession implements adapterapi.LiveSession for a Codex app-server connection.
type codexSession struct {
	t        *transport
	threadID string

	activeMu   sync.Mutex
	activeTurn string

	eventCh      chan adapterapi.Event
	eventChClose sync.Once
	steerCh      <-chan adapterapi.SteerEvent

	ctx    context.Context
	cancel context.CancelFunc
}

// newSession creates and starts a codexSession.
func newSession(ctx context.Context, t *transport, threadID string, steerCh <-chan adapterapi.SteerEvent) *codexSession {
	sctx, cancel := context.WithCancel(ctx)
	s := &codexSession{
		t:        t,
		threadID: threadID,
		eventCh:  make(chan adapterapi.Event, 128),
		steerCh:  steerCh,
		ctx:      sctx,
		cancel:   cancel,
	}

	s.eventCh <- adapterapi.Event{
		Kind:      adapterapi.EventKindSessionStarted,
		SessionID: threadID,
	}

	go s.dispatchLoop()
	if steerCh != nil {
		go s.steerLoop()
	}

	return s
}

// SessionID returns the Codex thread ID (maps to cogent session ID).
func (s *codexSession) SessionID() string { return s.threadID }

// ActiveTurnID returns the current active Codex turn ID.
func (s *codexSession) ActiveTurnID() string {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	return s.activeTurn
}

// StartTurn begins a new turn in the session.
func (s *codexSession) StartTurn(ctx context.Context, input []adapterapi.Input) (string, error) {
	turnID, err := s.t.turnStart(ctx, s.threadID, input)
	if err != nil {
		return "", fmt.Errorf("turn/start: %w", err)
	}

	s.activeMu.Lock()
	s.activeTurn = turnID
	s.activeMu.Unlock()

	return turnID, nil
}

// Steer injects additional input into the active turn.
func (s *codexSession) Steer(ctx context.Context, expectedTurnID string, input []adapterapi.Input) error {
	return s.t.turnSteer(ctx, s.threadID, expectedTurnID, input)
}

// Interrupt cancels the active turn.
func (s *codexSession) Interrupt(ctx context.Context) error {
	s.activeMu.Lock()
	turnID := s.activeTurn
	s.activeMu.Unlock()

	if turnID == "" {
		return fmt.Errorf("no active turn")
	}
	return s.t.turnInterrupt(ctx, s.threadID, turnID)
}

// Events returns the session event channel.
func (s *codexSession) Events() <-chan adapterapi.Event {
	return s.eventCh
}

// Close shuts down the session. The event channel is closed by dispatchLoop
// once it observes the cancelled context, avoiding a race with emit().
func (s *codexSession) Close() error {
	s.cancel()
	return s.t.close()
}

// dispatchLoop reads notifications from the transport and converts them to Events.
// It owns the eventCh lifetime and closes it on exit.
func (s *codexSession) dispatchLoop() {
	defer s.eventChClose.Do(func() { close(s.eventCh) })
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.t.done:
			s.emit(adapterapi.Event{
				Kind:      adapterapi.EventKindSessionClosed,
				SessionID: s.threadID,
			})
			return
		case msg, ok := <-s.t.notifCh:
			if !ok {
				return
			}
			s.handleNotification(msg)
		}
	}
}

// handleNotification converts a Codex server notification to a cogent Event.
func (s *codexSession) handleNotification(msg rpcMessage) {
	switch msg.Method {
	case "turn/started":
		var p struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return
		}
		if p.ThreadID != s.threadID {
			return
		}
		s.activeMu.Lock()
		s.activeTurn = p.Turn.ID
		s.activeMu.Unlock()
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindTurnStarted,
			SessionID: s.threadID,
			TurnID:    p.Turn.ID,
		})

	case "turn/completed":
		var p struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return
		}
		if p.ThreadID != s.threadID {
			return
		}
		s.activeMu.Lock()
		if s.activeTurn == p.Turn.ID {
			s.activeTurn = ""
		}
		s.activeMu.Unlock()

		kind := adapterapi.EventKindTurnCompleted
		switch p.Turn.Status {
		case "failed":
			kind = adapterapi.EventKindTurnFailed
		case "interrupted":
			kind = adapterapi.EventKindTurnInterrupted
		}
		text := ""
		if p.Turn.Error != nil {
			text = p.Turn.Error.Message
		}
		s.emit(adapterapi.Event{
			Kind:      kind,
			SessionID: s.threadID,
			TurnID:    p.Turn.ID,
			Text:      text,
		})

	case "item/agentMessage/delta":
		var p struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Delta    string `json:"delta"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return
		}
		if p.ThreadID != s.threadID {
			return
		}
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindOutputDelta,
			SessionID: s.threadID,
			TurnID:    p.TurnID,
			Text:      p.Delta,
		})

	case "error":
		var p struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return
		}
		if p.ThreadID != "" && p.ThreadID != s.threadID {
			return
		}
		text := ""
		if p.Error != nil {
			text = p.Error.Message
		}
		s.emit(adapterapi.Event{
			Kind:      adapterapi.EventKindError,
			SessionID: s.threadID,
			TurnID:    p.TurnID,
			Text:      text,
		})
	}
}

// steerLoop watches the SteerCh and injects messages into active turns.
func (s *codexSession) steerLoop() {
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
			s.activeMu.Unlock()

			if turnID == "" {
				// No active turn; drop the steer event.
				continue
			}

			// Format as a cogent inter-agent message visible to the model.
			msg := fmt.Sprintf("[cogent:message from=\"supervisor\" type=\"info\"]\n%s\n[/cogent:message]", ev.Message)
			_ = s.t.turnSteer(s.ctx, s.threadID, turnID, []adapterapi.Input{adapterapi.TextInput(msg)})
		}
	}
}

// emit sends an event to the event channel.
// It does not block: events are dropped when the consumer is slow or the
// context is already cancelled.
func (s *codexSession) emit(ev adapterapi.Event) {
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
