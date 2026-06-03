package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicProviderBasics(t *testing.T) {
	p := newAnthropicProvider()
	if p.ID() != "anthropic" {
		t.Errorf("ID = %q", p.ID())
	}
	if !p.UsesCallbackServer() {
		t.Errorf("expected UsesCallbackServer true")
	}
	// Obfuscated client id decodes to the real value.
	if p.clientID != "9d1c250a-e61b-44d9-88ed-5944d1962f5e" {
		t.Errorf("clientID = %q", p.clientID)
	}
	// The minted metered API key (stored under Extra) is the inference
	// credential; Access is only a legacy fallback.
	if got := p.APIKey(Credentials{Extra: map[string]any{"apiKey": "sk-ant-minted"}}); got != "sk-ant-minted" {
		t.Errorf("APIKey (minted) = %q", got)
	}
	if got := p.APIKey(Credentials{Access: "tok"}); got != "tok" {
		t.Errorf("APIKey (fallback) = %q", got)
	}
}

func TestAnthropicExchangeAuthorizationCode(t *testing.T) {
	var gotGrant, gotKeyAuth string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		gotGrant, _ = m["grant_type"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"acc","refresh_token":"ref","expires_in":3600}`))
	}))
	defer tokenSrv.Close()

	keySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKeyAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"raw_key":"sk-ant-minted"}`))
	}))
	defer keySrv.Close()

	p := newAnthropicProvider()
	p.tokenURL = tokenSrv.URL
	p.createAPIKeyURL = keySrv.URL

	creds, err := p.exchangeAuthorizationCode(context.Background(), "code", "state", "verifier")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if gotGrant != "authorization_code" {
		t.Errorf("grant_type = %q", gotGrant)
	}
	// The minted key is requested with the freshly-obtained access token.
	if gotKeyAuth != "Bearer acc" {
		t.Errorf("create_api_key Authorization = %q, want %q", gotKeyAuth, "Bearer acc")
	}
	// The metered API key is stored as the credential; the raw OAuth tokens are
	// discarded, and the credential is marked non-expiring.
	if got := p.APIKey(creds); got != "sk-ant-minted" {
		t.Errorf("minted key = %q, want sk-ant-minted", got)
	}
	if creds.Access != "" {
		t.Errorf("raw access token should not be stored, got %q", creds.Access)
	}
	if creds.Expired() {
		t.Errorf("minted-key credential should not be expired")
	}
}

func TestAnthropicCreateAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer the-access-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"raw_key":"sk-ant-raw"}`))
	}))
	defer srv.Close()

	p := newAnthropicProvider()
	p.createAPIKeyURL = srv.URL
	key, err := p.createAPIKey(context.Background(), "the-access-token")
	if err != nil {
		t.Fatalf("createAPIKey: %v", err)
	}
	if key != "sk-ant-raw" {
		t.Errorf("key = %q, want sk-ant-raw", key)
	}
}

func TestAnthropicCreateAPIKeyHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()

	p := newAnthropicProvider()
	p.createAPIKeyURL = srv.URL
	if _, err := p.createAPIKey(context.Background(), "tok"); err == nil {
		t.Fatal("expected error on non-2xx")
	}
}

func TestAnthropicRefreshToken(t *testing.T) {
	setNow(t, 2_000_000)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if m["grant_type"] != "refresh_token" {
			t.Errorf("grant_type = %v", m["grant_type"])
		}
		_, _ = w.Write([]byte(`{"access_token":"acc2","refresh_token":"ref2","expires_in":100}`))
	}))
	defer srv.Close()

	p := newAnthropicProvider()
	p.tokenURL = srv.URL

	creds, err := p.RefreshToken(context.Background(), Credentials{Refresh: "old"})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if creds.Access != "acc2" || creds.Refresh != "ref2" {
		t.Errorf("creds = %+v", creds)
	}
	if creds.Expires != 2_000_000+100*1000-300000 {
		t.Errorf("expires = %d", creds.Expires)
	}
}

func TestAnthropicExchangeHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()

	p := newAnthropicProvider()
	p.tokenURL = srv.URL
	if _, err := p.exchangeAuthorizationCode(context.Background(), "c", "s", "v"); err == nil {
		t.Fatal("expected error on non-2xx")
	}
}
