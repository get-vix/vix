package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestNoArgToolInputSurvivesReload guards the "Input should be an object"
// regression. A no-argument tool call (todo_read) streams in as an empty
// object, but ContentBlock.Input's `json:"input,omitempty"` tag drops the empty
// map when the turn is persisted. After a daemon restart the reloaded block
// carries a nil Input map, and the request builder used to serialize the
// tool_use with a missing/null `input` — which the Anthropic API rejects with:
//
//	messages.N.content.M.tool_use.input: Input should be an object
//
// The fix coerces a nil tool input to an empty object at the request boundary.
// This scenario reproduces the exact persist→reload→resend flow and asserts the
// replayed outbound request carries `input` as an object.
//
// asserts: (1) turn 2 completes after restart and (2) the wire request replaying
// the persisted todo_read tool_use carries an object `input`.
func TestNoArgToolInputSurvivesReload(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "thread",
		Subcategory: "thread.persistence",
		Description: "a persisted no-arg tool_use replays with an object input after a daemon restart (Input should be an object)",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	// Turn 1 (before restart): the model calls the no-arg todo_read tool. The
	// accumulated block's input is an empty object; persistence drops it.
	h.Mock.Enqueue(
		harness.ToolUse("todo_read", `{}`),
		harness.Text("Checked the todo list."),
	)
	h.UI.Type("check the todo list")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Checked the todo list.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("before-restart")

	// Restart: the reloaded todo_read block now carries a nil Input map.
	h.Daemon.Restart()
	h.UI.WaitStable(700 * time.Millisecond)
	h.UI.WaitFor("Checked the todo list.")
	h.UI.Shot("after-restart")

	// Turn 2 (after restart): any turn re-sends the full history, including the
	// reloaded todo_read tool_use, to the model.
	h.Mock.Enqueue(harness.Text("Done."))
	h.UI.Type("thanks")
	h.UI.Enter()
	h.UI.WaitFor("Done.")

	// Wire: the replayed tool_use block must carry an object `input`, not a
	// missing/null field.
	present, isObject := lastToolUseInput(h, "todo_read")
	if !present {
		t.Fatalf("replayed todo_read tool_use has no `input` field; requests=%d\nlast body=%s",
			len(h.Mock.Requests()), lastRequestBody(h))
	}
	if !isObject {
		t.Fatalf("replayed todo_read tool_use `input` is not a JSON object\nlast body=%s", lastRequestBody(h))
	}
}

// lastToolUseInput walks the most recent Messages-wire request for a tool_use
// block with the given name and reports whether its `input` field is present
// and is a JSON object.
func lastToolUseInput(h *harness.Harness, name string) (present, isObject bool) {
	reqs := h.Mock.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		msgs, _ := reqs[i].Raw["messages"].([]any)
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			content, _ := mm["content"].([]any)
			for _, b := range content {
				bb, _ := b.(map[string]any)
				if bb["type"] == "tool_use" && bb["name"] == name {
					v, ok := bb["input"]
					if !ok {
						return false, false
					}
					_, obj := v.(map[string]any)
					return true, obj
				}
			}
		}
	}
	return false, false
}

// lastRequestBody returns the raw body of the most recent LLM request for
// diagnostics.
func lastRequestBody(h *harness.Harness) string {
	reqs := h.Mock.Requests()
	if len(reqs) == 0 {
		return "<no requests>"
	}
	return string(reqs[len(reqs)-1].Body())
}
