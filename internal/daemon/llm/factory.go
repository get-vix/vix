package llm

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kirby88/vix/internal/config"
)

// Config is the shared input set every adapter constructor takes.
type Config struct {
	Credential config.Credential
	Model      string // bare model name (no provider prefix)
	Effort     string // "", "low", "medium", "high", "max", "adaptive"
	MaxTokens  int64  // 0 = use DefaultMaxTokens
	PluginCfg  PluginConfig
	HTTPClient *http.Client // optional override; nil = use NewPluginHTTPClient(PluginCfg)

	// BaseURL overrides the adapter's default API endpoint. Empty means
	// use the provider's default. Primarily intended for tests redirecting
	// to httptest servers.
	BaseURL string

	// Per-provider options. Zero values are fine when the target provider
	// doesn't need them.
	OpenRouter OpenRouterOptions
	MiniMax    MiniMaxOptions
	MiMo       MiMoOptions

	StreamIdle    time.Duration // 0 = read from env or use DefaultStreamIdleTimeout
	ThinkingStall time.Duration // 0 = read from env or use DefaultThinkingStallTimeout
}

// OpenRouterOptions configures the OpenRouter adapter.
type OpenRouterOptions struct {
	// HTTPReferer is sent as the HTTP-Referer header for app attribution.
	// OpenRouter uses this for rankings and (optionally) free-credit
	// attribution.
	HTTPReferer string
	// XTitle is sent as the X-Title header — display name in OpenRouter
	// dashboards. Defaults to "vix" when empty.
	XTitle string
	// Routing, when non-empty, is sent as the `provider` block on each
	// request to control routing across upstream providers.
	Routing map[string]any
}

// MiniMaxOptions configures the MiniMax adapter.
type MiniMaxOptions struct {
	// BaseURL overrides the default region-derived base URL when non-empty.
	BaseURL string
	// Region selects the regional endpoint when BaseURL is unset.
	// "intl" → https://api.minimax.io/v1 (default).
	// "cn"   → https://api.minimaxi.com/v1.
	Region string
	// GroupID is sent as a ?GroupId query parameter on every request.
	// Required for some workspaces; empty is allowed but a startup warning
	// is logged.
	GroupID string
}

// MiMoOptions configures the Xiaomi MiMo adapter.
type MiMoOptions struct {
	// BaseURL overrides the default MiMo endpoint when non-empty.
	// Empty → https://api.xiaomimimo.com/v1.
	BaseURL string
}

// providerSpec is the single registration record for a model provider. Adding
// a provider means adding one entry to providerSpecs below: ParseModel,
// EnvVarFor, DefaultEffortFromSpec, NewFromModel, ListModels, UsesOAuth and the
// daemon's model-list set all derive from this table.
type providerSpec struct {
	id        ProviderID
	prefix    string // model-spec prefix, including the trailing slash
	envVar    string // credential env var; "" means OAuth-only (vix login)
	usesOAuth bool
	// effort returns the default reasoning effort for a bare model name.
	effort func(model string) string
	// newClient constructs the inference adapter.
	newClient func(cfg Config) (Client, error)
	// listModels fetches the provider's available models for cred.
	listModels func(ctx context.Context, cred config.Credential) ([]ModelListing, error)
}

// adaptiveEffort and openAIStyleEffort are the two default-effort policies.
func adaptiveEffort(string) string { return "adaptive" }
func openAIStyleEffort(model string) string {
	if isReasoningOpenAIModel(model) {
		return "medium"
	}
	return ""
}

// providerSpecs is the ordered registry of supported providers. Order is
// user-facing (model-list grouping and the ParseModel error hint).
var providerSpecs = []providerSpec{
	{id: ProviderAnthropic, prefix: "anthropic/", envVar: "ANTHROPIC_API_KEY", usesOAuth: true, effort: adaptiveEffort, newClient: NewAnthropic, listModels: listAnthropicCatalog},
	{id: ProviderOpenAI, prefix: "openai/", envVar: "OPENAI_API_KEY", effort: openAIStyleEffort, newClient: NewOpenAI, listModels: listOpenAICatalog},
	{id: ProviderCodex, prefix: "openai-codex/", usesOAuth: true, effort: openAIStyleEffort, newClient: NewCodex, listModels: listCodexCatalog},
	{id: ProviderOpenRouter, prefix: "openrouter/", envVar: "OPENROUTER_API_KEY", effort: openAIStyleEffort, newClient: NewOpenRouter, listModels: listOpenRouterCatalog},
	{id: ProviderMiniMax, prefix: "minimax/", envVar: "MINIMAX_API_KEY", effort: adaptiveEffort, newClient: NewMiniMax, listModels: listMiniMaxCatalog},
	{id: ProviderMiMo, prefix: "mimo/", envVar: "MIMO_API_KEY", effort: openAIStyleEffort, newClient: NewMiMo, listModels: listMiMoCatalog},
}

// providerSpecByID indexes providerSpecs for O(1) lookup.
var providerSpecByID = func() map[ProviderID]providerSpec {
	m := make(map[ProviderID]providerSpec, len(providerSpecs))
	for _, s := range providerSpecs {
		m[s.id] = s
	}
	return m
}()

// Providers returns every supported provider id, in registry order.
func Providers() []ProviderID {
	out := make([]ProviderID, len(providerSpecs))
	for i, s := range providerSpecs {
		out[i] = s.id
	}
	return out
}

// ParseModel maps a vix-style model spec (with mandatory provider prefix) to
// (provider, bare model name) via the providerSpecs registry — the first
// matching prefix wins. Bare unprefixed names (e.g. "claude-sonnet-4-6") error
// explicitly rather than silently routing to the wrong provider.
func ParseModel(spec string) (ProviderID, string, error) {
	if spec == "" {
		return "", "", fmt.Errorf("model spec is empty")
	}
	for _, s := range providerSpecs {
		if strings.HasPrefix(spec, s.prefix) {
			return s.id, strings.TrimPrefix(spec, s.prefix), nil
		}
	}
	return "", "", fmt.Errorf("model spec %q must start with one of: %s", spec, strings.Join(providerPrefixes(), ", "))
}

// providerPrefixes returns the registered model-spec prefixes, in order.
func providerPrefixes() []string {
	out := make([]string, len(providerSpecs))
	for i, s := range providerSpecs {
		out[i] = s.prefix
	}
	return out
}

// DefaultEffortFromSpec returns the default reasoning effort for the given
// model spec, per the provider's effort policy (see providerSpecs). Anthropic
// and MiniMax default to "adaptive"; the OpenAI-style providers default to
// "medium" for reasoning-capable models and "" otherwise.
func DefaultEffortFromSpec(spec string) string {
	prov, model, err := ParseModel(spec)
	if err != nil {
		return ""
	}
	if s, ok := providerSpecByID[prov]; ok && s.effort != nil {
		return s.effort(model)
	}
	return ""
}

func isReasoningOpenAIModel(model string) bool {
	m := strings.ToLower(model)
	// OpenRouter prefixes upstream models with "openai/" etc.
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "o1") ||
		strings.HasPrefix(m, "o3") ||
		strings.HasPrefix(m, "o4") ||
		strings.HasPrefix(m, "gpt-5") ||
		strings.Contains(m, "-thinking")
}

// Spec returns the full prefixed model spec for a Client (e.g.
// "anthropic/claude-opus-4-8"). Useful for cost calculation and logging
// where the bare Client.Model() alone is ambiguous across providers.
func Spec(c Client) string {
	return string(c.Provider()) + "/" + c.Model()
}

// EnvVarFor returns the canonical credential env var name for a provider, or
// "" for OAuth-only providers (and unknown ids). Used for error messages when
// a required credential is missing.
func EnvVarFor(p ProviderID) string {
	return providerSpecByID[p].envVar
}

// NewFromModel parses a vix-style model spec, resolves the right
// credential via config.ResolveProviderCredential, populates per-provider
// options from the environment, and constructs the matching adapter.
func NewFromModel(spec string, plugin PluginConfig, effort string, maxTokens int64) (Client, error) {
	prov, model, err := ParseModel(spec)
	if err != nil {
		return nil, err
	}
	// Resolve the credential, refreshing an expired stored OAuth token if
	// needed. The timeout bounds a possible token-refresh round-trip without
	// stalling LLM construction indefinitely.
	refreshCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cred := config.ResolveProviderCredentialFresh(refreshCtx, prov.CredentialName(), prov.UsesOAuth())
	if cred.Value == "" {
		if prov.UsesOAuth() && EnvVarFor(prov) == "" {
			return nil, fmt.Errorf("no credential for %s (run: vix login %s)", prov, prov.CredentialName())
		}
		return nil, fmt.Errorf("no credential for %s (set %s)", prov, EnvVarFor(prov))
	}
	cfg := Config{
		Credential: cred,
		Model:      model,
		Effort:     effort,
		MaxTokens:  maxTokens,
		PluginCfg:  plugin,
		OpenRouter: openRouterOptsFromEnv(),
		MiniMax:    miniMaxOptsFromEnv(),
		MiMo:       miMoOptsFromEnv(),
	}
	ps, ok := providerSpecByID[prov]
	if !ok || ps.newClient == nil {
		return nil, fmt.Errorf("unsupported provider: %s", prov)
	}
	return ps.newClient(cfg)
}

func openRouterOptsFromEnv() OpenRouterOptions {
	x := os.Getenv("OPENROUTER_X_TITLE")
	if x == "" {
		x = "vix"
	}
	return OpenRouterOptions{
		HTTPReferer: os.Getenv("OPENROUTER_HTTP_REFERER"),
		XTitle:      x,
	}
}

func miniMaxOptsFromEnv() MiniMaxOptions {
	region := os.Getenv("MINIMAX_REGION")
	if region != "cn" {
		region = "intl"
	}
	return MiniMaxOptions{
		BaseURL: os.Getenv("MINIMAX_BASE_URL"),
		Region:  region,
		GroupID: os.Getenv("MINIMAX_GROUP_ID"),
	}
}

func miMoOptsFromEnv() MiMoOptions {
	return MiMoOptions{
		BaseURL: os.Getenv("MIMO_BASE_URL"),
	}
}
