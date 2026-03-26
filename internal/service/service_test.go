package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusefmosiah/fase/internal/core"
)

var (
	testBinaryOnce sync.Once
	testBinaryPath string
	testBinaryErr  error
)

func TestRunPersistsFailedJobForUnavailableAdapter(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)
	setTestExecutable(t)
	setTestExecutable(t)

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.factory]\nbinary = \"/definitely/missing/droid\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	cwd := t.TempDir()
	result, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "factory",
		CWD:          cwd,
		Prompt:       "build milestone 1",
		PromptSource: "prompt",
		Label:        "bootstrap",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Job.State != core.JobStateQueued {
		t.Fatalf("expected queued job state, got %s", result.Job.State)
	}

	status := waitForTerminalStatus(t, svc, result.Job.JobID)
	if status.Job.State != "failed" {
		t.Fatalf("expected failed job state, got %s", status.Job.State)
	}
	if len(status.Events) < 5 {
		t.Fatalf("expected persisted events, got %d", len(status.Events))
	}

	rawLogs, err := svc.RawLogs(context.Background(), result.Job.JobID, 50)
	if err != nil {
		t.Fatalf("RawLogs returned error: %v", err)
	}
	if len(rawLogs) == 0 {
		t.Fatal("expected at least one raw artifact")
	}
	if filepath.Base(rawLogs[0].Path) == "" {
		t.Fatalf("expected raw log path to be populated: %+v", rawLogs[0])
	}
}

func TestRunCompletesWithFakeCodexAdapter(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)
	setTestExecutable(t)
	setTestExecutable(t)

	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "codex"))
	if err != nil {
		t.Fatalf("resolve fake codex path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake codex: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.codex]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	result, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "codex",
		CWD:          t.TempDir(),
		Prompt:       "build milestone 2",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Job.State != core.JobStateQueued {
		t.Fatalf("expected queued job state, got %s", result.Job.State)
	}

	status := waitForTerminalStatus(t, svc, result.Job.JobID)
	if status.Job.State != core.JobStateCompleted {
		t.Fatalf("expected completed status, got %s", status.Job.State)
	}
	if status.Job.NativeSessionID != "codex-session-123" {
		t.Fatalf("expected discovered native session, got %q", status.Job.NativeSessionID)
	}

	rawLogs, err := svc.RawLogs(context.Background(), result.Job.JobID, 100)
	if err != nil {
		t.Fatalf("RawLogs returned error: %v", err)
	}
	if len(rawLogs) == 0 {
		t.Fatal("expected raw logs for fake codex run")
	}

	eventLogs, err := svc.Logs(context.Background(), result.Job.JobID, 100)
	if err != nil {
		t.Fatalf("Logs returned error: %v", err)
	}
	var foundAssistant bool
	for _, event := range eventLogs {
		if event.Kind == "assistant.message" && bytes.Contains(event.Payload, []byte("Codex completed the task")) {
			foundAssistant = true
		}
	}
	if !foundAssistant {
		t.Fatal("expected translated assistant.message event")
	}
	if status.Usage == nil || status.Usage.InputTokens == 0 || status.Usage.OutputTokens == 0 {
		t.Fatalf("expected usage summary, got %+v", status.Usage)
	}
	if status.Cost != nil {
		t.Fatalf("expected no estimated cost without explicit model, got %+v", status.Cost)
	}
}

func TestRunStatusEstimatesCostWhenModelPricingKnown(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "codex"))
	if err != nil {
		t.Fatalf("resolve fake codex path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake codex: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.codex]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	result, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "codex",
		Model:        "gpt-5-nano",
		CWD:          t.TempDir(),
		Prompt:       "build milestone 2",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	status := waitForTerminalStatus(t, svc, result.Job.JobID)
	if status.Cost == nil || status.Cost.TotalCostUSD <= 0 {
		t.Fatalf("expected estimated cost, got %+v", status.Cost)
	}
	if !status.Cost.Estimated {
		t.Fatalf("expected estimated cost, got %+v", status.Cost)
	}
	if status.EstimatedCost == nil || status.EstimatedCost.TotalCostUSD != status.Cost.TotalCostUSD {
		t.Fatalf("expected explicit estimated cost, got %+v", status.EstimatedCost)
	}
	if status.EstimatedCost.ObservedAt == nil {
		t.Fatalf("expected estimated cost provenance observed_at, got %+v", status.EstimatedCost)
	}
}

func TestClaudeRunStatusUsesVendorCost(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "claude"))
	if err != nil {
		t.Fatalf("resolve fake claude path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake claude: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.claude]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	result, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "claude",
		Model:        "claude-sonnet-4-6",
		CWD:          t.TempDir(),
		Prompt:       "build milestone 2",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	status := waitForTerminalStatus(t, svc, result.Job.JobID)
	if status.Cost == nil || status.Cost.TotalCostUSD <= 0 {
		t.Fatalf("expected vendor cost, got %+v", status.Cost)
	}
	if status.Cost.Estimated {
		t.Fatalf("expected vendor-reported cost, got %+v", status.Cost)
	}
	if status.VendorCost == nil || status.VendorCost.TotalCostUSD != status.Cost.TotalCostUSD {
		t.Fatalf("expected explicit vendor cost, got %+v", status.VendorCost)
	}
	if status.EstimatedCost == nil || status.EstimatedCost.TotalCostUSD <= 0 {
		t.Fatalf("expected explicit estimated cost alongside vendor cost, got %+v", status.EstimatedCost)
	}
	if status.EstimatedCost.ObservedAt == nil {
		t.Fatalf("expected estimated cost provenance observed_at, got %+v", status.EstimatedCost)
	}
}

func TestOpenCodeStructuredErrorMarksJobFailed(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "opencode"))
	if err != nil {
		t.Fatalf("resolve fake opencode path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake opencode: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.opencode]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	result, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "opencode",
		Model:        "openai/gpt-5.3-codex-spark",
		CWD:          t.TempDir(),
		Prompt:       "Reply with exactly OK and nothing else.",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	status := waitForTerminalStatus(t, svc, result.Job.JobID)
	if status.Job.State != core.JobStateFailed {
		t.Fatalf("expected failed status, got %s", status.Job.State)
	}
	if !strings.Contains(strings.ToLower(summaryString(status.Job.Summary, "message")), "not supported") {
		t.Fatalf("expected unsupported-model message, got %+v", status.Job.Summary)
	}
}

func TestWaitStatusReturnsTerminalState(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "codex"))
	if err != nil {
		t.Fatalf("resolve fake codex path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake codex: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.codex]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	result, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "codex",
		CWD:          t.TempDir(),
		Prompt:       "slow wait test",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	status, err := svc.WaitStatus(context.Background(), result.Job.JobID, 100*time.Millisecond, 10*time.Second)
	if err != nil {
		t.Fatalf("WaitStatus returned error: %v", err)
	}
	if status.Job.State != core.JobStateCompleted {
		t.Fatalf("expected completed status, got %s", status.Job.State)
	}
}

func TestSendContinuesFakeCodexSession(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "codex"))
	if err != nil {
		t.Fatalf("resolve fake codex path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake codex: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.codex]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	first, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "codex",
		CWD:          t.TempDir(),
		Prompt:       "initial prompt",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	firstStatus := waitForTerminalStatus(t, svc, first.Job.JobID)

	second, err := svc.Send(context.Background(), SendRequest{
		SessionID:    first.Session.SessionID,
		Prompt:       "follow up",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if second.Job.State != core.JobStateQueued {
		t.Fatalf("expected queued send job state, got %s", second.Job.State)
	}
	secondStatus := waitForTerminalStatus(t, svc, second.Job.JobID)
	if secondStatus.Job.State != core.JobStateCompleted {
		t.Fatalf("expected completed send job state, got %s", secondStatus.Job.State)
	}
	if secondStatus.Job.NativeSessionID != firstStatus.Job.NativeSessionID {
		t.Fatalf("expected same native session id, got %q want %q", secondStatus.Job.NativeSessionID, firstStatus.Job.NativeSessionID)
	}
	if got, _ := secondStatus.Job.Summary["message"].(string); !strings.Contains(got, "continued") {
		t.Fatalf("expected continued summary, got %q", got)
	}
}

func TestRunCompletesWithFakeFactoryAdapter(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "droid"))
	if err != nil {
		t.Fatalf("resolve fake droid path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake droid: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.factory]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	result, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "factory",
		CWD:          t.TempDir(),
		Prompt:       "build milestone 3",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	status := waitForTerminalStatus(t, svc, result.Job.JobID)
	if status.Job.State != core.JobStateCompleted {
		t.Fatalf("expected completed factory job state, got %s", status.Job.State)
	}
	if status.Job.NativeSessionID != "factory-session-123" {
		t.Fatalf("expected discovered factory native session, got %q", status.Job.NativeSessionID)
	}
}

func TestRunAndSessionWithFakePiAdapter(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()
	piDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	t.Setenv("PI_CODING_AGENT_DIR", piDir)
	setTestExecutable(t)

	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "pi"))
	if err != nil {
		t.Fatalf("resolve fake pi path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake pi: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.pi]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	first, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "pi",
		CWD:          t.TempDir(),
		Prompt:       "initial pi prompt",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	firstStatus := waitForTerminalStatus(t, svc, first.Job.JobID)
	if firstStatus.Job.NativeSessionID != "pi-session-123" {
		t.Fatalf("expected pi native session id, got %q", firstStatus.Job.NativeSessionID)
	}

	second, err := svc.Send(context.Background(), SendRequest{
		SessionID:    first.Session.SessionID,
		Prompt:       "continue pi prompt",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	secondStatus := waitForTerminalStatus(t, svc, second.Job.JobID)
	if secondStatus.Job.NativeSessionID != firstStatus.Job.NativeSessionID {
		t.Fatalf("expected same pi session id, got %q want %q", secondStatus.Job.NativeSessionID, firstStatus.Job.NativeSessionID)
	}

	session, err := svc.Session(context.Background(), first.Session.SessionID)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	if len(session.NativeSessions) != 1 {
		t.Fatalf("expected one native session, got %d", len(session.NativeSessions))
	}
	if got, _ := session.NativeSessions[0].Metadata["session_path"].(string); !strings.HasSuffix(got, ".jsonl") {
		t.Fatalf("expected pi session_path metadata, got %q", got)
	}
	if len(session.Turns) != 2 {
		t.Fatalf("expected two turns, got %d", len(session.Turns))
	}
	if len(session.Actions) == 0 || !session.Actions[0].Available {
		t.Fatalf("expected available send action, got %+v", session.Actions)
	}
}

func TestRunCompletesWithFakeGeminiAdapter(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "gemini"))
	if err != nil {
		t.Fatalf("resolve fake gemini path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake gemini: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.gemini]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	result, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "gemini",
		CWD:          t.TempDir(),
		Prompt:       "build milestone 4",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	status := waitForTerminalStatus(t, svc, result.Job.JobID)
	if status.Job.State != core.JobStateCompleted {
		t.Fatalf("expected completed gemini job state, got %s", status.Job.State)
	}
	if status.Job.NativeSessionID != "gemini-session-789" {
		t.Fatalf("expected discovered gemini native session, got %q", status.Job.NativeSessionID)
	}
}

func TestSendContinuesFakeOpenCodeSession(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "opencode"))
	if err != nil {
		t.Fatalf("resolve fake opencode path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake opencode: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.opencode]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	first, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "opencode",
		CWD:          t.TempDir(),
		Prompt:       "initial prompt",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	firstStatus := waitForTerminalStatus(t, svc, first.Job.JobID)

	second, err := svc.Send(context.Background(), SendRequest{
		SessionID:    first.Session.SessionID,
		Prompt:       "follow up",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	secondStatus := waitForTerminalStatus(t, svc, second.Job.JobID)
	if secondStatus.Job.NativeSessionID != firstStatus.Job.NativeSessionID {
		t.Fatalf("expected same opencode native session id, got %q want %q", secondStatus.Job.NativeSessionID, firstStatus.Job.NativeSessionID)
	}
}

func TestDebriefContinuesSessionAndWritesArtifact(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "codex"))
	if err != nil {
		t.Fatalf("resolve fake codex path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake codex: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.codex]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	first, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "codex",
		CWD:          t.TempDir(),
		Prompt:       "initial prompt",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	waitForTerminalStatus(t, svc, first.Job.JobID)

	result, err := svc.Debrief(context.Background(), DebriefRequest{
		SessionID: first.Session.SessionID,
		Reason:    "prepare a recovery summary",
	})
	if err != nil {
		t.Fatalf("Debrief returned error: %v", err)
	}
	if result.Path == "" {
		t.Fatal("expected debrief path")
	}

	status := waitForTerminalStatus(t, svc, result.Job.JobID)
	if status.Job.State != core.JobStateCompleted {
		t.Fatalf("expected completed debrief job state, got %s", status.Job.State)
	}

	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("read debrief artifact: %v", err)
	}
	if !strings.Contains(string(data), "# Objective") {
		t.Fatalf("expected markdown debrief artifact, got:\n%s", data)
	}

	artifacts, err := svc.store.ListArtifactsByJob(context.Background(), result.Job.JobID, 10)
	if err != nil {
		t.Fatalf("ListArtifactsByJob returned error: %v", err)
	}
	var foundDebrief bool
	for _, artifact := range artifacts {
		if artifact.Kind == "debrief" && artifact.Path == result.Path {
			foundDebrief = true
		}
	}
	if !foundDebrief {
		t.Fatalf("expected debrief artifact in %+v", artifacts)
	}

	events, err := svc.Logs(context.Background(), result.Job.JobID, 100)
	if err != nil {
		t.Fatalf("Logs returned error: %v", err)
	}
	var foundEvent bool
	for _, event := range events {
		if event.Kind == "debrief.exported" {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatal("expected debrief.exported event")
	}

	listed, err := svc.ListArtifacts(context.Background(), ArtifactsRequest{
		JobID: result.Job.JobID,
		Kind:  "debrief",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListArtifacts returned error: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected one listed debrief artifact, got %+v", listed)
	}

	artifactResult, err := svc.ReadArtifact(context.Background(), listed[0].ArtifactID)
	if err != nil {
		t.Fatalf("ReadArtifact returned error: %v", err)
	}
	if !strings.Contains(artifactResult.Content, "# Recommended Next Step") {
		t.Fatalf("expected debrief content, got:\n%s", artifactResult.Content)
	}
}

func TestRuntimeIncludesAdapterTraits(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.codex]\nbinary = \"codex\"\nenabled = true\nsummary = \"primary code editor\"\nspeed = \"fast\"\ncost = \"high\"\ntags = [\"default\", \"tools\"]\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	report, err := svc.Runtime(context.Background(), "codex")
	if err != nil {
		t.Fatalf("Runtime returned error: %v", err)
	}
	if !report.ConfigPresent {
		t.Fatal("expected config to be marked present")
	}
	if report.ConfigPath != configPath {
		t.Fatalf("expected config path %q, got %q", configPath, report.ConfigPath)
	}
	if len(report.Adapters) != 1 {
		t.Fatalf("expected one adapter, got %d", len(report.Adapters))
	}
	if report.Adapters[0].Speed != "fast" || report.Adapters[0].Cost != "high" {
		t.Fatalf("unexpected runtime traits: %+v", report.Adapters[0])
	}
	if len(report.Adapters[0].Tags) != 2 {
		t.Fatalf("unexpected runtime tags: %+v", report.Adapters[0].Tags)
	}
}

func TestSyncAndShowCatalog(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	t.Setenv("GEMINI_API_KEY", "test-gemini-key")
	setTestExecutable(t)

	fakeCodex, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "codex"))
	if err != nil {
		t.Fatalf("resolve fake codex path: %v", err)
	}
	fakeClaude, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "claude"))
	if err != nil {
		t.Fatalf("resolve fake claude path: %v", err)
	}
	fakeOpenCode, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "opencode"))
	if err != nil {
		t.Fatalf("resolve fake opencode path: %v", err)
	}
	fakePi, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "pi"))
	if err != nil {
		t.Fatalf("resolve fake pi path: %v", err)
	}
	fakeDroid, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "droid"))
	if err != nil {
		t.Fatalf("resolve fake droid path: %v", err)
	}
	fakeGemini, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "gemini"))
	if err != nil {
		t.Fatalf("resolve fake gemini path: %v", err)
	}
	for _, binary := range []string{fakeCodex, fakeClaude, fakeOpenCode, fakePi, fakeDroid, fakeGemini} {
		if err := os.Chmod(binary, 0o755); err != nil {
			t.Fatalf("chmod fake binary: %v", err)
		}
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte(
		"[adapters.codex]\n" +
			"binary = \"" + fakeCodex + "\"\n" +
			"enabled = true\n\n" +
			"[adapters.claude]\n" +
			"binary = \"" + fakeClaude + "\"\n" +
			"enabled = true\n\n" +
			"[adapters.opencode]\n" +
			"binary = \"" + fakeOpenCode + "\"\n" +
			"enabled = true\n\n" +
			"[adapters.pi]\n" +
			"binary = \"" + fakePi + "\"\n" +
			"enabled = true\n\n" +
			"[adapters.factory]\n" +
			"binary = \"" + fakeDroid + "\"\n" +
			"enabled = true\n\n" +
			"[adapters.gemini]\n" +
			"binary = \"" + fakeGemini + "\"\n" +
			"enabled = true\n",
	)
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	synced, err := svc.SyncCatalog(context.Background())
	if err != nil {
		t.Fatalf("SyncCatalog returned error: %v", err)
	}
	if synced.Snapshot.SnapshotID == "" {
		t.Fatal("expected snapshot id")
	}
	if len(synced.Snapshot.Entries) == 0 {
		t.Fatal("expected catalog entries")
	}

	shown, err := svc.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog returned error: %v", err)
	}
	if shown.Snapshot.SnapshotID != synced.Snapshot.SnapshotID {
		t.Fatalf("expected latest snapshot %q, got %q", synced.Snapshot.SnapshotID, shown.Snapshot.SnapshotID)
	}

	assertCatalogEntry := func(adapter, provider, model, authMethod, billing string) {
		t.Helper()
		for _, entry := range shown.Snapshot.Entries {
			if entry.Adapter == adapter && entry.Provider == provider && entry.Model == model {
				if authMethod != "" && entry.AuthMethod != authMethod {
					t.Fatalf("expected auth method %q for %+v, got %q", authMethod, entry, entry.AuthMethod)
				}
				if billing != "" && entry.BillingClass != billing {
					t.Fatalf("expected billing %q for %+v, got %q", billing, entry, entry.BillingClass)
				}
				return
			}
		}
		t.Fatalf("missing catalog entry adapter=%s provider=%s model=%s", adapter, provider, model)
	}

	assertCatalogEntry("codex", "openai", "", "chatgpt", "subscription")
	assertCatalogEntry("claude", "firstparty", "", "claude_ai", "subscription")
	assertCatalogEntry("opencode", "openai", "gpt-5-nano", "oauth", "subscription")
	assertCatalogEntry("pi", "google", "gemini-2.5-flash", "api_key", "metered_api")
	assertCatalogEntry("factory", "factory", "glm-5", "api_key", "metered_api")
	assertCatalogEntry("gemini", "google", "", "api_key", "metered_api")

	for _, entry := range shown.Snapshot.Entries {
		if entry.Adapter == "opencode" && entry.Provider == "openai" && entry.Model == "gpt-5-nano" {
			if entry.Pricing == nil || entry.Pricing.InputUSDPerMTok <= 0 || entry.Pricing.OutputUSDPerMTok <= 0 {
				t.Fatalf("expected pricing on catalog entry, got %+v", entry)
			}
			if len(entry.Traits) == 0 {
				t.Fatalf("expected inferred traits on catalog entry, got %+v", entry)
			}
		}
	}
}

func TestReadyWorkUsesCatalogModelTraitsAndModelPreferences(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("" +
		"[adapters.codex]\n" +
		"binary = \"codex\"\n" +
		"enabled = true\n\n" +
		"[adapters.claude]\n" +
		"binary = \"claude\"\n" +
		"enabled = true\n\n" +
		"[adapters.opencode]\n" +
		"binary = \"opencode\"\n" +
		"enabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	now := time.Now().UTC()
	snapshot := core.CatalogSnapshot{
		SnapshotID: core.GenerateID("snap"),
		CreatedAt:  now,
		Entries: []core.CatalogEntry{
			{Adapter: "codex", Provider: "openai", Model: "gpt-5.4", Available: true},
			{Adapter: "opencode", Provider: "zai-coding-plan", Model: "glm-5", Available: true},
			{Adapter: "claude", Provider: "anthropic", Model: "claude-haiku-4-5", Available: true},
			{Adapter: "opencode", Provider: "opencode", Model: "minimax-m2.5-free", Available: true},
		},
	}
	if err := svc.store.CreateCatalogSnapshot(context.Background(), snapshot); err != nil {
		t.Fatalf("CreateCatalogSnapshot returned error: %v", err)
	}

	planning, err := svc.CreateWork(context.Background(), WorkCreateRequest{
		Title:               "Root planning",
		Objective:           "Use strongest planner",
		Kind:                "plan",
		PreferredModels:     []string{"gpt-5.4"},
		RequiredModelTraits: []string{"planning"},
	})
	if err != nil {
		t.Fatalf("CreateWork planning returned error: %v", err)
	}
	verification, err := svc.CreateWork(context.Background(), WorkCreateRequest{
		Title:               "Long verifier",
		Objective:           "Use glm verifier",
		Kind:                "verify",
		PreferredAdapters:   []string{"opencode"},
		RequiredModelTraits: []string{"verification"},
	})
	if err != nil {
		t.Fatalf("CreateWork verification returned error: %v", err)
	}
	impossible, err := svc.CreateWork(context.Background(), WorkCreateRequest{
		Title:               "Needs multimodal",
		Objective:           "Require missing trait",
		Kind:                "verify",
		RequiredModelTraits: []string{"multimodal"},
	})
	if err != nil {
		t.Fatalf("CreateWork impossible returned error: %v", err)
	}

	items, err := svc.ReadyWork(context.Background(), 20, false)
	if err != nil {
		t.Fatalf("ReadyWork returned error: %v", err)
	}

	seen := map[string]bool{}
	for _, item := range items {
		seen[item.WorkID] = true
	}
	if !seen[planning.WorkID] {
		t.Fatalf("expected planning work to be ready, got %+v", items)
	}
	if !seen[verification.WorkID] {
		t.Fatalf("expected verification work to be ready, got %+v", items)
	}
	if seen[impossible.WorkID] {
		t.Fatalf("did not expect impossible work to be ready, got %+v", items)
	}
}

func TestProbeCatalogClassifiesEntries(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)
	t.Setenv("GEMINI_API_KEY", "test-gemini-key")

	fakeOpenCode, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "opencode"))
	if err != nil {
		t.Fatalf("resolve fake opencode path: %v", err)
	}
	if err := os.Chmod(fakeOpenCode, 0o755); err != nil {
		t.Fatalf("chmod fake opencode: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[store]\nstate_dir = \"" + stateDir + "\"\n\n[adapters.opencode]\nbinary = \"" + fakeOpenCode + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	if _, err := svc.SyncCatalog(context.Background()); err != nil {
		t.Fatalf("SyncCatalog returned error: %v", err)
	}

	result, err := svc.ProbeCatalog(context.Background(), ProbeCatalogRequest{
		Adapter:     "opencode",
		Provider:    "openai",
		Model:       "gpt-5.3-codex-spark",
		CWD:         t.TempDir(),
		Timeout:     2 * time.Second,
		Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("ProbeCatalog returned error: %v", err)
	}

	found := false
	for _, entry := range result.Snapshot.Entries {
		if entry.Adapter == "opencode" && entry.Provider == "openai" && entry.Model == "gpt-5.3-codex-spark" {
			found = true
			if entry.ProbeStatus != "unsupported_by_plan" {
				t.Fatalf("expected unsupported_by_plan, got %+v", entry)
			}
		}
	}
	if !found {
		t.Fatal("missing probed catalog entry")
	}
}

func TestCatalogReflectsRecentModelHistory(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeOpenCode, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "opencode"))
	if err != nil {
		t.Fatalf("resolve fake opencode path: %v", err)
	}
	if err := os.Chmod(fakeOpenCode, 0o755); err != nil {
		t.Fatalf("chmod fake opencode: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[store]\nstate_dir = \"" + stateDir + "\"\n\n[adapters.opencode]\nbinary = \"" + fakeOpenCode + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	if _, err := svc.SyncCatalog(context.Background()); err != nil {
		t.Fatalf("SyncCatalog returned error: %v", err)
	}

	success, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "opencode",
		CWD:          t.TempDir(),
		Prompt:       "Reply with exactly OK and nothing else.",
		PromptSource: "prompt",
		Model:        "openai/gpt-5-nano",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	waitForTerminalStatus(t, svc, success.Job.JobID)

	failed, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "opencode",
		CWD:          t.TempDir(),
		Prompt:       "Reply with exactly OK and nothing else.",
		PromptSource: "prompt",
		Model:        "openai/gpt-5.3-codex-spark",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	waitForTerminalStatus(t, svc, failed.Job.JobID)

	shown, err := svc.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog returned error: %v", err)
	}

	var (
		successEntry *core.CatalogEntry
		failedEntry  *core.CatalogEntry
		successIdx   = -1
		failedIdx    = -1
	)
	for idx := range shown.Snapshot.Entries {
		entry := &shown.Snapshot.Entries[idx]
		if entry.Adapter == "opencode" && entry.Provider == "openai" && entry.Model == "gpt-5-nano" {
			successEntry = entry
			successIdx = idx
		}
		if entry.Adapter == "opencode" && entry.Provider == "openai" && entry.Model == "gpt-5.3-codex-spark" {
			failedEntry = entry
			failedIdx = idx
		}
	}

	if successEntry == nil || failedEntry == nil {
		t.Fatalf("expected both catalog entries, got success=%v failed=%v", successEntry != nil, failedEntry != nil)
	}
	if successEntry.History == nil || successEntry.History.RecentSuccesses == 0 {
		t.Fatalf("expected success history on runnable model, got %+v", successEntry.History)
	}
	if failedEntry.History == nil || failedEntry.History.RecentFailures == 0 {
		t.Fatalf("expected failure history on unsupported model, got %+v", failedEntry.History)
	}
	if successIdx == -1 || failedIdx == -1 || successIdx > failedIdx {
		t.Fatalf("expected successful recent model to sort ahead of failing one, got successIdx=%d failedIdx=%d", successIdx, failedIdx)
	}
}

func TestCatalogUsageRollsUpPerModelHistory(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	snapshot := core.CatalogSnapshot{
		SnapshotID: core.GenerateID("snap"),
		CreatedAt:  time.Now().UTC(),
		Entries: []core.CatalogEntry{
			{Adapter: "claude", Provider: "anthropic", Model: "claude-haiku-4-5", Available: true},
			{Adapter: "claude", Provider: "anthropic", Model: "claude-sonnet-4-6", Available: true},
			{Adapter: "claude", Provider: "anthropic", Model: "", Available: true},
		},
	}
	if err := svc.store.CreateCatalogSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("CreateCatalogSnapshot: %v", err)
	}

	session, job := createTestSessionAndJob(t, svc, core.JobRecord{
		Adapter: "claude",
		State:   core.JobStateCompleted,
		CWD:     t.TempDir(),
		Label:   "catalog usage rollup",
	})
	_ = session
	if err := svc.applyUsageHint(ctx, &job, map[string]any{
		"provider":                    "anthropic",
		"model":                       "multi",
		"input_tokens":                int64(110),
		"output_tokens":               int64(220),
		"total_tokens":                int64(703),
		"cached_input_tokens":         int64(55),
		"cache_read_input_tokens":     int64(123),
		"cache_creation_input_tokens": int64(195),
		"cost_usd":                    1.5,
		"model_usage": []any{
			map[string]any{
				"provider":                    "anthropic",
				"model":                       "claude-haiku-4-5",
				"input_tokens":                int64(10),
				"output_tokens":               int64(20),
				"total_tokens":                int64(67),
				"cached_input_tokens":         int64(5),
				"cache_read_input_tokens":     int64(12),
				"cache_creation_input_tokens": int64(20),
				"cost_usd":                    0.5,
			},
			map[string]any{
				"provider":                    "anthropic",
				"model":                       "claude-sonnet-4-6",
				"input_tokens":                int64(100),
				"output_tokens":               int64(200),
				"total_tokens":                int64(636),
				"cached_input_tokens":         int64(50),
				"cache_read_input_tokens":     int64(111),
				"cache_creation_input_tokens": int64(175),
				"cost_usd":                    1.0,
			},
		},
	}); err != nil {
		t.Fatalf("applyUsageHint: %v", err)
	}

	shown, err := svc.Catalog(ctx)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}

	findHistory := func(model string) *core.CatalogHistory {
		t.Helper()
		for _, entry := range shown.Snapshot.Entries {
			if entry.Adapter == "claude" && entry.Provider == "anthropic" && entry.Model == model {
				return entry.History
			}
		}
		t.Fatalf("missing catalog entry for model %q", model)
		return nil
	}

	haiku := findHistory("claude-haiku-4-5")
	if haiku == nil || haiku.RecentJobs != 1 {
		t.Fatalf("expected one recent haiku job, got %+v", haiku)
	}
	if haiku.TotalInputTokens != 10 || haiku.TotalCachedInputTokens != 5 || haiku.TotalCacheReadInputTokens != 12 || haiku.TotalCacheCreationInputTokens != 20 || haiku.TotalOutputTokens != 20 || haiku.TotalTokens != 67 {
		t.Fatalf("unexpected haiku usage rollup: %+v", haiku)
	}

	sonnet := findHistory("claude-sonnet-4-6")
	if sonnet == nil || sonnet.RecentJobs != 1 {
		t.Fatalf("expected one recent sonnet job, got %+v", sonnet)
	}
	if sonnet.TotalInputTokens != 100 || sonnet.TotalCachedInputTokens != 50 || sonnet.TotalCacheReadInputTokens != 111 || sonnet.TotalCacheCreationInputTokens != 175 || sonnet.TotalOutputTokens != 200 || sonnet.TotalTokens != 636 {
		t.Fatalf("unexpected sonnet usage rollup: %+v", sonnet)
	}

	provider := findHistory("")
	if provider == nil || provider.RecentJobs != 1 {
		t.Fatalf("expected one recent provider-level job, got %+v", provider)
	}
	if provider.TotalInputTokens != 110 || provider.TotalCachedInputTokens != 55 || provider.TotalCacheReadInputTokens != 123 || provider.TotalCacheCreationInputTokens != 195 || provider.TotalOutputTokens != 220 || provider.TotalTokens != 703 {
		t.Fatalf("unexpected provider usage rollup: %+v", provider)
	}
}

func TestStatusUsageAttributionSurvivesRetriesAndVerifierFanout(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	parent, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "usage lineage parent",
		Objective: "track worker and verifier usage",
	})
	if err != nil {
		t.Fatalf("CreateWork parent: %v", err)
	}

	_, workerJobAttempt1 := createTestSessionAndJob(t, svc, core.JobRecord{
		Adapter: "codex",
		WorkID:  parent.WorkID,
		State:   core.JobStateCreated,
		CWD:     t.TempDir(),
		Label:   "usage lineage attempt 1",
	})
	workerSession1, _ := svc.store.GetSession(ctx, workerJobAttempt1.SessionID)
	if err := svc.markWorkQueued(ctx, parent.WorkID, &workerJobAttempt1, workerSession1); err != nil {
		t.Fatalf("markWorkQueued attempt 1: %v", err)
	}
	if err := svc.applyUsageHint(ctx, &workerJobAttempt1, map[string]any{
		"provider":      "openai",
		"model":         "gpt-5-nano",
		"input_tokens":  int64(100),
		"output_tokens": int64(25),
		"total_tokens":  int64(125),
	}); err != nil {
		t.Fatalf("applyUsageHint attempt 1: %v", err)
	}

	parent, err = svc.ResetWork(ctx, WorkResetRequest{
		WorkID:    parent.WorkID,
		Reason:    "retry with verifier fanout",
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("ResetWork: %v", err)
	}

	_, workerJobAttempt2 := createTestSessionAndJob(t, svc, core.JobRecord{
		Adapter: "codex",
		WorkID:  parent.WorkID,
		State:   core.JobStateCreated,
		CWD:     t.TempDir(),
		Label:   "usage lineage attempt 2",
	})
	workerSession2, _ := svc.store.GetSession(ctx, workerJobAttempt2.SessionID)
	if err := svc.markWorkQueued(ctx, parent.WorkID, &workerJobAttempt2, workerSession2); err != nil {
		t.Fatalf("markWorkQueued attempt 2: %v", err)
	}
	if err := svc.applyUsageHint(ctx, &workerJobAttempt2, map[string]any{
		"provider":      "openai",
		"model":         "gpt-5-nano",
		"input_tokens":  int64(200),
		"output_tokens": int64(50),
		"total_tokens":  int64(250),
	}); err != nil {
		t.Fatalf("applyUsageHint attempt 2: %v", err)
	}

	verifierWork, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:        "usage lineage verifier",
		Objective:    "verify accepted attempt",
		Kind:         "attest",
		ParentWorkID: parent.WorkID,
		Metadata: map[string]any{
			"parent_work_id": parent.WorkID,
			"worker_job_id":  workerJobAttempt2.JobID,
			"attempt_epoch":  parent.AttemptEpoch,
		},
	})
	if err != nil {
		t.Fatalf("CreateWork verifier: %v", err)
	}

	_, verifierJob := createTestSessionAndJob(t, svc, core.JobRecord{
		Adapter: "claude",
		WorkID:  verifierWork.WorkID,
		State:   core.JobStateCreated,
		CWD:     t.TempDir(),
		Label:   "usage lineage verifier attempt 2",
	})
	verifierSession, _ := svc.store.GetSession(ctx, verifierJob.SessionID)
	if err := svc.markWorkQueued(ctx, verifierWork.WorkID, &verifierJob, verifierSession); err != nil {
		t.Fatalf("markWorkQueued verifier: %v", err)
	}
	if err := svc.applyUsageHint(ctx, &verifierJob, map[string]any{
		"provider":      "anthropic",
		"model":         "multi",
		"input_tokens":  int64(110),
		"output_tokens": int64(220),
		"total_tokens":  int64(330),
		"cost_usd":      1.5,
		"model_usage": []any{
			map[string]any{"provider": "anthropic", "model": "claude-haiku-4-5", "input_tokens": int64(10), "output_tokens": int64(20), "total_tokens": int64(30), "cost_usd": 0.5},
			map[string]any{"provider": "anthropic", "model": "claude-sonnet-4-6", "input_tokens": int64(100), "output_tokens": int64(200), "total_tokens": int64(300), "cost_usd": 1.0},
		},
	}); err != nil {
		t.Fatalf("applyUsageHint verifier: %v", err)
	}

	attempt1Status, err := svc.Status(ctx, workerJobAttempt1.JobID)
	if err != nil {
		t.Fatalf("Status attempt 1: %v", err)
	}
	if attempt1Status.UsageAttribution == nil || attempt1Status.UsageAttribution.AttemptEpoch != 1 || attempt1Status.UsageAttribution.CurrentAttempt {
		t.Fatalf("expected superseded attempt 1 attribution, got %+v", attempt1Status.UsageAttribution)
	}

	attempt2Status, err := svc.Status(ctx, workerJobAttempt2.JobID)
	if err != nil {
		t.Fatalf("Status attempt 2: %v", err)
	}
	if attempt2Status.UsageAttribution == nil || attempt2Status.UsageAttribution.Role != "worker" || attempt2Status.UsageAttribution.AttemptEpoch != 2 || !attempt2Status.UsageAttribution.CurrentAttempt {
		t.Fatalf("expected current worker attribution, got %+v", attempt2Status.UsageAttribution)
	}

	verifierStatus, err := svc.Status(ctx, verifierJob.JobID)
	if err != nil {
		t.Fatalf("Status verifier: %v", err)
	}
	if verifierStatus.UsageAttribution == nil || verifierStatus.UsageAttribution.Role != "verifier" || verifierStatus.UsageAttribution.AttemptEpoch != 2 || !verifierStatus.UsageAttribution.CurrentAttempt {
		t.Fatalf("expected current verifier attribution, got %+v", verifierStatus.UsageAttribution)
	}
	if verifierStatus.UsageAttribution.ParentWorkID != parent.WorkID || verifierStatus.UsageAttribution.WorkerJobID != workerJobAttempt2.JobID {
		t.Fatalf("expected verifier linkage to accepted attempt, got %+v", verifierStatus.UsageAttribution)
	}
	if len(verifierStatus.UsageByModel) != 2 || verifierStatus.UsageByModel[0].Model != "claude-haiku-4-5" || verifierStatus.UsageByModel[1].Model != "claude-sonnet-4-6" {
		t.Fatalf("expected deterministic verifier model usage ordering, got %+v", verifierStatus.UsageByModel)
	}
	if verifierStatus.Cost == nil || verifierStatus.Cost.Estimated {
		t.Fatalf("expected vendor-selected verifier cost, got %+v", verifierStatus.Cost)
	}
}

func TestHistoryUsageSearchReturnsCanonicalAttribution(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	parent, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "usage history parent",
		Objective: "track usage history attribution",
	})
	if err != nil {
		t.Fatalf("CreateWork parent: %v", err)
	}

	_, workerJobAttempt1 := createTestSessionAndJob(t, svc, core.JobRecord{
		Adapter: "codex",
		WorkID:  parent.WorkID,
		State:   core.JobStateCompleted,
		CWD:     t.TempDir(),
		Label:   "usage lineage history attempt",
	})
	workerSession1, _ := svc.store.GetSession(ctx, workerJobAttempt1.SessionID)
	if err := svc.markWorkQueued(ctx, parent.WorkID, &workerJobAttempt1, workerSession1); err != nil {
		t.Fatalf("markWorkQueued attempt 1: %v", err)
	}
	if err := svc.applyUsageHint(ctx, &workerJobAttempt1, map[string]any{
		"provider":      "openai",
		"model":         "gpt-5-nano",
		"input_tokens":  int64(80),
		"output_tokens": int64(20),
		"total_tokens":  int64(100),
	}); err != nil {
		t.Fatalf("applyUsageHint attempt 1: %v", err)
	}

	parent, err = svc.ResetWork(ctx, WorkResetRequest{
		WorkID:    parent.WorkID,
		Reason:    "retry for usage history",
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("ResetWork: %v", err)
	}

	_, workerJobAttempt2 := createTestSessionAndJob(t, svc, core.JobRecord{
		Adapter: "codex",
		WorkID:  parent.WorkID,
		State:   core.JobStateCompleted,
		CWD:     t.TempDir(),
		Label:   "usage lineage history attempt",
	})
	workerSession2, _ := svc.store.GetSession(ctx, workerJobAttempt2.SessionID)
	if err := svc.markWorkQueued(ctx, parent.WorkID, &workerJobAttempt2, workerSession2); err != nil {
		t.Fatalf("markWorkQueued attempt 2: %v", err)
	}
	if err := svc.applyUsageHint(ctx, &workerJobAttempt2, map[string]any{
		"provider":      "openai",
		"model":         "gpt-5-nano",
		"input_tokens":  int64(160),
		"output_tokens": int64(40),
		"total_tokens":  int64(200),
	}); err != nil {
		t.Fatalf("applyUsageHint attempt 2: %v", err)
	}

	verifierWork, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:        "usage history verifier",
		Objective:    "track verifier usage history",
		Kind:         "attest",
		ParentWorkID: parent.WorkID,
		Metadata: map[string]any{
			"parent_work_id": parent.WorkID,
			"worker_job_id":  workerJobAttempt2.JobID,
			"attempt_epoch":  parent.AttemptEpoch,
		},
	})
	if err != nil {
		t.Fatalf("CreateWork verifier: %v", err)
	}

	_, verifierJob := createTestSessionAndJob(t, svc, core.JobRecord{
		Adapter: "claude",
		WorkID:  verifierWork.WorkID,
		State:   core.JobStateCompleted,
		CWD:     t.TempDir(),
		Label:   "usage lineage history attempt",
	})
	verifierSession, _ := svc.store.GetSession(ctx, verifierJob.SessionID)
	if err := svc.markWorkQueued(ctx, verifierWork.WorkID, &verifierJob, verifierSession); err != nil {
		t.Fatalf("markWorkQueued verifier: %v", err)
	}
	if err := svc.applyUsageHint(ctx, &verifierJob, map[string]any{
		"provider":      "anthropic",
		"model":         "multi",
		"input_tokens":  int64(110),
		"output_tokens": int64(220),
		"total_tokens":  int64(330),
		"cost_usd":      1.5,
		"model_usage": []any{
			map[string]any{"provider": "anthropic", "model": "claude-haiku-4-5", "input_tokens": int64(10), "output_tokens": int64(20), "total_tokens": int64(30), "cost_usd": 0.5},
			map[string]any{"provider": "anthropic", "model": "claude-sonnet-4-6", "input_tokens": int64(100), "output_tokens": int64(200), "total_tokens": int64(300), "cost_usd": 1.0},
		},
	}); err != nil {
		t.Fatalf("applyUsageHint verifier: %v", err)
	}

	result, err := svc.SearchHistory(ctx, HistorySearchRequest{
		Query: "usage lineage history",
		Kinds: []string{"job"},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("SearchHistory all jobs: %v", err)
	}

	byJobID := make(map[string]core.HistoryMatch, len(result.Matches))
	for _, match := range result.Matches {
		byJobID[match.JobID] = match
	}
	if match, ok := byJobID[workerJobAttempt1.JobID]; !ok || match.UsageAttribution == nil || match.UsageAttribution.CurrentAttempt {
		t.Fatalf("expected superseded attempt-1 history attribution, got %+v", match)
	}
	if match, ok := byJobID[workerJobAttempt2.JobID]; !ok || match.UsageAttribution == nil || !match.UsageAttribution.CurrentAttempt {
		t.Fatalf("expected current attempt-2 history attribution, got %+v", match)
	}
	verifierMatch, ok := byJobID[verifierJob.JobID]
	if !ok {
		t.Fatalf("expected verifier history match in %+v", result.Matches)
	}
	if verifierMatch.Usage == nil || verifierMatch.Cost == nil || verifierMatch.UsageAttribution == nil {
		t.Fatalf("expected canonical usage contract on verifier history match, got %+v", verifierMatch)
	}
	if verifierMatch.UsageAttribution.Role != "verifier" || verifierMatch.UsageAttribution.ParentWorkID != parent.WorkID || verifierMatch.UsageAttribution.WorkerJobID != workerJobAttempt2.JobID {
		t.Fatalf("expected verifier linkage on history match, got %+v", verifierMatch.UsageAttribution)
	}
	if len(verifierMatch.UsageByModel) != 2 || verifierMatch.UsageByModel[1].Model != "claude-sonnet-4-6" {
		t.Fatalf("expected deterministic verifier usage_by_model on history match, got %+v", verifierMatch.UsageByModel)
	}

	filtered, err := svc.SearchHistory(ctx, HistorySearchRequest{
		Query: "usage lineage history",
		Kinds: []string{"job"},
		Model: "claude-sonnet-4-6",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("SearchHistory filtered: %v", err)
	}
	if len(filtered.Matches) != 1 || filtered.Matches[0].JobID != verifierJob.JobID {
		t.Fatalf("expected model filter to match multi-model verifier job, got %+v", filtered.Matches)
	}
}

func TestSearchHistoryFindsTurnsAndArtifacts(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeCodex, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "codex"))
	if err != nil {
		t.Fatalf("resolve fake codex path: %v", err)
	}
	if err := os.Chmod(fakeCodex, 0o755); err != nil {
		t.Fatalf("chmod fake codex: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.codex]\nbinary = \"" + fakeCodex + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	run, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "codex",
		CWD:          t.TempDir(),
		Prompt:       "history banana workflow",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	waitForTerminalStatus(t, svc, run.Job.JobID)

	artifactPath := filepath.Join(stateDir, "artifacts", "history-note.md")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte("This artifact contains banana recovery notes."), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	artifact := core.ArtifactRecord{
		ArtifactID: core.GenerateID("art"),
		JobID:      run.Job.JobID,
		SessionID:  run.Session.SessionID,
		Kind:       "debrief_markdown",
		Path:       artifactPath,
		CreatedAt:  time.Now().UTC(),
		Metadata:   map[string]any{"note": "banana"},
	}
	if err := svc.store.InsertArtifact(context.Background(), artifact); err != nil {
		t.Fatalf("InsertArtifact returned error: %v", err)
	}

	result, err := svc.SearchHistory(context.Background(), HistorySearchRequest{
		Query: "banana",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("SearchHistory returned error: %v", err)
	}

	var sawTurn bool
	var sawArtifact bool
	for _, match := range result.Matches {
		switch match.Kind {
		case "turn":
			sawTurn = sawTurn || strings.Contains(strings.ToLower(match.Snippet), "banana")
		case "artifact":
			sawArtifact = sawArtifact || strings.Contains(strings.ToLower(match.Snippet), "banana")
		}
	}
	if !sawTurn {
		t.Fatalf("expected turn match in %+v", result.Matches)
	}
	if !sawArtifact {
		t.Fatalf("expected artifact match in %+v", result.Matches)
	}
}

func TestTransferExportAndRun(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeCodex, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "codex"))
	if err != nil {
		t.Fatalf("resolve fake codex path: %v", err)
	}
	fakeGemini, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "gemini"))
	if err != nil {
		t.Fatalf("resolve fake gemini path: %v", err)
	}
	for _, binary := range []string{fakeCodex, fakeGemini} {
		if err := os.Chmod(binary, 0o755); err != nil {
			t.Fatalf("chmod fake binary: %v", err)
		}
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.codex]\nbinary = \"" + fakeCodex + "\"\nenabled = true\n\n[adapters.gemini]\nbinary = \"" + fakeGemini + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	run, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "codex",
		CWD:          t.TempDir(),
		Prompt:       "solve the problem and summarize it",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	runStatus := waitForTerminalStatus(t, svc, run.Job.JobID)

	exported, err := svc.ExportTransfer(context.Background(), TransferExportRequest{JobID: run.Job.JobID, Reason: "provider outage", Mode: "recovery"})
	if err != nil {
		t.Fatalf("ExportTransfer returned error: %v", err)
	}
	if exported.Transfer.Packet.Source.JobID != run.Job.JobID {
		t.Fatalf("expected transfer source job %s, got %s", run.Job.JobID, exported.Transfer.Packet.Source.JobID)
	}
	if exported.Transfer.Packet.Source.CWD == "" {
		t.Fatal("expected transfer source cwd")
	}
	if len(exported.Transfer.Packet.RecentTurnsInline) == 0 {
		t.Fatal("expected transfer inline turns")
	}
	if exported.Path == "" {
		t.Fatal("expected transfer path")
	}

	continued, err := svc.RunTransfer(context.Background(), TransferRunRequest{
		TransferRef: exported.Transfer.TransferID,
		Adapter:     "gemini",
		CWD:         t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RunTransfer returned error: %v", err)
	}
	continuedStatus := waitForTerminalStatus(t, svc, continued.Job.JobID)
	if continuedStatus.Job.Adapter != "gemini" {
		t.Fatalf("expected gemini target adapter, got %s", continuedStatus.Job.Adapter)
	}
	if continuedStatus.Job.Summary["transfer_id"] != exported.Transfer.TransferID {
		t.Fatalf("expected transfer id in job summary, got %+v", continuedStatus.Job.Summary)
	}
	if runStatus.Job.State != core.JobStateCompleted {
		t.Fatalf("expected completed source job state, got %s", runStatus.Job.State)
	}
}

func TestExportAndRunTransfer(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	fakeCodex, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "codex"))
	if err != nil {
		t.Fatalf("resolve fake codex path: %v", err)
	}
	if err := os.Chmod(fakeCodex, 0o755); err != nil {
		t.Fatalf("chmod fake codex: %v", err)
	}
	fakeDroid, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "droid"))
	if err != nil {
		t.Fatalf("resolve fake droid path: %v", err)
	}
	if err := os.Chmod(fakeDroid, 0o755); err != nil {
		t.Fatalf("chmod fake droid: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte(
		"[adapters.codex]\n" +
			"binary = \"" + fakeCodex + "\"\n" +
			"enabled = true\n\n" +
			"[adapters.factory]\n" +
			"binary = \"" + fakeDroid + "\"\n" +
			"enabled = true\n",
	)
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	source, err := svc.Run(context.Background(), RunRequest{
		Adapter:      "codex",
		CWD:          t.TempDir(),
		Prompt:       "build a transfer source run",
		PromptSource: "prompt",
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	sourceStatus := waitForTerminalStatus(t, svc, source.Job.JobID)

	exported, err := svc.ExportTransfer(context.Background(), TransferExportRequest{
		JobID: source.Job.JobID,
	})
	if err != nil {
		t.Fatalf("ExportTransfer returned error: %v", err)
	}
	if exported.Transfer.Packet.Source.JobID != source.Job.JobID {
		t.Fatalf("expected exported source job %q, got %q", source.Job.JobID, exported.Transfer.Packet.Source.JobID)
	}
	if _, err := os.Stat(exported.Path); err != nil {
		t.Fatalf("expected exported transfer file at %q: %v", exported.Path, err)
	}

	target, err := svc.RunTransfer(context.Background(), TransferRunRequest{
		TransferRef: exported.Transfer.TransferID,
		Adapter:     "factory",
	})
	if err != nil {
		t.Fatalf("RunTransfer returned error: %v", err)
	}
	targetStatus := waitForTerminalStatus(t, svc, target.Job.JobID)
	if targetStatus.Job.State != core.JobStateCompleted {
		t.Fatalf("expected completed transfer run state, got %s", targetStatus.Job.State)
	}
	if targetStatus.Job.Adapter != "factory" {
		t.Fatalf("expected factory adapter, got %q", targetStatus.Job.Adapter)
	}
	if got, _ := targetStatus.Job.Summary["transfer_id"].(string); got != exported.Transfer.TransferID {
		t.Fatalf("expected transfer id %q in summary, got %q", exported.Transfer.TransferID, got)
	}
	if target.Session.ParentSession == nil || *target.Session.ParentSession != source.Session.SessionID {
		t.Fatalf("expected parent session %q, got %+v", source.Session.SessionID, target.Session.ParentSession)
	}
	if sourceStatus.Job.State != core.JobStateCompleted {
		t.Fatalf("expected completed source transfer job, got %s", sourceStatus.Job.State)
	}
}

func TestDetachedWorkerEnvIncludesRuntimePaths(t *testing.T) {
	svc := &Service{
		ConfigPath: "/tmp/fase-config/config.toml",
		Paths: core.Paths{
			StateDir: "/tmp/fase-state",
			CacheDir: "/tmp/fase-cache",
		},
	}

	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("EXISTING_VAR", "present")

	env := svc.detachedWorkerEnv("/opt/fase/bin/fase")
	envMap := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		envMap[key] = value
	}

	if got := envMap["FASE_EXECUTABLE"]; got != "/opt/fase/bin/fase" {
		t.Fatalf("expected executable path to be propagated, got %q", got)
	}
	if got := envMap["FASE_CONFIG_DIR"]; got != "/tmp/fase-config" {
		t.Fatalf("expected config dir to be propagated, got %q", got)
	}
	if got := envMap["FASE_STATE_DIR"]; got != "/tmp/fase-state" {
		t.Fatalf("expected state dir to be propagated, got %q", got)
	}
	if got := envMap["FASE_CACHE_DIR"]; got != "/tmp/fase-cache" {
		t.Fatalf("expected cache dir to be propagated, got %q", got)
	}
	if got := envMap["EXISTING_VAR"]; got != "present" {
		t.Fatalf("expected existing env var to be preserved, got %q", got)
	}
	if got := envMap["PATH"]; got != "/opt/fase/bin:/usr/bin:/bin" {
		t.Fatalf("expected PATH to be prefixed with executable dir, got %q", got)
	}
}

func TestDetachedExecutablePathPrefersCurrentBinaryOutsideGoTest(t *testing.T) {
	t.Setenv("FASE_EXECUTABLE", "/stale/fase")
	original := osExecutable
	osExecutable = func() (string, error) { return "/opt/fase/bin/fase", nil }
	defer func() { osExecutable = original }()

	got, err := detachedExecutablePath()
	if err != nil {
		t.Fatalf("detachedExecutablePath returned error: %v", err)
	}
	if got != "/opt/fase/bin/fase" {
		t.Fatalf("expected current executable to win over stale env override, got %q", got)
	}
}

func TestDiagnosticMessageHandlesStringError(t *testing.T) {
	got := diagnosticMessage(map[string]any{"error": "provider exploded"})
	if got != "provider exploded" {
		t.Fatalf("expected string error to be returned, got %q", got)
	}
}

func TestInspectBootstrapClassifiesStandardProject(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "AGENTS.md"), "# instructions\n")
	mustWriteFile(t, filepath.Join(root, "README.md"), "# readme\n")
	mustWriteFile(t, filepath.Join(root, "go.mod"), "module example.com/test\n")

	svc := &Service{}
	assessment, err := svc.InspectBootstrap(context.Background(), BootstrapInspectRequest{
		Paths: []string{root},
	})
	if err != nil {
		t.Fatalf("InspectBootstrap returned error: %v", err)
	}
	if !assessment.BootstrapReady {
		t.Fatalf("expected bootstrap_ready=true, got false: %+v", assessment)
	}
	if len(assessment.Entrypoints) < 2 {
		t.Fatalf("expected multiple entrypoints, got %+v", assessment.Entrypoints)
	}
}

func TestBootstrapCreateSeedsWorkAndBootstrapNote(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	projectRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(projectRoot, "README.md"), "# readme\n")
	mustWriteFile(t, filepath.Join(projectRoot, "AGENTS.md"), "# agents\n")
	mustWriteFile(t, filepath.Join(projectRoot, "package.json"), "{\n  \"name\": \"bootstrap-test\"\n}\n")

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	result, err := svc.BootstrapCreate(context.Background(), BootstrapCreateRequest{
		Paths: []string{projectRoot},
		Title: "Bootstrap test",
	})
	if err != nil {
		t.Fatalf("BootstrapCreate returned error: %v", err)
	}
	if result.Work.WorkID == "" {
		t.Fatalf("expected work id, got %+v", result.Work)
	}
	show, err := svc.Work(context.Background(), result.Work.WorkID)
	if err != nil {
		t.Fatalf("Work returned error: %v", err)
	}
	if len(show.Notes) == 0 {
		t.Fatalf("expected bootstrap note, got none")
	}
	if !strings.Contains(show.Notes[0].Body, "bootstrap roots:") {
		t.Fatalf("expected bootstrap note body, got %q", show.Notes[0].Body)
	}
}

func TestReviewWorkProposalRejectsSecondParentEdge(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	rootA, err := svc.CreateWork(context.Background(), WorkCreateRequest{
		Title:     "Root A",
		Objective: "first root",
		Kind:      "feature",
	})
	if err != nil {
		t.Fatalf("CreateWork rootA returned error: %v", err)
	}
	rootB, err := svc.CreateWork(context.Background(), WorkCreateRequest{
		Title:     "Root B",
		Objective: "second root",
		Kind:      "feature",
	})
	if err != nil {
		t.Fatalf("CreateWork rootB returned error: %v", err)
	}
	child, err := svc.CreateWork(context.Background(), WorkCreateRequest{
		Title:        "Child",
		Objective:    "child work",
		Kind:         "task",
		ParentWorkID: rootA.WorkID,
	})
	if err != nil {
		t.Fatalf("CreateWork child returned error: %v", err)
	}

	proposal, err := svc.CreateWorkProposal(context.Background(), WorkProposalCreateRequest{
		ProposalType: "add_edge",
		Rationale:    "try to add a second parent",
		Patch: map[string]any{
			"from_work_id": rootB.WorkID,
			"to_work_id":   child.WorkID,
			"edge_type":    "parent_of",
		},
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("CreateWorkProposal returned error: %v", err)
	}

	if _, _, err := svc.ReviewWorkProposal(context.Background(), proposal.ProposalID, "accept"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for second parent edge, got %v", err)
	}
}

func TestReviewWorkProposalRejectsParentCycleOnReparent(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	root, err := svc.CreateWork(context.Background(), WorkCreateRequest{
		Title:     "Root",
		Objective: "root work",
		Kind:      "feature",
	})
	if err != nil {
		t.Fatalf("CreateWork root returned error: %v", err)
	}
	child, err := svc.CreateWork(context.Background(), WorkCreateRequest{
		Title:        "Child",
		Objective:    "child work",
		Kind:         "task",
		ParentWorkID: root.WorkID,
	})
	if err != nil {
		t.Fatalf("CreateWork child returned error: %v", err)
	}
	grandchild, err := svc.CreateWork(context.Background(), WorkCreateRequest{
		Title:        "Grandchild",
		Objective:    "grandchild work",
		Kind:         "task",
		ParentWorkID: child.WorkID,
	})
	if err != nil {
		t.Fatalf("CreateWork grandchild returned error: %v", err)
	}

	proposal, err := svc.CreateWorkProposal(context.Background(), WorkProposalCreateRequest{
		ProposalType: "reparent_work",
		TargetWorkID: root.WorkID,
		Rationale:    "this should be rejected because it creates a cycle",
		Patch: map[string]any{
			"parent_work_id": grandchild.WorkID,
		},
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("CreateWorkProposal returned error: %v", err)
	}

	if _, _, err := svc.ReviewWorkProposal(context.Background(), proposal.ProposalID, "accept"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for parent cycle, got %v", err)
	}
}

func TestAttestationSignatureFieldsRoundTrip(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)

	svc, err := Open(context.Background(), "")
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	work, err := svc.CreateWork(context.Background(), WorkCreateRequest{
		Title:     "sign attestation",
		Objective: "exercise attestation signature persistence",
	})
	if err != nil {
		t.Fatalf("CreateWork returned error: %v", err)
	}

	record := core.AttestationRecord{
		AttestationID: core.GenerateID("attest"),
		SubjectKind:   "work",
		SubjectID:     work.WorkID,
		Result:        "passed",
		Summary:       "signature persistence",
		SignerPubkey:  "pubkey-b64",
		Signature:     "signature-b64",
		CreatedBy:     "test",
		CreatedAt:     time.Now().UTC(),
	}
	if err := svc.store.CreateAttestationRecord(context.Background(), record); err != nil {
		t.Fatalf("CreateAttestationRecord returned error: %v", err)
	}

	records, err := svc.store.ListAttestationRecords(context.Background(), "work", work.WorkID, 10)
	if err != nil {
		t.Fatalf("ListAttestationRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 attestation record, got %d", len(records))
	}
	if records[0].SignerPubkey != record.SignerPubkey || records[0].Signature != record.Signature {
		t.Fatalf("expected signature fields to round-trip, got %+v", records[0])
	}
}

func setTestExecutable(t *testing.T) {
	t.Helper()

	testBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fase-service-test-*")
		if err != nil {
			testBinaryErr = err
			return
		}
		testBinaryPath = filepath.Join(dir, "fase")
		cmd := exec.Command("go", "build", "-o", testBinaryPath, "./cmd/fase")
		cmd.Dir = filepath.Join("..", "..")
		output, err := cmd.CombinedOutput()
		if err != nil {
			testBinaryErr = errors.New(string(output))
			return
		}
	})
	if testBinaryErr != nil {
		t.Fatalf("build fase binary: %v", testBinaryErr)
	}

	t.Setenv("FASE_EXECUTABLE", testBinaryPath)
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func waitForTerminalStatus(t *testing.T, svc *Service, jobID string) *StatusResult {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	var last *StatusResult
	for time.Now().Before(deadline) {
		status, err := svc.Status(context.Background(), jobID)
		if err != nil {
			t.Fatalf("Status returned error: %v", err)
		}
		last = status
		if status.Job.State.Terminal() {
			return status
		}
		time.Sleep(100 * time.Millisecond)
	}

	if last != nil {
		t.Fatalf("job %s did not reach a terminal state; last state=%s events=%d", jobID, last.Job.State, len(last.Events))
	}
	t.Fatalf("job %s did not reach a terminal state", jobID)
	return nil
}

// TestCheckRecordFlow tests CreateCheckRecord, GetCheckRecord, ListCheckRecords,
// and CreateCheckRecordDirect through the service layer end-to-end.
func TestCheckRecordFlow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "check record test",
		Objective: "verify check record CRUD",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	// Create a passing check record.
	pass, err := svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{
		WorkID:       work.WorkID,
		Result:       "pass",
		CheckerModel: "claude-haiku-4-5",
		WorkerModel:  "glm-5-turbo",
		Report: core.CheckReport{
			BuildOK:      true,
			TestsPassed:  3,
			TestOutput:   "go test ./internal/service\nok\tgithub.com/yusefmosiah/fase/internal/service\t0.123s",
			CheckerNotes: "all good",
		},
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("CreateCheckRecord pass: %v", err)
	}
	if pass.CheckID == "" {
		t.Fatal("expected non-empty check_id")
	}
	if pass.Result != "pass" {
		t.Fatalf("expected result=pass, got %q", pass.Result)
	}
	if pass.Report.BuildOK != true {
		t.Fatal("expected build_ok=true")
	}
	if pass.Report.TestsPassed != 3 {
		t.Fatalf("expected tests_passed=3, got %d", pass.Report.TestsPassed)
	}

	// Create a failing check record.
	fail, err := svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{
		WorkID:    work.WorkID,
		Result:    "fail",
		CreatedBy: "test",
		Report:    core.CheckReport{BuildOK: false, TestsFailed: 1},
	})
	if err != nil {
		t.Fatalf("CreateCheckRecord fail: %v", err)
	}
	if fail.Result != "fail" {
		t.Fatalf("expected result=fail, got %q", fail.Result)
	}

	// GetCheckRecord round-trips all fields.
	got, err := svc.GetCheckRecord(ctx, pass.CheckID)
	if err != nil {
		t.Fatalf("GetCheckRecord: %v", err)
	}
	if got.CheckID != pass.CheckID {
		t.Fatalf("expected check_id=%q, got %q", pass.CheckID, got.CheckID)
	}
	if got.Report.CheckerNotes != "all good" {
		t.Fatalf("expected checker_notes='all good', got %q", got.Report.CheckerNotes)
	}

	// ListCheckRecords returns both records newest-first.
	records, err := svc.ListCheckRecords(ctx, work.WorkID, 10)
	if err != nil {
		t.Fatalf("ListCheckRecords: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	// Newest first — fail was created after pass.
	if records[0].Result != "fail" {
		t.Fatalf("expected newest record to be fail, got %q", records[0].Result)
	}

	// CreateCheckRecordDirect is the native adapter bridge — same semantics.
	direct, err := svc.CreateCheckRecordDirect(ctx, work.WorkID, "pass", "glm-5-turbo", "gpt-5.4-mini", core.CheckReport{
		BuildOK:      true,
		TestsPassed:  5,
		TestOutput:   "go test ./...\nok\tgithub.com/yusefmosiah/fase/internal/service\t0.321s",
		CheckerNotes: "via direct bridge",
	}, "worker")
	if err != nil {
		t.Fatalf("CreateCheckRecordDirect: %v", err)
	}
	if direct.Result != "pass" {
		t.Fatalf("expected result=pass from direct bridge, got %q", direct.Result)
	}

	// Validation: empty work_id is rejected.
	_, err = svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{WorkID: "", Result: "pass"})
	if err == nil {
		t.Fatal("expected error for empty work_id")
	}

	// Validation: invalid result is rejected.
	_, err = svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{WorkID: work.WorkID, Result: "unknown"})
	if err == nil {
		t.Fatal("expected error for invalid result")
	}
}

func TestCreateCheckRecordRejectsPassWithoutBuild(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "backend-only verification",
		Objective: "verify check record validation",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	_, err = svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{
		WorkID: work.WorkID,
		Result: "pass",
		Report: core.CheckReport{
			BuildOK:     false,
			TestsPassed: 1,
		},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for pass without build_ok, got %v", err)
	}
}

func TestCreateCheckRecordRejectsPassWithoutCommandProvenance(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "command provenance required",
		Objective: "verify canonical check evidence",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	_, err = svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{
		WorkID: work.WorkID,
		Result: "pass",
		Report: core.CheckReport{
			BuildOK:      true,
			TestsPassed:  1,
			CheckerNotes: "verified the change",
		},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for missing test_output, got %v", err)
	}
}

func TestCreateCheckRecordRejectsMissingDeliverableEvidence(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "deliverable evidence required",
		Objective: "verify mind-graph/index.html and docs/spec-check-flow.md",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	_, err = svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{
		WorkID: work.WorkID,
		Result: "pass",
		Report: core.CheckReport{
			BuildOK:      true,
			TestsPassed:  2,
			TestOutput:   "go test ./internal/service\nok\tgithub.com/yusefmosiah/fase/internal/service\t0.234s",
			CheckerNotes: "verified screenshots and commands, but omitted explicit deliverable paths",
		},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for missing deliverable evidence, got %v", err)
	}
}

func TestCreateCheckRecordRequiresScreenshotsForUIWork(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "verify mind-graph UI",
		Objective: "confirm mind-graph/index.html renders correctly",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	_, err = svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{
		WorkID: work.WorkID,
		Result: "pass",
		Report: core.CheckReport{
			BuildOK:      true,
			TestsPassed:  1,
			TestOutput:   "go test ./mind-graph\nok\tmind-graph\t0.100s",
			DiffStat:     " mind-graph/index.html | 12 +++++++-----",
			CheckerNotes: "verified mind-graph/index.html render output",
		},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for UI pass without screenshots, got %v", err)
	}
}

func TestCreateCheckRecordRequiresScreenshotsForUITaggedWork(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "dashboard polish",
		Objective: "update the board cards",
		Metadata: map[string]any{
			"tags": []string{"ui"},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	_, err = svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{
		WorkID: work.WorkID,
		Result: "pass",
		Report: core.CheckReport{
			BuildOK:      true,
			TestsPassed:  1,
			TestOutput:   "npm test\n1 passed",
			CheckerNotes: "verified UI-tagged work evidence",
		},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for UI-tagged pass without screenshots, got %v", err)
	}
}

func TestCreateCheckRecordRejectsMissingArtifactPaths(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "artifact path validation",
		Objective: "verify checker evidence paths",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	_, err = svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{
		WorkID: work.WorkID,
		Result: "fail",
		Report: core.CheckReport{
			BuildOK:     false,
			TestsFailed: 1,
			Screenshots: []string{filepath.Join(t.TempDir(), "missing.png")},
		},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for missing screenshot path, got %v", err)
	}
}

func TestCreateCheckRecordPersistsTextArtifacts(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "artifact persistence",
		Objective: "verify durable checker artifacts",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	record, err := svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{
		WorkID: work.WorkID,
		Result: "pass",
		Report: core.CheckReport{
			BuildOK:      true,
			TestsPassed:  2,
			TestOutput:   "go test ./internal/service\nok\tgithub.com/yusefmosiah/fase/internal/service\t0.456s",
			DiffStat:     " internal/service/service.go | 12 +++++++++---",
			CheckerNotes: "verified internal/service/service.go exists and recorded durable evidence",
		},
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("CreateCheckRecord: %v", err)
	}
	if record.Result != "pass" {
		t.Fatalf("expected pass result, got %q", record.Result)
	}

	artifactDir := filepath.Join(svc.Paths.StateDir, "artifacts", work.WorkID)
	for name, want := range map[string]string{
		"go-test-output.txt": record.Report.TestOutput,
		"diff-stat.txt":      record.Report.DiffStat,
		"checker-notes.md":   record.Report.CheckerNotes,
	} {
		got, readErr := os.ReadFile(filepath.Join(artifactDir, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestWorkShowIncludesCanonicalReviewBundle(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "review bundle",
		Objective: "inspect canonical review bundle",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	now := time.Now().UTC()
	session := core.SessionRecord{
		SessionID:     "sess_review_bundle",
		Label:         "review bundle session",
		CreatedAt:     now,
		UpdatedAt:     now,
		Status:        "completed",
		OriginAdapter: "codex",
		CWD:           t.TempDir(),
		Metadata:      map[string]any{},
	}
	if err := svc.store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	job := core.JobRecord{
		JobID:     core.GenerateID("job"),
		SessionID: session.SessionID,
		WorkID:    work.WorkID,
		Adapter:   "codex",
		State:     core.JobStateCompleted,
		CWD:       session.CWD,
		CreatedAt: now,
		UpdatedAt: now,
		Summary:   map[string]any{"message": "implemented review bundle"},
	}
	if err := svc.store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	check, err := svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{
		WorkID:       work.WorkID,
		Result:       "pass",
		CheckerModel: "claude-sonnet-4-6",
		WorkerModel:  "glm-5-turbo",
		Report: core.CheckReport{
			BuildOK:      true,
			TestsPassed:  2,
			TestOutput:   "go test ./internal/service\nok\tgithub.com/yusefmosiah/fase/internal/service\t0.101s",
			CheckerNotes: "verified canonical review bundle evidence",
		},
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("CreateCheckRecord: %v", err)
	}

	attestation, _, err := svc.AttestWork(ctx, WorkAttestRequest{
		WorkID:       work.WorkID,
		Result:       "passed",
		Summary:      "review gate resolved",
		VerifierKind: "attestation",
		Method:       "automated_review",
		CreatedBy:    "test",
	})
	if err != nil {
		t.Fatalf("AttestWork: %v", err)
	}

	if _, _, err := svc.SetDocContent(ctx, work.WorkID, "docs/review-bundle.md", "Review Bundle", "# Review\n", "markdown"); err != nil {
		t.Fatalf("SetDocContent: %v", err)
	}

	artifactPath := filepath.Join(t.TempDir(), "review.txt")
	if err := os.WriteFile(artifactPath, []byte("review artifact"), 0o644); err != nil {
		t.Fatalf("WriteFile artifact: %v", err)
	}
	if err := svc.store.InsertArtifact(ctx, core.ArtifactRecord{
		ArtifactID: core.GenerateID("art"),
		JobID:      job.JobID,
		SessionID:  job.SessionID,
		Kind:       "review_note",
		Path:       artifactPath,
		CreatedAt:  time.Now().UTC(),
		Metadata:   map[string]any{"work_id": work.WorkID},
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	show, err := svc.Work(ctx, work.WorkID)
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if len(show.CheckRecords) != 1 || show.CheckRecords[0].CheckID != check.CheckID {
		t.Fatalf("expected check record in work show, got %+v", show.CheckRecords)
	}
	if len(show.Attestations) != 1 || show.Attestations[0].AttestationID != attestation.AttestationID {
		t.Fatalf("expected attestation in work show, got %+v", show.Attestations)
	}
	if len(show.Artifacts) == 0 {
		t.Fatalf("expected artifacts in work show, got %+v", show.Artifacts)
	}
	if len(show.Docs) != 1 || show.Docs[0].Path != "docs/review-bundle.md" {
		t.Fatalf("expected doc in work show, got %+v", show.Docs)
	}
}

func TestSupervisorReviewGuidanceKeepsChecksEvidenceOnly(t *testing.T) {
	prompt := supervisorRolePrompt()
	if !strings.Contains(prompt, "use work_show to review the canonical evidence bundle") {
		t.Fatalf("supervisor prompt should direct reviewers to work_show, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "call work update <id> --execution-state done (emails automatically)") {
		t.Fatalf("supervisor prompt still teaches check-pass=>done, got:\n%s", prompt)
	}

	checkFlow, ok := supervisorDispatchProtocol()["check_flow"].([]string)
	if !ok {
		t.Fatalf("check_flow missing or wrong type: %#v", supervisorDispatchProtocol()["check_flow"])
	}
	joined := strings.Join(checkFlow, "\n")
	for _, want := range []string{
		"Call work_show <work-id> to review the canonical evidence bundle",
		"Passing checks are evidence only",
		"Checks never authorize done on their own",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("check flow missing %q:\n%s", want, joined)
		}
	}
	for _, old := range []string{
		"2. If result is 'pass': call 'fase work update <work-id> --execution-state done'. This emails automatically with the report.",
		"Only mark work as done when a check passes.",
	} {
		if strings.Contains(joined, old) {
			t.Fatalf("check flow still contains legacy completion guidance %q:\n%s", old, joined)
		}
	}
}

func TestAttestationChildRuntimePinsUITaggedWorkToStrongModels(t *testing.T) {
	svc := newTestService(t)
	parent := core.WorkItemRecord{
		WorkID:    "work_ui_456",
		Title:     "UI attestation",
		Objective: "verify mind-graph/index.html",
		Metadata: map[string]any{
			"tags": []string{"ui"},
		},
	}

	adapters, models := svc.attestationChildRuntime(parent, "native", 0)
	if len(adapters) != 1 || adapters[0] != "claude" {
		t.Fatalf("expected claude adapter for UI work, got %v", adapters)
	}
	if len(models) != 2 || models[0] != "claude-opus-4-6" || models[1] != "claude-sonnet-4-6" {
		t.Fatalf("expected strong multimodal model preference for UI work, got %v", models)
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()
	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	svc, err := Open(context.Background(), "")
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func newRepoBackedTestService(t *testing.T) (*Service, string) {
	t.Helper()
	repoRoot := t.TempDir()
	stateDir := filepath.Join(repoRoot, ".fase")
	configDir := t.TempDir()
	cacheDir := t.TempDir()
	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	svc, err := Open(context.Background(), "")
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc, repoRoot
}

func createTestSessionAndJob(t *testing.T, svc *Service, job core.JobRecord) (core.SessionRecord, core.JobRecord) {
	t.Helper()
	now := time.Now().UTC()
	if job.JobID == "" {
		job.JobID = core.GenerateID("job")
	}
	if job.SessionID == "" {
		job.SessionID = core.GenerateID("ses")
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = now
	}
	if job.Summary == nil {
		job.Summary = map[string]any{}
	}
	session := core.SessionRecord{
		SessionID:     job.SessionID,
		Label:         "test session",
		CreatedAt:     now,
		UpdatedAt:     now,
		Status:        "active",
		OriginAdapter: job.Adapter,
		OriginJobID:   job.JobID,
		CWD:           job.CWD,
		LatestJobID:   job.JobID,
		Tags:          []string{},
		Metadata:      map[string]any{},
	}
	if err := svc.store.CreateSessionAndJob(context.Background(), session, job); err != nil {
		t.Fatalf("CreateSessionAndJob: %v", err)
	}
	return session, job
}

func TestSetDocContentNormalizesAuthoritativeRepoPath(t *testing.T) {
	svc, repoRoot := newRepoBackedTestService(t)
	ctx := context.Background()

	repoFile := filepath.Join(repoRoot, "docs", "review-bundle.md")
	mustWriteFile(t, repoFile, "# Review\n")

	doc, workID, err := svc.SetDocContent(ctx, "", repoFile, "", "# Review\n", "markdown")
	if err != nil {
		t.Fatalf("SetDocContent: %v", err)
	}
	if workID == "" {
		t.Fatal("expected auto-created work id")
	}
	if doc.WorkID != workID {
		t.Fatalf("expected doc work_id %q, got %q", workID, doc.WorkID)
	}
	if doc.Path != "docs/review-bundle.md" {
		t.Fatalf("expected authoritative repo-relative path, got %q", doc.Path)
	}
	if !doc.RepoFileExists {
		t.Fatalf("expected doc to report repo file exists, got %+v", doc)
	}
	if !doc.MatchesRepo {
		t.Fatalf("expected doc body to match repo file, got %+v", doc)
	}

	show, err := svc.Work(ctx, workID)
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if len(show.Docs) != 1 {
		t.Fatalf("expected 1 doc in work show, got %+v", show.Docs)
	}
	if show.Docs[0].Path != "docs/review-bundle.md" {
		t.Fatalf("expected work show doc path to be repo-relative, got %+v", show.Docs[0])
	}
	if !show.Docs[0].RepoFileExists || !show.Docs[0].MatchesRepo {
		t.Fatalf("expected work show doc to reflect authoritative repo file, got %+v", show.Docs[0])
	}
}

func TestSetDocContentRejectsPathLinkedToDifferentWork(t *testing.T) {
	svc, repoRoot := newRepoBackedTestService(t)
	ctx := context.Background()

	first, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "first doc work",
		Objective: "track first doc",
	})
	if err != nil {
		t.Fatalf("CreateWork first: %v", err)
	}
	second, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "second doc work",
		Objective: "track second doc",
	})
	if err != nil {
		t.Fatalf("CreateWork second: %v", err)
	}

	repoFile := filepath.Join(repoRoot, "docs", "shared.md")
	mustWriteFile(t, repoFile, "# Shared\n")

	if _, _, err := svc.SetDocContent(ctx, first.WorkID, repoFile, "Shared", "# Shared\n", "markdown"); err != nil {
		t.Fatalf("SetDocContent first: %v", err)
	}
	if _, _, err := svc.SetDocContent(ctx, second.WorkID, repoFile, "Shared", "# Shared\n", "markdown"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for conflicting doc path linkage, got %v", err)
	}
}

func TestWorkShowDocsReportMissingAndDriftedRepoFiles(t *testing.T) {
	svc, repoRoot := newRepoBackedTestService(t)
	ctx := context.Background()

	doc, workID, err := svc.SetDocContent(ctx, "", "docs/runtime-only.md", "Runtime Only", "# Runtime\n", "markdown")
	if err != nil {
		t.Fatalf("SetDocContent: %v", err)
	}
	if doc.RepoFileExists || doc.MatchesRepo {
		t.Fatalf("expected runtime-only doc to start non-authoritative, got %+v", doc)
	}

	show, err := svc.Work(ctx, workID)
	if err != nil {
		t.Fatalf("Work missing repo file: %v", err)
	}
	if len(show.Docs) != 1 {
		t.Fatalf("expected 1 doc, got %+v", show.Docs)
	}
	if show.Docs[0].RepoFileExists || show.Docs[0].MatchesRepo {
		t.Fatalf("expected missing repo file to remain non-authoritative, got %+v", show.Docs[0])
	}

	repoFile := filepath.Join(repoRoot, "docs", "runtime-only.md")
	mustWriteFile(t, repoFile, "# Runtime\nchanged\n")

	show, err = svc.Work(ctx, workID)
	if err != nil {
		t.Fatalf("Work drifted repo file: %v", err)
	}
	if !show.Docs[0].RepoFileExists {
		t.Fatalf("expected repo file to exist after writing it, got %+v", show.Docs[0])
	}
	if show.Docs[0].MatchesRepo {
		t.Fatalf("expected mismatched repo file to remain non-authoritative, got %+v", show.Docs[0])
	}
}

func TestCreateWorkNormalizesRequiredDocs(t *testing.T) {
	svc, repoRoot := newRepoBackedTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:        "doc policy work",
		Objective:    "normalize required doc paths",
		Kind:         "attest",
		RequiredDocs: []string{filepath.Join(repoRoot, "docs", "policy.md"), "docs/policy.md"},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}
	if len(work.RequiredDocs) != 1 || work.RequiredDocs[0] != "docs/policy.md" {
		t.Fatalf("expected normalized required docs, got %+v", work.RequiredDocs)
	}
}

func TestRequiredDocsGateBlocksDoneUntilRepoDocAligned(t *testing.T) {
	svc, repoRoot := newRepoBackedTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:        "docs gate",
		Objective:    "required docs must align before done",
		Kind:         "attest",
		RequiredDocs: []string{"docs/review-bundle.md"},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	_, err = svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateDone,
		Message:        "attempt done without tracked doc",
		CreatedBy:      "test",
	})
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "required doc docs/review-bundle.md is not tracked") {
		t.Fatalf("expected missing required doc error, got %v", err)
	}

	repoFile := filepath.Join(repoRoot, "docs", "review-bundle.md")
	mustWriteFile(t, repoFile, "# Review\n")
	if _, _, err := svc.SetDocContent(ctx, work.WorkID, repoFile, "Review Bundle", "# Review\n", "markdown"); err != nil {
		t.Fatalf("SetDocContent: %v", err)
	}

	mustWriteFile(t, repoFile, "# Review\nupdated\n")
	_, err = svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateDone,
		Message:        "attempt done with drifted doc",
		CreatedBy:      "test",
	})
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "stale or mismatched") {
		t.Fatalf("expected drifted required doc error, got %v", err)
	}

	if _, _, err := svc.SetDocContent(ctx, work.WorkID, repoFile, "Review Bundle", "# Review\nupdated\n", "markdown"); err != nil {
		t.Fatalf("SetDocContent updated: %v", err)
	}

	updated, err := svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateDone,
		Message:        "done after doc alignment",
		CreatedBy:      "test",
	})
	if err != nil {
		t.Fatalf("UpdateWork after doc alignment: %v", err)
	}
	if updated.ExecutionState != core.WorkExecutionStateDone {
		t.Fatalf("expected done state after doc alignment, got %s", updated.ExecutionState)
	}
}

func TestSyncWorkStateFromJobLeavesDocsRequiredWorkCheckingUntilDocsAlign(t *testing.T) {
	svc, repoRoot := newRepoBackedTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:        "sync docs gate",
		Objective:    "completed jobs stay checking until required docs align",
		Kind:         "attest",
		RequiredDocs: []string{"docs/runtime.md"},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	job := core.JobRecord{
		JobID:     core.GenerateID("job"),
		SessionID: core.GenerateID("ses"),
		WorkID:    work.WorkID,
		State:     core.JobStateCompleted,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := svc.syncWorkStateFromJob(ctx, job, nil); err != nil {
		t.Fatalf("syncWorkStateFromJob missing docs: %v", err)
	}

	current, err := svc.store.GetWorkItem(ctx, work.WorkID)
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	if current.ExecutionState != core.WorkExecutionStateChecking {
		t.Fatalf("expected checking with unresolved required docs, got %s", current.ExecutionState)
	}

	repoFile := filepath.Join(repoRoot, "docs", "runtime.md")
	mustWriteFile(t, repoFile, "# Runtime\n")
	if _, _, err := svc.SetDocContent(ctx, work.WorkID, repoFile, "Runtime", "# Runtime\n", "markdown"); err != nil {
		t.Fatalf("SetDocContent: %v", err)
	}

	job2 := job
	job2.JobID = core.GenerateID("job")
	job2.UpdatedAt = time.Now().UTC()
	if err := svc.syncWorkStateFromJob(ctx, job2, nil); err != nil {
		t.Fatalf("syncWorkStateFromJob aligned docs: %v", err)
	}

	current, err = svc.store.GetWorkItem(ctx, work.WorkID)
	if err != nil {
		t.Fatalf("GetWorkItem after alignment: %v", err)
	}
	if current.ExecutionState != core.WorkExecutionStateDone {
		t.Fatalf("expected done once required docs align, got %s", current.ExecutionState)
	}
}

func TestAttestAndApproveWorkRespectRequiredDocsGate(t *testing.T) {
	svc, repoRoot := newRepoBackedTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "attestation plus docs gate",
		Objective: "docs and attestations share one completion gate",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		},
		RequiredDocs: []string{"docs/review.md"},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	repoFile := filepath.Join(repoRoot, "docs", "review.md")
	mustWriteFile(t, repoFile, "# Review\n")
	if _, _, err := svc.SetDocContent(ctx, work.WorkID, repoFile, "Review", "# Review\n", "markdown"); err != nil {
		t.Fatalf("SetDocContent: %v", err)
	}
	mustWriteFile(t, repoFile, "# Review\nupdated\n")

	if _, err := svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateChecking,
		Message:        "implementation complete",
		CreatedBy:      "worker",
	}); err != nil {
		t.Fatalf("UpdateWork checking: %v", err)
	}

	_, updated, err := svc.AttestWork(ctx, WorkAttestRequest{
		WorkID:       work.WorkID,
		Result:       "passed",
		Summary:      "attestation passed",
		VerifierKind: "attestation",
		Method:       "automated_review",
		CreatedBy:    "verifier",
	})
	if err != nil {
		t.Fatalf("AttestWork: %v", err)
	}
	if updated.ExecutionState != core.WorkExecutionStateChecking {
		t.Fatalf("expected checking while required docs drift, got %s", updated.ExecutionState)
	}

	if _, err := svc.ApproveWork(ctx, work.WorkID, "approver", "attempt approval with drifted docs"); !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "docs/review.md") {
		t.Fatalf("expected docs gate approval error, got %v", err)
	}

	if _, _, err := svc.SetDocContent(ctx, work.WorkID, repoFile, "Review", "# Review\nupdated\n", "markdown"); err != nil {
		t.Fatalf("SetDocContent aligned: %v", err)
	}

	done, err := svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateDone,
		Message:        "done after docs align",
		CreatedBy:      "worker",
	})
	if err != nil {
		t.Fatalf("UpdateWork done after docs align: %v", err)
	}
	if done.ExecutionState != core.WorkExecutionStateDone {
		t.Fatalf("expected done after docs align, got %s", done.ExecutionState)
	}

	approved, err := svc.ApproveWork(ctx, work.WorkID, "approver", "approve once docs align")
	if err != nil {
		t.Fatalf("ApproveWork after docs align: %v", err)
	}
	if approved.ApprovalState != core.WorkApprovalStateVerified {
		t.Fatalf("expected verified approval state, got %s", approved.ApprovalState)
	}
}

func TestWorkJSONNormalizesDeprecatedExecutionState(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "legacy state work",
		Objective: "verify deprecated execution state normalization on read",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	work.ExecutionState = core.WorkExecutionStateAwaitingAttestation
	if err := svc.store.UpdateWorkItem(ctx, *work); err != nil {
		t.Fatalf("UpdateWorkItem: %v", err)
	}
	if err := svc.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:       core.GenerateID("upd"),
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateAwaitingAttestation,
		Message:        "legacy state update",
		Metadata:       map[string]any{},
		CreatedBy:      "test",
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateWorkUpdate: %v", err)
	}

	show, err := svc.Work(ctx, work.WorkID)
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if show.Work.ExecutionState != core.WorkExecutionStateChecking {
		t.Fatalf("Work.ExecutionState = %q, want %q", show.Work.ExecutionState, core.WorkExecutionStateChecking)
	}
	if len(show.Updates) != 1 {
		t.Fatalf("len(Updates) = %d, want 1", len(show.Updates))
	}
	if show.Updates[0].ExecutionState != core.WorkExecutionStateChecking {
		t.Fatalf("Updates[0].ExecutionState = %q, want %q", show.Updates[0].ExecutionState, core.WorkExecutionStateChecking)
	}

	showJSON, err := json.Marshal(show)
	if err != nil {
		t.Fatalf("Marshal show: %v", err)
	}
	if bytes.Contains(showJSON, []byte("awaiting_attestation")) {
		t.Fatalf("show JSON leaked deprecated state: %s", showJSON)
	}

	items, err := svc.ListWork(ctx, WorkListRequest{Limit: 10})
	if err != nil {
		t.Fatalf("ListWork: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ExecutionState != core.WorkExecutionStateChecking {
		t.Fatalf("items[0].ExecutionState = %q, want %q", items[0].ExecutionState, core.WorkExecutionStateChecking)
	}

	listJSON, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("Marshal list: %v", err)
	}
	if bytes.Contains(listJSON, []byte("awaiting_attestation")) {
		t.Fatalf("list JSON leaked deprecated state: %s", listJSON)
	}
}

func TestAttestationGateBlocksArchive(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "gate test",
		Objective: "must not archive without attestation",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	_, err = svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateArchived,
		Message:        "attempt archive",
		CreatedBy:      "test",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput when archiving with unresolved attestations, got %v", err)
	}
}

func TestAttestationGateBlocksDone(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "gate test done",
		Objective: "must not set done without attestation",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	_, err = svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateDone,
		Message:        "attempt done",
		CreatedBy:      "test",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput when setting done with unresolved attestations, got %v", err)
	}
}

func TestAttestationGateAllowsArchiveAfterAttestation(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "gate allow archive",
		Objective: "archive after attestation passes",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	_, _, err = svc.AttestWork(ctx, WorkAttestRequest{
		WorkID:       work.WorkID,
		Result:       "passed",
		Summary:      "looks good",
		VerifierKind: "attestation",
		Method:       "automated_review",
		CreatedBy:    "verifier",
	})
	if err != nil {
		t.Fatalf("AttestWork: %v", err)
	}

	updated, err := svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateArchived,
		Message:        "archive after attestation",
		CreatedBy:      "test",
	})
	if err != nil {
		t.Fatalf("UpdateWork archive after attestation: %v", err)
	}
	if updated.ExecutionState != core.WorkExecutionStateArchived {
		t.Fatalf("expected archived state, got %s", updated.ExecutionState)
	}
}

func TestAttestationGateAllowsDoneAfterAttestation(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "gate allow done",
		Objective: "done after attestation passes",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	_, _, err = svc.AttestWork(ctx, WorkAttestRequest{
		WorkID:       work.WorkID,
		Result:       "passed",
		Summary:      "looks good",
		VerifierKind: "attestation",
		Method:       "automated_review",
		CreatedBy:    "verifier",
	})
	if err != nil {
		t.Fatalf("AttestWork: %v", err)
	}

	updated, err := svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateDone,
		Message:        "done after attestation",
		CreatedBy:      "test",
	})
	if err != nil {
		t.Fatalf("UpdateWork done after attestation: %v", err)
	}
	if updated.ExecutionState != core.WorkExecutionStateDone {
		t.Fatalf("expected done state, got %s", updated.ExecutionState)
	}
}

func TestAttestWorkRejectsMismatchedSlotParams(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "mismatch test",
		Objective: "attestation params must match the slot",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	_, _, err = svc.AttestWork(ctx, WorkAttestRequest{
		WorkID:       work.WorkID,
		Result:       "passed",
		Summary:      "wrong verifier and method",
		VerifierKind: "security",
		Method:       "security_review",
		CreatedBy:    "verifier",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for mismatched attestation params, got %v", err)
	}
	if !strings.Contains(err.Error(), "expected one of [attestation/automated_review]") {
		t.Fatalf("expected error to list required slot, got %v", err)
	}
	if !strings.Contains(err.Error(), `got verifier_kind="security" method="security_review"`) {
		t.Fatalf("expected error to include actual params, got %v", err)
	}

	attestations, err := svc.store.ListAttestationRecords(ctx, "work", work.WorkID, 10)
	if err != nil {
		t.Fatalf("ListAttestationRecords: %v", err)
	}
	if len(attestations) != 0 {
		t.Fatalf("expected no attestation to be recorded, got %d", len(attestations))
	}
}

func TestAttestWorkAutoFillsSingleUnsatisfiedSlot(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "autofill test",
		Objective: "single remaining slot should populate params",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	record, updated, err := svc.AttestWork(ctx, WorkAttestRequest{
		WorkID:    work.WorkID,
		Result:    "passed",
		Summary:   "looks good",
		CreatedBy: "verifier",
	})
	if err != nil {
		t.Fatalf("AttestWork: %v", err)
	}
	if record.VerifierKind != "attestation" {
		t.Fatalf("expected verifier kind to be autofilled, got %q", record.VerifierKind)
	}
	if record.Method != "automated_review" {
		t.Fatalf("expected method to be autofilled, got %q", record.Method)
	}
	if updated.ExecutionState != core.WorkExecutionStateDone {
		t.Fatalf("expected done state after autofilled attestation, got %s", updated.ExecutionState)
	}
}

func TestAttestationGateExemptsFailedAndCancelled(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	for _, state := range []core.WorkExecutionState{
		core.WorkExecutionStateFailed,
		core.WorkExecutionStateCancelled,
	} {
		work, err := svc.CreateWork(ctx, WorkCreateRequest{
			Title:     "exempt test",
			Objective: "can always fail or cancel",
			RequiredAttestations: []core.RequiredAttestation{
				{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
			},
		})
		if err != nil {
			t.Fatalf("CreateWork for %s: %v", state, err)
		}

		updated, err := svc.UpdateWork(ctx, WorkUpdateRequest{
			WorkID:         work.WorkID,
			ExecutionState: state,
			Message:        "transition to " + string(state),
			CreatedBy:      "test",
		})
		if err != nil {
			t.Fatalf("expected no error transitioning to %s without attestation, got %v", state, err)
		}
		if updated.ExecutionState != state {
			t.Fatalf("expected %s state, got %s", state, updated.ExecutionState)
		}
	}
}

func TestAttestationGateDefaultAttestations(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "default attestation gate",
		Objective: "defaults to requiring attestation",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}
	if len(work.RequiredAttestations) == 0 {
		t.Fatal("expected default required attestations to be set")
	}

	_, err = svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateArchived,
		Message:        "attempt archive",
		CreatedBy:      "test",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for archive with default unresolved attestations, got %v", err)
	}

	_, err = svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateDone,
		Message:        "attempt done",
		CreatedBy:      "test",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for done with default unresolved attestations, got %v", err)
	}
}

func TestAttestationGateNoAttestationsRequired(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "no attestations needed",
		Objective: "attest-kind work has no requirements",
		Kind:      "attest",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}
	if len(work.RequiredAttestations) != 0 {
		t.Fatalf("expected no required attestations for attest-kind work, got %d", len(work.RequiredAttestations))
	}

	for _, state := range []core.WorkExecutionState{
		core.WorkExecutionStateDone,
		core.WorkExecutionStateArchived,
	} {
		updated, err := svc.UpdateWork(ctx, WorkUpdateRequest{
			WorkID:         work.WorkID,
			ExecutionState: state,
			Message:        "transition to " + string(state),
			CreatedBy:      "test",
		})
		if err != nil {
			t.Fatalf("expected no error for %s without attestation requirements, got %v", state, err)
		}
		if updated.ExecutionState != state {
			t.Fatalf("expected %s, got %s", state, updated.ExecutionState)
		}
	}
}

func TestPersistCheckScreenshots(t *testing.T) {
	// Create a temporary project structure
	projectRoot := t.TempDir()
	workID := "work_test123"

	// Create a mock job with worktree
	worktreeDir := filepath.Join(projectRoot, ".fase", "worktrees", workID)
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	// Create test screenshot files in the worktree
	testResultsDir := filepath.Join(worktreeDir, "test-results")
	if err := os.MkdirAll(testResultsDir, 0755); err != nil {
		t.Fatalf("mkdir test-results: %v", err)
	}

	// Create test Playwright artifacts.
	srcScreenshot := filepath.Join(testResultsDir, "test-1.png")
	testData := []byte("fake PNG data")
	if err := os.WriteFile(srcScreenshot, testData, 0644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}
	srcVideo := filepath.Join(testResultsDir, "test-1.webm")
	videoData := []byte("fake WEBM data")
	if err := os.WriteFile(srcVideo, videoData, 0644); err != nil {
		t.Fatalf("write video: %v", err)
	}

	// Initialize a git repo at the project root
	cmd := exec.Command("git", "init", projectRoot)
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Set up service with the project root
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	setTestExecutable(t)

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[adapters.native]\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	svc, err := Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = svc.Close() }()

	ctx := context.Background()

	// Create a work item
	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "test playwright screenshots",
		Objective: "verify screenshot persistence",
		Kind:      "implement",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	// Create a job record for this work pointing to the worktree
	now := time.Now().UTC()
	jobID := core.GenerateID("job")
	sessionID := core.GenerateID("ses")

	session := core.SessionRecord{
		SessionID:     sessionID,
		CreatedAt:     now,
		UpdatedAt:     now,
		Status:        "active",
		OriginAdapter: "test",
		OriginJobID:   jobID,
		CWD:           worktreeDir,
		LatestJobID:   jobID,
		Tags:          []string{},
		Metadata:      map[string]any{},
	}
	if err := svc.store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	job := core.JobRecord{
		JobID:           jobID,
		SessionID:       sessionID,
		WorkID:          work.WorkID,
		Adapter:         "native",
		State:           core.JobStateCompleted,
		NativeSessionID: sessionID,
		CWD:             worktreeDir,
		CreatedAt:       now,
		UpdatedAt:       now,
		Summary:         map[string]any{},
	}
	if err := svc.store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Now test the persistCheckScreenshots function
	screenshots := []string{srcScreenshot, srcVideo}
	newPaths, err := svc.persistCheckScreenshots(ctx, work.WorkID, screenshots)
	if err != nil {
		t.Fatalf("persistCheckScreenshots: %v", err)
	}

	if len(newPaths) != 2 {
		t.Fatalf("expected 2 artifact paths, got %d", len(newPaths))
	}

	// Verify the files were copied to the artifacts directory.
	// Use realpath to handle symlinks on macOS
	expectedPaths := map[string][]byte{
		filepath.Join(projectRoot, ".fase", "artifacts", work.WorkID, "screenshots", "test-1.png"):  testData,
		filepath.Join(projectRoot, ".fase", "artifacts", work.WorkID, "screenshots", "test-1.webm"): videoData,
	}
	normalizedNewPaths := make(map[string]bool, len(newPaths))
	for _, path := range newPaths {
		realPath, err := filepath.EvalSymlinks(path)
		if err == nil {
			path = realPath
		}
		normalizedNewPaths[path] = true
	}
	for expectedPath, expectedData := range expectedPaths {
		realExpectedPath, err := filepath.EvalSymlinks(expectedPath)
		if err == nil {
			expectedPath = realExpectedPath
		}
		if !normalizedNewPaths[expectedPath] {
			t.Fatalf("expected copied path %s in %v", expectedPath, newPaths)
		}
		copiedData, err := os.ReadFile(expectedPath)
		if err != nil {
			t.Fatalf("read copied file %s: %v", expectedPath, err)
		}
		if !bytes.Equal(copiedData, expectedData) {
			t.Fatalf("copied file content mismatch for %s: expected %q, got %q", expectedPath, expectedData, copiedData)
		}
	}

	collectedPaths := svc.collectScreenshotPaths(ctx, work.WorkID, core.CheckRecord{})
	if len(collectedPaths) != 2 {
		t.Fatalf("expected 2 collected artifact paths, got %d", len(collectedPaths))
	}

	// Test that collectPlaywrightAttachments finds the persisted artifacts.
	attachments := svc.collectPlaywrightAttachments(ctx, work.WorkID)
	if len(attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(attachments))
	}

	contentTypes := make(map[string]string, len(attachments))
	for _, attachment := range attachments {
		contentTypes[attachment.Filename] = attachment.ContentType
	}
	if contentTypes["test-1.png"] != "image/png" {
		t.Fatalf("expected PNG attachment, got %q", contentTypes["test-1.png"])
	}
	if contentTypes["test-1.webm"] != "video/webm" {
		t.Fatalf("expected WEBM attachment, got %q", contentTypes["test-1.webm"])
	}

	checkAttachments := svc.collectCheckArtifacts(ctx, work.WorkID, core.CheckRecord{
		Report: core.CheckReport{Screenshots: collectedPaths},
	})
	if len(checkAttachments) != 2 {
		t.Fatalf("expected 2 check attachments, got %d", len(checkAttachments))
	}
	checkContentTypes := make(map[string]string, len(checkAttachments))
	for _, attachment := range checkAttachments {
		checkContentTypes[attachment.Filename] = attachment.ContentType
	}
	if checkContentTypes["test-1.webm"] != "video/webm" {
		t.Fatalf("expected WEBM check attachment, got %q", checkContentTypes["test-1.webm"])
	}
}

// TestGitMainRepoRoot verifies that gitMainRepoRoot resolves the main repo root
// even when the CWD is inside a git worktree at .fase/worktrees/<workID>.
func TestGitMainRepoRoot(t *testing.T) {
	mainRepo := t.TempDir()
	workID := "work_testworktree"

	// Initialise a proper git repo with a commit so worktree add works.
	for _, args := range [][]string{
		{"init", mainRepo},
		{"-C", mainRepo, "config", "user.email", "test@test.com"},
		{"-C", mainRepo, "config", "user.name", "Test"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sentinel := filepath.Join(mainRepo, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("hi"), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	for _, args := range [][]string{
		{"-C", mainRepo, "add", "sentinel.txt"},
		{"-C", mainRepo, "commit", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Create a real git worktree at the project's standard path.
	worktreesDir := filepath.Join(mainRepo, ".fase", "worktrees")
	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	worktreeDir := filepath.Join(worktreesDir, workID)
	branch := "fase/work/" + workID
	if out, err := exec.Command("git", "-C", mainRepo, "worktree", "add", "-b", branch, worktreeDir).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", mainRepo, "worktree", "remove", "--force", worktreeDir).Run()
	})

	// Verify --show-toplevel on the real worktree returns the worktree dir (the bug scenario).
	out, err := exec.Command("git", "-C", worktreeDir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	showTop := strings.TrimSpace(string(out))
	realWorktreeDir, _ := filepath.EvalSymlinks(worktreeDir)
	realShowTop, _ := filepath.EvalSymlinks(showTop)
	if realShowTop != realWorktreeDir {
		t.Skipf("git --show-toplevel returned %q, not worktree dir %q — skipping", showTop, worktreeDir)
	}

	// Our helper should return the main repo root, not the worktree dir.
	ctx := context.Background()
	got, err := gitMainRepoRoot(ctx, worktreeDir)
	if err != nil {
		t.Fatalf("gitMainRepoRoot: %v", err)
	}
	realGot, _ := filepath.EvalSymlinks(got)
	realMainRepo, _ := filepath.EvalSymlinks(mainRepo)
	if realGot != realMainRepo {
		t.Fatalf("gitMainRepoRoot returned %q, want main repo root %q", got, mainRepo)
	}
}

func TestIsStricterContract(t *testing.T) {
	existing := []core.RequiredAttestation{
		{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		{VerifierKind: "attestation", Method: "human_review", Blocking: false},
	}

	// Test case 1: Adding new requirement should be stricter
	t.Run("adds new requirement", func(t *testing.T) {
		proposed := []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
			{VerifierKind: "attestation", Method: "human_review", Blocking: false},
			{VerifierKind: "attestation", Method: "security_scan", Blocking: true},
		}
		if !isStricterContract(existing, proposed) {
			t.Error("expected adding new requirement to be stricter")
		}
	})

	// Test case 2: Tightening blocking flag (non-blocking -> blocking) should be stricter
	t.Run("tightens blocking flag", func(t *testing.T) {
		proposed := []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
			{VerifierKind: "attestation", Method: "human_review", Blocking: true}, // was false, now true
		}
		if !isStricterContract(existing, proposed) {
			t.Error("expected blocking-flag tightening to be stricter")
		}
	})

	// Test case 3: Weakening blocking flag (blocking -> non-blocking) should NOT be stricter
	t.Run("weakens blocking flag", func(t *testing.T) {
		proposed := []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: false}, // was true, now false
			{VerifierKind: "attestation", Method: "human_review", Blocking: false},
		}
		if isStricterContract(existing, proposed) {
			t.Error("expected blocking-flag weakening to NOT be stricter")
		}
	})

	// Test case 4: Same contract with no changes should NOT be stricter
	t.Run("same contract no change", func(t *testing.T) {
		proposed := []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
			{VerifierKind: "attestation", Method: "human_review", Blocking: false},
		}
		if isStricterContract(existing, proposed) {
			t.Error("expected identical contract to NOT be stricter")
		}
	})

	// Test case 5: Removing requirements should NOT be stricter
	t.Run("removes requirement", func(t *testing.T) {
		proposed := []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		}
		if isStricterContract(existing, proposed) {
			t.Error("expected removal to NOT be stricter")
		}
	})

	// Test case 6: Same contract with different order (same length, same items) should NOT be stricter
	t.Run("same contract different order", func(t *testing.T) {
		proposed := []core.RequiredAttestation{
			// Same items, same blocking status, just different order
			{VerifierKind: "attestation", Method: "human_review", Blocking: false},
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		}
		// Same length, same items, no new requirements, no blocking tightening - should be false
		if isStricterContract(existing, proposed) {
			t.Error("expected re-ordered contract to NOT be stricter")
		}
	})
}

// TestApplyEscalateContractProposal tests the explicit escalation path for frozen contracts.
// This tests VAL-CONTRACT-003: post-freeze changes may only make the contract stricter
// and must flow through the explicit escalate_contract path.
func TestApplyEscalateContractProposal(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	now := time.Now()

	// Create a work item and start execution to trigger attestation freeze
	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "escalation test work",
		Objective: "test contract escalation",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	// To freeze the contract, we need to either run a job or transition to AwaitingAttestation.
	// For testing, we manually set AttestationFrozenAt to simulate the frozen state.
	// IMPORTANT: Must fetch the work first and update all fields, not create a new record
	frozenTime := time.Now().UTC()
	workToUpdate := *work
	workToUpdate.AttestationFrozenAt = &frozenTime
	err = svc.store.UpdateWorkItem(ctx, workToUpdate)
	if err != nil {
		t.Fatalf("UpdateWorkItem to set frozen: %v", err)
	}

	// Get the work to verify AttestationFrozenAt is set
	updatedWork, err := svc.store.GetWorkItem(ctx, work.WorkID)
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	if updatedWork.AttestationFrozenAt == nil {
		t.Fatal("expected AttestationFrozenAt to be set")
	}

	// Helper to convert attestations to []any with map[string]any elements
	// summaryAttestations expects each element to be a map, not a struct
	attToMapSlice := func(att []core.RequiredAttestation) []any {
		result := make([]any, len(att))
		for i, a := range att {
			result[i] = map[string]any{
				"verifier_kind": a.VerifierKind,
				"method":        a.Method,
				"blocking":      a.Blocking,
			}
		}
		return result
	}

	// Test case 1: Successful escalation with new requirements
	t.Run("successful escalation with new requirements", func(t *testing.T) {
		proposal := core.WorkProposalRecord{
			ProposalID:   core.GenerateID("proposal"),
			ProposalType: "escalate_contract",
			TargetWorkID: updatedWork.WorkID,
			CreatedBy:    "test-user",
			Rationale:    "Adding security requirements due to new threat model",
			CreatedAt:    now,
			ProposedPatch: map[string]any{
				"required_attestations": attToMapSlice([]core.RequiredAttestation{
					{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
					{VerifierKind: "attestation", Method: "security_scan", Blocking: true},
				}),
			},
		}

		err := svc.applyEscalateContractProposal(ctx, proposal, now)
		if err != nil {
			t.Fatalf("applyEscalateContractProposal: %v", err)
		}

		// Verify the work was updated with new attestation requirements
		updated, err := svc.store.GetWorkItem(ctx, work.WorkID)
		if err != nil {
			t.Fatalf("GetWorkItem after escalation: %v", err)
		}

		// Verify we now have 2 attestations
		if len(updated.RequiredAttestations) != 2 {
			t.Fatalf("expected 2 attestations after escalation, got %d", len(updated.RequiredAttestations))
		}

		// Verify escalation fields are set on the NEW attestation (security_scan)
		var foundNewWithFields bool
		for _, att := range updated.RequiredAttestations {
			if att.Method == "security_scan" {
				if att.EscalatedAt == nil {
					t.Error("expected EscalatedAt to be set on new attestation")
				}
				if att.EscalationBy == "" {
					t.Error("expected EscalationBy to be set on new attestation")
				}
				if att.EscalationReason == "" {
					t.Error("expected EscalationReason to be set on new attestation")
				}
				if att.EscalationBy != "test-user" {
					t.Errorf("expected EscalationBy to be 'test-user', got %q", att.EscalationBy)
				}
				if att.EscalationReason != "Adding security requirements due to new threat model" {
					t.Errorf("expected EscalationReason to match, got %q", att.EscalationReason)
				}
				foundNewWithFields = true
			}
		}
		if !foundNewWithFields {
			t.Error("did not find new attestation with escalation fields")
		}

		// Verify original attestation still has nil escalation fields
		for _, att := range updated.RequiredAttestations {
			if att.Method == "automated_review" {
				if att.EscalatedAt != nil {
					t.Error("expected original attestation to have nil EscalatedAt")
				}
			}
		}

		// Verify metadata is recorded
		if updated.Metadata["contract_escalated_at"] == nil {
			t.Error("expected contract_escalated_at in metadata")
		}
		if updated.Metadata["contract_escalation_proposal"] == nil {
			t.Error("expected contract_escalation_proposal in metadata")
		}
	})

	// Test case 2: Rejection of weakening (blocking -> non-blocking)
	t.Run("rejects weakening", func(t *testing.T) {
		proposal := core.WorkProposalRecord{
			ProposalID:   core.GenerateID("proposal"),
			ProposalType: "escalate_contract",
			TargetWorkID: updatedWork.WorkID,
			CreatedBy:    "test-user",
			Rationale:    "Attempt to weaken contract",
			CreatedAt:    now,
			ProposedPatch: map[string]any{
				"required_attestations": attToMapSlice([]core.RequiredAttestation{
					// Try to change automated_review from blocking to non-blocking
					{VerifierKind: "attestation", Method: "automated_review", Blocking: false},
				}),
			},
		}

		err := svc.applyEscalateContractProposal(ctx, proposal, now)
		if err == nil {
			t.Error("expected error when trying to weaken contract")
		}
		if !strings.Contains(err.Error(), "stricter requirements") {
			t.Errorf("expected error about stricter requirements, got: %v", err)
		}
	})

	// Test case 3: Rejection of no stricter changes (same contract)
	t.Run("rejects same contract", func(t *testing.T) {
		proposal := core.WorkProposalRecord{
			ProposalID:   core.GenerateID("proposal"),
			ProposalType: "escalate_contract",
			TargetWorkID: updatedWork.WorkID,
			CreatedBy:    "test-user",
			Rationale:    "No changes",
			CreatedAt:    now,
			ProposedPatch: map[string]any{
				"required_attestations": attToMapSlice([]core.RequiredAttestation{
					{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
				}),
			},
		}

		err := svc.applyEscalateContractProposal(ctx, proposal, now)
		if err == nil {
			t.Error("expected error when proposing same contract")
		}
	})

	// Test case 4: Rejection when contract is not frozen
	t.Run("rejects unfrozen contract", func(t *testing.T) {
		// Create a new work without starting execution
		freshWork, err := svc.CreateWork(ctx, WorkCreateRequest{
			Title:     "unfrozen work",
			Objective: "test unfrozen rejection",
			RequiredAttestations: []core.RequiredAttestation{
				{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
			},
		})
		if err != nil {
			t.Fatalf("CreateWork: %v", err)
		}

		proposal := core.WorkProposalRecord{
			ProposalID:   core.GenerateID("proposal"),
			ProposalType: "escalate_contract",
			TargetWorkID: freshWork.WorkID,
			CreatedBy:    "test-user",
			Rationale:    "Try escalation before freeze",
			CreatedAt:    now,
			ProposedPatch: map[string]any{
				"required_attestations": attToMapSlice([]core.RequiredAttestation{
					{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
					{VerifierKind: "attestation", Method: "security_scan", Blocking: true},
				}),
			},
		}

		err = svc.applyEscalateContractProposal(ctx, proposal, now)
		if err == nil {
			t.Error("expected error when escalating unfrozen contract")
		}
		if !strings.Contains(err.Error(), "started execution first") {
			t.Errorf("expected error about execution requirement, got: %v", err)
		}
	})
}

// TestSummaryAttestations tests the summaryAttestations helper function
// that extracts attestation requirements from proposal patches.
func TestSummaryAttestations(t *testing.T) {
	// Test extracting attestations from a valid patch - must use []any (not concrete type)
	patch := map[string]any{
		"required_attestations": []any{
			map[string]any{
				"verifier_kind": "attestation",
				"method":        "automated_review",
				"blocking":      true,
			},
			map[string]any{
				"verifier_kind": "attestation",
				"method":        "security_scan",
				"blocking":      false,
			},
		},
	}

	attestations := summaryAttestations(patch, "required_attestations")
	if len(attestations) != 2 {
		t.Fatalf("expected 2 attestations, got %d", len(attestations))
	}
	if attestations[0].VerifierKind != "attestation" || attestations[0].Method != "automated_review" {
		t.Error("first attestation does not match expected")
	}
	if attestations[1].VerifierKind != "attestation" || attestations[1].Method != "security_scan" {
		t.Error("second attestation does not match expected")
	}

	// Test with missing key
	emptyPatch := map[string]any{}
	result := summaryAttestations(emptyPatch, "required_attestations")
	if len(result) != 0 {
		t.Fatalf("expected 0 attestations for missing key, got %d", len(result))
	}

	// Test with wrong type
	wrongTypePatch := map[string]any{
		"required_attestations": "not-an-array",
	}
	result = summaryAttestations(wrongTypePatch, "required_attestations")
	if len(result) != 0 {
		t.Fatalf("expected 0 attestations for wrong type, got %d", len(result))
	}
}

// TestResetWorkStartsNewAttemptEpoch verifies VAL-LIFECYCLE-005:
// Retry/reset starts a new attempt epoch without stale current-attempt linkage.
func TestResetWorkStartsNewAttemptEpoch(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "retry test",
		Objective: "test retry/reset behavior",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}
	if work.AttemptEpoch != 1 {
		t.Fatalf("expected AttemptEpoch 1 on creation, got %d", work.AttemptEpoch)
	}

	// First attempt - complete some work
	_, err = svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateInProgress,
		CreatedBy:      "test",
	})
	if err != nil {
		t.Fatalf("UpdateWork: %v", err)
	}

	// Reset the work
	reset, err := svc.ResetWork(ctx, WorkResetRequest{
		WorkID:      work.WorkID,
		Reason:      "retry after failure",
		CreatedBy:   "test",
		ClearClaims: true,
	})
	if err != nil {
		t.Fatalf("ResetWork: %v", err)
	}

	if reset.AttemptEpoch != 2 {
		t.Fatalf("expected AttemptEpoch 2 after reset, got %d", reset.AttemptEpoch)
	}
	if reset.ExecutionState != core.WorkExecutionStateReady {
		t.Fatalf("expected Ready state after reset, got %s", reset.ExecutionState)
	}
	if reset.CurrentJobID != "" {
		t.Fatalf("expected empty CurrentJobID after reset, got %s", reset.CurrentJobID)
	}
	if reset.AttestationFrozenAt != nil {
		t.Fatalf("expected nil AttestationFrozenAt after reset, got %v", reset.AttestationFrozenAt)
	}

	// Second reset
	reset2, err := svc.ResetWork(ctx, WorkResetRequest{
		WorkID:      work.WorkID,
		Reason:      "another retry",
		CreatedBy:   "test",
		ClearClaims: true,
	})
	if err != nil {
		t.Fatalf("ResetWork: %v", err)
	}
	if reset2.AttemptEpoch != 3 {
		t.Fatalf("expected AttemptEpoch 3 after second reset, got %d", reset2.AttemptEpoch)
	}

	// Verify work update records the epoch
	updates, err := svc.store.ListWorkUpdates(ctx, work.WorkID, 10)
	if err != nil {
		t.Fatalf("ListWorkUpdates: %v", err)
	}
	foundEpoch := false
	for _, update := range updates {
		if epoch, ok := update.Metadata["attempt_epoch"]; ok {
			foundEpoch = true
			if epoch != 3.0 {
				t.Errorf("expected attempt_epoch 3 in update metadata, got %v", epoch)
			}
			break
		}
	}
	if !foundEpoch {
		t.Error("expected attempt_epoch in work update metadata")
	}
}

// TestResetWorkClearsAttestationNonce verifies VAL-LIFECYCLE-005:
// Retry/reset clears the attestation nonce so old attestations cannot satisfy the new run.
func TestResetWorkClearsAttestationNonce(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "nonce test",
		Objective: "test nonce clearing on reset",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	// Simulate completing work and creating attestation state
	_, err = svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         work.WorkID,
		ExecutionState: core.WorkExecutionStateDone,
		Metadata:       map[string]any{"attestation_nonce": "test-nonce-123"},
		ForceDone:      true,
	})
	if err != nil {
		t.Fatalf("UpdateWork: %v", err)
	}

	// Verify nonce exists
	workItem, err := svc.store.GetWorkItem(ctx, work.WorkID)
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	if nonce, ok := workItem.Metadata["attestation_nonce"]; !ok || nonce != "test-nonce-123" {
		t.Fatalf("expected nonce to be set before reset, got %v", nonce)
	}

	attestation, _, err := svc.AttestWork(ctx, WorkAttestRequest{
		WorkID:       work.WorkID,
		Result:       "passed",
		Summary:      "first attempt approved",
		Nonce:        "test-nonce-123",
		VerifierKind: "attestation",
		Method:       "automated_review",
		CreatedBy:    "verifier",
	})
	if err != nil {
		t.Fatalf("AttestWork: %v", err)
	}
	if got := attestation.Metadata["attempt_epoch"]; got != 1 {
		t.Fatalf("expected recorded attempt_epoch 1, got %v", got)
	}

	// Reset
	reset, err := svc.ResetWork(ctx, WorkResetRequest{
		WorkID:    work.WorkID,
		Reason:    "retry",
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("ResetWork: %v", err)
	}

	// Verify nonce is cleared
	if reset.Metadata != nil {
		if _, ok := reset.Metadata["attestation_nonce"]; ok {
			t.Fatal("attestation_nonce should be cleared after reset")
		}
	}

	// Verify epoch incremented
	if reset.AttemptEpoch != 2 {
		t.Fatalf("expected AttemptEpoch 2, got %d", reset.AttemptEpoch)
	}

	attestations, err := svc.store.ListAttestationRecords(ctx, "work", work.WorkID, 10)
	if err != nil {
		t.Fatalf("ListAttestationRecords: %v", err)
	}
	if requiredAttestationsResolved(*reset, attestations) {
		t.Fatal("expected prior-attempt attestation to be ignored after reset")
	}
}

// TestAttestationChildEpochAwareness verifies VAL-LIFECYCLE-004:
// Attestation child creation is idempotent per-epoch.
func TestAttestationChildEpochAwareness(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	parent, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "epoch-aware parent",
		Objective: "test attestation child epoch awareness",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}
	if parent.AttemptEpoch != 1 {
		t.Fatalf("expected AttemptEpoch 1, got %d", parent.AttemptEpoch)
	}
	job1 := core.JobRecord{
		JobID:     core.GenerateID("job"),
		SessionID: core.GenerateID("ses"),
		Adapter:   "codex",
		State:     core.JobStateCompleted,
		Summary:   map[string]any{"model": "gpt-5.4"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := svc.spawnAttestationChildren(ctx, *parent, job1); err != nil {
		t.Fatalf("spawnAttestationChildren attempt 1: %v", err)
	}

	children, err := svc.store.ListWorkChildren(ctx, parent.WorkID, 10)
	if err != nil {
		t.Fatalf("ListWorkChildren: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 attestation child after first spawn, got %d", len(children))
	}
	child1 := children[0]
	if child1.AttemptEpoch != 1 {
		t.Fatalf("expected child1 attempt epoch 1, got %d", child1.AttemptEpoch)
	}
	if child1.Metadata["attempt_epoch"] != 1.0 {
		t.Fatalf("expected child1 metadata epoch 1, got %v", child1.Metadata["attempt_epoch"])
	}

	// Replaying the same fanout for the same attempt should be idempotent.
	currentParent, err := svc.store.GetWorkItem(ctx, parent.WorkID)
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	if err := svc.spawnAttestationChildren(ctx, currentParent, job1); err != nil {
		t.Fatalf("spawnAttestationChildren replay: %v", err)
	}
	children, err = svc.store.ListWorkChildren(ctx, parent.WorkID, 10)
	if err != nil {
		t.Fatalf("ListWorkChildren replay: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected idempotent child fanout for same epoch, got %d children", len(children))
	}

	// Reset to attempt 2 and ensure a fresh child set is created.
	parent, err = svc.ResetWork(ctx, WorkResetRequest{
		WorkID:    parent.WorkID,
		Reason:    "second attempt",
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("ResetWork: %v", err)
	}
	if parent.AttemptEpoch != 2 {
		t.Fatalf("expected AttemptEpoch 2, got %d", parent.AttemptEpoch)
	}
	job2 := core.JobRecord{
		JobID:     core.GenerateID("job"),
		SessionID: core.GenerateID("ses"),
		Adapter:   "codex",
		State:     core.JobStateCompleted,
		Summary:   map[string]any{"model": "gpt-5.4"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := svc.spawnAttestationChildren(ctx, *parent, job2); err != nil {
		t.Fatalf("spawnAttestationChildren attempt 2: %v", err)
	}

	children, err = svc.store.ListWorkChildren(ctx, parent.WorkID, 10)
	if err != nil {
		t.Fatalf("ListWorkChildren attempt 2: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected historical and current child after reset, got %d", len(children))
	}

	var child2 core.WorkItemRecord
	for _, child := range children {
		if child.AttemptEpoch == 2 {
			child2 = child
			break
		}
	}
	if child2.WorkID == "" {
		t.Fatal("expected a fresh attempt-2 attestation child")
	}
	if child1.WorkID == child2.WorkID {
		t.Fatal("expected different work IDs for children from different epochs")
	}
	if child2.Metadata["attempt_epoch"] != 2.0 {
		t.Fatalf("expected child2 metadata epoch 2, got %v", child2.Metadata["attempt_epoch"])
	}
}

// TestAttestationEpochMismatchRejection verifies that attestations from
// prior epochs cannot satisfy current attempt requirements.
func TestAttestationEpochMismatchRejection(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	parent, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "epoch mismatch test",
		Objective: "test epoch mismatch rejection",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	// Set up parent at attempt 2
	parent.AttemptEpoch = 2
	parent.Metadata = map[string]any{"attestation_nonce": "nonce-for-attempt-2"}
	parent.ExecutionState = core.WorkExecutionStateAwaitingAttestation
	if err := svc.store.UpdateWorkItem(ctx, *parent); err != nil {
		t.Fatalf("UpdateWorkItem: %v", err)
	}

	// Create attestation child for attempt 1 (wrong epoch)
	child, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "Attest for parent",
		Objective: "test attestation",
		Kind:      "attest",
		Metadata: map[string]any{
			"parent_work_id":    parent.WorkID,
			"attestation_nonce": "nonce-for-attempt-2",
			"attempt_epoch":     1, // Wrong epoch!
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	// Attempt attestation should fail due to epoch mismatch
	_, _, err = svc.AttestWork(ctx, WorkAttestRequest{
		WorkID:       child.WorkID,
		Result:       "passed",
		Summary:      "looks good",
		Nonce:        "nonce-for-attempt-2",
		VerifierKind: "attestation",
		Method:       "automated_review",
		CreatedBy:    "verifier",
	})
	if err == nil {
		t.Fatal("expected epoch mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "epoch mismatch") {
		t.Fatalf("expected 'epoch mismatch' error, got: %v", err)
	}
}

// TestSyncWorkStateFromJobMapping verifies VAL-LIFECYCLE-003:
// Job states map deterministically to canonical work states.
func TestSyncWorkStateFromJobMapping(t *testing.T) {
	tests := []struct {
		name                 string
		kind                 string
		requiredAttestations []core.RequiredAttestation
		jobState             core.JobState
		wantWorkState        core.WorkExecutionState
	}{
		{"queued maps to claimed", "", nil, core.JobStateQueued, core.WorkExecutionStateClaimed},
		{"created maps to claimed", "", nil, core.JobStateCreated, core.WorkExecutionStateClaimed},
		{"starting maps to in_progress", "", nil, core.JobStateStarting, core.WorkExecutionStateInProgress},
		{"running maps to in_progress", "", nil, core.JobStateRunning, core.WorkExecutionStateInProgress},
		{"waiting_input maps to in_progress", "", nil, core.JobStateWaitingInput, core.WorkExecutionStateInProgress},
		{"completed task maps to checking", "", nil, core.JobStateCompleted, core.WorkExecutionStateChecking},
		{"completed attestation child maps to done", "attest", nil, core.JobStateCompleted, core.WorkExecutionStateDone},
		{"failed maps to failed", "", nil, core.JobStateFailed, core.WorkExecutionStateFailed},
		{"cancelled maps to cancelled", "", nil, core.JobStateCancelled, core.WorkExecutionStateCancelled},
		{"blocked maps to blocked", "", nil, core.JobStateBlocked, core.WorkExecutionStateBlocked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()

			// Create work item
			work, err := svc.CreateWork(ctx, WorkCreateRequest{
				Title:                "mapping test",
				Objective:            "test job-to-work state mapping",
				Kind:                 tt.kind,
				RequiredAttestations: tt.requiredAttestations,
			})
			if err != nil {
				t.Fatalf("CreateWork: %v", err)
			}

			// Create a job
			job := core.JobRecord{
				JobID:     core.GenerateID("job"),
				SessionID: core.GenerateID("ses"),
				WorkID:    work.WorkID,
				State:     tt.jobState,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}

			// Sync state
			if err := svc.syncWorkStateFromJob(ctx, job, nil); err != nil {
				t.Fatalf("syncWorkStateFromJob: %v", err)
			}

			// Verify mapping
			updated, err := svc.store.GetWorkItem(ctx, work.WorkID)
			if err != nil {
				t.Fatalf("GetWorkItem: %v", err)
			}
			if updated.ExecutionState != tt.wantWorkState {
				t.Errorf("job state %s: expected work state %s, got %s",
					tt.jobState, tt.wantWorkState, updated.ExecutionState)
			}
		})
	}
}

// TestParentAggregationFromChildren verifies VAL-LIFECYCLE-004:
// Parent aggregation resolves deterministically from child outcomes.
func TestParentAggregationFromChildren(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	parent, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "parent aggregation test",
		Objective: "test parent state from child outcomes",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "review1", Blocking: true},
			{VerifierKind: "attestation", Method: "review2", Blocking: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork parent: %v", err)
	}
	job := core.JobRecord{
		JobID:     core.GenerateID("job"),
		SessionID: core.GenerateID("ses"),
		Adapter:   "codex",
		State:     core.JobStateCompleted,
		Summary:   map[string]any{"model": "gpt-5.4"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := svc.spawnAttestationChildren(ctx, *parent, job); err != nil {
		t.Fatalf("spawnAttestationChildren: %v", err)
	}

	children, err := svc.store.ListWorkChildren(ctx, parent.WorkID, 10)
	if err != nil {
		t.Fatalf("ListWorkChildren: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 attestation children, got %d", len(children))
	}

	var child1, child2 core.WorkItemRecord
	for _, child := range children {
		slotIdx, _ := metadataInt(child.Metadata, "slot_index")
		switch slotIdx {
		case 0:
			child1 = child
		case 1:
			child2 = child
		}
	}
	if child1.WorkID == "" || child2.WorkID == "" {
		t.Fatalf("expected both attestation slots to be present, got %+v", children)
	}

	// Complete child1
	if _, err := svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         child1.WorkID,
		ExecutionState: core.WorkExecutionStateDone,
		ForceDone:      true,
	}); err != nil {
		t.Fatalf("UpdateWork child1: %v", err)
	}

	// Refresh parent state (child1 done, child2 pending)
	if err := svc.refreshAttestationParentState(ctx, parent.WorkID); err != nil {
		t.Fatalf("refreshAttestationParentState: %v", err)
	}

	// Parent should still be checking (child2 not done)
	parentItem, _ := svc.store.GetWorkItem(ctx, parent.WorkID)
	if parentItem.ExecutionState != core.WorkExecutionStateChecking {
		t.Fatalf("expected checking with one child done, got %s", parentItem.ExecutionState)
	}

	// Fail child2
	if _, err := svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         child2.WorkID,
		ExecutionState: core.WorkExecutionStateFailed,
	}); err != nil {
		t.Fatalf("UpdateWork child2: %v", err)
	}

	// Refresh parent state (child1 done, child2 failed)
	if err := svc.refreshAttestationParentState(ctx, parent.WorkID); err != nil {
		t.Fatalf("refreshAttestationParentState: %v", err)
	}

	// Parent should be failed (blocking child failed)
	parentItem, _ = svc.store.GetWorkItem(ctx, parent.WorkID)
	if parentItem.ExecutionState != core.WorkExecutionStateFailed {
		t.Fatalf("expected failed state with blocking child failed, got %s", parentItem.ExecutionState)
	}
}

func TestResetWorkIgnoresHistoricalAttestationChildren(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	parent, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "historical child test",
		Objective: "old attestation children must not leak into new attempts",
		RequiredAttestations: []core.RequiredAttestation{
			{VerifierKind: "attestation", Method: "review", Blocking: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	job := func() core.JobRecord {
		return core.JobRecord{
			JobID:     core.GenerateID("job"),
			SessionID: core.GenerateID("ses"),
			Adapter:   "codex",
			State:     core.JobStateCompleted,
			Summary:   map[string]any{"model": "gpt-5.4"},
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
	}

	if err := svc.spawnAttestationChildren(ctx, *parent, job()); err != nil {
		t.Fatalf("spawnAttestationChildren attempt 1: %v", err)
	}

	children, err := svc.store.ListWorkChildren(ctx, parent.WorkID, 10)
	if err != nil {
		t.Fatalf("ListWorkChildren attempt 1: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 child in attempt 1, got %d", len(children))
	}

	if _, err := svc.UpdateWork(ctx, WorkUpdateRequest{
		WorkID:         children[0].WorkID,
		ExecutionState: core.WorkExecutionStateFailed,
	}); err != nil {
		t.Fatalf("UpdateWork failed child: %v", err)
	}
	if err := svc.refreshAttestationParentState(ctx, parent.WorkID); err != nil {
		t.Fatalf("refreshAttestationParentState attempt 1: %v", err)
	}

	parent, err = svc.ResetWork(ctx, WorkResetRequest{
		WorkID:    parent.WorkID,
		Reason:    "retry after failed review",
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("ResetWork: %v", err)
	}
	if parent.AttemptEpoch != 2 {
		t.Fatalf("expected AttemptEpoch 2 after reset, got %d", parent.AttemptEpoch)
	}

	if err := svc.spawnAttestationChildren(ctx, *parent, job()); err != nil {
		t.Fatalf("spawnAttestationChildren attempt 2: %v", err)
	}
	if err := svc.refreshAttestationParentState(ctx, parent.WorkID); err != nil {
		t.Fatalf("refreshAttestationParentState attempt 2: %v", err)
	}

	parentItem, err := svc.store.GetWorkItem(ctx, parent.WorkID)
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	if parentItem.ExecutionState != core.WorkExecutionStateChecking {
		t.Fatalf("expected new attempt to remain checking, got %s", parentItem.ExecutionState)
	}

	children, err = svc.store.ListWorkChildren(ctx, parent.WorkID, 10)
	if err != nil {
		t.Fatalf("ListWorkChildren attempt 2: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected historical and fresh child to coexist, got %d", len(children))
	}
}

// TestEscalationFieldsRoundTrip tests that escalation fields round-trip correctly
// through JSON serialization (simulating storage/persistence).
func TestEscalationFieldsRoundTrip(t *testing.T) {
	original := []core.RequiredAttestation{
		{VerifierKind: "attestation", Method: "automated_review", Blocking: true},
		{
			VerifierKind:     "attestation",
			Method:           "security_scan",
			Blocking:         true,
			EscalatedAt:      func() *time.Time { t := time.Now(); return &t }(),
			EscalationBy:     "test-user",
			EscalationReason: "Critical security fix required",
		},
	}

	// Serialize to JSON (simulating storage)
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Deserialize back (simulating retrieval)
	var restored []core.RequiredAttestation
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify original unchanged
	if restored[0].EscalatedAt != nil || restored[0].EscalationBy != "" {
		t.Error("original attestation should have nil escalation fields")
	}

	// Verify escalated attestation restored correctly
	if restored[1].EscalatedAt == nil {
		t.Error("expected EscalatedAt to be restored")
	}
	if restored[1].EscalationBy != "test-user" {
		t.Errorf("expected EscalationBy 'test-user', got %q", restored[1].EscalationBy)
	}
	if restored[1].EscalationReason != "Critical security fix required" {
		t.Errorf("expected EscalationReason, got %q", restored[1].EscalationReason)
	}
}
