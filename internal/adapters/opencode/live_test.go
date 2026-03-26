package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	opencodesdk "github.com/sst/opencode-sdk-go"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

type fakeOpenCodeServer struct {
	t *testing.T

	srv    *httptest.Server
	url    string
	events chan string

	mu sync.Mutex

	sessionID string
	cwd       string

	startPromptCount int
	steerCount       int
	abortCount       int
	steerNoReply     bool
	aborted          bool
}

func newFakeOpenCodeServer(t *testing.T) *fakeOpenCodeServer {
	t.Helper()

	f := &fakeOpenCodeServer{
		t:         t,
		events:    make(chan string, 32),
		sessionID: "session-123",
		cwd:       t.TempDir(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/session", f.handleSessionRoot)
	mux.HandleFunc("/session/", f.handleSession)
	mux.HandleFunc("/event", f.handleEvents)

	f.srv = httptest.NewServer(mux)
	f.url = f.srv.URL
	return f
}

func (f *fakeOpenCodeServer) close() {
	f.srv.Close()
}

func (f *fakeOpenCodeServer) handleSessionRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, []any{})
	case http.MethodPost:
		session := fakeSession(f.sessionID, f.cwd)
		writeJSON(w, session)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOpenCodeServer) handleSession(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/session/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || parts[0] != f.sessionID {
		http.NotFound(w, r)
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, fakeSession(f.sessionID, f.cwd))
		default:
			http.NotFound(w, r)
		}
		return
	}

	switch parts[1] {
	case "message":
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		text := promptText(req)
		if text == "" {
			http.Error(w, "empty prompt", http.StatusBadRequest)
			return
		}

		if boolValue(req["noReply"]) {
			f.mu.Lock()
			f.steerCount++
			f.steerNoReply = true
			f.mu.Unlock()

			go f.pushPartDelta("msg-steer", "part-steer", text)
			writeJSON(w, fakePromptResponse("msg-steer", f.sessionID, text, false))
			return
		}

		f.mu.Lock()
		f.startPromptCount++
		f.aborted = false
		f.mu.Unlock()

		go f.pushPartDelta("msg-1", "part-1", "PONG")
		go func() {
			time.Sleep(400 * time.Millisecond)
			f.mu.Lock()
			aborted := f.aborted
			f.mu.Unlock()
			if aborted {
				return
			}
			f.pushMessageCompleted("msg-1")
		}()

		writeJSON(w, fakePromptResponse("msg-1", f.sessionID, text, true))

	case "abort":
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}

		f.mu.Lock()
		f.abortCount++
		f.aborted = true
		f.mu.Unlock()

		f.pushSessionError("message aborted")
		writeJSON(w, true)

	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOpenCodeServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Accept"); got != "text/event-stream" {
		http.Error(w, "missing event-stream accept", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case evt := <-f.events:
			_, _ = fmt.Fprint(w, evt)
			flusher.Flush()
		}
	}
}

func (f *fakeOpenCodeServer) pushPartDelta(messageID, partID, delta string) {
	part := opencodesdk.Part{
		ID:        partID,
		MessageID: messageID,
		SessionID: f.sessionID,
		Type:      opencodesdk.PartTypeText,
		Text:      delta,
	}
	payload := map[string]any{
		"type": "message.part.updated",
		"properties": map[string]any{
			"part":  part,
			"delta": delta,
		},
	}
	f.sendEvent(payload)
}

func (f *fakeOpenCodeServer) pushMessageCompleted(messageID string) {
	msg := fakeAssistantMessage(messageID, f.sessionID, f.cwd, true)
	payload := map[string]any{
		"type": "message.updated",
		"properties": map[string]any{
			"info": msg,
		},
	}
	f.sendEvent(payload)
}

func (f *fakeOpenCodeServer) pushSessionError(message string) {
	payload := map[string]any{
		"type": "session.error",
		"properties": map[string]any{
			"sessionID": f.sessionID,
			"error": map[string]any{
				"name": "MessageAbortedError",
				"data": map[string]any{
					"message": message,
				},
			},
		},
	}
	f.sendEvent(payload)
}

func (f *fakeOpenCodeServer) sendEvent(payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatalf("marshal event: %v", err)
	}
	record := fmt.Sprintf("event: %s\ndata: %s\n\n", payload["type"], data)
	select {
	case f.events <- record:
	default:
		f.t.Fatalf("event buffer full while sending %s", payload["type"])
	}
}

func promptText(req map[string]any) string {
	parts, ok := req["parts"].([]any)
	if !ok || len(parts) == 0 {
		return ""
	}
	first, ok := parts[0].(map[string]any)
	if !ok {
		return ""
	}
	text, _ := first["text"].(string)
	return text
}

func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}

func fakeSession(sessionID, cwd string) opencodesdk.Session {
	return opencodesdk.Session{
		ID:        sessionID,
		Directory: cwd,
		ProjectID: "project-123",
		Time: opencodesdk.SessionTime{
			Created: 1,
		},
		Title:   "OpenCode Test",
		Version: "1.0.0",
	}
}

func fakeAssistantMessage(messageID, sessionID, cwd string, completed bool) opencodesdk.AssistantMessage {
	msg := opencodesdk.AssistantMessage{
		ID:         messageID,
		Cost:       0,
		Mode:       "agent",
		ModelID:    "gpt-5.3-codex-spark",
		ParentID:   "",
		Path:       opencodesdk.AssistantMessagePath{Cwd: cwd, Root: cwd},
		ProviderID: "openai",
		Role:       opencodesdk.AssistantMessageRoleAssistant,
		SessionID:  sessionID,
		System:     []string{"cogent"},
		Time: opencodesdk.AssistantMessageTime{
			Created:   1,
			Completed: 0,
		},
		Tokens: opencodesdk.AssistantMessageTokens{
			Cache:     opencodesdk.AssistantMessageTokensCache{},
			Input:     0,
			Output:    0,
			Reasoning: 0,
		},
	}
	if completed {
		msg.Time.Completed = 2
	}
	return msg
}

func fakePromptResponse(messageID, sessionID, text string, completed bool) map[string]any {
	part := opencodesdk.Part{
		ID:        "part-" + messageID,
		MessageID: messageID,
		SessionID: sessionID,
		Type:      opencodesdk.PartTypeText,
		Text:      text,
	}
	return map[string]any{
		"info":  fakeAssistantMessage(messageID, sessionID, "", completed),
		"parts": []opencodesdk.Part{part},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestLiveAdapter_StartResumeAndEvents(t *testing.T) {
	server := newFakeOpenCodeServer(t)
	defer server.close()

	adapter := &LiveAdapter{
		baseURL:       server.url,
		clientFactory: defaultOpenCodeClient,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := adapter.StartSession(ctx, adapterapi.StartSessionRequest{
		CWD: server.cwd,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if session.SessionID() != server.sessionID {
		t.Fatalf("session id mismatch: got %s want %s", session.SessionID(), server.sessionID)
	}

	started := drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)
	if started.SessionID != server.sessionID {
		t.Fatalf("session started mismatch: got %s want %s", started.SessionID, server.sessionID)
	}

	turnID, err := session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput("Reply with exactly PONG")})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if turnID == "" {
		t.Fatal("expected non-empty turn id")
	}

	events := collectUntil(t, ctx, session.Events(), adapterapi.EventKindTurnCompleted)
	assertHasKind(t, events, adapterapi.EventKindTurnStarted)
	assertHasKind(t, events, adapterapi.EventKindOutputDelta)
	assertHasKind(t, events, adapterapi.EventKindTurnCompleted)
	if got := server.startPromptCount; got != 1 {
		t.Fatalf("expected one start prompt, got %d", got)
	}

	resumed, err := adapter.ResumeSession(ctx, session.SessionID(), adapterapi.StartSessionRequest{CWD: server.cwd})
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	defer func() { _ = resumed.Close() }()

	if got := drainUntil(t, ctx, resumed.Events(), adapterapi.EventKindSessionResumed); got.SessionID != server.sessionID {
		t.Fatalf("resumed session mismatch: got %s want %s", got.SessionID, server.sessionID)
	}
}

func TestLiveAdapter_SteerUsesNoReply(t *testing.T) {
	server := newFakeOpenCodeServer(t)
	defer server.close()

	adapter := &LiveAdapter{
		baseURL:       server.url,
		clientFactory: defaultOpenCodeClient,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := adapter.StartSession(ctx, adapterapi.StartSessionRequest{CWD: server.cwd})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()
	drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)

	turnID, err := session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput("Reply slowly")})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainUntil(t, ctx, session.Events(), adapterapi.EventKindOutputDelta)

	if err := session.Steer(ctx, turnID, []adapterapi.Input{adapterapi.TextInput("Say STEERED")}); err != nil {
		t.Fatalf("Steer: %v", err)
	}

	if !server.steerNoReply {
		t.Fatal("expected steer request to set noReply=true")
	}

	drainUntil(t, ctx, session.Events(), adapterapi.EventKindTurnCompleted)
	if got := server.steerCount; got != 1 {
		t.Fatalf("expected one steer request, got %d", got)
	}
}

func TestLiveAdapter_InterruptUsesAbort(t *testing.T) {
	server := newFakeOpenCodeServer(t)
	defer server.close()

	adapter := &LiveAdapter{
		baseURL:       server.url,
		clientFactory: defaultOpenCodeClient,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := adapter.StartSession(ctx, adapterapi.StartSessionRequest{CWD: server.cwd})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close() }()
	drainUntil(t, ctx, session.Events(), adapterapi.EventKindSessionStarted)

	if _, err := session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput("Count to 1000")}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainUntil(t, ctx, session.Events(), adapterapi.EventKindOutputDelta)

	if err := session.Interrupt(ctx); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	ev := drainUntilAny(t, ctx, session.Events(), []adapterapi.EventKind{
		adapterapi.EventKindTurnInterrupted,
		adapterapi.EventKindTurnFailed,
	})
	if ev.Kind != adapterapi.EventKindTurnInterrupted {
		t.Fatalf("expected interrupted event, got %s", ev.Kind)
	}
	if got := server.abortCount; got != 1 {
		t.Fatalf("expected one abort request, got %d", got)
	}
}

func collectUntil(t *testing.T, ctx context.Context, ch <-chan adapterapi.Event, kind adapterapi.EventKind) []adapterapi.Event {
	t.Helper()
	var events []adapterapi.Event
	for {
		select {
		case ev := <-ch:
			events = append(events, ev)
			if ev.Kind == kind {
				return events
			}
		case <-ctx.Done():
			t.Fatalf("timeout waiting for %s", kind)
		}
	}
}

func drainUntil(t *testing.T, ctx context.Context, ch <-chan adapterapi.Event, kind adapterapi.EventKind) adapterapi.Event {
	t.Helper()
	for {
		select {
		case ev := <-ch:
			if ev.Kind == kind {
				return ev
			}
		case <-ctx.Done():
			t.Fatalf("timeout waiting for %s", kind)
		}
	}
}

func drainUntilAny(t *testing.T, ctx context.Context, ch <-chan adapterapi.Event, kinds []adapterapi.EventKind) adapterapi.Event {
	t.Helper()
	want := make(map[adapterapi.EventKind]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	for {
		select {
		case ev := <-ch:
			if want[ev.Kind] {
				return ev
			}
		case <-ctx.Done():
			t.Fatalf("timeout waiting for one of %v", kinds)
		}
	}
}

func assertHasKind(t *testing.T, events []adapterapi.Event, kind adapterapi.EventKind) {
	t.Helper()
	for _, ev := range events {
		if ev.Kind == kind {
			return
		}
	}
	t.Fatalf("expected event kind %s in %#v", kind, events)
}
