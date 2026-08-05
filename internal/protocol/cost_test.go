package protocol

import (
	"math"
	"testing"
)

func TestCalculateCost(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		input      int64
		output     int64
		cacheWrite int64
		cacheRead  int64
		wantMin    float64 // use range to handle float precision
		wantMax    float64
	}{
		// --- Legacy bare claude-* names (no prefix) — backstop for older callers/logs ---
		{
			name:    "legacy bare sonnet",
			model:   "claude-sonnet-4-20250514",
			input:   1_000_000,
			output:  100_000,
			wantMin: 4.49, wantMax: 4.51,
		},
		{
			name:    "legacy bare opus 4.5",
			model:   "claude-opus-4-5-20250929",
			input:   1_000_000,
			output:  100_000,
			wantMin: 7.49, wantMax: 7.51,
		},
		{
			name:    "legacy bare opus 4 (expensive)",
			model:   "claude-opus-4-20250514",
			input:   1_000_000,
			output:  100_000,
			wantMin: 22.49, wantMax: 22.51,
		},
		{
			name:    "legacy bare haiku",
			model:   "claude-haiku-4-5-20251001",
			input:   1_000_000,
			output:  100_000,
			wantMin: 1.49, wantMax: 1.51,
		},

		// --- Prefixed Anthropic specs (the new canonical form) ---
		{
			name:    "anthropic/ prefixed opus 4.8",
			model:   "anthropic/claude-opus-4-8",
			input:   1_000_000,
			output:  100_000,
			wantMin: 7.49, wantMax: 7.51, // same pricing tier as 4.5/4.6/4.7
		},
		{
			name:    "anthropic/ prefixed sonnet",
			model:   "anthropic/claude-sonnet-4-6",
			input:   1_000_000,
			output:  100_000,
			wantMin: 4.49, wantMax: 4.51,
		},

		// --- OpenAI ---
		{
			name:   "openai gpt-5.1",
			model:  "openai/gpt-5.1",
			input:  1_000_000,
			output: 100_000,
			// uncached input 1M * 2.5/1M + 100k * 10/1M = 2.5 + 1.0 = 3.5
			wantMin: 3.49, wantMax: 3.51,
		},
		{
			name:   "openai o3",
			model:  "openai/o3",
			input:  1_000_000,
			output: 100_000,
			// 1M * 2.0/1M + 100k * 8/1M = 2.0 + 0.8 = 2.8
			wantMin: 2.79, wantMax: 2.81,
		},
		{
			name:   "openai gpt-4o-mini",
			model:  "openai/gpt-4o-mini",
			input:  1_000_000,
			output: 100_000,
			// 1M * 0.15/1M + 100k * 0.60/1M = 0.15 + 0.06 = 0.21
			wantMin: 0.20, wantMax: 0.22,
		},

		// --- MiniMax ---
		{
			name:   "minimax M2.7",
			model:  "minimax/MiniMax-M2.7",
			input:  1_000_000,
			output: 100_000,
			// 1M * 0.279/1M + 100k * 1.20/1M = 0.279 + 0.12 = 0.399
			wantMin: 0.39, wantMax: 0.41,
		},
		{
			name:   "minimax M2.5",
			model:  "minimax/MiniMax-M2.5",
			input:  1_000_000,
			output: 100_000,
			// 1M * 0.15/1M + 100k * 0.60/1M = 0.15 + 0.06 = 0.21
			wantMin: 0.20, wantMax: 0.22,
		},

		// --- OpenRouter — no local table; caller is expected to use Usage.CostUSD ---
		{
			name:    "openrouter returns zero (caller uses Usage.CostUSD)",
			model:   "openrouter/anthropic/claude-opus-4-8",
			input:   1_000_000,
			output:  100_000,
			wantMin: 0, wantMax: 0,
		},

		// --- Unknown providers / models ---
		{
			name:    "unknown provider returns zero",
			model:   "weirdco/some-model",
			input:   1_000_000,
			output:  100_000,
			wantMin: 0, wantMax: 0,
		},
		{
			name:    "openai unknown model returns zero",
			model:   "openai/totally-made-up",
			input:   1_000_000,
			output:  100_000,
			wantMin: 0, wantMax: 0,
		},
		{
			name:    "completely bare unknown returns zero",
			model:   "unknown-model",
			input:   1_000_000,
			output:  100_000,
			wantMin: 0, wantMax: 0,
		},

		// --- Edge cases ---
		{
			name:       "cache deduction clamps to zero (prefixed)",
			model:      "anthropic/claude-sonnet-4-6",
			input:      100,
			output:     0,
			cacheWrite: 80,
			cacheRead:  80,
			// uncachedInput = 100 - 80 - 80 = -60, clamped to 0
			// cost = 0 + 0 + 80*3.75/1M + 80*0.30/1M ≈ 0.000324
			wantMin: 0.0003, wantMax: 0.0004,
		},
		{
			name:    "zero tokens",
			model:   "anthropic/claude-sonnet-4-6",
			input:   0,
			output:  0,
			wantMin: 0, wantMax: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateCost(tt.model, tt.input, tt.output, tt.cacheWrite, tt.cacheRead)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("CalculateCost() = %f, want [%f, %f]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestCalculateCostPrefixOrdering(t *testing.T) {
	// Verify that "claude-opus-4-5" matches before "claude-opus-4"
	// (longest prefix first within the anthropic table). Run against both
	// legacy bare names and the prefixed form.
	for _, prefix := range []string{"", "anthropic/"} {
		costOpus45 := CalculateCost(prefix+"claude-opus-4-5-20250929", 1_000_000, 0, 0, 0)
		costOpus4 := CalculateCost(prefix+"claude-opus-4-20250514", 1_000_000, 0, 0, 0)

		if math.Abs(costOpus45-5.0) > 0.01 {
			t.Errorf("[%s]opus-4-5 cost = %f, want ~5.0 (not opus-4 pricing)", prefix, costOpus45)
		}
		if math.Abs(costOpus4-15.0) > 0.01 {
			t.Errorf("[%s]opus-4 cost = %f, want ~15.0", prefix, costOpus4)
		}
	}
}

func TestSplitModelSpec(t *testing.T) {
	cases := []struct {
		spec         string
		wantProvider string
		wantBare     string
	}{
		{"anthropic/claude-opus-4-8", "anthropic", "claude-opus-4-8"},
		{"openai/o3", "openai", "o3"},
		{"openrouter/anthropic/claude-opus-4-8", "openrouter", "anthropic/claude-opus-4-8"},
		{"minimax/MiniMax-M2.7", "minimax", "MiniMax-M2.7"},
		{"claude-sonnet-4-6", "anthropic", "claude-sonnet-4-6"}, // legacy bare fallback
		{"unknown-thing", "", "unknown-thing"},
		{"", "", ""},
	}
	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			gotProv, gotBare := splitModelSpec(c.spec)
			if gotProv != c.wantProvider {
				t.Errorf("provider = %q, want %q", gotProv, c.wantProvider)
			}
			if gotBare != c.wantBare {
				t.Errorf("bare = %q, want %q", gotBare, c.wantBare)
			}
		})
	}
}
