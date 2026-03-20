package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/yusefmosiah/fase/internal/adapterapi"
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
	return "codex"
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
		MCP:              true,
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
	lastMessageFile, err := os.CreateTemp("", "fase-codex-last-*.txt")
	if err != nil {
		return nil, fmt.Errorf("create last-message temp file: %w", err)
	}
	_ = lastMessageFile.Close()

	args := []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		"-C", req.CWD,
		"-o", lastMessageFile.Name(),
	}
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}
	if req.Profile != "" {
		args = append(args, "-p", req.Profile)
	}
	args = append(args, "-")

	cmd := exec.CommandContext(ctx, a.binary, args...)
	cmd.Dir = req.CWD
	adapterapi.PrepareCommand(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open codex stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open codex stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open codex stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex exec: %w", err)
	}

	go func() {
		_, _ = stdin.Write([]byte(req.Prompt))
		_ = stdin.Close()
	}()

	return &adapterapi.RunHandle{
		Cmd:             cmd,
		Stdout:          stdout,
		Stderr:          stderr,
		LastMessagePath: lastMessageFile.Name(),
		Cleanup: func() error {
			return os.Remove(lastMessageFile.Name())
		},
	}, nil
}

func (a *Adapter) ContinueRun(ctx context.Context, req adapterapi.ContinueRunRequest) (*adapterapi.RunHandle, error) {
	lastMessageFile, err := os.CreateTemp("", "fase-codex-last-*.txt")
	if err != nil {
		return nil, fmt.Errorf("create last-message temp file: %w", err)
	}
	_ = lastMessageFile.Close()

	args := []string{
		"exec",
		"resume",
		req.NativeSessionID,
		"--json",
		"--skip-git-repo-check",
		"-o", lastMessageFile.Name(),
		"-",
	}
	if req.Model != "" {
		args = append(args[:3], append([]string{"-m", req.Model}, args[3:]...)...)
	}

	cmd := exec.CommandContext(ctx, a.binary, args...)
	cmd.Dir = req.CWD
	adapterapi.PrepareCommand(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open codex resume stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open codex resume stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open codex resume stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex exec resume: %w", err)
	}

	go func() {
		_, _ = stdin.Write([]byte(req.Prompt))
		_ = stdin.Close()
	}()

	return &adapterapi.RunHandle{
		Cmd:             cmd,
		Stdout:          stdout,
		Stderr:          stderr,
		LastMessagePath: lastMessageFile.Name(),
		Cleanup: func() error {
			return os.Remove(lastMessageFile.Name())
		},
	}, nil
}
