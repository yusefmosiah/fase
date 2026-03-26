package native

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

// largeTestHistory creates a history large enough to trigger compression.
// Uses a model with 128K context window (threshold=64K tokens=256K chars).
// Produces messages that total ~80K tokens (> 64K threshold).
func largeTestHistory(count int, charsPerMsg int) []Message {
	msgs := make([]Message, count)
	for i := range msgs {
		msgs[i] = Message{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: strings.Repeat("a", charsPerMsg),
			}},
		}
	}
	return msgs
}

// --- Token estimation tests ---

func TestEstimateTokensEmpty(t *testing.T) {
	t.Parallel()
	if got := estimateTokens(nil); got != 0 {
		t.Fatalf("expected 0 tokens for nil, got %d", got)
	}
	if got := estimateTokens([]Message{}); got != 0 {
		t.Fatalf("expected 0 tokens for empty, got %d", got)
	}
}

func TestEstimateTokensTextMessages(t *testing.T) {
	t.Parallel()
	// 100 chars of text → ~25 tokens (100/4) + role overhead (4)
	msgs := []Message{{
		Role: "user",
		Content: []ContentBlock{{
			Type: "text",
			Text: strings.Repeat("a", 100),
		}},
	}}
	tokens := estimateTokens(msgs)
	if tokens < 20 || tokens > 35 {
		t.Fatalf("expected ~29 tokens for 100 chars, got %d", tokens)
	}
}

func TestEstimateTokensToolUse(t *testing.T) {
	t.Parallel()
	msgs := []Message{{
		Role: "assistant",
		Content: []ContentBlock{{
			Type:  "tool_use",
			Name:  "read_file",
			Input: json.RawMessage(`{"path": "/tmp/test.txt"}`),
		}},
	}}
	tokens := estimateTokens(msgs)
	if tokens < 5 {
		t.Fatalf("expected non-zero tokens for tool_use, got %d", tokens)
	}
}

func TestEstimateTokensMultipleMessages(t *testing.T) {
	t.Parallel()
	msgs := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello world"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "hi there!"}}},
		{Role: "user", Content: []ContentBlock{{Type: "tool_result", Text: "some result"}}},
	}
	tokens := estimateTokens(msgs)
	if tokens < 10 {
		t.Fatalf("expected reasonable token count, got %d", tokens)
	}
	// Multiple messages should have more tokens than single
	single := estimateTokens(msgs[:1])
	if tokens <= single {
		t.Fatalf("expected %d > %d (more messages = more tokens)", tokens, single)
	}
}

// --- Context window tests ---

func TestContextWindowForModel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		model    string
		expected int
	}{
		{"claude-opus-4-6", 200000},
		{"claude-sonnet-4-6", 200000},
		{"claude-haiku-4-5", 200000},
		{"claude-3-5-sonnet-20241022", 200000},
		{"us.anthropic.claude-sonnet-4-6", 200000},
		{"anthropic.claude-opus-4-6-v1:0", 200000},
		{"glm-4.7", 128000},
		{"gpt-5.4-mini", 200000},
		{"gpt-4-turbo", 128000},
		{"o3-2025-04-16", 200000},
		{"unknown-model-xyz", 128000},
	}
	for _, tt := range tests {
		got := contextWindowForModel(tt.model)
		if got != tt.expected {
			t.Errorf("contextWindowForModel(%q) = %d, want %d", tt.model, got, tt.expected)
		}
	}
}

// --- needsCompression tests ---

func TestNeedsCompressionBelowThreshold(t *testing.T) {
	t.Parallel()
	// Small history should not trigger compression
	msgs := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
	}
	if needsCompression(msgs, "claude-sonnet-4-6") {
		t.Fatal("expected no compression for small history")
	}
}

func TestNeedsCompressionTooFewMessages(t *testing.T) {
	t.Parallel()
	// Even with large content, if message count is too low, no compression.
	// minRecentTurns=6, minTurnsToCompress=4, need > 10 messages
	msgs := make([]Message, 8)
	for i := range msgs {
		msgs[i] = Message{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: strings.Repeat("x", 10000),
			}},
		}
	}
	if needsCompression(msgs, "claude-sonnet-4-6") {
		t.Fatal("expected no compression with too few messages")
	}
}

func TestNeedsCompressionAboveThreshold(t *testing.T) {
	t.Parallel()
	// Use glm- model (128K window, 64K threshold = 256K chars needed).
	// 400 messages * 1000 chars = 400K chars ≈ 100K tokens > 64K threshold.
	msgs := largeTestHistory(400, 1000)
	if !needsCompression(msgs, "glm-4.7") {
		t.Fatalf("expected compression needed: %d msgs, %d estimated tokens",
			len(msgs), estimateTokens(msgs))
	}
}

func TestNeedsCompressionClaudeModelNeedsMore(t *testing.T) {
	t.Parallel()
	// Claude sonnet 4.6 has 200K window (100K threshold).
	// 300 messages * 1000 chars = 75K tokens < 100K → should NOT trigger.
	msgs := largeTestHistory(300, 1000)
	if needsCompression(msgs, "claude-sonnet-4-6") {
		t.Fatal("expected no compression for claude-sonnet-4-6 with 75K tokens")
	}
	// But 400 messages * 1500 chars = 150K tokens > 100K → should trigger.
	msgs = largeTestHistory(400, 1500)
	if !needsCompression(msgs, "claude-sonnet-4-6") {
		t.Fatalf("expected compression for claude-sonnet-4-6 with %d estimated tokens",
			estimateTokens(msgs))
	}
}

// --- Supervisor context file tests ---

func TestSupervisorContextPath(t *testing.T) {
	t.Parallel()
	path := supervisorContextPath("/project")
	expected := filepath.Join("/project", ".fase", "supervisor-context.md")
	if path != expected {
		t.Fatalf("expected %q, got %q", expected, path)
	}
}

func TestSaveAndLoadSupervisorContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Loading non-existent file should return empty string
	ctx, err := loadSupervisorContext(dir)
	if err != nil {
		t.Fatalf("load non-existent: %v", err)
	}
	if ctx != "" {
		t.Fatalf("expected empty string, got %q", ctx)
	}

	// Save and reload
	summary := "User asked to implement feature X. Files modified: foo.go, bar.go."
	if err := saveSupervisorContext(dir, summary); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := loadSupervisorContext(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !strings.Contains(loaded, "User asked to implement feature X") {
		t.Fatalf("expected summary content in loaded context, got %q", loaded)
	}
	if !strings.Contains(loaded, "# Supervisor Context") {
		t.Fatalf("expected header in loaded context, got %q", loaded)
	}

	// Verify file exists
	data, err := os.ReadFile(supervisorContextPath(dir))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty file")
	}
}

func TestSaveSupervisorContextCreatesDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	targetDir := filepath.Join(dir, ".fase")
	// Remove .fase if it exists
	os.RemoveAll(targetDir)

	if err := saveSupervisorContext(dir, "test summary"); err != nil {
		t.Fatalf("save with missing dir: %v", err)
	}
	if _, err := os.Stat(supervisorContextPath(dir)); err != nil {
		t.Fatalf("file should exist: %v", err)
	}
}

// --- injectSupervisorContext tests ---

func TestInjectSupervisorContextNoCWD(t *testing.T) {
	t.Parallel()
	msgs := []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}}}
	result := injectSupervisorContext("", msgs)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
}

func TestInjectSupervisorContextNoHistory(t *testing.T) {
	t.Parallel()
	result := injectSupervisorContext("/tmp", nil)
	if len(result) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(result))
	}
}

func TestInjectSupervisorContextNoFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	msgs := []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}}}
	result := injectSupervisorContext(dir, msgs)
	if len(result) != 1 {
		t.Fatalf("expected 1 message (no file to inject), got %d", len(result))
	}
}

func TestInjectSupervisorContextWithFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_ = saveSupervisorContext(dir, "Previous work: implemented auth module")

	msgs := []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}}}
	result := injectSupervisorContext(dir, msgs)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages (context + original), got %d", len(result))
	}
	if !strings.Contains(result[0].Content[0].Text, "supervisor-context.md") {
		t.Fatalf("expected supervisor context header in first message, got %q", result[0].Content[0].Text)
	}
	if !strings.Contains(result[0].Content[0].Text, "auth module") {
		t.Fatalf("expected summary content in first message, got %q", result[0].Content[0].Text)
	}
}

func TestInjectSupervisorContextSkipsIfAlreadySummary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_ = saveSupervisorContext(dir, "Previous work")

	// History already starts with a summary
	msgs := []Message{{
		Role: "user",
		Content: []ContentBlock{{
			Type: "text",
			Text: "[Previous conversation summary]\nSome existing summary...",
		}},
	}}
	result := injectSupervisorContext(dir, msgs)
	// Should not duplicate — still 1 message
	if len(result) != 1 {
		t.Fatalf("expected 1 message (already has summary), got %d", len(result))
	}
}

// --- compressHistory integration tests ---

func TestCompressHistoryNotNeeded(t *testing.T) {
	t.Parallel()
	client := &scriptedClient{} // won't be called
	msgs := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
	}
	result, err := compressHistory(context.Background(), client, apiFormatAnthropic, "claude-sonnet-4-6", msgs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected unchanged history (2 messages), got %d", len(result))
	}
}

func TestCompressHistoryWithLLMSummary(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotSystem string

	client := &scriptedClient{
		steps: []scriptStep{
			func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
				mu.Lock()
				gotSystem = req.System
				mu.Unlock()
				return &LLMResponse{
					ID:         "summary-1",
					TextBlocks: []string{"User asked to build a REST API. Files created: main.go, handler.go, router.go. Decided to use chi router. Tests passing."},
					StopReason: "end_turn",
				}, nil
			},
		},
	}

	// Use glm- model (128K window). 400 msgs * 1000 chars = 100K tokens > 64K threshold.
	msgs := largeTestHistory(400, 1000)

	dir := t.TempDir()
	result, err := compressHistory(context.Background(), client, apiFormatAnthropic, "glm-4.7", msgs, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have 1 summary + minRecentTurns(6) messages
	expectedLen := 1 + minRecentTurns
	if len(result) != expectedLen {
		t.Fatalf("expected %d messages (1 summary + %d recent), got %d", expectedLen, minRecentTurns, len(result))
	}

	// First message should be the summary
	firstText := result[0].Content[0].Text
	if len(firstText) > 50 {
		firstText = firstText[:50]
	}
	if !strings.HasPrefix(result[0].Content[0].Text, "[Previous conversation summary]") {
		t.Fatalf("expected summary header, got %q", firstText)
	}

	// Summary client should have received a system prompt
	if !strings.Contains(gotSystem, "context summarizer") {
		t.Fatalf("expected summarizer system prompt, got %q", gotSystem)
	}

	// Supervisor context file should have been saved
	loaded, err := loadSupervisorContext(dir)
	if err != nil {
		t.Fatalf("load supervisor context: %v", err)
	}
	if !strings.Contains(loaded, "REST API") {
		t.Fatalf("expected summary content in supervisor context file, got %q", loaded)
	}
}

func TestCompressHistoryFallbackOnLLMError(t *testing.T) {
	t.Parallel()

	client := &scriptedClient{
		steps: []scriptStep{
			func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
				return nil, context.DeadlineExceeded
			},
		},
	}

	// Build large history using glm model
	msgs := largeTestHistory(400, 1000)

	result, err := compressHistory(context.Background(), client, apiFormatAnthropic, "glm-4.7", msgs, "")
	if err != nil {
		t.Fatalf("should not return error on LLM failure (fallback), got: %v", err)
	}
	// Should return original history unchanged
	if len(result) != 400 {
		t.Fatalf("expected original 400 messages on failure, got %d", len(result))
	}
}

// --- maybeCompressHistory integration test ---

func TestMaybeCompressHistoryNoTrigger(t *testing.T) {
	t.Parallel()

	session := newNativeSession(context.Background(), nativeSessionConfig{
		id:       "nsess_compress_no_trigger",
		provider: Provider{Name: providerZAI, APIFormat: apiFormatAnthropic, ModelID: "claude-sonnet-4-6"},
		client:   &scriptedClient{}, // should not be called
		registry: MustNewToolRegistry(),
		cwd:      t.TempDir(),
	})
	defer func() { _ = session.Close() }()

	session.history = []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
	}

	session.maybeCompressHistory(context.Background())

	// History should be unchanged
	if len(session.history) != 2 {
		t.Fatalf("expected 2 messages after no-op compression, got %d", len(session.history))
	}
}

func TestMaybeCompressHistoryTriggers(t *testing.T) {
	t.Parallel()

	session := newNativeSession(context.Background(), nativeSessionConfig{
		id:       "nsess_compress_trigger",
		provider: Provider{Name: providerZAI, APIFormat: apiFormatAnthropic, ModelID: "glm-4.7"},
		client: &scriptedClient{
			steps: []scriptStep{
				func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
					return &LLMResponse{
						ID:         "summary-1",
						TextBlocks: []string{"Compressed: user was working on auth, files modified, tests green."},
						StopReason: "end_turn",
					}, nil
				},
			},
		},
		registry: MustNewToolRegistry(),
		cwd:      t.TempDir(),
	})
	defer func() { _ = session.Close() }()

	// Build large history using glm model
	session.history = largeTestHistory(400, 1000)

	session.maybeCompressHistory(context.Background())

	expectedLen := 1 + minRecentTurns
	if len(session.history) != expectedLen {
		t.Fatalf("expected %d messages after compression, got %d", expectedLen, len(session.history))
	}

	// Previous ID should be reset
	session.mu.Lock()
	prevID := session.previousID
	session.mu.Unlock()
	if prevID != "" {
		t.Fatalf("expected previousID to be reset after compression")
	}
}

// --- marshalMessagesSize test ---

func TestMarshalMessagesSize(t *testing.T) {
	t.Parallel()
	msgs := []Message{{
		Role: "user",
		Content: []ContentBlock{{
			Type: "text",
			Text: "hello world",
		}},
	}}
	size := marshalMessagesSize(msgs)
	if size <= 0 {
		t.Fatalf("expected positive size, got %d", size)
	}
}

// --- generateSummary test ---

func TestGenerateSummaryEmptyTurns(t *testing.T) {
	t.Parallel()
	_, err := generateSummary(context.Background(), &scriptedClient{
		steps: []scriptStep{
			func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
				return &LLMResponse{
					ID:         "summary-empty",
					TextBlocks: []string{"No conversation to summarize."},
					StopReason: "end_turn",
				}, nil
			},
		},
	}, apiFormatAnthropic, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- End-to-end: turn completion triggers compression ---

func TestTurnCompletionTriggersCompression(t *testing.T) {
	t.Parallel()

	compressCalled := make(chan struct{}, 1)

	session := newNativeSession(context.Background(), nativeSessionConfig{
		id:       "nsess_e2e_compress",
		provider: Provider{Name: providerZAI, APIFormat: apiFormatAnthropic, ModelID: "glm-4.7"},
		client: &scriptedClient{
			steps: []scriptStep{
				// First call: normal response
				func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
					return &LLMResponse{
						ID:         "resp-1",
						TextBlocks: []string{"done"},
						StopReason: "end_turn",
					}, nil
				},
				// Second call: compression summary (if triggered)
				func(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
					close(compressCalled)
					return &LLMResponse{
						ID:         "summary-1",
						TextBlocks: []string{"Summary of previous work."},
						StopReason: "end_turn",
					}, nil
				},
			},
		},
		registry: MustNewToolRegistry(),
		cwd:      t.TempDir(),
	})
	defer func() { _ = session.Close() }()

	// Pre-populate with enough history to trigger compression (glm-4.7, 128K window)
	session.history = largeTestHistory(400, 1000)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Add one more message and run a turn that will complete and trigger compression
	session.appendHistory(messageFromInputs([]adapterapi.Input{adapterapi.TextInput("continue")}))

	if err := session.runToolLoop(ctx, "nturn-compress-e2e"); err != nil {
		t.Fatalf("runToolLoop: %v", err)
	}

	// Wait for compression to have been called
	select {
	case <-compressCalled:
		// Compression was triggered — good
	case <-time.After(2 * time.Second):
		t.Fatal("expected compression to be called within 2s")
	}

	// History should be compressed
	if len(session.history) > 20 {
		t.Fatalf("expected compressed history (<= 20 messages), got %d", len(session.history))
	}
}
