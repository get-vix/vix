package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

func readAll(r *http.Request) []byte {
	b, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	return b
}

var toolUseSeq atomic.Int64

// sseFrame writes one Server-Sent-Events frame with a compact JSON payload.
func sseFrame(w http.ResponseWriter, fl http.Flusher, event string, data any) {
	payload, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
	fl.Flush()
}

// anthropicErrorType maps an HTTP status to the Anthropic error envelope `type`.
// vix classifies typed errors by status code, so this is cosmetic but kept
// faithful to real responses.
func anthropicErrorType(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 413:
		return "request_too_large"
	case 429:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// writeMessagesSSE renders a Reply as an Anthropic Messages API event stream.
func writeMessagesSSE(w http.ResponseWriter, r Reply) {
	if r.kind == replyError {
		writeJSONError(w, r.errStatus, map[string]any{
			"type":  "error",
			"error": map[string]any{"type": anthropicErrorType(r.errStatus), "message": r.errMsg},
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

	inTokens := int64(10)
	if r.inTokens > 0 {
		inTokens = r.inTokens
	}
	sseFrame(w, fl, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_e2e", "type": "message", "role": "assistant",
			"model": "claude-e2e", "content": []any{},
			"stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": inTokens, "output_tokens": 0},
		},
	})

	switch r.kind {
	case replyText:
		idx := 0
		// Optional thinking block before the visible answer.
		if r.thinking != "" {
			sseFrame(w, fl, "content_block_start", map[string]any{
				"type": "content_block_start", "index": idx,
				"content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""},
			})
			sseFrame(w, fl, "content_block_delta", map[string]any{
				"type": "content_block_delta", "index": idx,
				"delta": map[string]any{"type": "thinking_delta", "thinking": r.thinking},
			})
			sseFrame(w, fl, "content_block_delta", map[string]any{
				"type": "content_block_delta", "index": idx,
				"delta": map[string]any{"type": "signature_delta", "signature": "e2e-thinking-sig"},
			})
			sseFrame(w, fl, "content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
			idx++
		}

		sseFrame(w, fl, "content_block_start", map[string]any{
			"type": "content_block_start", "index": idx,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		// Stream the answer as one delta per chunk (or a single delta).
		for _, part := range textDeltas(r) {
			sseFrame(w, fl, "content_block_delta", map[string]any{
				"type": "content_block_delta", "index": idx,
				"delta": map[string]any{"type": "text_delta", "text": part},
			})
		}
		sseFrame(w, fl, "content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		sseFrame(w, fl, "message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": outTokens(r, 12)},
		})

	case replyToolUse:
		id := fmt.Sprintf("toolu_e2e_%d", toolUseSeq.Add(1))
		sseFrame(w, fl, "content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "tool_use", "id": id, "name": r.toolName, "input": map[string]any{}},
		})
		// Stream the JSON args as a single input_json_delta fragment.
		sseFrame(w, fl, "content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": r.toolArgs},
		})
		sseFrame(w, fl, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		sseFrame(w, fl, "message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": outTokens(r, 20)},
		})
	}

	sseFrame(w, fl, "message_stop", map[string]any{"type": "message_stop"})
}

// textDeltas returns the text fragments to stream for a text reply: the chunks
// when set, otherwise the whole text as one delta.
func textDeltas(r Reply) []string {
	if len(r.chunks) > 0 {
		return r.chunks
	}
	return []string{r.text}
}

// outTokens returns the reply's output-token override, or def when unset.
func outTokens(r Reply, def int64) int64 {
	if r.outTokens > 0 {
		return r.outTokens
	}
	return def
}

// --- wire-agnostic extraction over a decoded request body ---

// lastUserText returns the text of the most recent user message.
func lastUserText(raw map[string]any) string {
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

// lastToolResult returns the content of the most recent tool_result block.
func lastToolResult(raw map[string]any) string {
	msgs, _ := raw["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m == nil {
			continue
		}
		blocks, _ := m["content"].([]any)
		for j := len(blocks) - 1; j >= 0; j-- {
			b, _ := blocks[j].(map[string]any)
			if b == nil || b["type"] != "tool_result" {
				continue
			}
			return toolResultContent(b["content"])
		}
	}
	return ""
}

// toolResultContent flattens an Anthropic tool_result content (string or array
// of text blocks) into one string.
func toolResultContent(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		out := ""
		for _, blk := range v {
			b, _ := blk.(map[string]any)
			if b != nil {
				if s, ok := b["text"].(string); ok {
					out += s
				}
			}
		}
		return out
	}
	return ""
}
