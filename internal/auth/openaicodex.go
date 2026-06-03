package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Login method ids for the OpenAI Codex provider.
const (
	OpenAICodexBrowserLoginMethod    = "browser"
	OpenAICodexDeviceCodeLoginMethod = "device_code"
)

const openaiCodexJWTClaimPath = "https://api.openai.com/auth"

// openaiCodexProvider implements the ChatGPT (Codex subscription) OAuth flow,
// offering both a browser (authorization code + PKCE) and a headless
// device-code path.
type openaiCodexProvider struct {
	clientID              string
	authorizeURL          string
	tokenURL              string
	redirectURI           string
	deviceUserCodeURL     string
	deviceTokenURL        string
	deviceVerificationURI string
	deviceRedirectURI     string
	deviceTimeoutSeconds  int
	scope                 string
	originator            string
	callbackPort          int
	callbackPath          string
}

func newOpenAICodexProvider() *openaiCodexProvider {
	const authBase = "https://auth.openai.com"
	return &openaiCodexProvider{
		clientID:              "app_EMoamEEZ73f0CkXaXp7hrann",
		authorizeURL:          authBase + "/oauth/authorize",
		tokenURL:              authBase + "/oauth/token",
		redirectURI:           "http://localhost:1455/auth/callback",
		deviceUserCodeURL:     authBase + "/api/accounts/deviceauth/usercode",
		deviceTokenURL:        authBase + "/api/accounts/deviceauth/token",
		deviceVerificationURI: authBase + "/codex/device",
		deviceRedirectURI:     authBase + "/deviceauth/callback",
		deviceTimeoutSeconds:  15 * 60,
		scope:                 "openid profile email offline_access",
		originator:            "vix",
		callbackPort:          1455,
		callbackPath:          "/auth/callback",
	}
}

func (p *openaiCodexProvider) ID() string                  { return "openai-codex" }
func (p *openaiCodexProvider) Name() string                { return "ChatGPT Plus/Pro (Codex Subscription)" }
func (p *openaiCodexProvider) UsesCallbackServer() bool    { return true }
func (p *openaiCodexProvider) APIKey(c Credentials) string { return c.Access }

func (p *openaiCodexProvider) Login(ctx context.Context, cb LoginCallbacks) (Credentials, error) {
	method, err := cb.OnSelect(SelectPrompt{
		Message: "Select OpenAI Codex login method:",
		Options: []SelectOption{
			{ID: OpenAICodexBrowserLoginMethod, Label: "Browser login (default)"},
			{ID: OpenAICodexDeviceCodeLoginMethod, Label: "Device code login (headless)"},
		},
	})
	if err != nil {
		return Credentials{}, err
	}
	if method == "" {
		return Credentials{}, errors.New(deviceCancelMessage)
	}
	lg().Info("openai-codex: login method selected", "method", method)

	switch method {
	case OpenAICodexDeviceCodeLoginMethod:
		return p.loginDeviceCode(ctx, cb)
	case OpenAICodexBrowserLoginMethod:
		return p.loginBrowser(ctx, cb)
	default:
		return Credentials{}, fmt.Errorf("unknown OpenAI Codex login method: %s", method)
	}
}

func (p *openaiCodexProvider) loginBrowser(ctx context.Context, cb LoginCallbacks) (Credentials, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return Credentials{}, err
	}
	state, err := createState()
	if err != nil {
		return Credentials{}, err
	}

	u, _ := url.Parse(p.authorizeURL)
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", p.clientID)
	q.Set("redirect_uri", p.redirectURI)
	q.Set("scope", p.scope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("originator", p.originator)
	u.RawQuery = q.Encode()

	server, err := startCallbackServer(callbackHost(), p.callbackPort, p.callbackPath,
		"OpenAI authentication completed. You can close this window.", state)
	if err != nil {
		return Credentials{}, err
	}
	defer server.close()

	lg().Info("openai-codex: authorization URL ready (waiting for browser callback)", "url", u.String(), "redirect_uri", p.redirectURI)
	if cb.OnAuth != nil {
		cb.OnAuth(AuthInfo{URL: u.String(), Instructions: "A browser window should open. Complete login to finish."})
	}

	code, _, err := waitForAuthorizationCode(ctx, server, cb, state)
	if err != nil {
		return Credentials{}, err
	}

	if code == "" {
		input, err := cb.OnPrompt(Prompt{Message: "Paste the authorization code (or full redirect URL):"})
		if err != nil {
			return Credentials{}, err
		}
		parsed := parseAuthorizationInput(input)
		if parsed.state != "" && parsed.state != state {
			return Credentials{}, errors.New("state mismatch")
		}
		code = parsed.code
	}

	if code == "" {
		return Credentials{}, errors.New("missing authorization code")
	}

	return p.exchangeAuthorizationCode(ctx, code, verifier, p.redirectURI)
}

func (p *openaiCodexProvider) loginDeviceCode(ctx context.Context, cb LoginCallbacks) (Credentials, error) {
	device, err := p.startDeviceAuth(ctx)
	if err != nil {
		return Credentials{}, err
	}
	lg().Info("openai-codex: device code issued", "user_code", device.userCode, "verification_uri", p.deviceVerificationURI, "interval_s", device.intervalSeconds)
	if cb.OnDeviceCode != nil {
		cb.OnDeviceCode(DeviceCodeInfo{
			UserCode:         device.userCode,
			VerificationURI:  p.deviceVerificationURI,
			IntervalSeconds:  device.intervalSeconds,
			ExpiresInSeconds: p.deviceTimeoutSeconds,
		})
	}
	success, err := p.pollDeviceAuth(ctx, device)
	if err != nil {
		return Credentials{}, err
	}
	return p.exchangeAuthorizationCode(ctx, success.authorizationCode, success.codeVerifier, p.deviceRedirectURI)
}

func (p *openaiCodexProvider) RefreshToken(ctx context.Context, creds Credentials) (Credentials, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", creds.Refresh)
	form.Set("client_id", p.clientID)
	return p.postToken(ctx, form, "refresh")
}

func (p *openaiCodexProvider) exchangeAuthorizationCode(ctx context.Context, code, verifier, redirectURI string) (Credentials, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", p.clientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)
	return p.postToken(ctx, form, "exchange")
}

// postToken posts a token request and converts the response into credentials,
// extracting the ChatGPT account id from the access-token JWT.
func (p *openaiCodexProvider) postToken(ctx context.Context, form url.Values, op string) (Credentials, error) {
	status, data, err := httpRequest(ctx, http.MethodPost, p.tokenURL, map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	}, strings.NewReader(form.Encode()))
	if err != nil {
		return Credentials{}, fmt.Errorf("OpenAI Codex token %s error: %w", op, err)
	}
	if status < 200 || status >= 300 {
		return Credentials{}, fmt.Errorf("OpenAI Codex token %s failed (%d): %s", op, status, string(data))
	}

	var j struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &j); err != nil || j.AccessToken == "" || j.RefreshToken == "" || j.ExpiresIn == 0 {
		lg().Error("openai-codex: token response missing fields", "op", op, "body_bytes", len(data), "has_access", j.AccessToken != "", "has_refresh", j.RefreshToken != "", "expires_in_s", j.ExpiresIn)
		return Credentials{}, fmt.Errorf("OpenAI Codex token %s response missing fields: %s", op, string(data))
	}

	accountID := openaiCodexAccountID(j.AccessToken)
	if accountID == "" {
		lg().Error("openai-codex: could not extract chatgpt_account_id from access token JWT", "op", op)
		return Credentials{}, errors.New("failed to extract accountId from token")
	}
	lg().Info("openai-codex: token "+op+" succeeded", "account_id", accountID, "expires_in_s", j.ExpiresIn, "access", redact(j.AccessToken))
	return Credentials{
		Access:  j.AccessToken,
		Refresh: j.RefreshToken,
		Expires: nowMillis() + j.ExpiresIn*1000,
		Extra:   map[string]any{"accountId": accountID},
	}, nil
}

type codexDeviceAuth struct {
	deviceAuthID    string
	userCode        string
	intervalSeconds int
}

func (p *openaiCodexProvider) startDeviceAuth(ctx context.Context) (codexDeviceAuth, error) {
	body, _ := json.Marshal(map[string]any{"client_id": p.clientID})
	status, data, err := httpRequest(ctx, http.MethodPost, p.deviceUserCodeURL, map[string]string{
		"Content-Type": "application/json",
	}, strings.NewReader(string(body)))
	if err != nil {
		return codexDeviceAuth{}, err
	}
	if status == http.StatusNotFound {
		lg().Error("openai-codex: device code login not enabled on server (404)", "url", p.deviceUserCodeURL)
		return codexDeviceAuth{}, errors.New("OpenAI Codex device code login is not enabled for this server. Use browser login or verify the server URL.")
	}
	if status < 200 || status >= 300 {
		return codexDeviceAuth{}, fmt.Errorf("OpenAI Codex device code request failed with status %d: %s", status, string(data))
	}

	var resp struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
		Interval     any    `json:"interval"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return codexDeviceAuth{}, fmt.Errorf("invalid OpenAI Codex device code response: %s", string(data))
	}
	interval, ok := coerceInterval(resp.Interval)
	if resp.DeviceAuthID == "" || resp.UserCode == "" || !ok || interval < 0 {
		return codexDeviceAuth{}, fmt.Errorf("invalid OpenAI Codex device code response: %s", string(data))
	}
	return codexDeviceAuth{deviceAuthID: resp.DeviceAuthID, userCode: resp.UserCode, intervalSeconds: interval}, nil
}

type codexDeviceSuccess struct {
	authorizationCode string
	codeVerifier      string
}

func (p *openaiCodexProvider) pollDeviceAuth(ctx context.Context, device codexDeviceAuth) (codexDeviceSuccess, error) {
	return pollDeviceCode[codexDeviceSuccess](ctx, devicePollOptions[codexDeviceSuccess]{
		Label:            "openai-codex",
		IntervalSeconds:  device.intervalSeconds,
		ExpiresInSeconds: p.deviceTimeoutSeconds,
		Poll: func(ctx context.Context) (pollResult[codexDeviceSuccess], error) {
			body, _ := json.Marshal(map[string]any{
				"device_auth_id": device.deviceAuthID,
				"user_code":      device.userCode,
			})
			status, data, err := httpRequest(ctx, http.MethodPost, p.deviceTokenURL, map[string]string{
				"Content-Type": "application/json",
			}, strings.NewReader(string(body)))
			if err != nil {
				return pollResult[codexDeviceSuccess]{}, err
			}

			if status >= 200 && status < 300 {
				var resp struct {
					AuthorizationCode string `json:"authorization_code"`
					CodeVerifier      string `json:"code_verifier"`
				}
				if err := json.Unmarshal(data, &resp); err != nil || resp.AuthorizationCode == "" || resp.CodeVerifier == "" {
					return pollResult[codexDeviceSuccess]{Status: pollFailed, Message: "invalid OpenAI Codex device auth token response: " + string(data)}, nil
				}
				return pollResult[codexDeviceSuccess]{Status: pollComplete, Value: codexDeviceSuccess{
					authorizationCode: resp.AuthorizationCode,
					codeVerifier:      resp.CodeVerifier,
				}}, nil
			}

			if status == http.StatusForbidden || status == http.StatusNotFound {
				return pollResult[codexDeviceSuccess]{Status: pollPending}, nil
			}

			switch codexDeviceErrorCode(data) {
			case "deviceauth_authorization_pending":
				return pollResult[codexDeviceSuccess]{Status: pollPending}, nil
			case "slow_down":
				return pollResult[codexDeviceSuccess]{Status: pollSlowDown}, nil
			}
			return pollResult[codexDeviceSuccess]{Status: pollFailed, Message: fmt.Sprintf("OpenAI Codex device auth failed with status %d: %s", status, string(data))}, nil
		},
	})
}

// coerceInterval accepts a JSON number or numeric string and returns its
// integer value (the device-auth `interval` field arrives as either).
func coerceInterval(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return int(x), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// codexDeviceErrorCode extracts the error code from a device-auth error body,
// handling both `{"error":"code"}` and `{"error":{"code":"..."}}`.
func codexDeviceErrorCode(data []byte) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	raw, ok := m["error"]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Code
	}
	return ""
}

// createState returns a random hex state value.
func createState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CodexAccountID extracts the chatgpt_account_id claim from a Codex OAuth
// access token (a JWT), or "" if absent. Inference adapters send it as the
// chatgpt-account-id header.
func CodexAccountID(accessToken string) string {
	return openaiCodexAccountID(accessToken)
}

// openaiCodexAccountID extracts the chatgpt_account_id claim from an access
// token JWT, or "" if absent.
func openaiCodexAccountID(accessToken string) string {
	payload := decodeJWTClaims(accessToken)
	if payload == nil {
		return ""
	}
	auth, ok := payload[openaiCodexJWTClaimPath].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := auth["chatgpt_account_id"].(string)
	return id
}

// decodeJWTClaims decodes the (unverified) payload of a JWT.
func decodeJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Fall back to standard base64 for tolerant decoding.
		raw, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}
