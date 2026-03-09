package adapterapi

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

type Capabilities struct {
	HeadlessRun      bool `json:"headless_run"`
	StreamJSON       bool `json:"stream_json"`
	NativeResume     bool `json:"native_resume"`
	NativeFork       bool `json:"native_fork"`
	StructuredOutput bool `json:"structured_output"`
	InteractiveMode  bool `json:"interactive_mode"`
	RPCMode          bool `json:"rpc_mode"`
	MCP              bool `json:"mcp"`
	Checkpointing    bool `json:"checkpointing"`
	SessionExport    bool `json:"session_export"`
}

type Diagnosis struct {
	Adapter      string       `json:"adapter"`
	Binary       string       `json:"binary"`
	Version      *string      `json:"version"`
	Available    bool         `json:"available"`
	Enabled      bool         `json:"enabled"`
	Implemented  bool         `json:"implemented"`
	Capabilities Capabilities `json:"capabilities"`
}

type StartRunRequest struct {
	CWD     string
	Prompt  string
	Model   string
	Profile string
}

type RunHandle struct {
	Cmd             *exec.Cmd
	Stdout          io.ReadCloser
	Stderr          io.ReadCloser
	LastMessagePath string
	Cleanup         func() error
}

type Adapter interface {
	Name() string
	Capabilities() Capabilities
	Implemented() bool
	Binary() string
	Detect(ctx context.Context) (Diagnosis, error)
	StartRun(ctx context.Context, req StartRunRequest) (*RunHandle, error)
}

func DetectVersion(ctx context.Context, binary string, args ...string) (*string, error) {
	lookup, err := exec.LookPath(binary)
	if err != nil {
		return nil, err
	}

	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(versionCtx, lookup, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("run %s %s: %w", lookup, strings.Join(args, " "), err)
	}

	text := strings.TrimSpace(string(output))
	if text == "" {
		return nil, nil
	}

	return &text, nil
}
