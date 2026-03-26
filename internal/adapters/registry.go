package adapters

import (
	"context"
	"sort"

	"github.com/yusefmosiah/cogent/internal/adapterapi"
	"github.com/yusefmosiah/cogent/internal/adapters/claude"
	"github.com/yusefmosiah/cogent/internal/adapters/codex"
	"github.com/yusefmosiah/cogent/internal/adapters/factory"
	"github.com/yusefmosiah/cogent/internal/adapters/gemini"
	"github.com/yusefmosiah/cogent/internal/adapters/native"
	"github.com/yusefmosiah/cogent/internal/adapters/opencode"
	"github.com/yusefmosiah/cogent/internal/adapters/pi"
	"github.com/yusefmosiah/cogent/internal/core"
)

type Capabilities = adapterapi.Capabilities
type Diagnosis = adapterapi.Diagnosis

func CatalogFromConfig(cfg core.Config) []Diagnosis {
	entries := []Diagnosis{
		describeAdapter(context.Background(), claude.New(cfg.Adapters.Claude.Binary, cfg.Adapters.Claude.Enabled)),
		describeAdapter(context.Background(), factory.New(cfg.Adapters.Factory.Binary, cfg.Adapters.Factory.Enabled)),
		describeAdapter(context.Background(), gemini.New(cfg.Adapters.Gemini.Binary, cfg.Adapters.Gemini.Enabled)),
		describeAdapter(context.Background(), native.New(cfg.Adapters.Native.Binary, cfg.Adapters.Native.Enabled)),
		describeAdapter(context.Background(), opencode.New(cfg.Adapters.OpenCode.Binary, cfg.Adapters.OpenCode.Enabled)),
		describeAdapter(context.Background(), pi.New(cfg.Adapters.Pi.Binary, cfg.Adapters.Pi.Enabled)),
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
	case "factory":
		adapter = factory.New(cfg.Adapters.Factory.Binary, cfg.Adapters.Factory.Enabled)
	case "gemini":
		adapter = gemini.New(cfg.Adapters.Gemini.Binary, cfg.Adapters.Gemini.Enabled)
	case "native":
		adapter = native.New(cfg.Adapters.Native.Binary, cfg.Adapters.Native.Enabled)
	case "opencode":
		adapter = opencode.New(cfg.Adapters.OpenCode.Binary, cfg.Adapters.OpenCode.Enabled)
	case "pi":
		adapter = pi.New(cfg.Adapters.Pi.Binary, cfg.Adapters.Pi.Enabled)
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
