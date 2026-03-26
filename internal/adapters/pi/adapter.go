package pi

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

type builder struct{}

func New(binary string, enabled bool) adapterapi.Adapter {
	return adapterapi.NewBaseAdapter("pi", binary, enabled, adapterapi.Capabilities{
		HeadlessRun:      true,
		StreamJSON:       true,
		NativeResume:     true,
		StructuredOutput: true,
		InteractiveMode:  true,
		RPCMode:          true,
		SessionExport:    true,
	}, builder{})
}

func (builder) StartArgs(req adapterapi.StartRunRequest) (adapterapi.RunSpec, error) {
	sessionPath, err := resolveSessionPath(req.CanonicalSessionID)
	if err != nil {
		return adapterapi.RunSpec{}, err
	}
	return buildSpec(sessionPath, req.Model, req.Prompt), nil
}

func (builder) ContinueArgs(req adapterapi.ContinueRunRequest) (adapterapi.RunSpec, error) {
	sessionPath, _ := req.NativeSessionMeta["session_path"].(string)
	if sessionPath == "" {
		return adapterapi.RunSpec{}, fmt.Errorf("pi continuation requires native session metadata.session_path")
	}
	return buildSpec(sessionPath, req.Model, req.Prompt), nil
}

func buildSpec(sessionPath, model, prompt string) adapterapi.RunSpec {
	args := []string{
		"--mode", "json",
		"--print",
		"--session", sessionPath,
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, prompt)

	return adapterapi.RunSpec{
		Args: args,
		NativeSessionMeta: map[string]any{
			"session_path": sessionPath,
		},
	}
}

func resolveSessionPath(sessionID string) (string, error) {
	base := os.Getenv("PI_CODING_AGENT_DIR")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home for pi session path: %w", err)
		}
		base = filepath.Join(home, ".pi", "agent")
	}
	return filepath.Join(base, "sessions", "fase-"+sessionID+".jsonl"), nil
}
