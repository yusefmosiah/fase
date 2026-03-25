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
