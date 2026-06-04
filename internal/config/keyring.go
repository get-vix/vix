package config

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/get-vix/vix/internal/auth"
	"github.com/zalando/go-keyring"
)

const (
	keyringService = "vix"
)

// KeySource describes where the API key was found.
type KeySource string

const (
	KeySourceEnv         KeySource = "env"
	KeySourceOAuthToken  KeySource = "oauth-token"
	KeySourceOAuthAPIKey KeySource = "oauth-api-key"
	KeySourceKeychain    KeySource = "keychain"
	KeySourceEnvFile     KeySource = "dotenv"
	KeySourceNone        KeySource = "none"
)

// Credential bundles an API key or OAuth token with its source.
// Use RequestOptions() to get the correct SDK auth options.
type Credential struct {
	Value  string
	Source KeySource
}

// RequestOptions returns the appropriate Anthropic SDK options for this
// credential. A raw OAuth access token (KeySourceOAuthToken, e.g. a
// CLAUDE_CODE_OAUTH_TOKEN) is sent as a Bearer token; everything else —
// including the metered API key minted by `vix login anthropic`
// (KeySourceOAuthAPIKey) — is sent as an x-api-key.
func (c Credential) RequestOptions() []option.RequestOption {
	if c.Source == KeySourceOAuthToken {
		return []option.RequestOption{
			option.WithHeader("Authorization", "Bearer "+c.Value),
		}
	}
	return []option.RequestOption{option.WithAPIKey(c.Value)}
}

// oauthCredentialSource reports how a provider's stored interactive-login
// credential authenticates inference. `vix login anthropic` mints a metered
// API key (sent as x-api-key, so KeySourceOAuthAPIKey); other OAuth providers
// (e.g. OpenAI Codex) authenticate with a Bearer access token.
func oauthCredentialSource(provider string) KeySource {
	if provider == "anthropic" {
		return KeySourceOAuthAPIKey
	}
	return KeySourceOAuthToken
}

// ResolveEnvVar checks the environment and .env files for a variable.
// Returns the value and true if found, or empty string and false.
func ResolveEnvVar(name string) (string, bool) {
	if v := os.Getenv(name); v != "" {
		return v, true
	}
	if v := loadKeyFromEnvFile(loadExeEnvFilePath(), name); v != "" {
		return v, true
	}
	if v := loadKeyFromEnvFile(".env", name); v != "" {
		return v, true
	}
	return "", false
}

// ProviderKey holds a provider name and a display prefix of its stored key.
type ProviderKey struct {
	Provider string
	Prefix   string // first 10 chars of the stored key, for display; empty if not stored
}

// providerKeyringUser returns the keyring "user" field for a given provider.
// e.g. "anthropic" → "anthropic-api-key", "openai" → "openai-api-key"
func providerKeyringUser(provider string) string {
	return provider + "-api-key"
}

// providerEnvVar returns the environment variable name for a given provider.
func providerEnvVar(provider string) string {
	switch provider {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "minimax":
		return "MINIMAX_API_KEY"
	case "mimo":
		return "MIMO_API_KEY"
	default:
		return ""
	}
}

// resolveKey searches env var, OS keychain, and .env files for the given variable name
// and optional keyring user. Returns the value and source, or empty if not found.
func resolveKey(envVar, keyringUser string) (string, KeySource) {
	// 1. Environment variable
	if envVar != "" {
		if key := os.Getenv(envVar); key != "" {
			return key, KeySourceEnv
		}
	}

	// 2. OS Keychain
	if keyringUser != "" {
		if key, err := keyring.Get(keyringService, keyringUser); err == nil && key != "" {
			return key, KeySourceKeychain
		}
	}

	// 3. .env next to executable
	if envVar != "" {
		if key := loadKeyFromEnvFile(loadExeEnvFilePath(), envVar); key != "" {
			return key, KeySourceEnvFile
		}

		// 4. .env in CWD
		if key := loadKeyFromEnvFile(".env", envVar); key != "" {
			return key, KeySourceEnvFile
		}
	}

	return "", KeySourceNone
}

// ResolveOAuthToken resolves the CLAUDE_CODE_OAUTH_TOKEN through the standard
// source chain (env var → keychain → .env) and returns its value and source.
func ResolveOAuthToken() (string, KeySource) {
	key, _ := resolveKey("CLAUDE_CODE_OAUTH_TOKEN", "claude-code-oauth-token")
	if key != "" {
		return key, KeySourceOAuthToken
	}
	return "", KeySourceNone
}

// ResolveProviderKey checks all sources in priority order and returns the key and its source.
// For anthropic, ANTHROPIC_API_KEY is checked across all sources first, then
// CLAUDE_CODE_OAUTH_TOKEN is checked across all sources as a fallback (only when allowOAuth is true).
func ResolveProviderKey(provider string, allowOAuth bool) (key string, source KeySource) {
	envVar := providerEnvVar(provider)
	key, source = resolveKey(envVar, providerKeyringUser(provider))
	if key != "" {
		return key, source
	}

	// Stored interactive OAuth login (`vix login`). This is a local keychain
	// lookup with no network refresh, so it is safe to call from UI/render
	// paths. The daemon's LLM construction uses ResolveProviderCredentialFresh,
	// which refreshes an expired token before use.
	if allowOAuth {
		if token, _, ok := auth.DefaultStorage().AccessToken(provider); ok && token != "" {
			return token, oauthCredentialSource(provider)
		}
	}

	// Fall back to Claude Code OAuth token (anthropic only)
	if allowOAuth && provider == "anthropic" {
		key, source = resolveKey("CLAUDE_CODE_OAUTH_TOKEN", "claude-code-oauth-token")
		if key != "" {
			return key, KeySourceOAuthToken
		}
	}

	return "", KeySourceNone
}

// ResolveProviderCredentialFresh resolves a provider credential like
// ResolveProviderCredential, but when the credential comes from a stored OAuth
// login (`vix login`) it refreshes an expired access token over the network
// before returning. It is intended for the daemon's LLM construction path,
// where a network round-trip is acceptable; ctx bounds the refresh.
//
// Explicit API keys (env var, keychain, .env) still win and never trigger a
// network call.
func ResolveProviderCredentialFresh(ctx context.Context, provider string, allowOAuth bool) Credential {
	if key, src := resolveKey(providerEnvVar(provider), providerKeyringUser(provider)); key != "" {
		return Credential{Value: key, Source: src}
	}

	if allowOAuth {
		// Returns ErrNoCredentials when no login is stored, or a refresh error
		// when one is stored but cannot be refreshed; both fall through to the
		// remaining sources below. A genuine refresh failure is logged (to the
		// daemon log / vixd.log) so it is not silently swallowed; full detail is
		// in the auth log (see auth.AuthLogPath).
		token, err := auth.DefaultStorage().AccessTokenRefreshing(ctx, provider)
		if err == nil && token != "" {
			return Credential{Value: token, Source: oauthCredentialSource(provider)}
		}
		if err != nil && !errors.Is(err, auth.ErrNoCredentials) {
			log.Printf("[auth] stored OAuth credential for %q unusable: %v", provider, err)
		}
	}

	if allowOAuth && provider == "anthropic" {
		if key, _ := resolveKey("CLAUDE_CODE_OAUTH_TOKEN", "claude-code-oauth-token"); key != "" {
			return Credential{Value: key, Source: KeySourceOAuthToken}
		}
	}

	return Credential{Source: KeySourceNone}
}

// ResolveProviderCredential returns a Credential for the given provider.
func ResolveProviderCredential(provider string, allowOAuth bool) Credential {
	key, source := ResolveProviderKey(provider, allowOAuth)
	return Credential{Value: key, Source: source}
}

// StoreProviderKey writes the API key for the given provider to the OS keychain.
func StoreProviderKey(provider, key string) error {
	return keyring.Set(keyringService, providerKeyringUser(provider), key)
}

// DeleteProviderKey removes the API key for the given provider from the OS keychain.
func DeleteProviderKey(provider string) error {
	return keyring.Delete(keyringService, providerKeyringUser(provider))
}

// ListStoredProviderKeys returns the stored key info for all known providers.
// The Prefix field holds the first 10 chars of the stored key (empty if not stored).
func ListStoredProviderKeys() []ProviderKey {
	providers := []string{"anthropic", "openai", "openrouter", "minimax", "mimo"}
	result := make([]ProviderKey, 0, len(providers))
	for _, p := range providers {
		pk := ProviderKey{Provider: p}
		if k, err := keyring.Get(keyringService, providerKeyringUser(p)); err == nil && k != "" {
			if len(k) > 10 {
				pk.Prefix = k[:10]
			} else {
				pk.Prefix = k
			}
		}
		result = append(result, pk)
	}
	return result
}

// loadExeEnvFilePath returns the path to the .env file next to the executable.
func loadExeEnvFilePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "..", "..", ".env")
}

// loadKeyFromEnvFile reads a .env file and extracts the value of the given variable name.
func loadKeyFromEnvFile(path, varName string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	prefix := varName + "="
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
		}
	}
	return ""
}
