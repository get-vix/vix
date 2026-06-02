package auth

import "testing"

func TestBuiltinProvidersRegistered(t *testing.T) {
	for _, id := range []string{"anthropic", "github-copilot", "openai-codex"} {
		if _, ok := GetProvider(id); !ok {
			t.Errorf("built-in provider %q not registered", id)
		}
	}
	if got := len(GetProviders()); got != 3 {
		t.Errorf("GetProviders len = %d, want 3", got)
	}
}

func TestRegisterAndUnregisterCustomProvider(t *testing.T) {
	t.Cleanup(ResetProviders)

	custom := &stubProvider{id: "custom", name: "Custom"}
	RegisterProvider(custom)

	if _, ok := GetProvider("custom"); !ok {
		t.Fatal("custom provider not found after register")
	}
	if len(GetProviders()) != 4 {
		t.Errorf("expected 4 providers after register")
	}

	UnregisterProvider("custom")
	if _, ok := GetProvider("custom"); ok {
		t.Error("custom provider still present after unregister")
	}
	if len(GetProviders()) != 3 {
		t.Errorf("expected 3 providers after unregister")
	}
}

func TestUnregisterBuiltinRestoresDefault(t *testing.T) {
	t.Cleanup(ResetProviders)

	// Override a built-in with a stub of the same id.
	RegisterProvider(&stubProvider{id: "anthropic", name: "Overridden"})
	if p, _ := GetProvider("anthropic"); p.Name() != "Overridden" {
		t.Fatalf("override did not take: %q", p.Name())
	}

	// Unregistering a built-in restores its default implementation.
	UnregisterProvider("anthropic")
	if p, _ := GetProvider("anthropic"); p.Name() != "Anthropic (Claude Pro/Max)" {
		t.Errorf("built-in not restored: %q", p.Name())
	}
}

func TestResetProviders(t *testing.T) {
	RegisterProvider(&stubProvider{id: "temp", name: "Temp"})
	ResetProviders()
	if _, ok := GetProvider("temp"); ok {
		t.Error("ResetProviders did not drop custom provider")
	}
	if len(GetProviders()) != 3 {
		t.Errorf("expected 3 providers after reset")
	}
}
