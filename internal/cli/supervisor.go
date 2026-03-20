package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/yusefmosiah/fase/internal/core"
	"github.com/yusefmosiah/fase/internal/service"
)

// rotationEntry pairs an adapter name with the model to use for that adapter.
type rotationEntry struct {
	adapter string
	model   string
}

// workRotation is the round-robin pool for dispatch and attestation.
// Order matters: attestation uses offset +1 from the work entry.
var workRotation = []rotationEntry{
	{adapter: "claude", model: "claude-sonnet-4-6"},
	{adapter: "codex", model: "gpt-5.4-mini"},
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

// modelForAdapter returns the model string for a known rotation adapter, or "".
func modelForAdapter(adapter string) string {
	for _, e := range workRotation {
		if e.adapter == adapter {
			return e.model
		}
	}
	return ""
}

// pickAdapterModel selects adapter+model for a work item.
// Priority: (1) item.PreferredAdapters, (2) rotation offset from job history,
// (3) global round-robin counter.
// jobs should be ordered newest-first so jobs[0] is the most recent attempt.
// rotation is the effective pool to draw from (budget-filtered); nil falls back to workRotation.
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
		return "", ""
	}

	forbidden := make(map[string]struct{}, len(item.ForbiddenAdapters))
	for _, a := range item.ForbiddenAdapters {
		forbidden[a] = struct{}{}
	}
	avoidModel := make(map[string]struct{}, len(item.AvoidModels))
	for _, m := range item.AvoidModels {
		avoidModel[m] = struct{}{}
	}

	isAllowed := func(adapter, model string) bool {
		if _, ok := forbidden[adapter]; ok {
			return false
		}
		if model != "" {
			if _, ok := avoidModel[model]; ok {
				return false
			}
		}
		return true
	}

	findCandidate := func(adapter string, exactModels []string) (string, string, bool) {
		for _, e := range pool {
			if e.adapter != adapter {
				continue
			}
			if !isAllowed(e.adapter, e.model) {
				continue
			}
			if len(exactModels) > 0 {
				match := false
				for _, m := range exactModels {
					if e.model == m {
						match = true
						break
					}
				}
				if !match {
					continue
				}
			}
			return e.adapter, e.model, true
		}
		return "", "", false
	}

	if len(item.PreferredAdapters) > 0 {
		for _, a := range item.PreferredAdapters {
			if adapter, model, ok := findCandidate(a, item.PreferredModels); ok {
				return adapter, model
			}
			if model := modelForAdapter(a); model != "" && isAllowed(a, model) {
				return a, model
			}
		}
	}

	if len(jobs) > 0 {
		for _, job := range jobs {
			lastIdx := rotationIndexForEntry(job.Adapter, pool)
			if lastIdx < 0 {
				continue
			}
			for step := 1; step <= len(pool); step++ {
				next := pool[(lastIdx+step)%len(pool)]
				if isAllowed(next.adapter, next.model) {
					return next.adapter, next.model
				}
			}
		}
	}

	if defaultAdapter != "" {
		if adapter, model, ok := findCandidate(defaultAdapter, item.PreferredModels); ok {
			return adapter, model
		}
		if model := modelForAdapter(defaultAdapter); model != "" && isAllowed(defaultAdapter, model) {
			return defaultAdapter, model
		}
	}

	// No stronger signal: choose the best available entry with round-robin
	// fairness, skipping explicitly forbidden adapters and avoided models.
	base := int(atomic.AddInt64(&globalRotationIdx, 1) - 1)
	for i := 0; i < len(pool); i++ {
		idx := (base + i) % len(pool)
		if isAllowed(pool[idx].adapter, pool[idx].model) {
			return pool[idx].adapter, pool[idx].model
		}
	}
	return pool[0].adapter, pool[0].model
}

// rotationIndexForEntry returns the index of adapter in the given pool, or -1.
func rotationIndexForEntry(adapter string, pool []rotationEntry) int {
	for i, e := range pool {
		if e.adapter == adapter {
			return i
		}
	}
	return -1
}

// attestAdapterModel returns the adapter+model to use for attestation, offset
// by one slot from the work adapter so a different model independently reviews.
func attestAdapterModel(workAdapter string) (adapter, model string) {
	idx := rotationIndexForAdapter(workAdapter)
	if idx < 0 {
		// Unknown adapter — pick the first rotation entry that differs.
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

type supervisorOptions struct {
	interval       time.Duration
	maxConcurrent  int
	defaultAdapter string
	cwd            string
	dryRun         bool
}

// inFlightJob tracks a dispatched job by its real fase job ID.
type inFlightJob struct {
	workID       string
	jobID        string // real fase job_id from `run --json` output
	adapter      string
	model        string // model used for this job (for attestation offset)
	started      time.Time
	leaseNext    time.Time // when to renew the lease
	workerPID    int       // OS PID of the detached __run-job worker (0 if unknown)
	worktreePath string    // absolute path to git worktree (empty if not using worktrees)
	branchName   string    // git branch name for this job's worktree
	tokenPath    string    // path to the issued capability token file
	sshKeyPath   string    // path to the ephemeral SSH signing key (Phase 2)
	agentEmail   string    // email identity in allowed_signers (Phase 2)
}

type supervisorCycleReport struct {
	Cycle      int              `json:"cycle"`
	Timestamp  string           `json:"timestamp"`
	Ready      int              `json:"ready"`
	InFlight   int              `json:"in_flight"`
	Dispatched []dispatchEntry  `json:"dispatched,omitempty"`
	Completed  []completedEntry `json:"completed,omitempty"`
	DryRun     bool             `json:"dry_run,omitempty"`
	Paused     bool             `json:"paused,omitempty"`
}

type dispatchEntry struct {
	WorkID  string `json:"work_id"`
	Title   string `json:"title"`
	Adapter string `json:"adapter"`
	JobID   string `json:"job_id,omitempty"`
}

type completedEntry struct {
	WorkID string `json:"work_id"`
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

func newSupervisorCommand(root *rootOptions) *cobra.Command {
	opts := &supervisorOptions{
		interval:       30 * time.Second,
		maxConcurrent:  1,
		defaultAdapter: "codex",
		cwd:            ".",
	}

	cmd := &cobra.Command{
		Use:   "supervisor",
		Short: "Run an autonomous dispatch loop that claims and executes ready work",
		Long: `The supervisor polls for ready work items, claims them, hydrates their
briefing, and spawns "fase run" processes to execute each item. It tracks
real job IDs and polls their status for completion.

The core loop:
  1. Reconcile expired leases
  2. Poll in-flight jobs via "fase status" and mark work done/failed
  3. Renew leases on still-running jobs
  4. If in-flight < max-concurrent, claim next work and spawn a run
  5. Sleep for the poll interval`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSupervisor(cmd, root, opts)
		},
	}

	cmd.Flags().DurationVar(&opts.interval, "interval", 30*time.Second, "poll interval between cycles")
	cmd.Flags().IntVar(&opts.maxConcurrent, "max-concurrent", 1, "max simultaneous jobs")
	cmd.Flags().StringVar(&opts.defaultAdapter, "default-adapter", "codex", "fallback adapter when work has no preference")
	cmd.Flags().StringVar(&opts.cwd, "cwd", ".", "working directory for spawned jobs")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "show what would be dispatched without doing it")

	cmd.AddCommand(
		newSupervisorPauseCommand(root),
		newSupervisorResumeCommand(root),
	)

	return cmd
}

// supervisorServeURL reads .fase/serve.json to discover the running serve API port.
func supervisorServeURL(root *rootOptions) (string, error) {
	svc, err := service.Open(context.Background(), root.configPath)
	if err != nil {
		return "", fmt.Errorf("open service: %w", err)
	}
	_ = svc.Close()

	data, err := os.ReadFile(filepath.Join(svc.Paths.StateDir, "serve.json"))
	if err != nil {
		return "", fmt.Errorf("serve is not running (no serve.json found): %w", err)
	}
	var info struct {
		Port int    `json:"port"`
		Host string `json:"host,omitempty"`
	}
	if err := json.Unmarshal(data, &info); err != nil || info.Port == 0 {
		return "", fmt.Errorf("invalid serve.json")
	}
	host := "localhost"
	return fmt.Sprintf("http://%s:%d", host, info.Port), nil
}

func newSupervisorPauseCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "pause",
		Short: "Pause dispatch in the running supervisor (in-flight jobs continue)",
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := supervisorServeURL(root)
			if err != nil {
				return err
			}
			resp, err := http.Post(base+"/api/supervisor/pause", "application/json", nil) //nolint:noctx
			if err != nil {
				return fmt.Errorf("pause request failed: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				var body map[string]string
				_ = json.NewDecoder(resp.Body).Decode(&body)
				return fmt.Errorf("pause failed (%d): %s", resp.StatusCode, body["error"])
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "supervisor: dispatch paused")
			return nil
		},
	}
}

func newSupervisorResumeCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "resume",
		Short: "Resume dispatch in the running supervisor",
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := supervisorServeURL(root)
			if err != nil {
				return err
			}
			resp, err := http.Post(base+"/api/supervisor/resume", "application/json", nil) //nolint:noctx
			if err != nil {
				return fmt.Errorf("resume request failed: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				var body map[string]string
				_ = json.NewDecoder(resp.Body).Decode(&body)
				return fmt.Errorf("resume failed (%d): %s", resp.StatusCode, body["error"])
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "supervisor: dispatch resumed")
			return nil
		},
	}
}

func runSupervisor(cmd *cobra.Command, root *rootOptions, opts *supervisorOptions) error {
	return runEventDrivenSupervisor(cmd, root, opts)
}

// spawnRun launches `fase run --json` and extracts the real job_id from the output.
// The run command queues background work and returns immediately with job metadata.
//
// Process hierarchy and orphan prevention:
//
//	supervisor (this process)
//	  └─ fase run --json   (short-lived, returns job ID and exits)
//	       └─ __run-job       (detached worker, Setpgid=true, survives parent)
//
// The `fase run` subprocess is synchronous (we wait for its output). It spawns
// a detached background worker via service.spawnDetachedWorker which sets
// Setpgid: true, placing the worker in its own process group. This means:
//
//  1. The worker intentionally survives `fase run` exiting.
//  2. If the supervisor is killed (SIGKILL), workers will NOT be automatically
//     cleaned up because they are in separate process groups. This is by design
//     for crash resilience — workers can finish their work even if the supervisor
//     dies. On restart, the supervisor reconciles via lease expiry.
//  3. On graceful shutdown (SIGINT/SIGTERM), the supervisor explicitly cancels
//     each in-flight job via svc.Cancel(), which sends escalating signals
//     (SIGINT → SIGTERM → SIGKILL) to the worker's process group.
//
// We still set Setpgid on the `fase run` command itself so it doesn't receive
// signals meant for the supervisor's terminal session (e.g., Ctrl+C) before we
// get a chance to do orderly cleanup.
func spawnRun(bin, configPath, adapter, model, cwd, prompt string, extraEnv []string) (string, error) {
	args := []string{"run", "--json", "--adapter", adapter, "--cwd", cwd, "--prompt", prompt}
	if model != "" {
		args = append(args, "--model", model)
	}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}

	runCmd := exec.Command(bin, args...)
	runCmd.Dir = cwd // ensure fase resolves .fase/ from the target repo
	runCmd.Stderr = nil
	runCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if len(extraEnv) > 0 {
		runCmd.Env = append(os.Environ(), extraEnv...)
	}
	out, err := runCmd.Output()
	if err != nil {
		return "", fmt.Errorf("fase run failed: %w", err)
	}

	// Parse the JSON output to get the job ID
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

// pollJobStatus calls `fase status --json <job-id>` and returns the job state.
func pollJobStatus(ctx context.Context, bin, configPath, jobID, cwd string) (string, error) {
	args := []string{"status", "--json", jobID}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}

	statusCmd := exec.CommandContext(ctx, bin, args...)
	if cwd != "" {
		statusCmd.Dir = cwd
	}
	statusCmd.Stderr = nil
	out, err := statusCmd.Output()
	if err != nil {
		return "", fmt.Errorf("fase status failed: %w", err)
	}

	var result struct {
		Job struct {
			State string `json:"state"`
		} `json:"job"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return "", fmt.Errorf("failed to parse status output: %w", err)
	}
	return result.Job.State, nil
}

func isTerminal(state string) bool {
	switch state {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// isJobStalled checks if a job has produced no new output for the given duration.
// It looks at the modification time of the newest JSONL file in the job's raw stdout dir.
func isJobStalled(jobRawDir string, threshold time.Duration) bool {
	entries, err := os.ReadDir(jobRawDir)
	if err != nil {
		return false // can't determine, assume not stalled
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
		return false // no files yet, job might just be starting
	}
	return time.Since(newest) > threshold
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return true
	}
	return syscall.Kill(pid, 0) == nil
}

type supervisorState struct {
	PID       int                   `json:"pid"`
	Cycle     int                   `json:"cycle"`
	Timestamp string                `json:"timestamp"`
	Uptime    string                `json:"uptime,omitempty"`
	Paused    bool                  `json:"paused,omitempty"`
	InFlight  []inFlightState       `json:"in_flight"`
	Ready     int                   `json:"ready"`
	Report    supervisorCycleReport `json:"last_report"`
}

type inFlightState struct {
	WorkID  string `json:"work_id"`
	JobID   string `json:"job_id"`
	Adapter string `json:"adapter"`
	Elapsed string `json:"elapsed"`
}

var supervisorStart = time.Now()

func writeSupState(cwd string, cycle int, paused bool, inFlight map[string]*inFlightJob, report supervisorCycleReport) {
	stateDir := core.ResolveRepoStateDirFrom(cwd)
	if stateDir == "" {
		stateDir = filepath.Join(cwd, ".fase")
	}
	_ = os.MkdirAll(stateDir, 0o755)

	flights := make([]inFlightState, 0, len(inFlight))
	for _, f := range inFlight {
		flights = append(flights, inFlightState{
			WorkID:  f.workID,
			JobID:   f.jobID,
			Adapter: f.adapter,
			Elapsed: time.Since(f.started).Truncate(time.Second).String(),
		})
	}

	state := supervisorState{
		PID:       os.Getpid(),
		Cycle:     cycle,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Uptime:    time.Since(supervisorStart).Truncate(time.Second).String(),
		Paused:    paused,
		InFlight:  flights,
		Ready:     report.Ready,
		Report:    report,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(stateDir, "supervisor.json"), data, 0o644)
}

func sleepOrStop(ctx context.Context, d time.Duration, sigCh chan os.Signal, stopping *bool) {
	select {
	case <-time.After(d):
	case <-sigCh:
		*stopping = true
	case <-ctx.Done():
		*stopping = true
	}
}

// bootstrapRepo creates a root "bootstrap" work item from repo metadata.
// It deeply walks the doc tree, reads index files and frontmatter, and
// generates an objective that teaches the agent to mirror the project's
// own documentation ontology as the work graph.
func bootstrapRepo(ctx context.Context, svc *service.Service, cwd string) error {
	repoName := filepath.Base(cwd)
	adrInfo := discoverADRNumbering(cwd)
	adrExample := "ADR-0001"
	if adrInfo.highest > 0 {
		adrExample = fmt.Sprintf("ADR-%04d", adrInfo.highest+1)
	}

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("Repository: %s\nPath: %s\n", repoName, cwd))

	// Read README
	for _, name := range []string{"README.md", "README", "readme.md"} {
		data, err := os.ReadFile(filepath.Join(cwd, name))
		if err == nil {
			excerpt := string(data)
			if len(excerpt) > 3000 {
				excerpt = excerpt[:3000] + "\n...(truncated)"
			}
			buf.WriteString(fmt.Sprintf("\n## %s\n%s\n", name, excerpt))
			break
		}
	}

	// Read doc indexes (ATLAS.md, NARRATIVE_INDEX.md, etc.)
	for _, idx := range []string{
		"docs/ATLAS.md", "docs/README.md", "docs/INDEX.md",
		"docs/architecture/NARRATIVE_INDEX.md", "ARCHITECTURE.md",
	} {
		data, err := os.ReadFile(filepath.Join(cwd, idx))
		if err == nil {
			excerpt := string(data)
			if len(excerpt) > 3000 {
				excerpt = excerpt[:3000] + "\n...(truncated)"
			}
			buf.WriteString(fmt.Sprintf("\n## %s\n%s\n", idx, excerpt))
		}
	}

	// Deep walk docs/ tree — list all docs with their paths
	docsDir := filepath.Join(cwd, "docs")
	if _, err := os.Stat(docsDir); err == nil {
		buf.WriteString("\n## Full docs/ tree\n")
		docCount := 0
		_ = filepath.Walk(docsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".md") {
				return nil
			}
			rel, _ := filepath.Rel(cwd, path)
			// Read first 3 lines for frontmatter/title
			header := readFileHeader(path, 5)
			buf.WriteString(fmt.Sprintf("- %s", rel))
			if header != "" {
				buf.WriteString(fmt.Sprintf("  [%s]", header))
			}
			buf.WriteString("\n")
			docCount++
			return nil
		})
		buf.WriteString(fmt.Sprintf("\n(%d docs total)\n", docCount))
	}

	// Top-level code structure
	if entries, err := os.ReadDir(cwd); err == nil {
		buf.WriteString("\n## Top-level structure\n")
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			marker := ""
			if e.IsDir() {
				marker = "/"
			}
			buf.WriteString(fmt.Sprintf("- %s%s\n", e.Name(), marker))
		}
	}

	if adrInfo.highest > 0 {
		buf.WriteString("\n## ADR numbering\n")
		buf.WriteString(fmt.Sprintf(
			"Highest discovered ADR: ADR-%04d\nContinue numbering from: ADR-%04d\n",
			adrInfo.highest, adrInfo.highest+1,
		))
		if len(adrInfo.samples) > 0 {
			buf.WriteString("Discovered ADR references:\n")
			for _, sample := range adrInfo.samples {
				buf.WriteString(fmt.Sprintf("- %s\n", sample))
			}
		}
	}

	objective := fmt.Sprintf(`Bootstrap the %s repository into the fase work graph by ingesting its documentation.

## Approach

1. DISCOVER THE DOC ONTOLOGY: Read the doc index files (ATLAS.md, README, etc.) to understand how this project organizes its documentation. Look for categories, taxonomies, and hierarchies that the project itself defines. Do NOT impose an external structure — mirror the project's own organization.

2. RESPECT EXISTING ADR NUMBERING: If the repository already uses ADR numbering, continue from the highest discovered ADR number rather than restarting at ADR-0001. The repo context below includes the highest discovered ADR, if any.

3. CREATE WORK ITEMS FROM DOCS: Each significant document (ADR, design doc, spec, guide) should become a work item. The work item's state should reflect the document's status:
   - Documents in "theory" or "proposed" or "draft" directories → kind=plan, state=ready
   - Documents in "practice" or "accepted" or "implemented" directories → kind=implement, state=done
   - Test reports, audits, load tests → these are attestation evidence, not work items. Record them as attestations on the relevant work item using: fase work attest <work-id> --result passed --summary "..." --verifier-kind deterministic --method test
   - Guides and runbooks → attach as notes on the relevant work item
   - Archived/deprecated docs → skip (historical context, not active work)

4. CREATE EDGES FROM DEPENDENCIES: If documents reference each other (e.g., "Requires: ADR-0014" or "Dependencies: ..."), create blocking edges between the corresponding work items.

5. READ KEY DOCUMENTS: For each work item you create, read the actual document content and set the objective to a meaningful summary of what the document describes. Include the document path in the objective so it can be found.

6. RECORD ATTESTATION EVIDENCE: Test reports and validation results should be recorded as attestations on the work items they validate. Use the report's findings as the attestation summary.

## Commands

Create work items:
  fase work create --title "%s: Per-User VM Lifecycle" --objective "..." --kind plan --priority 2

Record attestations (for test reports):
  fase work attest <work-id> --result passed --summary "Load test: 62 concurrent VMs, KSM saving 1.7GB" --verifier-kind deterministic --method test

Add notes (for guides, snapshots, findings):
  fase work note-add <work-id> --type finding --text "Implementation guide at docs/theory/guides/adr-0014-implementation.md"

Mark implemented items as done via attestation (bootstrap only — normal workers must NOT call this):
  fase work attest <work-id> --result passed --summary "Implemented and operational per practice/decisions/"

Create dependency edges between work items:
  fase work proposal create --type add_edge --target <blocked-work-id> --rationale "ADR-0024 requires ADR-0014" --patch '{"edge_type":"blocks","source_work_id":"<blocker-work-id>"}'

## Repository Context

%s`, repoName, adrExample, buf.String())

	_, err := svc.CreateWork(ctx, service.WorkCreateRequest{
		Title:     fmt.Sprintf("Bootstrap %s", repoName),
		Objective: objective,
		Kind:      "plan",
		Priority:  1,
	})
	return err
}

type adrNumberingInfo struct {
	highest int
	samples []string
}

var adrNumberPattern = regexp.MustCompile(`(?i)\bADR-(\d+)\b`)

func discoverADRNumbering(cwd string) adrNumberingInfo {
	docsDir := filepath.Join(cwd, "docs")
	info := adrNumberingInfo{}
	if _, err := os.Stat(docsDir); err != nil {
		return info
	}

	seen := map[int]string{}
	_ = filepath.Walk(docsDir, func(path string, fileInfo os.FileInfo, err error) error {
		if err != nil || fileInfo.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".md") {
			return nil
		}
		rel, relErr := filepath.Rel(cwd, path)
		if relErr != nil {
			rel = path
		}
		header := readFileHeader(path, 20)
		if header == "" {
			return nil
		}
		matches := adrNumberPattern.FindAllStringSubmatch(header, -1)
		if len(matches) == 0 {
			return nil
		}
		seenNumbers := map[int]struct{}{}
		for _, match := range matches {
			n, convErr := strconv.Atoi(match[1])
			if convErr != nil || n <= 0 {
				continue
			}
			if n > info.highest {
				info.highest = n
			}
			if _, ok := seenNumbers[n]; ok {
				continue
			}
			seenNumbers[n] = struct{}{}
			if _, ok := seen[n]; !ok {
				snippet := readFileHeader(path, 5)
				if snippet != "" {
					seen[n] = fmt.Sprintf("%s [%s]", rel, snippet)
				} else {
					seen[n] = rel
				}
			}
		}
		return nil
	})

	if len(seen) == 0 {
		return info
	}
	keys := make([]int, 0, len(seen))
	for n := range seen {
		keys = append(keys, n)
	}
	sort.Ints(keys)
	for _, n := range keys {
		info.samples = append(info.samples, seen[n])
	}
	return info
}

// readFileHeader reads the first N lines of a file and returns a compact summary.
func readFileHeader(path string, maxLines int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.SplitN(string(data), "\n", maxLines+1)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	// Extract title or frontmatter hints
	var parts []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "---" {
			continue
		}
		// Frontmatter fields
		if strings.HasPrefix(line, "Date:") || strings.HasPrefix(line, "Status:") ||
			strings.HasPrefix(line, "Kind:") || strings.HasPrefix(line, "Priority:") {
			parts = append(parts, line)
			continue
		}
		// Markdown title (H1 or H2 — ADR numbers appear in both)
		if strings.HasPrefix(line, "## ") {
			parts = append(parts, line[3:])
			continue
		}
		if strings.HasPrefix(line, "# ") {
			parts = append(parts, line[2:])
			continue
		}
	}
	return strings.Join(parts, " | ")
}

// gitIsRepo returns true if cwd is inside a git repository.
func gitIsRepo(cwd string) bool {
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--git-dir")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// createWorktree creates a git worktree at .fase/worktrees/<workID> on a new
// branch fase/work_<workID>. Returns the worktree path and branch name.
func createWorktree(cwd, workID string) (worktreePath, branchName string, err error) {
	branchName = "fase/work_" + workID
	worktreePath = filepath.Join(cwd, ".fase", "worktrees", workID)

	if err = os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", "", fmt.Errorf("create worktrees dir: %w", err)
	}

	cmd := exec.Command("git", "-C", cwd, "worktree", "add", "-b", branchName, worktreePath)
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		return "", "", fmt.Errorf("git worktree add: %w: %s", runErr, strings.TrimSpace(string(out)))
	}
	return worktreePath, branchName, nil
}

// removeWorktree removes a git worktree and prunes stale entries.
func removeWorktree(cwd, worktreePath string) error {
	cmd := exec.Command("git", "-C", cwd, "worktree", "remove", "--force", worktreePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w: %s", err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("git", "-C", cwd, "worktree", "prune").Run()
	return nil
}

// mergeWorktree merges branchName into the current branch of cwd with --no-ff.
// Returns (conflicted=true, nil) when there are merge conflicts so the caller
// can spawn a resolver rather than treating the error as fatal.
func mergeWorktree(cwd, branchName string) (conflicted bool, err error) {
	cmd := exec.Command("git", "-C", cwd, "merge", "--no-ff", branchName,
		"-m", "Merge "+branchName)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		outStr := string(out)
		if strings.Contains(outStr, "CONFLICT") || strings.Contains(outStr, "conflict") {
			// Leave the conflicted state; caller must resolve or abort.
			return true, nil
		}
		return false, fmt.Errorf("git merge: %w: %s", runErr, strings.TrimSpace(outStr))
	}
	return false, nil
}

// deleteBranch deletes a local branch (best-effort, ignores errors).
func deleteBranch(cwd, branchName string) {
	_ = exec.Command("git", "-C", cwd, "branch", "-d", branchName).Run()
}

// spawnMergeResolver launches a fase run job that resolves merge conflicts.
// conflictFiles is the list of paths that have conflict markers.
func spawnMergeResolver(selfBin, configPath, cwd, workID, branchName string) error {
	prompt := fmt.Sprintf(`You are a merge-conflict resolver.
A git merge of branch %s into the main working directory failed with conflicts.

Working directory: %s
Work item ID: %s

Steps:
1. Run: git status   — identify conflicted files
2. For each conflicted file: open it, resolve the conflict markers (<<<<<<<, =======, >>>>>>>)
3. Run: git add <resolved-files>
4. Run: git commit -m "Resolve merge conflicts from %s"
5. Add a note: fase work note-add %s --type finding --text "Merge conflicts resolved automatically"

Only resolve conflicts; do not make other changes.`, branchName, cwd, workID, branchName, workID)

	args := []string{"run", "--json", "--adapter", "claude", "--cwd", cwd, "--prompt", prompt}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	cmd := exec.Command(selfBin, args...)
	cmd.Dir = cwd
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}
