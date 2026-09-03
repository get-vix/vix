package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestTurnSeparatorShowsAnswerReceivedTime guards the turn-separator timestamp
// feature: the dim separator rendered at the end of each turn (model name ·
// elapsed · cost) now also carries the full date and time the answer was
// received.
//
// In test-render mode every wall-clock value is frozen to the deterministic
// instant 2025-01-01 12:00 UTC, so the separator renders that exact date+time.
// The real "uses the actual receive time" behaviour is covered by unit tests
// (renderTurnInfo), which can assert concrete instants without the frozen-clock
// determinism constraint.
func TestTurnSeparatorShowsAnswerReceivedTime(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "thread",
		Subcategory: "thread.turn_separator",
		Description: "turn separator shows the date and time the model answer was received",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	// One full turn so a turn-end separator is appended to the transcript.
	h.Mock.Enqueue(harness.Text("Done."))
	h.UI.Type("do a thing")
	h.UI.Enter()
	h.UI.WaitFor("Done.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("turn-separator")

	// Screen: the separator carries the frozen answer-received date+time.
	if !h.UI.Contains("Jan 1, 2025 · 12:00 PM") {
		t.Fatalf("turn separator missing answer-received date+time; screen:\n%s", h.UI.Snapshot())
	}
}
