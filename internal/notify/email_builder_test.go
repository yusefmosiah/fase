package notify

import (
	"testing"

	"github.com/yusefmosiah/fase/internal/core"
)

func TestBuildWorkCompletionEmailSuccess(t *testing.T) {
	work := &core.WorkItemRecord{
		WorkID:    "work_test123",
		Title:     "Test Task: Implement Email Notification",
		Objective: "Add email notifications via Resend API for work item completion",
		Kind:      "implement",
		Metadata: map[string]any{
			"result": "passed",
		},
	}
	message := "Successfully implemented email notifications. All tests passing."
	attestations := []core.AttestationRecord{
		{
			Method:       "manual",
			VerifierKind: "supervisor",
			Result:       "passed",
		},
	}

	html := BuildWorkCompletionEmail(work, message, attestations, true)

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
}

func TestBuildWorkCompletionEmailFailure(t *testing.T) {
	work := &core.WorkItemRecord{
		WorkID:    "work_test456",
		Title:     "Test Task: Failing Job",
		Objective: "Test email for failures",
		Kind:      "implement",
	}
	message := "Task failed: database connection timeout"
	attestations := []core.AttestationRecord{}

	html := BuildWorkCompletionEmail(work, message, attestations, false)

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
}

func TestHTMLEscaping(t *testing.T) {
	work := &core.WorkItemRecord{
		WorkID:    "work_test",
		Title:     "Test Alert XSS",
		Objective: "Test & verify escaping of HTML special chars <tag>",
		Kind:      "implement",
	}

	html := BuildWorkCompletionEmail(work, "", []core.AttestationRecord{}, true)

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
