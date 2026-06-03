package llm

import (
	"context"
	"log"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"

	"github.com/get-vix/vix/internal/config"
)

// mimoClient is the Xiaomi MiMo adapter. MiMo exposes an OpenAI-compatible
// Chat Completions endpoint, so this reuses the shared chat_completions
// engine. Unlike MiniMax it needs no GroupId middleware or regional split —
// just a base URL and a Bearer API key.
type mimoClient struct {
	sdk          openai.Client
	model        string
	effort       string
	maxTokens    int64
	cred         config.Credential
	systemPrefix string
	streamIdle   time.Duration
}

// mimoDefaultBaseURL is the first-party Xiaomi MiMo Open Platform endpoint.
const mimoDefaultBaseURL = "https://api.xiaomimimo.com/v1"

// NewMiMo constructs the MiMo adapter.
func NewMiMo(cfg Config) (Client, error) {
	opts := []option.RequestOption{
		option.WithMaxRetries(0),
		option.WithAPIKey(cfg.Credential.Value),
	}

	baseURL := cfg.MiMo.BaseURL
	if baseURL == "" {
		baseURL = mimoDefaultBaseURL
	}
	if cfg.BaseURL != "" {
		baseURL = cfg.BaseURL
	}
	opts = append(opts, option.WithBaseURL(baseURL))

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = NewPluginHTTPClient(cfg.PluginCfg)
	}
	opts = append(opts, option.WithHTTPClient(httpClient))

	sdk := openai.NewClient(opts...)

	idle := cfg.StreamIdle
	if idle <= 0 {
		idle = EnvDuration("VIX_STREAM_IDLE_TIMEOUT", DefaultStreamIdleTimeout)
	}

	return &mimoClient{
		sdk:          sdk,
		model:        cfg.Model,
		effort:       cfg.Effort,
		maxTokens:    cfg.MaxTokens,
		cred:         cfg.Credential,
		systemPrefix: cfg.PluginCfg.SystemPrefix,
		streamIdle:   idle,
	}, nil
}

func (m *mimoClient) Provider() ProviderID          { return ProviderMiMo }
func (m *mimoClient) Model() string                 { return m.model }
func (m *mimoClient) Credential() config.Credential { return m.cred }
func (m *mimoClient) MaxTokens() int64              { return m.maxTokens }
func (m *mimoClient) Effort() string                { return m.effort }

func (m *mimoClient) StreamMessage(
	ctx context.Context,
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	onDelta func(string),
	onThinkingDelta func(string),
) (*Message, time.Duration, error) {
	return m.StreamMessageWith(ctx, system, messages, tools, onDelta, onThinkingDelta, StreamOpts{})
}

func (m *mimoClient) StreamMessageWith(
	ctx context.Context,
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	onDelta func(string),
	onThinkingDelta func(string),
	opts StreamOpts,
) (*Message, time.Duration, error) {
	t0 := time.Now()

	if m.systemPrefix != "" {
		system = append([]SystemBlock{{Text: m.systemPrefix}}, system...)
	}

	maxTokens := m.maxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	effort := m.effort
	if opts.EffortOverride != nil {
		effort = *opts.EffortOverride
	}

	params := openai.ChatCompletionNewParams{
		Model:               m.model,
		Messages:            buildChatCompletionMessages(system, messages),
		Tools:               buildChatCompletionTools(tools),
		MaxCompletionTokens: param.NewOpt(maxTokens),
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		},
	}

	// MiMo accepts the standard OpenAI reasoning_effort knob on its
	// reasoning-capable models; addReasoningEffort no-ops otherwise.
	addReasoningEffort(&params, effort, m.model)

	reqID := RequestIDFromContext(ctx)
	if reqID == "" {
		reqID = NewRequestID()
		ctx = WithRequestID(ctx, reqID)
	}
	log.Printf("[llm req=%s] stream_start provider=mimo model=%s max_tokens=%d messages=%d tools=%d effort=%q",
		reqID, m.model, maxTokens, len(messages), len(tools), effort)

	adapter := &chatCompletionsAdapter{
		provider:          ProviderMiMo,
		sdk:               m.sdk,
		model:             m.model,
		effort:            effort,
		maxTokens:         maxTokens,
		streamIdleTimeout: m.streamIdle,
	}
	msg, err := streamChatCompletion(ctx, adapter, params, nil, onDelta)
	if err != nil {
		return nil, 0, err
	}
	return msg, time.Since(t0), nil
}

var _ Client = (*mimoClient)(nil)
