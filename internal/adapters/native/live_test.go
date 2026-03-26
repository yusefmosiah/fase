package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

func TestLiveAdapterStartSession(t *testing.T) {
	t.Parallel()

	adapter := NewLiveAdapter(nil, nil)
	adapter.newClientFn = func(provider Provider, httpClient HTTPDoer) (LLMClient, error) {
		return &scriptedClient{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := adapter.StartSession(ctx, adapterapi.StartSessionRequest{
		CWD:   t.TempDir(),
		Model: "zai/glm-4.7",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if session.SessionID() == "" {
		t.Fatal("expected non-empty session ID")
	}

	ev := drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)
	if ev.SessionID != session.SessionID() {
		t.Fatalf("session ID mismatch: got %q want %q", ev.SessionID, session.SessionID())
	}
}

func TestLiveAdapterResumeSession(t *testing.T) {
	t.Parallel()

	adapter := NewLiveAdapter(nil, nil)
	adapter.newClientFn = func(provider Provider, httpClient HTTPDoer) (LLMClient, error) {
		return &scriptedClient{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := adapter.ResumeSession(ctx, "nsess_resume", adapterapi.StartSessionRequest{
		CWD:   t.TempDir(),
		Model: "chatgpt/gpt-5.4-mini",
	})
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	ev := drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionResumed)
	if ev.SessionID != "nsess_resume" {
		t.Fatalf("unexpected session ID: %q", ev.SessionID)
	}
}

func TestLiveSessionToolLoopCompletesTurn(t *testing.T) {
	t.Parallel()

	registry := MustNewToolRegistry(toolFromFunc(
		"lookup",
		"lookup a value",
		jsonSchemaObject(map[string]any{
			"query": map[string]any{"type": "string"},
		}, []string{"query"}, false),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var input struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(args, &input); err != nil {
				return "", err
			}
			return "lookup-result:" + input.Query, nil
		},
	))

	client := &scriptedClient{
		steps: []scriptStep{
			func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
				if got := lastText(req.Messages); got != "find phase3" {
					t.Fatalf("first request text = %q", got)
				}
				req.OnDelta("thinking ")
				return &LLMResponse{
					ID:         "resp-1",
					TextBlocks: []string{"thinking "},
					ToolCalls: []ToolCall{{
						ID:        "call-1",
						Name:      "lookup",
						Arguments: mustJSONRaw(t, map[string]any{"query": "phase3"}),
					}},
					StopReason: "tool_use",
				}, nil
			},
			func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
				if req.PreviousResponseID != "resp-1" {
					t.Fatalf("expected previous response id resp-1, got %q", req.PreviousResponseID)
				}
				if len(req.Messages) != 3 {
					t.Fatalf("expected 3 messages after tool result, got %d", len(req.Messages))
				}
				last := req.Messages[len(req.Messages)-1]
				if last.Role != "user" || len(last.Content) != 1 || last.Content[0].Type != "tool_result" {
					t.Fatalf("unexpected tool result message: %+v", last)
				}
				if last.Content[0].Text != "lookup-result:phase3" {
					t.Fatalf("unexpected tool output: %q", last.Content[0].Text)
				}
				req.OnDelta("done")
				return &LLMResponse{
					ID:         "resp-2",
					TextBlocks: []string{"done"},
					StopReason: "end_turn",
				}, nil
			},
		},
	}

	session := newNativeSession(context.Background(), nativeSessionConfig{
		id:       "nsess_tool_loop",
		provider: Provider{Name: providerChatGPT, APIFormat: apiFormatOpenAI},
		client:   client,
		registry: registry,
	})
	defer func() { _ = session.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)

	turnID, err := session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput("find phase3")})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}

	var deltas []string
	for {
		ev := nextEvent(t, ctx, session.Events())
		switch ev.Kind {
		case adapterapi.EventKindOutputDelta:
			deltas = append(deltas, ev.Text)
		case adapterapi.EventKindTurnCompleted:
			if ev.TurnID != turnID {
				t.Fatalf("turn ID mismatch: got %q want %q", ev.TurnID, turnID)
			}
			if got := fmt.Sprint(deltas); got != "[thinking  done]" {
				t.Fatalf("unexpected deltas: %v", deltas)
			}
			if session.ActiveTurnID() != "" {
				t.Fatalf("expected no active turn after completion, got %q", session.ActiveTurnID())
			}
			return
		case adapterapi.EventKindTurnFailed:
			t.Fatalf("unexpected failure: %s", ev.Text)
		}
	}
}

func TestLiveSessionSteeringPrependsNextRequest(t *testing.T) {
	t.Parallel()

	toolStarted := make(chan struct{})
	releaseTool := make(chan struct{})

	registry := MustNewToolRegistry(toolFromFunc(
		"wait_tool",
		"wait for a signal",
		nil,
		func(ctx context.Context, args json.RawMessage) (string, error) {
			close(toolStarted)
			select {
			case <-releaseTool:
				return "tool-finished", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	))

	client := &scriptedClient{
		steps: []scriptStep{
			func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
				return &LLMResponse{
					ID: "resp-1",
					ToolCalls: []ToolCall{{
						ID:   "call-1",
						Name: "wait_tool",
					}},
					StopReason: "tool_use",
				}, nil
			},
			func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
				last := req.Messages[len(req.Messages)-1]
				if len(last.Content) != 2 {
					t.Fatalf("expected steer text + tool_result, got %+v", last.Content)
				}
				if last.Content[0].Type != "text" {
					t.Fatalf("expected steer text block, got %+v", last.Content[0])
				}
				if want := "[fase:steer]\nSay exactly: STEERED\n[/fase:steer]"; last.Content[0].Text != want {
					t.Fatalf("unexpected steer block: %q", last.Content[0].Text)
				}
				if last.Content[1].Type != "tool_result" || last.Content[1].Text != "tool-finished" {
					t.Fatalf("unexpected tool result block: %+v", last.Content[1])
				}
				return &LLMResponse{ID: "resp-2", StopReason: "end_turn"}, nil
			},
		},
	}

	session := newNativeSession(context.Background(), nativeSessionConfig{
		id:       "nsess_steer",
		provider: Provider{Name: providerZAI, APIFormat: apiFormatAnthropic},
		client:   client,
		registry: registry,
	})
	defer func() { _ = session.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)

	turnID, err := session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput("wait")})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}

	select {
	case <-toolStarted:
	case <-ctx.Done():
		t.Fatal("timeout waiting for tool execution")
	}

	if err := session.Steer(ctx, turnID, []adapterapi.Input{adapterapi.TextInput("Say exactly: STEERED")}); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	close(releaseTool)

	for {
		ev := nextEvent(t, ctx, session.Events())
		switch ev.Kind {
		case adapterapi.EventKindTurnCompleted:
			if ev.TurnID != turnID {
				t.Fatalf("turn ID mismatch: got %q want %q", ev.TurnID, turnID)
			}
			return
		case adapterapi.EventKindTurnFailed:
			t.Fatalf("unexpected failure: %s", ev.Text)
		}
	}
}

func TestLiveSessionInterrupt(t *testing.T) {
	t.Parallel()

	client := &scriptedClient{
		steps: []scriptStep{
			func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}

	session := newNativeSession(context.Background(), nativeSessionConfig{
		id:       "nsess_interrupt",
		provider: Provider{Name: providerZAI, APIFormat: apiFormatAnthropic},
		client:   client,
		registry: MustNewToolRegistry(),
	})
	defer func() { _ = session.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)

	turnID, err := session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput("interrupt me")})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if err := session.Interrupt(ctx); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	for {
		ev := nextEvent(t, ctx, session.Events())
		if ev.Kind == adapterapi.EventKindTurnInterrupted {
			if ev.TurnID != turnID {
				t.Fatalf("turn ID mismatch: got %q want %q", ev.TurnID, turnID)
			}
			return
		}
		if ev.Kind == adapterapi.EventKindTurnFailed {
			t.Fatalf("unexpected turn failure: %s", ev.Text)
		}
	}
}

func TestLiveSessionOnlyOneActiveTurn(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	client := &scriptedClient{
		steps: []scriptStep{
			func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
				<-block
				return &LLMResponse{StopReason: "end_turn"}, nil
			},
		},
	}

	session := newNativeSession(context.Background(), nativeSessionConfig{
		id:       "nsess_single_turn",
		provider: Provider{Name: providerZAI, APIFormat: apiFormatAnthropic},
		client:   client,
		registry: MustNewToolRegistry(),
	})
	defer func() { _ = session.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)

	if _, err := session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput("first")}); err != nil {
		t.Fatalf("StartTurn first: %v", err)
	}
	if _, err := session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput("second")}); err == nil {
		t.Fatal("expected second StartTurn to fail while first turn is active")
	}

	close(block)
	drainUntil(t, ctx, session.Events(), adapterapi.EventKindTurnCompleted)
}

func TestLiveSessionMaxTokensFailsTurn(t *testing.T) {
	t.Parallel()

	session := newNativeSession(context.Background(), nativeSessionConfig{
		id:       "nsess_max_tokens",
		provider: Provider{Name: providerZAI, APIFormat: apiFormatAnthropic},
		client: &scriptedClient{
			steps: []scriptStep{
				func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
					return &LLMResponse{StopReason: "max_tokens"}, nil
				},
			},
		},
		registry: MustNewToolRegistry(),
	})
	defer func() { _ = session.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)

	if _, err := session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput("overflow")}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}

	ev := drainUntil(t, ctx, session.Events(), adapterapi.EventKindTurnFailed)
	if ev.Text == "" {
		t.Fatal("expected max_tokens failure message")
	}
}

func TestLiveAdapterNativeCoAgentSession(t *testing.T) {
	t.Parallel()

	adapter := NewLiveAdapter(nil, nil)

	var mu sync.Mutex
	clients := []LLMClient{
		&scriptedClient{
			steps: []scriptStep{
				func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
					if got := lastText(req.Messages); got != "delegate work" {
						t.Fatalf("unexpected parent prompt: %q", got)
					}
					return &LLMResponse{
						ID: "parent-1",
						ToolCalls: []ToolCall{{
							ID:   "call-spawn",
							Name: "spawn_session",
							Arguments: mustJSONRaw(t, map[string]any{
								"adapter": "native",
								"model":   "zai/glm-4.7",
							}),
						}},
						StopReason: "tool_use",
					}, nil
				},
				func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
					sessionID := toolResultJSONField(t, req.Messages[len(req.Messages)-1], "session_id")
					if sessionID == "" {
						t.Fatalf("expected spawned session_id in tool result: %+v", req.Messages[len(req.Messages)-1])
					}
					return &LLMResponse{
						ID: "parent-2",
						ToolCalls: []ToolCall{{
							ID:   "call-send",
							Name: "send_turn",
							Arguments: mustJSONRaw(t, map[string]any{
								"session_id": sessionID,
								"input":      "child, say delegated result",
							}),
						}},
						StopReason: "tool_use",
					}, nil
				},
				func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
					last := req.Messages[len(req.Messages)-1]
					if len(last.Content) != 1 || last.Content[0].Type != "tool_result" {
						t.Fatalf("unexpected send_turn result message: %+v", last)
					}
					if !strings.Contains(last.Content[0].Text, "delegated result") {
						t.Fatalf("expected delegated result in tool output: %s", last.Content[0].Text)
					}
					req.OnDelta("parent received delegated result")
					return &LLMResponse{
						ID:         "parent-3",
						TextBlocks: []string{"parent received delegated result"},
						StopReason: "end_turn",
					}, nil
				},
			},
		},
		&scriptedClient{
			steps: []scriptStep{
				func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
					if got := lastText(req.Messages); got != "child, say delegated result" {
						t.Fatalf("unexpected child prompt: %q", got)
					}
					req.OnDelta("delegated result")
					return &LLMResponse{
						ID:         "child-1",
						TextBlocks: []string{"delegated result"},
						StopReason: "end_turn",
					}, nil
				},
			},
		},
	}
	adapter.newClientFn = func(provider Provider, httpClient HTTPDoer) (LLMClient, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(clients) == 0 {
			return nil, errors.New("unexpected client creation")
		}
		client := clients[0]
		clients = clients[1:]
		return client, nil
	}
	adapter.SetCoAgents(map[string]adapterapi.LiveAgentAdapter{"native": adapter})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := adapter.StartSession(ctx, adapterapi.StartSessionRequest{
		CWD:   t.TempDir(),
		Model: "zai/glm-4.7",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)

	turnID, err := session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput("delegate work")})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}

	var deltas []string
	for {
		ev := nextEvent(t, ctx, session.Events())
		switch ev.Kind {
		case adapterapi.EventKindOutputDelta:
			deltas = append(deltas, ev.Text)
		case adapterapi.EventKindTurnCompleted:
			if ev.TurnID != turnID {
				t.Fatalf("unexpected turn ID: got %q want %q", ev.TurnID, turnID)
			}
			if strings.Join(deltas, "") != "parent received delegated result" {
				t.Fatalf("unexpected parent deltas: %v", deltas)
			}
			return
		case adapterapi.EventKindTurnFailed:
			t.Fatalf("unexpected failure: %s", ev.Text)
		}
	}
}

type scriptStep func(ctx context.Context, req LLMRequest) (*LLMResponse, error)

type scriptedClient struct {
	mu    sync.Mutex
	steps []scriptStep
	calls int
}

func (c *scriptedClient) Call(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	c.mu.Lock()
	callIndex := c.calls
	c.calls++
	var step scriptStep
	if callIndex < len(c.steps) {
		step = c.steps[callIndex]
	}
	c.mu.Unlock()

	if step == nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			return nil, errors.New("unexpected client call")
		}
	}
	return step(ctx, req)
}

func drainUntil(t *testing.T, ctx context.Context, ch <-chan adapterapi.Event, kind adapterapi.EventKind) adapterapi.Event {
	t.Helper()
	for {
		ev := nextEvent(t, ctx, ch)
		if ev.Kind == kind {
			return ev
		}
	}
}

func nextEvent(t *testing.T, ctx context.Context, ch <-chan adapterapi.Event) adapterapi.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("event channel closed unexpectedly")
		}
		return ev
	case <-ctx.Done():
		t.Fatal("timeout waiting for event")
		return adapterapi.Event{}
	}
}

func mustJSONRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return data
}

func toolResultJSONField(t *testing.T, msg Message, key string) string {
	t.Helper()
	for _, block := range msg.Content {
		if block.Type != "tool_result" || strings.TrimSpace(block.Text) == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(block.Text), &payload); err != nil {
			t.Fatalf("decode tool result JSON: %v", err)
		}
		if value, _ := payload[key].(string); value != "" {
			return value
		}
	}
	return ""
}

func lastText(messages []Message) string {
	if len(messages) == 0 {
		return ""
	}
	last := messages[len(messages)-1]
	for _, block := range last.Content {
		if block.Type == "text" {
			return block.Text
		}
	}
	return ""
}
