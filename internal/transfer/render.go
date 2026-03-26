package transfer

import (
	"fmt"
	"strings"

	"github.com/yusefmosiah/cogent/internal/core"
)

func RenderPrompt(targetAdapter string, packet core.TransferPacket) string {
	var b strings.Builder

	fmt.Fprintf(&b, "You are receiving a fase context transfer from %s into %s.\n\n", packet.Source.Adapter, targetAdapter)
	if packet.Disclaimer != "" {
		fmt.Fprintf(&b, "%s\n\n", packet.Disclaimer)
	}

	b.WriteString("Transfer metadata:\n")
	fmt.Fprintf(&b, "- mode: %s\n", packet.Mode)
	if packet.Reason != "" {
		fmt.Fprintf(&b, "- reason: %s\n", packet.Reason)
	}
	fmt.Fprintf(&b, "- source adapter: %s\n", packet.Source.Adapter)
	if packet.Source.Model != "" {
		fmt.Fprintf(&b, "- source model: %s\n", packet.Source.Model)
	}
	if packet.Source.SessionID != "" {
		fmt.Fprintf(&b, "- source session: %s\n", packet.Source.SessionID)
	}
	if packet.Source.JobID != "" {
		fmt.Fprintf(&b, "- source job: %s\n", packet.Source.JobID)
	}
	if packet.Source.CWD != "" {
		fmt.Fprintf(&b, "- source cwd: %s\n", packet.Source.CWD)
	}
	b.WriteString("\n")

	if packet.Objective != "" {
		fmt.Fprintf(&b, "Objective:\n%s\n\n", packet.Objective)
	}
	if packet.Summary != "" {
		fmt.Fprintf(&b, "Summary:\n%s\n\n", packet.Summary)
	}
	if len(packet.Unresolved) > 0 {
		b.WriteString("Unresolved:\n")
		for _, item := range packet.Unresolved {
			fmt.Fprintf(&b, "- %s\n", item)
		}
		b.WriteString("\n")
	}
	if len(packet.ImportantFiles) > 0 {
		b.WriteString("Important files:\n")
		for _, path := range packet.ImportantFiles {
			fmt.Fprintf(&b, "- %s\n", path)
		}
		b.WriteString("\n")
	}
	if len(packet.RecentTurnsInline) > 0 {
		b.WriteString("Recent turns (inline excerpt):\n")
		for _, turn := range packet.RecentTurnsInline {
			fmt.Fprintf(&b, "- [%s] input=%q summary=%q\n", turn.Adapter, turn.InputText, turn.ResultSummary)
		}
		b.WriteString("\n")
	}
	if len(packet.EvidenceRefs) > 0 {
		b.WriteString("Evidence references:\n")
		for _, ref := range packet.EvidenceRefs {
			fmt.Fprintf(&b, "- %s: %s\n", ref.Kind, ref.Path)
		}
		b.WriteString("\n")
	}
	if len(packet.Constraints) > 0 {
		b.WriteString("Constraints:\n")
		for _, item := range packet.Constraints {
			fmt.Fprintf(&b, "- %s\n", item)
		}
		b.WriteString("\n")
	}
	if len(packet.RecommendedNextSteps) > 0 {
		b.WriteString("Recommended next steps:\n")
		for _, item := range packet.RecommendedNextSteps {
			fmt.Fprintf(&b, "- %s\n", item)
		}
		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String())
}
