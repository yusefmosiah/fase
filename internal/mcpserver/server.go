package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusefmosiah/fase/internal/core"
	"github.com/yusefmosiah/fase/internal/service"
)

// Server wraps the MCP server and provides channel notification support.
type Server struct {
	MCP *mcp.Server
	svc *service.Service

	// mu protects writes to the stdio connection so channel notifications
	// don't interleave with MCP protocol messages.
	mu sync.Mutex
	w  io.Writer
}

// New creates an MCP server backed by the given service.
func New(svc *service.Service) *Server {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "FASE", Version: "0.1.0"},
		&mcp.ServerOptions{
			Instructions: "FASE work graph server. Use tools to inspect and manage work items, attestations, and project state.\n\nWhen working with tool results, write down any important information you might need later in your response, as the original tool result may be cleared later.",
			Capabilities: &mcp.ServerCapabilities{
				Experimental: map[string]any{
					"claude/channel": map[string]any{},
				},
			},
		},
	)
	s := &Server{MCP: mcpServer, svc: svc, w: os.Stdout}
	registerTools(mcpServer, svc)
	return s
}

// SetWriter sets the writer for channel notifications (defaults to os.Stdout).
func (s *Server) SetWriter(w io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w = w
}

// channelNotification is a JSON-RPC notification for claude/channel.
type channelNotification struct {
	JSONRPC string              `json:"jsonrpc"`
	Method  string              `json:"method"`
	Params  channelNotifyParams `json:"params"`
}

type channelNotifyParams struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// SendChannelEvent pushes a channel event to the connected Claude Code session.
func (s *Server) SendChannelEvent(content string, meta map[string]string) error {
	msg := channelNotification{
		JSONRPC: "2.0",
		Method:  "notifications/claude/channel",
		Params:  channelNotifyParams{Content: content, Meta: meta},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal channel event: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.w.Write(data)
	return err
}

// RunStdio runs the MCP server over stdio transport.
func (s *Server) RunStdio(ctx context.Context) error {
	go s.runEventForwarder(ctx)
	return s.MCP.Run(ctx, &mcp.StdioTransport{})
}

// runEventForwarder subscribes to the service event bus and forwards
// work graph events as channel notifications.
func (s *Server) runEventForwarder(ctx context.Context) {
	ch := s.svc.Events.Subscribe()
	defer s.svc.Events.Unsubscribe(ch)

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			content := fmt.Sprintf("Work item %s (%s): %s [state=%s]", ev.WorkID, ev.Title, ev.Kind, ev.State)
			meta := map[string]string{
				"work_id": ev.WorkID,
				"kind":    string(ev.Kind),
				"state":   ev.State,
			}
			if ev.PrevState != "" && ev.PrevState != ev.State {
				content = fmt.Sprintf("Work item %s (%s): %s [%s → %s]", ev.WorkID, ev.Title, ev.Kind, ev.PrevState, ev.State)
				meta["prev_state"] = ev.PrevState
			}
			_ = s.SendChannelEvent(content, meta)
		}
	}
}

// --- Tool input types ---

type projectHydrateInput struct {
	Mode string `json:"mode,omitempty" jsonschema:"hydration mode: thin, standard, or deep"`
}

type workListInput struct {
	Limit          int    `json:"limit,omitempty" jsonschema:"max items to return"`
	Kind           string `json:"kind,omitempty" jsonschema:"filter by kind"`
	ExecutionState string `json:"execution_state,omitempty" jsonschema:"filter by execution state"`
}

type workShowInput struct {
	WorkID string `json:"work_id" jsonschema:"required,work item ID"`
}

type workNotesInput struct {
	WorkID string `json:"work_id" jsonschema:"required,work item ID"`
}

type workUpdateInput struct {
	WorkID         string `json:"work_id" jsonschema:"required,work item ID"`
	ExecutionState string `json:"execution_state,omitempty" jsonschema:"new execution state"`
	Message        string `json:"message,omitempty" jsonschema:"update message"`
}

type workCreateInput struct {
	Title     string `json:"title" jsonschema:"required,work item title"`
	Objective string `json:"objective" jsonschema:"required,work item objective"`
	Kind      string `json:"kind" jsonschema:"required,work item kind: implement, plan, or attest"`
	Priority  int    `json:"priority,omitempty" jsonschema:"priority (higher is more urgent)"`
}

type workNoteAddInput struct {
	WorkID   string `json:"work_id" jsonschema:"required,work item ID"`
	NoteType string `json:"note_type" jsonschema:"required,note type: finding, convention, or private"`
	Body     string `json:"body" jsonschema:"required,note body text"`
}

type workAttestInput struct {
	WorkID       string `json:"work_id" jsonschema:"required,work item ID"`
	Result       string `json:"result" jsonschema:"required,attestation result: passed or failed"`
	Summary      string `json:"summary" jsonschema:"required,attestation summary"`
	VerifierKind string `json:"verifier_kind,omitempty" jsonschema:"verifier kind (default: attestation)"`
	Method       string `json:"method,omitempty" jsonschema:"attestation method (default: automated_review)"`
}

type workClaimInput struct {
	WorkID   string `json:"work_id" jsonschema:"required,work item ID"`
	Claimant string `json:"claimant,omitempty" jsonschema:"who is claiming (default: mcp)"`
}

type readyWorkInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"max items to return"`
}

func registerTools(server *mcp.Server, svc *service.Service) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "project_hydrate",
		Description: "Compile a project-scoped briefing: conventions, graph summary, active work, pending attestations, contract.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input projectHydrateInput) (*mcp.CallToolResult, any, error) {
		mode := input.Mode
		if mode == "" {
			mode = "standard"
		}
		result, err := svc.ProjectHydrate(ctx, service.ProjectHydrateRequest{Mode: mode})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(result)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "work_list",
		Description: "List work items, optionally filtered by kind or execution state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input workListInput) (*mcp.CallToolResult, any, error) {
		items, err := svc.ListWork(ctx, service.WorkListRequest{
			Limit:          input.Limit,
			Kind:           input.Kind,
			ExecutionState: input.ExecutionState,
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(items)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "work_show",
		Description: "Show full details for a single work item including updates, notes, and attestations.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input workShowInput) (*mcp.CallToolResult, any, error) {
		detail, err := svc.Work(ctx, input.WorkID)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(detail)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "work_notes",
		Description: "List notes for a work item.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input workNotesInput) (*mcp.CallToolResult, any, error) {
		detail, err := svc.Work(ctx, input.WorkID)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(detail.Notes)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "work_update",
		Description: "Update a work item's execution state and/or add an update message.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input workUpdateInput) (*mcp.CallToolResult, any, error) {
		work, err := svc.UpdateWork(ctx, service.WorkUpdateRequest{
			WorkID:         input.WorkID,
			ExecutionState: core.WorkExecutionState(input.ExecutionState),
			Message:        input.Message,
			CreatedBy:      "mcp",
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(work)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "work_create",
		Description: "Create a new work item.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input workCreateInput) (*mcp.CallToolResult, any, error) {
		work, err := svc.CreateWork(ctx, service.WorkCreateRequest{
			Title:     input.Title,
			Objective: input.Objective,
			Kind:      input.Kind,
			Priority:  input.Priority,
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(work)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "work_note_add",
		Description: "Add a note to a work item.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input workNoteAddInput) (*mcp.CallToolResult, any, error) {
		note, err := svc.AddWorkNote(ctx, service.WorkNoteRequest{
			WorkID:    input.WorkID,
			NoteType:  input.NoteType,
			Body:      input.Body,
			CreatedBy: "mcp",
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(note)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "work_attest",
		Description: "Submit an attestation for a work item.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input workAttestInput) (*mcp.CallToolResult, any, error) {
		verifierKind := input.VerifierKind
		if verifierKind == "" {
			verifierKind = "attestation"
		}
		method := input.Method
		if method == "" {
			method = "automated_review"
		}
		att, _, err := svc.AttestWork(ctx, service.WorkAttestRequest{
			WorkID:       input.WorkID,
			Result:       input.Result,
			Summary:      input.Summary,
			VerifierKind: verifierKind,
			Method:       method,
			CreatedBy:    "mcp",
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(att)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "work_claim",
		Description: "Claim a work item for execution.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input workClaimInput) (*mcp.CallToolResult, any, error) {
		claimant := input.Claimant
		if claimant == "" {
			claimant = "mcp"
		}
		work, err := svc.ClaimWork(ctx, service.WorkClaimRequest{
			WorkID:   input.WorkID,
			Claimant: claimant,
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(work)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ready_work",
		Description: "List work items that are ready for dispatch (unclaimed, unblocked).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input readyWorkInput) (*mcp.CallToolResult, any, error) {
		limit := input.Limit
		if limit <= 0 {
			limit = 20
		}
		items, err := svc.ReadyWork(ctx, limit, false)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(items)
	})
}

func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal result: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}
