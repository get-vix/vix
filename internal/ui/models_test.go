package ui

import "testing"

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

// TestLocateActiveModel covers the provider-column resolution the Settings tab
// uses when it opens. Models are fetched live, so only the provider column is
// resolved (from the spec prefix); the model row always starts at 0.
func TestLocateActiveModel(t *testing.T) {
	pi, mi := locateActiveModel("anthropic/claude-opus-4-8")
	if AvailableProviders[pi].Name != "anthropic" || mi != 0 {
		t.Errorf("anthropic spec: got (%d,%d) provider=%q", pi, mi, AvailableProviders[pi].Name)
	}
	pi, mi = locateActiveModel("openai/o3")
	if AvailableProviders[pi].Name != "openai" || mi != 0 {
		t.Errorf("openai spec: provider=%q mi=%d", AvailableProviders[pi].Name, mi)
	}
	// An OpenRouter nested route resolves to the openrouter column.
	if pi, _ := locateActiveModel("openrouter/anthropic/claude-opus-4-8"); AvailableProviders[pi].Name != "openrouter" {
		t.Errorf("openrouter spec: provider=%q", AvailableProviders[pi].Name)
	}
	// Unknown provider falls back to (0, 0).
	if pi, mi := locateActiveModel("weirdco/nope"); pi != 0 || mi != 0 {
		t.Errorf("unknown provider: got (%d,%d), want (0,0)", pi, mi)
	}
}
