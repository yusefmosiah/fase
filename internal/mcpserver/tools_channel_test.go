package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusefmosiah/cogent/internal/channelmeta"
	"github.com/yusefmosiah/cogent/internal/service"
)

func TestReportToolUsesWorkerReportContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server := New(&service.Service{})
	var writer bytes.Buffer
	server.SetWriter(&writer)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		_ = server.MCP.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "report",
		Arguments: map[string]any{"message": "hello from mcp"},
	})
	if err != nil {
		t.Fatalf("call report tool: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected a single result content item, got %d", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	var ack reportResult
	if err := json.Unmarshal([]byte(text.Text), &ack); err != nil {
		t.Fatalf("decode report result: %v", err)
	}
	if ack.Status != "sent" {
		t.Fatalf("unexpected report status: %q", ack.Status)
	}

	var notification channelNotification
	if err := json.Unmarshal(bytes.TrimSpace(writer.Bytes()), &notification); err != nil {
		t.Fatalf("decode channel notification: %v", err)
	}
	if notification.Method != "notifications/claude/channel" {
		t.Fatalf("unexpected notification method: %s", notification.Method)
	}
	if notification.Params.Content != "hello from mcp" {
		t.Fatalf("unexpected notification content: %q", notification.Params.Content)
	}
	if want := channelmeta.WorkerReportMeta(channelmeta.TypeInfo); !reflect.DeepEqual(notification.Params.Meta, want) {
		t.Fatalf("unexpected notification meta: got %#v want %#v", notification.Params.Meta, want)
	}

	cancel()
	<-serverDone
}
