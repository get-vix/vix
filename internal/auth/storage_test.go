package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

// fakeProvider is a test Provider whose APIKey is the access token and whose
// RefreshToken bumps the access token + expiry.
type fakeProvider struct {
	id        string
	refreshes int
}

func (p *fakeProvider) ID() string                  { return p.id }
func (p *fakeProvider) Name() string                { return "Fake" }
func (p *fakeProvider) UsesCallbackServer() bool    { return false }
func (p *fakeProvider) APIKey(c Credentials) string { return c.Access }
func (p *fakeProvider) Login(context.Context, LoginCallbacks) (Credentials, error) {
	return Credentials{Access: "logged-in"}, nil
}
func (p *fakeProvider) RefreshToken(_ context.Context, _ Credentials) (Credentials, error) {
	p.refreshes++
	return Credentials{Access: "refreshed", Refresh: "r2", Expires: nowMillis() + 60_000}, nil
}

func TestStorageRoundTrip(t *testing.T) {
	st := NewStorage(NewMemoryBackend())
	if st.HasLogin("foo") {
		t.Fatal("expected no login initially")
	}
	if err := st.Set("foo", Credentials{Access: "tok", Refresh: "r", Expires: 0}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !st.HasLogin("foo") {
		t.Fatal("expected login after Set")
	}
	got, ok, err := st.Get("foo")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Access != "tok" || got.Refresh != "r" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if err := st.Remove("foo"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if st.HasLogin("foo") {
		t.Fatal("expected no login after Remove")
	}
}

func TestAccessTokenRefreshing(t *testing.T) {
	p := &fakeProvider{id: "fake-refresh"}
	RegisterProvider(p)
	defer UnregisterProvider(p.id)

	st := NewStorage(NewMemoryBackend())

	// Non-expiring token: returned as-is, no refresh.
	if err := st.Set(p.id, Credentials{Access: "tok", Expires: 0}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	tok, err := st.AccessTokenRefreshing(context.Background(), p.id)
	if err != nil || tok != "tok" {
		t.Fatalf("expected tok, got %q err=%v", tok, err)
	}
	if p.refreshes != 0 {
		t.Errorf("did not expect a refresh, got %d", p.refreshes)
	}

	// Expired token: should refresh once and return the new access token.
	if err := st.Set(p.id, Credentials{Access: "old", Refresh: "r", Expires: nowMillis() - 1000}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	tok, err = st.AccessTokenRefreshing(context.Background(), p.id)
	if err != nil {
		t.Fatalf("AccessTokenRefreshing: %v", err)
	}
	if tok != "refreshed" {
		t.Errorf("expected refreshed token, got %q", tok)
	}
	if p.refreshes != 1 {
		t.Errorf("expected exactly 1 refresh, got %d", p.refreshes)
	}
}

func TestGeneratePKCE(t *testing.T) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE: %v", err)
	}
	if verifier == "" || challenge == "" {
		t.Fatal("empty verifier/challenge")
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if challenge != want {
		t.Errorf("challenge != base64url(sha256(verifier)): got %q want %q", challenge, want)
	}
}

func TestFileBackendRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	f := &fileBackend{path: path}

	if _, ok, _ := f.Get("anthropic-oauth"); ok {
		t.Fatal("expected no value initially")
	}
	if err := f.Set("anthropic-oauth", "secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, ok, err := f.Get("anthropic-oauth")
	if err != nil || !ok || v != "secret" {
		t.Fatalf("Get: v=%q ok=%v err=%v", v, ok, err)
	}
	// The plaintext file must be owner-only (0600).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("auth.json perm = %o, want 600", perm)
	}
	if err := f.Delete("anthropic-oauth"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := f.Get("anthropic-oauth"); ok {
		t.Fatal("expected no value after Delete")
	}
	// Deleting an absent key is a no-op, not an error.
	if err := f.Delete("missing"); err != nil {
		t.Errorf("Delete(missing): %v", err)
	}
}

func TestSelectDefaultBackend(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), "auth.json")
	t.Cleanup(func() { SetAuthFilePath("") })
	SetAuthFilePath(authFile)

	// Keyring usable -> keyring backend.
	keyring.MockInit()
	if _, ok := selectDefaultBackend().(keyringBackend); !ok {
		t.Errorf("keyring usable: expected keyringBackend, got %T", selectDefaultBackend())
	}

	// Keyring unusable -> plaintext file backend at the configured path (no
	// opt-in required; mirrors the API-key store).
	keyring.MockInitWithError(errors.New("no keychain"))
	b, ok := selectDefaultBackend().(*fileBackend)
	if !ok {
		t.Fatalf("keyless: expected *fileBackend, got %T", selectDefaultBackend())
	}
	if b.path != authFile {
		t.Errorf("fileBackend path = %q, want %q", b.path, authFile)
	}
}

func TestLoginPersistsToFileFallback(t *testing.T) {
	p := &fakeProvider{id: "fake-file-login"}
	RegisterProvider(p)
	defer UnregisterProvider(p.id)

	authFile := filepath.Join(t.TempDir(), "auth.json")
	st := NewStorage(&fileBackend{path: authFile})

	if err := st.Login(context.Background(), p.id, LoginCallbacks{}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	creds, ok, err := st.Get(p.id)
	if err != nil || !ok {
		t.Fatalf("Get after login: ok=%v err=%v", ok, err)
	}
	if creds.Access != "logged-in" {
		t.Errorf("unexpected stored creds: %+v", creds)
	}
	if _, err := os.Stat(authFile); err != nil {
		t.Errorf("expected auth.json written: %v", err)
	}
}

// TestDefaultStorageFallsBackToFileWithoutKeychain is the regression for #56: on
// a machine with no usable OS keychain, an OAuth login must persist to the
// plaintext auth.json automatically (no opt-in flag). It also exercises the
// ordering hazard that broke the old opt-in: DefaultStorage() is used *before*
// the auth-file path is wired in, and SetAuthFilePath must invalidate the cached
// backend so the login still lands in the intended auth.json.
func TestDefaultStorageFallsBackToFileWithoutKeychain(t *testing.T) {
	p := &fakeProvider{id: "fake-fallback-login"}
	RegisterProvider(p)
	defer UnregisterProvider(p.id)

	keyring.MockInitWithError(errors.New("no keychain"))
	t.Cleanup(func() { SetAuthFilePath("") })

	// Early use before the path is configured (as via startup credential
	// resolution): builds a storage over a temp-file backend.
	SetAuthFilePath("")
	_ = DefaultStorage()

	// The process then wires in the real path; this must rebuild the storage.
	authFile := filepath.Join(t.TempDir(), "auth.json")
	SetAuthFilePath(authFile)

	if err := DefaultStorage().Login(context.Background(), p.id, LoginCallbacks{}); err != nil {
		t.Fatalf("keyless login should fall back to file, got: %v", err)
	}
	creds, ok, err := DefaultStorage().Get(p.id)
	if err != nil || !ok || creds.Access != "logged-in" {
		t.Fatalf("expected stored creds after fallback login: ok=%v err=%v creds=%+v", ok, err, creds)
	}
	if _, err := os.Stat(authFile); err != nil {
		t.Errorf("expected token persisted to %s: %v", authFile, err)
	}
}

func TestCredentialsExpiry(t *testing.T) {
	if (Credentials{Expires: 0}).Expired() {
		t.Error("Expires==0 should never be expired")
	}
	if !(Credentials{Expires: nowMillis() - 1}).Expired() {
		t.Error("past expiry should be expired")
	}
	if (Credentials{Expires: nowMillis() + 60_000}).Expired() {
		t.Error("future expiry should not be expired")
	}
}

func TestCredentialsJSONRoundTripFlat(t *testing.T) {
	in := Credentials{Access: "a", Refresh: "r", Expires: 123, Extra: map[string]any{"accountId": "acct"}}
	data, err := in.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Credentials
	if err := out.UnmarshalJSON(data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Access != "a" || out.Refresh != "r" || out.Expires != 123 || out.StringExtra("accountId") != "acct" {
		t.Errorf("round-trip mismatch: %+v", out)
	}
}
