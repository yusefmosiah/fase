package core

import (
	"encoding/json"
	"testing"
)

func TestWorkExecutionStateValid(t *testing.T) {
	tests := []struct {
		name  string
		state WorkExecutionState
		want  bool
	}{
		// Canonical active states - should be valid for new writes
		{"ready is valid", WorkExecutionStateReady, true},
		{"claimed is valid", WorkExecutionStateClaimed, true},
		{"in_progress is valid", WorkExecutionStateInProgress, true},
		{"checking is valid", WorkExecutionStateChecking, true},
		{"blocked is valid", WorkExecutionStateBlocked, true},
		{"done is valid", WorkExecutionStateDone, true},
		{"failed is valid", WorkExecutionStateFailed, true},
		{"cancelled is valid", WorkExecutionStateCancelled, true},
		{"archived is valid", WorkExecutionStateArchived, true},

		// Deprecated states - should NOT be valid for new writes
		{"awaiting_attestation is not valid", WorkExecutionStateAwaitingAttestation, false},

		// Invalid/unknown states
		{"unknown state is not valid", WorkExecutionState("unknown"), false},
		{"empty state is not valid", WorkExecutionState(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.state.Valid()
			if got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWorkExecutionStateIsDeprecatedState(t *testing.T) {
	tests := []struct {
		name  string
		state WorkExecutionState
		want  bool
	}{
		// Canonical states - not deprecated
		{"ready is not deprecated", WorkExecutionStateReady, false},
		{"claimed is not deprecated", WorkExecutionStateClaimed, false},
		{"in_progress is not deprecated", WorkExecutionStateInProgress, false},
		{"checking is not deprecated", WorkExecutionStateChecking, false},
		{"blocked is not deprecated", WorkExecutionStateBlocked, false},
		{"done is not deprecated", WorkExecutionStateDone, false},
		{"failed is not deprecated", WorkExecutionStateFailed, false},
		{"cancelled is not deprecated", WorkExecutionStateCancelled, false},
		{"archived is not deprecated", WorkExecutionStateArchived, false},

		// Deprecated states
		{"awaiting_attestation is deprecated", WorkExecutionStateAwaitingAttestation, true},

		// Invalid/unknown states - not deprecated (they're just invalid)
		{"unknown state is not deprecated", WorkExecutionState("unknown"), false},
		{"empty state is not deprecated", WorkExecutionState(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.state.IsDeprecatedState()
			if got != tt.want {
				t.Errorf("IsDeprecatedState() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWorkExecutionStateCanonical(t *testing.T) {
	tests := []struct {
		name  string
		state WorkExecutionState
		want  WorkExecutionState
	}{
		// Canonical states - unchanged
		{"ready stays ready", WorkExecutionStateReady, WorkExecutionStateReady},
		{"claimed stays claimed", WorkExecutionStateClaimed, WorkExecutionStateClaimed},
		{"in_progress stays in_progress", WorkExecutionStateInProgress, WorkExecutionStateInProgress},
		{"checking normalizes to in_progress", WorkExecutionStateChecking, WorkExecutionStateInProgress},
		{"blocked stays blocked", WorkExecutionStateBlocked, WorkExecutionStateBlocked},
		{"done stays done", WorkExecutionStateDone, WorkExecutionStateDone},
		{"failed stays failed", WorkExecutionStateFailed, WorkExecutionStateFailed},
		{"cancelled stays cancelled", WorkExecutionStateCancelled, WorkExecutionStateCancelled},
		{"archived stays archived", WorkExecutionStateArchived, WorkExecutionStateArchived},

		// Deprecated states - normalized to canonical equivalents
		{"awaiting_attestation normalizes to in_progress", WorkExecutionStateAwaitingAttestation, WorkExecutionStateInProgress},

		// Invalid/unknown states - unchanged (they don't have canonical forms)
		{"unknown state stays unknown", WorkExecutionState("unknown"), WorkExecutionState("unknown")},
		{"empty state stays empty", WorkExecutionState(""), WorkExecutionState("")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.state.Canonical()
			if got != tt.want {
				t.Errorf("Canonical() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWorkExecutionStateTerminal(t *testing.T) {
	tests := []struct {
		name  string
		state WorkExecutionState
		want  bool
	}{
		// Terminal states
		{"done is terminal", WorkExecutionStateDone, true},
		{"failed is terminal", WorkExecutionStateFailed, true},
		{"cancelled is terminal", WorkExecutionStateCancelled, true},
		{"archived is terminal", WorkExecutionStateArchived, true},

		// Non-terminal states
		{"ready is not terminal", WorkExecutionStateReady, false},
		{"claimed is not terminal", WorkExecutionStateClaimed, false},
		{"in_progress is not terminal", WorkExecutionStateInProgress, false},
		{"checking is not terminal", WorkExecutionStateChecking, false},
		{"blocked is not terminal", WorkExecutionStateBlocked, false},
		{"awaiting_attestation is not terminal", WorkExecutionStateAwaitingAttestation, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.state.Terminal()
			if got != tt.want {
				t.Errorf("Terminal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWorkExecutionStateMarshalJSONCanonicalizesDeprecatedState(t *testing.T) {
	payload, err := json.Marshal(WorkExecutionStateAwaitingAttestation)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(payload) != `"in_progress"` {
		t.Fatalf("Marshal = %s, want %q", payload, `"in_progress"`)
	}
}

func TestWorkExecutionStateMarshalTextCanonicalizesDeprecatedState(t *testing.T) {
	payload, err := WorkExecutionStateAwaitingAttestation.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	if string(payload) != "in_progress" {
		t.Fatalf("MarshalText = %q, want %q", payload, "in_progress")
	}
}
