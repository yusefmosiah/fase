package cli

import (
	"testing"

	"github.com/yusefmosiah/cogent/internal/service"
)

// TestFilterNovelEventsFiltersDuplicates verifies that events with the same
// (WorkID, State) pair as previously seen events are filtered out.
func TestFilterNovelEventsFiltersDuplicates(t *testing.T) {
	seen := map[string]string{
		"work-1": "done",
		"work-2": "failed",
	}

	events := []service.WorkEvent{
		{WorkID: "work-1", State: "done"},   // duplicate - should be filtered
		{WorkID: "work-2", State: "failed"}, // duplicate - should be filtered
		{WorkID: "work-3", State: "done"},   // new - should pass
		{WorkID: "work-1", State: "failed"}, // new state for work-1 - should pass
	}

	novel := filterNovelEvents(events, seen)

	if len(novel) != 2 {
		t.Fatalf("expected 2 novel events, got %d", len(novel))
	}

	// Check that work-3 (done) and work-1 (failed) are in the novel set
	found := make(map[string]bool)
	for _, ev := range novel {
		found[ev.WorkID+ev.State] = true
	}

	if !found["work-3done"] {
		t.Error("expected work-3 done to be in novel events")
	}
	if !found["work-1failed"] {
		t.Error("expected work-1 failed to be in novel events")
	}
}

// TestFilterNovelEventsPreservesEmptyWorkID verifies that events without
// a WorkID are preserved (they may be system events).
func TestFilterNovelEventsPreservesEmptyWorkID(t *testing.T) {
	seen := map[string]string{}

	events := []service.WorkEvent{
		{WorkID: "", State: "created"}, // no work ID - should pass
		{WorkID: "work-1", State: "done"},
	}

	novel := filterNovelEvents(events, seen)

	if len(novel) != 2 {
		t.Fatalf("expected 2 novel events, got %d", len(novel))
	}
}

// TestRecordSeenUpdatesMap verifies that recordSeen correctly records
// (WorkID, State) pairs into the seen map.
func TestRecordSeenUpdatesMap(t *testing.T) {
	seen := map[string]string{}

	events := []service.WorkEvent{
		{WorkID: "work-1", State: "done"},
		{WorkID: "work-2", State: "failed"},
		{WorkID: "", State: "ignored"}, // empty WorkID should be skipped
	}

	recordSeen(events, seen)

	if seen["work-1"] != "done" {
		t.Errorf("expected work-1 -> done, got %q", seen["work-1"])
	}
	if seen["work-2"] != "failed" {
		t.Errorf("expected work-2 -> failed, got %q", seen["work-2"])
	}
	if len(seen) != 2 {
		t.Errorf("expected 2 entries, got %d", len(seen))
	}
}
