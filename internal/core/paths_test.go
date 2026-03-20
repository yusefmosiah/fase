package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePathsUsesCagentOverrides(t *testing.T) {
	env := map[string]string{
		"FASE_CONFIG_DIR": "/tmp/fase-config",
		"FASE_STATE_DIR":  "/tmp/fase-state",
		"FASE_CACHE_DIR":  "/tmp/fase-cache",
	}

	paths, err := ResolvePathsFromEnv("/Users/tester", func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("ResolvePathsFromEnv returned error: %v", err)
	}

	if paths.ConfigDir != "/tmp/fase-config" {
		t.Fatalf("expected config override, got %q", paths.ConfigDir)
	}
	if paths.StateDir != "/tmp/fase-state" {
		t.Fatalf("expected state override, got %q", paths.StateDir)
	}
	if paths.CacheDir != "/tmp/fase-cache" {
		t.Fatalf("expected cache override, got %q", paths.CacheDir)
	}
	if paths.DBPath != "/tmp/fase-state/fase.db" {
		t.Fatalf("expected DB path under state dir, got %q", paths.DBPath)
	}
}

func TestResolvePathsUsesXDGFallbacks(t *testing.T) {
	env := map[string]string{
		"XDG_CONFIG_HOME": "/tmp/xdg-config",
		"XDG_STATE_HOME":  "/tmp/xdg-state",
		"XDG_CACHE_HOME":  "/tmp/xdg-cache",
	}

	paths, err := ResolvePathsFromEnv("/Users/tester", func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("ResolvePathsFromEnv returned error: %v", err)
	}

	if paths.ConfigDir != "/tmp/xdg-config/fase" {
		t.Fatalf("expected XDG config dir, got %q", paths.ConfigDir)
	}
	if paths.StateDir != "/tmp/xdg-state/fase" {
		t.Fatalf("expected XDG state dir, got %q", paths.StateDir)
	}
	if paths.CacheDir != "/tmp/xdg-cache/fase" {
		t.Fatalf("expected XDG cache dir, got %q", paths.CacheDir)
	}
}

func TestResolvePathsUsesHomeFallbacks(t *testing.T) {
	paths, err := ResolvePathsFromEnv("/Users/tester", func(string) string { return "" })
	if err != nil {
		t.Fatalf("ResolvePathsFromEnv returned error: %v", err)
	}

	if paths.ConfigDir != filepath.Join("/Users/tester", ".config", "fase") {
		t.Fatalf("unexpected config dir: %q", paths.ConfigDir)
	}
	if paths.StateDir != filepath.Join("/Users/tester", ".local", "state", "fase") {
		t.Fatalf("unexpected state dir: %q", paths.StateDir)
	}
	if paths.CacheDir != filepath.Join("/Users/tester", ".cache", "fase") {
		t.Fatalf("unexpected cache dir: %q", paths.CacheDir)
	}
}

func TestResolvePathsSupportsLegacyOverrides(t *testing.T) {
	env := map[string]string{
		"CAGENT_CONFIG_DIR": "/tmp/cagent-config",
		"CAGENT_STATE_DIR":  "/tmp/cagent-state",
		"CAGENT_CACHE_DIR":  "/tmp/cagent-cache",
	}

	paths, err := ResolvePathsFromEnv("/Users/tester", func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("ResolvePathsFromEnv returned error: %v", err)
	}

	if paths.ConfigDir != "/tmp/cagent-config" {
		t.Fatalf("expected legacy config override, got %q", paths.ConfigDir)
	}
	if paths.StateDir != "/tmp/cagent-state" {
		t.Fatalf("expected legacy state override, got %q", paths.StateDir)
	}
	if paths.CacheDir != "/tmp/cagent-cache" {
		t.Fatalf("expected legacy cache override, got %q", paths.CacheDir)
	}
}

func TestResolveRepoStateDirFromPrefersFaseAndFallsBackToLegacy(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir git: %v", err)
	}

	legacy := filepath.Join(root, ".cagent")
	if err := os.Mkdir(legacy, 0o755); err != nil {
		t.Fatalf("mkdir legacy state dir: %v", err)
	}
	if got := ResolveRepoStateDirFrom(root); got != legacy {
		t.Fatalf("expected legacy state dir, got %q", got)
	}

	fase := filepath.Join(root, ".fase")
	if err := os.Mkdir(fase, 0o755); err != nil {
		t.Fatalf("mkdir fase state dir: %v", err)
	}
	if got := ResolveRepoStateDirFrom(root); got != fase {
		t.Fatalf("expected fase state dir, got %q", got)
	}
}
