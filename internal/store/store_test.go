package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/yusefmosiah/fase/internal/core"
)

func openTestDB(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, path: ":memory:"}
	if err := s.bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return s
}

func mustParseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func sampleWorkItem(workID string) core.WorkItemRecord {
	now := time.Now().UTC()
	return core.WorkItemRecord{
		WorkID:         workID,
		Title:          "Test Work",
		Objective:      "Do something",
		Kind:           "implement",
		ExecutionState: core.WorkExecutionStateReady,
		ApprovalState:  core.WorkApprovalStateNone,
		LockState:      core.WorkLockStateUnlocked,
		Priority:       5,
		Position:       1,
		AttemptEpoch:   1,
		Metadata:       map[string]any{"key": "val"},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func sampleSession(sessionID string) core.SessionRecord {
	now := time.Now().UTC()
	return core.SessionRecord{
		SessionID:     sessionID,
		Status:        "active",
		OriginAdapter: "test",
		OriginJobID:   "job_test",
		CWD:           "/tmp",
		CreatedAt:     now,
		UpdatedAt:     now,
		Tags:          []string{"test"},
		Metadata:      map[string]any{},
	}
}

func sampleJob(jobID, sessionID string) core.JobRecord {
	now := time.Now().UTC()
	return core.JobRecord{
		JobID:     jobID,
		SessionID: sessionID,
		Adapter:   "test",
		State:     core.JobStateCreated,
		CWD:       "/tmp",
		CreatedAt: now,
		UpdatedAt: now,
		Summary:   map[string]any{"status": "ok"},
	}
}

func seedSession(t *testing.T, s *Store, sessionID string) {
	t.Helper()
	if err := s.CreateSession(context.Background(), sampleSession(sessionID)); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

func seedWorkItem(t *testing.T, s *Store, w core.WorkItemRecord) {
	t.Helper()
	if err := s.CreateWorkItem(context.Background(), w); err != nil {
		t.Fatalf("seed work item: %v", err)
	}
}

func seedJob(t *testing.T, s *Store, j core.JobRecord) {
	t.Helper()
	if err := s.CreateJob(context.Background(), j); err != nil {
		t.Fatalf("seed job: %v", err)
	}
}

func TestCreateWorkItemAndGetRoundtrip(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")

	if err := s.CreateWorkItem(ctx, w); err != nil {
		t.Fatalf("CreateWorkItem: %v", err)
	}

	got, err := s.GetWorkItem(ctx, "work_01")
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}

	if got.WorkID != w.WorkID {
		t.Errorf("WorkID = %q, want %q", got.WorkID, w.WorkID)
	}
	if got.Title != w.Title {
		t.Errorf("Title = %q, want %q", got.Title, w.Title)
	}
	if got.Objective != w.Objective {
		t.Errorf("Objective = %q, want %q", got.Objective, w.Objective)
	}
	if got.Kind != w.Kind {
		t.Errorf("Kind = %q, want %q", got.Kind, w.Kind)
	}
	if got.ExecutionState != w.ExecutionState {
		t.Errorf("ExecutionState = %q, want %q", got.ExecutionState, w.ExecutionState)
	}
	if got.Priority != w.Priority {
		t.Errorf("Priority = %d, want %d", got.Priority, w.Priority)
	}
	if got.Position != w.Position {
		t.Errorf("Position = %d, want %d", got.Position, w.Position)
	}
	if got.AttemptEpoch != w.AttemptEpoch {
		t.Errorf("AttemptEpoch = %d, want %d", got.AttemptEpoch, w.AttemptEpoch)
	}
	if got.Metadata["key"] != w.Metadata["key"] {
		t.Errorf("Metadata[key] = %v, want %v", got.Metadata["key"], w.Metadata["key"])
	}
}

func TestGetWorkItemNotFound(t *testing.T) {
	s := openTestDB(t)
	_, err := s.GetWorkItem(context.Background(), "work_nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetWorkItemCanonicalizesDeprecatedExecutionState(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_legacy")
	w.ExecutionState = core.WorkExecutionStateAwaitingAttestation
	seedWorkItem(t, s, w)

	got, err := s.GetWorkItem(ctx, w.WorkID)
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	if got.ExecutionState != core.WorkExecutionStateChecking {
		t.Fatalf("ExecutionState = %q, want %q", got.ExecutionState, core.WorkExecutionStateChecking)
	}
}

func TestUpdateWorkItem(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	seedWorkItem(t, s, w)

	got, err := s.GetWorkItem(ctx, "work_01")
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}

	got.Title = "Updated Title"
	got.Objective = "New objective"
	got.ExecutionState = core.WorkExecutionStateClaimed
	got.Priority = 10
	got.AttemptEpoch = 3
	got.UpdatedAt = time.Now().UTC()

	if err := s.UpdateWorkItem(ctx, got); err != nil {
		t.Fatalf("UpdateWorkItem: %v", err)
	}

	updated, err := s.GetWorkItem(ctx, "work_01")
	if err != nil {
		t.Fatalf("GetWorkItem after update: %v", err)
	}
	if updated.Title != "Updated Title" {
		t.Errorf("Title = %q, want %q", updated.Title, "Updated Title")
	}
	if updated.Objective != "New objective" {
		t.Errorf("Objective = %q, want %q", updated.Objective, "New objective")
	}
	if updated.ExecutionState != core.WorkExecutionStateClaimed {
		t.Errorf("ExecutionState = %q, want %q", updated.ExecutionState, core.WorkExecutionStateClaimed)
	}
	if updated.Priority != 10 {
		t.Errorf("Priority = %d, want %d", updated.Priority, 10)
	}
	if updated.AttemptEpoch != 3 {
		t.Errorf("AttemptEpoch = %d, want %d", updated.AttemptEpoch, 3)
	}
}

func TestUpdateWorkItemNotFound(t *testing.T) {
	s := openTestDB(t)
	w := sampleWorkItem("work_nonexistent")
	err := s.UpdateWorkItem(context.Background(), w)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListWorkItems(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	seedWorkItem(t, s, sampleWorkItem("work_01"))
	seedWorkItem(t, s, sampleWorkItem("work_02"))

	items, err := s.ListWorkItems(ctx, 10, "", "", "", false)
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}

	items, err = s.ListWorkItems(ctx, 1, "", "", "", false)
	if err != nil {
		t.Fatalf("ListWorkItems limit: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) with limit=1 = %d, want 1", len(items))
	}
}

func TestListWorkItemsCanonicalizesDeprecatedExecutionState(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_legacy")
	w.ExecutionState = core.WorkExecutionStateAwaitingAttestation
	seedWorkItem(t, s, w)

	items, err := s.ListWorkItems(ctx, 10, "", "", "", false)
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ExecutionState != core.WorkExecutionStateChecking {
		t.Fatalf("ExecutionState = %q, want %q", items[0].ExecutionState, core.WorkExecutionStateChecking)
	}
}

func TestListWorkUpdatesCanonicalizesDeprecatedExecutionState(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_legacy_updates")
	seedWorkItem(t, s, w)

	err := s.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:       "upd_legacy",
		WorkID:         w.WorkID,
		ExecutionState: core.WorkExecutionStateAwaitingAttestation,
		ApprovalState:  core.WorkApprovalStatePending,
		Message:        "legacy state update",
		Metadata:       map[string]any{},
		CreatedBy:      "test",
		CreatedAt:      time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateWorkUpdate: %v", err)
	}

	updates, err := s.ListWorkUpdates(ctx, w.WorkID, 10)
	if err != nil {
		t.Fatalf("ListWorkUpdates: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("len(updates) = %d, want 1", len(updates))
	}
	if updates[0].ExecutionState != core.WorkExecutionStateChecking {
		t.Fatalf("ExecutionState = %q, want %q", updates[0].ExecutionState, core.WorkExecutionStateChecking)
	}
}

func TestListWorkItemsFilterByKind(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	w1 := sampleWorkItem("work_01")
	w1.Kind = "implement"
	w2 := sampleWorkItem("work_02")
	w2.Kind = "investigate"
	seedWorkItem(t, s, w1)
	seedWorkItem(t, s, w2)

	items, err := s.ListWorkItems(ctx, 10, "implement", "", "", false)
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(items) != 1 || items[0].WorkID != "work_01" {
		t.Fatalf("expected only work_01, got %d items", len(items))
	}
}

func TestListWorkItemsFilterByExecutionState(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	w1 := sampleWorkItem("work_01")
	w1.ExecutionState = core.WorkExecutionStateReady
	w2 := sampleWorkItem("work_02")
	w2.ExecutionState = core.WorkExecutionStateDone
	seedWorkItem(t, s, w1)
	seedWorkItem(t, s, w2)

	items, err := s.ListWorkItems(ctx, 10, "", string(core.WorkExecutionStateDone), "", false)
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(items) != 1 || items[0].WorkID != "work_02" {
		t.Fatalf("expected only work_02, got %d items", len(items))
	}
}

func TestListWorkItemsExcludeArchived(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	w1 := sampleWorkItem("work_01")
	w1.ExecutionState = core.WorkExecutionStateReady
	w2 := sampleWorkItem("work_02")
	w2.ExecutionState = core.WorkExecutionStateArchived
	seedWorkItem(t, s, w1)
	seedWorkItem(t, s, w2)

	items, err := s.ListWorkItems(ctx, 10, "", "", "", false)
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(items) != 1 || items[0].WorkID != "work_01" {
		t.Fatalf("expected only work_01 (archived excluded), got %d items", len(items))
	}

	items, err = s.ListWorkItems(ctx, 10, "", "", "", true)
	if err != nil {
		t.Fatalf("ListWorkItems includeArchived: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items (includeArchived), got %d", len(items))
	}
}

func TestClaimWorkItem(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	seedWorkItem(t, s, sampleWorkItem("work_01"))

	leaseUntil := time.Now().UTC().Add(30 * time.Minute)
	claimed, err := s.ClaimWorkItem(ctx, "work_01", "agent-1", leaseUntil)
	if err != nil {
		t.Fatalf("ClaimWorkItem: %v", err)
	}
	if claimed.ClaimedBy != "agent-1" {
		t.Errorf("ClaimedBy = %q, want %q", claimed.ClaimedBy, "agent-1")
	}
	if claimed.ExecutionState != core.WorkExecutionStateClaimed {
		t.Errorf("ExecutionState = %q, want %q", claimed.ExecutionState, core.WorkExecutionStateClaimed)
	}
	if claimed.ClaimedUntil == nil {
		t.Fatal("ClaimedUntil is nil")
	}
}

func TestClaimWorkItemTransitionReadyToClaimed(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	w.ExecutionState = core.WorkExecutionStateReady
	seedWorkItem(t, s, w)

	leaseUntil := time.Now().UTC().Add(30 * time.Minute)
	claimed, err := s.ClaimWorkItem(ctx, "work_01", "agent-1", leaseUntil)
	if err != nil {
		t.Fatalf("ClaimWorkItem: %v", err)
	}
	if claimed.ExecutionState != core.WorkExecutionStateClaimed {
		t.Errorf("ExecutionState = %q, want claimed (ready->claimed transition)", claimed.ExecutionState)
	}
}

func TestClaimWorkItemAlreadyDone(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	w.ExecutionState = core.WorkExecutionStateDone
	seedWorkItem(t, s, w)

	leaseUntil := time.Now().UTC().Add(30 * time.Minute)
	_, err := s.ClaimWorkItem(ctx, "work_01", "agent-1", leaseUntil)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy for done work, got %v", err)
	}
}

func TestClaimWorkItemContended(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	w.ExecutionState = core.WorkExecutionStateClaimed
	w.ClaimedBy = "agent-1"
	lease := time.Now().UTC().Add(30 * time.Minute)
	w.ClaimedUntil = &lease
	seedWorkItem(t, s, w)

	_, err := s.ClaimWorkItem(ctx, "work_01", "agent-2", time.Now().UTC().Add(30*time.Minute))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy for contended claim, got %v", err)
	}
}

func TestClaimWorkItemExpiredLease(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	w.ExecutionState = core.WorkExecutionStateClaimed
	w.ClaimedBy = "agent-1"
	past := time.Now().UTC().Add(-1 * time.Minute)
	w.ClaimedUntil = &past
	seedWorkItem(t, s, w)

	claimed, err := s.ClaimWorkItem(ctx, "work_01", "agent-2", time.Now().UTC().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("ClaimWorkItem with expired lease: %v", err)
	}
	if claimed.ClaimedBy != "agent-2" {
		t.Errorf("ClaimedBy = %q, want %q", claimed.ClaimedBy, "agent-2")
	}
}

func TestReleaseWorkItemClaim(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	w.ExecutionState = core.WorkExecutionStateClaimed
	w.ClaimedBy = "agent-1"
	lease := time.Now().UTC().Add(30 * time.Minute)
	w.ClaimedUntil = &lease
	seedWorkItem(t, s, w)

	released, err := s.ReleaseWorkItemClaim(ctx, "work_01", "agent-1", false)
	if err != nil {
		t.Fatalf("ReleaseWorkItemClaim: %v", err)
	}
	if released.ClaimedBy != "" {
		t.Errorf("ClaimedBy = %q, want empty", released.ClaimedBy)
	}
	if released.ClaimedUntil != nil {
		t.Errorf("ClaimedUntil = %v, want nil", released.ClaimedUntil)
	}
	if released.ExecutionState != core.WorkExecutionStateReady {
		t.Errorf("ExecutionState = %q, want %q", released.ExecutionState, core.WorkExecutionStateReady)
	}
}

func TestReleaseWorkItemClaimNotClaimant(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	w.ExecutionState = core.WorkExecutionStateClaimed
	w.ClaimedBy = "agent-1"
	lease := time.Now().UTC().Add(30 * time.Minute)
	w.ClaimedUntil = &lease
	seedWorkItem(t, s, w)

	_, err := s.ReleaseWorkItemClaim(ctx, "work_01", "agent-2", false)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
}

func TestReleaseWorkItemClaimForce(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	w.ExecutionState = core.WorkExecutionStateClaimed
	w.ClaimedBy = "agent-1"
	past := time.Now().UTC().Add(-1 * time.Minute)
	w.ClaimedUntil = &past
	seedWorkItem(t, s, w)

	released, err := s.ReleaseWorkItemClaim(ctx, "work_01", "agent-2", true)
	if err != nil {
		t.Fatalf("ReleaseWorkItemClaim force: %v", err)
	}
	if released.ExecutionState != core.WorkExecutionStateReady {
		t.Errorf("ExecutionState = %q, want %q", released.ExecutionState, core.WorkExecutionStateReady)
	}
	if released.ClaimedBy != "" {
		t.Errorf("ClaimedBy = %q, want empty", released.ClaimedBy)
	}
}

func TestNextWorkPosition(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	pos, err := s.NextWorkPosition(ctx)
	if err != nil {
		t.Fatalf("NextWorkPosition empty: %v", err)
	}
	if pos != 1 {
		t.Errorf("pos = %d, want 1", pos)
	}

	w := sampleWorkItem("work_01")
	w.Position = 3
	seedWorkItem(t, s, w)

	pos, err = s.NextWorkPosition(ctx)
	if err != nil {
		t.Fatalf("NextWorkPosition after insert: %v", err)
	}
	if pos != 4 {
		t.Errorf("pos = %d, want 4", pos)
	}
}

func TestShiftWorkPositions(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	w1 := sampleWorkItem("work_01")
	w1.Position = 1
	w2 := sampleWorkItem("work_02")
	w2.Position = 2
	w3 := sampleWorkItem("work_03")
	w3.Position = 3
	seedWorkItem(t, s, w1)
	seedWorkItem(t, s, w2)
	seedWorkItem(t, s, w3)

	if err := s.ShiftWorkPositions(ctx, 2, 3, 1); err != nil {
		t.Fatalf("ShiftWorkPositions: %v", err)
	}

	got2, _ := s.GetWorkItem(ctx, "work_02")
	got3, _ := s.GetWorkItem(ctx, "work_03")
	if got2.Position != 3 {
		t.Errorf("work_02 Position = %d, want 3", got2.Position)
	}
	if got3.Position != 4 {
		t.Errorf("work_03 Position = %d, want 4", got3.Position)
	}

	got1, _ := s.GetWorkItem(ctx, "work_01")
	if got1.Position != 1 {
		t.Errorf("work_01 Position = %d, want 1 (unchanged)", got1.Position)
	}
}

func TestSetWorkPosition(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	w.Position = 1
	seedWorkItem(t, s, w)

	if err := s.SetWorkPosition(ctx, "work_01", 42); err != nil {
		t.Fatalf("SetWorkPosition: %v", err)
	}

	got, _ := s.GetWorkItem(ctx, "work_01")
	if got.Position != 42 {
		t.Errorf("Position = %d, want 42", got.Position)
	}
}

func TestSetWorkPositionNotFound(t *testing.T) {
	s := openTestDB(t)
	err := s.SetWorkPosition(context.Background(), "work_nonexistent", 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMoveWorkPosition(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	w1 := sampleWorkItem("work_01")
	w1.Position = 1
	w2 := sampleWorkItem("work_02")
	w2.Position = 2
	w3 := sampleWorkItem("work_03")
	w3.Position = 3
	seedWorkItem(t, s, w1)
	seedWorkItem(t, s, w2)
	seedWorkItem(t, s, w3)

	if err := s.MoveWorkPosition(ctx, "work_03", 1); err != nil {
		t.Fatalf("MoveWorkPosition: %v", err)
	}

	got1, _ := s.GetWorkItem(ctx, "work_01")
	got2, _ := s.GetWorkItem(ctx, "work_02")
	got3, _ := s.GetWorkItem(ctx, "work_03")
	if got3.Position != 1 {
		t.Errorf("work_03 Position = %d, want 1", got3.Position)
	}
	if got1.Position != 2 {
		t.Errorf("work_01 Position = %d, want 2", got1.Position)
	}
	if got2.Position != 3 {
		t.Errorf("work_02 Position = %d, want 3", got2.Position)
	}
}

func TestMoveWorkPositionNoop(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	w.Position = 5
	seedWorkItem(t, s, w)

	if err := s.MoveWorkPosition(ctx, "work_01", 5); err != nil {
		t.Fatalf("MoveWorkPosition noop: %v", err)
	}

	got, _ := s.GetWorkItem(ctx, "work_01")
	if got.Position != 5 {
		t.Errorf("Position = %d, want 5 (unchanged)", got.Position)
	}
}

func TestReorderWorkPositions(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	w1 := sampleWorkItem("work_01")
	w1.Position = 3
	w2 := sampleWorkItem("work_02")
	w2.Position = 1
	w3 := sampleWorkItem("work_03")
	w3.Position = 2
	seedWorkItem(t, s, w1)
	seedWorkItem(t, s, w2)
	seedWorkItem(t, s, w3)

	if err := s.ReorderWorkPositions(ctx, []string{"work_02", "work_03", "work_01"}); err != nil {
		t.Fatalf("ReorderWorkPositions: %v", err)
	}

	got1, _ := s.GetWorkItem(ctx, "work_01")
	got2, _ := s.GetWorkItem(ctx, "work_02")
	got3, _ := s.GetWorkItem(ctx, "work_03")
	if got2.Position != 1 {
		t.Errorf("work_02 Position = %d, want 1", got2.Position)
	}
	if got3.Position != 2 {
		t.Errorf("work_03 Position = %d, want 2", got3.Position)
	}
	if got1.Position != 3 {
		t.Errorf("work_01 Position = %d, want 3", got1.Position)
	}
}

func TestCreateWorkItemWithAutoPosition(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	w1 := sampleWorkItem("work_01")
	w1.Position = 0
	if err := s.CreateWorkItemWithAutoPosition(ctx, w1); err != nil {
		t.Fatalf("CreateWorkItemWithAutoPosition: %v", err)
	}

	got1, _ := s.GetWorkItem(ctx, "work_01")
	if got1.Position != 1 {
		t.Errorf("Position = %d, want 1", got1.Position)
	}

	w2 := sampleWorkItem("work_02")
	w2.Position = 0
	if err := s.CreateWorkItemWithAutoPosition(ctx, w2); err != nil {
		t.Fatalf("CreateWorkItemWithAutoPosition 2: %v", err)
	}

	got2, _ := s.GetWorkItem(ctx, "work_02")
	if got2.Position != 2 {
		t.Errorf("Position = %d, want 2", got2.Position)
	}
}

func TestCreateJobAndGet(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	seedSession(t, s, "sess_01")

	j := sampleJob("job_01", "sess_01")
	if err := s.CreateJob(ctx, j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	got, err := s.GetJob(ctx, "job_01")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.JobID != j.JobID {
		t.Errorf("JobID = %q, want %q", got.JobID, j.JobID)
	}
	if got.SessionID != j.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, j.SessionID)
	}
	if got.State != j.State {
		t.Errorf("State = %q, want %q", got.State, j.State)
	}
	if got.Summary["status"] != j.Summary["status"] {
		t.Errorf("Summary[status] = %v, want %v", got.Summary["status"], j.Summary["status"])
	}
}

func TestGetJobNotFound(t *testing.T) {
	s := openTestDB(t)
	_, err := s.GetJob(context.Background(), "job_nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateJob(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	seedSession(t, s, "sess_01")
	seedWorkItem(t, s, sampleWorkItem("work_01"))
	seedJob(t, s, sampleJob("job_01", "sess_01"))

	got, err := s.GetJob(ctx, "job_01")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}

	got.State = core.JobStateCompleted
	now := time.Now().UTC()
	got.FinishedAt = &now
	got.UpdatedAt = now
	got.Summary = map[string]any{"result": "success"}
	got.WorkID = "work_01"

	if err := s.UpdateJob(ctx, got); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}

	updated, err := s.GetJob(ctx, "job_01")
	if err != nil {
		t.Fatalf("GetJob after update: %v", err)
	}
	if updated.State != core.JobStateCompleted {
		t.Errorf("State = %q, want %q", updated.State, core.JobStateCompleted)
	}
	if updated.WorkID != "work_01" {
		t.Errorf("WorkID = %q, want %q", updated.WorkID, "work_01")
	}
	if updated.FinishedAt == nil {
		t.Fatal("FinishedAt is nil after update")
	}
}

func TestUpdateJobNotFound(t *testing.T) {
	s := openTestDB(t)
	j := sampleJob("job_nonexistent", "sess_01")
	err := s.UpdateJob(context.Background(), j)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateJobAndUpdateSession(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	seedSession(t, s, "sess_01")

	j := sampleJob("job_01", "sess_01")
	now := time.Now().UTC()
	if err := s.CreateJobAndUpdateSession(ctx, "sess_01", now, j); err != nil {
		t.Fatalf("CreateJobAndUpdateSession: %v", err)
	}

	gotJob, err := s.GetJob(ctx, "job_01")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if gotJob.JobID != "job_01" {
		t.Errorf("JobID = %q, want %q", gotJob.JobID, "job_01")
	}

	gotSess, err := s.GetSession(ctx, "sess_01")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if gotSess.LatestJobID != "job_01" {
		t.Errorf("LatestJobID = %q, want %q", gotSess.LatestJobID, "job_01")
	}
}

func TestBootstrapIdempotency(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	if err := s.bootstrap(ctx); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	if err := s.bootstrap(ctx); err != nil {
		t.Fatalf("third bootstrap: %v", err)
	}

	w := sampleWorkItem("work_after_rebootstrap")
	if err := s.CreateWorkItem(ctx, w); err != nil {
		t.Fatalf("CreateWorkItem after rebootstrap: %v", err)
	}

	got, err := s.GetWorkItem(ctx, "work_after_rebootstrap")
	if err != nil {
		t.Fatalf("GetWorkItem after rebootstrap: %v", err)
	}
	if got.WorkID != "work_after_rebootstrap" {
		t.Errorf("WorkID = %q, want %q", got.WorkID, "work_after_rebootstrap")
	}
}

func TestListJobs(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	seedSession(t, s, "sess_01")
	seedJob(t, s, sampleJob("job_01", "sess_01"))
	seedJob(t, s, sampleJob("job_02", "sess_01"))

	jobs, err := s.ListJobs(ctx, 10)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("len(jobs) = %d, want 2", len(jobs))
	}
}

func TestListJobsFiltered(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	seedSession(t, s, "sess_01")

	j1 := sampleJob("job_01", "sess_01")
	j1.State = core.JobStateCompleted
	j2 := sampleJob("job_02", "sess_01")
	j2.State = core.JobStateRunning
	seedJob(t, s, j1)
	seedJob(t, s, j2)

	jobs, err := s.ListJobsFiltered(ctx, 10, "", string(core.JobStateRunning), "")
	if err != nil {
		t.Fatalf("ListJobsFiltered: %v", err)
	}
	if len(jobs) != 1 || jobs[0].JobID != "job_02" {
		t.Fatalf("expected only job_02, got %d items", len(jobs))
	}
}

func TestRenewWorkItemLease(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	w.ExecutionState = core.WorkExecutionStateClaimed
	w.ClaimedBy = "agent-1"
	lease := time.Now().UTC().Add(30 * time.Minute)
	w.ClaimedUntil = &lease
	seedWorkItem(t, s, w)

	newLease := time.Now().UTC().Add(2 * time.Hour)
	renewed, err := s.RenewWorkItemLease(ctx, "work_01", "agent-1", newLease)
	if err != nil {
		t.Fatalf("RenewWorkItemLease: %v", err)
	}
	if renewed.ClaimedUntil == nil || renewed.ClaimedUntil.Before(time.Now().UTC().Add(time.Hour)) {
		t.Errorf("ClaimedUntil not extended properly: %v", renewed.ClaimedUntil)
	}
}

func TestRenewWorkItemLeaseWrongClaimant(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	w.ExecutionState = core.WorkExecutionStateClaimed
	w.ClaimedBy = "agent-1"
	lease := time.Now().UTC().Add(30 * time.Minute)
	w.ClaimedUntil = &lease
	seedWorkItem(t, s, w)

	_, err := s.RenewWorkItemLease(ctx, "work_01", "agent-2", time.Now().UTC().Add(2*time.Hour))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
}

func TestHumanLockedWorkCannotBeClaimed(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	w := sampleWorkItem("work_01")
	w.LockState = core.WorkLockStateHumanLocked
	seedWorkItem(t, s, w)

	_, err := s.ClaimWorkItem(ctx, "work_01", "agent-1", time.Now().UTC().Add(30*time.Minute))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy for human_locked work, got %v", err)
	}
}

func TestWorkEdgeCRUD(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	seedWorkItem(t, s, sampleWorkItem("work_01"))
	seedWorkItem(t, s, sampleWorkItem("work_02"))

	edge := core.WorkEdgeRecord{
		EdgeID:     "edge_01",
		FromWorkID: "work_01",
		ToWorkID:   "work_02",
		EdgeType:   "parent_of",
		CreatedBy:  "system",
		Metadata:   map[string]any{},
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.CreateWorkEdge(ctx, edge); err != nil {
		t.Fatalf("CreateWorkEdge: %v", err)
	}

	edges, err := s.ListWorkEdges(ctx, 10, "parent_of", "work_01", "")
	if err != nil {
		t.Fatalf("ListWorkEdges: %v", err)
	}
	if len(edges) != 1 || edges[0].EdgeID != "edge_01" {
		t.Fatalf("expected edge_01, got %d edges", len(edges))
	}

	if err := s.DeleteWorkEdge(ctx, "edge_01"); err != nil {
		t.Fatalf("DeleteWorkEdge: %v", err)
	}

	edges, err = s.ListWorkEdges(ctx, 10, "", "", "")
	if err != nil {
		t.Fatalf("ListWorkEdges after delete: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges after delete, got %d", len(edges))
	}
}

func TestWorkUpdates(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	seedWorkItem(t, s, sampleWorkItem("work_01"))

	u1 := core.WorkUpdateRecord{
		UpdateID:  "upd_01",
		WorkID:    "work_01",
		Message:   "started work",
		CreatedAt: time.Now().UTC(),
		Metadata:  map[string]any{},
	}
	u2 := core.WorkUpdateRecord{
		UpdateID:  "upd_02",
		WorkID:    "work_01",
		Message:   "finished work",
		CreatedAt: time.Now().UTC().Add(time.Second),
		Metadata:  map[string]any{},
	}
	if err := s.CreateWorkUpdate(ctx, u1); err != nil {
		t.Fatalf("CreateWorkUpdate: %v", err)
	}
	if err := s.CreateWorkUpdate(ctx, u2); err != nil {
		t.Fatalf("CreateWorkUpdate 2: %v", err)
	}

	updates, err := s.ListWorkUpdates(ctx, "work_01", 10)
	if err != nil {
		t.Fatalf("ListWorkUpdates: %v", err)
	}
	if len(updates) != 2 {
		t.Fatalf("len(updates) = %d, want 2", len(updates))
	}
	if updates[0].UpdateID != "upd_02" {
		t.Errorf("first update should be upd_02 (DESC order), got %q", updates[0].UpdateID)
	}
}

func TestWorkNotes(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	seedWorkItem(t, s, sampleWorkItem("work_01"))

	n := core.WorkNoteRecord{
		NoteID:    "note_01",
		WorkID:    "work_01",
		Body:      "found an issue",
		CreatedAt: time.Now().UTC(),
		Metadata:  map[string]any{},
	}
	if err := s.CreateWorkNote(ctx, n); err != nil {
		t.Fatalf("CreateWorkNote: %v", err)
	}

	notes, err := s.ListWorkNotes(ctx, "work_01", 10)
	if err != nil {
		t.Fatalf("ListWorkNotes: %v", err)
	}
	if len(notes) != 1 || notes[0].NoteID != "note_01" {
		t.Fatalf("expected note_01, got %d notes", len(notes))
	}
}

func TestUpsertDocContentAndGetByPath(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	seedWorkItem(t, s, sampleWorkItem("work_doc"))

	rec := core.DocContentRecord{
		DocID:  "doc_01",
		WorkID: "work_doc",
		Path:   "docs/review.md",
		Title:  "Review",
		Body:   "# Review\n",
		Format: "markdown",
	}
	if err := s.UpsertDocContent(ctx, rec); err != nil {
		t.Fatalf("UpsertDocContent: %v", err)
	}

	docs, err := s.GetDocContent(ctx, "work_doc")
	if err != nil {
		t.Fatalf("GetDocContent: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	if docs[0].DocID != rec.DocID || docs[0].Path != rec.Path || docs[0].Version != 1 {
		t.Fatalf("unexpected doc row: %+v", docs[0])
	}

	byPath, err := s.GetDocContentByPath(ctx, "docs/review.md")
	if err != nil {
		t.Fatalf("GetDocContentByPath: %v", err)
	}
	if byPath.WorkID != rec.WorkID || byPath.Title != rec.Title {
		t.Fatalf("unexpected doc by path: %+v", byPath)
	}
}

func TestUpsertDocContentIncrementsVersionOnExistingPath(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	seedWorkItem(t, s, sampleWorkItem("work_doc"))

	first := core.DocContentRecord{
		DocID:  "doc_01",
		WorkID: "work_doc",
		Path:   "docs/review.md",
		Title:  "Review",
		Body:   "# Review\n",
		Format: "markdown",
	}
	if err := s.UpsertDocContent(ctx, first); err != nil {
		t.Fatalf("UpsertDocContent first: %v", err)
	}

	second := core.DocContentRecord{
		DocID:  "doc_02",
		WorkID: "work_doc",
		Path:   "docs/review.md",
		Title:  "Updated Review",
		Body:   "# Review\nupdated\n",
		Format: "markdown",
	}
	if err := s.UpsertDocContent(ctx, second); err != nil {
		t.Fatalf("UpsertDocContent second: %v", err)
	}

	byPath, err := s.GetDocContentByPath(ctx, "docs/review.md")
	if err != nil {
		t.Fatalf("GetDocContentByPath: %v", err)
	}
	if byPath.DocID != first.DocID {
		t.Fatalf("expected original doc_id to remain stable, got %q", byPath.DocID)
	}
	if byPath.Version != 2 {
		t.Fatalf("expected version 2, got %d", byPath.Version)
	}
	if byPath.Title != second.Title || byPath.Body != second.Body {
		t.Fatalf("expected updated content, got %+v", byPath)
	}
}
