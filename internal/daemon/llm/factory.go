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

// ParseModel maps a vix-style model spec (with mandatory provider prefix)
// to (provider, bare model name).
//
// Routing rules — first match wins:
//
//	"openrouter/<rest>"  → (OpenRouter, <rest>)
//	"minimax/<m>"        → (MiniMax, <m>)
//	"mimo/<m>"           → (MiMo, <m>)
//	"openai/<m>"         → (OpenAI, <m>)
//	"anthropic/<m>"      → (Anthropic, <m>)
//	anything else        → error
//
// Bare unprefixed names (e.g. "claude-sonnet-4-6") error explicitly — the
// previous silent fallback could silently route to the wrong provider.
func ParseModel(spec string) (ProviderID, string, error) {
	switch {
	case strings.HasPrefix(spec, "openrouter/"):
		return ProviderOpenRouter, strings.TrimPrefix(spec, "openrouter/"), nil
	case strings.HasPrefix(spec, "minimax/"):
		return ProviderMiniMax, strings.TrimPrefix(spec, "minimax/"), nil
	case strings.HasPrefix(spec, "mimo/"):
		return ProviderMiMo, strings.TrimPrefix(spec, "mimo/"), nil
	case strings.HasPrefix(spec, "openai/"):
		return ProviderOpenAI, strings.TrimPrefix(spec, "openai/"), nil
	case strings.HasPrefix(spec, "anthropic/"):
		return ProviderAnthropic, strings.TrimPrefix(spec, "anthropic/"), nil
	case spec == "":
		return "", "", fmt.Errorf("model spec is empty")
	default:
		return "", "", fmt.Errorf("model spec %q must start with anthropic/, openai/, openrouter/, minimax/, or mimo/", spec)
	}
}

// DefaultEffortFromSpec returns the default reasoning effort for the given
// model spec. Anthropic and MiniMax default to "adaptive". OpenAI and
// OpenRouter default to "medium" when the model is reasoning-capable and
// to "" otherwise.
func DefaultEffortFromSpec(spec string) string {
	prov, model, err := ParseModel(spec)
	if err != nil {
		return ""
	}
	switch prov {
	case ProviderAnthropic:
		return "adaptive"
	case ProviderMiniMax:
		return "adaptive"
	case ProviderMiMo:
		// MiMo's reasoning-capable models accept the OpenAI reasoning_effort
		// knob; default to medium there and off otherwise.
		if isReasoningOpenAIModel(model) {
			return "medium"
		}
		return ""
	case ProviderOpenAI, ProviderOpenRouter:
		if isReasoningOpenAIModel(model) {
			return "medium"
		}
		return ""
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

// EnvVarFor returns the canonical credential env var name for a provider.
// Used for error messages when a required credential is missing.
func EnvVarFor(p ProviderID) string {
	switch p {
	case ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case ProviderOpenAI:
		return "OPENAI_API_KEY"
	case ProviderOpenRouter:
		return "OPENROUTER_API_KEY"
	case ProviderMiniMax:
		return "MINIMAX_API_KEY"
	case ProviderMiMo:
		return "MIMO_API_KEY"
	}
	return ""
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
	cred := config.ResolveProviderCredentialFresh(refreshCtx, prov.CredentialName(), prov == ProviderAnthropic)
	if cred.Value == "" {
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
	switch prov {
	case ProviderAnthropic:
		return NewAnthropic(cfg)
	case ProviderOpenAI:
		return NewOpenAI(cfg)
	case ProviderOpenRouter:
		return NewOpenRouter(cfg)
	case ProviderMiniMax:
		return NewMiniMax(cfg)
	case ProviderMiMo:
		return NewMiMo(cfg)
	}
	return nil, fmt.Errorf("unsupported provider: %s", prov)
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
