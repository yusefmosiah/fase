package debrief

import (
	"fmt"
	"strings"

	"github.com/yusefmosiah/fase/internal/core"
)

func RenderPrompt(session core.SessionRecord, adapter, reason string) string {
	var b strings.Builder

	b.WriteString("You are writing a fase debrief for your current session.\n")
	b.WriteString("Do not continue working, do not make more changes, and do not run more tools.\n")
	b.WriteString("Your job is to land the plane and export your current world model for a host agent.\n\n")

	b.WriteString("Context:\n")
	fmt.Fprintf(&b, "- canonical session: %s\n", session.SessionID)
	fmt.Fprintf(&b, "- adapter: %s\n", adapter)
	if session.CWD != "" {
		fmt.Fprintf(&b, "- working directory: %s\n", session.CWD)
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		fmt.Fprintf(&b, "- requested focus: %s\n", reason)
	}
	b.WriteString("\n")

	b.WriteString("Return concise Markdown with these exact headings:\n")
	b.WriteString("# Objective\n")
	b.WriteString("# Completed\n")
	b.WriteString("# Current State\n")
	b.WriteString("# Outstanding Work\n")
	b.WriteString("# Risks\n")
	b.WriteString("# Important Files\n")
	b.WriteString("# Recommended Next Step\n\n")

	b.WriteString("Requirements:\n")
	b.WriteString("- Base the report on the existing session state.\n")
	b.WriteString("- Prefer concrete facts over speculation.\n")
	b.WriteString("- Include one absolute path per line under Important Files when possible.\n")
	b.WriteString("- If something is uncertain, say so explicitly.\n")

	return strings.TrimSpace(b.String())
}
