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

// storeAnthropicOAuth stores a non-expired anthropic OAuth login in the (mock)
// keychain and registers cleanup.
func storeAnthropicOAuth(t *testing.T, access string) {
	t.Helper()
	creds := auth.Credentials{
		Access:  access,
		Refresh: "refresh-token",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
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
	storeAnthropicOAuth(t, "oauth-access-token")

	key, source := ResolveProviderKey("anthropic", true)
	if key != "oauth-access-token" {
		t.Errorf("key = %q, want oauth-access-token", key)
	}
	if source != KeySourceOAuthToken {
		t.Errorf("source = %q, want %q", source, KeySourceOAuthToken)
	}
}

func TestResolveProviderKey_EnvWinsOverStoredOAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	storeAnthropicOAuth(t, "oauth-access-token")

	key, source := ResolveProviderKey("anthropic", true)
	if key != "env-key" || source != KeySourceEnv {
		t.Errorf("expected env key to win, got key=%q source=%q", key, source)
	}
}

func TestResolveProviderKey_StoredOAuthSkippedWhenDisallowed(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	DeleteProviderKey("anthropic")
	storeAnthropicOAuth(t, "oauth-access-token")

	key, source := ResolveProviderKey("anthropic", false)
	if key != "" || source != KeySourceNone {
		t.Errorf("expected no key when OAuth disallowed, got key=%q source=%q", key, source)
	}
}

func TestResolveProviderCredentialFresh_StoredOAuth(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	DeleteProviderKey("anthropic")
	storeAnthropicOAuth(t, "oauth-access-token")

	// Token is valid (not expired) so no network refresh happens.
	cred := ResolveProviderCredentialFresh(context.Background(), "anthropic", true)
	if cred.Value != "oauth-access-token" || cred.Source != KeySourceOAuthToken {
		t.Errorf("cred = %+v", cred)
	}
}

func TestResolveProviderCredentialFresh_EnvKeyWins(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	storeAnthropicOAuth(t, "oauth-access-token")

	cred := ResolveProviderCredentialFresh(context.Background(), "anthropic", true)
	if cred.Value != "env-key" || cred.Source != KeySourceEnv {
		t.Errorf("cred = %+v, want env-key", cred)
	}
}
