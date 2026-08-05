package config

import "testing"

// TestOAuthLoginID pins the provider→login-id mapping derived from the provider
// auth methods. Providers without an OAuth method must return "".
func TestOAuthLoginID(t *testing.T) {
	want := map[string]string{
		"anthropic":  "anthropic",
		"openai":     "openai-codex",
		"openrouter": "openrouter",
		"minimax":    "",
		"mimo":       "",
		"unknown":    "",
	}
	for p, id := range want {
		if got := OAuthLoginID(p); got != id {
			t.Errorf("OAuthLoginID(%q) = %q, want %q", p, got, id)
		}
	}
}

// TestReorderAuthMethods checks that the method whose ID matches the preference
// is promoted to the front, that legacy kind-level preferences still work, and
// that an unknown/empty preference leaves the order unchanged.
func TestReorderAuthMethods(t *testing.T) {
	methods := AuthMethodsFor("anthropic") // [apikey, claude-oauth-token(bearer), oauth-token]

	// No preference: order unchanged.
	if got := reorderAuthMethods(methods, ""); !sameOrder(got, methods) {
		t.Errorf("no-pref reorder changed order")
	}

	// Prefer the OAuth login by its method ID: it leads.
	oauthID := methods[len(methods)-1].ID()
	oauthFirst := reorderAuthMethods(methods, oauthID)
	if oauthFirst[0].ID() != oauthID {
		t.Errorf("prefer-by-id: expected %q first, got %q", oauthID, oauthFirst[0].ID())
	}
	if !isOAuthMethod(oauthFirst[0]) {
		t.Errorf("prefer-by-id: expected an OAuth method first")
	}

	// Prefer the API key by its method ID: it leads (already first here).
	apiID := methods[0].ID()
	apiFirst := reorderAuthMethods(methods, apiID)
	if apiFirst[0].ID() != apiID {
		t.Errorf("prefer-api-by-id: expected %q first, got %q", apiID, apiFirst[0].ID())
	}

	// Legacy kind-level preference still promotes the first method of that kind.
	legacyOAuth := reorderAuthMethods(methods, AuthDefaultOAuth)
	if !isOAuthMethod(legacyOAuth[0]) {
		t.Errorf("legacy prefer-oauth: expected an OAuth method first")
	}

	// Unknown preference leaves order unchanged.
	if got := reorderAuthMethods(methods, "does-not-exist"); !sameOrder(got, methods) {
		t.Errorf("unknown-pref reorder changed order")
	}

	// Single-method provider is unaffected regardless of preference.
	mm := AuthMethodsFor("minimax")
	if got := reorderAuthMethods(mm, AuthDefaultOAuth); !sameOrder(got, mm) {
		t.Errorf("single-method provider should be unaffected by preference")
	}
}

func sameOrder(a, b []AuthMethod) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].EnvVar != b[i].EnvVar || a[i].LoginID != b[i].LoginID || a[i].HeaderStyle != b[i].HeaderStyle {
			return false
		}
	}
	return true
}

// TestEffectiveDefaultMethod covers default-method derivation across stored
// state, explicit per-method preferences, and legacy kind-level preferences.
func TestEffectiveDefaultMethod(t *testing.T) {
	apiKey := MethodStatus{ID: "k", Label: "API Key"}
	oauth := MethodStatus{ID: "o", Label: "OAuth", OAuth: true}
	stored := func(m MethodStatus) MethodStatus { m.Stored = true; return m }

	cases := []struct {
		name    string
		methods []MethodStatus
		pref    string
		want    string
	}{
		{"empty", nil, "", ""},
		{"none-stored-falls-to-first", []MethodStatus{apiKey, oauth}, "", "k"},
		{"first-stored-wins", []MethodStatus{stored(apiKey), oauth}, "", "k"},
		{"only-oauth-stored", []MethodStatus{apiKey, stored(oauth)}, "", "o"},
		{"both-stored-first-wins", []MethodStatus{stored(apiKey), stored(oauth)}, "", "k"},
		{"explicit-id-stored", []MethodStatus{stored(apiKey), stored(oauth)}, "o", "o"},
		{"explicit-id-not-stored-falls-through", []MethodStatus{stored(apiKey), oauth}, "o", "k"},
		{"legacy-oauth", []MethodStatus{stored(apiKey), stored(oauth)}, AuthDefaultOAuth, "o"},
		{"legacy-apikey", []MethodStatus{stored(apiKey), stored(oauth)}, AuthDefaultAPIKey, "k"},
	}
	for _, c := range cases {
		if got := effectiveDefaultMethod(c.methods, c.pref); got != c.want {
			t.Errorf("%s: effectiveDefaultMethod(_, %q) = %q, want %q", c.name, c.pref, got, c.want)
		}
	}
}
