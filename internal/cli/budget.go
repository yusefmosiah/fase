package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yusefmosiah/fase/internal/core"
)

// dailyUsage tracks per-adapter/model run counts for the current UTC day.
// It persists to <stateDir>/usage.json and resets automatically on day change.
type dailyUsage struct {
	mu       sync.Mutex
	stateDir string
	date     string
	runs     map[string]int
}

func newDailyUsage(stateDir string) *dailyUsage {
	u := &dailyUsage{
		stateDir: stateDir,
		date:     usageToday(),
		runs:     map[string]int{},
	}
	u.load()
	return u
}

func usageToday() string {
	return time.Now().UTC().Format("2006-01-02")
}

func (u *dailyUsage) usagePath() string {
	return filepath.Join(u.stateDir, "usage.json")
}

// load reads the persisted daily usage file. Silently resets on missing/stale/corrupt data.
func (u *dailyUsage) load() {
	data, err := os.ReadFile(u.usagePath())
	if err != nil {
		return
	}
	var persisted struct {
		Date string         `json:"date"`
		Runs map[string]int `json:"runs"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil || persisted.Date != usageToday() {
		return // stale date or corrupt — start fresh
	}
	u.date = persisted.Date
	u.runs = persisted.Runs
}

func (u *dailyUsage) save() {
	_ = os.MkdirAll(u.stateDir, 0o755)
	data, _ := json.MarshalIndent(struct {
		Date string         `json:"date"`
		Runs map[string]int `json:"runs"`
	}{u.date, u.runs}, "", "  ")
	_ = os.WriteFile(u.usagePath(), data, 0o644)
}

// resetIfNewDay resets the counter when the UTC date changes. Must be called with lock held.
func (u *dailyUsage) resetIfNewDay() {
	if today := usageToday(); u.date != today {
		u.date = today
		u.runs = map[string]int{}
	}
}

// runsToday returns how many times adapter/model has been dispatched today.
func (u *dailyUsage) runsToday(adapter, model string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.resetIfNewDay()
	return u.runs[adapter+"/"+model]
}

// recordRun increments the run counter for adapter/model and persists.
func (u *dailyUsage) recordRun(adapter, model string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.resetIfNewDay()
	u.runs[adapter+"/"+model]++
	u.save()
}

// rotationFromConfig converts config entries into rotationEntry slice.
// Falls back to the hard-coded workRotation when no entries are configured.
func rotationFromConfig(cfg core.Config) []rotationEntry {
	if len(cfg.Rotation.Entries) == 0 {
		return workRotation
	}
	result := make([]rotationEntry, len(cfg.Rotation.Entries))
	for i, e := range cfg.Rotation.Entries {
		result[i] = rotationEntry{adapter: e.Adapter, model: e.Model}
	}
	return result
}

// buildLimitsMap returns a map of "adapter/model" -> max_runs_per_day (0 = unlimited).
func buildLimitsMap(cfg core.Config) map[string]int {
	limits := make(map[string]int, len(cfg.Rotation.Entries))
	for _, e := range cfg.Rotation.Entries {
		if e.MaxRunsPerDay > 0 {
			limits[e.Adapter+"/"+e.Model] = e.MaxRunsPerDay
		}
	}
	return limits
}

// budgetFilter returns the subset of entries that have not exceeded their daily
// limit. If all entries are exhausted it returns the full list (fail-open) so
// dispatch never deadlocks.
func budgetFilter(entries []rotationEntry, limits map[string]int, usage *dailyUsage) []rotationEntry {
	if len(limits) == 0 {
		return entries // no limits configured — nothing to filter
	}
	available := make([]rotationEntry, 0, len(entries))
	for _, e := range entries {
		max := limits[e.adapter+"/"+e.model]
		if max <= 0 {
			available = append(available, e) // unlimited
			continue
		}
		if usage.runsToday(e.adapter, e.model) < max {
			available = append(available, e)
		}
	}
	if len(available) == 0 {
		// All exhausted — fail open so work still makes progress.
		return entries
	}
	return available
}
