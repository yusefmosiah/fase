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

	"github.com/yusefmosiah/cagent/internal/core"
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

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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
}

func TestClaudeRunStatusUsesVendorCost(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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
}

func TestWaitStatusReturnsTerminalState(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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
		}
	}
}

func TestTransferExportAndRun(t *testing.T) {
	stateDir := t.TempDir()
	configDir := t.TempDir()
	cacheDir := t.TempDir()

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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

	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
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

func setTestExecutable(t *testing.T) {
	t.Helper()

	testBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cagent-service-test-*")
		if err != nil {
			testBinaryErr = err
			return
		}
		testBinaryPath = filepath.Join(dir, "cagent")
		cmd := exec.Command("go", "build", "-o", testBinaryPath, "./cmd/cagent")
		cmd.Dir = filepath.Join("..", "..")
		output, err := cmd.CombinedOutput()
		if err != nil {
			testBinaryErr = errors.New(string(output))
			return
		}
	})
	if testBinaryErr != nil {
		t.Fatalf("build cagent binary: %v", testBinaryErr)
	}

	t.Setenv("CAGENT_EXECUTABLE", testBinaryPath)
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
