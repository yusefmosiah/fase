package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func RegisterCodingTools(registry *ToolRegistry, cwd string) error {
	for _, tool := range NewCodingTools(cwd) {
		if err := registry.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func NewCodingTools(cwd string) []Tool {
	base := filepath.Clean(cwd)
	if strings.TrimSpace(base) == "" {
		base = "."
	}
	tools := []Tool{
		newReadFileTool(base),
		newWriteFileTool(base),
		newEditFileTool(base),
		newGlobTool(base),
		newGrepTool(base),
		newBashTool(base),
		newGitStatusTool(base),
		newGitDiffTool(base),
		newGitCommitTool(base),
	}
	// Mark core tools — these get full schemas on first call.
	// Others are discoverable via the tool catalog in the system prompt.
	core := map[string]bool{"read_file": true, "write_file": true, "bash": true}
	for i := range tools {
		if core[tools[i].Name] {
			tools[i].Core = true
		}
	}
	return tools
}

func newReadFileTool(cwd string) Tool {
	type args struct {
		Path string `json:"path"`
	}
	return toolFromFunc(
		"read_file",
		"Read a file from disk.",
		jsonSchemaObject(map[string]any{
			"path": map[string]any{"type": "string", "description": "Path to read, relative to the session cwd unless absolute."},
		}, []string{"path"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode read_file args: %w", err)
			}
			resolved, err := resolveToolPath(cwd, in.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(resolved)
			if err != nil {
				return "", err
			}
			return jsonString(map[string]any{
				"path":    resolved,
				"content": string(data),
			})
		},
	)
}

func newWriteFileTool(cwd string) Tool {
	type args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	return toolFromFunc(
		"write_file",
		"Create or overwrite a file on disk.",
		jsonSchemaObject(map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		}, []string{"path", "content"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode write_file args: %w", err)
			}
			resolved, err := resolveToolPath(cwd, in.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(resolved, []byte(in.Content), 0o644); err != nil {
				return "", err
			}
			return jsonString(map[string]any{
				"path":          resolved,
				"bytes_written": len(in.Content),
			})
		},
	)
}

func newEditFileTool(cwd string) Tool {
	type args struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all,omitempty"`
	}
	return toolFromFunc(
		"edit_file",
		"Replace text in a file by matching old_string and writing new_string.",
		jsonSchemaObject(map[string]any{
			"path":        map[string]any{"type": "string"},
			"old_string":  map[string]any{"type": "string"},
			"new_string":  map[string]any{"type": "string"},
			"replace_all": map[string]any{"type": "boolean"},
		}, []string{"path", "old_string", "new_string"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode edit_file args: %w", err)
			}
			if in.OldString == "" {
				return "", fmt.Errorf("old_string must not be empty")
			}
			resolved, err := resolveToolPath(cwd, in.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(resolved)
			if err != nil {
				return "", err
			}
			content := string(data)
			matches := strings.Count(content, in.OldString)
			if matches == 0 {
				return "", fmt.Errorf("old_string not found in %s", resolved)
			}
			if !in.ReplaceAll && matches != 1 {
				return "", fmt.Errorf("old_string matched %d times in %s; set replace_all to replace all matches", matches, resolved)
			}
			updated := content
			replacements := 1
			if in.ReplaceAll {
				replacements = -1
			}
			updated = strings.Replace(updated, in.OldString, in.NewString, replacements)
			if err := os.WriteFile(resolved, []byte(updated), 0o644); err != nil {
				return "", err
			}
			count := matches
			if !in.ReplaceAll {
				count = 1
			}
			return jsonString(map[string]any{
				"path":         resolved,
				"replacements": count,
			})
		},
	)
}

func newGlobTool(cwd string) Tool {
	type args struct {
		Pattern string `json:"pattern"`
		Limit   int    `json:"limit,omitempty"`
	}
	return toolFromFunc(
		"glob",
		"Find files by glob pattern.",
		jsonSchemaObject(map[string]any{
			"pattern": map[string]any{"type": "string"},
			"limit":   map[string]any{"type": "integer"},
		}, []string{"pattern"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode glob args: %w", err)
			}
			if strings.TrimSpace(in.Pattern) == "" {
				return "", fmt.Errorf("pattern must not be empty")
			}
			matcher, err := globPatternToRegexp(in.Pattern)
			if err != nil {
				return "", err
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 200
			}
			matches := make([]string, 0, min(limit, 32))
			err = filepath.WalkDir(cwd, func(current string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if current == cwd {
					return nil
				}
				rel, err := filepath.Rel(cwd, current)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel)
				if d.IsDir() {
					if rel == ".git" || strings.HasPrefix(rel, ".git/") {
						return filepath.SkipDir
					}
					return nil
				}
				if matcher.MatchString(rel) {
					matches = append(matches, rel)
					if len(matches) >= limit {
						return errToolLimitReached
					}
				}
				return nil
			})
			if err != nil && err != errToolLimitReached {
				return "", err
			}
			sort.Strings(matches)
			return jsonString(map[string]any{
				"pattern": in.Pattern,
				"matches": matches,
			})
		},
	)
}

func newGrepTool(cwd string) Tool {
	type args struct {
		Pattern         string `json:"pattern"`
		Path            string `json:"path,omitempty"`
		Glob            string `json:"glob,omitempty"`
		Limit           int    `json:"limit,omitempty"`
		CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	}
	return toolFromFunc(
		"grep",
		"Search file contents with a regular expression.",
		jsonSchemaObject(map[string]any{
			"pattern":          map[string]any{"type": "string"},
			"path":             map[string]any{"type": "string"},
			"glob":             map[string]any{"type": "string"},
			"limit":            map[string]any{"type": "integer"},
			"case_insensitive": map[string]any{"type": "boolean"},
		}, []string{"pattern"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode grep args: %w", err)
			}
			if strings.TrimSpace(in.Pattern) == "" {
				return "", fmt.Errorf("pattern must not be empty")
			}
			expr := in.Pattern
			if in.CaseInsensitive {
				expr = "(?i)" + expr
			}
			re, err := regexp.Compile(expr)
			if err != nil {
				return "", err
			}
			root := cwd
			if strings.TrimSpace(in.Path) != "" {
				root, err = resolveToolPath(cwd, in.Path)
				if err != nil {
					return "", err
				}
			}
			var globRE *regexp.Regexp
			if strings.TrimSpace(in.Glob) != "" {
				globRE, err = globPatternToRegexp(in.Glob)
				if err != nil {
					return "", err
				}
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 200
			}
			type match struct {
				Path    string `json:"path"`
				Line    int    `json:"line"`
				Content string `json:"content"`
			}
			matches := make([]match, 0, min(limit, 32))
			searchFile := func(filePath string) error {
				data, err := os.ReadFile(filePath)
				if err != nil {
					return nil
				}
				if bytes.IndexByte(data, 0) >= 0 {
					return nil
				}
				rel, err := filepath.Rel(cwd, filePath)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel)
				if globRE != nil && !globRE.MatchString(rel) {
					return nil
				}
				lines := strings.Split(string(data), "\n")
				for i, line := range lines {
					if re.MatchString(line) {
						matches = append(matches, match{
							Path:    rel,
							Line:    i + 1,
							Content: line,
						})
						if len(matches) >= limit {
							return errToolLimitReached
						}
					}
				}
				return nil
			}

			info, err := os.Stat(root)
			if err != nil {
				return "", err
			}
			if !info.IsDir() {
				if err := searchFile(root); err != nil && err != errToolLimitReached {
					return "", err
				}
			} else {
				err = filepath.WalkDir(root, func(current string, d fs.DirEntry, walkErr error) error {
					if walkErr != nil {
						return walkErr
					}
					if d.IsDir() {
						rel, relErr := filepath.Rel(cwd, current)
						if relErr == nil {
							rel = filepath.ToSlash(rel)
							if rel == ".git" || strings.HasPrefix(rel, ".git/") {
								return filepath.SkipDir
							}
						}
						return nil
					}
					return searchFile(current)
				})
				if err != nil && err != errToolLimitReached {
					return "", err
				}
			}

			return jsonString(map[string]any{
				"pattern": in.Pattern,
				"matches": matches,
			})
		},
	)
}

func newBashTool(cwd string) Tool {
	type args struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	}
	return toolFromFunc(
		"bash",
		"Execute a shell command with a timeout.",
		jsonSchemaObject(map[string]any{
			"command":         map[string]any{"type": "string"},
			"timeout_seconds": map[string]any{"type": "integer"},
		}, []string{"command"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode bash args: %w", err)
			}
			if strings.TrimSpace(in.Command) == "" {
				return "", fmt.Errorf("command must not be empty")
			}
			timeout := 30 * time.Second
			if in.TimeoutSeconds > 0 {
				timeout = time.Duration(in.TimeoutSeconds) * time.Second
			}
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			cmd := exec.CommandContext(runCtx, "zsh", "-lc", in.Command)
			cmd.Dir = cwd
			output, err := cmd.CombinedOutput()

			exitCode := 0
			if err != nil {
				var exitErr *exec.ExitError
				if errorsAs(err, &exitErr) {
					exitCode = exitErr.ExitCode()
				} else if runCtx.Err() == context.DeadlineExceeded {
					exitCode = 124
				} else {
					exitCode = -1
				}
			}

			result := map[string]any{
				"command":   in.Command,
				"cwd":       cwd,
				"output":    string(output),
				"exit_code": exitCode,
				"timed_out": runCtx.Err() == context.DeadlineExceeded,
			}
			if err != nil {
				result["error"] = err.Error()
			}
			return jsonString(result)
		},
	)
}

func newGitStatusTool(cwd string) Tool {
	return toolFromFunc(
		"git_status",
		"Show git status for the current repository.",
		jsonSchemaObject(nil, nil, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			return runGitTool(ctx, cwd, "status", "--short", "--branch")
		},
	)
}

func newGitDiffTool(cwd string) Tool {
	type args struct {
		Cached bool     `json:"cached,omitempty"`
		Base   string   `json:"base,omitempty"`
		Paths  []string `json:"paths,omitempty"`
	}
	return toolFromFunc(
		"git_diff",
		"Show git diff output.",
		jsonSchemaObject(map[string]any{
			"cached": map[string]any{"type": "boolean"},
			"base":   map[string]any{"type": "string"},
			"paths": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		}, nil, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode git_diff args: %w", err)
			}
			cmdArgs := []string{"diff"}
			if in.Cached {
				cmdArgs = append(cmdArgs, "--cached")
			}
			if strings.TrimSpace(in.Base) != "" {
				cmdArgs = append(cmdArgs, in.Base)
			}
			if len(in.Paths) > 0 {
				cmdArgs = append(cmdArgs, "--")
				cmdArgs = append(cmdArgs, in.Paths...)
			}
			return runGitTool(ctx, cwd, cmdArgs...)
		},
	)
}

func newGitCommitTool(cwd string) Tool {
	type args struct {
		Message string   `json:"message"`
		All     bool     `json:"all,omitempty"`
		Paths   []string `json:"paths,omitempty"`
	}
	return toolFromFunc(
		"git_commit",
		"Create a git commit with a message.",
		jsonSchemaObject(map[string]any{
			"message": map[string]any{"type": "string"},
			"all":     map[string]any{"type": "boolean"},
			"paths": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		}, []string{"message"}, false),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := json.Unmarshal(raw, &in); err != nil {
				return "", fmt.Errorf("decode git_commit args: %w", err)
			}
			if strings.TrimSpace(in.Message) == "" {
				return "", fmt.Errorf("message must not be empty")
			}
			cmdArgs := []string{"commit", "-m", in.Message}
			if in.All {
				cmdArgs = append(cmdArgs, "--all")
			}
			if len(in.Paths) > 0 {
				cmdArgs = append(cmdArgs, "--")
				cmdArgs = append(cmdArgs, in.Paths...)
			}
			return runGitTool(ctx, cwd, cmdArgs...)
		},
	)
}

var errToolLimitReached = fmt.Errorf("tool result limit reached")

func resolveToolPath(cwd, rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(rawPath) {
		return filepath.Clean(rawPath), nil
	}
	return filepath.Join(cwd, rawPath), nil
}

func globPatternToRegexp(pattern string) (*regexp.Regexp, error) {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" {
		return nil, fmt.Errorf("glob pattern must not be empty")
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString(`[^/]*`)
			}
		case '?':
			b.WriteString(`[^/]`)
		case '.', '(', ')', '+', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(ch)
		default:
			if ch == filepath.Separator {
				b.WriteByte('/')
			} else {
				b.WriteByte(ch)
			}
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func runGitTool(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	result := map[string]any{
		"command":   append([]string{"git"}, args...),
		"cwd":       cwd,
		"output":    string(output),
		"exit_code": 0,
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errorsAs(err, &exitErr) {
			result["exit_code"] = exitErr.ExitCode()
		} else {
			result["exit_code"] = -1
		}
		result["error"] = err.Error()
	}
	return jsonString(result)
}

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
