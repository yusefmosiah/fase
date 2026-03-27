package service

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/yusefmosiah/cogent/internal/core"
	"github.com/yusefmosiah/cogent/internal/notify"
)

func (s *Service) sendAttestationNotification(_ context.Context, work core.WorkItemRecord, attestation core.AttestationRecord) {
	// Skip internal work items (attest children, cleanup tasks).
	if strings.EqualFold(work.Kind, "attest") || strings.EqualFold(work.Kind, "task") {
		return
	}
	event := "check_fail"
	if attestation.Result == "passed" {
		event = "check_pass"
	}
	s.DigestCollector.Collect(digestItemForWork(work, event, firstNonEmpty(attestation.Summary, formatAttestationDigestSummary(attestation), work.Objective)))
}

// sendWorkFailureNotification collects a "failed" event into the digest.
func (s *Service) sendWorkFailureNotification(_ context.Context, work core.WorkItemRecord, message string) {
	s.DigestCollector.Collect(digestItemForWork(work, "failed", firstNonEmpty(message, work.Objective, "Work failed.")))
}

func digestItemForWork(work core.WorkItemRecord, event, summary string) notify.DigestItem {
	return notify.DigestItem{
		Time:      time.Now(),
		WorkID:    work.WorkID,
		Title:     work.Title,
		Objective: work.Objective,
		Event:     event,
		Summary:   strings.TrimSpace(summary),
	}
}

func formatAttestationDigestSummary(attestation core.AttestationRecord) string {
	result := strings.TrimSpace(attestation.Result)
	if result == "" {
		result = "updated"
	}
	summary := fmt.Sprintf("Attestation %s", result)
	if verifier := strings.TrimSpace(attestation.VerifierKind); verifier != "" {
		summary += " by " + verifier
	}
	if method := strings.TrimSpace(attestation.Method); method != "" {
		summary += " via " + method
	}
	return summary + "."
}

// SendSpecEscalationEmail emails the human when a work item has failed checks 3+ times.
func (s *Service) SendSpecEscalationEmail(ctx context.Context, workID, summary, recommendation string) {
	apiKey := os.Getenv("RESEND_API_KEY")
	to := os.Getenv("EMAIL_TO")
	if apiKey == "" || to == "" {
		return
	}
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return
	}
	subject := fmt.Sprintf("[Cogent] spec question: %s", work.Title)
	html := notify.BuildSpecEscalationEmail(s.notificationProofBundle(ctx, work), summary, recommendation)
	notify.SendEmail(ctx, apiKey, to, subject, html, nil)
}

// FlushDigest sends the accumulated digest email if there are any collected events.
// Called periodically by the housekeeping timer.
func (s *Service) FlushDigest(ctx context.Context) {
	s.DigestCollector.Flush(ctx)
}
func (s *Service) notificationProofBundle(ctx context.Context, work core.WorkItemRecord) notify.ProofBundle {
	result, err := s.Work(ctx, work.WorkID)
	if err != nil {
		log.Printf("debug: notificationProofBundle fallback for work %s: %v", work.WorkID, err)
		return notify.ProofBundle{Work: work}
	}
	return notify.ProofBundle{
		Work:         result.Work,
		CheckRecords: result.CheckRecords,
		Attestations: result.Attestations,
		Artifacts:    result.Artifacts,
		Docs:         result.Docs,
	}
}

func formatProofBundleRefs(bundle notify.ProofBundle) string {
	parts := []string{
		fmt.Sprintf("work=%s", bundle.Work.WorkID),
		fmt.Sprintf("state=%s", bundle.Work.ExecutionState),
		fmt.Sprintf("approval=%s", bundle.Work.ApprovalState),
	}
	if refs := checkRefs(bundle.CheckRecords); len(refs) > 0 {
		parts = append(parts, "checks="+strings.Join(refs, ","))
	}
	if refs := proofBundleAttestationRefs(bundle.Attestations); len(refs) > 0 {
		parts = append(parts, "attestations="+strings.Join(refs, ","))
	}
	if refs := proofBundleArtifactRefs(bundle.Artifacts); len(refs) > 0 {
		parts = append(parts, "artifacts="+strings.Join(refs, ","))
	}
	if refs := proofBundleDocRefs(bundle.Docs); len(refs) > 0 {
		parts = append(parts, "docs="+strings.Join(refs, ","))
	}
	return strings.Join(parts, " ")
}

func checkRefs(records []core.CheckRecord) []string {
	if len(records) == 0 {
		return nil
	}
	limit := min(3, len(records))
	refs := make([]string, 0, limit)
	for _, record := range records[:limit] {
		refs = append(refs, fmt.Sprintf("%s(%s)", record.CheckID, record.Result))
	}
	return refs
}

func proofBundleAttestationRefs(records []core.AttestationRecord) []string {
	if len(records) == 0 {
		return nil
	}
	limit := min(3, len(records))
	refs := make([]string, 0, limit)
	for _, record := range records[:limit] {
		ref := fmt.Sprintf("%s(%s)", record.AttestationID, record.Result)
		if record.ArtifactID != "" {
			ref += ":" + record.ArtifactID
		}
		refs = append(refs, ref)
	}
	return refs
}

func proofBundleArtifactRefs(records []core.ArtifactRecord) []string {
	if len(records) == 0 {
		return nil
	}
	limit := min(3, len(records))
	refs := make([]string, 0, limit)
	for _, record := range records[:limit] {
		refs = append(refs, fmt.Sprintf("%s(%s)", record.ArtifactID, record.Kind))
	}
	return refs
}

func proofBundleDocRefs(records []core.DocContentRecord) []string {
	if len(records) == 0 {
		return nil
	}
	limit := min(3, len(records))
	refs := make([]string, 0, limit)
	for _, record := range records[:limit] {
		refs = append(refs, fmt.Sprintf("%s(%s)", record.DocID, record.Path))
	}
	return refs
}
