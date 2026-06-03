package llm

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"

	"github.com/get-vix/vix/internal/config"
)

// miniMaxClient is the MiniMax adapter, using the OpenAI-compatible Chat
// Completions endpoint plus a GroupId query-param middleware.
type miniMaxClient struct {
	sdk          openai.Client
	model        string
	effort       string
	maxTokens    int64
	cred         config.Credential
	systemPrefix string
	streamIdle   time.Duration

	groupID string
}

const (
	miniMaxIntlBaseURL = "https://api.minimax.io/v1"
	miniMaxCNBaseURL   = "https://api.minimaxi.com/v1"
)

// NewMiniMax constructs the MiniMax adapter.
func NewMiniMax(cfg Config) (Client, error) {
	opts := []option.RequestOption{
		option.WithMaxRetries(0),
		option.WithAPIKey(cfg.Credential.Value),
	}

	baseURL := cfg.MiniMax.BaseURL
	if baseURL == "" {
		if cfg.MiniMax.Region == "cn" {
			baseURL = miniMaxCNBaseURL
		} else {
			baseURL = miniMaxIntlBaseURL
		}
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

	if cfg.MiniMax.GroupID == "" {
		log.Printf("[llm minimax] MINIMAX_GROUP_ID is unset; some workspaces require it")
	} else {
		opts = append(opts, option.WithMiddleware(miniMaxGroupIDMiddleware(cfg.MiniMax.GroupID)))
	}

	sdk := openai.NewClient(opts...)

	idle := cfg.StreamIdle
	if idle <= 0 {
		idle = EnvDuration("VIX_STREAM_IDLE_TIMEOUT", DefaultStreamIdleTimeout)
	}

	return &miniMaxClient{
		sdk:          sdk,
		model:        cfg.Model,
		effort:       cfg.Effort,
		maxTokens:    cfg.MaxTokens,
		cred:         cfg.Credential,
		systemPrefix: cfg.PluginCfg.SystemPrefix,
		streamIdle:   idle,
		groupID:      cfg.MiniMax.GroupID,
	}, nil
}

// miniMaxGroupIDMiddleware appends ?GroupId=<id> to every outgoing request.
func miniMaxGroupIDMiddleware(groupID string) option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		q := req.URL.Query()
		q.Set("GroupId", groupID)
		req.URL.RawQuery = q.Encode()
		return next(req)
	}
}

func (m *miniMaxClient) Provider() ProviderID          { return ProviderMiniMax }
func (m *miniMaxClient) Model() string                 { return m.model }
func (m *miniMaxClient) Credential() config.Credential { return m.cred }
func (m *miniMaxClient) MaxTokens() int64              { return m.maxTokens }
func (m *miniMaxClient) Effort() string                { return m.effort }

func (m *miniMaxClient) StreamMessage(
	ctx context.Context,
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	onDelta func(string),
	onThinkingDelta func(string),
) (*Message, time.Duration, error) {
	return m.StreamMessageWith(ctx, system, messages, tools, onDelta, onThinkingDelta, StreamOpts{})
}

func (m *miniMaxClient) StreamMessageWith(
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

	// MiniMax-specific: reasoning_split (no level knob — boolean).
	var perCallOpts []option.RequestOption
	if effort != "" {
		perCallOpts = append(perCallOpts, option.WithJSONSet("reasoning_split", true))
	}

	reqID := RequestIDFromContext(ctx)
	if reqID == "" {
		reqID = NewRequestID()
		ctx = WithRequestID(ctx, reqID)
	}
	log.Printf("[llm req=%s] stream_start provider=minimax model=%s max_tokens=%d messages=%d tools=%d effort=%q",
		reqID, m.model, maxTokens, len(messages), len(tools), effort)

	adapter := &chatCompletionsAdapter{
		provider:          ProviderMiniMax,
		sdk:               m.sdk,
		model:             m.model,
		effort:            effort,
		maxTokens:         maxTokens,
		streamIdleTimeout: m.streamIdle,
	}
	msg, err := streamChatCompletion(ctx, adapter, params, perCallOpts, onDelta)
	if err != nil {
		return nil, 0, err
	}
	return msg, time.Since(t0), nil
}

var _ Client = (*miniMaxClient)(nil)
