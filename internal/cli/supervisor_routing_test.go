package cli

import (
	"testing"
	"time"

	"github.com/yusefmosiah/fase/internal/core"
)

func TestKindAffinity(t *testing.T) {
	cases := []struct {
		adapter string
		kind    string
		want    float64
	}{
		{"claude", "implement", 0.15},
		{"codex", "implement", 0},
		{"opencode", "implement", 0},
		{"claude", "attest", 0.1},
		{"codex", "attest", 0.1},
		{"opencode", "research", 0.1},
		{"opencode", "plan", 0.1},
		{"opencode", "review", 0.1},
		{"claude", "recovery", 0.15},
		{"codex", "recovery", 0},
		{"gemini", "implement", 0},
	}

	for _, tc := range cases {
		got := kindAffinity(tc.adapter, tc.kind)
		if got != tc.want {
			t.Errorf("kindAffinity(%q, %q) = %v, want %v", tc.adapter, tc.kind, got, tc.want)
		}
	}
}

func TestAdapterHealthTrackerRecordAndScore(t *testing.T) {
	tracker := newAdapterHealthTracker(t.TempDir())

	initial := tracker.score("claude", "claude-sonnet-4-6", core.WorkItemRecord{Kind: "implement"})
	if initial != 1.15 {
		t.Fatalf("initial score = %v, want 1.15", initial)
	}

	tracker.recordFailure("claude", "claude-sonnet-4-6")
	afterFail := tracker.score("claude", "claude-sonnet-4-6", core.WorkItemRecord{Kind: "implement"})
	if afterFail >= initial {
		t.Fatalf("score after failure %v should be less than initial %v", afterFail, initial)
	}

	tracker.recordSuccess("claude", "claude-sonnet-4-6", 5*time.Minute)
	afterSuccess := tracker.score("claude", "claude-sonnet-4-6", core.WorkItemRecord{Kind: "implement"})
	if afterSuccess <= afterFail {
		t.Fatalf("score after success %v should be greater than after failure %v", afterSuccess, afterFail)
	}
}

func TestAdapterHealthTrackerCircuitBreaker(t *testing.T) {
	tracker := newAdapterHealthTracker(t.TempDir())

	for i := 0; i < 3; i++ {
		tracker.recordFailure("codex", "gpt-5.4-mini")
	}

	if !tracker.isCircuitOpen("codex") {
		t.Fatal("circuit should be open after 3 consecutive failures")
	}

	if tracker.isCircuitOpen("claude") {
		t.Fatal("circuit should not be open for healthy adapter")
	}
}

func TestAdapterHealthTrackerPersistence(t *testing.T) {
	dir := t.TempDir()

	tracker1 := newAdapterHealthTracker(dir)
	tracker1.recordSuccess("claude", "claude-sonnet-4-6", 3*time.Minute)
	tracker1.recordFailure("codex", "gpt-5.4-mini")
	tracker1.recordFailure("codex", "gpt-5.4-mini")

	tracker2 := newAdapterHealthTracker(dir)
	score := tracker2.score("claude", "claude-sonnet-4-6", core.WorkItemRecord{Kind: "implement"})
	if score <= 1.0 {
		t.Fatalf("persisted score for claude = %v, want > 1.0", score)
	}
}

func TestScoreAndSelectSkipsCircuitOpenPreferredAdapter(t *testing.T) {
	loop := newEventDrivenLoop(1, t.TempDir(), "fase", "")
	for i := 0; i < 3; i++ {
		loop.health.recordFailure("claude", "claude-sonnet-4-6")
	}

	item := core.WorkItemRecord{
		Kind:              "implement",
		PreferredAdapters: []string{"claude"},
	}
	adapter, model := loop.scoreAndSelect(item, nil, []rotationEntry{
		{adapter: "claude", model: "claude-sonnet-4-6"},
		{adapter: "codex", model: "gpt-5.4-mini"},
	})
	if adapter == "claude" {
		t.Fatalf("expected circuit-open preferred adapter to be skipped, got %q / %q", adapter, model)
	}
	if adapter == "" || model == "" {
		t.Fatalf("expected fallback adapter selection, got %q / %q", adapter, model)
	}
}

func TestProcessEventStruct(t *testing.T) {
	pev := ProcessEvent{
		WorkID:   "work_01ABC",
		JobID:    "job_01XYZ",
		PID:      12345,
		ExitCode: 0,
		ExitErr:  nil,
	}
	if pev.WorkID != "work_01ABC" || pev.JobID != "job_01XYZ" || pev.PID != 12345 {
		t.Fatal("ProcessEvent fields not set correctly")
	}
}

func TestEventDrivenLoopDebounce(t *testing.T) {
	loop := newEventDrivenLoop(1, "/tmp", "fase", "")
	now := time.Now()
	loop.lastDispatch = now
	debounced := time.Since(loop.lastDispatch) < dispatchDebounce
	if !debounced {
		t.Fatal("fresh dispatch should be within debounce window")
	}
}
