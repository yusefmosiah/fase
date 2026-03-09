package core

import (
	"path/filepath"
	"testing"
)

func TestResolvePathsUsesCagentOverrides(t *testing.T) {
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
		t.Fatalf("expected config override, got %q", paths.ConfigDir)
	}
	if paths.StateDir != "/tmp/cagent-state" {
		t.Fatalf("expected state override, got %q", paths.StateDir)
	}
	if paths.CacheDir != "/tmp/cagent-cache" {
		t.Fatalf("expected cache override, got %q", paths.CacheDir)
	}
	if paths.DBPath != "/tmp/cagent-state/cagent.db" {
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

	if paths.ConfigDir != "/tmp/xdg-config/cagent" {
		t.Fatalf("expected XDG config dir, got %q", paths.ConfigDir)
	}
	if paths.StateDir != "/tmp/xdg-state/cagent" {
		t.Fatalf("expected XDG state dir, got %q", paths.StateDir)
	}
	if paths.CacheDir != "/tmp/xdg-cache/cagent" {
		t.Fatalf("expected XDG cache dir, got %q", paths.CacheDir)
	}
}

func TestResolvePathsUsesHomeFallbacks(t *testing.T) {
	paths, err := ResolvePathsFromEnv("/Users/tester", func(string) string { return "" })
	if err != nil {
		t.Fatalf("ResolvePathsFromEnv returned error: %v", err)
	}

	if paths.ConfigDir != filepath.Join("/Users/tester", ".config", "cagent") {
		t.Fatalf("unexpected config dir: %q", paths.ConfigDir)
	}
	if paths.StateDir != filepath.Join("/Users/tester", ".local", "state", "cagent") {
		t.Fatalf("unexpected state dir: %q", paths.StateDir)
	}
	if paths.CacheDir != filepath.Join("/Users/tester", ".cache", "cagent") {
		t.Fatalf("unexpected cache dir: %q", paths.CacheDir)
	}
}
