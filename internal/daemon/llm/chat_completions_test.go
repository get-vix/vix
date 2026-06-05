package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// --- Pure translation tests (no network) ---

// TestChatCompletions_BuildMessages_SystemConcatenated verifies that
// multiple SystemBlocks collapse into a single role:"system" message with
// their text joined by newlines.
func TestChatCompletions_BuildMessages_SystemConcatenated(t *testing.T) {
	system := []SystemBlock{
		{Text: "block A"},
		{Text: "block B"},
		{Text: "block C"},
	}
	out := buildChatCompletionMessages(system, nil)

	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	raw, _ := json.Marshal(out[0])
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)

	if parsed["role"] != "system" {
		t.Errorf("role = %v, want system", parsed["role"])
	}
	content, _ := parsed["content"].(string)
	if content != "block A\nblock B\nblock C" {
		t.Errorf("content = %q, want %q", content, "block A\nblock B\nblock C")
	}
}

// TestChatCompletions_BuildMessages_ToolResultSplit verifies the
// Anthropic→OpenAI semantic difference: Anthropic packs multiple
// tool_results into one user message; Chat Completions requires each to
// be a standalone role:"tool" message.
func TestChatCompletions_BuildMessages_ToolResultSplit(t *testing.T) {
	msgs := []MessageParam{
		NewUserMessage(
			NewToolResultBlock("toolu_1", "output A", false),
			NewToolResultBlock("toolu_2", "output B", false),
			NewToolResultBlock("toolu_3", "err C", true),
		),
	}
	out := buildChatCompletionMessages(nil, msgs)

	if len(out) != 3 {
		t.Fatalf("expected 3 tool messages, got %d", len(out))
	}
	for i, want := range []struct {
		callID  string
		content string
	}{
		{"toolu_1", "output A"},
		{"toolu_2", "output B"},
		{"toolu_3", "Error: err C"},
	} {
		raw, _ := json.Marshal(out[i])
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)
		if parsed["role"] != "tool" {
			t.Errorf("msg[%d].role = %v, want tool", i, parsed["role"])
		}
		if parsed["tool_call_id"] != want.callID {
			t.Errorf("msg[%d].tool_call_id = %v, want %q", i, parsed["tool_call_id"], want.callID)
		}
		gotContent := parsed["content"]
		// content may be a string or array; accept either if it contains the expected text.
		gotStr := fmt.Sprint(gotContent)
		if !strings.Contains(gotStr, want.content) {
			t.Errorf("msg[%d].content = %v, want to contain %q", i, gotContent, want.content)
		}
	}
}

// TestChatCompletions_BuildMessages_AssistantToolUse verifies an assistant
// turn with both text and tool_use blocks produces a single assistant
// message carrying content + tool_calls[] with JSON-marshalled arguments.
func TestChatCompletions_BuildMessages_AssistantToolUse(t *testing.T) {
	msgs := []MessageParam{
		NewAssistantMessage(
			NewTextBlock("let me check"),
			NewToolUseBlock("call_1", "bash", map[string]any{"command": "ls"}),
			NewToolUseBlock("call_2", "read_file", map[string]any{"path": "/tmp/x"}),
		),
	}
	out := buildChatCompletionMessages(nil, msgs)

	if len(out) != 1 {
		t.Fatalf("expected 1 assistant message, got %d", len(out))
	}
	raw, _ := json.Marshal(out[0])
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)

	if parsed["role"] != "assistant" {
		t.Errorf("role = %v, want assistant", parsed["role"])
	}
	if !strings.Contains(fmt.Sprint(parsed["content"]), "let me check") {
		t.Errorf("content missing text; got %v", parsed["content"])
	}
	toolCalls, _ := parsed["tool_calls"].([]any)
	if len(toolCalls) != 2 {
		t.Fatalf("tool_calls len = %d, want 2", len(toolCalls))
	}
	for i, want := range []struct {
		id, name, args string
	}{
		{"call_1", "bash", `{"command":"ls"}`},
		{"call_2", "read_file", `{"path":"/tmp/x"}`},
	} {
		tc, _ := toolCalls[i].(map[string]any)
		if tc["id"] != want.id {
			t.Errorf("tool_calls[%d].id = %v, want %q", i, tc["id"], want.id)
		}
		fn, _ := tc["function"].(map[string]any)
		if fn["name"] != want.name {
			t.Errorf("tool_calls[%d].function.name = %v, want %q", i, fn["name"], want.name)
		}
		// JSON object key order isn't guaranteed; compare via unmarshal.
		var gotArgs, wantArgs map[string]any
		_ = json.Unmarshal([]byte(fmt.Sprint(fn["arguments"])), &gotArgs)
		_ = json.Unmarshal([]byte(want.args), &wantArgs)
		if !equalMaps(gotArgs, wantArgs) {
			t.Errorf("tool_calls[%d].function.arguments = %v, want %v", i, gotArgs, wantArgs)
		}
	}
}

// TestChatCompletions_BuildMessages_DropsThinking verifies BlockThinking
// in an assistant turn is silently dropped (no Chat Completions equivalent).
func TestChatCompletions_BuildMessages_DropsThinking(t *testing.T) {
	msgs := []MessageParam{
		NewAssistantMessage(
			NewThinkingBlock("internal thought", "sig"),
			NewTextBlock("the reply"),
		),
	}
	out := buildChatCompletionMessages(nil, msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	raw, _ := json.Marshal(out[0])
	if strings.Contains(string(raw), "internal thought") {
		t.Errorf("thinking text should be dropped, got: %s", raw)
	}
	if !strings.Contains(string(raw), "the reply") {
		t.Errorf("reply text should survive, got: %s", raw)
	}
}

// TestChatCompletions_BuildTools verifies the tool definition translation.
func TestChatCompletions_BuildTools(t *testing.T) {
	tools := []ToolParam{
		{
			Name:        "bash",
			Description: "run a command",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"command": map[string]any{"type": "string"}},
				"required":   []string{"command"},
			},
		},
		{
			Name: "ping", // no description on purpose
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
	out := buildChatCompletionTools(tools)
	if len(out) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(out))
	}
	raw, _ := json.Marshal(out)
	var parsed []map[string]any
	_ = json.Unmarshal(raw, &parsed)

	if parsed[0]["type"] != "function" {
		t.Errorf("tool[0].type = %v, want function", parsed[0]["type"])
	}
	fn0, _ := parsed[0]["function"].(map[string]any)
	if fn0["name"] != "bash" {
		t.Errorf("tool[0].function.name = %v, want bash", fn0["name"])
	}
	if fn0["description"] != "run a command" {
		t.Errorf("tool[0].function.description = %v, want %q", fn0["description"], "run a command")
	}
	// tool[1] should NOT have a description field (Description elided).
	fn1, _ := parsed[1]["function"].(map[string]any)
	if _, ok := fn1["description"]; ok {
		t.Errorf("tool[1].function.description should be absent, got %v", fn1["description"])
	}
}

// TestChatCompletions_StopReasonMapping table-tests the finish_reason
// vocabulary.
func TestChatCompletions_StopReasonMapping(t *testing.T) {
	cases := []struct {
		in   string
		want StopReason
	}{
		{"stop", StopEndTurn},
		{"length", StopMaxTokens},
		{"tool_calls", StopToolUse},
		{"function_call", StopToolUse},
		{"content_filter", StopContentFilter},
		{"", StopOther},
		{"weird", StopOther},
	}
	for _, c := range cases {
		if got := mapChatCompletionStopReason(c.in); got != c.want {
			t.Errorf("%q → %s, want %s", c.in, got, c.want)
		}
	}
}

// --- Stream/wire tests via the generic chat client (shared wire) ---

// TestChatCompletions_StreamReassembly_SingleTool verifies that argument
// fragments split across three chunks reassemble into the correct map.
func TestChatCompletions_StreamReassembly_SingleTool(t *testing.T) {
	srv, _ := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		// Three chunks each carrying a fragment of the tool_call arguments.
		sseSend(w, "", chatChunk(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_xyz","type":"function","function":{"name":"bash","arguments":"{\"comm"}}]},"finish_reason":null}]}`))
		sseSend(w, "", chatChunk(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls"}}]},"finish_reason":null}]}`))
		sseSend(w, "", chatChunk(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":" -la\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}}`))
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})

	client := newChatTestClient(t, srv.URL, "anthropic/claude", "", 5*time.Second)

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
		t.Fatalf("expected 1 tool call, got %d: %+v", len(msg.ToolCalls), msg.ToolCalls)
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "call_xyz" {
		t.Errorf("ToolCall.ID = %q, want call_xyz", tc.ID)
	}
	if tc.Name != "bash" {
		t.Errorf("ToolCall.Name = %q, want bash", tc.Name)
	}
	if tc.Input["command"] != "ls -la" {
		t.Errorf("ToolCall.Input[command] = %v, want %q", tc.Input["command"], "ls -la")
	}
}

// TestChatCompletions_StreamReassembly_ParallelTools verifies two tool
// calls with index 0 and 1 interleaved across chunks both reassemble
// correctly and end up in index order.
func TestChatCompletions_StreamReassembly_ParallelTools(t *testing.T) {
	srv, _ := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		// Tool 0 begins, tool 1 begins, then their argument fragments
		// interleave, finish_reason on the last chunk.
		sseSend(w, "", chatChunk(`{"id":"x","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_A","type":"function","function":{"name":"bash","arguments":"{\"comm"}}]},"finish_reason":null}]}`))
		sseSend(w, "", chatChunk(`{"id":"x","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_B","type":"function","function":{"name":"read_file","arguments":"{\"pa"}}]},"finish_reason":null}]}`))
		sseSend(w, "", chatChunk(`{"id":"x","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls\"}"}}]},"finish_reason":null}]}`))
		sseSend(w, "", chatChunk(`{"id":"x","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"th\":\"/tmp/x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}}`))
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})

	client := newChatTestClient(t, srv.URL, "anthropic/claude", "", 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msg, _, err := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}

	if len(msg.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d: %+v", len(msg.ToolCalls), msg.ToolCalls)
	}
	if msg.ToolCalls[0].ID != "call_A" || msg.ToolCalls[0].Name != "bash" {
		t.Errorf("ToolCalls[0] = %+v, want call_A/bash", msg.ToolCalls[0])
	}
	if msg.ToolCalls[0].Input["command"] != "ls" {
		t.Errorf("ToolCalls[0].Input[command] = %v, want ls", msg.ToolCalls[0].Input["command"])
	}
	if msg.ToolCalls[1].ID != "call_B" || msg.ToolCalls[1].Name != "read_file" {
		t.Errorf("ToolCalls[1] = %+v, want call_B/read_file", msg.ToolCalls[1])
	}
	if msg.ToolCalls[1].Input["path"] != "/tmp/x" {
		t.Errorf("ToolCalls[1].Input[path] = %v, want /tmp/x", msg.ToolCalls[1].Input["path"])
	}
}

// TestChatCompletions_StreamReassembly_BadJSON verifies that malformed
// argument fragments don't panic — the adapter logs and emits Input: {}.
func TestChatCompletions_StreamReassembly_BadJSON(t *testing.T) {
	srv, _ := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		sseSend(w, "", chatChunk(`{"id":"x","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"bash","arguments":"{not json"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}}`))
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})

	client := newChatTestClient(t, srv.URL, "anthropic/claude", "", 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msg, _, err := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)
	if err != nil {
		t.Fatalf("StreamMessage: %v (should not have errored)", err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Input == nil {
		t.Errorf("ToolCalls[0].Input should be initialized (empty map), got nil")
	}
	if len(msg.ToolCalls[0].Input) != 0 {
		t.Errorf("ToolCalls[0].Input should be empty after parse failure, got %v", msg.ToolCalls[0].Input)
	}
}

// TestChatCompletions_Usage verifies usage fields and the required
// stream_options.include_usage flag.
func TestChatCompletions_Usage(t *testing.T) {
	srv, log := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		sseSend(w, "", chatChunk(`{"id":"x","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":42,"total_tokens":162,"prompt_tokens_details":{"cached_tokens":80},"completion_tokens_details":{"reasoning_tokens":12}}}`))
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})

	client := newChatTestClient(t, srv.URL, "anthropic/claude", "", 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msg, _, err := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}

	if msg.Usage.InputTokens != 120 {
		t.Errorf("InputTokens = %d, want 120", msg.Usage.InputTokens)
	}
	if msg.Usage.OutputTokens != 42 {
		t.Errorf("OutputTokens = %d, want 42", msg.Usage.OutputTokens)
	}
	if msg.Usage.CacheReadTokens != 80 {
		t.Errorf("CacheReadTokens = %d, want 80", msg.Usage.CacheReadTokens)
	}
	if msg.Usage.ReasoningTokens != 12 {
		t.Errorf("ReasoningTokens = %d, want 12", msg.Usage.ReasoningTokens)
	}

	// stream_options.include_usage MUST be true in the request body — without
	// it OpenAI/OpenRouter/MiniMax omit usage from streaming responses.
	body := log.Last(t).JSONBody(t)
	streamOpts, _ := body["stream_options"].(map[string]any)
	if streamOpts == nil || streamOpts["include_usage"] != true {
		t.Errorf("stream_options.include_usage should be true; got %v", body["stream_options"])
	}
}

// TestChatCompletions_IdleTimeout verifies the watchdog fires for the
// Chat Completions wire path too.
func TestChatCompletions_IdleTimeout(t *testing.T) {
	srv, _ := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		sseSend(w, "", chatChunk(`{"id":"x","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`))
		<-r.Context().Done()
	})

	client := newChatTestClient(t, srv.URL, "anthropic/claude", "", 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t0 := time.Now()
	_, _, streamErr := client.StreamMessage(ctx, nil, []MessageParam{NewUserMessage(NewTextBlock("hi"))}, nil, nil, nil)
	elapsed := time.Since(t0)

	if !errors.Is(streamErr, ErrStreamIdleTimeout) {
		t.Fatalf("expected ErrStreamIdleTimeout, got %v", streamErr)
	}
	if elapsed > 5*time.Second {
		t.Errorf("idle timeout took too long: %v", elapsed)
	}
}

// --- helpers ---

// chatChunk returns the data: line content for one Chat Completions SSE
// chunk. The wire format is `data: {json}\n\n` (no event: prefix).
func chatChunk(jsonBody string) string {
	// sseSend writes "event: <event>\ndata: <data>\n\n". For Chat Completions
	// we use an empty event name — that produces "event: \ndata: ...\n\n"
	// which the openai-go SSE parser accepts.
	return jsonBody
}

func equalMaps(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || fmt.Sprint(va) != fmt.Sprint(vb) {
			return false
		}
	}
	return true
}
