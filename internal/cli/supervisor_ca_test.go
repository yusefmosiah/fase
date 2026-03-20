package cli

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSSHSigningKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	dir := t.TempDir()
	path, err := writeSSHSigningKey(dir, priv)
	if err != nil {
		t.Fatalf("writeSSHSigningKey: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %o, want 0600", info.Mode().Perm())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	if !strings.Contains(string(data), "BEGIN") {
		t.Fatal("key file does not contain PEM header")
	}
	if !strings.Contains(string(data), "PRIVATE KEY") {
		t.Fatal("key file does not contain PRIVATE KEY in PEM header")
	}

	// Verify the file is under stateDir/keys/.
	if !strings.HasPrefix(path, filepath.Join(dir, "keys")) {
		t.Fatalf("key file path %q not under keys dir", path)
	}
}

func TestAllowedSignersAddRemove(t *testing.T) {
	dir := t.TempDir()
	ca := &supervisorCA{stateDir: dir}

	pub1, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key 1: %v", err)
	}
	pub2, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key 2: %v", err)
	}

	email1 := "agent1@fase.local"
	email2 := "agent2@fase.local"

	ca.addAllowedSigner(email1, pub1)
	ca.addAllowedSigner(email2, pub2)

	signersPath := filepath.Join(dir, "allowed_signers")
	data, err := os.ReadFile(signersPath)
	if err != nil {
		t.Fatalf("read allowed_signers: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, email1) {
		t.Fatalf("allowed_signers missing %s", email1)
	}
	if !strings.Contains(content, email2) {
		t.Fatalf("allowed_signers missing %s", email2)
	}

	// Remove agent1, verify only agent2 remains.
	ca.removeAllowedSigner(email1)

	data, err = os.ReadFile(signersPath)
	if err != nil {
		t.Fatalf("read allowed_signers after remove: %v", err)
	}
	content = string(data)
	if strings.Contains(content, email1) {
		t.Fatalf("allowed_signers still contains removed %s", email1)
	}
	if !strings.Contains(content, email2) {
		t.Fatalf("allowed_signers missing %s after removing %s", email2, email1)
	}
}

func TestIssueAndWriteCredentialReturnsDispatchCredential(t *testing.T) {
	dir := t.TempDir()
	ca, err := loadOrCreateCA(dir)
	if err != nil {
		t.Fatalf("loadOrCreateCA: %v", err)
	}

	dc := ca.issueAndWriteCredential(dir, "work_01ABC", "worker", "claude", "claude-sonnet-4-6")
	if dc == nil {
		t.Fatal("issueAndWriteCredential returned nil")
	}
	if dc.tokenPath == "" {
		t.Fatal("tokenPath is empty")
	}
	if dc.sshKeyPath == "" {
		t.Fatal("sshKeyPath is empty")
	}
	if len(dc.gitEnv) == 0 {
		t.Fatal("gitEnv is empty")
	}

	// Verify files exist on disk.
	if _, err := os.Stat(dc.tokenPath); err != nil {
		t.Fatalf("token file does not exist: %v", err)
	}
	if _, err := os.Stat(dc.sshKeyPath); err != nil {
		t.Fatalf("ssh key file does not exist: %v", err)
	}

	// Verify gitEnv contains expected keys.
	hasSigningKey := false
	hasGPGFormat := false
	for _, env := range dc.gitEnv {
		if strings.HasPrefix(env, "GIT_CONFIG_VALUE_0=ssh") {
			hasGPGFormat = true
		}
		if strings.HasPrefix(env, "GIT_CONFIG_VALUE_1=") && strings.Contains(env, dc.sshKeyPath) {
			hasSigningKey = true
		}
	}
	if !hasGPGFormat {
		t.Fatal("gitEnv missing gpg.format=ssh")
	}
	if !hasSigningKey {
		t.Fatal("gitEnv missing user.signingkey pointing to sshKeyPath")
	}
}
