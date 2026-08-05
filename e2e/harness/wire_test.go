package harness

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// flushRecorder is an httptest.ResponseRecorder that also satisfies
// http.Flusher (the renderers require a flushable writer).
type flushRecorder struct{ *httptest.ResponseRecorder }

func (flushRecorder) Flush() {}

func newRec() flushRecorder { return flushRecorder{httptest.NewRecorder()} }

func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad test JSON: %v", err)
	}
	return m
}

func TestMessagesRenderer(t *testing.T) {
	rec := newRec()
	writeMessagesSSE(rec, Text("hello world"))
	out := rec.Body.String()
	for _, want := range []string{"event: message_start", "text_delta", "hello world", `"stop_reason":"end_turn"`, "event: message_stop"} {
		if !strings.Contains(out, want) {
			t.Errorf("text stream missing %q\n%s", want, out)
		}
	}

	rec = newRec()
	writeMessagesSSE(rec, ToolUse("bash", `{"command":"ls"}`))
	out = rec.Body.String()
	for _, want := range []string{`"type":"tool_use"`, `"name":"bash"`, "input_json_delta", `ls`, `"stop_reason":"tool_use"`} {
		if !strings.Contains(out, want) {
			t.Errorf("tool stream missing %q\n%s", want, out)
		}
	}
}

func TestChatCompletionsRenderer(t *testing.T) {
	rec := newRec()
	writeChatCompletionsSSE(rec, Text("hi there"))
	out := rec.Body.String()
	for _, want := range []string{`"delta":{"role":"assistant"}`, `"content":"hi there"`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(out, want) {
			t.Errorf("text stream missing %q\n%s", want, out)
		}
	}

	rec = newRec()
	writeChatCompletionsSSE(rec, ToolUse("write_file", `{"path":"a.txt"}`))
	out = rec.Body.String()
	for _, want := range []string{"tool_calls", `"name":"write_file"`, `a.txt`, `"finish_reason":"tool_calls"`, "data: [DONE]"} {
		if !strings.Contains(out, want) {
			t.Errorf("tool stream missing %q\n%s", want, out)
		}
	}
}

func TestResponsesRenderer(t *testing.T) {
	rec := newRec()
	writeResponsesSSE(rec, Text("done"))
	out := rec.Body.String()
	for _, want := range []string{"response.created", "response.output_text.delta", "response.completed", `"output_text"`, `"text":"done"`} {
		if !strings.Contains(out, want) {
			t.Errorf("text stream missing %q\n%s", want, out)
		}
	}

	rec = newRec()
	writeResponsesSSE(rec, ToolUse("bash", `{"command":"ls"}`))
	out = rec.Body.String()
	for _, want := range []string{"function_call", `"name":"bash"`, "response.function_call_arguments.delta", `"status":"completed"`} {
		if !strings.Contains(out, want) {
			t.Errorf("tool stream missing %q\n%s", want, out)
		}
	}
}

// TestMessagesRendererExtensions covers the Tier-4 primitives on the Anthropic
// wire: usage override (drives auto-compaction), thinking block, multi-chunk
// streaming, and the HTTP error envelope.
func TestMessagesRendererExtensions(t *testing.T) {
	// usage override appears in message_start.
	rec := newRec()
	writeMessagesSSE(rec, Text("ok").WithUsage(900000, 7))
	out := rec.Body.String()
	for _, want := range []string{`"input_tokens":900000`, `"output_tokens":7`} {
		if !strings.Contains(out, want) {
			t.Errorf("usage stream missing %q\n%s", want, out)
		}
	}

	// thinking block precedes the text block.
	rec = newRec()
	writeMessagesSSE(rec, Thinking("let me reason", "the answer"))
	out = rec.Body.String()
	for _, want := range []string{`"type":"thinking"`, "thinking_delta", "let me reason", "signature_delta", "the answer"} {
		if !strings.Contains(out, want) {
			t.Errorf("thinking stream missing %q\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, "thinking_delta"), strings.Index(out, "text_delta"); i == -1 || j == -1 || i > j {
		t.Errorf("thinking_delta(%d) must precede text_delta(%d)\n%s", i, j, out)
	}

	// multi-chunk text emits one delta per chunk.
	rec = newRec()
	writeMessagesSSE(rec, TextChunks("foo", "bar", "baz"))
	out = rec.Body.String()
	if n := strings.Count(out, "text_delta"); n != 3 {
		t.Errorf("text_delta count = %d, want 3\n%s", n, out)
	}

	// HTTP error renders a non-200 with the Anthropic error envelope.
	rec = newRec()
	writeMessagesSSE(rec, HTTPError(529, "overloaded, try again"))
	if rec.Code != 529 {
		t.Errorf("error status = %d, want 529", rec.Code)
	}
	out = rec.Body.String()
	for _, want := range []string{`"type":"error"`, "overloaded_error", "overloaded, try again"} {
		if !strings.Contains(out, want) {
			t.Errorf("error body missing %q\n%s", want, out)
		}
	}
}

// TestResponsesRendererExtensions covers the same primitives on the OpenAI
// Responses wire (reasoning summary instead of a thinking block).
func TestResponsesRendererExtensions(t *testing.T) {
	rec := newRec()
	writeResponsesSSE(rec, Text("ok").WithUsage(800000, 9))
	out := rec.Body.String()
	for _, want := range []string{`"input_tokens":800000`, `"output_tokens":9`} {
		if !strings.Contains(out, want) {
			t.Errorf("usage stream missing %q\n%s", want, out)
		}
	}

	rec = newRec()
	writeResponsesSSE(rec, Thinking("reasoning here", "final answer"))
	out = rec.Body.String()
	for _, want := range []string{`"type":"reasoning"`, "response.reasoning_summary_text.delta", "reasoning here", "final answer"} {
		if !strings.Contains(out, want) {
			t.Errorf("reasoning stream missing %q\n%s", want, out)
		}
	}

	rec = newRec()
	writeResponsesSSE(rec, TextChunks("a", "b", "c"))
	out = rec.Body.String()
	if n := strings.Count(out, "event: response.output_text.delta"); n != 3 {
		t.Errorf("output_text.delta count = %d, want 3\n%s", n, out)
	}

	rec = newRec()
	writeResponsesSSE(rec, HTTPError(400, "bad request detail"))
	if rec.Code != 400 {
		t.Errorf("error status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "bad request detail") {
		t.Errorf("error body missing detail\n%s", rec.Body.String())
	}
}

// TestChatCompletionsRendererExtensions covers multi-chunk text and the HTTP
// error envelope on the Chat Completions wire (no thinking channel).
func TestChatCompletionsRendererExtensions(t *testing.T) {
	rec := newRec()
	writeChatCompletionsSSE(rec, TextChunks("x", "y", "z"))
	out := rec.Body.String()
	for _, want := range []string{`"content":"x"`, `"content":"y"`, `"content":"z"`, "data: [DONE]"} {
		if !strings.Contains(out, want) {
			t.Errorf("chunked stream missing %q\n%s", want, out)
		}
	}

	rec = newRec()
	writeChatCompletionsSSE(rec, HTTPError(429, "slow down"))
	if rec.Code != 429 {
		t.Errorf("error status = %d, want 429", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "slow down") {
		t.Errorf("error body missing detail\n%s", rec.Body.String())
	}
}

func TestExtractorsPerWire(t *testing.T) {
	// messages (anthropic) shape
	msgs := mustJSON(t, `{"messages":[
		{"role":"user","content":[{"type":"text","text":"hello msg"}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"LS-MSG"}]}]}
	]}`)
	v := RequestView{Wire: wireMessages, Raw: msgs}
	if got := v.LastUserText(); got != "hello msg" {
		t.Errorf("messages LastUserText = %q", got)
	}
	if got := v.LastToolResult(); got != "LS-MSG" {
		t.Errorf("messages LastToolResult = %q", got)
	}

	// chat completions shape
	chat := mustJSON(t, `{"messages":[
		{"role":"user","content":[{"type":"text","text":"hello chat"}]},
		{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"bash","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"LS-CHAT"}
	]}`)
	v = RequestView{Wire: wireChatCompletions, Raw: chat}
	if got := v.LastUserText(); got != "hello chat" {
		t.Errorf("chat LastUserText = %q", got)
	}
	if got := v.LastToolResult(); got != "LS-CHAT" {
		t.Errorf("chat LastToolResult = %q", got)
	}

	// responses shape
	resp := mustJSON(t, `{"input":[
		{"type":"message","role":"user","content":"hello resp"},
		{"type":"function_call","call_id":"r1","name":"bash","arguments":"{}"},
		{"type":"function_call_output","call_id":"r1","output":"LS-RESP"}
	]}`)
	v = RequestView{Wire: wireResponses, Raw: resp}
	if got := v.LastUserText(); got != "hello resp" {
		t.Errorf("responses LastUserText = %q", got)
	}
	if got := v.LastToolResult(); got != "LS-RESP" {
		t.Errorf("responses LastToolResult = %q", got)
	}
}
