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
	if got := p.APIKey(Credentials{Access: "tok"}); got != "tok" {
		t.Errorf("APIKey = %q", got)
	}
}

func TestAnthropicExchangeAuthorizationCode(t *testing.T) {
	setNow(t, 1_000_000)

	var gotGrant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		gotGrant, _ = m["grant_type"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"acc","refresh_token":"ref","expires_in":3600}`))
	}))
	defer srv.Close()

	p := newAnthropicProvider()
	p.tokenURL = srv.URL

	creds, err := p.exchangeAuthorizationCode(context.Background(), "code", "state", "verifier")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if gotGrant != "authorization_code" {
		t.Errorf("grant_type = %q", gotGrant)
	}
	if creds.Access != "acc" || creds.Refresh != "ref" {
		t.Errorf("creds = %+v", creds)
	}
	// 1_000_000 + 3600*1000 - 5*60*1000 = 4_300_000.
	if creds.Expires != 4_300_000 {
		t.Errorf("expires = %d, want 4300000", creds.Expires)
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
