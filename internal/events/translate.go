package events

import (
	"encoding/json"
	"strings"
)

type Hint struct {
	Kind            string         `json:"kind"`
	Phase           string         `json:"phase"`
	NativeSessionID string         `json:"native_session_id,omitempty"`
	Payload         map[string]any `json:"payload"`
}

func TranslateLine(adapter, stream, line string) []Hint {
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return nil
	}

	var hints []Hint

	if nativeID := firstString(payload, "session_id", "conversation_id", "thread_id"); nativeID != "" {
		hints = append(hints, Hint{
			Kind:            "session.discovered",
			Phase:           "translation",
			NativeSessionID: nativeID,
			Payload: map[string]any{
				"adapter":           adapter,
				"native_session_id": nativeID,
				"stream":            stream,
			},
		})
	}

	if delta := extractDelta(payload); delta != "" {
		hints = append(hints, Hint{
			Kind:  "assistant.delta",
			Phase: "translation",
			Payload: map[string]any{
				"text":   delta,
				"stream": stream,
			},
		})
	}

	if message := extractAssistantMessage(payload); message != "" {
		hints = append(hints, Hint{
			Kind:  "assistant.message",
			Phase: "translation",
			Payload: map[string]any{
				"text":   message,
				"stream": stream,
			},
		})
	}

	eventType := strings.ToLower(firstString(payload, "type", "event"))
	switch {
	case strings.Contains(eventType, "tool_call"), strings.Contains(eventType, "tool_use"):
		hints = append(hints, Hint{
			Kind:    "tool.call",
			Phase:   "execution",
			Payload: payload,
		})
	case strings.Contains(eventType, "tool_result"), strings.Contains(eventType, "tool_response"):
		hints = append(hints, Hint{
			Kind:    "tool.result",
			Phase:   "execution",
			Payload: payload,
		})
	case strings.Contains(eventType, "error"):
		hints = append(hints, Hint{
			Kind:    "diagnostic",
			Phase:   "translation",
			Payload: payload,
		})
	}

	return hints
}

func firstString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func extractDelta(payload map[string]any) string {
	if value := firstString(payload, "delta", "text_delta", "content_delta"); value != "" {
		return value
	}

	if delta, ok := payload["delta"].(map[string]any); ok {
		return firstString(delta, "text", "content")
	}

	return ""
}

func extractAssistantMessage(payload map[string]any) string {
	role := strings.ToLower(firstString(payload, "role"))
	if role == "assistant" {
		if value := firstString(payload, "content", "text", "message"); value != "" {
			return value
		}
	}

	if message, ok := payload["message"].(map[string]any); ok {
		if strings.ToLower(firstString(message, "role")) == "assistant" {
			if content := extractContent(message["content"]); content != "" {
				return content
			}
			if value := firstString(message, "text", "message"); value != "" {
				return value
			}
		}
	}

	if strings.Contains(strings.ToLower(firstString(payload, "type")), "assistant") {
		if value := firstString(payload, "content", "text", "message"); value != "" {
			return value
		}
		if content := extractContent(payload["content"]); content != "" {
			return content
		}
	}

	return ""
}

func extractContent(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var parts []string
		for _, item := range typed {
			if block, ok := item.(map[string]any); ok {
				if text := firstString(block, "text", "content"); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		return firstString(typed, "text", "content")
	default:
		return ""
	}
}
