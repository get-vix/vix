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

	"github.com/openai/openai-go/responses"

	"github.com/get-vix/vix/internal/config"
)

// responsesResponseStatusForTest is a tiny helper so table tests don't
// have to import responses just for the type conversion.
func responsesResponseStatusForTest(s string) responses.ResponseStatus {
	return responses.ResponseStatus(s)
}

// TestOpenAI_BuildResponsesInput_RoundTripsReasoning verifies that a
// BlockThinking carrying an OpenAI reasoning-item ID (rs_*) and an
// encrypted_content blob in Signature gets re-emitted as a {type:"reasoning"}
// input item with the right id, summary, and encrypted_content. This is the
// load-bearing translation for stateless multi-turn conversations with
// o3/o4/gpt-5-thinking.
func TestOpenAI_BuildResponsesInput_RoundTripsReasoning(t *testing.T) {
	msgs := []MessageParam{
		NewAssistantMessage(
			NewThinkingBlock("the visible summary text", "rs_xyz123"),
			NewTextBlock("the assistant reply"),
		),
	}
	// NewThinkingBlock takes (text, signature). For the OpenAI case we
	// also need an ID; set it directly so it round-trips.
	msgs[0].Content[0].ID = "rs_xyz123"
	msgs[0].Content[0].Signature = "encrypted-blob-xyz"

	input := buildResponsesInput(msgs)

	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	var parsed []map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}

	// Walk for the reasoning item. Assistant messages emitted via
	// EasyInputMessage carry `role: "assistant"` but no explicit `type`
	// field in the JSON, so look for role to identify the text item.
	var reasoningItem map[string]any
	var reasoningIdx int = -1
	var textIdx int = -1
	for i, item := range parsed {
		if item["type"] == "reasoning" {
			reasoningItem = item
			reasoningIdx = i
		}
		if item["role"] == "assistant" {
			textIdx = i
		}
	}

	if reasoningItem == nil {
		t.Fatalf("expected a reasoning input item, got: %s", raw)
	}
	if got := reasoningItem["id"]; got != "rs_xyz123" {
		t.Errorf("reasoning.id = %v, want %q", got, "rs_xyz123")
	}
	if got := reasoningItem["encrypted_content"]; got != "encrypted-blob-xyz" {
		t.Errorf("reasoning.encrypted_content = %v, want %q", got, "encrypted-blob-xyz")
	}
	summary, _ := reasoningItem["summary"].([]any)
	if len(summary) != 1 {
		t.Fatalf("reasoning.summary len = %d, want 1; raw=%s", len(summary), raw)
	}
	if first, _ := summary[0].(map[string]any); first["text"] != "the visible summary text" {
		t.Errorf("summary[0].text = %v, want %q", first["text"], "the visible summary text")
	}

	// Reasoning item MUST precede the assistant text it belongs to.
	if reasoningIdx == -1 || textIdx == -1 {
		t.Fatalf("missing one of reasoning(%d)/text(%d): %s", reasoningIdx, textIdx, raw)
	}
	if reasoningIdx > textIdx {
		t.Errorf("reasoning item (idx=%d) must precede text item (idx=%d) for the model to resume chain of thought", reasoningIdx, textIdx)
	}
}

// TestOpenAI_BuildResponsesInput_DropsAnthropicThinking verifies that
// Anthropic-produced thinking blocks (Signature is an Anthropic signature,
// not encrypted_content, and ID lacks the rs_ prefix) are silently dropped
// rather than re-emitted as malformed reasoning items.
func TestOpenAI_BuildResponsesInput_DropsAnthropicThinking(t *testing.T) {
	msgs := []MessageParam{
		NewAssistantMessage(
			// Anthropic-style: empty ID, opaque signature.
			ContentBlock{Type: BlockThinking, Text: "thought", Signature: "anthropic-sig-blob"},
			NewTextBlock("the reply"),
		),
	}

	input := buildResponsesInput(msgs)
	raw, _ := json.Marshal(input)
	var parsed []map[string]any
	_ = json.Unmarshal(raw, &parsed)

	for _, item := range parsed {
		if item["type"] == "reasoning" {
			t.Fatalf("expected NO reasoning item for Anthropic-shaped thinking block, got: %s", raw)
		}
	}
}

// TestAnthropic_DropsOpenAIReasoningItems verifies the mirror: an
// OpenAI-produced reasoning block (rs_ prefix) gets dropped when replaying
// history through the Anthropic adapter, so Anthropic doesn't reject the
// request with "invalid thinking signature".
func TestAnthropic_DropsOpenAIReasoningItems(t *testing.T) {
	msgs := []MessageParam{
		NewAssistantMessage(
			ContentBlock{Type: BlockThinking, ID: "rs_xyz", Signature: "encrypted-blob", Text: "thought"},
			NewTextBlock("the reply"),
		),
	}

	out, err := toAnthropicMessages(msgs)
	if err != nil {
		t.Fatalf("toAnthropicMessages: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 anthropic message, got %d", len(out))
	}

	// Re-marshal and inspect — we want zero thinking blocks (rs_ one dropped).
	raw, err := json.Marshal(out[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"type":"thinking"`) {
		t.Errorf("expected no thinking block (OpenAI rs_ blocks should be dropped), got: %s", raw)
	}
	// But the text block should survive.
	if !strings.Contains(string(raw), "the reply") {
		t.Errorf("expected text block to survive, got: %s", raw)
	}
}

// TestOpenAI_StreamMessage_ExtractsReasoningItem verifies that when the
// server returns a response.completed event whose output[] contains a
// reasoning item with encrypted_content, the adapter captures it into a
// neutral BlockThinking with ID, Signature, and Text populated.
func TestOpenAI_StreamMessage_ExtractsReasoningItem(t *testing.T) {
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
		// Minimal but valid Responses SSE: one reasoning item + one text
		// message in the completed event's output array. SSE `data:` must
		// be a single line so the JSON is intentionally compact.
		send("response.completed", `{"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"o3","output":[{"type":"reasoning","id":"rs_abc","summary":[{"type":"summary_text","text":"thought summary"}],"encrypted_content":"opaque-blob"},{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"the reply"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":3}},"parallel_tool_calls":false,"tool_choice":"auto","tools":[]}}`)
	}))
	defer srv.Close()

	client, err := NewOpenAI(Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      "o3",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msg, _, err := client.StreamMessage(ctx, nil, []MessageParam{
		NewUserMessage(NewTextBlock("hi")),
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}

	// Expect Content = [thinking, text] in that order.
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d: %+v", len(msg.Content), msg.Content)
	}
	thinking := msg.Content[0]
	if thinking.Type != BlockThinking {
		t.Errorf("Content[0].Type = %s, want %s", thinking.Type, BlockThinking)
	}
	if thinking.ID != "rs_abc" {
		t.Errorf("Content[0].ID = %q, want %q", thinking.ID, "rs_abc")
	}
	if thinking.Signature != "opaque-blob" {
		t.Errorf("Content[0].Signature = %q, want %q", thinking.Signature, "opaque-blob")
	}
	if thinking.Text != "thought summary" {
		t.Errorf("Content[0].Text = %q, want %q", thinking.Text, "thought summary")
	}
	text := msg.Content[1]
	if text.Type != BlockText || text.Text != "the reply" {
		t.Errorf("Content[1] = %+v, want {Text, %q}", text, "the reply")
	}
}

// TestOpenAI_StreamMessage_RequestsIncludeForReasoning verifies that the
// adapter sets include=["reasoning.encrypted_content"] on the request when
// the model is reasoning-capable. Without this the server returns summaries
// but no encrypted blob, breaking stateless replay.
func TestOpenAI_StreamMessage_RequestsIncludeForReasoning(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(raw, &parsed)
		mu.Lock()
		bodies = append(bodies, parsed)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f, _ := w.(http.Flusher)
		fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", `{"type":"response.completed","sequence_number":1,"response":{"id":"r","object":"response","created_at":1,"status":"completed","model":"o3","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}},"parallel_tool_calls":false,"tool_choice":"auto","tools":[]}}`)
		if f != nil {
			f.Flush()
		}
	}))
	defer srv.Close()

	// Reasoning-capable model with non-empty effort.
	client, err := NewOpenAI(Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      "o3",
		Effort:     "medium",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, _, err := client.StreamMessage(ctx, nil, []MessageParam{
		NewUserMessage(NewTextBlock("hi")),
	}, nil, nil, nil); err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("expected 1 request body, got %d", len(bodies))
	}
	include, _ := bodies[0]["include"].([]any)
	found := false
	for _, v := range include {
		if v == "reasoning.encrypted_content" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected include to contain reasoning.encrypted_content; body=%v", bodies[0])
	}
}

// TestOpenAI_StreamMessage_CodexBackendSetsStoreFalse verifies that requests to
// the ChatGPT/Codex backend carry store=false. That endpoint rejects the
// Responses API default (store=true) with a 400 — see issue #27. The regular
// OpenAI endpoint must NOT receive store at all (leaving the server default).
func TestOpenAI_StreamMessage_CodexBackendSetsStoreFalse(t *testing.T) {
	capture := func(codex bool) map[string]any {
		t.Helper()
		var (
			mu     sync.Mutex
			bodies []map[string]any
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			var parsed map[string]any
			_ = json.Unmarshal(raw, &parsed)
			mu.Lock()
			bodies = append(bodies, parsed)
			mu.Unlock()

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			f, _ := w.(http.Flusher)
			fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", `{"type":"response.completed","sequence_number":1,"response":{"id":"r","object":"response","created_at":1,"status":"completed","model":"gpt-5","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}},"parallel_tool_calls":false,"tool_choice":"auto","tools":[]}}`)
			if f != nil {
				f.Flush()
			}
		}))
		defer srv.Close()

		client, err := NewOpenAI(Config{
			Credential: config.Credential{Value: "test-key"},
			Model:      "gpt-5",
			MaxTokens:  1024,
			BaseURL:    srv.URL,
			StreamIdle: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("NewOpenAI: %v", err)
		}
		// The test server URL can't also contain "chatgpt.com", so flip the
		// codex flag directly to exercise the store=false branch.
		client.(*openaiClient).codexBackend = codex

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, _, err := client.StreamMessage(ctx, nil, []MessageParam{
			NewUserMessage(NewTextBlock("hi")),
		}, nil, nil, nil); err != nil {
			t.Fatalf("StreamMessage: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()
		if len(bodies) != 1 {
			t.Fatalf("expected 1 request body, got %d", len(bodies))
		}
		return bodies[0]
	}

	codexBody := capture(true)
	store, ok := codexBody["store"]
	if !ok {
		t.Fatalf("codex backend: expected store field present; body=%v", codexBody)
	}
	if store != false {
		t.Errorf("codex backend: expected store=false, got %v", store)
	}

	regularBody := capture(false)
	if _, ok := regularBody["store"]; ok {
		t.Errorf("regular backend: expected store omitted, got %v", regularBody["store"])
	}
}

// TestIsCodexBackend pins the base-URL detection used to scope store=false.
func TestIsCodexBackend(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://chatgpt.com/backend-api/codex", true},
		{"https://CHATGPT.com/backend-api/codex", true},
		{"https://api.openai.com/v1", false},
		{"http://127.0.0.1:8080", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isCodexBackend(c.url); got != c.want {
			t.Errorf("isCodexBackend(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

// TestOpenAI_StreamMessage_IdleTimeout verifies the watchdog fires when
// the Responses SSE stream goes silent past the idle window.
func TestOpenAI_StreamMessage_IdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		// One heartbeat-shaped event then nothing.
		sseSend(w, "response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"r","object":"response","created_at":1,"status":"in_progress","model":"o3","output":[],"parallel_tool_calls":false,"tool_choice":"auto","tools":[]}}`)
		<-r.Context().Done()
	}))
	defer srv.Close()

	client, err := NewOpenAI(Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      "o3",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
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

// TestOpenAI_StreamMessage_FunctionCallReassembly verifies that function
// call argument fragments streamed across multiple
// response.function_call_arguments.delta events get reassembled correctly,
// AND that the captured tool call from finalResponse.Output uses the
// authoritative arguments string (not the delta accumulator) so the two
// agree.
func TestOpenAI_StreamMessage_FunctionCallReassembly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseHeader(w)
		// Function call item is added, then arguments stream in 3 fragments,
		// then completed with the final response containing the assembled call.
		sseSend(w, "response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_abc","name":"bash","arguments":"","status":"in_progress"}}`)
		sseSend(w, "response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"item_1","delta":"{\"command\":\"l"}`)
		sseSend(w, "response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":3,"output_index":0,"item_id":"item_1","delta":"s -la"}`)
		sseSend(w, "response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":4,"output_index":0,"item_id":"item_1","delta":"\"}"}`)
		sseSend(w, "response.completed", `{"type":"response.completed","sequence_number":5,"response":{"id":"r","object":"response","created_at":1,"status":"completed","model":"o3","output":[{"type":"function_call","id":"item_1","call_id":"call_abc","name":"bash","arguments":"{\"command\":\"ls -la\"}","status":"completed"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}},"parallel_tool_calls":false,"tool_choice":"auto","tools":[]}}`)
	}))
	defer srv.Close()

	client, err := NewOpenAI(Config{
		Credential: config.Credential{Value: "test-key"},
		Model:      "o3",
		MaxTokens:  1024,
		BaseURL:    srv.URL,
		StreamIdle: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
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
	if tc.ID != "call_abc" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "call_abc")
	}
	if tc.Name != "bash" {
		t.Errorf("ToolCall.Name = %q, want %q", tc.Name, "bash")
	}
	if got := tc.Input["command"]; got != "ls -la" {
		t.Errorf("ToolCall.Input[command] = %v, want %q", got, "ls -la")
	}
}

// TestOpenAI_StopReasonMapping table-tests the canonical paths from
// Responses API (status, incomplete_reason, hasToolCalls) into the
// neutral StopReason enum.
func TestOpenAI_StopReasonMapping(t *testing.T) {
	cases := []struct {
		name             string
		status           string
		incompleteReason string
		hasToolCalls     bool
		want             StopReason
	}{
		{"completed_no_tools", "completed", "", false, StopEndTurn},
		{"completed_tool_use", "completed", "", true, StopToolUse},
		{"incomplete_max_tokens", "incomplete", "max_output_tokens", false, StopMaxTokens},
		{"incomplete_content_filter", "incomplete", "content_filter", false, StopContentFilter},
		{"incomplete_unknown", "incomplete", "weird", false, StopOther},
		{"failed", "failed", "", false, StopError},
		{"unknown_status", "weird", "", false, StopOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapResponsesStopReason(responsesResponseStatusForTest(tc.status), tc.incompleteReason, tc.hasToolCalls)
			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}
