package daemon

import (
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

// PluginSource is the daemon-side alias for llm.PluginSource — the callback
// NewFromModel invokes at client-construction time to obtain the plugin
// config for a specific provider/model/credential.
type PluginSource = llm.PluginSource

// ThinkingStallError is the daemon-side alias for llm.ThinkingStallError.
type ThinkingStallError = llm.ThinkingStallError

// ErrStreamIdleTimeout / ErrThinkingStall — re-exported from llm so retry
// loops can `errors.Is(err, ErrThinkingStall)` without importing llm.
var (
	ErrStreamIdleTimeout = llm.ErrStreamIdleTimeout
	ErrThinkingStall     = llm.ErrThinkingStall
)
