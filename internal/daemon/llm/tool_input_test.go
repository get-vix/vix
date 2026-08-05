package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
)

// A no-argument tool call (e.g. todo_read) streams in as an empty map, which
// ContentBlock.Input's `json:"input,omitempty"` tag drops on persist. After a
// reload the block carries a nil Input map, and every provider must still
// serialize `input` as an object — the Anthropic/Bedrock APIs reject a missing
// or null tool_use input with:
//
//	messages.N.content.M.tool_use.input: Input should be an object
//
// These tests lock the request-boundary coercion in for all four providers.

func TestNormalizeToolInput(t *testing.T) {
	if got := normalizeToolInput(nil); got == nil || len(got) != 0 {
		t.Errorf("normalizeToolInput(nil) = %v, want non-nil empty map", got)
	}
	populated := map[string]any{"path": "main.go"}
	if got := normalizeToolInput(populated); got["path"] != "main.go" {
		t.Errorf("normalizeToolInput(populated) dropped data: %v", got)
	}
}

// findToolUseInput walks an Anthropic request body's messages for the first
// tool_use block and returns its raw `input` field plus whether the key was
// present at all.
func findToolUseInput(t *testing.T, body map[string]any) (val any, present bool) {
	t.Helper()
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		content, _ := mm["content"].([]any)
		for _, b := range content {
			bb, _ := b.(map[string]any)
			if bb["type"] == "tool_use" {
				v, ok := bb["input"]
				return v, ok
			}
		}
	}
	t.Fatalf("no tool_use block in request body: %+v", body)
	return nil, false
}

func TestAnthropic_ToolUse_NilInputSerializesAsObject(t *testing.T) {
	srv, log := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		sseSend(w, "message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`)
		sseSend(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		sseSend(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		sseSend(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		sseSend(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`)
		sseSend(w, "message_stop", `{"type":"message_stop"}`)
	})

	client, err := NewAnthropic(Config{
		Credential: config.Credential{},
		Model:      "test-model",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}

	// A reloaded no-arg tool_use block: Input is a nil map.
	msgs := []MessageParam{
		NewUserMessage(NewTextBlock("hi")),
		NewAssistantMessage(NewToolUseBlock("toolu_1", "todo_read", nil)),
		NewUserMessage(NewToolResultBlock("toolu_1", "ok", false)),
		NewUserMessage(NewTextBlock("continue")),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := client.StreamMessageWith(ctx, nil, msgs, nil, nil, nil, StreamOpts{}); err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	body := log.Last(t).JSONBody(t)
	val, present := findToolUseInput(t, body)
	if !present {
		t.Fatalf("tool_use block missing `input` field; body=%+v", body)
	}
	if _, ok := val.(map[string]any); !ok {
		t.Errorf("tool_use.input = %#v (type %T), want a JSON object", val, val)
	}
}

func TestOpenAI_BuildResponsesInput_NilToolInputSerializesAsObject(t *testing.T) {
	msgs := []MessageParam{
		NewAssistantMessage(NewToolUseBlock("toolu_1", "todo_read", nil)),
	}
	raw, err := json.Marshal(buildResponsesInput(msgs))
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	var parsed []map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	var found bool
	for _, item := range parsed {
		if item["type"] == "function_call" {
			found = true
			if item["arguments"] != "{}" {
				t.Errorf("function_call.arguments = %q, want %q (raw=%s)", item["arguments"], "{}", raw)
			}
		}
	}
	if !found {
		t.Fatalf("no function_call item in input: %s", raw)
	}
}

func TestChatCompletions_ToolUse_NilInputSerializesAsObject(t *testing.T) {
	srv, log := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		sseSend(w, "", chatChunk(`{"id":"x","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}}`))
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})

	client := newChatTestClient(t, srv.URL, "anthropic/claude", "", 5*time.Second)

	msgs := []MessageParam{
		NewUserMessage(NewTextBlock("hi")),
		NewAssistantMessage(NewToolUseBlock("toolu_1", "todo_read", nil)),
		NewUserMessage(NewToolResultBlock("toolu_1", "ok", false)),
		NewUserMessage(NewTextBlock("continue")),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := client.StreamMessage(ctx, nil, msgs, nil, nil, nil); err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	body := log.Last(t).JSONBody(t)
	bodyMsgs, _ := body["messages"].([]any)
	var found bool
	for _, m := range bodyMsgs {
		mm, _ := m.(map[string]any)
		calls, _ := mm["tool_calls"].([]any)
		for _, c := range calls {
			cc, _ := c.(map[string]any)
			fn, _ := cc["function"].(map[string]any)
			found = true
			if fn["arguments"] != "{}" {
				t.Errorf("tool_calls.function.arguments = %q, want %q; body=%+v", fn["arguments"], "{}", body)
			}
		}
	}
	if !found {
		t.Fatalf("no tool_calls in request body: %+v", body)
	}
}

func TestBedrock_ToolUse_NilInputSerializesAsObject(t *testing.T) {
	bc, err := toBedrockContent(NewToolUseBlock("toolu_1", "todo_read", nil))
	if err != nil {
		t.Fatalf("toBedrockContent: %v", err)
	}
	raw, err := json.Marshal(bc)
	if err != nil {
		t.Fatalf("marshal bdContent: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal bdContent: %v", err)
	}
	val, present := parsed["input"]
	if !present {
		t.Fatalf("bedrock tool_use missing `input` field; raw=%s", raw)
	}
	if _, ok := val.(map[string]any); !ok {
		t.Errorf("bedrock tool_use.input = %#v (type %T), want a JSON object; raw=%s", val, val, raw)
	}
}
