package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/get-vix/vix/internal/providers"
	"github.com/zalando/go-keyring"
)

func init() {
	// Use in-memory mock keyring for all tests.
	keyring.MockInit()
}

func TestResolveProviderKey_EnvVarWins(t *testing.T) {
	// Store a key in the keychain
	if err := StoreProviderKey("anthropic", "keychain-key"); err != nil {
		t.Fatalf("StoreProviderKey: %v", err)
	}
	defer DeleteProviderKey("anthropic")

	// Set env var — should take priority
	t.Setenv("ANTHROPIC_API_KEY", "env-key")

	key, source := ResolveProviderKey("anthropic")
	if key != "env-key" {
		t.Errorf("expected env-key, got %q", key)
	}
	if source != KeySourceEnv {
		t.Errorf("expected source %q, got %q", KeySourceEnv, source)
	}
}

func TestResolveProviderKey_KeychainFallback(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")

	if err := StoreProviderKey("anthropic", "keychain-key"); err != nil {
		t.Fatalf("StoreProviderKey: %v", err)
	}
	defer DeleteProviderKey("anthropic")

	key, source := ResolveProviderKey("anthropic")
	if key != "keychain-key" {
		t.Errorf("expected keychain-key, got %q", key)
	}
	if source != KeySourceKeychain {
		t.Errorf("expected source %q, got %q", KeySourceKeychain, source)
	}
}

func TestResolveProviderKey_NoneWhenEmpty(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	// Ensure no keychain entry
	DeleteProviderKey("anthropic")

	key, source := ResolveProviderKey("anthropic")
	if key != "" {
		t.Errorf("expected empty key, got %q", key)
	}
	if source != KeySourceNone {
		t.Errorf("expected source %q, got %q", KeySourceNone, source)
	}
}

func TestStoreAndResolveRoundTrip(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")

	if err := StoreProviderKey("anthropic", "roundtrip-key"); err != nil {
		t.Fatalf("StoreProviderKey: %v", err)
	}
	defer DeleteProviderKey("anthropic")

	key, source := ResolveProviderKey("anthropic")
	if key != "roundtrip-key" || source != KeySourceKeychain {
		t.Errorf("round-trip failed: key=%q source=%q", key, source)
	}
}

func TestDeleteProviderKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")

	StoreProviderKey("anthropic", "delete-me")
	if err := DeleteProviderKey("anthropic"); err != nil {
		t.Fatalf("DeleteProviderKey: %v", err)
	}

	key, source := ResolveProviderKey("anthropic")
	if key != "" || source != KeySourceNone {
		t.Errorf("expected empty after delete, got key=%q source=%q", key, source)
	}
}

func TestResolveProviderKey_OpenAI(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-env-key")
	defer t.Setenv("OPENAI_API_KEY", "")

	key, source := ResolveProviderKey("openai")
	if key != "openai-env-key" {
		t.Errorf("expected openai-env-key, got %q", key)
	}
	if source != KeySourceEnv {
		t.Errorf("expected source %q, got %q", KeySourceEnv, source)
	}
}

func TestResolveProviderKey_OAuthFallback(t *testing.T) {
	// With no API key, the Claude Code OAuth token method is the fallback and
	// resolves with a Bearer-style source.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-token-value")
	DeleteProviderKey("anthropic")

	cred := ResolveProviderCredential("anthropic")
	if cred.Value != "oauth-token-value" {
		t.Errorf("expected oauth-token-value, got %q", cred.Value)
	}
	if cred.Source != KeySourceOAuthToken {
		t.Errorf("expected source %q, got %q", KeySourceOAuthToken, cred.Source)
	}
	if cred.HeaderStyle != BearerHeader {
		t.Errorf("expected BearerHeader, got %q", cred.HeaderStyle)
	}
}

func TestResolveProviderKey_APIKeyBeatsOAuthToken(t *testing.T) {
	// The plain API key method is listed first, so it wins over the OAuth token.
	t.Setenv("ANTHROPIC_API_KEY", "real-api-key")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-token-value")
	defer t.Setenv("ANTHROPIC_API_KEY", "")

	cred := ResolveProviderCredential("anthropic")
	if cred.Value != "real-api-key" {
		t.Errorf("expected real-api-key, got %q", cred.Value)
	}
	if cred.Source != KeySourceEnv {
		t.Errorf("expected source %q, got %q", KeySourceEnv, cred.Source)
	}
	if cred.HeaderStyle != APIKeyHeader {
		t.Errorf("expected APIKeyHeader, got %q", cred.HeaderStyle)
	}
}

func TestListStoredProviderKeys(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("MINIMAX_API_KEY", "")
	t.Setenv("MIMO_API_KEY", "")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	t.Setenv("OLLAMA_API_KEY", "")
	t.Setenv("LLAMACPP_API_KEY", "")
	DeleteProviderKey("anthropic")
	DeleteProviderKey("openai")
	DeleteProviderKey("openrouter")
	DeleteProviderKey("minimax")
	DeleteProviderKey("mimo")
	DeleteProviderKey("bedrock")
	DeleteProviderKey("ollama")
	DeleteProviderKey("llamacpp")

	StoreProviderKey("anthropic", "sk-ant-test-key")
	defer DeleteProviderKey("anthropic")

	keys := ListStoredProviderKeys()
	if len(keys) != 10 {
		t.Fatalf("expected 10 provider entries, got %d", len(keys))
	}

	anthropicFound := false
	for _, pk := range keys {
		if pk.Provider == "anthropic" {
			anthropicFound = true
			if pk.Prefix == "" {
				t.Errorf("expected non-empty prefix for anthropic")
			}
		}
		if pk.Provider == "openai" && pk.Prefix != "" {
			t.Errorf("expected empty prefix for openai (not stored)")
		}
	}
	if !anthropicFound {
		t.Errorf("anthropic not found in ListStoredProviderKeys")
	}
}

// TestMiMoTokenPlanCredential covers the Token Plan credential method: a key and
// a user-supplied base URL are stored together, resolution returns both, and the
// BaseURLEnv override wins over the stored endpoint.
func TestMiMoTokenPlanCredential(t *testing.T) {
	t.Setenv("MIMO_API_KEY", "")
	t.Setenv("MIMO_TOKENPLAN_BASE_URL", "")
	DeleteProviderMethodKey("mimo", "API Key")
	DeleteProviderMethodKey("mimo", "Token Plan")

	const tpKey = "tp-secret-key"
	const tpURL = "https://eu.tokenplan.example/v1"
	if err := StoreProviderMethodKey("mimo", "Token Plan", tpKey, tpURL); err != nil {
		t.Fatalf("StoreProviderMethodKey: %v", err)
	}
	defer DeleteProviderMethodKey("mimo", "Token Plan")

	// Billing key is empty, so resolution falls through to the Token Plan method.
	cred := ResolveProviderCredential("mimo")
	if cred.Value != tpKey {
		t.Errorf("value = %q, want %q", cred.Value, tpKey)
	}
	if cred.BaseURL != tpURL {
		t.Errorf("baseURL = %q, want stored token-plan endpoint %q", cred.BaseURL, tpURL)
	}

	// The status panel reports both methods, with Token Plan stored + its endpoint.
	st := GetProviderAuthStatus("mimo")
	if len(st.Methods) != 2 {
		t.Fatalf("expected 2 mimo methods, got %d", len(st.Methods))
	}
	var tp MethodStatus
	for _, ms := range st.Methods {
		if ms.ID == "Token Plan" {
			tp = ms
		}
	}
	if !tp.Stored || tp.BaseURL != tpURL || !tp.RequiresBaseURL {
		t.Errorf("token-plan status = %+v, want stored with base URL %q", tp, tpURL)
	}

	// Env override wins over the stored endpoint.
	t.Setenv("MIMO_TOKENPLAN_BASE_URL", "https://override.example/v1")
	if got := ResolveProviderCredential("mimo").BaseURL; got != "https://override.example/v1" {
		t.Errorf("env override baseURL = %q, want the override", got)
	}
}

// TestLocalProviderCredential covers keyless local providers (NoneAuth): with
// nothing configured, resolution yields the placeholder credential; a set env
// key wins because the api_key method is declared first.
func TestLocalProviderCredential(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", "")
	DeleteProviderKey("ollama")

	cred := ResolveProviderCredential("ollama")
	if cred.Value == "" || cred.Source != KeySourceLocal {
		t.Errorf("keyless resolution = %+v, want placeholder with KeySourceLocal", cred)
	}

	// An explicit key (e.g. proxied Ollama) takes precedence.
	t.Setenv("OLLAMA_API_KEY", "proxy-secret")
	cred = ResolveProviderCredential("ollama")
	if cred.Value != "proxy-secret" || cred.Source != KeySourceEnv {
		t.Errorf("env-key resolution = %+v, want proxy-secret from env", cred)
	}

	// The status panel surfaces only the user-manageable API-key method; the
	// none method is implicit and never rendered as a row.
	st := GetProviderAuthStatus("ollama")
	if len(st.Methods) != 1 || st.Methods[0].Label != "API Key" {
		t.Errorf("ollama auth status = %+v, want a single API Key method", st.Methods)
	}
}

// configureCustomProvider registers a custom provider "acmeenv" with the given
// credential_methods JSON and resets the registry to embedded defaults on
// cleanup. It models the providers.json overlay path from issue #57.
func configureCustomProvider(t *testing.T, credMethods string) {
	t.Helper()
	dir := t.TempDir()
	overlay := filepath.Join(dir, "providers.json")
	body := `{
	  "schema_version": 1,
	  "providers": [
	    { "id": "acmeenv", "display_name": "Acme Env", "model_prefix": "acmeenv",
	      "wire_format": "chat_completions",
	      "inference": { "base_url": "https://api.acme.example/v1", "auth_scheme": "bearer" },
	      "credential_methods": ` + credMethods + `,
	      "models": [ { "spec": "acmeenv/fast", "display_name": "Acme Fast" } ] }
	  ]
	}`
	if err := os.WriteFile(overlay, []byte(body), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	if err := providers.Configure([]string{overlay}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { _ = providers.Configure(nil) })
}

// TestEnvOnlyProvider_HasCredential is the regression for issue #57: a custom
// provider whose only credential method is an env_var (no keyring) must report
// as configured when the env var is set, without any keychain entry.
func TestEnvOnlyProvider_HasCredential(t *testing.T) {
	keyring.MockInit() // clean, reachable keychain with nothing stored
	configureCustomProvider(t, `[ { "kind": "api_key", "env_var": "ACME_ENV_API_KEY" } ]`)
	t.Setenv("ACME_ENV_API_KEY", "acme-env-secret")

	st := GetProviderAuthStatus("acmeenv")
	if !st.HasCredential() {
		t.Fatalf("HasCredential = false, want true (env var set, no keychain entry)")
	}
	if len(st.Methods) != 1 || !st.Methods[0].Stored {
		t.Fatalf("method status = %+v, want a single stored API Key method", st.Methods)
	}
	if st.Methods[0].Prefix != "acme-env-s" {
		t.Errorf("prefix = %q, want %q", st.Methods[0].Prefix, "acme-env-s")
	}

	key, source := ResolveProviderKey("acmeenv")
	if key != "acme-env-secret" || source != KeySourceEnv {
		t.Errorf("resolved = %q/%q, want acme-env-secret/%s", key, source, KeySourceEnv)
	}
}

// TestEnvOnlyProvider_NoCredentialWithoutEnv confirms the same provider reports
// as unconfigured when neither env var nor keychain provides a value.
func TestEnvOnlyProvider_NoCredentialWithoutEnv(t *testing.T) {
	keyring.MockInit()
	configureCustomProvider(t, `[ { "kind": "api_key", "env_var": "ACME_ENV_API_KEY" } ]`)
	t.Setenv("ACME_ENV_API_KEY", "")

	if GetProviderAuthStatus("acmeenv").HasCredential() {
		t.Errorf("HasCredential = true, want false with no env var and empty keychain")
	}
}

// TestEnvOnlyProvider_KeychainUnavailable is the headless/container case: the
// keychain is unreachable, but the env var must still make the provider count as
// configured.
func TestEnvOnlyProvider_KeychainUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keychain"))
	t.Cleanup(keyring.MockInit)
	configureCustomProvider(t, `[ { "kind": "api_key", "env_var": "ACME_ENV_API_KEY" } ]`)
	t.Setenv("ACME_ENV_API_KEY", "acme-env-secret")

	if !GetProviderAuthStatus("acmeenv").HasCredential() {
		t.Errorf("HasCredential = false, want true (env var set, keychain unavailable)")
	}
}

// TestEnvVarProvider_KeyringDeclaredButEmpty guards the exact symptom from the
// issue: with a keyring name declared but nothing stored, the env var alone must
// still resolve the credential — no dummy keychain value required.
func TestEnvVarProvider_KeyringDeclaredButEmpty(t *testing.T) {
	keyring.MockInit()
	configureCustomProvider(t, `[ { "kind": "api_key", "env_var": "ACME_ENV_API_KEY", "keyring": "acmeenv-api-key" } ]`)
	t.Setenv("ACME_ENV_API_KEY", "acme-env-secret")

	st := GetProviderAuthStatus("acmeenv")
	if !st.HasCredential() {
		t.Errorf("HasCredential = false, want true (env var set, keyring declared but empty)")
	}
}
