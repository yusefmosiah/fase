package native

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

func TestCoAgentManagerSpawnSendListAndClose(t *testing.T) {
	t.Parallel()

	manager := newCoAgentManager(t.TempDir(), "", map[string]adapterapi.LiveAgentAdapter{
		"fake": fakeLiveAdapter{},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	spawned, err := manager.spawn(ctx, "fake", "", "", "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if spawned == "" {
		t.Fatal("expected spawn response")
	}

	listed := manager.list()
	if listed == "" {
		t.Fatal("expected list response")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(spawned), &payload); err != nil {
		t.Fatalf("unmarshal spawn response: %v", err)
	}
	sessionID, _ := payload["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("missing session_id in spawn response: %s", spawned)
	}

	sent, err := manager.sendTurn(ctx, sessionID, "hello co-agent")
	if err != nil {
		t.Fatalf("sendTurn: %v", err)
	}
	if sent == "" {
		t.Fatal("expected sendTurn response")
	}

	closed, err := manager.closeOne(sessionID)
	if err != nil {
		t.Fatalf("closeOne: %v", err)
	}
	if closed == "" {
		t.Fatal("expected close response")
	}
}

type fakeLiveAdapter struct{}

func (fakeLiveAdapter) Name() string { return "fake" }

func (fakeLiveAdapter) StartSession(ctx context.Context, req adapterapi.StartSessionRequest) (adapterapi.LiveSession, error) {
	return newFakeLiveSession(), nil
}

func (fakeLiveAdapter) ResumeSession(ctx context.Context, nativeSessionID string, req adapterapi.StartSessionRequest) (adapterapi.LiveSession, error) {
	return newFakeLiveSession(), nil
}

type fakeLiveSession struct {
	id      string
	turnID  string
	eventCh chan adapterapi.Event
}

func newFakeLiveSession() *fakeLiveSession {
	s := &fakeLiveSession{id: "fake-session", eventCh: make(chan adapterapi.Event, 8)}
	s.eventCh <- adapterapi.Event{Kind: adapterapi.EventKindSessionStarted, SessionID: s.id}
	return s
}

func (s *fakeLiveSession) SessionID() string { return s.id }
func (s *fakeLiveSession) ActiveTurnID() string {
	return s.turnID
}
func (s *fakeLiveSession) StartTurn(ctx context.Context, input []adapterapi.Input) (string, error) {
	s.turnID = "fake-turn"
	s.eventCh <- adapterapi.Event{Kind: adapterapi.EventKindTurnStarted, SessionID: s.id, TurnID: s.turnID}
	s.eventCh <- adapterapi.Event{Kind: adapterapi.EventKindOutputDelta, SessionID: s.id, TurnID: s.turnID, Text: "delegated"}
	s.eventCh <- adapterapi.Event{Kind: adapterapi.EventKindTurnCompleted, SessionID: s.id, TurnID: s.turnID}
	return s.turnID, nil
}
func (s *fakeLiveSession) Steer(ctx context.Context, expectedTurnID string, input []adapterapi.Input) error {
	return nil
}
func (s *fakeLiveSession) Interrupt(ctx context.Context) error { return nil }
func (s *fakeLiveSession) Events() <-chan adapterapi.Event     { return s.eventCh }
func (s *fakeLiveSession) Close() error {
	close(s.eventCh)
	return nil
}
