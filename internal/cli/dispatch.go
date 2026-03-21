package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newDispatchCommand(root *rootOptions) *cobra.Command {
	var adapter string
	var model string
	var workID string
	var force bool

	cmd := &cobra.Command{
		Use:   "dispatch [work-id]",
		Short: "Dispatch the next ready work item via fase serve",
		Long: `Dispatches a single work item for execution via the running fase serve process.

Without arguments, picks the highest-priority ready item.
With a work-id argument, dispatches that specific item (must be ready).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				workID = args[0]
			}
			c, err := connectServe()
			if err != nil {
				return err
			}
			req := map[string]any{
				"work_id": workID,
				"adapter": adapter,
				"model":   model,
				"force":   force,
			}
			data, err := c.doPost("/api/dispatch", req)
			if err != nil {
				return err
			}
			if root.jsonOutput {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			var resp map[string]any
			if err := json.Unmarshal(data, &resp); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			if msg, ok := resp["message"].(string); ok {
				fmt.Fprintln(cmd.OutOrStdout(), msg)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dispatched %s → %s via %s\n",
				resp["work_id"], resp["job_id"], resp["adapter"])
			if title, ok := resp["title"].(string); ok {
				fmt.Fprintf(cmd.OutOrStdout(), "  title: %s\n", title)
			}
			if m, ok := resp["model"].(string); ok && m != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  model: %s\n", m)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&adapter, "adapter", "", "override adapter selection")
	cmd.Flags().StringVar(&model, "model", "", "override model selection")
	cmd.Flags().BoolVar(&force, "force", false, "dispatch even if other work is in progress")

	return cmd
}
