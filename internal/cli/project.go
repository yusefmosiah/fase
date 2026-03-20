package cli

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/yusefmosiah/cagent/internal/service"
)

func newProjectCommand(root *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Project-scoped operations",
	}

	var hydrateMode string

	hydrateCmd := &cobra.Command{
		Use:   "hydrate",
		Short: "Compile a project-scoped briefing for cold-starting any session",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()
			result, err := svc.ProjectHydrate(context.Background(), service.ProjectHydrateRequest{
				Mode: hydrateMode,
			})
			if err != nil {
				return mapServiceError(err)
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	hydrateCmd.Flags().StringVar(&hydrateMode, "mode", "standard", "hydration mode: thin, standard, or deep")

	cmd.AddCommand(hydrateCmd)
	return cmd
}
