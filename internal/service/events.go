package service

import "sync"

// WorkEventKind identifies the type of work graph mutation.
type WorkEventKind string

const (
	WorkEventCreated    WorkEventKind = "work_created"
	WorkEventUpdated    WorkEventKind = "work_updated"
	WorkEventClaimed    WorkEventKind = "work_claimed"
	WorkEventReleased   WorkEventKind = "work_released"
	WorkEventAttested   WorkEventKind = "work_attested"
	WorkEventLeaseRenew WorkEventKind = "work_lease_renewed"
)

// WorkEvent is emitted when a work item is mutated.
type WorkEvent struct {
	Kind      WorkEventKind
	WorkID    string
	Title     string
	State     string
	PrevState string
}

// EventBus is a synchronous fan-out event bus for work graph mutations.
// Subscribers receive events on a buffered channel. Slow consumers are dropped.
type EventBus struct {
	mu   sync.Mutex
	subs []chan WorkEvent
}

// Subscribe returns a channel that receives work events.
// Call Unsubscribe when done.
func (b *EventBus) Subscribe() chan WorkEvent {
	ch := make(chan WorkEvent, 64)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel and closes it.
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

// publish sends an event to all subscribers. Non-blocking; slow consumers are skipped.
func (b *EventBus) publish(ev WorkEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
