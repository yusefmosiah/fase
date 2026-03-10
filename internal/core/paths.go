package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Paths struct {
	ConfigDir    string
	StateDir     string
	CacheDir     string
	ConfigPath   string
	DBPath       string
	JobsDir      string
	RawDir       string
	TransfersDir string
}

func ResolvePaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}

	return ResolvePathsFromEnv(home, os.Getenv)
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
		ConfigDir:    configDir,
		StateDir:     stateDir,
		CacheDir:     cacheDir,
		ConfigPath:   filepath.Join(configDir, "config.toml"),
		DBPath:       filepath.Join(stateDir, "cagent.db"),
		JobsDir:      filepath.Join(stateDir, "jobs"),
		RawDir:       filepath.Join(stateDir, "raw"),
		TransfersDir: filepath.Join(stateDir, "transfers"),
	}, nil
}

func (p Paths) WithStateDir(stateDir string) Paths {
	p.StateDir = stateDir
	p.DBPath = filepath.Join(stateDir, "cagent.db")
	p.JobsDir = filepath.Join(stateDir, "jobs")
	p.RawDir = filepath.Join(stateDir, "raw")
	p.TransfersDir = filepath.Join(stateDir, "transfers")
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
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %q: %w", dir, err)
		}
	}

	return nil
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
