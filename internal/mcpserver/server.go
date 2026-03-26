package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusefmosiah/cogent/internal/core"
	"github.com/yusefmosiah/cogent/internal/service"
)

// Context keys for request-scoped provenance tracking.
// These replace the process-global internalSessionID approach to ensure
// concurrent external and supervisor MCP traffic is correctly labeled.
type contextKey string

const (
	// ContextKeyCallerRole carries the caller role (e.g., "supervisor", "mcp", "host").
	// This enables request-scoped provenance tracking instead of global state.
	ContextKeyCallerRole contextKey = "fase.caller.role"
	// ContextKeySessionID carries the session ID for attribution.
	ContextKeySessionID contextKey = "fase.session.id"
)

// Server wraps the MCP server and provides channel notification support.
type Server struct {
	MCP *mcp.Server
	svc *service.Service

	// mu protects w, broadcastFn, callerRole, and stdio writes.
	mu sync.Mutex
	w  io.Writer

	// broadcastFn, when set, routes channel events through the WebSocket hub
	// (serve mode). When nil, events are written to w directly (stdio mode).
	broadcastFn func(string, any)

	// callerRole tracks the role for this server's session (e.g., "supervisor", "mcp", "host").
	// When set, all tool mutations from this server instance use this role for provenance.
	// This is per-server (not global) because each session has its own Server instance.
	callerRole string
}

// SessionManager manages per-session MCP server instances for StreamableHTTP
// transport, ensuring session isolation and correct provenance tracking.
// This solves the shared mutable state problem identified in round-5 scrutiny.
type SessionManager struct {
	svc                 *service.Service
	mu                  sync.RWMutex
	sessions            map[string]*Server // sessionID -> server
	supervisorSessionID string             // tracks which session is the supervisor
	broadcastFn         func(string, any)  // global broadcast function for new sessions to inherit
}

// NewSessionManager creates a new session manager backed by the given service.
func NewSessionManager(svc *service.Service) *SessionManager {
	return &SessionManager{
		svc:      svc,
		sessions: make(map[string]*Server),
	}
}

// GetServer returns an MCP server for the given session ID.
// For the supervisor session, the server is configured with supervisor provenance.
// For external sessions, the server returns "mcp" as the default CreatedBy.
// This implements per-session state isolation for VAL-SUPERVISOR-003.
func (sm *SessionManager) GetServer(sessionID string) *mcp.Server {
	sm.mu.RLock()
	server, exists := sm.sessions[sessionID]
	isSupervisor := sm.supervisorSessionID == sessionID
	sm.mu.RUnlock()

	if exists {
		return server.MCP
	}

	// Create new server for this session
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Double-check after acquiring write lock
	if server, exists := sm.sessions[sessionID]; exists {
		return server.MCP
	}

	server = New(sm.svc)
	if isSupervisor {
		server.SetCallerRole("supervisor")
	}
	// Inherit broadcast function for serve-mode sessions (broadcast propagation fix)
	if sm.broadcastFn != nil {
		server.SetBroadcastFunc(sm.broadcastFn)
	}
	sm.sessions[sessionID] = server
	return server.MCP
}

// SetSupervisorSession marks the given session ID as the supervisor session.
// Future GetServer calls for this session will return a server with supervisor provenance.
func (sm *SessionManager) SetSupervisorSession(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.supervisorSessionID = sessionID
}

// GetServerInstance returns the mcpserver.Server instance for the given session ID.
// This is used for operations like SendChannelEvent that need the wrapper, not the MCP server.
func (sm *SessionManager) GetServerInstance(sessionID string) *Server {
	sm.mu.RLock()
	server, exists := sm.sessions[sessionID]
	isSupervisor := sm.supervisorSessionID == sessionID
	sm.mu.RUnlock()

	if exists {
		return server
	}

	// Create new server for this session
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Double-check after acquiring write lock
	if server, exists := sm.sessions[sessionID]; exists {
		return server
	}

	server = New(sm.svc)
	if isSupervisor {
		server.SetCallerRole("supervisor")
	}
	// Inherit broadcast function for serve-mode sessions (broadcast propagation fix)
	if sm.broadcastFn != nil {
		server.SetBroadcastFunc(sm.broadcastFn)
	}
	sm.sessions[sessionID] = server
	return server
}

// GetSupervisorSession returns the current supervisor session ID (if any).
func (sm *SessionManager) GetSupervisorSession() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.supervisorSessionID
}

// SetBroadcastFunc sets the broadcast function on all managed servers
// and stores it for future sessions to inherit. This is used in serve mode
// to route channel events through the WebSocket hub (VAL-SUPERVISOR-003).
func (sm *SessionManager) SetBroadcastFunc(fn func(string, any)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	// Store the broadcast function so new sessions inherit it
	sm.broadcastFn = fn
	// Apply to existing sessions
	for _, server := range sm.sessions {
		server.SetBroadcastFunc(fn)
	}
}

// GetServerForRequest returns the MCP server for a given HTTP request.
// It extracts the session ID from the Mcp-Session-Id header and returns
// the appropriate server instance for that session.
// This method signature matches what mcp.NewStreamableHTTPHandler expects.
func (sm *SessionManager) GetServerForRequest(req *http.Request) *mcp.Server {
	sessionID := req.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		sessionID = "default"
	}
	return sm.GetServer(sessionID)
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
	// Register MCP tools with request-scoped provenance tracking.
	// Tools extract caller context from the request to determine proper Actor
	// (supervisor vs mcp vs host) for emitted events.
	registerTools(mcpServer, s)
	registerChannelTools(mcpServer, s)
	return s
}

// SetWriter sets the writer for channel notifications (defaults to os.Stdout).
func (s *Server) SetWriter(w io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w = w
}

// SetBroadcastFunc registers a broadcast function for serve mode. When set,
// SendChannelEvent routes channel events through this function (which should
// call hub.broadcast) instead of writing to w. The MCP proxy's WebSocket
// listener then relays them as notifications/claude/channel to Claude Code.
func (s *Server) SetBroadcastFunc(fn func(string, any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcastFn = fn
}

// SetCallerRole sets the caller role for this server instance.
// When set, all MCP tool mutations from this server will use this role
// (e.g., "supervisor", "host", "mcp") for provenance tracking.
// This is per-server (not global) because each session has its own Server instance.
// This implements VAL-SUPERVISOR-003: supervisor-triggered MCP mutations
// preserve trustworthy provenance without interfering with concurrent traffic.
func (s *Server) SetCallerRole(role string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callerRole = role
}

// GetCallerRole returns the current caller role for this server instance.
func (s *Server) GetCallerRole() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callerRole
}

// WithCallerRole returns a context with the caller role set.
// Use this to mark a context as coming from a specific caller (supervisor, host, etc.)
// so that MCP tool mutations emit events with correct Actor provenance.
// This implements request-scoped provenance tracking (VAL-SUPERVISOR-003).
func WithCallerRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, ContextKeyCallerRole, role)
}

// WithSessionID returns a context with the session ID set for attribution.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, ContextKeySessionID, sessionID)
}

// CreatedBy extracts the appropriate CreatedBy value from the request context and server.
// It checks for caller role in the context (set by WithCallerRole), then falls back
// to the server's callerRole, and finally defaults to "mcp" for external callers.
// This enables request-scoped provenance tracking: supervisor-triggered mutations
// show ActorSupervisor, external MCP calls show ActorMCP.
func (s *Server) CreatedBy(ctx context.Context) string {
	// First, check if the context has an explicit role override
	if role, ok := ctx.Value(ContextKeyCallerRole).(string); ok && role != "" {
		return role
	}

	// Second, check the server's configured caller role
	s.mu.Lock()
	role := s.callerRole
	s.mu.Unlock()
	if role != "" {
		return role
	}

	// Default: external MCP caller
	return "mcp"
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
// In serve mode (broadcastFn set): routes via WebSocket hub so the MCP proxy
// can relay it as notifications/claude/channel. In stdio mode: writes directly.
func (s *Server) SendChannelEvent(content string, meta map[string]string) error {
	s.mu.Lock()
	fn := s.broadcastFn
	s.mu.Unlock()

	if fn != nil {
		// Serve/proxy mode: broadcast via WebSocket hub. The proxy's
		// listenWebSocketChannels goroutine converts channel_message events
		// to notifications/claude/channel on its stdout.
		payload := map[string]any{"content": content}
		if len(meta) > 0 {
			payload["meta"] = meta
		}
		fn("channel_message", payload)
		return nil
	}

	// Stdio mode: write JSON-RPC notification directly to the transport writer.
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
	Mode   string `json:"mode,omitempty" jsonschema:"hydration mode: thin, standard, or deep"`
	Format string `json:"format,omitempty" jsonschema:"output format: json or markdown (default: markdown)"`
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
	Title             string   `json:"title" jsonschema:"required,work item title"`
	Objective         string   `json:"objective" jsonschema:"required,work item objective"`
	Kind              string   `json:"kind" jsonschema:"required,work item kind: implement, plan, or attest"`
	Priority          int      `json:"priority,omitempty" jsonschema:"priority (higher is more urgent)"`
	PreferredAdapters []string `json:"preferred_adapters,omitempty" jsonschema:"preferred adapter names for dispatch"`
	PreferredModels   []string `json:"preferred_models,omitempty" jsonschema:"preferred model IDs for dispatch"`
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

type checkRecordCreateInput struct {
	WorkID       string   `json:"work_id" jsonschema:"required,work item ID"`
	Result       string   `json:"result" jsonschema:"required,check result: pass or fail"`
	BuildOK      bool     `json:"build_ok" jsonschema:"whether the build succeeded"`
	TestsPassed  int      `json:"tests_passed,omitempty" jsonschema:"number of tests that passed"`
	TestsFailed  int      `json:"tests_failed,omitempty" jsonschema:"number of tests that failed"`
	TestOutput   string   `json:"test_output,omitempty" jsonschema:"test output (truncated to 50KB)"`
	DiffStat     string   `json:"diff_stat,omitempty" jsonschema:"git diff --stat output"`
	Screenshots  []string `json:"screenshots,omitempty" jsonschema:"absolute paths to screenshots in .fase/artifacts/<work-id>/screenshots/"`
	Videos       []string `json:"videos,omitempty" jsonschema:"absolute paths to video recordings"`
	CheckerNotes string   `json:"checker_notes,omitempty" jsonschema:"free-form observations from the checker"`
	CheckerModel string   `json:"checker_model,omitempty" jsonschema:"model that performed the check"`
	WorkerModel  string   `json:"worker_model,omitempty" jsonschema:"model that did the work"`
}

type checkRecordShowInput struct {
	CheckID string `json:"check_id" jsonschema:"required,check record ID"`
}

type checkRecordListInput struct {
	WorkID string `json:"work_id" jsonschema:"required,work item ID"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max records to return (default 10)"`
}

type sessionSendInput struct {
	SessionID string `json:"session_id" jsonschema:"required,session ID to send the message to"`
	Prompt    string `json:"prompt" jsonschema:"required,message to send to the session"`
	WorkID    string `json:"work_id,omitempty" jsonschema:"associated work item ID"`
	Adapter   string `json:"adapter,omitempty" jsonschema:"adapter to use (default: same as session's adapter)"`
	Model     string `json:"model,omitempty" jsonschema:"model to use"`
}

type sendEscalationEmailInput struct {
	WorkID         string `json:"work_id" jsonschema:"required,work item ID"`
	Summary        string `json:"summary" jsonschema:"required,summary of what keeps going wrong"`
	Recommendation string `json:"recommendation" jsonschema:"required,supervisor's recommendation for spec change"`
}

func registerTools(server *mcp.Server, mcpSrv *Server) {
	svc := mcpSrv.svc
	mcp.AddTool(server, &mcp.Tool{
		Name:        "project_hydrate",
		Description: "Compile a project-scoped briefing: conventions, graph summary, active work, pending attestations, contract. Returns markdown by default for session injection.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input projectHydrateInput) (*mcp.CallToolResult, any, error) {
		mode := input.Mode
		if mode == "" {
			mode = "standard"
		}
		format := input.Format
		if format == "" {
			format = "markdown"
		}
		result, err := svc.ProjectHydrate(ctx, service.ProjectHydrateRequest{Mode: mode, Format: format})
		if err != nil {
			return nil, nil, err
		}
		if format == "markdown" {
			md := service.RenderProjectHydrateMarkdown(result)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: md}}}, nil, nil
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
		Description: "Show the canonical review bundle for a single work item, including state, checks, attestations, artifacts, docs, approvals, and promotions.",
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
			CreatedBy:      mcpSrv.CreatedBy(ctx),
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
			Title:             input.Title,
			Objective:         input.Objective,
			Kind:              input.Kind,
			Priority:          input.Priority,
			PreferredAdapters: input.PreferredAdapters,
			PreferredModels:   input.PreferredModels,
			CreatedBy:         mcpSrv.CreatedBy(ctx),
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
			CreatedBy: mcpSrv.CreatedBy(ctx),
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
			CreatedBy:    mcpSrv.CreatedBy(ctx),
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

	mcp.AddTool(server, &mcp.Tool{
		Name:        "check_record_create",
		Description: "Submit a check record for a work item. Called by checkers to record their verification results (build status, test results, screenshots, notes).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input checkRecordCreateInput) (*mcp.CallToolResult, any, error) {
		rec, err := svc.CreateCheckRecord(ctx, service.CheckRecordCreateRequest{
			WorkID:       input.WorkID,
			CheckerModel: input.CheckerModel,
			WorkerModel:  input.WorkerModel,
			Result:       input.Result,
			Report: core.CheckReport{
				BuildOK:      input.BuildOK,
				TestsPassed:  input.TestsPassed,
				TestsFailed:  input.TestsFailed,
				TestOutput:   input.TestOutput,
				DiffStat:     input.DiffStat,
				Screenshots:  input.Screenshots,
				Videos:       input.Videos,
				CheckerNotes: input.CheckerNotes,
			},
			CreatedBy: mcpSrv.CreatedBy(ctx),
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(rec)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "check_record_show",
		Description: "Show a check record by ID, including the full report with test results, diff stat, and checker notes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input checkRecordShowInput) (*mcp.CallToolResult, any, error) {
		rec, err := svc.GetCheckRecord(ctx, input.CheckID)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(rec)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "check_record_list",
		Description: "List check records for a work item, newest first. Use this to read check reports and count failures.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input checkRecordListInput) (*mcp.CallToolResult, any, error) {
		limit := input.Limit
		if limit <= 0 {
			limit = core.DefaultCheckRecordListLimit
		}
		records, err := svc.ListCheckRecords(ctx, input.WorkID, limit)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(records)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_send",
		Description: "Send a message to an existing session (e.g., send failure context back to a worker). The session must support continuation.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input sessionSendInput) (*mcp.CallToolResult, any, error) {
		result, err := svc.Send(ctx, service.SendRequest{
			SessionID: input.SessionID,
			Prompt:    input.Prompt,
			WorkID:    input.WorkID,
			Adapter:   input.Adapter,
			Model:     input.Model,
		})
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(result)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "send_escalation_email",
		Description: "Email the human when a work item has failed verification 3+ times. Include a summary of what keeps going wrong and a recommendation for how to fix the spec.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input sendEscalationEmailInput) (*mcp.CallToolResult, any, error) {
		svc.SendSpecEscalationEmail(ctx, input.WorkID, input.Summary, input.Recommendation)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Escalation email sent."}}}, nil, nil
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
