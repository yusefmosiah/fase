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
	"time"

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
	envFile     string
	artifactDir string
	sessionID   string
}

type sendOptions struct {
	sessionID  string
	adapter    string
	prompt     string
	promptFile string
	stdin      bool
	model      string
	profile    string
}

type debriefOptions struct {
	sessionID string
	adapter   string
	model     string
	profile   string
	output    string
	reason    string
}

type transferExportOptions struct {
	jobID     string
	sessionID string
	output    string
	reason    string
	mode      string
}

type transferRunOptions struct {
	transfer string
	adapter  string
	cwd      string
	model    string
	profile  string
	label    string
}

type runtimeOptions struct {
	adapter string
}

type statusOptions struct {
	wait     bool
	interval time.Duration
	timeout  time.Duration
}

type artifactsListOptions struct {
	jobID     string
	sessionID string
	kind      string
	limit     int
}

type historySearchOptions struct {
	query     string
	adapter   string
	model     string
	cwd       string
	sessionID string
	kinds     string
	limit     int
	scanLimit int
}

type internalRunJobOptions struct {
	jobID  string
	turnID string
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
		newSendCommand(opts),
		newDebriefCommand(opts),
		newCancelCommand(opts),
		newListCommand(opts),
		newArtifactsCommand(opts),
		newHistoryCommand(opts),
		newSessionCommand(opts),
		newTransferCommand(opts, "transfer", "Export and launch explicit cross-vendor transfers", false),
		newTransferCommand(opts, "handoff", "Deprecated alias for transfer", true),
		newAdaptersCommand(opts),
		newCatalogCommand(opts),
		newRuntimeCommand(opts, "runtime", "Show the current host-agent runtime inventory", false),
		newInternalRunJobCommand(opts),
		newVersionCommand(),
	)

	return cmd
}

func newRunCommand(root *rootOptions) *cobra.Command {
	opts := &runOptions{}

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Queue a new background job",
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
	cmd.Flags().StringVar(&opts.envFile, "env-file", "", "path to an env file")
	cmd.Flags().StringVar(&opts.artifactDir, "artifact-dir", "", "override artifact directory")
	cmd.Flags().StringVar(&opts.sessionID, "session", "", "attach the run to an existing canonical session")
	_ = cmd.MarkFlagRequired("adapter")
	_ = cmd.MarkFlagRequired("cwd")

	return cmd
}

func newStatusCommand(root *rootOptions) *cobra.Command {
	opts := &statusOptions{}

	cmd := &cobra.Command{
		Use:   "status <job-id>",
		Short: "Show the latest job state and summary",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			var status *service.StatusResult
			if opts.wait {
				status, err = svc.WaitStatus(context.Background(), args[0], opts.interval, opts.timeout)
			} else {
				status, err = svc.Status(context.Background(), args[0])
			}
			if err != nil {
				return mapServiceError(err)
			}

			return renderStatus(cmd, root.jsonOutput, status)
		},
	}
	cmd.Flags().BoolVar(&opts.wait, "wait", false, "wait for the job to reach a terminal state")
	cmd.Flags().DurationVar(&opts.interval, "interval", 250*time.Millisecond, "poll interval when waiting")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 0, "maximum time to wait before exiting with a timeout")
	return cmd
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

			if follow {
				if raw {
					return followRawLogs(cmd, svc, args[0], root.jsonOutput, limit)
				}
				return followEvents(cmd, svc, args[0], root.jsonOutput, limit)
			}

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

func newSendCommand(root *rootOptions) *cobra.Command {
	opts := &sendOptions{}

	cmd := &cobra.Command{
		Use:   "send",
		Short: "Queue a continuation on a resumable native session",
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt, source, err := resolveSendPrompt(cmd, opts)
			if err != nil {
				return exitf(2, "%v", err)
			}

			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			result, sendErr := svc.Send(context.Background(), service.SendRequest{
				SessionID:    opts.sessionID,
				Adapter:      opts.adapter,
				Prompt:       prompt,
				PromptSource: source,
				Model:        opts.model,
				Profile:      opts.profile,
			})
			if result != nil {
				if err := renderRunResult(cmd, root.jsonOutput, result); err != nil {
					return err
				}
			}
			if sendErr != nil {
				return mapServiceError(sendErr)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.sessionID, "session", "", "canonical session to continue")
	cmd.Flags().StringVar(&opts.adapter, "adapter", "", "optional adapter override when a session has multiple resumable links")
	cmd.Flags().StringVar(&opts.prompt, "prompt", "", "prompt text")
	cmd.Flags().StringVar(&opts.promptFile, "prompt-file", "", "path to prompt file")
	cmd.Flags().BoolVar(&opts.stdin, "stdin", false, "read prompt from stdin")
	cmd.Flags().StringVar(&opts.model, "model", "", "requested model override")
	cmd.Flags().StringVar(&opts.profile, "profile", "", "requested adapter profile")
	_ = cmd.MarkFlagRequired("session")

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

func newDebriefCommand(root *rootOptions) *cobra.Command {
	opts := &debriefOptions{}

	cmd := &cobra.Command{
		Use:   "debrief",
		Short: "Queue a model-authored session debrief",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			result, debriefErr := svc.Debrief(context.Background(), service.DebriefRequest{
				SessionID:  opts.sessionID,
				Adapter:    opts.adapter,
				Model:      opts.model,
				Profile:    opts.profile,
				OutputPath: opts.output,
				Reason:     opts.reason,
			})
			if result != nil {
				if err := renderDebriefResult(cmd, root.jsonOutput, result); err != nil {
					return err
				}
			}
			if debriefErr != nil {
				return mapServiceError(debriefErr)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.sessionID, "session", "", "canonical session to debrief")
	cmd.Flags().StringVar(&opts.adapter, "adapter", "", "optional adapter override when a session has multiple resumable links")
	cmd.Flags().StringVar(&opts.model, "model", "", "requested model override")
	cmd.Flags().StringVar(&opts.profile, "profile", "", "requested adapter profile")
	cmd.Flags().StringVar(&opts.output, "output", "", "write the debrief artifact to a specific file")
	cmd.Flags().StringVar(&opts.reason, "reason", "", "operator-supplied focus for the debrief")
	_ = cmd.MarkFlagRequired("session")
	return cmd
}

func newSessionCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "session <session-id>",
		Short: "Inspect canonical session state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			result, err := svc.Session(context.Background(), args[0])
			if err != nil {
				return mapServiceError(err)
			}

			return renderSession(cmd, root.jsonOutput, result)
		},
	}
}

func newListCommand(root *rootOptions) *cobra.Command {
	var limit int
	var kind string
	var adapter string
	var state string
	var sessionID string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List jobs or sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			switch kind {
			case "jobs":
				jobs, err := svc.ListJobs(context.Background(), service.ListJobsRequest{
					Limit:     limit,
					Adapter:   adapter,
					State:     state,
					SessionID: sessionID,
				})
				if err != nil {
					return mapServiceError(err)
				}
				if root.jsonOutput {
					return writeJSON(cmd.OutOrStdout(), jobs)
				}
				for _, job := range jobs {
					if err := writef(
						cmd.OutOrStdout(),
						"%s\t%s\t%s\t%s\t%s\t%s\n",
						job.JobID,
						job.SessionID,
						job.Adapter,
						job.State,
						job.CreatedAt.Format("2006-01-02 15:04:05"),
						job.Label,
					); err != nil {
						return err
					}
				}
			case "sessions":
				sessions, err := svc.ListSessions(context.Background(), service.ListSessionsRequest{
					Limit:   limit,
					Adapter: adapter,
					Status:  state,
				})
				if err != nil {
					return mapServiceError(err)
				}
				if root.jsonOutput {
					return writeJSON(cmd.OutOrStdout(), sessions)
				}
				for _, session := range sessions {
					if err := writef(
						cmd.OutOrStdout(),
						"%s\t%s\t%s\t%s\t%s\n",
						session.SessionID,
						session.OriginAdapter,
						session.Status,
						session.UpdatedAt.Format("2006-01-02 15:04:05"),
						session.Label,
					); err != nil {
						return err
					}
				}
			default:
				return exitf(2, "invalid kind %q: expected jobs or sessions", kind)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&kind, "kind", "jobs", "list kind: jobs or sessions")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum number of records to list")
	cmd.Flags().StringVar(&adapter, "adapter", "", "filter by adapter")
	cmd.Flags().StringVar(&state, "state", "", "filter by job state or session status")
	cmd.Flags().StringVar(&sessionID, "session", "", "filter jobs by canonical session id")
	return cmd
}

func newArtifactsCommand(root *rootOptions) *cobra.Command {
	listOpts := &artifactsListOptions{}

	cmd := &cobra.Command{
		Use:   "artifacts",
		Short: "Inspect persisted artifacts",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List artifacts for a job or session",
			RunE: func(cmd *cobra.Command, args []string) error {
				svc, err := service.Open(context.Background(), root.configPath)
				if err != nil {
					return err
				}
				defer func() { _ = svc.Close() }()

				artifacts, err := svc.ListArtifacts(context.Background(), service.ArtifactsRequest{
					JobID:     listOpts.jobID,
					SessionID: listOpts.sessionID,
					Kind:      listOpts.kind,
					Limit:     listOpts.limit,
				})
				if err != nil {
					return mapServiceError(err)
				}
				return renderArtifacts(cmd, root.jsonOutput, artifacts)
			},
		},
		&cobra.Command{
			Use:   "show <artifact-id>",
			Short: "Show one artifact and its content",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				svc, err := service.Open(context.Background(), root.configPath)
				if err != nil {
					return err
				}
				defer func() { _ = svc.Close() }()

				result, err := svc.ReadArtifact(context.Background(), args[0])
				if err != nil {
					return mapServiceError(err)
				}
				return renderArtifact(cmd, root.jsonOutput, result)
			},
		},
	)

	listCmd := cmd.Commands()[0]
	listCmd.Flags().StringVar(&listOpts.jobID, "job", "", "list artifacts for a job")
	listCmd.Flags().StringVar(&listOpts.sessionID, "session", "", "list artifacts for a session")
	listCmd.Flags().StringVar(&listOpts.kind, "kind", "", "filter by artifact kind")
	listCmd.Flags().IntVar(&listOpts.limit, "limit", 20, "maximum number of artifacts to list")

	return cmd
}

func newHistoryCommand(root *rootOptions) *cobra.Command {
	searchOpts := &historySearchOptions{}

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Search canonical local cagent history",
	}

	searchCmd := &cobra.Command{
		Use:   "search",
		Short: "Search canonical jobs, turns, events, and artifacts",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			result, err := svc.SearchHistory(context.Background(), service.HistorySearchRequest{
				Query:     searchOpts.query,
				Adapter:   searchOpts.adapter,
				Model:     searchOpts.model,
				CWD:       searchOpts.cwd,
				SessionID: searchOpts.sessionID,
				Kinds:     splitCSV(searchOpts.kinds),
				Limit:     searchOpts.limit,
				ScanLimit: searchOpts.scanLimit,
			})
			if err != nil {
				return mapServiceError(err)
			}
			return renderHistoryMatches(cmd, root.jsonOutput, result.Matches)
		},
	}

	searchCmd.Flags().StringVar(&searchOpts.query, "query", "", "search text")
	searchCmd.Flags().StringVar(&searchOpts.adapter, "adapter", "", "limit to one adapter")
	searchCmd.Flags().StringVar(&searchOpts.model, "model", "", "limit to one model")
	searchCmd.Flags().StringVar(&searchOpts.cwd, "cwd", "", "limit to one working directory")
	searchCmd.Flags().StringVar(&searchOpts.sessionID, "session", "", "limit to one canonical session")
	searchCmd.Flags().StringVar(&searchOpts.kinds, "kinds", "", "comma-separated kinds: job,turn,event,artifact")
	searchCmd.Flags().IntVar(&searchOpts.limit, "limit", 20, "maximum matches to return")
	searchCmd.Flags().IntVar(&searchOpts.scanLimit, "scan-limit", 500, "maximum recent records per kind to scan")
	_ = searchCmd.MarkFlagRequired("query")

	cmd.AddCommand(searchCmd)
	return cmd
}

func newInternalRunJobCommand(root *rootOptions) *cobra.Command {
	opts := &internalRunJobOptions{}

	cmd := &cobra.Command{
		Use:    "__run-job",
		Short:  "Internal background worker entrypoint",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			return svc.ExecuteDetachedJob(context.Background(), opts.jobID, opts.turnID)
		},
	}

	cmd.Flags().StringVar(&opts.jobID, "job", "", "job id")
	cmd.Flags().StringVar(&opts.turnID, "turn", "", "turn id")
	_ = cmd.MarkFlagRequired("job")
	_ = cmd.MarkFlagRequired("turn")
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

func newRuntimeCommand(root *rootOptions, use, short string, hidden bool) *cobra.Command {
	opts := &runtimeOptions{}

	cmd := &cobra.Command{
		Use:    use,
		Short:  short,
		Hidden: hidden,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			result, err := svc.Runtime(context.Background(), opts.adapter)
			if err != nil {
				return mapServiceError(err)
			}

			return renderRuntime(cmd, root.jsonOutput, result)
		},
	}

	cmd.Flags().StringVar(&opts.adapter, "adapter", "", "limit output to a single adapter")
	return cmd
}

func newCatalogCommand(root *rootOptions) *cobra.Command {
	probeOpts := &struct {
		adapter     string
		provider    string
		model       string
		cwd         string
		prompt      string
		timeout     time.Duration
		concurrency int
		limit       int
	}{}

	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Discover and show provider/model inventory",
	}

	syncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Refresh the discovered provider/model catalog",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			result, err := svc.SyncCatalog(context.Background())
			if err != nil {
				return mapServiceError(err)
			}
			return renderCatalog(cmd, root.jsonOutput, result)
		},
	}

	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Show the latest discovered provider/model catalog",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			result, err := svc.Catalog(context.Background())
			if err != nil {
				return mapServiceError(err)
			}
			return renderCatalog(cmd, root.jsonOutput, result)
		},
	}

	probeCmd := &cobra.Command{
		Use:   "probe",
		Short: "Run short entitlement probes against catalog entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := service.Open(context.Background(), root.configPath)
			if err != nil {
				return err
			}
			defer func() { _ = svc.Close() }()

			result, err := svc.ProbeCatalog(context.Background(), service.ProbeCatalogRequest{
				Adapter:     probeOpts.adapter,
				Provider:    probeOpts.provider,
				Model:       probeOpts.model,
				CWD:         probeOpts.cwd,
				Prompt:      probeOpts.prompt,
				Timeout:     probeOpts.timeout,
				Concurrency: probeOpts.concurrency,
				Limit:       probeOpts.limit,
			})
			if err != nil {
				return mapServiceError(err)
			}
			return renderCatalog(cmd, root.jsonOutput, result)
		},
	}

	cmd.AddCommand(syncCmd, showCmd, probeCmd)
	probeCmd.Flags().StringVar(&probeOpts.adapter, "adapter", "", "limit probes to one adapter")
	probeCmd.Flags().StringVar(&probeOpts.provider, "provider", "", "limit probes to one provider")
	probeCmd.Flags().StringVar(&probeOpts.model, "model", "", "limit probes to one model name")
	probeCmd.Flags().StringVar(&probeOpts.cwd, "cwd", ".", "working directory for the probe run")
	probeCmd.Flags().StringVar(&probeOpts.prompt, "prompt", "", "probe prompt (defaults to a trivial OK probe)")
	probeCmd.Flags().DurationVar(&probeOpts.timeout, "timeout", 30*time.Second, "maximum time to wait per probe")
	probeCmd.Flags().IntVar(&probeOpts.concurrency, "concurrency", 4, "number of concurrent probes")
	probeCmd.Flags().IntVar(&probeOpts.limit, "limit", 0, "maximum number of matching entries to probe")

	return cmd
}

func newTransferCommand(root *rootOptions, use, short string, hidden bool) *cobra.Command {
	exportOpts := &transferExportOptions{}
	runOpts := &transferRunOptions{}

	cmd := &cobra.Command{
		Use:    use,
		Short:  short,
		Hidden: hidden,
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "export",
			Short: "Export a structured transfer bundle",
			RunE: func(cmd *cobra.Command, args []string) error {
				svc, err := service.Open(context.Background(), root.configPath)
				if err != nil {
					return err
				}
				defer func() { _ = svc.Close() }()

				result, err := svc.ExportTransfer(context.Background(), service.TransferExportRequest{
					JobID:      exportOpts.jobID,
					SessionID:  exportOpts.sessionID,
					OutputPath: exportOpts.output,
					Reason:     exportOpts.reason,
					Mode:       exportOpts.mode,
				})
				if err != nil {
					return mapServiceError(err)
				}

				if root.jsonOutput {
					return writeJSON(cmd.OutOrStdout(), result)
				}
				return writef(cmd.OutOrStdout(), "%s\t%s\n", result.Transfer.TransferID, result.Path)
			},
		},
		&cobra.Command{
			Use:   "run",
			Short: "Queue a job from a transfer bundle",
			RunE: func(cmd *cobra.Command, args []string) error {
				svc, err := service.Open(context.Background(), root.configPath)
				if err != nil {
					return err
				}
				defer func() { _ = svc.Close() }()

				result, runErr := svc.RunTransfer(context.Background(), service.TransferRunRequest{
					TransferRef: runOpts.transfer,
					Adapter:     runOpts.adapter,
					CWD:         runOpts.cwd,
					Model:       runOpts.model,
					Profile:     runOpts.profile,
					Label:       runOpts.label,
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
		},
	)

	exportCmd := cmd.Commands()[0]
	exportCmd.Flags().StringVar(&exportOpts.jobID, "job", "", "source job to export")
	exportCmd.Flags().StringVar(&exportOpts.sessionID, "session", "", "source session to export from its latest job")
	exportCmd.Flags().StringVar(&exportOpts.output, "output", "", "write the transfer manifest to a specific file")
	exportCmd.Flags().StringVar(&exportOpts.reason, "reason", "", "operator-supplied reason for the transfer")
	exportCmd.Flags().StringVar(&exportOpts.mode, "mode", "manual", "transfer mode: manual, recovery, operator_override, cost, capability")

	runCmd := cmd.Commands()[1]
	runCmd.Flags().StringVar(&runOpts.transfer, "transfer", "", "transfer id or path to a transfer JSON file")
	runCmd.Flags().StringVar(&runOpts.adapter, "adapter", "", "target adapter")
	runCmd.Flags().StringVar(&runOpts.cwd, "cwd", "", "override working directory for the target run")
	runCmd.Flags().StringVar(&runOpts.model, "model", "", "requested model override")
	runCmd.Flags().StringVar(&runOpts.profile, "profile", "", "requested adapter profile")
	runCmd.Flags().StringVar(&runOpts.label, "label", "", "optional label for the new session")
	_ = runCmd.MarkFlagRequired("transfer")
	_ = runCmd.MarkFlagRequired("adapter")

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

func resolveSendPrompt(cmd *cobra.Command, opts *sendOptions) (string, string, error) {
	runOpts := &runOptions{
		prompt:     opts.prompt,
		promptFile: opts.promptFile,
		stdin:      opts.stdin,
	}
	return resolvePrompt(cmd, runOpts)
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

func renderDebriefResult(cmd *cobra.Command, jsonOutput bool, result *service.DebriefResult) error {
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
	if result.Path != "" {
		if err := writef(cmd.OutOrStdout(), "debrief: %s\n", result.Path); err != nil {
			return err
		}
	}
	if result.Message != "" {
		if err := writef(cmd.OutOrStdout(), "message: %s\n", result.Message); err != nil {
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
	if status.Usage != nil {
		if err := writef(
			cmd.OutOrStdout(),
			"usage: input=%d output=%d total=%d cache_read=%d cache_write=%d\n",
			status.Usage.InputTokens,
			status.Usage.OutputTokens,
			status.Usage.TotalTokens,
			status.Usage.CacheReadInputTokens,
			status.Usage.CacheCreationInputTokens,
		); err != nil {
			return err
		}
	}
	if status.Cost != nil && status.Cost.TotalCostUSD > 0 {
		label := "vendor"
		if status.Cost.Estimated {
			label = "estimated"
		}
		if err := writef(cmd.OutOrStdout(), "cost: $%.6f (%s)\n", status.Cost.TotalCostUSD, label); err != nil {
			return err
		}
	}
	if len(status.NativeSessions) > 0 {
		if err := writef(cmd.OutOrStdout(), "native_sessions: %d\n", len(status.NativeSessions)); err != nil {
			return err
		}
	}
	return writef(cmd.OutOrStdout(), "events: %d\n", len(status.Events))
}

func renderSession(cmd *cobra.Command, jsonOutput bool, result *service.SessionResult) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), result)
	}

	if err := writef(cmd.OutOrStdout(), "session: %s\n", result.Session.SessionID); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "status: %s\n", result.Session.Status); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "cwd: %s\n", result.Session.CWD); err != nil {
		return err
	}
	if result.Session.LatestJobID != "" {
		if err := writef(cmd.OutOrStdout(), "latest_job: %s\n", result.Session.LatestJobID); err != nil {
			return err
		}
	}
	for _, native := range result.NativeSessions {
		lockState := ""
		if native.LockedByJobID != "" {
			lockState = "\tlocked_by=" + native.LockedByJobID
		}
		if err := writef(cmd.OutOrStdout(), "native: %s\t%s\tresumable=%t%s\n", native.Adapter, native.NativeSessionID, native.Resumable, lockState); err != nil {
			return err
		}
	}
	for _, turn := range result.Turns {
		if err := writef(cmd.OutOrStdout(), "turn: %s\t%s\t%s\t%s\n", turn.TurnID, turn.Adapter, turn.Status, turn.ResultSummary); err != nil {
			return err
		}
	}
	for _, action := range result.Actions {
		if err := writef(cmd.OutOrStdout(), "action: %s\tadapter=%s\tavailable=%t\t%s\n", action.Action, action.Adapter, action.Available, action.Reason); err != nil {
			return err
		}
	}
	return nil
}

func renderArtifacts(cmd *cobra.Command, jsonOutput bool, artifacts []core.ArtifactRecord) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), artifacts)
	}

	for _, artifact := range artifacts {
		if err := writef(
			cmd.OutOrStdout(),
			"%s\t%s\t%s\t%s\t%s\n",
			artifact.ArtifactID,
			artifact.JobID,
			artifact.Kind,
			artifact.CreatedAt.Format("2006-01-02 15:04:05"),
			artifact.Path,
		); err != nil {
			return err
		}
	}
	return nil
}

func renderArtifact(cmd *cobra.Command, jsonOutput bool, result *service.ArtifactResult) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), result)
	}

	if err := writef(cmd.OutOrStdout(), "artifact: %s\n", result.Artifact.ArtifactID); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "job: %s\n", result.Artifact.JobID); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "session: %s\n", result.Artifact.SessionID); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "kind: %s\n", result.Artifact.Kind); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "path: %s\n", result.Artifact.Path); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "created: %s\n", result.Artifact.CreatedAt.Format("2006-01-02T15:04:05Z07:00")); err != nil {
		return err
	}
	if len(result.Artifact.Metadata) > 0 {
		if err := writef(cmd.OutOrStdout(), "metadata: %s\n", compactJSON(mustJSON(result.Artifact.Metadata))); err != nil {
			return err
		}
	}
	if result.Content != "" {
		if err := writef(cmd.OutOrStdout(), "\n%s", result.Content); err != nil {
			return err
		}
		if !strings.HasSuffix(result.Content, "\n") {
			return writef(cmd.OutOrStdout(), "\n")
		}
	}
	return nil
}

func renderHistoryMatches(cmd *cobra.Command, jsonOutput bool, matches []core.HistoryMatch) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), matches)
	}

	for _, match := range matches {
		if err := writef(
			cmd.OutOrStdout(),
			"%s\t%s\t%s\t%s\t%s\n",
			match.Timestamp.Format("2006-01-02 15:04:05"),
			match.Kind,
			match.Adapter,
			emptyDash(match.Model),
			match.ID,
		); err != nil {
			return err
		}
		if err := writef(cmd.OutOrStdout(), "  session=%s job=%s cwd=%s\n", match.SessionID, emptyDash(match.JobID), emptyDash(match.CWD)); err != nil {
			return err
		}
		if match.Path != "" {
			if err := writef(cmd.OutOrStdout(), "  path=%s\n", match.Path); err != nil {
				return err
			}
		}
		if match.Title != "" {
			if err := writef(cmd.OutOrStdout(), "  title=%s\n", match.Title); err != nil {
				return err
			}
		}
		if match.Snippet != "" {
			if err := writef(cmd.OutOrStdout(), "  %s\n", match.Snippet); err != nil {
				return err
			}
		}
	}
	return nil
}

func followEvents(cmd *cobra.Command, svc *service.Service, jobID string, jsonOutput bool, limit int) error {
	var lastSeq int64
	for {
		events, err := svc.LogsAfter(context.Background(), jobID, lastSeq, limit)
		if err != nil {
			return err
		}
		if jsonOutput {
			for _, event := range events {
				if err := writeJSON(cmd.OutOrStdout(), event); err != nil {
					return err
				}
			}
		} else {
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
				lastSeq = event.Seq
			}
		}
		if jsonOutput && len(events) > 0 {
			lastSeq = events[len(events)-1].Seq
		}

		status, err := svc.Status(context.Background(), jobID)
		if err != nil {
			return err
		}
		if status.Job.State.Terminal() && len(events) == 0 {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func followRawLogs(cmd *cobra.Command, svc *service.Service, jobID string, jsonOutput bool, limit int) error {
	var lastSeq int64
	for {
		logs, events, err := svc.RawLogsAfter(context.Background(), jobID, lastSeq, limit)
		if err != nil {
			return err
		}
		for _, event := range events {
			lastSeq = event.Seq
		}

		if jsonOutput {
			for _, entry := range logs {
				if err := writeJSON(cmd.OutOrStdout(), entry); err != nil {
					return err
				}
			}
		} else {
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
		}

		status, err := svc.Status(context.Background(), jobID)
		if err != nil {
			return err
		}
		if status.Job.State.Terminal() && len(events) == 0 {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func renderEvents(cmd *cobra.Command, jsonOutput, follow bool, events []core.EventRecord) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), events)
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

func renderRuntime(cmd *cobra.Command, jsonOutput bool, result *service.RuntimeResult) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), result)
	}

	if err := writef(cmd.OutOrStdout(), "config: %s\tpresent=%t\n", result.ConfigPath, result.ConfigPresent); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "state_dir: %s\n", result.Paths.StateDir); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "cache_dir: %s\n", result.Paths.CacheDir); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "defaults:\tjson=%t\n", result.Defaults.JSON); err != nil {
		return err
	}
	for _, adapter := range result.Adapters {
		if err := writef(
			cmd.OutOrStdout(),
			"adapter: %s\tenabled=%t\tavailable=%t\tbinary=%s\tspeed=%s\tcost=%s\n",
			adapter.Adapter,
			adapter.Enabled,
			adapter.Available,
			adapter.Binary,
			emptyDash(adapter.Speed),
			emptyDash(adapter.Cost),
		); err != nil {
			return err
		}
		if adapter.Summary != "" {
			if err := writef(cmd.OutOrStdout(), "  summary: %s\n", adapter.Summary); err != nil {
				return err
			}
		}
		if len(adapter.Tags) > 0 {
			if err := writef(cmd.OutOrStdout(), "  tags: %s\n", strings.Join(adapter.Tags, ", ")); err != nil {
				return err
			}
		}
	}

	return nil
}

func renderCatalog(cmd *cobra.Command, jsonOutput bool, result *service.CatalogResult) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), result)
	}

	if err := writef(cmd.OutOrStdout(), "snapshot: %s\n", result.Snapshot.SnapshotID); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "created: %s\n", result.Snapshot.CreatedAt.Format("2006-01-02T15:04:05Z07:00")); err != nil {
		return err
	}
	for _, entry := range result.Snapshot.Entries {
		pricingText := "-"
		if entry.Pricing != nil && (entry.Pricing.InputUSDPerMTok > 0 || entry.Pricing.OutputUSDPerMTok > 0) {
			pricingText = fmt.Sprintf("in=$%.3f/M out=$%.3f/M", entry.Pricing.InputUSDPerMTok, entry.Pricing.OutputUSDPerMTok)
		}
		probeText := emptyDash(entry.ProbeStatus)
		if entry.ProbeStatus != "" && entry.ProbeMessage != "" {
			probeText = entry.ProbeStatus + ":" + entry.ProbeMessage
		}
		historyText := "-"
		if entry.History != nil {
			historyText = fmt.Sprintf(
				"jobs=%d ok=%d fail=%d cancel=%d",
				entry.History.RecentJobs,
				entry.History.RecentSuccesses,
				entry.History.RecentFailures,
				entry.History.RecentCancelled,
			)
			if entry.History.LastUsedAt != nil {
				historyText += " last=" + entry.History.LastUsedAt.Format("2006-01-02")
			}
		}
		if err := writef(
			cmd.OutOrStdout(),
			"%s\t%s\t%s\tselected=%t\tauth=%s\tbilling=%s\tpricing=%s\tprobe=%s\thistory=%s\tsource=%s\n",
			entry.Adapter,
			emptyDash(entry.Provider),
			emptyDash(entry.Model),
			entry.Selected,
			emptyDash(entry.AuthMethod),
			emptyDash(entry.BillingClass),
			pricingText,
			probeText,
			historyText,
			emptyDash(entry.Source),
		); err != nil {
			return err
		}
	}
	for _, issue := range result.Snapshot.Issues {
		if err := writef(cmd.OutOrStdout(), "issue: %s\t%s\t%s\n", issue.Adapter, issue.Severity, issue.Message); err != nil {
			return err
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
	if errors.Is(err, service.ErrBusy) {
		return exitf(7, "%v", err)
	}
	if errors.Is(err, service.ErrSessionLocked) {
		return exitf(7, "%v", err)
	}
	if errors.Is(err, service.ErrVendorProcess) {
		return exitf(8, "%v", err)
	}
	if errors.Is(err, service.ErrTimeout) {
		return exitf(9, "%v", err)
	}

	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return err
	}

	return err
}

func mustJSON(v any) []byte {
	encoded, _ := json.Marshal(v)
	return encoded
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
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

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
