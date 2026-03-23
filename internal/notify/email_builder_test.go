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
