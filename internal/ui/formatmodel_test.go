package ui

import "testing"

func TestFormatModelName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Provider prefix dropped; trailing version joined with a dot.
		{"anthropic/claude-opus-4-8", "claude-opus-4.8"},
		{"anthropic/claude-sonnet-4-6", "claude-sonnet-4.6"},
		{"openai-codex/gpt-5-codex", "gpt-5-codex"},
		{"openai/gpt-5-thinking", "gpt-5-thinking"},
		{"openai/o4-mini", "o4-mini"},
		{"minimax/MiniMax-M2.7", "minimax-m2.7"},
		// Only the bare id survives a multi-segment (OpenRouter) route.
		{"openrouter/anthropic/claude-opus-4-8", "claude-opus-4.8"},
		// Already-dotted versions are left alone.
		{"openai/gpt-5.1", "gpt-5.1"},
		// A bare id with no prefix still formats.
		{"claude-opus-4-8", "claude-opus-4.8"},
	}
	for _, c := range cases {
		if got := formatModelName(c.in); got != c.want {
			t.Errorf("formatModelName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
