package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/get-vix/vix/internal/config"
)

// anthropicClient is the Anthropic Messages API adapter. One instance is
// bound to (cred, model, effort, maxTokens, pluginCfg).
type anthropicClient struct {
	sdk                  anthropic.Client
	model                string
	effort               string // "adaptive" | "low" | "medium" | "high" | "max" | ""
	maxTokens            int64  // 0 means use DefaultMaxTokens
	cred                 config.Credential
	systemPrefix         string
	streamIdleTimeout    time.Duration
	thinkingStallTimeout time.Duration
}

// NewAnthropic constructs an Anthropic adapter from cfg.
func NewAnthropic(cfg Config) (Client, error) {
	// Disable the SDK's built-in retry loop. Vix runs its own retry at a
	// higher level (session.streamWithRetry / workflow retry), and the SDK's
	// retry uses an uninterruptible time.Sleep which delays cancellation.
	allOpts := []option.RequestOption{option.WithMaxRetries(0)}
	allOpts = append(allOpts, cfg.Credential.RequestOptions()...)

	// Plugin headers + lifecycle logging are all applied through a single
	// wrapped HTTP client. Composes set/strip → logging → shared transport.
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = NewPluginHTTPClient(cfg.PluginCfg)
	}
	allOpts = append(allOpts, option.WithHTTPClient(httpClient))

	if cfg.BaseURL != "" {
		allOpts = append(allOpts, option.WithBaseURL(cfg.BaseURL))
	}

	sdk := anthropic.NewClient(allOpts...)

	idle := cfg.StreamIdle
	if idle <= 0 {
		idle = EnvDuration("VIX_STREAM_IDLE_TIMEOUT", DefaultStreamIdleTimeout)
	}
	stall := cfg.ThinkingStall
	if stall <= 0 {
		stall = EnvDuration("VIX_STREAM_THINKING_STALL_TIMEOUT", DefaultThinkingStallTimeout)
	}

	return &anthropicClient{
		sdk:                  sdk,
		model:                cfg.Model,
		effort:               cfg.Effort,
		maxTokens:            cfg.MaxTokens,
		cred:                 cfg.Credential,
		systemPrefix:         cfg.PluginCfg.SystemPrefix,
		streamIdleTimeout:    idle,
		thinkingStallTimeout: stall,
	}, nil
}

func (a *anthropicClient) Provider() ProviderID          { return ProviderAnthropic }
func (a *anthropicClient) Model() string                 { return a.model }
func (a *anthropicClient) Credential() config.Credential { return a.cred }
func (a *anthropicClient) MaxTokens() int64              { return a.maxTokens }
func (a *anthropicClient) Effort() string                { return a.effort }

func (a *anthropicClient) StreamMessage(
	ctx context.Context,
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	onDelta func(string),
	onThinkingDelta func(string),
) (*Message, time.Duration, error) {
	return a.StreamMessageWith(ctx, system, messages, tools, onDelta, onThinkingDelta, StreamOpts{})
}

func (a *anthropicClient) StreamMessageWith(
	ctx context.Context,
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	onDelta func(string),
	onThinkingDelta func(string),
	opts StreamOpts,
) (*Message, time.Duration, error) {
	t0 := time.Now()

	// Prepend plugin system prefix as the first system block, if set.
	if a.systemPrefix != "" {
		prefix := SystemBlock{Text: a.systemPrefix}
		system = append([]SystemBlock{prefix}, system...)
	}

	maxTokens := a.maxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	// Translate neutral → anthropic for the request.
	anthropicSystem := toAnthropicSystem(system)
	anthropicMessages, err := toAnthropicMessages(messages)
	if err != nil {
		return nil, 0, fmt.Errorf("anthropic: build messages: %w", err)
	}
	anthropicTools := toAnthropicTools(tools)

	params := anthropic.MessageNewParams{
		Model:        anthropic.Model(a.model),
		MaxTokens:    maxTokens,
		System:       anthropicSystem,
		Messages:     anthropicMessages,
		Tools:        anthropicTools,
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
	}

	effort := a.effort
	if opts.EffortOverride != nil {
		effort = *opts.EffortOverride
	}
	// Claude Haiku models don't support adaptive/extended thinking; the API
	// rejects such requests with "Adaptive thinking not allowed for this
	// model". The provider's default effort policy is "adaptive" for every
	// Anthropic model, so force it off here for Haiku.
	if !anthropicModelSupportsThinking(a.model) {
		effort = ""
	}
	applyEffort(&params, effort)

	// Correlation ID for this attempt.
	reqID := RequestIDFromContext(ctx)
	if reqID == "" {
		reqID = NewRequestID()
		ctx = WithRequestID(ctx, reqID)
	}
	log.Printf("[llm req=%s] stream_start provider=anthropic model=%s max_tokens=%d messages=%d tools=%d effort=%q verbose=%v",
		reqID, a.model, maxTokens, len(messages), len(tools), effort, StreamDebugVerbose())

	// The Go SDK (v1.26.0) doesn't expose thinking.display as a typed field;
	// when adaptive thinking is enabled inject display="summarized" via JSON.
	// Opus 4.7 silently changed the default to "omitted" which returns
	// thinking blocks with empty text; this restores visible thinking.
	var perCallOpts []option.RequestOption
	if effort != "" {
		perCallOpts = append(perCallOpts, option.WithJSONSet("thinking.display", "summarized"))
	}

	stream := a.sdk.Messages.NewStreaming(ctx, params, perCallOpts...)

	msg, err := a.runStream(ctx, stream, t0, reqID, onDelta, onThinkingDelta)
	if err != nil {
		return nil, 0, err
	}
	return msg, time.Since(t0), nil
}

// runStream drives the SSE event loop with an idle-timeout watchdog plus
// an optional thinking-stall watchdog (active only while inside a thinking
// content block). Returns the accumulated neutral Message.
func (a *anthropicClient) runStream(
	ctx context.Context,
	stream *ssestream.Stream[anthropic.MessageStreamEventUnion],
	t0 time.Time,
	reqID string,
	onDelta func(string),
	onThinkingDelta func(string),
) (*Message, error) {
	idleTimeout := a.streamIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = DefaultStreamIdleTimeout
	}
	stallTimeout := a.thinkingStallTimeout
	if stallTimeout <= 0 {
		stallTimeout = DefaultThinkingStallTimeout
	}

	var (
		eventCount         int
		firstEventAt       time.Time
		lastEventAt        time.Time
		inThinking         bool
		thinkingStartedAt  time.Time
		thinkingSummaryBuf strings.Builder
		stallCh            <-chan time.Time
	)

	type streamEvent struct {
		event anthropic.MessageStreamEventUnion
		done  bool
		err   error
	}
	done := make(chan struct{})
	defer close(done)
	events := make(chan streamEvent, 1)
	go func() {
		defer close(events)
		for stream.Next() {
			select {
			case events <- streamEvent{event: stream.Current()}:
			case <-done:
				return
			}
		}
		select {
		case events <- streamEvent{done: true, err: stream.Err()}:
		case <-done:
		}
	}()

	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

	acc := anthropic.Message{}
loop:
	for {
		select {
		case ev, ok := <-events:
			if !ok || ev.done {
				if ev.err != nil {
					log.Printf("[llm req=%s] SSE stream error after %d events: %v", reqID, eventCount, ev.err)
					return nil, ev.err
				}
				break loop
			}
			idleTimer.Reset(idleTimeout)
			eventCount++
			lastEventAt = time.Now()
			if firstEventAt.IsZero() {
				firstEventAt = lastEventAt
				log.Printf("[llm req=%s] first_sse_event=%s type=%T",
					reqID, DurStr(t0, firstEventAt), ev.event.AsAny())
			}
			if err := acc.Accumulate(ev.event); err != nil {
				return nil, err
			}
			switch e := ev.event.AsAny().(type) {
			case anthropic.ContentBlockStartEvent:
				if e.ContentBlock.Type == "thinking" {
					inThinking = true
					thinkingStartedAt = time.Now()
					thinkingSummaryBuf.Reset()
					stallCh = time.After(stallTimeout)
				}
			case anthropic.ContentBlockStopEvent:
				if inThinking {
					inThinking = false
					stallCh = nil
				}
			case anthropic.ContentBlockDeltaEvent:
				switch d := e.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					if onDelta != nil {
						onDelta(d.Text)
					}
				case anthropic.ThinkingDelta:
					thinkingSummaryBuf.WriteString(d.Thinking)
					if onThinkingDelta != nil {
						onThinkingDelta(d.Thinking)
					}
				}
			}
		case <-stallCh:
			elapsed := time.Since(thinkingStartedAt)
			summary := thinkingSummaryBuf.String()
			log.Printf("[llm req=%s] thinking_stall after=%s summary=%d chars",
				reqID, elapsed, len(summary))
			stream.Close()
			return nil, &ThinkingStallError{Elapsed: elapsed, Summary: summary}
		case <-idleTimer.C:
			sinceLast := "never"
			if !lastEventAt.IsZero() {
				sinceLast = time.Since(lastEventAt).String()
			}
			firstSeen := "never"
			if !firstEventAt.IsZero() {
				firstSeen = DurStr(t0, firstEventAt)
			}
			log.Printf("[llm req=%s] idle_timeout after=%s events_seen=%d first_event=%s since_last_event=%s",
				reqID, idleTimeout, eventCount, firstSeen, sinceLast)
			stream.Close()
			log.Printf("[llm req=%s] stream.Err after Close: %v", reqID, stream.Err())
			return nil, fmt.Errorf("%w: no SSE events for %s", ErrStreamIdleTimeout, idleTimeout)
		case <-ctx.Done():
			stream.Close()
			return nil, ctx.Err()
		}
	}

	return fromAnthropicMessage(&acc), nil
}

// applyEffort configures adaptive thinking and effort on an Anthropic request.
// "adaptive" enables adaptive thinking without an effort override.
// "low"/"medium"/"high"/"max" enables adaptive thinking with an effort level.
// Empty string is a no-op.
func applyEffort(params *anthropic.MessageNewParams, effort string) {
	if effort == "" {
		return
	}
	adaptive := anthropic.NewThinkingConfigAdaptiveParam()
	params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive}
	switch effort {
	case "low":
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortLow}
	case "medium":
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortMedium}
	case "high":
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortHigh}
	case "max":
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortMax}
	}
}

// anthropicModelSupportsThinking reports whether a bare Anthropic model name
// accepts adaptive/extended thinking. Claude Haiku models do not: enabling the
// adaptive thinking config for them makes the API reject the request with
// "Adaptive thinking not allowed for this model".
func anthropicModelSupportsThinking(model string) bool {
	return !strings.Contains(strings.ToLower(model), "haiku")
}

// toAnthropicSystem maps neutral SystemBlocks to anthropic.TextBlockParam.
// CacheControl on the last block (when present) is translated; otherwise
// the top-level MessageNewParams.CacheControl handles caching.
func toAnthropicSystem(system []SystemBlock) []anthropic.TextBlockParam {
	if len(system) == 0 {
		return nil
	}
	out := make([]anthropic.TextBlockParam, len(system))
	for i, b := range system {
		out[i] = anthropic.TextBlockParam{Text: b.Text}
		if b.CacheControl != nil {
			out[i].CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
	}
	return out
}

// toAnthropicMessages maps neutral MessageParam slices into the anthropic
// SDK shape. Each ContentBlock variant becomes the appropriate SDK helper.
func toAnthropicMessages(messages []MessageParam) ([]anthropic.MessageParam, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	out := make([]anthropic.MessageParam, 0, len(messages))
	for _, m := range messages {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case BlockText:
				blocks = append(blocks, anthropic.NewTextBlock(b.Text))
			case BlockImage:
				blocks = append(blocks, anthropic.NewImageBlockBase64(b.MediaType, b.Data))
			case BlockThinking:
				// Skip OpenAI-produced reasoning items (their Signature
				// holds an encrypted_content blob in OpenAI's namespace,
				// not an Anthropic thinking signature). OpenAI reasoning
				// IDs are stable-prefixed with "rs_".
				if strings.HasPrefix(b.ID, "rs_") {
					continue
				}
				blocks = append(blocks, anthropic.NewThinkingBlock(b.Signature, b.Text))
			case BlockToolUse:
				blocks = append(blocks, anthropic.NewToolUseBlock(b.ID, b.Input, b.Name))
			case BlockToolResult:
				blocks = append(blocks, anthropic.NewToolResultBlock(b.ToolUseID, b.Output, b.IsError))
			default:
				return nil, fmt.Errorf("unknown content block type %q", b.Type)
			}
		}
		switch m.Role {
		case RoleUser:
			out = append(out, anthropic.NewUserMessage(blocks...))
		case RoleAssistant:
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		default:
			return nil, fmt.Errorf("unknown role %q", m.Role)
		}
	}
	return out, nil
}

// toAnthropicTools maps neutral ToolParam slices into the anthropic SDK
// union shape.
func toAnthropicTools(tools []ToolParam) []anthropic.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		props, _ := t.InputSchema["properties"]
		var required []string
		if r, ok := t.InputSchema["required"].([]string); ok {
			required = r
		} else if r2, ok := t.InputSchema["required"].([]any); ok {
			required = make([]string, 0, len(r2))
			for _, s := range r2 {
				if str, ok := s.(string); ok {
					required = append(required, str)
				}
			}
		}
		tp := anthropic.ToolParam{
			Name: t.Name,
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: props,
				Required:   required,
			},
		}
		if t.Description != "" {
			tp.Description = anthropic.String(t.Description)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tp})
	}
	return out
}

// fromAnthropicMessage converts an accumulated anthropic.Message into the
// neutral Message shape. Walks msg.Content once, populating Content,
// ToolCalls, and TextContent in tandem.
func fromAnthropicMessage(msg *anthropic.Message) *Message {
	out := &Message{
		StopReason: mapAnthropicStopReason(string(msg.StopReason)),
		Raw:        msg,
		Usage: Usage{
			InputTokens:         msg.Usage.InputTokens,
			OutputTokens:        msg.Usage.OutputTokens,
			CacheCreationTokens: msg.Usage.CacheCreationInputTokens,
			CacheReadTokens:     msg.Usage.CacheReadInputTokens,
		},
	}
	var textParts []string
	out.Content = make([]ContentBlock, 0, len(msg.Content))
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			out.Content = append(out.Content, ContentBlock{Type: BlockText, Text: b.Text})
			textParts = append(textParts, b.Text)
		case anthropic.ThinkingBlock:
			out.Content = append(out.Content, ContentBlock{Type: BlockThinking, Text: b.Thinking, Signature: b.Signature})
		case anthropic.ToolUseBlock:
			// b.Input is json.RawMessage in v1.26.0; parse to map[string]any.
			input := anthropicToolInputToMap(b)
			out.Content = append(out.Content, ContentBlock{
				Type:  BlockToolUse,
				ID:    b.ID,
				Name:  b.Name,
				Input: input,
			})
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:    b.ID,
				Name:  b.Name,
				Input: input,
			})
		}
	}
	out.TextContent = strings.Join(textParts, "")
	return out
}

// anthropicToolInputToMap parses a ToolUseBlock.Input (json.RawMessage) into map[string]any.
func anthropicToolInputToMap(b anthropic.ToolUseBlock) map[string]any {
	raw := b.Input
	if len(raw) == 0 {
		return map[string]any{}
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		log.Printf("[llm anthropic] tool input parse failed for %s: %v", b.Name, err)
		return map[string]any{}
	}
	return input
}

// mapAnthropicStopReason converts an anthropic stop_reason string to the
// neutral StopReason enum.
func mapAnthropicStopReason(s string) StopReason {
	switch s {
	case "end_turn":
		return StopEndTurn
	case "tool_use":
		return StopToolUse
	case "max_tokens":
		return StopMaxTokens
	case "stop_sequence":
		return StopStopSequence
	case "":
		return StopOther
	default:
		return StopOther
	}
}

// ensure interface satisfaction
var _ Client = (*anthropicClient)(nil)
