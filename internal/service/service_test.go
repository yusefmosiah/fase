package service

import (
	"bytes"
	"context"
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
