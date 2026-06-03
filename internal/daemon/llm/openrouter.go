package llm

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"

	"github.com/get-vix/vix/internal/config"
)

// openRouterClient is the OpenRouter adapter (Chat Completions wire format).
type openRouterClient struct {
	sdk          openai.Client
	model        string
	effort       string
	maxTokens    int64
	cred         config.Credential
	systemPrefix string
	streamIdle   time.Duration

	httpReferer string
	xTitle      string
	routing     map[string]any
}

const openRouterDefaultBaseURL = "https://openrouter.ai/api/v1"

// NewOpenRouter constructs the OpenRouter adapter.
func NewOpenRouter(cfg Config) (Client, error) {
	opts := []option.RequestOption{
		option.WithMaxRetries(0),
		option.WithAPIKey(cfg.Credential.Value),
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = openRouterDefaultBaseURL
	}
	opts = append(opts, option.WithBaseURL(baseURL))

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = NewPluginHTTPClient(cfg.PluginCfg)
	}
	opts = append(opts, option.WithHTTPClient(httpClient))

	if cfg.OpenRouter.HTTPReferer != "" {
		opts = append(opts, option.WithHeader("HTTP-Referer", cfg.OpenRouter.HTTPReferer))
	}
	if cfg.OpenRouter.XTitle != "" {
		opts = append(opts, option.WithHeader("X-Title", cfg.OpenRouter.XTitle))
	}

	sdk := openai.NewClient(opts...)

	idle := cfg.StreamIdle
	if idle <= 0 {
		idle = EnvDuration("VIX_STREAM_IDLE_TIMEOUT", DefaultStreamIdleTimeout)
	}

	return &openRouterClient{
		sdk:          sdk,
		model:        cfg.Model,
		effort:       cfg.Effort,
		maxTokens:    cfg.MaxTokens,
		cred:         cfg.Credential,
		systemPrefix: cfg.PluginCfg.SystemPrefix,
		streamIdle:   idle,
		httpReferer:  cfg.OpenRouter.HTTPReferer,
		xTitle:       cfg.OpenRouter.XTitle,
		routing:      cfg.OpenRouter.Routing,
	}, nil
}

func (o *openRouterClient) Provider() ProviderID          { return ProviderOpenRouter }
func (o *openRouterClient) Model() string                 { return o.model }
func (o *openRouterClient) Credential() config.Credential { return o.cred }
func (o *openRouterClient) MaxTokens() int64              { return o.maxTokens }
func (o *openRouterClient) Effort() string                { return o.effort }

func (o *openRouterClient) StreamMessage(
	ctx context.Context,
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	onDelta func(string),
	onThinkingDelta func(string),
) (*Message, time.Duration, error) {
	return o.StreamMessageWith(ctx, system, messages, tools, onDelta, onThinkingDelta, StreamOpts{})
}

func (o *openRouterClient) StreamMessageWith(
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
	addReasoningEffort(&params, effort, o.model)

	// OpenRouter extras via JSON injection.
	var perCallOpts []option.RequestOption
	perCallOpts = append(perCallOpts, option.WithJSONSet("usage.include", true))
	if o.routing != nil {
		if routingJSON, err := json.Marshal(o.routing); err == nil {
			perCallOpts = append(perCallOpts, option.WithJSONSet("provider", json.RawMessage(routingJSON)))
		}
	}

	reqID := RequestIDFromContext(ctx)
	if reqID == "" {
		reqID = NewRequestID()
		ctx = WithRequestID(ctx, reqID)
	}
	log.Printf("[llm req=%s] stream_start provider=openrouter model=%s max_tokens=%d messages=%d tools=%d effort=%q",
		reqID, o.model, maxTokens, len(messages), len(tools), effort)

	// Use the shared streamChatCompletion helper.
	adapter := &chatCompletionsAdapter{
		provider:          ProviderOpenRouter,
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

var _ Client = (*openRouterClient)(nil)
