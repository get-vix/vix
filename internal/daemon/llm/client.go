package llm

import (
	"context"
	"time"

	"github.com/get-vix/vix/internal/config"
)

// Client is the provider-neutral LLM interface. One Client is bound to a
// single (provider, model, credential, effort, maxTokens, pluginCfg) tuple
// and is safe for concurrent calls (the underlying SDKs handle request
// locking themselves).
type Client interface {
	// StreamMessage runs a streaming request with default options.
	StreamMessage(
		ctx context.Context,
		system []SystemBlock,
		messages []MessageParam,
		tools []ToolParam,
		onDelta func(string),
		onThinkingDelta func(string),
	) (*Message, time.Duration, error)

	// StreamMessageWith runs a streaming request honoring per-call
	// overrides from opts (currently just EffortOverride).
	StreamMessageWith(
		ctx context.Context,
		system []SystemBlock,
		messages []MessageParam,
		tools []ToolParam,
		onDelta func(string),
		onThinkingDelta func(string),
		opts StreamOpts,
	) (*Message, time.Duration, error)

	// Provider identifies which upstream this client talks to.
	Provider() ProviderID

	// Model returns the bare model name (no provider prefix).
	Model() string

	// Credential returns the credential this client was built with.
	Credential() config.Credential

	// MaxTokens returns the per-call output token cap configured on this
	// client. Zero means "use the default" (32768).
	MaxTokens() int64

	// Effort returns the reasoning effort configured at construction time.
	Effort() string
}

// StreamOpts carries per-call overrides for StreamMessageWith. The zero
// value preserves the instance-level defaults.
type StreamOpts struct {
	// EffortOverride, when non-nil, replaces Client.Effort() for this call
	// only. Empty string disables reasoning entirely. Used by the retry
	// loops to force a non-thinking response on the final attempt after
	// repeated thinking stalls.
	EffortOverride *string
}
