package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/packages/ssestream"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"

	"github.com/get-vix/vix/internal/config"
)

// openaiClient is the OpenAI Responses API adapter. Stateless mode — we
// re-send the full conversation each turn rather than relying on the
// server's previous_response_id linking, so the abstraction is symmetric
// across providers.
type openaiClient struct {
	sdk                  openai.Client
	model                string
	effort               string
	maxTokens            int64
	cred                 config.Credential
	systemPrefix         string
	streamIdleTimeout    time.Duration
	thinkingStallTimeout time.Duration
}

// NewOpenAI constructs the OpenAI Responses adapter.
func NewOpenAI(cfg Config) (Client, error) {
	opts := []option.RequestOption{option.WithMaxRetries(0)}
	opts = append(opts, openaiAuthOptions(cfg.Credential)...)

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = NewPluginHTTPClient(cfg.PluginCfg)
	}
	opts = append(opts, option.WithHTTPClient(httpClient))
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}

	sdk := openai.NewClient(opts...)

	idle := cfg.StreamIdle
	if idle <= 0 {
		idle = EnvDuration("VIX_STREAM_IDLE_TIMEOUT", DefaultStreamIdleTimeout)
	}
	stall := cfg.ThinkingStall
	if stall <= 0 {
		stall = EnvDuration("VIX_STREAM_THINKING_STALL_TIMEOUT", DefaultThinkingStallTimeout)
	}

	return &openaiClient{
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

func (o *openaiClient) Provider() ProviderID          { return ProviderOpenAI }
func (o *openaiClient) Model() string                 { return o.model }
func (o *openaiClient) Credential() config.Credential { return o.cred }
func (o *openaiClient) MaxTokens() int64              { return o.maxTokens }
func (o *openaiClient) Effort() string                { return o.effort }

func (o *openaiClient) StreamMessage(
	ctx context.Context,
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	onDelta func(string),
	onThinkingDelta func(string),
) (*Message, time.Duration, error) {
	return o.StreamMessageWith(ctx, system, messages, tools, onDelta, onThinkingDelta, StreamOpts{})
}

func (o *openaiClient) StreamMessageWith(
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
		prefix := SystemBlock{Text: o.systemPrefix}
		system = append([]SystemBlock{prefix}, system...)
	}

	maxTokens := o.maxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	// Build instructions from system blocks (Responses API top-level field).
	var instructionsParts []string
	for _, b := range system {
		instructionsParts = append(instructionsParts, b.Text)
	}
	instructions := strings.Join(instructionsParts, "\n")

	// Build input items from neutral messages.
	input := buildResponsesInput(messages)

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(o.model),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
	}
	if instructions != "" {
		params.Instructions = param.NewOpt(instructions)
	}
	if maxTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(maxTokens)
	}

	// Tools translation: each neutral ToolParam → openai responses.FunctionToolParam.
	if len(tools) > 0 {
		params.Tools = make([]responses.ToolUnionParam, 0, len(tools))
		for _, t := range tools {
			ft := responses.FunctionToolParam{
				Name:       t.Name,
				Parameters: t.InputSchema,
			}
			if t.Description != "" {
				ft.Description = param.NewOpt(t.Description)
			}
			params.Tools = append(params.Tools, responses.ToolUnionParam{OfFunction: &ft})
		}
	}

	// Reasoning effort (only for reasoning models, only when set).
	effort := o.effort
	if opts.EffortOverride != nil {
		effort = *opts.EffortOverride
	}
	if effort != "" && isReasoningOpenAIModel(o.model) {
		level := effort
		switch effort {
		case "adaptive":
			level = "medium"
		case "max":
			level = "high"
		}
		switch level {
		case "low":
			params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffortLow}
		case "medium":
			params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffortMedium}
		case "high":
			params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffortHigh}
		}
		// Ask the server to return the encrypted reasoning blob — required
		// for stateless replay on subsequent turns. Without this, the model
		// loses its chain of thought across turns.
		params.Include = []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		}
	}

	reqID := RequestIDFromContext(ctx)
	if reqID == "" {
		reqID = NewRequestID()
		ctx = WithRequestID(ctx, reqID)
	}
	log.Printf("[llm req=%s] stream_start provider=openai model=%s max_tokens=%d messages=%d tools=%d effort=%q",
		reqID, o.model, maxTokens, len(messages), len(tools), effort)

	stream := o.sdk.Responses.NewStreaming(ctx, params)

	msg, err := o.runResponsesStream(ctx, stream, reqID, onDelta, onThinkingDelta)
	if err != nil {
		return nil, 0, err
	}
	return msg, time.Since(t0), nil
}

// buildResponsesInput translates neutral MessageParams into a flat
// ResponseInputParam (input item list). User messages become role=user
// EasyInputMessages; tool results become function_call_output items;
// assistant messages with tool_use become function_call items.
func buildResponsesInput(messages []MessageParam) responses.ResponseInputParam {
	var input responses.ResponseInputParam

	for _, m := range messages {
		switch m.Role {
		case RoleUser:
			var textParts []string
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					textParts = append(textParts, b.Text)
				case BlockToolResult:
					content := b.Output
					if b.IsError {
						content = "Error: " + content
					}
					input = append(input, responses.ResponseInputItemParamOfFunctionCallOutput(b.ToolUseID, content))
				case BlockImage:
					// TODO: image input via ResponseInputImageParam — deferred for v1.
				}
			}
			if len(textParts) > 0 {
				input = append(input, responses.ResponseInputItemParamOfMessage(strings.Join(textParts, "\n"), responses.EasyInputMessageRoleUser))
			}
		case RoleAssistant:
			// Walk content blocks in order. Reasoning items MUST precede
			// the text/function_call they belong to, so we can't batch
			// text — emit each block as its own input item in arrival
			// order. Adjacent text blocks get coalesced into a single
			// message; reasoning/tool boundaries flush the pending text.
			var pendingText []string
			flushText := func() {
				if len(pendingText) > 0 {
					input = append(input, responses.ResponseInputItemParamOfMessage(strings.Join(pendingText, "\n"), responses.EasyInputMessageRoleAssistant))
					pendingText = nil
				}
			}
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					pendingText = append(pendingText, b.Text)
				case BlockToolUse:
					flushText()
					args, _ := json.Marshal(b.Input)
					input = append(input, responses.ResponseInputItemParamOfFunctionCall(string(args), b.ID, b.Name))
				case BlockThinking:
					// Only re-emit reasoning items produced by THIS provider
					// (have both an ID like rs_xxx and an EncryptedContent
					// blob in Signature). Anthropic thinking blocks use the
					// Signature slot for a different value and would be
					// rejected as malformed encrypted_content.
					if !strings.HasPrefix(b.ID, "rs_") || b.Signature == "" {
						continue
					}
					flushText()
					reasoning := responses.ResponseReasoningItemParam{
						ID:               b.ID,
						Summary:          []responses.ResponseReasoningItemSummaryParam{{Text: b.Text}},
						EncryptedContent: param.NewOpt(b.Signature),
					}
					input = append(input, responses.ResponseInputItemUnionParam{OfReasoning: &reasoning})
				}
			}
			flushText()
		}
	}

	return input
}

// runResponsesStream drives the Responses API SSE event loop with the
// idle-timeout watchdog. Accumulates text deltas, function-call items
// with their argument deltas, and final usage.
func (o *openaiClient) runResponsesStream(
	ctx context.Context,
	stream *ssestream.Stream[responses.ResponseStreamEventUnion],
	reqID string,
	onDelta func(string),
	onThinkingDelta func(string),
) (*Message, error) {
	idleTimeout := o.streamIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = DefaultStreamIdleTimeout
	}

	type streamEvent struct {
		event responses.ResponseStreamEventUnion
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

	var textBuf strings.Builder
	// Track function-call items by their output index.
	type fnState struct {
		id, name string
		args     strings.Builder
	}
	fnByIdx := map[int64]*fnState{}
	var fnOrder []int64
	var finalResponse *responses.Response

loop:
	for {
		select {
		case ev, ok := <-events:
			if !ok || ev.done {
				if ev.err != nil {
					return nil, ev.err
				}
				break loop
			}
			idleTimer.Reset(idleTimeout)
			switch e := ev.event.AsAny().(type) {
			case responses.ResponseTextDeltaEvent:
				textBuf.WriteString(e.Delta)
				if onDelta != nil {
					onDelta(e.Delta)
				}
			case responses.ResponseReasoningSummaryTextDeltaEvent:
				if onThinkingDelta != nil {
					onThinkingDelta(e.Delta)
				}
			case responses.ResponseOutputItemAddedEvent:
				if e.Item.Type == "function_call" {
					st := &fnState{id: e.Item.CallID, name: e.Item.Name}
					fnByIdx[e.OutputIndex] = st
					fnOrder = append(fnOrder, e.OutputIndex)
				}
			case responses.ResponseFunctionCallArgumentsDeltaEvent:
				if st, ok := fnByIdx[e.OutputIndex]; ok {
					st.args.WriteString(e.Delta)
				}
			case responses.ResponseCompletedEvent:
				r := e.Response
				finalResponse = &r
			case responses.ResponseFailedEvent:
				return nil, fmt.Errorf("openai response failed: %s", e.Response.Error.Message)
			case responses.ResponseErrorEvent:
				return nil, fmt.Errorf("openai stream error: %s", e.Message)
			}
		case <-idleTimer.C:
			stream.Close()
			return nil, fmt.Errorf("%w: no SSE events for %s", ErrStreamIdleTimeout, idleTimeout)
		case <-ctx.Done():
			stream.Close()
			return nil, ctx.Err()
		}
	}

	out := &Message{
		TextContent: textBuf.String(),
	}

	if finalResponse != nil {
		// Authoritative path: walk finalResponse.Output to get items in
		// their original order, including reasoning items with their
		// encrypted_content needed for next-turn round-trip. Stream-
		// accumulated text/tool buffers are ignored here because
		// finalResponse.Output carries the same data plus the reasoning
		// items the stream events don't expose.
		for _, item := range finalResponse.Output {
			switch item.Type {
			case "message":
				// Concatenate all text content parts on this assistant message.
				var msgText strings.Builder
				for _, part := range item.Content {
					if part.Type == "output_text" {
						msgText.WriteString(part.Text)
					}
				}
				if msgText.Len() > 0 {
					out.Content = append(out.Content, ContentBlock{Type: BlockText, Text: msgText.String()})
				}
			case "function_call":
				var input map[string]any
				if item.Arguments != "" {
					if err := json.Unmarshal([]byte(item.Arguments), &input); err != nil {
						log.Printf("[llm openai] function args parse failed for %s: %v", item.Name, err)
						input = map[string]any{}
					}
				} else {
					input = map[string]any{}
				}
				out.Content = append(out.Content, ContentBlock{
					Type:  BlockToolUse,
					ID:    item.CallID,
					Name:  item.Name,
					Input: input,
				})
				out.ToolCalls = append(out.ToolCalls, ToolCall{ID: item.CallID, Name: item.Name, Input: input})
			case "reasoning":
				// Capture the reasoning item for stateless round-trip on
				// the next turn. ID + EncryptedContent together let the
				// server resume the chain of thought; Summary is the
				// visible part we surface for logging/UI.
				var summary strings.Builder
				for _, s := range item.Summary {
					if summary.Len() > 0 {
						summary.WriteString("\n")
					}
					summary.WriteString(s.Text)
				}
				out.Content = append(out.Content, ContentBlock{
					Type:      BlockThinking,
					ID:        item.ID,
					Signature: item.EncryptedContent,
					Text:      summary.String(),
				})
			}
		}
		out.StopReason = mapResponsesStopReason(finalResponse.Status, finalResponse.IncompleteDetails.Reason, len(out.ToolCalls) > 0)
		out.Usage = Usage{
			InputTokens:     finalResponse.Usage.InputTokens,
			OutputTokens:    finalResponse.Usage.OutputTokens,
			CacheReadTokens: finalResponse.Usage.InputTokensDetails.CachedTokens,
			ReasoningTokens: finalResponse.Usage.OutputTokensDetails.ReasoningTokens,
		}
		out.Raw = finalResponse
	} else {
		// Fallback path: no completed event (stream cut mid-flight).
		// Reconstruct from streaming buffers — we'll have text and tool
		// calls but no reasoning items (those only land in
		// finalResponse.Output). A partial reasoning block isn't
		// re-feedable anyway, so dropping it is correct.
		if textBuf.Len() > 0 {
			out.Content = append(out.Content, ContentBlock{Type: BlockText, Text: textBuf.String()})
		}
		for _, idx := range fnOrder {
			st := fnByIdx[idx]
			var input map[string]any
			if st.args.Len() > 0 {
				if err := json.Unmarshal([]byte(st.args.String()), &input); err != nil {
					log.Printf("[llm openai] function args parse failed for %s: %v", st.name, err)
					input = map[string]any{}
				}
			} else {
				input = map[string]any{}
			}
			out.Content = append(out.Content, ContentBlock{
				Type:  BlockToolUse,
				ID:    st.id,
				Name:  st.name,
				Input: input,
			})
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: st.id, Name: st.name, Input: input})
		}
		if len(out.ToolCalls) > 0 {
			out.StopReason = StopToolUse
		} else {
			out.StopReason = StopEndTurn
		}
	}

	return out, nil
}

// mapResponsesStopReason maps a Responses API (status, incompleteReason,
// hasToolCalls) tuple into the neutral StopReason enum. status and
// incompleteReason are the raw string values from the Response object.
func mapResponsesStopReason(status responses.ResponseStatus, incompleteReason string, hasToolCalls bool) StopReason {
	switch status {
	case "completed":
		if hasToolCalls {
			return StopToolUse
		}
		return StopEndTurn
	case "incomplete":
		if incompleteReason == "max_output_tokens" {
			return StopMaxTokens
		}
		if incompleteReason == "content_filter" {
			return StopContentFilter
		}
		return StopOther
	case "failed":
		return StopError
	}
	return StopOther
}

var _ Client = (*openaiClient)(nil)
