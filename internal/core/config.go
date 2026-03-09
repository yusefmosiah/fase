package core

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Store    StoreConfig    `toml:"store"`
	Defaults DefaultsConfig `toml:"defaults"`
	Adapters AdaptersConfig `toml:"adapters"`
}

type StoreConfig struct {
	StateDir string `toml:"state_dir"`
}

type DefaultsConfig struct {
	Detached bool `toml:"detached"`
	JSON     bool `toml:"json"`
}

type AdaptersConfig struct {
	Codex    AdapterConfig `toml:"codex"`
	Claude   AdapterConfig `toml:"claude"`
	Factory  AdapterConfig `toml:"factory"`
	Pi       AdapterConfig `toml:"pi"`
	PiRust   AdapterConfig `toml:"pi_rust"`
	Gemini   AdapterConfig `toml:"gemini"`
	OpenCode AdapterConfig `toml:"opencode"`
}

type AdapterConfig struct {
	Binary  string `toml:"binary"`
	Enabled bool   `toml:"enabled"`
}

func DefaultConfig(paths Paths) Config {
	return Config{
		Store: StoreConfig{
			StateDir: paths.StateDir,
		},
		Defaults: DefaultsConfig{
			Detached: true,
			JSON:     false,
		},
		Adapters: AdaptersConfig{
			Codex:    AdapterConfig{Binary: "codex", Enabled: true},
			Claude:   AdapterConfig{Binary: "claude", Enabled: true},
			Factory:  AdapterConfig{Binary: "droid", Enabled: true},
			Pi:       AdapterConfig{Binary: "pi", Enabled: true},
			PiRust:   AdapterConfig{Binary: "pi", Enabled: false},
			Gemini:   AdapterConfig{Binary: "gemini", Enabled: true},
			OpenCode: AdapterConfig{Binary: "opencode", Enabled: false},
		},
	}
}

func LoadConfig(path string) (Config, error) {
	paths, err := ResolvePaths()
	if err != nil {
		return Config{}, err
	}

	cfg := DefaultConfig(paths)
	target := path
	if target == "" {
		target = paths.ConfigPath
	}

	target, err = expandUser(target)
	if err != nil {
		return Config{}, fmt.Errorf("expand config path: %w", err)
	}

	if _, err := os.Stat(target); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("stat config %q: %w", target, err)
	}

	if _, err := toml.DecodeFile(target, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", target, err)
	}

	return cfg, nil
}
