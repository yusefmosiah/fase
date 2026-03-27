package native

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// historyCompressor manages proactive history compression to prevent
// context window overflow. It estimates token usage after each turn,
// compresses old turns into a summary when approaching the threshold,
// and maintains a persistent supervisor context file.
//
// Approach (informed by Claude Code auto-compact, Codex CLI, and
// Microsoft AutoGen patterns):
//  1. After each completed turn, estimate total history token count.
//  2. When approaching 50% of model context window, compress old turns
//     into a summary using a lightweight LLM call.
//  3. Replace old turns with the summary block.
//  4. Keep the summary + recent turns within budget.
//  5. Persist a supervisor context file (.cogent/supervisor-context.md)
//     that survives session restarts.
//
// compressionThreshold is the fraction of the context window at which
// compression is triggered. 0.5 = 50%.
const compressionThreshold = 0.5

// minRecentTurns is the minimum number of recent messages (user+assistant pairs)
// to keep in full after compression. This preserves the agent's current momentum.
const minRecentTurns = 6 // ~3 user messages + 3 assistant/tool messages

// minTurnsToCompress is the minimum number of messages beyond the recent window
// that must exist before compression is worthwhile.
const minTurnsToCompress = 4

// contextWindowForModel returns the known context window size for common models.
// Returns a conservative default (128K) for unknown models.
func contextWindowForModel(modelID string) int {
	// Map of model prefix -> context window size in tokens.
	windows := map[string]int{
		// Claude models (Anthropic / z.ai / Bedrock)
		"claude-opus-4-6":      200000,
		"claude-sonnet-4-6":    200000,
		"claude-haiku-4-5":     200000,
		"claude-sonnet-4-5":    200000,
		"claude-3-5-sonnet":    200000,
		"claude-3-opus":        200000,
		"claude-3-haiku":       200000,
		"us.anthropic.claude-": 200000,
		"anthropic.claude-":    200000,
		"glm-":                 128000,
		// OpenAI models (ChatGPT / Responses API)
		"gpt-5":      200000,
		"gpt-4":      128000,
		"o3":         200000,
		"o4":         200000,
		"codex-mini": 200000,
	}
	for prefix, window := range windows {
		if strings.HasPrefix(modelID, prefix) {
			return window
		}
	}
	return 128000 // conservative default
}

// estimateTokens provides a rough token count estimate for messages.
// Uses ~4 characters per token for English text, which is a standard
// approximation for LLM tokenizers (BPE-based). This errs on the side
// of overestimation to trigger compression early.
func estimateTokens(messages []Message) int {
	if len(messages) == 0 {
		return 0
	}
	total := 0
	for _, msg := range messages {
		// Message role overhead
		total += 4
		for _, block := range msg.Content {
			switch block.Type {
			case "text", "tool_result":
				// Text content: ~4 chars per token
				total += len(block.Text) / 4
			case "tool_use":
				// Tool name + arguments
				total += len(block.Name) / 4
				total += len(block.Input) / 4
				total += 20 // overhead for tool call structure
			case "thinking":
				// Thinking blocks can be large; count them but
				// they'll be excluded from compression targets
				total += len(block.Text) / 4
			}
		}
	}
	return total
}

// needsCompression checks if the history exceeds the compression threshold.
// Returns true if the estimated token count is >= threshold * contextWindow.
func needsCompression(messages []Message, modelID string) bool {
	window := contextWindowForModel(modelID)
	estimated := estimateTokens(messages)
	threshold := int(float64(window) * compressionThreshold)
	return estimated >= threshold && len(messages) > minRecentTurns+minTurnsToCompress
}

// compressHistory compresses old conversation turns into a summary.
// It keeps the most recent minRecentTurns messages intact and replaces
// everything older with a single summary message.
//
// Returns the compressed history and any error. If compression is not
// needed or fails, returns the original history unchanged.
func compressHistory(ctx context.Context, client LLMClient, apiFormat, modelID string, messages []Message, cwd string) ([]Message, error) {
	if !needsCompression(messages, modelID) {
		return messages, nil
	}

	// Split history into old (to compress) and recent (to keep).
	splitPoint := len(messages) - minRecentTurns
	if splitPoint < 1 {
		return messages, nil
	}

	oldTurns := messages[:splitPoint]
	recentTurns := messages[splitPoint:]

	// Generate summary of old turns.
	summary, err := generateSummary(ctx, client, apiFormat, oldTurns)
	if err != nil {
		// If summarization fails, fall back to reactive trimming
		// rather than failing the turn.
		return messages, nil
	}

	// Build compressed history: summary message + recent turns.
	summaryMsg := Message{
		Role: "user",
		Content: []ContentBlock{{
			Type: "text",
			Text: summary,
		}},
	}

	// Also save to persistent supervisor context file.
	if cwd != "" {
		_ = saveSupervisorContext(cwd, summary)
	}

	compressed := make([]Message, 0, 1+len(recentTurns))
	compressed = append(compressed, summaryMsg)
	compressed = append(compressed, recentTurns...)

	return compressed, nil
}

// generateSummary calls the LLM to create a concise summary of old turns.
// The summary preserves: what was asked, what files were changed, what
// decisions were made, what errors were encountered, and what remains to do.
func generateSummary(ctx context.Context, client LLMClient, apiFormat string, turns []Message) (string, error) {
	// Build a compact representation of the old turns for summarization.
	var builder strings.Builder
	builder.WriteString("Summarize the following conversation turns into a concise context block for an AI coding agent.\n\n")
	builder.WriteString("Rules:\n")
	builder.WriteString("- Preserve: what the user asked, files read/modified, key decisions, errors encountered, what remains\n")
	builder.WriteString("- Be specific about file paths, function names, and code changes\n")
	builder.WriteString("- Keep it under 1500 words\n")
	builder.WriteString("- Use markdown formatting\n")
	builder.WriteString("- Omit tool raw output unless it contains critical information\n")
	builder.WriteString("- If there were errors, note what was tried and what failed\n\n")
	builder.WriteString("Conversation turns:\n\n")

	for _, msg := range turns {
		role := msg.Role
		builder.WriteString(fmt.Sprintf("[%s]\n", role))
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				text := block.Text
				if len(text) > 500 {
					text = text[:500] + "... (truncated)"
				}
				builder.WriteString(text)
				builder.WriteString("\n")
			case "tool_use":
				args := string(block.Input)
				if len(args) > 200 {
					args = args[:200] + "... (truncated)"
				}
				builder.WriteString(fmt.Sprintf("Tool call: %s(%s)\n", block.Name, args))
			case "tool_result":
				output := block.Text
				if len(output) > 300 {
					output = output[:300] + "... (truncated)"
				}
				builder.WriteString(fmt.Sprintf("Tool result: %s\n", output))
			case "thinking":
				// Skip thinking blocks for summarization — they're
				// internal reasoning, not useful context.
			}
		}
		builder.WriteString("\n")
	}

	prompt := builder.String()

	// Use a non-streaming call for the summary.
	resp, err := client.Call(ctx, LLMRequest{
		System: "You are a context summarizer for an AI coding agent. Produce concise, factual summaries that preserve critical context.",
		Messages: []Message{{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: prompt,
			}},
		}},
		Stream: false,
	})
	if err != nil {
		return "", fmt.Errorf("history compression LLM call failed: %w", err)
	}

	if len(resp.TextBlocks) == 0 {
		return "", fmt.Errorf("history compression produced no summary")
	}

	summary := strings.TrimSpace(strings.Join(resp.TextBlocks, ""))
	summary = "[Previous conversation summary]\n" + summary

	return summary, nil
}

// supervisorContextPath returns the path to the persistent supervisor
// context file that survives session restarts.
func supervisorContextPath(cwd string) string {
	return filepath.Join(cwd, ".cogent", "supervisor-context.md")
}

// loadSupervisorContext loads the persistent supervisor context from disk.
// Returns empty string if the file doesn't exist.
func loadSupervisorContext(cwd string) (string, error) {
	path := supervisorContextPath(cwd)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", nil
	}
	return content, nil
}

// saveSupervisorContext persists the summary to the supervisor context file.
func saveSupervisorContext(cwd string, summary string) error {
	dir := filepath.Dir(supervisorContextPath(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create supervisor context dir: %w", err)
	}
	// Strip the summary header for the persistent file
	content := strings.TrimPrefix(summary, "[Previous conversation summary]\n")
	content = "# Supervisor Context (auto-generated)\n\n" +
		"This file contains a compressed summary of previous conversation turns.\n" +
		"It is automatically updated when history compression occurs.\n\n" +
		content + "\n"
	return os.WriteFile(supervisorContextPath(cwd), []byte(content), 0o644)
}

// injectSupervisorContext checks for a persistent supervisor context file
// and prepends it as a user message if the session is being resumed and
// has a non-trivial history (indicating a continuation).
func injectSupervisorContext(cwd string, history []Message) []Message {
	if cwd == "" || len(history) == 0 {
		return history
	}
	context, err := loadSupervisorContext(cwd)
	if err != nil || context == "" {
		return history
	}
	// Check if the history already starts with a summary to avoid duplication.
	if len(history) > 0 && len(history[0].Content) > 0 &&
		strings.HasPrefix(history[0].Content[0].Text, "[Previous conversation summary]") {
		return history
	}
	// Prepend the supervisor context as the first message.
	contextMsg := Message{
		Role: "user",
		Content: []ContentBlock{{
			Type: "text",
			Text: "[Previous session context from supervisor-context.md]\n" + context,
		}},
	}
	result := make([]Message, 0, 1+len(history))
	result = append(result, contextMsg)
	result = append(result, history...)
	return result
}

// marshalMessagesSize returns the approximate serialized size of messages in bytes.
// Used for logging and diagnostics.
func marshalMessagesSize(messages []Message) int {
	data, _ := json.Marshal(messages)
	return len(data)
}
