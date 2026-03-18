package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/yusefmosiah/cagent/internal/core"
)

type WorkVerifyResult struct {
	Work         core.WorkItemRecord           `json:"work"`
	Commit       WorkVerifyCommitResult        `json:"commit,omitempty"`
	Attestations []WorkVerifyAttestationResult `json:"attestations,omitempty"`
	Verdict      string                        `json:"verdict"`
	Issues       []string                      `json:"issues,omitempty"`
	VerifiedAt   time.Time                     `json:"verified_at"`
}

type WorkVerifyCommitResult struct {
	OID    string `json:"oid,omitempty"`
	Repo   string `json:"repo,omitempty"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type WorkVerifyAttestationResult struct {
	AttestationID   string `json:"attestation_id"`
	Result          string `json:"result"`
	Method          string `json:"method,omitempty"`
	VerifierKind    string `json:"verifier_kind,omitempty"`
	SignerPubkey    string `json:"signer_pubkey,omitempty"`
	SignatureStatus string `json:"signature_status"`
	SignerStatus    string `json:"signer_status"`
	MetadataStatus  string `json:"metadata_status,omitempty"`
}

func (s *Service) VerifyWork(ctx context.Context, workID string) (*WorkVerifyResult, error) {
	if strings.TrimSpace(workID) == "" {
		return nil, fmt.Errorf("%w: work id must not be empty", ErrInvalidInput)
	}

	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}

	jobs, err := s.store.ListJobsByWork(ctx, workID, 200)
	if err != nil {
		return nil, err
	}
	attestations, err := s.store.ListAttestationRecords(ctx, "work", workID, 200)
	if err != nil {
		return nil, err
	}

	report := &WorkVerifyResult{
		Work:       work,
		VerifiedAt: time.Now().UTC(),
	}

	if work.HeadCommitOID != "" {
		repoPath := verifyRepoPath(jobs)
		report.Commit = verifyCommit(ctx, repoPath, work.HeadCommitOID)
		if report.Commit.Status != "valid" {
			report.Issues = append(report.Issues, fmt.Sprintf("commit %s: %s", work.HeadCommitOID, report.Commit.Status))
		}
	}

	sawSignatureData := false
	sawMissingSignature := false
	for _, attestation := range attestations {
		item := WorkVerifyAttestationResult{
			AttestationID:   attestation.AttestationID,
			Result:          attestation.Result,
			Method:          attestation.Method,
			VerifierKind:    attestation.VerifierKind,
			SignerPubkey:    attestation.SignerPubkey,
			SignatureStatus: "missing",
			SignerStatus:    "missing",
		}
		if strings.TrimSpace(attestation.SignerPubkey) != "" {
			item.SignerStatus = "present"
		}
		if strings.TrimSpace(attestation.Signature) != "" {
			item.SignatureStatus = "present"
		}
		if item.SignerStatus == "present" || item.SignatureStatus == "present" {
			sawSignatureData = true
		}
		if item.SignerStatus != "present" || item.SignatureStatus != "present" {
			sawMissingSignature = true
			report.Issues = append(report.Issues, fmt.Sprintf("attestation %s: signature fields incomplete", attestation.AttestationID))
		}
		report.Attestations = append(report.Attestations, item)
	}

	switch {
	case report.Commit.Status == "invalid":
		report.Verdict = "unverified"
	case sawSignatureData && !sawMissingSignature && report.Commit.Status == "valid":
		report.Verdict = "verified"
	case len(report.Issues) == 0:
		report.Verdict = "legacy"
	default:
		report.Verdict = "unverified"
	}

	return report, nil
}

func verifyRepoPath(jobs []core.JobRecord) string {
	for _, job := range jobs {
		if strings.TrimSpace(job.CWD) != "" {
			return job.CWD
		}
	}
	return "."
}

func verifyCommit(ctx context.Context, repoPath, oid string) WorkVerifyCommitResult {
	result := WorkVerifyCommitResult{
		OID:    oid,
		Repo:   repoPath,
		Status: "missing",
	}
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		repoPath = "."
	}
	if _, err := os.Stat(repoPath); err != nil {
		result.Status = "unverified"
		result.Detail = fmt.Sprintf("repository path unavailable: %v", err)
		return result
	}

	if out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--verify", oid+"^{commit}").CombinedOutput(); err != nil {
		result.Status = "invalid"
		result.Detail = strings.TrimSpace(string(out))
		if result.Detail == "" {
			result.Detail = err.Error()
		}
		return result
	}

	out, err := exec.CommandContext(ctx, "git", "-C", repoPath, "verify-commit", oid).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		lower := strings.ToLower(detail)
		switch {
		case strings.Contains(lower, "no signature"):
			result.Status = "unsigned"
		case strings.Contains(lower, "gpg failed to sign the data") || strings.Contains(lower, "bad signature"):
			result.Status = "invalid"
		default:
			result.Status = "unverified"
		}
		result.Detail = detail
		return result
	}

	result.Status = "valid"
	result.Detail = strings.TrimSpace(string(out))
	return result
}
