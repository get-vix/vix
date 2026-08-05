package config

import "testing"

func TestCredentialBackendIsDazSecrets(t *testing.T) {
	if got := CredentialBackend(); got != BackendProvider {
		t.Fatalf("CredentialBackend() = %q, want %q", got, BackendProvider)
	}
}
