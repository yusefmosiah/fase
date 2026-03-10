package events

import (
	"encoding/json"
	"strconv"
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

	if nativeID := extractNativeSessionID(payload); nativeID != "" {
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

	hints = append(hints, extractToolHints(payload)...)
	if usage := extractUsageHint(payload, stream); usage != nil {
		hints = append(hints, *usage)
	}

	eventType := strings.ToLower(firstString(payload, "type", "event"))
	if strings.Contains(eventType, "error") {
		hints = append(hints, Hint{
			Kind:    "diagnostic",
			Phase:   "translation",
			Payload: payload,
		})
	}

	return hints
}

func extractNativeSessionID(payload map[string]any) string {
	eventType := strings.ToLower(firstString(payload, "type", "event"))
	if eventType == "result" {
		return ""
	}

	if nativeID := firstString(payload, "session_id", "conversation_id", "thread_id"); nativeID != "" {
		return nativeID
	}

	if nativeID := firstString(payload, "sessionID"); nativeID != "" {
		if eventType == "step_start" || eventType == "step-start" || eventType == "init" {
			return nativeID
		}
	}

	if strings.ToLower(firstString(payload, "type")) == "session" {
		return firstString(payload, "id")
	}

	return ""
}

func extractToolHints(payload map[string]any) []Hint {
	var hints []Hint

	eventType := strings.ToLower(firstString(payload, "type", "event"))
	switch {
	case strings.Contains(eventType, "tool_call"), strings.Contains(eventType, "tool_use"):
		hints = append(hints, Hint{Kind: "tool.call", Phase: "execution", Payload: payload})
	case strings.Contains(eventType, "tool_result"), strings.Contains(eventType, "tool_response"):
		hints = append(hints, Hint{Kind: "tool.result", Phase: "execution", Payload: payload})
	}

	if item, ok := payload["item"].(map[string]any); ok {
		itemType := strings.ToLower(firstString(item, "type"))
		switch itemType {
		case "command_execution", "web_search", "collab_tool_call", "tool_call", "tool_use":
			hints = append(hints, Hint{Kind: "tool.call", Phase: "execution", Payload: item})
		case "command_execution_result", "web_search_result", "collab_tool_result", "tool_result", "tool_response":
			hints = append(hints, Hint{Kind: "tool.result", Phase: "execution", Payload: item})
		}
	}

	if message, ok := payload["message"].(map[string]any); ok {
		hints = append(hints, extractToolHintsFromContent(message["content"])...)
	}
	hints = append(hints, extractToolHintsFromContent(payload["content"])...)

	return hints
}

func extractToolHintsFromContent(value any) []Hint {
	blocks, ok := value.([]any)
	if !ok {
		return nil
	}

	var hints []Hint
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blockType := strings.ToLower(firstString(block, "type"))
		switch {
		case strings.Contains(blockType, "tool_use"):
			hints = append(hints, Hint{Kind: "tool.call", Phase: "execution", Payload: block})
		case strings.Contains(blockType, "tool_result"):
			hints = append(hints, Hint{Kind: "tool.result", Phase: "execution", Payload: block})
		}
	}

	return hints
}

func extractUsageHint(payload map[string]any, stream string) *Hint {
	usage := usagePayload(payload)
	if len(usage) == 0 {
		return nil
	}
	usage["stream"] = stream
	return &Hint{
		Kind:    "usage.reported",
		Phase:   "translation",
		Payload: usage,
	}
}

func usagePayload(payload map[string]any) map[string]any {
	result := map[string]any{}
	appendUsageFields(result, payload)

	if usage, ok := payload["usage"].(map[string]any); ok {
		appendUsageFields(result, usage)
	}
	if usage, ok := payload["usageMetadata"].(map[string]any); ok {
		appendUsageFields(result, usage)
	}
	if usage, ok := payload["tokenUsage"].(map[string]any); ok {
		appendUsageFields(result, usage)
	}
	if message, ok := payload["message"].(map[string]any); ok {
		appendUsageFields(result, message)
		if usage, ok := message["usage"].(map[string]any); ok {
			appendUsageFields(result, usage)
		}
	}
	if completion, ok := payload["completion"].(map[string]any); ok {
		appendUsageFields(result, completion)
		if usage, ok := completion["usage"].(map[string]any); ok {
			appendUsageFields(result, usage)
		}
	}
	if modelUsage, ok := payload["modelUsage"].(map[string]any); ok {
		for model, value := range modelUsage {
			entry, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if result["model"] == nil {
				result["model"] = model
			}
			appendUsageFields(result, entry)
			if cost, ok := number(entry["costUSD"]); ok {
				result["cost_usd"] = cost
			}
			break
		}
	}

	if len(result) == 0 {
		return nil
	}
	if _, ok := result["cost_usd"]; !ok {
		metrics := []string{
			"input_tokens",
			"output_tokens",
			"total_tokens",
			"cached_input_tokens",
			"cache_read_input_tokens",
			"cache_creation_input_tokens",
		}
		hasMetric := false
		for _, key := range metrics {
			if _, ok := result[key]; ok {
				hasMetric = true
				break
			}
		}
		if !hasMetric {
			return nil
		}
	}
	if _, ok := result["total_tokens"]; !ok {
		input, _ := intValue(result["input_tokens"])
		output, _ := intValue(result["output_tokens"])
		cached, _ := intValue(result["cached_input_tokens"])
		cacheRead, _ := intValue(result["cache_read_input_tokens"])
		cacheCreate, _ := intValue(result["cache_creation_input_tokens"])
		total := input + output + cached + cacheRead + cacheCreate
		if total > 0 {
			result["total_tokens"] = total
		}
	}
	return result
}

func appendUsageFields(dst map[string]any, src map[string]any) {
	copyIntField(dst, src, "input_tokens", "input_tokens", "inputTokens", "prompt_token_count")
	copyIntField(dst, src, "output_tokens", "output_tokens", "outputTokens", "candidates_token_count")
	copyIntField(dst, src, "total_tokens", "total_tokens", "totalTokens", "total_token_count")
	copyIntField(dst, src, "cached_input_tokens", "cached_input_tokens", "cachedInputTokens")
	copyIntField(dst, src, "cache_read_input_tokens", "cache_read_input_tokens", "cacheReadInputTokens")
	copyIntField(dst, src, "cache_creation_input_tokens", "cache_creation_input_tokens", "cacheCreationInputTokens")
	copyIntField(dst, src, "cache_read_input_tokens", "cache_read_input_tokens", "cache_read_input_tokens")
	copyIntField(dst, src, "cache_creation_input_tokens", "cache_creation_input_tokens", "cache_creation_input_tokens")
	copyFloatField(dst, src, "cost_usd", "total_cost_usd", "costUSD")
	copyStringField(dst, src, "model", "model")
	copyStringField(dst, src, "provider", "provider")
}

func copyIntField(dst, src map[string]any, target string, keys ...string) {
	if _, ok := dst[target]; ok {
		return
	}
	for _, key := range keys {
		if value, ok := intValue(src[key]); ok {
			dst[target] = value
			return
		}
	}
}

func copyFloatField(dst, src map[string]any, target string, keys ...string) {
	if _, ok := dst[target]; ok {
		return
	}
	for _, key := range keys {
		if value, ok := number(src[key]); ok {
			dst[target] = value
			return
		}
	}
}

func copyStringField(dst, src map[string]any, target string, keys ...string) {
	if _, ok := dst[target]; ok {
		return
	}
	for _, key := range keys {
		if value, ok := src[key].(string); ok && value != "" {
			dst[target] = value
			return
		}
	}
}

func intValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
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

	if event, ok := payload["assistantMessageEvent"].(map[string]any); ok {
		if value := firstString(event, "delta", "content"); value != "" {
			return value
		}
	}

	if delta, ok := payload["delta"].(map[string]any); ok {
		return firstString(delta, "text", "content")
	}

	return ""
}

func extractAssistantMessage(payload map[string]any) string {
	if item, ok := payload["item"].(map[string]any); ok {
		if strings.ToLower(firstString(item, "type")) == "agent_message" {
			if value := firstString(item, "text", "content", "message"); value != "" {
				return value
			}
		}
	}

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

	if strings.ToLower(firstString(payload, "type")) == "result" {
		if value := firstString(payload, "result"); value != "" {
			return value
		}
	}

	if completion, ok := payload["completion"].(map[string]any); ok {
		if value := firstString(completion, "finalText", "final_text", "text"); value != "" {
			return value
		}
	}

	if value := firstString(payload, "final_text", "finalText"); value != "" {
		return value
	}

	if strings.ToLower(firstString(payload, "type")) == "text" {
		if part, ok := payload["part"].(map[string]any); ok {
			if value := firstString(part, "text"); value != "" {
				return value
			}
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
