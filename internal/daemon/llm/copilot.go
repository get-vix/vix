package llm

import (
	"context"
	"log"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"

	"github.com/kirby88/vix/internal/auth"
	"github.com/kirby88/vix/internal/config"
)

// copilotClient is the GitHub Copilot adapter. Copilot speaks the OpenAI
// Chat Completions wire format, so this mirrors the OpenRouter adapter, with
// two differences: the base URL is derived from the proxy endpoint embedded in
// the Copilot token, and Copilot's editor identification headers are sent.
type copilotClient struct {
	sdk          openai.Client
	model        string
	effort       string
	maxTokens    int64
	cred         config.Credential
	systemPrefix string
	streamIdle   time.Duration
}

// copilotHeaders are the editor-identification headers Copilot's API expects.
var copilotHeaders = map[string]string{
	"Copilot-Integration-Id": "vscode-chat",
	"Editor-Version":         "vscode/1.107.0",
	"Editor-Plugin-Version":  "copilot-chat/0.35.0",
	"User-Agent":             "GitHubCopilotChat/0.35.0",
}

// NewCopilot constructs the GitHub Copilot adapter.
func NewCopilot(cfg Config) (Client, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = auth.GitHubCopilotBaseURL(cfg.Credential.Value, "")
	}

	opts := []option.RequestOption{
		option.WithMaxRetries(0),
		option.WithAPIKey(cfg.Credential.Value), // sent as Authorization: Bearer
		option.WithBaseURL(baseURL),
	}
	for k, v := range copilotHeaders {
		opts = append(opts, option.WithHeader(k, v))
	}

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

	return &copilotClient{
		sdk:          sdk,
		model:        cfg.Model,
		effort:       cfg.Effort,
		maxTokens:    cfg.MaxTokens,
		cred:         cfg.Credential,
		systemPrefix: cfg.PluginCfg.SystemPrefix,
		streamIdle:   idle,
	}, nil
}

func (o *copilotClient) Provider() ProviderID          { return ProviderCopilot }
func (o *copilotClient) Model() string                 { return o.model }
func (o *copilotClient) Credential() config.Credential { return o.cred }
func (o *copilotClient) MaxTokens() int64              { return o.maxTokens }
func (o *copilotClient) Effort() string                { return o.effort }

func (o *copilotClient) StreamMessage(
	ctx context.Context,
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	onDelta func(string),
	onThinkingDelta func(string),
) (*Message, time.Duration, error) {
	return o.StreamMessageWith(ctx, system, messages, tools, onDelta, onThinkingDelta, StreamOpts{})
}

func (o *copilotClient) StreamMessageWith(
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

	reqID := RequestIDFromContext(ctx)
	if reqID == "" {
		reqID = NewRequestID()
		ctx = WithRequestID(ctx, reqID)
	}
	log.Printf("[llm req=%s] stream_start provider=github-copilot model=%s max_tokens=%d messages=%d tools=%d effort=%q",
		reqID, o.model, maxTokens, len(messages), len(tools), effort)

	adapter := &chatCompletionsAdapter{
		provider:          ProviderCopilot,
		sdk:               o.sdk,
		model:             o.model,
		effort:            effort,
		maxTokens:         maxTokens,
		streamIdleTimeout: o.streamIdle,
	}
	msg, err := streamChatCompletion(ctx, adapter, params, nil, onDelta)
	if err != nil {
		return nil, 0, err
	}
	return msg, time.Since(t0), nil
}

var _ Client = (*copilotClient)(nil)
