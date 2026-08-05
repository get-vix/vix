package mcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// ErrNeedsAuth signals that an OAuth MCP server has no usable token stored (or
// its refresh token is no longer valid). The user must (re-)run the interactive
// authorization flow (`vix mcp auth <server>` or the F4 tab).
var ErrNeedsAuth = errors.New("mcp: server requires authentication")

// TokenStore persists OAuth tokens per MCP server. Implementations are backed by
// the vix credential store (OS keyring, with a 0600 auth.json fallback). Load
// returns (nil, nil) when no token is stored.
type TokenStore interface {
	Load(server string) (*oauth2.Token, error)
	Save(server string, tok *oauth2.Token) error
	Delete(server string) error
}

// oauthHTTPTimeout bounds discovery and token-exchange HTTP calls.
const oauthHTTPTimeout = 30 * time.Second

// oauthAuthorizeTimeout bounds how long the loopback listener waits for the user
// to complete the browser consent before the flow is abandoned.
const oauthAuthorizeTimeout = 5 * time.Minute

// persistingTokenSource is an oauth2.TokenSource that transparently refreshes an
// access token using the stored refresh token and writes any rotated token back
// to the TokenStore. It is safe for concurrent use.
type persistingTokenSource struct {
	server string
	store  TokenStore
	cfg    *oauth2.Config
	ctx    context.Context

	mu   sync.Mutex
	base oauth2.TokenSource
	cur  *oauth2.Token
}

// newTokenSource loads the stored token for cfg's server and returns a source
// that refreshes and persists it. Returns ErrNeedsAuth when no token is stored.
func newTokenSource(ctx context.Context, cfg ServerConfig, store TokenStore, oc *oauth2.Config) (*persistingTokenSource, error) {
	tok, err := store.Load(cfg.Name)
	if err != nil {
		return nil, fmt.Errorf("load token: %w", err)
	}
	if tok == nil || tok.RefreshToken == "" && !tok.Valid() {
		return nil, ErrNeedsAuth
	}
	ts := &persistingTokenSource{server: cfg.Name, store: store, cfg: oc, ctx: ctx}
	ts.reset(tok)
	return ts, nil
}

// reset rebuilds the underlying reuse source around tok. Caller holds mu (or is
// the constructor before publishing ts).
func (p *persistingTokenSource) reset(tok *oauth2.Token) {
	p.cur = tok
	p.base = p.cfg.TokenSource(p.ctx, tok)
}

// Token returns a valid access token, refreshing if necessary, and persists any
// change (e.g. a rotated refresh token) back to the store.
func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tok, err := p.base.Token()
	if err != nil {
		return nil, err
	}
	if p.cur == nil || tok.AccessToken != p.cur.AccessToken || !tok.Expiry.Equal(p.cur.Expiry) {
		if serr := p.store.Save(p.server, tok); serr != nil {
			log.Printf("[mcp] %s: failed to persist refreshed token: %v", p.server, serr)
		}
		p.cur = tok
	}
	return tok, nil
}

// Invalidate forces the next Token call to refresh, used after a 401 that a
// non-expired cached token could not satisfy (e.g. server-side revocation).
func (p *persistingTokenSource) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur != nil {
		expired := *p.cur
		expired.Expiry = time.Now().Add(-time.Hour)
		p.reset(&expired)
	}
}

// resolveOAuthConfig produces an oauth2.Config for cfg, resolving endpoints
// either from explicit config or via discovery. redirectURL may be empty for
// non-interactive (refresh-only) use.
func resolveOAuthConfig(ctx context.Context, cfg ServerConfig, redirectURL string) (*oauth2.Config, error) {
	if cfg.OAuth == nil {
		return nil, fmt.Errorf("server %q has no oauth config", cfg.Name)
	}
	var ep oauth2.Endpoint
	scopes := cfg.OAuth.Scopes
	if cfg.OAuth.AuthURL != "" && cfg.OAuth.TokenURL != "" {
		ep = oauth2.Endpoint{AuthURL: cfg.OAuth.AuthURL, TokenURL: cfg.OAuth.TokenURL}
	} else {
		httpc := &http.Client{Timeout: oauthHTTPTimeout}
		d, err := discoverOAuthEndpoints(ctx, httpc, cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("discover oauth endpoints: %w", err)
		}
		ep = oauth2.Endpoint{AuthURL: d.AuthURL, TokenURL: d.TokenURL}
		if len(scopes) == 0 {
			scopes = d.Scopes
		}
	}
	return &oauth2.Config{
		ClientID:     cfg.OAuth.ClientID,
		ClientSecret: expandEnvValue(cfg.OAuth.ClientSecret),
		Endpoint:     ep,
		Scopes:       scopes,
		RedirectURL:  redirectURL,
	}, nil
}

// AuthFlow is an in-progress OAuth authorization. Create it with NewAuthFlow
// (which resolves endpoints and builds the authorization URL), hand AuthURL to a
// browser, then call Complete with the code + state delivered to the redirect
// URI. The same flow backs both the shared mission-control callback route and
// the ephemeral loopback listener (Authorize).
type AuthFlow struct {
	Server  string // MCP server name
	State   string // opaque anti-CSRF token, also the registry key
	AuthURL string // URL to open in the browser

	verifier string
	oc       *oauth2.Config
	store    TokenStore
	created  time.Time
}

// NewAuthFlow resolves cfg's OAuth endpoints, generates PKCE + state, and builds
// the authorization URL for the given redirect URI (where the provider will send
// the browser back).
func NewAuthFlow(ctx context.Context, cfg ServerConfig, store TokenStore, redirectURL string) (*AuthFlow, error) {
	if !cfg.UsesOAuth() {
		return nil, fmt.Errorf("server %q is not configured for oauth", cfg.Name)
	}
	oc, err := resolveOAuthConfig(ctx, cfg, redirectURL)
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()
	state, err := randomToken()
	if err != nil {
		return nil, err
	}
	authURL := oc.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce,
		oauth2.S256ChallengeOption(verifier),
	)
	return &AuthFlow{
		Server:   cfg.Name,
		State:    state,
		AuthURL:  authURL,
		verifier: verifier,
		oc:       oc,
		store:    store,
		created:  time.Now(),
	}, nil
}

// Expired reports whether the flow is older than the authorization window.
func (f *AuthFlow) Expired() bool {
	return time.Since(f.created) > oauthAuthorizeTimeout
}

// Complete validates the returned state, exchanges the code for a token, and
// persists it.
func (f *AuthFlow) Complete(ctx context.Context, code, state string) error {
	if state != f.State {
		return errors.New("state mismatch (possible CSRF)")
	}
	if code == "" {
		return errors.New("missing authorization code")
	}
	tok, err := f.oc.Exchange(ctx, code, oauth2.VerifierOption(f.verifier))
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}
	if err := f.store.Save(f.Server, tok); err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	log.Printf("[mcp] %s: authorized (token stored)", f.Server)
	return nil
}

// Authorize runs the interactive OAuth flow using a self-hosted ephemeral
// loopback listener. It is the fallback used when no shared callback host (the
// mission-control web server) is available. It starts a listener, hands the auth
// URL to onURL, waits for the redirect, and completes the flow.
func Authorize(ctx context.Context, cfg ServerConfig, store TokenStore, onURL func(url string)) error {
	if !cfg.UsesOAuth() {
		return fmt.Errorf("server %q is not configured for oauth", cfg.Name)
	}

	port := 0
	if cfg.OAuth.RedirectPort > 0 {
		port = cfg.OAuth.RedirectPort
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("start loopback listener: %w", err)
	}
	defer ln.Close()
	redirectURL := "http://" + ln.Addr().String() + "/callback"

	flow, err := NewAuthFlow(ctx, cfg, store, redirectURL)
	if err != nil {
		return err
	}

	type result struct{ err error }
	resc := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			WriteCallbackPage(w, false, e)
			resc <- result{err: fmt.Errorf("authorization denied: %s", e)}
			return
		}
		if err := flow.Complete(r.Context(), q.Get("code"), q.Get("state")); err != nil {
			WriteCallbackPage(w, false, err.Error())
			resc <- result{err: err}
			return
		}
		WriteCallbackPage(w, true, cfg.Name)
		resc <- result{}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	if onURL != nil {
		onURL(flow.AuthURL)
	}

	ctx, cancel := context.WithTimeout(ctx, oauthAuthorizeTimeout)
	defer cancel()

	select {
	case res := <-resc:
		return res.err
	case <-ctx.Done():
		return fmt.Errorf("authorization timed out or was cancelled: %w", ctx.Err())
	}
}

// randomToken returns a URL-safe random string for use as the OAuth state param.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// WriteCallbackPage renders the minimal browser page shown after the redirect.
func WriteCallbackPage(w http.ResponseWriter, ok bool, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if ok {
		fmt.Fprintf(w, "<html><body style='font-family:sans-serif'><h2>Connected to %s</h2><p>You may close this tab and return to vix.</p></body></html>", html.EscapeString(detail))
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, "<html><body style='font-family:sans-serif'><h2>Authorization failed</h2><p>%s</p></body></html>", html.EscapeString(detail))
}
