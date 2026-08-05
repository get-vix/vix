package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// memStore is an in-memory TokenStore for tests.
type memStore struct {
	mu sync.Mutex
	m  map[string]*oauth2.Token
}

func newMemStore() *memStore { return &memStore{m: map[string]*oauth2.Token{}} }

func (s *memStore) Load(server string) (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[server], nil
}

func (s *memStore) Save(server string, tok *oauth2.Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[server] = tok
	return nil
}

func (s *memStore) Delete(server string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, server)
	return nil
}

func TestWellKnownURLs(t *testing.T) {
	u, _ := url.Parse("https://drivemcp.googleapis.com/mcp/v1")
	got := wellKnownURLs(u, "oauth-protected-resource")
	want := []string{
		"https://drivemcp.googleapis.com/.well-known/oauth-protected-resource/mcp/v1",
		"https://drivemcp.googleapis.com/.well-known/oauth-protected-resource",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("url[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// No path: only the origin-root variant.
	u2, _ := url.Parse("https://accounts.example.com")
	got2 := wellKnownURLs(u2, "oauth-authorization-server")
	if len(got2) != 1 || got2[0] != "https://accounts.example.com/.well-known/oauth-authorization-server" {
		t.Errorf("no-path case = %v", got2)
	}
}

func TestDiscoverOAuthEndpoints(t *testing.T) {
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(protectedResourceMetadata{
			Resource:             base + "/mcp/v1",
			AuthorizationServers: []string{base},
			ScopesSupported:      []string{"drive.readonly", "drive.file"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(authServerMetadata{
			Issuer:                base,
			AuthorizationEndpoint: base + "/authorize",
			TokenEndpoint:         base + "/token",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	d, err := discoverOAuthEndpoints(context.Background(), srv.Client(), base+"/mcp/v1")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if d.AuthURL != base+"/authorize" || d.TokenURL != base+"/token" {
		t.Errorf("endpoints = %+v", d)
	}
	if len(d.Scopes) != 2 || d.Scopes[0] != "drive.readonly" {
		t.Errorf("scopes = %v, want the protected-resource scopes", d.Scopes)
	}
}

func TestDiscoverOAuthEndpoints_NoAuthServer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(protectedResourceMetadata{AuthorizationServers: nil})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := discoverOAuthEndpoints(context.Background(), srv.Client(), srv.URL+"/mcp"); err == nil {
		t.Fatal("expected error when no authorization servers are listed")
	}
}

func TestNewTokenSource_NeedsAuth(t *testing.T) {
	_, err := newTokenSource(context.Background(), ServerConfig{Name: "s"}, newMemStore(), &oauth2.Config{})
	if err != ErrNeedsAuth {
		t.Fatalf("err = %v, want ErrNeedsAuth", err)
	}
}

func TestPersistingTokenSource_RefreshAndPersist(t *testing.T) {
	var refreshHits int
	tokServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHits++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"token_type":    "Bearer",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
		})
	}))
	defer tokServer.Close()

	oc := &oauth2.Config{
		ClientID: "cid",
		Endpoint: oauth2.Endpoint{TokenURL: tokServer.URL},
	}
	store := newMemStore()
	store.Save("s", &oauth2.Token{
		AccessToken:  "stale",
		RefreshToken: "old-refresh",
		Expiry:       time.Now().Add(-time.Hour),
	})

	ts, err := newTokenSource(context.Background(), ServerConfig{Name: "s"}, store, oc)
	if err != nil {
		t.Fatalf("newTokenSource: %v", err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken != "new-access" {
		t.Errorf("access token = %q, want refreshed", tok.AccessToken)
	}
	if refreshHits != 1 {
		t.Errorf("refresh hits = %d, want 1", refreshHits)
	}
	// The rotated token must be persisted.
	saved, _ := store.Load("s")
	if saved.AccessToken != "new-access" || saved.RefreshToken != "new-refresh" {
		t.Errorf("stored token not updated: %+v", saved)
	}
}

// fakeBearer returns cur; Invalidate swaps in next and counts calls.
type fakeBearer struct {
	mu    sync.Mutex
	cur   string
	next  string
	count int
}

func (f *fakeBearer) Token() (*oauth2.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &oauth2.Token{AccessToken: f.cur, TokenType: "Bearer"}, nil
}

func (f *fakeBearer) Invalidate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count++
	if f.next != "" {
		f.cur = f.next
	}
}

func TestHTTPClient_BearerRefreshOn401(t *testing.T) {
	const goodToken = "good"
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+goodToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05"}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{"name": "echo", "description": "echo", "inputSchema": map[string]any{"type": "object"}},
			}}
		default:
			result = map[string]any{}
		}
		raw, _ := json.Marshal(result)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(raw)}
		json.NewEncoder(w).Encode(resp)
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()

	bearer := &fakeBearer{cur: "stale", next: goodToken}
	c, err := newHTTPClient("s", srv.URL, nil, bearer)
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	if bearer.count == 0 {
		t.Error("expected at least one Invalidate after a 401")
	}
	if len(c.ListTools()) != 1 {
		t.Fatalf("expected 1 tool after refresh, got %d", len(c.ListTools()))
	}
}

// TestAuthorize_FullFlow drives Authorize end to end against a fake authorization
// server: the onURL callback simulates the browser by hitting the loopback
// redirect with a code, and the fake token endpoint returns a token.
func TestAuthorize_FullFlow(t *testing.T) {
	var tokenHits int
	tokServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenHits++
		r.ParseForm()
		if r.Form.Get("code") != "the-code" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-1",
			"token_type":    "Bearer",
			"refresh_token": "refresh-1",
			"expires_in":    3600,
		})
	}))
	defer tokServer.Close()

	cfg := ServerConfig{
		Name: "drive",
		Type: "url",
		URL:  "https://example.test/mcp",
		OAuth: &OAuthConfig{
			ClientID: "cid",
			AuthURL:  "https://auth.example.test/authorize",
			TokenURL: tokServer.URL,
			Scopes:   []string{"drive.file"},
		},
	}
	store := newMemStore()

	onURL := func(rawAuthURL string) {
		u, err := url.Parse(rawAuthURL)
		if err != nil {
			t.Errorf("bad auth url: %v", err)
			return
		}
		q := u.Query()
		redirect := q.Get("redirect_uri")
		state := q.Get("state")
		// Simulate the provider redirecting the browser back with a code.
		go func() {
			_, _ = http.Get(fmt.Sprintf("%s?code=the-code&state=%s", redirect, url.QueryEscape(state)))
		}()
	}

	if err := Authorize(context.Background(), cfg, store, onURL); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if tokenHits != 1 {
		t.Errorf("token endpoint hits = %d, want 1", tokenHits)
	}
	saved, _ := store.Load("drive")
	if saved == nil || saved.AccessToken != "access-1" {
		t.Errorf("token not stored: %+v", saved)
	}
}

func TestAuthorize_StateMismatchRejected(t *testing.T) {
	cfg := ServerConfig{
		Name: "drive",
		Type: "url",
		URL:  "https://example.test/mcp",
		OAuth: &OAuthConfig{
			ClientID: "cid",
			AuthURL:  "https://auth.example.test/authorize",
			TokenURL: "https://auth.example.test/token",
		},
	}
	onURL := func(rawAuthURL string) {
		u, _ := url.Parse(rawAuthURL)
		redirect := u.Query().Get("redirect_uri")
		go func() {
			_, _ = http.Get(redirect + "?code=x&state=WRONG")
		}()
	}
	err := Authorize(context.Background(), cfg, newMemStore(), onURL)
	if err == nil {
		t.Fatal("expected state-mismatch error")
	}
}

func TestAuthFlow_NewAndComplete(t *testing.T) {
	var hits int
	tokServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
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

	cfg := ServerConfig{
		Name: "drive", Type: "url", URL: "https://example.test/mcp",
		OAuth: &OAuthConfig{ClientID: "cid", AuthURL: "https://auth/authorize", TokenURL: tokServer.URL, Scopes: []string{"s"}},
	}
	store := newMemStore()
	const redirect = "http://127.0.0.1:1337/mcp/oauth/callback"

	flow, err := NewAuthFlow(context.Background(), cfg, store, redirect)
	if err != nil {
		t.Fatalf("NewAuthFlow: %v", err)
	}
	// The auth URL must carry our fixed redirect and a PKCE challenge.
	u, _ := url.Parse(flow.AuthURL)
	if u.Query().Get("redirect_uri") != redirect {
		t.Errorf("redirect_uri = %q, want %q", u.Query().Get("redirect_uri"), redirect)
	}
	if u.Query().Get("code_challenge") == "" {
		t.Error("auth URL missing PKCE code_challenge")
	}
	if u.Query().Get("state") != flow.State {
		t.Error("auth URL state does not match flow state")
	}

	// Wrong state is rejected without hitting the token endpoint.
	if err := flow.Complete(context.Background(), "the-code", "nope"); err == nil {
		t.Error("expected state-mismatch error")
	}
	if hits != 0 {
		t.Errorf("token endpoint hit on bad state: %d", hits)
	}

	// Correct state exchanges and stores the token.
	if err := flow.Complete(context.Background(), "the-code", flow.State); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	saved, _ := store.Load("drive")
	if saved == nil || saved.AccessToken != "acc" {
		t.Errorf("token not stored: %+v", saved)
	}
}
