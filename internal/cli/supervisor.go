package cli

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/yusefmosiah/cogent/internal/core"
)

// rotationEntry pairs an adapter name with the model to use for that adapter.
type rotationEntry struct {
	adapter string
	model   string
}

// workRotation is the round-robin pool for dispatch.
var workRotation = []rotationEntry{
	{adapter: "native", model: "chatgpt/gpt-5.4-mini"},
	{adapter: "native", model: "zai/glm-5-turbo"},
	{adapter: "native", model: "bedrock/claude-haiku-4-5"},
	{adapter: "codex", model: "gpt-5.4"},
	{adapter: "codex", model: "gpt-5.4-mini"},
	{adapter: "claude", model: "claude-sonnet-4-6"},
	{adapter: "claude", model: "claude-haiku-4-5"},
	{adapter: "opencode", model: "zai-coding-plan/glm-5-turbo"},
}

// globalRotationIdx is incremented each time we dispatch without prior history.
var globalRotationIdx int64

// pickAdapterModel selects adapter+model for a work item.
func pickAdapterModel(item core.WorkItemRecord, jobs []core.JobRecord, rotation []rotationEntry) (adapter, model string) {
	return pickAdapterModelWithFallback(item, jobs, rotation, "")
}

// pickAdapterModelWithFallback selects adapter/model with an optional default
// adapter hint when the work item and history do not provide a stronger signal.
func pickAdapterModelWithFallback(item core.WorkItemRecord, jobs []core.JobRecord, rotation []rotationEntry, defaultAdapter string) (adapter, model string) {
	pool := rotation
	if len(pool) == 0 {
		pool = workRotation
	}
	if len(pool) == 0 {
		return "codex", "gpt-5.4"
	}

	// 1. If item has preferred adapters + models, use the first match in pool.
	if len(item.PreferredAdapters) > 0 {
		for _, pa := range item.PreferredAdapters {
			for _, pm := range item.PreferredModels {
				for _, e := range pool {
					if e.adapter == pa && e.model == pm {
						return e.adapter, e.model
					}
				}
			}
			// Adapter match without model preference
			for _, e := range pool {
				if e.adapter == pa {
					return e.adapter, e.model
				}
			}
		}
	}

	// 2. If item has preferred models without adapter, find in pool.
	if len(item.PreferredModels) > 0 {
		for _, pm := range item.PreferredModels {
			for _, e := range pool {
				if e.model == pm {
					return e.adapter, e.model
				}
			}
		}
	}

	// 3. Round-robin based on job history (avoid repeating last adapter).
	if len(jobs) > 0 {
		lastAdapter := jobs[0].Adapter
		idx := rotationIndexForEntry(lastAdapter, pool)
		if idx >= 0 {
			next := pool[(idx+1)%len(pool)]
			return next.adapter, next.model
		}
	}

	// 4. Default adapter hint.
	if defaultAdapter != "" {
		for _, e := range pool {
			if e.adapter == defaultAdapter {
				return e.adapter, e.model
			}
		}
	}

	// 5. Global round-robin.
	idx := int(atomic.AddInt64(&globalRotationIdx, 1)-1) % len(pool)
	return pool[idx].adapter, pool[idx].model
}

func rotationIndexForEntry(adapter string, pool []rotationEntry) int {
	for i, e := range pool {
		if e.adapter == adapter {
			return i
		}
	}
	return -1
}

func newSupervisorCommand(_ *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "supervisor",
		Short: "Supervisor commands (use 'fase serve --auto' for agentic supervisor)",
		Long:  `The deterministic supervisor has been removed. Use 'fase serve --auto' with the agentic supervisor (ADR-0041).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("the standalone supervisor has been removed; use 'fase serve --auto' for the agentic supervisor (ADR-0041)")
		},
	}

	cmd.AddCommand(
		newSupervisorPauseCommand(),
		newSupervisorResumeCommand(),
		newSupervisorSendCommand(),
	)

	return cmd
}

func newSupervisorPauseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "pause",
		Short: "Pause dispatch in the running supervisor",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			data, err := c.doPost("/api/supervisor/pause", nil)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}

func newSupervisorResumeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "resume",
		Short: "Resume dispatch in the running supervisor",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			data, err := c.doPost("/api/supervisor/resume", nil)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}

func newSupervisorSendCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "send [message]",
		Short: "Send a message to the running supervisor",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			msg := strings.Join(args, " ")
			data, err := c.doPost("/api/supervisor/send", map[string]string{"message": msg})
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}

func isTerminal(state string) bool {
	switch state {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

func isJobStalled(jobRawDir string, threshold time.Duration) bool {
	entries, err := os.ReadDir(jobRawDir)
	if err != nil {
		return false
	}
	var newest time.Time
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return false
	}
	return time.Since(newest) > threshold
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return true
	}
	return syscall.Kill(pid, 0) == nil
}
