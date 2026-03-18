package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yusefmosiah/cagent/internal/core"
)

const (
	caKeyFile   = "ca.key"
	caPubFile   = "ca.pub"
	tokenExpiry = 35 * time.Minute // slightly longer than the 30-min lease window
)

// supervisorCA holds the CA keypair for capability token issuance.
type supervisorCA struct {
	caPriv ed25519.PrivateKey
	caPub  ed25519.PublicKey
}

// loadOrCreateCA loads or creates the supervisor CA Ed25519 keypair from stateDir.
// Files: <stateDir>/ca.key (base64 private key, mode 0600) and <stateDir>/ca.pub.
func loadOrCreateCA(stateDir string) (*supervisorCA, error) {
	keyPath := filepath.Join(stateDir, caKeyFile)
	pubPath := filepath.Join(stateDir, caPubFile)

	keyData, keyErr := os.ReadFile(keyPath)
	pubData, pubErr := os.ReadFile(pubPath)

	if keyErr == nil && pubErr == nil {
		privBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyData)))
		if err != nil {
			return nil, fmt.Errorf("decode ca.key: %w", err)
		}
		pubBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(pubData)))
		if err != nil {
			return nil, fmt.Errorf("decode ca.pub: %w", err)
		}
		return &supervisorCA{
			caPriv: ed25519.PrivateKey(privBytes),
			caPub:  ed25519.PublicKey(pubBytes),
		}, nil
	}

	// Generate a new CA keypair.
	caPub, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA keypair: %w", err)
	}

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(caPriv)), 0o600); err != nil {
		return nil, fmt.Errorf("write ca.key: %w", err)
	}
	if err := os.WriteFile(pubPath, []byte(base64.StdEncoding.EncodeToString(caPub)), 0o644); err != nil {
		return nil, fmt.Errorf("write ca.pub: %w", err)
	}
	return &supervisorCA{caPriv: caPriv, caPub: caPub}, nil
}

// issueAndWriteCredential mints a signed capability token for the given work
// assignment and writes it to a temp file. Returns the file path, or "" on error.
// Non-fatal in audit mode — the agent run proceeds without a token.
func (ca *supervisorCA) issueAndWriteCredential(stateDir, _ string, workID, role, adapter, model string, _ time.Duration) string {
	cred, err := ca.issueCredential(workID, role, adapter, model)
	if err != nil {
		return ""
	}
	path, err := writeCredentialFile(stateDir, cred)
	if err != nil {
		return ""
	}
	return path
}

// issueCredential mints a signed capability token and returns the AgentCredential.
func (ca *supervisorCA) issueCredential(workID, role, adapter, model string) (*core.AgentCredential, error) {
	agentPub, agentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate agent keypair: %w", err)
	}

	caps := core.RoleCapabilities[role]
	if len(caps) == 0 {
		caps = core.RoleCapabilities["worker"]
	}

	now := time.Now().UTC()
	signable := core.TokenSignable{
		Version: 1,
		Subject: core.TokenSubject{
			WorkID:  workID,
			Role:    role,
			Adapter: adapter,
			Model:   model,
		},
		Capabilities: caps,
		IssuedAt:     now.Format(time.RFC3339),
		ExpiresAt:    now.Add(tokenExpiry).Format(time.RFC3339),
		IssuerPubkey: base64.StdEncoding.EncodeToString(ca.caPub),
		AgentPubkey:  base64.StdEncoding.EncodeToString(agentPub),
	}

	signableJSON, err := json.Marshal(signable)
	if err != nil {
		return nil, fmt.Errorf("marshal signable payload: %w", err)
	}

	sig := ed25519.Sign(ca.caPriv, signableJSON)

	token := core.CapabilityToken{
		Version:      signable.Version,
		Subject:      signable.Subject,
		Capabilities: signable.Capabilities,
		IssuedAt:     signable.IssuedAt,
		ExpiresAt:    signable.ExpiresAt,
		IssuerPubkey: signable.IssuerPubkey,
		AgentPubkey:  signable.AgentPubkey,
		Signature:    base64.StdEncoding.EncodeToString(sig),
	}

	return &core.AgentCredential{
		Token:      token,
		PrivateKey: base64.StdEncoding.EncodeToString(agentPriv),
	}, nil
}

// writeCredentialFile writes an AgentCredential to a temporary file (mode 0600)
// under <stateDir>/tokens/ and returns its path.
func writeCredentialFile(stateDir string, cred *core.AgentCredential) (string, error) {
	data, err := json.Marshal(cred)
	if err != nil {
		return "", fmt.Errorf("marshal credential: %w", err)
	}
	tokenDir := filepath.Join(stateDir, "tokens")
	if err := os.MkdirAll(tokenDir, 0o700); err != nil {
		return "", fmt.Errorf("create token dir: %w", err)
	}
	f, err := os.CreateTemp(tokenDir, "token-*.json")
	if err != nil {
		return "", fmt.Errorf("create token file: %w", err)
	}
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("chmod token file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write token file: %w", err)
	}
	_ = f.Close()
	return f.Name(), nil
}

// removeCredentialFile deletes the token file at path, ignoring not-found errors.
func removeCredentialFile(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// sweepStaleTokenFiles removes credential files in stateDir/tokens/ older than maxAge.
// Called at supervisor startup to clean up orphans from previous runs.
func sweepStaleTokenFiles(stateDir string, maxAge time.Duration) {
	dir := filepath.Join(stateDir, "tokens")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
