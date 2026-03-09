package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yusefmosiah/cagent/internal/adapters"
	"github.com/yusefmosiah/cagent/internal/core"
	"github.com/yusefmosiah/cagent/internal/service"
)

var version = "dev"

type rootOptions struct {
	configPath string
	jsonOutput bool
}

type runOptions struct {
	adapter     string
	cwd         string
	prompt      string
	promptFile  string
	stdin       bool
	label       string
	model       string
	profile     string
	detached    bool
	envFile     string
	artifactDir string
	sessionID   string
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
		newRunCommand(opts),
		newStatusCommand(opts),
		newLogsCommand(opts),
		newPlaceholderCommand("send", "Continue a resumable native session"),
		newCancelCommand(opts),
		newListCommand(opts),
		newPlaceholderCommand("session", "Inspect canonical session state"),
		newHandoffCommand(),
		newAdaptersCommand(opts),
		newPlaceholderCommand("doctor", "Check adapter binaries, auth, and writable dirs"),
		newPlaceholderCommand("gc", "Collect old artifacts and compact the store"),
		newVersionCommand(),
	)

	return cmd
}

func newRunCommand(root *rootOptions) *cobra.Command {
	opts := &runOptions{}

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start a new job",
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt, source, err := resolvePrompt(cmd, opts)
			if err != nil {
				return exitf(2, "%v", err)
			}

			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			result, runErr := svc.Run(context.Background(), service.RunRequest{
				Adapter:      opts.adapter,
				CWD:          opts.cwd,
				Prompt:       prompt,
				PromptSource: source,
				Label:        opts.label,
				Model:        opts.model,
				Profile:      opts.profile,
				Detached:     opts.detached,
				EnvFile:      opts.envFile,
				ArtifactDir:  opts.artifactDir,
				SessionID:    opts.sessionID,
			})

			if result != nil {
				if err := renderRunResult(cmd, root.jsonOutput, result); err != nil {
					return err
				}
			}

			if runErr != nil {
				return mapServiceError(runErr)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&opts.adapter, "adapter", "", "adapter to use")
	cmd.Flags().StringVar(&opts.cwd, "cwd", "", "working directory for the run")
	cmd.Flags().StringVar(&opts.prompt, "prompt", "", "prompt text")
	cmd.Flags().StringVar(&opts.promptFile, "prompt-file", "", "path to prompt file")
	cmd.Flags().BoolVar(&opts.stdin, "stdin", false, "read prompt from stdin")
	cmd.Flags().StringVar(&opts.label, "label", "", "optional human label")
	cmd.Flags().StringVar(&opts.model, "model", "", "requested model override")
	cmd.Flags().StringVar(&opts.profile, "profile", "", "requested adapter profile")
	cmd.Flags().BoolVar(&opts.detached, "detached", false, "return immediately after launching")
	cmd.Flags().StringVar(&opts.envFile, "env-file", "", "path to an env file")
	cmd.Flags().StringVar(&opts.artifactDir, "artifact-dir", "", "override artifact directory")
	cmd.Flags().StringVar(&opts.sessionID, "session", "", "attach the run to an existing canonical session")
	_ = cmd.MarkFlagRequired("adapter")
	_ = cmd.MarkFlagRequired("cwd")

	return cmd
}

func newStatusCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status <job-id>",
		Short: "Show the latest job state and summary",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			status, err := svc.Status(context.Background(), args[0])
			if err != nil {
				return mapServiceError(err)
			}

			return renderStatus(cmd, root.jsonOutput, status)
		},
	}
}

func newLogsCommand(root *rootOptions) *cobra.Command {
	var raw bool
	var follow bool
	var limit int

	cmd := &cobra.Command{
		Use:   "logs <job-id>",
		Short: "Stream canonical events or raw output",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			if raw {
				logs, err := svc.RawLogs(context.Background(), args[0], limit)
				if err != nil {
					return mapServiceError(err)
				}
				return renderRawLogs(cmd, root.jsonOutput, follow, logs)
			}

			logs, err := svc.Logs(context.Background(), args[0], limit)
			if err != nil {
				return mapServiceError(err)
			}
			return renderEvents(cmd, root.jsonOutput, follow, logs)
		},
	}

	cmd.Flags().BoolVar(&raw, "raw", false, "show raw persisted artifacts instead of canonical events")
	cmd.Flags().BoolVar(&follow, "follow", false, "follow logs until the job is terminal")
	cmd.Flags().IntVar(&limit, "limit", 200, "maximum number of entries to return")

	return cmd
}

func newCancelCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <job-id>",
		Short: "Cancel a running job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			job, err := svc.Cancel(context.Background(), args[0])
			if err != nil {
				return mapServiceError(err)
			}

			if root.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), job)
			}

			return writef(cmd.OutOrStdout(), "%s: %s\n", job.JobID, job.State)
		},
	}
}

func newListCommand(root *rootOptions) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List jobs or sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			jobs, err := svc.ListJobs(context.Background(), limit)
			if err != nil {
				return mapServiceError(err)
			}

			if root.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), jobs)
			}

			for _, job := range jobs {
				if err := writef(
					cmd.OutOrStdout(),
					"%s\t%s\t%s\t%s\t%s\n",
					job.JobID,
					job.Adapter,
					job.State,
					job.CreatedAt.Format("2006-01-02 15:04:05"),
					job.Label,
				); err != nil {
					return err
				}
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 50, "maximum number of jobs to list")
	return cmd
}

func newAdaptersCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "adapters",
		Short: "List adapter availability and capability flags",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			catalog := adapters.CatalogFromConfig(svc.Config)
			if root.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), catalog)
			}

			for _, entry := range catalog {
				if err := writef(
					cmd.OutOrStdout(),
					"%s\tavailable=%t\timplemented=%t\tbinary=%s\n",
					entry.Adapter,
					entry.Available,
					entry.Implemented,
					entry.Binary,
				); err != nil {
					return err
				}
			}

			return nil
		},
	}
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
			return exitf(5, "%s is not implemented yet", cmd.CommandPath())
		},
	}
}

func resolvePrompt(cmd *cobra.Command, opts *runOptions) (string, string, error) {
	sources := 0
	if opts.prompt != "" {
		sources++
	}
	if opts.promptFile != "" {
		sources++
	}
	if opts.stdin {
		sources++
	}

	if sources != 1 {
		return "", "", errors.New("exactly one of --prompt, --prompt-file, or --stdin is required")
	}

	switch {
	case opts.prompt != "":
		return opts.prompt, "prompt", nil
	case opts.promptFile != "":
		data, err := os.ReadFile(opts.promptFile)
		if err != nil {
			return "", "", fmt.Errorf("read prompt file: %w", err)
		}
		return string(data), "prompt_file", nil
	default:
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", "", fmt.Errorf("read stdin: %w", err)
		}
		return string(data), "stdin", nil
	}
}

func renderRunResult(cmd *cobra.Command, jsonOutput bool, result *service.RunResult) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), result)
	}

	if err := writef(cmd.OutOrStdout(), "job: %s\n", result.Job.JobID); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "session: %s\n", result.Session.SessionID); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "adapter: %s\n", result.Job.Adapter); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "state: %s\n", result.Job.State); err != nil {
		return err
	}
	if msg := result.Message; msg != "" {
		if err := writef(cmd.OutOrStdout(), "message: %s\n", msg); err != nil {
			return err
		}
	}

	return nil
}

func renderStatus(cmd *cobra.Command, jsonOutput bool, status *service.StatusResult) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), status)
	}

	if err := writef(cmd.OutOrStdout(), "job: %s\n", status.Job.JobID); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "session: %s\n", status.Session.SessionID); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "adapter: %s\n", status.Job.Adapter); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "state: %s\n", status.Job.State); err != nil {
		return err
	}
	if status.Job.Label != "" {
		if err := writef(cmd.OutOrStdout(), "label: %s\n", status.Job.Label); err != nil {
			return err
		}
	}
	if status.Job.FinishedAt != nil {
		if err := writef(cmd.OutOrStdout(), "finished: %s\n", status.Job.FinishedAt.Format("2006-01-02T15:04:05Z07:00")); err != nil {
			return err
		}
	}
	if summary, ok := status.Job.Summary["message"].(string); ok && summary != "" {
		if err := writef(cmd.OutOrStdout(), "summary: %s\n", summary); err != nil {
			return err
		}
	}
	return writef(cmd.OutOrStdout(), "events: %d\n", len(status.Events))
}

func renderEvents(cmd *cobra.Command, jsonOutput, follow bool, events []core.EventRecord) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), events)
	}

	if follow {
		if err := writef(cmd.OutOrStdout(), "follow is currently bounded to persisted events\n"); err != nil {
			return err
		}
	}

	for _, event := range events {
		payload := compactJSON(event.Payload)
		if payload == "" {
			if err := writef(cmd.OutOrStdout(), "%05d %s %s\n", event.Seq, event.Kind, event.TS.Format(timeLayout)); err != nil {
				return err
			}
		} else {
			if err := writef(cmd.OutOrStdout(), "%05d %s %s %s\n", event.Seq, event.Kind, event.TS.Format(timeLayout), payload); err != nil {
				return err
			}
		}
	}

	return nil
}

func renderRawLogs(cmd *cobra.Command, jsonOutput, follow bool, logs []service.RawLogEntry) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), logs)
	}

	if follow {
		if err := writef(cmd.OutOrStdout(), "follow is currently bounded to persisted artifacts\n"); err != nil {
			return err
		}
	}

	for _, entry := range logs {
		if err := writef(cmd.OutOrStdout(), "[%s] %s\n", entry.Stream, entry.Path); err != nil {
			return err
		}
		if entry.Content != "" {
			if err := writef(cmd.OutOrStdout(), "%s", entry.Content); err != nil {
				return err
			}
			if !strings.HasSuffix(entry.Content, "\n") {
				if err := writef(cmd.OutOrStdout(), "\n"); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writef(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func mapServiceError(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, service.ErrNotFound) {
		return exitf(6, "%v", err)
	}
	if errors.Is(err, service.ErrUnsupported) {
		return exitf(5, "%v", err)
	}
	if errors.Is(err, service.ErrAdapterUnavailable) {
		return exitf(3, "%v", err)
	}
	if errors.Is(err, service.ErrInvalidInput) {
		return exitf(2, "%v", err)
	}
	if errors.Is(err, service.ErrVendorProcess) {
		return exitf(8, "%v", err)
	}

	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return err
	}

	return err
}

const timeLayout = "2006-01-02T15:04:05Z07:00"

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "{}" {
		return ""
	}

	var dst bytes.Buffer
	if err := json.Compact(&dst, raw); err != nil {
		return string(raw)
	}

	return dst.String()
}
