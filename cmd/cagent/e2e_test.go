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
	Usage *struct {
		InputTokens              int64  `json:"input_tokens"`
		OutputTokens             int64  `json:"output_tokens"`
		TotalTokens              int64  `json:"total_tokens"`
		CachedInputTokens        int64  `json:"cached_input_tokens"`
		CacheReadInputTokens     int64  `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64  `json:"cache_creation_input_tokens"`
		Model                    string `json:"model"`
		Provider                 string `json:"provider"`
	} `json:"usage"`
	Cost *struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
		Estimated    bool    `json:"estimated"`
		Source       string  `json:"source"`
	} `json:"cost"`
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

type cliArtifactRecord struct {
	ArtifactID string `json:"artifact_id"`
	JobID      string `json:"job_id"`
	SessionID  string `json:"session_id"`
	Kind       string `json:"kind"`
	Path       string `json:"path"`
}

type cliArtifactResult struct {
	Artifact cliArtifactRecord `json:"artifact"`
	Content  string            `json:"content"`
}

type cliCatalogResult struct {
	Snapshot struct {
		SnapshotID string              `json:"snapshot_id"`
		Entries    []cliCatalogEntry   `json:"entries"`
		Issues     []map[string]string `json:"issues"`
	} `json:"snapshot"`
}

type cliCatalogEntry struct {
	Adapter      string `json:"adapter"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	AuthMethod   string `json:"auth_method"`
	BillingClass string `json:"billing_class"`
	Pricing      *struct {
		InputUSDPerMTok    float64 `json:"input_usd_per_mtok"`
		OutputUSDPerMTok   float64 `json:"output_usd_per_mtok"`
		CachedInputPerMTok float64 `json:"cached_input_usd_per_mtok"`
		Source             string  `json:"source"`
	} `json:"pricing"`
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

func TestStatusWaitReturnsTerminalJob(t *testing.T) {
	binary := buildCagentBinary(t)
	configPath := writeFakeCodexConfig(t)

	runOutput := runCagent(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "slow wait test")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal detached run: %v\n%s", err, runOutput)
	}

	statusOutput := runCagent(t, binary, configPath, "--json", "status", "--wait", "--timeout", "10s", runResult.Job.JobID)
	var status cliStatusResult
	if err := json.Unmarshal([]byte(statusOutput), &status); err != nil {
		t.Fatalf("unmarshal waited status: %v\n%s", err, statusOutput)
	}
	if status.Job.State != "completed" {
		t.Fatalf("expected completed waited status, got %q", status.Job.State)
	}
}

func TestStatusReportsUsageAndEstimatedCost(t *testing.T) {
	binary := buildCagentBinary(t)
	configPath := writeFakeCodexConfig(t)

	runOutput := runCagent(t, binary, configPath, "--json", "run", "--adapter", "codex", "--model", "gpt-5-nano", "--cwd", t.TempDir(), "--prompt", "usage reporting test")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal run output: %v\n%s", err, runOutput)
	}
	waitForJobState(t, binary, configPath, runResult.Job.JobID, map[string]bool{"completed": true})

	statusOutput := runCagent(t, binary, configPath, "--json", "status", runResult.Job.JobID)
	var status cliStatusResult
	if err := json.Unmarshal([]byte(statusOutput), &status); err != nil {
		t.Fatalf("unmarshal status output: %v\n%s", err, statusOutput)
	}
	if status.Usage == nil || status.Usage.InputTokens == 0 || status.Usage.OutputTokens == 0 {
		t.Fatalf("expected usage in status, got %+v", status.Usage)
	}
	if status.Cost == nil || status.Cost.TotalCostUSD <= 0 {
		t.Fatalf("expected estimated cost in status, got %+v", status.Cost)
	}
	if !status.Cost.Estimated {
		t.Fatalf("expected estimated cost for codex fake run, got %+v", status.Cost)
	}
}

func TestClaudeStatusReportsVendorCost(t *testing.T) {
	binary := buildCagentBinary(t)
	configPath := writeFakeClaudeConfig(t)

	runOutput := runCagent(t, binary, configPath, "--json", "run", "--adapter", "claude", "--model", "claude-sonnet-4-6", "--cwd", t.TempDir(), "--prompt", "vendor cost reporting test")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal run output: %v\n%s", err, runOutput)
	}
	waitForJobState(t, binary, configPath, runResult.Job.JobID, map[string]bool{"completed": true})

	statusOutput := runCagent(t, binary, configPath, "--json", "status", runResult.Job.JobID)
	var status cliStatusResult
	if err := json.Unmarshal([]byte(statusOutput), &status); err != nil {
		t.Fatalf("unmarshal status output: %v\n%s", err, statusOutput)
	}
	if status.Cost == nil || status.Cost.TotalCostUSD <= 0 {
		t.Fatalf("expected vendor cost in status, got %+v", status.Cost)
	}
	if status.Cost.Estimated {
		t.Fatalf("expected vendor-reported cost, got %+v", status.Cost)
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

	artifactsOutput := runCagent(t, binary, configPath, "--json", "artifacts", "list", "--job", debriefResult.Job.JobID, "--kind", "debrief")
	var artifacts []cliArtifactRecord
	if err := json.Unmarshal([]byte(artifactsOutput), &artifacts); err != nil {
		t.Fatalf("unmarshal artifacts list: %v\n%s", err, artifactsOutput)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected one debrief artifact, got %+v", artifacts)
	}

	artifactOutput := runCagent(t, binary, configPath, "--json", "artifacts", "show", artifacts[0].ArtifactID)
	var artifact cliArtifactResult
	if err := json.Unmarshal([]byte(artifactOutput), &artifact); err != nil {
		t.Fatalf("unmarshal artifact show: %v\n%s", err, artifactOutput)
	}
	if !strings.Contains(artifact.Content, "# Objective") {
		t.Fatalf("expected debrief content from artifact show, got:\n%s", artifact.Content)
	}
}

func TestCatalogSyncAndShow(t *testing.T) {
	binary := buildCagentBinary(t)
	configPath := writeFakeCatalogConfig(t)
	t.Setenv("GEMINI_API_KEY", "test-gemini-key")

	syncOutput := runCagent(t, binary, configPath, "--json", "catalog", "sync")
	var synced cliCatalogResult
	if err := json.Unmarshal([]byte(syncOutput), &synced); err != nil {
		t.Fatalf("unmarshal catalog sync: %v\n%s", err, syncOutput)
	}
	if synced.Snapshot.SnapshotID == "" {
		t.Fatal("expected catalog snapshot id")
	}
	if len(synced.Snapshot.Entries) == 0 {
		t.Fatal("expected catalog entries")
	}

	showOutput := runCagent(t, binary, configPath, "--json", "catalog", "show")
	var shown cliCatalogResult
	if err := json.Unmarshal([]byte(showOutput), &shown); err != nil {
		t.Fatalf("unmarshal catalog show: %v\n%s", err, showOutput)
	}
	if shown.Snapshot.SnapshotID != synced.Snapshot.SnapshotID {
		t.Fatalf("expected shown snapshot %q, got %q", synced.Snapshot.SnapshotID, shown.Snapshot.SnapshotID)
	}

	assertEntry := func(adapter, provider, model, authMethod, billing string) {
		t.Helper()
		for _, entry := range shown.Snapshot.Entries {
			if entry.Adapter == adapter && entry.Provider == provider && entry.Model == model {
				if authMethod != "" && entry.AuthMethod != authMethod {
					t.Fatalf("expected auth method %q for %+v, got %q", authMethod, entry, entry.AuthMethod)
				}
				if billing != "" && entry.BillingClass != billing {
					t.Fatalf("expected billing class %q for %+v, got %q", billing, entry, entry.BillingClass)
				}
				return
			}
		}
		t.Fatalf("missing catalog entry adapter=%s provider=%s model=%s", adapter, provider, model)
	}

	assertEntry("codex", "openai", "", "chatgpt", "subscription")
	assertEntry("claude", "firstparty", "", "claude_ai", "subscription")
	assertEntry("opencode", "openai", "gpt-5-nano", "oauth", "subscription")
	assertEntry("pi", "google", "gemini-2.5-flash", "api_key", "metered_api")
	assertEntry("factory", "factory", "glm-5", "api_key", "metered_api")
	assertEntry("gemini", "google", "", "api_key", "metered_api")

	for _, entry := range shown.Snapshot.Entries {
		if entry.Adapter == "opencode" && entry.Provider == "openai" && entry.Model == "gpt-5-nano" {
			if entry.Pricing == nil || entry.Pricing.InputUSDPerMTok <= 0 || entry.Pricing.OutputUSDPerMTok <= 0 {
				t.Fatalf("expected pricing on catalog entry, got %+v", entry)
			}
		}
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

func writeFakeClaudeConfig(t *testing.T) string {
	t.Helper()

	configDir := t.TempDir()
	stateDir := t.TempDir()
	cacheDir := t.TempDir()
	fakeClaude, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "claude"))
	if err != nil {
		t.Fatalf("resolve fake claude path: %v", err)
	}
	if err := os.Chmod(fakeClaude, 0o755); err != nil {
		t.Fatalf("chmod fake claude: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[store]\nstate_dir = \"" + stateDir + "\"\n\n[adapters.claude]\nbinary = \"" + fakeClaude + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("CAGENT_CONFIG_DIR", configDir)
	t.Setenv("CAGENT_STATE_DIR", stateDir)
	t.Setenv("CAGENT_CACHE_DIR", cacheDir)
	return configPath
}

func writeFakeCatalogConfig(t *testing.T) string {
	t.Helper()

	configDir := t.TempDir()
	stateDir := t.TempDir()
	cacheDir := t.TempDir()
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
		"[store]\nstate_dir = \"" + stateDir + "\"\n\n" +
			"[adapters.codex]\nbinary = \"" + fakeCodex + "\"\nenabled = true\n\n" +
			"[adapters.claude]\nbinary = \"" + fakeClaude + "\"\nenabled = true\n\n" +
			"[adapters.opencode]\nbinary = \"" + fakeOpenCode + "\"\nenabled = true\n\n" +
			"[adapters.pi]\nbinary = \"" + fakePi + "\"\nenabled = true\n\n" +
			"[adapters.factory]\nbinary = \"" + fakeDroid + "\"\nenabled = true\n\n" +
			"[adapters.gemini]\nbinary = \"" + fakeGemini + "\"\nenabled = true\n",
	)
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
