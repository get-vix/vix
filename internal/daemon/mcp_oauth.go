package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/mcp"
	"golang.org/x/oauth2"
)

// oauthBeginTimeout bounds how long the ephemeral-fallback BeginMCPAuth waits to
// obtain the authorization URL (endpoint discovery + listener startup) before
// giving up.
const oauthBeginTimeout = 35 * time.Second

// mcpTokenStore persists MCP OAuth tokens through the vix credential store,
// keyed "mcp-oauth-<server>". It satisfies mcp.TokenStore.
type mcpTokenStore struct {
	store config.CredentialStore
}

func newMCPTokenStore() mcpTokenStore {
	return mcpTokenStore{store: config.DefaultCredentialStore()}
}

func mcpTokenKey(server string) string { return "mcp-oauth-" + server }

// Load returns the stored token for server, or (nil, nil) when none is stored.
func (m mcpTokenStore) Load(server string) (*oauth2.Token, error) {
	v, err := m.store.Get(mcpTokenKey(server))
	if errors.Is(err, config.ErrCredNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(v), &tok); err != nil {
		return nil, fmt.Errorf("decode stored token: %w", err)
	}
	return &tok, nil
}

func (m mcpTokenStore) Save(server string, tok *oauth2.Token) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return m.store.Set(mcpTokenKey(server), string(b))
}

func (m mcpTokenStore) Delete(server string) error {
	err := m.store.Delete(mcpTokenKey(server))
	if errors.Is(err, config.ErrCredNotFound) {
		return nil
	}
	return err
}

// hasMCPToken reports whether a token is stored for server.
func hasMCPToken(server string) bool {
	tok, err := newMCPTokenStore().Load(server)
	return err == nil && tok != nil
}

// mcpConfigByName returns the home MCP server config with the given name.
func (s *Server) mcpConfigByName(name string) (mcp.ServerConfig, bool) {
	for _, cfg := range s.readHomeMCPServers() {
		if cfg.Name == name {
			return cfg, true
		}
	}
	return mcp.ServerConfig{}, false
}

// BeginMCPAuth starts the interactive OAuth flow for the named server and returns
// the authorization URL to open in a browser.
//
// When the mission-control web server is running (webPort > 0), the flow uses its
// fixed callback route (/mcp/oauth/callback) — a single stable redirect URI that
// works for every OAuth MCP server. Otherwise it falls back to a self-hosted
// ephemeral loopback listener. Either way the exchange completes asynchronously
// and the daemon broadcasts event.mcp_changed on success.
func (s *Server) BeginMCPAuth(name string) (string, error) {
	cfg, ok := s.mcpConfigByName(name)
	if !ok {
		return "", fmt.Errorf("MCP server %q not found", name)
	}
	if !cfg.UsesOAuth() {
		return "", fmt.Errorf("MCP server %q is not configured for oauth", name)
	}
	store := newMCPTokenStore()

	// Preferred: reuse the mission-control web server as a fixed callback host.
	if port := s.webPort; port > 0 {
		redirectURL := fmt.Sprintf("http://127.0.0.1:%d/mcp/oauth/callback", port)
		flow, err := mcp.NewAuthFlow(context.Background(), cfg, store, redirectURL)
		if err != nil {
			return "", err
		}
		s.registerMCPAuthFlow(flow)
		return flow.AuthURL, nil
	}

	// Fallback: self-hosted ephemeral loopback listener, completed async.
	return s.beginEphemeralMCPAuth(cfg, store)
}

// registerMCPAuthFlow stores a pending flow keyed by its state token, sweeping
// any expired flows first to bound map growth.
func (s *Server) registerMCPAuthFlow(flow *mcp.AuthFlow) {
	s.mcpAuthMu.Lock()
	defer s.mcpAuthMu.Unlock()
	if s.mcpAuthFlows == nil {
		s.mcpAuthFlows = make(map[string]*mcp.AuthFlow)
	}
	for st, f := range s.mcpAuthFlows {
		if f.Expired() {
			delete(s.mcpAuthFlows, st)
		}
	}
	s.mcpAuthFlows[flow.State] = flow
}

// takeMCPAuthFlow removes and returns the pending flow for state, or nil.
func (s *Server) takeMCPAuthFlow(state string) *mcp.AuthFlow {
	s.mcpAuthMu.Lock()
	defer s.mcpAuthMu.Unlock()
	f := s.mcpAuthFlows[state]
	if f != nil {
		delete(s.mcpAuthFlows, state)
	}
	return f
}

// handleMCPOAuthCallback handles the browser redirect for the shared
// mission-control OAuth callback route. It is intentionally unauthenticated: the
// provider's redirect carries no vix token, but the `state` param is an
// unguessable secret that must match a live pending flow (single-use, expiring).
func (s *Server) handleMCPOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	flow := s.takeMCPAuthFlow(q.Get("state"))
	if flow == nil {
		mcp.WriteCallbackPage(w, false, "unknown or expired authorization")
		return
	}
	if flow.Expired() {
		mcp.WriteCallbackPage(w, false, "authorization expired; please try again")
		return
	}
	if e := q.Get("error"); e != "" {
		mcp.WriteCallbackPage(w, false, e)
		log.Printf("[mcp] %s: authorization denied: %s", flow.Server, e)
		return
	}
	if err := flow.Complete(r.Context(), q.Get("code"), q.Get("state")); err != nil {
		mcp.WriteCallbackPage(w, false, err.Error())
		log.Printf("[mcp] %s: authorization failed: %v", flow.Server, err)
		return
	}
	mcp.WriteCallbackPage(w, true, flow.Server)
	s.broadcastMCPChanged()
}

// beginEphemeralMCPAuth runs the fallback flow with a self-hosted loopback
// listener when no mission-control callback host is available.
func (s *Server) beginEphemeralMCPAuth(cfg mcp.ServerConfig, store mcp.TokenStore) (string, error) {
	urlc := make(chan string, 1)
	errc := make(chan error, 1)
	go func() {
		err := mcp.Authorize(context.Background(), cfg, store, func(u string) { urlc <- u })
		if err != nil {
			log.Printf("[mcp] %s: authorization failed: %v", cfg.Name, err)
			select {
			case errc <- err:
			default:
			}
			return
		}
		s.broadcastMCPChanged()
	}()

	select {
	case u := <-urlc:
		return u, nil
	case err := <-errc:
		return "", err
	case <-time.After(oauthBeginTimeout):
		return "", fmt.Errorf("timed out preparing authorization for %q", cfg.Name)
	}
}

// LogoutMCP deletes the stored OAuth token for the named server and refreshes
// attached MCP tabs.
func (s *Server) LogoutMCP(name string) error {
	if _, ok := s.mcpConfigByName(name); !ok {
		return fmt.Errorf("MCP server %q not found", name)
	}
	if err := newMCPTokenStore().Delete(name); err != nil {
		return err
	}
	s.broadcastMCPChanged()
	return nil
}
