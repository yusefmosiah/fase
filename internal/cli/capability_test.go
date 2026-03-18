package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusefmosiah/cagent/internal/core"
)

func TestRequireCapabilitiesDefaultsToAudit(t *testing.T) {
	t.Setenv(EnvCapabilityEnforcement, "")
	t.Setenv(core.EnvAgentToken, "")

	if err := requireCapabilities(core.CapWorkUpdate); err != nil {
		t.Fatalf("requireCapabilities returned error in audit mode: %v", err)
	}
}

func TestRequireCapabilitiesEnforceModeFailsWithoutToken(t *testing.T) {
	t.Setenv(EnvCapabilityEnforcement, string(core.CapabilityEnforcementEnforce))
	t.Setenv(core.EnvAgentToken, "")

	if err := requireCapabilities(core.CapWorkUpdate); err == nil {
		t.Fatal("requireCapabilities returned nil in enforce mode without a token")
	}
}

func TestPreviewCapabilitiesWithWorkerToken(t *testing.T) {
	tokenPath := writeTestToken(t, core.TokenSubject{
		JobID:   "job_123",
		WorkID:  "work_456",
		Role:    "worker",
		Adapter: "codex",
		Model:   "gpt-5.4-mini",
	}, []string{core.CapWorkUpdate, core.CapWorkNoteAdd})

	t.Setenv(core.EnvAgentToken, tokenPath)
	t.Setenv(EnvCapabilityEnforcement, string(core.CapabilityEnforcementAudit))

	report := previewCapabilities()
	if report.TokenStatus != "present" {
		t.Fatalf("TokenStatus = %q, want present", report.TokenStatus)
	}
	if report.TokenRole != "worker" {
		t.Fatalf("TokenRole = %q, want worker", report.TokenRole)
	}

	entries := map[string]capabilityPreviewEntry{}
	for _, entry := range report.Entries {
		entries[entry.Capability] = entry
	}

	if got := entries[core.CapWorkUpdate]; !got.Allowed {
		t.Fatalf("work:update entry = %#v, want allowed", got)
	}
	if got := entries[core.CapWorkAttest]; got.Allowed || got.Reason != "missing_capability" {
		t.Fatalf("work:attest entry = %#v, want missing_capability", got)
	}
}

func TestPreviewCapabilitiesMissingToken(t *testing.T) {
	t.Setenv(core.EnvAgentToken, "")
	t.Setenv(EnvCapabilityEnforcement, string(core.CapabilityEnforcementAudit))

	report := previewCapabilities()
	if report.TokenStatus != "missing" {
		t.Fatalf("TokenStatus = %q, want missing", report.TokenStatus)
	}
	for _, entry := range report.Entries {
		if entry.Allowed {
			t.Fatalf("entry %#v unexpectedly allowed", entry)
		}
		if entry.Reason != "missing_token" {
			t.Fatalf("entry %#v reason = %q, want missing_token", entry, entry.Reason)
		}
	}
}

func writeTestToken(t *testing.T, subject core.TokenSubject, caps []string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "cred.json")
	cred := core.AgentCredential{
		Token: core.CapabilityToken{
			Version:      1,
			Subject:      subject,
			Capabilities: caps,
			IssuedAt:     time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
			ExpiresAt:    time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			IssuerPubkey: "issuer",
			AgentPubkey:  "agent",
			Signature:    "sig",
		},
		PrivateKey: "priv",
	}

	data, err := json.Marshal(cred)
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write credential: %v", err)
	}
	return path
}

func TestCapabilityViolationEventsAreJSONFriendly(t *testing.T) {
	event := capabilityViolationEvent{
		Level:      "warn",
		Kind:       "capability_violation",
		Capability: core.CapWorkUpdate,
		Reason:     "missing_capability",
		Detail:     "example",
		Timestamp:  "2026-03-18T12:00:00Z",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if !strings.Contains(string(data), `"kind":"capability_violation"`) {
		t.Fatalf("json = %s, want capability_violation", data)
	}
}
