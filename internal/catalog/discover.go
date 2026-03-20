package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/yusefmosiah/fase/internal/adapters"
	"github.com/yusefmosiah/fase/internal/core"
)

type Runner interface {
	CombinedOutput(ctx context.Context, binary string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) CombinedOutput(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	return cmd.CombinedOutput()
}

func Snapshot(ctx context.Context, cfg core.Config, runner Runner) core.CatalogSnapshot {
	if runner == nil {
		runner = ExecRunner{}
	}

	snapshot := core.CatalogSnapshot{
		SnapshotID: core.GenerateID("cat"),
		CreatedAt:  time.Now().UTC(),
		Entries:    []core.CatalogEntry{},
		Issues:     []core.CatalogIssue{},
	}

	for _, diag := range adapters.CatalogFromConfig(cfg) {
		if !diag.Enabled {
			continue
		}
		cfgEntry, ok := cfg.Adapters.ByName(diag.Adapter)
		if !ok {
			continue
		}
		if !diag.Available {
			snapshot.Issues = append(snapshot.Issues, core.CatalogIssue{
				Adapter:  diag.Adapter,
				Severity: "warning",
				Message:  fmt.Sprintf("binary %q is not available on PATH", cfgEntry.Binary),
			})
			continue
		}

		entries, issues := discoverAdapter(ctx, runner, diag.Adapter, cfgEntry.Binary, snapshot.CreatedAt)
		snapshot.Entries = append(snapshot.Entries, entries...)
		snapshot.Issues = append(snapshot.Issues, issues...)
	}

	sort.Slice(snapshot.Entries, func(i, j int) bool {
		left := snapshot.Entries[i]
		right := snapshot.Entries[j]
		if left.Adapter != right.Adapter {
			return left.Adapter < right.Adapter
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		return left.Model < right.Model
	})

	return snapshot
}

func discoverAdapter(ctx context.Context, runner Runner, adapter, binary string, observedAt time.Time) ([]core.CatalogEntry, []core.CatalogIssue) {
	switch adapter {
	case "codex":
		return discoverCodex(ctx, runner, binary, observedAt)
	case "claude":
		return discoverClaude(ctx, runner, binary, observedAt)
	case "gemini":
		return discoverGemini(ctx, runner, binary, observedAt)
	case "opencode":
		return discoverOpenCode(ctx, runner, binary, observedAt)
	case "pi":
		return discoverPi(ctx, runner, binary, observedAt)
	case "factory":
		return discoverFactory(ctx, runner, binary, observedAt)
	default:
		return nil, []core.CatalogIssue{{
			Adapter:  adapter,
			Severity: "warning",
			Message:  "no catalog discoverer implemented",
		}}
	}
}

func discoverCodex(ctx context.Context, runner Runner, binary string, observedAt time.Time) ([]core.CatalogEntry, []core.CatalogIssue) {
	output, err := runner.CombinedOutput(ctx, binary, "login", "status")
	if err != nil {
		return nil, []core.CatalogIssue{{
			Adapter:  "codex",
			Severity: "warning",
			Message:  fmt.Sprintf("codex login status failed: %v", err),
		}}
	}
	text := strings.TrimSpace(stripANSI(string(output)))
	entry := core.CatalogEntry{
		Adapter:   "codex",
		Provider:  "openai",
		Available: true,
		Source:    "cli",
		Provenance: core.CatalogProvenance{
			Source:     "cli",
			Command:    binary + " login status",
			ObservedAt: observedAt,
		},
		Metadata: map[string]any{
			"status_text": text,
		},
	}

	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "chatgpt"):
		entry.AuthMethod = "chatgpt"
		entry.BillingClass = "subscription"
		entry.Selected = true
	case strings.Contains(lower, "api key"):
		entry.AuthMethod = "api_key"
		entry.BillingClass = "metered_api"
		entry.Selected = true
	default:
		entry.AuthMethod = "unknown"
		entry.BillingClass = "unknown"
	}

	return []core.CatalogEntry{entry}, nil
}

func discoverClaude(ctx context.Context, runner Runner, binary string, observedAt time.Time) ([]core.CatalogEntry, []core.CatalogIssue) {
	output, err := runner.CombinedOutput(ctx, binary, "auth", "status")
	if err != nil {
		return nil, []core.CatalogIssue{{
			Adapter:  "claude",
			Severity: "warning",
			Message:  fmt.Sprintf("claude auth status failed: %v", err),
		}}
	}

	var payload struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		APIProvider      string `json:"apiProvider"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return nil, []core.CatalogIssue{{
			Adapter:  "claude",
			Severity: "warning",
			Message:  fmt.Sprintf("parse claude auth status: %v", err),
		}}
	}

	entry := core.CatalogEntry{
		Adapter:      "claude",
		Provider:     providerOrDefault(payload.APIProvider, "anthropic"),
		Available:    payload.LoggedIn,
		AuthMethod:   normalizeClaudeAuthMethod(payload.AuthMethod),
		BillingClass: normalizeClaudeBilling(payload.AuthMethod, payload.APIProvider),
		Selected:     payload.LoggedIn,
		Source:       "cli",
		Provenance: core.CatalogProvenance{
			Source:     "cli",
			Command:    binary + " auth status",
			ObservedAt: observedAt,
		},
		Metadata: map[string]any{
			"subscription_type": payload.SubscriptionType,
		},
	}
	return []core.CatalogEntry{entry}, nil
}

func discoverGemini(_ context.Context, _ Runner, _ string, observedAt time.Time) ([]core.CatalogEntry, []core.CatalogIssue) {
	entry := core.CatalogEntry{
		Adapter:   "gemini",
		Provider:  "google",
		Available: true,
		Source:    "env",
		Provenance: core.CatalogProvenance{
			Source:     "env",
			ObservedAt: observedAt,
		},
	}

	switch {
	case os.Getenv("GOOGLE_GENAI_USE_VERTEXAI") != "" || os.Getenv("GOOGLE_CLOUD_PROJECT") != "":
		entry.AuthMethod = "vertex"
		entry.BillingClass = "cloud_project"
		entry.Selected = true
	case os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_API_KEY") != "":
		entry.AuthMethod = "api_key"
		entry.BillingClass = "metered_api"
		entry.Selected = true
	default:
		entry.AuthMethod = "unknown"
		entry.BillingClass = "unknown"
		entry.Available = false
	}

	return []core.CatalogEntry{entry}, nil
}

func discoverOpenCode(ctx context.Context, runner Runner, binary string, observedAt time.Time) ([]core.CatalogEntry, []core.CatalogIssue) {
	authOutput, authErr := runner.CombinedOutput(ctx, binary, "auth", "list")
	modelOutput, modelErr := runner.CombinedOutput(ctx, binary, "models")
	issues := []core.CatalogIssue{}
	if authErr != nil {
		issues = append(issues, core.CatalogIssue{
			Adapter:  "opencode",
			Severity: "warning",
			Message:  fmt.Sprintf("opencode auth list failed: %v", authErr),
		})
	}
	if modelErr != nil {
		issues = append(issues, core.CatalogIssue{
			Adapter:  "opencode",
			Severity: "warning",
			Message:  fmt.Sprintf("opencode models failed: %v", modelErr),
		})
	}

	authByProvider := parseOpenCodeAuthList(stripANSI(string(authOutput)))
	models := parseOpenCodeModels(stripANSI(string(modelOutput)))
	entries := make([]core.CatalogEntry, 0, len(models))
	for _, model := range models {
		method := authByProvider[model.Provider]
		entry := core.CatalogEntry{
			Adapter:      "opencode",
			Provider:     model.Provider,
			Model:        model.Model,
			Available:    true,
			AuthMethod:   method,
			BillingClass: normalizeOpenCodeBilling(method),
			Source:       "cli",
			Provenance: core.CatalogProvenance{
				Source:     "cli",
				Command:    binary + " models",
				ObservedAt: observedAt,
			},
			Metadata: map[string]any{},
		}
		if method == "oauth" && model.Provider == "openai" {
			entry.Metadata["preferred_account_reuse"] = true
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		entries = append(entries, core.CatalogEntry{
			Adapter:      "opencode",
			Available:    false,
			AuthMethod:   "unknown",
			BillingClass: "unknown",
			Source:       "cli",
			Provenance: core.CatalogProvenance{
				Source:     "cli",
				Command:    binary + " auth list",
				ObservedAt: observedAt,
			},
		})
	}
	return entries, issues
}

func discoverPi(ctx context.Context, runner Runner, binary string, observedAt time.Time) ([]core.CatalogEntry, []core.CatalogIssue) {
	helpOutput, helpErr := runner.CombinedOutput(ctx, binary, "--help")
	modelOutput, modelErr := runner.CombinedOutput(ctx, binary, "--list-models")
	issues := []core.CatalogIssue{}
	if helpErr != nil {
		issues = append(issues, core.CatalogIssue{
			Adapter:  "pi",
			Severity: "warning",
			Message:  fmt.Sprintf("pi --help failed: %v", helpErr),
		})
	}
	if modelErr != nil {
		issues = append(issues, core.CatalogIssue{
			Adapter:  "pi",
			Severity: "warning",
			Message:  fmt.Sprintf("pi --list-models failed: %v", modelErr),
		})
	}

	defaultProvider, defaultModel := parsePiDefaults(stripANSI(string(helpOutput)))
	models := parsePiModelList(stripANSI(string(modelOutput)))
	entries := make([]core.CatalogEntry, 0, len(models))
	for _, model := range models {
		entry := core.CatalogEntry{
			Adapter:      "pi",
			Provider:     model.Provider,
			Model:        model.Model,
			Available:    true,
			AuthMethod:   inferPiAuthMethod(model.Provider),
			BillingClass: inferPiBilling(model.Provider),
			Selected:     model.Provider == defaultProvider && model.Model == defaultModel,
			Source:       "cli",
			Provenance: core.CatalogProvenance{
				Source:     "cli",
				Command:    binary + " --list-models",
				ObservedAt: observedAt,
			},
		}
		entries = append(entries, entry)
	}
	return entries, issues
}

func discoverFactory(ctx context.Context, runner Runner, binary string, observedAt time.Time) ([]core.CatalogEntry, []core.CatalogIssue) {
	output, err := runner.CombinedOutput(ctx, binary, "exec", "--help")
	if err != nil {
		return nil, []core.CatalogIssue{{
			Adapter:  "factory",
			Severity: "warning",
			Message:  fmt.Sprintf("droid exec --help failed: %v", err),
		}}
	}
	defaultModel, models := parseFactoryHelp(stripANSI(string(output)))
	entries := make([]core.CatalogEntry, 0, len(models))
	for _, model := range models {
		entries = append(entries, core.CatalogEntry{
			Adapter:      "factory",
			Provider:     "factory",
			Model:        model,
			Available:    true,
			AuthMethod:   "api_key",
			BillingClass: "metered_api",
			Selected:     model == defaultModel,
			Source:       "cli",
			Provenance: core.CatalogProvenance{
				Source:     "cli",
				Command:    binary + " exec --help",
				ObservedAt: observedAt,
			},
		})
	}
	return entries, nil
}

type providerModel struct {
	Provider string
	Model    string
}

func parseOpenCodeAuthList(text string) map[string]string {
	result := map[string]string{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "Credentials") {
			continue
		}
		if strings.HasPrefix(line, "●") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "●"))
			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}
			method := parts[len(parts)-1]
			provider := normalizeProviderName(strings.Join(parts[:len(parts)-1], " "))
			result[provider] = strings.ToLower(method)
		}
	}
	return result
}

func parseOpenCodeModels(text string) []providerModel {
	var result []providerModel
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.Contains(line, "/") {
			continue
		}
		parts := strings.SplitN(line, "/", 2)
		if len(parts) != 2 {
			continue
		}
		result = append(result, providerModel{Provider: parts[0], Model: parts[1]})
	}
	return result
}

func parsePiDefaults(text string) (string, string) {
	var provider string
	var model string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "--provider ") && strings.Contains(line, "(default:"):
			provider = extractDefaultValue(line)
		case strings.HasPrefix(line, "--model ") && strings.Contains(line, "(default:"):
			model = extractDefaultValue(line)
		}
	}
	return provider, model
}

func parsePiModelList(text string) []providerModel {
	lines := strings.Split(text, "\n")
	var result []providerModel
	for idx, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || idx == 0 || strings.HasPrefix(line, "provider") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		result = append(result, providerModel{Provider: fields[0], Model: fields[1]})
	}
	return result
}

func parseFactoryHelp(text string) (string, []string) {
	defaultModel := ""
	var models []string
	inModels := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "-m, --model") && strings.Contains(line, "(default:") {
			defaultModel = extractDefaultValue(line)
		}
		if strings.HasPrefix(line, "Available Models:") {
			inModels = true
			continue
		}
		if inModels {
			if line == "" || strings.HasSuffix(line, ":") {
				inModels = false
				continue
			}
			fields := strings.Fields(line)
			if len(fields) > 0 {
				models = append(models, fields[0])
			}
		}
	}
	return defaultModel, dedupeStrings(models)
}

func normalizeClaudeAuthMethod(authMethod string) string {
	switch strings.ToLower(strings.TrimSpace(authMethod)) {
	case "claude.ai":
		return "claude_ai"
	case "apikey", "api_key":
		return "api_key"
	default:
		return strings.ToLower(strings.TrimSpace(authMethod))
	}
}

func normalizeClaudeBilling(authMethod, apiProvider string) string {
	switch strings.ToLower(strings.TrimSpace(authMethod)) {
	case "claude.ai":
		return "subscription"
	case "apikey", "api_key":
		if strings.Contains(strings.ToLower(apiProvider), "bedrock") || strings.Contains(strings.ToLower(apiProvider), "vertex") {
			return "cloud_project"
		}
		return "metered_api"
	default:
		if strings.Contains(strings.ToLower(apiProvider), "bedrock") || strings.Contains(strings.ToLower(apiProvider), "vertex") {
			return "cloud_project"
		}
		return "unknown"
	}
}

func normalizeOpenCodeBilling(method string) string {
	switch method {
	case "oauth":
		return "subscription"
	case "api":
		return "metered_api"
	default:
		return "unknown"
	}
}

func inferPiAuthMethod(provider string) string {
	switch provider {
	case "google", "google-antigravity", "anthropic", "openai", "openrouter", "zai", "mistral", "minimax", "bedrock", "github-copilot":
		return "api_key"
	default:
		return "unknown"
	}
}

func inferPiBilling(provider string) string {
	switch provider {
	case "bedrock":
		return "cloud_project"
	case "google-antigravity":
		return "unknown"
	default:
		return "metered_api"
	}
}

func providerOrDefault(value, fallback string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return fallback
	}
	return value
}

func normalizeProviderName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, " ", "-")
	switch value {
	case "openai":
		return "openai"
	case "anthropic":
		return "anthropic"
	case "openrouter":
		return "openrouter"
	case "amazon-bedrock":
		return "amazon-bedrock"
	case "z.ai", "z-ai":
		return "zai"
	case "z.ai-coding-plan", "z-ai-coding-plan":
		return "zai"
	case "kimi-for-coding":
		return "kimi"
	default:
		return value
	}
}

func extractDefaultValue(line string) string {
	start := strings.Index(line, "(default:")
	if start < 0 {
		return ""
	}
	rest := line[start+len("(default:"):]
	end := strings.Index(rest, ")")
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}

func dedupeStrings(items []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(text string) string {
	text = ansiPattern.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\r", "")
	text = string(bytes.TrimSpace([]byte(text)))
	return text
}
