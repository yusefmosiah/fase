package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version = "dev"

type rootOptions struct {
	configPath string
	jsonOutput bool
}

func Execute() error {
	return NewRootCommand().Execute()
}

func NewRootCommand() *cobra.Command {
	opts := &rootOptions{}

	cmd := &cobra.Command{
		Use:           "cagent",
		Short:         "Run coding-agent CLIs behind one local contract",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().StringVar(&opts.configPath, "config", "", "path to config.toml")
	cmd.PersistentFlags().BoolVar(&opts.jsonOutput, "json", false, "emit machine-readable output")

	cmd.AddCommand(
		newPlaceholderCommand("run", "Start a new job"),
		newPlaceholderCommand("status", "Show the latest job state and summary"),
		newPlaceholderCommand("logs", "Stream canonical events or raw output"),
		newPlaceholderCommand("send", "Continue a resumable native session"),
		newPlaceholderCommand("cancel", "Cancel a running job"),
		newPlaceholderCommand("list", "List jobs or sessions"),
		newPlaceholderCommand("session", "Inspect canonical session state"),
		newHandoffCommand(),
		newPlaceholderCommand("adapters", "List adapter availability and capability flags"),
		newPlaceholderCommand("doctor", "Check adapter binaries, auth, and writable dirs"),
		newPlaceholderCommand("gc", "Collect old artifacts and compact the store"),
		newVersionCommand(),
	)

	return cmd
}

func newHandoffCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "handoff",
		Short: "Export and launch cross-vendor handoffs",
	}

	cmd.AddCommand(
		newPlaceholderCommand("export", "Export a structured handoff packet"),
		newPlaceholderCommand("run", "Run a job from a handoff packet"),
	)

	return cmd
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the cagent version",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Println(version)
		},
	}
}

func newPlaceholderCommand(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("%s is not implemented yet", cmd.CommandPath())
		},
	}
}
