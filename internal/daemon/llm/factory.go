package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/providers"
)

// ErrNoCredential is wrapped by NewFromModel when the selected model's provider
// has no resolvable credential. Callers use errors.Is to distinguish this
// (recoverable: the user just needs to add a key) from other construction
// failures such as an unknown provider prefix.
var ErrNoCredential = errors.New("no credential")

// Config is the shared input set every wire builder takes.
type Config struct {
	Credential config.Credential
	Model      string // bare model name (no provider prefix)
	Effort     string // "", "low", "medium", "high", "max", "adaptive"
	MaxTokens  int64  // 0 = use DefaultMaxTokens
	PluginCfg  PluginConfig
	HTTPClient *http.Client // optional override; nil = use NewPluginHTTPClient(PluginCfg)

	// BaseURL overrides the adapter's default API endpoint. Empty means
	// use the provider's default. Set from a credential's endpoint override
	// (e.g. the Codex backend) or by tests redirecting to httptest servers.
	BaseURL string

	StreamIdle    time.Duration // 0 = read from env or use DefaultStreamIdleTimeout
	ThinkingStall time.Duration // 0 = read from env or use DefaultThinkingStallTimeout
}

// ParseModel maps a vix-style model spec (with mandatory provider prefix) to
// (provider id, bare model name) via the providers registry — the first
// matching prefix wins. Bare unprefixed names error explicitly. Thin wrapper
// over providers.Default().ParseModel so existing callers keep the ProviderID
// return type.
func ParseModel(spec string) (ProviderID, string, error) {
	p, model, err := providers.Default().ParseModel(spec)
	if err != nil {
		return "", "", err
	}
	return ProviderID(p.ID), model, nil
}

// DefaultEffortFromSpec returns the default reasoning effort for the given model
// spec, per the provider's effort policy. Anthropic and MiniMax default to
// "adaptive"; the OpenAI-style providers default to "medium" for
// reasoning-capable models and "" otherwise.
func DefaultEffortFromSpec(spec string) string {
	p, model, err := providers.Default().ParseModel(spec)
	if err != nil {
		return ""
	}
	return p.DefaultEffort(model)
}

// Providers returns every supported provider id, in registry order.
func Providers() []ProviderID {
	ids := providers.Default().IDs()
	out := make([]ProviderID, len(ids))
	for i, id := range ids {
		out[i] = ProviderID(id)
	}
	return out
}

func isReasoningOpenAIModel(model string) bool {
	return providers.IsReasoningModel(model)
}

// Spec returns the full prefixed model spec for a Client (e.g.
// "anthropic/claude-opus-4-8"). Useful for cost calculation and logging
// where the bare Client.Model() alone is ambiguous across providers.
func Spec(c Client) string {
	return string(c.Provider()) + "/" + c.Model()
}

// NewFromModel parses a vix-style model spec, resolves the right credential via
// config.ResolveProviderCredentialFresh, and constructs the matching adapter by
// dispatching on the provider's wire_format. All endpoint/header/query data
// comes from the providers registry (providers.json).
func NewFromModel(spec string, plugin PluginConfig, effort string, maxTokens int64) (Client, error) {
	p, model, err := providers.Default().ParseModel(spec)
	if err != nil {
		return nil, err
	}
	// Resolve the credential, refreshing an expired stored OAuth token if
	// needed. The timeout bounds a possible token-refresh round-trip.
	refreshCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cred := config.ResolveProviderCredentialFresh(refreshCtx, p.ID)
	if cred.Value == "" {
		if env := config.PrimaryEnvVar(p.ID); env != "" {
			return nil, fmt.Errorf("no credential for %s (set %s): %w", p.ID, env, ErrNoCredential)
		}
		return nil, fmt.Errorf("no credential for %s: %w", p.ID, ErrNoCredential)
	}

	inf := p.Inference.Resolve()
	cfg := Config{
		Credential: cred,
		Model:      model,
		Effort:     effort,
		MaxTokens:  maxTokens,
		PluginCfg:  plugin,
	}

	// An auth method may carry an endpoint override (e.g. the Codex backend).
	if cred.BaseURL != "" {
		cfg.BaseURL = cred.BaseURL
	}

	switch p.WireFormat {
	case providers.WireMessages:
		return buildMessages(p, inf, cfg)
	case providers.WireResponses:
		return buildResponses(p, inf, cfg)
	case providers.WireChatCompletions:
		return buildChatCompletions(p, inf, cfg)
	}
	return nil, fmt.Errorf("unsupported wire_format %q for provider %s", p.WireFormat, p.ID)
}

// buildMessages constructs the Anthropic Messages adapter (the default) or the
// AWS Bedrock adapter when the provider is bedrock. The Anthropic SDK owns its
// base URL + path (/v1/messages); injecting the spec base_url would double the
// path, so we let the SDK default stand and honor only an explicit cfg.BaseURL
// override (credential endpoint or test server). Bedrock builds its endpoint
// URL dynamically from region + model at runtime.
func buildMessages(p providers.ProviderSpec, inf providers.InferenceSpec, cfg Config) (Client, error) {
	if p.ID == "bedrock" {
		if cfg.BaseURL == "" {
			cfg.BaseURL = inf.BaseURL
		}
		return NewBedrock(cfg)
	}
	return NewAnthropic(cfg)
}

// buildResponses constructs the OpenAI Responses adapter (also used by the
// ChatGPT/Codex backend, whose endpoint arrives via cred.BaseURL → cfg.BaseURL).
// Like Messages, the SDK owns its base path (/responses).
func buildResponses(_ providers.ProviderSpec, _ providers.InferenceSpec, cfg Config) (Client, error) {
	return NewOpenAI(cfg)
}

// buildChatCompletions constructs the generic OpenAI-compatible Chat
// Completions adapter, fully parameterized by the resolved inference spec
// (base URL, static headers, query params, json_set, effort_style). This single
// builder serves OpenRouter, MiniMax, MiMo, and any other compatible vendor.
func buildChatCompletions(p providers.ProviderSpec, inf providers.InferenceSpec, cfg Config) (Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = inf.BaseURL
	}
	return newChatCompletionsClient(cfg, chatParams{
		provider:    ProviderID(p.ID),
		headers:     inf.Headers,
		queryParams: inf.QueryParams,
		jsonSet:     inf.JSONSet,
		effortStyle: inf.EffortStyle,
	})
}
