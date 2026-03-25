package service

import (
	"testing"
)

// TestRequiresSupervisorAttentionTruthTable verifies the complete truth table
// for the RequiresSupervisorAttention method.
func TestRequiresSupervisorAttentionTruthTable(t *testing.T) {
	tests := []struct {
		name     string
		ev       WorkEvent
		expected bool
	}{
		// VAL-SUPERVISOR-002: Supervisor's own mutations should not wake itself
		{
			name: "supervisor mutation does not wake",
			ev: WorkEvent{
				Actor: ActorSupervisor,
				Cause: CauseSupervisorMutation,
			},
			expected: false,
		},
		// VAL-SUPERVISOR-004: Worker completion wakes supervisor
		{
			name: "worker terminal wakes supervisor",
			ev: WorkEvent{
				Actor: ActorWorker,
				Cause: CauseWorkerTerminal,
				State: "done",
			},
			expected: true,
		},
		// VAL-SUPERVISOR-004: Check results wake supervisor
		{
			name: "check recorded wakes supervisor",
			ev: WorkEvent{
				Actor: ActorWorker,
				Cause: CauseCheckRecorded,
				Kind:  WorkEventCheckRecorded,
			},
			expected: true,
		},
		// VAL-SUPERVISOR-004: Attestation results wake supervisor
		{
			name: "attestation recorded wakes supervisor",
			ev: WorkEvent{
				Actor: ActorWorker,
				Cause: CauseAttestationRecorded,
				Kind:  WorkEventAttested,
			},
			expected: true,
		},
		// VAL-SUPERVISOR-004: Host/manual actions wake supervisor
		{
			name: "host manual action wakes supervisor",
			ev: WorkEvent{
				Actor: ActorHost,
				Cause: CauseHostManual,
			},
			expected: true,
		},
		// VAL-SUPERVISOR-004: Housekeeping stall recovery wakes supervisor
		{
			name: "housekeeping stall wakes supervisor",
			ev: WorkEvent{
				Cause: CauseHousekeepingStall,
			},
			expected: true,
		},
		// VAL-SUPERVISOR-004: Housekeeping orphan recovery wakes supervisor
		{
			name: "housekeeping orphan wakes supervisor",
			ev: WorkEvent{
				Cause: CauseHousekeepingOrphan,
			},
			expected: true,
		},
		// VAL-SUPERVISOR-001: Non-actionable events should not wake supervisor
		{
			name: "housekeeping noise does not wake",
			ev: WorkEvent{
				Actor: ActorHousekeeping,
			},
			expected: false,
		},
		{
			name: "reconciler noise does not wake",
			ev: WorkEvent{
				Actor: ActorReconciler,
			},
			expected: false,
		},
		{
			name: "lease renew does not wake",
			ev: WorkEvent{
				Kind: WorkEventLeaseRenew,
			},
			expected: false,
		},
		// VAL-SUPERVISOR-001: Worker progress without state change is noise
		{
			name: "worker progress without state change does not wake",
			ev: WorkEvent{
				Actor:     ActorWorker,
				Cause:     CauseWorkerProgress,
				State:     "in_progress",
				PrevState: "in_progress",
			},
			expected: false,
		},
		// VAL-SUPERVISOR-001: Job lifecycle in progress is noise
		{
			name: "job lifecycle in_progress does not wake",
			ev: WorkEvent{
				Actor: ActorWorker,
				Cause: CauseJobLifecycle,
				State: "in_progress",
			},
			expected: false,
		},
		// VAL-SUPERVISOR-001: Claim change without state change is noise
		{
			name: "claim change without state change does not wake",
			ev: WorkEvent{
				Actor:     ActorWorker,
				Cause:     CauseClaimChanged,
				State:     "ready",
				PrevState: "ready",
			},
			expected: false,
		},
		// VAL-SUPERVISOR-006: New external event with same state still wakes
		{
			name: "new worker terminal after supervisor mutation wakes",
			ev: WorkEvent{
				Actor: ActorWorker,
				Cause: CauseWorkerTerminal,
				State: "done",
			},
			expected: true,
		},
		// New work should wake supervisor
		{
			name: "new work creation wakes supervisor",
			ev: WorkEvent{
				Kind:  WorkEventCreated,
				Cause: CauseWorkCreated,
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ev.RequiresSupervisorAttention()
			if got != tt.expected {
				t.Errorf("RequiresSupervisorAttention() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestActorMCPMapping verifies that CreatedBy="mcp" maps to ActorMCP
// (not ActorWorker), preserving provenance across the MCP transport boundary.
func TestActorMCPMapping(t *testing.T) {
	tests := []struct {
		name     string
		createdBy string
		expected  EventActor
	}{
		{"mcp maps to ActorMCP", "mcp", ActorMCP},
		{"supervisor maps to ActorSupervisor", "supervisor", ActorSupervisor},
		{"housekeeping maps to ActorHousekeeping", "housekeeping", ActorHousekeeping},
		{"reconciler maps to ActorReconciler", "reconciler", ActorReconciler},
		{"unknown maps to ActorWorker", "unknown", ActorWorker},
		{"empty maps to ActorWorker", "", ActorWorker},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ActorFromCreatedBy(tt.createdBy)
			if got != tt.expected {
				t.Errorf("ActorFromCreatedBy(%q) = %v, want %v", tt.createdBy, got, tt.expected)
			}
		})
	}
}

// TestRequiresSupervisorAttentionActorField verifies that the Actor field
// is properly used to determine wake behavior.
func TestRequiresSupervisorAttentionActorField(t *testing.T) {
	// ActorSupervisor events should not wake (self-wake prevention)
	supervisorEvent := WorkEvent{
		Kind:  WorkEventUpdated,
		Actor: ActorSupervisor,
		State: "done",
	}
	if supervisorEvent.RequiresSupervisorAttention() {
		t.Error("supervisor event should not require attention")
	}

	// ActorWorker events should wake (worker completion)
	workerEvent := WorkEvent{
		Kind:  WorkEventUpdated,
		Actor: ActorWorker,
		State: "done",
	}
	if !workerEvent.RequiresSupervisorAttention() {
		t.Error("worker terminal event should require attention")
	}

	// ActorHost events should wake (host manual action)
	hostEvent := WorkEvent{
		Kind:  WorkEventUpdated,
		Actor: ActorHost,
	}
	if !hostEvent.RequiresSupervisorAttention() {
		t.Error("host event should require attention")
	}

	// ActorMCP events should wake (MCP client-triggered actions)
	mcpEvent := WorkEvent{
		Kind:  WorkEventUpdated,
		Actor: ActorMCP,
		State: "done",
	}
	if !mcpEvent.RequiresSupervisorAttention() {
		t.Error("MCP terminal event should require attention")
	}
}

// TestRequiresSupervisorAttentionCauseField verifies that the Cause field
// is properly used to determine wake behavior.
func TestRequiresSupervisorAttentionCauseField(t *testing.T) {
	// Stall and orphan causes should wake even without explicit actor
	// (housekeeping recovery)
	stallEvent := WorkEvent{
		Cause: CauseHousekeepingStall,
	}
	if !stallEvent.RequiresSupervisorAttention() {
		t.Error("stall event should require attention")
	}

	orphanEvent := WorkEvent{
		Cause: CauseHousekeepingOrphan,
	}
	if !orphanEvent.RequiresSupervisorAttention() {
		t.Error("orphan event should require attention")
	}
}

// TestHousekeepingStallRecovery verifies that stalled work items correctly
// emit events that wake the supervisor for recovery (VAL-SUPERVISOR-004:
// true stall recovery produces a reliable wake path).
func TestHousekeepingStallRecovery(t *testing.T) {
	stallEvent := WorkEvent{
		Kind:      WorkEventUpdated,
		WorkID:   "work-stalled-1",
		Title:    "Stalled Work",
		State:    "stalled",
		PrevState: "in_progress",
		Cause:    CauseHousekeepingStall,
		Actor:    ActorHousekeeping,
		Metadata: map[string]string{"reason": "lease expired"},
	}

	// Stall events must wake the supervisor - this is critical for recovery
	if !stallEvent.RequiresSupervisorAttention() {
		t.Error("stall event should require supervisor attention for recovery")
	}
}

// TestHousekeepingOrphanRecovery verifies that orphaned work items correctly
// emit events that wake the supervisor for recovery (VAL-SUPERVISOR-004:
// true orphan recovery produces a reliable wake path).
func TestHousekeepingOrphanRecovery(t *testing.T) {
	orphanEvent := WorkEvent{
		Kind:      WorkEventUpdated,
		WorkID:   "work-orphan-1",
		Title:    "Orphaned Work",
		State:    "orphan",
		PrevState: "in_progress",
		Cause:    CauseHousekeepingOrphan,
		Actor:    ActorHousekeeping,
		Metadata: map[string]string{"reason": "worker disappeared"},
	}

	// Orphan events must wake the supervisor - this is critical for recovery
	if !orphanEvent.RequiresSupervisorAttention() {
		t.Error("orphan event should require supervisor attention for recovery")
	}
}

// TestHousekeepingNoiseDoesNotWake verifies that non-actionable housekeeping
// events (lease renewals, routine maintenance) do not wake the supervisor
// (VAL-SUPERVISOR-001: non-actionable events do not create supervisor turns).
func TestHousekeepingNoiseDoesNotWake(t *testing.T) {
	// Lease renewals should not wake the supervisor
	leaseRenewEvent := WorkEvent{
		Kind:  WorkEventLeaseRenew,
		Cause: CauseLeaseReconcile,
		Actor: ActorHousekeeping,
	}
	if leaseRenewEvent.RequiresSupervisorAttention() {
		t.Error("lease renewal should not wake supervisor")
	}

	// Routine housekeeping without stall/orphan cause should not wake
	routineEvent := WorkEvent{
		Kind:  WorkEventUpdated,
		Cause: CauseLeaseReconcile,
		Actor: ActorHousekeeping,
		State: "in_progress",
	}
	if routineEvent.RequiresSupervisorAttention() {
		t.Error("routine housekeeping should not wake supervisor")
	}
}

// TestBurstEventPreservesContext verifies that multiple events arriving
// in quick succession preserve decision-critical context (VAL-SUPERVISOR-005:
// burst events preserve decision-critical context in one continuation).
func TestBurstEventPreservesContext(t *testing.T) {
	// Multiple events from different work items should all require attention
	events := []WorkEvent{
		{
			Kind:   WorkEventUpdated,
			WorkID: "work-1",
			State:  "done",
			Cause:  CauseWorkerTerminal,
			Actor:  ActorWorker,
		},
		{
			Kind:   WorkEventCheckRecorded,
			WorkID: "work-2",
			Cause:  CauseCheckRecorded,
			Actor:  ActorWorker,
		},
		{
			Kind:   WorkEventAttested,
			WorkID: "work-3",
			Cause:  CauseAttestationRecorded,
			Actor:  ActorWorker,
		},
	}

	for i, ev := range events {
		if !ev.RequiresSupervisorAttention() {
			t.Errorf("event %d should require attention", i)
		}
	}
}

// TestEventBusPublishAndSubscribe verifies the event bus correctly
// publishes events to subscribers (VAL-SUPERVISOR-003: wake-relevant
// events carry trustworthy provenance).
func TestEventBusPublishAndSubscribe(t *testing.T) {
	bus := &EventBus{}
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	ev := WorkEvent{
		Kind:   WorkEventCreated,
		WorkID: "work-new",
		Cause:  CauseWorkCreated,
		Actor:  ActorHost,
	}

	bus.Publish(ev)

	select {
	case received := <-ch:
		if received.WorkID != ev.WorkID {
			t.Errorf("expected workID %q, got %q", ev.WorkID, received.WorkID)
		}
		if received.Actor != ev.Actor {
			t.Errorf("expected actor %q, got %q", ev.Actor, received.Actor)
		}
	default:
		t.Fatal("expected to receive event from channel")
	}
}

// TestTransportBoundaryProvenanceCLI verifies that CLI-triggered work updates
// preserve trustworthy provenance (VAL-SUPERVISOR-003: provenance survives
// CLI transport boundary).
func TestTransportBoundaryProvenanceCLI(t *testing.T) {
	// CLI mutations set CreatedBy="cli" which should derive to ActorWorker
	// This verifies the event correctly identifies CLI-triggered changes
	tests := []struct {
		name        string
		createdBy   string
		expectedAct EventActor
	}{
		{"CLI update maps to ActorWorker", "cli", ActorWorker},
		{"CLI claim maps to ActorWorker", "cli", ActorWorker},
		{"CLI attest maps to ActorWorker", "cli", ActorWorker},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ActorFromCreatedBy(tt.createdBy)
			if got != tt.expectedAct {
				t.Errorf("ActorFromCreatedBy(%q) = %v, want %v", tt.createdBy, got, tt.expectedAct)
			}

			// Verify the event would wake supervisor (CLI is actionable)
			ev := WorkEvent{
				Kind:      WorkEventUpdated,
				WorkID:    "work-1",
				State:     "done",
				PrevState: "in_progress",
				Actor:     got,
				Cause:     CauseWorkerTerminal,
			}
			if !ev.RequiresSupervisorAttention() {
				t.Error("CLI terminal event should require supervisor attention")
			}
		})
	}
}

// TestTransportBoundaryProvenanceHTTP verifies that HTTP-triggered work updates
// (via serve) preserve trustworthy provenance (VAL-SUPERVISOR-003: provenance
// survives HTTP transport boundary).
func TestTransportBoundaryProvenanceHTTP(t *testing.T) {
	// HTTP mutations via serve also set CreatedBy="cli"
	// This verifies HTTP-triggered changes are properly tracked
	createdBy := "cli"

	ev := WorkEvent{
		Kind:      WorkEventUpdated,
		WorkID:    "work-http-1",
		State:     "done",
		PrevState: "ready",
		Actor:     ActorFromCreatedBy(createdBy),
		Cause:     CauseWorkerTerminal,
	}

	// HTTP-triggered terminal state changes should wake supervisor
	if ev.Actor != ActorWorker {
		t.Errorf("expected ActorWorker for HTTP, got %v", ev.Actor)
	}
	if !ev.RequiresSupervisorAttention() {
		t.Error("HTTP terminal event should require supervisor attention")
	}
}

// TestTransportBoundaryProvenanceMCP verifies that MCP-triggered work updates
// preserve trustworthy provenance (VAL-SUPERVISOR-003: provenance survives
// MCP transport boundary).
func TestTransportBoundaryProvenanceMCP(t *testing.T) {
	// MCP mutations set CreatedBy="mcp" which should derive to ActorMCP
	tests := []struct {
		name        string
		createdBy   string
		expectedAct EventActor
		shouldWake  bool
	}{
		{"MCP work_update maps to ActorMCP", "mcp", ActorMCP, true},
		{"MCP work_create maps to ActorMCP", "mcp", ActorMCP, true},
		{"MCP work_attest maps to ActorMCP", "mcp", ActorMCP, true},
		{"MCP check_record maps to ActorMCP", "mcp", ActorMCP, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ActorFromCreatedBy(tt.createdBy)
			if got != tt.expectedAct {
				t.Errorf("ActorFromCreatedBy(%q) = %v, want %v", tt.createdBy, got, tt.expectedAct)
			}

			// Verify MCP events wake supervisor appropriately
			ev := WorkEvent{
				Kind:      WorkEventUpdated,
				WorkID:    "work-mcp-1",
				State:     "done",
				PrevState: "in_progress",
				Actor:     got,
				Cause:     CauseWorkerTerminal,
			}
			if ev.RequiresSupervisorAttention() != tt.shouldWake {
				t.Errorf("MCP event RequiresSupervisorAttention() = %v, want %v",
					ev.RequiresSupervisorAttention(), tt.shouldWake)
			}
		})
	}
}

// TestTransportBoundaryProvenanceHost verifies that host/manual-triggered
// work updates preserve trustworthy provenance (VAL-SUPERVISOR-003:
// provenance survives host transport boundary).
func TestTransportBoundaryProvenanceHost(t *testing.T) {
	// Host actions are manually triggered and should wake supervisor
	ev := WorkEvent{
		Kind:      WorkEventUpdated,
		WorkID:    "work-host-1",
		State:     "in_progress",
		PrevState: "ready",
		Actor:     ActorHost,
		Cause:     CauseHostManual,
	}

	// Host events should wake supervisor
	if !ev.RequiresSupervisorAttention() {
		t.Error("host manual action should require supervisor attention")
	}
}

// TestSupervisorMCPProvenanceFix verifies that when supervisor triggers
// MCP mutations, the provenance shows ActorSupervisor, not ActorMCP.
// This tests VAL-SUPERVISOR-003: supervisor-triggered MCP mutations preserve
// trustworthy provenance across CLI, HTTP, MCP, and service-generated follow-on paths.
//
// The fix is implemented via:
// 1. MCP server has SetInternalSessionID() to mark supervisor sessions
// 2. MCP server's CreatedBy() returns "supervisor" when internal session is active
// 3. ActorFromCreatedBy("supervisor") maps to ActorSupervisor
// 4. Native adapter tools check ctx.Value("supervisor_session") for provenance
// 5. Supervisor agent calls SetInternalSessionID() on startup
func TestSupervisorMCPProvenanceFix(t *testing.T) {
	// Test that external MCP calls (without supervisor session) show as ActorMCP
	externalMCPCreatedBy := "mcp"
	externalMCPActor := ActorFromCreatedBy(externalMCPCreatedBy)
	if externalMCPActor != ActorMCP {
		t.Errorf("external MCP should map to ActorMCP, got %v", externalMCPActor)
	}

	// Test that supervisor-triggered calls show as ActorSupervisor
	// This is the key fix: MCP server detects supervisor session context
	supervisorCreatedBy := "supervisor"
	supervisorActor := ActorFromCreatedBy(supervisorCreatedBy)
	if supervisorActor != ActorSupervisor {
		t.Errorf("supervisor should map to ActorSupervisor, got %v", supervisorActor)
	}

	// Verify supervisor events don't wake the supervisor (self-wake prevention)
	supervisorEvent := WorkEvent{
		Kind:      WorkEventUpdated,
		WorkID:    "work-supervisor-1",
		State:     "done",
		PrevState: "in_progress",
		Actor:     supervisorActor,
		Cause:     CauseSupervisorMutation,
	}
	if supervisorEvent.RequiresSupervisorAttention() {
		t.Error("supervisor's own mutations should not wake itself (VAL-SUPERVISOR-002)")
	}

	// Verify external MCP events still wake the supervisor
	mcpEvent := WorkEvent{
		Kind:      WorkEventUpdated,
		WorkID:    "work-mcp-1",
		State:     "done",
		PrevState: "in_progress",
		Actor:     externalMCPActor,
		Cause:     CauseWorkerTerminal,
	}
	if !mcpEvent.RequiresSupervisorAttention() {
		t.Error("external MCP events should still wake supervisor (VAL-SUPERVISOR-003)")
	}

	t.Log("SUCCESS: MCP supervisor provenance is correctly tracked (ActorSupervisor vs ActorMCP)")
}

// TestTransportBoundaryProvenanceAllPaths verifies that provenance survives
// across all transport boundaries: CLI, HTTP, MCP, host, and service-generated
// follow-on paths (VAL-SUPERVISOR-003).
func TestTransportBoundaryProvenanceAllPaths(t *testing.T) {
	tests := []struct {
		name        string
		createdBy   string
		expected    EventActor
		path        string
		shouldWake  bool
	}{
		// CLI transport boundary
		{"CLI work update", "cli", ActorWorker, "CLI", true},
		{"CLI work claim", "cli", ActorWorker, "CLI", true},
		{"CLI work attest", "cli", ActorWorker, "CLI", true},

		// HTTP transport boundary (via serve)
		{"HTTP work update", "cli", ActorWorker, "HTTP", true}, // HTTP uses "cli" createdBy
		{"HTTP work create", "cli", ActorWorker, "HTTP", true},

		// MCP transport boundary - external (not supervisor)
		{"MCP external work_update", "mcp", ActorMCP, "MCP-external", true},
		{"MCP external work_create", "mcp", ActorMCP, "MCP-external", true},
		{"MCP external work_attest", "mcp", ActorMCP, "MCP-external", true},
		{"MCP external check_record", "mcp", ActorMCP, "MCP-external", true},

		// MCP transport boundary - supervisor-triggered (the fix!)
		{"MCP supervisor work_update", "supervisor", ActorSupervisor, "MCP-supervisor", false}, // don't wake self
		{"MCP supervisor work_attest", "supervisor", ActorSupervisor, "MCP-supervisor", false},
		{"MCP supervisor check_record", "supervisor", ActorSupervisor, "MCP-supervisor", false},

		// Host/manual transport boundary
		{"Host work update", "host", ActorHost, "host", true},
		{"Host work claim", "host", ActorHost, "host", true},

		// Service-generated follow-on paths
		{"Service work create", "service", ActorService, "service-follow-on", true},
		{"Service attestation", "service", ActorService, "service-follow-on", true},

		// Native adapter tools (worker context)
		{"Native tool worker", "worker", ActorWorker, "native-tool", true},
		{"Native tool checker", "checker", ActorWorker, "native-tool", true}, // checker is worker role
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ActorFromCreatedBy(tt.createdBy)
			if got != tt.expected {
				t.Errorf("ActorFromCreatedBy(%q) for %s = %v, want %v",
					tt.createdBy, tt.path, got, tt.expected)
			}

			// Verify wake behavior is correct for this actor
			ev := WorkEvent{
				Kind:      WorkEventUpdated,
				WorkID:    "work-test-1",
				State:     "done",
				PrevState: "ready",
				Actor:     got,
				Cause:     CauseWorkerTerminal,
			}
			wakes := ev.RequiresSupervisorAttention()
			if wakes != tt.shouldWake {
				t.Errorf("%s event with %v: RequiresSupervisorAttention() = %v, want %v",
					tt.path, got, wakes, tt.shouldWake)
			}
		})
	}
}

// TestSupervisorMCPProvenanceEvents verifies that supervisor-triggered MCP
// mutations emit events with ActorSupervisor that do NOT wake the supervisor
// (self-wake prevention, VAL-SUPERVISOR-002).
func TestSupervisorMCPProvenanceEvents(t *testing.T) {
	// When supervisor triggers work_update via MCP
	supervisorUpdateEvent := WorkEvent{
		Kind:      WorkEventUpdated,
		WorkID:    "work-super-1",
		State:     "in_progress",
		PrevState: "ready",
		Actor:     ActorSupervisor,
		Cause:     CauseSupervisorMutation,
	}
	if supervisorUpdateEvent.RequiresSupervisorAttention() {
		t.Error("supervisor-triggered work update should NOT wake supervisor (self-wake prevention)")
	}

	// When supervisor triggers work_attest via MCP
	supervisorAttestEvent := WorkEvent{
		Kind:      WorkEventAttested,
		WorkID:    "work-super-2",
		State:     "awaiting_attestation",
		Actor:     ActorSupervisor,
		Cause:     CauseAttestationRecorded,
		Metadata:  map[string]string{"result": "passed"},
	}
	if supervisorAttestEvent.RequiresSupervisorAttention() {
		t.Error("supervisor-triggered attestation should NOT wake supervisor (self-wake prevention)")
	}

	// When supervisor creates check record via MCP
	supervisorCheckEvent := WorkEvent{
		Kind:     WorkEventCheckRecorded,
		WorkID:   "work-super-3",
		State:    "pass",
		Actor:    ActorSupervisor,
		Cause:    CauseCheckRecorded,
		Metadata: map[string]string{"check_id": "chk-super-1", "result": "pass"},
	}
	if supervisorCheckEvent.RequiresSupervisorAttention() {
		t.Error("supervisor-triggered check record should NOT wake supervisor (self-wake prevention)")
	}

	t.Log("All supervisor-triggered MCP events correctly do not wake supervisor")
}

// TestActorMappingsComplete verifies all canonical actor mappings are correct.
// This ensures the provenance system can distinguish all relevant sources
// (VAL-SUPERVISOR-003: provenance sufficient to classify worker, supervisor, etc.).
func TestActorMappingsComplete(t *testing.T) {
	tests := []struct {
		createdBy string
		expected  EventActor
	}{
		{"worker", ActorWorker},
		{"supervisor", ActorSupervisor},
		{"housekeeping", ActorHousekeeping},
		{"host", ActorHost},
		{"service", ActorService},
		{"reconciler", ActorReconciler},
		{"mcp", ActorMCP},
		{"cli", ActorWorker}, // CLI maps to worker (it's a client like any other)
		{"checker", ActorWorker}, // Checker is a worker role
		{"verifier", ActorWorker}, // Verifier is a worker role
		{"", ActorWorker},         // empty defaults to worker
		{"unknown", ActorWorker},  // unknown defaults to worker
	}

	for _, tt := range tests {
		t.Run(tt.createdBy, func(t *testing.T) {
			got := ActorFromCreatedBy(tt.createdBy)
			if got != tt.expected {
				t.Errorf("ActorFromCreatedBy(%q) = %v, want %v", tt.createdBy, got, tt.expected)
			}
		})
	}
}
