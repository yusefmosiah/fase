package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/yusefmosiah/cagent/internal/service"
)

func newDispatchCommand(root *rootOptions) *cobra.Command {
	var adapter string
	var model string
	var workID string

	cmd := &cobra.Command{
		Use:   "dispatch [work-id]",
		Short: "Run the next ready work item through the DAG",
		Long: `Dispatches a single work item for execution, respecting the DAG.

Without arguments, picks the highest-priority ready item.
With a work-id argument, dispatches that specific item (must be ready).

This is the preferred way to run work — it goes through the DAG
instead of bypassing it like "cagent run".`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				workID = args[0]
			}
			return runDispatch(cmd, root, workID, adapter, model)
		},
	}

	cmd.Flags().StringVar(&adapter, "adapter", "", "override adapter selection")
	cmd.Flags().StringVar(&model, "model", "", "override model selection")

	return cmd
}

func runDispatch(cmd *cobra.Command, root *rootOptions, workID, adapterOverride, modelOverride string) error {
	ctx := context.Background()

	svc, err := service.Open(ctx, root.configPath)
	if err != nil {
		return err
	}
	defer func() { _ = svc.Close() }()

	cwd, _ := os.Getwd()

	// If no work ID specified, pick the highest-priority ready item
	var item *service.WorkShowResult
	if workID == "" {
		readyItems, err := svc.ReadyWork(ctx, 1, false)
		if err != nil {
			return fmt.Errorf("list ready work: %w", err)
		}
		if len(readyItems) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no ready work items")
			return nil
		}
		workID = readyItems[0].WorkID
		result, err := svc.Work(ctx, workID)
		if err != nil {
			return mapServiceError(err)
		}
		item = result
	} else {
		result, err := svc.Work(ctx, workID)
		if err != nil {
			return mapServiceError(err)
		}
		if result.Work.ExecutionState != "ready" {
			return fmt.Errorf("work %s is %s, not ready", workID, result.Work.ExecutionState)
		}
		item = result
	}

	// Pick adapter+model using round-robin rotation, offset from job history.
	// Explicit --adapter and --model flags take priority.
	pickedAdapter, pickedModel := pickAdapterModel(item.Work, item.Jobs, nil)
	adapter := adapterOverride
	if adapter == "" {
		adapter = pickedAdapter
	}
	model := modelOverride
	if model == "" {
		model = pickedModel
	}

	// Hydrate briefing
	briefing, err := svc.HydrateWork(ctx, service.WorkHydrateRequest{
		WorkID:   workID,
		Mode:     "standard",
		Claimant: "dispatch",
	})
	if err != nil {
		return fmt.Errorf("hydrate work: %w", err)
	}

	briefingJSON, _ := json.Marshal(briefing)

	// Build prompt — include model override if specified
	prompt := string(briefingJSON)

	// Dispatch via cagent run (reuses existing job infrastructure)
	result, runErr := svc.Run(ctx, service.RunRequest{
		Adapter: adapter,
		CWD:     cwd,
		Prompt:  prompt,
		Model:   model,
		WorkID:  workID,
	})

	if result != nil {
		if root.jsonOutput {
			_ = writeJSON(cmd.OutOrStdout(), result)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "dispatched %s → %s via %s\n", workID, result.Job.JobID, adapter)
			fmt.Fprintf(cmd.OutOrStdout(), "  title: %s\n", item.Work.Title)
			if model != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  model: %s\n", model)
			}
		}
	}

	if runErr != nil {
		return mapServiceError(runErr)
	}

	// Claim the work item
	_, _ = svc.ClaimWork(ctx, service.WorkClaimRequest{
		WorkID:        workID,
		Claimant:      "dispatch",
		LeaseDuration: 30 * time.Minute,
	})

	return nil
}
