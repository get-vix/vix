package ui

import (
	"testing"
)

// TestStartProviderLoginUnknownProviderReturnsNil verifies a provider with no
// OAuth login yields a nil cmd (no-op). The former keyless short-circuit that
// returned an ErrKeychainUnavailable cmd is gone: OAuth logins now fall back to
// the plaintext auth.json automatically, so there is no synchronous error path.
// The #53 no-freeze guarantee and the #56 fallback disclosure are covered
// end-to-end (e2e/scenarios/regressions_login_test.go).
func TestStartProviderLoginUnknownProviderReturnsNil(t *testing.T) {
	if cmd := startProviderLogin("not-a-real-provider"); cmd != nil {
		t.Errorf("expected nil cmd for unknown provider, got non-nil")
	}
}
