package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type cliRunResult struct {
	Job struct {
		JobID     string `json:"job_id"`
		SessionID string `json:"session_id"`
		State     string `json:"state"`
	} `json:"job"`
	Session struct {
		SessionID string `json:"session_id"`
	} `json:"session"`
}

type cliStatusResult struct {
	Job struct {
		JobID string `json:"job_id"`
		State string `json:"state"`
	} `json:"job"`
}

type cliJobRecord struct {
	JobID string `json:"job_id"`
	State string `json:"state"`
}

type cliSessionResult struct {
	SessionID string `json:"session_id"`
}

type cliTransferExportResult struct {
	Transfer struct {
		TransferID string `json:"transfer_id"`
		Packet     struct {
			Reason string `json:"reason"`
			Mode   string `json:"mode"`
		} `json:"packet"`
	} `json:"transfer"`
	Path string `json:"path"`
}

type cliDebriefResult struct {
	Job struct {
		JobID string `json:"job_id"`
		State string `json:"state"`
	} `json:"job"`
	Session struct {
		SessionID string `json:"session_id"`
	} `json:"session"`
	Path string `json:"path"`
}

func TestDetachedRunCanBeCancelled(t *testing.T) {
	binary := buildCagentBinary(t)
	configPath := writeFakeCodexConfig(t)

	runOutput := runCagent(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "hang for cancellation test")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal detached run: %v\n%s", err, runOutput)
	}
	if runResult.Job.State != "queued" {
		t.Fatalf("expected queued detached job, got %q", runResult.Job.State)
	}

	waitForJobState(t, binary, configPath, runResult.Job.JobID, map[string]bool{"queued": true, "running": true})

	cancelOutput := runCagent(t, binary, configPath, "--json", "cancel", runResult.Job.JobID)
	var cancelled cliJobRecord
	if err := json.Unmarshal([]byte(cancelOutput), &cancelled); err != nil {
		t.Fatalf("unmarshal cancel output: %v\n%s", err, cancelOutput)
	}
	if cancelled.State != "cancelled" {
		t.Fatalf("expected cancelled job state, got %q", cancelled.State)
	}

	waitForJobState(t, binary, configPath, runResult.Job.JobID, map[string]bool{"cancelled": true})
}

func TestFollowLogsAndListFilters(t *testing.T) {
	binary := buildCagentBinary(t)
	configPath := writeFakeCodexConfig(t)

	runOutput := runCagent(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "slow follow test")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal detached run: %v\n%s", err, runOutput)
	}

	logOutput := runCagent(t, binary, configPath, "logs", runResult.Job.JobID, "--follow")
	if !strings.Contains(logOutput, "assistant.message") {
		t.Fatalf("expected assistant.message in follow output:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "job.completed") {
		t.Fatalf("expected job.completed in follow output:\n%s", logOutput)
	}

	jobsOutput := runCagent(t, binary, configPath, "--json", "list", "--kind", "jobs", "--adapter", "codex", "--state", "completed", "--session", runResult.Session.SessionID)
	var jobs []map[string]any
	if err := json.Unmarshal([]byte(jobsOutput), &jobs); err != nil {
		t.Fatalf("unmarshal filtered jobs: %v\n%s", err, jobsOutput)
	}
	if len(jobs) == 0 {
		t.Fatalf("expected completed job in filtered list")
	}

	sessionsOutput := runCagent(t, binary, configPath, "--json", "list", "--kind", "sessions", "--adapter", "codex", "--state", "active")
	var sessions []cliSessionResult
	if err := json.Unmarshal([]byte(sessionsOutput), &sessions); err != nil {
		t.Fatalf("unmarshal filtered sessions: %v\n%s", err, sessionsOutput)
	}
	if len(sessions) == 0 {
		t.Fatalf("expected active codex session in filtered session list")
	}
}

func TestTransferExportAndRun(t *testing.T) {
	binary := buildCagentBinary(t)
	configPath := writeFakeCodexGeminiConfig(t)

	runOutput := runCagent(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "build transfer source")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal source run: %v\n%s", err, runOutput)
	}
	waitForJobState(t, binary, configPath, runResult.Job.JobID, map[string]bool{"completed": true})

	exportOutput := runCagent(t, binary, configPath, "--json", "transfer", "export", "--job", runResult.Job.JobID, "--reason", "provider outage", "--mode", "recovery")
	var transferResult cliTransferExportResult
	if err := json.Unmarshal([]byte(exportOutput), &transferResult); err != nil {
		t.Fatalf("unmarshal transfer export: %v\n%s", err, exportOutput)
	}
	if transferResult.Transfer.TransferID == "" {
		t.Fatal("expected transfer id")
	}
	if transferResult.Transfer.Packet.Reason != "provider outage" {
		t.Fatalf("unexpected transfer reason: %q", transferResult.Transfer.Packet.Reason)
	}
	if transferResult.Transfer.Packet.Mode != "recovery" {
		t.Fatalf("unexpected transfer mode: %q", transferResult.Transfer.Packet.Mode)
	}
	if _, err := os.Stat(transferResult.Path); err != nil {
		t.Fatalf("expected transfer file at %q: %v", transferResult.Path, err)
	}

	targetOutput := runCagent(t, binary, configPath, "--json", "transfer", "run", "--transfer", transferResult.Transfer.TransferID, "--adapter", "gemini", "--cwd", t.TempDir())
	var targetRun cliRunResult
	if err := json.Unmarshal([]byte(targetOutput), &targetRun); err != nil {
		t.Fatalf("unmarshal transfer run: %v\n%s", err, targetOutput)
	}
	waitForJobState(t, binary, configPath, targetRun.Job.JobID, map[string]bool{"completed": true})
}

func TestDebriefQueuesAndWritesArtifact(t *testing.T) {
	binary := buildCagentBinary(t)
	configPath := writeFakeCodexConfig(t)

	runOutput := runCagent(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "build debrief source")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal source run: %v\n%s", err, runOutput)
	}
	waitForJobState(t, binary, configPath, runResult.Job.JobID, map[string]bool{"completed": true})

	debriefOutput := runCagent(t, binary, configPath, "--json", "debrief", "--session", runResult.Session.SessionID)
	var debriefResult cliDebriefResult
	if err := json.Unmarshal([]byte(debriefOutput), &debriefResult); err != nil {
		t.Fatalf("unmarshal debrief run: %v\n%s", err, debriefOutput)
	}
	if debriefResult.Path == "" {
		t.Fatal("expected debrief output path")
	}
	waitForJobState(t, binary, configPath, debriefResult.Job.JobID, map[string]bool{"completed": true})

	data, err := os.ReadFile(debriefResult.Path)
	if err != nil {
		t.Fatalf("read debrief artifact: %v", err)
	}
	if !strings.Contains(string(data), "# Recommended Next Step") {
		t.Fatalf("expected markdown debrief headings, got:\n%s", data)
	}

	logOutput := runCagent(t, binary, configPath, "logs", debriefResult.Job.JobID)
	if !strings.Contains(logOutput, "debrief.exported") {
		t.Fatalf("expected debrief.exported event in logs:\n%s", logOutput)
	}
}

func buildCagentBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "cagent")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/cagent")
	cmd.Dir = filepath.Join("..", "..")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build cagent binary: %v\n%s", err, output)
	}
	return binary
}

func writeFakeCodexConfig(t *testing.T) string {
	t.Helper()

	configDir := t.TempDir()
	stateDir := t.TempDir()
	cacheDir := t.TempDir()
	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "codex"))
	if err != nil {
		t.Fatalf("resolve fake codex path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake codex: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[store]\nstate_dir = \"" + stateDir + "\"\n\n[adapters.codex]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
	return configPath
}

func writeFakeCodexGeminiConfig(t *testing.T) string {
	t.Helper()

	configDir := t.TempDir()
	stateDir := t.TempDir()
	cacheDir := t.TempDir()
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
	configBody := []byte("[store]\nstate_dir = \"" + stateDir + "\"\n\n[adapters.codex]\nbinary = \"" + fakeCodex + "\"\nenabled = true\n\n[adapters.gemini]\nbinary = \"" + fakeGemini + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
	return configPath
}

func runCagent(t *testing.T, binary, configPath string, args ...string) string {
	t.Helper()

	cmd := exec.Command(binary, append([]string{"--config", configPath}, args...)...)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run cagent %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func waitForJobState(t *testing.T, binary, configPath, jobID string, allowed map[string]bool) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		output := runCagent(t, binary, configPath, "--json", "status", jobID)
		var status cliStatusResult
		if err := json.Unmarshal([]byte(output), &status); err != nil {
			t.Fatalf("unmarshal status: %v\n%s", err, output)
		}
		if allowed[status.Job.State] {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach expected state set %v", jobID, allowed)
}
