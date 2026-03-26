package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusefmosiah/cogent/internal/channelmeta"
)

type reportInput struct {
	Message string `json:"message" jsonschema:"status report to send to supervisor or host"`
	Type    string `json:"type,omitempty" jsonschema:"message type: info, status_update, escalation (default: info)"`
}

type reportResult struct {
	Status string `json:"status"`
}

func registerChannelTools(server *mcp.Server, mcpSrv *Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "report",
		Description: "Report status to whoever dispatched you (supervisor or host). Use to report progress, completion, questions, or issues.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input reportInput) (*mcp.CallToolResult, any, error) {
		msg := strings.TrimSpace(input.Message)
		if msg == "" {
			return nil, nil, fmt.Errorf("message must not be empty")
		}
		msgType := channelmeta.NormalizeWorkerReportType(input.Type)

		if err := mcpSrv.SendChannelEvent(msg, channelmeta.WorkerReportMeta(msgType)); err != nil {
			return nil, nil, err
		}
		return jsonResult(reportResult{Status: "sent"})
	})
}
