package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type openAIClient struct {
	provider Provider
	http     HTTPDoer
}

func NewOpenAIClient(provider Provider, httpClient HTTPDoer) (LLMClient, error) {
	return &openAIClient{provider: provider, http: newHTTPClient(httpClient)}, nil
}

type openAIRequest struct {
	Model              string              `json:"model"`
	Instructions       string              `json:"instructions,omitempty"`
	Input              []openAIItem        `json:"input,omitempty"`
	Tools              []openAITool        `json:"tools,omitempty"`
	Store              bool                `json:"store"`
	Stream             bool                `json:"stream,omitempty"`
	Reasoning          *openAIReasoning    `json:"reasoning,omitempty"`
	PreviousResponseID string              `json:"previous_response_id,omitempty"`
}

type openAIReasoning struct {
	Effort string `json:"effort"` // "low", "medium", "high", "xhigh"
}

type openAIItem struct {
	Type      string `json:"type,omitempty"`
	Role      string `json:"role,omitempty"`
	Content   any    `json:"content,omitempty"`
	Name      string `json:"name,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}

type openAITool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openAIResponse struct {
	ID     string               `json:"id"`
	Output []openAIResponseItem `json:"output"`
	Usage  Usage                `json:"usage"`
}

type openAIResponseItem struct {
	Type      string                  `json:"type"`
	ID        string                  `json:"id,omitempty"`
	CallID    string                  `json:"call_id,omitempty"`
	Name      string                  `json:"name,omitempty"`
	Arguments json.RawMessage         `json:"arguments,omitempty"`
	Content   []openAIResponseContent `json:"content,omitempty"`
	Role      string                  `json:"role,omitempty"`
	Status    string                  `json:"status,omitempty"`
}

type openAIResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func (c *openAIClient) Call(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	instructions := strings.TrimSpace(req.System)
	if instructions == "" {
		instructions = defaultSystemPrompt()
	}
	payload := openAIRequest{
		Model:        c.provider.ModelID,
		Instructions: instructions,
		Input:        openAIInput(req.Messages),
		Tools:        openAITools(req.Tools),
		Store:        false,
		Stream:       req.Stream,
	}
	if req.ReasoningEffort != "" {
		payload.Reasoning = &openAIReasoning{Effort: req.ReasoningEffort}
	}
	if c.provider.Name != providerChatGPT {
		payload.PreviousResponseID = req.PreviousResponseID
	}
	httpReq, err := newJSONRequest(ctx, http.MethodPost, c.provider.BaseURL, payload)
	if err != nil {
		return nil, err
	}
	authHeader, err := c.provider.authHeader(ctx)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", authHeader)
	httpReq.Header.Set("Accept", "application/json")
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai call: status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	if req.Stream {
		return parseOpenAIStream(resp, req.OnDelta)
	}
	return parseOpenAIResponse(resp)
}

func openAIInput(messages []Message) []openAIItem {
	out := make([]openAIItem, 0, len(messages))
	for _, msg := range messages {
		var parts []map[string]string
		flushText := func() {
			if len(parts) == 0 {
				return
			}
			out = append(out, openAIItem{
				Role:    msg.Role,
				Content: parts,
			})
			parts = nil
		}
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					parts = append(parts, map[string]string{
						"type": "input_text",
						"text": block.Text,
					})
				}
			case "tool_use":
				flushText()
				out = append(out, openAIItem{
					Type:      "function_call",
					CallID:    block.ID,
					Name:      block.Name,
					Arguments: string(canonicalJSON(block.Input)),
				})
			case "tool_result":
				flushText()
				out = append(out, openAIItem{
					Type:   "function_call_output",
					CallID: block.ToolUseID,
					Output: block.Text,
				})
			}
		}
		flushText()
	}
	return out
}

func openAITools(tools []ToolDef) []openAITool {
	out := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, openAITool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}
	return out
}

func parseOpenAIResponse(resp *http.Response) (*LLMResponse, error) {
	var payload openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	result := &LLMResponse{
		ID:    payload.ID,
		Usage: payload.Usage,
	}
	if result.Usage.TotalTokens == 0 {
		result.Usage.TotalTokens = result.Usage.InputTokens + result.Usage.OutputTokens
	}
	for _, item := range payload.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Text != "" {
					result.TextBlocks = append(result.TextBlocks, part.Text)
				}
			}
			result.StopReason = "end_turn"
		case "function_call":
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: canonicalJSON(item.Arguments),
			})
			result.StopReason = "tool_use"
		}
	}
	return result, nil
}

func parseOpenAIStream(resp *http.Response, onDelta func(string)) (*LLMResponse, error) {
	result := &LLMResponse{}
	type toolBuffer struct {
		callID string
		name   string
		args   string
	}
	tools := map[string]*toolBuffer{}
	if err := readSSE(resp, func(evt sseEvent) error {
		if len(evt.Data) == 0 || strings.TrimSpace(string(evt.Data)) == "[DONE]" {
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal(evt.Data, &payload); err != nil {
			return nil
		}
		typ, _ := payload["type"].(string)
		switch typ {
		case "response.created":
			if response, ok := payload["response"].(map[string]any); ok {
				result.ID = stringValue(response["id"])
			}
		case "response.output_text.delta":
			text := stringValue(payload["delta"])
			if text != "" {
				if onDelta != nil {
					onDelta(text)
				}
				result.TextBlocks = appendTextBlock(result.TextBlocks, text)
			}
		case "response.output_item.added":
			item, _ := payload["item"].(map[string]any)
			if stringValue(item["type"]) == "function_call" {
				key := stringValue(item["id"])
				tools[key] = &toolBuffer{
					callID: stringValue(item["call_id"]),
					name:   stringValue(item["name"]),
					args:   stringValue(item["arguments"]),
				}
			}
		case "response.function_call_arguments.delta":
			key := stringValue(payload["item_id"])
			if tools[key] == nil {
				tools[key] = &toolBuffer{}
			}
			tools[key].args += stringValue(payload["delta"])
			if tools[key].callID == "" {
				tools[key].callID = stringValue(payload["call_id"])
			}
			if tools[key].name == "" {
				tools[key].name = stringValue(payload["name"])
			}
		case "response.function_call_arguments.done":
			key := stringValue(payload["item_id"])
			if tools[key] == nil {
				tools[key] = &toolBuffer{}
			}
			if arguments := stringValue(payload["arguments"]); arguments != "" {
				tools[key].args = arguments
			}
			if tools[key].callID == "" {
				tools[key].callID = stringValue(payload["call_id"])
			}
			if tools[key].name == "" {
				tools[key].name = stringValue(payload["name"])
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        tools[key].callID,
				Name:      tools[key].name,
				Arguments: canonicalJSON(json.RawMessage(tools[key].args)),
			})
			delete(tools, key)
			result.StopReason = "tool_use"
		case "response.output_item.done":
			item, _ := payload["item"].(map[string]any)
			if stringValue(item["type"]) == "function_call" {
				args, _ := item["arguments"].(string)
				result.ToolCalls = append(result.ToolCalls, ToolCall{
					ID:        stringValue(item["call_id"]),
					Name:      stringValue(item["name"]),
					Arguments: canonicalJSON(json.RawMessage(args)),
				})
				result.StopReason = "tool_use"
			}
		case "response.completed":
			response, _ := payload["response"].(map[string]any)
			if response != nil {
				result.ID = stringValue(response["id"])
				if usage, ok := response["usage"].(map[string]any); ok {
					result.Usage = Usage{
						InputTokens:  int(numberValue(usage["input_tokens"])),
						OutputTokens: int(numberValue(usage["output_tokens"])),
						TotalTokens:  int(numberValue(usage["total_tokens"])),
					}
				}
			}
			if result.StopReason == "" {
				result.StopReason = "end_turn"
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read openai stream: %w", err)
	}
	if result.Usage.TotalTokens == 0 {
		result.Usage.TotalTokens = result.Usage.InputTokens + result.Usage.OutputTokens
	}
	return result, nil
}
