package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/yusefmosiah/fase/internal/core"
)

// rotationEntry pairs an adapter name with the model to use for that adapter.
type rotationEntry struct {
	adapter string
	model   string
}

// workRotation is the round-robin pool for dispatch.
var workRotation = []rotationEntry{
	{adapter: "codex", model: "gpt-5.4"},
	{adapter: "codex", model: "gpt-5.4-mini"},
	{adapter: "claude", model: "claude-sonnet-4-6"},
	{adapter: "claude", model: "claude-haiku-4-5"},
	{adapter: "opencode", model: "zai-coding-plan/glm-5-turbo"},
}

// globalRotationIdx is incremented each time we dispatch without prior history.
var globalRotationIdx int64

// rotationIndexForAdapter returns the index of adapter in workRotation, or -1.
func rotationIndexForAdapter(adapter string) int {
	for i, e := range workRotation {
		if e.adapter == adapter {
			return i
		}
	}
	return -1
}

func modelForAdapter(adapter string) string {
	for _, e := range workRotation {
		if e.adapter == adapter {
			return e.model
		}
	}
	return ""
}

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

func attestAdapterModel(workAdapter string) (adapter, model string) {
	idx := rotationIndexForAdapter(workAdapter)
	if idx < 0 {
		for _, e := range workRotation {
			if e.adapter != workAdapter {
				return e.adapter, e.model
			}
		}
		return workRotation[0].adapter, workRotation[0].model
	}
	next := workRotation[(idx+1)%len(workRotation)]
	return next.adapter, next.model
}

func newSupervisorCommand(root *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "supervisor",
		Short: "Supervisor commands (use 'fase serve --auto' for agentic supervisor)",
		Long:  `The deterministic supervisor has been removed. Use 'fase serve --auto' with the agentic supervisor (ADR-0041).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("the standalone supervisor has been removed; use 'fase serve --auto' for the agentic supervisor (ADR-0041)")
		},
	}

	cmd.AddCommand(
		newSupervisorPauseCommand(root),
		newSupervisorResumeCommand(root),
	)

	return cmd
}

func newSupervisorPauseCommand(root *rootOptions) *cobra.Command {
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

func newSupervisorResumeCommand(root *rootOptions) *cobra.Command {
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

// spawnRun launches `fase run --json` and extracts the job_id from the output.
func spawnRun(bin, configPath, adapter, model, cwd, prompt string, extraEnv []string) (string, error) {
	args := []string{"run", "--json", "--adapter", adapter, "--cwd", cwd, "--prompt", prompt}
	if model != "" {
		args = append(args, "--model", model)
	}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}

	runCmd := exec.Command(bin, args...)
	runCmd.Dir = cwd
	runCmd.Stderr = nil
	runCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if len(extraEnv) > 0 {
		runCmd.Env = append(os.Environ(), extraEnv...)
	}
	out, err := runCmd.Output()
	if err != nil {
		return "", fmt.Errorf("fase run failed: %w", err)
	}

	var result struct {
		Job struct {
			JobID string `json:"job_id"`
		} `json:"job"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return "", fmt.Errorf("failed to parse run output: %w", err)
	}
	if result.Job.JobID == "" {
		return "", fmt.Errorf("run returned no job_id")
	}
	return result.Job.JobID, nil
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
