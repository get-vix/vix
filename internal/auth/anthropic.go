package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// anthropicProvider implements the Claude Pro/Max OAuth flow (authorization
// code + PKCE with a local callback server). On successful login it spends the
// granted org:create_api_key scope to mint a standard, metered API key and
// stores that for inference — the raw OAuth access token is never used as an
// inference credential.
type anthropicProvider struct {
	clientID        string
	authorizeURL    string
	tokenURL        string
	createAPIKeyURL string
	callbackPort    int
	callbackPath    string
	redirectURI     string
	scopes          string
}

// anthropicClientIDB64 is the public OAuth client id, base64-encoded so it is
// not a bare string in the source.
const anthropicClientIDB64 = "OWQxYzI1MGEtZTYxYi00NGQ5LTg4ZWQtNTk0NGQxOTYyZjVl"

// anthropicKeyNeverExpires marks a minted-API-key credential as non-expiring
// (standard API keys do not expire), so the daemon never attempts a token
// refresh for it. Fixed far-future Unix-ms timestamp (2100-01-01).
const anthropicKeyNeverExpires int64 = 4102444800000

func newAnthropicProvider() *anthropicProvider {
	id := anthropicClientIDB64
	if decoded, err := base64.StdEncoding.DecodeString(anthropicClientIDB64); err == nil {
		id = string(decoded)
	}
	const port = 53692
	const path = "/callback"
	return &anthropicProvider{
		clientID:        id,
		authorizeURL:    "https://claude.ai/oauth/authorize",
		tokenURL:        "https://platform.claude.com/v1/oauth/token",
		createAPIKeyURL: "https://api.anthropic.com/api/oauth/claude_cli/create_api_key",
		callbackPort:    port,
		callbackPath:    path,
		redirectURI:     fmt.Sprintf("http://localhost:%d%s", port, path),
		scopes:          "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload",
	}
}

func (p *anthropicProvider) ID() string               { return "anthropic" }
func (p *anthropicProvider) Name() string             { return "Anthropic (Claude Pro/Max)" }
func (p *anthropicProvider) UsesCallbackServer() bool { return true }

// APIKey returns the metered API key minted at login. Older credentials saved
// before the metered-key change carry no minted key; they fall back to Access
// and a re-run of `vix login anthropic` mints one.
func (p *anthropicProvider) APIKey(c Credentials) string {
	if k := c.StringExtra("apiKey"); k != "" {
		return k
	}
	return c.Access
}

func (p *anthropicProvider) Login(ctx context.Context, cb LoginCallbacks) (Credentials, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return Credentials{}, err
	}

	// The PKCE verifier doubles as the OAuth state value.
	server, err := startCallbackServer(callbackHost(), p.callbackPort, p.callbackPath,
		"Anthropic authentication completed. You can close this window.", verifier)
	if err != nil {
		return Credentials{}, err
	}
	defer server.close()

	authParams := url.Values{}
	authParams.Set("code", "true")
	authParams.Set("client_id", p.clientID)
	authParams.Set("response_type", "code")
	authParams.Set("redirect_uri", p.redirectURI)
	authParams.Set("scope", p.scopes)
	authParams.Set("code_challenge", challenge)
	authParams.Set("code_challenge_method", "S256")
	authParams.Set("state", verifier)

	authURL := p.authorizeURL + "?" + authParams.Encode()
	lg().Info("anthropic: authorization URL ready (waiting for browser callback)", "url", authURL, "redirect_uri", p.redirectURI)
	if cb.OnAuth != nil {
		cb.OnAuth(AuthInfo{
			URL: authURL,
			Instructions: "Complete login in your browser. If the browser is on another machine, " +
				"paste the final redirect URL here.",
		})
	}

	code, state, err := waitForAuthorizationCode(ctx, server, cb, verifier)
	if err != nil {
		return Credentials{}, err
	}

	if code == "" {
		input, err := cb.OnPrompt(Prompt{
			Message:     "Paste the authorization code or full redirect URL:",
			Placeholder: p.redirectURI,
		})
		if err != nil {
			return Credentials{}, err
		}
		parsed := parseAuthorizationInput(input)
		if parsed.state != "" && parsed.state != verifier {
			return Credentials{}, errors.New("OAuth state mismatch")
		}
		code = parsed.code
		state = parsed.state
		if state == "" {
			state = verifier
		}
	}

	if code == "" {
		return Credentials{}, errors.New("missing authorization code")
	}
	if state == "" {
		return Credentials{}, errors.New("missing OAuth state")
	}

	cb.progress("Exchanging authorization code for tokens...")
	return p.exchangeAuthorizationCode(ctx, code, state, verifier)
}

func (p *anthropicProvider) exchangeAuthorizationCode(ctx context.Context, code, state, verifier string) (Credentials, error) {
	data, err := postJSONForToken(ctx, p.tokenURL, map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     p.clientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  p.redirectURI,
		"code_verifier": verifier,
	})
	if err != nil {
		return Credentials{}, fmt.Errorf("token exchange request failed. url=%s; redirect_uri=%s; response_type=authorization_code: %w", p.tokenURL, p.redirectURI, err)
	}

	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &token); err != nil {
		lg().Error("anthropic: token exchange returned invalid JSON", "body_bytes", len(data), "err", err)
		return Credentials{}, fmt.Errorf("token exchange returned invalid JSON. url=%s; body=%s: %w", p.tokenURL, string(data), err)
	}

	lg().Info("anthropic: token exchange succeeded", "expires_in_s", token.ExpiresIn, "access", redact(token.AccessToken), "refresh", redact(token.RefreshToken))

	// Spend the granted org:create_api_key scope to mint a standard API key and
	// store THAT as the inference credential. Usage bills as ordinary metered
	// API spend rather than presenting as Claude Code against the subscription's
	// plan limits.
	apiKey, err := p.createAPIKey(ctx, token.AccessToken)
	if err != nil {
		return Credentials{}, fmt.Errorf("anthropic: minting API key from OAuth login failed: %w", err)
	}
	lg().Info("anthropic: minted metered API key", "api_key", redact(apiKey))
	return Credentials{
		Expires: anthropicKeyNeverExpires,
		Extra:   map[string]any{"apiKey": apiKey},
	}, nil
}

// createAPIKey exchanges a freshly-obtained OAuth access token (which must
// carry the org:create_api_key scope) for a standard Anthropic API key. That
// key — not the access token — is what vix uses for inference.
func (p *anthropicProvider) createAPIKey(ctx context.Context, accessToken string) (string, error) {
	status, data, err := httpRequest(ctx, http.MethodPost, p.createAPIKeyURL, map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}, strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("create_api_key request failed. status=%d; url=%s; body=%s", status, p.createAPIKeyURL, string(data))
	}

	// The endpoint returns the new key under raw_key; accept a couple of field
	// name variants defensively in case the response shape shifts.
	var resp struct {
		RawKey string `json:"raw_key"`
		Key    string `json:"key"`
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("create_api_key returned invalid JSON: %w", err)
	}
	key := resp.RawKey
	if key == "" {
		key = resp.Key
	}
	if key == "" {
		key = resp.APIKey
	}
	if key == "" {
		return "", errors.New("create_api_key response did not contain a key")
	}
	return key, nil
}

func (p *anthropicProvider) RefreshToken(ctx context.Context, creds Credentials) (Credentials, error) {
	data, err := postJSONForToken(ctx, p.tokenURL, map[string]any{
		"grant_type":    "refresh_token",
		"client_id":     p.clientID,
		"refresh_token": creds.Refresh,
	})
	if err != nil {
		return Credentials{}, fmt.Errorf("anthropic token refresh request failed. url=%s: %w", p.tokenURL, err)
	}

	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &token); err != nil {
		lg().Error("anthropic: token refresh returned invalid JSON", "body_bytes", len(data), "err", err)
		return Credentials{}, fmt.Errorf("anthropic token refresh returned invalid JSON. url=%s; body=%s: %w", p.tokenURL, string(data), err)
	}

	lg().Debug("anthropic: token refresh succeeded", "expires_in_s", token.ExpiresIn)
	return Credentials{
		Refresh: token.RefreshToken,
		Access:  token.AccessToken,
		Expires: nowMillis() + token.ExpiresIn*1000 - 5*60*1000,
	}, nil
}
