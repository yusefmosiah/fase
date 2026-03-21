package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicClientCallStream(t *testing.T) {
	var gotAuth string
	var requestBody anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("unexpected accept: %s", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":3}}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello \"}}\n\n")
		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"read_file\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"/tmp/a.txt\\\"}\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		fmt.Fprint(w, "event: message_delta\n")
		fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":7}}\n\n")
	}))
	defer srv.Close()

	client, err := NewAnthropicClient(Provider{
		Name:      providerZAI,
		APIFormat: apiFormatAnthropic,
		BaseURL:   srv.URL,
		ModelID:   "glm-4.7",
		AuthFunc: func(context.Context) (string, error) {
			return "Bearer test-token", nil
		},
	}, srv.Client())
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}

	var deltas []string
	resp, err := client.Call(context.Background(), LLMRequest{
		System: "system",
		Messages: []Message{{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: "hello",
			}},
		}},
		Tools: []ToolDef{{
			Name:        "read_file",
			Description: "Read file contents",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"required": []string{"path"},
			},
		}},
		Stream: true,
		OnDelta: func(text string) {
			deltas = append(deltas, text)
		},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("unexpected auth header: %s", gotAuth)
	}
	if requestBody.Model != "glm-4.7" || requestBody.System != "system" || !requestBody.Stream {
		t.Fatalf("unexpected request body: %+v", requestBody)
	}
	if len(requestBody.Messages) != 1 || requestBody.Messages[0].Role != "user" {
		t.Fatalf("unexpected request messages: %+v", requestBody.Messages)
	}
	if len(requestBody.Tools) != 1 || requestBody.Tools[0].Name != "read_file" {
		t.Fatalf("unexpected anthropic tools: %+v", requestBody.Tools)
	}
	if got := requestBody.Tools[0].InputSchema["type"]; got != "object" {
		t.Fatalf("unexpected anthropic input schema: %+v", requestBody.Tools[0].InputSchema)
	}
	if resp.ID != "msg_1" || resp.StopReason != "tool_use" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if strings.Join(resp.TextBlocks, "") != "hello " || strings.Join(deltas, "") != "hello " {
		t.Fatalf("unexpected text blocks=%q deltas=%q", resp.TextBlocks, deltas)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read_file" || string(resp.ToolCalls[0].Arguments) != "{\"path\":\"/tmp/a.txt\"}" {
		t.Fatalf("unexpected tool calls: %+v", resp.ToolCalls)
	}
	if resp.Usage.InputTokens != 3 || resp.Usage.OutputTokens != 7 || resp.Usage.TotalTokens != 10 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

func TestOpenAIClientCallStream(t *testing.T) {
	var requestBody openAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer openai-token" {
			t.Fatalf("unexpected auth header: %s", got)
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("unexpected accept: %s", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\n")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi \"}\n\n")
		fmt.Fprint(w, "event: response.output_item.added\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"item_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"glob\"}}\n\n")
		fmt.Fprint(w, "event: response.function_call_arguments.done\n")
		fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"item_1\",\"call_id\":\"call_1\",\"name\":\"glob\",\"arguments\":\"{\\\"pattern\\\":\\\"*.go\\\"}\"}\n\n")
		fmt.Fprint(w, "event: response.completed\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":5,\"output_tokens\":8,\"total_tokens\":13}}}\n\n")
	}))
	defer srv.Close()

	client, err := NewOpenAIClient(Provider{
		Name:      providerChatGPT,
		APIFormat: apiFormatOpenAI,
		BaseURL:   srv.URL,
		ModelID:   "gpt-5.4-mini",
		AuthFunc: func(context.Context) (string, error) {
			return "Bearer openai-token", nil
		},
	}, srv.Client())
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}

	var deltas []string
	resp, err := client.Call(context.Background(), LLMRequest{
		System: "system",
		Messages: []Message{{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: "list go files",
			}},
		}},
		Tools: []ToolDef{{
			Name:        "glob",
			Description: "match files",
			Parameters: map[string]any{
				"type": "object",
			},
		}},
		Stream: true,
		OnDelta: func(text string) {
			deltas = append(deltas, text)
		},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if requestBody.Model != "gpt-5.4-mini" || requestBody.Instructions != "system" || !requestBody.Stream {
		t.Fatalf("unexpected request body: %+v", requestBody)
	}
	if len(requestBody.Input) != 1 || requestBody.Input[0].Role != "user" {
		t.Fatalf("unexpected input items: %+v", requestBody.Input)
	}
	if len(requestBody.Tools) != 1 || requestBody.Tools[0].Type != "function" || requestBody.Tools[0].Name != "glob" {
		t.Fatalf("unexpected tool definitions: %+v", requestBody.Tools)
	}
	if strings.Join(resp.TextBlocks, "") != "hi " || strings.Join(deltas, "") != "hi " {
		t.Fatalf("unexpected text blocks=%q deltas=%q", resp.TextBlocks, deltas)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Name != "glob" {
		t.Fatalf("unexpected tool calls: %+v", resp.ToolCalls)
	}
	if string(resp.ToolCalls[0].Arguments) != "{\"pattern\":\"*.go\"}" {
		t.Fatalf("unexpected tool args: %s", resp.ToolCalls[0].Arguments)
	}
	if resp.StopReason != "tool_use" || resp.Usage.TotalTokens != 13 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestOpenAIClientDefaultsInstructions(t *testing.T) {
	var requestBody openAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_1","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`)
	}))
	defer srv.Close()

	client, err := NewOpenAIClient(Provider{
		Name:      providerChatGPT,
		APIFormat: apiFormatOpenAI,
		BaseURL:   srv.URL,
		ModelID:   "gpt-5.4-mini",
		AuthFunc: func(context.Context) (string, error) {
			return "Bearer openai-token", nil
		},
	}, srv.Client())
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}

	_, err = client.Call(context.Background(), LLMRequest{
		Messages: []Message{{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: "say ok",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if strings.TrimSpace(requestBody.Instructions) == "" {
		t.Fatalf("expected default instructions to be sent, got %+v", requestBody)
	}
}

func TestOpenAIClientOmitsPreviousResponseIDForChatGPTProvider(t *testing.T) {
	var requestBody openAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_1","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`)
	}))
	defer srv.Close()

	client, err := NewOpenAIClient(Provider{
		Name:      providerChatGPT,
		APIFormat: apiFormatOpenAI,
		BaseURL:   srv.URL,
		ModelID:   "gpt-5.4-mini",
		AuthFunc: func(context.Context) (string, error) {
			return "Bearer openai-token", nil
		},
	}, srv.Client())
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}

	_, err = client.Call(context.Background(), LLMRequest{
		System:             "system",
		PreviousResponseID: "resp_prev",
		Messages: []Message{{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: "say ok",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if requestBody.PreviousResponseID != "" {
		t.Fatalf("expected previous_response_id to be omitted for chatgpt provider, got %+v", requestBody)
	}
}

func TestOpenAIInputEncodesToolUseArgumentsAsString(t *testing.T) {
	items := openAIInput([]Message{{
		Role: "assistant",
		Content: []ContentBlock{{
			Type:  "tool_use",
			ID:    "call_1",
			Name:  "grep",
			Input: json.RawMessage(`{"pattern":"TODO"}`),
		}},
	}})
	if len(items) != 1 {
		t.Fatalf("expected one item, got %+v", items)
	}
	if items[0].Arguments != `{"pattern":"TODO"}` {
		t.Fatalf("expected tool arguments to stay JSON string, got %+v", items[0])
	}
}

func TestOpenAIInputEmitsToolResultItemsAlongsideText(t *testing.T) {
	items := openAIInput([]Message{{
		Role: "user",
		Content: []ContentBlock{
			{Type: "text", Text: "steer"},
			{Type: "tool_result", ToolUseID: "call_1", Text: "done"},
			{Type: "tool_result", ToolUseID: "call_2", Text: "done2"},
		},
	}})
	if len(items) != 3 {
		t.Fatalf("expected text plus two tool outputs, got %+v", items)
	}
	if items[0].Role != "user" {
		t.Fatalf("expected first item to preserve user text, got %+v", items[0])
	}
	if items[1].Type != "function_call_output" || items[1].CallID != "call_1" || items[1].Output != "done" {
		t.Fatalf("unexpected first tool output item: %+v", items[1])
	}
	if items[2].Type != "function_call_output" || items[2].CallID != "call_2" || items[2].Output != "done2" {
		t.Fatalf("unexpected second tool output item: %+v", items[2])
	}
}
