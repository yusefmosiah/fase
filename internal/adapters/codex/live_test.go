package codex_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/yusefmosiah/cagent/internal/adapterapi"
	"github.com/yusefmosiah/cagent/internal/adapters/codex"
)

func codexBinary(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex binary not found; skipping live adapter test")
	}
	return p
}

// TestLiveAdapter_StartSession verifies that the Codex live adapter can spawn
// the app-server, initialize, start a thread, and receive a session.started event.
func TestLiveAdapter_StartSession(t *testing.T) {
	bin := codexBinary(t)

	adapter := codex.NewLiveAdapter(bin)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cwd := t.TempDir()
	session, err := adapter.StartSession(ctx, adapterapi.StartSessionRequest{
		CWD: cwd,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if session.SessionID() == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Wait for the session.started event.
	select {
	case ev := <-session.Events():
		if ev.Kind != adapterapi.EventKindSessionStarted {
			t.Fatalf("expected session.started event, got %s", ev.Kind)
		}
		if ev.SessionID != session.SessionID() {
			t.Fatalf("event session ID mismatch: %s != %s", ev.SessionID, session.SessionID())
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for session.started event")
	}

	t.Logf("session started: %s", session.SessionID())
}

// TestLiveAdapter_StartTurn verifies that a turn can be started and
// produces proper lifecycle events with output deltas.
func TestLiveAdapter_StartTurn(t *testing.T) {
	bin := codexBinary(t)

	adapter := codex.NewLiveAdapter(bin)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cwd := t.TempDir()
	session, err := adapter.StartSession(ctx, adapterapi.StartSessionRequest{
		CWD: cwd,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Drain the session.started event.
	drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)

	turnID, err := session.StartTurn(ctx, []adapterapi.Input{
		adapterapi.TextInput("Reply with exactly the word: PONG"),
	})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if turnID == "" {
		t.Fatal("expected non-empty turn ID")
	}
	t.Logf("turn started: %s", turnID)

	// Wait for turn.completed.
	completed := drainUntil(t, ctx, session.Events(), adapterapi.EventKindTurnCompleted)
	if completed.TurnID != turnID {
		t.Fatalf("turn ID mismatch: %s != %s", completed.TurnID, turnID)
	}
}

// TestLiveAdapter_Interrupt verifies that an active turn can be interrupted.
func TestLiveAdapter_Interrupt(t *testing.T) {
	bin := codexBinary(t)

	adapter := codex.NewLiveAdapter(bin)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cwd := t.TempDir()
	session, err := adapter.StartSession(ctx, adapterapi.StartSessionRequest{
		CWD: cwd,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)

	turnID, err := session.StartTurn(ctx, []adapterapi.Input{
		adapterapi.TextInput("Count slowly from 1 to 1000, one number per line."),
	})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}

	// Wait for turn.started notification before interrupting.
	drainUntil(t, ctx, session.Events(), adapterapi.EventKindTurnStarted)

	// Interrupt the turn.
	if err := session.Interrupt(ctx); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	t.Logf("interrupted turn %s", turnID)

	// Wait for the turn to complete (either interrupted or completed).
	for {
		select {
		case ev := <-session.Events():
			t.Logf("event: kind=%s turn=%s", ev.Kind, ev.TurnID)
			switch ev.Kind {
			case adapterapi.EventKindTurnCompleted,
				adapterapi.EventKindTurnInterrupted,
				adapterapi.EventKindTurnFailed:
				t.Logf("turn ended with %s", ev.Kind)
				return
			}
		case <-ctx.Done():
			t.Fatal("timeout waiting for turn to end after interrupt")
		}
	}
}

// TestLiveAdapter_ResumeSession verifies that a session can be resumed by thread ID.
func TestLiveAdapter_ResumeSession(t *testing.T) {
	bin := codexBinary(t)

	adapter := codex.NewLiveAdapter(bin)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cwd := t.TempDir()

	// Start a session and run a turn to establish state.
	session1, err := adapter.StartSession(ctx, adapterapi.StartSessionRequest{CWD: cwd})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	drainUntil(t, ctx, session1.Events(), adapterapi.EventKindSessionStarted)

	threadID := session1.SessionID()
	t.Logf("original session: %s", threadID)

	if _, err := session1.StartTurn(ctx, []adapterapi.Input{
		adapterapi.TextInput("Remember the secret word: CAGENT"),
	}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainUntil(t, ctx, session1.Events(), adapterapi.EventKindTurnCompleted)

	_ = session1.Close()

	// Resume with a new app-server instance.
	session2, err := adapter.ResumeSession(ctx, threadID, adapterapi.StartSessionRequest{CWD: cwd})
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	defer func() { _ = session2.Close() }()

	drainUntil(t, ctx, session2.Events(), adapterapi.EventKindSessionStarted)

	if session2.SessionID() != threadID {
		t.Fatalf("resumed session ID mismatch: %s != %s", session2.SessionID(), threadID)
	}

	t.Logf("resumed session: %s", session2.SessionID())
}

// TestLiveAdapter_SteerCh verifies that SteerCh events are relayed mid-turn.
func TestLiveAdapter_SteerCh(t *testing.T) {
	bin := codexBinary(t)

	adapter := codex.NewLiveAdapter(bin)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	steerCh := make(chan adapterapi.SteerEvent, 4)

	cwd := t.TempDir()
	session, err := adapter.StartSession(ctx, adapterapi.StartSessionRequest{
		CWD:     cwd,
		SteerCh: steerCh,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)

	// Start a turn that waits for direction.
	if _, err := session.StartTurn(ctx, []adapterapi.Input{
		adapterapi.TextInput("Wait for instructions. Reply READY when you see a cagent message, then follow it."),
	}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}

	// Give the turn a moment to get started.
	time.Sleep(500 * time.Millisecond)

	// Send a steer event.
	steerCh <- adapterapi.SteerEvent{Message: "Say exactly: STEERED"}

	// Wait for turn to complete.
	drainUntil(t, ctx, session.Events(), adapterapi.EventKindTurnCompleted)
	t.Log("turn completed after steer")
}

// drainUntil reads events until the target kind is found, returning it.
func drainUntil(t *testing.T, ctx context.Context, ch <-chan adapterapi.Event, kind adapterapi.EventKind) adapterapi.Event {
	t.Helper()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("event channel closed before receiving %s", kind)
			}
			t.Logf("event: kind=%s session=%s turn=%s text=%q", ev.Kind, ev.SessionID, ev.TurnID, ev.Text)
			if ev.Kind == kind {
				return ev
			}
		case <-ctx.Done():
			t.Fatalf("timeout waiting for %s event", kind)
		}
	}
}
