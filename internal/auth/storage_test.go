package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

type testProvider struct {
	id        string
	refreshes int
}

func (p *testProvider) ID() string                  { return p.id }
func (p *testProvider) Name() string                { return "Test" }
func (p *testProvider) UsesCallbackServer() bool    { return false }
func (p *testProvider) APIKey(c Credentials) string { return c.Access }
func (p *testProvider) Login(context.Context, LoginCallbacks) (Credentials, error) {
	return Credentials{Access: "logged-in"}, nil
}
func (p *testProvider) RefreshToken(_ context.Context, _ Credentials) (Credentials, error) {
	p.refreshes++
	return Credentials{Access: "refreshed", Refresh: "r2", Expires: nowMillis() + 60_000}, nil
}

func TestStorageRoundTrip(t *testing.T) {
	storage := NewStorage(NewMemoryBackend())
	if err := storage.Set("provider", Credentials{Access: "token", Refresh: "refresh"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := storage.Get("provider")
	if err != nil || !ok || got.Access != "token" || got.Refresh != "refresh" {
		t.Fatalf("Get = %+v, %v, %v", got, ok, err)
	}
	if err := storage.Remove("provider"); err != nil || storage.HasLogin("provider") {
		t.Fatalf("remove failed: %v", err)
	}
}

func TestAccessTokenRefreshing(t *testing.T) {
	provider := &testProvider{id: "test-refresh"}
	RegisterProvider(provider)
	defer UnregisterProvider(provider.id)
	storage := NewStorage(NewMemoryBackend())
	if err := storage.Set(provider.id, Credentials{Access: "old", Refresh: "r", Expires: nowMillis() - 1}); err != nil {
		t.Fatal(err)
	}
	token, err := storage.AccessTokenRefreshing(context.Background(), provider.id)
	if err != nil || token != "refreshed" || provider.refreshes != 1 {
		t.Fatalf("token=%q refreshes=%d err=%v", token, provider.refreshes, err)
	}
}

func TestLoginPersistsThroughBackend(t *testing.T) {
	provider := &testProvider{id: "test-login"}
	RegisterProvider(provider)
	defer UnregisterProvider(provider.id)
	storage := NewStorage(NewMemoryBackend())
	if err := storage.Login(context.Background(), provider.id, LoginCallbacks{}); err != nil {
		t.Fatal(err)
	}
	credentials, ok, err := storage.Get(provider.id)
	if err != nil || !ok || credentials.Access != "logged-in" {
		t.Fatalf("credentials=%+v ok=%v err=%v", credentials, ok, err)
	}
}

func TestGeneratePKCE(t *testing.T) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); challenge != want {
		t.Fatalf("challenge=%q want=%q", challenge, want)
	}
}

func TestCredentialsExpiry(t *testing.T) {
	if (Credentials{Expires: 0}).Expired() || !(Credentials{Expires: nowMillis() - 1}).Expired() || (Credentials{Expires: nowMillis() + 60_000}).Expired() {
		t.Fatal("credential expiry classification is incorrect")
	}
}

func TestCredentialsJSONRoundTripFlat(t *testing.T) {
	in := Credentials{Access: "a", Refresh: "r", Expires: 123, Extra: map[string]any{"accountId": "acct"}}
	data, err := in.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var out Credentials
	if err := out.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	if out.Access != "a" || out.Refresh != "r" || out.Expires != 123 || out.StringExtra("accountId") != "acct" {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}
