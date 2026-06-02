// Package auth ports pi's OAuth login + credential system to Go.
//
// It implements the OAuth flows for the providers pi supports — Anthropic
// (Claude Pro/Max), GitHub Copilot, and OpenAI Codex (ChatGPT) — plus the
// shared machinery they rely on: PKCE generation, RFC 8628 device-code
// polling, a local callback HTTP server, a provider registry, and credential
// storage with automatic token refresh.
//
// Unlike pi, which persists credentials to a plaintext auth.json, vix stores
// OAuth credentials in the OS keychain (see storage.go) to match the rest of
// the codebase's secret handling.
package auth

import (
	"context"
	"encoding/json"
)

// Credentials holds the OAuth tokens for a provider.
//
// It mirrors pi's OAuthCredentials: a flat JSON object with the well-known
// access/refresh/expires fields plus arbitrary provider-specific extras
// (e.g. accountId for OpenAI Codex, enterpriseUrl for GitHub Copilot). The
// extras round-trip through storage via custom JSON (un)marshalling so the
// stored object stays flat, exactly like pi.
type Credentials struct {
	// Access is the token used to authenticate API requests.
	Access string
	// Refresh is the long-lived token used to obtain a new Access token.
	Refresh string
	// Expires is the absolute access-token expiry in Unix milliseconds. This
	// matches pi's Date.now()-based `expires`; a refresh is due once
	// nowMillis() >= Expires.
	Expires int64
	// Extra carries additional provider-specific fields that must survive a
	// storage round-trip. Keys are stored at the top level of the JSON object.
	Extra map[string]any
}

// MarshalJSON flattens Extra into the same object as access/refresh/expires so
// the on-disk/keychain representation matches pi's flat credential shape.
func (c Credentials) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(c.Extra)+3)
	for k, v := range c.Extra {
		m[k] = v
	}
	m["access"] = c.Access
	m["refresh"] = c.Refresh
	m["expires"] = c.Expires
	return json.Marshal(m)
}

// UnmarshalJSON reads the flat credential object, pulling the well-known
// fields out and keeping everything else in Extra.
func (c *Credentials) UnmarshalJSON(data []byte) error {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	c.Access, _ = m["access"].(string)
	c.Refresh, _ = m["refresh"].(string)
	switch e := m["expires"].(type) {
	case float64:
		c.Expires = int64(e)
	case json.Number:
		c.Expires, _ = e.Int64()
	}
	delete(m, "access")
	delete(m, "refresh")
	delete(m, "expires")
	if len(m) > 0 {
		c.Extra = m
	}
	return nil
}

// StringExtra returns the string value of an extra field, or "" if absent or
// not a string.
func (c Credentials) StringExtra(key string) string {
	if c.Extra == nil {
		return ""
	}
	s, _ := c.Extra[key].(string)
	return s
}

// Expired reports whether the access token is at or past its expiry.
func (c Credentials) Expired() bool {
	return nowMillis() >= c.Expires
}

// AuthInfo is passed to the OnAuth callback when a browser-based flow starts.
type AuthInfo struct {
	URL          string
	Instructions string
}

// DeviceCodeInfo is passed to the OnDeviceCode callback for device-code flows.
type DeviceCodeInfo struct {
	UserCode         string
	VerificationURI  string
	IntervalSeconds  int
	ExpiresInSeconds int
}

// Prompt asks the user for a single line of free-form input.
type Prompt struct {
	Message     string
	Placeholder string
	AllowEmpty  bool
}

// SelectOption is a single choice in a SelectPrompt.
type SelectOption struct {
	ID    string
	Label string
}

// SelectPrompt asks the user to pick one of several options.
type SelectPrompt struct {
	Message string
	Options []SelectOption
}

// LoginCallbacks is the set of UI hooks a login flow drives. It mirrors pi's
// OAuthLoginCallbacks. Optional hooks may be nil; the flows degrade
// gracefully (e.g. falling back to OnPrompt when OnManualCodeInput is nil).
//
// Cancellation is carried by the context.Context passed to Login rather than
// pi's AbortSignal.
type LoginCallbacks struct {
	// OnAuth is invoked with an authorization URL the user should open.
	OnAuth func(AuthInfo)
	// OnDeviceCode is invoked with the user code and verification URI.
	OnDeviceCode func(DeviceCodeInfo)
	// OnPrompt asks the user for input and returns the entered string.
	OnPrompt func(Prompt) (string, error)
	// OnProgress reports human-readable progress messages. May be nil.
	OnProgress func(string)
	// OnManualCodeInput, when non-nil, returns a user-pasted code/URL and is
	// raced against the local callback server.
	OnManualCodeInput func() (string, error)
	// OnSelect shows a chooser and returns the selected option id, or "" if
	// the user cancelled.
	OnSelect func(SelectPrompt) (string, error)
}

func (c LoginCallbacks) progress(msg string) {
	if c.OnProgress != nil {
		c.OnProgress(msg)
	}
}

// Provider is the interface every OAuth provider implements. It mirrors pi's
// OAuthProviderInterface.
type Provider interface {
	// ID is the stable identifier used as the storage key and registry key.
	ID() string
	// Name is a human-readable label.
	Name() string
	// UsesCallbackServer reports whether Login spins up a local HTTP server to
	// receive the OAuth redirect (and therefore supports manual code paste).
	UsesCallbackServer() bool
	// Login runs the interactive flow and returns credentials to persist.
	Login(ctx context.Context, cb LoginCallbacks) (Credentials, error)
	// RefreshToken exchanges a refresh token for fresh credentials.
	RefreshToken(ctx context.Context, creds Credentials) (Credentials, error)
	// APIKey returns the request credential (access token) for the provider.
	APIKey(creds Credentials) string
}
