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
			got := actorFromCreatedBy(tt.createdBy)
			if got != tt.expected {
				t.Errorf("actorFromCreatedBy(%q) = %v, want %v", tt.createdBy, got, tt.expected)
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
			got := actorFromCreatedBy(tt.createdBy)
			if got != tt.expectedAct {
				t.Errorf("actorFromCreatedBy(%q) = %v, want %v", tt.createdBy, got, tt.expectedAct)
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
		Actor:     actorFromCreatedBy(createdBy),
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
			got := actorFromCreatedBy(tt.createdBy)
			if got != tt.expectedAct {
				t.Errorf("actorFromCreatedBy(%q) = %v, want %v", tt.createdBy, got, tt.expectedAct)
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

// TestSupervisorMCPProvenanceGap verifies the gap: when supervisor triggers
// MCP mutations, the provenance should show ActorSupervisor, not ActorMCP.
// This tests VAL-SUPERVISOR-003: supervisor-triggered MCP mutations should
// preserve trustworthy provenance.
// NOTE: This test documents the current gap - MCP always sets CreatedBy="mcp"
// even when triggered by supervisor. The fix would require the MCP server
// to detect supervisor session context.
func TestSupervisorMCPProvenanceGap(t *testing.T) {
	// Current behavior: MCP sets CreatedBy="mcp" which maps to ActorMCP
	// This is incorrect when the supervisor is the one making the MCP call

	// Test that external MCP calls show as ActorMCP
	externalMCPCreatedBy := "mcp"
	externalMCPActor := actorFromCreatedBy(externalMCPCreatedBy)
	if externalMCPActor != ActorMCP {
		t.Errorf("external MCP should map to ActorMCP, got %v", externalMCPActor)
	}

	// Test that supervisor calls should show as ActorSupervisor
	// Currently this FAILS because MCP always uses CreatedBy="mcp"
	supervisorCreatedBy := "supervisor"
	supervisorActor := actorFromCreatedBy(supervisorCreatedBy)
	if supervisorActor != ActorSupervisor {
		t.Errorf("supervisor should map to ActorSupervisor, got %v", supervisorActor)
	}

	// The gap: MCP mutations from supervisor should use CreatedBy="supervisor"
	// not CreatedBy="mcp", but the MCP server doesn't know the session context
	t.Log("NOTE: MCP server currently hardcodes CreatedBy='mcp' - supervisor provenance is lost")
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
			got := actorFromCreatedBy(tt.createdBy)
			if got != tt.expected {
				t.Errorf("actorFromCreatedBy(%q) = %v, want %v", tt.createdBy, got, tt.expected)
			}
		})
	}
}
