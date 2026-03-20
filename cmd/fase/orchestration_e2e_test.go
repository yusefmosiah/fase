package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSendContinuesCanonicalSession(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)

	runOutput := runFase(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "plan stage")
	var initial cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &initial); err != nil {
		t.Fatalf("unmarshal initial run: %v\n%s", err, runOutput)
	}
	waitForJobState(t, binary, configPath, initial.Job.JobID, map[string]bool{"completed": true})

	sendOutput := runFase(t, binary, configPath, "--json", "send", "--session", initial.Session.SessionID, "--prompt", "implement stage")
	var continued cliRunResult
	if err := json.Unmarshal([]byte(sendOutput), &continued); err != nil {
		t.Fatalf("unmarshal send output: %v\n%s", err, sendOutput)
	}
	if continued.Session.SessionID != initial.Session.SessionID {
		t.Fatalf("expected send to keep session %q, got %q", initial.Session.SessionID, continued.Session.SessionID)
	}
	waitForJobState(t, binary, configPath, continued.Job.JobID, map[string]bool{"completed": true})

	statusOutput := runFase(t, binary, configPath, "--json", "status", continued.Job.JobID)
	var status cliStatusResult
	if err := json.Unmarshal([]byte(statusOutput), &status); err != nil {
		t.Fatalf("unmarshal continued status: %v\n%s", err, statusOutput)
	}
	if status.Usage == nil || status.Usage.OutputTokens == 0 {
		t.Fatalf("expected continued job usage, got %+v", status.Usage)
	}
}

func TestHostDrivenPlanImplementVerifyPipeline(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)

	steps := []string{"plan stage", "implement stage", "verify stage", "review stage", "red team stage", "security report stage"}
	var latestSession string
	for idx, step := range steps {
		args := []string{"--json"}
		if idx == 0 {
			args = append(args, "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", step)
		} else {
			args = append(args, "send", "--session", latestSession, "--prompt", step)
		}
		output := runFase(t, binary, configPath, args...)
		var result cliRunResult
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("unmarshal step %q: %v\n%s", step, err, output)
		}
		latestSession = result.Session.SessionID
		waitForJobState(t, binary, configPath, result.Job.JobID, map[string]bool{"completed": true})
	}

	sessionOutput := runFase(t, binary, configPath, "--json", "session", latestSession)
	if !strings.Contains(sessionOutput, latestSession) {
		t.Fatalf("expected session output to mention %s:\n%s", latestSession, sessionOutput)
	}

	jobsOutput := runFase(t, binary, configPath, "--json", "list", "--kind", "jobs", "--session", latestSession)
	var jobs []map[string]any
	if err := json.Unmarshal([]byte(jobsOutput), &jobs); err != nil {
		t.Fatalf("unmarshal pipeline jobs: %v\n%s", err, jobsOutput)
	}
	if len(jobs) < len(steps) {
		t.Fatalf("expected at least %d jobs in pipeline, got %d", len(steps), len(jobs))
	}
}

func TestRecursiveCagentWorkflow(t *testing.T) {
	binary := buildFaseBinary(t)
	configPath := writeFakeCodexConfig(t)
	t.Setenv("FASE_EXECUTABLE", binary)
	t.Setenv("FASE_CONFIG_PATH", configPath)

	runOutput := runFase(t, binary, configPath, "--json", "run", "--adapter", "codex", "--cwd", t.TempDir(), "--prompt", "recursive fase orchestration")
	var result cliRunResult
	if err := json.Unmarshal([]byte(runOutput), &result); err != nil {
		t.Fatalf("unmarshal recursive run: %v\n%s", err, runOutput)
	}
	waitForJobState(t, binary, configPath, result.Job.JobID, map[string]bool{"completed": true})

	logOutput := runFase(t, binary, configPath, "logs", result.Job.JobID)
	if !strings.Contains(logOutput, "Recursive orchestration completed.") {
		t.Fatalf("expected recursive orchestration summary in logs:\n%s", logOutput)
	}

	jobsOutput := runFase(t, binary, configPath, "--json", "list", "--kind", "jobs", "--adapter", "codex")
	var jobs []map[string]any
	if err := json.Unmarshal([]byte(jobsOutput), &jobs); err != nil {
		t.Fatalf("unmarshal recursive jobs: %v\n%s", err, jobsOutput)
	}
	if len(jobs) < 4 {
		t.Fatalf("expected parent plus child jobs, got %d", len(jobs))
	}
}
