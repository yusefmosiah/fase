package notify

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yusefmosiah/fase/internal/core"
)

// BuildWorkCompletionEmail builds an HTML email for work item completion or failure.
func BuildWorkCompletionEmail(work *core.WorkItemRecord, message string, attestations []core.AttestationRecord, isSuccess bool) string {
	status := "FAILED"
	statusColor := "#dc2626"
	if isSuccess {
		status = "COMPLETED"
		statusColor = "#16a34a"
	}

	attestationSummary := ""
	if len(attestations) > 0 {
		attestationSummary = "<h3>Attestations</h3><ul>"
		for _, att := range attestations {
			result := att.Result
			if result == "" {
				result = "unknown"
			}
			attestationSummary += fmt.Sprintf(
				"<li><strong>%s</strong>: %s (verifier: %s)</li>",
				att.Method, result, att.VerifierKind,
			)
		}
		attestationSummary += "</ul>"
	}

	updateMessage := ""
	if message != "" {
		updateMessage = fmt.Sprintf("<h3>Update Message</h3><p>%s</p>", escapeHTML(message))
	}

	html := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<style>
		body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; line-height: 1.6; color: #333; margin: 0; padding: 0; }
		.container { max-width: 600px; margin: 0 auto; padding: 20px; }
		.header { background: %s; color: white; padding: 20px; border-radius: 8px 8px 0 0; }
		.header h1 { margin: 0; font-size: 24px; }
		.header p { margin: 8px 0 0 0; opacity: 0.9; }
		.content { background: #f9fafb; padding: 20px; border-radius: 0 0 8px 8px; }
		.content h3 { color: #1f2937; margin-top: 20px; margin-bottom: 10px; }
		.content p { margin: 0 0 15px 0; }
		.content ul { margin: 0; padding-left: 20px; }
		.content li { margin-bottom: 8px; }
		.metadata { background: #ffffff; padding: 15px; border-left: 4px solid %s; margin: 15px 0; border-radius: 4px; font-size: 13px; }
		.footer { text-align: center; margin-top: 20px; padding-top: 20px; border-top: 1px solid #e5e7eb; font-size: 12px; color: #6b7280; }
		code { background: #f3f4f6; padding: 2px 6px; border-radius: 3px; font-family: "Monaco", "Courier New", monospace; }
	</style>
</head>
<body>
	<div class="container">
		<div class="header">
			<h1>Work Item %s</h1>
			<p>%s</p>
		</div>
		<div class="content">
			<div class="metadata">
				<div><strong>Work ID:</strong> <code>%s</code></div>
				<div><strong>Title:</strong> %s</div>
				<div><strong>Kind:</strong> %s</div>
				<div><strong>Timestamp:</strong> %s</div>
			</div>

			<h3>Objective</h3>
			<p>%s</p>

			%s

			%s

			%s
		</div>
		<div class="footer">
			<p>This is an automated notification from FASE work management system.</p>
		</div>
	</div>
</body>
</html>
	`,
		statusColor,
		statusColor,
		status,
		work.Title,
		work.WorkID,
		escapeHTML(work.Title),
		work.Kind,
		work.UpdatedAt.Format(time.RFC3339),
		escapeHTML(work.Objective),
		updateMessage,
		attestationSummary,
		buildMetadataSection(work),
	)

	return strings.TrimSpace(html)
}

// BuildCheckReportEmail renders a CheckReport as an HTML email body for work completion.
// Subject should be "[FASE] done: <title>".
func BuildCheckReportEmail(work *core.WorkItemRecord, cr core.CheckRecord) string {
	testStatus := "✓ All tests passed"
	testColor := "#16a34a"
	if cr.Report.TestsFailed > 0 {
		testStatus = fmt.Sprintf("✗ %d failed, %d passed", cr.Report.TestsFailed, cr.Report.TestsPassed)
		testColor = "#dc2626"
	} else if cr.Report.TestsPassed > 0 {
		testStatus = fmt.Sprintf("✓ %d passed", cr.Report.TestsPassed)
	}

	buildStatus := "✓ Build OK"
	buildColor := "#16a34a"
	if !cr.Report.BuildOK {
		buildStatus = "✗ Build failed"
		buildColor = "#dc2626"
	}

	diffStatSection := ""
	if cr.Report.DiffStat != "" {
		diffStatSection = fmt.Sprintf(`<h3>Changes</h3><pre style="background:#f3f4f6;padding:10px;border-radius:4px;font-size:12px;overflow-x:auto">%s</pre>`, escapeHTML(cr.Report.DiffStat))
	}

	testOutputSection := ""
	if cr.Report.TestOutput != "" {
		out := cr.Report.TestOutput
		if len(out) > 4096 {
			out = out[:4096] + "\n... (truncated)"
		}
		testOutputSection = fmt.Sprintf(`<h3>Test Output</h3><pre style="background:#f3f4f6;padding:10px;border-radius:4px;font-size:11px;overflow-x:auto;max-height:300px">%s</pre>`, escapeHTML(out))
	}

	checkerNotesSection := ""
	if cr.Report.CheckerNotes != "" {
		checkerNotesSection = fmt.Sprintf(`<h3>Checker Notes</h3><p style="white-space:pre-wrap">%s</p>`, escapeHTML(cr.Report.CheckerNotes))
	}

	screenshotsSection := buildInlineScreenshots(cr.Report.Screenshots)

	return strings.TrimSpace(fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<style>
		body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; line-height: 1.6; color: #333; margin: 0; padding: 0; }
		.container { max-width: 700px; margin: 0 auto; padding: 20px; }
		.header { background: #16a34a; color: white; padding: 20px; border-radius: 8px 8px 0 0; }
		.header h1 { margin: 0; font-size: 24px; }
		.header p { margin: 8px 0 0 0; opacity: 0.9; }
		.content { background: #f9fafb; padding: 20px; border-radius: 0 0 8px 8px; }
		.content h3 { color: #1f2937; margin-top: 20px; margin-bottom: 10px; }
		.metadata { background: #ffffff; padding: 15px; border-left: 4px solid #16a34a; margin: 15px 0; border-radius: 4px; font-size: 13px; }
		.stat { display: inline-block; margin-right: 20px; }
		.footer { text-align: center; margin-top: 20px; padding-top: 20px; border-top: 1px solid #e5e7eb; font-size: 12px; color: #6b7280; }
		code { background: #f3f4f6; padding: 2px 6px; border-radius: 3px; font-family: "Monaco", "Courier New", monospace; }
		img { max-width: 100%%; border-radius: 4px; margin: 8px 0; }
	</style>
</head>
<body>
	<div class="container">
		<div class="header">
			<h1>✓ Work Complete</h1>
			<p>%s</p>
		</div>
		<div class="content">
			<div class="metadata">
				<div><strong>Work ID:</strong> <code>%s</code></div>
				<div><strong>Kind:</strong> %s</div>
				<div><strong>Completed:</strong> %s</div>
				<div><strong>Checker:</strong> %s</div>
			</div>

			<h3>Verification Results</h3>
			<p>
				<span class="stat"><strong style="color:%s">%s</strong></span>
				<span class="stat"><strong style="color:%s">%s</strong></span>
			</p>

			%s
			%s
			%s
			%s
		</div>
		<div class="footer">
			<p>This is an automated notification from FASE work management system.</p>
		</div>
	</div>
</body>
</html>`,
		escapeHTML(work.Title),
		work.WorkID,
		work.Kind,
		cr.CreatedAt.Format(time.RFC3339),
		escapeHTML(cr.CheckerModel),
		buildColor, buildStatus,
		testColor, testStatus,
		diffStatSection,
		testOutputSection,
		checkerNotesSection,
		screenshotsSection,
	))
}

// buildInlineScreenshots creates an HTML section with inline base64-encoded screenshots.
func buildInlineScreenshots(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<h3>Screenshots</h3>")
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		ext := strings.ToLower(filepath.Ext(p))
		contentType := "image/png"
		if ext == ".jpg" || ext == ".jpeg" {
			contentType = "image/jpeg"
		}
		b64 := base64.StdEncoding.EncodeToString(data)
		fmt.Fprintf(&b, `<div><p style="font-size:12px;color:#6b7280">%s</p><img src="data:%s;base64,%s" alt="%s"></div>`,
			escapeHTML(filepath.Base(p)), contentType, b64, escapeHTML(filepath.Base(p)))
	}
	return b.String()
}

// BuildSpecEscalationEmail builds an HTML email for spec escalation after 3+ failed checks.
func BuildSpecEscalationEmail(work *core.WorkItemRecord, checkRecords []core.CheckRecord, summary, recommendation string) string {
	checksSection := ""
	if len(checkRecords) > 0 {
		checksSection = "<h3>Check History</h3><ul>"
		for _, cr := range checkRecords {
			icon := "✓"
			color := "#16a34a"
			if cr.Result == "fail" {
				icon = "✗"
				color = "#dc2626"
			}
			note := cr.Report.CheckerNotes
			if len(note) > 200 {
				note = note[:200] + "..."
			}
			checksSection += fmt.Sprintf(`<li><strong style="color:%s">%s %s</strong> (%s)`,
				color, icon, cr.Result, cr.CreatedAt.Format("2006-01-02 15:04"))
			if note != "" {
				checksSection += fmt.Sprintf(`: %s`, escapeHTML(note))
			}
			checksSection += "</li>"
		}
		checksSection += "</ul>"
	}

	summarySection := ""
	if summary != "" {
		summarySection = fmt.Sprintf(`<h3>What Keeps Going Wrong</h3><p style="white-space:pre-wrap">%s</p>`, escapeHTML(summary))
	}

	recommendationSection := ""
	if recommendation != "" {
		recommendationSection = fmt.Sprintf(`<h3>Recommendation</h3><p style="white-space:pre-wrap">%s</p>`, escapeHTML(recommendation))
	}

	return strings.TrimSpace(fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<style>
		body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; line-height: 1.6; color: #333; margin: 0; padding: 0; }
		.container { max-width: 700px; margin: 0 auto; padding: 20px; }
		.header { background: #d97706; color: white; padding: 20px; border-radius: 8px 8px 0 0; }
		.header h1 { margin: 0; font-size: 24px; }
		.header p { margin: 8px 0 0 0; opacity: 0.9; }
		.content { background: #f9fafb; padding: 20px; border-radius: 0 0 8px 8px; }
		.content h3 { color: #1f2937; margin-top: 20px; margin-bottom: 10px; }
		.metadata { background: #ffffff; padding: 15px; border-left: 4px solid #d97706; margin: 15px 0; border-radius: 4px; font-size: 13px; }
		.footer { text-align: center; margin-top: 20px; padding-top: 20px; border-top: 1px solid #e5e7eb; font-size: 12px; color: #6b7280; }
		code { background: #f3f4f6; padding: 2px 6px; border-radius: 3px; font-family: "Monaco", "Courier New", monospace; }
	</style>
</head>
<body>
	<div class="container">
		<div class="header">
			<h1>⚠ Spec Question</h1>
			<p>%s</p>
		</div>
		<div class="content">
			<div class="metadata">
				<div><strong>Work ID:</strong> <code>%s</code></div>
				<div><strong>Kind:</strong> %s</div>
				<div><strong>Failed checks:</strong> %d</div>
			</div>

			<p>This item has failed verification %d time(s). The spec may need to change.</p>

			%s
			%s
			%s
		</div>
		<div class="footer">
			<p>This is an automated notification from FASE work management system.</p>
		</div>
	</div>
</body>
</html>`,
		escapeHTML(work.Title),
		work.WorkID,
		work.Kind,
		len(checkRecords),
		len(checkRecords),
		checksSection,
		summarySection,
		recommendationSection,
	))
}

// BuildAttestationEmail builds an HTML email for attestation events (passed or failed).
// It includes the attestation result, verifier details, check report summary, and screenshots.
func BuildAttestationEmail(work *core.WorkItemRecord, attestation core.AttestationRecord, checkRecord *core.CheckRecord) string {
	isPassed := attestation.Result == "passed"
	statusLabel := "FAILED"
	statusColor := "#dc2626"
	icon := "✗"
	if isPassed {
		statusLabel = "PASSED"
		statusColor = "#16a34a"
		icon = "✓"
	}

	summarySection := ""
	if attestation.Summary != "" {
		summarySection = fmt.Sprintf(`<h3>Attestation Summary</h3><p style="white-space:pre-wrap">%s</p>`, escapeHTML(attestation.Summary))
	}

	verifierIdentity := attestation.VerifierIdentity
	if verifierIdentity == "" {
		verifierIdentity = attestation.CreatedBy
	}

	verifierSection := fmt.Sprintf(`
		<div class="metadata">
			<div><strong>Result:</strong> <span style="color:%s">%s %s</span></div>
			<div><strong>Verifier:</strong> %s</div>
			<div><strong>Method:</strong> %s</div>
			<div><strong>Work ID:</strong> <code>%s</code></div>
			<div><strong>Kind:</strong> %s</div>
			<div><strong>Attested:</strong> %s</div>
		</div>`,
		statusColor, icon, statusLabel,
		escapeHTML(attestation.VerifierKind),
		escapeHTML(attestation.Method),
		work.WorkID,
		work.Kind,
		attestation.CreatedAt.Format(time.RFC3339),
	)

	testStatusSection := ""
	diffStatSection := ""
	testOutputSection := ""
	checkerNotesSection := ""
	screenshotsSection := ""

	if checkRecord != nil {
		buildStatus := "✓ Build OK"
		buildColor := "#16a34a"
		if !checkRecord.Report.BuildOK {
			buildStatus = "✗ Build failed"
			buildColor = "#dc2626"
		}

		testStatus := "✓ All tests passed"
		testColor := "#16a34a"
		if checkRecord.Report.TestsFailed > 0 {
			testStatus = fmt.Sprintf("✗ %d failed, %d passed", checkRecord.Report.TestsFailed, checkRecord.Report.TestsPassed)
			testColor = "#dc2626"
		} else if checkRecord.Report.TestsPassed > 0 {
			testStatus = fmt.Sprintf("✓ %d passed", checkRecord.Report.TestsPassed)
		}

		testStatusSection = fmt.Sprintf(`
		<h3>Test Results</h3>
		<p>
			<span class="stat"><strong style="color:%s">%s</strong></span>
			<span class="stat"><strong style="color:%s">%s</strong></span>
		</p>`, buildColor, buildStatus, testColor, testStatus)

		if checkRecord.Report.DiffStat != "" {
			diffStatSection = fmt.Sprintf(`<h3>Changes (git diff --stat)</h3><pre style="background:#f3f4f6;padding:10px;border-radius:4px;font-size:12px;overflow-x:auto">%s</pre>`, escapeHTML(checkRecord.Report.DiffStat))
		}

		if checkRecord.Report.TestOutput != "" {
			out := checkRecord.Report.TestOutput
			if len(out) > 4096 {
				out = out[:4096] + "\n... (truncated)"
			}
			testOutputSection = fmt.Sprintf(`<h3>Test Output</h3><pre style="background:#f3f4f6;padding:10px;border-radius:4px;font-size:11px;overflow-x:auto;max-height:300px">%s</pre>`, escapeHTML(out))
		}

		if checkRecord.Report.CheckerNotes != "" {
			checkerNotesSection = fmt.Sprintf(`<h3>Checker Notes</h3><p style="white-space:pre-wrap">%s</p>`, escapeHTML(checkRecord.Report.CheckerNotes))
		}

		screenshotsSection = buildInlineScreenshots(checkRecord.Report.Screenshots)
	}

	return strings.TrimSpace(fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<style>
		body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; line-height: 1.6; color: #333; margin: 0; padding: 0; }
		.container { max-width: 700px; margin: 0 auto; padding: 20px; }
		.header { background: %s; color: white; padding: 20px; border-radius: 8px 8px 0 0; }
		.header h1 { margin: 0; font-size: 24px; }
		.header p { margin: 8px 0 0 0; opacity: 0.9; }
		.content { background: #f9fafb; padding: 20px; border-radius: 0 0 8px 8px; }
		.content h3 { color: #1f2937; margin-top: 20px; margin-bottom: 10px; }
		.metadata { background: #ffffff; padding: 15px; border-left: 4px solid %s; margin: 15px 0; border-radius: 4px; font-size: 13px; }
		.stat { display: inline-block; margin-right: 20px; }
		.footer { text-align: center; margin-top: 20px; padding-top: 20px; border-top: 1px solid #e5e7eb; font-size: 12px; color: #6b7280; }
		code { background: #f3f4f6; padding: 2px 6px; border-radius: 3px; font-family: "Monaco", "Courier New", monospace; }
		img { max-width: 100%%; border-radius: 4px; margin: 8px 0; }
	</style>
</head>
<body>
	<div class="container">
		<div class="header">
			<h1>%s Attestation %s</h1>
			<p>%s</p>
		</div>
		<div class="content">
			%s
			<h3>Objective</h3>
			<p>%s</p>
			%s
			%s
			%s
			%s
			%s
			%s
		</div>
		<div class="footer">
			<p>This is an automated notification from FASE work management system.</p>
		</div>
	</div>
</body>
</html>`,
		statusColor,
		statusColor,
		icon, statusLabel,
		escapeHTML(work.Title),
		verifierSection,
		escapeHTML(work.Objective),
		summarySection,
		testStatusSection,
		diffStatSection,
		testOutputSection,
		checkerNotesSection,
		screenshotsSection,
	))
}

func escapeHTML(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(s)
}

func buildMetadataSection(work *core.WorkItemRecord) string {
	if work.Metadata == nil || len(work.Metadata) == 0 {
		return ""
	}

	html := "<h3>Metadata</h3><ul>"
	for k, v := range work.Metadata {
		// Skip internal keys
		if strings.HasPrefix(k, "_") {
			continue
		}
		html += fmt.Sprintf("<li><strong>%s:</strong> %v</li>", escapeHTML(k), escapeHTML(fmt.Sprintf("%v", v)))
	}
	html += "</ul>"
	return html
}
