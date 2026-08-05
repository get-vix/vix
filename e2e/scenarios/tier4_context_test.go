package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// requestBodyForUser returns the body of the request whose most-recent user
// message is exactly userText (the turn the user started), letting a scenario
// inspect a specific turn's request regardless of other (e.g. async title-gen)
// requests in flight.
func requestBodyForUser(h *harness.Harness, userText string) (string, bool) {
	for _, r := range h.Mock.Requests() {
		if r.LastUserText() == userText {
			return string(r.Body()), true
		}
	}
	return "", false
}

// TestAutoCompactionTriggers guards issue #19: when a turn's prompt exceeds the
// configured fraction of the model's context window, the next turn's pre-turn
// auto-compaction summarizes the older turns and replaces them with a single
// summary message. claude-sonnet-4-6 has a 1,000,000-token window and the
// default threshold is 0.8, so a reported 900k-token prompt trips it.
//
// Kept at two pre-trigger turns so the thread stays under the 3-end-turn
// auto-title call (which would otherwise issue a stray LLM request). Asserted on
// the wire. context.auto_compact
func TestAutoCompactionTriggers(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "context",
		Subcategory: "context.auto_compact",
		Description: "usage over the threshold triggers auto-compaction; a summary replaces the dropped turns (#19)",
		Wire:        harness.WireMessages,
	}, harness.WithModel("anthropic/claude-sonnet-4-6"))

	h.UI.WaitStable(400 * time.Millisecond)

	// Two normal turns; the second reports a prompt size over 0.8 × 1,000,000.
	h.Mock.Enqueue(
		harness.Text("Turn one. EARLY_MARKER_ONE"),
		harness.Text("Turn two.").WithUsage(900000, 10),
	)
	h.UI.Type("first message")
	h.UI.Enter()
	h.UI.WaitFor("Turn one.")
	h.UI.Type("second message")
	h.UI.Enter()
	h.UI.WaitFor("Turn two.")
	h.UI.WaitStable(300 * time.Millisecond)

	// Third turn: auto-compaction runs first (its own summarization LLM call →
	// the summary), then the turn proceeds.
	h.Mock.Enqueue(
		harness.Text("COMPACTION_SUMMARY_MARKER condensed earlier turns"),
		harness.Text("Turn three done."),
	)
	h.UI.Type("third message")
	h.UI.Enter()
	h.UI.WaitFor("Turn three done.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-compaction")

	// The summarization call was made (its trailing prompt is distinctive)...
	if !anyRequestBodyContains(h, "Summarize the conversation above") {
		t.Fatal("no summarization request — auto-compaction did not trigger")
	}
	// ...and the post-compaction turn carries the summary, not the dropped turn.
	body, ok := requestBodyForUser(h, "third message")
	if !ok {
		t.Fatal("no request found for the post-compaction turn")
	}
	if !strings.Contains(body, "COMPACTION_SUMMARY_MARKER") {
		t.Fatal("post-compaction request does not carry the summary")
	}
	if strings.Contains(body, "EARLY_MARKER_ONE") {
		t.Fatal("post-compaction request still carries the dropped turn — compaction didn't drop it")
	}
}

// TestClearResetsHistory proves /clear wipes the conversation: the turn after a
// /clear carries none of the pre-clear history on the wire. /clear is intercepted
// by the daemon (no LLM call), so it consumes no scripted reply. context.manual
func TestClearResetsHistory(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "context",
		Subcategory: "context.manual",
		Description: "/clear resets the conversation; the next turn carries no prior history",
		Wire:        harness.WireMessages,
		Variant:     "clear",
	})

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(harness.Text("First turn recorded."))
	h.UI.Type("remember EARLY_CLEAR_MARKER please")
	h.UI.Enter()
	h.UI.WaitFor("First turn recorded.")

	// /clear via Esc (close the slash menu) then Enter (submit the raw command,
	// which the daemon intercepts).
	h.UI.Type("/clear")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Key("esc")
	h.UI.WaitStable(200 * time.Millisecond)
	h.UI.Enter()
	h.UI.WaitStable(300 * time.Millisecond)

	h.Mock.Enqueue(harness.Text("Second turn after clear."))
	h.UI.Type("anything else now")
	h.UI.Enter()
	h.UI.WaitFor("Second turn after clear.")
	h.UI.Shot("after-clear")

	body, ok := requestBodyForUser(h, "anything else now")
	if !ok {
		t.Fatal("no request found for the post-clear turn")
	}
	if strings.Contains(body, "EARLY_CLEAR_MARKER") {
		t.Fatal("/clear did not reset history — the post-clear request still carries the earlier turn")
	}
}
