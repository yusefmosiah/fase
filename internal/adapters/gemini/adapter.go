package gemini

import (
	"fmt"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

type builder struct{}

func New(binary string, enabled bool) adapterapi.Adapter {
	return adapterapi.NewBaseAdapter("gemini", binary, enabled, adapterapi.Capabilities{
		HeadlessRun:      true,
		StreamJSON:       true,
		StructuredOutput: true,
		InteractiveMode:  true,
		MCP:              true,
		Checkpointing:    true,
	}, builder{})
}

func (builder) StartArgs(req adapterapi.StartRunRequest) (adapterapi.RunSpec, error) {
	args := []string{
		"-p", req.Prompt,
		"--output-format", "stream-json",
		"--approval-mode", "yolo",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	return adapterapi.RunSpec{Args: args}, nil
}

func (builder) ContinueArgs(_ adapterapi.ContinueRunRequest) (adapterapi.RunSpec, error) {
	return adapterapi.RunSpec{}, fmt.Errorf("gemini CLI continuation is not implemented")
}
