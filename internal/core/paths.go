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
	projectStateDirName = ".cogent"
	projectSlug         = "cogent"
)

func ResolvePaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}

	return ResolvePathsFromEnv(home, os.Getenv)
}

// ResolveRepoStateDir finds the git repo root from cwd and returns the
// repository-local .cogent state directory. If COGENT_STATE_DIR is set, it
// is returned directly so that isolated environments (e.g. tests) always
// resolve to the configured state directory.
func ResolveRepoStateDir() string {
	if envDir := os.Getenv("COGENT_STATE_DIR"); envDir != "" {
		return envDir
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return ResolveRepoStateDirFrom(dir)
}

// ResolveRepoStateDirFrom walks upward from startDir and returns the repo-local
// .cogent state directory.
func ResolveRepoStateDirFrom(startDir string) string {
	if repoRoot := repoRootFromStartDir(startDir); repoRoot != "" {
		return filepath.Join(repoRoot, projectStateDirName)
	}
	return ""
}

// ResolvePathsForRepo resolves paths scoped to the current git repo.
// If in a git repo, uses <repo-root>/.cogent/ for state.
// Otherwise falls back to global XDG paths.
func ResolvePathsForRepo() (Paths, error) {
	// Explicit override always wins
	if os.Getenv("COGENT_STATE_DIR") != "" {
		return ResolvePaths()
	}
	if cwd, err := os.Getwd(); err == nil {
		_ = MigrateLegacyRepoStateDirFrom(cwd, nil)
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

	configDir, err := resolveDir(getenv("COGENT_CONFIG_DIR"), getenv("XDG_CONFIG_HOME"), home, ".config")
	if err != nil {
		return Paths{}, fmt.Errorf("resolve config dir: %w", err)
	}

	stateDir, err := resolveDir(getenv("COGENT_STATE_DIR"), getenv("XDG_STATE_HOME"), home, filepath.Join(".local", "state"))
	if err != nil {
		return Paths{}, fmt.Errorf("resolve state dir: %w", err)
	}

	cacheDir, err := resolveDir(getenv("COGENT_CACHE_DIR"), getenv("XDG_CACHE_HOME"), home, ".cache")
	if err != nil {
		return Paths{}, fmt.Errorf("resolve cache dir: %w", err)
	}

	return Paths{
		ConfigDir:     configDir,
		StateDir:      stateDir,
		CacheDir:      cacheDir,
		ConfigPath:    filepath.Join(configDir, "config.toml"),
		DBPath:        filepath.Join(stateDir, stateFilePrefix(stateDir)+".db"),
		PrivateDBPath: filepath.Join(stateDir, stateFilePrefix(stateDir)+"-private.db"),
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
	if err := migrateLegacyStateFiles(paths.StateDir, nil); err != nil {
		return err
	}

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
	if base != projectStateDirName {
		return
	}
	repoRoot := filepath.Dir(stateDir)
	gitignorePath := filepath.Join(repoRoot, ".gitignore")

	if data, err := os.ReadFile(gitignorePath); err == nil {
		if strings.Contains(string(data), projectStateDirName+"/") {
			return
		}
	}

	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString("\n# cogent local state — track public DB, ignore private DB and artifacts\n.cogent/raw/\n.cogent/jobs/\n.cogent/transfers/\n.cogent/debriefs/\n.cogent/cogent.db-shm\n.cogent/cogent.db-wal\n.cogent/cogent-private.db\n.cogent/cogent-private.db-shm\n.cogent/cogent-private.db-wal\n")
}

func resolveDir(override, xdgBase, home, fallbackBase string) (string, error) {
	switch {
	case override != "":
		return expandUser(override)
	case xdgBase != "":
		return filepath.Join(xdgBase, projectSlug), nil
	default:
		return filepath.Join(home, fallbackBase, projectSlug), nil
	}
}

func stateFilePrefix(_ string) string {
	return projectSlug
}

func MigrateLegacyRepoStateDirFrom(startDir string, logf func(string, ...any)) error {
	return nil
}

func migrateLegacyStateFiles(stateDir string, logf func(string, ...any)) error {
	return nil
}

func repoRootFromStartDir(startDir string) string {
	dir := startDir
	for {
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func logMigration(logf func(string, ...any), format string, args ...any) {
	if logf != nil {
		logf(format, args...)
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
