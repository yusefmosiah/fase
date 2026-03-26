package factory

import (
	"fmt"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

type builder struct{}

func New(binary string, enabled bool) adapterapi.Adapter {
	return adapterapi.NewBaseAdapter("factory", binary, enabled, adapterapi.Capabilities{
		HeadlessRun:      true,
		StreamJSON:       true,
		StructuredOutput: true,
		InteractiveMode:  true,
		RPCMode:          true,
	}, builder{})
}

func (builder) StartArgs(req adapterapi.StartRunRequest) (adapterapi.RunSpec, error) {
	args := []string{"exec"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, "-o", "stream-json", req.Prompt)
	return adapterapi.RunSpec{Args: args}, nil
}

func (builder) ContinueArgs(_ adapterapi.ContinueRunRequest) (adapterapi.RunSpec, error) {
	return adapterapi.RunSpec{}, fmt.Errorf("factory CLI continuation is not verified yet")
}
