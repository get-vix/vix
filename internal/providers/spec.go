// Package providers is the single, data-driven source of truth for vix's LLM
// providers and models. Everything that differs between providers that is pure
// data — model prefixes, base URLs, auth header styles, request headers/query
// params, credential env vars, OAuth endpoints, and the curated model
// catalogue — lives in an embedded providers.json (optionally overlaid by
// ~/.vix and ./.vix copies). Behavior (SSE parsing, request shaping, OAuth
// mechanics) stays compiled and is selected from JSON via closed enums:
// wire_format, the credential kinds, the auth flow, and the chat effort_style.
//
// The package has no dependencies on other internal packages so it can be
// imported by the inference layer (internal/daemon/llm), credential resolution
// (internal/config), the OAuth subsystem (internal/auth), and the TUI
// catalogue (internal/ui) without import cycles.
package providers

// SchemaVersion is the highest providers.json schema_version this binary
// understands. Files declaring a newer version are rejected at load.
const SchemaVersion = 1

// WireFormat selects the compiled inference adapter for a provider. The set is
// closed; an unknown value is a load-time validation error.
type WireFormat string

const (
	// WireMessages is the Anthropic Messages API (internal/daemon/llm/anthropic.go).
	WireMessages WireFormat = "messages"
	// WireResponses is the OpenAI Responses API (internal/daemon/llm/openai.go),
	// shared by OpenAI and the ChatGPT/Codex backend.
	WireResponses WireFormat = "responses"
	// WireChatCompletions is the OpenAI-compatible Chat Completions API
	// (internal/daemon/llm/chat_completions.go), shared by OpenRouter, MiniMax,
	// MiMo, and any other OpenAI-compatible vendor.
	WireChatCompletions WireFormat = "chat_completions"
)

// Effort policy names (default reasoning effort for a model spec).
const (
	// EffortAdaptive always defaults to "adaptive" (Anthropic, MiniMax).
	EffortAdaptive = "adaptive"
	// EffortOpenAIReasoning defaults to "medium" for reasoning-capable models
	// and "" otherwise (OpenAI, OpenRouter, MiMo).
	EffortOpenAIReasoning = "openai_reasoning"
)

// Chat-only effort styles: how a non-empty effort maps onto a Chat Completions
// request body. The set is closed.
const (
	// EffortStyleReasoningEffort sends the standard OpenAI reasoning_effort knob
	// (level), gated on reasoning-capable models (OpenRouter, MiMo).
	EffortStyleReasoningEffort = "reasoning_effort"
	// EffortStyleReasoningSplit sends reasoning_split=true whenever effort is
	// non-empty (MiniMax M2 — no level knob).
	EffortStyleReasoningSplit = "reasoning_split"
	// EffortStyleNone sends no reasoning field.
	EffortStyleNone = ""
)

// Auth scheme names: how a resolved credential value authenticates a request.
const (
	// AuthSchemeBearer sends Authorization: Bearer <value> (OpenAI-family).
	AuthSchemeBearer = "bearer"
	// AuthSchemeXAPIKey sends x-api-key: <value> (Anthropic SDK default).
	AuthSchemeXAPIKey = "x-api-key"
)

// Credential method kinds (mirror config.AuthKind without importing config).
const (
	// CredAPIKey is a static API key from env / keychain / .env.
	CredAPIKey = "api_key"
	// CredOAuthMintKey is an OAuth login that mints a normal (non-refreshing) API key.
	CredOAuthMintKey = "oauth_mint_key"
	// CredOAuthToken is an OAuth login yielding a refreshable access token.
	CredOAuthToken = "oauth_token"
)

// OAuth flow names: select the compiled auth.Provider implementation.
const (
	// FlowOAuthPKCEToken is PKCE auth-code → direct bearer access token (Anthropic).
	FlowOAuthPKCEToken = "oauth_pkce_token"
	// FlowOAuthCodex is the ChatGPT/Codex browser + device-code flow.
	FlowOAuthCodex = "oauth_codex"
	// FlowOAuthPKCEMint is PKCE → minted user API key (OpenRouter).
	FlowOAuthPKCEMint  = "oauth_pkce_mint"
	FlowOAuthGHCopilot = "oauth_gh_copilot"
)

// ExtraHeaders producer names: compiled functions that derive extra request
// headers from a resolved credential value. The set is closed.
const (
	// ProducerAnthropicOAuth adds the anthropic-beta header an OAuth bearer needs.
	ProducerAnthropicOAuth = "anthropic_oauth"
	// ProducerCodexOAuth adds the Codex backend headers (chatgpt-account-id, beta).
	ProducerCodexOAuth = "codex_oauth"
)

// File is the top-level providers.json document.
type File struct {
	SchemaVersion int            `json:"schema_version"`
	Providers     []ProviderSpec `json:"providers"`
	AuthLogins    []AuthLogin    `json:"auth_logins"`
}

// ProviderSpec is one transport provider: how vix reaches it, how it
// authenticates, and which models it exposes in the picker.
type ProviderSpec struct {
	ID           string             `json:"id"`
	DisplayName  string             `json:"display_name"`
	ModelPrefix  string             `json:"model_prefix"` // without trailing slash, e.g. "anthropic"
	WireFormat   WireFormat         `json:"wire_format"`
	EffortPolicy string             `json:"effort_policy"`
	Inference    InferenceSpec      `json:"inference"`
	Credential   []CredentialMethod `json:"credential_methods"`
	Models       []ModelSpec        `json:"models"`
}

// Prefix returns the model-spec prefix including the trailing slash.
func (p ProviderSpec) Prefix() string { return p.ModelPrefix + "/" }

// InferenceSpec holds the data needed to build a wire client. String values may
// contain ${env:VAR} / ${env:VAR:-default} interpolation, resolved when the
// inference layer constructs a client (see Resolve).
type InferenceSpec struct {
	BaseURL     string            `json:"base_url"`
	AuthScheme  string            `json:"auth_scheme"`  // bearer | x-api-key
	AuthHeader  string            `json:"auth_header"`  // for non-standard raw schemes; usually empty
	Headers     map[string]string `json:"headers"`      // static request headers
	QueryParams map[string]string `json:"query_params"` // appended to every request
	JSONSet     map[string]any    `json:"json_set"`     // injected into every request body
	EffortStyle string            `json:"effort_style"` // chat_completions only
}

// CredentialMethod is one ordered way to obtain a credential for a provider.
type CredentialMethod struct {
	Kind                 string `json:"kind"` // api_key | oauth_mint_key | oauth_token
	EnvVar               string `json:"env_var"`
	Keyring              string `json:"keyring"`
	LoginID              string `json:"login_id"`               // oauth_*: internal/auth login id
	BaseURL              string `json:"base_url"`               // endpoint override implied by this method
	HeaderStyle          string `json:"header_style"`           // "" | "bearer"
	ExtraHeadersProducer string `json:"extra_headers_producer"` // "" | anthropic_oauth | codex_oauth
}

// ModelSpec is one catalogue entry shown in the model picker.
type ModelSpec struct {
	Spec        string `json:"spec"` // full prefixed identifier, e.g. "anthropic/claude-opus-4-8"
	DisplayName string `json:"display_name"`
	// ContextWindow is the input context window in tokens. 0 (omitted) means
	// unknown: callers render it as "—" and disable auto-compaction.
	ContextWindow int64 `json:"context_window,omitempty"`
}

// AuthLogin describes one OAuth login flow, keyed by a login id that may differ
// from a transport provider id (e.g. "openai-codex"). A credential method
// references it by LoginID.
type AuthLogin struct {
	ID                   string            `json:"id"`
	Flow                 string            `json:"flow"`
	ClientID             string            `json:"client_id"`
	ClientIDB64          string            `json:"client_id_b64"`
	AuthorizeURL         string            `json:"authorize_url"`
	TokenURL             string            `json:"token_url"`
	KeysURL              string            `json:"keys_url"` // mint flow
	CallbackPort         int               `json:"callback_port"`
	CallbackPath         string            `json:"callback_path"`
	RedirectURI          string            `json:"redirect_uri"`
	Scope                string            `json:"scope"`
	Originator           string            `json:"originator"`
	ExtraAuthorizeParams map[string]string `json:"extra_authorize_params"`
	Device               *DeviceSpec       `json:"device"`
}

// DeviceSpec holds the RFC 8628 device-code endpoints for a flow that supports
// a headless path (Codex).
type DeviceSpec struct {
	UserCodeURL     string `json:"user_code_url"`
	TokenURL        string `json:"token_url"`
	VerificationURI string `json:"verification_uri"`
	RedirectURI     string `json:"redirect_uri"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
}
