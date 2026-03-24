package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yusefmosiah/fase/internal/adapters"
	"github.com/yusefmosiah/fase/internal/core"
	"github.com/yusefmosiah/fase/internal/service"
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
	artifactDir string
	sessionID   string
	workID      string
}

type sendOptions struct {
	sessionID  string
	adapter    string
	prompt     string
	promptFile string
	stdin      bool
	model      string
	profile    string
	workID     string
}

type workCreateOptions struct {
	title                string
	objective            string
	kind                 string
	parent               string
	lockState            string
	priority             int
	position             int
	requiredCapabilities string
	requiredModelTraits  string
	preferredAdapters    string
	forbiddenAdapters    string
	preferredModels      string
	avoidModels          string
	requiredAttestations string
	acceptance           string
	headCommitOID        string
	configurationClass   string
	budgetClass          string
}

type workListOptions struct {
	limit           int
	kind            string
	executionState  string
	approvalState   string
	includeArchived bool
}

type workUpdateOptions struct {
	executionState string
	approvalState  string
	lockState      string
	phase          string
	message        string
	jobID          string
	sessionID      string
	artifactID     string
	force          bool
}

type workNoteOptions struct {
	noteType string
	text     string
}

type workShowOptions struct {
	limit int
}

type workReadyOptions struct {
	limit           int
	includeArchived bool
}

type workClaimOptions struct {
	claimant string
	lease    time.Duration
	limit    int
	force    bool
}

type workDiscoverOptions struct {
	title     string
	objective string
	kind      string
	rationale string
}

type workProposalListOptions struct {
	limit  int
	state  string
	target string
	source string
}

type workProposalCreateOptions struct {
	proposalType string
	target       string
	source       string
	rationale    string
	patch        string
}

type workAttestOptions struct {
	result                  string
	summary                 string
	jobID                   string
	sessionID               string
	artifactID              string
	method                  string
	verifierKind            string
	verifierIdentity        string
	confidence              float64
	blocking                bool
	supersedesAttestationID string
	metadata                string
	nonce                   string
}

type workProjectionOptions struct {
	format string
}

type workApproveOptions struct {
	message string
}

type workCheckOptions struct {
	result       string
	checkerModel string
	workerModel  string
	testOutput   string
	testsPassed  int
	testsFailed  int
	buildOK      bool
	diffStat     string
	checkerNotes string
	screenshots  string // comma-separated paths
	videos       string // comma-separated paths
}

type workHydrateOptions struct {
	mode    string
	debrief bool
}

type workPromoteOptions struct {
	environment string
	targetRef   string
	message     string
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
	workID    string
	kind      string
	limit     int
}

type artifactsAttachOptions struct {
	jobID     string
	sessionID string
	workID    string
	path      string
	kind      string
	copy      bool
	metadata  string
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

type bootstrapInspectOptions struct {
	paths []string
}

type bootstrapCreateOptions struct {
	paths     []string
	title     string
	objective string
	kind      string
}

func Execute() error {
	return NewRootCommand().Execute()
}

func NewRootCommand() *cobra.Command {
	opts := &rootOptions{}

	cmd := &cobra.Command{
		Use:           "fase",
		Aliases:       []string{},
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
		newBootstrapCommand(opts),
		newWorkCommand(opts),
		newCheckCommand(opts),
		newReportCommand(opts),
		newSessionCommand(opts),
		newTransferCommand(opts, "transfer", "Export and launch explicit cross-vendor transfers", false),
		newAdaptersCommand(opts),
		newCatalogCommand(opts),
		newRuntimeCommand(opts, "runtime", "Show the current host-agent runtime inventory", false),
		newInternalRunJobCommand(opts),
		newInboxCommand(opts),
		newReconcileCommand(opts),
		newSupervisorCommand(opts),
		newDashboardCommand(opts),
		newServeCommand(opts),
		newDispatchCommand(opts),
		newProjectCommand(opts),
		newMCPCommand(opts),
		newLoginCommand(opts),
		newVersionCommand(),
	)

	return cmd
}

func newBootstrapCommand(root *rootOptions) *cobra.Command {
	inspectOpts := &bootstrapInspectOptions{}
	createOpts := &bootstrapCreateOptions{}

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Inspect arbitrary paths and seed work graph bootstrap state",
	}

	inspectCmd := &cobra.Command{
		Use:   "inspect",
		Short: "Assess whether one or more paths are work-graph-native",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/bootstrap/inspect", service.BootstrapInspectRequest{
				Paths: inspectOpts.paths,
			})
			if err != nil {
				return mapServiceError(err)
			}
			var assessment service.BootstrapAssessment
			if err := json.Unmarshal(data, &assessment); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderBootstrapAssessment(cmd, root.jsonOutput, &assessment)
		},
	}
	inspectCmd.Flags().StringArrayVar(&inspectOpts.paths, "path", nil, "directory or file path to inspect (repeatable)")
	_ = inspectCmd.MarkFlagRequired("path")

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a root work item from discovered code/docs entrypoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkCapability(core.CapWorkCreate); err != nil {
				return err
			}
			c := connectOrDie()
			data, err := c.doPost("/api/bootstrap/create", service.BootstrapCreateRequest{
				Paths:     createOpts.paths,
				Title:     createOpts.title,
				Objective: createOpts.objective,
				Kind:      createOpts.kind,
			})
			if err != nil {
				return mapServiceError(err)
			}
			var result service.BootstrapCreateResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderBootstrapCreateResult(cmd, root.jsonOutput, &result)
		},
	}
	createCmd.Flags().StringArrayVar(&createOpts.paths, "path", nil, "directory or file path to inspect (repeatable)")
	createCmd.Flags().StringVar(&createOpts.title, "title", "Bootstrap work graph", "root work title")
	createCmd.Flags().StringVar(&createOpts.objective, "objective", "", "root work objective")
	createCmd.Flags().StringVar(&createOpts.kind, "kind", "plan", "root work kind")
	_ = createCmd.MarkFlagRequired("path")

	cmd.AddCommand(inspectCmd, createCmd)
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

			c := connectOrDie()
			data, runErr := c.doPost("/api/job/run", service.RunRequest{
				Adapter:      opts.adapter,
				CWD:          opts.cwd,
				Prompt:       prompt,
				PromptSource: source,
				Label:        opts.label,
				Model:        opts.model,
				Profile:      opts.profile,
				ArtifactDir:  opts.artifactDir,
				SessionID:    opts.sessionID,
				WorkID:       opts.workID,
			})
			if runErr != nil {
				return mapServiceError(runErr)
			}

			var result service.RunResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderRunResult(cmd, root.jsonOutput, &result)
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
	cmd.Flags().StringVar(&opts.artifactDir, "artifact-dir", "", "override artifact directory")
	cmd.Flags().StringVar(&opts.sessionID, "session", "", "attach the run to an existing canonical session")
	cmd.Flags().StringVar(&opts.workID, "work", "", "attach the run to a work item")
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
			c := connectOrDie()
			jobID := args[0]

			var status *service.StatusResult
			if opts.wait {
				var err error
				status, err = waitStatusHTTP(c, jobID, opts.interval, opts.timeout)
				if err != nil {
					return err
				}
			} else {
				data, err := c.doGet("/api/job/"+jobID+"/status", nil)
				if err != nil {
					return mapServiceError(err)
				}
				var s service.StatusResult
				if err := json.Unmarshal(data, &s); err != nil {
					return fmt.Errorf("decoding response: %w", err)
				}
				status = &s
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
			c := connectOrDie()
			jobID := args[0]

			if follow {
				if raw {
					return followRawLogsHTTP(cmd, c, jobID, root.jsonOutput, limit)
				}
				return followEventsHTTP(cmd, c, jobID, root.jsonOutput, limit)
			}

			if raw {
				params := url.Values{"limit": {strconv.Itoa(limit)}}
				data, err := c.doGet("/api/job/"+jobID+"/logs-raw", params)
				if err != nil {
					return mapServiceError(err)
				}
				var logs []service.RawLogEntry
				if err := json.Unmarshal(data, &logs); err != nil {
					return fmt.Errorf("decoding response: %w", err)
				}
				return renderRawLogs(cmd, root.jsonOutput, false, logs)
			}

			params := url.Values{"limit": {strconv.Itoa(limit)}}
			data, err := c.doGet("/api/job/"+jobID+"/logs", params)
			if err != nil {
				return mapServiceError(err)
			}
			var events []core.EventRecord
			if err := json.Unmarshal(data, &events); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderEvents(cmd, root.jsonOutput, false, events)
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

			c := connectOrDie()
			data, sendErr := c.doPost("/api/job/send", service.SendRequest{
				SessionID:    opts.sessionID,
				Adapter:      opts.adapter,
				Prompt:       prompt,
				PromptSource: source,
				Model:        opts.model,
				Profile:      opts.profile,
				WorkID:       opts.workID,
			})
			if sendErr != nil {
				return mapServiceError(sendErr)
			}
			var result service.RunResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderRunResult(cmd, root.jsonOutput, &result)
		},
	}

	cmd.Flags().StringVar(&opts.sessionID, "session", "", "canonical session to continue")
	cmd.Flags().StringVar(&opts.adapter, "adapter", "", "optional adapter override when a session has multiple resumable links")
	cmd.Flags().StringVar(&opts.prompt, "prompt", "", "prompt text")
	cmd.Flags().StringVar(&opts.promptFile, "prompt-file", "", "path to prompt file")
	cmd.Flags().BoolVar(&opts.stdin, "stdin", false, "read prompt from stdin")
	cmd.Flags().StringVar(&opts.model, "model", "", "requested model override")
	cmd.Flags().StringVar(&opts.profile, "profile", "", "requested adapter profile")
	cmd.Flags().StringVar(&opts.workID, "work", "", "attach the continuation to a work item")
	_ = cmd.MarkFlagRequired("session")

	return cmd
}

func newCancelCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <job-id>",
		Short: "Cancel a running job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/job/"+args[0]+"/cancel", nil)
			if err != nil {
				return mapServiceError(err)
			}
			var job core.JobRecord
			if err := json.Unmarshal(data, &job); err != nil {
				return fmt.Errorf("decoding response: %w", err)
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
			c := connectOrDie()
			data, debriefErr := c.doPost("/api/debrief", service.DebriefRequest{
				SessionID:  opts.sessionID,
				Adapter:    opts.adapter,
				Model:      opts.model,
				Profile:    opts.profile,
				OutputPath: opts.output,
				Reason:     opts.reason,
			})
			if debriefErr != nil {
				return mapServiceError(debriefErr)
			}
			var result service.DebriefResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderDebriefResult(cmd, root.jsonOutput, &result)
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
			c := connectOrDie()
			data, err := c.doGet("/api/session/"+args[0], nil)
			if err != nil {
				return mapServiceError(err)
			}
			var result service.SessionResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderSession(cmd, root.jsonOutput, &result)
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
			c := connectOrDie()
			params := url.Values{}
			params.Set("limit", strconv.Itoa(limit))
			if adapter != "" {
				params.Set("adapter", adapter)
			}
			if state != "" {
				params.Set("state", state)
			}

			switch kind {
			case "jobs":
				if sessionID != "" {
					params.Set("session", sessionID)
				}
				data, err := c.doGet("/api/job/list", params)
				if err != nil {
					return mapServiceError(err)
				}
				var jobs []core.JobRecord
				if err := json.Unmarshal(data, &jobs); err != nil {
					return fmt.Errorf("decoding response: %w", err)
				}
				if root.jsonOutput {
					if jobs == nil {
						jobs = []core.JobRecord{}
					}
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
				if state != "" {
					params.Set("status", state)
					params.Del("state")
				}
				data, err := c.doGet("/api/session/list", params)
				if err != nil {
					return mapServiceError(err)
				}
				var sessions []core.SessionRecord
				if err := json.Unmarshal(data, &sessions); err != nil {
					return fmt.Errorf("decoding response: %w", err)
				}
				if root.jsonOutput {
					if sessions == nil {
						sessions = []core.SessionRecord{}
					}
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
	attachOpts := &artifactsAttachOptions{copy: true}

	cmd := &cobra.Command{
		Use:   "artifacts",
		Short: "Inspect persisted artifacts",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List artifacts for a job, session, or work item",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			params := url.Values{}
			if listOpts.jobID != "" {
				params.Set("job", listOpts.jobID)
			}
			if listOpts.sessionID != "" {
				params.Set("session", listOpts.sessionID)
			}
			if listOpts.workID != "" {
				params.Set("work", listOpts.workID)
			}
			if listOpts.kind != "" {
				params.Set("kind", listOpts.kind)
			}
			params.Set("limit", strconv.Itoa(listOpts.limit))
			data, err := c.doGet("/api/artifact/list", params)
			if err != nil {
				return mapServiceError(err)
			}
			var artifacts []core.ArtifactRecord
			if err := json.Unmarshal(data, &artifacts); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderArtifacts(cmd, root.jsonOutput, artifacts)
		},
	}
	attachCmd := &cobra.Command{
		Use:   "attach",
		Short: "Attach a file as a persisted artifact",
		RunE: func(cmd *cobra.Command, args []string) error {
			metadata, err := parseJSONObjectFlag(attachOpts.metadata)
			if err != nil {
				return exitf(2, "invalid --metadata JSON: %v", err)
			}
			c := connectOrDie()
			data, err := c.doPost("/api/artifact/attach", service.AttachArtifactRequest{
				JobID:     attachOpts.jobID,
				SessionID: attachOpts.sessionID,
				WorkID:    attachOpts.workID,
				Path:      attachOpts.path,
				Kind:      attachOpts.kind,
				Copy:      attachOpts.copy,
				Metadata:  metadata,
			})
			if err != nil {
				return mapServiceError(err)
			}
			var artifact core.ArtifactRecord
			if err := json.Unmarshal(data, &artifact); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			if root.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), artifact)
			}
			return writef(cmd.OutOrStdout(), "%s\t%s\t%s\n", artifact.ArtifactID, artifact.Kind, artifact.Path)
		},
	}
	showCmd := &cobra.Command{
		Use:   "show <artifact-id>",
		Short: "Show one artifact and its content",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doGet("/api/artifact/"+args[0], nil)
			if err != nil {
				return mapServiceError(err)
			}
			var result service.ArtifactResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderArtifact(cmd, root.jsonOutput, &result)
		},
	}
	cmd.AddCommand(listCmd, attachCmd, showCmd)

	listCmd.Flags().StringVar(&listOpts.jobID, "job", "", "list artifacts for a job")
	listCmd.Flags().StringVar(&listOpts.sessionID, "session", "", "list artifacts for a session")
	listCmd.Flags().StringVar(&listOpts.workID, "work", "", "list artifacts for a work item")
	listCmd.Flags().StringVar(&listOpts.kind, "kind", "", "filter by artifact kind")
	listCmd.Flags().IntVar(&listOpts.limit, "limit", 20, "maximum number of artifacts to list")

	attachCmd.Flags().StringVar(&attachOpts.jobID, "job", "", "attach artifact to a job")
	attachCmd.Flags().StringVar(&attachOpts.sessionID, "session", "", "attach artifact to a session")
	attachCmd.Flags().StringVar(&attachOpts.workID, "work", "", "attach artifact to a work item's current job/session")
	attachCmd.Flags().StringVar(&attachOpts.path, "path", "", "file path to attach")
	attachCmd.Flags().StringVar(&attachOpts.kind, "kind", "", "artifact kind override")
	attachCmd.Flags().BoolVar(&attachOpts.copy, "copy", true, "copy the file into fase state for durability")
	attachCmd.Flags().StringVar(&attachOpts.metadata, "metadata", "", "JSON object metadata")
	_ = attachCmd.MarkFlagRequired("path")

	return cmd
}

func newHistoryCommand(root *rootOptions) *cobra.Command {
	searchOpts := &historySearchOptions{}

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Search canonical local fase history",
	}

	searchCmd := &cobra.Command{
		Use:   "search",
		Short: "Search canonical jobs, turns, events, and artifacts",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			params := url.Values{}
			params.Set("query", searchOpts.query)
			if searchOpts.adapter != "" {
				params.Set("adapter", searchOpts.adapter)
			}
			if searchOpts.model != "" {
				params.Set("model", searchOpts.model)
			}
			if searchOpts.cwd != "" {
				params.Set("cwd", searchOpts.cwd)
			}
			if searchOpts.sessionID != "" {
				params.Set("session", searchOpts.sessionID)
			}
			if searchOpts.kinds != "" {
				params.Set("kinds", searchOpts.kinds)
			}
			params.Set("limit", strconv.Itoa(searchOpts.limit))
			params.Set("scan_limit", strconv.Itoa(searchOpts.scanLimit))
			data, err := c.doGet("/api/history/search", params)
			if err != nil {
				return mapServiceError(err)
			}
			var result service.HistorySearchResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
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

func newWorkCommand(root *rootOptions) *cobra.Command {
	createOpts := &workCreateOptions{}
	listOpts := &workListOptions{}
	updateOpts := &workUpdateOptions{}
	noteOpts := &workNoteOptions{}
	showOpts := &workShowOptions{limit: 50}
	readyOpts := &workReadyOptions{limit: 50}
	claimOpts := &workClaimOptions{lease: 15 * time.Minute, limit: 25}
	discoverOpts := &workDiscoverOptions{}
	proposalListOpts := &workProposalListOptions{}
	proposalCreateOpts := &workProposalCreateOptions{}
	attestOpts := &workAttestOptions{}
	approveOpts := &workApproveOptions{}
	checkOpts := &workCheckOptions{}
	hydrateOpts := &workHydrateOptions{mode: "standard"}
	promoteOpts := &workPromoteOptions{environment: "staging"}
	projectionOpts := &workProjectionOptions{format: "markdown"}

	cmd := &cobra.Command{
		Use:   "work",
		Short: "Inspect and mutate durable work graph state",
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a work item",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkCapability(core.CapWorkCreate); err != nil {
				return err
			}
			acceptance, err := parseJSONObjectFlag(createOpts.acceptance)
			if err != nil {
				return exitf(2, "invalid --acceptance JSON: %v", err)
			}
			requiredAttestations, err := parseRequiredAttestations(createOpts.requiredAttestations)
			if err != nil {
				return exitf(2, "invalid --required-attestations JSON: %v", err)
			}
			req := service.WorkCreateRequest{
				Title:                createOpts.title,
				Objective:            createOpts.objective,
				Kind:                 createOpts.kind,
				ParentWorkID:         createOpts.parent,
				LockState:            core.WorkLockState(createOpts.lockState),
				Priority:             createOpts.priority,
				Position:             createOpts.position,
				RequiredCapabilities: splitCSV(createOpts.requiredCapabilities),
				RequiredModelTraits:  splitCSV(createOpts.requiredModelTraits),
				PreferredAdapters:    splitCSV(createOpts.preferredAdapters),
				ForbiddenAdapters:    splitCSV(createOpts.forbiddenAdapters),
				PreferredModels:      splitCSV(createOpts.preferredModels),
				AvoidModels:          splitCSV(createOpts.avoidModels),
				RequiredAttestations: requiredAttestations,
				Acceptance:           acceptance,
				HeadCommitOID:        createOpts.headCommitOID,
				ConfigurationClass:   createOpts.configurationClass,
				BudgetClass:          createOpts.budgetClass,
			}
			c := connectOrDie()
			data, err := c.doPost("/api/work/create", req)
			if err != nil {
				return err
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}
	createCmd.Flags().StringVar(&createOpts.title, "title", "", "work title")
	createCmd.Flags().StringVar(&createOpts.objective, "objective", "", "work objective")
	createCmd.Flags().StringVar(&createOpts.kind, "kind", "task", "work kind")
	createCmd.Flags().StringVar(&createOpts.parent, "parent", "", "optional parent work id")
	createCmd.Flags().StringVar(&createOpts.lockState, "lock-state", string(core.WorkLockStateUnlocked), "initial lock state")
	createCmd.Flags().IntVar(&createOpts.priority, "priority", 0, "priority")
	createCmd.Flags().IntVar(&createOpts.position, "position", 0, "queue position (1 = front, 0 = auto-assign)")
	createCmd.Flags().StringVar(&createOpts.requiredCapabilities, "required-capabilities", "", "comma-separated required capabilities")
	createCmd.Flags().StringVar(&createOpts.requiredModelTraits, "required-model-traits", "", "comma-separated required model traits")
	createCmd.Flags().StringVar(&createOpts.preferredAdapters, "preferred-adapters", "", "comma-separated preferred adapters")
	createCmd.Flags().StringVar(&createOpts.forbiddenAdapters, "forbidden-adapters", "", "comma-separated forbidden adapters")
	createCmd.Flags().StringVar(&createOpts.preferredModels, "preferred-models", "", "comma-separated preferred model ids")
	createCmd.Flags().StringVar(&createOpts.avoidModels, "avoid-models", "", "comma-separated model ids to avoid")
	createCmd.Flags().StringVar(&createOpts.requiredAttestations, "required-attestations", "", "JSON array of required attestation policy slots")
	createCmd.Flags().StringVar(&createOpts.acceptance, "acceptance", "", "JSON object for acceptance criteria")
	createCmd.Flags().StringVar(&createOpts.headCommitOID, "head-commit", "", "Git commit oid currently associated with the work")
	createCmd.Flags().StringVar(&createOpts.configurationClass, "configuration-class", "", "configuration class for attestation and dispatch defaults")
	createCmd.Flags().StringVar(&createOpts.budgetClass, "budget-class", "", "budget class for policy and reporting defaults")
	_ = createCmd.MarkFlagRequired("title")
	_ = createCmd.MarkFlagRequired("objective")

	showCmd := &cobra.Command{
		Use:   "show <work-id>",
		Short: "Show one work item and its related state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doGet("/api/work/"+args[0], nil)
			if err != nil {
				return err
			}
			var result service.WorkShowResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			if showOpts.limit > 0 {
				if len(result.Children) > showOpts.limit {
					result.Children = result.Children[:showOpts.limit]
				}
				if len(result.Updates) > showOpts.limit {
					result.Updates = result.Updates[:showOpts.limit]
				}
				if len(result.Notes) > showOpts.limit {
					result.Notes = result.Notes[:showOpts.limit]
				}
				if len(result.Jobs) > showOpts.limit {
					result.Jobs = result.Jobs[:showOpts.limit]
				}
				if len(result.Proposals) > showOpts.limit {
					result.Proposals = result.Proposals[:showOpts.limit]
				}
				if len(result.Attestations) > showOpts.limit {
					result.Attestations = result.Attestations[:showOpts.limit]
				}
				if len(result.Approvals) > showOpts.limit {
					result.Approvals = result.Approvals[:showOpts.limit]
				}
				if len(result.Promotions) > showOpts.limit {
					result.Promotions = result.Promotions[:showOpts.limit]
				}
				if len(result.Artifacts) > showOpts.limit {
					result.Artifacts = result.Artifacts[:showOpts.limit]
				}
			}
			return renderWorkShow(cmd, root.jsonOutput, &result)
		},
	}
	showCmd.Flags().IntVar(&showOpts.limit, "limit", 50, "maximum related records per section")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List work items",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			params := url.Values{}
			if listOpts.limit > 0 {
				params.Set("limit", strconv.Itoa(listOpts.limit))
			}
			if listOpts.kind != "" {
				params.Set("kind", listOpts.kind)
			}
			if listOpts.executionState != "" {
				params.Set("state", listOpts.executionState)
			}
			if listOpts.approvalState != "" {
				params.Set("approval_state", listOpts.approvalState)
			}
			if listOpts.includeArchived {
				params.Set("include_archived", "1")
			}
			data, err := c.doGet("/api/work/list", params)
			if err != nil {
				return err
			}
			var items []core.WorkItemRecord
			if err := json.Unmarshal(data, &items); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItems(cmd, root.jsonOutput, items)
		},
	}
	listCmd.Flags().IntVar(&listOpts.limit, "limit", 50, "maximum number of work items")
	listCmd.Flags().StringVar(&listOpts.kind, "kind", "", "filter by work kind")
	listCmd.Flags().StringVar(&listOpts.executionState, "execution-state", "", "filter by execution state")
	listCmd.Flags().StringVar(&listOpts.approvalState, "approval-state", "", "filter by approval state")
	listCmd.Flags().BoolVar(&listOpts.includeArchived, "include-archived", false, "include archived work items")

	readyCmd := &cobra.Command{
		Use:   "ready",
		Short: "List work items that are currently ready",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			params := url.Values{}
			if readyOpts.limit > 0 {
				params.Set("limit", strconv.Itoa(readyOpts.limit))
			}
			if readyOpts.includeArchived {
				params.Set("include_archived", "1")
			}
			data, err := c.doGet("/api/work/ready", params)
			if err != nil {
				return err
			}
			var items []core.WorkItemRecord
			if err := json.Unmarshal(data, &items); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItems(cmd, root.jsonOutput, items)
		},
	}
	readyCmd.Flags().IntVar(&readyOpts.limit, "limit", 50, "maximum number of work items")
	readyCmd.Flags().BoolVar(&readyOpts.includeArchived, "include-archived", false, "include archived work items")

	claimCmd := &cobra.Command{
		Use:   "claim <work-id>",
		Short: "Claim a work item for a lease interval",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/claim", service.WorkClaimRequest{
				WorkID:        args[0],
				Claimant:      claimOpts.claimant,
				LeaseDuration: claimOpts.lease,
			})
			if err != nil {
				return mapServiceError(err)
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}
	claimCmd.Flags().StringVar(&claimOpts.claimant, "claimant", "cli", "worker or runtime claiming the work")
	claimCmd.Flags().DurationVar(&claimOpts.lease, "lease", 15*time.Minute, "lease duration")

	claimNextCmd := &cobra.Command{
		Use:   "claim-next",
		Short: "Claim the next compatible ready work item",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/work/claim-next", service.WorkClaimNextRequest{
				Claimant:      claimOpts.claimant,
				LeaseDuration: claimOpts.lease,
				Limit:         claimOpts.limit,
			})
			if err != nil {
				return mapServiceError(err)
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}
	claimNextCmd.Flags().StringVar(&claimOpts.claimant, "claimant", "cli", "worker or runtime claiming the work")
	claimNextCmd.Flags().DurationVar(&claimOpts.lease, "lease", 15*time.Minute, "lease duration")
	claimNextCmd.Flags().IntVar(&claimOpts.limit, "limit", 25, "maximum compatible ready candidates to inspect")

	releaseCmd := &cobra.Command{
		Use:   "release <work-id>",
		Short: "Release a work claim",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/release", service.WorkReleaseRequest{
				WorkID:    args[0],
				Claimant:  claimOpts.claimant,
				CreatedBy: "cli",
				Force:     claimOpts.force,
			})
			if err != nil {
				return mapServiceError(err)
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}
	releaseCmd.Flags().StringVar(&claimOpts.claimant, "claimant", "cli", "worker or runtime releasing the claim")
	releaseCmd.Flags().BoolVar(&claimOpts.force, "force", false, "force release even if claimed by another worker (requires expired lease)")

	renewLeaseCmd := &cobra.Command{
		Use:   "renew-lease <work-id>",
		Short: "Extend the lease on a claimed work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			data, err := c.doPost("/api/work/"+args[0]+"/renew-lease", service.WorkRenewLeaseRequest{
				Claimant:      claimOpts.claimant,
				LeaseDuration: claimOpts.lease,
			})
			if err != nil {
				return err
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}
	renewLeaseCmd.Flags().StringVar(&claimOpts.claimant, "claimant", "cli", "worker or runtime renewing the lease")
	renewLeaseCmd.Flags().DurationVar(&claimOpts.lease, "lease", 15*time.Minute, "lease duration")

	updateCmd := &cobra.Command{
		Use:   "update <work-id>",
		Short: "Append a structured work update and mutate current work state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkCapability(core.CapWorkUpdate); err != nil {
				return err
			}
			if updateOpts.force {
				if err := checkCapability(core.CapWorkForceDone); err != nil {
					return err
				}
			}
			c, err := connectServe()
			if err != nil {
				return err
			}
			req := service.WorkUpdateRequest{
				ExecutionState: core.WorkExecutionState(updateOpts.executionState),
				ApprovalState:  core.WorkApprovalState(updateOpts.approvalState),
				LockState:      core.WorkLockState(updateOpts.lockState),
				Phase:          updateOpts.phase,
				Message:        updateOpts.message,
				JobID:          updateOpts.jobID,
				SessionID:      updateOpts.sessionID,
				ArtifactID:     updateOpts.artifactID,
				ForceDone:      updateOpts.force,
				CreatedBy:      "cli",
			}
			data, err := c.doPost("/api/work/"+args[0]+"/update", req)
			if err != nil {
				return err
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}
	updateCmd.Flags().StringVar(&updateOpts.executionState, "execution-state", "", "new execution state")
	updateCmd.Flags().StringVar(&updateOpts.approvalState, "approval-state", "", "new approval state")
	updateCmd.Flags().StringVar(&updateOpts.lockState, "lock-state", "", "new lock state")
	updateCmd.Flags().StringVar(&updateOpts.phase, "phase", "", "phase label")
	updateCmd.Flags().StringVar(&updateOpts.message, "message", "", "update message")
	updateCmd.Flags().StringVar(&updateOpts.jobID, "job", "", "related job id")
	updateCmd.Flags().StringVar(&updateOpts.sessionID, "session", "", "related session id")
	updateCmd.Flags().StringVar(&updateOpts.artifactID, "artifact", "", "related artifact id")
	updateCmd.Flags().BoolVar(&updateOpts.force, "force", false, "bypass attestation guard (requires work:force-done capability; overseer role only)")

	blockCmd := &cobra.Command{
		Use:   "block <work-id>",
		Short: "Mark work blocked and append an update",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/block", map[string]string{"message": updateOpts.message})
			if err != nil {
				return err
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}
	blockCmd.Flags().StringVar(&updateOpts.message, "message", "", "blocker message")

	archiveCmd := &cobra.Command{
		Use:   "archive <work-id>",
		Short: "Archive work and append an update",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/archive", map[string]string{"message": updateOpts.message})
			if err != nil {
				return err
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}
	archiveCmd.Flags().StringVar(&updateOpts.message, "message", "", "archive message")

	retryCmd := &cobra.Command{
		Use:   "retry <work-id>",
		Short: "Reset a failed/cancelled work item to ready",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/retry", nil)
			if err != nil {
				return err
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}

	notesCmd := &cobra.Command{
		Use:   "notes <work-id>",
		Short: "List notes for a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doGet("/api/work/"+args[0]+"/notes", nil)
			if err != nil {
				return err
			}
			var notes []core.WorkNoteRecord
			if err := json.Unmarshal(data, &notes); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkNotes(cmd, root.jsonOutput, notes)
		},
	}

	noteAddCmd := &cobra.Command{
		Use:   "note-add <work-id>",
		Short: "Append a note to a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkCapability(core.CapWorkNoteAdd); err != nil {
				return err
			}
			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/note-add", service.WorkNoteRequest{
				WorkID:    args[0],
				NoteType:  noteOpts.noteType,
				Body:      noteOpts.text,
				CreatedBy: "cli",
			})
			if err != nil {
				return err
			}
			var note core.WorkNoteRecord
			if err := json.Unmarshal(data, &note); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkNote(cmd, root.jsonOutput, &note)
		},
	}
	noteAddCmd.Flags().StringVar(&noteOpts.noteType, "type", "", "note type")
	noteAddCmd.Flags().StringVar(&noteOpts.text, "text", "", "note body")
	_ = noteAddCmd.MarkFlagRequired("text")

	privateNoteCmd := &cobra.Command{
		Use:   "private-note <work-id>",
		Short: "Add a private note (stored in gitignored DB, never committed)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			data, err := c.doPost("/api/work/"+args[0]+"/private-note", map[string]string{"note_type": noteOpts.noteType, "body": noteOpts.text})
			if err != nil {
				return err
			}
			var note core.WorkNoteRecord
			if err := json.Unmarshal(data, &note); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkNote(cmd, root.jsonOutput, &note)
		},
	}
	privateNoteCmd.Flags().StringVar(&noteOpts.noteType, "type", "private", "note type")
	privateNoteCmd.Flags().StringVar(&noteOpts.text, "text", "", "note body")
	_ = privateNoteCmd.MarkFlagRequired("text")

	docSetCmd := &cobra.Command{
		Use:   "doc-set [work-id]",
		Short: "Store doc content, auto-creating a work item if needed",
		Long: `Associates a document (from file or inline) with a work item.
If no work-id is given, auto-creates a work item from the doc content.
This guarantees every doc has a corresponding work item.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkCapability(core.CapWorkCreate); err != nil {
				return err
			}
			c, err := connectServe()
			if err != nil {
				return err
			}

			docPath, _ := cmd.Flags().GetString("path")
			docTitle, _ := cmd.Flags().GetString("title")
			docFile, _ := cmd.Flags().GetString("file")
			docFormat, _ := cmd.Flags().GetString("format")

			var body string
			if docFile != "" {
				fileData, err := os.ReadFile(docFile)
				if err != nil {
					return fmt.Errorf("read file: %w", err)
				}
				body = string(fileData)
				if docPath == "" {
					docPath = docFile
				}
			} else {
				bodyFlag, _ := cmd.Flags().GetString("body")
				body = bodyFlag
			}

			workID := ""
			if len(args) > 0 {
				workID = args[0]
			}

			data, err := c.doPost("/api/work/"+workID+"/doc-set", map[string]string{
				"path": docPath, "title": docTitle, "body": body, "format": docFormat,
			})
			if err != nil {
				return err
			}
			if root.jsonOutput {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			var resp struct {
				Doc struct {
					DocID string `json:"doc_id"`
				} `json:"doc"`
				WorkID string `json:"work_id"`
			}
			if err := json.Unmarshal(data, &resp); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			if workID == "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "doc %s stored (%d bytes, path=%s) → work item %s (auto-created)\n", resp.Doc.DocID, len(body), docPath, resp.WorkID)
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "doc %s stored (%d bytes, path=%s)\n", resp.Doc.DocID, len(body), docPath)
			}
			return nil
		},
	}
	docSetCmd.Flags().String("path", "", "document path (e.g., docs/adr-0014.md)")
	docSetCmd.Flags().String("title", "", "document title")
	docSetCmd.Flags().String("file", "", "read body from file")
	docSetCmd.Flags().String("body", "", "document body (inline)")
	docSetCmd.Flags().String("format", "markdown", "document format")

	childrenCmd := &cobra.Command{
		Use:   "children <work-id>",
		Short: "List child work items",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			data, err := c.doGet("/api/work/"+args[0]+"/children", nil)
			if err != nil {
				return err
			}
			var items []core.WorkItemRecord
			if err := json.Unmarshal(data, &items); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItems(cmd, root.jsonOutput, items)
		},
	}

	discoverCmd := &cobra.Command{
		Use:   "discover <work-id>",
		Short: "Create a discovery proposal from a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkCapability(core.CapWorkCreate); err != nil {
				return err
			}
			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/discover", map[string]string{
				"title": discoverOpts.title, "objective": discoverOpts.objective,
				"kind": discoverOpts.kind, "rationale": discoverOpts.rationale,
			})
			if err != nil {
				return err
			}
			var proposal core.WorkProposalRecord
			if err := json.Unmarshal(data, &proposal); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkProposal(cmd, root.jsonOutput, &proposal, nil)
		},
	}
	discoverCmd.Flags().StringVar(&discoverOpts.title, "title", "", "discovered work title")
	discoverCmd.Flags().StringVar(&discoverOpts.objective, "objective", "", "discovered work objective")
	discoverCmd.Flags().StringVar(&discoverOpts.kind, "kind", "task", "discovered work kind")
	discoverCmd.Flags().StringVar(&discoverOpts.rationale, "rationale", "", "why this discovered work matters")
	_ = discoverCmd.MarkFlagRequired("title")
	_ = discoverCmd.MarkFlagRequired("objective")

	attestCmd := &cobra.Command{
		Use:   "attest <work-id>",
		Short: "Record an attestation for a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkCapability(core.CapWorkAttest); err != nil {
				return err
			}
			metadata, err := parseJSONObjectFlag(attestOpts.metadata)
			if err != nil {
				return exitf(2, "invalid --metadata JSON: %v", err)
			}

			// Phase 3: load agent credential for attestation signing.
			var signerPubkey string
			cred, agentPrivKey, credErr := loadAgentCredential()
			if credErr == nil && cred != nil {
				signerPubkey = cred.Token.AgentPubkey
			}

			req := service.WorkAttestRequest{
				WorkID:                  args[0],
				Result:                  attestOpts.result,
				Summary:                 attestOpts.summary,
				JobID:                   attestOpts.jobID,
				SessionID:               attestOpts.sessionID,
				ArtifactID:              attestOpts.artifactID,
				Method:                  attestOpts.method,
				VerifierKind:            attestOpts.verifierKind,
				VerifierIdentity:        attestOpts.verifierIdentity,
				Confidence:              attestOpts.confidence,
				Blocking:                attestOpts.blocking,
				SupersedesAttestationID: attestOpts.supersedesAttestationID,
				Metadata:                metadata,
				CreatedBy:               "cli",
				Nonce:                   strings.TrimSpace(attestOpts.nonce),
				SignerPubkey:            signerPubkey,
			}

			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/attest", req)
			if err != nil {
				return err
			}
			var resp struct {
				Attestation *core.AttestationRecord `json:"attestation"`
				Work        *core.WorkItemRecord    `json:"work"`
			}
			if err := json.Unmarshal(data, &resp); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			// Phase 3: sign the attestation record with the agent's private key.
			if agentPrivKey != nil && resp.Attestation != nil {
				signable := resp.Attestation.Signable()
				sig, signErr := core.SignJSON(signable, agentPrivKey)
				if signErr == nil {
					_, _ = c.doPost("/api/attestation/"+resp.Attestation.AttestationID+"/sign", map[string]string{"signature": sig})
					resp.Attestation.Signature = sig
				}
			}
			return renderAttestation(cmd, root.jsonOutput, resp.Attestation, resp.Work)
		},
	}
	attestCmd.Flags().StringVar(&attestOpts.result, "result", "", "attestation result: passed, failed, blocked, inconclusive")
	attestCmd.Flags().StringVar(&attestOpts.summary, "summary", "", "attestation summary")
	attestCmd.Flags().StringVar(&attestOpts.jobID, "job", "", "related job id")
	attestCmd.Flags().StringVar(&attestOpts.sessionID, "session", "", "related session id")
	attestCmd.Flags().StringVar(&attestOpts.artifactID, "artifact", "", "related artifact id")
	attestCmd.Flags().StringVar(&attestOpts.method, "method", "", "attestation method, such as test or review")
	attestCmd.Flags().StringVar(&attestOpts.verifierKind, "verifier-kind", "", "verifier kind, such as deterministic or code_review")
	attestCmd.Flags().StringVar(&attestOpts.verifierIdentity, "verifier-identity", "", "verifier identity, such as a model or script name")
	attestCmd.Flags().Float64Var(&attestOpts.confidence, "confidence", 0, "confidence score from 0 to 1")
	attestCmd.Flags().BoolVar(&attestOpts.blocking, "blocking", false, "mark this attestation as blocking evidence")
	attestCmd.Flags().StringVar(&attestOpts.supersedesAttestationID, "supersedes", "", "prior attestation id this record supersedes")
	attestCmd.Flags().StringVar(&attestOpts.metadata, "metadata", "", "JSON object with attestation metadata")
	attestCmd.Flags().StringVar(&attestOpts.nonce, "nonce", "", "attestation nonce (generated post-job-completion, required for automated attestation)")
	_ = attestCmd.MarkFlagRequired("result")

	verifyCmd := &cobra.Command{
		Use:   "verify <work-id>",
		Short: "Verify the recorded work graph and artifact chain for a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			if dryRun {
				report := previewCapabilities()
				return writeJSON(cmd.OutOrStdout(), report)
			}
			c, err := connectServe()
			if err != nil {
				return err
			}
			data, err := c.doPost("/api/work/"+args[0]+"/verify", nil)
			if err != nil {
				return err
			}
			var result service.WorkVerifyResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderVerification(cmd, root.jsonOutput, &result)
		},
	}

	verifyCmd.Flags().Bool("dry-run", false, "show capability token preview without verifying a work item")

	lockCmd := &cobra.Command{
		Use:   "lock <work-id>",
		Short: "Apply a human lock to a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/lock", nil)
			if err != nil {
				return err
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}

	unlockCmd := &cobra.Command{
		Use:   "unlock <work-id>",
		Short: "Remove a human lock from a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/unlock", nil)
			if err != nil {
				return err
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}

	approveCmd := &cobra.Command{
		Use:   "approve <work-id>",
		Short: "Approve a work item after required attestations pass",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkCapability(core.CapWorkApprove); err != nil {
				return err
			}
			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/approve", map[string]string{"message": approveOpts.message})
			if err != nil {
				return err
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}
	approveCmd.Flags().StringVar(&approveOpts.message, "message", "", "approval note")

	rejectCmd := &cobra.Command{
		Use:   "reject <work-id>",
		Short: "Reject a work item during approval",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkCapability(core.CapWorkReject); err != nil {
				return err
			}
			c, err := connectServe()
			if err != nil {
				return err
			}
			data, err := c.doPost("/api/work/"+args[0]+"/reject", map[string]string{"message": approveOpts.message})
			if err != nil {
				return err
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}
	rejectCmd.Flags().StringVar(&approveOpts.message, "message", "", "rejection note")

	promoteCmd := &cobra.Command{
		Use:   "promote <work-id>",
		Short: "Record a promotion event for approved work",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/promote", service.WorkPromoteRequest{
				WorkID:      args[0],
				Environment: promoteOpts.environment,
				TargetRef:   promoteOpts.targetRef,
				Message:     promoteOpts.message,
				CreatedBy:   "cli",
			})
			if err != nil {
				return err
			}
			var resp struct {
				Promotion *core.PromotionRecord `json:"promotion"`
				Work      *core.WorkItemRecord  `json:"work"`
			}
			if err := json.Unmarshal(data, &resp); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderPromotion(cmd, root.jsonOutput, resp.Promotion, resp.Work)
		},
	}
	promoteCmd.Flags().StringVar(&promoteOpts.environment, "environment", "staging", "promotion environment")
	promoteCmd.Flags().StringVar(&promoteOpts.targetRef, "ref", "", "target Git ref or tag")
	promoteCmd.Flags().StringVar(&promoteOpts.message, "message", "", "promotion note")

	proposalCmd := &cobra.Command{
		Use:   "proposal",
		Short: "Inspect and review work proposals",
	}

	proposalCreateCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a work proposal",
		RunE: func(cmd *cobra.Command, args []string) error {
			patch, err := parseJSONObjectFlag(proposalCreateOpts.patch)
			if err != nil {
				return exitf(2, "invalid --patch JSON: %v", err)
			}
			c := connectOrDie()
			data, err := c.doPost("/api/proposal/create", service.WorkProposalCreateRequest{
				ProposalType: proposalCreateOpts.proposalType,
				TargetWorkID: proposalCreateOpts.target,
				SourceWorkID: proposalCreateOpts.source,
				Rationale:    proposalCreateOpts.rationale,
				Patch:        patch,
				CreatedBy:    "cli",
			})
			if err != nil {
				return err
			}
			var proposal core.WorkProposalRecord
			if err := json.Unmarshal(data, &proposal); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkProposal(cmd, root.jsonOutput, &proposal, nil)
		},
	}
	proposalCreateCmd.Flags().StringVar(&proposalCreateOpts.proposalType, "type", "", "proposal type")
	proposalCreateCmd.Flags().StringVar(&proposalCreateOpts.target, "target", "", "target work id")
	proposalCreateCmd.Flags().StringVar(&proposalCreateOpts.source, "source", "", "source work id")
	proposalCreateCmd.Flags().StringVar(&proposalCreateOpts.rationale, "rationale", "", "proposal rationale")
	proposalCreateCmd.Flags().StringVar(&proposalCreateOpts.patch, "patch", "", "JSON object describing the proposed change")
	_ = proposalCreateCmd.MarkFlagRequired("type")

	projectionCmd := &cobra.Command{
		Use:   "projection",
		Short: "Render deterministic text projections from work state",
	}

	hydrateCmd := &cobra.Command{
		Use:   "hydrate <work-id>",
		Short: "Compile a deterministic worker briefing for a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			params := url.Values{"mode": []string{hydrateOpts.mode}}
			data, err := c.doGet("/api/work/"+args[0]+"/hydrate", params)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	hydrateCmd.Flags().StringVar(&hydrateOpts.mode, "mode", "standard", "hydration mode: thin, standard, or deep")
	hydrateCmd.Flags().BoolVar(&hydrateOpts.debrief, "debrief", false, "request debrief hydration mode (not yet implemented)")

	proposalListCmd := &cobra.Command{
		Use:   "list",
		Short: "List work proposals",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			params := url.Values{}
			if proposalListOpts.limit > 0 {
				params.Set("limit", strconv.Itoa(proposalListOpts.limit))
			}
			if proposalListOpts.state != "" {
				params.Set("state", proposalListOpts.state)
			}
			if proposalListOpts.target != "" {
				params.Set("target", proposalListOpts.target)
			}
			if proposalListOpts.source != "" {
				params.Set("source", proposalListOpts.source)
			}
			data, err := c.doGet("/api/proposal/list", params)
			if err != nil {
				return err
			}
			var proposals []core.WorkProposalRecord
			if err := json.Unmarshal(data, &proposals); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkProposals(cmd, root.jsonOutput, proposals)
		},
	}
	proposalListCmd.Flags().IntVar(&proposalListOpts.limit, "limit", 50, "maximum number of proposals")
	proposalListCmd.Flags().StringVar(&proposalListOpts.state, "state", "", "filter by proposal state")
	proposalListCmd.Flags().StringVar(&proposalListOpts.target, "target", "", "filter by target work id")
	proposalListCmd.Flags().StringVar(&proposalListOpts.source, "source", "", "filter by source work id")

	proposalShowCmd := &cobra.Command{
		Use:   "show <proposal-id>",
		Short: "Show one work proposal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			data, err := c.doGet("/api/proposal/"+args[0], nil)
			if err != nil {
				return err
			}
			var proposal core.WorkProposalRecord
			if err := json.Unmarshal(data, &proposal); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkProposal(cmd, root.jsonOutput, &proposal, nil)
		},
	}

	proposalAcceptCmd := &cobra.Command{
		Use:   "accept <proposal-id>",
		Short: "Accept a work proposal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/proposal/"+args[0]+"/accept", nil)
			if err != nil {
				return err
			}
			var resp struct {
				Proposal *core.WorkProposalRecord `json:"proposal"`
				Created  *core.WorkItemRecord     `json:"created"`
			}
			if err := json.Unmarshal(data, &resp); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkProposal(cmd, root.jsonOutput, resp.Proposal, resp.Created)
		},
	}

	proposalRejectCmd := &cobra.Command{
		Use:   "reject <proposal-id>",
		Short: "Reject a work proposal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			data, err := c.doPost("/api/proposal/"+args[0]+"/reject", nil)
			if err != nil {
				return err
			}
			var proposal core.WorkProposalRecord
			if err := json.Unmarshal(data, &proposal); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkProposal(cmd, root.jsonOutput, &proposal, nil)
		},
	}

	proposalCmd.AddCommand(proposalCreateCmd, proposalListCmd, proposalShowCmd, proposalAcceptCmd, proposalRejectCmd)
	projectionChecklistCmd := &cobra.Command{
		Use:   "checklist <work-id>",
		Short: "Render a checklist projection for a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doGet("/api/work/"+args[0], nil)
			if err != nil {
				return err
			}
			var result service.WorkShowResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkProjection(cmd, root.jsonOutput, "checklist", projectionOpts.format, &result)
		},
	}
	projectionChecklistCmd.Flags().StringVar(&projectionOpts.format, "format", "markdown", "projection format")

	projectionStatusCmd := &cobra.Command{
		Use:   "status <work-id>",
		Short: "Render a status projection for a work item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doGet("/api/work/"+args[0], nil)
			if err != nil {
				return err
			}
			var result service.WorkShowResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkProjection(cmd, root.jsonOutput, "status", projectionOpts.format, &result)
		},
	}
	projectionStatusCmd.Flags().StringVar(&projectionOpts.format, "format", "markdown", "projection format")

	projectionCmd.AddCommand(projectionChecklistCmd, projectionStatusCmd)

	// --- edge subcommands ---
	edgeCmd := &cobra.Command{
		Use:   "edge",
		Short: "Manage edges in the work DAG",
	}

	var edgeType string
	edgeAddCmd := &cobra.Command{
		Use:   "add <from-work-id> <to-work-id>",
		Short: "Add a blocking edge (from blocks to)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkCapability(core.CapWorkEdgeAdd); err != nil {
				return err
			}
			c, err := connectServe()
			if err != nil {
				return err
			}
			data, err := c.doPost("/api/work/edges/add", map[string]string{
				"from": args[0], "to": args[1], "edge_type": edgeType,
			})
			if err != nil {
				return err
			}
			if root.jsonOutput {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			var edge core.WorkEdgeRecord
			if err := json.Unmarshal(data, &edge); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s → %s\n", edge.EdgeID, edge.EdgeType, edge.FromWorkID, edge.ToWorkID)
			return nil
		},
	}
	edgeAddCmd.Flags().StringVar(&edgeType, "type", "blocks", "edge type (blocks, parent_of, supersedes)")

	edgeRmCmd := &cobra.Command{
		Use:   "rm <from-work-id> <to-work-id>",
		Short: "Remove an edge between two work items",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			_, err = c.doPost("/api/work/edges/rm", map[string]string{
				"from": args[0], "to": args[1],
			})
			if err != nil {
				return err
			}
			if !root.jsonOutput {
				fmt.Fprintf(cmd.OutOrStdout(), "removed edges from %s to %s\n", args[0], args[1])
			}
			return nil
		},
	}

	edgeLsCmd := &cobra.Command{
		Use:   "ls [work-id]",
		Short: "List edges, optionally filtered by work item",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := connectServe()
			if err != nil {
				return err
			}
			params := url.Values{}
			if len(args) == 1 {
				params.Set("work_id", args[0])
			}
			data, err := c.doGet("/api/work/edges", params)
			if err != nil {
				return err
			}
			if root.jsonOutput {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			var edges []core.WorkEdgeRecord
			if err := json.Unmarshal(data, &edges); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			for _, e := range edges {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s → %s\n", e.EdgeID, e.EdgeType, e.FromWorkID, e.ToWorkID)
			}
			if len(edges) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no edges)")
			}
			return nil
		},
	}

	edgeCmd.AddCommand(edgeAddCmd, edgeRmCmd, edgeLsCmd)

	checkCmd := &cobra.Command{
		Use:   "check <work-id>",
		Short: "Submit a check record and transition work state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := service.WorkCheckRequest{
				WorkID:       args[0],
				Result:       checkOpts.result,
				CheckerModel: checkOpts.checkerModel,
				WorkerModel:  checkOpts.workerModel,
				CreatedBy:    "cli",
				Report: core.CheckReport{
					BuildOK:      checkOpts.buildOK,
					TestsPassed:  checkOpts.testsPassed,
					TestsFailed:  checkOpts.testsFailed,
					TestOutput:   checkOpts.testOutput,
					DiffStat:     checkOpts.diffStat,
					CheckerNotes: checkOpts.checkerNotes,
					Screenshots:  splitCSV(checkOpts.screenshots),
					Videos:       splitCSV(checkOpts.videos),
				},
			}
			c := connectOrDie()
			data, err := c.doPost("/api/work/"+args[0]+"/check", req)
			if err != nil {
				return err
			}
			var result service.WorkCheckResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			if root.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "check %s: %s\n", result.CheckRecord.CheckID, result.CheckRecord.Result)
			fmt.Fprintf(cmd.OutOrStdout(), "work %s → %s\n", result.Work.WorkID, result.Work.ExecutionState)
			return nil
		},
	}
	checkCmd.Flags().StringVar(&checkOpts.result, "result", "", "check result: pass or fail")
	checkCmd.Flags().StringVar(&checkOpts.checkerModel, "checker-model", "", "model that ran the check")
	checkCmd.Flags().StringVar(&checkOpts.workerModel, "worker-model", "", "model that did the work")
	checkCmd.Flags().BoolVar(&checkOpts.buildOK, "build-ok", false, "whether the build succeeded")
	checkCmd.Flags().IntVar(&checkOpts.testsPassed, "tests-passed", 0, "number of tests that passed")
	checkCmd.Flags().IntVar(&checkOpts.testsFailed, "tests-failed", 0, "number of tests that failed")
	checkCmd.Flags().StringVar(&checkOpts.testOutput, "test-output", "", "test output (truncated to 50KB)")
	checkCmd.Flags().StringVar(&checkOpts.diffStat, "diff-stat", "", "git diff --stat output")
	checkCmd.Flags().StringVar(&checkOpts.checkerNotes, "notes", "", "checker's free-form observations")
	checkCmd.Flags().StringVar(&checkOpts.screenshots, "screenshots", "", "comma-separated screenshot paths")
	checkCmd.Flags().StringVar(&checkOpts.videos, "videos", "", "comma-separated video paths")
	_ = checkCmd.MarkFlagRequired("result")

	cmd.AddCommand(createCmd, showCmd, listCmd, readyCmd, claimCmd, claimNextCmd, releaseCmd, renewLeaseCmd, updateCmd, blockCmd, archiveCmd, retryCmd, lockCmd, unlockCmd, approveCmd, rejectCmd, promoteCmd, notesCmd, noteAddCmd, privateNoteCmd, docSetCmd, childrenCmd, discoverCmd, attestCmd, verifyCmd, hydrateCmd, proposalCmd, projectionCmd, edgeCmd, checkCmd)
	return cmd
}

func newInternalRunJobCommand(root *rootOptions) *cobra.Command {
	opts := &internalRunJobOptions{}

	cmd := &cobra.Command{
		Use:    "__run-job",
		Short:  "Internal background worker entrypoint",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load .env: check serve.json for explicit path, fall back to cwd/.env
			if info, err := loadServeInfo(); err == nil {
				if envPath, ok := info.EnvFile(); ok {
					loadDotEnv(envPath)
				} else {
					loadDotEnv()
				}
			} else {
				loadDotEnv()
			}

			c, err := connectServe()
			if err != nil {
				return fmt.Errorf("__run-job requires fase serve: %w", err)
			}
			_, err = c.doPost("/api/internal/run-job", map[string]string{
				"job_id":  opts.jobID,
				"turn_id": opts.turnID,
			})
			return err
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
			c := connectOrDie()
			data, err := c.doGet("/api/adapters", nil)
			if err != nil {
				return mapServiceError(err)
			}
			var catalog []adapters.Diagnosis
			if err := json.Unmarshal(data, &catalog); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
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
			c := connectOrDie()
			params := url.Values{}
			if opts.adapter != "" {
				params.Set("adapter", opts.adapter)
			}
			data, err := c.doGet("/api/runtime", params)
			if err != nil {
				return mapServiceError(err)
			}
			var result service.RuntimeResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderRuntime(cmd, root.jsonOutput, &result)
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
			c := connectOrDie()
			data, err := c.doPost("/api/catalog/sync", nil)
			if err != nil {
				return mapServiceError(err)
			}
			var result service.CatalogResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderCatalog(cmd, root.jsonOutput, &result)
		},
	}

	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Show the latest discovered provider/model catalog",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doGet("/api/catalog/show", nil)
			if err != nil {
				return mapServiceError(err)
			}
			var result service.CatalogResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderCatalog(cmd, root.jsonOutput, &result)
		},
	}

	probeCmd := &cobra.Command{
		Use:   "probe",
		Short: "Run short entitlement probes against catalog entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/catalog/probe", service.ProbeCatalogRequest{
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
			var result service.CatalogResult
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderCatalog(cmd, root.jsonOutput, &result)
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
				c := connectOrDie()
				data, err := c.doPost("/api/transfer/export", service.TransferExportRequest{
					JobID:      exportOpts.jobID,
					SessionID:  exportOpts.sessionID,
					OutputPath: exportOpts.output,
					Reason:     exportOpts.reason,
					Mode:       exportOpts.mode,
				})
				if err != nil {
					return mapServiceError(err)
				}
				var result service.TransferExportResult
				if err := json.Unmarshal(data, &result); err != nil {
					return fmt.Errorf("decoding response: %w", err)
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
				c := connectOrDie()
				data, err := c.doPost("/api/transfer/run", service.TransferRunRequest{
					TransferRef: runOpts.transfer,
					Adapter:     runOpts.adapter,
					CWD:         runOpts.cwd,
					Model:       runOpts.model,
					Profile:     runOpts.profile,
					Label:       runOpts.label,
				})
				if err != nil {
					return mapServiceError(err)
				}
				var result service.RunResult
				if err := json.Unmarshal(data, &result); err != nil {
					return fmt.Errorf("decoding response: %w", err)
				}
				return renderRunResult(cmd, root.jsonOutput, &result)
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

func newInboxCommand(root *rootOptions) *cobra.Command {
	var kind string
	var objective string
	var priority int

	cmd := &cobra.Command{
		Use:   "inbox [title...]",
		Short: "Quick-add an idea to the work graph (shorthand for work create --kind idea)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			title := strings.Join(args, " ")
			obj := objective
			if obj == "" {
				obj = title
			}
			c := connectOrDie()
			data, err := c.doPost("/api/work/create", service.WorkCreateRequest{
				Title:     title,
				Objective: obj,
				Kind:      kind,
				Priority:  priority,
			})
			if err != nil {
				return mapServiceError(err)
			}
			var work core.WorkItemRecord
			if err := json.Unmarshal(data, &work); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			return renderWorkItem(cmd, root.jsonOutput, &work)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "idea", "work kind (default: idea)")
	cmd.Flags().StringVar(&objective, "objective", "", "work objective (defaults to title)")
	cmd.Flags().IntVar(&priority, "priority", 3, "priority (default: 3)")
	return cmd
}

func newReconcileCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "reconcile",
		Short: "Release orphaned work items with expired leases",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doPost("/api/reconcile", nil)
			if err != nil {
				return mapServiceError(err)
			}
			var result struct {
				ReconciledWorkIDs []string `json:"reconciled_work_ids"`
				Count             int      `json:"count"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				return fmt.Errorf("decoding response: %w", err)
			}
			if root.jsonOutput {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			if result.Count == 0 {
				cmd.Println("No orphaned work items found.")
				return nil
			}
			cmd.Printf("Reconciled %d orphaned work item(s):\n", result.Count)
			for _, id := range result.ReconciledWorkIDs {
				cmd.Printf("  %s\n", id)
			}
			return nil
		},
	}
}

func newDashboardCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:     "dashboard",
		Aliases: []string{"dash"},
		Short:   "Show live supervisor and work graph status",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := connectOrDie()
			data, err := c.doGet("/api/dashboard", nil)
			if err != nil {
				return mapServiceError(err)
			}
			if root.jsonOutput {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			var result struct {
				WorkStates map[string]int `json:"work_states"`
				TotalItems int            `json:"total_items"`
				Supervisor map[string]any `json:"supervisor"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				_, writeErr := cmd.OutOrStdout().Write(data)
				return writeErr
			}
			if result.Supervisor != nil {
				pid, _ := result.Supervisor["pid"].(float64)
				if pid > 0 {
					cycle, _ := result.Supervisor["cycle"].(float64)
					uptime, _ := result.Supervisor["uptime"].(string)
					fmt.Fprintf(cmd.OutOrStdout(), "SUPERVISOR: pid %d, cycle %d, uptime %s\n", int(pid), int(cycle), uptime)
					if inFlight, ok := result.Supervisor["in_flight"].([]any); ok && len(inFlight) > 0 {
						fmt.Fprintln(cmd.OutOrStdout(), "IN-FLIGHT:")
						for _, f := range inFlight {
							if fm, ok := f.(map[string]any); ok {
								workID, _ := fm["work_id"].(string)
								adapter, _ := fm["adapter"].(string)
								elapsed, _ := fm["elapsed"].(string)
								fmt.Fprintf(cmd.OutOrStdout(), "  %s (%s, %s)\n", workID, adapter, elapsed)
							}
						}
					}
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "SUPERVISOR: not running (stale state)")
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "SUPERVISOR: not running")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "WORK: %d total", result.TotalItems)
			for _, s := range []string{"ready", "claimed", "in_progress", "checking", "awaiting_attestation", "blocked", "done", "failed", "cancelled", "archived"} {
				if n, ok := result.WorkStates[s]; ok && n > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), ", %d %s", n, s)
				}
			}
			fmt.Fprintln(cmd.OutOrStdout())
			return nil
		},
	}
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the fase version",
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
	for _, usage := range status.UsageByModel {
		if err := writef(
			cmd.OutOrStdout(),
			"usage_model: %s input=%d output=%d total=%d cache_read=%d cache_write=%d cost=$%.6f\n",
			emptyDash(usage.Model),
			usage.InputTokens,
			usage.OutputTokens,
			usage.TotalTokens,
			usage.CacheReadInputTokens,
			usage.CacheCreationInputTokens,
			usage.CostUSD,
		); err != nil {
			return err
		}
	}
	if status.VendorCost != nil && status.VendorCost.TotalCostUSD > 0 {
		if err := writef(cmd.OutOrStdout(), "api_cost_vendor: $%.6f\n", status.VendorCost.TotalCostUSD); err != nil {
			return err
		}
	}
	if status.EstimatedCost != nil && status.EstimatedCost.TotalCostUSD > 0 {
		if err := writef(cmd.OutOrStdout(), "api_cost_estimated: $%.6f\n", status.EstimatedCost.TotalCostUSD); err != nil {
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
		if artifacts == nil {
			artifacts = []core.ArtifactRecord{}
		}
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
		if matches == nil {
			matches = []core.HistoryMatch{}
		}
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

func renderWorkItem(cmd *cobra.Command, jsonOutput bool, work *core.WorkItemRecord) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), work)
	}
	if err := writef(
		cmd.OutOrStdout(),
		"%s\t%s\t%s\t%s\t%s\n",
		work.WorkID,
		work.Kind,
		work.ExecutionState,
		work.ApprovalState,
		work.Title,
	); err != nil {
		return err
	}
	if work.Objective != "" {
		if err := writef(cmd.OutOrStdout(), "  objective=%s\n", work.Objective); err != nil {
			return err
		}
	}
	if work.Phase != "" {
		if err := writef(cmd.OutOrStdout(), "  phase=%s\n", work.Phase); err != nil {
			return err
		}
	}
	if work.LockState != "" {
		if err := writef(cmd.OutOrStdout(), "  lock_state=%s\n", work.LockState); err != nil {
			return err
		}
	}
	if work.HeadCommitOID != "" {
		if err := writef(cmd.OutOrStdout(), "  head_commit=%s\n", work.HeadCommitOID); err != nil {
			return err
		}
	}
	if len(work.RequiredModelTraits) > 0 {
		if err := writef(cmd.OutOrStdout(), "  required_model_traits=%s\n", strings.Join(work.RequiredModelTraits, ",")); err != nil {
			return err
		}
	}
	if len(work.RequiredAttestations) > 0 {
		if err := writef(cmd.OutOrStdout(), "  required_attestations=%d\n", len(work.RequiredAttestations)); err != nil {
			return err
		}
	}
	if len(work.PreferredModels) > 0 {
		if err := writef(cmd.OutOrStdout(), "  preferred_models=%s\n", strings.Join(work.PreferredModels, ",")); err != nil {
			return err
		}
	}
	if len(work.AvoidModels) > 0 {
		if err := writef(cmd.OutOrStdout(), "  avoid_models=%s\n", strings.Join(work.AvoidModels, ",")); err != nil {
			return err
		}
	}
	if work.CurrentJobID != "" || work.CurrentSessionID != "" {
		if err := writef(cmd.OutOrStdout(), "  session=%s job=%s\n", emptyDash(work.CurrentSessionID), emptyDash(work.CurrentJobID)); err != nil {
			return err
		}
	}
	if work.ClaimedBy != "" || work.ClaimedUntil != nil {
		if err := writef(cmd.OutOrStdout(), "  claim=%s until=%s\n", emptyDash(work.ClaimedBy), timeStringPtr(work.ClaimedUntil)); err != nil {
			return err
		}
	}
	return nil
}

func renderBootstrapAssessment(cmd *cobra.Command, jsonOutput bool, assessment *service.BootstrapAssessment) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), assessment)
	}
	if err := writef(cmd.OutOrStdout(), "roots: %s\n", strings.Join(assessment.Roots, ", ")); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "bootstrap_ready: %t\n", assessment.BootstrapReady); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "score: %d\n", assessment.Score); err != nil {
		return err
	}
	if err := writef(cmd.OutOrStdout(), "recommended_action: %s\n", assessment.RecommendedAction); err != nil {
		return err
	}
	if len(assessment.Entrypoints) > 0 {
		if err := writef(cmd.OutOrStdout(), "entrypoints:\n"); err != nil {
			return err
		}
		for _, entry := range assessment.Entrypoints {
			if err := writef(cmd.OutOrStdout(), "  %s\t%s\t%s\n", entry.Role, entry.Kind, entry.Path); err != nil {
				return err
			}
		}
	}
	if len(assessment.Missing) > 0 {
		if err := writef(cmd.OutOrStdout(), "missing:\n"); err != nil {
			return err
		}
		for _, item := range assessment.Missing {
			if err := writef(cmd.OutOrStdout(), "  %s\n", item); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderBootstrapCreateResult(cmd *cobra.Command, jsonOutput bool, result *service.BootstrapCreateResult) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), result)
	}
	if err := renderWorkItem(cmd, false, &result.Work); err != nil {
		return err
	}
	return renderBootstrapAssessment(cmd, false, result.Assessment)
}

func renderWorkItems(cmd *cobra.Command, jsonOutput bool, items []core.WorkItemRecord) error {
	if jsonOutput {
		if items == nil {
			items = []core.WorkItemRecord{}
		}
		return writeJSON(cmd.OutOrStdout(), items)
	}
	for _, item := range items {
		if err := writef(
			cmd.OutOrStdout(),
			"%s\t%s\t%s\t%s\t%s\n",
			item.WorkID,
			item.Kind,
			item.ExecutionState,
			item.ApprovalState,
			item.Title,
		); err != nil {
			return err
		}
	}
	return nil
}

func renderWorkShow(cmd *cobra.Command, jsonOutput bool, result *service.WorkShowResult) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), result)
	}
	if err := renderWorkItem(cmd, false, &result.Work); err != nil {
		return err
	}
	if len(result.Children) > 0 {
		if err := writef(cmd.OutOrStdout(), "children: %d\n", len(result.Children)); err != nil {
			return err
		}
		for _, child := range result.Children {
			if err := writef(cmd.OutOrStdout(), "  %s\t%s\t%s\t%s\n", child.WorkID, child.Kind, child.ExecutionState, child.Title); err != nil {
				return err
			}
		}
	}
	if len(result.Updates) > 0 {
		if err := writef(cmd.OutOrStdout(), "updates: %d\n", len(result.Updates)); err != nil {
			return err
		}
		for _, update := range result.Updates {
			if err := writef(cmd.OutOrStdout(), "  %s\t%s\t%s\n", update.CreatedAt.Format("2006-01-02 15:04:05"), emptyDash(update.Phase), update.Message); err != nil {
				return err
			}
		}
	}
	if len(result.Notes) > 0 {
		if err := writef(cmd.OutOrStdout(), "notes: %d\n", len(result.Notes)); err != nil {
			return err
		}
		for _, note := range result.Notes {
			if err := writef(cmd.OutOrStdout(), "  %s\t%s\t%s\n", note.CreatedAt.Format("2006-01-02 15:04:05"), emptyDash(note.NoteType), note.Body); err != nil {
				return err
			}
		}
	}
	if len(result.Jobs) > 0 {
		if err := writef(cmd.OutOrStdout(), "jobs: %d\n", len(result.Jobs)); err != nil {
			return err
		}
		for _, job := range result.Jobs {
			if err := writef(cmd.OutOrStdout(), "  %s\t%s\t%s\n", job.JobID, job.State, emptyDash(summaryStringMap(job.Summary, "message"))); err != nil {
				return err
			}
		}
	}
	if len(result.Proposals) > 0 {
		if err := writef(cmd.OutOrStdout(), "proposals: %d\n", len(result.Proposals)); err != nil {
			return err
		}
		for _, proposal := range result.Proposals {
			if err := writef(cmd.OutOrStdout(), "  %s\t%s\t%s\n", proposal.ProposalID, proposal.ProposalType, proposal.State); err != nil {
				return err
			}
		}
	}
	if len(result.Attestations) > 0 {
		if err := writef(cmd.OutOrStdout(), "attestations: %d\n", len(result.Attestations)); err != nil {
			return err
		}
		for _, attestation := range result.Attestations {
			if err := writef(cmd.OutOrStdout(), "  %s\t%s\t%s\t%s\n", attestation.CreatedAt.Format("2006-01-02 15:04:05"), attestation.Result, emptyDash(attestation.VerifierKind), attestation.Summary); err != nil {
				return err
			}
		}
	}
	if len(result.Approvals) > 0 {
		if err := writef(cmd.OutOrStdout(), "approvals: %d\n", len(result.Approvals)); err != nil {
			return err
		}
		for _, approval := range result.Approvals {
			if err := writef(cmd.OutOrStdout(), "  %s\t%s\t%s\n", approval.ApprovedAt.Format("2006-01-02 15:04:05"), approval.Status, emptyDash(approval.ApprovedCommitOID)); err != nil {
				return err
			}
		}
	}
	if len(result.Promotions) > 0 {
		if err := writef(cmd.OutOrStdout(), "promotions: %d\n", len(result.Promotions)); err != nil {
			return err
		}
		for _, promotion := range result.Promotions {
			if err := writef(cmd.OutOrStdout(), "  %s\t%s\t%s\n", promotion.PromotedAt.Format("2006-01-02 15:04:05"), promotion.Environment, emptyDash(promotion.TargetRef)); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderWorkNotes(cmd *cobra.Command, jsonOutput bool, notes []core.WorkNoteRecord) error {
	if jsonOutput {
		if notes == nil {
			notes = []core.WorkNoteRecord{}
		}
		return writeJSON(cmd.OutOrStdout(), notes)
	}
	for _, note := range notes {
		if err := writef(cmd.OutOrStdout(), "%s\t%s\t%s\n", note.CreatedAt.Format("2006-01-02 15:04:05"), emptyDash(note.NoteType), note.Body); err != nil {
			return err
		}
	}
	return nil
}

func renderWorkNote(cmd *cobra.Command, jsonOutput bool, note *core.WorkNoteRecord) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), note)
	}
	return writef(cmd.OutOrStdout(), "%s\t%s\t%s\n", note.NoteID, emptyDash(note.NoteType), note.Body)
}

func renderWorkProposals(cmd *cobra.Command, jsonOutput bool, proposals []core.WorkProposalRecord) error {
	if jsonOutput {
		if proposals == nil {
			proposals = []core.WorkProposalRecord{}
		}
		return writeJSON(cmd.OutOrStdout(), proposals)
	}
	for _, proposal := range proposals {
		if err := writef(cmd.OutOrStdout(), "%s\t%s\t%s\ttarget=%s\tsource=%s\n", proposal.ProposalID, proposal.ProposalType, proposal.State, emptyDash(proposal.TargetWorkID), emptyDash(proposal.SourceWorkID)); err != nil {
			return err
		}
	}
	return nil
}

func renderWorkProposal(cmd *cobra.Command, jsonOutput bool, proposal *core.WorkProposalRecord, created *core.WorkItemRecord) error {
	if jsonOutput {
		payload := map[string]any{"proposal": proposal}
		if created != nil {
			payload["created_work"] = created
		}
		return writeJSON(cmd.OutOrStdout(), payload)
	}
	if err := writef(cmd.OutOrStdout(), "%s\t%s\t%s\n", proposal.ProposalID, proposal.ProposalType, proposal.State); err != nil {
		return err
	}
	if proposal.Rationale != "" {
		if err := writef(cmd.OutOrStdout(), "  rationale=%s\n", proposal.Rationale); err != nil {
			return err
		}
	}
	if created != nil {
		if err := writef(cmd.OutOrStdout(), "  created_work=%s\n", created.WorkID); err != nil {
			return err
		}
	}
	return nil
}

func renderAttestation(cmd *cobra.Command, jsonOutput bool, record *core.AttestationRecord, work *core.WorkItemRecord) error {
	if jsonOutput {
		payload := map[string]any{
			"attestation": record,
			"work":        work,
		}
		return writeJSON(cmd.OutOrStdout(), payload)
	}
	if err := writef(cmd.OutOrStdout(), "%s\t%s\t%s\n", record.AttestationID, record.Result, record.SubjectID); err != nil {
		return err
	}
	if record.Summary != "" {
		if err := writef(cmd.OutOrStdout(), "  %s\n", record.Summary); err != nil {
			return err
		}
	}
	if work != nil {
		if err := writef(cmd.OutOrStdout(), "  approval=%s\n", work.ApprovalState); err != nil {
			return err
		}
	}
	return nil
}

func renderVerification(cmd *cobra.Command, jsonOutput bool, result *service.WorkVerifyResult) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), map[string]any{"verification": result})
	}
	if err := writef(cmd.OutOrStdout(), "%s\t%s\n", result.Work.WorkID, strings.ToUpper(result.Verdict)); err != nil {
		return err
	}
	if result.Commit.OID != "" {
		if err := writef(cmd.OutOrStdout(), "  commit: %s [%s]\n", result.Commit.OID, result.Commit.Status); err != nil {
			return err
		}
		if result.Commit.Detail != "" {
			if err := writef(cmd.OutOrStdout(), "    %s\n", result.Commit.Detail); err != nil {
				return err
			}
		}
	}
	if len(result.Attestations) > 0 {
		if err := writef(cmd.OutOrStdout(), "  attestations: %d\n", len(result.Attestations)); err != nil {
			return err
		}
		for _, attestation := range result.Attestations {
			if err := writef(cmd.OutOrStdout(), "    %s %s sig=%s signer=%s\n", attestation.AttestationID, attestation.Result, attestation.SignatureStatus, attestation.SignerStatus); err != nil {
				return err
			}
		}
	}
	if len(result.Issues) > 0 {
		if err := writef(cmd.OutOrStdout(), "  issues:\n"); err != nil {
			return err
		}
		for _, issue := range result.Issues {
			if err := writef(cmd.OutOrStdout(), "    - %s\n", issue); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderPromotion(cmd *cobra.Command, jsonOutput bool, record *core.PromotionRecord, work *core.WorkItemRecord) error {
	if jsonOutput {
		payload := map[string]any{
			"promotion": record,
			"work":      work,
		}
		return writeJSON(cmd.OutOrStdout(), payload)
	}
	if err := writef(cmd.OutOrStdout(), "%s\t%s\t%s\n", record.PromotionID, record.Environment, emptyDash(record.TargetRef)); err != nil {
		return err
	}
	if work != nil {
		if err := writef(cmd.OutOrStdout(), "  approval=%s\n", work.ApprovalState); err != nil {
			return err
		}
	}
	return nil
}

func renderWorkProjection(cmd *cobra.Command, jsonOutput bool, kind, format string, result *service.WorkShowResult) error {
	if jsonOutput {
		payload := map[string]any{
			"kind":    kind,
			"format":  format,
			"work_id": result.Work.WorkID,
			"content": workProjection(kind, result),
		}
		return writeJSON(cmd.OutOrStdout(), payload)
	}
	_, err := io.WriteString(cmd.OutOrStdout(), workProjection(kind, result))
	return err
}

func workProjection(kind string, result *service.WorkShowResult) string {
	switch kind {
	case "checklist":
		return renderChecklistProjection(result)
	default:
		return renderStatusProjection(result)
	}
}

func renderChecklistProjection(result *service.WorkShowResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", result.Work.Title)
	fmt.Fprintf(&b, "Work: `%s`\n\n", result.Work.WorkID)
	fmt.Fprintf(&b, "- Objective: %s\n", result.Work.Objective)
	fmt.Fprintf(&b, "- Execution: %s\n", result.Work.ExecutionState)
	fmt.Fprintf(&b, "- Approval: %s\n\n", result.Work.ApprovalState)
	if len(result.Children) == 0 {
		b.WriteString("- [ ] No child work items yet\n")
		return b.String()
	}
	for _, child := range result.Children {
		box := " "
		if child.ExecutionState == "done" {
			box = "x"
		}
		fmt.Fprintf(&b, "- [%s] %s (%s / %s) `%s`\n", box, child.Title, child.ExecutionState, child.ApprovalState, child.WorkID)
	}
	return b.String()
}

func renderStatusProjection(result *service.WorkShowResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", result.Work.Title)
	fmt.Fprintf(&b, "- Work: `%s`\n", result.Work.WorkID)
	fmt.Fprintf(&b, "- Kind: %s\n", result.Work.Kind)
	fmt.Fprintf(&b, "- Objective: %s\n", result.Work.Objective)
	fmt.Fprintf(&b, "- Execution: %s\n", result.Work.ExecutionState)
	fmt.Fprintf(&b, "- Approval: %s\n", result.Work.ApprovalState)
	if result.Work.Phase != "" {
		fmt.Fprintf(&b, "- Phase: %s\n", result.Work.Phase)
	}
	if result.Work.CurrentSessionID != "" || result.Work.CurrentJobID != "" {
		fmt.Fprintf(&b, "- Current session/job: `%s` / `%s`\n", emptyDash(result.Work.CurrentSessionID), emptyDash(result.Work.CurrentJobID))
	}
	if len(result.Updates) > 0 {
		update := result.Updates[0]
		fmt.Fprintf(&b, "\n## Latest Update\n\n%s\n", update.Message)
	}
	if len(result.Attestations) > 0 {
		attestation := result.Attestations[0]
		fmt.Fprintf(&b, "\n## Latest Attestation\n\n- Result: %s\n- Verifier: %s\n- Summary: %s\n", attestation.Result, emptyDash(attestation.VerifierKind), emptyDash(attestation.Summary))
	}
	if len(result.Approvals) > 0 {
		approval := result.Approvals[0]
		fmt.Fprintf(&b, "\n## Latest Approval\n\n- Status: %s\n- Commit: %s\n", approval.Status, emptyDash(approval.ApprovedCommitOID))
	}
	if len(result.Promotions) > 0 {
		promotion := result.Promotions[0]
		fmt.Fprintf(&b, "\n## Latest Promotion\n\n- Environment: %s\n- Ref: %s\n", promotion.Environment, emptyDash(promotion.TargetRef))
	}
	if len(result.Children) > 0 {
		b.WriteString("\n## Children\n\n")
		for _, child := range result.Children {
			fmt.Fprintf(&b, "- `%s` %s (%s / %s)\n", child.WorkID, child.Title, child.ExecutionState, child.ApprovalState)
		}
	}
	return b.String()
}

// waitStatusHTTP polls /api/job/{id}/status until the job reaches a terminal state.
func waitStatusHTTP(c *serveClient, jobID string, interval, timeout time.Duration) (*service.StatusResult, error) {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		data, err := c.doGet("/api/job/"+jobID+"/status", nil)
		if err != nil {
			return nil, err
		}
		var status service.StatusResult
		if err := json.Unmarshal(data, &status); err != nil {
			return nil, fmt.Errorf("decoding status: %w", err)
		}
		if status.Job.State.Terminal() {
			return &status, nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return &status, nil
		}
		time.Sleep(interval)
	}
}

// followEventsHTTP polls /api/job/{id}/logs-after until the job is terminal and no more events arrive.
func followEventsHTTP(cmd *cobra.Command, c *serveClient, jobID string, jsonOutput bool, limit int) error {
	var lastSeq int64
	for {
		params := url.Values{
			"after": {strconv.FormatInt(lastSeq, 10)},
			"limit": {strconv.Itoa(limit)},
		}
		data, err := c.doGet("/api/job/"+jobID+"/logs-after", params)
		if err != nil {
			return err
		}
		var events []core.EventRecord
		if err := json.Unmarshal(data, &events); err != nil {
			return fmt.Errorf("decoding events: %w", err)
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
			}
		}
		if len(events) > 0 {
			lastSeq = events[len(events)-1].Seq
		}

		statusData, err := c.doGet("/api/job/"+jobID+"/status", nil)
		if err != nil {
			return err
		}
		var status service.StatusResult
		if err := json.Unmarshal(statusData, &status); err != nil {
			return fmt.Errorf("decoding status: %w", err)
		}
		if status.Job.State.Terminal() && len(events) == 0 {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// followRawLogsHTTP polls /api/job/{id}/logs-raw-after until the job is terminal and no more logs arrive.
func followRawLogsHTTP(cmd *cobra.Command, c *serveClient, jobID string, jsonOutput bool, limit int) error {
	var lastSeq int64
	for {
		params := url.Values{
			"after": {strconv.FormatInt(lastSeq, 10)},
			"limit": {strconv.Itoa(limit)},
		}
		data, err := c.doGet("/api/job/"+jobID+"/logs-raw-after", params)
		if err != nil {
			return err
		}
		var resp struct {
			Logs   []service.RawLogEntry `json:"logs"`
			Events []core.EventRecord    `json:"events"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return fmt.Errorf("decoding raw logs: %w", err)
		}
		for _, event := range resp.Events {
			lastSeq = event.Seq
		}
		if jsonOutput {
			for _, entry := range resp.Logs {
				if err := writeJSON(cmd.OutOrStdout(), entry); err != nil {
					return err
				}
			}
		} else {
			for _, entry := range resp.Logs {
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

		statusData, err := c.doGet("/api/job/"+jobID+"/status", nil)
		if err != nil {
			return err
		}
		var status service.StatusResult
		if err := json.Unmarshal(statusData, &status); err != nil {
			return fmt.Errorf("decoding status: %w", err)
		}
		if status.Job.State.Terminal() && len(resp.Events) == 0 {
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
			"%s\t%s\t%s\tselected=%t\tauth=%s\tbilling=%s\tpricing=%s\tprobe=%s\thistory=%s\tsource=%s\ttraits=%s\n",
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
			emptyDash(strings.Join(entry.Traits, ",")),
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
	if errors.Is(err, errServeBusy) {
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

func attestationNonceFromWorkShow(result *service.WorkShowResult) string {
	if result == nil {
		return ""
	}
	if nonce := stringMetadata(result.Work.Metadata, "attestation_nonce"); nonce != "" {
		return nonce
	}
	for _, child := range result.Children {
		if !strings.EqualFold(child.Kind, "attest") {
			continue
		}
		if nonce := stringMetadata(child.Metadata, "attestation_nonce"); nonce != "" {
			return nonce
		}
	}
	return ""
}

func stringMetadata(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func parseJSONObjectFlag(value string) (map[string]any, error) {
	if strings.TrimSpace(value) == "" {
		return map[string]any{}, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, err
	}
	if decoded == nil {
		decoded = map[string]any{}
	}
	return decoded, nil
}

func parseRequiredAttestations(value string) ([]core.RequiredAttestation, error) {
	if strings.TrimSpace(value) == "" {
		return []core.RequiredAttestation{}, nil
	}
	var decoded []core.RequiredAttestation
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, err
	}
	if decoded == nil {
		decoded = []core.RequiredAttestation{}
	}
	for i := range decoded {
		if decoded[i].Metadata == nil {
			decoded[i].Metadata = map[string]any{}
		}
	}
	return decoded, nil
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

func summaryStringMap(summary map[string]any, key string) string {
	if summary == nil {
		return ""
	}
	value, _ := summary[key].(string)
	return value
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

func timeStringPtr(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.UTC().Format(time.RFC3339Nano)
}
