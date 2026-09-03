package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestReopenReadOnlyWhileInitializing guards the "read-only while initializing"
// behaviour: a reopened thread renders its on-disk transcript BEFORE the
// daemon's initBrain finishes (which can stall offline on OAuth token refresh /
// MCP connects), so the conversation is viewable immediately. Input stays
// locked (read-only) until initBrain completes and event.replay_ready unlocks
// it.
//
// The daemon's initBrain is artificially slowed via VIX_TEST_INITBRAIN_DELAY_MS
// so the read-only window is observable deterministically.
//
// asserts screen: (1) the transcript replays while init is still running,
// (2) a read-only indicator is shown and typing is swallowed, and (3) once init
// finishes the indicator clears and a fresh turn goes through.
func TestReopenReadOnlyWhileInitializing(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "thread",
		Subcategory: "thread.persistence",
		Description: "a reopened thread shows its transcript read-only while the daemon finishes initializing, then unlocks input",
		Wire:        harness.WireMessages,
	}, harness.WithEnv("VIX_TEST_INITBRAIN_DELAY_MS", "3000"))

	h.UI.WaitStable(400 * time.Millisecond)

	// One full turn so the daemon persists the conversation to disk.
	h.Mock.Enqueue(harness.Text("Acknowledged: the number is 42."))
	h.UI.Type("please remember the number 42")
	h.UI.Enter()
	h.UI.WaitFor("Acknowledged: the number is 42.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("before-restart")

	// Restart the stack on the same HOME + socket. The freshly launched TUI
	// re-attaches; the daemon emits a content-only event.replay before its
	// (3s-delayed) initBrain, opening the read-only window.
	h.Daemon.Restart()

	// Sync on the read-only indicator: it only appears once the reconnect's
	// early replay has rendered the transcript with input locked.
	h.UI.WaitFor("read only")
	h.UI.Shot("reopened-initializing")

	// The transcript is visible during the read-only window (rendered before
	// initBrain finished — no waiting on the network).
	if !h.UI.Contains("Acknowledged: the number is 42.") {
		t.Fatalf("transcript should be visible while initializing; screen:\n%s", h.UI.Snapshot())
	}

	// Input is read-only: keystrokes and Enter are swallowed, so nothing is
	// typed into the box and no turn is sent.
	h.UI.Type("this must not send")
	h.UI.Enter()
	if h.UI.Contains("this must not send") {
		t.Fatalf("input should be swallowed while read-only; screen:\n%s", h.UI.Snapshot())
	}

	// Once initBrain finishes, event.replay_ready unlocks input and the
	// read-only indicator clears. Poll on that condition (no fixed sleep).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && h.UI.Contains("read only") {
		time.Sleep(150 * time.Millisecond)
	}
	if h.UI.Contains("read only") {
		t.Fatalf("read-only indicator never cleared after init; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("unlocked")

	// Input now works: a fresh turn goes through end-to-end.
	h.Mock.Enqueue(harness.Text("The number is 42."))
	h.UI.Type("what number did I ask you to remember?")
	h.UI.Enter()
	h.UI.WaitFor("The number is 42.")
	h.UI.Shot("after-unlock-turn")
}
