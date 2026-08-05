package daemon

import (
	"testing"

	"github.com/zalando/go-keyring"
)

func init() {
	// Hermetic: keep credential RPC tests off the real OS keychain.
	keyring.MockInit()
}

// callHandler invokes a registered handler by name and fails the test on error.
func callHandler(t *testing.T, s *Server, name string, data map[string]any) map[string]any {
	t.Helper()
	h := s.GetHandler(name)
	if h == nil {
		t.Fatalf("handler %q not registered", name)
	}
	resp, err := h(data)
	if err != nil {
		t.Fatalf("handler %q: %v", name, err)
	}
	return resp
}

func providerEntry(t *testing.T, resp map[string]any, provider string) ProviderCredEntry {
	t.Helper()
	cs, err := parseCredResponse(resp)
	if err != nil {
		t.Fatalf("parseCredResponse: %v", err)
	}
	return ProviderCredEntry{Provider: provider, Status: cs.Providers[provider]}
}

func TestCredentialHandlers_StoreStatusDelete(t *testing.T) {
	s := &Server{handlers: make(map[string]HandlerFunc)}
	RegisterCredentialHandlers(s)

	// openai has a single plain API-key method; its ID is the method id used by
	// the per-method store. Resolve it from the status so the test is robust to
	// the exact id string.
	status := callHandler(t, s, "creds.status", map[string]any{})
	entry := providerEntry(t, status, "openai")
	if len(entry.Status.Methods) == 0 {
		t.Fatal("openai has no credential methods")
	}
	methodID := entry.Status.Methods[0].ID
	if entry.Status.HasCredential() {
		t.Fatal("openai should start with no stored credential")
	}

	// Store.
	stored := callHandler(t, s, "creds.store", map[string]any{
		"provider":  "openai",
		"method_id": methodID,
		"key":       "sk-openai-test-1234567890",
	})
	cs, err := parseCredResponse(stored)
	if err != nil {
		t.Fatalf("store parse: %v", err)
	}
	if !cs.Providers["openai"].HasCredential() {
		t.Fatal("openai should have a stored credential after creds.store")
	}
	if cs.Backend == "" {
		t.Error("expected a non-empty backend in the response")
	}

	// Delete.
	deleted := callHandler(t, s, "creds.delete", map[string]any{
		"provider":  "openai",
		"method_id": methodID,
	})
	cs2, err := parseCredResponse(deleted)
	if err != nil {
		t.Fatalf("delete parse: %v", err)
	}
	if cs2.Providers["openai"].HasCredential() {
		t.Fatal("openai credential should be gone after creds.delete")
	}
}

func TestCredentialHandlers_Validation(t *testing.T) {
	s := &Server{handlers: make(map[string]HandlerFunc)}
	RegisterCredentialHandlers(s)

	resp := callHandler(t, s, "creds.store", map[string]any{"provider": "openai"})
	if resp["status"] != "error" {
		t.Errorf("creds.store with missing key: status = %v, want error", resp["status"])
	}
	if _, err := parseCredResponse(resp); err == nil {
		t.Error("parseCredResponse should surface the error status")
	}
}
