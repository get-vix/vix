package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	githubCopilotLoginID = "github-copilot"

	githubDeviceCodeURL  = "https://github.com/login/device/code"
	githubTokenURL       = "https://github.com/login/oauth/access_token"
	githubDeviceVerifyUI = "https://github.com/login/device"
	githubClientID       = "Iv1.b507a08c87ecfe98"
	githubScope          = "read:user"

	copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

	copilotDeviceTimeoutSeconds = 900
)

type githubCopilotProvider struct {
	clientID              string
	deviceUserCodeURL     string
	deviceTokenURL        string
	deviceVerificationURI string
	deviceTimeoutSeconds  int

	copilotTokenURL string
}

func newGithubCopilotProvider() *githubCopilotProvider {
	p := &githubCopilotProvider{
		clientID:              githubClientID,
		deviceUserCodeURL:     githubDeviceCodeURL,
		deviceTokenURL:        githubTokenURL,
		deviceVerificationURI: githubDeviceVerifyUI,
		deviceTimeoutSeconds:  copilotDeviceTimeoutSeconds,
		copilotTokenURL:       copilotTokenURL,
	}
	if s, ok := loginSpec(githubCopilotLoginID); ok {
		if s.ClientID != "" {
			p.clientID = decodeClientID(s.ClientID)
		}
		if d := s.Device; d != nil {
			if d.UserCodeURL != "" {
				p.deviceUserCodeURL = d.UserCodeURL
			}
			if d.TokenURL != "" {
				p.deviceTokenURL = d.TokenURL
			}
			if d.VerificationURI != "" {
				p.deviceVerificationURI = d.VerificationURI
			}
			if d.TimeoutSeconds != 0 {
				p.deviceTimeoutSeconds = d.TimeoutSeconds
			}
		}
	}
	return p
}

func (p *githubCopilotProvider) ID() string               { return githubCopilotLoginID }
func (p *githubCopilotProvider) Name() string              { return "GitHub Copilot" }
func (p *githubCopilotProvider) UsesCallbackServer() bool  { return false }
func (p *githubCopilotProvider) APIKey(c Credentials) string { return c.Access }

func (p *githubCopilotProvider) Login(ctx context.Context, cb LoginCallbacks) (Credentials, error) {
	device, err := p.startDeviceAuth(ctx)
	if err != nil {
		return Credentials{}, err
	}

	if cb.OnDeviceCode != nil {
		cb.OnDeviceCode(DeviceCodeInfo{
			UserCode:         device.userCode,
			VerificationURI:  p.deviceVerificationURI,
			IntervalSeconds:  device.intervalSeconds,
			ExpiresInSeconds: p.deviceTimeoutSeconds,
		})
	}
	lg().Info("github-copilot: device code issued", "user_code", device.userCode, "verification_uri", p.deviceVerificationURI, "interval_s", device.intervalSeconds)

	githubToken, err := p.pollDeviceAuth(ctx, device)
	if err != nil {
		return Credentials{}, err
	}

	// Copilot Personal users authenticate with the raw GitHub OAuth token
	// (ghu_*) directly via "Authorization: Bearer <token>" to
	// api.individual.githubcopilot.com. Only Enterprise/Business users need
	// the token exchange at api.github.com/copilot_internal/v2/token.
	//
	// Since most personal users have no Business exchange, skip the exchange.
	// The Enterprise path can be added later when vix supports org accounts.
	return Credentials{
		Access:  githubToken,
		Refresh: githubToken,
	}, nil
}

func (p *githubCopilotProvider) RefreshToken(ctx context.Context, creds Credentials) (Credentials, error) {
	githubToken := creds.Refresh
	if githubToken == "" {
		return Credentials{}, errors.New("github-copilot: no refresh token (GitHub OAuth token) available")
	}
	// Personal users don't exchange; the raw GitHub OAuth token is long-lived.
	return creds, nil
}

type githubDeviceAuth struct {
	deviceCode      string
	userCode        string
	intervalSeconds int
}

func (p *githubCopilotProvider) startDeviceAuth(ctx context.Context) (githubDeviceAuth, error) {
	body := fmt.Sprintf("client_id=%s&scope=%s", p.clientID, githubScope)
	status, data, err := httpRequest(ctx, http.MethodPost, p.deviceUserCodeURL, map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"Accept":       "application/json",
	}, strings.NewReader(body))
	if err != nil {
		return githubDeviceAuth{}, err
	}
	if status < 200 || status >= 300 {
		return githubDeviceAuth{}, fmt.Errorf("github device code request failed with status %d: %s", status, string(data))
	}

	var resp struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return githubDeviceAuth{}, fmt.Errorf("invalid GitHub device code response: %s", string(data))
	}
	if resp.DeviceCode == "" || resp.UserCode == "" {
		return githubDeviceAuth{}, fmt.Errorf("invalid GitHub device code response: %s", string(data))
	}
	interval := resp.Interval
	if interval <= 0 {
		interval = deviceDefaultPollIntervalSeconds
	}
	if resp.ExpiresIn > 0 && resp.ExpiresIn < p.deviceTimeoutSeconds {
		p.deviceTimeoutSeconds = resp.ExpiresIn
	}
	return githubDeviceAuth{
		deviceCode:      resp.DeviceCode,
		userCode:        resp.UserCode,
		intervalSeconds: interval,
	}, nil
}

func (p *githubCopilotProvider) pollDeviceAuth(ctx context.Context, device githubDeviceAuth) (string, error) {
	result, err := pollDeviceCode(ctx, devicePollOptions[string]{
		Label:            "github-copilot",
		IntervalSeconds:  device.intervalSeconds,
		ExpiresInSeconds: p.deviceTimeoutSeconds,
		Poll: func(ctx context.Context) (pollResult[string], error) {
			body := fmt.Sprintf("client_id=%s&device_code=%s&grant_type=urn:ietf:params:oauth:grant-type:device_code",
				p.clientID, device.deviceCode)
			status, data, err := httpRequest(ctx, http.MethodPost, p.deviceTokenURL, map[string]string{
				"Content-Type": "application/x-www-form-urlencoded",
				"Accept":       "application/json",
			}, strings.NewReader(body))
			if err != nil {
				return pollResult[string]{}, err
			}

			if status >= 200 && status < 300 {
				// GitHub returns HTTP 200 for success AND pending/error states.
				// Check for error field first.
				var errResp struct {
					Error string `json:"error"`
				}
				if json.Unmarshal(data, &errResp) == nil && errResp.Error != "" {
					switch errResp.Error {
					case "authorization_pending":
						return pollResult[string]{Status: pollPending}, nil
					case "slow_down":
						return pollResult[string]{Status: pollSlowDown}, nil
					case "expired_token":
						return pollResult[string]{Status: pollFailed, Message: "GitHub device code expired"}, nil
					case "access_denied":
						return pollResult[string]{Status: pollFailed, Message: "GitHub authorization denied"}, nil
					}
					return pollResult[string]{Status: pollFailed, Message: fmt.Sprintf("GitHub device auth error: %s", errResp.Error)}, nil
				}

				var resp struct {
					AccessToken string `json:"access_token"`
					TokenType   string `json:"token_type"`
				}
				if err := json.Unmarshal(data, &resp); err != nil || resp.AccessToken == "" {
					return pollResult[string]{Status: pollFailed, Message: "invalid GitHub token response: " + string(data)}, nil
				}
				lg().Info("github-copilot: GitHub OAuth token obtained", "token", redact(resp.AccessToken))
				return pollResult[string]{Status: pollComplete, Value: resp.AccessToken}, nil
			}
			return pollResult[string]{Status: pollFailed, Message: fmt.Sprintf("GitHub token request failed with status %d", status)}, nil
		},
	})
	if err != nil {
		return "", err
	}
	return result, nil
}

func (p *githubCopilotProvider) exchangeForCopilotToken(ctx context.Context, githubToken string) (Credentials, error) {
	lg().Info("github-copilot: exchanging GitHub OAuth token for Copilot session token", "github_token", redact(githubToken))
	status, data, err := httpRequest(ctx, http.MethodGet, p.copilotTokenURL, map[string]string{
		"Authorization":          "token " + githubToken,
		"Accept":                 "application/json",
		"User-Agent":             "GitHubCopilot/1.0",
		"Editor-Version":         "vscode/1.96.0",
		"Copilot-Integration-Id": "vscode-chat",
	}, nil)
	if err != nil {
		return Credentials{}, fmt.Errorf("copilot token exchange error: %w", err)
	}
	if status < 200 || status >= 300 {
		return Credentials{}, fmt.Errorf("copilot token exchange failed (%d): %s", status, string(data))
	}

	var resp struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"` // Unix seconds
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(data, &resp); err != nil || resp.Token == "" {
		return Credentials{}, fmt.Errorf("invalid copilot token exchange response: %s", string(data))
	}

	expiresMS := int64(0)
	if resp.ExpiresAt > 0 {
		expiresMS = resp.ExpiresAt * 1000
		// Refresh 5 minutes before expiry to avoid edge cases.
		refreshMargin := int64(5 * 60 * 1000)
		if expiresMS > refreshMargin {
			expiresMS -= refreshMargin
		}
	}

	extra := map[string]any{}
	if resp.Endpoints.API != "" {
		extra["api_endpoint"] = resp.Endpoints.API
	}

	lg().Info("github-copilot: token exchange succeeded",
		"expires_at_unix_s", resp.ExpiresAt,
		"api_endpoint", resp.Endpoints.API,
		"token", redact(resp.Token))
	return Credentials{
		Access:  resp.Token,
		Refresh: githubToken,
		Expires: expiresMS,
		Extra:   extra,
	}, nil
}
