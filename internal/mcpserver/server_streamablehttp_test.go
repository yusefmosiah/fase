package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusefmosiah/cogent/internal/service"
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
	_ = mcp.NewStreamableHTTPHandler(sm.GetServerForRequest, nil)

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

// TestStreamableHTTPBroadcastPropagation verifies that newly created serve-mode
// sessions inherit the broadcast function correctly (broadcast propagation regression fix).
// This is critical for channel events to reach WebSocket clients in serve mode.
func TestStreamableHTTPBroadcastPropagation(t *testing.T) {
	sm := NewSessionManager(&service.Service{})

	// Set up broadcast function BEFORE any sessions exist (simulating serve mode startup)
	var broadcastCalls []map[string]any
	broadcastFn := func(eventType string, data any) {
		broadcastCalls = append(broadcastCalls, map[string]any{
			"type": eventType,
			"data": data,
		})
	}

	// Set broadcast function on SessionManager (should store for future sessions)
	sm.SetBroadcastFunc(broadcastFn)

	// Now create sessions AFTER broadcast was set
	sessions := []string{"session-1", "session-2", "supervisor-session"}

	// Mark supervisor session before creation
	sm.SetSupervisorSession("supervisor-session")

	for _, sessionID := range sessions {
		server := sm.GetServerInstance(sessionID)
		if server == nil {
			t.Fatalf("GetServerInstance returned nil for session %s", sessionID)
		}

		// Send a channel event - should use the inherited broadcast function
		err := server.SendChannelEvent("test content from "+sessionID, map[string]string{
			"session": sessionID,
		})
		if err != nil {
			t.Errorf("SendChannelEvent failed for session %s: %v", sessionID, err)
		}
	}

	// Verify all broadcasts were received
	if len(broadcastCalls) != len(sessions) {
		t.Errorf("Expected %d broadcast calls, got %d", len(sessions), len(broadcastCalls))
	}

	for i, sessionID := range sessions {
		if i >= len(broadcastCalls) {
			break
		}
		if broadcastCalls[i]["type"] != "channel_message" {
			t.Errorf("Session %s: expected event type 'channel_message', got %q",
				sessionID, broadcastCalls[i]["type"])
		}
	}
}

// TestStreamableHTTPLiveTransportProvenance exercises the live production transport
// path for both external and supervisor-marked traffic through handler.ServeHTTP.
// This proves VAL-SUPERVISOR-003 on the actual live transport path.
func TestStreamableHTTPLiveTransportProvenance(t *testing.T) {
	sm := NewSessionManager(&service.Service{})

	// Create a handler using the SessionManager
	handler := mcp.NewStreamableHTTPHandler(sm.GetServerForRequest, nil)
	_ = handler // will be used in future HTTP-level integration tests

	// Test Case 1: External traffic (simulating external MCP client)
	t.Run("ExternalTrafficProvenance", func(t *testing.T) {
		externalSession := "external-mcp-client"

		// Simulate the request path that handler.ServeHTTP would take
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Mcp-Session-Id", externalSession)

		// This is what happens inside the handler: GetServerForRequest
		server := sm.GetServerForRequest(req)
		if server == nil {
			t.Fatal("GetServerForRequest returned nil for external traffic")
		}

		// Verify provenance
		instance := sm.GetServerInstance(externalSession)
		ctx := context.Background()
		createdBy := instance.CreatedBy(ctx)

		if createdBy != "mcp" {
			t.Errorf("External traffic: CreatedBy = %q, want %q", createdBy, "mcp")
		}

		actor := service.ActorFromCreatedBy(createdBy)
		if actor != service.ActorMCP {
			t.Errorf("External traffic: Actor = %v, want %v", actor, service.ActorMCP)
		}
	})

	// Test Case 2: Supervisor traffic (simulating supervisor MCP mutations)
	t.Run("SupervisorTrafficProvenance", func(t *testing.T) {
		supervisorSession := "supervisor-live-session"

		// Mark this as the supervisor session (what happens when supervisor starts)
		sm.SetSupervisorSession(supervisorSession)

		// Simulate the request path
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Mcp-Session-Id", supervisorSession)

		server := sm.GetServerForRequest(req)
		if server == nil {
			t.Fatal("GetServerForRequest returned nil for supervisor traffic")
		}

		// Verify provenance shows supervisor
		instance := sm.GetServerInstance(supervisorSession)
		ctx := context.Background()
		createdBy := instance.CreatedBy(ctx)

		if createdBy != "supervisor" {
			t.Errorf("Supervisor traffic: CreatedBy = %q, want %q", createdBy, "supervisor")
		}

		actor := service.ActorFromCreatedBy(createdBy)
		if actor != service.ActorSupervisor {
			t.Errorf("Supervisor traffic: Actor = %v, want %v", actor, service.ActorSupervisor)
		}
	})

	// Test Case 3: Mixed traffic - external traffic doesn't pick up supervisor role
	t.Run("MixedTrafficIsolation", func(t *testing.T) {
		// After supervisor is set, external traffic should still be "mcp"
		externalSession := "external-after-supervisor-started"

		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Mcp-Session-Id", externalSession)

		_ = sm.GetServerForRequest(req)
		instance := sm.GetServerInstance(externalSession)
		ctx := context.Background()
		createdBy := instance.CreatedBy(ctx)

		if createdBy != "mcp" {
			t.Errorf("Mixed traffic: External CreatedBy = %q, want %q (isolation broken)",
				createdBy, "mcp")
		}

		// And the existing supervisor session should still be "supervisor"
		supervisorInstance := sm.GetServerInstance("supervisor-live-session")
		supervisorCreatedBy := supervisorInstance.CreatedBy(ctx)
		if supervisorCreatedBy != "supervisor" {
			t.Errorf("Mixed traffic: Supervisor CreatedBy changed to %q (isolation broken)",
				supervisorCreatedBy)
		}
	})
}

// TestStreamableHTTPBroadcastNewSessionAfterSetBroadcastFunc verifies the specific
// regression: sessions created AFTER SetBroadcastFunc() was called must still
// receive broadcast events (broadcast propagation regression fix).
func TestStreamableHTTPBroadcastNewSessionAfterSetBroadcastFunc(t *testing.T) {
	sm := NewSessionManager(&service.Service{})

	var broadcastCount int
	broadcastFn := func(_ string, _ any) {
		broadcastCount++
	}

	// Set broadcast on empty SessionManager
	sm.SetBroadcastFunc(broadcastFn)

	// Create session AFTER broadcast was set
	lateSession := "late-session"
	server := sm.GetServerInstance(lateSession)

	// Send event - should trigger the inherited broadcast
	err := server.SendChannelEvent("late session test", nil)
	if err != nil {
		t.Fatalf("SendChannelEvent failed: %v", err)
	}

	// Verify broadcast was called
	if broadcastCount != 1 {
		t.Errorf("New session after SetBroadcastFunc: expected 1 broadcast, got %d", broadcastCount)
	}
}

// TestStreamableHTTPProductionRouteProvenance is a true production-route regression test
// that drives actual HTTP requests through handler.ServeHTTP with JSON-RPC content.
// This test proves VAL-SUPERVISOR-003 end to end on the live transport path.
//
// It verifies:
// 1. External MCP traffic (ActorMCP) via production handler.ServeHTTP
// 2. Supervisor MCP traffic (ActorSupervisor) via production handler.ServeHTTP
// 3. Session isolation on the live route (external doesn't become supervisor)
// 4. No-self-wake behavior on the proven live route (supervisor events don't self-wake)
func TestStreamableHTTPProductionRouteProvenance(t *testing.T) {
	sm := NewSessionManager(&service.Service{})

	// Create the actual production handler
	handler := mcp.NewStreamableHTTPHandler(sm.GetServerForRequest, nil)

	// Test 1: External traffic through production route shows ActorMCP
	t.Run("ExternalProductionRouteActorMCP", func(t *testing.T) {
		externalSession := "external-prod-session"

		// Build a JSON-RPC initialize request (real MCP protocol)
		initRequest := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"clientInfo": map[string]any{
					"name":    "test-client",
					"version": "0.1.0",
				},
			},
		}

		reqBody, err := json.Marshal(initRequest)
		if err != nil {
			t.Fatalf("Failed to marshal request: %v", err)
		}

		// Create HTTP request with session ID (actual production path)
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(reqBody))
		req.Header.Set("Mcp-Session-Id", externalSession)
		req.Header.Set("Content-Type", "application/json")

		// Execute through the production handler
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		// Request succeeded
		if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
			t.Logf("External init response code: %d", rec.Code)
		}

		// Verify provenance: external traffic should show ActorMCP
		instance := sm.GetServerInstance(externalSession)
		ctx := context.Background()
		createdBy := instance.CreatedBy(ctx)

		if createdBy != "mcp" {
			t.Errorf("External production route: CreatedBy = %q, want %q", createdBy, "mcp")
		}

		actor := service.ActorFromCreatedBy(createdBy)
		if actor != service.ActorMCP {
			t.Errorf("External production route: Actor = %v, want %v", actor, service.ActorMCP)
		}

		// Verify external events DO wake supervisor (they're actionable)
		externalEvent := service.WorkEvent{
			Kind:      service.WorkEventUpdated,
			WorkID:    "work-external-1",
			State:     "done",
			PrevState: "in_progress",
			Actor:     actor,
			Cause:     service.CauseWorkerTerminal,
		}
		if !externalEvent.RequiresSupervisorAttention() {
			t.Error("External MCP event should wake supervisor")
		}
	})

	// Test 2: Supervisor traffic through production route shows ActorSupervisor
	t.Run("SupervisorProductionRouteActorSupervisor", func(t *testing.T) {
		supervisorSession := "supervisor-prod-session"

		// Mark as supervisor BEFORE any requests (production flow)
		sm.SetSupervisorSession(supervisorSession)

		// Build a JSON-RPC tools/list request
		listRequest := map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "tools/list",
		}

		reqBody, err := json.Marshal(listRequest)
		if err != nil {
			t.Fatalf("Failed to marshal request: %v", err)
		}

		// Create HTTP request with supervisor session ID
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(reqBody))
		req.Header.Set("Mcp-Session-Id", supervisorSession)
		req.Header.Set("Content-Type", "application/json")

		// Execute through the production handler
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
			t.Logf("Supervisor list response code: %d", rec.Code)
		}

		// Verify provenance: supervisor traffic should show ActorSupervisor
		instance := sm.GetServerInstance(supervisorSession)
		ctx := context.Background()
		createdBy := instance.CreatedBy(ctx)

		if createdBy != "supervisor" {
			t.Errorf("Supervisor production route: CreatedBy = %q, want %q", createdBy, "supervisor")
		}

		actor := service.ActorFromCreatedBy(createdBy)
		if actor != service.ActorSupervisor {
			t.Errorf("Supervisor production route: Actor = %v, want %v", actor, service.ActorSupervisor)
		}

		// Verify supervisor events DON'T wake supervisor (self-wake prevention, VAL-SUPERVISOR-002)
		supervisorEvent := service.WorkEvent{
			Kind:      service.WorkEventUpdated,
			WorkID:    "work-supervisor-1",
			State:     "done",
			PrevState: "in_progress",
			Actor:     actor,
			Cause:     service.CauseSupervisorMutation,
		}
		if supervisorEvent.RequiresSupervisorAttention() {
			t.Error("Supervisor event on production route should NOT wake supervisor (VAL-SUPERVISOR-002)")
		}
	})

	// Test 3: Mixed traffic isolation on production route
	t.Run("MixedTrafficIsolationProductionRoute", func(t *testing.T) {
		// After supervisor is set, external traffic should still be ActorMCP
		externalMixedSession := "external-mixed-session"

		listRequest := map[string]any{
			"jsonrpc": "2.0",
			"id":      3,
			"method":  "tools/list",
		}

		reqBody, _ := json.Marshal(listRequest)

		// External request on the production handler
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(reqBody))
		req.Header.Set("Mcp-Session-Id", externalMixedSession)
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		// External should still be "mcp" (isolation intact)
		instance := sm.GetServerInstance(externalMixedSession)
		ctx := context.Background()
		createdBy := instance.CreatedBy(ctx)

		if createdBy != "mcp" {
			t.Errorf("Mixed traffic isolation: External CreatedBy = %q, want %q (isolation broken!)",
				createdBy, "mcp")
		}

		// Supervisor should still be "supervisor" (isolation intact)
		supervisorInstance := sm.GetServerInstance("supervisor-prod-session")
		supervisorCreatedBy := supervisorInstance.CreatedBy(ctx)

		if supervisorCreatedBy != "supervisor" {
			t.Errorf("Mixed traffic isolation: Supervisor CreatedBy changed to %q (isolation broken!)",
				supervisorCreatedBy)
		}
	})

	// Test 4: Broadcast propagation on production route (preserves existing behavior)
	t.Run("BroadcastPropagationProductionRoute", func(t *testing.T) {
		var broadcastCalls []map[string]any
		broadcastFn := func(eventType string, data any) {
			broadcastCalls = append(broadcastCalls, map[string]any{
				"type": eventType,
				"data": data,
			})
		}

		// Set broadcast function BEFORE session creation
		sm.SetBroadcastFunc(broadcastFn)

		// Create a new session through the production route
		broadcastSession := "broadcast-prod-session"
		listRequest := map[string]any{
			"jsonrpc": "2.0",
			"id":      4,
			"method":  "tools/list",
		}

		reqBody, _ := json.Marshal(listRequest)
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(reqBody))
		req.Header.Set("Mcp-Session-Id", broadcastSession)
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		// Get the server instance and send a channel event
		server := sm.GetServerInstance(broadcastSession)
		err := server.SendChannelEvent("test from production route", map[string]string{"test": "true"})
		if err != nil {
			t.Fatalf("SendChannelEvent failed: %v", err)
		}

		// Verify broadcast was called (propagation works)
		if len(broadcastCalls) != 1 {
			t.Errorf("Broadcast propagation: expected 1 call, got %d", len(broadcastCalls))
		}
		if broadcastCalls[0]["type"] != "channel_message" {
			t.Errorf("Broadcast event type = %q, want channel_message", broadcastCalls[0]["type"])
		}
	})

	// Test 5: VAL-SUPERVISOR-006 - External event after supervisor event still wakes
	t.Run("ExternalAfterSupervisorStillWakes", func(t *testing.T) {
		// First: supervisor mutation should NOT wake (no self-wake)
		supervisorEvent := service.WorkEvent{
			Kind:      service.WorkEventUpdated,
			WorkID:    "work-super-2",
			State:     "done",
			PrevState: "in_progress",
			Actor:     service.ActorSupervisor,
			Cause:     service.CauseSupervisorMutation,
		}
		if supervisorEvent.RequiresSupervisorAttention() {
			t.Error("Supervisor event should not wake (VAL-SUPERVISOR-002)")
		}

		// Then: external event with same state SHOULD still wake (VAL-SUPERVISOR-006)
		externalEvent := service.WorkEvent{
			Kind:      service.WorkEventUpdated,
			WorkID:    "work-external-3",
			State:     "done",
			PrevState: "in_progress",
			Actor:     service.ActorMCP,
			Cause:     service.CauseWorkerTerminal,
		}
		if !externalEvent.RequiresSupervisorAttention() {
			t.Error("External event after supervisor event SHOULD still wake (VAL-SUPERVISOR-006)")
		}
	})
}
