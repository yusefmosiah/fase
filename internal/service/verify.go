package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/yusefmosiah/cogent/internal/core"
)

type WorkVerifyResult struct {
	Work         core.WorkItemRecord           `json:"work"`
	Commit       WorkVerifyCommitResult        `json:"commit,omitempty"`
	Attestations []WorkVerifyAttestationResult `json:"attestations,omitempty"`
	Agents       []WorkVerifyAgentResult       `json:"agents,omitempty"`
	CATrust      *WorkVerifyCATrustResult      `json:"ca_trust,omitempty"`
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

type WorkVerifyAgentResult struct {
	JobID          string `json:"job_id"`
	Adapter        string `json:"adapter,omitempty"`
	TokenStatus    string `json:"token_status"`
	SignatureValid bool   `json:"signature_valid,omitempty"`
	Role           string `json:"role,omitempty"`
	Expiry         string `json:"expiry,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type WorkVerifyCATrustResult struct {
	TrustRoot string `json:"trust_root"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
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

	ca, caErr := core.EnsureCA(s.Paths.StateDir)
	if caErr != nil {
		report.CATrust = &WorkVerifyCATrustResult{
			Status: "unavailable",
			Reason: caErr.Error(),
		}
		report.Issues = append(report.Issues, "CA trust root unavailable: "+caErr.Error())
	} else {
		pubFingerprint := fmt.Sprintf("ed25519:%x", ca.PublicKey[:8])
		report.CATrust = &WorkVerifyCATrustResult{
			TrustRoot: pubFingerprint,
			Status:    "loaded",
		}
	}

	if work.HeadCommitOID != "" {
		repoPath := verifyRepoPath(jobs)
		report.Commit = verifyCommit(ctx, repoPath, work.HeadCommitOID)
		if report.Commit.Status != "valid" {
			report.Issues = append(report.Issues, fmt.Sprintf("commit %s: %s", work.HeadCommitOID, report.Commit.Status))
		}
	}

	for _, job := range jobs {
		agent := WorkVerifyAgentResult{
			JobID:   job.JobID,
			Adapter: job.Adapter,
		}
		tokenPath := os.Getenv(core.EnvAgentToken)
		if tokenPath == "" && job.JobID != "" {
			agent.TokenStatus = "legacy"
			agent.Reason = "no token found (pre-Phase-1 job)"
			report.Agents = append(report.Agents, agent)
			continue
		}
		if ca == nil {
			agent.TokenStatus = "unverified"
			agent.Reason = "CA unavailable"
			report.Agents = append(report.Agents, agent)
			continue
		}
		token, tokenErr := loadVerifyToken(tokenPath)
		if tokenErr != nil {
			agent.TokenStatus = "unavailable"
			agent.Reason = tokenErr.Error()
			report.Agents = append(report.Agents, agent)
			continue
		}
		if token == nil {
			agent.TokenStatus = "missing"
			agent.Reason = "COGENT_AGENT_TOKEN not set"
			report.Agents = append(report.Agents, agent)
			continue
		}
		result := core.VerifyToken(token, ca.PublicKey)
		if result.Valid {
			agent.TokenStatus = "verified"
			agent.SignatureValid = true
			agent.Role = result.Role
			agent.Expiry = result.ExpAt
			if token.Expired() {
				agent.TokenStatus = "expired"
				agent.SignatureValid = false
				report.Issues = append(report.Issues, fmt.Sprintf("job %s: token expired at %s", job.JobID, result.ExpAt))
			}
		} else {
			agent.TokenStatus = "invalid"
			agent.Reason = result.Reason
			report.Issues = append(report.Issues, fmt.Sprintf("job %s: token signature invalid: %s", job.JobID, result.Reason))
		}
		report.Agents = append(report.Agents, agent)
	}

	sawSignatureData := false
	sawMissingSignature := false
	allSigsValid := true
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

		hasPubkey := strings.TrimSpace(attestation.SignerPubkey) != ""
		hasSig := strings.TrimSpace(attestation.Signature) != ""

		if hasPubkey {
			item.SignerStatus = "present"
		}
		if hasSig {
			item.SignatureStatus = "present"
		}

		if hasPubkey || hasSig {
			sawSignatureData = true
		}

		if !hasPubkey || !hasSig {
			sawMissingSignature = true
			report.Issues = append(report.Issues, fmt.Sprintf("attestation %s: signature fields incomplete", attestation.AttestationID))
			allSigsValid = false
		} else {
			// Both pubkey and signature present — verify cryptographically.
			signable := attestation.Signable()
			pubKeyBytes, decErr := base64.StdEncoding.DecodeString(attestation.SignerPubkey)
			if decErr != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
				item.SignatureStatus = "invalid"
				item.MetadataStatus = "signer pubkey decode error"
				report.Issues = append(report.Issues, fmt.Sprintf("attestation %s: invalid signer pubkey", attestation.AttestationID))
				allSigsValid = false
			} else {
				pubKey := ed25519.PublicKey(pubKeyBytes)
				if core.VerifyJSONSignature(signable, attestation.Signature, pubKey) {
					item.SignatureStatus = "verified"
				} else {
					item.SignatureStatus = "invalid"
					report.Issues = append(report.Issues, fmt.Sprintf("attestation %s: signature verification failed", attestation.AttestationID))
					allSigsValid = false
				}
			}
		}
		report.Attestations = append(report.Attestations, item)
	}

	switch {
	case report.Commit.Status == "invalid":
		report.Verdict = "unverified"
	case sawSignatureData && !sawMissingSignature && allSigsValid && report.Commit.Status == "valid":
		report.Verdict = "verified"
	case sawSignatureData && allSigsValid && len(report.Issues) == 0:
		report.Verdict = "verified"
	case len(report.Issues) == 0:
		report.Verdict = "legacy"
	default:
		report.Verdict = "unverified"
	}

	return report, nil
}

func loadVerifyToken(path string) (*core.CapabilityToken, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read token file: %w", err)
	}
	var cred core.AgentCredential
	if err := json.Unmarshal(data, &cred); err != nil {
		return nil, fmt.Errorf("parse token file: %w", err)
	}
	return &cred.Token, nil
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
