package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/get-vix/vix/internal/providers"
)

type isolatedCredentialStore struct{ values map[string]string }

func (s *isolatedCredentialStore) Get(account string) (string, error) {
	value, ok := s.values[account]
	if !ok {
		return "", ErrCredNotFound
	}
	return value, nil
}
func (s *isolatedCredentialStore) Set(account, value string) error {
	s.values[account] = value
	return nil
}
func (s *isolatedCredentialStore) Delete(account string) error {
	if _, ok := s.values[account]; !ok {
		return ErrCredNotFound
	}
	delete(s.values, account)
	return nil
}
func (*isolatedCredentialStore) Backend() string { return BackendProvider }

func resetCredentialStore(t *testing.T) {
	t.Helper()
	previous := defaultStoreInst
	defaultStoreInst = &isolatedCredentialStore{values: make(map[string]string)}
	t.Cleanup(func() { defaultStoreInst = previous })
}

func TestStoreResolveDeleteProviderKey(t *testing.T) {
	resetCredentialStore(t)
	if err := StoreProviderKey("anthropic", "provider-value"); err != nil {
		t.Fatalf("StoreProviderKey: %v", err)
	}
	key, source := ResolveProviderKey("anthropic")
	if key != "provider-value" || source != KeySourceSecretProvider {
		t.Fatalf("resolved = %q/%q", key, source)
	}
	if err := DeleteProviderKey("anthropic"); err != nil {
		t.Fatalf("DeleteProviderKey: %v", err)
	}
	if key, source = ResolveProviderKey("anthropic"); key != "" || source != KeySourceNone {
		t.Fatalf("after delete = %q/%q", key, source)
	}
}

func TestSecretEnvironmentVariablesAreIgnored(t *testing.T) {
	resetCredentialStore(t)
	t.Setenv("ANTHROPIC_API_KEY", "must-not-be-read")
	key, source := ResolveProviderKey("anthropic")
	if key != "" || source != KeySourceNone {
		t.Fatalf("environment secret leaked into resolution: %q/%q", key, source)
	}
}

func TestMethodCredentialAndBaseURLRoundTrip(t *testing.T) {
	resetCredentialStore(t)
	const key = "method-value"
	const endpoint = "https://eu.tokenplan.example/v1"
	if err := StoreProviderMethodKey("mimo", "Token Plan", key, endpoint); err != nil {
		t.Fatalf("StoreProviderMethodKey: %v", err)
	}
	credential := ResolveProviderCredential("mimo")
	if credential.Value != key || credential.BaseURL != endpoint || credential.Source != KeySourceSecretProvider {
		t.Fatalf("credential = %+v", credential)
	}
	t.Setenv("MIMO_TOKENPLAN_BASE_URL", "https://must-not-win.example/v1")
	if got := ResolveProviderCredential("mimo").BaseURL; got != endpoint {
		t.Fatalf("environment endpoint overrode provider value: %q", got)
	}
}

func TestLocalProviderUsesPlaceholderWithoutStoredKey(t *testing.T) {
	resetCredentialStore(t)
	credential := ResolveProviderCredential("ollama")
	if credential.Value == "" || credential.Source != KeySourceLocal {
		t.Fatalf("local credential = %+v", credential)
	}
}

func configureCustomProvider(t *testing.T, methods string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	body := `{"schema_version":1,"providers":[{"id":"acmeenv","display_name":"Acme","model_prefix":"acmeenv","wire_format":"chat_completions","inference":{"base_url":"https://api.acme.example/v1","auth_scheme":"bearer"},"credential_methods":` + methods + `,"models":[{"spec":"acmeenv/fast","display_name":"Acme Fast"}]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write providers: %v", err)
	}
	if err := providers.Configure([]string{path}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { _ = providers.Configure(nil) })
}

func TestLegacyEnvOnlyMethodMapsToProviderAccount(t *testing.T) {
	resetCredentialStore(t)
	configureCustomProvider(t, `[{"kind":"api_key","env_var":"ACME_ENV_API_KEY"}]`)
	if err := defaultStore().Set("acme-env-api-key", "stored-value"); err != nil {
		t.Fatal(err)
	}
	key, source := ResolveProviderKey("acmeenv")
	if key != "stored-value" || source != KeySourceSecretProvider {
		t.Fatalf("resolved = %q/%q", key, source)
	}
}

func TestDeleteMissingProviderKeyIsIdempotent(t *testing.T) {
	resetCredentialStore(t)
	if err := DeleteProviderKey("openai"); err != nil && !errors.Is(err, ErrCredNotFound) {
		t.Fatalf("DeleteProviderKey: %v", err)
	}
}
