package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectBashLogCommandsParsesCodexClaudeAndOpenCode(t *testing.T) {
	cwd := t.TempDir()
	rawDir := filepath.Join(cwd, ".fase", "raw", "stdout")
	jobDir := filepath.Join(rawDir, "job_0002")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatalf("mkdir job dir: %v", err)
	}

	lines := []string{
		`{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc \"pwd\"","exit_code":0,"aggregated_output":"/tmp/work"}}`,
		`{"type":"tool_use","id":"call_1","name":"Bash","input":{"command":"ls"}}`,
		`{"type":"tool_result","tool_use_id":"call_1","content":"done"}`,
		`{"type":"text","sessionID":"opencode-session-123","part":{"type":"text","text":"OpenCode completed the task"}}`,
		`{"type":"tool_use","sessionID":"opencode-session-123","part":{"type":"tool_use","id":"call_2","name":"bash","input":{"command":"git status"}}}`,
		`{"type":"tool_result","sessionID":"opencode-session-123","part":{"type":"tool_result","tool_use_id":"call_2","stdout":"clean","exit_code":0}}`,
	}
	if err := os.WriteFile(filepath.Join(jobDir, "00001.jsonl"), []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	commands, jobID := collectBashLogCommands(rawDir)
	if jobID != "job_0002" {
		t.Fatalf("jobID = %q, want job_0002", jobID)
	}
	if len(commands) != 4 {
		t.Fatalf("commands = %#v, want 4 entries", commands)
	}

	if got := commands[0]; got.Command != "pwd" || got.ExitCode != 0 || got.OutputPreview != "/tmp/work" {
		t.Fatalf("codex command = %#v, want pwd/0/tmp/work", got)
	}
	if got := commands[1]; got.Command != "ls" || got.ExitCode != 0 || got.OutputPreview != "done" {
		t.Fatalf("claude command = %#v, want ls/0/done", got)
	}
	if got := commands[2]; got.Comment != "OpenCode completed the task" {
		t.Fatalf("opencode comment = %#v, want comment", got)
	}
	if got := commands[3]; got.Command != "git status" || got.ExitCode != 0 || got.OutputPreview != "clean" {
		t.Fatalf("opencode command = %#v, want git status/0/clean", got)
	}
}

func TestParseRunDiffStatExtractsFilesAndLines(t *testing.T) {
	stat, ok := parseRunDiffStat(" 3 files changed, 12 insertions(+), 4 deletions(-)\n")
	if !ok {
		t.Fatal("expected diff stat to parse")
	}
	if stat.filesChanged != 3 || stat.linesAdded != 12 || stat.linesRemoved != 4 {
		t.Fatalf("stat = %#v, want 3/12/4", stat)
	}
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	out := lines[0]
	for _, line := range lines[1:] {
		out += "\n" + line
	}
	return out
}
