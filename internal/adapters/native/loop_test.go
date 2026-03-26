package native

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

func TestRunToolLoopExecutesToolsAndRecallsLLM(t *testing.T) {
	t.Parallel()

	var toolRuns int
	session := newNativeSession(context.Background(), nativeSessionConfig{
		id:       "nsess_loop_direct",
		provider: Provider{Name: providerChatGPT, APIFormat: apiFormatOpenAI},
		client: &scriptedClient{
			steps: []scriptStep{
				func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
					if req.PreviousResponseID != "" {
						t.Fatalf("unexpected previous response on first call: %q", req.PreviousResponseID)
					}
					if got := lastText(req.Messages); got != "find loop" {
						t.Fatalf("first request text = %q", got)
					}
					req.OnDelta("tooling ")
					return &LLMResponse{
						ID:         "resp-1",
						TextBlocks: []string{"tooling "},
						ToolCalls: []ToolCall{{
							ID:        "call-1",
							Name:      "lookup",
							Arguments: mustJSONRaw(t, map[string]any{"query": "loop"}),
						}},
						StopReason: "tool_use",
					}, nil
				},
				func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
					if req.PreviousResponseID != "resp-1" {
						t.Fatalf("expected previous response id resp-1, got %q", req.PreviousResponseID)
					}
					if len(req.Messages) != 3 {
						t.Fatalf("expected user + assistant + tool result history, got %d messages", len(req.Messages))
					}
					last := req.Messages[len(req.Messages)-1]
					if len(last.Content) != 1 || last.Content[0].Type != "tool_result" || last.Content[0].Text != "lookup-result:loop" {
						t.Fatalf("unexpected tool result message: %+v", last)
					}
					req.OnDelta("done")
					return &LLMResponse{
						ID:         "resp-2",
						TextBlocks: []string{"done"},
						StopReason: "end_turn",
					}, nil
				},
			},
		},
		registry: MustNewToolRegistry(toolFromFunc(
			"lookup",
			"lookup a value",
			jsonSchemaObject(map[string]any{
				"query": map[string]any{"type": "string"},
			}, []string{"query"}, false),
			func(ctx context.Context, args json.RawMessage) (string, error) {
				toolRuns++
				var input struct {
					Query string `json:"query"`
				}
				if err := json.Unmarshal(args, &input); err != nil {
					return "", err
				}
				return "lookup-result:" + input.Query, nil
			},
		)),
	})
	defer func() { _ = session.Close() }()

	session.appendHistory(messageFromInputs([]adapterapi.Input{adapterapi.TextInput("find loop")}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.runToolLoop(ctx, "nturn-direct"); err != nil {
		t.Fatalf("runToolLoop: %v", err)
	}
	if toolRuns != 1 {
		t.Fatalf("expected one tool execution, got %d", toolRuns)
	}
	if len(session.history) != 4 {
		t.Fatalf("expected 4 history messages, got %d", len(session.history))
	}
	if session.history[1].Role != "assistant" || session.history[2].Role != "user" || session.history[3].Role != "assistant" {
		t.Fatalf("unexpected history roles: %+v", session.history)
	}

	var deltas []string
	for {
		select {
		case ev := <-session.Events():
			if ev.Kind == adapterapi.EventKindOutputDelta {
				deltas = append(deltas, ev.Text)
				continue
			}
			if ev.Kind == adapterapi.EventKindTurnCompleted {
				if got := deltas[0] + deltas[1]; got != "tooling done" {
					t.Fatalf("unexpected output deltas: %v", deltas)
				}
				return
			}
		default:
			t.Fatalf("expected turn completion event, got deltas=%v", deltas)
		}
	}
}

func TestRunToolLoopStopsOnEndTurn(t *testing.T) {
	t.Parallel()

	session := newNativeSession(context.Background(), nativeSessionConfig{
		id:       "nsess_end_turn",
		provider: Provider{Name: providerZAI, APIFormat: apiFormatAnthropic},
		client: &scriptedClient{
			steps: []scriptStep{
				func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
					if len(req.Messages) != 1 {
						t.Fatalf("expected one input message, got %d", len(req.Messages))
					}
					return &LLMResponse{
						ID:         "resp-end",
						TextBlocks: []string{"finished"},
						StopReason: "end_turn",
					}, nil
				},
			},
		},
		registry: MustNewToolRegistry(toolFromFunc("unused", "unused", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
			t.Fatal("tool should not have been executed")
			return "", nil
		})),
	})
	defer func() { _ = session.Close() }()

	session.appendHistory(messageFromInputs([]adapterapi.Input{adapterapi.TextInput("just answer")}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.runToolLoop(ctx, "nturn-end"); err != nil {
		t.Fatalf("runToolLoop: %v", err)
	}
	if len(session.history) != 2 {
		t.Fatalf("expected user + assistant history, got %d", len(session.history))
	}

	completed := drainUntil(t, ctx, session.Events(), adapterapi.EventKindTurnCompleted)
	if completed.TurnID != "nturn-end" {
		t.Fatalf("unexpected completed turn id: %q", completed.TurnID)
	}
}

func TestExecuteToolsDrainsSteersBetweenToolExecutions(t *testing.T) {
	t.Parallel()

	session := newNativeSession(context.Background(), nativeSessionConfig{
		id:       "nsess_steer_drain",
		provider: Provider{Name: providerZAI, APIFormat: apiFormatAnthropic},
		client:   &scriptedClient{},
		registry: MustNewToolRegistry(toolFromFunc("echo", "echo", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
			var input struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal(args, &input); err != nil {
				return "", err
			}
			return "echo:" + input.Value, nil
		})),
	})
	defer func() { _ = session.Close() }()

	session.steerQ <- adapterapi.SteerEvent{Message: "first steer"}

	msg1, err := session.executeTools(context.Background(), []ToolCall{{
		ID:        "call-1",
		Name:      "echo",
		Arguments: mustJSONRaw(t, map[string]any{"value": "one"}),
	}})
	if err != nil {
		t.Fatalf("executeTools first: %v", err)
	}
	if len(msg1.Content) != 2 || msg1.Content[0].Type != "text" || msg1.Content[1].Type != "tool_result" {
		t.Fatalf("unexpected first tool message: %+v", msg1)
	}
	if got := msg1.Content[0].Text; got != "[cogent:steer]\nfirst steer\n[/cogent:steer]" {
		t.Fatalf("unexpected first steer text: %q", got)
	}

	session.steerQ <- adapterapi.SteerEvent{Message: "second steer"}

	msg2, err := session.executeTools(context.Background(), []ToolCall{{
		ID:        "call-2",
		Name:      "echo",
		Arguments: mustJSONRaw(t, map[string]any{"value": "two"}),
	}})
	if err != nil {
		t.Fatalf("executeTools second: %v", err)
	}
	if len(msg2.Content) != 2 || msg2.Content[0].Text != "[cogent:steer]\nsecond steer\n[/cogent:steer]" {
		t.Fatalf("unexpected second tool message: %+v", msg2)
	}
	if leftover := session.drainSteers(); leftover != "" {
		t.Fatalf("expected steer queue to be drained, got %q", leftover)
	}
}
