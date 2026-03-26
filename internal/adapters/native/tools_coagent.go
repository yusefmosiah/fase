package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

type coAgentManager struct {
	cwd      string
	profile  string
	adapters map[string]adapterapi.LiveAgentAdapter

	mu       sync.Mutex
	sessions map[string]*coAgentSession
}

type coAgentSession struct {
	adapter string
	model   string
	session adapterapi.LiveSession
}

func newCoAgentManager(cwd, profile string, adapters map[string]adapterapi.LiveAgentAdapter) *coAgentManager {
	cloned := make(map[string]adapterapi.LiveAgentAdapter, len(adapters))
	for name, adapter := range adapters {
		if adapter != nil {
			cloned[name] = adapter
		}
	}
	return &coAgentManager{
		cwd:      cwd,
		profile:  profile,
		adapters: cloned,
		sessions: map[string]*coAgentSession{},
	}
}

func RegisterCoAgentTools(registry *ToolRegistry, manager *coAgentManager) error {
	for _, tool := range NewCoAgentTools(manager) {
		if err := registry.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func NewCoAgentTools(manager *coAgentManager) []Tool {
	return []Tool{
		newSpawnSessionTool(manager),
		newSendTurnTool(manager),
		newSteerSessionTool(manager),
		newCloseSessionTool(manager),
		newListSessionsTool(manager),
	}
}

func newSpawnSessionTool(manager *coAgentManager) Tool {
	type args struct {
		Adapter string `json:"adapter"`
		Model   string `json:"model"`
		Profile string `json:"profile,omitempty"`
		CWD     string `json:"cwd,omitempty"`
	}
	return toolFromFunc(
		"spawn_session",
		"Start a co-agent live session on another adapter.",
		jsonSchemaObject(map[string]any{
			"adapter": map[string]any{"type": "string"},
			"model":   map[string]any{"type": "string"},
			"profile": map[string]any{"type": "string"},
			"cwd":     map[string]any{"type": "string"},
		}, []string{"adapter"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode spawn_session args: %w", err)
			}
			return manager.spawn(ctx, in.Adapter, in.Model, in.Profile, in.CWD)
		},
	)
}

func newSendTurnTool(manager *coAgentManager) Tool {
	type args struct {
		SessionID string `json:"session_id"`
		Input     string `json:"input"`
	}
	return toolFromFunc(
		"send_turn",
		"Send a prompt to a co-agent session and wait for the turn result.",
		jsonSchemaObject(map[string]any{
			"session_id": map[string]any{"type": "string"},
			"input":      map[string]any{"type": "string"},
		}, []string{"session_id", "input"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode send_turn args: %w", err)
			}
			return manager.sendTurn(ctx, in.SessionID, in.Input)
		},
	)
}

func newSteerSessionTool(manager *coAgentManager) Tool {
	type args struct {
		SessionID string `json:"session_id"`
		TurnID    string `json:"turn_id,omitempty"`
		Input     string `json:"input"`
	}
	return toolFromFunc(
		"steer_session",
		"Inject input into a running co-agent turn.",
		jsonSchemaObject(map[string]any{
			"session_id": map[string]any{"type": "string"},
			"turn_id":    map[string]any{"type": "string"},
			"input":      map[string]any{"type": "string"},
		}, []string{"session_id", "input"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode steer_session args: %w", err)
			}
			return manager.steer(ctx, in.SessionID, in.TurnID, in.Input)
		},
	)
}

func newCloseSessionTool(manager *coAgentManager) Tool {
	type args struct {
		SessionID string `json:"session_id"`
	}
	return toolFromFunc(
		"close_session",
		"Close a co-agent session.",
		jsonSchemaObject(map[string]any{
			"session_id": map[string]any{"type": "string"},
		}, []string{"session_id"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode close_session args: %w", err)
			}
			return manager.closeOne(in.SessionID)
		},
	)
}

func newListSessionsTool(manager *coAgentManager) Tool {
	return toolFromFunc(
		"list_sessions",
		"List active co-agent sessions.",
		nil,
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			return manager.list(), nil
		},
	)
}

func (m *coAgentManager) spawn(ctx context.Context, adapterName, model, profile, cwd string) (string, error) {
	adapterName = strings.TrimSpace(adapterName)
	if adapterName == "" {
		return "", fmt.Errorf("adapter must not be empty")
	}
	adapter, ok := m.adapters[adapterName]
	if !ok {
		return "", fmt.Errorf("co-agent adapter %q not registered", adapterName)
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = m.cwd
	}
	if strings.TrimSpace(profile) == "" {
		profile = m.profile
	}
	session, err := adapter.StartSession(ctx, adapterapi.StartSessionRequest{
		CWD:     cwd,
		Model:   model,
		Profile: profile,
	})
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	m.sessions[session.SessionID()] = &coAgentSession{
		adapter: adapterName,
		model:   model,
		session: session,
	}
	m.mu.Unlock()

	return jsonString(map[string]any{
		"session_id": session.SessionID(),
		"adapter":    adapterName,
		"model":      model,
		"profile":    profile,
		"cwd":        cwd,
	})
}

func (m *coAgentManager) sendTurn(ctx context.Context, sessionID, input string) (string, error) {
	co, err := m.lookup(sessionID)
	if err != nil {
		return "", err
	}
	turnID, err := co.session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput(input)})
	if err != nil {
		return "", err
	}

	var output strings.Builder
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case ev, ok := <-co.session.Events():
			if !ok {
				return "", fmt.Errorf("co-agent session %s closed", sessionID)
			}
			if ev.TurnID != "" && ev.TurnID != turnID {
				continue
			}
			switch ev.Kind {
			case adapterapi.EventKindOutputDelta:
				output.WriteString(ev.Text)
			case adapterapi.EventKindTurnCompleted:
				return jsonString(map[string]any{
					"session_id": sessionID,
					"turn_id":    turnID,
					"output":     output.String(),
				})
			case adapterapi.EventKindTurnFailed:
				return "", fmt.Errorf("co-agent turn failed: %s", ev.Text)
			case adapterapi.EventKindTurnInterrupted:
				return "", fmt.Errorf("co-agent turn interrupted")
			}
		}
	}
}

func (m *coAgentManager) steer(ctx context.Context, sessionID, turnID, input string) (string, error) {
	co, err := m.lookup(sessionID)
	if err != nil {
		return "", err
	}
	if err := co.session.Steer(ctx, turnID, []adapterapi.Input{adapterapi.TextInput(input)}); err != nil {
		return "", err
	}
	return jsonString(map[string]any{
		"session_id": sessionID,
		"turn_id":    co.session.ActiveTurnID(),
		"status":     "ok",
	})
}

func (m *coAgentManager) closeOne(sessionID string) (string, error) {
	m.mu.Lock()
	co, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("co-agent session %q not found", sessionID)
	}
	if err := co.session.Close(); err != nil {
		return "", err
	}
	return jsonString(map[string]any{
		"session_id": sessionID,
		"status":     "closed",
	})
}

func (m *coAgentManager) list() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]map[string]any, 0, len(m.sessions))
	for id, co := range m.sessions {
		items = append(items, map[string]any{
			"session_id":  id,
			"adapter":     co.adapter,
			"model":       co.model,
			"active_turn": co.session.ActiveTurnID(),
		})
	}
	out, _ := jsonString(map[string]any{"sessions": items})
	return out
}

func (m *coAgentManager) lookup(sessionID string) (*coAgentSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	co, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("co-agent session %q not found", sessionID)
	}
	return co, nil
}

func (m *coAgentManager) closeAll() error {
	m.mu.Lock()
	sessions := make([]*coAgentSession, 0, len(m.sessions))
	for _, co := range m.sessions {
		sessions = append(sessions, co)
	}
	m.sessions = map[string]*coAgentSession{}
	m.mu.Unlock()

	var firstErr error
	for _, co := range sessions {
		if err := co.session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
