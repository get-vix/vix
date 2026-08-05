package ui

import (
	"strings"

	"github.com/get-vix/vix/internal/providers"
)

// ModelInfo describes a single LLM model available for selection in the
// Settings tab. Spec is the prefixed identifier that gets sent on
// thread.set_model — the picker never sends a bare model name.
type ModelInfo struct {
	Spec        string // full prefixed identifier, e.g. "anthropic/claude-opus-4-8"
	Provider    string // "anthropic" | "openai" | "openrouter" | "minimax" | "mimo"
	DisplayName string // human-readable label shown in the UI
}

// ProviderInfo describes one provider for the Settings tab provider column.
type ProviderInfo struct {
	Name        string // matches ModelInfo.Provider; also config.ProviderKey.Provider
	DisplayName string // human-readable label shown in the UI
	Local       bool   // user-run local server (Ollama, llama.cpp): grouped separately, models discovered live
}

// AvailableProviders returns the providers shown in the left column of the
// Settings tab Model section, in registry (providers.json) order. The order is
// what users see.
func AvailableProviders() []ProviderInfo {
	specs := providers.Default().All()
	out := make([]ProviderInfo, 0, len(specs))
	for _, p := range specs {
		out = append(out, ProviderInfo{Name: p.ID, DisplayName: p.DisplayName, Local: p.Local})
	}
	return out
}

// DisplayNameForProvider returns the human-readable label for a provider name,
// falling back to the raw name when it isn't a known provider.
func DisplayNameForProvider(name string) string {
	if p, ok := providers.Default().Lookup(name); ok {
		return p.DisplayName
	}
	return name
}

// IsLocalProvider reports whether a provider id is backed by a user-run local
// server (Ollama, llama.cpp).
func IsLocalProvider(name string) bool {
	p, ok := providers.Default().Lookup(name)
	return ok && p.Local
}

// LocalProviderUI is the Models-tab view state for one local provider, built
// from the daemon's providers.local_status probe. Until the first probe result
// arrives Fetched is false and the provider renders as "probing".
type LocalProviderUI struct {
	BaseURL   string      // resolved server endpoint, for display
	Reachable bool        // the server answered the probe
	Fetched   bool        // a probe result (success or failure) has arrived
	Models    []ModelInfo // live-discovered models, specs prefixed
}

// AvailableModels returns the curated catalogue of selectable models, sourced
// from the providers registry (embedded providers.json plus any ~/.vix and
// ./.vix overlays). There is no runtime fetch of provider model lists.
// OpenRouter can route to anything; the catalogue entries are popular routes —
// users with other targets set them via agent frontmatter.
func AvailableModels() []ModelInfo {
	var out []ModelInfo
	for _, p := range providers.Default().All() {
		for _, m := range p.Models {
			out = append(out, ModelInfo{Spec: m.Spec, Provider: p.ID, DisplayName: m.DisplayName})
		}
	}
	return out
}

// modelGridCols is the number of columns in the Settings tab model picker
// grid; models are laid out row-major across this many columns. Cursor
// navigation and the renderer both derive from it.
const modelGridCols = 3

// ModelsForProvider returns the catalogue entries for the given provider id, in
// registry order. Returns nil for an unknown provider.
func ModelsForProvider(provider string) []ModelInfo {
	p, ok := providers.Default().Lookup(provider)
	if !ok {
		return nil
	}
	out := make([]ModelInfo, 0, len(p.Models))
	for _, m := range p.Models {
		out = append(out, ModelInfo{Spec: m.Spec, Provider: p.ID, DisplayName: m.DisplayName})
	}
	return out
}

// DisplayModelsForProvider returns the models shown in the Settings grid for a
// provider. Both the renderer and the cursor navigation use this so selection
// indices stay consistent.
func DisplayModelsForProvider(provider string) []ModelInfo {
	return ModelsForProvider(provider)
}

// FilterModels returns the entries whose DisplayName contains query
// (case-insensitive). An empty query returns models unchanged. The Spec is not
// matched: its provider prefix (e.g. "openai/") would match common query
// letters and surface unrelated models.
func FilterModels(models []ModelInfo, query string) []ModelInfo {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return models
	}
	var out []ModelInfo
	for _, m := range models {
		if strings.Contains(strings.ToLower(m.DisplayName), query) {
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
	providerList := AvailableProviders()
	for pi, p := range providerList {
		models := DisplayModelsForProvider(p.Name)
		for mi, mod := range models {
			if mod.Spec == spec {
				return pi, mi
			}
		}
	}
	specProv := ProviderOf(spec)
	for pi, p := range providerList {
		if p.Name == specProv {
			return pi, 0
		}
	}
	return 0, 0
}
