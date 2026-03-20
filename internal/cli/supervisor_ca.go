package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yusefmosiah/fase/internal/core"
	"golang.org/x/crypto/ssh"
)

const (
	caKeyFile   = "ca.key"
	caPubFile   = "ca.pub"
	tokenExpiry = 35 * time.Minute // slightly longer than the 30-min lease window
)

// supervisorCA holds the CA keypair for capability token issuance and
// manages the allowed_signers file for git SSH commit verification.
type supervisorCA struct {
	caPriv ed25519.PrivateKey
	caPub  ed25519.PublicKey

	// signersMu serialises allowed_signers writes (single-writer goroutine per ADR-0035 §3).
	signersMu sync.Mutex
	stateDir  string // set once at init for allowed_signers path
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
			caPriv:   ed25519.PrivateKey(privBytes),
			caPub:    ed25519.PublicKey(pubBytes),
			stateDir: stateDir,
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
	return &supervisorCA{caPriv: caPriv, caPub: caPub, stateDir: stateDir}, nil
}

// dispatchCredential is the result of issuing a credential for a dispatch.
// It bundles the token file path, SSH signing key path, and git env vars.
type dispatchCredential struct {
	tokenPath  string   // path to the capability token JSON file
	sshKeyPath string   // path to the ephemeral SSH signing key (OpenSSH PEM)
	gitEnv     []string // env vars for git SSH commit signing
}

// issueAndWriteCredential mints a signed capability token and writes both the
// credential file and an SSH signing key file. Returns dispatchCredential with
// paths and git signing env vars, or nil on error.
func (ca *supervisorCA) issueAndWriteCredential(stateDir string, workID, role, adapter, model string) *dispatchCredential {
	cred, agentPriv, err := ca.issueCredential(workID, role, adapter, model)
	if err != nil {
		return nil
	}
	tokenPath, err := writeCredentialFile(stateDir, cred)
	if err != nil {
		return nil
	}

	// Write ephemeral SSH signing key for git commit signing.
	sshKeyPath, err := writeSSHSigningKey(stateDir, agentPriv)
	if err != nil {
		// Non-fatal: token still works, just no git signing.
		return &dispatchCredential{tokenPath: tokenPath}
	}

	// Build the email identity for this agent using workID (jobID not yet known at dispatch).
	workShort := workID
	if len(workShort) > 16 {
		workShort = workShort[:16]
	}
	email := workID + "@fase.local"
	committerName := "fase-" + role + "-" + workShort

	// Add to allowed_signers so git verify-commit works.
	agentPubKey := agentPriv.Public().(ed25519.PublicKey)
	ca.addAllowedSigner(email, agentPubKey)

	// Construct the allowed_signers path for this state dir.
	allowedSignersPath := filepath.Join(stateDir, "allowed_signers")

	gitEnv := []string{
		"GIT_COMMITTER_NAME=" + committerName,
		"GIT_COMMITTER_EMAIL=" + email,
		"GIT_AUTHOR_NAME=" + committerName,
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=gpg.format",
		"GIT_CONFIG_VALUE_0=ssh",
		"GIT_CONFIG_KEY_1=user.signingkey",
		"GIT_CONFIG_VALUE_1=" + sshKeyPath,
		"GIT_CONFIG_KEY_2=commit.gpgsign",
		"GIT_CONFIG_VALUE_2=true",
		"GIT_CONFIG_KEY_3=gpg.ssh.allowedSignersFile",
		"GIT_CONFIG_VALUE_3=" + allowedSignersPath,
	}

	return &dispatchCredential{
		tokenPath:  tokenPath,
		sshKeyPath: sshKeyPath,
		gitEnv:     gitEnv,
	}
}

// issueCredential mints a signed capability token and returns the AgentCredential
// plus the raw agent private key (needed for SSH key file writing).
func (ca *supervisorCA) issueCredential(workID, role, adapter, model string) (*core.AgentCredential, ed25519.PrivateKey, error) {
	agentPub, agentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate agent keypair: %w", err)
	}

	caps := core.CapabilitiesForRole(role)
	if len(caps) == 0 {
		caps = core.CapabilitiesForRole("worker")
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
		return nil, nil, fmt.Errorf("marshal signable payload: %w", err)
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
	}, agentPriv, nil
}

// writeSSHSigningKey writes an Ed25519 private key in OpenSSH PEM format
// to stateDir/keys/ for use with git SSH commit signing. Returns the file path.
func writeSSHSigningKey(stateDir string, privKey ed25519.PrivateKey) (string, error) {
	keysDir := filepath.Join(stateDir, "keys")
	if err := os.MkdirAll(keysDir, 0o700); err != nil {
		return "", fmt.Errorf("create keys dir: %w", err)
	}

	// Marshal to OpenSSH PEM format.
	pemBlock, err := ssh.MarshalPrivateKey(privKey, "")
	if err != nil {
		return "", fmt.Errorf("marshal ssh private key: %w", err)
	}
	pemData := pem.EncodeToMemory(pemBlock)

	f, err := os.CreateTemp(keysDir, "agent-*.pem")
	if err != nil {
		return "", fmt.Errorf("create ssh key file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("chmod ssh key file: %w", err)
	}
	if _, err := f.Write(pemData); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write ssh key file: %w", err)
	}
	_ = f.Close()
	return f.Name(), nil
}

// addAllowedSigner adds an entry to the allowed_signers file (atomic write).
// Thread-safe: serialised by signersMu.
func (ca *supervisorCA) addAllowedSigner(email string, pubKey ed25519.PublicKey) {
	ca.signersMu.Lock()
	defer ca.signersMu.Unlock()

	if ca.stateDir == "" {
		return
	}
	signersPath := filepath.Join(ca.stateDir, "allowed_signers")

	// Read existing content.
	existing, _ := os.ReadFile(signersPath)

	// Format: <email> <key-type> <base64-pubkey>
	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return
	}
	entry := fmt.Sprintf("%s %s", email, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))))

	// Check if already present.
	if strings.Contains(string(existing), entry) {
		return
	}

	// Append and atomic write.
	content := string(existing)
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += entry + "\n"

	// Atomic write: write to temp, rename.
	tmpFile := signersPath + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(content), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmpFile, signersPath)
}

// removeAllowedSigner removes the entry for email from the allowed_signers file.
// Thread-safe: serialised by signersMu.
func (ca *supervisorCA) removeAllowedSigner(email string) {
	ca.signersMu.Lock()
	defer ca.signersMu.Unlock()

	if ca.stateDir == "" {
		return
	}
	signersPath := filepath.Join(ca.stateDir, "allowed_signers")
	data, err := os.ReadFile(signersPath)
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	var kept []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, email+" ") {
			continue
		}
		kept = append(kept, line)
	}

	content := strings.Join(kept, "\n")
	if len(kept) > 0 {
		content += "\n"
	}
	tmpFile := signersPath + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(content), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmpFile, signersPath)
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

// removeSSHKeyFile deletes the SSH signing key file at path, ignoring not-found errors.
func removeSSHKeyFile(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// sweepStaleTokenFiles removes credential files in stateDir/tokens/ older than maxAge.
// Called at supervisor startup to clean up orphans from previous runs.
func sweepStaleTokenFiles(stateDir string, maxAge time.Duration) {
	sweepStaleFilesInDir(filepath.Join(stateDir, "tokens"), ".json", maxAge)
	sweepStaleFilesInDir(filepath.Join(stateDir, "keys"), ".pem", maxAge)
}

func sweepStaleFilesInDir(dir, suffix string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), suffix) {
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
