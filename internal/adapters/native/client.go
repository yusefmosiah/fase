package native

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	apiFormatAnthropic = "anthropic"
	apiFormatOpenAI    = "openai"
)

// HTTPDoer is the subset of *http.Client needed by the native clients.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// LLMClient abstracts Anthropic Messages API vs OpenAI Responses API.
type LLMClient interface {
	Call(ctx context.Context, req LLMRequest) (*LLMResponse, error)
}

type LLMRequest struct {
	System             string
	Messages           []Message
	Tools              []ToolDef
	Stream             bool
	ReasoningEffort    string // "low", "medium", "high", "max" (Anthropic) or "xhigh" (OpenAI)
	PreviousResponseID string
	OnDelta            func(text string)
}

type LLMResponse struct {
	ID         string
	TextBlocks []string
	ToolCalls  []ToolCall
	StopReason string
	Usage      Usage
}

type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
}

type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

func newHTTPClient(httpClient HTTPDoer) HTTPDoer {
	if httpClient != nil {
		return httpClient
	}
	return &http.Client{}
}

func newJSONRequest(ctx context.Context, method, rawURL string, body any) (*http.Request, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

type sseEvent struct {
	Event string
	Data  []byte
}

func readSSE(resp *http.Response, fn func(sseEvent) error) error {
	if resp.Body == nil {
		return fmt.Errorf("sse response has no body")
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var evt sseEvent
	dispatch := func() error {
		if len(evt.Data) == 0 && evt.Event == "" {
			return nil
		}
		evt.Data = bytes.TrimSpace(evt.Data)
		if err := fn(evt); err != nil {
			return err
		}
		evt = sseEvent{}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			evt.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			evt.Data = append(evt.Data, data...)
			evt.Data = append(evt.Data, '\n')
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}
	return dispatch()
}

func appendTextBlock(blocks []string, text string) []string {
	if text == "" {
		return blocks
	}
	if len(blocks) == 0 {
		return append(blocks, text)
	}
	blocks[len(blocks)-1] += text
	return blocks
}

func canonicalJSON(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage([]byte("{}"))
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func numberValue(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
}
