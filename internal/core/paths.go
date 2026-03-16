package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Paths struct {
	ConfigDir     string
	StateDir      string
	CacheDir      string
	ConfigPath    string
	DBPath        string
	PrivateDBPath string
	JobsDir       string
	RawDir        string
	TransfersDir  string
	DebriefsDir   string
}

func ResolvePaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}

	return ResolvePathsFromEnv(home, os.Getenv)
}

// ResolveRepoStatDir finds the git repo root from cwd and returns
// <repo-root>/.cagent as the state dir. Returns "" if not in a git repo.
func ResolveRepoStateDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
			return filepath.Join(dir, ".cagent")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ResolvePathsForRepo resolves paths scoped to the current git repo.
// If in a git repo, uses <repo-root>/.cagent/ for state.
// Otherwise falls back to global XDG paths.
func ResolvePathsForRepo() (Paths, error) {
	// Explicit override always wins
	if os.Getenv("CAGENT_STATE_DIR") != "" {
		return ResolvePaths()
	}
	repoState := ResolveRepoStateDir()
	if repoState == "" {
		return ResolvePaths()
	}
	paths, err := ResolvePaths()
	if err != nil {
		return paths, err
	}
	return paths.WithStateDir(repoState), nil
}

func ResolvePathsFromEnv(home string, getenv func(string) string) (Paths, error) {
	if home == "" {
		return Paths{}, fmt.Errorf("home directory is required")
	}

	configDir, err := resolveDir(getenv("CAGENT_CONFIG_DIR"), getenv("XDG_CONFIG_HOME"), home, ".config")
	if err != nil {
		return Paths{}, fmt.Errorf("resolve config dir: %w", err)
	}

	stateDir, err := resolveDir(getenv("CAGENT_STATE_DIR"), getenv("XDG_STATE_HOME"), home, filepath.Join(".local", "state"))
	if err != nil {
		return Paths{}, fmt.Errorf("resolve state dir: %w", err)
	}

	cacheDir, err := resolveDir(getenv("CAGENT_CACHE_DIR"), getenv("XDG_CACHE_HOME"), home, ".cache")
	if err != nil {
		return Paths{}, fmt.Errorf("resolve cache dir: %w", err)
	}

	return Paths{
		ConfigDir:     configDir,
		StateDir:      stateDir,
		CacheDir:      cacheDir,
		ConfigPath:    filepath.Join(configDir, "config.toml"),
		DBPath:        filepath.Join(stateDir, "cagent.db"),
		PrivateDBPath: filepath.Join(stateDir, "cagent-private.db"),
		JobsDir:       filepath.Join(stateDir, "jobs"),
		RawDir:        filepath.Join(stateDir, "raw"),
		TransfersDir:  filepath.Join(stateDir, "transfers"),
		DebriefsDir:   filepath.Join(stateDir, "debriefs"),
	}, nil
}

func (p Paths) WithStateDir(stateDir string) Paths {
	p.StateDir = stateDir
	p.DBPath = filepath.Join(stateDir, "cagent.db")
	p.PrivateDBPath = filepath.Join(stateDir, "cagent-private.db")
	p.JobsDir = filepath.Join(stateDir, "jobs")
	p.RawDir = filepath.Join(stateDir, "raw")
	p.TransfersDir = filepath.Join(stateDir, "transfers")
	p.DebriefsDir = filepath.Join(stateDir, "debriefs")
	return p
}

func ExpandPath(path string) (string, error) {
	return expandUser(path)
}

func EnsurePaths(paths Paths) error {
	dirs := []string{
		paths.ConfigDir,
		paths.StateDir,
		paths.CacheDir,
		paths.JobsDir,
		paths.RawDir,
		paths.TransfersDir,
		paths.DebriefsDir,
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %q: %w", dir, err)
		}
	}

	// Auto-add .cagent/ to repo .gitignore if state dir is inside a git repo
	ensureGitignore(paths.StateDir)

	return nil
}

// ensureGitignore adds .cagent/ to the repo's .gitignore if the state dir
// is a .cagent/ directory inside a git repo.
func ensureGitignore(stateDir string) {
	if filepath.Base(stateDir) != ".cagent" {
		return
	}
	repoRoot := filepath.Dir(stateDir)
	gitignorePath := filepath.Join(repoRoot, ".gitignore")

	// Check if .gitignore already contains .cagent/
	if data, err := os.ReadFile(gitignorePath); err == nil {
		if strings.Contains(string(data), ".cagent") {
			return
		}
	}

	// Append cagent ignores — track DB, ignore bulky artifacts
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString("\n# cagent local state — track public DB, ignore private DB and artifacts\n.cagent/raw/\n.cagent/jobs/\n.cagent/transfers/\n.cagent/debriefs/\n.cagent/cagent.db-shm\n.cagent/cagent.db-wal\n.cagent/cagent-private.db\n.cagent/cagent-private.db-shm\n.cagent/cagent-private.db-wal\n")
}

func resolveDir(override, xdgBase, home, fallbackBase string) (string, error) {
	switch {
	case override != "":
		return expandUser(override)
	case xdgBase != "":
		return filepath.Join(xdgBase, "cagent"), nil
	default:
		return filepath.Join(home, fallbackBase, "cagent"), nil
	}
}

func expandUser(path string) (string, error) {
	switch {
	case path == "":
		return "", nil
	case path == "~":
		return os.UserHomeDir()
	case strings.HasPrefix(path, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[2:]), nil
	default:
		return path, nil
	}
}
