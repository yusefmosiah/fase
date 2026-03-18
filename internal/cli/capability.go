package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yusefmosiah/cagent/internal/core"
)

const (
	// EnvCapabilityEnforcement controls audit vs enforce mode.
	// Values: "audit" (default) or "enforce".
	EnvCapabilityEnforcement = "CAGENT_CAPABILITY_ENFORCEMENT"
)

// capabilityEnforcementMode reads CAGENT_CAPABILITY_ENFORCEMENT and returns the mode.
// Defaults to audit.
func capabilityEnforcementMode() core.CapabilityEnforcementMode {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvCapabilityEnforcement)))
	if v == string(core.CapabilityEnforcementEnforce) {
		return core.CapabilityEnforcementEnforce
	}
	return core.CapabilityEnforcementAudit
}

func allCapabilities() []string {
	return []string{
		core.CapWorkCreate,
		core.CapWorkUpdate,
		core.CapWorkNoteAdd,
		core.CapWorkAttest,
		core.CapWorkEdgeAdd,
		core.CapWorkApprove,
		core.CapWorkReject,
	}
}

// loadAgentToken reads the credential file at CAGENT_AGENT_TOKEN and returns the token.
// Returns (nil, nil) if the env var is not set (no token configured).
// Returns (nil, err) if the env var is set but the file is unreadable or malformed.
func loadAgentToken() (*core.CapabilityToken, error) {
	path := strings.TrimSpace(os.Getenv(core.EnvAgentToken))
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent credential file %s: %w", path, err)
	}
	var cred core.AgentCredential
	if err := json.Unmarshal(data, &cred); err != nil {
		return nil, fmt.Errorf("parse agent credential file %s: %w", path, err)
	}
	return &cred.Token, nil
}

// capabilityViolationEvent is the structured log event emitted for audit violations.
type capabilityViolationEvent struct {
	Level      string `json:"level"`
	Kind       string `json:"kind"`
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
	Detail     string `json:"detail,omitempty"`
	Timestamp  string `json:"timestamp"`
}

// emitCapabilityViolation writes a structured warning event to stderr.
func emitCapabilityViolation(capability, reason, detail string) {
	event := capabilityViolationEvent{
		Level:      "warn",
		Kind:       "capability_violation",
		Capability: capability,
		Reason:     reason,
		Detail:     detail,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(event)
	_, _ = fmt.Fprintln(os.Stderr, string(data))
}

// checkCapability verifies the current agent token grants the requested capability.
//
// Audit mode (default): violations are logged to stderr as structured events but
// the operation proceeds normally — no behavior change for existing installs.
//
// Enforce mode (CAGENT_CAPABILITY_ENFORCEMENT=enforce): violations return an error
// that aborts the command.
func checkCapability(capability string) error {
	token, err := loadAgentToken()
	if err != nil {
		// Credential file is set but unreadable/malformed — always warn.
		emitCapabilityViolation(capability, "malformed_token", err.Error())
		if capabilityEnforcementMode() == core.CapabilityEnforcementEnforce {
			return fmt.Errorf("capability enforcement active: invalid token: %w", err)
		}
		return nil
	}

	if token == nil {
		// No token present. In audit mode this is fine (agent may pre-date Phase 0).
		// In enforce mode it is an error.
		if capabilityEnforcementMode() == core.CapabilityEnforcementEnforce {
			return fmt.Errorf("capability enforcement active: no token found (%s not set)", core.EnvAgentToken)
		}
		// Audit: do not log a violation for missing tokens — that would spam every
		// interactive operator command. Violations are only logged when a token IS
		// present but lacks the required capability (wrong role) or is expired.
		return nil
	}

	// Token is present — check expiry.
	if token.Expired() {
		exp, _ := token.ExpiresAtTime()
		detail := fmt.Sprintf("token expired at %s (job=%s)", exp.Format(time.RFC3339), token.Subject.JobID)
		emitCapabilityViolation(capability, "token_expired", detail)
		if capabilityEnforcementMode() == core.CapabilityEnforcementEnforce {
			return fmt.Errorf("capability enforcement active: %s", detail)
		}
		return nil
	}

	// Check the capability list.
	if !token.HasCapability(capability) {
		detail := fmt.Sprintf("role=%s granted=%v lacks=%s job=%s",
			token.Subject.Role, token.Capabilities, capability, token.Subject.JobID)
		emitCapabilityViolation(capability, "missing_capability", detail)
		if capabilityEnforcementMode() == core.CapabilityEnforcementEnforce {
			return fmt.Errorf("capability enforcement active: token lacks capability %q (%s)", capability, detail)
		}
		return nil
	}

	return nil
}

// requireCapabilities checks each capability in order and returns the first
// enforcement error, if any. In audit mode, violations are logged but the
// caller continues.
func requireCapabilities(capabilities ...string) error {
	for _, capability := range capabilities {
		if err := checkCapability(capability); err != nil {
			return err
		}
	}
	return nil
}

type capabilityPreviewEntry struct {
	Capability string `json:"capability"`
	Allowed    bool   `json:"allowed"`
	Reason     string `json:"reason,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

type capabilityPreviewReport struct {
	Mode        string                   `json:"mode"`
	TokenStatus string                   `json:"token_status"`
	TokenRole   string                   `json:"token_role,omitempty"`
	Entries     []capabilityPreviewEntry `json:"entries"`
}

// previewCapabilities returns a non-blocking snapshot of the current token's
// ability to satisfy the known capability matrix. Unlike checkCapability, it
// does not emit warnings or fail in audit mode.
func previewCapabilities() capabilityPreviewReport {
	report := capabilityPreviewReport{
		Mode:        string(capabilityEnforcementMode()),
		TokenStatus: "missing",
	}

	token, err := loadAgentToken()
	switch {
	case err != nil:
		report.TokenStatus = "malformed"
		for _, capability := range allCapabilities() {
			report.Entries = append(report.Entries, capabilityPreviewEntry{
				Capability: capability,
				Allowed:    false,
				Reason:     "malformed_token",
				Detail:     err.Error(),
			})
		}
		return report
	case token == nil:
		for _, capability := range allCapabilities() {
			report.Entries = append(report.Entries, capabilityPreviewEntry{
				Capability: capability,
				Allowed:    false,
				Reason:     "missing_token",
				Detail:     "CAGENT_AGENT_TOKEN not set",
			})
		}
		return report
	}

	report.TokenStatus = "present"
	report.TokenRole = token.Subject.Role
	if token.Expired() {
		exp, _ := token.ExpiresAtTime()
		detail := fmt.Sprintf("token expired at %s (job=%s)", exp.Format(time.RFC3339), token.Subject.JobID)
		for _, capability := range allCapabilities() {
			report.Entries = append(report.Entries, capabilityPreviewEntry{
				Capability: capability,
				Allowed:    false,
				Reason:     "token_expired",
				Detail:     detail,
			})
		}
		return report
	}

	for _, capability := range allCapabilities() {
		entry := capabilityPreviewEntry{
			Capability: capability,
			Allowed:    token.HasCapability(capability),
		}
		if !entry.Allowed {
			entry.Reason = "missing_capability"
			entry.Detail = fmt.Sprintf("role=%s granted=%v lacks=%s job=%s",
				token.Subject.Role, token.Capabilities, capability, token.Subject.JobID)
		}
		report.Entries = append(report.Entries, entry)
	}
	return report
}
