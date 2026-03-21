package native

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// sessionState is the on-disk representation of a native session.
type sessionState struct {
	SessionID       string    `json:"session_id"`
	Provider        string    `json:"provider"` // "zai/glm-5-turbo", etc.
	Model           string    `json:"model"`
	CWD             string    `json:"cwd"`
	History         []Message `json:"history"`
	ActiveTools     []string  `json:"active_tools"` // tool names with full schemas loaded
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	PreviousID      string    `json:"previous_id,omitempty"` // OpenAI response ID for chaining
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// sessionsDir returns the directory for persisted native sessions.
func sessionsDir(cwd string) string {
	return filepath.Join(cwd, ".fase", "native-sessions")
}

// sessionPath returns the file path for a session.
func sessionPath(cwd, sessionID string) string {
	return filepath.Join(sessionsDir(cwd), sessionID+".json")
}

// saveSession persists the current session state to disk.
func (s *nativeSession) saveSession(cwd string) error {
	s.mu.Lock()
	history := make([]Message, len(s.history))
	copy(history, s.history)
	activeTools := make([]string, 0, len(s.tools))
	for _, t := range s.tools {
		activeTools = append(activeTools, t.Name)
	}
	previousID := s.previousID
	s.mu.Unlock()

	state := sessionState{
		SessionID:       s.id,
		Provider:        s.provider.Name + "/" + s.provider.ModelID,
		Model:           s.provider.ModelID,
		CWD:             cwd,
		History:         history,
		ActiveTools:     activeTools,
		ReasoningEffort: s.reasoningEffort,
		PreviousID:      previousID,
		UpdatedAt:       time.Now().UTC(),
	}

	dir := sessionsDir(cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	return os.WriteFile(sessionPath(cwd, s.id), append(data, '\n'), 0o644)
}

// loadSessionState reads a persisted session from disk.
func loadSessionState(cwd, sessionID string) (*sessionState, error) {
	data, err := os.ReadFile(sessionPath(cwd, sessionID))
	if err != nil {
		return nil, err
	}
	var state sessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode session %s: %w", sessionID, err)
	}
	return &state, nil
}
