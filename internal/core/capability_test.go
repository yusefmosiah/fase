package core

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureCACreatesNewKeypair(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	if ca == nil {
		t.Fatal("EnsureCA returned nil")
	}
	if len(ca.PrivateKey) != ed25519.PrivateKeySize {
		t.Fatalf("private key size = %d, want %d", len(ca.PrivateKey), ed25519.PrivateKeySize)
	}
	if len(ca.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("public key size = %d, want %d", len(ca.PublicKey), ed25519.PublicKeySize)
	}

	if _, err := os.Stat(filepath.Join(dir, "ca.key")); err != nil {
		t.Fatalf("ca.key not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.pub")); err != nil {
		t.Fatalf("ca.pub not created: %v", err)
	}
}

func TestEnsureCALoadsExistingKeypair(t *testing.T) {
	dir := t.TempDir()
	ca1, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA (first): %v", err)
	}
	ca2, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA (second): %v", err)
	}
	if string(ca2.PrivateKey) != string(ca1.PrivateKey) {
		t.Fatal("second load produced different private key")
	}
	if string(ca2.PublicKey) != string(ca1.PublicKey) {
		t.Fatal("second load produced different public key")
	}
}

func TestIssueTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	agentPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate agent key: %v", err)
	}

	subject := TokenSubject{
		JobID:   "job_01ABC",
		WorkID:  "work_01XYZ",
		Role:    "worker",
		Adapter: "claude",
		Model:   "claude-sonnet-4-6",
	}
	caps := []string{CapWorkUpdate, CapWorkNoteAdd}

	token, err := IssueToken(ca, agentPub, subject, caps, 30*time.Minute)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if token.Version != 1 {
		t.Fatalf("version = %d, want 1", token.Version)
	}
	if token.Expired() {
		t.Fatal("freshly issued token should not be expired")
	}
	if !token.HasCapability(CapWorkUpdate) {
		t.Fatal("token should have work:update")
	}
	if token.HasCapability(CapWorkAttest) {
		t.Fatal("worker token should not have work:attest")
	}

	signable := token.Signable()
	if signable.Subject.JobID != "job_01ABC" {
		t.Fatalf("signable subject job_id = %q", signable.Subject.JobID)
	}

	payload, err := json.Marshal(signable)
	if err != nil {
		t.Fatalf("marshal signable: %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(token.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if !ed25519.Verify(ca.PublicKey, payload, sig) {
		t.Fatal("signature verification failed")
	}
}

func TestIssueTokenExpired(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	agentPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate agent key: %v", err)
	}

	subject := TokenSubject{JobID: "j", WorkID: "w", Role: "worker"}
	token, err := IssueToken(ca, agentPub, subject, []string{CapWorkUpdate}, 0)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if token.Expired() {
		t.Fatal("freshly issued token should not be expired")
	}

	expired := *token
	expired.ExpiresAt = time.Now().UTC().Add(-time.Second).Format(time.RFC3339)
	if !expired.Expired() {
		t.Fatal("token with past ExpiresAt should be expired")
	}
}

func TestWriteCredentialCreatesFile(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	agentPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate agent key: %v", err)
	}

	subject := TokenSubject{JobID: "j", WorkID: "w", Role: "worker"}
	token, err := IssueToken(ca, agentPub, subject, []string{CapWorkUpdate}, 0)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	cred := AgentCredential{
		Token:      *token,
		PrivateKey: base64.StdEncoding.EncodeToString([]byte("fake-key")),
	}
	path, err := WriteCredential(dir, cred)
	if err != nil {
		t.Fatalf("WriteCredential: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credential file: %v", err)
	}
	if string(data) == "" {
		t.Fatal("credential file is empty")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("credential file does not exist: %v", err)
	}
}

func TestSweepStaleTokenFiles(t *testing.T) {
	dir := t.TempDir()
	tokensDir := filepath.Join(dir, "tokens")
	if err := os.MkdirAll(tokensDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	writeFile := func(name string) {
		if err := os.WriteFile(filepath.Join(tokensDir, name), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeFile("stale.json")
	writeFile("fresh.json")

	stalePath := filepath.Join(tokensDir, "stale.json")
	if err := os.Chtimes(stalePath, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	SweepStaleTokenFiles(dir, time.Hour)

	if _, err := os.Stat(filepath.Join(tokensDir, "stale.json")); err == nil {
		t.Fatal("stale file should have been removed")
	}
	if _, err := os.Stat(filepath.Join(tokensDir, "fresh.json")); err != nil {
		t.Fatal("fresh file should still exist")
	}
}

func TestExpiredParsesRFC3339(t *testing.T) {
	token := CapabilityToken{
		Version:   1,
		ExpiresAt: time.Now().UTC().Add(-time.Second).Format(time.RFC3339),
	}
	if !token.Expired() {
		t.Fatal("token expired 1 second ago should be expired")
	}

	token2 := CapabilityToken{
		Version:   1,
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	if token2.Expired() {
		t.Fatal("token expiring in 1 hour should not be expired")
	}
}

func TestCapabilitiesForRole(t *testing.T) {
	caps := CapabilitiesForRole("worker")
	if len(caps) != 2 {
		t.Fatalf("worker caps = %v, want 2", caps)
	}

	caps = CapabilitiesForRole("unknown_role")
	if caps != nil {
		t.Fatalf("unknown role caps = %v, want nil", caps)
	}
}
