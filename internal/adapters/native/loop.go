package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yusefmosiah/fase/internal/adapterapi"
)

func (s *nativeSession) runToolLoop(ctx context.Context, turnID string) error {
	for {
		response, err := s.client.Call(ctx, LLMRequest{
			System:             defaultSystemPrompt(),
			Messages:           s.snapshotHistory(),
			Tools:              s.tools,
			Stream:             true,
			PreviousResponseID: s.previousResponseID(),
			OnDelta: func(text string) {
				s.emit(adapterapi.Event{
					Kind:      adapterapi.EventKindOutputDelta,
					SessionID: s.id,
					TurnID:    turnID,
					Text:      text,
				})
			},
		})
		if err != nil {
			return err
		}

		s.recordResponseID(response.ID)
		s.appendHistory(responseToAssistantMessage(response))

		switch response.StopReason {
		case "tool_use":
			if len(response.ToolCalls) == 0 {
				return fmt.Errorf("native session: provider returned tool_use without tool calls")
			}
			toolResultMessage, err := s.executeTools(ctx, response.ToolCalls)
			if err != nil {
				return err
			}
			s.appendHistory(toolResultMessage)
		case "end_turn", "":
			s.emit(adapterapi.Event{
				Kind:      adapterapi.EventKindTurnCompleted,
				SessionID: s.id,
				TurnID:    turnID,
			})
			return nil
		case "max_tokens":
			return fmt.Errorf("native session: model stopped at max_tokens")
		default:
			return fmt.Errorf("native session: unsupported stop reason %q", response.StopReason)
		}
	}
}

func defaultSystemPrompt() string {
	return "You are the FASE native coding agent. Help with software tasks, use tools when needed, and keep responses concise and actionable."
}

func (s *nativeSession) executeTools(ctx context.Context, calls []ToolCall) (Message, error) {
	message := Message{Role: "user"}

	for _, call := range calls {
		output, err := s.registry.Execute(ctx, call.Name, call.Arguments)
		if err != nil {
			output = fmt.Sprintf("tool_error: %v", err)
		}
		message.Content = append(message.Content, ContentBlock{
			Type:      "tool_result",
			ToolUseID: call.ID,
			Text:      output,
		})
	}
	if steerText := s.drainSteers(); steerText != "" {
		message.Content = append([]ContentBlock{{
			Type: "text",
			Text: steerText,
		}}, message.Content...)
	}
	return message, nil
}

func (s *nativeSession) drainSteers() string {
	var blocks []string
	for {
		select {
		case steer := <-s.steerQ:
			if msg := strings.TrimSpace(steer.Message); msg != "" {
				blocks = append(blocks, fmt.Sprintf("[fase:steer]\n%s\n[/fase:steer]", msg))
			}
		default:
			return strings.Join(blocks, "\n\n")
		}
	}
}

func (s *nativeSession) snapshotHistory() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, len(s.history))
	copy(out, s.history)
	return out
}

func (s *nativeSession) appendHistory(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, msg)
}

func (s *nativeSession) previousResponseID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider.APIFormat != apiFormatOpenAI {
		return ""
	}
	return s.previousID
}

func (s *nativeSession) recordResponseID(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.previousID = id
}

func responseToAssistantMessage(response *LLMResponse) Message {
	msg := Message{
		Role:    "assistant",
		Content: make([]ContentBlock, 0, len(response.ThinkingBlocks)+len(response.TextBlocks)+len(response.ToolCalls)),
	}
	// Thinking blocks must come first and be preserved for multi-turn context.
	for _, thinking := range response.ThinkingBlocks {
		if thinking == "" {
			continue
		}
		msg.Content = append(msg.Content, ContentBlock{
			Type: "thinking",
			Text: thinking,
		})
	}
	for _, text := range response.TextBlocks {
		if text == "" {
			continue
		}
		msg.Content = append(msg.Content, ContentBlock{
			Type: "text",
			Text: text,
		})
	}
	for _, call := range response.ToolCalls {
		msg.Content = append(msg.Content, ContentBlock{
			Type:  "tool_use",
			ID:    call.ID,
			Name:  call.Name,
			Input: json.RawMessage(call.Arguments),
		})
	}
	return msg
}
