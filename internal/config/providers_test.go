package config

import "testing"

// TestProviderAuthWellFormed checks every known provider has at least one auth
// method and that API-key methods declare both an env var and a keychain user.
func TestProviderAuthWellFormed(t *testing.T) {
	for _, p := range KnownProviders() {
		methods := AuthMethodsFor(p)
		if len(methods) == 0 {
			t.Errorf("provider %q has no auth methods", p)
		}
		for i, m := range methods {
			if m.Kind == APIKeyAuth {
				if m.EnvVar == "" {
					t.Errorf("provider %q method %d: APIKeyAuth with empty EnvVar", p, i)
				}
				if m.Keyring == "" {
					t.Errorf("provider %q method %d: APIKeyAuth with empty Keyring", p, i)
				}
			}
		}
	}
}

// TestPrimaryEnvVar pins the canonical env var per provider (used in error
// messages) and confirms OAuth-bearer methods are not chosen as primary.
func TestPrimaryEnvVar(t *testing.T) {
	want := map[string]string{
		"anthropic":  "ANTHROPIC_API_KEY",
		"openai":     "OPENAI_API_KEY",
		"openrouter": "OPENROUTER_API_KEY",
		"minimax":    "MINIMAX_API_KEY",
		"mimo":       "MIMO_API_KEY",
	}
	for p, env := range want {
		if got := PrimaryEnvVar(p); got != env {
			t.Errorf("PrimaryEnvVar(%q) = %q, want %q", p, got, env)
		}
	}
	if got := PrimaryEnvVar("unknown"); got != "" {
		t.Errorf("PrimaryEnvVar(unknown) = %q, want empty", got)
	}
}

func TestKnownProvidersStable(t *testing.T) {
	got := KnownProviders()
	want := []string{"anthropic", "openai", "openrouter", "minimax", "mimo", "github-copilot"}
	if len(got) != len(want) {
		t.Fatalf("KnownProviders len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("KnownProviders[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
