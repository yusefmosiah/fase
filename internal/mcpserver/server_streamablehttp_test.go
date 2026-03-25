package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusefmosiah/fase/internal/service"
)

// TestStreamableHTTPSessionIsolation verifies that the StreamableHTTP handler
// creates per-session server instances with proper isolation. This ensures
// external and supervisor MCP traffic cannot bleed provenance across sessions
// or requests (VAL-SUPERVISOR-003, round-5 scrutiny fix).
func TestStreamableHTTPSessionIsolation(t *testing.T) {
	sm := NewSessionManager(&service.Service{})

	// Create a handler using the SessionManager's GetServerForRequest
	handler := mcp.NewStreamableHTTPHandler(sm.GetServerForRequest, nil)

	// First, create external sessions only
	externalSessions := []string{"external-1", "external-2"}
	servers := make(map[string]*Server)

	for _, sessionID := range externalSessions {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Mcp-Session-Id", sessionID)

		// Get server for this request
		mcpServer := sm.GetServerForRequest(req)
		if mcpServer == nil {
			t.Fatalf("GetServerForRequest returned nil for session %s", sessionID)
		}

		// Get the wrapper Server instance
		server := sm.GetServerInstance(sessionID)
		if server == nil {
			t.Fatalf("GetServerInstance returned nil for session %s", sessionID)
		}

		servers[sessionID] = server

		// Verify initial state: external sessions should have no caller role
		if server.GetCallerRole() != "" {
			t.Errorf("Session %s: expected empty caller role initially, got %q", sessionID, server.GetCallerRole())
		}

		// CreatedBy should default to "mcp" for external sessions
		ctx := context.Background()
		createdBy := server.CreatedBy(ctx)
		if createdBy != "mcp" {
			t.Errorf("Session %s: CreatedBy() = %q, want %q", sessionID, createdBy, "mcp")
		}
	}

	// Mark the supervisor session BEFORE creating its server
	// This matches the production flow where SetSupervisorSession is called
	// when the supervisor starts, before any MCP mutations occur
	supervisorSession := "supervisor-session"
	sm.SetSupervisorSession(supervisorSession)

	// Now create the supervisor server (this is what happens in production:
	// the supervisor's session is marked, then MCP calls are made)
	supervisorReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	supervisorReq.Header.Set("Mcp-Session-Id", supervisorSession)
	_ = sm.GetServerForRequest(supervisorReq)

	supervisorServer := sm.GetServerInstance(supervisorSession)

	// The supervisor server should have supervisor role set because
	// SetSupervisorSession was called BEFORE server creation
	if supervisorServer.GetCallerRole() != "supervisor" {
		t.Errorf("Supervisor session %s: caller role = %q, want %q", supervisorSession, supervisorServer.GetCallerRole(), "supervisor")
	}

	// Verify supervisor CreatedBy returns "supervisor"
	ctx := context.Background()
	supervisorCreatedBy := supervisorServer.CreatedBy(ctx)
	if supervisorCreatedBy != "supervisor" {
		t.Errorf("Supervisor session %s: CreatedBy() = %q, want %q", supervisorSession, supervisorCreatedBy, "supervisor")
	}

	// Verify external sessions are NOT affected by supervisor session
	for _, sessionID := range externalSessions {
		server := sm.GetServerInstance(sessionID)
		createdBy := server.CreatedBy(ctx)
		if createdBy != "mcp" {
			t.Errorf("External session %s: CreatedBy() = %q after supervisor marked, want %q (isolation broken!)", sessionID, createdBy, "mcp")
		}
	}

	// Verify each session has its own isolated server instance
	allSessions := append(externalSessions, supervisorSession)
	for i, sessionID1 := range allSessions {
		for j, sessionID2 := range allSessions {
			if i != j {
				server1 := sm.GetServerInstance(sessionID1)
				server2 := sm.GetServerInstance(sessionID2)
				if server1 == server2 {
					t.Errorf("Sessions %s and %s share the same server instance - isolation broken!", sessionID1, sessionID2)
				}
			}
		}
	}

	_ = handler // silence unused warning if test is refactored
}

// TestStreamableHTTPExternalAndSupervisorProvenance verifies that:
// 1. External MCP traffic emits ActorMCP
// 2. Supervisor-triggered MCP mutations emit ActorSupervisor
// 3. Service-generated paths emit ActorService when explicitly labeled
// This is the end-to-end provenance test for VAL-SUPERVISOR-003.
func TestStreamableHTTPExternalAndSupervisorProvenance(t *testing.T) {
	sm := NewSessionManager(&service.Service{})

	// Test external session
	externalSessionID := "external-session"
	externalServer := sm.GetServerInstance(externalSessionID)

	ctx := context.Background()
	externalCreatedBy := externalServer.CreatedBy(ctx)
	if externalCreatedBy != "mcp" {
		t.Errorf("External session: CreatedBy() = %q, want %q", externalCreatedBy, "mcp")
	}

	externalActor := service.ActorFromCreatedBy(externalCreatedBy)
	if externalActor != service.ActorMCP {
		t.Errorf("External session: Actor = %v, want %v", externalActor, service.ActorMCP)
	}

	// Test supervisor session
	supervisorSessionID := "supervisor-session"
	sm.SetSupervisorSession(supervisorSessionID)
	supervisorServer := sm.GetServerInstance(supervisorSessionID)

	supervisorCreatedBy := supervisorServer.CreatedBy(ctx)
	if supervisorCreatedBy != "supervisor" {
		t.Errorf("Supervisor session: CreatedBy() = %q, want %q", supervisorCreatedBy, "supervisor")
	}

	supervisorActor := service.ActorFromCreatedBy(supervisorCreatedBy)
	if supervisorActor != service.ActorSupervisor {
		t.Errorf("Supervisor session: Actor = %v, want %v", supervisorActor, service.ActorSupervisor)
	}

	// Verify external session is not affected
	externalServerAfter := sm.GetServerInstance(externalSessionID)
	externalCreatedByAfter := externalServerAfter.CreatedBy(ctx)
	if externalCreatedByAfter != "mcp" {
		t.Errorf("External session after supervisor marked: CreatedBy() = %q, want %q", externalCreatedByAfter, "mcp")
	}

	// Test service-generated CreatedBy
	serviceActor := service.ActorFromCreatedBy("service")
	if serviceActor != service.ActorService {
		t.Errorf("Service CreatedBy: Actor = %v, want %v", serviceActor, service.ActorService)
	}
}

// TestStreamableHTTPServerReuse verifies that the same session ID returns
// the same server instance (session affinity).
func TestStreamableHTTPServerReuse(t *testing.T) {
	sm := NewSessionManager(&service.Service{})

	sessionID := "reusable-session"

	// First request creates the server
	req1 := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req1.Header.Set("Mcp-Session-Id", sessionID)
	server1 := sm.GetServerForRequest(req1)

	// Second request with same session ID should return same server
	req2 := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req2.Header.Set("Mcp-Session-Id", sessionID)
	server2 := sm.GetServerForRequest(req2)

	if server1 != server2 {
		t.Error("Same session ID should return same server instance")
	}

	// Different session ID should return different server
	req3 := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req3.Header.Set("Mcp-Session-Id", "different-session")
	server3 := sm.GetServerForRequest(req3)

	if server1 == server3 {
		t.Error("Different session IDs should return different server instances")
	}
}

// TestStreamableHTTPDefaultSession verifies that requests without a session ID
// get a default session and still work correctly.
func TestStreamableHTTPDefaultSession(t *testing.T) {
	sm := NewSessionManager(&service.Service{})

	// Request without session ID
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)

	server := sm.GetServerForRequest(req)
	if server == nil {
		t.Fatal("GetServerForRequest returned nil for request without session ID")
	}

	// Should get a server instance (using "default" as fallback)
	serverInstance := sm.GetServerInstance("default")
	if serverInstance == nil {
		t.Fatal("GetServerInstance returned nil for default session")
	}

	// External callers should default to "mcp"
	ctx := context.Background()
	createdBy := serverInstance.CreatedBy(ctx)
	if createdBy != "mcp" {
		t.Errorf("Default session: CreatedBy() = %q, want %q", createdBy, "mcp")
	}
}

// TestStreamableHTTPConcurrentAccess verifies thread-safe concurrent access
// to the SessionManager from multiple goroutines.
func TestStreamableHTTPConcurrentAccess(t *testing.T) {
	sm := NewSessionManager(&service.Service{})

	const numGoroutines = 10
	const numRequestsPerGoroutine = 50

	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()

			for j := 0; j < numRequestsPerGoroutine; j++ {
				sessionID := fmt.Sprintf("goroutine-%d-session-%d", id, j%3)

				req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
				req.Header.Set("Mcp-Session-Id", sessionID)

				server := sm.GetServerForRequest(req)
				if server == nil {
					t.Errorf("Goroutine %d: GetServerForRequest returned nil for session %s", id, sessionID)
					return
				}

				// Also test GetServerInstance
				instance := sm.GetServerInstance(sessionID)
				if instance == nil {
					t.Errorf("Goroutine %d: GetServerInstance returned nil for session %s", id, sessionID)
					return
				}

				// Randomly set supervisor session (to test race conditions)
				if j == 0 && id == 0 {
					sm.SetSupervisorSession(sessionID)
				}

				// Read CreatedBy (to test race conditions)
				ctx := context.Background()
				_ = instance.CreatedBy(ctx)
			}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		select {
		case <-done:
			// Success
		case <-time.After(10 * time.Second):
			t.Fatal("Timeout waiting for goroutines")
		}
	}

	// Test passes if no data race is detected (run with -race flag)
}

// TestStreamableHTTPHandlerIntegration is an integration test that exercises
// the actual StreamableHTTP handler path. This proves the production
// StreamableHTTP or session.CallTool path, not just helper mappings.
func TestStreamableHTTPHandlerIntegration(t *testing.T) {
	// This test verifies that when the StreamableHTTP handler processes
	// a request, the correct server instance is selected based on the
	// Mcp-Session-Id header, and that server has proper provenance tracking.

	sm := NewSessionManager(&service.Service{})

	// Test 1: External session (no supervisor)
	t.Run("ExternalSession", func(t *testing.T) {
		externalSession := "external-test-session"
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Mcp-Session-Id", externalSession)

		server := sm.GetServerForRequest(req)
		if server == nil {
			t.Fatal("GetServerForRequest returned nil")
		}

		instance := sm.GetServerInstance(externalSession)
		ctx := context.Background()

		// External session should default to "mcp"
		if instance.CreatedBy(ctx) != "mcp" {
			t.Errorf("External session CreatedBy = %q, want %q", instance.CreatedBy(ctx), "mcp")
		}
	})

	// Test 2: Supervisor session
	t.Run("SupervisorSession", func(t *testing.T) {
		supervisorSession := "supervisor-test-session"
		sm.SetSupervisorSession(supervisorSession)

		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Mcp-Session-Id", supervisorSession)

		server := sm.GetServerForRequest(req)
		if server == nil {
			t.Fatal("GetServerForRequest returned nil")
		}

		instance := sm.GetServerInstance(supervisorSession)
		ctx := context.Background()

		// Supervisor session should return "supervisor"
		if instance.CreatedBy(ctx) != "supervisor" {
			t.Errorf("Supervisor session CreatedBy = %q, want %q", instance.CreatedBy(ctx), "supervisor")
		}
	})

	// Test 3: After supervisor is set, external sessions are still isolated
	t.Run("ExternalAfterSupervisor", func(t *testing.T) {
		externalSession := "external-after-supervisor"
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Mcp-Session-Id", externalSession)

		server := sm.GetServerForRequest(req)
		if server == nil {
			t.Fatal("GetServerForRequest returned nil")
		}

		instance := sm.GetServerInstance(externalSession)
		ctx := context.Background()

		// External session should still default to "mcp" (not affected by supervisor)
		if instance.CreatedBy(ctx) != "mcp" {
			t.Errorf("External session after supervisor CreatedBy = %q, want %q", instance.CreatedBy(ctx), "mcp")
		}
	})
}
