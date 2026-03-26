package pi_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
	"github.com/yusefmosiah/cogent/internal/adapters/pi"
)

func piBinary(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi binary not found; skipping live adapter test")
	}
	return p
}

func TestLiveAdapter_StartSession(t *testing.T) {
	bin := piBinary(t)

	adapter := pi.NewLiveAdapter(bin)

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

func TestLiveAdapter_StartTurn(t *testing.T) {
	bin := piBinary(t)

	adapter := pi.NewLiveAdapter(bin)

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
		adapterapi.TextInput("Reply with exactly the word: PONG"),
	})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if turnID == "" {
		t.Fatal("expected non-empty turn ID")
	}
	t.Logf("turn started: %s", turnID)

	completed := drainUntil(t, ctx, session.Events(), adapterapi.EventKindTurnCompleted)
	if completed.TurnID != "" && completed.TurnID != turnID {
		t.Fatalf("turn ID mismatch: %s != %s", completed.TurnID, turnID)
	}
}

func TestLiveAdapter_Interrupt(t *testing.T) {
	bin := piBinary(t)

	adapter := pi.NewLiveAdapter(bin)

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

	_, err = session.StartTurn(ctx, []adapterapi.Input{
		adapterapi.TextInput("Count slowly from 1 to 1000, one number per line."),
	})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindTurnStarted)

	if err := session.Interrupt(ctx); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	t.Logf("interrupted turn")

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

func TestLiveAdapter_SteerCh(t *testing.T) {
	bin := piBinary(t)

	adapter := pi.NewLiveAdapter(bin)

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

	if _, err := session.StartTurn(ctx, []adapterapi.Input{
		adapterapi.TextInput("Wait for instructions. Reply READY when you see a cogent message, then follow it."),
	}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	steerCh <- adapterapi.SteerEvent{Message: "Say exactly: STEERED"}

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindTurnCompleted)
	t.Log("turn completed after steer")
}

func TestLiveAdapter_FollowUpDelivery(t *testing.T) {
	bin := piBinary(t)

	adapter := pi.NewLiveAdapter(bin)

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

	ps, ok := session.(interface{ SetDeliveryMode(pi.DeliveryMode) })
	if !ok {
		t.Skip("session does not support SetDeliveryMode")
	}
	ps.SetDeliveryMode(pi.DeliveryFollowUp)

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)

	turnID, err := session.StartTurn(ctx, []adapterapi.Input{
		adapterapi.TextInput("Reply with exactly: FOLLOWUP_OK"),
	})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	t.Logf("turn started with follow_up delivery: %s", turnID)

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindTurnCompleted)
	t.Log("turn completed with follow_up delivery mode")
}

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
