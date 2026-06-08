package ui

import (
	"strings"
	"testing"

	"github.com/get-vix/vix/internal/config"
)

// TestMaskSecret checks the first-6-visible masking used by the key popup.
func TestMaskSecret(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"abcdef", "abcdef"},
		{"sk-ant-123", "sk-ant" + strings.Repeat("•", 4)},
		{"héllo-secret", "héllo-" + strings.Repeat("•", 6)},
	}
	for _, c := range cases {
		if got := maskSecret(c.in); got != c.want {
			t.Errorf("maskSecret(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestAuthButtonsFor checks that delete/make-default buttons appear only when the
// credential is stored / not already the default, and that OAuth-less providers
// expose no OAuth-row buttons.
func TestAuthButtonsFor(t *testing.T) {
	ids := func(btns []authButton) []string {
		out := make([]string, len(btns))
		for i, b := range btns {
			out[i] = b.id
		}
		return out
	}
	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	// API key absent: only "create".
	st := config.ProviderAuthStatus{APIKeyStored: false, Default: config.AuthDefaultAPIKey}
	if got := ids(authButtonsFor(st, authRowAPIKey)); !eq(got, []string{"set_key"}) {
		t.Errorf("api key absent: got %v", got)
	}

	// API key stored and default: update + delete (no make-default).
	st = config.ProviderAuthStatus{APIKeyStored: true, Default: config.AuthDefaultAPIKey}
	if got := ids(authButtonsFor(st, authRowAPIKey)); !eq(got, []string{"set_key", "del_key"}) {
		t.Errorf("api key stored+default: got %v", got)
	}

	// API key stored but NOT default: update + delete + make-default.
	st = config.ProviderAuthStatus{APIKeyStored: true, Default: config.AuthDefaultOAuth}
	if got := ids(authButtonsFor(st, authRowAPIKey)); !eq(got, []string{"set_key", "del_key", "default_key"}) {
		t.Errorf("api key stored+not-default: got %v", got)
	}

	// OAuth unsupported: no buttons on the OAuth row.
	st = config.ProviderAuthStatus{OAuthSupported: false}
	if got := authButtonsFor(st, authRowOAuth); len(got) != 0 {
		t.Errorf("oauth unsupported: expected no buttons, got %v", ids(got))
	}

	// OAuth stored but not default: update + delete + make-default.
	st = config.ProviderAuthStatus{OAuthSupported: true, OAuthStored: true, Default: config.AuthDefaultAPIKey}
	if got := ids(authButtonsFor(st, authRowOAuth)); !eq(got, []string{"set_token", "del_token", "default_token"}) {
		t.Errorf("oauth stored+not-default: got %v", got)
	}
}

// TestTabBarHasModelsAndSettings verifies the tab bar shows the Models [F3] and
// Settings [F4] tabs after the remap.
func TestTabBarHasModelsAndSettings(t *testing.T) {
	bar := renderTabBar(TabKindModels, 120, NewStyles(false), true, false, false, false)
	for _, want := range []string{"Models [F3]", "Settings [F4]", "Sessions [F1]", "Workspace [F2]"} {
		if !strings.Contains(bar, want) {
			t.Errorf("tab bar missing %q\n%s", want, bar)
		}
	}
}
