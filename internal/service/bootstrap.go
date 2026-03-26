package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yusefmosiah/cogent/internal/core"
)

type BootstrapInspectRequest struct {
	Paths []string
}

type BootstrapEntrypoint struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Role string `json:"role"`
}

type BootstrapSignal struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type BootstrapAssessment struct {
	Roots             []string              `json:"roots"`
	BootstrapReady    bool                  `json:"bootstrap_ready"`
	Score             int                   `json:"score"`
	Entrypoints       []BootstrapEntrypoint `json:"entrypoints"`
	Signals           []BootstrapSignal     `json:"signals"`
	Missing           []string              `json:"missing,omitempty"`
	RecommendedAction string                `json:"recommended_action,omitempty"`
}

type BootstrapCreateRequest struct {
	Paths     []string
	Title     string
	Objective string
	Kind      string
}

type BootstrapCreateResult struct {
	Assessment *BootstrapAssessment `json:"assessment"`
	Work       core.WorkItemRecord  `json:"work"`
}

func (s *Service) InspectBootstrap(_ context.Context, req BootstrapInspectRequest) (*BootstrapAssessment, error) {
	if len(req.Paths) == 0 {
		return nil, fmt.Errorf("%w: at least one --path is required", ErrInvalidInput)
	}

	roots := make([]string, 0, len(req.Paths))
	rootSeen := map[string]bool{}
	entrypoints := []BootstrapEntrypoint{}
	signals := []BootstrapSignal{}
	entrySeen := map[string]bool{}
	signalSeen := map[string]bool{}
	hasAgents := false
	hasReadme := false
	hasCodeSignal := false

	addEntrypoint := func(path, kind, role string) {
		key := path + "|" + role
		if entrySeen[key] {
			return
		}
		entrySeen[key] = true
		entrypoints = append(entrypoints, BootstrapEntrypoint{
			Path: path,
			Kind: kind,
			Role: role,
		})
	}
	addSignal := func(path, kind, message string) {
		key := path + "|" + kind + "|" + message
		if signalSeen[key] {
			return
		}
		signalSeen[key] = true
		signals = append(signals, BootstrapSignal{
			Path:    path,
			Kind:    kind,
			Message: message,
		})
	}

	for _, raw := range req.Paths {
		target, err := filepath.Abs(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("%w: resolve path %q: %v", ErrInvalidInput, raw, err)
		}
		info, err := os.Stat(target)
		if err != nil {
			return nil, fmt.Errorf("%w: stat path %q: %v", ErrInvalidInput, target, err)
		}
		root := target
		if !info.IsDir() {
			root = filepath.Dir(target)
		}
		if !rootSeen[root] {
			rootSeen[root] = true
			roots = append(roots, root)
		}

		agentsPath := filepath.Join(root, "AGENTS.md")
		if fileExists(agentsPath) {
			hasAgents = true
			addEntrypoint(agentsPath, "markdown", "agent_instructions")
			addSignal(agentsPath, "docs", "agent instructions found")
		}

		readmePath := filepath.Join(root, "README.md")
		if fileExists(readmePath) {
			hasReadme = true
			addEntrypoint(readmePath, "markdown", "readme")
			addSignal(readmePath, "docs", "repository readme found")
		}

		for _, codePath := range []string{
			filepath.Join(root, "go.mod"),
			filepath.Join(root, "package.json"),
			filepath.Join(root, "pyproject.toml"),
			filepath.Join(root, "Cargo.toml"),
			filepath.Join(root, "Makefile"),
		} {
			if fileExists(codePath) {
				hasCodeSignal = true
				addSignal(codePath, "code", "codebase entrypoint found")
			}
		}
	}

	sort.Strings(roots)
	sort.Slice(entrypoints, func(i, j int) bool { return entrypoints[i].Path < entrypoints[j].Path })
	sort.Slice(signals, func(i, j int) bool {
		if signals[i].Path == signals[j].Path {
			return signals[i].Kind < signals[j].Kind
		}
		return signals[i].Path < signals[j].Path
	})

	score := 0
	if hasAgents {
		score += 2
	}
	if hasReadme {
		score++
	}
	if hasCodeSignal {
		score += 2
	}

	bootstrapReady := len(entrypoints) > 0 && (hasCodeSignal || hasReadme)

	missing := []string{}
	if !hasAgents {
		missing = append(missing, "AGENTS.md")
	}
	if !hasReadme {
		missing = append(missing, "README.md")
	}
	if !hasCodeSignal {
		missing = append(missing, "build/runtime entrypoint (go.mod, package.json, pyproject.toml, Cargo.toml, or Makefile)")
	}

	action := "create root work from discovered docs/code entrypoints"
	if !bootstrapReady {
		action = "add an entrypoint doc or instructions file before bootstrapping"
	}

	return &BootstrapAssessment{
		Roots:             roots,
		BootstrapReady:    bootstrapReady,
		Score:             score,
		Entrypoints:       entrypoints,
		Signals:           signals,
		Missing:           missing,
		RecommendedAction: action,
	}, nil
}

func (s *Service) BootstrapCreate(ctx context.Context, req BootstrapCreateRequest) (*BootstrapCreateResult, error) {
	assessment, err := s.InspectBootstrap(ctx, BootstrapInspectRequest{Paths: req.Paths})
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Bootstrap work graph"
	}
	objective := strings.TrimSpace(req.Objective)
	if objective == "" {
		objective = "Bootstrap a fase work graph from the discovered code/docs entrypoints"
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "plan"
	}
	work, err := s.CreateWork(ctx, WorkCreateRequest{
		Title:     title,
		Objective: objective,
		Kind:      kind,
		CreatedBy: "service",
		Metadata: map[string]any{
			"bootstrap_roots":            assessment.Roots,
			"bootstrap_ready":            assessment.BootstrapReady,
			"bootstrap_score":            assessment.Score,
			"bootstrap_entrypoint_count": len(assessment.Entrypoints),
		},
	})
	if err != nil {
		return nil, err
	}
	lines := []string{
		fmt.Sprintf("bootstrap roots: %s", strings.Join(assessment.Roots, ", ")),
		fmt.Sprintf("bootstrap_ready: %t", assessment.BootstrapReady),
		fmt.Sprintf("recommended_action: %s", assessment.RecommendedAction),
	}
	if len(assessment.Entrypoints) > 0 {
		lines = append(lines, "entrypoints:")
		for _, entry := range assessment.Entrypoints {
			lines = append(lines, fmt.Sprintf("- [%s] %s (%s)", entry.Role, entry.Path, entry.Kind))
		}
	}
	if len(assessment.Missing) > 0 {
		lines = append(lines, "missing:")
		for _, item := range assessment.Missing {
			lines = append(lines, "- "+item)
		}
	}
	if _, err := s.AddWorkNote(ctx, WorkNoteRequest{
		WorkID:    work.WorkID,
		NoteType:  "bootstrap",
		Body:      strings.Join(lines, "\n"),
		CreatedBy: "service",
	}); err != nil {
		return nil, err
	}
	return &BootstrapCreateResult{
		Assessment: assessment,
		Work:       *work,
	}, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
