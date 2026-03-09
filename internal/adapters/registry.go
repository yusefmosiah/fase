package adapters

import (
	"context"
	"os/exec"
	"sort"

	"github.com/yusefmosiah/cagent/internal/adapterapi"
	"github.com/yusefmosiah/cagent/internal/adapters/claude"
	"github.com/yusefmosiah/cagent/internal/adapters/codex"
	"github.com/yusefmosiah/cagent/internal/core"
)

type Capabilities = adapterapi.Capabilities
type Diagnosis = adapterapi.Diagnosis

func CatalogFromConfig(cfg core.Config) []Diagnosis {
	entries := []Diagnosis{
		describeAdapter(context.Background(), claude.New(cfg.Adapters.Claude.Binary, cfg.Adapters.Claude.Enabled)),
		describeStatic("factory", cfg.Adapters.Factory),
		describeStatic("gemini", cfg.Adapters.Gemini),
		describeStatic("opencode", cfg.Adapters.OpenCode),
		describeStatic("pi", cfg.Adapters.Pi),
		describeStatic("pi_rust", cfg.Adapters.PiRust),
		describeAdapter(context.Background(), codex.New(cfg.Adapters.Codex.Binary, cfg.Adapters.Codex.Enabled)),
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Adapter < entries[j].Adapter
	})

	return entries
}

func Resolve(ctx context.Context, cfg core.Config, name string) (adapterapi.Adapter, Diagnosis, bool) {
	var adapter adapterapi.Adapter

	switch name {
	case "claude":
		adapter = claude.New(cfg.Adapters.Claude.Binary, cfg.Adapters.Claude.Enabled)
	case "codex":
		adapter = codex.New(cfg.Adapters.Codex.Binary, cfg.Adapters.Codex.Enabled)
	default:
		return nil, Diagnosis{}, false
	}

	diag, _ := adapter.Detect(ctx)

	return adapter, diag, true
}

func describeAdapter(ctx context.Context, adapter adapterapi.Adapter) Diagnosis {
	diag, _ := adapter.Detect(ctx)
	return diag
}

func describeStatic(name string, cfg core.AdapterConfig) Diagnosis {
	_, err := exec.LookPath(cfg.Binary)
	return Diagnosis{
		Adapter:      name,
		Binary:       cfg.Binary,
		Available:    err == nil,
		Enabled:      cfg.Enabled,
		Implemented:  false,
		Capabilities: adapterapi.Capabilities{},
	}
}
