package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestWriteThenLs is the spine scenario: the model writes a file, the file
// really lands on disk, the model runs a real `ls`, the genuine listing flows
// back to the mock, and the model's confirmation renders on screen. It asserts
// all three surfaces — disk, wire, and screen.
func TestWriteThenLs(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "files",
		Subcategory: "files.write",
		Description: "model writes hello.txt, then a real ls feeds the listing back and the confirmation renders",
		Wire:        harness.WireMessages,
	})

	// Let the TUI finish painting its initial frame before typing.
	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("initial")

	// Act as the model for all three turns up front (blind scripting): write a
	// file, list the dir, then confirm. The harness serves these in order with
	// no per-turn wait.
	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"hello.txt","content":"hi"}`),
		harness.ToolUse("bash", `{"command":"ls"}`),
		harness.Text("Created hello.txt and listed the directory."),
	)

	h.UI.Type("create hello.txt containing hi, then list the directory")
	h.UI.Enter()

	// Wait for the final assistant turn, auto-approving any tool prompts.
	h.UI.ResolveToolPrompts("listed the directory")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-run")

	// Surface 1 — disk: the file really exists with the right content.
	if got := string(h.FS.Read("hello.txt")); got != "hi" {
		t.Fatalf("hello.txt on disk = %q, want %q", got, "hi")
	}

	// Surface 2 — wire: some request carried a real ls tool_result that
	// includes the file the agent just wrote (proves the execute→feedback loop).
	if !anyToolResultContains(h, "hello.txt") {
		t.Fatalf("no request carried an ls tool_result containing hello.txt (requests=%d)",
			len(h.Mock.Requests()))
	}

	// Surface 3 — screen: the confirmation is visible.
	if !h.UI.Contains("listed the directory") {
		t.Fatalf("final confirmation not rendered; screen:\n%s", h.UI.Snapshot())
	}
}

func anyToolResultContains(h *harness.Harness, sub string) bool {
	for _, r := range h.Mock.Requests() {
		if strings.Contains(r.LastToolResult(), sub) {
			return true
		}
	}
	return false
}
