package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
)

// TestStreamMessage_IdleTimeout verifies that StreamMessage returns
// ErrStreamIdleTimeout when the SSE stream stalls (no events arrive
// within the idle timeout window).
func TestStreamMessage_IdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, "event: ping\ndata: {}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	client, err := NewAnthropic(Config{
		Credential: config.Credential{},
		Model:      "test-model",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t0 := time.Now()
	_, _, streamErr := client.StreamMessage(ctx, nil, []MessageParam{
		NewUserMessage(NewTextBlock("hello")),
	}, nil, nil, nil)
	elapsed := time.Since(t0)

	if streamErr == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(streamErr, ErrStreamIdleTimeout) {
		t.Fatalf("expected ErrStreamIdleTimeout, got: %v", streamErr)
	}
	if elapsed > 5*time.Second {
		t.Errorf("idle timeout took too long: %v (expected ~2s)", elapsed)
	}
}

// TestStreamMessage_ThinkingStall verifies that when a thinking block is
// opened and then stalls past thinkingStallTimeout, StreamMessage returns a
// ThinkingStallError carrying the accumulated summary text.
func TestStreamMessage_ThinkingStall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f, _ := w.(http.Flusher)
		send := func(event, data string) {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
			if f != nil {
				f.Flush()
			}
		}
		send("message_start", `{"type":"message_start","message":{"id":"msg_x","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`)
		send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"pondering the problem..."}}`)
		<-r.Context().Done()
	}))
	defer srv.Close()

	client, err := NewAnthropic(Config{
		Credential:    config.Credential{},
		Model:         "test-model",
		Effort:        "adaptive",
		MaxTokens:     1024,
		BaseURL:       srv.URL,
		StreamIdle:    30 * time.Second,
		ThinkingStall: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var thinkingReceived strings.Builder
	t0 := time.Now()
	_, _, streamErr := client.StreamMessage(ctx, nil, []MessageParam{
		NewUserMessage(NewTextBlock("hello")),
	}, nil, nil, func(delta string) {
		thinkingReceived.WriteString(delta)
	})
	elapsed := time.Since(t0)

	if streamErr == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(streamErr, ErrThinkingStall) {
		t.Fatalf("expected ErrThinkingStall, got: %v", streamErr)
	}
	var stallErr *ThinkingStallError
	if !errors.As(streamErr, &stallErr) {
		t.Fatalf("expected *ThinkingStallError, got: %T (%v)", streamErr, streamErr)
	}
	if !strings.Contains(stallErr.Summary, "pondering") {
		t.Errorf("expected summary to contain thinking delta, got %q", stallErr.Summary)
	}
	if stallErr.Elapsed < 1*time.Second || stallErr.Elapsed > 5*time.Second {
		t.Errorf("stall elapsed out of expected range: %v", stallErr.Elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("stall took too long: %v (expected ~2s)", elapsed)
	}
	if !strings.Contains(thinkingReceived.String(), "pondering") {
		t.Errorf("expected onThinkingDelta to receive text, got %q", thinkingReceived.String())
	}
}

// TestStreamMessageWith_EffortOverride_DisablesThinking verifies that passing
// StreamOpts{EffortOverride: &""} suppresses both the `thinking` field in the
// outbound request and the `thinking.display` JSON injection, while a
// subsequent call with zero-value opts falls back to the instance's configured
// effort.
func TestStreamMessageWith_EffortOverride_DisablesThinking(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Errorf("failed to parse request body: %v\nbody=%s", err, raw)
		}
		mu.Lock()
		bodies = append(bodies, parsed)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f, _ := w.(http.Flusher)
		send := func(event, data string) {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
			if f != nil {
				f.Flush()
			}
		}
		send("message_start", `{"type":"message_start","message":{"id":"msg_x","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`)
		send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`)
		send("message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	client, err := NewAnthropic(Config{
		Credential: config.Credential{},
		Model:      "test-model",
		Effort:     "high",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msgs := []MessageParam{NewUserMessage(NewTextBlock("hi"))}

	// Call 1: override effort to "" for this call only.
	empty := ""
	if _, _, err := client.StreamMessageWith(ctx, nil, msgs, nil, nil, nil, StreamOpts{EffortOverride: &empty}); err != nil {
		t.Fatalf("call 1 failed: %v", err)
	}

	// Call 2: zero-value opts — should use instance effort="high".
	if _, _, err := client.StreamMessageWith(ctx, nil, msgs, nil, nil, nil, StreamOpts{}); err != nil {
		t.Fatalf("call 2 failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("expected 2 captured bodies, got %d", len(bodies))
	}

	// Call 1 should carry no thinking config at all.
	if _, ok := bodies[0]["thinking"]; ok {
		t.Errorf("call 1 (effort override \"\"): expected no `thinking` field, got %v", bodies[0]["thinking"])
	}

	// Call 2 should carry thinking config with display=summarized.
	thinking, ok := bodies[1]["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("call 2 (default opts): expected `thinking` object, got %v", bodies[1]["thinking"])
	}
	if thinking["display"] != "summarized" {
		t.Errorf("call 2: expected thinking.display=\"summarized\", got %v", thinking["display"])
	}
}

// TestStreamMessageWith_HaikuOmitsThinking verifies that Claude Haiku models
// never carry a `thinking` config in the outbound request, even when the
// configured effort is non-empty. Haiku rejects adaptive thinking with
// "Adaptive thinking not allowed for this model", so the adapter must force it
// off.
func TestStreamMessageWith_HaikuOmitsThinking(t *testing.T) {
	var (
		mu   sync.Mutex
		body map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Errorf("failed to parse request body: %v\nbody=%s", err, raw)
		}
		mu.Lock()
		body = parsed
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f, _ := w.(http.Flusher)
		send := func(event, data string) {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
			if f != nil {
				f.Flush()
			}
		}
		send("message_start", `{"type":"message_start","message":{"id":"msg_x","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`)
		send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`)
		send("message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	client, err := NewAnthropic(Config{
		Credential: config.Credential{},
		Model:      "claude-haiku-4-5-20251001",
		Effort:     "adaptive",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msgs := []MessageParam{NewUserMessage(NewTextBlock("hi"))}
	if _, _, err := client.StreamMessageWith(ctx, nil, msgs, nil, nil, nil, StreamOpts{}); err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if _, ok := body["thinking"]; ok {
		t.Errorf("haiku request: expected no `thinking` field, got %v", body["thinking"])
	}
}

// TestAnthropic_StreamMessage_ToolCall verifies the adapter reconstructs
// a tool_use block from a streamed sequence of input_json_delta fragments
// — the load-bearing path for every tool the agent dispatches.
func TestAnthropic_StreamMessage_ToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		sseSend(w, "message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}`)
		sseSend(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_abc","name":"bash","input":{}}}`)
		// Three fragments that, concatenated, form valid JSON.
		sseSend(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"comm"}}`)
		sseSend(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"and\":\"ls"}}`)
		sseSend(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" -la\"}"}}`)
		sseSend(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		sseSend(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":15}}`)
		sseSend(w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	client, err := NewAnthropic(Config{
		Credential: config.Credential{},
		Model:      "test-model",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msg, _, err := client.StreamMessage(ctx, nil, []MessageParam{
		NewUserMessage(NewTextBlock("list files")),
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}

	if msg.StopReason != StopToolUse {
		t.Errorf("StopReason = %s, want %s", msg.StopReason, StopToolUse)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "toolu_abc" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "toolu_abc")
	}
	if tc.Name != "bash" {
		t.Errorf("ToolCall.Name = %q, want %q", tc.Name, "bash")
	}
	if got := tc.Input["command"]; got != "ls -la" {
		t.Errorf("ToolCall.Input[command] = %v, want %q", got, "ls -la")
	}
}

// TestAnthropic_StreamMessage_Usage verifies that all four token counts
// (including cache_read / cache_creation) populate from the streamed
// message_start + message_delta usage payloads.
func TestAnthropic_StreamMessage_Usage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		sseSend(w, "message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":1,"cache_creation_input_tokens":50,"cache_read_input_tokens":200}}}`)
		sseSend(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		sseSend(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		sseSend(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		sseSend(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":42}}`)
		sseSend(w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()

	client, err := NewAnthropic(Config{
		Credential: config.Credential{},
		Model:      "test-model",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msg, _, err := client.StreamMessage(ctx, nil, []MessageParam{
		NewUserMessage(NewTextBlock("hi")),
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}

	if msg.Usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", msg.Usage.InputTokens)
	}
	if msg.Usage.OutputTokens != 42 {
		t.Errorf("OutputTokens = %d, want 42", msg.Usage.OutputTokens)
	}
	if msg.Usage.CacheCreationTokens != 50 {
		t.Errorf("CacheCreationTokens = %d, want 50", msg.Usage.CacheCreationTokens)
	}
	if msg.Usage.CacheReadTokens != 200 {
		t.Errorf("CacheReadTokens = %d, want 200", msg.Usage.CacheReadTokens)
	}
}
