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
// credential is stored / not already the default, for both API-key and OAuth
// (token) methods.
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
	ms := config.MethodStatus{Stored: false}
	if got := ids(authButtonsFor(ms)); !eq(got, []string{"set_key"}) {
		t.Errorf("api key absent: got %v", got)
	}

	// API key stored and default: update + delete (no make-default).
	ms = config.MethodStatus{Stored: true, IsDefault: true}
	if got := ids(authButtonsFor(ms)); !eq(got, []string{"set_key", "del_key"}) {
		t.Errorf("api key stored+default: got %v", got)
	}

	// API key stored but NOT default: update + delete + make-default.
	ms = config.MethodStatus{Stored: true, IsDefault: false}
	if got := ids(authButtonsFor(ms)); !eq(got, []string{"set_key", "del_key", "default_key"}) {
		t.Errorf("api key stored+not-default: got %v", got)
	}

	// OAuth method absent: only "create token".
	ms = config.MethodStatus{OAuth: true, Stored: false}
	if got := ids(authButtonsFor(ms)); !eq(got, []string{"set_token"}) {
		t.Errorf("oauth absent: got %v", got)
	}

	// OAuth stored but not default: update + delete + make-default.
	ms = config.MethodStatus{OAuth: true, Stored: true, IsDefault: false}
	if got := ids(authButtonsFor(ms)); !eq(got, []string{"set_token", "del_token", "default_token"}) {
		t.Errorf("oauth stored+not-default: got %v", got)
	}
}

// TestTabBarHasModelsAndSettings verifies the tab bar shows the tabs in their
// remapped F-key order, including the MCP [F4], Jobs & Triggers [F5], and
// Settings [F6] tabs.
func TestTabBarHasModelsAndSettings(t *testing.T) {
	bar := renderTabBar(TabKindModels, 160, NewStyles(false), true, false, false, false, false)
	for _, want := range []string{"Threads [F1]", "Workspace [F2]", "Models [F3]", "MCP [F4]", "Jobs & Triggers [F5]", "Settings [F6]"} {
		if !strings.Contains(bar, want) {
			t.Errorf("tab bar missing %q\n%s", want, bar)
		}
	}
}
