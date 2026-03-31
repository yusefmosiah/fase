package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yusefmosiah/cogent/internal/core"
	"github.com/yusefmosiah/cogent/internal/store"
)

func (s *Service) buildTransferPacket(
	job core.JobRecord,
	session core.SessionRecord,
	turns []core.TurnRecord,
	events []core.EventRecord,
	artifacts []core.ArtifactRecord,
	reason string,
	mode string,
) core.TransferPacket {
	mode = normalizeTransferMode(mode)
	packet := core.TransferPacket{
		TransferID: core.GenerateID("xfer"),
		ExportedAt: time.Now().UTC(),
		Mode:       mode,
		Reason:     strings.TrimSpace(reason),
		Disclaimer: "This is a context transfer, not native session continuation.",
		Source: core.TransferSource{
			Adapter:         job.Adapter,
			Model:           summaryString(job.Summary, "model"),
			JobID:           job.JobID,
			SessionID:       session.SessionID,
			NativeSessionID: job.NativeSessionID,
			CWD:             session.CWD,
		},
		Objective:            latestObjective(turns),
		Summary:              summarizeTurns(turns),
		Unresolved:           collectUnresolved(job, events),
		ImportantFiles:       collectImportantFiles(session.CWD, events, artifacts),
		RecentTurnsInline:    condenseTurns(turns, 3),
		RecentEventsInline:   condenseEvents(events, 6),
		EvidenceRefs:         []core.TransferArtifact{},
		Artifacts:            toTransferArtifacts(s.Paths.StateDir, artifacts),
		Constraints:          []string{"Keep CLI flags and JSON output backward compatible.", fmt.Sprintf("Work within %s.", session.CWD)},
		RecommendedNextSteps: recommendNextSteps(job, turns),
	}
	if packet.Objective == "" {
		packet.Objective = "Continue the latest session objective."
	}
	if packet.Summary == "" {
		packet.Summary = "No prior turn summary was captured."
	}
	if packet.Reason == "" {
		packet.Reason = defaultTransferReason(job)
	}
	if len(packet.Unresolved) == 0 && job.State != core.JobStateCompleted {
		packet.Unresolved = []string{fmt.Sprintf("Latest job ended in state %s.", job.State)}
	}
	if packet.Unresolved == nil {
		packet.Unresolved = []string{}
	}
	if packet.ImportantFiles == nil {
		packet.ImportantFiles = []string{}
	}
	if packet.RecentTurnsInline == nil {
		packet.RecentTurnsInline = []core.TurnRecord{}
	}
	if packet.RecentEventsInline == nil {
		packet.RecentEventsInline = []core.EventRecord{}
	}
	if packet.Artifacts == nil {
		packet.Artifacts = []core.TransferArtifact{}
	}
	if packet.EvidenceRefs == nil {
		packet.EvidenceRefs = []core.TransferArtifact{}
	}
	return packet
}

func (s *Service) writeTransferBundle(packet core.TransferPacket, outputPath string, turns []core.TurnRecord, events []core.EventRecord) (core.TransferPacket, string, error) {
	path := outputPath
	if path == "" {
		path = filepath.Join(s.Paths.TransfersDir, packet.TransferID, "transfer.json")
	} else {
		expanded, err := core.ExpandPath(outputPath)
		if err != nil {
			return packet, "", fmt.Errorf("%w: expand transfer output path: %v", ErrInvalidInput, err)
		}
		path = expanded
	}
	if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return packet, "", fmt.Errorf("%w: resolve transfer output path: %v", ErrInvalidInput, err)
		}
		path = absolute
	}

	bundleDir := filepath.Dir(path)
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return packet, "", fmt.Errorf("create transfer directory: %w", err)
	}

	turnsPath := filepath.Join(bundleDir, "recent_turns.json")
	eventsPath := filepath.Join(bundleDir, "recent_events.jsonl")
	if err := writeIndentedJSON(turnsPath, turns); err != nil {
		return packet, "", err
	}
	if err := writeJSONL(eventsPath, condenseEvents(events, 20)); err != nil {
		return packet, "", err
	}
	packet.EvidenceRefs = append(packet.EvidenceRefs,
		core.TransferArtifact{Kind: "recent_turns_json", Path: turnsPath},
		core.TransferArtifact{Kind: "recent_events_jsonl", Path: eventsPath},
	)

	encoded, err := json.MarshalIndent(packet, "", "  ")
	if err != nil {
		return packet, "", fmt.Errorf("marshal transfer packet: %w", err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		return packet, "", fmt.Errorf("write transfer packet: %w", err)
	}

	return packet, path, nil
}

func (s *Service) loadTransfer(ctx context.Context, ref string) (core.TransferRecord, error) {
	if stat, err := os.Stat(ref); err == nil && !stat.IsDir() {
		data, err := os.ReadFile(ref)
		if err != nil {
			return core.TransferRecord{}, fmt.Errorf("read transfer file: %w", err)
		}
		var packet core.TransferPacket
		if err := json.Unmarshal(data, &packet); err != nil {
			return core.TransferRecord{}, fmt.Errorf("%w: decode transfer file: %v", ErrInvalidInput, err)
		}
		return core.TransferRecord{
			TransferID: packet.TransferID,
			JobID:      packet.Source.JobID,
			SessionID:  packet.Source.SessionID,
			CreatedAt:  packet.ExportedAt,
			Packet:     packet,
		}, nil
	}

	record, err := s.store.GetTransfer(ctx, ref)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return core.TransferRecord{}, fmt.Errorf("%w: transfer %s", ErrNotFound, ref)
		}
		return core.TransferRecord{}, err
	}
	return record, nil
}

func defaultTransferReason(job core.JobRecord) string {
	if job.State != core.JobStateCompleted {
		return fmt.Sprintf("source job ended in state %s", job.State)
	}
	return "operator-requested context transfer"
}

func normalizeDebriefReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "operator-requested debrief"
	}
	return reason
}

func normalizeTransferMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "", "manual":
		return "manual"
	case "recovery", "operator_override", "cost", "capability":
		return mode
	default:
		return "manual"
	}
}

func latestObjective(turns []core.TurnRecord) string {
	for _, turn := range turns {
		if strings.TrimSpace(turn.InputText) != "" {
			return strings.TrimSpace(turn.InputText)
		}
	}
	return ""
}

func summarizeTurns(turns []core.TurnRecord) string {
	var parts []string
	for _, turn := range turns {
		if text := strings.TrimSpace(turn.ResultSummary); text != "" {
			parts = append(parts, text)
		}
		if len(parts) == 3 {
			break
		}
	}
	return strings.Join(parts, "\n")
}

func condenseTurns(turns []core.TurnRecord, limit int) []core.TurnRecord {
	if limit > 0 && len(turns) > limit {
		turns = turns[:limit]
	}
	condensed := make([]core.TurnRecord, 0, len(turns))
	for _, turn := range turns {
		turn.InputText = truncateString(turn.InputText, 800)
		turn.ResultSummary = truncateString(turn.ResultSummary, 400)
		condensed = append(condensed, turn)
	}
	return condensed
}

func collectUnresolved(job core.JobRecord, events []core.EventRecord) []string {
	var unresolved []string
	if job.State != core.JobStateCompleted {
		unresolved = append(unresolved, fmt.Sprintf("Latest job state is %s.", job.State))
	}
	for _, event := range events {
		if event.Kind != "diagnostic" && event.Kind != "job.failed" && event.Kind != "job.cancelled" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		if message, ok := payload["message"].(string); ok && strings.TrimSpace(message) != "" {
			unresolved = append(unresolved, strings.TrimSpace(message))
		}
	}
	return dedupeStrings(unresolved, 6)
}

func collectImportantFiles(cwd string, events []core.EventRecord, artifacts []core.ArtifactRecord) []string {
	var files []string
	seen := map[string]struct{}{}
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || !filepath.IsAbs(path) {
			return
		}
		stat, err := os.Stat(path)
		if err != nil || stat.IsDir() {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		files = append(files, path)
	}

	for _, artifact := range artifacts {
		add(artifact.Path)
	}
	for _, event := range events {
		var decoded any
		if err := json.Unmarshal(event.Payload, &decoded); err != nil {
			continue
		}
		walkStrings(decoded, func(value string) {
			add(value)
			if cwd != "" && !filepath.IsAbs(value) {
				add(filepath.Join(cwd, value))
			}
		})
	}

	if len(files) > 12 {
		files = files[:12]
	}
	return files
}

func condenseEvents(events []core.EventRecord, limit int) []core.EventRecord {
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}

	condensed := make([]core.EventRecord, 0, len(events))
	for _, event := range events {
		var payload any
		if err := json.Unmarshal(event.Payload, &payload); err == nil {
			payload = truncateNestedStrings(payload, 400)
			if encoded, err := json.Marshal(payload); err == nil {
				event.Payload = encoded
			}
		}
		condensed = append(condensed, event)
	}
	return condensed
}

func truncateNestedStrings(value any, max int) any {
	switch typed := value.(type) {
	case string:
		if max > 0 && len(typed) > max {
			return typed[:max] + "...(truncated)"
		}
		return typed
	case []any:
		for i := range typed {
			typed[i] = truncateNestedStrings(typed[i], max)
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = truncateNestedStrings(item, max)
		}
		return typed
	default:
		return value
	}
}

func toTransferArtifacts(stateDir string, artifacts []core.ArtifactRecord) []core.TransferArtifact {
	result := make([]core.TransferArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		path := artifact.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(stateDir, path)
		}
		result = append(result, core.TransferArtifact{
			Kind:     artifact.Kind,
			Path:     path,
			Metadata: artifact.Metadata,
		})
	}
	return result
}

func writeIndentedJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeJSONL(path string, values []core.EventRecord) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	encoder := json.NewEncoder(file)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func truncateString(text string, max int) string {
	if max > 0 && len(text) > max {
		return text[:max] + "...(truncated)"
	}
	return text
}

func recommendNextSteps(job core.JobRecord, turns []core.TurnRecord) []string {
	steps := []string{"Review the most recent summary and unresolved items.", "Inspect the important files before making changes.", "Run verification before finishing."}
	if job.State != core.JobStateCompleted {
		steps[0] = fmt.Sprintf("Investigate why the latest job ended in state %s.", job.State)
	}
	if len(turns) > 0 && turns[0].ResultSummary != "" {
		steps = append([]string{"Use the last turn summary as the starting context."}, steps...)
	}
	return dedupeStrings(steps, 5)
}

func walkStrings(value any, visit func(string)) {
	switch typed := value.(type) {
	case string:
		visit(typed)
	case []any:
		for _, item := range typed {
			walkStrings(item, visit)
		}
	case map[string]any:
		for _, item := range typed {
			walkStrings(item, visit)
		}
	}
}

func dedupeStrings(values []string, limit int) []string {
	seen := map[string]struct{}{}
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if limit > 0 && len(result) == limit {
			break
		}
	}
	return result
}

func normalizeStoreError(kind, id string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %s %s", ErrNotFound, kind, id)
	}
	return err
}

func inferArtifactKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return "markdown"
	case ".json":
		return "json"
	case ".jsonl":
		return "jsonl"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "image"
	case ".mp4", ".mov", ".webm":
		return "video"
	case ".txt", ".log":
		return "text"
	default:
		return "file"
	}
}


func streamFromRawRef(rawRef string) string {
	parts := strings.Split(filepath.ToSlash(rawRef), "/")
	for _, candidate := range []string{"stdout", "stderr", "native"} {
		for _, part := range parts {
			if part == candidate {
				return candidate
			}
		}
		if filepath.ToSlash(rawRef) == candidate || filepath.Clean(rawRef) == candidate {
			return candidate
		}
	}

	return "raw"
}
