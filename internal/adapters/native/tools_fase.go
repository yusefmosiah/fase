package native

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/yusefmosiah/fase/internal/core"
)

// faseBridge is the acyclic interface for FASE service tools.
// Using only core and primitive types avoids the service→adapters import cycle.
type faseBridge interface {
	CreateCheckRecordDirect(ctx context.Context, workID, result, checkerModel, workerModel string, report core.CheckReport) (core.CheckRecord, error)
	GetCheckRecord(ctx context.Context, checkID string) (core.CheckRecord, error)
	ListCheckRecords(ctx context.Context, workID string, limit int) ([]core.CheckRecord, error)
}

// RegisterFASETools registers check record and checker-specific tools into the registry.
// svcAny must satisfy faseBridge (i.e., be *service.Service). If it does not, tool
// registration is skipped silently so non-service sessions still work.
func RegisterFASETools(registry *ToolRegistry, svcAny any) error {
	svc, ok := svcAny.(faseBridge)
	if !ok {
		return nil
	}
	tools := []Tool{
		newCheckRecordCreateTool(svc),
		newCheckRecordListTool(svc),
		newCheckRecordShowTool(svc),
		newRunTestsTool(),
		newRunPlaywrightTool(),
	}
	for _, tool := range tools {
		if err := registry.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

// newCheckRecordCreateTool registers check_record_create.
// The checker calls this after running tests and collecting screenshots.
func newCheckRecordCreateTool(svc faseBridge) Tool {
	type args struct {
		WorkID       string   `json:"work_id"`
		Result       string   `json:"result"`
		BuildOK      bool     `json:"build_ok"`
		TestsPassed  int      `json:"tests_passed"`
		TestsFailed  int      `json:"tests_failed"`
		TestOutput   string   `json:"test_output"`
		DiffStat     string   `json:"diff_stat"`
		Screenshots  []string `json:"screenshots"`
		Videos       []string `json:"videos"`
		CheckerNotes string   `json:"checker_notes"`
		CheckerModel string   `json:"checker_model"`
		WorkerModel  string   `json:"worker_model"`
	}
	return toolFromFunc(
		"check_record_create",
		"Record a checker's verdict for a work item. Call this after running tests and collecting evidence. result must be 'pass' or 'fail'.",
		jsonSchemaObject(map[string]any{
			"work_id":       map[string]any{"type": "string", "description": "Work item ID to check"},
			"result":        map[string]any{"type": "string", "enum": []any{"pass", "fail"}, "description": "Check verdict"},
			"build_ok":      map[string]any{"type": "boolean", "description": "Whether the build succeeded"},
			"tests_passed":  map[string]any{"type": "integer", "description": "Number of tests that passed"},
			"tests_failed":  map[string]any{"type": "integer", "description": "Number of tests that failed"},
			"test_output":   map[string]any{"type": "string", "description": "Test output (truncated to 50KB)"},
			"diff_stat":     map[string]any{"type": "string", "description": "git diff --stat output"},
			"screenshots":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Paths to screenshots"},
			"videos":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Paths to video recordings"},
			"checker_notes": map[string]any{"type": "string", "description": "Free-form observations from the checker"},
			"checker_model": map[string]any{"type": "string", "description": "Model that performed the check"},
			"worker_model":  map[string]any{"type": "string", "description": "Model that did the implementation work"},
		}, []string{"work_id", "result"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode check_record_create args: %w", err)
			}
			report := core.CheckReport{
				BuildOK:      in.BuildOK,
				TestsPassed:  in.TestsPassed,
				TestsFailed:  in.TestsFailed,
				CheckerNotes: in.CheckerNotes,
			}
			if in.TestOutput != "" {
				const maxOutput = 50 * 1024
				if len(in.TestOutput) > maxOutput {
					report.TestOutput = in.TestOutput[:maxOutput] + "\n[truncated]"
				} else {
					report.TestOutput = in.TestOutput
				}
			}
			if in.DiffStat != "" {
				report.DiffStat = in.DiffStat
			}
			if len(in.Screenshots) > 0 {
				report.Screenshots = in.Screenshots
			}
			if len(in.Videos) > 0 {
				report.Videos = in.Videos
			}
			rec, err := svc.CreateCheckRecordDirect(ctx, in.WorkID, in.Result, in.CheckerModel, in.WorkerModel, report)
			if err != nil {
				return "", fmt.Errorf("create check record: %w", err)
			}
			return jsonString(map[string]any{
				"check_id":  rec.CheckID,
				"work_id":   rec.WorkID,
				"result":    rec.Result,
				"created_at": rec.CreatedAt.Format(time.RFC3339),
			})
		},
	)
}

// newCheckRecordListTool registers check_record_list.
func newCheckRecordListTool(svc faseBridge) Tool {
	type args struct {
		WorkID string `json:"work_id"`
		Limit  int    `json:"limit"`
	}
	return toolFromFunc(
		"check_record_list",
		"List check records for a work item, newest first.",
		jsonSchemaObject(map[string]any{
			"work_id": map[string]any{"type": "string", "description": "Work item ID"},
			"limit":   map[string]any{"type": "integer", "description": "Max records to return (default 10)"},
		}, []string{"work_id"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode check_record_list args: %w", err)
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 10
			}
			recs, err := svc.ListCheckRecords(ctx, in.WorkID, limit)
			if err != nil {
				return "", fmt.Errorf("list check records: %w", err)
			}
			items := make([]map[string]any, 0, len(recs))
			for _, r := range recs {
				items = append(items, map[string]any{
					"check_id":      r.CheckID,
					"work_id":       r.WorkID,
					"result":        r.Result,
					"checker_model": r.CheckerModel,
					"worker_model":  r.WorkerModel,
					"created_at":    r.CreatedAt.Format(time.RFC3339),
				})
			}
			return jsonString(map[string]any{
				"work_id": in.WorkID,
				"records": items,
				"count":   len(items),
			})
		},
	)
}

// newCheckRecordShowTool registers check_record_show.
func newCheckRecordShowTool(svc faseBridge) Tool {
	type args struct {
		CheckID string `json:"check_id"`
	}
	return toolFromFunc(
		"check_record_show",
		"Show full details for a single check record including the report.",
		jsonSchemaObject(map[string]any{
			"check_id": map[string]any{"type": "string", "description": "Check record ID"},
		}, []string{"check_id"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode check_record_show args: %w", err)
			}
			rec, err := svc.GetCheckRecord(ctx, in.CheckID)
			if err != nil {
				return "", fmt.Errorf("get check record: %w", err)
			}
			return jsonString(map[string]any{
				"check_id":      rec.CheckID,
				"work_id":       rec.WorkID,
				"result":        rec.Result,
				"checker_model": rec.CheckerModel,
				"worker_model":  rec.WorkerModel,
				"created_at":    rec.CreatedAt.Format(time.RFC3339),
				"report": map[string]any{
					"build_ok":      rec.Report.BuildOK,
					"tests_passed":  rec.Report.TestsPassed,
					"tests_failed":  rec.Report.TestsFailed,
					"test_output":   rec.Report.TestOutput,
					"diff_stat":     rec.Report.DiffStat,
					"screenshots":   rec.Report.Screenshots,
					"videos":        rec.Report.Videos,
					"checker_notes": rec.Report.CheckerNotes,
				},
			})
		},
	)
}

// newRunTestsTool registers run_tests — runs go test ./... and returns structured output.
func newRunTestsTool() Tool {
	type args struct {
		Packages       string `json:"packages"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		Tags           string `json:"tags"`
	}
	return toolFromFunc(
		"run_tests",
		"Run Go tests and return structured output with pass/fail counts and output. Default packages: ./...",
		jsonSchemaObject(map[string]any{
			"packages":        map[string]any{"type": "string", "description": "Package pattern (default: ./...)"},
			"timeout_seconds": map[string]any{"type": "integer", "description": "Test timeout in seconds (default: 300)"},
			"tags":            map[string]any{"type": "string", "description": "Build tags (e.g. 'integration')"},
		}, nil, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode run_tests args: %w", err)
			}
			pkgs := strings.TrimSpace(in.Packages)
			if pkgs == "" {
				pkgs = "./..."
			}
			timeout := in.TimeoutSeconds
			if timeout <= 0 {
				timeout = 300
			}

			cmdArgs := []string{"test", "-v", "-count=1", fmt.Sprintf("-timeout=%ds", timeout)}
			if in.Tags != "" {
				cmdArgs = append(cmdArgs, "-tags", in.Tags)
			}
			cmdArgs = append(cmdArgs, pkgs)

			runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout+30)*time.Second)
			defer cancel()

			cmd := exec.CommandContext(runCtx, "go", cmdArgs...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			start := time.Now()
			runErr := cmd.Run()
			elapsed := time.Since(start)

			exitCode := 0
			if runErr != nil {
				var exitErr *exec.ExitError
				if errorsAs(runErr, &exitErr) {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = -1
				}
			}

			output := stdout.String() + stderr.String()
			passed, failed := parseGoTestCounts(output)

			const maxOutput = 50 * 1024
			if len(output) > maxOutput {
				output = output[:maxOutput] + "\n[output truncated]"
			}

			return jsonString(map[string]any{
				"packages":      pkgs,
				"exit_code":     exitCode,
				"passed":        passed,
				"failed":        failed,
				"elapsed_ms":    elapsed.Milliseconds(),
				"output":        output,
				"build_ok":      exitCode == 0 || failed > 0, // build succeeded even if tests fail
				"all_passed":    exitCode == 0,
			})
		},
	)
}

// newRunPlaywrightTool registers run_playwright — runs npx playwright test and collects screenshots.
func newRunPlaywrightTool() Tool {
	type args struct {
		TestFile       string `json:"test_file"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		Reporter       string `json:"reporter"`
	}
	return toolFromFunc(
		"run_playwright",
		"Run Playwright e2e tests and collect screenshots. Returns structured output with pass/fail counts and screenshot paths.",
		jsonSchemaObject(map[string]any{
			"test_file":       map[string]any{"type": "string", "description": "Test file or pattern (optional, runs all tests if omitted)"},
			"timeout_seconds": map[string]any{"type": "integer", "description": "Test timeout in seconds (default: 120)"},
			"reporter":        map[string]any{"type": "string", "description": "Playwright reporter (default: list)"},
		}, nil, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode run_playwright args: %w", err)
			}
			timeout := in.TimeoutSeconds
			if timeout <= 0 {
				timeout = 120
			}
			reporter := strings.TrimSpace(in.Reporter)
			if reporter == "" {
				reporter = "list"
			}

			cmdArgs := []string{"playwright", "test", "--reporter", reporter}
			if strings.TrimSpace(in.TestFile) != "" {
				cmdArgs = append(cmdArgs, in.TestFile)
			}

			runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout+30)*time.Second)
			defer cancel()

			cmd := exec.CommandContext(runCtx, "npx", cmdArgs...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			start := time.Now()
			runErr := cmd.Run()
			elapsed := time.Since(start)

			exitCode := 0
			if runErr != nil {
				var exitErr *exec.ExitError
				if errorsAs(runErr, &exitErr) {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = -1
				}
			}

			output := stdout.String() + stderr.String()
			passed, failed := parsePlaywrightCounts(output)

			const maxOutput = 50 * 1024
			if len(output) > maxOutput {
				output = output[:maxOutput] + "\n[output truncated]"
			}

			// Collect screenshot paths from output (Playwright logs screenshot paths on failure).
			screenshots := collectPlaywrightScreenshots(output)

			return jsonString(map[string]any{
				"exit_code":   exitCode,
				"passed":      passed,
				"failed":      failed,
				"elapsed_ms":  elapsed.Milliseconds(),
				"output":      output,
				"all_passed":  exitCode == 0,
				"screenshots": screenshots,
			})
		},
	)
}

// parseGoTestCounts parses go test -v output for pass/fail counts.
func parseGoTestCounts(output string) (passed, failed int) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "--- PASS:") {
			passed++
		} else if strings.HasPrefix(line, "--- FAIL:") {
			failed++
		}
	}
	return
}

// parsePlaywrightCounts parses playwright output for pass/fail counts.
func parsePlaywrightCounts(output string) (passed, failed int) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, " passed") {
			// "  5 passed (10s)" style
			var n int
			if _, err := fmt.Sscanf(line, "%d passed", &n); err == nil {
				passed += n
			}
		}
		if strings.Contains(line, " failed") {
			var n int
			if _, err := fmt.Sscanf(line, "%d failed", &n); err == nil {
				failed += n
			}
		}
	}
	return
}

// collectPlaywrightScreenshots finds screenshot file paths in playwright output.
func collectPlaywrightScreenshots(output string) []string {
	var paths []string
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp"} {
			if idx := strings.LastIndex(line, ext); idx >= 0 {
				// Walk back to find the start of the path.
				start := idx
				for start > 0 && line[start-1] != ' ' && line[start-1] != '\t' && line[start-1] != '"' && line[start-1] != '\'' {
					start--
				}
				candidate := line[start : idx+len(ext)]
				if strings.Contains(candidate, "/") && !seen[candidate] {
					paths = append(paths, candidate)
					seen[candidate] = true
				}
			}
		}
	}
	return paths
}
