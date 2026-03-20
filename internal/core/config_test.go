package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigParsesAdapterTraits(t *testing.T) {
	tempDir := t.TempDir()

	t.Setenv("FASE_CONFIG_DIR", tempDir)
	t.Setenv("FASE_STATE_DIR", filepath.Join(tempDir, "state"))
	t.Setenv("FASE_CACHE_DIR", filepath.Join(tempDir, "cache"))

	configPath := filepath.Join(tempDir, "config.toml")
	configBody := []byte(`
[adapters.codex]
binary = "codex"
enabled = true
summary = "primary code-editing adapter"
speed = "fast"
cost = "high"
tags = ["default", "tools"]

[[pricing.models]]
provider = "openai"
model = "gpt-5-mini"
input_usd_per_mtok = 0.25
output_usd_per_mtok = 2
cached_input_usd_per_mtok = 0.025
source = "manual"
`)
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.Adapters.Codex.Summary != "primary code-editing adapter" {
		t.Fatalf("unexpected summary: %q", cfg.Adapters.Codex.Summary)
	}
	if cfg.Adapters.Codex.Speed != "fast" {
		t.Fatalf("unexpected speed: %q", cfg.Adapters.Codex.Speed)
	}
	if cfg.Adapters.Codex.Cost != "high" {
		t.Fatalf("unexpected cost: %q", cfg.Adapters.Codex.Cost)
	}
	if len(cfg.Adapters.Codex.Tags) != 2 || cfg.Adapters.Codex.Tags[0] != "default" || cfg.Adapters.Codex.Tags[1] != "tools" {
		t.Fatalf("unexpected tags: %#v", cfg.Adapters.Codex.Tags)
	}
	if len(cfg.Pricing.Models) != 1 {
		t.Fatalf("expected one pricing override, got %d", len(cfg.Pricing.Models))
	}
	override := cfg.Pricing.Models[0]
	if override.Provider != "openai" || override.Model != "gpt-5-mini" {
		t.Fatalf("unexpected pricing override: %+v", override)
	}
	if override.InputUSDPerMTok != 0.25 || override.OutputUSDPerMTok != 2 {
		t.Fatalf("unexpected pricing override values: %+v", override)
	}
}
