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

// chatCompletionsClient is the shared Client implementation for providers that
// speak the OpenAI Chat Completions wire format (OpenRouter, MiniMax, MiMo).
// Each provider's New* constructor configures the openai.Client (base URL,
// auth, extra headers/middleware) and an optional per-call tune hook; the
// neutral⇄OpenAI translation and streaming live here and in
// streamChatCompletion. Mirrors newResponsesClient for the Responses family.
type chatCompletionsClient struct {
	provider     ProviderID
	sdk          openai.Client
	model        string
	effort       string
	maxTokens    int64
	cred         config.Credential
	systemPrefix string
	streamIdle   time.Duration
	// tune, when set, shapes each streamed request: it may mutate params (e.g.
	// set the reasoning effort) and returns per-call request options (e.g.
	// JSON-injected body fields). It receives the call's effective effort.
	tune func(params *openai.ChatCompletionNewParams, effort string) []option.RequestOption
}

// chatCompletionsConfig carries the per-provider knobs newChatCompletionsClient
// needs. Only provider is required.
type chatCompletionsConfig struct {
	provider ProviderID
	// baseURL is the provider's default endpoint; Config.BaseURL overrides it.
	baseURL string
	// reqOpts are applied once at construction (extra headers, middleware, …).
	reqOpts []option.RequestOption
	// tune is the optional per-call request shaper (see chatCompletionsClient).
	tune func(params *openai.ChatCompletionNewParams, effort string) []option.RequestOption
}

// newChatCompletionsClient builds a Chat-Completions Client from cfg and the
// provider-specific cc.
func newChatCompletionsClient(cfg Config, cc chatCompletionsConfig) *chatCompletionsClient {
	opts := []option.RequestOption{
		option.WithMaxRetries(0),
		option.WithAPIKey(cfg.Credential.Value),
	}

	baseURL := cc.baseURL
	if cfg.BaseURL != "" {
		baseURL = cfg.BaseURL
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = NewPluginHTTPClient(cfg.PluginCfg)
	}
	opts = append(opts, option.WithHTTPClient(httpClient))
	opts = append(opts, cc.reqOpts...)

	idle := cfg.StreamIdle
	if idle <= 0 {
		idle = EnvDuration("VIX_STREAM_IDLE_TIMEOUT", DefaultStreamIdleTimeout)
	}

	return &chatCompletionsClient{
		provider:     cc.provider,
		sdk:          openai.NewClient(opts...),
		model:        cfg.Model,
		effort:       cfg.Effort,
		maxTokens:    cfg.MaxTokens,
		cred:         cfg.Credential,
		systemPrefix: cfg.PluginCfg.SystemPrefix,
		streamIdle:   idle,
		tune:         cc.tune,
	}
}

func (o *chatCompletionsClient) Provider() ProviderID          { return o.provider }
func (o *chatCompletionsClient) Model() string                 { return o.model }
func (o *chatCompletionsClient) Credential() config.Credential { return o.cred }
func (o *chatCompletionsClient) MaxTokens() int64              { return o.maxTokens }
func (o *chatCompletionsClient) Effort() string                { return o.effort }

func (o *chatCompletionsClient) StreamMessage(
	ctx context.Context,
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	onDelta func(string),
	onThinkingDelta func(string),
) (*Message, time.Duration, error) {
	return o.StreamMessageWith(ctx, system, messages, tools, onDelta, onThinkingDelta, StreamOpts{})
}

func (o *chatCompletionsClient) StreamMessageWith(
	ctx context.Context,
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	onDelta func(string),
	onThinkingDelta func(string),
	opts StreamOpts,
) (*Message, time.Duration, error) {
	t0 := time.Now()

	if o.systemPrefix != "" {
		system = append([]SystemBlock{{Text: o.systemPrefix}}, system...)
	}

	maxTokens := o.maxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	effort := o.effort
	if opts.EffortOverride != nil {
		effort = *opts.EffortOverride
	}

	params := openai.ChatCompletionNewParams{
		Model:               o.model,
		Messages:            buildChatCompletionMessages(system, messages),
		Tools:               buildChatCompletionTools(tools),
		MaxCompletionTokens: param.NewOpt(maxTokens),
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		},
	}

	var perCallOpts []option.RequestOption
	if o.tune != nil {
		perCallOpts = o.tune(&params, effort)
	}

	reqID := RequestIDFromContext(ctx)
	if reqID == "" {
		reqID = NewRequestID()
		ctx = WithRequestID(ctx, reqID)
	}
	log.Printf("[llm req=%s] stream_start provider=%s model=%s max_tokens=%d messages=%d tools=%d effort=%q",
		reqID, o.provider, o.model, maxTokens, len(messages), len(tools), effort)

	adapter := &chatCompletionsAdapter{
		provider:          o.provider,
		sdk:               o.sdk,
		model:             o.model,
		effort:            effort,
		maxTokens:         maxTokens,
		streamIdleTimeout: o.streamIdle,
	}
	msg, err := streamChatCompletion(ctx, adapter, params, perCallOpts, onDelta)
	if err != nil {
		return nil, 0, err
	}
	return msg, time.Since(t0), nil
}

var _ Client = (*chatCompletionsClient)(nil)
