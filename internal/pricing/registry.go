package pricing

import (
	"strings"
	"time"

	"github.com/yusefmosiah/cogent/internal/core"
)

var (
	openAIObservedAt    = time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC)
	anthropicObservedAt = time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC)
	googleObservedAt    = time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC)
)

type key struct {
	provider string
	model    string
}

var builtin = map[key]core.ModelPricing{
	{provider: "openai", model: "gpt-5-mini"}: {
		Currency:              "USD",
		InputUSDPerMTok:       0.25,
		OutputUSDPerMTok:      2.0,
		CachedInputUSDPerMTok: 0.025,
		Source:                "builtin_official_snapshot",
		SourceURL:             "https://openai.com/api/pricing/",
		ObservedAt:            &openAIObservedAt,
	},
	{provider: "openai", model: "gpt-5-nano"}: {
		Currency:              "USD",
		InputUSDPerMTok:       0.05,
		OutputUSDPerMTok:      0.4,
		CachedInputUSDPerMTok: 0.005,
		Source:                "builtin_official_snapshot",
		SourceURL:             "https://platform.openai.com/pricing",
		ObservedAt:            &openAIObservedAt,
	},
	{provider: "openai", model: "gpt-4o-mini"}: {
		Currency:              "USD",
		InputUSDPerMTok:       0.15,
		OutputUSDPerMTok:      0.6,
		CachedInputUSDPerMTok: 0.075,
		Source:                "builtin_official_snapshot",
		SourceURL:             "https://openai.com/api/pricing/",
		ObservedAt:            &openAIObservedAt,
	},
	{provider: "anthropic", model: "claude-sonnet-4-6"}: {
		Currency:            "USD",
		InputUSDPerMTok:     3.0,
		OutputUSDPerMTok:    15.0,
		CacheReadUSDPerMTok: 0.3,
		Source:              "builtin_official_snapshot",
		SourceURL:           "https://www.anthropic.com/pricing",
		ObservedAt:          &anthropicObservedAt,
	},
	{provider: "anthropic", model: "claude-opus-4-6"}: {
		Currency:            "USD",
		InputUSDPerMTok:     15.0,
		OutputUSDPerMTok:    75.0,
		CacheReadUSDPerMTok: 1.5,
		Source:              "builtin_official_snapshot",
		SourceURL:           "https://www.anthropic.com/pricing",
		ObservedAt:          &anthropicObservedAt,
	},
	{provider: "google", model: "gemini-2.5-flash"}: {
		Currency:         "USD",
		InputUSDPerMTok:  0.3,
		OutputUSDPerMTok: 2.5,
		Source:           "builtin_official_snapshot",
		SourceURL:        "https://ai.google.dev/gemini-api/docs/pricing",
		ObservedAt:       &googleObservedAt,
	},
	{provider: "google", model: "gemini-2.5-flash-lite"}: {
		Currency:         "USD",
		InputUSDPerMTok:  0.1,
		OutputUSDPerMTok: 0.4,
		Source:           "builtin_official_snapshot",
		SourceURL:        "https://ai.google.dev/gemini-api/docs/pricing",
		ObservedAt:       &googleObservedAt,
	},
}

func Resolve(cfg core.Config, provider, model string) *core.ModelPricing {
	provider = normalizeProvider(provider)
	model = strings.TrimSpace(strings.ToLower(model))
	if provider == "" || model == "" {
		return nil
	}

	for _, override := range cfg.Pricing.Models {
		if normalizeProvider(override.Provider) != provider || strings.ToLower(strings.TrimSpace(override.Model)) != model {
			continue
		}
		pricing := core.ModelPricing{
			Currency:                "USD",
			InputUSDPerMTok:         override.InputUSDPerMTok,
			OutputUSDPerMTok:        override.OutputUSDPerMTok,
			CachedInputUSDPerMTok:   override.CachedInputUSDPerMTok,
			CacheReadUSDPerMTok:     override.CacheReadUSDPerMTok,
			CacheCreationUSDPerMTok: override.CacheCreationUSDPerMTok,
			Source:                  override.Source,
			SourceURL:               override.SourceURL,
		}
		if pricing.Source == "" {
			pricing.Source = "config_override"
		}
		return &pricing
	}

	pricing, ok := builtin[key{provider: provider, model: model}]
	if !ok {
		return nil
	}
	copy := pricing
	return &copy
}

func Estimate(usage core.UsageReport, pricing *core.ModelPricing) *core.CostEstimate {
	if pricing == nil {
		return nil
	}

	cost := &core.CostEstimate{
		Currency:   "USD",
		Estimated:  true,
		Source:     pricing.Source,
		SourceURL:  pricing.SourceURL,
		ObservedAt: pricing.ObservedAt,
	}
	cost.InputCostUSD = float64(usage.InputTokens) * pricing.InputUSDPerMTok / 1_000_000
	cost.OutputCostUSD = float64(usage.OutputTokens) * pricing.OutputUSDPerMTok / 1_000_000
	cost.CachedInputCostUSD = float64(usage.CachedInputTokens) * pricing.CachedInputUSDPerMTok / 1_000_000

	cacheReadRate := pricing.CacheReadUSDPerMTok
	if cacheReadRate == 0 {
		cacheReadRate = pricing.CachedInputUSDPerMTok
	}
	cost.CacheReadCostUSD = float64(usage.CacheReadInputTokens) * cacheReadRate / 1_000_000

	cacheCreationRate := pricing.CacheCreationUSDPerMTok
	if cacheCreationRate == 0 {
		cacheCreationRate = pricing.InputUSDPerMTok
	}
	cost.CacheCreationCostUSD = float64(usage.CacheCreationInputTokens) * cacheCreationRate / 1_000_000

	cost.TotalCostUSD = cost.InputCostUSD + cost.OutputCostUSD + cost.CachedInputCostUSD + cost.CacheReadCostUSD + cost.CacheCreationCostUSD
	if cost.TotalCostUSD == 0 {
		return nil
	}
	return cost
}

func normalizeProvider(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "firstparty", "claude.ai":
		return "anthropic"
	default:
		return value
	}
}
