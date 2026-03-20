package service

import (
	"sync"
	"sync/atomic"
)

type WorkEventKind string

const (
	WorkEventCreated    WorkEventKind = "work_created"
	WorkEventUpdated    WorkEventKind = "work_updated"
	WorkEventClaimed    WorkEventKind = "work_claimed"
	WorkEventReleased   WorkEventKind = "work_released"
	WorkEventAttested   WorkEventKind = "work_attested"
	WorkEventLeaseRenew WorkEventKind = "work_lease_renewed"
)

type WorkEvent struct {
	Kind      WorkEventKind
	WorkID    string
	Title     string
	State     string
	PrevState string
	JobID     string
	Adapter   string
	Metadata  map[string]string
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

func (b *EventBus) publish(ev WorkEvent) {
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
