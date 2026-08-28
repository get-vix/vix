package config

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/get-vix/vix/internal/auth"
)

// Auth-default kinds: which credential method a provider prefers when more than
// one is available. Stored as a small non-secret marker in the secret provider
// (see ProviderAuthDefault) so the preference lives alongside the credentials
// it governs and is honored by credential resolution everywhere.
const (
	AuthDefaultAPIKey = "api_key"
	AuthDefaultOAuth  = "oauth"
)

// KeySource describes where the API key was found.
type KeySource string

const (
	KeySourceOAuthToken     KeySource = "oauth-token"
	KeySourceSecretProvider KeySource = "secret-provider"
	// KeySourceLocal marks a keyless local provider (NoneAuth): the credential
	// is a fixed placeholder the server ignores.
	KeySourceLocal KeySource = "local"
	KeySourceNone  KeySource = "none"
)

// Credential bundles an API key or OAuth token with everything an adapter
// needs to authenticate: its source (for display), the wire header style, an
// optional endpoint override, and any extra headers implied by the auth method.
type Credential struct {
	Value        string
	Source       KeySource
	HeaderStyle  HeaderStyle       // "" (APIKeyHeader) means SDK-native API key header
	BaseURL      string            // "" means use the adapter's default endpoint
	ExtraHeaders map[string]string // applied verbatim on every request
}

// RequestOptions returns the appropriate Anthropic SDK options for this
// credential, driven by HeaderStyle and ExtraHeaders. BearerHeader sends an
// Authorization: Bearer token; otherwise the value is sent as the API key
// (x-api-key on Anthropic).
func (c Credential) RequestOptions() []option.RequestOption {
	var opts []option.RequestOption
	if c.HeaderStyle == BearerHeader {
		opts = append(opts, option.WithHeader("Authorization", "Bearer "+c.Value))
	} else {
		opts = append(opts, option.WithAPIKey(c.Value))
	}
	for k, v := range c.ExtraHeaders {
		opts = append(opts, option.WithHeader(k, v))
	}
	return opts
}

// ResolveStoredSecret reads one account from the configured provider.
func ResolveStoredSecret(account string) (string, bool) {
	value, err := defaultStore().Get(account)
	return value, err == nil && value != ""
}

// ProviderKey holds a provider name and a display prefix of its stored key.
type ProviderKey struct {
	Provider string
	Prefix   string // first 10 chars of the stored key, for display; empty if not stored
}

// providerKeyringUser returns the legacy schema account for a provider's
// primary API key, e.g. "anthropic" → "anthropic-api-key".
func providerKeyringUser(provider string) string {
	return provider + "-api-key"
}

// resolveKey reads the configured provider account. Legacy EnvVar metadata is
// used only to derive an account name for providers that do not declare one; Vix
// never reads the process environment or dotenv files for secrets.
func resolveKey(envVar, keyringUser string) (string, KeySource) {
	account := keyringUser
	if account == "" && envVar != "" {
		account = strings.ToLower(strings.ReplaceAll(envVar, "_", "-"))
	}
	if account != "" {
		if key, err := defaultStore().Get(account); err == nil && key != "" {
			return key, KeySourceSecretProvider
		}
	}

	return "", KeySourceNone
}

// ResolveProviderCredential resolves a Credential for the given provider by
// walking its AuthMethods (see providers.go) in priority order and returning
// the first that yields a value. API-key methods are resolved from daz-secrets;
// OAuth methods are resolved from the same provider-backed auth
// subsystem (without refreshing). Returns a KeySourceNone credential when
// nothing is found.
func ResolveProviderCredential(provider string) Credential {
	return resolveProviderCredential(context.Background(), provider, false)
}

// ResolveProviderCredentialFresh behaves like ResolveProviderCredential but
// refreshes an expired stored OAuth access token first (bounded by ctx).
func ResolveProviderCredentialFresh(ctx context.Context, provider string) Credential {
	return resolveProviderCredential(ctx, provider, true)
}

func resolveProviderCredential(ctx context.Context, provider string, refresh bool) Credential {
	for _, m := range orderedAuthMethods(provider) {
		switch m.Kind {
		case APIKeyAuth:
			value, src := resolveKey(m.EnvVar, m.Keyring)
			if value == "" {
				continue
			}
			if m.HeaderStyle == BearerHeader {
				src = KeySourceOAuthToken
			}
			return buildCredential(value, src, m)
		case OAuthMintKey, OAuthToken:
			value := resolveOAuthValue(ctx, m.LoginID, refresh)
			if value == "" {
				continue
			}
			return buildCredential(value, KeySourceOAuthToken, m)
		case NoneAuth:
			// Keyless local provider: any non-empty placeholder works — the
			// server ignores the Authorization header.
			return buildCredential("local", KeySourceLocal, m)
		}
	}
	return Credential{Source: KeySourceNone}
}

// isOAuthMethod reports whether a method obtains its credential via an OAuth
// login. This includes the interactive OAuth flows (OAuthMintKey/OAuthToken)
// and bearer-style API-key methods, which carry an OAuth token shipped through
// the configured provider (legacy schemas may still name an EnvVar).
func isOAuthMethod(m AuthMethod) bool {
	if m.Kind == OAuthMintKey || m.Kind == OAuthToken {
		return true
	}
	return m.Kind == APIKeyAuth && m.HeaderStyle == BearerHeader
}

// orderedAuthMethods returns a provider's auth methods reordered so the user's
// preferred method (identified by AuthMethod.ID) is tried first. When no
// preference is stored, the provider's declared method order (providers.json) is
// preserved.
func orderedAuthMethods(provider string) []AuthMethod {
	return reorderAuthMethods(AuthMethodsFor(provider), ProviderAuthDefault(provider))
}

// reorderAuthMethods is the pure core of orderedAuthMethods: it promotes the
// method whose ID matches pref ahead of the rest while keeping the relative
// order of the others stable, so resolution still falls through to the remaining
// methods when the preferred one yields no value. A pref of "" — or one matching
// no method — leaves the order unchanged. Legacy kind-level preferences
// ("api_key"/"oauth") are honored by promoting the first method of that kind.
func reorderAuthMethods(methods []AuthMethod, pref string) []AuthMethod {
	if pref == "" || len(methods) < 2 {
		return methods
	}
	idx := -1
	for i, m := range methods {
		if m.ID() == pref {
			idx = i
			break
		}
	}
	if idx < 0 {
		// Legacy binary preference: promote the first method of the kind.
		preferOAuth := pref == AuthDefaultOAuth
		for i, m := range methods {
			if isOAuthMethod(m) == preferOAuth {
				idx = i
				break
			}
		}
	}
	if idx <= 0 {
		return methods
	}
	out := make([]AuthMethod, 0, len(methods))
	out = append(out, methods[idx])
	out = append(out, methods[:idx]...)
	out = append(out, methods[idx+1:]...)
	return out
}

// buildCredential assembles a Credential from a resolved value and its auth
// method (header style, endpoint override, and any derived extra headers). For a
// method whose endpoint is supplied by the user (RequiresBaseURL), the stored
// base URL overrides the method's static BaseURL.
func buildCredential(value string, src KeySource, m AuthMethod) Credential {
	baseURL := m.BaseURL
	if m.RequiresBaseURL {
		if u := resolveMethodBaseURL(m); u != "" {
			baseURL = u
		}
	}
	cred := Credential{Value: value, Source: src, HeaderStyle: m.HeaderStyle, BaseURL: baseURL}
	if m.Extra != nil {
		cred.ExtraHeaders = m.Extra(value)
	}
	return cred
}

// resolveMethodBaseURL returns the user-supplied endpoint stored next to the
// method credential. Returns "" when it is unset.
func resolveMethodBaseURL(m AuthMethod) string {
	if m.Keyring != "" {
		if v, err := defaultStore().Get(methodBaseURLKeyringUser(m.Keyring)); err == nil && v != "" {
			return v
		}
	}
	return ""
}

// resolveOAuthValue returns the stored access token / minted key for an OAuth
// login id, optionally refreshing an expired token. Returns "" when no login is
// stored or resolution fails (callers then fall through to the next method).
func resolveOAuthValue(ctx context.Context, loginID string, refresh bool) string {
	if loginID == "" {
		return ""
	}
	st := auth.DefaultStorage()
	if refresh {
		tok, err := st.AccessTokenRefreshing(ctx, loginID)
		if err != nil {
			return ""
		}
		return tok
	}
	tok, _, ok := st.AccessToken(loginID)
	if !ok {
		return ""
	}
	return tok
}

// ResolveProviderKey returns the resolved credential value and source for a
// provider. Thin wrapper over ResolveProviderCredential for callers that only
// need the key string.
func ResolveProviderKey(provider string) (key string, source KeySource) {
	cred := ResolveProviderCredential(provider)
	return cred.Value, cred.Source
}

// StoreProviderKey writes the API key for the given provider to daz-secrets.
func StoreProviderKey(provider, key string) error {
	return defaultStore().Set(providerKeyringUser(provider), key)
}

// DeleteProviderKey removes the API key for the given provider from the
// credential store. It also clears any stored default-method marker so
// credential resolution falls back to whatever credential remains.
func DeleteProviderKey(provider string) error {
	err := defaultStore().Delete(providerKeyringUser(provider))
	_ = ClearProviderAuthDefault(provider)
	if errors.Is(err, ErrCredNotFound) {
		return nil
	}
	return err
}

// methodBaseURLKeyringUser returns the legacy schema account for the endpoint
// stored alongside a method's key, e.g. "mimo-tokenplan-api-key" →
// "mimo-tokenplan-api-key-base-url".
func methodBaseURLKeyringUser(keyringUser string) string {
	return keyringUser + "-base-url"
}

// methodByID returns the auth method with the given identity for a provider.
func methodByID(provider, methodID string) (AuthMethod, bool) {
	for _, m := range AuthMethodsFor(provider) {
		if m.ID() == methodID {
			return m, true
		}
	}
	return AuthMethod{}, false
}

// StoreProviderMethodKey writes the API key for a specific credential method of
// a provider, plus the user-supplied base URL when the method requires one.
func StoreProviderMethodKey(provider, methodID, key, baseURL string) error {
	m, ok := methodByID(provider, methodID)
	if !ok {
		return fmt.Errorf("provider %q: unknown credential method %q", provider, methodID)
	}
	if m.Keyring == "" {
		return fmt.Errorf("provider %q method %q: no secret-provider account", provider, methodID)
	}
	if err := defaultStore().Set(m.Keyring, key); err != nil {
		return err
	}
	if m.RequiresBaseURL {
		if err := defaultStore().Set(methodBaseURLKeyringUser(m.Keyring), baseURL); err != nil {
			return err
		}
	}
	return nil
}

// DeleteProviderMethodKey removes the stored key (and endpoint) for a specific
// method, clearing the provider's default marker when it pointed at this method.
func DeleteProviderMethodKey(provider, methodID string) error {
	m, ok := methodByID(provider, methodID)
	if !ok {
		return fmt.Errorf("provider %q: unknown credential method %q", provider, methodID)
	}
	if m.Keyring == "" {
		return fmt.Errorf("provider %q method %q: no secret-provider account", provider, methodID)
	}
	err := defaultStore().Delete(m.Keyring)
	if errors.Is(err, ErrCredNotFound) {
		err = nil
	}
	if m.RequiresBaseURL {
		if derr := defaultStore().Delete(methodBaseURLKeyringUser(m.Keyring)); derr != nil && !errors.Is(derr, ErrCredNotFound) {
			// Best-effort: a missing endpoint entry is fine.
		}
	}
	if ProviderAuthDefault(provider) == methodID {
		_ = ClearProviderAuthDefault(provider)
	}
	return err
}

// providerAuthDefaultUser returns the secret-provider account for a provider's
// default-method marker, e.g. "anthropic" → "anthropic-auth-default".
func providerAuthDefaultUser(provider string) string {
	return provider + "-auth-default"
}

// ProviderAuthDefault returns the stored default-method preference for a
// provider — AuthDefaultAPIKey, AuthDefaultOAuth, or "" when unset.
func ProviderAuthDefault(provider string) string {
	v, err := defaultStore().Get(providerAuthDefaultUser(provider))
	if err != nil {
		return ""
	}
	return v
}

// SetProviderAuthDefault stores the default-method preference for a provider.
func SetProviderAuthDefault(provider, kind string) error {
	return defaultStore().Set(providerAuthDefaultUser(provider), kind)
}

// ClearProviderAuthDefault removes a provider's default-method marker. A missing
// marker is not an error.
func ClearProviderAuthDefault(provider string) error {
	err := defaultStore().Delete(providerAuthDefaultUser(provider))
	if errors.Is(err, ErrCredNotFound) {
		return nil
	}
	return err
}

// MethodStatus is the stored-credential state of one user-manageable credential
// method (an API-key method the user enters, or an interactive OAuth login).
// Bearer-token env fallbacks (e.g. CLAUDE_CODE_OAUTH_TOKEN) are not surfaced as
// their own method.
type MethodStatus struct {
	ID              string `json:"id"`                 // AuthMethod.ID() — stable identity for the default marker
	Label           string `json:"label"`              // display label ("API Key", "Token Plan", "OAuth")
	OAuth           bool   `json:"oauth"`              // render as a token method rather than an API key
	Stored          bool   `json:"stored"`             // a provider credential or OAuth login is available
	Prefix          string `json:"prefix,omitempty"`   // first 10 chars of a stored API key, for display
	RequiresBaseURL bool   `json:"requires_base_url"`  // method carries a user-supplied endpoint
	BaseURL         string `json:"base_url,omitempty"` // the stored/env endpoint, for display
	IsDefault       bool   `json:"is_default"`         // this method is the effective default
}

// ProviderAuthStatus summarizes a provider's user-manageable credential methods
// and which one is the effective default. It is the single read the UI needs to
// render the authentication panel without touching the provider or auth
// subsystem directly.
type ProviderAuthStatus struct {
	Methods []MethodStatus `json:"methods"`
}

// HasCredential reports whether any method has a stored/available credential.
func (s ProviderAuthStatus) HasCredential() bool {
	for _, m := range s.Methods {
		if m.Stored {
			return true
		}
	}
	return false
}

// Default returns the ID of the effective default method, or "" when none.
func (s ProviderAuthStatus) Default() string {
	for _, m := range s.Methods {
		if m.IsDefault {
			return m.ID
		}
	}
	return ""
}

// isDisplayable reports whether a method is a user-manageable panel row: a plain
// API-key method (entered by the user) or an interactive OAuth login. Bearer
// API-key methods are implicit env fallbacks and are not shown.
func isDisplayable(m AuthMethod) bool {
	switch m.Kind {
	case OAuthMintKey, OAuthToken:
		return true
	case APIKeyAuth:
		return m.HeaderStyle == APIKeyHeader
	}
	return false
}

// methodLabel returns the display label for a method, defaulting by kind.
func methodLabel(m AuthMethod) string {
	if m.Label != "" {
		return m.Label
	}
	if isOAuthMethod(m) {
		return "OAuth"
	}
	return "API Key"
}

// methodStored reports whether a credential is available for a method and, for
// API-key methods, the first 10 chars of the provider-backed key for display.
func methodStored(m AuthMethod) (stored bool, prefix string) {
	if isOAuthMethod(m) {
		if m.LoginID == "" {
			return false, ""
		}
		return auth.DefaultStorage().HasLogin(m.LoginID), ""
	}
	if m.EnvVar == "" && m.Keyring == "" {
		return false, ""
	}
	k, _ := resolveKey(m.EnvVar, m.Keyring)
	if k == "" {
		return false, ""
	}
	if len(k) > 10 {
		return true, k[:10]
	}
	return true, k
}

// GetProviderAuthStatus reports the per-method stored-credential state and the
// effective default method for a provider. When no preference is stored, the
// default is the first method with a stored credential, else the first method.
func GetProviderAuthStatus(provider string) ProviderAuthStatus {
	var st ProviderAuthStatus
	for _, m := range AuthMethodsFor(provider) {
		if !isDisplayable(m) {
			continue
		}
		stored, prefix := methodStored(m)
		ms := MethodStatus{
			ID:              m.ID(),
			Label:           methodLabel(m),
			OAuth:           isOAuthMethod(m),
			Stored:          stored,
			Prefix:          prefix,
			RequiresBaseURL: m.RequiresBaseURL,
		}
		if m.RequiresBaseURL && stored {
			ms.BaseURL = resolveMethodBaseURL(m)
		}
		st.Methods = append(st.Methods, ms)
	}
	def := effectiveDefaultMethod(st.Methods, ProviderAuthDefault(provider))
	for i := range st.Methods {
		st.Methods[i].IsDefault = st.Methods[i].ID == def
	}
	return st
}

// effectiveDefaultMethod returns the ID of the default method given the stored
// preference. An explicit preference wins when it names a method with a stored
// credential; legacy kind-level preferences ("api_key"/"oauth") promote the
// first stored method of that kind; otherwise the first method with a stored
// credential leads, falling back to the first method.
func effectiveDefaultMethod(methods []MethodStatus, pref string) string {
	if len(methods) == 0 {
		return ""
	}
	if pref != "" {
		for _, m := range methods {
			if m.ID == pref && m.Stored {
				return m.ID
			}
		}
		if pref == AuthDefaultAPIKey || pref == AuthDefaultOAuth {
			preferOAuth := pref == AuthDefaultOAuth
			for _, m := range methods {
				if m.OAuth == preferOAuth && m.Stored {
					return m.ID
				}
			}
		}
	}
	for _, m := range methods {
		if m.Stored {
			return m.ID
		}
	}
	return methods[0].ID
}

// ListStoredProviderKeys returns the stored key info for all known providers.
// The Prefix field holds the first 10 chars of the stored key (empty if not stored).
func ListStoredProviderKeys() []ProviderKey {
	providers := KnownProviders()
	result := make([]ProviderKey, 0, len(providers))
	for _, p := range providers {
		pk := ProviderKey{Provider: p}
		if k, err := defaultStore().Get(providerKeyringUser(p)); err == nil && k != "" {
			if len(k) > 10 {
				pk.Prefix = k[:10]
			} else {
				pk.Prefix = k
			}
		}
		result = append(result, pk)
	}
	return result
}
