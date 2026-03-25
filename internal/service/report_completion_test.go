package service

import (
	"context"
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

	"github.com/yusefmosiah/fase/internal/channelmeta"
	"github.com/yusefmosiah/fase/internal/core"
)

func TestReportJobCompletionPostsChannelNotification(t *testing.T) {
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

	serveData, err := json.Marshal(map[string]any{"port": port})
	if err != nil {
		t.Fatalf("marshal serve.json: %v", err)
	}

	svc := newTestService(t)
	if err := os.WriteFile(filepath.Join(svc.Paths.StateDir, "serve.json"), serveData, 0o644); err != nil {
		t.Fatalf("write service serve.json: %v", err)
	}

	ctx := context.Background()
	work, err := svc.CreateWork(ctx, WorkCreateRequest{
		Title:     "completion report",
		Objective: "Inspect canonical proof bundle reporting",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}
	now := time.Now().UTC()
	session := core.SessionRecord{
		SessionID:     "sess_report_completion",
		Label:         "report completion session",
		CreatedAt:     now,
		UpdatedAt:     now,
		Status:        "completed",
		OriginAdapter: "codex",
		CWD:           t.TempDir(),
		Metadata:      map[string]any{},
	}
	if err := svc.store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	job := core.JobRecord{
		JobID:     "job_test123",
		SessionID: session.SessionID,
		WorkID:    work.WorkID,
		Adapter:   "codex",
		State:     core.JobStateCompleted,
		CWD:       session.CWD,
		CreatedAt: now,
		UpdatedAt: now,
		Summary:   map[string]any{"message": "job finished"},
	}
	if err := svc.store.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, _, err := svc.SetDocContent(ctx, work.WorkID, "docs/spec-check-flow.md", "Check Flow Spec", "# Check Flow\n", "markdown"); err != nil {
		t.Fatalf("SetDocContent: %v", err)
	}
	artifactPath := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(artifactPath, []byte("report artifact"), 0o644); err != nil {
		t.Fatalf("WriteFile artifact: %v", err)
	}
	artifact := core.ArtifactRecord{
		ArtifactID: core.GenerateID("art"),
		JobID:      job.JobID,
		SessionID:  job.SessionID,
		Kind:       "check_output",
		Path:       artifactPath,
		CreatedAt:  time.Now().UTC(),
		Metadata:   map[string]any{"work_id": work.WorkID},
	}
	if err := svc.store.InsertArtifact(ctx, artifact); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	check, err := svc.CreateCheckRecord(ctx, CheckRecordCreateRequest{
		WorkID:       work.WorkID,
		Result:       "pass",
		CheckerModel: "claude-sonnet-4-6",
		Report: core.CheckReport{
			BuildOK:      true,
			TestsPassed:  1,
			TestOutput:   "go test ./internal/service",
			CheckerNotes: "verified canonical proof bundle",
		},
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("CreateCheckRecord: %v", err)
	}
	attestation, _, err := svc.AttestWork(ctx, WorkAttestRequest{
		WorkID:       work.WorkID,
		Result:       "passed",
		Summary:      "review complete",
		Method:       "automated_review",
		VerifierKind: "attestation",
		ArtifactID:   artifact.ArtifactID,
		CreatedBy:    "test",
	})
	if err != nil {
		t.Fatalf("AttestWork: %v", err)
	}

	svc.reportJobCompletion(job, "job.completed", "job finished")

	for _, want := range []string{"job_test123", work.WorkID, check.CheckID, attestation.AttestationID, artifact.ArtifactID, "doc_", "proof:"} {
		if !strings.Contains(got.Content, want) {
			t.Fatalf("expected content to include %q, got %q", want, got.Content)
		}
	}
	if want := channelmeta.JobCompletionMeta(); !reflect.DeepEqual(got.Meta, want) {
		t.Fatalf("unexpected meta: got %#v want %#v", got.Meta, want)
	}
}
