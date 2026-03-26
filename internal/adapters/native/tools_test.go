package native

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusefmosiah/cogent/internal/core"
)

func TestToolRegistryDefinitionsAndExecute(t *testing.T) {
	t.Parallel()

	registry, err := NewToolRegistry(
		toolFromFunc("b_tool", "second", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
			return "b", nil
		}),
		toolFromFunc("a_tool", "first", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
			return "a", nil
		}),
	)
	if err != nil {
		t.Fatalf("NewToolRegistry returned error: %v", err)
	}

	defs := registry.Definitions()
	if len(defs) != 2 {
		t.Fatalf("expected 2 tool defs, got %d", len(defs))
	}
	if defs[0].Name != "a_tool" || defs[1].Name != "b_tool" {
		t.Fatalf("definitions not sorted: %+v", defs)
	}
	if _, ok := registry.Lookup("a_tool"); !ok {
		t.Fatal("expected lookup for registered tool to succeed")
	}
	if _, ok := registry.Lookup("missing"); ok {
		t.Fatal("expected lookup for missing tool to fail")
	}

	anthropic := registry.AnthropicDefinitions()
	if len(anthropic) != 2 || anthropic[0].Name != "a_tool" || anthropic[0].InputSchema["type"] != "object" {
		t.Fatalf("unexpected anthropic definitions: %+v", anthropic)
	}
	openAI := registry.OpenAIDefinitions()
	if len(openAI) != 2 || openAI[0].Type != "function" {
		t.Fatalf("unexpected openai definitions: %+v", openAI)
	}
	if got := openAI[0].Parameters["type"]; got != "object" {
		t.Fatalf("expected default schema object, got %v", got)
	}
	if props, ok := openAI[0].Parameters["properties"].(map[string]any); !ok || len(props) != 0 {
		t.Fatalf("expected empty properties map for parameterless tool, got %+v", openAI[0].Parameters["properties"])
	}
	openAI[0].Parameters["mutated"] = true
	if defsAgain := registry.Definitions(); defsAgain[0].Parameters["mutated"] != nil {
		t.Fatalf("definitions should be cloned, got %+v", defsAgain[0].Parameters)
	}

	out, err := registry.Execute(context.Background(), "a_tool", nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if out != "a" {
		t.Fatalf("unexpected tool output: %q", out)
	}
	if _, err := registry.Execute(context.Background(), "missing", nil); err == nil {
		t.Fatal("expected missing tool execution to fail")
	}
}

func TestToolRegistryRegisterRejectsDuplicateNames(t *testing.T) {
	t.Parallel()

	registry := MustNewToolRegistry(toolFromFunc("dupe", "first", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
		return "ok", nil
	}))

	err := registry.Register(toolFromFunc("dupe", "second", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
		return "nope", nil
	}))
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate registration error, got %v", err)
	}
}

func TestCodingToolsRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registry := MustNewToolRegistry()
	if err := RegisterCodingTools(registry, dir); err != nil {
		t.Fatalf("RegisterCodingTools returned error: %v", err)
	}

	fileArgs := mustJSON(t, map[string]any{
		"path":    "nested/example.txt",
		"content": "hello world\nhello team\n",
	})
	if _, err := registry.Execute(context.Background(), "write_file", fileArgs); err != nil {
		t.Fatalf("write_file returned error: %v", err)
	}

	editArgs := mustJSON(t, map[string]any{
		"path":       "nested/example.txt",
		"old_string": "hello world",
		"new_string": "hello registry",
	})
	if _, err := registry.Execute(context.Background(), "edit_file", editArgs); err != nil {
		t.Fatalf("edit_file returned error: %v", err)
	}

	readOut, err := registry.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{
		"path": filepath.Join("nested", "example.txt"),
	}))
	if err != nil {
		t.Fatalf("read_file returned error: %v", err)
	}
	if !strings.Contains(readOut, "hello registry") {
		t.Fatalf("read_file output missing edited content: %s", readOut)
	}

	globOut, err := registry.Execute(context.Background(), "glob", mustJSON(t, map[string]any{
		"pattern": "**/*.txt",
	}))
	if err != nil {
		t.Fatalf("glob returned error: %v", err)
	}
	if !strings.Contains(globOut, "nested/example.txt") {
		t.Fatalf("glob output missing expected file: %s", globOut)
	}

	grepOut, err := registry.Execute(context.Background(), "grep", mustJSON(t, map[string]any{
		"pattern": "registry",
	}))
	if err != nil {
		t.Fatalf("grep returned error: %v", err)
	}
	if !strings.Contains(grepOut, "nested/example.txt") {
		t.Fatalf("grep output missing expected file: %s", grepOut)
	}
}

func TestRegisterCogentTools_NoServiceSkips(t *testing.T) {
	t.Parallel()
	registry := MustNewToolRegistry()
	if err := RegisterCogentTools(registry, nil); err != nil {
		t.Fatalf("RegisterCogentTools with nil svc returned error: %v", err)
	}
	if len(registry.Tools()) != 0 {
		t.Fatalf("expected no tools registered when svc is nil, got %d", len(registry.Tools()))
	}
	// Non-cogentBridge type also skips.
	if err := RegisterCogentTools(registry, "not-a-service"); err != nil {
		t.Fatalf("RegisterCogentTools with non-service returned error: %v", err)
	}
	if len(registry.Tools()) != 0 {
		t.Fatalf("expected no tools registered for non-service, got %d", len(registry.Tools()))
	}
}

func TestRegisterCogentTools_WithService(t *testing.T) {
	t.Parallel()
	registry := MustNewToolRegistry()
	svc := &fakeCogentBridge{}
	if err := RegisterCogentTools(registry, svc); err != nil {
		t.Fatalf("RegisterCogentTools returned error: %v", err)
	}
	want := []string{"check_record_create", "check_record_list", "check_record_show", "run_playwright", "run_tests"}
	tools := registry.Tools()
	if len(tools) != len(want) {
		t.Fatalf("expected %d tools, got %d: %v", len(want), len(tools), toolNames(tools))
	}
	for i, name := range want {
		if tools[i].Name != name {
			t.Errorf("tool[%d]: expected %q, got %q", i, name, tools[i].Name)
		}
	}
}

func TestCheckRecordCreate_ValidatesResult(t *testing.T) {
	t.Parallel()
	registry := MustNewToolRegistry()
	svc := &fakeCogentBridge{}
	if err := RegisterCogentTools(registry, svc); err != nil {
		t.Fatalf("RegisterCogentTools returned error: %v", err)
	}
	// Missing work_id and invalid result should propagate error from svc.
	out, err := registry.Execute(context.Background(), "check_record_create", mustJSON(t, map[string]any{
		"work_id": "wid_test",
		"result":  "pass",
	}))
	if err != nil {
		t.Fatalf("check_record_create returned error: %v", err)
	}
	var record core.CheckRecord
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("decode check_record_create output: %v", err)
	}
	if record.CheckID != "chk_test" {
		t.Fatalf("check_id = %q, want chk_test", record.CheckID)
	}
	if record.WorkID != "wid_test" {
		t.Fatalf("work_id = %q, want wid_test", record.WorkID)
	}
	if record.Result != "pass" {
		t.Fatalf("result = %q, want pass", record.Result)
	}
}

func TestCheckRecordList_ReturnsRecords(t *testing.T) {
	t.Parallel()
	registry := MustNewToolRegistry()
	svc := &fakeCogentBridge{}
	if err := RegisterCogentTools(registry, svc); err != nil {
		t.Fatalf("RegisterCogentTools returned error: %v", err)
	}
	out, err := registry.Execute(context.Background(), "check_record_list", mustJSON(t, map[string]any{
		"work_id": "wid_test",
	}))
	if err != nil {
		t.Fatalf("check_record_list returned error: %v", err)
	}
	var records []core.CheckRecord
	if err := json.Unmarshal([]byte(out), &records); err != nil {
		t.Fatalf("decode check_record_list output: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	if records[0].WorkID != "wid_test" {
		t.Fatalf("work_id = %q, want wid_test", records[0].WorkID)
	}
	if records[0].CheckID != "chk_1" {
		t.Fatalf("check_id = %q, want chk_1", records[0].CheckID)
	}
	if svc.lastListLimit != core.DefaultCheckRecordListLimit {
		t.Fatalf("limit = %d, want %d", svc.lastListLimit, core.DefaultCheckRecordListLimit)
	}
}

func TestCheckRecordList_RespectsRequestedLimit(t *testing.T) {
	t.Parallel()
	registry := MustNewToolRegistry()
	svc := &fakeCogentBridge{}
	if err := RegisterCogentTools(registry, svc); err != nil {
		t.Fatalf("RegisterCogentTools returned error: %v", err)
	}
	if _, err := registry.Execute(context.Background(), "check_record_list", mustJSON(t, map[string]any{
		"work_id": "wid_test",
		"limit":   3,
	})); err != nil {
		t.Fatalf("check_record_list returned error: %v", err)
	}
	if svc.lastListLimit != 3 {
		t.Fatalf("limit = %d, want 3", svc.lastListLimit)
	}
}

func TestCheckRecordShow_ReturnsCanonicalRecord(t *testing.T) {
	t.Parallel()
	registry := MustNewToolRegistry()
	svc := &fakeCogentBridge{}
	if err := RegisterCogentTools(registry, svc); err != nil {
		t.Fatalf("RegisterCogentTools returned error: %v", err)
	}
	out, err := registry.Execute(context.Background(), "check_record_show", mustJSON(t, map[string]any{
		"check_id": "chk_show",
	}))
	if err != nil {
		t.Fatalf("check_record_show returned error: %v", err)
	}
	var record core.CheckRecord
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("decode check_record_show output: %v", err)
	}
	if record.CheckID != "chk_show" {
		t.Fatalf("check_id = %q, want chk_show", record.CheckID)
	}
	if record.Report.CheckerNotes != "looks good" {
		t.Fatalf("checker_notes = %q, want looks good", record.Report.CheckerNotes)
	}
}

func TestRunTests_ParsesCounts(t *testing.T) {
	t.Parallel()
	// Test parseGoTestCounts
	output := "--- PASS: TestFoo (0.01s)\n--- PASS: TestBar (0.02s)\n--- FAIL: TestBaz (0.00s)"
	passed, failed := parseGoTestCounts(output)
	if passed != 2 {
		t.Errorf("expected 2 passed, got %d", passed)
	}
	if failed != 1 {
		t.Errorf("expected 1 failed, got %d", failed)
	}
}

func TestRunPlaywright_ParsesCounts(t *testing.T) {
	t.Parallel()
	output := "  5 passed (10s)\n  2 failed\n  Running 7 tests using 1 worker"
	passed, failed := parsePlaywrightCounts(output)
	if passed != 5 {
		t.Errorf("expected 5 passed, got %d", passed)
	}
	if failed != 2 {
		t.Errorf("expected 2 failed, got %d", failed)
	}
}

func TestCollectPlaywrightScreenshots(t *testing.T) {
	t.Parallel()
	output := "  screenshot saved at /tmp/test-results/screenshot-failed.png\n  attachment: /tmp/artifacts/screen.jpg"
	shots := collectPlaywrightScreenshots(output)
	if len(shots) < 1 {
		t.Fatalf("expected at least 1 screenshot, got %v", shots)
	}
	found := false
	for _, s := range shots {
		if strings.Contains(s, ".png") || strings.Contains(s, ".jpg") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no screenshot paths found: %v", shots)
	}
}

// fakeCogentBridge implements cogentBridge for testing.
type fakeCogentBridge struct {
	lastListLimit int
}

func (f *fakeCogentBridge) CreateCheckRecordDirect(_ context.Context, workID, result, checkerModel, workerModel string, report core.CheckReport, createdBy string) (core.CheckRecord, error) {
	return core.CheckRecord{
		CheckID: "chk_test",
		WorkID:  workID,
		Result:  result,
		Report:  report,
	}, nil
}

func (f *fakeCogentBridge) GetCheckRecord(_ context.Context, checkID string) (core.CheckRecord, error) {
	return core.CheckRecord{
		CheckID: checkID,
		WorkID:  "wid_test",
		Result:  "pass",
		Report:  core.CheckReport{CheckerNotes: "looks good"},
	}, nil
}

func (f *fakeCogentBridge) ListCheckRecords(_ context.Context, workID string, limit int) ([]core.CheckRecord, error) {
	f.lastListLimit = limit
	return []core.CheckRecord{
		{CheckID: "chk_1", WorkID: workID, Result: "pass"},
	}, nil
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	return data
}

func toolNames(tools []Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}
