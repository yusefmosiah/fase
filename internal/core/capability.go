package core

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Capability constants — the canonical string names checked by the CLI.
const (
	CapWorkUpdate    = "work:update"
	CapWorkNoteAdd   = "work:note-add"
	CapWorkAttest    = "work:attest"
	CapWorkCreate    = "work:create"
	CapWorkEdgeAdd   = "work:edge-add"
	CapWorkApprove   = "work:approve"
	CapWorkReject    = "work:reject"
	CapWorkForceDone = "work:force-done"

	EnvAgentToken = "COGENT_AGENT_TOKEN"
)

// CapabilityEnforcementMode controls whether violations block or only warn.
type CapabilityEnforcementMode string

const (
	CapabilityEnforcementAudit   CapabilityEnforcementMode = "audit"
	CapabilityEnforcementEnforce CapabilityEnforcementMode = "enforce"
)

// roleCapabilities maps role name → default capability set.
var roleCapabilities = map[string][]string{
	"worker":   {CapWorkUpdate, CapWorkNoteAdd},
	"attestor": {CapWorkAttest, CapWorkNoteAdd},
	"reviewer": {CapWorkApprove, CapWorkReject, CapWorkNoteAdd},
	"planner":  {CapWorkCreate, CapWorkEdgeAdd, CapWorkNoteAdd},
	// overseer is the host/supervisor role — it holds all standard caps plus the
	// force-done escape hatch for manual recovery.
	"overseer": {CapWorkUpdate, CapWorkNoteAdd, CapWorkAttest, CapWorkCreate, CapWorkEdgeAdd, CapWorkApprove, CapWorkReject, CapWorkForceDone},
}

// CapabilitiesForRole returns the standard capability slice for a role.
// Returns nil for unknown roles.
func CapabilitiesForRole(role string) []string {
	return roleCapabilities[role]
}

// TokenSubject identifies the agent and scope a capability token was issued for.
type TokenSubject struct {
	JobID   string `json:"job_id"`
	WorkID  string `json:"work_id"`
	Role    string `json:"role"`
	Adapter string `json:"adapter"`
	Model   string `json:"model"`
}

// TokenSignable is the canonical signed payload — all token fields except Signature.
// Timestamps are RFC3339 strings so the serialised form is stable across platforms.
type TokenSignable struct {
	Version      int          `json:"version"`
	Subject      TokenSubject `json:"subject"`
	Capabilities []string     `json:"capabilities"`
	IssuedAt     string       `json:"issued_at"`
	ExpiresAt    string       `json:"expires_at"`
	IssuerPubkey string       `json:"issuer_pubkey"`
	AgentPubkey  string       `json:"agent_pubkey"`
}

// CapabilityToken is a signed capability grant issued by the supervisor CA.
type CapabilityToken struct {
	Version      int          `json:"version"`
	Subject      TokenSubject `json:"subject"`
	Capabilities []string     `json:"capabilities"`
	IssuedAt     string       `json:"issued_at"`
	ExpiresAt    string       `json:"expires_at"`
	IssuerPubkey string       `json:"issuer_pubkey"`
	AgentPubkey  string       `json:"agent_pubkey"`
	Signature    string       `json:"signature"`
}

// IssuedAtTime parses and returns the issued_at timestamp.
func (t *CapabilityToken) IssuedAtTime() (time.Time, error) {
	return time.Parse(time.RFC3339, t.IssuedAt)
}

// ExpiresAtTime parses and returns the expires_at timestamp.
func (t *CapabilityToken) ExpiresAtTime() (time.Time, error) {
	return time.Parse(time.RFC3339, t.ExpiresAt)
}

// Expired reports whether the token's expiry time has passed.
func (t *CapabilityToken) Expired() bool {
	exp, err := t.ExpiresAtTime()
	if err != nil {
		return true
	}
	return time.Now().UTC().After(exp)
}

// HasCapability reports whether the token grants the given capability string.
func (t *CapabilityToken) HasCapability(cap string) bool {
	for _, c := range t.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// Signable returns the canonical signed payload (all fields except Signature).
func (t *CapabilityToken) Signable() TokenSignable {
	return TokenSignable{
		Version:      t.Version,
		Subject:      t.Subject,
		Capabilities: t.Capabilities,
		IssuedAt:     t.IssuedAt,
		ExpiresAt:    t.ExpiresAt,
		IssuerPubkey: t.IssuerPubkey,
		AgentPubkey:  t.AgentPubkey,
	}
}

// AgentCredential is the on-disk format written to a temp file pointed to by
// COGENT_AGENT_TOKEN. The agent reads this file on startup.
type AgentCredential struct {
	Token      CapabilityToken `json:"token"`
	PrivateKey string          `json:"private_key"` // base64-encoded Ed25519 private key seed
}

// ─── CA keypair ───────────────────────────────────────────────────────────────

// CAKeyPair holds the supervisor Certificate Authority keypair.
type CAKeyPair struct {
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
}

var caMu sync.Mutex

// EnsureCA loads the CA keypair from stateDir/ca.key and stateDir/ca.pub.
// If those files are absent, a new keypair is generated and persisted.
// A process-wide mutex prevents TOCTOU races between concurrent goroutines
// within the same process. For cross-process safety, callers should serialise
// at a higher level (e.g. supervisor single-process guarantee).
func EnsureCA(stateDir string) (*CAKeyPair, error) {
	caMu.Lock()
	defer caMu.Unlock()

	privPath := filepath.Join(stateDir, "ca.key")
	pubPath := filepath.Join(stateDir, "ca.pub")

	privData, privErr := os.ReadFile(privPath)
	pubData, pubErr := os.ReadFile(pubPath)

	if privErr == nil && pubErr == nil {
		privBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(privData)))
		if err != nil {
			return nil, fmt.Errorf("decode CA private key: %w", err)
		}
		pubBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(pubData)))
		if err != nil {
			return nil, fmt.Errorf("decode CA public key: %w", err)
		}
		if len(privBytes) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("CA private key wrong size: got %d want %d", len(privBytes), ed25519.PrivateKeySize)
		}
		if len(pubBytes) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("CA public key wrong size: got %d want %d", len(pubBytes), ed25519.PublicKeySize)
		}
		return &CAKeyPair{
			PrivateKey: ed25519.PrivateKey(privBytes),
			PublicKey:  ed25519.PublicKey(pubBytes),
		}, nil
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA keypair: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	if err := os.WriteFile(privPath, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600); err != nil {
		return nil, fmt.Errorf("write CA private key: %w", err)
	}
	if err := os.WriteFile(pubPath, []byte(base64.StdEncoding.EncodeToString(pub)), 0o644); err != nil {
		return nil, fmt.Errorf("write CA public key: %w", err)
	}
	return &CAKeyPair{PrivateKey: priv, PublicKey: pub}, nil
}

// ─── Token issuance ───────────────────────────────────────────────────────────

const defaultTokenExpiry = 30 * time.Minute

// IssueToken signs and returns a new capability token.
// expiry sets the token lifetime; pass 0 to use the default (30 minutes).
func IssueToken(ca *CAKeyPair, agentPub ed25519.PublicKey, subject TokenSubject, capabilities []string, expiry time.Duration) (*CapabilityToken, error) {
	if expiry == 0 {
		expiry = defaultTokenExpiry
	}
	now := time.Now().UTC().Truncate(time.Second)
	signable := TokenSignable{
		Version:      1,
		Subject:      subject,
		Capabilities: capabilities,
		IssuedAt:     now.Format(time.RFC3339),
		ExpiresAt:    now.Add(expiry).Format(time.RFC3339),
		IssuerPubkey: base64.StdEncoding.EncodeToString(ca.PublicKey),
		AgentPubkey:  base64.StdEncoding.EncodeToString(agentPub),
	}

	payload, err := json.Marshal(signable)
	if err != nil {
		return nil, fmt.Errorf("marshal signable: %w", err)
	}
	sig := ed25519.Sign(ca.PrivateKey, payload)

	return &CapabilityToken{
		Version:      signable.Version,
		Subject:      signable.Subject,
		Capabilities: signable.Capabilities,
		IssuedAt:     signable.IssuedAt,
		ExpiresAt:    signable.ExpiresAt,
		IssuerPubkey: signable.IssuerPubkey,
		AgentPubkey:  signable.AgentPubkey,
		Signature:    base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// ─── Token file I/O ───────────────────────────────────────────────────────────

// WriteCredential serialises cred to a temp file in stateDir/tokens/ and returns its path.
func WriteCredential(stateDir string, cred AgentCredential) (string, error) {
	tokensDir := filepath.Join(stateDir, "tokens")
	if err := os.MkdirAll(tokensDir, 0o700); err != nil {
		return "", fmt.Errorf("create tokens dir: %w", err)
	}
	f, err := os.CreateTemp(tokensDir, "token-*.json")
	if err != nil {
		return "", fmt.Errorf("create token file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("chmod token file: %w", err)
	}
	data, err := json.Marshal(cred)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("marshal credential: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write token file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("close token file: %w", err)
	}
	return f.Name(), nil
}

// ─── Token verification ────────────────────────────────────────────────────────

// VerifyTokenResult describes the outcome of token signature verification.
type VerifyTokenResult struct {
	Valid    bool   `json:"valid"`
	Reason   string `json:"reason,omitempty"`
	Role     string `json:"role,omitempty"`
	JobID    string `json:"job_id,omitempty"`
	WorkID   string `json:"work_id,omitempty"`
	ExpAt    string `json:"expires_at,omitempty"`
	CapCount int    `json:"capability_count"`
}

// VerifyToken checks that the token's Ed25519 signature was produced by the
// given CA public key. It does NOT check expiry — callers should check
// token.Expired() separately if needed.
func VerifyToken(token *CapabilityToken, caPub ed25519.PublicKey) VerifyTokenResult {
	if token == nil {
		return VerifyTokenResult{Reason: "nil token"}
	}
	if caPub == nil {
		return VerifyTokenResult{Reason: "nil CA public key"}
	}

	signable := token.Signable()
	payload, err := json.Marshal(signable)
	if err != nil {
		return VerifyTokenResult{Reason: "marshal signable: " + err.Error()}
	}

	sigBytes, err := base64.StdEncoding.DecodeString(token.Signature)
	if err != nil {
		return VerifyTokenResult{Reason: "decode signature: " + err.Error()}
	}

	if !ed25519.Verify(caPub, payload, sigBytes) {
		return VerifyTokenResult{
			Reason:   "signature mismatch",
			Role:     token.Subject.Role,
			JobID:    token.Subject.JobID,
			WorkID:   token.Subject.WorkID,
			ExpAt:    token.ExpiresAt,
			CapCount: len(token.Capabilities),
		}
	}

	return VerifyTokenResult{
		Valid:    true,
		Role:     token.Subject.Role,
		JobID:    token.Subject.JobID,
		WorkID:   token.Subject.WorkID,
		ExpAt:    token.ExpiresAt,
		CapCount: len(token.Capabilities),
	}
}

// CanonicalJSON serialises v to a deterministic JSON byte slice suitable for
// cryptographic signing. It uses json.Marshal (which sorts struct fields) and
// strips trailing whitespace. For struct types this is stable; for map types
// the caller MUST convert to a sorted representation first — Go randomises
// map iteration order, so json.Marshal on maps is non-deterministic.
func CanonicalJSON(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.TrimSpace(data), nil
}

// SignJSON signs a canonical JSON serialisation of v using privKey.
// Returns the base64-encoded Ed25519 signature.
func SignJSON(v any, privKey ed25519.PrivateKey) (string, error) {
	payload, err := CanonicalJSON(v)
	if err != nil {
		return "", fmt.Errorf("canonical json: %w", err)
	}
	sig := ed25519.Sign(privKey, payload)
	return base64.StdEncoding.EncodeToString(sig), nil
}

// VerifyJSONSignature checks that sig (base64 Ed25519) is a valid signature
// over the canonical JSON of v, produced by pubKey.
func VerifyJSONSignature(v any, sig string, pubKey ed25519.PublicKey) bool {
	payload, err := CanonicalJSON(v)
	if err != nil {
		return false
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	return ed25519.Verify(pubKey, payload, sigBytes)
}

// AttestationSignable is the canonical payload signed for attestation records.
// It excludes Signature, SignerPubkey, and Metadata (map types are non-deterministic
// per ADR-0035 §Security Considerations).
type AttestationSignable struct {
	AttestationID           string  `json:"attestation_id"`
	SubjectKind             string  `json:"subject_kind"`
	SubjectID               string  `json:"subject_id"`
	Result                  string  `json:"result"`
	Summary                 string  `json:"summary,omitempty"`
	ArtifactID              string  `json:"artifact_id,omitempty"`
	JobID                   string  `json:"job_id,omitempty"`
	SessionID               string  `json:"session_id,omitempty"`
	Method                  string  `json:"method,omitempty"`
	VerifierKind            string  `json:"verifier_kind,omitempty"`
	VerifierIdentity        string  `json:"verifier_identity,omitempty"`
	Confidence              float64 `json:"confidence,omitempty"`
	Blocking                bool    `json:"blocking,omitempty"`
	SupersedesAttestationID string  `json:"supersedes_attestation_id,omitempty"`
	CreatedBy               string  `json:"created_by,omitempty"`
	CreatedAt               string  `json:"created_at"`
}

// Signable returns the canonical signable payload for an attestation record.
func (r *AttestationRecord) Signable() AttestationSignable {
	return AttestationSignable{
		AttestationID:           r.AttestationID,
		SubjectKind:             r.SubjectKind,
		SubjectID:               r.SubjectID,
		Result:                  r.Result,
		Summary:                 r.Summary,
		ArtifactID:              r.ArtifactID,
		JobID:                   r.JobID,
		SessionID:               r.SessionID,
		Method:                  r.Method,
		VerifierKind:            r.VerifierKind,
		VerifierIdentity:        r.VerifierIdentity,
		Confidence:              r.Confidence,
		Blocking:                r.Blocking,
		SupersedesAttestationID: r.SupersedesAttestationID,
		CreatedBy:               r.CreatedBy,
		CreatedAt:               r.CreatedAt.UTC().Format(time.RFC3339),
	}
}
