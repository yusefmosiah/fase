package cli

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/yusefmosiah/fase/internal/core"
)

type AdapterHealth struct {
	Adapter        string
	Model          string
	RecentFailures int
	LastFailure    time.Time
	LastSuccess    time.Time
	TotalRuns      int
	TotalFailures  int
	AvgDuration    time.Duration
	durations      []time.Duration
}

type AdapterHealthTracker struct {
	mu       sync.Mutex
	health   map[string]*AdapterHealth
	stateDir string
}

func newAdapterHealthTracker(stateDir string) *AdapterHealthTracker {
	t := &AdapterHealthTracker{
		health:   make(map[string]*AdapterHealth),
		stateDir: stateDir,
	}
	t.load()
	return t
}

func (t *AdapterHealthTracker) key(adapter, model string) string {
	return adapter + "/" + model
}

func (t *AdapterHealthTracker) recordDispatch(adapter, model string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := t.key(adapter, model)
	h, ok := t.health[k]
	if !ok {
		h = &AdapterHealth{Adapter: adapter, Model: model}
		t.health[k] = h
	}
	h.TotalRuns++
}

func (t *AdapterHealthTracker) recordSuccess(adapter, model string, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := t.key(adapter, model)
	h, ok := t.health[k]
	if !ok {
		h = &AdapterHealth{Adapter: adapter, Model: model}
		t.health[k] = h
	}
	h.LastSuccess = time.Now()
	h.RecentFailures = 0
	if len(h.durations) >= 10 {
		h.durations = h.durations[1:]
	}
	h.durations = append(h.durations, duration)
	var total time.Duration
	for _, d := range h.durations {
		total += d
	}
	h.AvgDuration = time.Duration(int64(total) / int64(len(h.durations)))
	t.save()
}

func (t *AdapterHealthTracker) recordFailure(adapter, model string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := t.key(adapter, model)
	h, ok := t.health[k]
	if !ok {
		h = &AdapterHealth{Adapter: adapter, Model: model}
		t.health[k] = h
	}
	h.LastFailure = time.Now()
	h.RecentFailures++
	h.TotalFailures++
	t.save()
}

func (t *AdapterHealthTracker) isCircuitOpen(adapter string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, h := range t.health {
		if h.Adapter == adapter && h.RecentFailures >= 3 {
			if time.Since(h.LastFailure) < 5*time.Minute {
				return true
			}
		}
	}
	return false
}

func (t *AdapterHealthTracker) score(adapter, model string, item core.WorkItemRecord) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	k := t.key(adapter, model)
	h, ok := t.health[k]

	baseScore := 1.0

	if ok {
		if h.RecentFailures > 0 && time.Since(h.LastFailure) < time.Hour {
			baseScore -= float64(h.RecentFailures) * 0.2
		}
		if !h.LastSuccess.IsZero() && time.Since(h.LastSuccess) < 24*time.Hour {
			baseScore += 0.1
		}
		if !h.LastFailure.IsZero() && time.Since(h.LastFailure) < 10*time.Minute {
			baseScore -= 0.3
		}
	}

	baseScore += kindAffinity(adapter, item.Kind)

	return baseScore
}

func kindAffinity(adapter, kind string) float64 {
	switch kind {
	case "implement":
		if adapter == "claude" {
			return 0.15
		}
	case "attest":
		return 0.1
	case "research", "plan":
		if adapter == "opencode" {
			return 0.1
		}
	case "review":
		if adapter == "opencode" {
			return 0.1
		}
	case "recovery":
		if adapter == "claude" {
			return 0.15
		}
	}
	return 0
}

func (t *AdapterHealthTracker) load() {
	path := t.healthPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var persisted map[string]*AdapterHealth
	if err := json.Unmarshal(data, &persisted); err != nil {
		return
	}
	t.health = persisted
}

func (t *AdapterHealthTracker) save() {
	path := t.healthPath()
	data, err := json.MarshalIndent(t.health, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

func (t *AdapterHealthTracker) healthPath() string {
	return t.stateDir + "/adapter_health.json"
}
