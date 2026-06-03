package config

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/kirby88/vix/internal/auth"
)

// Keep auth-flow logs out of the real ~/.vix/logs/auth.log during config tests.
func init() {
	auth.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// storeAnthropicOAuth stores a completed anthropic login in the (mock)
// keychain and registers cleanup. A real `vix login anthropic` mints a metered
// API key (stored under Extra) and marks the credential non-expiring.
func storeAnthropicOAuth(t *testing.T, apiKey string) {
	t.Helper()
	creds := auth.Credentials{
		Expires: time.Now().Add(100 * time.Hour).UnixMilli(),
		Extra:   map[string]any{"apiKey": apiKey},
	}
	if err := auth.DefaultStorage().Set("anthropic", creds); err != nil {
		t.Fatalf("store oauth: %v", err)
	}
	t.Cleanup(func() { _ = auth.DefaultStorage().Remove("anthropic") })
}

func TestResolveProviderKey_StoredOAuthLogin(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	DeleteProviderKey("anthropic")
	storeAnthropicOAuth(t, "minted-api-key")

	key, source := ResolveProviderKey("anthropic", true)
	if key != "minted-api-key" {
		t.Errorf("key = %q, want minted-api-key", key)
	}
	// The minted key is sent as x-api-key, not a Bearer OAuth token.
	if source != KeySourceOAuthAPIKey {
		t.Errorf("source = %q, want %q", source, KeySourceOAuthAPIKey)
	}
}

func TestResolveProviderKey_EnvWinsOverStoredOAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	storeAnthropicOAuth(t, "minted-api-key")

	key, source := ResolveProviderKey("anthropic", true)
	if key != "env-key" || source != KeySourceEnv {
		t.Errorf("expected env key to win, got key=%q source=%q", key, source)
	}
}

func TestResolveProviderKey_StoredOAuthSkippedWhenDisallowed(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	DeleteProviderKey("anthropic")
	storeAnthropicOAuth(t, "minted-api-key")

	key, source := ResolveProviderKey("anthropic", false)
	if key != "" || source != KeySourceNone {
		t.Errorf("expected no key when OAuth disallowed, got key=%q source=%q", key, source)
	}
}

func TestResolveProviderCredentialFresh_StoredOAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	DeleteProviderKey("anthropic")
	storeAnthropicOAuth(t, "minted-api-key")

	// Credential is valid (not expired) so no network refresh happens.
	cred := ResolveProviderCredentialFresh(context.Background(), "anthropic", true)
	if cred.Value != "minted-api-key" || cred.Source != KeySourceOAuthAPIKey {
		t.Errorf("cred = %+v", cred)
	}
}

func TestResolveProviderCredentialFresh_EnvKeyWins(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	storeAnthropicOAuth(t, "minted-api-key")

	cred := ResolveProviderCredentialFresh(context.Background(), "anthropic", true)
	if cred.Value != "env-key" || cred.Source != KeySourceEnv {
		t.Errorf("cred = %+v, want env-key", cred)
	}
}
