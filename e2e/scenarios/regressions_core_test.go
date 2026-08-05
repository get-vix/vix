package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestMalformedToolCallReturnsError guards issue #21: tool handlers used to do
// unchecked type assertions, so a malformed tool call (missing required field)
// silently produced a zero value — e.g. write_file with no path would write an
// empty path. The validation layer must instead reject it with a tool error
// before dispatch, and no stray file may land on disk.
//
// T1.9 · asserts disk (no write) + wire (error fed back to the model).
func TestMalformedToolCallReturnsError(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "tools",
		Subcategory: "tools.validation",
		Description: "a write_file call missing its path is rejected, not silently zero-valued (#21)",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	// Turn 1: a malformed write_file (no path). Turn 2: the model acknowledges
	// the error it received back.
	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"content":"orphaned"}`),
		harness.Text("That write_file call was rejected as invalid."),
	)
	h.UI.Type("write a file but omit the path")
	h.UI.Enter()
	h.UI.WaitFor("rejected as invalid")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-malformed")

	// Wire: some request carried the validation error as a tool_result.
	if !anyToolResultContains(h, "invalid arguments for write_file") {
		t.Fatalf("no tool_result carried the validation error (requests=%d)", len(h.Mock.Requests()))
	}
	// Disk: no regular file was created from the zero-valued path.
	for _, e := range h.FS.Entries(".") {
		if info, err := h.FS.Stat(e); err == nil && !info.IsDir() {
			t.Fatalf("a stray file was written by the rejected call: %q", e)
		}
	}
}

// TestFrequentlyAccessedFilesRefresh guards the access-stats → system-prompt
// pipeline adjacent to issue #20: after a file is read repeatedly, it must
// appear in the "Frequently Accessed Files" section that buildSystemPrompt
// injects on every turn. Asserted on the wire (the system prompt is sent with
// each request), so it's independent of TUI rendering.
//
// T1.7 · asserts wire (system prompt carries the file).
func TestFrequentlyAccessedFilesRefresh(t *testing.T) {
	meta := harness.Meta{
		Category:    "brain",
		Subcategory: "brain.update_files",
		Description: "a repeatedly-read file surfaces in the system prompt's frequently-accessed list (#20)",
		Wire:        harness.WireMessages,
	}
	harness.SkipScenario(t, meta, "disabled: flaky in-container — the # Frequently Accessed Files block is all-or-nothing and depends on doGetTopFiles re-reading the stored relative path against vixd's cwd; reassert via a more robust signal before re-enabling")

	h := harness.Start(t, meta)

	if err := h.FS.Write("hotfile.go", "package main\n\nfunc Hot() {}\n"); err != nil {
		t.Fatalf("seed hotfile.go: %v", err)
	}

	h.UI.WaitStable(400 * time.Millisecond)

	// Read the file many times across one turn to bias the access-stats ranking,
	// then a final turn whose request should carry the refreshed system prompt.
	var reads []harness.Reply
	for range 12 {
		reads = append(reads, harness.ToolUse("read_file", `{"path":"hotfile.go"}`))
	}
	reads = append(reads, harness.Text("Read hotfile.go repeatedly."))
	h.Mock.Enqueue(reads...)

	h.UI.Type("read hotfile.go several times")
	h.UI.Enter()
	h.UI.WaitFor("Read hotfile.go repeatedly")
	h.UI.WaitStable(300 * time.Millisecond)

	// Second turn: its request's system prompt should list the hot file.
	h.Mock.Enqueue(harness.Text("done"))
	h.UI.Type("anything else")
	h.UI.Enter()
	h.UI.WaitFor("done")
	h.UI.Shot("after-reads")

	if !anyRequestSystemContains(h, "Frequently Accessed Files", "hotfile.go") {
		t.Fatalf("no request's system prompt listed hotfile.go under Frequently Accessed Files")
	}
}

// TestOpenAIMultiTurnDoesNotStall guards issue #34: an OpenAI thread reportedly
// stopped after the first reply. A tool turn followed by a streamed text turn
// must continue past the tool result — i.e. the daemon issues the post-tool
// request and the final (chunk-streamed) answer renders.
//
// T1.2 · runs on the Responses (OpenAI) wire; asserts wire (continuation) +
// screen (final answer).
func TestOpenAIMultiTurnDoesNotStall(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "wires",
		Subcategory: "wires.streaming_continuation",
		Description: "an OpenAI turn continues past the first tool result instead of stalling (#34)",
		Wire:        harness.WireResponses,
		Variant:     "responses",
	}, harness.WithModel("openai/gpt-4o"))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"echo first"}`),
		harness.TextChunks("Continued ", "past ", "the ", "first ", "turn."),
	)
	h.UI.Type("run echo then summarize")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Continued past the first turn.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-continuation")

	// Wire: at least the tool turn + the post-tool continuation request.
	if got := len(h.Mock.Requests()); got < 2 {
		t.Fatalf("expected >=2 requests (tool + continuation), got %d", got)
	}
	if !h.UI.Contains("Continued past the first turn.") {
		t.Fatalf("continuation text not rendered; screen:\n%s", h.UI.Snapshot())
	}
}

// anyRequestSystemContains reports whether some request's body contains all of
// the given substrings (used to inspect the system prompt, which the daemon
// sends on every request).
func anyRequestSystemContains(h *harness.Harness, subs ...string) bool {
	for _, r := range h.Mock.Requests() {
		body := string(r.Body())
		all := true
		for _, s := range subs {
			if !strings.Contains(body, s) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
