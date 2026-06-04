package daemon

import (
	"log"
	"strings"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/llm"
)

// LLM is the daemon-side alias for llm.Client. All callers use this type;
// the underlying adapter is provider-dependent (Anthropic, OpenAI, ...).
type LLM = llm.Client

// StreamOpts is the daemon-side alias for llm.StreamOpts.
type StreamOpts = llm.StreamOpts

// PluginConfig is the daemon-side alias for llm.PluginConfig. Kept as a
// type alias so the existing plugin loader code (which produces this type)
// works unchanged.
type PluginConfig = llm.PluginConfig

// ThinkingStallError is the daemon-side alias for llm.ThinkingStallError.
type ThinkingStallError = llm.ThinkingStallError

// ErrStreamIdleTimeout / ErrThinkingStall — re-exported from llm so retry
// loops can `errors.Is(err, ErrThinkingStall)` without importing llm.
var (
	ErrStreamIdleTimeout = llm.ErrStreamIdleTimeout
	ErrThinkingStall     = llm.ErrThinkingStall
)

// NewLLM constructs an Anthropic LLM client. This is now only used as a
// fallback when llm.NewFromModel cannot resolve credentials for the target
// provider (e.g. no OPENAI_API_KEY). The caller-supplied credential is used
// directly — no provider-based resolution is performed here.
func NewLLM(cred config.Credential, model, effort string, maxTokens int64, pluginCfg PluginConfig) LLM {
	client, err := llm.NewAnthropic(llm.Config{
		Credential: cred,
		Model:      model,
		Effort:     effort,
		MaxTokens:  maxTokens,
		PluginCfg:  pluginCfg,
	})
	if err != nil {
		// NewAnthropic currently has no failure path (matches the old
		// infallible NewLLM signature). Log defensively and return nil so
		// callers see a clear panic at first use rather than silently
		// continuing with a half-initialized client.
		log.Printf("[llm] NewAnthropic failed: %v", err)
		return nil
	}
	return client
}

// defaultSessionEffort returns the default effort for an interactive
// session with the given model. Anthropic (claude-*) defaults to
// "adaptive"; everything else returns "" until the prefix-aware factory
// is wired into the session startup path.
func defaultSessionEffort(model string) string {
	if strings.HasPrefix(model, "claude-") {
		return "adaptive"
	}
	return ""
}
