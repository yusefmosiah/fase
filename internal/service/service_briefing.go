package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/yusefmosiah/cogent/internal/core"
)

func fallbackSkillContent(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "checker":
		return strings.TrimSpace(`# Cogent Checker Contract

- Review the parent work item thoroughly: inspect the code, diff, tests, notes, and evidence.
- Record notes for your findings before attesting.
- If the work involves a web UI: run Playwright e2e tests with 'cd mind-graph && npx playwright test'. Screenshots and videos are saved to mind-graph/test-results/ and will be attached to the attestation email automatically.
- If no Playwright tests exist for web UI work, FAIL the attestation — backend-only tests are insufficient for web UI work.
- REQUIRED: You MUST call 'cogent work attest <parent-work-id> --result passed|failed --message "<your finding summary>"' to submit your attestation result.
- Use --result passed if the work meets its objective; use --result failed if it does not.
- Do NOT create new work items, proposals, or child work. Only do what was assigned.
- Do NOT call cogent work complete or cogent work fail.`) + "\n"
	default:
		return strings.TrimSpace(`# Cogent Worker Contract

- Do the work, add notes as you go, then commit and update state before exiting.
- REQUIRED before exit: git add -A && git commit -m "cogent(<scope>): <summary>"
- REQUIRED on success: cogent work update <work-id> --execution-state done --message "<summary of what you did>"
- REQUIRED on failure: cogent work update <work-id> --execution-state failed --message "<what went wrong>"
- You MUST call one of the above before exiting. The supervisor cannot see your work otherwise.
- REQUIRED: Report your results before exiting. Use 'cogent report "<summary of what you did, files changed, test results>"'. This notifies whoever dispatched you (supervisor or host).
- Record notes for findings, risks, and open questions.
- Run verification (tests, builds) and report results as notes.
- If the work involves a web UI: you MUST add e2e tests (default: Playwright) covering all interactive features (buttons, drag, resize, navigation). Backend tests alone are insufficient — they cannot catch broken UI behavior.
- Do NOT create new work items, proposals, or child work. Only do what was assigned.
- Do NOT call cogent work attest — an independent agent handles attestation.`) + "\n"
	}
}

func (s *Service) loadSkillFile(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}

	seen := map[string]struct{}{}
	roots := make([]string, 0, 3)
	addRoot := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		root = filepath.Clean(root)
		if _, ok := seen[root]; ok {
			return
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}

	addRoot(s.docRepoRoot(context.Background()))
	addRoot(s.Paths.StateDir)
	if filepath.Base(strings.TrimSpace(s.Paths.StateDir)) == ".cogent" {
		addRoot(filepath.Dir(strings.TrimSpace(s.Paths.StateDir)))
	}

	for _, root := range roots {
		path := filepath.Join(root, "skills", "cogent", name, "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == "" {
			break
		}
		return string(data)
	}

	return fallbackSkillContent(name)
}

// CompileWorkerBriefing deterministically compiles a worker briefing from
// runtime state. This is the adapter-independent hydration contract — all
// adapters consume the same compiled briefing.
func (s *Service) CompileWorkerBriefing(ctx context.Context, workID, mode string) (WorkHydrateResult, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "standard"
	}
	if mode != "thin" && mode != "standard" && mode != "deep" && mode != "supervisor" {
		return nil, fmt.Errorf("%w: hydrate mode must be thin, standard, deep, or supervisor", ErrInvalidInput)
	}
	result, err := s.Work(ctx, workID)
	if err != nil {
		return nil, err
	}

	parent, _ := s.firstRelatedWork(ctx, workID, "parent_of", false)
	blockingInbound, _ := s.relatedWork(ctx, workID, "blocks", false, 25)
	blockingOutbound, _ := s.relatedWork(ctx, workID, "blocks", true, 25)
	children, _ := s.relatedWork(ctx, workID, "parent_of", true, 25)
	verifierNodes, _ := s.relatedWork(ctx, workID, "verifier", false, 25)
	discoveredNodes, _ := s.relatedWork(ctx, workID, "discovered_from", false, 25)
	supersedes, _ := s.relatedWork(ctx, workID, "supersedes", true, 25)
	supersededBy, _ := s.relatedWork(ctx, workID, "supersedes", false, 25)

	updateLimit, noteLimit, attestationLimit, artifactLimit, jobLimit := hydrationLimits(mode)
	updates := result.Updates
	if len(updates) > updateLimit {
		updates = updates[:updateLimit]
	}
	notes := result.Notes
	if len(notes) > noteLimit {
		notes = notes[:noteLimit]
	}
	attestations := result.Attestations
	if len(attestations) > attestationLimit {
		attestations = attestations[:attestationLimit]
	}
	artifacts := result.Artifacts
	if len(artifacts) > artifactLimit {
		artifacts = artifacts[:artifactLimit]
	}
	jobs := result.Jobs
	if len(jobs) > jobLimit {
		jobs = jobs[:jobLimit]
	}

	summary := fmt.Sprintf("%s: %s", result.Work.Kind, result.Work.Objective)
	if len(updates) > 0 && strings.TrimSpace(updates[0].Message) != "" {
		summary = summary + " Latest update: " + strings.TrimSpace(updates[0].Message)
	}
	openQuestions := []string{}
	if len(blockingInbound) > 0 {
		openQuestions = append(openQuestions, fmt.Sprintf("%d blocking dependencies remain unresolved.", len(blockingInbound)))
	}
	if len(attestations) == 0 && len(result.Work.RequiredAttestations) > 0 {
		openQuestions = append(openQuestions, "Required attestations have not been recorded yet.")
	}
	nextActions := []string{
		"Inspect the current work item objective and acceptance before making changes.",
		"Review the most recent updates, notes, and attestations.",
		"Publish a structured work update before handing off or stopping.",
	}
	nextActions = append(nextActions, delegationNextAction(result.Work))

	writeCommands := []string{
		"cogent work update <work-id>",
		"cogent work note-add <work-id>",
	}
	updateDoneCmd := fmt.Sprintf("cogent work update %s --execution-state done --message \"<summary of what you did>\"", workID)
	gitCommitCmd := fmt.Sprintf("git add -A && git commit -m \"cogent(%s): <summary>\"", workID)
	updateFailCmd := fmt.Sprintf("cogent work update %s --execution-state failed --message \"<what went wrong>\"", workID)
	contractRules := []string{
		"Do the work, add notes as you go, then commit and update state before exiting.",
		fmt.Sprintf("REQUIRED before exit: %s", gitCommitCmd),
		fmt.Sprintf("REQUIRED on success: %s", updateDoneCmd),
		fmt.Sprintf("REQUIRED on failure: %s", updateFailCmd),
		"You MUST call one of the above before exiting. The supervisor cannot see your work otherwise.",
		"REQUIRED: Report your results before exiting. Use 'cogent report \"<summary of what you did, files changed, test results>\"'. This notifies whoever dispatched you (supervisor or host).",
		"Record notes for findings, risks, and open questions.",
		"Run verification (tests, builds) and report results as notes.",
		"If the work involves a web UI: you MUST add e2e tests (default: Playwright) covering all interactive features (buttons, drag, resize, navigation). Backend tests alone are insufficient — they cannot catch broken UI behavior.",
		"Do NOT create new work items, proposals, or child work. Only do what was assigned.",
		"Do NOT call cogent work attest — an independent agent handles attestation.",
		delegationNextAction(result.Work),
	}
	skillName := "worker"

	if result.Work.Kind == "attest" {
		skillName = "checker"
		parentWorkID := "<parent-work-id>"
		if parent != nil {
			parentWorkID = parent.WorkID
		}
		attestCmd := fmt.Sprintf("cogent work attest %s --result [passed|failed] --message \"<summary>\"", parentWorkID)
		writeCommands = append(writeCommands, attestCmd)
		attestInstruction := fmt.Sprintf(
			"REQUIRED: After completing your review, you MUST call: cogent work attest %s --result passed|failed --message \"<your finding summary>\"",
			parentWorkID,
		)
		nextActions = append(nextActions, attestInstruction)
		contractRules = []string{
			"Review the parent work item thoroughly: inspect the code, diff, tests, notes, and evidence.",
			"Record notes for your findings before attesting.",
			"If the work involves a web UI: run Playwright e2e tests with 'cd mind-graph && npx playwright test'. Screenshots and videos are saved to mind-graph/test-results/ and will be attached to the attestation email automatically.",
			"If no Playwright tests exist for web UI work, FAIL the attestation — backend-only tests are insufficient for web UI work.",
			fmt.Sprintf("REQUIRED: You MUST call 'cogent work attest %s --result passed|failed --message \"<your finding summary>\"' to submit your attestation result.", parentWorkID),
			"Use --result passed if the work meets its objective; use --result failed if it does not.",
			"Do NOT create new work items, proposals, or child work. Only do what was assigned.",
			"Do NOT call cogent work complete or cogent work fail.",
			delegationNextAction(result.Work),
		}
	}
	skillMarkdown := s.loadSkillFile(skillName)

	runtimeSection := map[string]any{
		"runtime_version": "dev",
		"config_path":     s.ConfigPath,
		"state_dir":       s.Paths.StateDir,
	}
	if claimant := firstNonEmpty(result.Work.ClaimedBy); claimant != "" {
		runtimeSection["claimant"] = claimant
	}

	assignmentSection := map[string]any{
		"work_id":         result.Work.WorkID,
		"title":           result.Work.Title,
		"objective":       result.Work.Objective,
		"kind":            result.Work.Kind,
		"execution_state": result.Work.ExecutionState,
		"approval_state":  result.Work.ApprovalState,
		"priority":        result.Work.Priority,
		"metadata":        cloneMap(result.Work.Metadata),
	}
	if result.Work.Phase != "" {
		assignmentSection["phase"] = result.Work.Phase
	}
	if result.Work.CurrentJobID != "" {
		assignmentSection["current_job_id"] = result.Work.CurrentJobID
	}
	if result.Work.CurrentSessionID != "" {
		assignmentSection["current_session_id"] = result.Work.CurrentSessionID
	}
	if result.Work.ClaimedBy != "" {
		assignmentSection["claimed_by"] = result.Work.ClaimedBy
	}
	if result.Work.ClaimedUntil != nil {
		assignmentSection["claimed_until"] = result.Work.ClaimedUntil.UTC().Format(time.RFC3339Nano)
	}

	return WorkHydrateResult{
		"schema_version": "cogent.worker_briefing.v1",
		"briefing_kind":  "assignment",
		"generated_at":   time.Now().UTC().Format(time.RFC3339Nano),
		"runtime":        runtimeSection,
		"assignment":     assignmentSection,
		"requirements": map[string]any{
			"acceptance":            cloneMap(result.Work.Acceptance),
			"required_capabilities": cloneSlice(result.Work.RequiredCapabilities),
			"preferred_adapters":    cloneSlice(result.Work.PreferredAdapters),
			"forbidden_adapters":    cloneSlice(result.Work.ForbiddenAdapters),
			"policy": map[string]any{
				"child_creation":      "proposal_only",
				"dependency_edits":    "proposal_only",
				"scope_expansion":     "proposal_only",
				"verification_policy": "attestation_driven",
			},
		},
		"graph_context": map[string]any{
			"parent":            workRefOrNil(parent),
			"blocking_inbound":  workRefs(blockingInbound),
			"blocking_outbound": workRefs(blockingOutbound),
			"children":          workRefs(children),
			"verifier_nodes":    workRefs(verifierNodes),
			"discovered_nodes":  workRefs(discoveredNodes),
			"supersession": map[string]any{
				"supersedes":      workRefs(supersedes),
				"supersededed_by": workRefs(supersededBy),
			},
		},
		"evidence": map[string]any{
			"latest_updates":      updateRefs(updates),
			"latest_notes":        noteRefs(notes),
			"latest_attestations": serializeAttestationRefs(attestations),
			"artifacts":           artifactRefs(artifacts),
			"recent_jobs":         jobRefs(jobs),
			"history_matches":     []map[string]any{},
		},
		"worker_contract": map[string]any{
			"read_commands": []string{
				"cogent work show <work-id>",
				"cogent work notes <work-id>",
				"cogent artifacts list --work <work-id>",
				"cogent history search --query <text>",
			},
			"write_commands": writeCommands,
			"rules":          contractRules,
			"skill_name":     skillName,
			"skill_markdown": skillMarkdown,
		},
		"hydration": map[string]any{
			"mode":                     mode,
			"summary":                  summary,
			"open_questions":           openQuestions,
			"recommended_next_actions": nextActions,
		},
	}, nil
}

// ProjectHydrate compiles a project-scoped briefing for cold-starting any session.
// Unlike work-scoped hydration, this covers the entire project: conventions, graph summary,
// active/blocked/ready work, and recent activity. Designed to replace the MEMORY.md bootstrap hack.
func (s *Service) ProjectHydrate(ctx context.Context, req ProjectHydrateRequest) (ProjectHydrateResult, error) {
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "standard"
	}
	if mode != "thin" && mode != "standard" && mode != "deep" && mode != "supervisor" {
		return nil, fmt.Errorf("%w: hydrate mode must be thin, standard, deep, or supervisor", ErrInvalidInput)
	}

	// Conventions — the core of project hydration.
	conventionLimit := 50
	if mode == "thin" {
		conventionLimit = 20
	} else if mode == "deep" {
		conventionLimit = 200
	}
	conventions, err := s.store.ListConventionNotes(ctx, conventionLimit)
	if err != nil {
		return nil, fmt.Errorf("list conventions: %w", err)
	}

	// Work queue summary — counts by execution state.
	allWork, err := s.ListWork(ctx, WorkListRequest{Limit: 500, IncludeArchived: false})
	if err != nil {
		return nil, fmt.Errorf("list work: %w", err)
	}
	stateCounts := map[core.WorkExecutionState]int{}
	var recentCompleted []map[string]any
	var activeWork []map[string]any
	var readyWork []map[string]any
	var blockedWork []map[string]any
	completedLimit := 5
	if mode == "deep" {
		completedLimit = 15
	}
	for _, w := range allWork {
		stateCounts[w.ExecutionState]++
		switch w.ExecutionState {
		case core.WorkExecutionStateDone:
			if len(recentCompleted) < completedLimit {
				recentCompleted = append(recentCompleted, map[string]any{
					"work_id": w.WorkID,
					"title":   w.Title,
					"kind":    w.Kind,
				})
			}
		case core.WorkExecutionStateInProgress, core.WorkExecutionStateClaimed:
			activeWork = append(activeWork, map[string]any{
				"work_id":    w.WorkID,
				"title":      w.Title,
				"kind":       w.Kind,
				"claimed_by": w.ClaimedBy,
			})
		case core.WorkExecutionStateReady:
			entry := map[string]any{
				"work_id":  w.WorkID,
				"title":    w.Title,
				"kind":     w.Kind,
				"priority": w.Priority,
			}
			if len(w.PreferredAdapters) > 0 {
				entry["preferred_adapters"] = w.PreferredAdapters
			}
			if len(w.PreferredModels) > 0 {
				entry["preferred_models"] = w.PreferredModels
			}
			readyWork = append(readyWork, entry)
		case core.WorkExecutionStateBlocked:
			blockedWork = append(blockedWork, map[string]any{
				"work_id": w.WorkID,
				"title":   w.Title,
				"kind":    w.Kind,
			})
		}
	}

	// Pending attestations — work awaiting review.
	var pendingAttestations []map[string]any
	for _, w := range allWork {
		if w.ExecutionState.Canonical() == core.WorkExecutionStateInProgress && len(w.RequiredAttestations) > 0 {
			pendingAttestations = append(pendingAttestations, map[string]any{
				"work_id":               w.WorkID,
				"title":                 w.Title,
				"required_attestations": w.RequiredAttestations,
			})
		}
	}

	// Compile conventions into a deduplicated list (newest wins on duplicate body).
	conventionEntries := make([]map[string]any, 0, len(conventions))
	seen := map[string]bool{}
	for _, note := range conventions {
		body := strings.TrimSpace(note.Body)
		if seen[body] {
			continue
		}
		seen[body] = true
		entry := map[string]any{
			"body":       body,
			"created_at": note.CreatedAt.UTC().Format(time.RFC3339),
		}
		if note.WorkID != "" {
			entry["source_work_id"] = note.WorkID
		}
		conventionEntries = append(conventionEntries, entry)
	}

	effectiveMode := mode
	if mode == "supervisor" {
		effectiveMode = "standard"
	}

	// Load project spec (SPEC.md) if present — gives supervisor and workers
	// project-specific context beyond conventions.
	var projectSpec string
	cwd, _ := os.Getwd()
	for _, specName := range []string{"SPEC.md", "spec.md", "SPEC", "README.md"} {
		if data, err := os.ReadFile(filepath.Join(cwd, specName)); err == nil {
			projectSpec = strings.TrimSpace(string(data))
			if len(projectSpec) > 4000 {
				projectSpec = projectSpec[:4000] + "\n\n[truncated — read full file with read_file tool]"
			}
			break
		}
	}

	result := ProjectHydrateResult{
		"schema_version": "cogent.project_briefing.v1",
		"briefing_kind":  "project",
		"generated_at":   time.Now().UTC().Format(time.RFC3339Nano),
		"mode":           mode,
		"runtime": map[string]any{
			"config_path": s.ConfigPath,
			"state_dir":   s.Paths.StateDir,
		},
		"conventions": conventionEntries,
		"queue_summary": map[string]any{
			"total_items":  len(allWork),
			"state_counts": stateCounts,
		},
		"active_work":          activeWork,
		"ready_work":           readyWork,
		"blocked_work":         blockedWork,
		"recent_completed":     recentCompleted,
		"pending_attestations": pendingAttestations,
	}
	_ = effectiveMode // reserved for future per-mode tuning

	contract := map[string]any{
		"read_commands": []string{
			"cogent work show <work-id>",
			"cogent work notes <work-id>",
			"cogent work hydrate <work-id>",
			"cogent work list",
			"cogent work ready",
			"cogent project hydrate",
		},
		"write_commands": []string{
			"cogent work create",
			"cogent work update <work-id>",
			"cogent work note-add <work-id>",
			"cogent work attest <work-id>",
			"cogent dispatch [work-id]",
		},
		"rules": []string{
			"Build: run 'make install' before running cogent commands. Always use 'cogent' (on PATH), never './cogent'.",
			"CLI routes through cogent serve — serve must be running for all commands.",
			"All persistent state belongs in the Cogent work queue (notes, updates, conventions).",
			"Do not use Claude memory system — all state in Cogent work queue.",
			"Do not create memory files, CLAUDE.md, or .claude hidden state files.",
			"One code-writer per environment, unlimited readers — plan/research/attest tasks can run concurrently.",
			"Host agent role: delegate and review, never write code directly.",
		},
		"available_adapters": []string{
			"native (zai/glm-5-turbo, zai/glm-5, zai/glm-4.7, zai/glm-4.7-flash, bedrock/claude-haiku-4-5, bedrock/claude-sonnet-4-6, bedrock/claude-opus-4-6, chatgpt/gpt-5.4, chatgpt/gpt-5.4-mini) — in-process Go adapter",
			"claude (claude-sonnet-4-6, claude-haiku-4-5) — Claude Code subprocess",
			"codex (gpt-5.4, gpt-5.4-mini) — Codex subprocess",
			"opencode (zai-coding-plan/glm-5-turbo) — OpenCode subprocess",
		},
		"model_capabilities": []string{
			"GLM models (glm-5-turbo, glm-5, glm-4.7, glm-4.7-flash): text-only, no multimodal. Cannot run Playwright or verify screenshots.",
			"Claude models (haiku, sonnet, opus): multimodal. Can run Playwright and verify visual output.",
			"GPT models (gpt-5.4, gpt-5.4-mini): multimodal. Can run Playwright and verify visual output.",
			"Native adapter: web search via Exa/Tavily/Brave/Serper (rate-limited, uses project API keys).",
			"External adapters (claude, codex): have their own built-in web search (no rate limits). Prefer external adapters for research-heavy tasks.",
		},
	}
	result["contract"] = contract
	if projectSpec != "" {
		result["project_spec"] = projectSpec
	}

	if mode == "supervisor" {
		result["supervisor_role"] = supervisorRolePrompt()
		result["dispatch_protocol"] = supervisorDispatchProtocol()
	}

	return result, nil
}

func RenderProjectHydrateMarkdown(r ProjectHydrateResult) string {
	var b strings.Builder

	b.WriteString("# Project Briefing\n\n")

	if gen, ok := r["generated_at"].(string); ok {
		fmt.Fprintf(&b, "Generated: %s\n", gen)
	}
	if mode, ok := r["mode"].(string); ok {
		fmt.Fprintf(&b, "Mode: %s\n\n", mode)
	}

	if conventions := toSlice(r["conventions"]); len(conventions) > 0 {
		b.WriteString("## Project Conventions\n\n")
		for _, c := range conventions {
			if entry, ok := c.(map[string]any); ok {
				if body, ok := entry["body"].(string); ok {
					for _, line := range strings.Split(body, "\n") {
						if strings.TrimSpace(line) == "" {
							continue
						}
						b.WriteString("- " + line + "\n")
					}
				}
			}
		}
		b.WriteString("\n")
	}

	if summary, ok := r["queue_summary"].(map[string]any); ok {
		b.WriteString("## Work Queue Summary\n\n")
		if total, ok := summary["total_items"].(int); ok {
			fmt.Fprintf(&b, "Total items: %d\n", total)
		}
		if counts, ok := summary["state_counts"].(map[any]any); ok {
			for k, v := range counts {
				fmt.Fprintf(&b, "  %v: %d\n", k, v)
			}
		}
		b.WriteString("\n")
	}

	renderWorkList := func(title string, key string) {
		items := toSlice(r[key])
		if len(items) == 0 {
			return
		}
		b.WriteString("## " + title + "\n\n")
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				wtitle := "(untitled)"
				if t, ok := m["title"].(string); ok {
					wtitle = t
				}
				id := ""
				if wid, ok := m["work_id"].(string); ok {
					id = wid
				}
				kind := ""
				if k, ok := m["kind"].(string); ok {
					kind = k
				}
				fmt.Fprintf(&b, "- **%s** `%s` [%s]", wtitle, id, kind)
				if claimed, ok := m["claimed_by"].(string); ok && claimed != "" {
					fmt.Fprintf(&b, " claimed by %s", claimed)
				}
				if pri, ok := m["priority"].(int); ok && pri != 0 {
					fmt.Fprintf(&b, " priority=%d", pri)
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	renderWorkList("Active Work", "active_work")
	renderWorkList("Ready Work", "ready_work")
	renderWorkList("Blocked Work", "blocked_work")
	renderWorkList("Recently Completed", "recent_completed")

	if atts := toSlice(r["pending_attestations"]); len(atts) > 0 {
		b.WriteString("## Pending Attestations\n\n")
		for _, a := range atts {
			if m, ok := a.(map[string]any); ok {
				wtitle := "(untitled)"
				if t, ok := m["title"].(string); ok {
					wtitle = t
				}
				if wid, ok := m["work_id"].(string); ok {
					fmt.Fprintf(&b, "- **%s** `%s`", wtitle, wid)
				}
				if ra, ok := m["required_attestations"].([]any); ok {
					fmt.Fprintf(&b, " requires: %v", ra)
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	if contract, ok := r["contract"].(map[string]any); ok {
		b.WriteString("## Contract\n\n")
		if cmds := toSlice(contract["read_commands"]); len(cmds) > 0 {
			b.WriteString("Read commands:\n")
			for _, c := range cmds {
				if s, ok := c.(string); ok {
					fmt.Fprintf(&b, "  - `%s`\n", s)
				}
			}
		}
		if cmds := toSlice(contract["write_commands"]); len(cmds) > 0 {
			b.WriteString("\nWrite commands:\n")
			for _, c := range cmds {
				if s, ok := c.(string); ok {
					fmt.Fprintf(&b, "  - `%s`\n", s)
				}
			}
		}
		if rules := toSlice(contract["rules"]); len(rules) > 0 {
			b.WriteString("\nRules:\n")
			for _, rule := range rules {
				if s, ok := rule.(string); ok {
					fmt.Fprintf(&b, "  - %s\n", s)
				}
			}
		}
		if adapters := toSlice(contract["available_adapters"]); len(adapters) > 0 {
			b.WriteString("\nAvailable adapters:\n")
			for _, a := range adapters {
				if s, ok := a.(string); ok {
					fmt.Fprintf(&b, "  - %s\n", s)
				}
			}
		}
		if caps := toSlice(contract["model_capabilities"]); len(caps) > 0 {
			b.WriteString("\nModel capabilities:\n")
			for _, c := range caps {
				if s, ok := c.(string); ok {
					fmt.Fprintf(&b, "  - %s\n", s)
				}
			}
		}
		b.WriteString("\n")
	}

	if spec, ok := r["project_spec"].(string); ok && spec != "" {
		b.WriteString("## Project Spec\n\n")
		b.WriteString(spec)
		b.WriteString("\n\n")
	}

	if role, ok := r["supervisor_role"].(string); ok {
		b.WriteString("## Supervisor Role\n\n")
		b.WriteString(role)
		b.WriteString("\n\n")
	}

	if proto, ok := r["dispatch_protocol"].(map[string]any); ok {
		renderProtoSection := func(title, key string) {
			if steps := toSlice(proto[key]); len(steps) > 0 {
				b.WriteString("### " + title + "\n\n")
				for _, step := range steps {
					if s, ok := step.(string); ok {
						b.WriteString(s + "\n")
					}
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("## Dispatch Protocol\n\n")
		renderProtoSection("Dispatch Flow", "dispatch_flow")
		renderProtoSection("Attestation Flow", "attestation_flow")
		renderProtoSection("Communication", "communication")
		renderProtoSection("Error Handling", "error_handling")
		renderProtoSection("Concurrency Rules", "concurrency_rules")
		renderProtoSection("Work Creation Rules", "work_creation_rules")
	}

	return b.String()
}

func supervisorRolePrompt() string {
	return `You are the Cogent supervisor. Your job is to manage the work queue using SEQUENTIAL dispatch:
1. NEVER dispatch multiple features in parallel. Complete one feature at a time.
2. Dispatch a single ready work item to a worker agent (choosing the right adapter and model).
3. Monitor worker progress. Work stays in_progress until completion gates are resolved and the worker can signal done.
4. When [check:pass] or [check:fail] events arrive, use work_show to review the canonical evidence bundle (work state, checks, attestations, artifacts, docs, approvals, promotions).
5. A passing check is evidence only. NEVER call work update <id> --execution-state done just because a check passed.
6. If check result is FAIL: count failures with check_record_list. If < 3: use session_send to send feedback to original worker; do NOT mark done. If >= 3: use send_escalation_email to notify human; mark work failed.
7. Ensure one code-writing feature at a time per the Cogent sequential model.

You are NOT a worker — you never write code directly. You delegate to worker agents
via the dispatch system and verify their output before allowing the next feature.`
}

func supervisorDispatchProtocol() map[string]any {
	return map[string]any{
		"dispatch_flow": []string{
			"SEQUENTIAL DISPATCH (not parallel): One feature at a time.",
			"1. Check active_work — if any item is in_progress, wait for it to complete.",
			"2. Only when no active work: select the next highest-priority ready item.",
			"3. For the selected item, choose adapter+model based on preferred_adapters/preferred_models, or round-robin.",
			"4. Claim the work item (cogent work claim <work-id>).",
			"5. Hydrate the worker briefing (cogent work hydrate <work-id>).",
			"6. Dispatch: spawn a worker session on the chosen adapter with the briefing as prompt.",
			"7. Monitor the worker until they resolve completion gates and signal 'done' or 'failed'.",
			"8. Wait for [check:pass] or [check:fail] events.",
			"CRITICAL: Do not dispatch the next feature until the current feature passes a check.",
		},
		"check_flow": []string{
			"REQUIRED STEP: Checks produce evidence, the canonical review/completion path makes decisions.",
			"When you see [check:pass] or [check:fail] event:",
			"1. Call work_show <work-id> to review the canonical evidence bundle (work state, checks, attestations, artifacts, docs, approvals, promotions). Use check_record_show only when you need one standalone check report.",
			"2. If result is 'pass': do NOT call 'cogent work update <work-id> --execution-state done'. Passing checks are evidence only; wait for the canonical attestation/review gate to resolve and then follow the resulting approval/promote path if required.",
			"3. If result is 'fail': call check_record_list <work-id> to count how many checks have failed.",
			"   - If failure count < 3: call session_send to send failure context back to the worker (they will fix and re-check).",
			"   - If failure count >= 3: call send_escalation_email to notify the human (spec may need updating).",
			"4. If you escalated or sent feedback, do NOT mark work as done — wait for the next check, attestation, or human action.",
			"RULE: Checks never authorize done on their own.",
		},
		"communication": []string{
			"REQUIRED: After each action (dispatch, attest), call the report tool with a structured status update.",
			"Format: '[action] work_title — result.' Example: '[dispatched] Fix RSS sources — sent to claude/haiku.' '[attested:passed] Search fix — passed all tests, merging.'",
			"On errors or repeated failures, report with type=escalation.",
			"Use the report MCP tool or 'cogent report \"message\"' CLI command.",
		},
		"model_preferences": []string{
			"Workers: prefer zai/glm-5-turbo (fast, unlimited, excellent at implementation including UI). claude/claude-haiku-4-5 as secondary. claude/claude-sonnet-4-6 for complex work.",
			"GLM-5-turbo is preferred over haiku for both cost and quality.",
			"Verification/review: use multimodal models — claude-opus-4-6, claude-sonnet-4-6, or chatgpt/gpt-5.4-mini. These can verify screenshots.",
			"GLM is text-only: great for writing code but CANNOT verify visual output. Never use GLM for Playwright-based review.",
			"DIVERSITY: always use a different model for review than was used for implementation. Avoid mode collapse — one model verifying another catches more bugs.",
			"AVOID bedrock adapter unless explicitly requested — use claude adapter for Claude models instead.",
			"AVOID codex/chatgpt for workers unless other adapters are unavailable.",
		},
		"error_handling": []string{
			"If a worker fails: the item returns to ready state. Do not immediately redispatch — let the queue settle first.",
			"If a worker stalls (no output for 30 minutes): housekeeping notifies you. Investigate before redispatching.",
			"If attestation is rejected: redispatch the work with feedback; do not move to next feature.",
			"If an adapter is unavailable: try the next adapter in rotation.",
		},
		"concurrency_rules": []string{
			"SEQUENTIAL DISPATCH: Only one feature dispatch at a time (implement/plan kind work).",
			"Planning, research, and attestation can run concurrently (parallel to the active dispatch).",
			"Strictly enforce: no new feature dispatch until the current feature is attested and approved.",
		},
		"work_creation_rules": []string{
			"When creating work items, include DETAILED objectives that a worker can execute independently.",
			"Title: concise but specific (e.g., 'Fix SSE streaming in AnthropicClient' not 'Fix bug').",
			"Objective: include (1) what to implement, (2) which files to create/modify, (3) acceptance criteria including e2e tests for UI work, (4) relevant context (ADR references, related work IDs).",
			"Always set kind (implement/plan/attest), priority, and preferred_adapters if the task needs a specific adapter.",
			"A worker reading only the objective should be able to complete the task without asking questions.",
			"Do NOT create throwaway/test work items. Only create real work that advances the project.",
		},
	}
}

func toSlice(v any) []any {
	if v == nil {
		return nil
	}
	if s, ok := v.([]any); ok {
		return s
	}
	val := reflect.ValueOf(v)
	if val.Kind() == reflect.Slice {
		result := make([]any, val.Len())
		for i := range val.Len() {
			result[i] = val.Index(i).Interface()
		}
		return result
	}
	return nil
}

func (s *Service) HydrateWork(ctx context.Context, req WorkHydrateRequest) (WorkHydrateResult, error) {
	if req.Debrief {
		return nil, fmt.Errorf("%w: debrief hydration is not implemented yet", ErrUnsupported)
	}
	briefing, err := s.CompileWorkerBriefing(ctx, req.WorkID, req.Mode)
	if err != nil {
		return nil, err
	}
	if claimant := firstNonEmpty(req.Claimant); claimant != "" {
		if runtimeSection, ok := briefing["runtime"].(map[string]any); ok {
			runtimeSection["claimant"] = claimant
		}
	}
	return briefing, nil
}

// RenderWorkerBriefingMarkdown renders a hydrated work briefing as markdown.
func RenderWorkerBriefingMarkdown(r WorkHydrateResult) string {
	var b strings.Builder

	// Assignment
	if a, ok := r["assignment"].(map[string]any); ok {
		b.WriteString("# Assignment\n\n")
		if title, _ := a["title"].(string); title != "" {
			fmt.Fprintf(&b, "**%s**\n", title)
		}
		if wid, _ := a["work_id"].(string); wid != "" {
			fmt.Fprintf(&b, "Work ID: `%s`\n", wid)
		}
		if kind, _ := a["kind"].(string); kind != "" {
			fmt.Fprintf(&b, "Kind: %s\n", kind)
		}
		if obj, _ := a["objective"].(string); obj != "" {
			fmt.Fprintf(&b, "\n%s\n", obj)
		}
	}

	// Notes (conventions, findings from prior work)
	if ev, ok := r["evidence"].(map[string]any); ok {
		if notes := toSlice(ev["latest_notes"]); len(notes) > 0 {
			b.WriteString("\n## Notes\n\n")
			for _, n := range notes {
				if note, ok := n.(map[string]any); ok {
					ntype, _ := note["note_type"].(string)
					body, _ := note["body"].(string)
					if body != "" {
						fmt.Fprintf(&b, "**[%s]** %s\n\n", ntype, body)
					}
				}
			}
		}
	}

	// Contract
	if wc, ok := r["worker_contract"].(map[string]any); ok {
		if rules := toSlice(wc["rules"]); len(rules) > 0 {
			b.WriteString("\n## Rules\n\n")
			for _, rule := range rules {
				if s, ok := rule.(string); ok {
					fmt.Fprintf(&b, "- %s\n", s)
				}
			}
		}
		if skill, _ := wc["skill_markdown"].(string); strings.TrimSpace(skill) != "" {
			title := "Embedded Skill"
			if skillName, _ := wc["skill_name"].(string); strings.TrimSpace(skillName) != "" {
				skillLabel := strings.TrimSpace(skillName)
				title = fmt.Sprintf("%s Skill", strings.ToUpper(skillLabel[:1])+skillLabel[1:])
			}
			b.WriteString("\n## " + title + "\n\n")
			b.WriteString(skill)
			if !strings.HasSuffix(skill, "\n") {
				b.WriteString("\n")
			}
		}
	}

	return b.String()
}

func hydrationLimits(mode string) (updates, notes, attestations, artifacts, jobs int) {
	switch mode {
	case "thin":
		// Minimal: just assignment + contract. No history.
		return 0, 3, 0, 0, 0
	case "deep":
		// Full context: prior runs, artifacts, attestations. For debugging/review.
		return 20, 20, 20, 25, 15
	default:
		// Standard: notes for context, no prior run artifacts or job history.
		return 3, 5, 0, 0, 0
	}
}

func delegationNextAction(work core.WorkItemRecord) string {
	return "Create child work directly only for unexpected work, fanout work, or sequential context isolation when success can be judged from bounded results."
}

func collectAttestationIDs(attestations []core.AttestationRecord) []string {
	ids := make([]string, 0, len(attestations))
	for _, attestation := range attestations {
		ids = append(ids, attestation.AttestationID)
	}
	return ids
}

func workRef(item core.WorkItemRecord) map[string]any {
	return map[string]any{
		"work_id":         item.WorkID,
		"title":           item.Title,
		"kind":            item.Kind,
		"execution_state": item.ExecutionState,
		"approval_state":  item.ApprovalState,
	}
}

func workRefOrNil(item *core.WorkItemRecord) any {
	if item == nil {
		return nil
	}
	return workRef(*item)
}

func workRefs(items []core.WorkItemRecord) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, workRef(item))
	}
	return result
}

func updateRefs(items []core.WorkUpdateRecord) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{
			"update_id":       item.UpdateID,
			"created_at":      item.CreatedAt.Format(time.RFC3339Nano),
			"phase":           item.Phase,
			"execution_state": item.ExecutionState,
			"approval_state":  item.ApprovalState,
			"message":         item.Message,
			"job_id":          item.JobID,
			"session_id":      item.SessionID,
			"artifact_id":     item.ArtifactID,
		})
	}
	return result
}

func noteRefs(items []core.WorkNoteRecord) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{
			"note_id":    item.NoteID,
			"created_at": item.CreatedAt.Format(time.RFC3339Nano),
			"note_type":  item.NoteType,
			"body":       item.Body,
		})
	}
	return result
}

func serializeAttestationRefs(items []core.AttestationRecord) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{
			"attestation_id": item.AttestationID,
			"created_at":     item.CreatedAt.Format(time.RFC3339Nano),
			"result":         item.Result,
			"summary":        item.Summary,
			"artifact_id":    item.ArtifactID,
			"verifier_kind":  item.VerifierKind,
			"method":         item.Method,
		})
	}
	return result
}

func artifactRefs(items []core.ArtifactRecord) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{
			"artifact_id": item.ArtifactID,
			"kind":        item.Kind,
			"path":        item.Path,
			"job_id":      item.JobID,
			"session_id":  item.SessionID,
		})
	}
	return result
}

func jobRefs(items []core.JobRecord) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{
			"job_id":            item.JobID,
			"state":             item.State,
			"adapter":           item.Adapter,
			"native_session_id": item.NativeSessionID,
			"summary_message":   summaryString(item.Summary, "message"),
		})
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
