package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// generatePKCE produces a PKCE code verifier and its S256 challenge.
//
// It mirrors pi's pkce.ts: a 32-byte random verifier, base64url-encoded
// without padding, and a challenge that is the base64url-encoded SHA-256 of
// the verifier string.
func generatePKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}
