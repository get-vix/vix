package llm

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
)

// benchQuiet silences the stdlib logger for the duration of a benchmark so the
// per-request log lines neither pollute output nor skew timings.
func benchQuiet(tb testing.TB) {
	tb.Helper()
	prev := log.Writer()
	log.SetOutput(io.Discard)
	tb.Cleanup(func() { log.SetOutput(prev) })
}

// Performance benchmark for the LLM streaming-decode path. The model is stubbed
// with an httptest server that replays a canned Anthropic SSE response, so this
// measures vix's SSE parsing + message assembly with zero real network.

// cannedAnthropicStream serves a fixed Anthropic message stream: a text block
// delivered over many small deltas (to exercise the incremental decoder) plus
// usage accounting.
func cannedAnthropicStream(w http.ResponseWriter, _ *http.Request) {
	sseHeader(w)
	sseSend(w, "message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1200,"output_tokens":1,"cache_read_input_tokens":800}}}`)
	sseSend(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	for i := 0; i < 64; i++ {
		sseSend(w, "content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"token-%d "}}`, i))
	}
	sseSend(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
	sseSend(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":64}}`)
	sseSend(w, "message_stop", `{"type":"message_stop"}`)
}

func newStreamBenchClient(tb testing.TB, baseURL string) Client {
	tb.Helper()
	c, err := NewAnthropic(Config{
		Credential: config.Credential{},
		Model:      "test-model",
		MaxTokens:  1024,
		BaseURL:    baseURL,
	})
	if err != nil {
		tb.Fatalf("NewAnthropic: %v", err)
	}
	return c
}

func streamDecodeOnce(tb testing.TB, client Client) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg, _, err := client.StreamMessage(ctx, nil,
		[]MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)
	if err != nil {
		tb.Fatalf("StreamMessage: %v", err)
	}
	if msg.StopReason != StopEndTurn || msg.TextContent == "" {
		tb.Fatalf("unexpected message: stop=%q text=%q", msg.StopReason, msg.TextContent)
	}
}

func BenchmarkLLMStreamDecode(b *testing.B) {
	benchQuiet(b)
	srv := httptest.NewServer(http.HandlerFunc(cannedAnthropicStream))
	defer srv.Close()
	client := newStreamBenchClient(b, srv.URL)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		streamDecodeOnce(b, client)
	}
}

func TestLLMStreamDecode_Smoke(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(cannedAnthropicStream))
	defer srv.Close()
	streamDecodeOnce(t, newStreamBenchClient(t, srv.URL))
}
