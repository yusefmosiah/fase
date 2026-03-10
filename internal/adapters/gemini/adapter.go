package gemini

import (
	"context"
	"fmt"
	"os/exec"

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
	return "gemini"
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
		StructuredOutput: true,
		InteractiveMode:  true,
		MCP:              true,
		Checkpointing:    true,
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
	args := []string{
		"-p", req.Prompt,
		"--output-format", "stream-json",
		"--approval-mode", "yolo",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	cmd := exec.CommandContext(ctx, a.binary, args...)
	cmd.Dir = req.CWD
	adapterapi.PrepareCommand(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open gemini stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open gemini stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start gemini prompt: %w", err)
	}

	return &adapterapi.RunHandle{
		Cmd:    cmd,
		Stdout: stdout,
		Stderr: stderr,
		Cleanup: func() error {
			return nil
		},
	}, nil
}

func (a *Adapter) ContinueRun(ctx context.Context, req adapterapi.ContinueRunRequest) (*adapterapi.RunHandle, error) {
	return nil, fmt.Errorf("gemini CLI continuation is not implemented")
}
