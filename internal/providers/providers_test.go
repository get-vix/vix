package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedLoadsAndValidates asserts the shipped providers.json parses,
// validates, and indexes — the offline source of truth must always be sound.
func TestEmbeddedLoadsAndValidates(t *testing.T) {
	reg, err := loadEmbedded()
	if err != nil {
		t.Fatalf("loadEmbedded: %v", err)
	}
	wantIDs := []string{"anthropic", "openai", "openrouter", "minimax", "mimo"}
	if got := reg.IDs(); len(got) != len(wantIDs) {
		t.Fatalf("IDs = %v, want %v", got, wantIDs)
	}
	for i, id := range wantIDs {
		if reg.IDs()[i] != id {
			t.Errorf("IDs[%d] = %q, want %q", i, reg.IDs()[i], id)
		}
	}
}

// TestGoldenProviderData pins the key fields that the wiring layers depend on,
// reproducing the previously-hardcoded tables exactly.
func TestGoldenProviderData(t *testing.T) {
	reg, err := loadEmbedded()
	if err != nil {
		t.Fatalf("loadEmbedded: %v", err)
	}
	cases := []struct {
		id          string
		prefix      string
		wire        WireFormat
		effort      string
		authScheme  string
		baseURLEnv  string // expected resolved base url with no env set
		effortStyle string
	}{
		{"anthropic", "anthropic", WireMessages, EffortAdaptive, AuthSchemeXAPIKey, "https://api.anthropic.com/v1", EffortStyleNone},
		{"openai", "openai", WireResponses, EffortOpenAIReasoning, AuthSchemeBearer, "https://api.openai.com/v1", EffortStyleNone},
		{"openrouter", "openrouter", WireChatCompletions, EffortOpenAIReasoning, AuthSchemeBearer, "https://openrouter.ai/api/v1", EffortStyleReasoningEffort},
		{"minimax", "minimax", WireChatCompletions, EffortAdaptive, AuthSchemeBearer, "https://api.minimax.io/v1", EffortStyleReasoningSplit},
		{"mimo", "mimo", WireChatCompletions, EffortOpenAIReasoning, AuthSchemeBearer, "https://api.xiaomimimo.com/v1", EffortStyleReasoningEffort},
	}
	for _, c := range cases {
		p, ok := reg.Lookup(c.id)
		if !ok {
			t.Errorf("Lookup(%q) not found", c.id)
			continue
		}
		if p.ModelPrefix != c.prefix {
			t.Errorf("%s: prefix = %q, want %q", c.id, p.ModelPrefix, c.prefix)
		}
		if p.WireFormat != c.wire {
			t.Errorf("%s: wire = %q, want %q", c.id, p.WireFormat, c.wire)
		}
		if p.EffortPolicy != c.effort {
			t.Errorf("%s: effort_policy = %q, want %q", c.id, p.EffortPolicy, c.effort)
		}
		res := p.Inference.Resolve()
		if res.AuthScheme != c.authScheme {
			t.Errorf("%s: auth_scheme = %q, want %q", c.id, res.AuthScheme, c.authScheme)
		}
		if res.BaseURL != c.baseURLEnv {
			t.Errorf("%s: resolved base_url = %q, want %q", c.id, res.BaseURL, c.baseURLEnv)
		}
		if res.EffortStyle != c.effortStyle {
			t.Errorf("%s: effort_style = %q, want %q", c.id, res.EffortStyle, c.effortStyle)
		}
	}
}

// TestGoldenCredentialMethods pins the credential wiring config/providers.go
// derives (env vars, keyring users, header styles, producers, login ids).
func TestGoldenCredentialMethods(t *testing.T) {
	reg, _ := loadEmbedded()

	anth, _ := reg.Lookup("anthropic")
	if len(anth.Credential) != 3 {
		t.Fatalf("anthropic credential methods = %d, want 3", len(anth.Credential))
	}
	if anth.Credential[0].EnvVar != "ANTHROPIC_API_KEY" || anth.Credential[0].Keyring != "anthropic-api-key" {
		t.Errorf("anthropic[0] = %+v", anth.Credential[0])
	}
	if anth.Credential[1].HeaderStyle != AuthSchemeBearer || anth.Credential[1].ExtraHeadersProducer != ProducerAnthropicOAuth {
		t.Errorf("anthropic[1] = %+v", anth.Credential[1])
	}
	if anth.Credential[2].Kind != CredOAuthToken || anth.Credential[2].LoginID != "anthropic" {
		t.Errorf("anthropic[2] = %+v", anth.Credential[2])
	}

	openai, _ := reg.Lookup("openai")
	if openai.Credential[1].LoginID != "openai-codex" || openai.Credential[1].BaseURL != "https://chatgpt.com/backend-api/codex" {
		t.Errorf("openai codex method = %+v", openai.Credential[1])
	}
	if openai.Credential[1].ExtraHeadersProducer != ProducerCodexOAuth {
		t.Errorf("openai codex producer = %q", openai.Credential[1].ExtraHeadersProducer)
	}

	or, _ := reg.Lookup("openrouter")
	if or.Credential[1].Kind != CredOAuthMintKey || or.Credential[1].LoginID != "openrouter" {
		t.Errorf("openrouter mint method = %+v", or.Credential[1])
	}
}

// TestParseModel mirrors the old llm.factory_test cases.
func TestParseModel(t *testing.T) {
	reg, _ := loadEmbedded()
	cases := []struct {
		spec      string
		wantID    string
		wantModel string
		wantErr   bool
	}{
		{"anthropic/claude-opus-4-8", "anthropic", "claude-opus-4-8", false},
		{"openai/gpt-5.1", "openai", "gpt-5.1", false},
		{"openrouter/openai/gpt-5.1", "openrouter", "openai/gpt-5.1", false},
		{"minimax/MiniMax-M2.7", "minimax", "MiniMax-M2.7", false},
		{"mimo/mimo-v2.5-pro", "mimo", "mimo-v2.5-pro", false},
		{"", "", "", true},
		{"claude-sonnet-4-6", "", "", true},
		{"gemini/pro", "", "", true},
	}
	for _, c := range cases {
		p, model, err := reg.ParseModel(c.spec)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseModel(%q): expected error", c.spec)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseModel(%q): unexpected error %v", c.spec, err)
			continue
		}
		if p.ID != c.wantID || model != c.wantModel {
			t.Errorf("ParseModel(%q) = (%q, %q), want (%q, %q)", c.spec, p.ID, model, c.wantID, c.wantModel)
		}
	}
}

// TestDefaultEffort mirrors the old llm.DefaultEffortFromSpec cases.
func TestDefaultEffort(t *testing.T) {
	reg, _ := loadEmbedded()
	cases := []struct {
		spec string
		want string
	}{
		{"anthropic/claude-opus-4-8", "adaptive"},
		{"minimax/MiniMax-M2.7", "adaptive"},
		{"openai/gpt-5.1", "medium"},
		{"openai/gpt-4o", ""},
		{"openrouter/openai/o3", "medium"},
		{"mimo/mimo-v2-flash", ""},
	}
	for _, c := range cases {
		p, model, err := reg.ParseModel(c.spec)
		if err != nil {
			t.Errorf("ParseModel(%q): %v", c.spec, err)
			continue
		}
		if got := p.DefaultEffort(model); got != c.want {
			t.Errorf("DefaultEffort(%q) = %q, want %q", c.spec, got, c.want)
		}
	}
}

// TestModelCatalogue checks structural invariants of every provider's model
// list. It deliberately avoids pinning counts or specific specs: the catalogue
// is regenerated from live provider APIs (script/fetch_models.py) and drifts.
func TestModelCatalogue(t *testing.T) {
	reg, _ := loadEmbedded()
	for _, p := range reg.All() {
		if len(p.Models) == 0 {
			t.Errorf("%s: no models — a shipped provider must list at least one", p.ID)
		}
		prefix := p.Prefix()
		seen := map[string]bool{}
		for _, m := range p.Models {
			if m.Spec == "" || m.DisplayName == "" {
				t.Errorf("%s: model with empty field: %+v", p.ID, m)
			}
			if !strings.HasPrefix(m.Spec, prefix) {
				t.Errorf("%s: spec %q lacks provider prefix %q", p.ID, m.Spec, prefix)
			}
			if m.ContextWindow < 0 {
				t.Errorf("%s: spec %q has negative context_window %d", p.ID, m.Spec, m.ContextWindow)
			}
			if seen[m.Spec] {
				t.Errorf("%s: duplicate spec %q", p.ID, m.Spec)
			}
			seen[m.Spec] = true
		}
	}
}

// TestAuthLogins pins the OAuth login specs auth/registry.go derives.
func TestAuthLogins(t *testing.T) {
	reg, _ := loadEmbedded()
	anth, ok := reg.AuthLogin("anthropic")
	if !ok || anth.Flow != FlowOAuthPKCEToken || anth.CallbackPort != 53692 {
		t.Errorf("anthropic login = %+v", anth)
	}
	codex, ok := reg.AuthLogin("openai-codex")
	if !ok || codex.Flow != FlowOAuthCodex || codex.Device == nil || codex.Device.TimeoutSeconds != 900 {
		t.Errorf("codex login = %+v", codex)
	}
	or, ok := reg.AuthLogin("openrouter")
	if !ok || or.Flow != FlowOAuthPKCEMint || or.CallbackPort != 53781 {
		t.Errorf("openrouter login = %+v", or)
	}
}

// TestInterpolation covers ${env:VAR} and ${env:VAR:-default}.
func TestInterpolation(t *testing.T) {
	t.Setenv("VIX_TEST_SET", "hello")
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"${env:VIX_TEST_SET}", "hello"},
		{"${env:VIX_TEST_SET:-fallback}", "hello"},
		{"${env:VIX_TEST_UNSET}", ""},
		{"${env:VIX_TEST_UNSET:-fallback}", "fallback"},
		{"pre-${env:VIX_TEST_SET}-post", "pre-hello-post"},
		{"${env:A_UNSET:-https://x.example/v1}", "https://x.example/v1"},
		{"${env:VIX_TEST_UNTERMINATED", "${env:VIX_TEST_UNTERMINATED"},
	}
	for _, c := range cases {
		if got := interpolate(c.in); got != c.want {
			t.Errorf("interpolate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestResolveDropsEmptyQueryParams ensures an unset query-param var is dropped
// (MiniMax GroupId behavior) but a set one survives.
func TestResolveDropsEmptyQueryParams(t *testing.T) {
	in := InferenceSpec{
		BaseURL:     "https://api.example/v1",
		AuthScheme:  AuthSchemeBearer,
		QueryParams: map[string]string{"GroupId": "${env:VIX_TEST_GROUP}"},
	}
	if got := in.Resolve(); len(got.QueryParams) != 0 {
		t.Errorf("unset GroupId should be dropped, got %v", got.QueryParams)
	}
	t.Setenv("VIX_TEST_GROUP", "grp_1")
	if got := in.Resolve(); got.QueryParams["GroupId"] != "grp_1" {
		t.Errorf("set GroupId = %v, want grp_1", got.QueryParams)
	}
}

// TestConfigureLayering verifies on-disk overlays patch fields and add ids.
func TestConfigureLayering(t *testing.T) {
	dir := t.TempDir()
	overlay := filepath.Join(dir, "providers.json")
	os.WriteFile(overlay, []byte(`{
	  "schema_version": 1,
	  "providers": [
	    { "id": "anthropic", "display_name": "Claude (custom)" },
	    { "id": "acme", "display_name": "Acme", "model_prefix": "acme",
	      "wire_format": "chat_completions", "effort_policy": "",
	      "inference": { "base_url": "https://api.example/v1", "auth_scheme": "bearer" },
	      "credential_methods": [ { "kind": "api_key", "env_var": "ACME_API_KEY", "keyring": "acme-api-key" } ],
	      "models": [ { "spec": "acme/fast", "display_name": "Acme Fast" } ] }
	  ]
	}`), 0o644)

	if err := Configure([]string{overlay}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(resetDefault)

	reg := Default()
	anth, _ := reg.Lookup("anthropic")
	if anth.DisplayName != "Claude (custom)" {
		t.Errorf("overlay did not patch display_name: %q", anth.DisplayName)
	}
	if anth.WireFormat != WireMessages {
		t.Errorf("overlay clobbered wire_format: %q", anth.WireFormat)
	}
	acme, ok := reg.Lookup("acme")
	if !ok || acme.ModelPrefix != "acme" {
		t.Errorf("overlay did not add acme: %+v", acme)
	}
}

// TestPrefixCollisionRejected ensures two providers cannot share a prefix.
func TestPrefixCollisionRejected(t *testing.T) {
	dir := t.TempDir()
	overlay := filepath.Join(dir, "providers.json")
	os.WriteFile(overlay, []byte(`{
	  "schema_version": 1,
	  "providers": [
	    { "id": "dupe", "display_name": "Dupe", "model_prefix": "anthropic",
	      "wire_format": "messages", "effort_policy": "adaptive",
	      "inference": { "base_url": "https://api.example/v1", "auth_scheme": "x-api-key" },
	      "credential_methods": [ { "kind": "api_key", "env_var": "X", "keyring": "x" } ] }
	  ]
	}`), 0o644)
	err := Configure([]string{overlay})
	t.Cleanup(resetDefault)
	if err == nil {
		t.Fatal("expected prefix-collision error, got nil")
	}
}

// TestValidationRejections covers the closed-enum / URL guards.
func TestValidationRejections(t *testing.T) {
	base := func(p string) File {
		var f File
		f.SchemaVersion = 1
		f.Providers = []ProviderSpec{{
			ID: "x", ModelPrefix: "x", WireFormat: WireChatCompletions,
			Inference:  InferenceSpec{BaseURL: "https://api.example/v1", AuthScheme: AuthSchemeBearer},
			Credential: []CredentialMethod{{Kind: CredAPIKey, EnvVar: "X"}},
		}}
		switch p {
		case "wire":
			f.Providers[0].WireFormat = "telepathy"
		case "scheme":
			f.Providers[0].Inference.AuthScheme = "magic"
		case "http":
			f.Providers[0].Inference.BaseURL = "http://api.example/v1"
		case "newver":
			f.SchemaVersion = SchemaVersion + 1
		case "authhost":
			f.AuthLogins = []AuthLogin{{ID: "x", Flow: FlowOAuthPKCEToken, TokenURL: "https://evil.example/token"}}
			f.Providers[0].Credential = []CredentialMethod{{Kind: CredOAuthToken, LoginID: "x"}}
		}
		return f
	}
	for _, name := range []string{"wire", "scheme", "http", "newver", "authhost"} {
		if err := validate(base(name)); err == nil {
			t.Errorf("validate(%s): expected error, got nil", name)
		}
	}
	// Sanity: the unmodified base validates.
	if err := validate(base("")); err != nil {
		t.Errorf("validate(ok): unexpected error %v", err)
	}
}

// TestDenyListHook verifies the injected URLDenied hook blocks a base URL.
func TestDenyListHook(t *testing.T) {
	URLDenied = func(u string) bool { return u == "https://api.anthropic.com/v1" }
	t.Cleanup(func() { URLDenied = nil })
	_, err := loadEmbedded()
	if err == nil {
		t.Fatal("expected deny-listed base_url to fail load")
	}
}

// resetDefault clears the process-wide registry so a later Default() reloads
// the embedded defaults. Test-only.
func resetDefault() {
	defaultMu.Lock()
	defaultReg = nil
	defaultMu.Unlock()
}
