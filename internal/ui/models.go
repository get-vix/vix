package ui

import "strings"

// ModelInfo describes a single LLM model available for selection in the
// Settings tab. Spec is the prefixed identifier that gets sent on
// session.set_model — the picker never sends a bare model name.
type ModelInfo struct {
	Spec        string // full prefixed identifier, e.g. "anthropic/claude-opus-4-8"
	Provider    string // "anthropic" | "openai" | "openai-codex" | "openrouter" | "minimax" | "mimo"
	DisplayName string // human-readable label shown in the UI
}

// ProviderInfo describes one provider for the Settings tab provider column.
type ProviderInfo struct {
	Name        string // matches ModelInfo.Provider; also config.ProviderKey.Provider
	DisplayName string // human-readable label shown in the UI
}

// AvailableProviders is the static set of providers shown in the left
// column of the Settings tab Model section. Order matters — it's the
// order users see.
var AvailableProviders = []ProviderInfo{
	{Name: "anthropic", DisplayName: "Anthropic"},
	{Name: "openai", DisplayName: "OpenAI"},
	{Name: "openai-codex", DisplayName: "OpenAI Codex"},
	{Name: "openrouter", DisplayName: "OpenRouter"},
	{Name: "minimax", DisplayName: "MiniMax"},
	{Name: "mimo", DisplayName: "Xiaomi MiMo"},
}

// AvailableModels is the curated catalogue of selectable models. OpenRouter
// can route to anything; the entries here are popular routes — users with
// other targets set them via agent frontmatter.
var AvailableModels = []ModelInfo{
	// Anthropic
	{Spec: "anthropic/claude-opus-4-8", Provider: "anthropic", DisplayName: "Claude Opus 4.8"},
	{Spec: "anthropic/claude-opus-4-7", Provider: "anthropic", DisplayName: "Claude Opus 4.7"},
	{Spec: "anthropic/claude-opus-4-6", Provider: "anthropic", DisplayName: "Claude Opus 4.6"},
	{Spec: "anthropic/claude-opus-4-5", Provider: "anthropic", DisplayName: "Claude Opus 4.5"},
	{Spec: "anthropic/claude-sonnet-4-6", Provider: "anthropic", DisplayName: "Claude Sonnet 4.6"},
	{Spec: "anthropic/claude-sonnet-4-5", Provider: "anthropic", DisplayName: "Claude Sonnet 4.5"},
	{Spec: "anthropic/claude-haiku-4-6", Provider: "anthropic", DisplayName: "Claude Haiku 4.6"},
	{Spec: "anthropic/claude-haiku-4-5", Provider: "anthropic", DisplayName: "Claude Haiku 4.5"},
	{Spec: "anthropic/claude-opus-4-0", Provider: "anthropic", DisplayName: "Claude Opus 4.0"},
	{Spec: "anthropic/claude-sonnet-4-0", Provider: "anthropic", DisplayName: "Claude Sonnet 4.0"},
	// OpenAI
	{Spec: "openai/gpt-5.1", Provider: "openai", DisplayName: "GPT-5.1"},
	{Spec: "openai/gpt-5-thinking", Provider: "openai", DisplayName: "GPT-5 Thinking"},
	{Spec: "openai/o3", Provider: "openai", DisplayName: "o3"},
	{Spec: "openai/o4-mini", Provider: "openai", DisplayName: "o4 Mini"},
	{Spec: "openai/gpt-4o", Provider: "openai", DisplayName: "GPT-4o"},
	{Spec: "openai/gpt-4o-mini", Provider: "openai", DisplayName: "GPT-4o Mini"},
	// OpenAI Codex (ChatGPT subscription) — curated; the ChatGPT backend has no
	// public model-list endpoint, so these mirror llm.codexModels.
	{Spec: "openai-codex/gpt-5-codex", Provider: "openai-codex", DisplayName: "GPT-5 Codex"},
	{Spec: "openai-codex/gpt-5", Provider: "openai-codex", DisplayName: "GPT-5"},
	// OpenRouter — curated popular routes; arbitrary slugs go via agent frontmatter.
	{Spec: "openrouter/anthropic/claude-opus-4-8", Provider: "openrouter", DisplayName: "Claude Opus 4.8 (via OpenRouter)"},
	{Spec: "openrouter/anthropic/claude-sonnet-4-6", Provider: "openrouter", DisplayName: "Claude Sonnet 4.6 (via OpenRouter)"},
	{Spec: "openrouter/openai/gpt-5.1", Provider: "openrouter", DisplayName: "GPT-5.1 (via OpenRouter)"},
	{Spec: "openrouter/openai/o3", Provider: "openrouter", DisplayName: "o3 (via OpenRouter)"},
	{Spec: "openrouter/google/gemini-2-flash", Provider: "openrouter", DisplayName: "Gemini 2 Flash (via OpenRouter)"},
	// MiniMax
	{Spec: "minimax/MiniMax-M2.7", Provider: "minimax", DisplayName: "MiniMax M2.7"},
	{Spec: "minimax/MiniMax-M2.7-highspeed", Provider: "minimax", DisplayName: "MiniMax M2.7 (highspeed)"},
	{Spec: "minimax/MiniMax-M2.5", Provider: "minimax", DisplayName: "MiniMax M2.5"},
	// Xiaomi MiMo
	{Spec: "mimo/mimo-v2.5-pro", Provider: "mimo", DisplayName: "MiMo v2.5 Pro"},
	{Spec: "mimo/mimo-v2.5", Provider: "mimo", DisplayName: "MiMo v2.5"},
	{Spec: "mimo/mimo-v2-flash", Provider: "mimo", DisplayName: "MiMo v2 Flash"},
}

// providerCatalog is the per-provider result of a live model fetch: whether a
// usable credential was found, and the models returned.
type providerCatalog struct {
	authenticated bool
	models        []ModelInfo
}

// providerAuthHint returns short, actionable lines telling the user how to
// authenticate a provider so its models can be fetched. Shown in the picker
// when a provider has no usable credential.
func providerAuthHint(provider string) []string {
	switch provider {
	case "anthropic":
		return []string{"run: vix login anthropic", "or set ANTHROPIC_API_KEY"}
	case "openai":
		return []string{"set OPENAI_API_KEY"}
	case "openai-codex":
		return []string{"run: vix login openai-codex"}
	case "openrouter":
		return []string{"set OPENROUTER_API_KEY"}
	case "minimax":
		return []string{"set MINIMAX_API_KEY"}
	case "mimo":
		return []string{"set MIMO_API_KEY"}
	}
	return []string{"no models available"}
}

// ModelsForProvider returns the entries in AvailableModels whose Provider
// matches the given provider name, in declaration order. Returns nil for
// an unknown provider.
func ModelsForProvider(provider string) []ModelInfo {
	var out []ModelInfo
	for _, m := range AvailableModels {
		if m.Provider == provider {
			out = append(out, m)
		}
	}
	return out
}

// ProviderOf returns the provider name embedded in a model spec. For
// "openrouter/anthropic/claude-..." the answer is "openrouter" — the
// provider WE talk to, not the upstream routed-through service. Returns
// "" when the spec has no prefix.
func ProviderOf(spec string) string {
	i := strings.Index(spec, "/")
	if i <= 0 {
		return ""
	}
	return spec[:i]
}

// locateActiveModel returns the (providerSel, modelSel) cursor coordinates
// for spec in the two-column Settings picker. When spec isn't in the
// curated catalogue, returns (providerIdxOfSpecPrefix, 0) so the cursor
// lands on the right provider even when the exact model isn't shown
// (e.g. user-installed agent uses a non-curated OpenRouter route).
// Falls back to (0, 0) when even the prefix doesn't match a known provider.
func locateActiveModel(spec string) (providerIdx, modelIdx int) {
	for pi, p := range AvailableProviders {
		models := ModelsForProvider(p.Name)
		for mi, mod := range models {
			if mod.Spec == spec {
				return pi, mi
			}
		}
	}
	// No exact match — fall back to the provider prefix at least.
	specProv := ProviderOf(spec)
	for pi, p := range AvailableProviders {
		if p.Name == specProv {
			return pi, 0
		}
	}
	return 0, 0
}
