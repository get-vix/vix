package llm

// bedrockClient calls Amazon Bedrock's invoke-with-response-stream endpoint
// using a bearer token (AWS_BEARER_TOKEN_BEDROCK). It speaks the Anthropic
// messages format ("anthropic_version":"bedrock-2023-05-31"), supports
// ephemeral prompt caching, and decodes responses from the Amazon binary
// EventStream format without requiring the full AWS SDK.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/get-vix/vix/internal/config"
)

const bedrockAnthropicVersion = "bedrock-2023-05-31"

type bedrockClient struct {
	httpClient           *http.Client
	model                string
	effort               string
	maxTokens            int64
	cred                 config.Credential
	systemPrefix         string
	region               string
	baseURL              string
	streamIdleTimeout    time.Duration
	thinkingStallTimeout time.Duration
}

func NewBedrock(cfg Config) (Client, error) {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = "us-east-1"
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = NewPluginHTTPClient(cfg.PluginCfg)
	}
	idle := cfg.StreamIdle
	if idle <= 0 {
		idle = EnvDuration("VIX_STREAM_IDLE_TIMEOUT", DefaultStreamIdleTimeout)
	}
	stall := cfg.ThinkingStall
	if stall <= 0 {
		stall = EnvDuration("VIX_STREAM_THINKING_STALL_TIMEOUT", DefaultThinkingStallTimeout)
	}
	return &bedrockClient{
		httpClient:           httpClient,
		model:                cfg.Model,
		effort:               cfg.Effort,
		maxTokens:            cfg.MaxTokens,
		cred:                 cfg.Credential,
		systemPrefix:         cfg.PluginCfg.SystemPrefix,
		region:               region,
		baseURL:              cfg.BaseURL,
		streamIdleTimeout:    idle,
		thinkingStallTimeout: stall,
	}, nil
}

func (b *bedrockClient) Provider() ProviderID          { return ProviderBedrock }
func (b *bedrockClient) Model() string                 { return b.model }
func (b *bedrockClient) Credential() config.Credential { return b.cred }
func (b *bedrockClient) MaxTokens() int64              { return b.maxTokens }
func (b *bedrockClient) Effort() string                { return b.effort }

func (b *bedrockClient) StreamMessage(
	ctx context.Context,
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	onDelta func(string),
	onThinkingDelta func(string),
) (*Message, time.Duration, error) {
	return b.StreamMessageWith(ctx, system, messages, tools, onDelta, onThinkingDelta, StreamOpts{})
}

func (b *bedrockClient) StreamMessageWith(
	ctx context.Context,
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	onDelta func(string),
	onThinkingDelta func(string),
	opts StreamOpts,
) (*Message, time.Duration, error) {
	t0 := time.Now()

	if b.systemPrefix != "" {
		system = append([]SystemBlock{{Text: b.systemPrefix}}, system...)
	}

	maxTokens := b.maxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	reqID := RequestIDFromContext(ctx)
	if reqID == "" {
		reqID = NewRequestID()
		ctx = WithRequestID(ctx, reqID)
	}

	body, err := b.buildRequest(system, messages, tools, maxTokens)
	if err != nil {
		return nil, 0, fmt.Errorf("bedrock: build request: %w", err)
	}

	log.Printf("[llm req=%s] stream_start provider=bedrock model=%s max_tokens=%d messages=%d tools=%d",
		reqID, b.model, maxTokens, len(messages), len(tools))

	var endpointURL string
	if b.baseURL != "" {
		endpointURL = strings.TrimRight(b.baseURL, "/") +
			"/model/" + url.PathEscape(b.model) + "/invoke-with-response-stream"
	} else {
		endpointURL = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s/invoke-with-response-stream",
			url.PathEscape(b.region), url.PathEscape(b.model))
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("bedrock: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/vnd.amazon.eventstream")
	httpReq.Header.Set("Authorization", "Bearer "+b.cred.Value)

	resp, err := b.httpClient.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("bedrock: HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		log.Printf("[llm req=%s] SSE stream error after 0 events: POST %q: %d %s %s",
			reqID, endpointURL, resp.StatusCode, resp.Status, string(raw))
		return nil, 0, b.httpStatusError(resp.StatusCode, resp.Status, raw)
	}

	msg, err := b.runStream(ctx, resp.Body, t0, reqID, onDelta, onThinkingDelta)
	if err != nil {
		return nil, 0, err
	}
	return msg, time.Since(t0), nil
}

// httpStatusError maps an HTTP status to a typed error recognisable by classifyError.
func (b *bedrockClient) httpStatusError(code int, status string, body []byte) error {
	msg := string(body)
	if msg == "" {
		msg = status
	}
	return &BedrockHTTPError{Code: code, Msg: msg}
}

// BedrockHTTPError is a typed error for non-2xx Bedrock HTTP responses.
// Exported so classifyError in the daemon layer can use errors.As for
// robust classification without string matching.
type BedrockHTTPError struct {
	Code int
	Msg  string
}

func (e *BedrockHTTPError) Error() string {
	return fmt.Sprintf("bedrock HTTP %d: %s", e.Code, e.Msg)
}

// classifyError recognises bedrockHTTPError via string fallback in errors.go;
// we expose the status through the message so isRateLimitError can detect 429.
// We also implement a method so errors.As can match directly.

// ---- EventStream parser -----------------------------------------------

// frame is one decoded Amazon EventStream frame.
type frame struct {
	eventType string
	payload   []byte
}

// readFrame reads one binary EventStream frame from r.
//
// Frame layout (all big-endian):
//
//	[0:4]  total byte length (includes all fields + trailing CRC)
//	[4:8]  headers byte length
//	[8:12] prelude CRC32
//	[12 : 12+headersLen] headers
//	[12+headersLen : totalLen-4] payload
//	[totalLen-4 : totalLen] message CRC32
func readFrame(r io.Reader) (*frame, error) {
	// Read 12-byte prelude
	var prelude [12]byte
	if _, err := io.ReadFull(r, prelude[:]); err != nil {
		return nil, err
	}
	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])
	const maxFrameSize = 512 * 1024 // 512 KB — well above any real chunk
	if totalLen < 16 || headersLen > totalLen-16 || totalLen > maxFrameSize {
		return nil, fmt.Errorf("bedrock eventstream: malformed frame (total=%d headers=%d)", totalLen, headersLen)
	}

	// Read rest of frame (after the 12-byte prelude)
	rest := make([]byte, int(totalLen)-12)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, err
	}

	headers := rest[:headersLen]
	payloadEnd := int(totalLen) - 12 - 4 // minus trailing CRC
	var payload []byte
	if payloadEnd > int(headersLen) {
		payload = rest[headersLen:payloadEnd]
	}

	// Parse `:event-type` from headers
	eventType := parseEventType(headers)
	return &frame{eventType: eventType, payload: payload}, nil
}

// parseEventType scans binary headers for the `:event-type` key (type 7 = string).
func parseEventType(headers []byte) string {
	i := 0
	for i < len(headers) {
		if i >= len(headers) {
			break
		}
		nameLen := int(headers[i])
		i++
		if i+nameLen > len(headers) {
			break
		}
		name := string(headers[i : i+nameLen])
		i += nameLen
		if i >= len(headers) {
			break
		}
		valueType := headers[i]
		i++
		if i+2 > len(headers) {
			break
		}
		valueLen := int(binary.BigEndian.Uint16(headers[i : i+2]))
		i += 2
		if i+valueLen > len(headers) {
			break
		}
		value := headers[i : i+valueLen]
		i += valueLen
		if name == ":event-type" && valueType == 7 {
			return string(value)
		}
	}
	return ""
}

// ---- streaming event types (Anthropic format, same as direct API) ------

type bdStreamEvent struct {
	Type  string   `json:"type"`
	Index int      `json:"index"`
	Delta *bdDelta `json:"delta,omitempty"`
	// message_start
	Message *bdMsgStart `json:"message,omitempty"`
	// message_delta usage
	Usage *bdUsage `json:"usage,omitempty"`
	// content_block_start
	ContentBlock *bdContentBlock `json:"content_block,omitempty"`
}

type bdMsgStart struct {
	Usage bdUsage `json:"usage"`
}

type bdUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

type bdContentBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Text      string `json:"text"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type bdDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"` // tool input_json_delta
	Thinking    string `json:"thinking"`
	Signature   string `json:"signature"`
	StopReason  string `json:"stop_reason"`
}

// runStream drives the Bedrock EventStream response, accumulating a neutral Message.
func (b *bedrockClient) runStream(
	ctx context.Context,
	body io.Reader,
	t0 time.Time,
	reqID string,
	onDelta func(string),
	onThinkingDelta func(string),
) (*Message, error) {
	idleTimeout := b.streamIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = DefaultStreamIdleTimeout
	}

	type frameResult struct {
		f   *frame
		err error
	}
	frameCh := make(chan frameResult, 1)
	done := make(chan struct{})
	defer close(done)

	go func() {
		for {
			f, err := readFrame(body)
			select {
			case frameCh <- frameResult{f, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var (
		acc          bedrockAccumulator
		eventCount   int
		firstEventAt time.Time
	)

	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case fr := <-frameCh:
			if fr.err == io.EOF || fr.err == io.ErrUnexpectedEOF {
				// Abrupt EOF before the clean empty-event-type terminator —
				// treat as a retryable connection loss, not a successful response.
				log.Printf("[llm req=%s] eventstream truncated after %d frames: %v", reqID, eventCount, fr.err)
				return nil, fmt.Errorf("bedrock: stream truncated: %w", fr.err)
			}
			if fr.err != nil {
				log.Printf("[llm req=%s] eventstream error after %d frames: %v", reqID, eventCount, fr.err)
				return nil, fr.err
			}
			idleTimer.Reset(idleTimeout)
			eventCount++
			if firstEventAt.IsZero() {
				firstEventAt = time.Now()
				log.Printf("[llm req=%s] first_sse_event=%s type=%s", reqID, DurStr(t0, firstEventAt), fr.f.eventType)
			}

			switch fr.f.eventType {
			case "chunk":
				// Bedrock wraps the model event as {"bytes":"<base64>"}.
				// Decode to get the actual Anthropic streaming event JSON.
				var wrapper struct {
					Bytes []byte `json:"bytes"`
				}
				if err := json.Unmarshal(fr.f.payload, &wrapper); err != nil || len(wrapper.Bytes) == 0 {
					continue
				}
				var ev bdStreamEvent
				if err := json.Unmarshal(wrapper.Bytes, &ev); err != nil {
					continue
				}
				acc.apply(ev, onDelta, onThinkingDelta)
			case "internalServerException", "modelStreamErrorException", "validationException":
				return nil, fmt.Errorf("bedrock stream error (%s): %s", fr.f.eventType, string(fr.f.payload))
			case "throttlingException":
				return nil, &BedrockHTTPError{Code: 429, Msg: "throttling: " + string(fr.f.payload)}
			case "":
				// Clean end-of-stream marker (empty event type) — success.
				return acc.toMessage(), nil
			}
			continue
		case <-idleTimer.C:
			log.Printf("[llm req=%s] idle_timeout after=%s events_seen=%d", reqID, idleTimeout, eventCount)
			return nil, fmt.Errorf("%w: no EventStream frames for %s", ErrStreamIdleTimeout, idleTimeout)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// bedrockAccumulator collects state across streaming events.
type bedrockAccumulator struct {
	textParts  []string
	content    []ContentBlock
	toolCalls  []ToolCall
	stopReason string
	usage      Usage

	// current block state
	curType      string // "text" | "thinking" | "tool_use"
	curID        string
	curName      string
	curText      strings.Builder
	curThinking  strings.Builder
	curSignature string
	curInput     strings.Builder
}

func (a *bedrockAccumulator) apply(ev bdStreamEvent, onDelta func(string), onThinkingDelta func(string)) {
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			a.usage.InputTokens = ev.Message.Usage.InputTokens
			a.usage.CacheCreationTokens = ev.Message.Usage.CacheCreationInputTokens
			a.usage.CacheReadTokens = ev.Message.Usage.CacheReadInputTokens
		}
	// message_delta carries output_tokens on Bedrock
	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			a.stopReason = ev.Delta.StopReason
		}
		if ev.Usage != nil {
			a.usage.OutputTokens = ev.Usage.OutputTokens
		}
	case "content_block_start":
		a.curText.Reset()
		a.curThinking.Reset()
		a.curInput.Reset()
		a.curSignature = ""
		a.curID = ""
		a.curName = ""
		if ev.ContentBlock != nil {
			a.curType = ev.ContentBlock.Type
			a.curID = ev.ContentBlock.ID
			a.curName = ev.ContentBlock.Name
		}
	case "content_block_delta":
		if ev.Delta == nil {
			return
		}
		switch ev.Delta.Type {
		case "text_delta":
			a.curText.WriteString(ev.Delta.Text)
			if onDelta != nil {
				onDelta(ev.Delta.Text)
			}
		case "thinking_delta":
			a.curThinking.WriteString(ev.Delta.Thinking)
			if onThinkingDelta != nil {
				onThinkingDelta(ev.Delta.Thinking)
			}
		case "signature_delta":
			a.curSignature = ev.Delta.Signature
		case "input_json_delta":
			a.curInput.WriteString(ev.Delta.PartialJSON)
		}
	case "content_block_stop":
		switch a.curType {
		case "text":
			t := a.curText.String()
			a.textParts = append(a.textParts, t)
			a.content = append(a.content, ContentBlock{Type: BlockText, Text: t})
		case "thinking":
			a.content = append(a.content, ContentBlock{
				Type:      BlockThinking,
				Text:      a.curThinking.String(),
				Signature: a.curSignature,
			})
		case "tool_use":
			var input map[string]any
			_ = json.Unmarshal([]byte(a.curInput.String()), &input)
			if input == nil {
				input = map[string]any{}
			}
			tc := ToolCall{ID: a.curID, Name: a.curName, Input: input}
			a.content = append(a.content, ContentBlock{
				Type: BlockToolUse, ID: a.curID, Name: a.curName, Input: input,
			})
			a.toolCalls = append(a.toolCalls, tc)
		}
	case "message_stop":
		// amazon-bedrock-invocationMetrics here; token counts already captured above.
	}

}

func (a *bedrockAccumulator) toMessage() *Message {
	return &Message{
		StopReason:  mapAnthropicStopReason(a.stopReason),
		TextContent: strings.Join(a.textParts, ""),
		Content:     a.content,
		ToolCalls:   a.toolCalls,
		Usage:       a.usage,
	}
}

// ---- request builder ---------------------------------------------------

type bedrockRequest struct {
	AnthropicVersion string          `json:"anthropic_version"`
	MaxTokens        int64           `json:"max_tokens"`
	System           []bdSystemBlock `json:"system,omitempty"`
	Messages         []bdMessage_    `json:"messages"`
	Tools            []bdTool        `json:"tools,omitempty"`
}

type bdSystemBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	CacheControl *bdCacheControl `json:"cache_control,omitempty"`
}

type bdCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type bdMessage_ struct {
	Role    string      `json:"role"`
	Content []bdContent `json:"content"`
}

type bdContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// image
	Source *bdImageSource `json:"source,omitempty"`
	// cache
	CacheControl *bdCacheControl `json:"cache_control,omitempty"`
}

type bdImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type bdTool struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	InputSchema bdToolSchema `json:"input_schema"`
}

type bdToolSchema struct {
	Type       string   `json:"type"`
	Properties any      `json:"properties,omitempty"`
	Required   []string `json:"required,omitempty"`
}

func (b *bedrockClient) buildRequest(
	system []SystemBlock,
	messages []MessageParam,
	tools []ToolParam,
	maxTokens int64,
) ([]byte, error) {
	req := bedrockRequest{
		AnthropicVersion: bedrockAnthropicVersion,
		MaxTokens:        maxTokens,
	}

	// System blocks — put ephemeral cache_control on the last block.
	for i, sb := range system {
		bs := bdSystemBlock{Type: "text", Text: sb.Text}
		if sb.CacheControl != nil || i == len(system)-1 {
			bs.CacheControl = &bdCacheControl{Type: "ephemeral"}
		}
		req.System = append(req.System, bs)
	}

	// Messages — mirror the Anthropic SDK's top-level CacheControl by marking
	// the last content block of the last user message as ephemeral. This caches
	// the growing conversation history between turns.
	lastUserIdx := -1
	for i, m := range messages {
		if m.Role == RoleUser {
			lastUserIdx = i
		}
	}
	for i, m := range messages {
		bm := bdMessage_{Role: string(m.Role)}
		for j, cb := range m.Content {
			bc, err := toBedrockContent(cb)
			if err != nil {
				return nil, err
			}
			if i == lastUserIdx && j == len(m.Content)-1 {
				bc.CacheControl = &bdCacheControl{Type: "ephemeral"}
			}
			bm.Content = append(bm.Content, bc)
		}
		req.Messages = append(req.Messages, bm)
	}

	// Tools
	for _, t := range tools {
		props, _ := t.InputSchema["properties"]
		var required []string
		if r, ok := t.InputSchema["required"].([]string); ok {
			required = r
		} else if r2, ok := t.InputSchema["required"].([]any); ok {
			for _, s := range r2 {
				if str, ok := s.(string); ok {
					required = append(required, str)
				}
			}
		}
		req.Tools = append(req.Tools, bdTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: bdToolSchema{Type: "object", Properties: props, Required: required},
		})
	}

	return json.Marshal(req)
}

func toBedrockContent(cb ContentBlock) (bdContent, error) {
	switch cb.Type {
	case BlockText:
		bc := bdContent{Type: "text", Text: cb.Text}
		if cb.CacheControl != nil {
			bc.CacheControl = &bdCacheControl{Type: "ephemeral"}
		}
		return bc, nil
	case BlockThinking:
		return bdContent{Type: "thinking", Thinking: cb.Text, Signature: cb.Signature}, nil
	case BlockToolUse:
		return bdContent{Type: "tool_use", ID: cb.ID, Name: cb.Name, Input: cb.Input}, nil
	case BlockToolResult:
		return bdContent{Type: "tool_result", ToolUseID: cb.ToolUseID, Content: cb.Output, IsError: cb.IsError}, nil
	case BlockImage:
		return bdContent{
			Type:   "image",
			Source: &bdImageSource{Type: "base64", MediaType: cb.MediaType, Data: cb.Data},
		}, nil
	default:
		return bdContent{}, fmt.Errorf("bedrock: unknown content block type %q", cb.Type)
	}
}

var _ Client = (*bedrockClient)(nil)
