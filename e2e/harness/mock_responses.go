package harness

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

var respSeq atomic.Int64

// writeResponsesSSE renders a Reply as an OpenAI Responses API event stream.
// vix's parser treats the final `response.completed` event's output as
// authoritative, so we emit streaming deltas (for the live UI) plus a completed
// event carrying the full output items + usage.
func writeResponsesSSE(w http.ResponseWriter, r Reply) {
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

	var seq int64
	emit := func(typ string, data map[string]any) {
		data["type"] = typ
		seq++
		data["sequence_number"] = seq
		sseFrame(w, fl, typ, data)
	}

	respID := fmt.Sprintf("resp_e2e_%d", respSeq.Add(1))
	emit("response.created", map[string]any{
		"response": map[string]any{"id": respID, "status": "in_progress", "output": []any{}},
	})

	var output []any
	outIdx := 0

	switch r.kind {
	case replyText:
		// Optional reasoning (thinking) item before the message.
		if r.thinking != "" {
			emit("response.output_item.added", map[string]any{
				"output_index": outIdx,
				"item":         map[string]any{"type": "reasoning", "id": "rs_e2e", "summary": []any{}},
			})
			emit("response.reasoning_summary_text.delta", map[string]any{
				"output_index": outIdx, "summary_index": 0, "delta": r.thinking,
			})
			output = append(output, map[string]any{
				"type": "reasoning", "id": "rs_e2e", "encrypted_content": "enc-e2e",
				"summary": []any{map[string]any{"type": "summary_text", "text": r.thinking}},
			})
			outIdx++
		}

		emit("response.output_item.added", map[string]any{
			"output_index": outIdx,
			"item":         map[string]any{"type": "message", "id": "msg_e2e", "role": "assistant", "status": "in_progress", "content": []any{}},
		})
		for _, part := range textDeltas(r) {
			emit("response.output_text.delta", map[string]any{"output_index": outIdx, "content_index": 0, "delta": part})
		}
		output = append(output, map[string]any{
			"type": "message", "id": "msg_e2e", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": r.text, "annotations": []any{}}},
		})

	case replyToolUse:
		callID := fmt.Sprintf("call_e2e_%d", respSeq.Add(1))
		emit("response.output_item.added", map[string]any{
			"output_index": 0,
			"item":         map[string]any{"type": "function_call", "id": "fc_e2e", "call_id": callID, "name": r.toolName, "arguments": ""},
		})
		emit("response.function_call_arguments.delta", map[string]any{"output_index": 0, "delta": r.toolArgs})
		output = []any{map[string]any{
			"type": "function_call", "id": "fc_e2e", "call_id": callID,
			"name": r.toolName, "arguments": r.toolArgs, "status": "completed",
		}}
	}

	inTokens := int64(10)
	if r.inTokens > 0 {
		inTokens = r.inTokens
	}
	emit("response.completed", map[string]any{
		"response": map[string]any{
			"id": respID, "status": "completed", "output": output,
			"usage": map[string]any{
				"input_tokens":          inTokens,
				"output_tokens":         outTokens(r, 12),
				"input_tokens_details":  map[string]any{"cached_tokens": 0},
				"output_tokens_details": map[string]any{"reasoning_tokens": 0},
			},
		},
	})
}

// --- responses request extraction (top-level "input" item list) ---

// respLastUserText returns the most recent user message text from the input
// items. EasyInputMessage content is a string or an array of input_text parts.
func respLastUserText(raw map[string]any) string {
	items, _ := raw["input"].([]any)
	for i := len(items) - 1; i >= 0; i-- {
		m, _ := items[i].(map[string]any)
		if m == nil {
			continue
		}
		if m["role"] != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			return c
		case []any:
			for _, blk := range c {
				b, _ := blk.(map[string]any)
				if b == nil {
					continue
				}
				if t, _ := b["type"].(string); t == "input_text" || t == "text" {
					if s, ok := b["text"].(string); ok {
						return s
					}
				}
			}
		}
	}
	return ""
}

// respLastToolResult returns the output of the most recent function_call_output
// item fed back to the model.
func respLastToolResult(raw map[string]any) string {
	items, _ := raw["input"].([]any)
	for i := len(items) - 1; i >= 0; i-- {
		m, _ := items[i].(map[string]any)
		if m == nil || m["type"] != "function_call_output" {
			continue
		}
		if s, ok := m["output"].(string); ok {
			return s
		}
		return toolResultContent(m["output"])
	}
	return ""
}
