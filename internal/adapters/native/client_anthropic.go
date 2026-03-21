package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type anthropicClient struct {
	provider Provider
	http     HTTPDoer
}

func NewAnthropicClient(provider Provider, httpClient HTTPDoer) (LLMClient, error) {
	return &anthropicClient{provider: provider, http: newHTTPClient(httpClient)}, nil
}

type anthropicRequest struct {
	Model            string              `json:"model,omitempty"`
	System           any                 `json:"system,omitempty"`
	Messages         []anthropicMessage  `json:"messages,omitempty"`
	Tools            []anthropicTool     `json:"tools,omitempty"`
	MaxTokens        int                 `json:"max_tokens"`
	Stream           bool                `json:"stream,omitempty"`
	AnthropicVersion string              `json:"anthropic_version,omitempty"`
	Thinking         *anthropicThinking  `json:"thinking,omitempty"`
	OutputConfig     *anthropicOutputCfg `json:"output_config,omitempty"`
}

type anthropicThinking struct {
	Type string `json:"type"` // "adaptive"
}

type anthropicOutputCfg struct {
	Effort string `json:"effort"` // "low", "medium", "high", "max"
}

type anthropicMessage struct {
	Role    string                `json:"role"`
	Content []anthropicContentAny `json:"content"`
}

type anthropicContentAny map[string]any

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

type anthropicResponse struct {
	ID         string                   `json:"id"`
	Content    []anthropicResponseBlock `json:"content"`
	StopReason string                   `json:"stop_reason"`
	Usage      anthropicUsage           `json:"usage"`
}

type anthropicResponseBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (c *anthropicClient) Call(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	// Bedrock uses binary EventStream for streaming, not SSE.
	// Force non-streaming and use the /invoke endpoint instead.
	useStream := req.Stream && !c.provider.ForceNoStream

	endpoint, err := c.provider.anthropicEndpoint(useStream)
	if err != nil {
		return nil, err
	}
	var systemPrompt any = req.System
	// Bedrock: wrap system prompt with cache_control for prompt caching.
	if c.provider.ModelInPath && req.System != "" {
		systemPrompt = []map[string]any{{
			"type":          "text",
			"text":          req.System,
			"cache_control": map[string]string{"type": "ephemeral"},
		}}
	}
	payload := anthropicRequest{
		System:           systemPrompt,
		Messages:         anthropicMessages(req.Messages),
		Tools:            anthropicTools(req.Tools),
		MaxTokens:        65536,
		AnthropicVersion: c.provider.AnthropicVersion,
	}
	if req.ReasoningEffort != "" {
		payload.Thinking = &anthropicThinking{Type: "adaptive"}
		payload.OutputConfig = &anthropicOutputCfg{Effort: req.ReasoningEffort}
	}
	// Bedrock: model goes in URL path, not body. Stream flag not in body.
	if !c.provider.ModelInPath {
		payload.Model = c.provider.ModelID
		payload.Stream = useStream
	}
	httpReq, err := newJSONRequest(ctx, http.MethodPost, endpoint, payload)
	if err != nil {
		return nil, err
	}
	authHeader, err := c.provider.authHeader(ctx)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", authHeader)
	httpReq.Header.Set("Accept", "application/json")
	if useStream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic call: status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	if useStream {
		return parseAnthropicStream(resp, req.OnDelta)
	}
	return parseAnthropicResponse(resp)
}

func anthropicMessages(msgs []Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))
	for _, msg := range msgs {
		item := anthropicMessage{Role: msg.Role, Content: make([]anthropicContentAny, 0, len(msg.Content))}
		for _, block := range msg.Content {
			switch block.Type {
			case "thinking":
				item.Content = append(item.Content, anthropicContentAny{
					"type":     "thinking",
					"thinking": block.Text,
				})
			case "text":
				item.Content = append(item.Content, anthropicContentAny{
					"type": "text",
					"text": block.Text,
				})
			case "tool_use":
				item.Content = append(item.Content, anthropicContentAny{
					"type":  "tool_use",
					"id":    block.ID,
					"name":  block.Name,
					"input": json.RawMessage(canonicalJSON(block.Input)),
				})
			case "tool_result":
				item.Content = append(item.Content, anthropicContentAny{
					"type":        "tool_result",
					"tool_use_id": block.ToolUseID,
					"content":     block.Text,
				})
			}
		}
		out = append(out, item)
	}
	return out
}

func anthropicTools(tools []ToolDef) []anthropicTool {
	out := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, anthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.Parameters,
		})
	}
	return out
}

func parseAnthropicResponse(resp *http.Response) (*LLMResponse, error) {
	var payload anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}
	result := &LLMResponse{
		ID:         payload.ID,
		StopReason: payload.StopReason,
		Usage: Usage{
			InputTokens:  payload.Usage.InputTokens,
			OutputTokens: payload.Usage.OutputTokens,
			TotalTokens:  payload.Usage.InputTokens + payload.Usage.OutputTokens,
		},
	}
	for _, block := range payload.Content {
		switch block.Type {
		case "text":
			result.TextBlocks = append(result.TextBlocks, block.Text)
		case "thinking":
			result.ThinkingBlocks = append(result.ThinkingBlocks, block.Text)
		case "tool_use":
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: canonicalJSON(block.Input),
			})
		}
	}
	return result, nil
}

func parseAnthropicStream(resp *http.Response, onDelta func(string)) (*LLMResponse, error) {
	result := &LLMResponse{}
	type toolBuffer struct {
		id   string
		name string
		args string
	}
	tools := map[int]*toolBuffer{}
	thinkingBlocks := map[int]*strings.Builder{}
	if err := readSSE(resp, func(evt sseEvent) error {
		if len(evt.Data) == 0 || string(evt.Data) == "[DONE]" {
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal(evt.Data, &payload); err != nil {
			return nil
		}
		typ, _ := payload["type"].(string)
		switch typ {
		case "message_start":
			if msg, ok := payload["message"].(map[string]any); ok {
				if id, _ := msg["id"].(string); id != "" {
					result.ID = id
				}
				if usage, ok := msg["usage"].(map[string]any); ok {
					result.Usage.InputTokens = int(numberValue(usage["input_tokens"]))
					result.Usage.OutputTokens = int(numberValue(usage["output_tokens"]))
				}
			}
		case "content_block_start":
			index := int(numberValue(payload["index"]))
			block, _ := payload["content_block"].(map[string]any)
			if block == nil {
				return nil
			}
			blockType, _ := block["type"].(string)
			switch blockType {
			case "tool_use":
				tools[index] = &toolBuffer{
					id:   stringValue(block["id"]),
					name: stringValue(block["name"]),
				}
			case "thinking":
				thinkingBlocks[index] = &strings.Builder{}
			}
		case "content_block_delta":
			index := int(numberValue(payload["index"]))
			delta, _ := payload["delta"].(map[string]any)
			if delta == nil {
				return nil
			}
			switch stringValue(delta["type"]) {
			case "text_delta":
				text := stringValue(delta["text"])
				if text != "" {
					if onDelta != nil {
						onDelta(text)
					}
					result.TextBlocks = appendTextBlock(result.TextBlocks, text)
				}
			case "thinking_delta":
				if tb := thinkingBlocks[index]; tb != nil {
					tb.WriteString(stringValue(delta["thinking"]))
				}
			case "input_json_delta":
				if tools[index] == nil {
					tools[index] = &toolBuffer{}
				}
				tools[index].args += stringValue(delta["partial_json"])
			}
		case "content_block_stop":
			index := int(numberValue(payload["index"]))
			if tool := tools[index]; tool != nil {
				result.ToolCalls = append(result.ToolCalls, ToolCall{
					ID:        tool.id,
					Name:      tool.name,
					Arguments: canonicalJSON(json.RawMessage(tool.args)),
				})
				delete(tools, index)
			}
			if tb := thinkingBlocks[index]; tb != nil {
				if s := tb.String(); s != "" {
					result.ThinkingBlocks = append(result.ThinkingBlocks, s)
				}
				delete(thinkingBlocks, index)
			}
		case "message_delta":
			if delta, ok := payload["delta"].(map[string]any); ok {
				result.StopReason = stringValue(delta["stop_reason"])
			}
			if usage, ok := payload["usage"].(map[string]any); ok {
				result.Usage.OutputTokens = int(numberValue(usage["output_tokens"]))
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read anthropic stream: %w", err)
	}
	result.Usage.TotalTokens = result.Usage.InputTokens + result.Usage.OutputTokens
	return result, nil
}
