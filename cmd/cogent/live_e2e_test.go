package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestLiveCheapSmokeMatrix(t *testing.T) {
	if os.Getenv("COGENT_LIVE_E2E") == "" {
		t.Skip("set COGENT_LIVE_E2E=1 to run live adapter tests")
	}

	binary := buildCogentBinary(t)
	configPath := os.Getenv("COGENT_LIVE_CONFIG")
	if configPath == "" {
		t.Fatal("COGENT_LIVE_CONFIG is required for live tests")
	}

	cases := []struct {
		name     string
		adapter  string
		modelEnv string
		prompt   string
	}{
		{name: "codex", adapter: "codex", modelEnv: "COGENT_LIVE_CODEX_MODEL", prompt: "Reply with exactly OK and nothing else."},
		{name: "claude", adapter: "claude", modelEnv: "COGENT_LIVE_CLAUDE_MODEL", prompt: "Reply with exactly OK and nothing else."},
		{name: "opencode", adapter: "opencode", modelEnv: "COGENT_LIVE_OPENCODE_MODEL", prompt: "Reply with exactly OK and nothing else."},
		{name: "gemini", adapter: "gemini", modelEnv: "COGENT_LIVE_GEMINI_MODEL", prompt: "Reply with exactly OK and nothing else."},
		{name: "pi", adapter: "pi", modelEnv: "COGENT_LIVE_PI_MODEL", prompt: "Reply with exactly OK and nothing else."},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			model := os.Getenv(tc.modelEnv)
			if model == "" {
				t.Skipf("set %s to run %s live lane", tc.modelEnv, tc.adapter)
			}

			output := runCogent(t, binary, configPath, "--json", "run", "--adapter", tc.adapter, "--model", model, "--cwd", t.TempDir(), "--prompt", tc.prompt)
			var result cliRunResult
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatalf("unmarshal live run: %v\n%s", err, output)
			}
			waitForJobState(t, binary, configPath, result.Job.JobID, map[string]bool{"completed": true})

			statusOutput := runCogent(t, binary, configPath, "--json", "status", result.Job.JobID)
			var status cliStatusResult
			if err := json.Unmarshal([]byte(statusOutput), &status); err != nil {
				t.Fatalf("unmarshal live status: %v\n%s", err, statusOutput)
			}
			if status.Job.State != "completed" {
				t.Fatalf("expected completed live job, got %q", status.Job.State)
			}
		})
	}
}
