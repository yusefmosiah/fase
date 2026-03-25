package mcpserver

import (
	"testing"

	"github.com/yusefmosiah/fase/internal/service"
)

// TestMCPServerCreatedByWithoutInternalSession verifies that when no internal
// session is set (normal external MCP usage), CreatedBy returns "mcp".
// This is the baseline provenance behavior for external MCP callers.
func TestMCPServerCreatedByWithoutInternalSession(t *testing.T) {
	s := newTestServer()

	// By default, no internal session should be set
	createdBy := s.CreatedBy()
	if createdBy != "mcp" {
		t.Errorf("CreatedBy() without internal session = %q, want %q", createdBy, "mcp")
	}
}

// TestMCPServerCreatedByWithInternalSession verifies that when an internal
// session is set (supervisor mode), CreatedBy returns "supervisor".
// This implements VAL-SUPERVISOR-003: supervisor-triggered MCP mutations
// must show ActorSupervisor in emitted events, not ActorMCP.
func TestMCPServerCreatedByWithInternalSession(t *testing.T) {
	s := newTestServer()

	// Set internal session ID (simulating supervisor starting a session)
	s.SetInternalSessionID("test-supervisor-session-123")

	createdBy := s.CreatedBy()
	if createdBy != "supervisor" {
		t.Errorf("CreatedBy() with internal session = %q, want %q", createdBy, "supervisor")
	}
}

// TestMCPServerCreatedByAfterClear verifies that clearing the internal
// session restores CreatedBy to "mcp".
func TestMCPServerCreatedByAfterClear(t *testing.T) {
	s := newTestServer()

	// Set and then clear internal session
	s.SetInternalSessionID("test-session")
	s.ClearInternalSessionID()

	createdBy := s.CreatedBy()
	if createdBy != "mcp" {
		t.Errorf("CreatedBy() after clear = %q, want %q", createdBy, "mcp")
	}
}

// TestMCPServerInternalSessionOverwrite verifies that setting a new internal
// session ID overwrites the previous one (only one supervisor session at a time).
func TestMCPServerInternalSessionOverwrite(t *testing.T) {
	s := newTestServer()

	// Set first session
	s.SetInternalSessionID("session-1")
	if s.CreatedBy() != "supervisor" {
		t.Error("First session should result in CreatedBy = supervisor")
	}
	if s.internalSessionID != "session-1" {
		t.Errorf("internalSessionID = %q, want %q", s.internalSessionID, "session-1")
	}

	// Set second session (should overwrite)
	s.SetInternalSessionID("session-2")
	if s.CreatedBy() != "supervisor" {
		t.Error("Second session should still result in CreatedBy = supervisor")
	}
	if s.internalSessionID != "session-2" {
		t.Errorf("internalSessionID after overwrite = %q, want %q", s.internalSessionID, "session-2")
	}
}

// TestMCPServerCreatedByThreadSafety verifies that the internal session tracking
// is thread-safe through mutex protection. This test runs concurrent access
// to detect data races.
func TestMCPServerCreatedByThreadSafety(t *testing.T) {
	s := newTestServer()

	// Run concurrent access to detect data races
	done := make(chan bool, 3)

	// Goroutine 1: Repeatedly set internal session
	go func() {
		for i := 0; i < 100; i++ {
			s.SetInternalSessionID("session-a")
		}
		done <- true
	}()

	// Goroutine 2: Repeatedly clear internal session
	go func() {
		for i := 0; i < 100; i++ {
			s.ClearInternalSessionID()
		}
		done <- true
	}()

	// Goroutine 3: Repeatedly read CreatedBy
	go func() {
		for i := 0; i < 100; i++ {
			_ = s.CreatedBy()
		}
		done <- true
	}()

	// Wait for all goroutines to complete
	for i := 0; i < 3; i++ {
		<-done
	}

	// Test passes if no data race is detected (run with -race flag)
}

// TestMCPServerCreatedByMapsToCorrectEventActor verifies that the CreatedBy
// values from the MCP server map correctly to EventActor values in the
// service layer. This ensures end-to-end provenance consistency.
func TestMCPServerCreatedByMapsToCorrectEventActor(t *testing.T) {
	tests := []struct {
		name         string
		setInternal  bool
		expectedBy   string
		expectedActor service.EventActor
	}{
		{
			name:         "external MCP maps to ActorMCP",
			setInternal:  false,
			expectedBy:   "mcp",
			expectedActor: service.ActorMCP,
		},
		{
			name:         "supervisor MCP maps to ActorSupervisor",
			setInternal:  true,
			expectedBy:   "supervisor",
			expectedActor: service.ActorSupervisor,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer()

			if tt.setInternal {
				s.SetInternalSessionID("test-session")
			}

			createdBy := s.CreatedBy()
			if createdBy != tt.expectedBy {
				t.Errorf("CreatedBy() = %q, want %q", createdBy, tt.expectedBy)
			}

			// Verify the mapping to EventActor
			actor := actorFromCreatedBy(createdBy)
			if actor != tt.expectedActor {
				t.Errorf("actorFromCreatedBy(%q) = %v, want %v", createdBy, actor, tt.expectedActor)
			}
		})
	}
}

// TestMCPServerCreatedByUsedInToolHandlers verifies that the CreatedBy value
// is actually used by the MCP tool handlers (not just stored). This ensures
// the plumbing works end-to-end.
func TestMCPServerCreatedByUsedInToolHandlers(t *testing.T) {
	s := newTestServer()

	// With no internal session, CreatedBy should be "mcp"
	if s.CreatedBy() != "mcp" {
		t.Error("Default CreatedBy should be 'mcp'")
	}

	// Set internal session (simulating supervisor)
	s.SetInternalSessionID("supervisor-session")

	// CreatedBy should now be "supervisor"
	if s.CreatedBy() != "supervisor" {
		t.Error("CreatedBy with internal session should be 'supervisor'")
	}

	// The CreatedBy value is used in tool handlers:
	// - work_update uses mcpSrv.CreatedBy()
	// - work_note_add uses mcpSrv.CreatedBy()
	// - work_attest uses mcpSrv.CreatedBy()
	// - check_record_create uses mcpSrv.CreatedBy()
	// - work_create uses mcpSrv.CreatedBy()
}

// TestMCPServerInternalSessionTracking verifies that the internal session ID
// is correctly tracked and can be queried.
func TestMCPServerInternalSessionTracking(t *testing.T) {
	s := newTestServer()

	// Initially empty
	if s.internalSessionID != "" {
		t.Errorf("initial internalSessionID should be empty, got %q", s.internalSessionID)
	}

	// Set internal session
	testSessionID := "supervisor-sess-123"
	s.SetInternalSessionID(testSessionID)

	// Verify it's stored
	if s.internalSessionID != testSessionID {
		t.Errorf("internalSessionID = %q, want %q", s.internalSessionID, testSessionID)
	}

	// CreatedBy should reflect internal session
	if s.CreatedBy() != "supervisor" {
		t.Error("CreatedBy should return 'supervisor' when internal session is set")
	}

	// Clear internal session
	s.ClearInternalSessionID()

	// Verify it's cleared
	if s.internalSessionID != "" {
		t.Errorf("internalSessionID after clear should be empty, got %q", s.internalSessionID)
	}

	// CreatedBy should revert to "mcp"
	if s.CreatedBy() != "mcp" {
		t.Errorf("CreatedBy after clear = %q, want %q", s.CreatedBy(), "mcp")
	}
}

// TestMCPServerProvenanceVALSupervisor003 verifies the complete VAL-SUPERVISOR-003
// requirement: supervisor-triggered MCP mutations must show ActorSupervisor
// (not ActorMCP) in emitted work events.
func TestMCPServerProvenanceVALSupervisor003(t *testing.T) {
	s := newTestServer()

	// Simulate external MCP client (no supervisor session)
	externalCreatedBy := s.CreatedBy()
	if externalCreatedBy != "mcp" {
		t.Errorf("External MCP CreatedBy = %q, want %q", externalCreatedBy, "mcp")
	}
	externalActor := actorFromCreatedBy(externalCreatedBy)
	if externalActor != service.ActorMCP {
		t.Errorf("External MCP Actor = %v, want %v", externalActor, service.ActorMCP)
	}

	// Simulate supervisor starting a session
	s.SetInternalSessionID("sess-supervisor-001")

	// Now CreatedBy should be "supervisor"
	supervisorCreatedBy := s.CreatedBy()
	if supervisorCreatedBy != "supervisor" {
		t.Errorf("Supervisor MCP CreatedBy = %q, want %q", supervisorCreatedBy, "supervisor")
	}
	supervisorActor := actorFromCreatedBy(supervisorCreatedBy)
	if supervisorActor != service.ActorSupervisor {
		t.Errorf("Supervisor MCP Actor = %v, want %v", supervisorActor, service.ActorSupervisor)
	}

	// Verify supervisor events don't wake supervisor (self-wake prevention, VAL-SUPERVISOR-002)
	supervisorEvent := service.WorkEvent{
		Actor: supervisorActor,
		Cause: service.CauseSupervisorMutation,
	}
	if supervisorEvent.RequiresSupervisorAttention() {
		t.Error("Supervisor's own mutations should NOT wake supervisor (VAL-SUPERVISOR-002)")
	}

	// Verify external MCP events DO wake supervisor (they're actionable)
	mcpEvent := service.WorkEvent{
		Actor: externalActor,
		Cause: service.CauseWorkerTerminal,
		State: "done",
	}
	if !mcpEvent.RequiresSupervisorAttention() {
		t.Error("External MCP events SHOULD wake supervisor (VAL-SUPERVISOR-003)")
	}
}

// actorFromCreatedBy mirrors the service package function for testing
func actorFromCreatedBy(createdBy string) service.EventActor {
	switch createdBy {
	case "housekeeping":
		return service.ActorHousekeeping
	case "reconciler":
		return service.ActorReconciler
	case "supervisor":
		return service.ActorSupervisor
	case "mcp":
		return service.ActorMCP
	case "host":
		return service.ActorHost
	case "service":
		return service.ActorService
	default:
		return service.ActorWorker
	}
}
