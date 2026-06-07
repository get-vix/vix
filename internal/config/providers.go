package config

import (
	"github.com/get-vix/vix/internal/auth"
	"github.com/get-vix/vix/internal/providers"
)

// AuthKind classifies how a provider credential is obtained.
type AuthKind int

const (
	// APIKeyAuth is a static API key resolved from env / keychain / .env.
	APIKeyAuth AuthKind = iota
	// OAuthMintKey is an interactive OAuth login that yields a normal API key,
	// stored and resolved exactly like an APIKeyAuth key.
	OAuthMintKey
	// OAuthToken is an interactive OAuth login that yields a refreshable access
	// token, resolved via the auth subsystem.
	OAuthToken
)

// HeaderStyle selects how a credential authenticates an HTTP request.
type HeaderStyle string

const (
	// APIKeyHeader uses the SDK-native API key header: Anthropic sends
	// x-api-key, OpenAI-family SDKs send Authorization: Bearer <key>. It is the
	// zero value so an unset HeaderStyle behaves like a plain API key.
	APIKeyHeader HeaderStyle = ""
	// BearerHeader sends Authorization: Bearer <token> regardless of provider —
	// used for OAuth access tokens.
	BearerHeader HeaderStyle = "bearer"
)

// AuthMethod is one way to obtain a credential for a provider. A provider lists
// its methods in priority order; resolution returns the first that yields a
// value. The data now lives in the providers registry (providers.json); this
// struct is the in-memory projection credential resolution consumes.
type AuthMethod struct {
	Kind        AuthKind
	EnvVar      string                               // APIKeyAuth: env var name ("" to skip env lookup)
	Keyring     string                               // keychain "user" field for this method
	LoginID     string                               // OAuth*: internal/auth provider id
	BaseURL     string                               // optional endpoint override implied by this method
	HeaderStyle HeaderStyle                          // wire auth header style
	Extra       func(value string) map[string]string // optional extra headers derived from the credential value
}

// extraHeaderProducers maps a providers.json extra_headers_producer name to the
// compiled function that derives the headers. The set is closed; an unknown
// name is rejected at providers-registry validation time, so a missing entry
// here would be a build defect.
var extraHeaderProducers = map[string]func(string) map[string]string{
	providers.ProducerAnthropicOAuth: anthropicOAuthExtra,
	providers.ProducerCodexOAuth:     codexOAuthExtra,
}

// credKindToAuthKind maps a providers.json credential kind to the local enum.
func credKindToAuthKind(kind string) AuthKind {
	switch kind {
	case providers.CredOAuthMintKey:
		return OAuthMintKey
	case providers.CredOAuthToken:
		return OAuthToken
	default:
		return APIKeyAuth
	}
}

// authMethodFromSpec projects a registry credential method into an AuthMethod.
func authMethodFromSpec(m providers.CredentialMethod) AuthMethod {
	am := AuthMethod{
		Kind:    credKindToAuthKind(m.Kind),
		EnvVar:  m.EnvVar,
		Keyring: m.Keyring,
		LoginID: m.LoginID,
		BaseURL: m.BaseURL,
	}
	if m.HeaderStyle == providers.AuthSchemeBearer {
		am.HeaderStyle = BearerHeader
	}
	if prod := extraHeaderProducers[m.ExtraHeadersProducer]; prod != nil {
		am.Extra = prod
	}
	return am
}

// anthropicOAuthExtra adds the beta header the Anthropic Messages API requires
// to accept an OAuth bearer token. It carries no Claude-Code identity markers.
func anthropicOAuthExtra(string) map[string]string {
	return map[string]string{"anthropic-beta": "oauth-2025-04-20"}
}

// codexOAuthExtra adds the headers the ChatGPT/Codex backend expects: the
// Responses beta opt-in and the per-user chatgpt-account-id derived from the
// access-token JWT.
func codexOAuthExtra(token string) map[string]string {
	h := map[string]string{"OpenAI-Beta": "responses=experimental"}
	if id := auth.CodexAccountID(token); id != "" {
		h["chatgpt-account-id"] = id
	}
	return h
}

// AuthMethodsFor returns the ordered auth methods for a provider, or nil.
func AuthMethodsFor(provider string) []AuthMethod {
	spec, ok := providers.Default().Lookup(provider)
	if !ok {
		return nil
	}
	out := make([]AuthMethod, 0, len(spec.Credential))
	for _, m := range spec.Credential {
		out = append(out, authMethodFromSpec(m))
	}
	return out
}

// ProviderHasAPIKeyAuth reports whether a provider has at least one API key
// credential method (i.e. non-OAuth). OAuth-only providers should hide the
// API Key row in the UI to avoid confusing "Create key" buttons.
func ProviderHasAPIKeyAuth(provider string) bool {
	for _, m := range AuthMethodsFor(provider) {
		if m.Kind == APIKeyAuth {
			return true
		}
	}
	return false
}

// HasOAuthLogin reports whether an OAuth login is stored for the given login id.
func HasOAuthLogin(loginID string) bool {
	return auth.DefaultStorage().HasLogin(loginID)
}

// OAuthLoginID returns the internal/auth login id for a provider's OAuth method,
// or "" when the provider has no OAuth login. Single source of truth for the
// provider→loginID mapping, shared by the UI and credential-status helpers.
func OAuthLoginID(provider string) string {
	spec, ok := providers.Default().Lookup(provider)
	if !ok {
		return ""
	}
	for _, m := range spec.Credential {
		if (m.Kind == providers.CredOAuthMintKey || m.Kind == providers.CredOAuthToken) && m.LoginID != "" {
			return m.LoginID
		}
	}
	return ""
}

// KnownProviders returns the known provider ids in registry (stable) order.
func KnownProviders() []string {
	return providers.Default().IDs()
}

// PrimaryEnvVar returns the canonical API-key env var for a provider, used for
// "no credential, set X" error messages. Returns "" for providers with no
// API-key env var.
func PrimaryEnvVar(provider string) string {
	methods := AuthMethodsFor(provider)
	// Prefer a plain API-key method (APIKeyHeader) so we point users at the
	// real key var rather than, e.g., an OAuth bearer-token env var.
	for _, m := range methods {
		if m.Kind == APIKeyAuth && m.EnvVar != "" && m.HeaderStyle == APIKeyHeader {
			return m.EnvVar
		}
	}
	for _, m := range methods {
		if m.EnvVar != "" {
			return m.EnvVar
		}
	}
	return ""
}
