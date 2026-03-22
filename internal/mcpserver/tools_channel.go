package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type notifyHostInput struct {
	Message string `json:"message" jsonschema:"the message to send to the host"`
	Type    string `json:"type,omitempty" jsonschema:"message type: status_update or question or escalation or info (default: info)"`
}

func registerChannelTools(server *mcp.Server, mcpSrv *Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "notify_host",
		Description: "Send a message to the host agent. Use this to report status updates, ask questions, or escalate issues.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input notifyHostInput) (*mcp.CallToolResult, any, error) {
		msg := strings.TrimSpace(input.Message)
		if msg == "" {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "error: message must not be empty"}}}, nil, nil
		}
		msgType := input.Type
		if msgType == "" {
			msgType = "info"
		}

		if err := mcpSrv.SendChannelEvent(msg, map[string]string{"source": "worker", "type": msgType}); err != nil {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "error: " + err.Error()}}}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Message sent to host."}}}, nil, nil
	})
}
