package codex

import (
	"fmt"
	"os"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
)

type builder struct{}

func New(binary string, enabled bool) adapterapi.Adapter {
	return adapterapi.NewBaseAdapter("codex", binary, enabled, adapterapi.Capabilities{
		HeadlessRun:      true,
		StreamJSON:       true,
		NativeResume:     true,
		StructuredOutput: true,
		InteractiveMode:  true,
		RPCMode:          true,
		MCP:              true,
		SessionExport:    true,
	}, builder{})
}

func (builder) StartArgs(req adapterapi.StartRunRequest) (adapterapi.RunSpec, error) {
	lastMessageFile, err := os.CreateTemp("", "cogent-codex-last-*.txt")
	if err != nil {
		return adapterapi.RunSpec{}, fmt.Errorf("create last-message temp file: %w", err)
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

	return adapterapi.RunSpec{
		Args:            args,
		StdinContent:    req.Prompt,
		UseStdin:        true,
		LastMessagePath: lastMessageFile.Name(),
		Cleanup: func() error {
			return os.Remove(lastMessageFile.Name())
		},
	}, nil
}

func (builder) ContinueArgs(req adapterapi.ContinueRunRequest) (adapterapi.RunSpec, error) {
	lastMessageFile, err := os.CreateTemp("", "cogent-codex-last-*.txt")
	if err != nil {
		return adapterapi.RunSpec{}, fmt.Errorf("create last-message temp file: %w", err)
	}
	_ = lastMessageFile.Close()

	args := []string{
		"exec",
		"resume",
		req.NativeSessionID,
	}
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}
	args = append(args,
		"--json",
		"--skip-git-repo-check",
		"-o", lastMessageFile.Name(),
		"-",
	)

	return adapterapi.RunSpec{
		Args:            args,
		StdinContent:    req.Prompt,
		UseStdin:        true,
		LastMessagePath: lastMessageFile.Name(),
		Cleanup: func() error {
			return os.Remove(lastMessageFile.Name())
		},
	}, nil
}
