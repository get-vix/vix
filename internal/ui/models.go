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

// AvailableProviders is the static set of providers shown in the left column of
// the Settings tab Model section. Order matters — it's the order users see.
// The models themselves are NOT hardcoded; they're fetched live per provider
// (see Model.modelsForProvider / the daemon's list_models RPC).
var AvailableProviders = []ProviderInfo{
	{Name: "anthropic", DisplayName: "Anthropic"},
	{Name: "openai", DisplayName: "OpenAI"},
	{Name: "openai-codex", DisplayName: "OpenAI Codex"},
	{Name: "openrouter", DisplayName: "OpenRouter"},
	{Name: "minimax", DisplayName: "MiniMax"},
	{Name: "mimo", DisplayName: "Xiaomi MiMo"},
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

// locateActiveModel returns the (providerSel, modelSel) cursor coordinates for
// spec in the two-column Settings picker. The model catalogue is fetched live
// and isn't known when the picker opens, so this resolves only the provider
// column (from spec's prefix) and starts the model cursor at row 0. Falls back
// to (0, 0) when the prefix doesn't match a known provider.
func locateActiveModel(spec string) (providerIdx, modelIdx int) {
	specProv := ProviderOf(spec)
	for pi, p := range AvailableProviders {
		if p.Name == specProv {
			return pi, 0
		}
	}
	return 0, 0
}
