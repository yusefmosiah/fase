package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/yusefmosiah/fase/internal/adapterapi"
	"github.com/yusefmosiah/fase/internal/adapters/codex"
	"github.com/yusefmosiah/fase/internal/adapters/opencode"
	"github.com/yusefmosiah/fase/internal/adapters/pi"
	"github.com/yusefmosiah/fase/internal/core"
)

type Adapter struct {
	binary  string
	enabled bool
	svc     any // service instance for FASE tools (set via SetService)
}

func New(binary string, enabled bool) *Adapter {
	return &Adapter{binary: binary, enabled: enabled}
}

// SetService injects the service instance so the native adapter can
// register FASE tools (work_list, dispatch, etc.) in its sessions.
func (a *Adapter) SetService(svc any) {
	a.svc = svc
}

func (a *Adapter) Name() string { return "native" }

func (a *Adapter) Binary() string { return a.binary }

func (a *Adapter) Implemented() bool { return true }

func (a *Adapter) Capabilities() adapterapi.Capabilities {
	return adapterapi.Capabilities{
		HeadlessRun:      true,
		NativeFork:       true,
		StructuredOutput: true,
	}
}

func (a *Adapter) Detect(ctx context.Context) (adapterapi.Diagnosis, error) {
	return adapterapi.Diagnosis{
		Adapter:      a.Name(),
		Binary:       a.binary,
		Available:    true,
		Enabled:      a.enabled,
		Implemented:  a.Implemented(),
		Capabilities: a.Capabilities(),
	}, nil
}

func (a *Adapter) StartRun(ctx context.Context, req adapterapi.StartRunRequest) (*adapterapi.RunHandle, error) {
	return a.start(ctx, req.CWD, req.Model, req.Profile, req.Prompt)
}

func (a *Adapter) ContinueRun(ctx context.Context, req adapterapi.ContinueRunRequest) (*adapterapi.RunHandle, error) {
	// Load persisted session history from disk if available.
	// The native adapter doesn't keep sessions in memory across processes —
	// history is persisted to .fase/native-sessions/<id>.json after each turn.
	var history []Message
	if req.CanonicalSessionID != "" {
		if state, err := loadSessionState(req.CWD, req.CanonicalSessionID); err == nil {
			history = state.History
		}
	}
	// Inject persistent supervisor context if available.
	// This file survives session restarts and provides cross-session memory.
	history = injectSupervisorContext(req.CWD, history)
	return a.startWithHistory(ctx, req.CWD, req.Model, req.Profile, req.Prompt, history)
}

func (a *Adapter) startWithHistory(ctx context.Context, cwd, model, profile, prompt string, history []Message) (*adapterapi.RunHandle, error) {
	return a.startInternal(ctx, cwd, model, profile, prompt, history)
}

func (a *Adapter) start(ctx context.Context, cwd, model, profile, prompt string) (*adapterapi.RunHandle, error) {
	return a.startInternal(ctx, cwd, model, profile, prompt, nil)
}

func (a *Adapter) startInternal(ctx context.Context, cwd, model, profile, prompt string, history []Message) (*adapterapi.RunHandle, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("native adapter requires --model provider/model")
	}

	helper := exec.CommandContext(ctx, "sh", "-c", "cat >/dev/null")
	adapterapi.PrepareCommand(helper)
	stdin, err := helper.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open native helper stdin: %w", err)
	}
	if err := helper.Start(); err != nil {
		return nil, fmt.Errorf("start native helper: %w", err)
	}

	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	go func() {
		defer func() {
			_ = stdin.Close()
			_ = stdoutW.Close()
			_ = stderrW.Close()
		}()

		live := NewLiveAdapter(a.svc, nil)
		live.SetCoAgents(defaultCoAgentAdapters(a.svc, a.binary))

		session, err := live.StartSession(ctx, adapterapi.StartSessionRequest{
			CWD:     cwd,
			Model:   model,
			Profile: profile,
		})
		if err != nil {
			writeNativeEvent(stderrW, map[string]any{
				"type":  "error",
				"error": err.Error(),
			})
			return
		}
		defer func() { _ = session.Close() }()

		// Inject persisted history for continued sessions.
		if len(history) > 0 {
			if ns, ok := session.(*nativeSession); ok {
				ns.mu.Lock()
				ns.history = history
				ns.mu.Unlock()
			}
		}

		writeNativeEvent(stdoutW, map[string]any{
			"type":       "session",
			"id":         session.SessionID(),
			"session_id": session.SessionID(),
		})

		// Heartbeat: for non-streaming providers (Bedrock), write periodic
		// events so housekeeping doesn't kill the job for "no output".
		heartbeatDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-heartbeatDone:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					writeNativeEvent(stdoutW, map[string]any{
						"type":       "heartbeat",
						"session_id": session.SessionID(),
					})
				}
			}
		}()

		defer close(heartbeatDone)

		turnID, err := session.StartTurn(ctx, []adapterapi.Input{adapterapi.TextInput(prompt)})
		if err != nil {
			writeNativeEvent(stderrW, map[string]any{
				"type":  "error",
				"error": err.Error(),
			})
			return
		}

		var final strings.Builder
		for ev := range session.Events() {
			switch ev.Kind {
			case adapterapi.EventKindSessionStarted, adapterapi.EventKindSessionResumed:
			case adapterapi.EventKindOutputDelta:
				final.WriteString(ev.Text)
				writeNativeEvent(stdoutW, map[string]any{
					"type":       "assistant.delta",
					"session_id": session.SessionID(),
					"turn_id":    turnID,
					"delta":      ev.Text,
				})
			case adapterapi.EventKindTurnCompleted:
				writeNativeEvent(stdoutW, map[string]any{
					"type":       "result",
					"session_id": session.SessionID(),
					"turn_id":    turnID,
					"result":     final.String(),
				})
				return
			case adapterapi.EventKindTurnInterrupted:
				writeNativeEvent(stderrW, map[string]any{
					"type":       "error",
					"session_id": session.SessionID(),
					"turn_id":    turnID,
					"error":      "turn interrupted",
				})
				return
			case adapterapi.EventKindTurnFailed, adapterapi.EventKindError:
				writeNativeEvent(stderrW, map[string]any{
					"type":       "error",
					"session_id": session.SessionID(),
					"turn_id":    turnID,
					"error":      ev.Text,
				})
				return
			}
		}
	}()

	return &adapterapi.RunHandle{
		Cmd:    helper,
		Stdout: stdoutR,
		Stderr: stderrR,
		Cleanup: func() error {
			return nil
		},
	}, nil
}

func writeNativeEvent(w io.Writer, payload map[string]any) {
	if w == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// LiveAdapter creates provider-backed native live sessions.
type LiveAdapter struct {
	svc         any
	baseTools   []Tool
	httpClient  HTTPDoer
	newClientFn func(provider Provider, httpClient HTTPDoer) (LLMClient, error)
	coAgents    map[string]adapterapi.LiveAgentAdapter
}

// NewLiveAdapter creates a native live adapter with optional shared tools.
func NewLiveAdapter(svc any, registry *ToolRegistry) *LiveAdapter {
	var baseTools []Tool
	if registry != nil {
		baseTools = append(baseTools, registry.Tools()...)
	}
	return &LiveAdapter{
		svc:         svc,
		baseTools:   baseTools,
		newClientFn: NewLLMClient,
		coAgents:    map[string]adapterapi.LiveAgentAdapter{},
	}
}

func (a *LiveAdapter) SetCoAgents(adapters map[string]adapterapi.LiveAgentAdapter) {
	if adapters == nil {
		a.coAgents = map[string]adapterapi.LiveAgentAdapter{}
		return
	}
	a.coAgents = adapters
}

func (a *LiveAdapter) Name() string { return "native" }

func (a *LiveAdapter) StartSession(ctx context.Context, req adapterapi.StartSessionRequest) (adapterapi.LiveSession, error) {
	return a.startSession(ctx, core.GenerateID("nsess"), req, false)
}

func (a *LiveAdapter) ResumeSession(ctx context.Context, nativeSessionID string, req adapterapi.StartSessionRequest) (adapterapi.LiveSession, error) {
	return a.startSession(ctx, nativeSessionID, req, true)
}

func (a *LiveAdapter) startSession(ctx context.Context, id string, req adapterapi.StartSessionRequest, resumed bool) (adapterapi.LiveSession, error) {
	provider, err := ParseProviderModel(req.Model)
	if err != nil {
		return nil, err
	}

	manager := newCoAgentManager(req.CWD, req.Profile, a.coAgents)
	registry, err := a.buildRegistry(req.CWD, manager)
	if err != nil {
		return nil, err
	}

	client, err := a.newClientFn(provider, a.httpClient)
	if err != nil {
		return nil, err
	}

	// Default reasoning effort: medium. Profile can override.
	effort := "medium"
	if req.Profile == "supervisor" {
		effort = "high"
	}

	return newNativeSession(ctx, nativeSessionConfig{
		id:              id,
		cwd:             req.CWD,
		provider:        provider,
		client:          client,
		registry:        registry,
		steerCh:         req.SteerCh,
		svc:             a.svc,
		resumed:         resumed,
		manager:         manager,
		reasoningEffort: effort,
		profile:         req.Profile,
	}), nil
}

func (a *LiveAdapter) buildRegistry(cwd string, manager *coAgentManager) (*ToolRegistry, error) {
	registry := MustNewToolRegistry()
	for _, tool := range a.baseTools {
		if err := registry.Register(tool); err != nil {
			return nil, err
		}
	}
	if err := RegisterCodingTools(registry, cwd); err != nil {
		return nil, err
	}
	// Web tools — available when any search API key is set.
	if os.Getenv("EXA_API_KEY") != "" || os.Getenv("TAVILY_API_KEY") != "" ||
		os.Getenv("BRAVE_API_KEY") != "" || os.Getenv("SERPER_API_KEY") != "" {
		_ = registry.Register(WebSearchTool())
		_ = registry.Register(WebFetchTool())
	}
	if a.svc != nil {
		if err := RegisterFASETools(registry, a.svc); err != nil {
			return nil, err
		}
	}
	if manager != nil && len(a.coAgents) > 0 {
		if err := RegisterCoAgentTools(registry, manager); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func defaultCoAgentAdapters(svc any, codexBinary string) map[string]adapterapi.LiveAgentAdapter {
	live := map[string]adapterapi.LiveAgentAdapter{
		"codex":    codex.NewLiveAdapter(defaultBinary(codexBinary, "codex")),
		"opencode": opencode.NewLiveAdapter("opencode"),
		"pi":       pi.NewLiveAdapter("pi"),
	}
	nativeAdapter := NewLiveAdapter(svc, nil)
	live["native"] = nativeAdapter
	nativeAdapter.SetCoAgents(live)
	return live
}

func defaultBinary(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
