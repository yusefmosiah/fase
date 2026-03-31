package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yusefmosiah/cogent/internal/core"
)

func (s *Service) CreateWork(ctx context.Context, req WorkCreateRequest) (*core.WorkItemRecord, error) {
	title := strings.TrimSpace(req.Title)
	objective := strings.TrimSpace(req.Objective)
	if title == "" {
		return nil, fmt.Errorf("%w: title must not be empty", ErrInvalidInput)
	}
	if objective == "" {
		return nil, fmt.Errorf("%w: objective must not be empty", ErrInvalidInput)
	}

	// DEDUP CHECK: Prevent MCP tool call retries from creating duplicate work items
	// Check for recent duplicate work items with identical title and objective
	// within the last 5 seconds (MCP retry window).
	recentWork, _ := s.store.ListWorkItems(ctx, 100, "", "", "", true)
	for _, existing := range recentWork {
		if existing.Title == title && existing.Objective == objective {
			if time.Since(existing.CreatedAt) < 5*time.Second {
				// Return the existing work item instead of creating a duplicate
				// This prevents MCP retries from creating multiple copies
				s.Events.Publish(WorkEvent{
					Kind:   WorkEventCreated,
					WorkID: existing.WorkID,
					Title:  existing.Title,
					State:  string(existing.ExecutionState),
					Actor:  ActorFromCreatedBy(req.CreatedBy),
					Cause:  CauseWorkCreated,
					Metadata: map[string]string{
						"duplicate_suppressed": "true",
					},
				})
				return &existing, nil
			}
		}
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "task"
	}
	requiredDocs, err := s.normalizeRequiredDocPaths(ctx, req.RequiredDocs)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	lockState := req.LockState
	if lockState == "" {
		lockState = core.WorkLockStateUnlocked
	}
	work := core.WorkItemRecord{
		WorkID:               core.GenerateID("work"),
		Title:                title,
		Objective:            objective,
		Kind:                 kind,
		ExecutionState:       core.WorkExecutionStateReady,
		ApprovalState:        core.WorkApprovalStateNone,
		LockState:            lockState,
		Priority:             req.Priority,
		ConfigurationClass:   strings.TrimSpace(req.ConfigurationClass),
		BudgetClass:          strings.TrimSpace(req.BudgetClass),
		RequiredCapabilities: cloneSlice(req.RequiredCapabilities),
		RequiredModelTraits:  cloneSlice(req.RequiredModelTraits),
		PreferredAdapters:    cloneSlice(req.PreferredAdapters),
		ForbiddenAdapters:    cloneSlice(req.ForbiddenAdapters),
		PreferredModels:      cloneSlice(req.PreferredModels),
		AvoidModels:          cloneSlice(req.AvoidModels),
		Acceptance:           cloneMap(req.Acceptance),
		RequiredDocs:         requiredDocs,
		Metadata:             cloneMap(req.Metadata),
		HeadCommitOID:        strings.TrimSpace(req.HeadCommitOID),
		AttemptEpoch:         1,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if epoch, ok := metadataInt(work.Metadata, "attempt_epoch"); ok && epoch > 0 {
		work.AttemptEpoch = epoch
	}
	work.RequiredAttestations = resolvedRequiredAttestations(work, req.RequiredAttestations)
	if req.Position > 0 {
		work.Position = req.Position
		if err := s.store.CreateWorkItem(ctx, work); err != nil {
			return nil, err
		}
	} else {
		if err := s.store.CreateWorkItemWithAutoPosition(ctx, work); err != nil {
			return nil, err
		}
	}
	if req.ParentWorkID != "" {
		if _, err := s.store.GetWorkItem(ctx, req.ParentWorkID); err != nil {
			return nil, normalizeStoreError("work", req.ParentWorkID, err)
		}
		if err := s.attachParentEdge(ctx, req.ParentWorkID, work.WorkID, "service", now, map[string]any{}, false); err != nil {
			return nil, err
		}
	}
	s.Events.Publish(WorkEvent{
		Kind:   WorkEventCreated,
		WorkID: work.WorkID,
		Title:  work.Title,
		State:  string(work.ExecutionState),
		Actor:  ActorFromCreatedBy(req.CreatedBy),
		Cause:  CauseWorkCreated,
	})
	return &work, nil
}

// MoveWork repositions a work item to newPosition, shifting other items to keep positions contiguous.
// newPosition is 1-based (1 = front of queue). If newPosition <= 0 it is treated as 1.
func (s *Service) MoveWork(ctx context.Context, workID string, newPosition int) (*core.WorkItemRecord, error) {
	if newPosition <= 0 {
		newPosition = 1
	}
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	if work.Position == newPosition {
		return &work, nil
	}
	if err := s.store.MoveWorkPosition(ctx, workID, newPosition); err != nil {
		return nil, err
	}
	work.Position = newPosition
	return &work, nil
}

// InsertWorkBefore moves workID to just before beforeWorkID in the queue.
func (s *Service) InsertWorkBefore(ctx context.Context, workID, beforeWorkID string) (*core.WorkItemRecord, error) {
	before, err := s.store.GetWorkItem(ctx, beforeWorkID)
	if err != nil {
		return nil, normalizeStoreError("work", beforeWorkID, err)
	}
	return s.MoveWork(ctx, workID, before.Position)
}

// MoveToFront moves a work item to position 1 (front of the dispatch queue).
func (s *Service) MoveToFront(ctx context.Context, workID string) (*core.WorkItemRecord, error) {
	return s.MoveWork(ctx, workID, 1)
}

// ReorderQueue assigns sequential positions 1..N to the given work IDs in order.
// Any work items not in the list retain their existing positions (shifted to follow the reordered items).
func (s *Service) ReorderQueue(ctx context.Context, workIDs []string) error {
	return s.store.ReorderWorkPositions(ctx, workIDs)
}

func (s *Service) Work(ctx context.Context, workID string) (*WorkShowResult, error) {
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	children, err := s.store.ListWorkChildren(ctx, workID, 100)
	if err != nil {
		return nil, err
	}
	updates, err := s.store.ListWorkUpdates(ctx, workID, 50)
	if err != nil {
		return nil, err
	}
	notes, err := s.store.ListWorkNotes(ctx, workID, 50)
	if err != nil {
		return nil, err
	}
	jobs, err := s.store.ListJobsByWork(ctx, workID, 20)
	if err != nil {
		return nil, err
	}
	targetProposals, err := s.store.ListWorkProposals(ctx, 50, "", workID, "")
	if err != nil {
		return nil, err
	}
	sourceProposals, err := s.store.ListWorkProposals(ctx, 50, "", "", workID)
	if err != nil {
		return nil, err
	}
	proposals := targetProposals
	seenProposals := map[string]bool{}
	for _, proposal := range proposals {
		seenProposals[proposal.ProposalID] = true
	}
	for _, proposal := range sourceProposals {
		if seenProposals[proposal.ProposalID] {
			continue
		}
		proposals = append(proposals, proposal)
	}
	checkRecords, err := s.store.ListCheckRecords(ctx, workID, 50)
	if err != nil {
		return nil, err
	}
	attestations, err := s.store.ListAttestationRecords(ctx, "work", workID, 50)
	if err != nil {
		return nil, err
	}
	approvals, err := s.store.ListApprovalRecords(ctx, workID, 50)
	if err != nil {
		return nil, err
	}
	promotions, err := s.store.ListPromotionRecords(ctx, workID, 50)
	if err != nil {
		return nil, err
	}
	artifacts, err := s.listArtifactsForWork(ctx, workID, 50)
	if err != nil {
		return nil, err
	}
	docs, _ := s.GetDocContent(ctx, workID)

	return &WorkShowResult{
		Work:         work,
		Children:     children,
		Updates:      updates,
		Notes:        notes,
		Jobs:         jobs,
		Proposals:    proposals,
		CheckRecords: checkRecords,
		Attestations: attestations,
		Approvals:    approvals,
		Promotions:   promotions,
		Artifacts:    artifacts,
		Docs:         docs,
	}, nil
}


func (s *Service) ListWork(ctx context.Context, req WorkListRequest) ([]core.WorkItemRecord, error) {
	return s.store.ListWorkItems(ctx, req.Limit, req.Kind, req.ExecutionState, req.ApprovalState, req.IncludeArchived)
}

func (s *Service) ReadyWork(ctx context.Context, limit int, includeArchived bool) ([]core.WorkItemRecord, error) {
	items, err := s.store.ListReadyWork(ctx, limit*4, includeArchived)
	if err != nil {
		return nil, err
	}
	// Filter out work items whose required_model_traits can't be satisfied by any
	// available catalog model. Work items with explicit preferred models/adapters
	// that are in the catalog are considered satisfiable regardless of trait tags.
	// Auto-syncs catalog if no snapshot exists so the filter is always applied.
	if snapshot, snapErr := s.catalogSnapshotOrSync(ctx); snapErr == nil {
		items = filterWorkByModelTraits(items, snapshot.Entries)
	}
	// ADR-0040: supervisor owns dispatch decisions. Runtime filtering
	// is no longer applied here — the supervisor scores and selects
	// adapters/models based on work item preferences and health.
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// filterWorkByModelTraits removes work items whose required_model_traits cannot
// be satisfied by any available catalog entry. Work items with preferred
// models or adapters available in the catalog are considered satisfiable even
// if the catalog entries lack explicit trait tags.
func filterWorkByModelTraits(items []core.WorkItemRecord, entries []core.CatalogEntry) []core.WorkItemRecord {
	if len(entries) == 0 {
		return items
	}
	availableTraits := map[string]struct{}{}
	availableModels := map[string]struct{}{}
	availableAdapters := map[string]struct{}{}
	for _, e := range entries {
		if !e.Available {
			continue
		}
		for _, t := range e.Traits {
			availableTraits[t] = struct{}{}
		}
		if e.Model != "" {
			availableModels[e.Model] = struct{}{}
		}
		if e.Adapter != "" {
			availableAdapters[e.Adapter] = struct{}{}
		}
	}
	var result []core.WorkItemRecord
	for _, item := range items {
		if canSatisfyModelTraits(item, availableTraits, availableModels, availableAdapters) {
			result = append(result, item)
		}
	}
	return result
}

func canSatisfyModelTraits(item core.WorkItemRecord, availableTraits, availableModels, availableAdapters map[string]struct{}) bool {
	if len(item.RequiredModelTraits) == 0 {
		// If the item specifies preferred adapters and none are available in the
		// catalog, exclude it — no available adapter can satisfy the request.
		if len(item.PreferredAdapters) > 0 {
			for _, a := range item.PreferredAdapters {
				if _, ok := availableAdapters[a]; ok {
					return true
				}
			}
			return false
		}
		return true
	}
	for _, m := range item.PreferredModels {
		if _, ok := availableModels[m]; ok {
			return true
		}
	}
	for _, a := range item.PreferredAdapters {
		if _, ok := availableAdapters[a]; ok {
			return true
		}
	}
	for _, t := range item.RequiredModelTraits {
		if _, ok := availableTraits[t]; !ok {
			return false
		}
	}
	return true
}


func (s *Service) UpdateWork(ctx context.Context, req WorkUpdateRequest) (*core.WorkItemRecord, error) {
	work, err := s.store.GetWorkItem(ctx, req.WorkID)
	if err != nil {
		return nil, normalizeStoreError("work", req.WorkID, err)
	}
	prevState := string(work.ExecutionState)
	now := time.Now().UTC()
	if req.ExecutionState != "" {
		if !req.ExecutionState.Valid() {
			return nil, fmt.Errorf("%w: invalid execution state %q", ErrInvalidInput, req.ExecutionState)
		}
		req.ExecutionState = req.ExecutionState.Canonical()
		// Guard: cannot transition to done or archived via UpdateWork if
		// completion gates are unresolved. Terminal-success transitions require
		// canonical completion checks to pass; failed/cancelled are exempt.
		if req.ExecutionState == core.WorkExecutionStateDone || req.ExecutionState == core.WorkExecutionStateArchived {
			if req.ForceDone {
				emitForceDoneWarning(req.WorkID, req.CreatedBy)
			} else {
				if err := s.guardDoneTransition(ctx, work); err != nil {
					return nil, err
				}
			}
		}
		work.ExecutionState = req.ExecutionState
	}
	if req.ApprovalState != "" {
		work.ApprovalState = req.ApprovalState
	}
	if req.LockState != "" {
		work.LockState = req.LockState
	}
	if req.Phase != "" {
		work.Phase = req.Phase
	}
	if req.ExecutionState == core.WorkExecutionStateReady ||
		req.ExecutionState == core.WorkExecutionStateBlocked ||
		req.ExecutionState == core.WorkExecutionStateDone ||
		req.ExecutionState == core.WorkExecutionStateFailed ||
		req.ExecutionState == core.WorkExecutionStateCancelled ||
		req.ExecutionState == core.WorkExecutionStateArchived {
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
	}
	if req.JobID != "" {
		work.CurrentJobID = req.JobID
	}
	if req.SessionID != "" {
		work.CurrentSessionID = req.SessionID
	}
	if req.Metadata != nil {
		if work.Metadata == nil {
			work.Metadata = map[string]any{}
		}
		for k, v := range req.Metadata {
			work.Metadata[k] = v
		}
	}
	if req.LockState == core.WorkLockStateHumanLocked {
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
	}
	if req.ExecutionState == core.WorkExecutionStateDone && req.ApprovalState == "" && shouldSetPendingApproval(work) {
		work.ApprovalState = core.WorkApprovalStatePending
	}
	work.UpdatedAt = now
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, err
	}
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:       core.GenerateID("wup"),
		WorkID:         work.WorkID,
		ExecutionState: req.ExecutionState,
		ApprovalState:  req.ApprovalState,
		Phase:          req.Phase,
		Message:        req.Message,
		JobID:          req.JobID,
		SessionID:      req.SessionID,
		ArtifactID:     req.ArtifactID,
		Metadata:       cloneMap(req.Metadata),
		CreatedBy:      req.CreatedBy,
		CreatedAt:      now,
	}); err != nil {
		return nil, err
	}
	ev := WorkEvent{
		Kind:      WorkEventUpdated,
		WorkID:    work.WorkID,
		Title:     work.Title,
		State:     string(work.ExecutionState),
		PrevState: prevState,
		JobID:     work.CurrentJobID,
		Actor:     ActorFromCreatedBy(req.CreatedBy),
	}
	if work.ExecutionState.Terminal() {
		ev.Cause = CauseWorkerTerminal
	} else {
		ev.Cause = CauseWorkerProgress
	}
	if req.Message != "" {
		ev.Metadata = map[string]string{"message": req.Message}
	}
	s.Events.Publish(ev)

	// Send email notification on first transition to a terminal state.
	// Deduplicate: only send if previous state was not already that terminal state.
	if string(work.ExecutionState) == "done" && prevState != "done" {
		s.sendWorkNotification(context.Background(), work, req.Message)
	} else if string(work.ExecutionState) == "failed" && prevState != "failed" {
		s.sendWorkFailureNotification(context.Background(), work, req.Message)
	}

	return &work, nil
}

func (s *Service) SetWorkLock(ctx context.Context, workID string, lockState core.WorkLockState, createdBy, message string) (*core.WorkItemRecord, error) {
	if lockState == "" {
		return nil, fmt.Errorf("%w: lock state must not be empty", ErrInvalidInput)
	}
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	work.LockState = lockState
	if lockState == core.WorkLockStateHumanLocked {
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
	}
	work.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, err
	}
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:  core.GenerateID("wup"),
		WorkID:    work.WorkID,
		Message:   message,
		Metadata:  map[string]any{"lock_state": string(lockState)},
		CreatedBy: createdBy,
		CreatedAt: work.UpdatedAt,
	}); err != nil {
		return nil, err
	}
	return &work, nil
}

// ResetWork starts a new attempt epoch for a work item. This clears the
// current job/session linkage, increments the attempt epoch, and resets the
// execution state to ready. Prior children, nonces, and review artifacts are
// preserved as historical records but will not satisfy the new attempt.
// This implements the VAL-LIFECYCLE-005 contract.
func (s *Service) ResetWork(ctx context.Context, req WorkResetRequest) (*core.WorkItemRecord, error) {
	work, err := s.store.GetWorkItem(ctx, req.WorkID)
	if err != nil {
		return nil, normalizeStoreError("work", req.WorkID, err)
	}

	now := time.Now().UTC()
	prevState := string(work.ExecutionState)
	previousTerminal := work.ExecutionState.Terminal()

	// Increment attempt epoch to isolate this new attempt from prior history
	work.AttemptEpoch = workAttemptEpoch(work) + 1

	// Clear current attempt linkage
	work.CurrentJobID = ""
	work.CurrentSessionID = ""
	work.ExecutionState = core.WorkExecutionStateReady
	work.ApprovalState = core.WorkApprovalStateNone

	// Clear claim state so the new attempt never inherits a stale lease.
	if req.ClearClaims || previousTerminal || work.ClaimedBy != "" || work.ClaimedUntil != nil {
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
	}

	// Reset attestation freeze to allow new attestations for this epoch
	work.AttestationFrozenAt = nil

	// Clear the attestation nonce from metadata to ensure old attestations
	// cannot be replayed against this new attempt
	if work.Metadata != nil {
		delete(work.Metadata, "attestation_nonce")
	}

	work.UpdatedAt = now

	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, err
	}

	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:       core.GenerateID("wup"),
		WorkID:         work.WorkID,
		ExecutionState: work.ExecutionState,
		ApprovalState:  work.ApprovalState,
		Message:        fmt.Sprintf("Reset to attempt %d: %s", work.AttemptEpoch, req.Reason),
		CreatedBy:      req.CreatedBy,
		CreatedAt:      now,
		Metadata: map[string]any{
			"attempt_epoch": work.AttemptEpoch,
			"prev_state":    prevState,
			"reset_reason":  req.Reason,
		},
	}); err != nil {
		return nil, err
	}

	s.Events.Publish(WorkEvent{
		Kind:      WorkEventUpdated,
		WorkID:    work.WorkID,
		Title:     work.Title,
		State:     string(work.ExecutionState),
		PrevState: prevState,
		Actor:     ActorFromCreatedBy(req.CreatedBy),
		Cause:     CauseHostManual,
		Metadata: map[string]string{
			"attempt_epoch": fmt.Sprintf("%d", work.AttemptEpoch),
		},
	})

	return &work, nil
}
func (s *Service) ApproveWork(ctx context.Context, workID, createdBy, message string) (*core.WorkItemRecord, error) {
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	if issues, err := s.completionGateIssues(ctx, work); err != nil {
		return nil, err
	} else if len(issues) > 0 {
		return nil, fmt.Errorf("%w: completion policy unresolved: %s", ErrInvalidInput, strings.Join(issues, "; "))
	}
	attestations, err := s.store.ListAttestationRecords(ctx, "work", workID, 200)
	if err != nil {
		return nil, err
	}
	if !blockingAttestationsSatisfied(work, attestations) {
		return nil, fmt.Errorf("%w: blocking attestation policy unresolved", ErrInvalidInput)
	}
	previousApprovals, err := s.store.ListApprovalRecords(ctx, workID, 1)
	if err != nil {
		return nil, err
	}
	work.ApprovalState = core.WorkApprovalStateVerified
	work.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, err
	}
	approval := core.ApprovalRecord{
		ApprovalID:        core.GenerateID("approval"),
		WorkID:            work.WorkID,
		ApprovedCommitOID: work.HeadCommitOID,
		AttestationIDs:    collectAttestationIDs(attestations),
		Status:            "approved",
		ApprovedBy:        createdBy,
		ApprovedAt:        work.UpdatedAt,
		Metadata:          map[string]any{"message": message},
	}
	if len(previousApprovals) > 0 {
		approval.SupersedesApprovalID = previousApprovals[0].ApprovalID
	}
	if err := s.store.CreateApprovalRecord(ctx, approval); err != nil {
		return nil, err
	}
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:      core.GenerateID("wup"),
		WorkID:        work.WorkID,
		ApprovalState: work.ApprovalState,
		Message:       message,
		CreatedBy:     createdBy,
		CreatedAt:     work.UpdatedAt,
		Metadata:      map[string]any{"approval_action": "approve"},
	}); err != nil {
		return nil, err
	}
	return &work, nil
}

func (s *Service) RejectWork(ctx context.Context, workID, createdBy, message string) (*core.WorkItemRecord, error) {
	work, err := s.store.GetWorkItem(ctx, workID)
	if err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	attestations, err := s.store.ListAttestationRecords(ctx, "work", workID, 200)
	if err != nil {
		return nil, err
	}
	previousApprovals, err := s.store.ListApprovalRecords(ctx, workID, 1)
	if err != nil {
		return nil, err
	}
	work.ApprovalState = core.WorkApprovalStateRejected
	work.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, err
	}
	approval := core.ApprovalRecord{
		ApprovalID:        core.GenerateID("approval"),
		WorkID:            work.WorkID,
		ApprovedCommitOID: work.HeadCommitOID,
		AttestationIDs:    collectAttestationIDs(attestations),
		Status:            "rejected",
		ApprovedBy:        createdBy,
		ApprovedAt:        work.UpdatedAt,
		Metadata:          map[string]any{"message": message},
	}
	if len(previousApprovals) > 0 {
		approval.SupersedesApprovalID = previousApprovals[0].ApprovalID
	}
	if err := s.store.CreateApprovalRecord(ctx, approval); err != nil {
		return nil, err
	}
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:      core.GenerateID("wup"),
		WorkID:        work.WorkID,
		ApprovalState: work.ApprovalState,
		Message:       message,
		CreatedBy:     createdBy,
		CreatedAt:     work.UpdatedAt,
		Metadata:      map[string]any{"approval_action": "reject"},
	}); err != nil {
		return nil, err
	}
	return &work, nil
}

func (s *Service) PromoteWork(ctx context.Context, req WorkPromoteRequest) (*core.PromotionRecord, *core.WorkItemRecord, error) {
	work, err := s.store.GetWorkItem(ctx, req.WorkID)
	if err != nil {
		return nil, nil, normalizeStoreError("work", req.WorkID, err)
	}
	if issues, err := s.completionGateIssues(ctx, work); err != nil {
		return nil, nil, err
	} else if len(issues) > 0 {
		return nil, nil, fmt.Errorf("%w: completion policy unresolved: %s", ErrInvalidInput, strings.Join(issues, "; "))
	}
	if work.ApprovalState != core.WorkApprovalStateVerified {
		return nil, nil, fmt.Errorf("%w: work must be approved before promotion", ErrInvalidInput)
	}
	approvals, err := s.store.ListApprovalRecords(ctx, req.WorkID, 1)
	if err != nil {
		return nil, nil, err
	}
	if len(approvals) == 0 || approvals[0].Status != "approved" {
		return nil, nil, fmt.Errorf("%w: missing approval record for promotion", ErrInvalidInput)
	}
	now := time.Now().UTC()
	promotion := core.PromotionRecord{
		PromotionID:       core.GenerateID("promote"),
		WorkID:            work.WorkID,
		ApprovalID:        approvals[0].ApprovalID,
		Environment:       strings.TrimSpace(req.Environment),
		PromotedCommitOID: work.HeadCommitOID,
		TargetRef:         strings.TrimSpace(req.TargetRef),
		Status:            "promoted",
		PromotedBy:        req.CreatedBy,
		PromotedAt:        now,
		Metadata:          map[string]any{"message": req.Message},
	}
	if promotion.Environment == "" {
		promotion.Environment = "staging"
	}
	if err := s.store.CreatePromotionRecord(ctx, promotion); err != nil {
		return nil, nil, err
	}
	if work.Metadata == nil {
		work.Metadata = map[string]any{}
	}
	work.Metadata["promoted"] = true
	work.Metadata["promoted_environment"] = promotion.Environment
	work.UpdatedAt = now
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, nil, err
	}
	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:      core.GenerateID("wup"),
		WorkID:        work.WorkID,
		ApprovalState: work.ApprovalState,
		Message:       firstNonEmpty(req.Message, "promoted"),
		CreatedBy:     req.CreatedBy,
		CreatedAt:     now,
		Metadata: map[string]any{
			"promotion_id": promotion.PromotionID,
			"environment":  promotion.Environment,
			"target_ref":   promotion.TargetRef,
		},
	}); err != nil {
		return nil, nil, err
	}
	return &promotion, &work, nil
}

func (s *Service) AttestWork(ctx context.Context, req WorkAttestRequest) (*core.AttestationRecord, *core.WorkItemRecord, error) {
	work, err := s.store.GetWorkItem(ctx, req.WorkID)
	if err != nil {
		return nil, nil, normalizeStoreError("work", req.WorkID, err)
	}
	attestationTarget := work
	if strings.EqualFold(work.Kind, "attest") {
		parentID := ""
		if work.Metadata != nil {
			if rawParentID, ok := work.Metadata["parent_work_id"].(string); ok {
				parentID = strings.TrimSpace(rawParentID)
			}
		}
		if parentID != "" {
			if parent, parentErr := s.store.GetWorkItem(ctx, parentID); parentErr == nil {
				attestationTarget = parent
			}
		}
	}
	prevState := string(work.ExecutionState)
	// Nonce validation: if the work item has an attestation nonce,
	// the caller must provide it. The nonce is generated after the worker
	// exits, so workers cannot attest their own work.
	// Additionally, attestations must match the current attempt epoch to prevent
	// stale attestations from prior attempts satisfying a new run (VAL-LIFECYCLE-005).
	if storedNonce := summaryString(attestationTarget.Metadata, "attestation_nonce"); storedNonce != "" {
		if req.Nonce == "" || req.Nonce != storedNonce {
			return nil, nil, fmt.Errorf("attestation nonce mismatch: work item requires valid nonce (generated post-completion)")
		}
	}
	// For attestation children, verify the attempt epoch matches the parent's current epoch
	if strings.EqualFold(work.Kind, "attest") && attestationTarget.WorkID != work.WorkID {
		childEpoch := workAttemptEpoch(work)
		if !matchesCurrentWorkAttempt(attestationTarget, work) {
			return nil, nil, fmt.Errorf("attestation epoch mismatch: attestation child is from attempt %d but parent is now at attempt %d", childEpoch, workAttemptEpoch(attestationTarget))
		}
	}
	now := time.Now().UTC()
	metadata := cloneMap(req.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["attempt_epoch"] = workAttemptEpoch(attestationTarget)
	if nonce := summaryString(attestationTarget.Metadata, "attestation_nonce"); nonce != "" {
		metadata["attestation_nonce"] = nonce
	}
	if attestationTarget.HeadCommitOID != "" {
		if _, ok := metadata["commit_oid"]; !ok {
			metadata["commit_oid"] = attestationTarget.HeadCommitOID
		}
	}

	unsatisfiedSlots := []core.RequiredAttestation(nil)
	if len(attestationTarget.RequiredAttestations) > 0 {
		attestations, fetchErr := s.store.ListAttestationRecords(ctx, "work", attestationTarget.WorkID, 200)
		if fetchErr != nil {
			return nil, nil, fetchErr
		}
		unsatisfiedSlots = pendingBlockingAttestationSlots(attestationTarget, attestations)
	}

	verifierKind := strings.TrimSpace(req.VerifierKind)
	method := strings.TrimSpace(req.Method)
	if len(unsatisfiedSlots) == 1 {
		if verifierKind == "" {
			verifierKind = strings.TrimSpace(unsatisfiedSlots[0].VerifierKind)
		}
		if method == "" {
			method = strings.TrimSpace(unsatisfiedSlots[0].Method)
		}
	}
	if verifierKind == "" || method == "" {
		return nil, nil, fmt.Errorf("%w: attestation requires non-empty verifier_kind and method; got verifier_kind=%q method=%q", ErrInvalidInput, verifierKind, method)
	}
	if len(unsatisfiedSlots) > 0 && !matchesAnyPendingAttestationSlot(verifierKind, method, unsatisfiedSlots) {
		return nil, nil, fmt.Errorf("%w: attestation verifier_kind/method must match one unsatisfied required attestation slot; expected one of %s, got verifier_kind=%q method=%q", ErrInvalidInput, describeAttestationSlots(unsatisfiedSlots), verifierKind, method)
	}

	record := core.AttestationRecord{
		AttestationID:           core.GenerateID("attest"),
		SubjectKind:             "work",
		SubjectID:               attestationTarget.WorkID,
		Result:                  req.Result,
		Summary:                 req.Summary,
		ArtifactID:              req.ArtifactID,
		JobID:                   req.JobID,
		SessionID:               req.SessionID,
		Method:                  method,
		VerifierKind:            verifierKind,
		VerifierIdentity:        strings.TrimSpace(req.VerifierIdentity),
		Confidence:              req.Confidence,
		Blocking:                req.Blocking,
		SupersedesAttestationID: strings.TrimSpace(req.SupersedesAttestationID),
		SignerPubkey:            strings.TrimSpace(req.SignerPubkey),
		Metadata:                metadata,
		CreatedBy:               req.CreatedBy,
		CreatedAt:               now,
	}
	if record.VerifierKind == "" {
		record.VerifierKind = "manual"
	}
	if err := s.store.CreateAttestationRecord(ctx, record); err != nil {
		return nil, nil, err
	}

	// Attestation is transactional: recording the attestation may also advance
	// the work item's execution state when the completion gate is already
	// satisfied by other evidence (for example, a passing check record).
	switch req.Result {
	case "passed":
		shouldSetDone := true
		if issues, gateErr := s.completionGateIssues(ctx, work); gateErr != nil {
			return nil, nil, gateErr
		} else if len(issues) > 0 {
			shouldSetDone = false
		}
		if shouldSetDone {
			work.ExecutionState = core.WorkExecutionStateDone
			work.ClaimedBy = ""
			work.ClaimedUntil = nil
			if shouldSetPendingApproval(work) {
				work.ApprovalState = core.WorkApprovalStatePending
			}
		}
	case "failed":
		work.ExecutionState = core.WorkExecutionStateFailed
		work.ClaimedBy = ""
		work.ClaimedUntil = nil
	}
	work.UpdatedAt = now
	if err := s.store.UpdateWorkItem(ctx, work); err != nil {
		return nil, nil, err
	}

	if err := s.store.CreateWorkUpdate(ctx, core.WorkUpdateRecord{
		UpdateID:       core.GenerateID("wup"),
		WorkID:         work.WorkID,
		ExecutionState: work.ExecutionState,
		ApprovalState:  work.ApprovalState,
		Message:        req.Summary,
		JobID:          req.JobID,
		SessionID:      req.SessionID,
		ArtifactID:     req.ArtifactID,
		CreatedBy:      req.CreatedBy,
		CreatedAt:      now,
		Metadata: map[string]any{
			"attestation_result":   req.Result,
			"attestation_method":   record.Method,
			"attestation_verifier": record.VerifierKind,
		},
	}); err != nil {
		return nil, nil, err
	}
	s.Events.Publish(WorkEvent{
		Kind:      WorkEventAttested,
		WorkID:    work.WorkID,
		Title:     work.Title,
		State:     string(work.ExecutionState),
		PrevState: prevState,
		Actor:     ActorService,
		Cause:     CauseAttestationRecorded,
		Metadata: map[string]string{
			"result":  req.Result,
			"summary": req.Summary,
		},
	})
	// Send email notification on attestation result (fire and forget).
	// Notify about the attestation target (the actual work being attested, not the attest child).
	if req.Result == "passed" || req.Result == "failed" {
		s.sendAttestationNotification(context.Background(), attestationTarget, record)
	}
	return &record, &work, nil
}

// SignAttestationRecord updates an attestation record with a cryptographic signature.
func (s *Service) SignAttestationRecord(ctx context.Context, attestationID, signature string) error {
	return s.store.UpdateAttestationSignature(ctx, attestationID, signature)
}

func (s *Service) AddWorkNote(ctx context.Context, req WorkNoteRequest) (*core.WorkNoteRecord, error) {
	if strings.TrimSpace(req.Body) == "" {
		return nil, fmt.Errorf("%w: note body must not be empty", ErrInvalidInput)
	}
	if _, err := s.store.GetWorkItem(ctx, req.WorkID); err != nil {
		return nil, normalizeStoreError("work", req.WorkID, err)
	}
	note := core.WorkNoteRecord{
		NoteID:    core.GenerateID("wnote"),
		WorkID:    req.WorkID,
		NoteType:  req.NoteType,
		Body:      req.Body,
		Metadata:  cloneMap(req.Metadata),
		CreatedBy: req.CreatedBy,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateWorkNote(ctx, note); err != nil {
		return nil, err
	}
	return &note, nil
}

func (s *Service) AddPrivateNote(ctx context.Context, workID, noteType, text, createdBy string) (*core.WorkNoteRecord, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: note text must not be empty", ErrInvalidInput)
	}
	if _, err := s.store.GetWorkItem(ctx, workID); err != nil {
		return nil, normalizeStoreError("work", workID, err)
	}
	noteID := core.GenerateID("pnote")
	if err := s.store.AddPrivateNote(ctx, noteID, workID, noteType, text, createdBy); err != nil {
		return nil, err
	}
	return &core.WorkNoteRecord{
		NoteID:    noteID,
		WorkID:    workID,
		NoteType:  noteType,
		Body:      text,
		CreatedBy: createdBy,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (s *Service) ListPrivateNotes(ctx context.Context, workID string) ([]core.WorkNoteRecord, error) {
	return s.store.ListPrivateNotes(ctx, workID, 50)
}

// SetDocContent stores doc content and auto-creates a work item if workID is empty.
// This guarantees every doc has a corresponding work item.
