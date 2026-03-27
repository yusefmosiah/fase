package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

type coAgentManager struct {
	cwd      string
	profile  string
	adapters map[string]adapterapi.LiveAgentAdapter

	mu       sync.Mutex
	sessions map[string]*coAgentSession
	channels *ChannelManager
}

type coAgentSession struct {
	adapter string
	model   string
	role    string
	workID  string
	cursor  uint64
	session adapterapi.LiveSession
	cancel  context.CancelFunc
	done    chan struct{}
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
		channels: NewChannelManager(),
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
		newSpawnAgentTool(manager),
		newPostMessageTool(manager),
		newReadMessagesTool(manager),
		newWaitForMessageTool(manager),
		newCloseAgentTool(manager),
	}
}

func newSpawnAgentTool(manager *coAgentManager) Tool {
	type args struct {
		WorkID  string `json:"work_id"`
		Adapter string `json:"adapter"`
		Model   string `json:"model"`
		Role    string `json:"role"`
		Profile string `json:"profile,omitempty"`
		CWD     string `json:"cwd,omitempty"`
	}
	return toolFromFunc(
		"spawn_agent",
		"Start a peer agent session on a work channel without waiting for a reply.",
		jsonSchemaObject(map[string]any{
			"work_id": map[string]any{"type": "string"},
			"adapter": map[string]any{"type": "string"},
			"model":   map[string]any{"type": "string"},
			"role":    map[string]any{"type": "string"},
			"profile": map[string]any{"type": "string"},
			"cwd":     map[string]any{"type": "string"},
		}, []string{"work_id", "model", "role"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode spawn_agent args: %w", err)
			}
			return manager.spawn(ctx, in.WorkID, in.Adapter, in.Model, in.Role, in.Profile, in.CWD)
		},
	)
}

func newPostMessageTool(manager *coAgentManager) Tool {
	type args struct {
		WorkID  string `json:"work_id"`
		From    string `json:"from,omitempty"`
		Role    string `json:"role,omitempty"`
		Content string `json:"content"`
	}
	return toolFromFunc(
		"post_message",
		"Post a message to a shared work channel and return immediately.",
		jsonSchemaObject(map[string]any{
			"work_id": map[string]any{"type": "string"},
			"from":    map[string]any{"type": "string"},
			"role":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		}, []string{"work_id", "content"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode post_message args: %w", err)
			}
			from := fallbackString(in.From, ctx.Value(ctxKeyNativeSessionID))
			role := fallbackString(in.Role, ctx.Value(ctxKeyNativeSessionRole))
			if from == "" {
				from = "host"
			}
			if role == "" {
				role = "agent"
			}
			return manager.postMessage(in.WorkID, from, role, in.Content)
		},
	)
}

func newReadMessagesTool(manager *coAgentManager) Tool {
	type args struct {
		WorkID string `json:"work_id"`
		Cursor uint64 `json:"cursor,omitempty"`
	}
	return toolFromFunc(
		"read_messages",
		"Read messages from a work channel since the supplied cursor without blocking.",
		jsonSchemaObject(map[string]any{
			"work_id": map[string]any{"type": "string"},
			"cursor":  map[string]any{"type": "integer", "minimum": 0},
		}, []string{"work_id"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode read_messages args: %w", err)
			}
			return manager.readMessages(in.WorkID, in.Cursor)
		},
	)
}

func newWaitForMessageTool(manager *coAgentManager) Tool {
	type args struct {
		WorkID    string `json:"work_id"`
		Cursor    uint64 `json:"cursor,omitempty"`
		TimeoutMS int    `json:"timeout_ms,omitempty"`
	}
	return toolFromFunc(
		"wait_for_message",
		"Block until a new message arrives on a work channel or the timeout expires.",
		jsonSchemaObject(map[string]any{
			"work_id":    map[string]any{"type": "string"},
			"cursor":     map[string]any{"type": "integer", "minimum": 0},
			"timeout_ms": map[string]any{"type": "integer", "minimum": 1},
		}, []string{"work_id"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode wait_for_message args: %w", err)
			}
			timeout := 30 * time.Second
			if in.TimeoutMS > 0 {
				timeout = time.Duration(in.TimeoutMS) * time.Millisecond
			}
			return manager.waitForMessage(ctx, in.WorkID, in.Cursor, timeout)
		},
	)
}

func newCloseAgentTool(manager *coAgentManager) Tool {
	type args struct {
		AgentID string `json:"agent_id"`
	}
	return toolFromFunc(
		"close_agent",
		"Close a peer agent session.",
		jsonSchemaObject(map[string]any{
			"agent_id": map[string]any{"type": "string"},
		}, []string{"agent_id"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode close_agent args: %w", err)
			}
			return manager.closeOne(in.AgentID)
		},
	)
}

func (m *coAgentManager) spawn(ctx context.Context, workID, adapterName, model, role, profile, cwd string) (string, error) {
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return "", fmt.Errorf("work_id must not be empty")
	}
	adapterName = strings.TrimSpace(adapterName)
	if adapterName == "" {
		adapterName = "native"
	}
	adapter, ok := m.adapters[adapterName]
	if !ok {
		return "", fmt.Errorf("peer adapter %q not registered", adapterName)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", fmt.Errorf("model must not be empty")
	}
	role = strings.TrimSpace(role)
	if role == "" {
		return "", fmt.Errorf("role must not be empty")
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = m.cwd
	}
	if strings.TrimSpace(profile) == "" {
		profile = role
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

	channel, err := m.channels.Channel(workID)
	if err != nil {
		_ = session.Close()
		return "", err
	}

	agentCtx, cancel := context.WithCancel(context.Background())
	co := &coAgentSession{
		adapter: adapterName,
		model:   model,
		role:    role,
		workID:  workID,
		cursor:  channel.Cursor(),
		session: session,
		cancel:  cancel,
		done:    make(chan struct{}),
	}

	m.mu.Lock()
	if _, exists := m.sessions[session.SessionID()]; exists {
		m.mu.Unlock()
		cancel()
		_ = session.Close()
		return "", fmt.Errorf("peer agent %q already exists", session.SessionID())
	}
	m.sessions[session.SessionID()] = co
	m.mu.Unlock()

	go m.runAgent(agentCtx, co)

	return jsonString(map[string]any{
		"agent_id":   session.SessionID(),
		"session_id": session.SessionID(),
		"work_id":    workID,
		"adapter":    adapterName,
		"model":      model,
		"role":       role,
		"profile":    profile,
		"cwd":        cwd,
	})
}

func (m *coAgentManager) postMessage(workID, from, role, content string) (string, error) {
	ch, err := m.channels.Channel(workID)
	if err != nil {
		return "", err
	}
	cursor, err := ch.Post(ChannelMessage{
		From:    strings.TrimSpace(from),
		Role:    strings.TrimSpace(role),
		Content: content,
	})
	if err != nil {
		return "", err
	}
	return jsonString(map[string]any{
		"work_id": workID,
		"cursor":  cursor,
		"status":  "posted",
	})
}

func (m *coAgentManager) readMessages(workID string, cursor uint64) (string, error) {
	ch, err := m.channels.Channel(workID)
	if err != nil {
		return "", err
	}
	messages, nextCursor, err := ch.ReadSince(cursor)
	if err != nil {
		return "", err
	}
	return jsonString(map[string]any{
		"work_id":  workID,
		"messages": messages,
		"cursor":   nextCursor,
	})
}

func (m *coAgentManager) waitForMessage(ctx context.Context, workID string, cursor uint64, timeout time.Duration) (string, error) {
	ch, err := m.channels.Channel(workID)
	if err != nil {
		return "", err
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	messages, nextCursor, err := ch.Wait(waitCtx, cursor)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return jsonString(map[string]any{
				"work_id":   workID,
				"messages":  []ChannelMessage{},
				"cursor":    cursor,
				"timed_out": true,
			})
		}
		return "", err
	}
	return jsonString(map[string]any{
		"work_id":   workID,
		"messages":  messages,
		"cursor":    nextCursor,
		"timed_out": false,
	})
}

func (m *coAgentManager) closeOne(agentID string) (string, error) {
	m.mu.Lock()
	co, ok := m.sessions[agentID]
	if ok {
		delete(m.sessions, agentID)
	}
	m.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("peer agent %q not found", agentID)
	}
	co.cancel()
	if err := co.session.Close(); err != nil {
		return "", err
	}
	<-co.done
	return jsonString(map[string]any{
		"agent_id":   agentID,
		"session_id": agentID,
		"status":     "closed",
	})
}

func (m *coAgentManager) runAgent(ctx context.Context, co *coAgentSession) {
	defer close(co.done)
	defer m.removeSession(co.session.SessionID())

	ch, err := m.channels.Channel(co.workID)
	if err != nil {
		return
	}
	cursor := co.cursor

	for {
		messages, nextCursor, err := ch.Wait(ctx, cursor)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, ErrChannelClosed) {
				return
			}
			_, _ = ch.Post(ChannelMessage{
				From:    co.session.SessionID(),
				Role:    co.role,
				Content: fmt.Sprintf("peer agent error: %v", err),
			})
			return
		}
		cursor = nextCursor
		for _, msg := range messages {
			if msg.From == co.session.SessionID() {
				continue
			}
			output, err := m.runAgentTurn(ctx, co, msg)
			if err != nil {
				_, _ = ch.Post(ChannelMessage{
					From:    co.session.SessionID(),
					Role:    co.role,
					Content: fmt.Sprintf("peer agent error: %v", err),
				})
				continue
			}
			if strings.TrimSpace(output) == "" {
				continue
			}
			_, _ = ch.Post(ChannelMessage{
				From:    co.session.SessionID(),
				Role:    co.role,
				Content: output,
			})
		}
	}
}

func (m *coAgentManager) runAgentTurn(ctx context.Context, co *coAgentSession, msg ChannelMessage) (string, error) {
	turnID, err := co.session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput(peerTurnPrompt(co, msg))})
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
				return "", fmt.Errorf("peer agent %s closed", co.session.SessionID())
			}
			if ev.TurnID != "" && ev.TurnID != turnID {
				continue
			}
			switch ev.Kind {
			case adapterapi.EventKindOutputDelta:
				output.WriteString(ev.Text)
			case adapterapi.EventKindTurnCompleted:
				return strings.TrimSpace(output.String()), nil
			case adapterapi.EventKindTurnFailed:
				return "", fmt.Errorf("peer agent turn failed: %s", ev.Text)
			case adapterapi.EventKindTurnInterrupted:
				return "", fmt.Errorf("peer agent turn interrupted")
			}
		}
	}
}

func peerTurnPrompt(co *coAgentSession, msg ChannelMessage) string {
	return strings.TrimSpace(fmt.Sprintf(
		"You are the %s peer agent for work %s.\nRespond to the latest channel message. Use any available tools you need, then end your turn with the exact message that should be posted back to the shared channel.\n\nLatest message:\nFrom: %s\nRole: %s\nContent:\n%s",
		co.role,
		co.workID,
		msg.From,
		msg.Role,
		msg.Content,
	))
}

func (m *coAgentManager) removeSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if co, ok := m.sessions[sessionID]; ok && co.session.SessionID() == sessionID {
		delete(m.sessions, sessionID)
	}
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
		co.cancel()
		if err := co.session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		<-co.done
	}
	return firstErr
}

func fallbackString(current string, candidate any) string {
	if strings.TrimSpace(current) != "" {
		return strings.TrimSpace(current)
	}
	if value, _ := candidate.(string); strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return ""
}
