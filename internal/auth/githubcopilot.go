package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// githubCopilotProvider implements the GitHub Copilot OAuth device-code flow.
// Ported from pi's github-copilot.ts.
type githubCopilotProvider struct {
	clientID string
	headers  map[string]string
	// resolveURLs builds the endpoint set for a domain. It is a field so tests
	// can point the flow at an httptest server.
	resolveURLs func(domain string) copilotURLs
}

type copilotURLs struct {
	deviceCode   string
	accessToken  string
	copilotToken string
}

const githubCopilotClientIDB64 = "SXYxLmI1MDdhMDhjODdlY2ZlOTg="

func defaultCopilotURLs(domain string) copilotURLs {
	return copilotURLs{
		deviceCode:   "https://" + domain + "/login/device/code",
		accessToken:  "https://" + domain + "/login/oauth/access_token",
		copilotToken: "https://api." + domain + "/copilot_internal/v2/token",
	}
}

func newGitHubCopilotProvider() *githubCopilotProvider {
	id := githubCopilotClientIDB64
	if decoded, err := base64.StdEncoding.DecodeString(githubCopilotClientIDB64); err == nil {
		id = string(decoded)
	}
	return &githubCopilotProvider{
		clientID: id,
		headers: map[string]string{
			"User-Agent":             "GitHubCopilotChat/0.35.0",
			"Editor-Version":         "vscode/1.107.0",
			"Editor-Plugin-Version":  "copilot-chat/0.35.0",
			"Copilot-Integration-Id": "vscode-chat",
		},
		resolveURLs: defaultCopilotURLs,
	}
}

func (p *githubCopilotProvider) ID() string                  { return "github-copilot" }
func (p *githubCopilotProvider) Name() string                { return "GitHub Copilot" }
func (p *githubCopilotProvider) UsesCallbackServer() bool    { return false }
func (p *githubCopilotProvider) APIKey(c Credentials) string { return c.Access }

func (p *githubCopilotProvider) Login(ctx context.Context, cb LoginCallbacks) (Credentials, error) {
	input, err := cb.OnPrompt(Prompt{
		Message:     "GitHub Enterprise URL/domain (blank for github.com)",
		Placeholder: "company.ghe.com",
		AllowEmpty:  true,
	})
	if err != nil {
		return Credentials{}, err
	}
	if ctx.Err() != nil {
		return Credentials{}, errors.New(deviceCancelMessage)
	}

	trimmed := strings.TrimSpace(input)
	enterpriseDomain := normalizeDomain(input)
	if trimmed != "" && enterpriseDomain == "" {
		return Credentials{}, errors.New("invalid GitHub Enterprise URL/domain")
	}
	domain := enterpriseDomain
	if domain == "" {
		domain = "github.com"
	}
	lg().Info("github-copilot: starting device login", "domain", domain, "enterprise", enterpriseDomain != "")

	device, err := p.startDeviceFlow(ctx, domain)
	if err != nil {
		return Credentials{}, err
	}
	lg().Info("github-copilot: device code issued", "user_code", device.userCode, "verification_uri", device.verificationURI, "interval_s", device.interval, "expires_in_s", device.expiresIn)
	if cb.OnDeviceCode != nil {
		cb.OnDeviceCode(DeviceCodeInfo{
			UserCode:         device.userCode,
			VerificationURI:  device.verificationURI,
			IntervalSeconds:  device.interval,
			ExpiresInSeconds: device.expiresIn,
		})
	}

	githubAccessToken, err := p.pollForAccessToken(ctx, domain, device)
	if err != nil {
		return Credentials{}, err
	}
	lg().Debug("github-copilot: GitHub access token received", "token", redact(githubAccessToken))

	cb.progress("Fetching Copilot token...")
	return p.refreshCopilotToken(ctx, githubAccessToken, enterpriseDomain)
}

func (p *githubCopilotProvider) RefreshToken(ctx context.Context, creds Credentials) (Credentials, error) {
	return p.refreshCopilotToken(ctx, creds.Refresh, creds.StringExtra("enterpriseUrl"))
}

type copilotDeviceCode struct {
	deviceCode      string
	userCode        string
	verificationURI string
	interval        int
	expiresIn       int
}

func (p *githubCopilotProvider) startDeviceFlow(ctx context.Context, domain string) (copilotDeviceCode, error) {
	urls := p.resolveURLs(domain)
	var resp struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expires_in"`
	}
	form := url.Values{}
	form.Set("client_id", p.clientID)
	form.Set("scope", "read:user")
	if err := postFormJSON(ctx, urls.deviceCode, form, map[string]string{
		"User-Agent": p.headers["User-Agent"],
	}, &resp); err != nil {
		return copilotDeviceCode{}, err
	}
	if resp.DeviceCode == "" || resp.UserCode == "" || resp.VerificationURI == "" || resp.ExpiresIn <= 0 {
		lg().Error("github-copilot: invalid device code response fields", "has_device_code", resp.DeviceCode != "", "has_user_code", resp.UserCode != "", "has_verification_uri", resp.VerificationURI != "", "expires_in_s", resp.ExpiresIn)
		return copilotDeviceCode{}, errors.New("invalid device code response fields")
	}
	return copilotDeviceCode{
		deviceCode:      resp.DeviceCode,
		userCode:        resp.UserCode,
		verificationURI: resp.VerificationURI,
		interval:        resp.Interval,
		expiresIn:       resp.ExpiresIn,
	}, nil
}

func (p *githubCopilotProvider) pollForAccessToken(ctx context.Context, domain string, device copilotDeviceCode) (string, error) {
	urls := p.resolveURLs(domain)
	return pollDeviceCode[string](ctx, devicePollOptions[string]{
		Label:            "github-copilot",
		IntervalSeconds:  device.interval,
		ExpiresInSeconds: device.expiresIn,
		Poll: func(ctx context.Context) (pollResult[string], error) {
			form := url.Values{}
			form.Set("client_id", p.clientID)
			form.Set("device_code", device.deviceCode)
			form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

			var resp struct {
				AccessToken      string `json:"access_token"`
				Error            string `json:"error"`
				ErrorDescription string `json:"error_description"`
			}
			if err := postFormJSON(ctx, urls.accessToken, form, map[string]string{
				"User-Agent": p.headers["User-Agent"],
			}, &resp); err != nil {
				return pollResult[string]{}, err
			}
			if resp.AccessToken != "" {
				return pollResult[string]{Status: pollComplete, Value: resp.AccessToken}, nil
			}
			switch resp.Error {
			case "authorization_pending":
				return pollResult[string]{Status: pollPending}, nil
			case "slow_down":
				return pollResult[string]{Status: pollSlowDown}, nil
			case "":
				return pollResult[string]{Status: pollFailed, Message: "invalid device token response"}, nil
			default:
				msg := "device flow failed: " + resp.Error
				if resp.ErrorDescription != "" {
					msg += ": " + resp.ErrorDescription
				}
				return pollResult[string]{Status: pollFailed, Message: msg}, nil
			}
		},
	})
}

func (p *githubCopilotProvider) refreshCopilotToken(ctx context.Context, githubAccessToken, enterpriseDomain string) (Credentials, error) {
	domain := enterpriseDomain
	if domain == "" {
		domain = "github.com"
	}
	urls := p.resolveURLs(domain)

	headers := map[string]string{
		"Accept":        "application/json",
		"Authorization": "Bearer " + githubAccessToken,
	}
	for k, v := range p.headers {
		headers[k] = v
	}

	var resp struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := getJSON(ctx, urls.copilotToken, headers, &resp); err != nil {
		lg().Error("github-copilot: fetching Copilot token failed", "url", urls.copilotToken, "err", err)
		return Credentials{}, err
	}
	if resp.Token == "" || resp.ExpiresAt == 0 {
		lg().Error("github-copilot: invalid Copilot token response fields", "has_token", resp.Token != "", "expires_at", resp.ExpiresAt)
		return Credentials{}, errors.New("invalid Copilot token response fields")
	}
	lg().Info("github-copilot: Copilot token fetched", "expires_at_unix_s", resp.ExpiresAt, "token", redact(resp.Token))

	creds := Credentials{
		Refresh: githubAccessToken,
		Access:  resp.Token,
		Expires: resp.ExpiresAt*1000 - 5*60*1000,
	}
	if enterpriseDomain != "" {
		creds.Extra = map[string]any{"enterpriseUrl": enterpriseDomain}
	}
	return creds, nil
}

var copilotProxyEPRe = regexp.MustCompile(`proxy-ep=([^;]+)`)

// GitHubCopilotBaseURL derives the API base URL for a Copilot access token,
// extracting the proxy endpoint embedded in the token. Falls back to the
// enterprise or individual host. Exported for callers wiring Copilot
// inference. Mirrors pi's getGitHubCopilotBaseUrl.
func GitHubCopilotBaseURL(token, enterpriseDomain string) string {
	if token != "" {
		if m := copilotProxyEPRe.FindStringSubmatch(token); m != nil {
			apiHost := strings.TrimPrefix(m[1], "proxy.")
			if apiHost != m[1] {
				apiHost = "api." + apiHost
			}
			return "https://" + apiHost
		}
	}
	if enterpriseDomain != "" {
		return "https://copilot-api." + enterpriseDomain
	}
	return "https://api.individual.githubcopilot.com"
}
