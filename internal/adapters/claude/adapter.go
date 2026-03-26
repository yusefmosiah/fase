package claude

import (
	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

type builder struct{}

func New(binary string, enabled bool) adapterapi.Adapter {
	return adapterapi.NewBaseAdapter("claude", binary, enabled, adapterapi.Capabilities{
		HeadlessRun:      true,
		StreamJSON:       true,
		NativeResume:     true,
		StructuredOutput: true,
		InteractiveMode:  true,
	}, builder{})
}

func (builder) StartArgs(req adapterapi.StartRunRequest) (adapterapi.RunSpec, error) {
	args := []string{
		"--print",
		"--verbose",
		"--output-format", "stream-json",
		"--permission-mode", "bypassPermissions",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, req.Prompt)
	return adapterapi.RunSpec{Args: args}, nil
}

func (builder) ContinueArgs(req adapterapi.ContinueRunRequest) (adapterapi.RunSpec, error) {
	args := []string{
		"--resume", req.NativeSessionID,
		"--print",
		"--verbose",
		"--output-format", "stream-json",
		"--permission-mode", "bypassPermissions",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, req.Prompt)
	return adapterapi.RunSpec{Args: args}, nil
}
