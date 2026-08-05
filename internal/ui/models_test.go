package ui

import (
	"strings"
	"testing"
)

// TestAvailableModels_AllPrefixed asserts every catalogue entry's Spec
// starts with its declared Provider name plus a slash. The settings UI
// dispatches Spec verbatim to thread.set_model; if a row leaks a bare
// name through, ParseModel rejects it and the model picker breaks.
func TestAvailableModels_AllPrefixed(t *testing.T) {
	for _, m := range AvailableModels() {
		wantPrefix := m.Provider + "/"
		if !strings.HasPrefix(m.Spec, wantPrefix) {
			t.Errorf("model %q has Provider=%q but Spec doesn't start with %q", m.DisplayName, m.Provider, wantPrefix)
		}
	}
}

// TestModelsForProvider_GroupsCorrectly asserts the filter returns models
// whose Provider matches AND covers every provider in AvailableProviders.
// Local providers (Ollama, llama.cpp) are exempt from the non-empty check:
// their models are discovered live from the running server, not catalogued.
func TestModelsForProvider_GroupsCorrectly(t *testing.T) {
	for _, p := range AvailableProviders() {
		models := ModelsForProvider(p.Name)
		if len(models) == 0 && !p.Local {
			t.Errorf("provider %q has no models in AvailableModels", p.Name)
		}
		for _, m := range models {
			if m.Provider != p.Name {
				t.Errorf("ModelsForProvider(%q) returned a model whose Provider=%q", p.Name, m.Provider)
			}
		}
	}
}

// TestProviderOf covers the prefix extraction including the OpenRouter
// nested-route case (we want the provider WE talk to, not the upstream).
func TestProviderOf(t *testing.T) {
	cases := []struct {
		spec string
		want string
	}{
		{"anthropic/claude-opus-4-8", "anthropic"},
		{"openrouter/anthropic/claude-opus-4-8", "openrouter"},
		{"openai/o3", "openai"},
		{"minimax/MiniMax-M2.7", "minimax"},
		{"claude-sonnet-4-6", ""},
		{"", ""},
	}
	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			if got := ProviderOf(c.spec); got != c.want {
				t.Errorf("ProviderOf(%q) = %q, want %q", c.spec, got, c.want)
			}
		})
	}
}

// tab uses when it opens with a model already selected.
func TestLocateActiveModel(t *testing.T) {
	// Exact match resolves to the row holding that exact spec. Derive the
	// target from the catalogue rather than pinning an order: the model list is
	// regenerated from live provider APIs (script/fetch_models.py) and reorders.
	anth := ModelsForProvider("anthropic")
	if len(anth) == 0 {
		t.Fatal("no anthropic models in catalogue")
	}
	want := anth[0].Spec
	pi, mi := locateActiveModel(want)
	if AvailableProviders()[pi].Name != "anthropic" {
		t.Errorf("%s: provider row resolved to %q, want anthropic", want, AvailableProviders()[pi].Name)
	}
	if ModelsForProvider("anthropic")[mi].Spec != want {
		t.Errorf("%s: model row resolved to %+v", want, ModelsForProvider("anthropic")[mi])
	}

	// Exact match from a different provider — verify pi advances and the row
	// resolves to the requested spec.
	target := ModelsForProvider("openai")[0].Spec
	pi, mi = locateActiveModel(target)
	if AvailableProviders()[pi].Name != "openai" {
		t.Errorf("%s: provider row resolved to %q, want openai", target, AvailableProviders()[pi].Name)
	}
	if ModelsForProvider("openai")[mi].Spec != target {
		t.Errorf("%s: model row resolved to %+v", target, ModelsForProvider("openai")[mi])
	}
	// Prefix-only match (not in curated list) — cursor on the right
	// provider column, model row 0.
	pi, mi = locateActiveModel("minimax/MiniMax-some-future-model")
	if AvailableProviders()[pi].Name != "minimax" {
		t.Errorf("future minimax: provider row resolved to %q, want minimax", AvailableProviders()[pi].Name)
	}
	if mi != 0 {
		t.Errorf("future minimax: expected model row 0 (no exact match), got %d", mi)
	}
	// Total fallback when prefix doesn't match any known provider.
	if pi, mi := locateActiveModel("weirdco/nope"); pi != 0 || mi != 0 {
		t.Errorf("unknown provider: got (%d,%d), want (0,0)", pi, mi)
	}
}
