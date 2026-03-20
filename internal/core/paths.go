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

const (
	projectStateDirName = ".fase"
	legacyStateDirName  = ".cagent"
	projectSlug         = "fase"
	legacyProjectSlug   = "cagent"
)

func ResolvePaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}

	return ResolvePathsFromEnv(home, os.Getenv)
}

// ResolveRepoStateDir finds the git repo root from cwd and returns the
// repository-local state directory. New repos use .fase; legacy repos keep
// using .cagent until they are migrated.
func ResolveRepoStateDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return ResolveRepoStateDirFrom(dir)
}

// ResolveRepoStateDirFrom walks upward from startDir and returns the repo-local
// state directory. New repos prefer .fase; existing .cagent state is preserved.
func ResolveRepoStateDirFrom(startDir string) string {
	dir := startDir
	for {
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
			faseDir := filepath.Join(dir, projectStateDirName)
			legacyDir := filepath.Join(dir, legacyStateDirName)
			switch {
			case dirExists(faseDir):
				return faseDir
			case dirExists(legacyDir):
				return legacyDir
			default:
				return faseDir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ResolvePathsForRepo resolves paths scoped to the current git repo.
// If in a git repo, uses <repo-root>/.fase/ for state.
// Otherwise falls back to global XDG paths.
func ResolvePathsForRepo() (Paths, error) {
	// Explicit override always wins
	if getenvAny(os.Getenv, "FASE_STATE_DIR", "CAGENT_STATE_DIR") != "" {
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

	configDir, err := resolveDir(getenvAny(getenv, "FASE_CONFIG_DIR", "CAGENT_CONFIG_DIR"), getenv("XDG_CONFIG_HOME"), home, ".config")
	if err != nil {
		return Paths{}, fmt.Errorf("resolve config dir: %w", err)
	}

	stateDir, err := resolveDir(getenvAny(getenv, "FASE_STATE_DIR", "CAGENT_STATE_DIR"), getenv("XDG_STATE_HOME"), home, filepath.Join(".local", "state"))
	if err != nil {
		return Paths{}, fmt.Errorf("resolve state dir: %w", err)
	}

	cacheDir, err := resolveDir(getenvAny(getenv, "FASE_CACHE_DIR", "CAGENT_CACHE_DIR"), getenv("XDG_CACHE_HOME"), home, ".cache")
	if err != nil {
		return Paths{}, fmt.Errorf("resolve cache dir: %w", err)
	}

	return Paths{
		ConfigDir:     configDir,
		StateDir:      stateDir,
		CacheDir:      cacheDir,
		ConfigPath:    filepath.Join(configDir, "config.toml"),
		DBPath:        filepath.Join(stateDir, "fase.db"),
		PrivateDBPath: filepath.Join(stateDir, "fase-private.db"),
		JobsDir:       filepath.Join(stateDir, "jobs"),
		RawDir:        filepath.Join(stateDir, "raw"),
		TransfersDir:  filepath.Join(stateDir, "transfers"),
		DebriefsDir:   filepath.Join(stateDir, "debriefs"),
	}, nil
}

func (p Paths) WithStateDir(stateDir string) Paths {
	p.StateDir = stateDir
	p.DBPath = filepath.Join(stateDir, stateFilePrefix(stateDir)+".db")
	p.PrivateDBPath = filepath.Join(stateDir, stateFilePrefix(stateDir)+"-private.db")
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

	// Auto-add repo-local state directories to .gitignore if state dir is inside a git repo.
	ensureGitignore(paths.StateDir)

	return nil
}

// ensureGitignore adds repo-local state directories to .gitignore if the state
// dir lives inside a git repo.
func ensureGitignore(stateDir string) {
	base := filepath.Base(stateDir)
	if base != projectStateDirName && base != legacyStateDirName {
		return
	}
	repoRoot := filepath.Dir(stateDir)
	gitignorePath := filepath.Join(repoRoot, ".gitignore")

	// Check if .gitignore already contains both state directory variants.
	if data, err := os.ReadFile(gitignorePath); err == nil {
		text := string(data)
		if strings.Contains(text, ".fase/") && strings.Contains(text, ".cagent/") {
			return
		}
	}

	// Append FASE ignores and legacy cagent ignores so old repos keep working.
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString("\n# fase local state — track public DB, ignore private DB and artifacts\n.fase/raw/\n.fase/jobs/\n.fase/transfers/\n.fase/debriefs/\n.fase/fase.db-shm\n.fase/fase.db-wal\n.fase/fase-private.db\n.fase/fase-private.db-shm\n.fase/fase-private.db-wal\n\n# legacy cagent state — preserved for compatibility\n.cagent/raw/\n.cagent/jobs/\n.cagent/transfers/\n.cagent/debriefs/\n.cagent/cagent.db-shm\n.cagent/cagent.db-wal\n.cagent/cagent-private.db\n.cagent/cagent-private.db-shm\n.cagent/cagent-private.db-wal\n")
}

func resolveDir(override, xdgBase, home, fallbackBase string) (string, error) {
	switch {
	case override != "":
		return expandUser(override)
	case xdgBase != "":
		candidate := filepath.Join(xdgBase, projectSlug)
		if !dirExists(candidate) {
			legacyCandidate := filepath.Join(xdgBase, legacyProjectSlug)
			if dirExists(legacyCandidate) {
				return legacyCandidate, nil
			}
		}
		return candidate, nil
	default:
		candidate := filepath.Join(home, fallbackBase, projectSlug)
		if !dirExists(candidate) {
			legacyCandidate := filepath.Join(home, fallbackBase, legacyProjectSlug)
			if dirExists(legacyCandidate) {
				return legacyCandidate, nil
			}
		}
		return candidate, nil
	}
}

func getenvAny(getenv func(string) string, keys ...string) string {
	for _, key := range keys {
		if value := getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func stateFilePrefix(stateDir string) string {
	if filepath.Base(stateDir) == legacyStateDirName {
		return legacyProjectSlug
	}
	return projectSlug
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
