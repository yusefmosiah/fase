package cli

import (
	"net/url"

	"github.com/spf13/cobra"
)

func newProjectCommand(root *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Project-scoped operations",
	}

	var hydrateMode string
	var hydrateFormat string

	hydrateCmd := &cobra.Command{
		Use:   "hydrate",
		Short: "Compile a project-scoped briefing for cold-starting any session",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			params := url.Values{
				"mode":   []string{hydrateMode},
				"format": []string{hydrateFormat},
			}
			data, err := c.doGet("/api/project/hydrate", params)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	hydrateCmd.Flags().StringVar(&hydrateMode, "mode", "standard", "hydration mode: thin, standard, or deep")
	hydrateCmd.Flags().StringVar(&hydrateFormat, "format", "markdown", "output format: markdown or json")

	cmd.AddCommand(hydrateCmd)
	return cmd
}
