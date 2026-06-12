package ui

import (
	"testing"

	"github.com/get-vix/vix/internal/daemon"
)

func TestIsLocalProvider(t *testing.T) {
	for _, name := range []string{"ollama", "llamacpp"} {
		if !IsLocalProvider(name) {
			t.Errorf("IsLocalProvider(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"anthropic", "openai", "openrouter", "minimax", "mimo", "bedrock", "nope"} {
		if IsLocalProvider(name) {
			t.Errorf("IsLocalProvider(%q) = true, want false", name)
		}
	}
}

// TestLocalProviderUIFromState covers the daemon-state → view-state conversion:
// loaded markers, the llama.cpp fixed-by-server label, and prefixed specs.
func TestLocalProviderUIFromState(t *testing.T) {
	ollama := localProviderUIFromState(daemon.LocalProviderState{
		Provider:  "ollama",
		BaseURL:   "http://localhost:11434/v1",
		Reachable: true,
		Models: []daemon.LocalModel{
			{Spec: "ollama/llama3.2:3b", DisplayName: "llama3.2:3b"},
			{Spec: "ollama/qwen3:8b", DisplayName: "qwen3:8b", Loaded: true},
		},
	})
	if !ollama.Fetched || !ollama.Reachable {
		t.Errorf("ollama ui = %+v, want fetched+reachable", ollama)
	}
	if len(ollama.Models) != 2 {
		t.Fatalf("models = %+v, want 2", ollama.Models)
	}
	if ollama.Models[0].DisplayName != "llama3.2:3b" || ollama.Models[0].Spec != "ollama/llama3.2:3b" {
		t.Errorf("model[0] = %+v", ollama.Models[0])
	}
	if ollama.Models[1].DisplayName != "qwen3:8b ●" {
		t.Errorf("loaded model display = %q, want loaded marker", ollama.Models[1].DisplayName)
	}

	llamacpp := localProviderUIFromState(daemon.LocalProviderState{
		Provider:  "llamacpp",
		Reachable: true,
		Models:    []daemon.LocalModel{{Spec: "llamacpp/qwen.gguf", DisplayName: "qwen.gguf"}},
	})
	if llamacpp.Models[0].DisplayName != "qwen.gguf (fixed by server)" {
		t.Errorf("single llamacpp model = %q, want fixed-by-server label", llamacpp.Models[0].DisplayName)
	}

	down := localProviderUIFromState(daemon.LocalProviderState{Provider: "ollama"})
	if !down.Fetched || down.Reachable || len(down.Models) != 0 {
		t.Errorf("down ui = %+v, want fetched+unreachable+no models", down)
	}
}

// TestRefreshModelsProviders_LocalGroup asserts local providers land in their
// own group (never logged-in/available) and the flat cursor order is
// logged-in, available, local last.
func TestRefreshModelsProviders_LocalGroup(t *testing.T) {
	m := &Model{socketPath: "/nonexistent/vixd.sock"} // cred RPC fails gracefully
	m.refreshModelsProviders()

	if len(m.modelsLocal) != 2 || m.modelsLocal[0] != "ollama" || m.modelsLocal[1] != "llamacpp" {
		t.Errorf("modelsLocal = %v, want [ollama llamacpp]", m.modelsLocal)
	}
	for _, name := range append(append([]string{}, m.modelsLoggedIn...), m.modelsAvailable...) {
		if name == "ollama" || name == "llamacpp" {
			t.Errorf("local provider %q leaked into logged-in/available", name)
		}
	}
	flat := m.modelsFlat()
	if len(flat) != len(m.modelsLoggedIn)+2+len(m.modelsAvailable) {
		t.Errorf("flat = %v, missing groups", flat)
	}
	// Local sits last, after logged-in and available.
	base := len(m.modelsLoggedIn) + len(m.modelsAvailable)
	if flat[base] != "ollama" || flat[base+1] != "llamacpp" {
		t.Errorf("flat order = %v, want local group last", flat)
	}

	// displayModelsForProvider: local providers read live state, not catalogue.
	if got := m.displayModelsForProvider("ollama"); len(got) != 0 {
		t.Errorf("unfetched local provider models = %v, want none", got)
	}
	m.modelsLocalUI = map[string]LocalProviderUI{
		"ollama": {Fetched: true, Reachable: true, Models: []ModelInfo{{Spec: "ollama/qwen3:8b", Provider: "ollama", DisplayName: "qwen3:8b"}}},
	}
	if got := m.displayModelsForProvider("ollama"); len(got) != 1 || got[0].Spec != "ollama/qwen3:8b" {
		t.Errorf("live models = %v", got)
	}
}
