package ui

import (
	"errors"
	"testing"

	"github.com/get-vix/vix/internal/auth"
	"github.com/zalando/go-keyring"
)

// TestStartProviderLoginReturnsCmdWhenNoBackend is the regression guard for
// issue #53: when no credential store can persist the token (keychain unusable
// and the plaintext fallback disabled), startProviderLogin must return a
// tea.Cmd carrying the error. It must NOT emit the message synchronously via
// teaProgram.Send, which would deadlock the Bubble Tea event loop when called
// from within Model.Update.
func TestStartProviderLoginReturnsCmdWhenNoBackend(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keychain"))
	auth.EnablePlaintextFallback(false, "")
	t.Cleanup(func() {
		auth.EnablePlaintextFallback(false, "")
		keyring.MockInit()
	})

	cmd := startProviderLogin("anthropic")
	if cmd == nil {
		t.Fatal("expected a non-nil tea.Cmd, got nil")
	}
	msg := cmd()
	done, ok := msg.(loginDoneMsg)
	if !ok {
		t.Fatalf("expected loginDoneMsg, got %T", msg)
	}
	if done.provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", done.provider)
	}
	if !errors.Is(done.err, auth.ErrKeychainUnavailable) {
		t.Errorf("err = %v, want ErrKeychainUnavailable", done.err)
	}
}

// TestStartProviderLoginUnknownProviderReturnsNil verifies a provider with no
// OAuth login yields a nil cmd (no-op).
func TestStartProviderLoginUnknownProviderReturnsNil(t *testing.T) {
	if cmd := startProviderLogin("not-a-real-provider"); cmd != nil {
		t.Errorf("expected nil cmd for unknown provider, got non-nil")
	}
}
