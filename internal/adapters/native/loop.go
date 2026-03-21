package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/yusefmosiah/fase/internal/adapterapi"
)

func (s *nativeSession) runToolLoop(ctx context.Context, turnID string) error {
	for {
		response, err := s.client.Call(ctx, LLMRequest{
			System:             s.systemPrompt(),
			Messages:           s.snapshotHistory(),
			Tools:              s.activeTools(),
			Stream:             !s.provider.ForceNoStream,
			ReasoningEffort:    s.reasoningEffort,
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
		case "model_context_window_exceeded":
			return fmt.Errorf("native session: context window exceeded — prompt or history too large for this model")
		default:
			return fmt.Errorf("native session: unsupported stop reason %q", response.StopReason)
		}
	}
}

func defaultSystemPrompt() string {
	return "You are the FASE native coding agent. Help with software tasks, use tools when needed, and keep responses concise and actionable."
}

func (s *nativeSession) systemPrompt() string {
	catalog := s.registry.Catalog()
	return defaultSystemPrompt() + "\n\n" + catalog + "\nCall any tool by name. Tool schemas are loaded automatically on first use."
}

// activeTools returns only the tool schemas for tools that have been used.
// On the first call (no tools activated yet), returns an empty list.
// The LLM discovers available tools via the catalog in the system prompt.
func (s *nativeSession) activeTools() []ToolDef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ToolDef{}, s.tools...)
}

// activateTool adds a tool's full schema to the active set if not already present.
func (s *nativeSession) activateTool(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tools {
		if t.Name == name {
			return // already active
		}
	}
	tool, ok := s.registry.Lookup(name)
	if !ok {
		return
	}
	s.tools = append(s.tools, tool.Definition())
}

func (s *nativeSession) executeTools(ctx context.Context, calls []ToolCall) (Message, error) {
	message := Message{Role: "user"}

	// Execute tool calls in parallel — results collected in order.
	type toolResult struct {
		callID string
		output string
	}
	results := make([]toolResult, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		s.activateTool(call.Name)
		wg.Add(1)
		go func(idx int, c ToolCall) {
			defer wg.Done()
			output, err := s.registry.Execute(ctx, c.Name, c.Arguments)
			if err != nil {
				output = fmt.Sprintf("tool_error: %v", err)
			}
			results[idx] = toolResult{callID: c.ID, output: output}
		}(i, call)
	}
	wg.Wait()

	// Check for consecutive errors.
	allErrors := true
	for _, r := range results {
		if !strings.HasPrefix(r.output, "tool_error:") {
			allErrors = false
			break
		}
	}
	if allErrors {
		s.toolErrors += len(results)
	} else {
		s.toolErrors = 0
	}

	for _, r := range results {
		output := r.output
		if s.toolErrors >= 5 && strings.HasPrefix(output, "tool_error:") {
			output += fmt.Sprintf("\n\nWARNING: %d consecutive tool errors. This tool may be unavailable. Stop retrying and work with what you have.", s.toolErrors)
		}
		message.Content = append(message.Content, ContentBlock{
			Type:      "tool_result",
			ToolUseID: r.callID,
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
	// The signature field carries encrypted full thinking for continuity.
	for _, tb := range response.ThinkingBlocks {
		if tb.Text == "" && tb.Signature == "" {
			continue
		}
		msg.Content = append(msg.Content, ContentBlock{
			Type:      "thinking",
			Text:      tb.Text,
			Signature: tb.Signature,
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
