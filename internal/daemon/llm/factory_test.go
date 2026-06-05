package llm

import (
	"testing"

	"github.com/get-vix/vix/internal/config"
)

func TestParseModel(t *testing.T) {
	cases := []struct {
		spec      string
		wantProv  ProviderID
		wantModel string
		wantErr   bool
	}{
		{"anthropic/claude-opus-4-8", ProviderAnthropic, "claude-opus-4-8", false},
		{"openai/gpt-5.1", ProviderOpenAI, "gpt-5.1", false},
		{"openrouter/openai/gpt-5.1", ProviderOpenRouter, "openai/gpt-5.1", false},
		{"minimax/MiniMax-M2.7", ProviderMiniMax, "MiniMax-M2.7", false},
		{"mimo/mimo-v2.5-pro", ProviderMiMo, "mimo-v2.5-pro", false},
		{"", "", "", true},
		{"claude-sonnet-4-6", "", "", true}, // bare name, no prefix
		{"gemini/pro", "", "", true},        // unknown prefix
	}
	for _, c := range cases {
		prov, model, err := ParseModel(c.spec)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseModel(%q): expected error, got (%q, %q)", c.spec, prov, model)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseModel(%q): unexpected error %v", c.spec, err)
			continue
		}
		if prov != c.wantProv || model != c.wantModel {
			t.Errorf("ParseModel(%q) = (%q, %q), want (%q, %q)", c.spec, prov, model, c.wantProv, c.wantModel)
		}
	}
}

func TestDefaultEffortFromSpec(t *testing.T) {
	cases := []struct {
		spec string
		want string
	}{
		{"anthropic/claude-opus-4-8", "adaptive"},
		{"minimax/MiniMax-M2.7", "adaptive"},
		{"openai/gpt-5.1", "medium"}, // reasoning-capable
		{"openai/gpt-4o", ""},        // not reasoning
		{"openrouter/openai/o3", "medium"},
		{"mimo/mimo-v2-flash", ""},
		{"bogus", ""}, // parse error → empty
	}
	for _, c := range cases {
		if got := DefaultEffortFromSpec(c.spec); got != c.want {
			t.Errorf("DefaultEffortFromSpec(%q) = %q, want %q", c.spec, got, c.want)
		}
	}
}

func TestOpenAIAuthOptions_ExtraHeaders(t *testing.T) {
	// A credential with extra headers (e.g. the Codex backend's
	// chatgpt-account-id) yields one WithAPIKey option plus one per header.
	cred := config.Credential{Value: "tok", ExtraHeaders: map[string]string{"chatgpt-account-id": "acct-123"}}
	opts := openaiAuthOptions(cred)
	if len(opts) != 2 {
		t.Errorf("expected 2 options (api key + 1 header), got %d", len(opts))
	}
}
