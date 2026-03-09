package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yusefmosiah/cagent/internal/adapterapi"
	"github.com/yusefmosiah/cagent/internal/adapters"
	"github.com/yusefmosiah/cagent/internal/core"
	"github.com/yusefmosiah/cagent/internal/events"
	"github.com/yusefmosiah/cagent/internal/store"
)

var (
	ErrNotFound           = errors.New("not found")
	ErrUnsupported        = errors.New("unsupported operation")
	ErrAdapterUnavailable = errors.New("adapter not available")
	ErrInvalidInput       = errors.New("invalid input")
	ErrVendorProcess      = errors.New("vendor process failed")
)

type Service struct {
	Paths  core.Paths
	Config core.Config
	store  *store.Store
}

type RunRequest struct {
	Adapter      string
	CWD          string
	Prompt       string
	PromptSource string
	Label        string
	Model        string
	Profile      string
	Detached     bool
	EnvFile      string
	ArtifactDir  string
	SessionID    string
}

type RunResult struct {
	Job     core.JobRecord     `json:"job"`
	Session core.SessionRecord `json:"session"`
	Message string             `json:"message,omitempty"`
}

type StatusResult struct {
	Job     core.JobRecord     `json:"job"`
	Session core.SessionRecord `json:"session"`
	Events  []core.EventRecord `json:"events"`
}

type RawLogEntry struct {
	Stream  string `json:"stream"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

type lineItem struct {
	stream string
	line   string
}

func Open(ctx context.Context, configPath string) (*Service, error) {
	paths, err := core.ResolvePaths()
	if err != nil {
		return nil, fmt.Errorf("resolve runtime paths: %w", err)
	}

	cfg, err := core.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	if cfg.Store.StateDir != "" {
		stateDir, err := core.ExpandPath(cfg.Store.StateDir)
		if err != nil {
			return nil, fmt.Errorf("expand state dir: %w", err)
		}
		paths = paths.WithStateDir(stateDir)
	}

	if err := core.EnsurePaths(paths); err != nil {
		return nil, fmt.Errorf("ensure runtime paths: %w", err)
	}

	db, err := store.Open(ctx, paths.DBPath)
	if err != nil {
		return nil, err
	}

	return &Service{
		Paths:  paths,
		Config: cfg,
		store:  db,
	}, nil
}

func (s *Service) Close() error {
	return s.store.Close()
}

func (s *Service) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	cwd, err := filepath.Abs(req.CWD)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve cwd: %v", ErrInvalidInput, err)
	}
	if stat, err := os.Stat(cwd); err != nil || !stat.IsDir() {
		return nil, fmt.Errorf("%w: cwd must be an existing directory", ErrInvalidInput)
	}
	if req.Prompt == "" {
		return nil, fmt.Errorf("%w: prompt must not be empty", ErrInvalidInput)
	}

	adapter, descriptor, ok := adapters.Resolve(ctx, s.Config, req.Adapter)
	if !ok {
		for _, entry := range adapters.CatalogFromConfig(s.Config) {
			if entry.Adapter == req.Adapter {
				descriptor = entry
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("%w: unknown adapter %q", ErrInvalidInput, req.Adapter)
		}
	}
	if !descriptor.Enabled {
		return nil, fmt.Errorf("%w: adapter %q is disabled in config", ErrUnsupported, req.Adapter)
	}

	now := time.Now().UTC()
	jobID := core.GenerateID("job")
	sessionID := req.SessionID
	var session core.SessionRecord

	if sessionID == "" {
		sessionID = core.GenerateID("ses")
		session = core.SessionRecord{
			SessionID:     sessionID,
			Label:         req.Label,
			CreatedAt:     now,
			UpdatedAt:     now,
			Status:        "active",
			OriginAdapter: req.Adapter,
			OriginJobID:   jobID,
			CWD:           cwd,
			LatestJobID:   jobID,
			Tags:          []string{},
			Metadata:      map[string]any{},
		}
	} else {
		session, err = s.store.GetSession(ctx, sessionID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("%w: session %s", ErrNotFound, sessionID)
			}
			return nil, err
		}
		session.LatestJobID = jobID
		session.UpdatedAt = now
	}

	job := core.JobRecord{
		JobID:     jobID,
		SessionID: sessionID,
		Adapter:   req.Adapter,
		State:     core.JobStateCreated,
		Label:     req.Label,
		CWD:       cwd,
		CreatedAt: now,
		UpdatedAt: now,
		Summary: map[string]any{
			"prompt_source": req.PromptSource,
			"detached":      req.Detached,
		},
	}
	if req.SessionID == "" {
		if err := s.store.CreateSessionAndJob(ctx, session, job); err != nil {
			return nil, err
		}
	} else {
		if err := s.store.CreateJobAndUpdateSession(ctx, sessionID, now, job); err != nil {
			return nil, err
		}
	}

	if _, err := s.emitEvent(ctx, job, "job.created", "lifecycle", map[string]any{
		"cwd":           cwd,
		"label":         req.Label,
		"prompt_source": req.PromptSource,
	}, "", nil); err != nil {
		return nil, err
	}

	rawPrompt, _ := json.Marshal(map[string]any{
		"prompt": req.Prompt,
		"source": req.PromptSource,
	})
	if _, err := s.emitEvent(ctx, job, "user.message", "input", map[string]any{
		"text":   req.Prompt,
		"source": req.PromptSource,
	}, "native", rawPrompt); err != nil {
		return nil, err
	}

	if err := s.transitionJob(ctx, &job, core.JobStateStarting, map[string]any{"message": "job starting"}); err != nil {
		return nil, err
	}

	result := &RunResult{
		Job:     job,
		Session: session,
	}

	var runErr error
	switch {
	case !descriptor.Available:
		message := fmt.Sprintf("adapter %q binary %q is not available on PATH", req.Adapter, descriptor.Binary)
		result.Message = message
		runErr = fmt.Errorf("%w: %s", ErrAdapterUnavailable, message)
	case !descriptor.Implemented:
		message := fmt.Sprintf("adapter %q is detected but not implemented in this build yet", req.Adapter)
		result.Message = message
		runErr = fmt.Errorf("%w: %s", ErrUnsupported, message)
	default:
		result.Message, runErr = s.executeAdapterRun(ctx, adapter, &job, req)
	}

	if runErr != nil {
		if _, err := s.emitEvent(ctx, job, "diagnostic", "translation", map[string]any{
			"message": result.Message,
		}, "", nil); err != nil {
			return result, err
		}
		if _, err := s.emitEvent(ctx, job, "process.stderr", "execution", map[string]any{
			"message": result.Message,
		}, "stderr", []byte(result.Message+"\n")); err != nil {
			return result, err
		}
	}

	job.Summary["message"] = result.Message
	if runErr != nil {
		if err := s.finishJob(ctx, &job, core.JobStateFailed); err != nil {
			return result, err
		}
		if _, err := s.emitEvent(ctx, job, "job.failed", "lifecycle", map[string]any{
			"message": result.Message,
		}, "", nil); err != nil {
			return result, err
		}
	} else {
		if err := s.finishJob(ctx, &job, core.JobStateCompleted); err != nil {
			return result, err
		}
		if _, err := s.emitEvent(ctx, job, "job.completed", "lifecycle", map[string]any{
			"message": result.Message,
		}, "", nil); err != nil {
			return result, err
		}
	}

	result.Job = job
	return result, runErr
}

func (s *Service) Status(ctx context.Context, jobID string) (*StatusResult, error) {
	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, normalizeStoreError("job", jobID, err)
	}

	session, err := s.store.GetSession(ctx, job.SessionID)
	if err != nil {
		return nil, normalizeStoreError("session", job.SessionID, err)
	}

	events, err := s.store.ListEvents(ctx, jobID, 50)
	if err != nil {
		return nil, err
	}

	return &StatusResult{
		Job:     job,
		Session: session,
		Events:  events,
	}, nil
}

func (s *Service) ListJobs(ctx context.Context, limit int) ([]core.JobRecord, error) {
	return s.store.ListJobs(ctx, limit)
}

func (s *Service) Logs(ctx context.Context, jobID string, limit int) ([]core.EventRecord, error) {
	if _, err := s.store.GetJob(ctx, jobID); err != nil {
		return nil, normalizeStoreError("job", jobID, err)
	}
	return s.store.ListEvents(ctx, jobID, limit)
}

func (s *Service) RawLogs(ctx context.Context, jobID string, limit int) ([]RawLogEntry, error) {
	events, err := s.Logs(ctx, jobID, limit)
	if err != nil {
		return nil, err
	}

	var logs []RawLogEntry
	for _, event := range events {
		if event.RawRef == "" {
			continue
		}

		path := filepath.Join(s.Paths.StateDir, event.RawRef)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read raw artifact %q: %w", path, err)
		}

		logs = append(logs, RawLogEntry{
			Stream:  streamFromRawRef(event.RawRef),
			Path:    path,
			Content: string(data),
		})
	}

	return logs, nil
}

func (s *Service) Cancel(ctx context.Context, jobID string) (*core.JobRecord, error) {
	job, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, normalizeStoreError("job", jobID, err)
	}

	if job.State.Terminal() {
		return &job, nil
	}

	job.Summary["message"] = "job cancelled before adapter execution was wired"
	if _, err := s.emitEvent(ctx, job, "job.cancelled", "lifecycle", map[string]any{
		"message": "job cancelled",
	}, "", nil); err != nil {
		return nil, err
	}
	if err := s.finishJob(ctx, &job, core.JobStateCancelled); err != nil {
		return nil, err
	}

	return &job, nil
}

func (s *Service) executeAdapterRun(
	ctx context.Context,
	adapter adapterapi.Adapter,
	job *core.JobRecord,
	req RunRequest,
) (string, error) {
	handle, err := adapter.StartRun(ctx, adapterapi.StartRunRequest{
		CWD:     job.CWD,
		Prompt:  req.Prompt,
		Model:   req.Model,
		Profile: req.Profile,
	})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrAdapterUnavailable, err)
	}
	defer func() {
		if handle.Cleanup != nil {
			_ = handle.Cleanup()
		}
	}()

	if _, err := s.emitEvent(ctx, *job, "process.spawned", "execution", map[string]any{
		"argv": handle.Cmd.Args,
		"pid":  handle.Cmd.Process.Pid,
	}, "", nil); err != nil {
		return "", err
	}
	if _, err := s.emitEvent(ctx, *job, "job.started", "lifecycle", map[string]any{
		"message": "job entered running state",
	}, "", nil); err != nil {
		return "", err
	}
	if err := s.transitionJob(ctx, job, core.JobStateRunning, map[string]any{"message": "job running"}); err != nil {
		return "", err
	}

	lineCh := make(chan lineItem, 64)
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go s.scanStream(handle.Stdout, "stdout", lineCh, errCh, &wg)
	go s.scanStream(handle.Stderr, "stderr", lineCh, errCh, &wg)
	go func() {
		wg.Wait()
		close(lineCh)
		close(errCh)
	}()

	var lastAssistant string
	for item := range lineCh {
		if _, err := s.emitEvent(ctx, *job, "process."+item.stream, "execution", map[string]any{
			"line": item.line,
		}, item.stream, []byte(item.line+"\n")); err != nil {
			return lastAssistant, err
		}

		hints := events.TranslateLine(job.Adapter, item.stream, item.line)
		for _, hint := range hints {
			emitHint := true
			if hint.NativeSessionID != "" {
				if job.NativeSessionID == "" {
					job.NativeSessionID = hint.NativeSessionID
					if err := s.store.UpdateJob(ctx, *job); err != nil {
						return lastAssistant, err
					}
					if err := s.store.UpsertNativeSession(ctx, job.SessionID, job.Adapter, hint.NativeSessionID, adapter.Capabilities().NativeResume); err != nil {
						return lastAssistant, err
					}
				} else if hint.Kind == "session.discovered" && job.NativeSessionID == hint.NativeSessionID {
					emitHint = false
				}
			}
			if text, ok := hint.Payload["text"].(string); ok && text != "" && hint.Kind == "assistant.message" {
				lastAssistant = text
			}
			if emitHint {
				if _, err := s.emitEvent(ctx, *job, hint.Kind, hint.Phase, hint.Payload, "", nil); err != nil {
					return lastAssistant, err
				}
			}
		}
	}

	for scanErr := range errCh {
		if scanErr != nil {
			return lastAssistant, scanErr
		}
	}

	waitErr := handle.Cmd.Wait()
	if lastMessage, err := s.readLastMessage(handle.LastMessagePath); err == nil && lastMessage != "" && lastMessage != lastAssistant {
		if _, emitErr := s.emitEvent(ctx, *job, "assistant.message", "translation", map[string]any{
			"text":   lastMessage,
			"source": "last_message_file",
		}, "", nil); emitErr != nil {
			return lastAssistant, emitErr
		}
		lastAssistant = lastMessage
	}

	if waitErr != nil {
		if _, err := s.emitEvent(ctx, *job, "diagnostic", "execution", map[string]any{
			"message": waitErr.Error(),
		}, "", nil); err != nil {
			return lastAssistant, err
		}
		return lastAssistant, fmt.Errorf("%w: %v", ErrVendorProcess, waitErr)
	}

	if lastAssistant == "" {
		lastAssistant = "adapter completed without a translated assistant message"
	}

	return lastAssistant, nil
}

func (s *Service) transitionJob(ctx context.Context, job *core.JobRecord, state core.JobState, payload map[string]any) error {
	job.State = state
	job.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateJob(ctx, *job); err != nil {
		return err
	}
	_, err := s.emitEvent(ctx, *job, "job.state_changed", "lifecycle", map[string]any{
		"state":   state,
		"message": payload["message"],
	}, "", nil)
	return err
}

func (s *Service) finishJob(ctx context.Context, job *core.JobRecord, state core.JobState) error {
	now := time.Now().UTC()
	job.State = state
	job.UpdatedAt = now
	job.FinishedAt = &now
	return s.store.UpdateJob(ctx, *job)
}

func (s *Service) emitEvent(
	ctx context.Context,
	job core.JobRecord,
	kind string,
	phase string,
	payload any,
	rawStream string,
	rawData []byte,
) (*core.EventRecord, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal event payload: %w", err)
	}

	event := &core.EventRecord{
		EventID:   core.GenerateID("evt"),
		TS:        time.Now().UTC(),
		JobID:     job.JobID,
		SessionID: job.SessionID,
		Adapter:   job.Adapter,
		Kind:      kind,
		Phase:     phase,
		Payload:   encoded,
	}

	if err := s.store.AppendEvent(ctx, event); err != nil {
		return nil, err
	}

	if len(rawData) > 0 && rawStream != "" {
		rawRef, err := s.writeRawArtifact(job, rawStream, event.Seq, rawData)
		if err != nil {
			return nil, err
		}

		artifact := core.ArtifactRecord{
			ArtifactID: core.GenerateID("art"),
			JobID:      job.JobID,
			SessionID:  job.SessionID,
			Kind:       rawStream,
			Path:       rawRef,
			CreatedAt:  time.Now().UTC(),
			Metadata: map[string]any{
				"seq": event.Seq,
			},
		}

		if err := s.store.AttachArtifactToEvent(ctx, event.EventID, job.JobID, rawRef, artifact); err != nil {
			_ = os.Remove(filepath.Join(s.Paths.StateDir, rawRef))
			return nil, err
		}

		event.RawRef = rawRef
	}

	return event, nil
}

func (s *Service) scanStream(
	reader io.Reader,
	stream string,
	lineCh chan<- lineItem,
	errCh chan<- error,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		lineCh <- lineItem{stream: stream, line: scanner.Text()}
	}
	errCh <- scanner.Err()
}

func (s *Service) writeRawArtifact(job core.JobRecord, stream string, seq int64, data []byte) (string, error) {
	dir := filepath.Join(s.Paths.RawDir, stream, job.JobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create raw artifact dir: %w", err)
	}

	name := fmt.Sprintf("%05d.jsonl", seq)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write raw artifact: %w", err)
	}

	return filepath.ToSlash(filepath.Join("raw", stream, job.JobID, name)), nil
}

func (s *Service) readLastMessage(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

func normalizeStoreError(kind, id string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %s %s", ErrNotFound, kind, id)
	}
	return err
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
