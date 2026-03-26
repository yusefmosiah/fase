package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusefmosiah/cogent/internal/channelmeta"
	"github.com/yusefmosiah/cogent/internal/core"
	"github.com/yusefmosiah/cogent/internal/service"
)

func TestReportCommandUsesWorkerReportContract(t *testing.T) {
	var got struct {
		Content string            `json:"content"`
		Meta    map[string]string `json:"meta"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/channel/send" {
			t.Fatalf("expected /api/channel/send, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"sent"}`))
	}))
	defer ts.Close()

	parsed, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}

	stateDir := t.TempDir()
	t.Setenv("COGENT_STATE_DIR", stateDir)
	serveData, err := json.Marshal(serveInfo{
		PID:  os.Getpid(),
		Port: port,
		CWD:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("marshal serve.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "serve.json"), serveData, 0o644); err != nil {
		t.Fatalf("write serve.json: %v", err)
	}

	cmd := NewRootCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"report", "hello from cli"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute report command: %v", err)
	}

	if got.Content != "hello from cli" {
		t.Fatalf("expected content to round-trip, got %q", got.Content)
	}
	if want := channelmeta.WorkerReportMeta(channelmeta.TypeInfo); !reflect.DeepEqual(got.Meta, want) {
		t.Fatalf("unexpected meta: got %#v want %#v", got.Meta, want)
	}
}

func TestCheckCreateCommandIncludesArtifacts(t *testing.T) {
	var got struct {
		WorkID string `json:"work_id"`
		Result string `json:"result"`
		Report struct {
			BuildOK      bool     `json:"build_ok"`
			TestsPassed  int      `json:"tests_passed"`
			Screenshots  []string `json:"screenshots"`
			Videos       []string `json:"videos"`
			CheckerNotes string   `json:"checker_notes"`
		} `json:"report"`
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/check/create" {
			t.Fatalf("expected /api/check/create, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(core.CheckRecord{CheckID: "chk_123", Result: got.Result})
	}))
	defer ts.Close()

	parsed, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}

	stateDir := t.TempDir()
	t.Setenv("COGENT_STATE_DIR", stateDir)
	serveData, err := json.Marshal(serveInfo{
		PID:  os.Getpid(),
		Port: port,
		CWD:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("marshal serve.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "serve.json"), serveData, 0o644); err != nil {
		t.Fatalf("write serve.json: %v", err)
	}

	cmd := NewRootCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{
		"check", "create", "work_123",
		"--result", "pass",
		"--build-ok",
		"--tests-passed", "3",
		"--screenshots", "/tmp/one.png,/tmp/two.png",
		"--videos", "/tmp/run.webm",
		"--notes", "captured evidence",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute check create command: %v", err)
	}

	if got.WorkID != "work_123" {
		t.Fatalf("expected work_id to round-trip, got %q", got.WorkID)
	}
	if got.Result != "pass" {
		t.Fatalf("expected result=pass, got %q", got.Result)
	}
	if !got.Report.BuildOK {
		t.Fatal("expected build_ok=true")
	}
	if got.Report.TestsPassed != 3 {
		t.Fatalf("expected tests_passed=3, got %d", got.Report.TestsPassed)
	}
	if !reflect.DeepEqual(got.Report.Screenshots, []string{"/tmp/one.png", "/tmp/two.png"}) {
		t.Fatalf("unexpected screenshots: %#v", got.Report.Screenshots)
	}
	if !reflect.DeepEqual(got.Report.Videos, []string{"/tmp/run.webm"}) {
		t.Fatalf("unexpected videos: %#v", got.Report.Videos)
	}
	if got.Report.CheckerNotes != "captured evidence" {
		t.Fatalf("unexpected checker_notes: %q", got.Report.CheckerNotes)
	}
}

func TestCheckListCommandUsesCanonicalDefaultLimit(t *testing.T) {
	var gotWorkID string
	var gotLimit string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/api/check/list" {
			t.Fatalf("expected /api/check/list, got %s", r.URL.Path)
		}
		gotWorkID = r.URL.Query().Get("work_id")
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]core.CheckRecord{{
			CheckID: "chk_list",
			WorkID:  gotWorkID,
			Result:  "pass",
		}})
	}))
	defer ts.Close()

	parsed, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}

	stateDir := t.TempDir()
	t.Setenv("COGENT_STATE_DIR", stateDir)
	serveData, err := json.Marshal(serveInfo{
		PID:  os.Getpid(),
		Port: port,
		CWD:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("marshal serve.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "serve.json"), serveData, 0o644); err != nil {
		t.Fatalf("write serve.json: %v", err)
	}

	cmd := NewRootCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"check", "list", "work_789"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute check list command: %v", err)
	}

	if gotWorkID != "work_789" {
		t.Fatalf("work_id = %q, want work_789", gotWorkID)
	}
	if gotLimit != strconv.Itoa(core.DefaultCheckRecordListLimit) {
		t.Fatalf("limit = %q, want %d", gotLimit, core.DefaultCheckRecordListLimit)
	}
	if !strings.Contains(stdout.String(), "chk_list\tpass") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
}

func TestWorkCheckCommandUsesCanonicalCheckResponse(t *testing.T) {
	var got struct {
		WorkID string `json:"work_id"`
		Result string `json:"result"`
		Report struct {
			BuildOK      bool   `json:"build_ok"`
			TestOutput   string `json:"test_output"`
			CheckerNotes string `json:"checker_notes"`
		} `json:"report"`
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/work/work_456/check" {
			t.Fatalf("expected /api/work/work_456/check, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(core.CheckRecord{CheckID: "chk_alias", WorkID: "work_456", Result: got.Result})
	}))
	defer ts.Close()

	parsed, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}

	stateDir := t.TempDir()
	t.Setenv("COGENT_STATE_DIR", stateDir)
	serveData, err := json.Marshal(serveInfo{
		PID:  os.Getpid(),
		Port: port,
		CWD:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("marshal serve.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "serve.json"), serveData, 0o644); err != nil {
		t.Fatalf("write serve.json: %v", err)
	}

	cmd := NewRootCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{
		"work", "check", "work_456",
		"--result", "pass",
		"--build-ok",
		"--test-output", "go test ./internal/service",
		"--notes", "verified canonical alias",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute work check command: %v", err)
	}

	if got.WorkID != "work_456" {
		t.Fatalf("expected work_id to round-trip, got %q", got.WorkID)
	}
	if got.Result != "pass" {
		t.Fatalf("expected result=pass, got %q", got.Result)
	}
	if !got.Report.BuildOK {
		t.Fatal("expected build_ok=true")
	}
	if got.Report.TestOutput != "go test ./internal/service" {
		t.Fatalf("unexpected test_output: %q", got.Report.TestOutput)
	}
	if got.Report.CheckerNotes != "verified canonical alias" {
		t.Fatalf("unexpected checker_notes: %q", got.Report.CheckerNotes)
	}
	if !strings.Contains(stdout.String(), "check chk_alias: pass") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
}

func TestRenderWorkShowIncludesCanonicalEvidenceBundle(t *testing.T) {
	var out bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&out)

	result := &service.WorkShowResult{
		Work: core.WorkItemRecord{
			WorkID:         "work_123",
			Title:          "Bundle review",
			ExecutionState: core.WorkExecutionStateChecking,
			ApprovalState:  core.WorkApprovalStatePending,
		},
		CheckRecords: []core.CheckRecord{{
			CheckID:      "chk_123",
			CheckerModel: "claude",
			Result:       "pass",
			Report:       core.CheckReport{CheckerNotes: "verified evidence bundle"},
			CreatedAt:    time.Unix(1700000000, 0).UTC(),
		}},
		Attestations: []core.AttestationRecord{{
			AttestationID: "att_123",
			Result:        "passed",
			VerifierKind:  "deterministic",
			Summary:       "policy resolved",
			CreatedAt:     time.Unix(1700000001, 0).UTC(),
		}},
		Artifacts: []core.ArtifactRecord{{
			ArtifactID: "art_123",
			Kind:       "check_output",
			Path:       "/tmp/check.txt",
		}},
		Docs: []core.DocContentRecord{{
			DocID:   "doc_123",
			Path:    "docs/review.md",
			Format:  "markdown",
			Version: 2,
			Title:   "Review Notes",
		}},
	}

	if err := renderWorkShow(cmd, false, result); err != nil {
		t.Fatalf("renderWorkShow: %v", err)
	}

	rendered := out.String()
	for _, want := range []string{"checks: 1", "chk_123", "attestations: 1", "att_123", "artifacts: 1", "art_123", "docs: 1", "doc_123", "verified evidence bundle", "docs/review.md"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered work show missing %q:\n%s", want, rendered)
		}
	}
}

func TestWorkVerifyCommandHasAuditOnlyDescription(t *testing.T) {
	cmd := NewRootCommand()
	workCmd, _, err := cmd.Find([]string{"work"})
	if err != nil {
		t.Fatalf("find work command: %v", err)
	}
	verifyCmd, _, err := workCmd.Find([]string{"verify"})
	if err != nil {
		t.Fatalf("find verify command: %v", err)
	}
	if !strings.Contains(strings.ToLower(verifyCmd.Short), "audit") {
		t.Fatalf("verify short help should describe audit semantics, got %q", verifyCmd.Short)
	}
	long := strings.ToLower(verifyCmd.Long)
	if !strings.Contains(long, "does not act as the completion-review bundle") || !strings.Contains(long, "does not change work state") {
		t.Fatalf("verify long help should disambiguate audit-only semantics, got %q", verifyCmd.Long)
	}
}
