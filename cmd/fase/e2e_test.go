package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
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
		ObservedAt   string  `json:"observed_at"`
	} `json:"cost"`
	VendorCost *struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
		Estimated    bool    `json:"estimated"`
		Source       string  `json:"source"`
		ObservedAt   string  `json:"observed_at"`
	} `json:"vendor_cost"`
	EstimatedCost *struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
		Estimated    bool    `json:"estimated"`
		Source       string  `json:"source"`
		ObservedAt   string  `json:"observed_at"`
	} `json:"estimated_cost"`
	UsageByModel []struct {
		Model        string  `json:"model"`
		TotalTokens  int64   `json:"total_tokens"`
		CostUSD      float64 `json:"cost_usd"`
		InputTokens  int64   `json:"input_tokens"`
		OutputTokens int64   `json:"output_tokens"`
	} `json:"usage_by_model"`
	UsageAttribution *struct {
		Role           string `json:"role"`
		AttemptEpoch   int    `json:"attempt_epoch"`
		CurrentAttempt bool   `json:"current_attempt"`
		ParentWorkID   string `json:"parent_work_id"`
		WorkerJobID    string `json:"worker_job_id"`
	} `json:"usage_attribution"`
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
	ProbeStatus  string `json:"probe_status"`
	ProbeMessage string `json:"probe_message"`
	Pricing      *struct {
		InputUSDPerMTok    float64 `json:"input_usd_per_mtok"`
		OutputUSDPerMTok   float64 `json:"output_usd_per_mtok"`
		CachedInputPerMTok float64 `json:"cached_input_usd_per_mtok"`
		Source             string  `json:"source"`
	} `json:"pricing"`
}

type cliHistoryMatch struct {
	Kind             string `json:"kind"`
	ID               string `json:"id"`
	WorkID           string `json:"work_id"`
	SessionID        string `json:"session_id"`
	JobID            string `json:"job_id"`
	Adapter          string `json:"adapter"`
	Model            string `json:"model"`
	Snippet          string `json:"snippet"`
	Path             string `json:"path"`
	UsageAttribution *struct {
		Role           string `json:"role"`
		AttemptEpoch   int    `json:"attempt_epoch"`
		CurrentAttempt bool   `json:"current_attempt"`
		ParentWorkID   string `json:"parent_work_id"`
		WorkerJobID    string `json:"worker_job_id"`
	} `json:"usage_attribution"`
	Usage *struct {
		Model       string `json:"model"`
		TotalTokens int64  `json:"total_tokens"`
	} `json:"usage"`
	UsageByModel []struct {
		Model string `json:"model"`
	} `json:"usage_by_model"`
}

type cliWorkItem struct {
	WorkID           string         `json:"work_id"`
	Title            string         `json:"title"`
	Kind             string         `json:"kind"`
	ExecutionState   string         `json:"execution_state"`
	ApprovalState    string         `json:"approval_state"`
	RequiredDocs     []string       `json:"required_docs"`
	CurrentJobID     string         `json:"current_job_id"`
	CurrentSessionID string         `json:"current_session_id"`
	ClaimedBy        string         `json:"claimed_by"`
	ClaimedUntil     string         `json:"claimed_until"`
	Metadata         map[string]any `json:"metadata"`
}

type cliWorkNote struct {
	NoteID   string `json:"note_id"`
	WorkID   string `json:"work_id"`
	NoteType string `json:"note_type"`
	Body     string `json:"body"`
}

type cliWorkShowResult struct {
	Work     cliWorkItem   `json:"work"`
	Children []cliWorkItem `json:"children"`
	Notes    []cliWorkNote `json:"notes"`
	Jobs     []struct {
		JobID  string `json:"job_id"`
		WorkID string `json:"work_id"`
		State  string `json:"state"`
	} `json:"jobs"`
	Proposals []struct {
		ProposalID string `json:"proposal_id"`
		State      string `json:"state"`
	} `json:"proposals"`
	CheckRecords []struct {
		CheckID string `json:"check_id"`
		Result  string `json:"result"`
		Report  struct {
			TestOutput   string `json:"test_output"`
			CheckerNotes string `json:"checker_notes"`
		} `json:"report"`
	} `json:"check_records"`
	Attestations []struct {
		AttestationID string `json:"attestation_id"`
		Result        string `json:"result"`
	} `json:"attestations"`
	Approvals []struct {
		ApprovalID        string `json:"approval_id"`
		Status            string `json:"status"`
		ApprovedCommitOID string `json:"approved_commit_oid"`
	} `json:"approvals"`
	Promotions []struct {
		PromotionID string `json:"promotion_id"`
		Environment string `json:"environment"`
		TargetRef   string `json:"target_ref"`
	} `json:"promotions"`
	Artifacts []struct {
		ArtifactID string `json:"artifact_id"`
		Kind       string `json:"kind"`
		Path       string `json:"path"`
	} `json:"artifacts"`
	Docs []struct {
		DocID          string `json:"doc_id"`
		Path           string `json:"path"`
		Title          string `json:"title"`
		RepoFileExists bool   `json:"repo_file_exists"`
		MatchesRepo    bool   `json:"matches_repo"`
	} `json:"docs"`
}

type cliWorkProposalPayload struct {
	Proposal struct {
		ProposalID   string `json:"proposal_id"`
		ProposalType string `json:"proposal_type"`
		State        string `json:"state"`
		SourceWorkID string `json:"source_work_id"`
		TargetWorkID string `json:"target_work_id"`
	} `json:"proposal"`
	CreatedWork *cliWorkItem `json:"created_work"`
}

type cliAttestationPayload struct {
	Attestation struct {
		AttestationID string `json:"attestation_id"`
		Result        string `json:"result"`
		SubjectID     string `json:"subject_id"`
	} `json:"attestation"`
	Work cliWorkItem `json:"work"`
}

type cliPromotionPayload struct {
	Promotion struct {
		PromotionID string `json:"promotion_id"`
		Environment string `json:"environment"`
		TargetRef   string `json:"target_ref"`
	} `json:"promotion"`
	Work cliWorkItem `json:"work"`
}

func TestDetachedRunCanBeCancelled(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)

	runOutput := runFase(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "hang for cancellation test")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal detached run: %v\n%s", err, runOutput)
	}
	if runResult.Job.State != "queued" {
		t.Fatalf("expected queued detached job, got %q", runResult.Job.State)
	}

	waitForJobState(t, binary, configPath, runResult.Job.JobID, map[string]bool{"queued": true, "running": true})

	cancelOutput := runFase(t, binary, configPath, "--json", "cancel", runResult.Job.JobID)
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
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)

	runOutput := runFase(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "slow follow test")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal detached run: %v\n%s", err, runOutput)
	}

	logOutput := runFase(t, binary, configPath, "logs", runResult.Job.JobID, "--follow")
	if !strings.Contains(logOutput, "assistant.message") {
		t.Fatalf("expected assistant.message in follow output:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "job.completed") {
		t.Fatalf("expected job.completed in follow output:\n%s", logOutput)
	}

	jobsOutput := runFase(t, binary, configPath, "--json", "list", "--kind", "jobs", "--adapter", "codex", "--state", "completed", "--session", runResult.Session.SessionID)
	var jobs []map[string]any
	if err := json.Unmarshal([]byte(jobsOutput), &jobs); err != nil {
		t.Fatalf("unmarshal filtered jobs: %v\n%s", err, jobsOutput)
	}
	if len(jobs) == 0 {
		t.Fatalf("expected completed job in filtered list")
	}

	sessionsOutput := runFase(t, binary, configPath, "--json", "list", "--kind", "sessions", "--adapter", "codex", "--state", "active")
	var sessions []cliSessionResult
	if err := json.Unmarshal([]byte(sessionsOutput), &sessions); err != nil {
		t.Fatalf("unmarshal filtered sessions: %v\n%s", err, sessionsOutput)
	}
	if len(sessions) == 0 {
		t.Fatalf("expected active codex session in filtered session list")
	}
}

func TestStatusWaitReturnsTerminalJob(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)

	runOutput := runFase(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "slow wait test")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal detached run: %v\n%s", err, runOutput)
	}

	statusOutput := runFase(t, binary, configPath, "--json", "status", "--wait", "--timeout", "10s", runResult.Job.JobID)
	var status cliStatusResult
	if err := json.Unmarshal([]byte(statusOutput), &status); err != nil {
		t.Fatalf("unmarshal waited status: %v\n%s", err, statusOutput)
	}
	if status.Job.State != "completed" {
		t.Fatalf("expected completed waited status, got %q", status.Job.State)
	}
}

func TestStatusReportsUsageAndEstimatedCost(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)

	runOutput := runFase(t, binary, configPath, "--json", "run", "--adapter", "codex", "--model", "gpt-5-nano", "--cwd", t.TempDir(), "--prompt", "usage reporting test")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal run output: %v\n%s", err, runOutput)
	}
	waitForJobState(t, binary, configPath, runResult.Job.JobID, map[string]bool{"completed": true})

	statusOutput := runFase(t, binary, configPath, "--json", "status", runResult.Job.JobID)
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
	if status.EstimatedCost == nil || status.EstimatedCost.TotalCostUSD != status.Cost.TotalCostUSD {
		t.Fatalf("expected explicit estimated_cost in status, got %+v", status.EstimatedCost)
	}
	if status.EstimatedCost.ObservedAt == "" {
		t.Fatalf("expected estimated_cost observed_at provenance, got %+v", status.EstimatedCost)
	}
}

func TestClaudeStatusReportsVendorCost(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeClaudeConfig(t)

	runOutput := runFase(t, binary, configPath, "--json", "run", "--adapter", "claude", "--model", "claude-sonnet-4-6", "--cwd", t.TempDir(), "--prompt", "vendor cost reporting test")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal run output: %v\n%s", err, runOutput)
	}
	waitForJobState(t, binary, configPath, runResult.Job.JobID, map[string]bool{"completed": true})

	statusOutput := runFase(t, binary, configPath, "--json", "status", runResult.Job.JobID)
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
	if status.VendorCost == nil || status.VendorCost.TotalCostUSD != status.Cost.TotalCostUSD {
		t.Fatalf("expected explicit vendor_cost in status, got %+v", status.VendorCost)
	}
	if status.EstimatedCost == nil || status.EstimatedCost.TotalCostUSD <= 0 {
		t.Fatalf("expected fallback estimated_cost alongside vendor cost, got %+v", status.EstimatedCost)
	}
	if status.EstimatedCost.ObservedAt == "" {
		t.Fatalf("expected estimated_cost observed_at provenance, got %+v", status.EstimatedCost)
	}
}

func TestListRunningJobsReturnsEmptyArray(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)

	output := runFase(t, binary, configPath, "--json", "list", "--state", "running")
	if strings.TrimSpace(output) != "[]" {
		t.Fatalf("expected empty JSON array, got %s", output)
	}
}

func TestTransferExportAndRun(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexGeminiConfig(t)

	runOutput := runFase(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "build transfer source")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal source run: %v\n%s", err, runOutput)
	}
	waitForJobState(t, binary, configPath, runResult.Job.JobID, map[string]bool{"completed": true})

	exportOutput := runFase(t, binary, configPath, "--json", "transfer", "export", "--job", runResult.Job.JobID, "--reason", "provider outage", "--mode", "recovery")
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

	targetOutput := runFase(t, binary, configPath, "--json", "transfer", "run", "--transfer", transferResult.Transfer.TransferID, "--adapter", "gemini", "--cwd", t.TempDir())
	var targetRun cliRunResult
	if err := json.Unmarshal([]byte(targetOutput), &targetRun); err != nil {
		t.Fatalf("unmarshal transfer run: %v\n%s", err, targetOutput)
	}
	waitForJobState(t, binary, configPath, targetRun.Job.JobID, map[string]bool{"completed": true})
}

func TestDebriefQueuesAndWritesArtifact(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)

	runOutput := runFase(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "build debrief source")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal source run: %v\n%s", err, runOutput)
	}
	waitForJobState(t, binary, configPath, runResult.Job.JobID, map[string]bool{"completed": true})

	debriefOutput := runFase(t, binary, configPath, "--json", "debrief", "--session", runResult.Session.SessionID)
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

	logOutput := runFase(t, binary, configPath, "logs", debriefResult.Job.JobID)
	if !strings.Contains(logOutput, "debrief.exported") {
		t.Fatalf("expected debrief.exported event in logs:\n%s", logOutput)
	}

	artifactsOutput := runFase(t, binary, configPath, "--json", "artifacts", "list", "--job", debriefResult.Job.JobID, "--kind", "debrief")
	var artifacts []cliArtifactRecord
	if err := json.Unmarshal([]byte(artifactsOutput), &artifacts); err != nil {
		t.Fatalf("unmarshal artifacts list: %v\n%s", err, artifactsOutput)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected one debrief artifact, got %+v", artifacts)
	}

	artifactOutput := runFase(t, binary, configPath, "--json", "artifacts", "show", artifacts[0].ArtifactID)
	var artifact cliArtifactResult
	if err := json.Unmarshal([]byte(artifactOutput), &artifact); err != nil {
		t.Fatalf("unmarshal artifact show: %v\n%s", err, artifactOutput)
	}
	if !strings.Contains(artifact.Content, "# Objective") {
		t.Fatalf("expected debrief content from artifact show, got:\n%s", artifact.Content)
	}
}

func TestHistorySearchFindsCanonicalMatches(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)

	runOutput := runFase(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "banana search workflow")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal run output: %v\n%s", err, runOutput)
	}
	waitForJobState(t, binary, configPath, runResult.Job.JobID, map[string]bool{"completed": true})

	searchOutput := runFase(t, binary, configPath, "--json", "history", "search", "--query", "banana", "--adapter", "codex")
	var matches []cliHistoryMatch
	if err := json.Unmarshal([]byte(searchOutput), &matches); err != nil {
		t.Fatalf("unmarshal history search output: %v\n%s", err, searchOutput)
	}
	if len(matches) == 0 {
		t.Fatalf("expected history matches, got none")
	}

	found := false
	for _, match := range matches {
		if match.Kind == "turn" && strings.Contains(strings.ToLower(match.Snippet), "banana") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected banana turn match in %+v", matches)
	}
}

func TestCatalogSyncAndShow(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCatalogConfig(t)
	t.Setenv("GEMINI_API_KEY", "test-gemini-key")

	syncOutput := runFase(t, binary, configPath, "--json", "catalog", "sync")
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

	showOutput := runFase(t, binary, configPath, "--json", "catalog", "show")
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

func TestCatalogProbeClassifiesEntries(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCatalogConfig(t)

	probeOutput := runFase(t, binary, configPath, "--json", "catalog", "probe", "--adapter", "opencode", "--provider", "openai", "--model", "gpt-5.3-codex-spark", "--timeout", "2s")
	var probed cliCatalogResult
	if err := json.Unmarshal([]byte(probeOutput), &probed); err != nil {
		t.Fatalf("unmarshal catalog probe: %v\n%s", err, probeOutput)
	}
	foundUnsupported := false
	for _, entry := range probed.Snapshot.Entries {
		if entry.Adapter == "opencode" && entry.Provider == "openai" && entry.Model == "gpt-5.3-codex-spark" {
			foundUnsupported = true
			if entry.ProbeStatus != "unsupported_by_plan" {
				t.Fatalf("expected unsupported_by_plan, got %+v", entry)
			}
		}
	}
	if !foundUnsupported {
		t.Fatal("missing probed openai/gpt-5.3-codex-spark entry")
	}

	timeoutOutput := runFase(t, binary, configPath, "--json", "catalog", "probe", "--adapter", "opencode", "--provider", "zai-coding-plan", "--model", "glm-4.7-flashx", "--timeout", "2s")
	if err := json.Unmarshal([]byte(timeoutOutput), &probed); err != nil {
		t.Fatalf("unmarshal timeout probe: %v\n%s", err, timeoutOutput)
	}
	foundTimeout := false
	for _, entry := range probed.Snapshot.Entries {
		if entry.Adapter == "opencode" && entry.Provider == "zai-coding-plan" && entry.Model == "glm-4.7-flashx" {
			foundTimeout = true
			if entry.ProbeStatus != "hung_or_unstable" {
				t.Fatalf("expected hung_or_unstable, got %+v", entry)
			}
		}
	}
	if !foundTimeout {
		t.Fatal("missing probed zai-coding-plan/glm-4.7-flashx entry")
	}
}

func TestWorkLifecycleCommands(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)
	projectDir := t.TempDir()
	stateDir := os.Getenv("FASE_STATE_DIR")
	cacheDir := os.Getenv("FASE_CACHE_DIR")
	configDir := os.Getenv("FASE_CONFIG_DIR")

	env := append(os.Environ(),
		"FASE_CAPABILITY_ENFORCEMENT=audit",
		"FASE_AGENT_TOKEN=",
		"FASE_CONFIG_DIR="+configDir,
		"FASE_STATE_DIR="+stateDir,
		"FASE_CACHE_DIR="+cacheDir,
	)

	serveCmd := exec.Command(binary, "--config", configPath, "serve", "--no-ui", "--no-browser")
	serveCmd.Dir = projectDir
	serveCmd.Env = env
	var serveLogs bytes.Buffer
	serveCmd.Stdout = &serveLogs
	serveCmd.Stderr = &serveLogs
	if err := serveCmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() {
		if serveCmd.Process != nil {
			_ = serveCmd.Process.Kill()
			_, _ = serveCmd.Process.Wait()
		}
	})

	waitForFile(t, filepath.Join(stateDir, "serve.json"), 10*time.Second, func() string {
		return serveLogs.String()
	})

	rootOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "create", "--title", "Root plan", "--objective", "Track work runtime implementation", "--kind", "plan")
	var rootWork cliWorkItem
	if err := json.Unmarshal([]byte(rootOutput), &rootWork); err != nil {
		t.Fatalf("unmarshal root work: %v\n%s", err, rootOutput)
	}
	if rootWork.WorkID == "" {
		t.Fatal("expected root work id")
	}

	childOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "create", "--title", "Implement child", "--objective", "Attach run lifecycle to work", "--kind", "implement", "--parent", rootWork.WorkID, "--head-commit", "abc123", "--required-attestations", `[{"verifier_kind":"deterministic","method":"test","blocking":true}]`)
	var childWork cliWorkItem
	if err := json.Unmarshal([]byte(childOutput), &childWork); err != nil {
		t.Fatalf("unmarshal child work: %v\n%s", err, childOutput)
	}

	runOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--work", childWork.WorkID, "--prompt", "work lifecycle test")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal work run: %v\n%s", err, runOutput)
	}
	waitForJobStateWithEnv(t, binary, configPath, projectDir, env, runResult.Job.JobID, map[string]bool{"completed": true})

	showOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "show", childWork.WorkID)
	var show cliWorkShowResult
	if err := json.Unmarshal([]byte(showOutput), &show); err != nil {
		t.Fatalf("unmarshal work show: %v\n%s", err, showOutput)
	}
	if show.Work.CurrentJobID != runResult.Job.JobID {
		t.Fatalf("expected current job %q, got %+v", runResult.Job.JobID, show.Work)
	}
	if show.Work.ExecutionState != "checking" {
		t.Fatalf("expected checking execution state, got %+v", show.Work)
	}
	if show.Work.ApprovalState != "none" {
		t.Fatalf("expected none approval state before attestation, got %+v", show.Work)
	}
	var attestationChildren []cliWorkItem
	for i := range show.Children {
		if show.Children[i].Kind == "attest" {
			attestationChildren = append(attestationChildren, show.Children[i])
		}
	}
	if len(attestationChildren) == 0 {
		t.Fatalf("expected spawned attestation child, got %+v", show.Children)
	}
	foundJob := false
	for _, job := range show.Jobs {
		if job.JobID == runResult.Job.JobID && job.WorkID == childWork.WorkID {
			foundJob = true
			break
		}
	}
	if !foundJob {
		t.Fatalf("expected linked job in work show, got %+v", show.Jobs)
	}

	noteOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "note-add", childWork.WorkID, "--type", "verifier_feedback", "--text", "Looks good")
	var note cliWorkNote
	if err := json.Unmarshal([]byte(noteOutput), &note); err != nil {
		t.Fatalf("unmarshal work note: %v\n%s", err, noteOutput)
	}
	if note.Body != "Looks good" {
		t.Fatalf("unexpected note body: %+v", note)
	}

	notesOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "notes", childWork.WorkID)
	var notes []cliWorkNote
	if err := json.Unmarshal([]byte(notesOutput), &notes); err != nil {
		t.Fatalf("unmarshal work notes: %v\n%s", err, notesOutput)
	}
	if len(notes) == 0 || notes[0].Body != "Looks good" {
		t.Fatalf("expected note in work notes, got %+v", notes)
	}

	docOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "doc-set", childWork.WorkID, "--path", "docs/review-bundle.md", "--title", "Review Bundle", "--body", "# Review\n")
	var docSet struct {
		Doc struct {
			DocID string `json:"doc_id"`
			Path  string `json:"path"`
		} `json:"doc"`
	}
	if err := json.Unmarshal([]byte(docOutput), &docSet); err != nil {
		t.Fatalf("unmarshal doc-set output: %v\n%s", err, docOutput)
	}
	if docSet.Doc.Path != "docs/review-bundle.md" {
		t.Fatalf("expected doc path in doc-set response, got %+v", docSet.Doc)
	}

	checkOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "check", "create", childWork.WorkID, "--result", "pass", "--build-ok", "--tests-passed", "1", "--test-output", "go test ./cmd/fase\nok\tgithub.com/yusefmosiah/cogent/cmd/fase\t0.111s", "--notes", "verified canonical review bundle")
	var checkRecord struct {
		CheckID string `json:"check_id"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal([]byte(checkOutput), &checkRecord); err != nil {
		t.Fatalf("unmarshal check output: %v\n%s", err, checkOutput)
	}
	if checkRecord.Result != "pass" {
		t.Fatalf("expected pass check record, got %+v", checkRecord)
	}

	showOutput = runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "show", childWork.WorkID)
	if err := json.Unmarshal([]byte(showOutput), &show); err != nil {
		t.Fatalf("unmarshal work show after check/doc: %v\n%s", err, showOutput)
	}
	if show.Work.ExecutionState != "checking" {
		t.Fatalf("expected passing check to keep work in checking until attestation resolves, got %+v", show.Work)
	}
	if len(show.CheckRecords) == 0 || show.CheckRecords[0].CheckID != checkRecord.CheckID {
		t.Fatalf("expected check record in canonical work show bundle, got %+v", show.CheckRecords)
	}
	if len(show.Docs) == 0 || show.Docs[0].Path != "docs/review-bundle.md" {
		t.Fatalf("expected docs in canonical work show bundle, got %+v", show.Docs)
	}
	if len(show.Artifacts) == 0 {
		t.Fatalf("expected artifacts in canonical work show bundle after check evidence, got %+v", show.Artifacts)
	}

	for _, attestationChild := range attestationChildren {
		nonce, _ := attestationChild.Metadata["attestation_nonce"].(string)
		if strings.TrimSpace(nonce) == "" {
			t.Fatalf("expected attestation nonce on child, got %+v", attestationChild)
		}
		attestOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "attest", attestationChild.WorkID, "--nonce", nonce, "--result", "passed", "--summary", "Attestation passed", "--verifier-kind", "deterministic", "--method", "test")
		var attestation cliAttestationPayload
		if err := json.Unmarshal([]byte(attestOutput), &attestation); err != nil {
			t.Fatalf("unmarshal attestation payload: %v\n%s", err, attestOutput)
		}
		if attestation.Work.ExecutionState != "done" || attestation.Work.ApprovalState != "none" {
			t.Fatalf("expected attestation child to complete cleanly, got %+v", attestation.Work)
		}
	}
	showOutput = runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "show", childWork.WorkID)
	if err := json.Unmarshal([]byte(showOutput), &show); err != nil {
		t.Fatalf("unmarshal work show after attestation: %v\n%s", err, showOutput)
	}
	if show.Work.ExecutionState != "done" {
		t.Fatalf("expected done after child attestations, got %+v", show.Work)
	}
	if show.Work.ApprovalState != "pending" {
		t.Fatalf("expected pending approval state after child attestations, got %+v", show.Work)
	}

	approveOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "approve", childWork.WorkID, "--message", "Ready to land")
	if err := json.Unmarshal([]byte(approveOutput), &childWork); err != nil {
		t.Fatalf("unmarshal work approval: %v\n%s", err, approveOutput)
	}
	if childWork.ApprovalState != "verified" {
		t.Fatalf("expected verified approval state, got %+v", childWork)
	}
	showOutput = runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "show", childWork.WorkID)
	if err := json.Unmarshal([]byte(showOutput), &show); err != nil {
		t.Fatalf("unmarshal work show after approval: %v\n%s", err, showOutput)
	}
	if len(show.Approvals) == 0 || show.Approvals[0].Status != "approved" || show.Approvals[0].ApprovedCommitOID != "abc123" {
		t.Fatalf("expected approval ledger entry in work show, got %+v", show.Approvals)
	}

	promoteOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "promote", childWork.WorkID, "--environment", "staging", "--ref", "refs/fase/promoted/staging", "--message", "Ship to staging")
	var promotion cliPromotionPayload
	if err := json.Unmarshal([]byte(promoteOutput), &promotion); err != nil {
		t.Fatalf("unmarshal work promotion: %v\n%s", err, promoteOutput)
	}
	if promotion.Promotion.Environment != "staging" || promotion.Promotion.TargetRef != "refs/fase/promoted/staging" {
		t.Fatalf("expected staging promotion payload, got %+v", promotion)
	}
	showOutput = runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "show", childWork.WorkID)
	if err := json.Unmarshal([]byte(showOutput), &show); err != nil {
		t.Fatalf("unmarshal work show after promotion: %v\n%s", err, showOutput)
	}
	if len(show.Promotions) == 0 || show.Promotions[0].Environment != "staging" {
		t.Fatalf("expected promotion record in work show, got %+v", show.Promotions)
	}

	artifactsOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "artifacts", "list", "--work", childWork.WorkID)
	var artifacts []cliArtifactRecord
	if err := json.Unmarshal([]byte(artifactsOutput), &artifacts); err != nil {
		t.Fatalf("unmarshal work artifacts: %v\n%s", err, artifactsOutput)
	}
	if len(artifacts) == 0 {
		t.Fatal("expected work-linked artifacts")
	}

	notePath := filepath.Join(t.TempDir(), "attached-note.md")
	if err := os.WriteFile(notePath, []byte("# Attached note\n\nVerifier context.\n"), 0o644); err != nil {
		t.Fatalf("write attached note: %v", err)
	}
	attachOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "artifacts", "attach", "--work", childWork.WorkID, "--path", notePath, "--kind", "spec_markdown")
	var attached cliArtifactRecord
	if err := json.Unmarshal([]byte(attachOutput), &attached); err != nil {
		t.Fatalf("unmarshal attached artifact: %v\n%s", err, attachOutput)
	}
	if attached.ArtifactID == "" || attached.Kind != "spec_markdown" {
		t.Fatalf("unexpected attached artifact payload: %+v", attached)
	}

	artifactsOutput = runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "artifacts", "list", "--work", childWork.WorkID)
	if err := json.Unmarshal([]byte(artifactsOutput), &artifacts); err != nil {
		t.Fatalf("unmarshal work artifacts after attach: %v\n%s", err, artifactsOutput)
	}
	foundAttached := false
	for _, artifact := range artifacts {
		if artifact.ArtifactID == attached.ArtifactID {
			foundAttached = true
			break
		}
	}
	if !foundAttached {
		t.Fatalf("expected attached artifact in work artifact list, got %+v", artifacts)
	}

	discoverOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "discover", childWork.WorkID, "--title", "Verifier follow-up", "--objective", "Add gate work", "--kind", "verify", "--rationale", "Discovered during implementation")
	var proposal cliWorkProposalPayload
	if err := json.Unmarshal([]byte(discoverOutput), &proposal); err != nil {
		t.Fatalf("unmarshal discover proposal: %v\n%s", err, discoverOutput)
	}
	if proposal.Proposal.ProposalID == "" || proposal.Proposal.State != "proposed" {
		t.Fatalf("expected proposed discovery, got %+v", proposal)
	}

	acceptOutput := runFase(t, binary, configPath, "--json", "work", "proposal", "accept", proposal.Proposal.ProposalID)
	if err := json.Unmarshal([]byte(acceptOutput), &proposal); err != nil {
		t.Fatalf("unmarshal accepted proposal: %v\n%s", err, acceptOutput)
	}
	if proposal.CreatedWork == nil || proposal.CreatedWork.WorkID == "" {
		t.Fatalf("expected created work from accepted discovery, got %+v", proposal)
	}

	readyOutput := runFase(t, binary, configPath, "--json", "work", "ready")
	var ready []cliWorkItem
	if err := json.Unmarshal([]byte(readyOutput), &ready); err != nil {
		t.Fatalf("unmarshal ready work: %v\n%s", err, readyOutput)
	}
	foundCreated := false
	for _, item := range ready {
		if proposal.CreatedWork != nil && item.WorkID == proposal.CreatedWork.WorkID {
			foundCreated = true
			break
		}
	}
	if !foundCreated {
		t.Fatalf("expected accepted discovered work in ready list, got %+v", ready)
	}

	checklistOutput := runFase(t, binary, configPath, "work", "projection", "checklist", rootWork.WorkID)
	if !strings.Contains(checklistOutput, "# Root plan") || !strings.Contains(checklistOutput, "Implement child") {
		t.Fatalf("expected checklist projection content, got:\n%s", checklistOutput)
	}

	statusProjection := runFase(t, binary, configPath, "work", "projection", "status", childWork.WorkID)
	if !strings.Contains(statusProjection, "Latest Attestation") || !strings.Contains(statusProjection, "Attestation passed") {
		t.Fatalf("expected status projection content, got:\n%s", statusProjection)
	}

	hydrateOutput := runFase(t, binary, configPath, "--json", "work", "hydrate", childWork.WorkID)
	var briefing map[string]any
	if err := json.Unmarshal([]byte(hydrateOutput), &briefing); err != nil {
		t.Fatalf("unmarshal work hydrate: %v\n%s", err, hydrateOutput)
	}
	if briefing["schema_version"] != "fase.worker_briefing.v1" {
		t.Fatalf("unexpected hydrate schema version: %+v", briefing)
	}
	evidence, ok := briefing["evidence"].(map[string]any)
	if !ok {
		t.Fatalf("expected hydrate evidence object, got %+v", briefing)
	}
	if _, ok := evidence["latest_attestations"]; !ok {
		t.Fatalf("expected latest_attestations in hydrate evidence, got %+v", evidence)
	}
	workerContract, ok := briefing["worker_contract"].(map[string]any)
	if !ok {
		t.Fatalf("expected hydrate worker_contract object, got %+v", briefing)
	}
	rules, ok := workerContract["rules"].([]any)
	if !ok || len(rules) == 0 {
		t.Fatalf("expected hydrate worker_contract rules, got %+v", workerContract)
	}
	foundDelegationRule := false
	for _, rule := range rules {
		text, _ := rule.(string)
		if strings.Contains(text, "Create child work directly only for unexpected work, fanout work, or sequential context isolation") {
			foundDelegationRule = true
			break
		}
	}
	if !foundDelegationRule {
		t.Fatalf("expected hydrate worker_contract to include delegation rule, got %+v", rules)
	}

	createChildProposalOutput := runFase(t, binary, configPath, "--json", "work", "proposal", "create", "--type", "create_child", "--target", rootWork.WorkID, "--patch", `{"title":"Review child","objective":"Review the implementation","kind":"review"}`)
	if err := json.Unmarshal([]byte(createChildProposalOutput), &proposal); err != nil {
		t.Fatalf("unmarshal create_child proposal: %v\n%s", err, createChildProposalOutput)
	}
	acceptChildOutput := runFase(t, binary, configPath, "--json", "work", "proposal", "accept", proposal.Proposal.ProposalID)
	if err := json.Unmarshal([]byte(acceptChildOutput), &proposal); err != nil {
		t.Fatalf("unmarshal accepted create_child proposal: %v\n%s", err, acceptChildOutput)
	}
	if proposal.CreatedWork == nil || proposal.CreatedWork.Kind != "review" {
		t.Fatalf("expected created review child, got %+v", proposal)
	}

	noReadyOutput := runFase(t, binary, configPath, "--json", "work", "create", "--title", "Needs impossible adapter", "--objective", "Should not be ready without a matching adapter", "--preferred-adapters", "nonexistent")
	var constrainedWork cliWorkItem
	if err := json.Unmarshal([]byte(noReadyOutput), &constrainedWork); err != nil {
		t.Fatalf("unmarshal constrained work: %v\n%s", err, noReadyOutput)
	}
	readyOutput = runFase(t, binary, configPath, "--json", "work", "ready")
	if err := json.Unmarshal([]byte(readyOutput), &ready); err != nil {
		t.Fatalf("unmarshal ready work after capability filter: %v\n%s", err, readyOutput)
	}
	for _, item := range ready {
		if item.WorkID == constrainedWork.WorkID {
			t.Fatalf("did not expect impossible-adapter work in ready list: %+v", ready)
		}
	}
}

func TestDocsRequiredWorkBlocksCompletionUntilRepoDocsAlign(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)
	projectDir := t.TempDir()
	stateDir := os.Getenv("FASE_STATE_DIR")
	cacheDir := os.Getenv("FASE_CACHE_DIR")
	configDir := os.Getenv("FASE_CONFIG_DIR")

	env := append(os.Environ(),
		"FASE_CAPABILITY_ENFORCEMENT=audit",
		"FASE_AGENT_TOKEN=",
		"FASE_CONFIG_DIR="+configDir,
		"FASE_STATE_DIR="+stateDir,
		"FASE_CACHE_DIR="+cacheDir,
	)

	serveCmd := exec.Command(binary, "--config", configPath, "serve", "--no-ui", "--no-browser")
	serveCmd.Dir = projectDir
	serveCmd.Env = env
	var serveLogs bytes.Buffer
	serveCmd.Stdout = &serveLogs
	serveCmd.Stderr = &serveLogs
	if err := serveCmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() {
		if serveCmd.Process != nil {
			_ = serveCmd.Process.Kill()
			_, _ = serveCmd.Process.Wait()
		}
	})

	waitForFile(t, filepath.Join(stateDir, "serve.json"), 10*time.Second, func() string {
		return serveLogs.String()
	})

	createOutput := runFaseWithEnv(t, binary, configPath, projectDir, env,
		"--json", "work", "create",
		"--title", "Docs required work",
		"--objective", "Implementation and docs must land together",
		"--kind", "implement",
		"--head-commit", "abc123",
		"--required-attestations", `[{"verifier_kind":"deterministic","method":"test","blocking":true}]`,
		"--required-docs", `["docs/review-bundle.md"]`,
	)
	var work cliWorkItem
	if err := json.Unmarshal([]byte(createOutput), &work); err != nil {
		t.Fatalf("unmarshal work create: %v\n%s", err, createOutput)
	}
	if len(work.RequiredDocs) != 1 || work.RequiredDocs[0] != "docs/review-bundle.md" {
		t.Fatalf("expected required doc policy on work create, got %+v", work)
	}

	runOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--work", work.WorkID, "--prompt", "docs gate test")
	var runResult cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &runResult); err != nil {
		t.Fatalf("unmarshal run output: %v\n%s", err, runOutput)
	}
	waitForJobStateWithEnv(t, binary, configPath, projectDir, env, runResult.Job.JobID, map[string]bool{"completed": true})

	docOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "doc-set", work.WorkID, "--path", "docs/review-bundle.md", "--title", "Review Bundle", "--body", "# Review\n")
	var docSet struct {
		Doc struct {
			Path           string `json:"path"`
			RepoFileExists bool   `json:"repo_file_exists"`
		} `json:"doc"`
	}
	if err := json.Unmarshal([]byte(docOutput), &docSet); err != nil {
		t.Fatalf("unmarshal doc-set output: %v\n%s", err, docOutput)
	}
	if docSet.Doc.Path != "docs/review-bundle.md" || docSet.Doc.RepoFileExists {
		t.Fatalf("expected tracked doc without repo file yet, got %+v", docSet.Doc)
	}

	checkOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "check", "create", work.WorkID, "--result", "pass", "--build-ok", "--tests-passed", "1", "--test-output", "go test ./cmd/fase\nok\tgithub.com/yusefmosiah/cogent/cmd/fase\t0.111s", "--notes", "verified canonical review bundle")
	var checkRecord struct {
		CheckID string `json:"check_id"`
	}
	if err := json.Unmarshal([]byte(checkOutput), &checkRecord); err != nil {
		t.Fatalf("unmarshal check output: %v\n%s", err, checkOutput)
	}
	if checkRecord.CheckID == "" {
		t.Fatalf("expected check record id, got %+v", checkRecord)
	}

	showOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "show", work.WorkID)
	var show cliWorkShowResult
	if err := json.Unmarshal([]byte(showOutput), &show); err != nil {
		t.Fatalf("unmarshal work show: %v\n%s", err, showOutput)
	}
	if show.Work.ExecutionState != "checking" {
		t.Fatalf("expected checking after completed run, got %+v", show.Work)
	}

	var attestationChildren []cliWorkItem
	for i := range show.Children {
		if show.Children[i].Kind == "attest" {
			attestationChildren = append(attestationChildren, show.Children[i])
		}
	}
	if len(attestationChildren) == 0 {
		t.Fatalf("expected attestation child, got %+v", show.Children)
	}

	for _, attestationChild := range attestationChildren {
		nonce, _ := attestationChild.Metadata["attestation_nonce"].(string)
		if strings.TrimSpace(nonce) == "" {
			t.Fatalf("expected attestation nonce, got %+v", attestationChild)
		}
		attestOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "attest", attestationChild.WorkID, "--nonce", nonce, "--result", "passed", "--summary", "Attestation passed", "--verifier-kind", "deterministic", "--method", "test")
		var attestation cliAttestationPayload
		if err := json.Unmarshal([]byte(attestOutput), &attestation); err != nil {
			t.Fatalf("unmarshal attestation output: %v\n%s", err, attestOutput)
		}
		if attestation.Work.ExecutionState != "done" {
			t.Fatalf("expected attestation child to finish, got %+v", attestation.Work)
		}
	}

	showOutput = runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "show", work.WorkID)
	if err := json.Unmarshal([]byte(showOutput), &show); err != nil {
		t.Fatalf("unmarshal work show after attestation: %v\n%s", err, showOutput)
	}
	if show.Work.ExecutionState != "checking" {
		t.Fatalf("expected docs-required work to remain checking until repo doc exists, got %+v", show.Work)
	}
	if len(show.Work.RequiredDocs) != 1 || show.Work.RequiredDocs[0] != "docs/review-bundle.md" {
		t.Fatalf("expected required docs in runtime output, got %+v", show.Work)
	}
	if len(show.Docs) != 1 || show.Docs[0].RepoFileExists || show.Docs[0].MatchesRepo {
		t.Fatalf("expected unresolved repo doc status, got %+v", show.Docs)
	}

	failCmd := exec.Command(binary, "--config", configPath, "--json", "work", "update", work.WorkID, "--execution-state", "done", "--message", "attempt done before docs align")
	failCmd.Dir = projectDir
	failCmd.Env = env
	failOutput, err := failCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected work update done to fail before docs align:\n%s", failOutput)
	}
	if !strings.Contains(string(failOutput), "docs/review-bundle.md") {
		t.Fatalf("expected missing required doc error, got %s", failOutput)
	}

	repoDoc := filepath.Join(projectDir, "docs", "review-bundle.md")
	if err := os.MkdirAll(filepath.Dir(repoDoc), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(repoDoc, []byte("# Review\n"), 0o644); err != nil {
		t.Fatalf("write repo doc: %v", err)
	}

	showOutput = runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "show", work.WorkID)
	if err := json.Unmarshal([]byte(showOutput), &show); err != nil {
		t.Fatalf("unmarshal work show after repo doc write: %v\n%s", err, showOutput)
	}
	if len(show.Docs) != 1 || !show.Docs[0].RepoFileExists || !show.Docs[0].MatchesRepo {
		t.Fatalf("expected aligned repo doc status, got %+v", show.Docs)
	}

	doneOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "update", work.WorkID, "--execution-state", "done", "--message", "docs aligned")
	if err := json.Unmarshal([]byte(doneOutput), &work); err != nil {
		t.Fatalf("unmarshal work update output: %v\n%s", err, doneOutput)
	}
	if work.ExecutionState != "done" {
		t.Fatalf("expected done after repo doc alignment, got %+v", work)
	}
}

func TestWorkDocSetAutoCreatesLinkedWorkWithAuthoritativePath(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)
	projectDir := t.TempDir()
	stateDir := os.Getenv("FASE_STATE_DIR")
	cacheDir := os.Getenv("FASE_CACHE_DIR")
	configDir := os.Getenv("FASE_CONFIG_DIR")

	env := append(os.Environ(),
		"FASE_CAPABILITY_ENFORCEMENT=audit",
		"FASE_AGENT_TOKEN=",
		"FASE_CONFIG_DIR="+configDir,
		"FASE_STATE_DIR="+stateDir,
		"FASE_CACHE_DIR="+cacheDir,
	)

	docPath := filepath.Join(projectDir, "docs", "authoritative.md")
	if err := os.MkdirAll(filepath.Dir(docPath), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(docPath, []byte("# Authoritative Doc\n\nHello repo truth.\n"), 0o644); err != nil {
		t.Fatalf("write authoritative doc: %v", err)
	}

	serveCmd := exec.Command(binary, "--config", configPath, "serve", "--no-ui", "--no-browser")
	serveCmd.Dir = projectDir
	serveCmd.Env = env
	var serveLogs bytes.Buffer
	serveCmd.Stdout = &serveLogs
	serveCmd.Stderr = &serveLogs
	if err := serveCmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() {
		if serveCmd.Process != nil {
			_ = serveCmd.Process.Kill()
			_, _ = serveCmd.Process.Wait()
		}
	})

	waitForFile(t, filepath.Join(stateDir, "serve.json"), 10*time.Second, func() string {
		return serveLogs.String()
	})

	docOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "doc-set", "--file", "docs/authoritative.md")
	var docSet struct {
		Doc struct {
			DocID          string `json:"doc_id"`
			Path           string `json:"path"`
			RepoFileExists bool   `json:"repo_file_exists"`
			MatchesRepo    bool   `json:"matches_repo"`
		} `json:"doc"`
		WorkID string `json:"work_id"`
	}
	if err := json.Unmarshal([]byte(docOutput), &docSet); err != nil {
		t.Fatalf("unmarshal doc-set output: %v\n%s", err, docOutput)
	}
	if docSet.WorkID == "" {
		t.Fatalf("expected auto-created work id, got %+v", docSet)
	}
	if docSet.Doc.Path != "docs/authoritative.md" {
		t.Fatalf("expected repo-relative doc path, got %+v", docSet.Doc)
	}
	if !docSet.Doc.RepoFileExists || !docSet.Doc.MatchesRepo {
		t.Fatalf("expected authoritative repo file linkage, got %+v", docSet.Doc)
	}

	showOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "show", docSet.WorkID)
	var show cliWorkShowResult
	if err := json.Unmarshal([]byte(showOutput), &show); err != nil {
		t.Fatalf("unmarshal work show: %v\n%s", err, showOutput)
	}
	if len(show.Docs) != 1 {
		t.Fatalf("expected one linked doc, got %+v", show.Docs)
	}
	if show.Docs[0].Path != "docs/authoritative.md" || !show.Docs[0].RepoFileExists || !show.Docs[0].MatchesRepo {
		t.Fatalf("expected authoritative repo-relative doc in work show, got %+v", show.Docs[0])
	}
}

func TestWorkClaimLifecycleCommands(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)

	workOutput := runFase(t, binary, configPath, "--json", "work", "create", "--title", "Claimable work", "--objective", "Exercise work lease semantics")
	var work cliWorkItem
	if err := json.Unmarshal([]byte(workOutput), &work); err != nil {
		t.Fatalf("unmarshal work create: %v\n%s", err, workOutput)
	}

	claimOutput := runFase(t, binary, configPath, "--json", "work", "claim", work.WorkID, "--claimant", "worker-a", "--lease", "1h")
	if err := json.Unmarshal([]byte(claimOutput), &work); err != nil {
		t.Fatalf("unmarshal work claim: %v\n%s", err, claimOutput)
	}
	if work.ExecutionState != "claimed" {
		t.Fatalf("expected claimed execution state after claim, got %q", work.ExecutionState)
	}
	if work.ClaimedBy != "worker-a" {
		t.Fatalf("expected claimant worker-a, got %q", work.ClaimedBy)
	}

	readyOutput := runFase(t, binary, configPath, "--json", "work", "ready")
	var ready []cliWorkItem
	if err := json.Unmarshal([]byte(readyOutput), &ready); err != nil {
		t.Fatalf("unmarshal ready work after claim: %v\n%s", err, readyOutput)
	}
	for _, candidate := range ready {
		if candidate.WorkID == work.WorkID {
			t.Fatalf("claimed work should not appear in ready list")
		}
	}

	if output, exitCode := runFaseExpectError(t, binary, configPath, "--json", "work", "claim", work.WorkID, "--claimant", "worker-b", "--lease", "1h"); exitCode != 7 {
		t.Fatalf("expected busy exit code 7 for conflicting claim, got %d\n%s", exitCode, output)
	}

	if output, exitCode := runFaseExpectError(t, binary, configPath, "--json", "work", "release", work.WorkID, "--claimant", "worker-b"); exitCode != 7 {
		t.Fatalf("expected busy exit code 7 for conflicting release, got %d\n%s", exitCode, output)
	}

	releaseOutput := runFase(t, binary, configPath, "--json", "work", "release", work.WorkID, "--claimant", "worker-a")
	var releasedWork cliWorkItem
	if err := json.Unmarshal([]byte(releaseOutput), &releasedWork); err != nil {
		t.Fatalf("unmarshal work release: %v\n%s", err, releaseOutput)
	}
	if releasedWork.ExecutionState != "ready" {
		t.Fatalf("expected ready execution state after release, got %q", releasedWork.ExecutionState)
	}
	if releasedWork.ClaimedBy != "" {
		t.Fatalf("expected cleared claimant after release, got %q", releasedWork.ClaimedBy)
	}

	expiringOutput := runFase(t, binary, configPath, "--json", "work", "claim", work.WorkID, "--claimant", "worker-a", "--lease", "50ms")
	var expiringWork cliWorkItem
	if err := json.Unmarshal([]byte(expiringOutput), &expiringWork); err != nil {
		t.Fatalf("unmarshal expiring claim: %v\n%s", err, expiringOutput)
	}
	time.Sleep(125 * time.Millisecond)

	claimNextOutput := runFase(t, binary, configPath, "--json", "work", "claim-next", "--claimant", "worker-b", "--lease", "1h")
	var nextWork cliWorkItem
	if err := json.Unmarshal([]byte(claimNextOutput), &nextWork); err != nil {
		t.Fatalf("unmarshal claim-next: %v\n%s", err, claimNextOutput)
	}
	if nextWork.WorkID != expiringWork.WorkID {
		t.Fatalf("expected claim-next to recover expired lease on %s, got %s", expiringWork.WorkID, nextWork.WorkID)
	}
	if nextWork.ClaimedBy != "worker-b" {
		t.Fatalf("expected worker-b to acquire expired lease, got %q", nextWork.ClaimedBy)
	}
}

func TestWorkArchiveCommands(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)

	workOutput := runFase(t, binary, configPath, "--json", "work", "create", "--title", "Archive me", "--objective", "Validate archived filtering", "--kind", "attest")
	var work cliWorkItem
	if err := json.Unmarshal([]byte(workOutput), &work); err != nil {
		t.Fatalf("unmarshal work create: %v\n%s", err, workOutput)
	}

	archiveOutput := runFase(t, binary, configPath, "--json", "work", "archive", work.WorkID, "--message", "created by mistake")
	if err := json.Unmarshal([]byte(archiveOutput), &work); err != nil {
		t.Fatalf("unmarshal work archive: %v\n%s", err, archiveOutput)
	}
	if work.ExecutionState != "archived" {
		t.Fatalf("expected archived execution state, got %q", work.ExecutionState)
	}

	listOutput := runFase(t, binary, configPath, "--json", "work", "list")
	var listed []cliWorkItem
	if err := json.Unmarshal([]byte(listOutput), &listed); err != nil {
		t.Fatalf("unmarshal work list: %v\n%s", err, listOutput)
	}
	for _, item := range listed {
		if item.WorkID == work.WorkID {
			t.Fatalf("archived work should not appear in default work list")
		}
	}

	listOutput = runFase(t, binary, configPath, "--json", "work", "list", "--include-archived")
	if err := json.Unmarshal([]byte(listOutput), &listed); err != nil {
		t.Fatalf("unmarshal archived work list: %v\n%s", err, listOutput)
	}
	foundArchived := false
	for _, item := range listed {
		if item.WorkID == work.WorkID {
			foundArchived = true
			if item.ExecutionState != "archived" {
				t.Fatalf("expected archived work state in list, got %+v", item)
			}
		}
	}
	if !foundArchived {
		t.Fatalf("expected archived work in include-archived list, got %+v", listed)
	}

	readyOutput := runFase(t, binary, configPath, "--json", "work", "ready")
	var ready []cliWorkItem
	if err := json.Unmarshal([]byte(readyOutput), &ready); err != nil {
		t.Fatalf("unmarshal ready work: %v\n%s", err, readyOutput)
	}
	for _, item := range ready {
		if item.WorkID == work.WorkID {
			t.Fatalf("archived work should not appear in default ready list")
		}
	}

	readyOutput = runFase(t, binary, configPath, "--json", "work", "ready", "--include-archived")
	if err := json.Unmarshal([]byte(readyOutput), &ready); err != nil {
		t.Fatalf("unmarshal archived ready work: %v\n%s", err, readyOutput)
	}
	foundArchived = false
	for _, item := range ready {
		if item.WorkID == work.WorkID {
			foundArchived = true
			if item.ExecutionState != "archived" {
				t.Fatalf("expected archived work state in ready query, got %+v", item)
			}
		}
	}
	if !foundArchived {
		t.Fatalf("expected archived work in include-archived ready query, got %+v", ready)
	}
}

func TestDispatchCompletionNotificationReachesMCPProxy(t *testing.T) {
	binary := buildFaseBinary(t)

	projectDir := t.TempDir()
	configDir := t.TempDir()
	stateDir := t.TempDir()
	cacheDir := t.TempDir()

	fakeBinary, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake_clis", "claude"))
	if err != nil {
		t.Fatalf("resolve fake claude path: %v", err)
	}
	if err := os.Chmod(fakeBinary, 0o755); err != nil {
		t.Fatalf("chmod fake claude: %v", err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	configBody := []byte("[store]\nstate_dir = \"" + stateDir + "\"\n\n[adapters.claude]\nbinary = \"" + fakeBinary + "\"\nenabled = true\n")
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	env := append(os.Environ(),
		"FASE_CAPABILITY_ENFORCEMENT=audit",
		"FASE_AGENT_TOKEN=",
		"FASE_CONFIG_DIR="+configDir,
		"FASE_STATE_DIR="+stateDir,
		"FASE_CACHE_DIR="+cacheDir,
	)

	serveCmd := exec.Command(binary, "--config", configPath, "serve", "--no-ui", "--no-browser")
	serveCmd.Dir = projectDir
	serveCmd.Env = env
	var serveLogs bytes.Buffer
	serveCmd.Stdout = &serveLogs
	serveCmd.Stderr = &serveLogs
	if err := serveCmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() {
		if serveCmd.Process != nil {
			_ = serveCmd.Process.Kill()
			_, _ = serveCmd.Process.Wait()
		}
	})

	waitForFile(t, filepath.Join(stateDir, "serve.json"), 10*time.Second, func() string {
		return serveLogs.String()
	})

	proxyCmd := exec.Command(binary, "--config", configPath, "mcp", "proxy")
	proxyCmd.Dir = projectDir
	proxyCmd.Env = env
	proxyStdin, err := proxyCmd.StdinPipe()
	if err != nil {
		t.Fatalf("proxy stdin pipe: %v", err)
	}
	proxyStdout, err := proxyCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("proxy stdout pipe: %v", err)
	}
	var proxyStderr bytes.Buffer
	proxyCmd.Stderr = &proxyStderr
	if err := proxyCmd.Start(); err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() {
		_ = proxyStdin.Close()
		if proxyCmd.Process != nil {
			_ = proxyCmd.Process.Kill()
			_, _ = proxyCmd.Process.Wait()
		}
	})

	proxyLines := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(proxyStdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			proxyLines <- scanner.Text()
		}
		close(proxyLines)
	}()

	workOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "work", "create", "--title", "Proxy relay test", "--objective", "Verify completion notification relay", "--kind", "plan")
	var work cliWorkItem
	if err := json.Unmarshal([]byte(workOutput), &work); err != nil {
		t.Fatalf("unmarshal work create: %v\n%s", err, workOutput)
	}

	dispatchOutput := runFaseWithEnv(t, binary, configPath, projectDir, env, "--json", "dispatch", work.WorkID, "--adapter", "claude")
	var dispatch struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(dispatchOutput), &dispatch); err != nil {
		t.Fatalf("unmarshal dispatch output: %v\n%s", err, dispatchOutput)
	}
	if dispatch.JobID == "" {
		t.Fatalf("dispatch returned no job id: %s", dispatchOutput)
	}

	waitForJobStateWithEnv(t, binary, configPath, projectDir, env, dispatch.JobID, map[string]bool{"completed": true})

	line := waitForProxyNotification(t, proxyLines, 15*time.Second, dispatch.JobID, func() string {
		return "proxy stderr:\n" + proxyStderr.String() + "\nserve logs:\n" + serveLogs.String()
	})

	var notification struct {
		Params struct {
			Content string            `json:"content"`
			Meta    map[string]string `json:"meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &notification); err != nil {
		t.Fatalf("unmarshal proxy notification: %v\n%s", err, line)
	}
	if notification.Params.Meta["source"] != "job_runner" {
		t.Fatalf("expected job_runner source, got %#v", notification.Params.Meta)
	}
	if notification.Params.Meta["type"] != "job_completed" {
		t.Fatalf("expected job_completed type, got %#v", notification.Params.Meta)
	}
}

func buildFaseBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "fase")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/fase")
	cmd.Dir = filepath.Join("..", "..")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fase binary: %v\n%s", err, output)
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

	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
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

	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
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

	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
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

	t.Setenv("FASE_CONFIG_DIR", configDir)
	t.Setenv("FASE_STATE_DIR", stateDir)
	t.Setenv("FASE_CACHE_DIR", cacheDir)
	return configPath
}

func runFase(t *testing.T, binary, configPath string, args ...string) string {
	t.Helper()
	ensureServeForCommand(t, binary, configPath, args)

	cmd := exec.Command(binary, append([]string{"--config", configPath}, args...)...)
	// Strip any ambient agent token from the developer shell so JSON assertions
	// do not inherit audit-mode capability warnings from unrelated state.
	cmd.Env = append(os.Environ(),
		"FASE_CAPABILITY_ENFORCEMENT=audit",
		"FASE_AGENT_TOKEN=",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run fase %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func runFaseWithEnv(t *testing.T, binary, configPath, dir string, env []string, args ...string) string {
	t.Helper()

	cmd := exec.Command(binary, append([]string{"--config", configPath}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run fase %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func runFaseExpectError(t *testing.T, binary, configPath string, args ...string) (string, int) {
	t.Helper()
	ensureServeForCommand(t, binary, configPath, args)

	cmd := exec.Command(binary, append([]string{"--config", configPath}, args...)...)
	cmd.Env = append(os.Environ(),
		"FASE_CAPABILITY_ENFORCEMENT=audit",
		"FASE_AGENT_TOKEN=",
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected fase command to fail: %v\n%s", args, output)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected exit error for %v: %v\n%s", args, err, output)
	}
	return string(output), exitErr.ExitCode()
}

func ensureServeForCommand(t *testing.T, binary, configPath string, args []string) {
	t.Helper()
	if !shouldAutoStartServe(args) {
		return
	}
	stateDir := os.Getenv("FASE_STATE_DIR")
	if stateDir == "" {
		return
	}
	serveInfoPath := filepath.Join(stateDir, "serve.json")
	if _, err := os.Stat(serveInfoPath); err == nil {
		return
	}

	serveCmd := exec.Command(binary, "--config", configPath, "serve", "--no-ui", "--no-browser")
	serveCmd.Env = append(os.Environ(),
		"FASE_CAPABILITY_ENFORCEMENT=audit",
		"FASE_AGENT_TOKEN=",
	)
	var serveLogs bytes.Buffer
	serveCmd.Stdout = &serveLogs
	serveCmd.Stderr = &serveLogs
	if err := serveCmd.Start(); err != nil {
		t.Fatalf("start serve for %v: %v", args, err)
	}
	t.Cleanup(func() {
		if serveCmd.Process != nil {
			_ = serveCmd.Process.Kill()
			_, _ = serveCmd.Process.Wait()
		}
	})

	waitForFile(t, serveInfoPath, 10*time.Second, func() string {
		return serveLogs.String()
	})
}

func shouldAutoStartServe(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg != "serve" && arg != "__run-job"
	}
	return false
}

func waitForJobState(t *testing.T, binary, configPath, jobID string, allowed map[string]bool) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		output := runFase(t, binary, configPath, "--json", "status", jobID)
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

func waitForJobStateWithEnv(t *testing.T, binary, configPath, dir string, env []string, jobID string, allowed map[string]bool) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		output := runFaseWithEnv(t, binary, configPath, dir, env, "--json", "status", jobID)
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

func waitForFile(t *testing.T, path string, timeout time.Duration, logs func() string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s\n%s", path, logs())
}

func waitForProxyNotification(t *testing.T, lines <-chan string, timeout time.Duration, jobID string, logs func() string) string {
	t.Helper()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("proxy exited before notification arrived\n%s", logs())
			}
			if !strings.Contains(line, `"method":"notifications/claude/channel"`) {
				continue
			}
			var notification struct {
				Params struct {
					Content string            `json:"content"`
					Meta    map[string]string `json:"meta"`
				} `json:"params"`
			}
			if err := json.Unmarshal([]byte(line), &notification); err != nil {
				continue
			}
			if notification.Params.Meta["source"] == "job_runner" &&
				notification.Params.Meta["type"] == "job_completed" &&
				strings.Contains(notification.Params.Content, "[job "+jobID+"] job.completed:") {
				return line
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for proxy notification for %s\n%s", jobID, logs())
		}
	}
}
