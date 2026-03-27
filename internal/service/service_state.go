package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yusefmosiah/cogent/internal/core"
)

func resolvedRequiredAttestations(work core.WorkItemRecord, explicit []core.RequiredAttestation) []core.RequiredAttestation {
	if len(explicit) > 0 {
		return copyRequiredAttestations(explicit)
	}
	if strings.EqualFold(work.Kind, "attest") {
		return []core.RequiredAttestation{}
	}
	return []core.RequiredAttestation{
		{
			VerifierKind: "attestation",
			Method:       "automated_review",
			Blocking:     true,
		},
	}
}

func metadataInt(metadata map[string]any, key string) (int, bool) {
	if metadata == nil {
		return 0, false
	}
	value, ok := metadata[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func shouldSetPendingApproval(work core.WorkItemRecord) bool {
	if work.ExecutionState != core.WorkExecutionStateDone {
		return false
	}
	if work.ApprovalState == core.WorkApprovalStateVerified || work.ApprovalState == core.WorkApprovalStateRejected {
		return false
	}
	for _, slot := range work.RequiredAttestations {
		if slot.Blocking {
			return true
		}
	}
	return false
}

func (s *Service) hasPassingCheckRecord(ctx context.Context, workID string) (bool, error) {
	checkRecords, err := s.store.ListCheckRecords(ctx, workID, 50)
	if err != nil {
		return false, err
	}
	for _, record := range checkRecords {
		if record.Result == "pass" {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) requiredDocIssues(ctx context.Context, work core.WorkItemRecord) ([]string, error) {
	if len(work.RequiredDocs) == 0 {
		return nil, nil
	}
	docs, err := s.GetDocContent(ctx, work.WorkID)
	if err != nil {
		return nil, err
	}
	docsByPath := make(map[string]core.DocContentRecord, len(docs))
	for _, doc := range docs {
		docsByPath[doc.Path] = doc
	}
	issues := make([]string, 0, len(work.RequiredDocs))
	for _, path := range work.RequiredDocs {
		doc, ok := docsByPath[path]
		if !ok {
			issues = append(issues, fmt.Sprintf("required doc %s is not tracked for this work", path))
			continue
		}
		if !doc.RepoFileExists {
			issues = append(issues, fmt.Sprintf("required doc %s is missing from the repo", path))
			continue
		}
		if !doc.MatchesRepo {
			issues = append(issues, fmt.Sprintf("required doc %s is stale or mismatched with the repo file", path))
		}
	}
	return issues, nil
}

func (s *Service) completionGateIssues(ctx context.Context, work core.WorkItemRecord) ([]string, error) {
	var issues []string
	hasPassingCheck, err := s.hasPassingCheckRecord(ctx, work.WorkID)
	if err != nil {
		return nil, err
	}
	if !hasPassingCheck {
		issues = append(issues, "missing passing check record")
	}

	docIssues, err := s.requiredDocIssues(ctx, work)
	if err != nil {
		return nil, err
	}
	issues = append(issues, docIssues...)
	return issues, nil
}

func workAttemptEpoch(work core.WorkItemRecord) int {
	if work.AttemptEpoch > 0 {
		return work.AttemptEpoch
	}
	if epoch, ok := metadataInt(work.Metadata, "attempt_epoch"); ok && epoch > 0 {
		return epoch
	}
	return 1
}

func attemptMetadataMatchesWork(work core.WorkItemRecord, metadata map[string]any) bool {
	expectedEpoch := workAttemptEpoch(work)
	if epoch, ok := metadataInt(metadata, "attempt_epoch"); ok {
		return epoch == expectedEpoch
	}
	currentNonce := summaryString(work.Metadata, "attestation_nonce")
	if currentNonce != "" {
		return summaryString(metadata, "attestation_nonce") == currentNonce
	}
	return expectedEpoch == 1
}

func matchesCurrentWorkAttempt(parent, child core.WorkItemRecord) bool {
	if child.AttemptEpoch > 0 {
		return child.AttemptEpoch == workAttemptEpoch(parent)
	}
	return attemptMetadataMatchesWork(parent, child.Metadata)
}

func blockingAttestationsSatisfied(work core.WorkItemRecord, attestations []core.AttestationRecord) bool {
	superseded := collectSupersededAttestations(attestations)
	for _, slot := range work.RequiredAttestations {
		if !slot.Blocking {
			continue
		}
		if !hasPassingAttestationForRequirement(work, slot, attestations, superseded) {
			return false
		}
	}
	return true
}

func pendingBlockingAttestationSlots(work core.WorkItemRecord, attestations []core.AttestationRecord) []core.RequiredAttestation {
	superseded := collectSupersededAttestations(attestations)
	var result []core.RequiredAttestation
	for _, slot := range work.RequiredAttestations {
		if hasPassingAttestationForRequirement(work, slot, attestations, superseded) {
			continue
		}
		result = append(result, slot)
	}
	return result
}

func matchesAnyPendingAttestationSlot(verifierKind, method string, slots []core.RequiredAttestation) bool {
	for _, slot := range slots {
		if attestationMatchesRequiredSlot(verifierKind, method, slot) {
			return true
		}
	}
	return false
}

func attestationMatchesRequiredSlot(verifierKind, method string, slot core.RequiredAttestation) bool {
	if slot.VerifierKind != "" && verifierKind != "" && verifierKind != slot.VerifierKind {
		return false
	}
	if slot.Method != "" && method != "" && method != slot.Method {
		return false
	}
	if slot.VerifierKind != "" && verifierKind == "" {
		return false
	}
	if slot.Method != "" && method == "" {
		return false
	}
	return true
}

func describeAttestationSlots(slots []core.RequiredAttestation) string {
	if len(slots) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(slots))
	for _, slot := range slots {
		verifier := strings.TrimSpace(slot.VerifierKind)
		if verifier == "" {
			verifier = "*"
		}
		method := strings.TrimSpace(slot.Method)
		if method == "" {
			method = "*"
		}
		parts = append(parts, fmt.Sprintf("%s/%s", verifier, method))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func collectSupersededAttestations(attestations []core.AttestationRecord) map[string]bool {
	superseded := make(map[string]bool, len(attestations))
	for _, attestation := range attestations {
		if attestation.SupersedesAttestationID != "" {
			superseded[attestation.SupersedesAttestationID] = true
		}
	}
	return superseded
}

func hasPassingAttestationForRequirement(work core.WorkItemRecord, slot core.RequiredAttestation, attestations []core.AttestationRecord, superseded map[string]bool) bool {
	for _, attestation := range attestations {
		if attestation.Result != "passed" {
			continue
		}
		if superseded[attestation.AttestationID] {
			continue
		}
		if !attemptMetadataMatchesWork(work, attestation.Metadata) {
			continue
		}
		if slot.VerifierKind != "" && attestation.VerifierKind != slot.VerifierKind {
			continue
		}
		if slot.Method != "" && attestation.Method != slot.Method {
			continue
		}
		if work.HeadCommitOID != "" {
			commitOID, _ := attestation.Metadata["commit_oid"].(string)
			if commitOID != work.HeadCommitOID {
				continue
			}
		}
		return true
	}
	return false
}
func emitForceDoneWarning(workID, actor string) {
	event := forceDoneWarningEvent{
		Level:     "warn",
		Kind:      "force_done_override",
		WorkID:    workID,
		Actor:     actor,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Message:   "guardDoneTransition bypassed via --force; attestation requirements not verified",
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, string(data))
}

// guardDoneTransition returns an error if the work item cannot transition to a
// terminal-success state (done, archived) because required completion evidence
// is still missing.
func (s *Service) guardDoneTransition(ctx context.Context, work core.WorkItemRecord) error {
	issues, err := s.completionGateIssues(ctx, work)
	if err != nil {
		return err
	}
	if len(issues) > 0 {
		return fmt.Errorf("%w: work item %s cannot transition to terminal success: %s",
			ErrInvalidInput, work.WorkID, strings.Join(issues, "; "))
	}
	return nil
}
