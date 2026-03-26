package service

import (
	"sync"
	"sync/atomic"
)

type WorkEventKind string

const (
	WorkEventCreated       WorkEventKind = "work_created"
	WorkEventUpdated       WorkEventKind = "work_updated"
	WorkEventClaimed       WorkEventKind = "work_claimed"
	WorkEventReleased      WorkEventKind = "work_released"
	WorkEventAttested      WorkEventKind = "work_attested"
	WorkEventLeaseRenew    WorkEventKind = "work_lease_renewed"
	WorkEventCheckRecorded WorkEventKind = "check_recorded"
)

// EventActor identifies who caused the event.
type EventActor string

const (
	ActorWorker       EventActor = "worker"
	ActorSupervisor   EventActor = "supervisor"
	ActorHousekeeping EventActor = "housekeeping"
	ActorHost         EventActor = "host"
	ActorService      EventActor = "service"
	ActorReconciler   EventActor = "reconciler"
	ActorMCP          EventActor = "mcp"
)

// EventCause classifies why the event was emitted.
type EventCause string

const (
	CauseWorkCreated         EventCause = "work_created"
	CauseWorkerProgress      EventCause = "worker_progress"
	CauseWorkerTerminal      EventCause = "worker_terminal"
	CauseAttestationRecorded EventCause = "attestation_recorded"
	CauseCheckRecorded       EventCause = "check_recorded"
	CauseParentTransition    EventCause = "parent_transition"
	CauseHousekeepingStall   EventCause = "housekeeping_stall"
	CauseHousekeepingOrphan  EventCause = "housekeeping_orphan"
	CauseSupervisorMutation  EventCause = "supervisor_mutation"
	CauseLeaseReconcile      EventCause = "lease_reconcile"
	CauseHostManual          EventCause = "host_manual"
	CauseClaimChanged        EventCause = "claim_changed"
	CauseJobLifecycle        EventCause = "job_lifecycle"
)

type WorkEvent struct {
	Kind      WorkEventKind
	WorkID    string
	Title     string
	State     string
	PrevState string
	JobID     string
	Adapter   string
	Actor     EventActor
	Cause     EventCause
	Metadata  map[string]string
}

// RequiresSupervisorAttention returns true if this event should wake the supervisor.
func (ev WorkEvent) RequiresSupervisorAttention() bool {
	// Supervisor's own mutations should not wake itself.
	if ev.Actor == ActorSupervisor {
		return false
	}
	// Stall events require supervisor attention (they don't auto-kill anymore).
	if ev.Cause == CauseHousekeepingStall || ev.Cause == CauseHousekeepingOrphan {
		return true
	}
	// Housekeeping, lease maintenance, and job lifecycle are noise.
	if ev.Actor == ActorHousekeeping || ev.Actor == ActorReconciler {
		return false
	}
	if ev.Kind == WorkEventLeaseRenew {
		return false
	}
	// Worker progress without a state change is noise — supervisor only cares
	// about meaningful transitions (done, failed), not mid-run updates.
	if ev.Cause == CauseWorkerProgress && ev.State == ev.PrevState {
		return false
	}
	// Job lifecycle events (started, running) are noise unless terminal.
	if ev.Cause == CauseJobLifecycle && ev.State == "in_progress" {
		return false
	}
	// Claim changes without state transition are noise.
	if ev.Cause == CauseClaimChanged && ev.State == ev.PrevState {
		return false
	}
	// Everything else is actionable: terminal state changes, attestations,
	// check records, new work, host actions.
	return true
}

type EventBus struct {
	mu        sync.Mutex
	subs      []chan WorkEvent
	drops     atomic.Int64
	published atomic.Int64
}

func (b *EventBus) Subscribe() chan WorkEvent {
	return b.SubscribeWithBuffer(64)
}

func (b *EventBus) SubscribeWithBuffer(size int) chan WorkEvent {
	if size <= 0 {
		size = 64
	}
	ch := make(chan WorkEvent, size)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch
}

func (b *EventBus) Unsubscribe(ch chan WorkEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, sub := range b.subs {
		if sub == ch {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

// actorFromClaimant maps a claimant string to an EventActor.
func actorFromClaimant(claimant string) EventActor {
	switch claimant {
	case "supervisor":
		return ActorSupervisor
	case "housekeeping":
		return ActorHousekeeping
	default:
		return ActorWorker
	}
}

// ActorFromCreatedBy maps a createdBy string to an EventActor.
// Empty string defaults to ActorWorker to match canonical test expectations.
// True service-generated paths must explicitly pass CreatedBy="service" to
// emit ActorService when intended.
func ActorFromCreatedBy(createdBy string) EventActor {
	switch createdBy {
	case "housekeeping":
		return ActorHousekeeping
	case "reconciler":
		return ActorReconciler
	case "supervisor":
		return ActorSupervisor
	case "mcp":
		return ActorMCP
	case "host":
		return ActorHost
	case "service":
		// Explicit "service" returns ActorService for true service-generated paths.
		return ActorService
	default:
		// Empty string and any unknown values default to ActorWorker.
		return ActorWorker
	}
}

func (b *EventBus) publish(ev WorkEvent) {
	b.Publish(ev)
}

func (b *EventBus) Publish(ev WorkEvent) {
	b.published.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			b.drops.Add(1)
		}
	}
}

func (b *EventBus) Stats() (published int64, drops int64) {
	return b.published.Load(), b.drops.Load()
}
