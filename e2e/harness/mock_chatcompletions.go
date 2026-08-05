package harness

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
)

var chatCallSeq atomic.Int64

// dataFrame writes one `data: {json}` SSE frame (no event: line) and flushes.
func dataFrame(w http.ResponseWriter, fl http.Flusher, data any) {
	payload, _ := json.Marshal(data)
	fmt.Fprintf(w, "data: %s\n\n", payload)
	fl.Flush()
}

// writeChatCompletionsSSE renders a Reply as an OpenAI Chat Completions event
// stream: newline-delimited `data: {chunk}` frames terminated by `data: [DONE]`.
func writeChatCompletionsSSE(w http.ResponseWriter, r Reply) {
	if r.kind == replyError {
		writeJSONError(w, r.errStatus, map[string]any{
			"error": map[string]any{"message": r.errMsg, "type": "api_error", "code": nil},
		})
		return
	}

	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	id := fmt.Sprintf("chatcmpl-e2e-%d", chatCallSeq.Add(1))
	base := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": 0, "model": "e2e",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
	}

	// Role preamble chunk.
	dataFrame(w, fl, base(map[string]any{"role": "assistant"}, nil))

	switch r.kind {
	case replyText:
		// Chat Completions has no thinking channel; only the answer renders.
		for _, part := range textDeltas(r) {
			dataFrame(w, fl, base(map[string]any{"content": part}, nil))
		}
		dataFrame(w, fl, base(map[string]any{}, "stop"))

	case replyToolUse:
		callID := fmt.Sprintf("call_e2e_%d", chatCallSeq.Add(1))
		// First chunk: tool-call header (id + name + empty args).
		dataFrame(w, fl, base(map[string]any{
			"tool_calls": []any{map[string]any{
				"index": 0, "id": callID, "type": "function",
				"function": map[string]any{"name": r.toolName, "arguments": ""},
			}},
		}, nil))
		// Second chunk: argument fragment.
		dataFrame(w, fl, base(map[string]any{
			"tool_calls": []any{map[string]any{
				"index": 0, "function": map[string]any{"arguments": r.toolArgs},
			}},
		}, nil))
		dataFrame(w, fl, base(map[string]any{}, "tool_calls"))
	}

	fmt.Fprint(w, "data: [DONE]\n\n")
	fl.Flush()
}

// --- chat completions request extraction ---

// chatLastUserText returns the most recent user message's text. User content is
// either a plain string or an array of {type:"text", text} parts.
func chatLastUserText(raw map[string]any) string {
	msgs, _ := raw["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m == nil || m["role"] != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			return c
		case []any:
			for _, blk := range c {
				b, _ := blk.(map[string]any)
				if b != nil && b["type"] == "text" {
					if s, ok := b["text"].(string); ok {
						return s
					}
				}
			}
		}
	}
	return ""
}

// chatLastToolResult returns the most recent role:"tool" message content.
func chatLastToolResult(raw map[string]any) string {
	msgs, _ := raw["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m == nil || m["role"] != "tool" {
			continue
		}
		if s, ok := m["content"].(string); ok {
			return s
		}
		return toolResultContent(m["content"])
	}
	return ""
}
