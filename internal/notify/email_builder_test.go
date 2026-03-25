package notify

import (
	"testing"

	"github.com/yusefmosiah/fase/internal/core"
)

func TestBuildWorkCompletionEmailSuccess(t *testing.T) {
	work := core.WorkItemRecord{
		WorkID:         "work_test123",
		Title:          "Test Task: Implement Email Notification",
		Objective:      "Add email notifications via Resend API for work item completion",
		Kind:           "implement",
		ExecutionState: core.WorkExecutionStateDone,
		ApprovalState:  core.WorkApprovalStateVerified,
		Metadata: map[string]any{
			"result": "passed",
		},
	}
	message := "Successfully implemented email notifications. All tests passing."
	bundle := ProofBundle{
		Work: work,
		CheckRecords: []core.CheckRecord{{
			CheckID:      "chk_test123",
			Result:       "pass",
			CheckerModel: "claude-sonnet-4-6",
		}},
		Attestations: []core.AttestationRecord{{
			AttestationID: "att_test123",
			Method:        "manual",
			VerifierKind:  "supervisor",
			Result:        "passed",
		}},
		Artifacts: []core.ArtifactRecord{{
			ArtifactID: "art_test123",
			Kind:       "check_output",
			Path:       "/tmp/check.txt",
		}},
		Docs: []core.DocContentRecord{{
			DocID:          "doc_test123",
			Path:           "docs/spec-check-flow.md",
			RepoFileExists: true,
			MatchesRepo:    true,
		}},
	}

	html := BuildWorkCompletionEmail(bundle, message, true)

	// Verify key elements are present
	if len(html) == 0 {
		t.Error("BuildWorkCompletionEmailSuccess: empty HTML generated")
	}
	if !contains(html, "COMPLETED") {
		t.Error("BuildWorkCompletionEmailSuccess: missing COMPLETED status")
	}
	if !contains(html, work.Title) {
		t.Error("BuildWorkCompletionEmailSuccess: missing work title")
	}
	if !contains(html, work.WorkID) {
		t.Error("BuildWorkCompletionEmailSuccess: missing work ID")
	}
	if !contains(html, message) {
		t.Error("BuildWorkCompletionEmailSuccess: missing update message")
	}
	if !contains(html, "passed") {
		t.Error("BuildWorkCompletionEmailSuccess: missing attestation result")
	}
	if !contains(html, "#16a34a") { // green color for success
		t.Error("BuildWorkCompletionEmailSuccess: missing success color")
	}
	for _, want := range []string{"Canonical Proof Bundle", "chk_test123", "att_test123", "art_test123", "doc_test123"} {
		if !contains(html, want) {
			t.Errorf("BuildWorkCompletionEmailSuccess: missing proof bundle reference %q", want)
		}
	}
}

func TestBuildWorkCompletionEmailFailure(t *testing.T) {
	work := core.WorkItemRecord{
		WorkID:         "work_test456",
		Title:          "Test Task: Failing Job",
		Objective:      "Test email for failures",
		Kind:           "implement",
		ExecutionState: core.WorkExecutionStateFailed,
		ApprovalState:  core.WorkApprovalStateRejected,
	}
	message := "Task failed: database connection timeout"
	bundle := ProofBundle{Work: work}

	html := BuildWorkCompletionEmail(bundle, message, false)

	// Verify key elements for failure
	if len(html) == 0 {
		t.Error("BuildWorkCompletionEmailFailure: empty HTML generated")
	}
	if !contains(html, "FAILED") {
		t.Error("BuildWorkCompletionEmailFailure: missing FAILED status")
	}
	if !contains(html, message) {
		t.Error("BuildWorkCompletionEmailFailure: missing failure message")
	}
	if !contains(html, "#dc2626") { // red color for failure
		t.Error("BuildWorkCompletionEmailFailure: missing failure color")
	}
	if !contains(html, "Canonical Proof Bundle") {
		t.Error("BuildWorkCompletionEmailFailure: missing proof bundle section")
	}
}

func TestHTMLEscaping(t *testing.T) {
	work := core.WorkItemRecord{
		WorkID:    "work_test",
		Title:     "Test Alert XSS",
		Objective: "Test & verify escaping of HTML special chars <tag>",
		Kind:      "implement",
	}

	html := BuildWorkCompletionEmail(ProofBundle{Work: work}, "", true)

	// Verify that special HTML characters in objective are escaped
	if !contains(html, "&lt;") && !contains(html, "&amp;") {
		t.Error("HTMLEscaping: HTML entities not properly escaped in output")
	}
}

func TestBuildAttestationEmailPassed(t *testing.T) {
	work := &core.WorkItemRecord{
		WorkID:    "work_attest01",
		Title:     "Implement email notifications",
		Objective: "Add Resend-based email notifications for work attestation events",
		Kind:      "implement",
	}
	attestation := core.AttestationRecord{
		AttestationID: "attest_01",
		Result:        "passed",
		Summary:       "All checks pass. Build is green. Screenshots look correct.",
		VerifierKind:  "attestation",
		Method:        "automated_review",
	}

	html := BuildAttestationEmail(work, attestation, nil)

	if len(html) == 0 {
		t.Error("BuildAttestationEmailPassed: empty HTML generated")
	}
	if !contains(html, "PASSED") {
		t.Error("BuildAttestationEmailPassed: missing PASSED status")
	}
	if !contains(html, "#16a34a") {
		t.Error("BuildAttestationEmailPassed: missing green color for passed")
	}
	if !contains(html, work.Title) {
		t.Error("BuildAttestationEmailPassed: missing work title")
	}
	if !contains(html, work.WorkID) {
		t.Error("BuildAttestationEmailPassed: missing work ID")
	}
	if !contains(html, "automated_review") {
		t.Error("BuildAttestationEmailPassed: missing attestation method")
	}
	if !contains(html, attestation.Summary) {
		t.Error("BuildAttestationEmailPassed: missing attestation summary")
	}
}

func TestBuildAttestationEmailFailed(t *testing.T) {
	work := &core.WorkItemRecord{
		WorkID:    "work_attest02",
		Title:     "Fix broken pipeline",
		Objective: "Repair the CI pipeline",
		Kind:      "implement",
	}
	attestation := core.AttestationRecord{
		AttestationID: "attest_02",
		Result:        "failed",
		Summary:       "Tests failing: 3 failures in auth module.",
		VerifierKind:  "attestation",
		Method:        "automated_review",
	}

	html := BuildAttestationEmail(work, attestation, nil)

	if !contains(html, "FAILED") {
		t.Error("BuildAttestationEmailFailed: missing FAILED status")
	}
	if !contains(html, "#dc2626") {
		t.Error("BuildAttestationEmailFailed: missing red color for failed")
	}
	if !contains(html, attestation.Summary) {
		t.Error("BuildAttestationEmailFailed: missing attestation summary")
	}
}

func TestBuildAttestationEmailWithCheckRecord(t *testing.T) {
	work := &core.WorkItemRecord{
		WorkID:    "work_attest03",
		Title:     "Add database migrations",
		Objective: "Add new schema migrations",
		Kind:      "implement",
	}
	attestation := core.AttestationRecord{
		AttestationID: "attest_03",
		Result:        "passed",
		VerifierKind:  "attestation",
		Method:        "automated_review",
	}
	cr := &core.CheckRecord{
		CheckID:      "chk_01",
		Result:       "pass",
		CheckerModel: "claude-sonnet-4-6",
		Report: core.CheckReport{
			BuildOK:      true,
			TestsPassed:  42,
			TestsFailed:  0,
			DiffStat:     "3 files changed, 120 insertions(+), 5 deletions(-)",
			TestOutput:   "ok  github.com/yusefmosiah/fase/internal/store",
			CheckerNotes: "Implementation is clean, all edge cases handled.",
		},
	}

	html := BuildAttestationEmail(work, attestation, cr)

	if !contains(html, "42 passed") {
		t.Error("BuildAttestationEmailWithCheckRecord: missing test count")
	}
	if !contains(html, "3 files changed") {
		t.Error("BuildAttestationEmailWithCheckRecord: missing diff stat")
	}
	if !contains(html, "Implementation is clean") {
		t.Error("BuildAttestationEmailWithCheckRecord: missing checker notes")
	}
	if !contains(html, "Build OK") {
		t.Error("BuildAttestationEmailWithCheckRecord: missing build status")
	}
}

func TestBuildCheckReportEmailIncludesCanonicalProofBundleReferences(t *testing.T) {
	bundle := ProofBundle{
		Work: core.WorkItemRecord{
			WorkID:         "work_bundle01",
			Title:          "Bundle email",
			Objective:      "Render proof bundle references",
			Kind:           "implement",
			ExecutionState: core.WorkExecutionStateDone,
			ApprovalState:  core.WorkApprovalStateVerified,
		},
		CheckRecords: []core.CheckRecord{{
			CheckID:      "chk_bundle01",
			Result:       "pass",
			CheckerModel: "claude-sonnet-4-6",
		}},
		Attestations: []core.AttestationRecord{{
			AttestationID: "att_bundle01",
			Result:        "passed",
			VerifierKind:  "deterministic",
			ArtifactID:    "art_bundle01",
		}},
		Artifacts: []core.ArtifactRecord{{
			ArtifactID: "art_bundle01",
			Kind:       "check_output",
			Path:       "/tmp/check.txt",
		}},
		Docs: []core.DocContentRecord{{
			DocID:          "doc_bundle01",
			Path:           "docs/spec-check-flow.md",
			RepoFileExists: true,
			MatchesRepo:    true,
		}},
	}
	cr := core.CheckRecord{
		CheckID:      "chk_bundle01",
		Result:       "pass",
		CheckerModel: "claude-sonnet-4-6",
		Report: core.CheckReport{
			BuildOK:      true,
			TestsPassed:  2,
			TestOutput:   "go test ./internal/notify",
			CheckerNotes: "verified bundle references",
		},
	}

	html := BuildCheckReportEmail(bundle, cr)
	for _, want := range []string{"Check ID", "chk_bundle01", "att_bundle01", "art_bundle01", "doc_bundle01", "Canonical Proof Bundle"} {
		if !contains(html, want) {
			t.Fatalf("BuildCheckReportEmail: missing %q in output:\n%s", want, html)
		}
	}
}

func TestBuildSpecEscalationEmailIncludesCanonicalProofBundleReferences(t *testing.T) {
	bundle := ProofBundle{
		Work: core.WorkItemRecord{
			WorkID:         "work_escalate01",
			Title:          "Escalation email",
			Objective:      "Render escalation bundle references",
			Kind:           "implement",
			ExecutionState: core.WorkExecutionStateFailed,
			ApprovalState:  core.WorkApprovalStateRejected,
		},
		CheckRecords: []core.CheckRecord{{
			CheckID:      "chk_escalate01",
			Result:       "fail",
			CheckerModel: "claude-opus-4-6",
		}},
		Docs: []core.DocContentRecord{{
			DocID:   "doc_escalate01",
			Path:    "docs/spec-check-flow.md",
			Title:   "Check Flow Spec",
			Version: 1,
		}},
	}

	html := BuildSpecEscalationEmail(bundle, "Still failing targeted tests.", "Clarify the contract.")
	for _, want := range []string{"chk_escalate01", "doc_escalate01", "Canonical Proof Bundle", "Clarify the contract."} {
		if !contains(html, want) {
			t.Fatalf("BuildSpecEscalationEmail: missing %q in output:\n%s", want, html)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || substring(s, substr))
}

func substring(s, substr string) bool {
	for i := 0; i < len(s)-len(substr)+1; i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
