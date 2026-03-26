package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusefmosiah/cogent/internal/core"
	"github.com/yusefmosiah/cogent/internal/service"
)

// newTestServer creates a minimal Server for testing (no DB, events work via zero-value EventBus).
func newTestServer() *Server {
	return New(&service.Service{})
}

func newServiceBackedTestServer(t *testing.T) (*Server, *service.Service) {
	t.Helper()
	t.Setenv("FASE_STATE_DIR", t.TempDir())
	t.Setenv("FASE_CONFIG_DIR", t.TempDir())
	t.Setenv("FASE_CACHE_DIR", t.TempDir())

	svc, err := service.Open(context.Background(), "")
	if err != nil {
		t.Fatalf("open service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return New(svc), svc
}

func TestSendChannelEventStdioMode(t *testing.T) {
	s := newTestServer()
	var buf bytes.Buffer
	s.SetWriter(&buf)

	if err := s.SendChannelEvent("hello", map[string]string{"type": "info"}); err != nil {
		t.Fatalf("SendChannelEvent: %v", err)
	}

	var notif channelNotification
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &notif); err != nil {
		t.Fatalf("decode notification: %v\nraw: %s", err, buf.String())
	}
	if notif.Method != "notifications/claude/channel" {
		t.Errorf("method = %q, want notifications/claude/channel", notif.Method)
	}
	if notif.Params.Content != "hello" {
		t.Errorf("content = %q, want hello", notif.Params.Content)
	}
	if notif.Params.Meta["type"] != "info" {
		t.Errorf("meta type = %q, want info", notif.Params.Meta["type"])
	}
}

func TestSendChannelEventServeModeUsesBroadcastFunc(t *testing.T) {
	s := newTestServer()
	var buf bytes.Buffer
	s.SetWriter(&buf)

	type call struct {
		eventType string
		data      any
	}
	var calls []call
	s.SetBroadcastFunc(func(eventType string, data any) {
		calls = append(calls, call{eventType, data})
	})

	if err := s.SendChannelEvent("from serve", map[string]string{"source": "job_runner", "type": "job_completed"}); err != nil {
		t.Fatalf("SendChannelEvent: %v", err)
	}

	// Writer must NOT be touched in serve mode.
	if buf.Len() > 0 {
		t.Errorf("writer was written to in serve mode: %s", buf.String())
	}

	if len(calls) != 1 {
		t.Fatalf("broadcastFn called %d times, want 1", len(calls))
	}
	if calls[0].eventType != "channel_message" {
		t.Errorf("event type = %q, want channel_message", calls[0].eventType)
	}
	payload, ok := calls[0].data.(map[string]any)
	if !ok {
		t.Fatalf("data type %T, want map[string]any", calls[0].data)
	}
	if payload["content"] != "from serve" {
		t.Errorf("content = %v, want 'from serve'", payload["content"])
	}
	meta, _ := payload["meta"].(map[string]string)
	if meta["type"] != "job_completed" {
		t.Errorf("meta type = %q, want job_completed", meta["type"])
	}
}

func TestSendChannelEventNoMetaOmitsField(t *testing.T) {
	s := newTestServer()
	var calls []map[string]any
	s.SetBroadcastFunc(func(_ string, data any) {
		calls = append(calls, data.(map[string]any))
	})

	if err := s.SendChannelEvent("no meta", nil); err != nil {
		t.Fatalf("SendChannelEvent: %v", err)
	}

	if len(calls) != 1 {
		t.Fatalf("broadcastFn called %d times, want 1", len(calls))
	}
	if _, hasMeta := calls[0]["meta"]; hasMeta {
		t.Errorf("meta should be omitted when nil, got %v", calls[0]["meta"])
	}
}

func TestReportMCPToolUsesBroadcastFuncInServeMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s := newTestServer()

	var broadcasts []map[string]any
	s.SetBroadcastFunc(func(_ string, data any) {
		if m, ok := data.(map[string]any); ok {
			broadcasts = append(broadcasts, m)
		}
	})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		_ = s.MCP.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "report",
		Arguments: map[string]any{"message": "task complete"},
	})
	if err != nil {
		t.Fatalf("call report tool: %v", err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("tool content type = %T, want *mcp.TextContent", result.Content[0])
	}
	var ack reportResult
	if err := json.Unmarshal([]byte(text.Text), &ack); err != nil {
		t.Fatalf("decode report result: %v", err)
	}
	if ack.Status != "sent" {
		t.Errorf("report status = %q, want sent", ack.Status)
	}

	if len(broadcasts) != 1 {
		t.Fatalf("broadcastFn called %d times, want 1", len(broadcasts))
	}
	if broadcasts[0]["content"] != "task complete" {
		t.Errorf("broadcast content = %v, want 'task complete'", broadcasts[0]["content"])
	}

	cancel()
	<-serverDone
}

func TestCheckRecordListMCPToolUsesCanonicalDefaultLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, svc := newServiceBackedTestServer(t)
	work, err := svc.CreateWork(ctx, service.WorkCreateRequest{
		Title:     "check list parity",
		Objective: "verify canonical default list limit",
	})
	if err != nil {
		t.Fatalf("create work: %v", err)
	}

	recordCount := core.DefaultCheckRecordListLimit - 1
	for i := 0; i < recordCount; i++ {
		if _, err := svc.CreateCheckRecord(ctx, service.CheckRecordCreateRequest{
			WorkID: work.WorkID,
			Result: "pass",
			Report: core.CheckReport{
				BuildOK:      true,
				TestsPassed:  1,
				TestOutput:   "go test ./internal/mcpserver\nok\tgithub.com/yusefmosiah/cogent/internal/mcpserver\t0.123s",
				CheckerNotes: "canonical default limit",
			},
			CreatedBy: "test",
		}); err != nil {
			t.Fatalf("create check record %d: %v", i, err)
		}
	}

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		_ = s.MCP.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "check_record_list",
		Arguments: map[string]any{"work_id": work.WorkID},
	})
	if err != nil {
		t.Fatalf("call check_record_list tool: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected single result content item, got %d", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("tool content type = %T, want *mcp.TextContent", result.Content[0])
	}

	var records []core.CheckRecord
	if err := json.Unmarshal([]byte(text.Text), &records); err != nil {
		t.Fatalf("decode check list result: %v", err)
	}
	if len(records) != recordCount {
		t.Fatalf("record count = %d, want %d", len(records), recordCount)
	}

	cancel()
	<-serverDone
}
