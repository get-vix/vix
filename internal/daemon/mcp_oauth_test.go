package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/mcp"
	"golang.org/x/oauth2"
)

// fakeCredStore is an in-memory config.CredentialStore for tests.
type fakeCredStore struct {
	m map[string]string
}

func (f *fakeCredStore) Get(user string) (string, error) {
	v, ok := f.m[user]
	if !ok {
		return "", config.ErrCredNotFound
	}
	return v, nil
}
func (f *fakeCredStore) Set(user, secret string) error { f.m[user] = secret; return nil }
func (f *fakeCredStore) Delete(user string) error {
	if _, ok := f.m[user]; !ok {
		return config.ErrCredNotFound
	}
	delete(f.m, user)
	return nil
}
func (f *fakeCredStore) Backend() string { return "file" }

func TestMCPTokenStore_RoundTrip(t *testing.T) {
	store := mcpTokenStore{store: &fakeCredStore{m: map[string]string{}}}

	// Absent → (nil, nil).
	tok, err := store.Load("drive")
	if err != nil || tok != nil {
		t.Fatalf("Load absent = (%v, %v), want (nil, nil)", tok, err)
	}

	orig := &oauth2.Token{AccessToken: "a", RefreshToken: "r", TokenType: "Bearer"}
	if err := store.Save("drive", orig); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load("drive")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AccessToken != "a" || got.RefreshToken != "r" {
		t.Errorf("round-trip token = %+v", got)
	}

	if err := store.Delete("drive"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Delete is idempotent.
	if err := store.Delete("drive"); err != nil {
		t.Errorf("Delete idempotent = %v, want nil", err)
	}
	if got, _ := store.Load("drive"); got != nil {
		t.Errorf("token still present after delete: %+v", got)
	}
}

func TestMCPServerSummaries_OAuthNeedsAuth(t *testing.T) {
	s := newRunTriggerTestServer(t)
	writeHomeSettingsMCP(t, s, `{
      "mcp_servers": [
        {"name": "drive", "type": "url", "url": "https://example.test/mcp",
         "oauth": {"client_id": "cid", "auth_url": "https://a/authorize", "token_url": "https://a/token"}}
      ]
    }`)

	sums := s.MCPServerSummaries()
	if len(sums) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(sums))
	}
	drive := sums[0]
	// With no stored token, the server must report needs_auth without a probe.
	if drive.Auth != "needs_auth" {
		t.Errorf("Auth = %q, want needs_auth", drive.Auth)
	}
	if drive.Status != "needs_auth" {
		t.Errorf("Status = %q, want needs_auth", drive.Status)
	}
	if drive.Type != "url" {
		t.Errorf("Type = %q, want url", drive.Type)
	}
}

func TestMCPConfigByName(t *testing.T) {
	s := newRunTriggerTestServer(t)
	writeHomeSettingsMCP(t, s, `{
      "mcp_servers": [
        {"name": "drive", "type": "url", "url": "https://example.test/mcp",
         "oauth": {"client_id": "cid"}}
      ]
    }`)
	cfg, ok := s.mcpConfigByName("drive")
	if !ok || !cfg.UsesOAuth() {
		t.Fatalf("mcpConfigByName(drive) = (%+v, %v), want oauth config", cfg, ok)
	}
	if _, ok := s.mcpConfigByName("ghost"); ok {
		t.Error("expected miss for unknown server")
	}
}

func TestBeginMCPAuth_NotOAuth(t *testing.T) {
	s := newRunTriggerTestServer(t)
	writeHomeSettingsMCP(t, s, `{
      "mcp_servers": [
        {"name": "plain", "type": "stdio", "command": "foo"}
      ]
    }`)
	if _, err := s.BeginMCPAuth("plain"); err == nil {
		t.Error("expected error authorizing a non-oauth server")
	}
	if _, err := s.BeginMCPAuth("ghost"); err == nil {
		t.Error("expected error for unknown server")
	}
}

// TestBeginMCPAuth_SharedCallback verifies that when the mission-control web
// server is running, BeginMCPAuth builds the fixed callback redirect URI and
// registers a pending flow keyed by state.
func TestBeginMCPAuth_SharedCallback(t *testing.T) {
	s := newRunTriggerTestServer(t)
	s.SetWebPort(1337)
	writeHomeSettingsMCP(t, s, `{
      "mcp_servers": [
        {"name": "drive", "type": "url", "url": "https://example.test/mcp",
         "oauth": {"client_id": "cid", "auth_url": "https://a/authorize", "token_url": "https://a/token"}}
      ]
    }`)

	authURL, err := s.BeginMCPAuth("drive")
	if err != nil {
		t.Fatalf("BeginMCPAuth: %v", err)
	}
	u, _ := url.Parse(authURL)
	redirect := u.Query().Get("redirect_uri")
	if redirect != "http://127.0.0.1:1337/mcp/oauth/callback" {
		t.Errorf("redirect_uri = %q, want the mission-control callback", redirect)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatal("auth URL missing state")
	}
	// The flow must be registered and retrievable by its state.
	if got := s.takeMCPAuthFlow(state); got == nil {
		t.Fatal("pending flow not registered by state")
	}
	// takeMCPAuthFlow removes it; a second take returns nil.
	if got := s.takeMCPAuthFlow(state); got != nil {
		t.Error("flow not removed after take")
	}
}

// TestHandleMCPOAuthCallback drives the shared callback route end to end: a
// registered flow, a browser redirect with code+state, a fake token endpoint,
// and the resulting stored token.
func TestHandleMCPOAuthCallback(t *testing.T) {
	tokServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code") != "the-code" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "acc", "token_type": "Bearer", "refresh_token": "ref", "expires_in": 3600,
		})
	}))
	defer tokServer.Close()

	s := newRunTriggerTestServer(t)
	tstore := mcpTokenStore{store: &fakeCredStore{m: map[string]string{}}}
	cfg := mcp.ServerConfig{
		Name: "drive", Type: "url", URL: "https://example.test/mcp",
		OAuth: &mcp.OAuthConfig{ClientID: "cid", AuthURL: "https://a/authorize", TokenURL: tokServer.URL},
	}
	flow, err := mcp.NewAuthFlow(context.Background(), cfg, tstore, "http://127.0.0.1:1337/mcp/oauth/callback")
	if err != nil {
		t.Fatalf("NewAuthFlow: %v", err)
	}
	s.registerMCPAuthFlow(flow)

	req := httptest.NewRequest(http.MethodGet, "/mcp/oauth/callback?code=the-code&state="+url.QueryEscape(flow.State), nil)
	w := httptest.NewRecorder()
	s.handleMCPOAuthCallback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("callback status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	tok, _ := tstore.Load("drive")
	if tok == nil || tok.AccessToken != "acc" {
		t.Errorf("token not stored after callback: %+v", tok)
	}
	// The flow is consumed (single-use).
	if got := s.takeMCPAuthFlow(flow.State); got != nil {
		t.Error("flow not consumed after successful callback")
	}
}

// TestHandleMCPOAuthCallback_UnknownState rejects a callback whose state matches
// no pending flow, without a token being stored.
func TestHandleMCPOAuthCallback_UnknownState(t *testing.T) {
	s := newRunTriggerTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/mcp/oauth/callback?code=x&state=nonexistent", nil)
	w := httptest.NewRecorder()
	s.handleMCPOAuthCallback(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown state", w.Code)
	}
}
