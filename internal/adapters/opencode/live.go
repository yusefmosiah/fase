package opencode

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	opencodesdk "github.com/sst/opencode-sdk-go"
	"github.com/sst/opencode-sdk-go/option"
	"github.com/sst/opencode-sdk-go/packages/ssestream"
	"github.com/sst/opencode-sdk-go/shared"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

// LiveAdapter implements adapterapi.LiveAgentAdapter for OpenCode.
//
// Transport: HTTP REST control via the official Go SDK and SSE event streaming.
// Steering: noReply prompt injection is used as the best-effort mid-turn workaround.
// Interrupt: session abort is best-effort because OpenCode abort is unreliable in practice.
type LiveAdapter struct {
	binary string

	baseURL string

	clientFactory func(baseURL string) *opencodesdk.Client
	spawnServer   func(ctx context.Context, cwd string) (*serverProcess, error)
}

// NewLiveAdapter creates a LiveAdapter using the given opencode binary.
func NewLiveAdapter(binary string) *LiveAdapter {
	return &LiveAdapter{
		binary:        binary,
		clientFactory: defaultOpenCodeClient,
		spawnServer:   nil,
	}
}

// Name returns the adapter identifier.
func (a *LiveAdapter) Name() string { return "opencode" }

// StartSession creates a new OpenCode session and connects to its SSE stream.
func (a *LiveAdapter) StartSession(ctx context.Context, req adapterapi.StartSessionRequest) (adapterapi.LiveSession, error) {
	return a.startSession(ctx, req, false, "")
}

// ResumeSession reconnects to an existing OpenCode session.
func (a *LiveAdapter) ResumeSession(ctx context.Context, nativeSessionID string, req adapterapi.StartSessionRequest) (adapterapi.LiveSession, error) {
	return a.startSession(ctx, req, true, nativeSessionID)
}

func (a *LiveAdapter) startSession(ctx context.Context, req adapterapi.StartSessionRequest, resume bool, nativeSessionID string) (adapterapi.LiveSession, error) {
	baseURL := a.baseURL
	var proc *serverProcess
	var err error
	if baseURL == "" {
		spawn := a.spawnServer
		if spawn == nil {
			spawn = a.defaultSpawnServer
		}
		proc, err = spawn(ctx, req.CWD)
		if err != nil {
			return nil, err
		}
		baseURL = proc.baseURL
	}

	client := a.clientFactory
	if client == nil {
		client = defaultOpenCodeClient
	}
	sdk := client(baseURL)

	var session *opencodesdk.Session
	if resume {
		session, err = sdk.Session.Get(ctx, nativeSessionID, opencodesdk.SessionGetParams{
			Directory: opencodesdk.String(req.CWD),
		})
	} else {
		session, err = sdk.Session.New(ctx, opencodesdk.SessionNewParams{
			Directory: opencodesdk.String(req.CWD),
		})
	}
	if err != nil {
		if proc != nil {
			_ = proc.close()
		}
		if resume {
			return nil, fmt.Errorf("opencode session/get: %w", err)
		}
		return nil, fmt.Errorf("opencode session/new: %w", err)
	}
	if session == nil || session.ID == "" {
		if proc != nil {
			_ = proc.close()
		}
		return nil, fmt.Errorf("opencode session: empty session id")
	}

	s := newLiveSession(ctx, sdk, proc, req.CWD, req.Model, req.Profile, session.ID, req.SteerCh, resume)
	return s, nil
}

func defaultOpenCodeClient(baseURL string) *opencodesdk.Client {
	return opencodesdk.NewClient(option.WithBaseURL(baseURL))
}

type serverProcess struct {
	cmd     *exec.Cmd
	baseURL string
}

func (p *serverProcess) close() error {
	if p == nil || p.cmd == nil {
		return nil
	}
	var err error
	if p.cmd.Process != nil {
		err = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	return err
}

func (a *LiveAdapter) defaultSpawnServer(ctx context.Context, cwd string) (*serverProcess, error) {
	port, release, err := reserveTCPPort()
	if err != nil {
		return nil, err
	}

	args := []string{
		"serve",
		"--hostname", "127.0.0.1",
		"--port", strconv.Itoa(port),
	}
	cmd := exec.CommandContext(ctx, a.binary, args...)
	cmd.Dir = cwd
	adapterapi.PrepareCommand(cmd)

	if err := cmd.Start(); err != nil {
		_ = release()
		return nil, fmt.Errorf("start opencode serve: %w", err)
	}
	_ = release()

	proc := &serverProcess{
		cmd:     cmd,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
	}

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := waitForOpenCode(waitCtx, proc.baseURL); err != nil {
		_ = proc.close()
		return nil, err
	}

	return proc, nil
}

func reserveTCPPort() (port int, release func() error, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, fmt.Errorf("reserve opencode port: %w", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	return addr.Port, ln.Close, nil
}

func waitForOpenCode(ctx context.Context, baseURL string) error {
	client := defaultOpenCodeClient(baseURL)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, err := client.Session.List(ctx, opencodesdk.SessionListParams{}); err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for opencode server: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// --- Live Session ---

type liveSession struct {
	client *opencodesdk.Client
	proc   *serverProcess

	cwd        string
	sessionID  string
	modelRef   string
	profile    string
	activeTurn string
	turnSeq    atomic.Int64
	stream     *ssestream.Stream[opencodesdk.EventListResponse]

	interruptRequested bool
	sessionClosed      bool

	eventCh      chan adapterapi.Event
	eventChClose sync.Once

	steerCh <-chan adapterapi.SteerEvent

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
}

func newLiveSession(ctx context.Context, client *opencodesdk.Client, proc *serverProcess, cwd, modelRef, profile, sessionID string, steerCh <-chan adapterapi.SteerEvent, resumed bool) *liveSession {
	sctx, cancel := context.WithCancel(ctx)
	s := &liveSession{
		client:    client,
		proc:      proc,
		cwd:       cwd,
		sessionID: sessionID,
		modelRef:  modelRef,
		profile:   profile,
		eventCh:   make(chan adapterapi.Event, 128),
		steerCh:   steerCh,
		ctx:       sctx,
		cancel:    cancel,
	}

	kind := adapterapi.EventKindSessionStarted
	if resumed {
		kind = adapterapi.EventKindSessionResumed
	}
	s.eventCh <- adapterapi.Event{Kind: kind, SessionID: sessionID}

	go s.dispatchLoop()
	if steerCh != nil {
		go s.steerLoop()
	}

	return s
}

func (s *liveSession) SessionID() string { return s.sessionID }

func (s *liveSession) ActiveTurnID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeTurn
}

func (s *liveSession) StartTurn(ctx context.Context, input []adapterapi.Input) (string, error) {
	text := firstInputText(input)
	if text == "" {
		return "", fmt.Errorf("opencode start turn: empty input")
	}

	params := opencodesdk.SessionPromptParams{
		Parts: opencodesdk.F([]opencodesdk.SessionPromptParamsPartUnion{
			opencodesdk.TextPartInputParam{
				Text: opencodesdk.String(text),
				Type: opencodesdk.F(opencodesdk.TextPartInputTypeText),
			},
		}),
		Directory: opencodesdk.String(s.cwd),
	}
	if s.profile != "" {
		params.Agent = opencodesdk.String(s.profile)
	}
	if model, ok := parseOpenCodeModel(s.modelRef); ok {
		params.Model = opencodesdk.F(model)
	}

	resp, err := s.client.Session.Prompt(ctx, s.sessionID, params)
	if err != nil {
		return "", fmt.Errorf("opencode prompt: %w", err)
	}

	turnID := resp.Info.ID
	if turnID == "" {
		turnID = fmt.Sprintf("turn-%d", s.turnSeq.Add(1))
	}

	s.mu.Lock()
	s.activeTurn = turnID
	s.interruptRequested = false
	s.mu.Unlock()

	s.emit(adapterapi.Event{
		Kind:      adapterapi.EventKindTurnStarted,
		SessionID: s.sessionID,
		TurnID:    turnID,
	})

	return turnID, nil
}

func (s *liveSession) Steer(ctx context.Context, expectedTurnID string, input []adapterapi.Input) error {
	s.mu.Lock()
	activeTurn := s.activeTurn
	s.mu.Unlock()

	if activeTurn == "" {
		return fmt.Errorf("opencode steer: no active turn")
	}
	if expectedTurnID != "" && expectedTurnID != activeTurn {
		return fmt.Errorf("opencode steer: turn mismatch expected=%s active=%s", expectedTurnID, activeTurn)
	}

	text := firstInputText(input)
	if text == "" {
		return fmt.Errorf("opencode steer: empty input")
	}

	params := opencodesdk.SessionPromptParams{
		Parts: opencodesdk.F([]opencodesdk.SessionPromptParamsPartUnion{
			opencodesdk.TextPartInputParam{
				Text: opencodesdk.String(text),
				Type: opencodesdk.F(opencodesdk.TextPartInputTypeText),
			},
		}),
		Directory: opencodesdk.String(s.cwd),
		NoReply:   opencodesdk.Bool(true),
	}
	if s.profile != "" {
		params.Agent = opencodesdk.String(s.profile)
	}
	if model, ok := parseOpenCodeModel(s.modelRef); ok {
		params.Model = opencodesdk.F(model)
	}

	if _, err := s.client.Session.Prompt(ctx, s.sessionID, params); err != nil {
		return fmt.Errorf("opencode steer prompt: %w", err)
	}
	return nil
}

func (s *liveSession) Interrupt(ctx context.Context) error {
	s.mu.Lock()
	if s.activeTurn == "" {
		s.mu.Unlock()
		return fmt.Errorf("opencode interrupt: no active turn")
	}
	s.interruptRequested = true
	s.mu.Unlock()

	if _, err := s.client.Session.Abort(ctx, s.sessionID, opencodesdk.SessionAbortParams{
		Directory: opencodesdk.String(s.cwd),
	}); err != nil {
		return fmt.Errorf("opencode abort: %w", err)
	}
	return nil
}

func (s *liveSession) Events() <-chan adapterapi.Event { return s.eventCh }

func (s *liveSession) Close() error {
	s.cancel()
	if s.stream != nil {
		_ = s.stream.Close()
	}
	if s.proc != nil {
		_ = s.proc.close()
	}
	return nil
}

func (s *liveSession) dispatchLoop() {
	defer s.eventChClose.Do(func() { close(s.eventCh) })

	stream := s.client.Event.ListStreaming(s.ctx, opencodesdk.EventListParams{
		Directory: opencodesdk.String(s.cwd),
	})
	s.mu.Lock()
	s.stream = stream
	s.mu.Unlock()
	defer func() {
		if stream != nil {
			_ = stream.Close()
		}
	}()

	for stream != nil && stream.Next() {
		s.handleEvent(stream.Current())
	}

	if stream != nil {
		if err := stream.Err(); err != nil && s.ctx.Err() == nil {
			s.emit(adapterapi.Event{
				Kind:      adapterapi.EventKindError,
				SessionID: s.sessionID,
				Text:      err.Error(),
			})
		}
	}

	s.finishSession(adapterapi.EventKindSessionClosed)
}

func (s *liveSession) handleEvent(evt opencodesdk.EventListResponse) {
	switch ev := evt.AsUnion().(type) {
	case opencodesdk.EventListResponseEventSessionError:
		if ev.Properties.SessionID != "" && ev.Properties.SessionID != s.sessionID {
			return
		}
		s.mu.Lock()
		turnID := s.activeTurn
		wasInterrupt := s.interruptRequested
		s.activeTurn = ""
		s.interruptRequested = false
		s.mu.Unlock()

		kind := adapterapi.EventKindTurnFailed
		if wasInterrupt || isOpenCodeAbortError(ev.Properties.Error) {
			kind = adapterapi.EventKindTurnInterrupted
		}
		s.emit(adapterapi.Event{
			Kind:      kind,
			SessionID: s.sessionID,
			TurnID:    turnID,
			Text:      eventErrorText(ev.Properties.Error),
		})
	case opencodesdk.EventListResponseEventMessageUpdated:
		msg := ev.Properties.Info
		if msg.SessionID != s.sessionID || msg.Role != "assistant" {
			return
		}

		s.mu.Lock()
		if s.activeTurn == "" {
			s.activeTurn = msg.ID
		}
		turnID := s.activeTurn
		completed := assistantCompleted(msg.Time)
		wasInterrupt := s.interruptRequested
		if completed {
			s.activeTurn = ""
			s.interruptRequested = false
		}
		s.mu.Unlock()

		if completed {
			kind := adapterapi.EventKindTurnCompleted
			if wasInterrupt {
				kind = adapterapi.EventKindTurnInterrupted
			}
			s.emit(adapterapi.Event{
				Kind:      kind,
				SessionID: s.sessionID,
				TurnID:    turnID,
			})
		}
	case opencodesdk.EventListResponseEventMessagePartUpdated:
		part := ev.Properties.Part
		if part.SessionID != s.sessionID {
			return
		}

		s.mu.Lock()
		if s.activeTurn == "" {
			s.activeTurn = part.MessageID
		}
		turnID := s.activeTurn
		s.mu.Unlock()

		delta := strings.TrimSpace(ev.Properties.Delta)
		if delta == "" {
			delta = strings.TrimSpace(part.Text)
		}
		if delta != "" {
			s.emit(adapterapi.Event{
				Kind:      adapterapi.EventKindOutputDelta,
				SessionID: s.sessionID,
				TurnID:    turnID,
				Text:      delta,
			})
		}
	}
}

func (s *liveSession) finishSession(kind adapterapi.EventKind) {
	s.mu.Lock()
	if s.sessionClosed {
		s.mu.Unlock()
		return
	}
	s.sessionClosed = true
	s.mu.Unlock()

	s.emit(adapterapi.Event{
		Kind:      kind,
		SessionID: s.sessionID,
	})
}

func (s *liveSession) emit(ev adapterapi.Event) {
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

func (s *liveSession) steerLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case ev, ok := <-s.steerCh:
			if !ok {
				return
			}
			s.mu.Lock()
			turnID := s.activeTurn
			s.mu.Unlock()
			if turnID == "" {
				continue
			}
			_ = s.Steer(s.ctx, turnID, []adapterapi.Input{adapterapi.TextInput(ev.Message)})
		}
	}
}

func firstInputText(input []adapterapi.Input) string {
	for _, inp := range input {
		if inp.Text != "" {
			return inp.Text
		}
	}
	return ""
}

func parseOpenCodeModel(modelRef string) (opencodesdk.SessionPromptParamsModel, bool) {
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		return opencodesdk.SessionPromptParamsModel{}, false
	}
	providerID, modelID, ok := strings.Cut(modelRef, "/")
	if !ok || providerID == "" || modelID == "" {
		return opencodesdk.SessionPromptParamsModel{}, false
	}
	return opencodesdk.SessionPromptParamsModel{
		ModelID:    opencodesdk.String(modelID),
		ProviderID: opencodesdk.String(providerID),
	}, true
}

func isOpenCodeAbortError(err opencodesdk.EventListResponseEventSessionErrorPropertiesError) bool {
	switch err.AsUnion().(type) {
	case shared.MessageAbortedError:
		return true
	case shared.UnknownError:
		return strings.Contains(strings.ToLower(eventErrorText(err)), "abort")
	default:
		return strings.Contains(strings.ToLower(eventErrorText(err)), "abort")
	}
}

func eventErrorText(err opencodesdk.EventListResponseEventSessionErrorPropertiesError) string {
	if err.Data == nil {
		return ""
	}
	switch data := err.Data.(type) {
	case map[string]any:
		if msg, ok := data["message"].(string); ok {
			return msg
		}
		if msg, ok := data["error"].(string); ok {
			return msg
		}
	case shared.MessageAbortedError:
		return data.Data.Message
	case shared.ProviderAuthError:
		return data.Data.Message
	case shared.UnknownError:
		return data.Data.Message
	}
	return fmt.Sprint(err.Data)
}

func assistantCompleted(value any) bool {
	switch t := value.(type) {
	case opencodesdk.AssistantMessageTime:
		return t.Completed > 0
	case *opencodesdk.AssistantMessageTime:
		return t != nil && t.Completed > 0
	default:
		return false
	}
}
