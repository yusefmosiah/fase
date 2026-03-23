package notify

import (
	"fmt"
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
