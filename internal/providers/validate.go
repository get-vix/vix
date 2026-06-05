package providers

import (
	"fmt"
	"net/url"
	"strings"
)

// allowedAuthHosts is the compiled allowlist of hosts that may appear in an
// auth_logins flow (authorize/token/keys/device URLs). A config file — even a
// local one — cannot point an OAuth token exchange at an arbitrary host. This
// closes the credential-exfiltration surface the data-driven design opens.
var allowedAuthHosts = map[string]bool{
	"claude.ai":           true,
	"platform.claude.com": true,
	"api.anthropic.com":   true,
	"auth.openai.com":     true,
	"openrouter.ai":       true,
}

// validWireFormats / validAuthSchemes / validCredKinds / validFlows /
// validEffortStyles / validProducers are the closed enums dispatched through
// compiled tables elsewhere. Unknown values are rejected here so JSON can never
// select behavior the binary doesn't implement.
var (
	validWireFormats  = map[WireFormat]bool{WireMessages: true, WireResponses: true, WireChatCompletions: true}
	validAuthSchemes  = map[string]bool{AuthSchemeBearer: true, AuthSchemeXAPIKey: true}
	validCredKinds    = map[string]bool{CredAPIKey: true, CredOAuthMintKey: true, CredOAuthToken: true}
	validFlows        = map[string]bool{FlowOAuthPKCEToken: true, FlowOAuthCodex: true, FlowOAuthPKCEMint: true}
	validEffortStyles = map[string]bool{EffortStyleNone: true, EffortStyleReasoningEffort: true, EffortStyleReasoningSplit: true}
	validEfforts      = map[string]bool{EffortAdaptive: true, EffortOpenAIReasoning: true, "": true}
	validProducers    = map[string]bool{"": true, ProducerAnthropicOAuth: true, ProducerCodexOAuth: true}
)

// validate checks schema version, closed enums, required fields, prefix
// uniqueness, and URL safety (HTTPS, auth-host allowlist, deny-list).
func validate(f File) error {
	if f.SchemaVersion == 0 {
		return fmt.Errorf("missing schema_version")
	}
	if f.SchemaVersion > SchemaVersion {
		return fmt.Errorf("schema_version %d is newer than supported (%d); upgrade vix", f.SchemaVersion, SchemaVersion)
	}
	if len(f.Providers) == 0 {
		return fmt.Errorf("no providers defined")
	}

	seenID := make(map[string]bool, len(f.Providers))
	seenPrefix := make(map[string]string, len(f.Providers)) // prefix -> id
	for _, p := range f.Providers {
		if err := validateProvider(p); err != nil {
			return err
		}
		if seenID[p.ID] {
			return fmt.Errorf("duplicate provider id %q", p.ID)
		}
		seenID[p.ID] = true
		if other, ok := seenPrefix[p.ModelPrefix]; ok {
			return fmt.Errorf("model_prefix %q collides between providers %q and %q", p.ModelPrefix, other, p.ID)
		}
		seenPrefix[p.ModelPrefix] = p.ID
	}

	authIDs := make(map[string]bool, len(f.AuthLogins))
	for _, l := range f.AuthLogins {
		if err := validateAuthLogin(l); err != nil {
			return err
		}
		if authIDs[l.ID] {
			return fmt.Errorf("duplicate auth login id %q", l.ID)
		}
		authIDs[l.ID] = true
	}

	// Every credential method that references an OAuth login must resolve to a
	// declared auth login.
	for _, p := range f.Providers {
		for _, m := range p.Credential {
			if m.LoginID != "" && !authIDs[m.LoginID] {
				return fmt.Errorf("provider %q credential method references unknown login_id %q", p.ID, m.LoginID)
			}
		}
	}
	return nil
}

func validateProvider(p ProviderSpec) error {
	if p.ID == "" {
		return fmt.Errorf("provider with empty id")
	}
	if p.ModelPrefix == "" {
		return fmt.Errorf("provider %q: empty model_prefix", p.ID)
	}
	if strings.Contains(p.ModelPrefix, "/") {
		return fmt.Errorf("provider %q: model_prefix %q must not contain '/'", p.ID, p.ModelPrefix)
	}
	if !validWireFormats[p.WireFormat] {
		return fmt.Errorf("provider %q: unknown wire_format %q", p.ID, p.WireFormat)
	}
	if !validEfforts[p.EffortPolicy] {
		return fmt.Errorf("provider %q: unknown effort_policy %q", p.ID, p.EffortPolicy)
	}
	if !validAuthSchemes[p.Inference.AuthScheme] {
		return fmt.Errorf("provider %q: unknown auth_scheme %q", p.ID, p.Inference.AuthScheme)
	}
	if !validEffortStyles[p.Inference.EffortStyle] {
		return fmt.Errorf("provider %q: unknown effort_style %q", p.ID, p.Inference.EffortStyle)
	}
	if p.Inference.BaseURL == "" {
		return fmt.Errorf("provider %q: empty inference.base_url", p.ID)
	}
	if err := checkURL(interpolate(p.Inference.BaseURL), false); err != nil {
		return fmt.Errorf("provider %q base_url: %w", p.ID, err)
	}
	if len(p.Credential) == 0 {
		return fmt.Errorf("provider %q: no credential_methods", p.ID)
	}
	for i, m := range p.Credential {
		if !validCredKinds[m.Kind] {
			return fmt.Errorf("provider %q credential[%d]: unknown kind %q", p.ID, i, m.Kind)
		}
		if !validProducers[m.ExtraHeadersProducer] {
			return fmt.Errorf("provider %q credential[%d]: unknown extra_headers_producer %q", p.ID, i, m.ExtraHeadersProducer)
		}
		if m.HeaderStyle != "" && m.HeaderStyle != AuthSchemeBearer {
			return fmt.Errorf("provider %q credential[%d]: unknown header_style %q", p.ID, i, m.HeaderStyle)
		}
		switch m.Kind {
		case CredAPIKey:
			if m.EnvVar == "" && m.Keyring == "" {
				return fmt.Errorf("provider %q credential[%d]: api_key needs env_var or keyring", p.ID, i)
			}
		case CredOAuthMintKey, CredOAuthToken:
			if m.LoginID == "" {
				return fmt.Errorf("provider %q credential[%d]: %s needs login_id", p.ID, i, m.Kind)
			}
		}
		if m.BaseURL != "" {
			if err := checkURL(interpolate(m.BaseURL), false); err != nil {
				return fmt.Errorf("provider %q credential[%d] base_url: %w", p.ID, i, err)
			}
		}
	}
	return nil
}

func validateAuthLogin(l AuthLogin) error {
	if l.ID == "" {
		return fmt.Errorf("auth login with empty id")
	}
	if !validFlows[l.Flow] {
		return fmt.Errorf("auth login %q: unknown flow %q", l.ID, l.Flow)
	}
	urls := []string{l.AuthorizeURL, l.TokenURL, l.KeysURL}
	if l.Device != nil {
		urls = append(urls, l.Device.UserCodeURL, l.Device.TokenURL, l.Device.VerificationURI)
	}
	for _, u := range urls {
		if u == "" {
			continue
		}
		if err := checkURL(u, false); err != nil {
			return fmt.Errorf("auth login %q: %w", l.ID, err)
		}
		if err := checkAuthHost(u); err != nil {
			return fmt.Errorf("auth login %q: %w", l.ID, err)
		}
	}
	// Redirect URIs are OAuth loopback callbacks: http://localhost is required.
	for _, u := range []string{l.RedirectURI, deviceRedirect(l)} {
		if u == "" {
			continue
		}
		if err := checkURL(u, true); err != nil {
			return fmt.Errorf("auth login %q redirect_uri: %w", l.ID, err)
		}
	}
	return nil
}

func deviceRedirect(l AuthLogin) string {
	if l.Device == nil {
		return ""
	}
	return l.Device.RedirectURI
}

// checkURL enforces HTTPS (unless allowLoopbackHTTP and the host is localhost)
// and the deny-list. It does not enforce the auth-host allowlist — callers add
// checkAuthHost for credential-bearing auth endpoints.
func checkURL(raw string, allowLoopbackHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	host := u.Hostname()
	switch u.Scheme {
	case "https":
		// ok
	case "http":
		if !(allowLoopbackHTTP && isLoopback(host)) {
			return fmt.Errorf("non-HTTPS URL %q", raw)
		}
	default:
		return fmt.Errorf("unsupported URL scheme in %q", raw)
	}
	if URLDenied != nil && URLDenied(raw) {
		return fmt.Errorf("URL %q is deny-listed", raw)
	}
	return nil
}

func checkAuthHost(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if !allowedAuthHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("auth host %q not in allowlist", u.Hostname())
	}
	return nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
