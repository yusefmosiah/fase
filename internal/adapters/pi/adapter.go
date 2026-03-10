package pi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/yusefmosiah/cagent/internal/adapterapi"
)

type Adapter struct {
	binary  string
	enabled bool
}

func New(binary string, enabled bool) *Adapter {
	return &Adapter{
		binary:  binary,
		enabled: enabled,
	}
}

func (a *Adapter) Name() string {
	return "pi"
}

func (a *Adapter) Binary() string {
	return a.binary
}

func (a *Adapter) Implemented() bool {
	return true
}

func (a *Adapter) Capabilities() adapterapi.Capabilities {
	return adapterapi.Capabilities{
		HeadlessRun:      true,
		StreamJSON:       true,
		NativeResume:     true,
		StructuredOutput: true,
		InteractiveMode:  true,
		RPCMode:          true,
		SessionExport:    true,
	}
}

func (a *Adapter) Detect(ctx context.Context) (adapterapi.Diagnosis, error) {
	_, err := exec.LookPath(a.binary)
	version, versionErr := adapterapi.DetectVersion(ctx, a.binary, "--version")
	return adapterapi.Diagnosis{
		Adapter:      a.Name(),
		Binary:       a.binary,
		Version:      version,
		Available:    err == nil,
		Enabled:      a.enabled,
		Implemented:  a.Implemented(),
		Capabilities: a.Capabilities(),
	}, versionErr
}

func (a *Adapter) StartRun(ctx context.Context, req adapterapi.StartRunRequest) (*adapterapi.RunHandle, error) {
	sessionPath, err := sessionPath(req.CanonicalSessionID)
	if err != nil {
		return nil, err
	}
	return a.start(ctx, req.CWD, sessionPath, req.Model, req.Prompt)
}

func (a *Adapter) ContinueRun(ctx context.Context, req adapterapi.ContinueRunRequest) (*adapterapi.RunHandle, error) {
	sessionPath, _ := req.NativeSessionMeta["session_path"].(string)
	if sessionPath == "" {
		return nil, fmt.Errorf("pi continuation requires native session metadata.session_path")
	}
	return a.start(ctx, req.CWD, sessionPath, req.Model, req.Prompt)
}

func (a *Adapter) start(ctx context.Context, cwd, sessionPath, model, prompt string) (*adapterapi.RunHandle, error) {
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		return nil, fmt.Errorf("create pi session directory: %w", err)
	}

	args := []string{
		"--mode", "json",
		"--print",
		"--session", sessionPath,
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, a.binary, args...)
	cmd.Dir = cwd
	adapterapi.PrepareCommand(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open pi stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open pi stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pi: %w", err)
	}

	return &adapterapi.RunHandle{
		Cmd:    cmd,
		Stdout: stdout,
		Stderr: stderr,
		NativeSessionMeta: map[string]any{
			"session_path": sessionPath,
		},
		Cleanup: func() error {
			return nil
		},
	}, nil
}

func sessionPath(sessionID string) (string, error) {
	base := os.Getenv("PI_CODING_AGENT_DIR")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home for pi session path: %w", err)
		}
		base = filepath.Join(home, ".pi", "agent")
	}

	return filepath.Join(base, "sessions", "cagent-"+sessionID+".jsonl"), nil
}
