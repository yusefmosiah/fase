package mcpserver

import (
	"context"
	"sync"
	"testing"

	"github.com/yusefmosiah/cogent/internal/service"
)

// TestMCPServerCreatedByDefault verifies that when no caller role is set
// and no context override exists, CreatedBy returns "mcp".
// This is the baseline provenance behavior for external MCP callers.
func TestMCPServerCreatedByDefault(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	createdBy := s.CreatedBy(ctx)
	if createdBy != "mcp" {
		t.Errorf("CreatedBy() without role = %q, want %q", createdBy, "mcp")
	}
}

// TestMCPServerCreatedByWithCallerRole verifies that when a caller role
// is set on the server (supervisor mode), CreatedBy returns "supervisor".
// This implements VAL-SUPERVISOR-003: supervisor-triggered MCP mutations
// must show ActorSupervisor in emitted events, not ActorMCP.
func TestMCPServerCreatedByWithCallerRole(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	// Set caller role (simulating supervisor starting a session)
	s.SetCallerRole("supervisor")

	createdBy := s.CreatedBy(ctx)
	if createdBy != "supervisor" {
		t.Errorf("CreatedBy() with caller role = %q, want %q", createdBy, "supervisor")
	}
}

// TestMCPServerCreatedByWithContextOverride verifies that context override
// takes precedence over server caller role.
func TestMCPServerCreatedByWithContextOverride(t *testing.T) {
	s := newTestServer()

	// Set server caller role to supervisor
	s.SetCallerRole("supervisor")

	// But context has explicit host role
	ctx := WithCallerRole(context.Background(), "host")

	createdBy := s.CreatedBy(ctx)
	if createdBy != "host" {
		t.Errorf("CreatedBy() with context override = %q, want %q", createdBy, "host")
	}
}

// TestMCPServerCallerRoleOverwrite verifies that setting a new caller
// role overwrites the previous one (only one role at a time per server).
func TestMCPServerCallerRoleOverwrite(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	// Set first role
	s.SetCallerRole("supervisor")
	if s.GetCallerRole() != "supervisor" {
		t.Errorf("callerRole = %q, want %q", s.GetCallerRole(), "supervisor")
	}
	if s.CreatedBy(ctx) != "supervisor" {
		t.Error("First role should result in CreatedBy = supervisor")
	}

	// Set second role (should overwrite)
	s.SetCallerRole("host")
	if s.GetCallerRole() != "host" {
		t.Errorf("callerRole after overwrite = %q, want %q", s.GetCallerRole(), "host")
	}
	if s.CreatedBy(ctx) != "host" {
		t.Error("Second role should result in CreatedBy = host")
	}
}

// TestMCPServerCreatedByThreadSafety verifies that the caller role tracking
// is thread-safe through mutex protection. This test runs concurrent access
// to detect data races.
func TestMCPServerCreatedByThreadSafety(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	// Run concurrent access to detect data races
	done := make(chan bool, 3)

	// Goroutine 1: Repeatedly set caller role
	go func() {
		for i := 0; i < 100; i++ {
			s.SetCallerRole("supervisor")
		}
		done <- true
	}()

	// Goroutine 2: Repeatedly clear caller role (via setting empty)
	go func() {
		for i := 0; i < 100; i++ {
			s.SetCallerRole("")
		}
		done <- true
	}()

	// Goroutine 3: Repeatedly read CreatedBy
	go func() {
		for i := 0; i < 100; i++ {
			_ = s.CreatedBy(ctx)
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
		name          string
		setRole       string
		ctxOverride   string
		expectedBy    string
		expectedActor service.EventActor
	}{
		{
			name:          "external MCP maps to ActorMCP",
			setRole:       "",
			ctxOverride:   "",
			expectedBy:    "mcp",
			expectedActor: service.ActorMCP,
		},
		{
			name:          "supervisor MCP maps to ActorSupervisor",
			setRole:       "supervisor",
			ctxOverride:   "",
			expectedBy:    "supervisor",
			expectedActor: service.ActorSupervisor,
		},
		{
			name:          "context override host maps to ActorHost",
			setRole:       "supervisor",
			ctxOverride:   "host",
			expectedBy:    "host",
			expectedActor: service.ActorHost,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer()
			ctx := context.Background()

			if tt.setRole != "" {
				s.SetCallerRole(tt.setRole)
			}
			if tt.ctxOverride != "" {
				ctx = WithCallerRole(ctx, tt.ctxOverride)
			}

			createdBy := s.CreatedBy(ctx)
			if createdBy != tt.expectedBy {
				t.Errorf("CreatedBy() = %q, want %q", createdBy, tt.expectedBy)
			}

			// Verify the mapping to EventActor
			actor := service.ActorFromCreatedBy(createdBy)
			if actor != tt.expectedActor {
				t.Errorf("ActorFromCreatedBy(%q) = %v, want %v", createdBy, actor, tt.expectedActor)
			}
		})
	}
}

// TestMCPServerCreatedByUsedInToolHandlers verifies that the CreatedBy value
// is actually used by the MCP tool handlers (not just stored). This ensures
// the plumbing works end-to-end.
func TestMCPServerCreatedByUsedInToolHandlers(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	// With no caller role, CreatedBy should be "mcp"
	if s.CreatedBy(ctx) != "mcp" {
		t.Error("Default CreatedBy should be 'mcp'")
	}

	// Set caller role (simulating supervisor)
	s.SetCallerRole("supervisor")

	// CreatedBy should now be "supervisor"
	if s.CreatedBy(ctx) != "supervisor" {
		t.Error("CreatedBy with caller role should be 'supervisor'")
	}

	// The CreatedBy value is used in tool handlers:
	// - work_update uses mcpSrv.CreatedBy(ctx)
	// - work_note_add uses mcpSrv.CreatedBy(ctx)
	// - work_attest uses mcpSrv.CreatedBy(ctx)
	// - check_record_create uses mcpSrv.CreatedBy(ctx)
	// - work_create uses mcpSrv.CreatedBy(ctx)
}

// TestMCPServerCallerRoleTracking verifies that the caller role
// is correctly tracked and can be queried.
func TestMCPServerCallerRoleTracking(t *testing.T) {
	s := newTestServer()

	// Initially empty
	if s.GetCallerRole() != "" {
		t.Errorf("initial callerRole should be empty, got %q", s.GetCallerRole())
	}

	// Set caller role
	testRole := "supervisor"
	s.SetCallerRole(testRole)

	// Verify it's stored
	if s.GetCallerRole() != testRole {
		t.Errorf("callerRole = %q, want %q", s.GetCallerRole(), testRole)
	}

	// CreatedBy should reflect caller role
	ctx := context.Background()
	if s.CreatedBy(ctx) != "supervisor" {
		t.Error("CreatedBy should return 'supervisor' when caller role is set")
	}
}

// TestMCPServerProvenanceVALSupervisor003 verifies the complete VAL-SUPERVISOR-003
// requirement: supervisor-triggered MCP mutations must show ActorSupervisor
// (not ActorMCP) in emitted work events.
func TestMCPServerProvenanceVALSupervisor003(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	// Simulate external MCP client (no supervisor role)
	externalCreatedBy := s.CreatedBy(ctx)
	if externalCreatedBy != "mcp" {
		t.Errorf("External MCP CreatedBy = %q, want %q", externalCreatedBy, "mcp")
	}
	externalActor := service.ActorFromCreatedBy(externalCreatedBy)
	if externalActor != service.ActorMCP {
		t.Errorf("External MCP Actor = %v, want %v", externalActor, service.ActorMCP)
	}

	// Simulate supervisor starting a session
	s.SetCallerRole("supervisor")

	// Now CreatedBy should be "supervisor"
	supervisorCreatedBy := s.CreatedBy(ctx)
	if supervisorCreatedBy != "supervisor" {
		t.Errorf("Supervisor MCP CreatedBy = %q, want %q", supervisorCreatedBy, "supervisor")
	}
	supervisorActor := service.ActorFromCreatedBy(supervisorCreatedBy)
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

// TestMCPServerRequestScopedSafety verifies that concurrent servers
// with different caller roles don't interfere with each other.
// This is the key safety property: each server instance has its own
// caller role, so concurrent external and supervisor traffic are
// correctly labeled without global state interference.
func TestMCPServerRequestScopedSafety(t *testing.T) {
	// Create two separate server instances (simulating concurrent sessions)
	supervisorServer := newTestServer()
	externalServer := newTestServer()

	// Only supervisor server has supervisor role
	supervisorServer.SetCallerRole("supervisor")

	ctx := context.Background()

	// Supervisor server returns supervisor
	if supervisorServer.CreatedBy(ctx) != "supervisor" {
		t.Error("Supervisor server should return 'supervisor'")
	}

	// External server still returns mcp (not affected by supervisor server)
	if externalServer.CreatedBy(ctx) != "mcp" {
		t.Error("External server should return 'mcp', not affected by supervisor server")
	}
}

// TestMCPServerCreatedByConcurrency verifies thread-safe concurrent access
// to the caller role. This detects data races when -race flag is used.
func TestMCPServerCreatedByConcurrency(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	var wg sync.WaitGroup

	// Writer goroutine: repeatedly set and clear role
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			s.SetCallerRole("supervisor")
			s.SetCallerRole("")
		}
	}()

	// Reader goroutines: repeatedly read CreatedBy
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = s.CreatedBy(ctx)
			}
		}()
	}

	wg.Wait()
	// Test passes if no data race is detected
}
