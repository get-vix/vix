package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestGeneratePKCE(t *testing.T) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE: %v", err)
	}
	if verifier == "" || challenge == "" {
		t.Fatal("expected non-empty verifier and challenge")
	}

	// 32 random bytes base64url (no padding) -> 43 chars.
	if len(verifier) != 43 {
		t.Errorf("verifier length = %d, want 43", len(verifier))
	}

	// base64url alphabet only: no '+', '/', or '=' padding.
	if strings.ContainsAny(verifier, "+/=") || strings.ContainsAny(challenge, "+/=") {
		t.Errorf("verifier/challenge contain non-base64url chars")
	}

	// challenge must be base64url(sha256(verifier)).
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if challenge != want {
		t.Errorf("challenge = %q, want %q", challenge, want)
	}
}

func TestGeneratePKCEUnique(t *testing.T) {
	v1, _, _ := generatePKCE()
	v2, _, _ := generatePKCE()
	if v1 == v2 {
		t.Errorf("expected distinct verifiers across calls")
	}
}
