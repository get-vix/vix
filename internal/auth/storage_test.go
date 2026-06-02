package auth

import (
	"context"
	"errors"
	"testing"
)

// stubProvider is a configurable Provider for registry/storage tests.
type stubProvider struct {
	id      string
	name    string
	refresh func(context.Context, Credentials) (Credentials, error)
	login   func(context.Context, LoginCallbacks) (Credentials, error)
}

func (p *stubProvider) ID() string                  { return p.id }
func (p *stubProvider) Name() string                { return p.name }
func (p *stubProvider) UsesCallbackServer() bool    { return false }
func (p *stubProvider) APIKey(c Credentials) string { return c.Access }

func (p *stubProvider) RefreshToken(ctx context.Context, c Credentials) (Credentials, error) {
	if p.refresh == nil {
		return Credentials{}, errors.New("refresh not implemented")
	}
	return p.refresh(ctx, c)
}

func (p *stubProvider) Login(ctx context.Context, cb LoginCallbacks) (Credentials, error) {
	if p.login == nil {
		return Credentials{}, errors.New("login not implemented")
	}
	return p.login(ctx, cb)
}

func registerStub(t *testing.T, p *stubProvider) {
	t.Helper()
	RegisterProvider(p)
	t.Cleanup(ResetProviders)
}

func TestStorageSetGetRemove(t *testing.T) {
	s := NewStorage(NewMemoryBackend())
	creds := Credentials{Access: "a", Refresh: "r", Expires: 123, Extra: map[string]any{"accountId": "x"}}

	if err := s.Set("anthropic", creds); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := s.Get("anthropic")
	if err != nil || !ok {
		t.Fatalf("Get ok=%v err=%v", ok, err)
	}
	if got.Access != "a" || got.StringExtra("accountId") != "x" {
		t.Errorf("got = %+v", got)
	}
	if !s.HasLogin("anthropic") {
		t.Error("HasLogin = false after Set")
	}

	if err := s.Remove("anthropic"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok, _ := s.Get("anthropic"); ok {
		t.Error("credential still present after Remove")
	}
}

func TestStorageList(t *testing.T) {
	registerStub(t, &stubProvider{id: "stub"})
	s := NewStorage(NewMemoryBackend())
	_ = s.Set("stub", Credentials{Access: "a"})

	list := s.List()
	found := false
	for _, id := range list {
		if id == "stub" {
			found = true
		}
	}
	if !found {
		t.Errorf("List = %v, expected to contain stub", list)
	}
}

func TestStorageAccessToken(t *testing.T) {
	setNow(t, 1000)
	registerStub(t, &stubProvider{id: "stub"})
	s := NewStorage(NewMemoryBackend())

	// Missing.
	if _, _, ok := s.AccessToken("stub"); ok {
		t.Error("expected ok=false when no creds stored")
	}

	// Not expired.
	_ = s.Set("stub", Credentials{Access: "valid", Expires: 2000})
	tok, expired, ok := s.AccessToken("stub")
	if !ok || expired || tok != "valid" {
		t.Errorf("got tok=%q expired=%v ok=%v", tok, expired, ok)
	}

	// Expired.
	_ = s.Set("stub", Credentials{Access: "stale", Expires: 500})
	_, expired, ok = s.AccessToken("stub")
	if !ok || !expired {
		t.Errorf("expected expired=true ok=true, got expired=%v ok=%v", expired, ok)
	}
}

func TestStorageRefreshOnExpiry(t *testing.T) {
	setNow(t, 1000)
	refreshCalled := 0
	registerStub(t, &stubProvider{
		id: "stub",
		refresh: func(_ context.Context, c Credentials) (Credentials, error) {
			refreshCalled++
			if c.Refresh != "rtok" {
				t.Errorf("refresh got refresh token %q", c.Refresh)
			}
			return Credentials{Access: "fresh", Refresh: "rtok2", Expires: 9999}, nil
		},
	})
	s := NewStorage(NewMemoryBackend())
	_ = s.Set("stub", Credentials{Access: "stale", Refresh: "rtok", Expires: 500})

	tok, err := s.AccessTokenRefreshing(context.Background(), "stub")
	if err != nil {
		t.Fatalf("AccessTokenRefreshing: %v", err)
	}
	if tok != "fresh" {
		t.Errorf("token = %q, want fresh", tok)
	}
	if refreshCalled != 1 {
		t.Errorf("refresh called %d times, want 1", refreshCalled)
	}

	// The refreshed credentials must be persisted.
	got, _, _ := s.Get("stub")
	if got.Access != "fresh" || got.Refresh != "rtok2" {
		t.Errorf("persisted creds = %+v", got)
	}
}

func TestStorageNoRefreshWhenValid(t *testing.T) {
	setNow(t, 1000)
	registerStub(t, &stubProvider{
		id: "stub",
		refresh: func(context.Context, Credentials) (Credentials, error) {
			t.Fatal("refresh should not be called for a valid token")
			return Credentials{}, nil
		},
	})
	s := NewStorage(NewMemoryBackend())
	_ = s.Set("stub", Credentials{Access: "valid", Expires: 5000})

	tok, err := s.AccessTokenRefreshing(context.Background(), "stub")
	if err != nil || tok != "valid" {
		t.Errorf("tok=%q err=%v", tok, err)
	}
}

func TestStorageRefreshMissing(t *testing.T) {
	registerStub(t, &stubProvider{id: "stub"})
	s := NewStorage(NewMemoryBackend())
	if _, err := s.AccessTokenRefreshing(context.Background(), "stub"); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("err = %v, want ErrNoCredentials", err)
	}
}

func TestStorageRefreshUnknownProvider(t *testing.T) {
	s := NewStorage(NewMemoryBackend())
	if _, err := s.AccessTokenRefreshing(context.Background(), "does-not-exist"); err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestStorageRefreshFailure(t *testing.T) {
	setNow(t, 1000)
	registerStub(t, &stubProvider{
		id: "stub",
		refresh: func(context.Context, Credentials) (Credentials, error) {
			return Credentials{}, errors.New("network down")
		},
	})
	s := NewStorage(NewMemoryBackend())
	_ = s.Set("stub", Credentials{Access: "stale", Refresh: "r", Expires: 500})

	if _, err := s.AccessTokenRefreshing(context.Background(), "stub"); err == nil {
		t.Error("expected refresh failure error")
	}
}

func TestStorageLogin(t *testing.T) {
	registerStub(t, &stubProvider{
		id: "stub",
		login: func(context.Context, LoginCallbacks) (Credentials, error) {
			return Credentials{Access: "logged-in", Refresh: "r", Expires: 9999}, nil
		},
	})
	s := NewStorage(NewMemoryBackend())

	if err := s.Login(context.Background(), "stub", LoginCallbacks{}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	got, ok, _ := s.Get("stub")
	if !ok || got.Access != "logged-in" {
		t.Errorf("stored creds = %+v ok=%v", got, ok)
	}
}

func TestStorageLogout(t *testing.T) {
	s := NewStorage(NewMemoryBackend())
	_ = s.Set("anthropic", Credentials{Access: "a"})
	if err := s.Logout("anthropic"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if s.HasLogin("anthropic") {
		t.Error("still logged in after Logout")
	}
}
