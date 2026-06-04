package daemon

import (
	"context"
	"testing"

	"github.com/get-vix/vix/internal/daemon/llm"
)

// buildToolUseMessage builds a neutral *llm.Message containing N tool_use
// calls (and matching Content blocks) so dispatchToolCalls can consume it.
//
// toolName controls whether dispatch runs in parallel or sequentially: write
// tools (edit_file, write_file, delete_file) force sequential execution.
func buildToolUseMessage(t *testing.T, toolName string, toolIDs ...string) *llm.Message {
	t.Helper()
	msg := &llm.Message{StopReason: llm.StopToolUse}
	for _, id := range toolIDs {
		input := map[string]any{"command": "echo hi", "path": "nonexistent.txt"}
		msg.Content = append(msg.Content, llm.NewToolUseBlock(id, toolName, input))
		msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{ID: id, Name: toolName, Input: input})
	}
	return msg
}

// extractToolUseIDs returns the tool_use_id of every tool_result block in
// the dispatch output, in order.
func extractToolUseIDs(results []llm.ContentBlock) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		if r.Type == llm.BlockToolResult {
			out = append(out, r.ToolUseID)
		}
	}
	return out
}

// TestDispatchToolCalls_PreCancelledContext verifies that when the context is
// already cancelled before dispatch begins, every tool_use block still receives
// a matching tool_result (with "Cancelled" content). This preserves the API
// invariant that each tool_use must be paired with a tool_result.
func TestDispatchToolCalls_PreCancelledContext(t *testing.T) {
	msg := buildToolUseMessage(t, "bash", "toolu_1", "toolu_2", "toolu_3")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before dispatch starts

	executeCalls := 0
	opts := dispatchOptions{
		cwd: t.TempDir(),
		executeTool: func(name string, input map[string]any) *ToolResult {
			executeCalls++
			return &ToolResult{Output: "should not run"}
		},
	}

	results := dispatchToolCalls(ctx, msg, opts)

	if executeCalls != 0 {
		t.Errorf("executeTool should not be called when ctx is pre-cancelled, got %d calls", executeCalls)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 tool_result blocks, got %d", len(results))
	}
	gotIDs := extractToolUseIDs(results)
	wantIDs := []string{"toolu_1", "toolu_2", "toolu_3"}
	for i, want := range wantIDs {
		if gotIDs[i] != want {
			t.Errorf("result[%d] tool_use_id = %q, want %q", i, gotIDs[i], want)
		}
	}
	for i, r := range results {
		if r.Type != llm.BlockToolResult {
			t.Fatalf("result[%d] is not a tool_result block", i)
		}
		if !r.IsError {
			t.Errorf("result[%d] should have is_error=true", i)
		}
		if r.Output != "Cancelled" {
			t.Errorf("result[%d] output = %q, want %q", i, r.Output, "Cancelled")
		}
	}
}

// TestDispatchToolCalls_CancelMidExecution verifies that when the context is
// cancelled partway through a batch, tools that already ran keep their real
// result and remaining tools get synthesized "Cancelled" results.
func TestDispatchToolCalls_CancelMidExecution(t *testing.T) {
	// Use edit_file to force sequential execution (write tools can't parallelize).
	msg := buildToolUseMessage(t, "edit_file", "toolu_1", "toolu_2", "toolu_3")

	ctx, cancel := context.WithCancel(context.Background())
	executeCount := 0
	opts := dispatchOptions{
		cwd: t.TempDir(),
		executeTool: func(name string, input map[string]any) *ToolResult {
			executeCount++
			if executeCount == 1 {
				// After first tool runs, cancel so subsequent tools are skipped.
				cancel()
				return &ToolResult{Output: "first tool ran"}
			}
			return &ToolResult{Output: "should not reach"}
		},
	}

	results := dispatchToolCalls(ctx, msg, opts)

	if executeCount != 1 {
		t.Errorf("executeTool should be called exactly once, got %d", executeCount)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 tool_result blocks (one per tool_use), got %d", len(results))
	}

	// Verify ID ordering is preserved.
	gotIDs := extractToolUseIDs(results)
	wantIDs := []string{"toolu_1", "toolu_2", "toolu_3"}
	for i, want := range wantIDs {
		if gotIDs[i] != want {
			t.Errorf("result[%d] tool_use_id = %q, want %q", i, gotIDs[i], want)
		}
	}

	// First result should be the real output.
	if results[0].Type != llm.BlockToolResult || results[0].Output != "first tool ran" {
		t.Errorf("result[0] should be real output, got %+v", results[0])
	}
	if results[0].IsError {
		t.Errorf("result[0] should not be an error")
	}

	// Second and third should be cancelled.
	for i := 1; i < 3; i++ {
		r := results[i]
		if r.Type != llm.BlockToolResult {
			t.Fatalf("result[%d] is not a tool_result block", i)
		}
		if r.Output != "Cancelled" {
			t.Errorf("result[%d] output = %q, want %q", i, r.Output, "Cancelled")
		}
		if !r.IsError {
			t.Errorf("result[%d] should have is_error=true", i)
		}
	}
}

// TestDispatchToolCalls_NoCancellation verifies that normal (non-cancelled)
// dispatch produces one real tool_result per tool_use.
func TestDispatchToolCalls_NoCancellation(t *testing.T) {
	msg := buildToolUseMessage(t, "bash", "toolu_a", "toolu_b")

	opts := dispatchOptions{
		cwd: t.TempDir(),
		executeTool: func(name string, input map[string]any) *ToolResult {
			return &ToolResult{Output: "ok"}
		},
	}

	results := dispatchToolCalls(context.Background(), msg, opts)

	if len(results) != 2 {
		t.Fatalf("expected 2 tool_result blocks, got %d", len(results))
	}
	for i, r := range results {
		if r.Type != llm.BlockToolResult {
			t.Fatalf("result[%d] is not a tool_result block", i)
		}
		if r.Output != "ok" {
			t.Errorf("result[%d] output = %q, want %q", i, r.Output, "ok")
		}
		if r.IsError {
			t.Errorf("result[%d] should not be an error", i)
		}
	}
}
