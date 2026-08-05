package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestSecondDaemonRefusesSocketTakeover guards the socket-ownership fix: a
// second vixd started against a socket a live daemon already owns must refuse
// and exit non-zero instead of silently unlinking the socket and hijacking it.
// The original incident: a stray second daemon stole /tmp/vixd.sock, so the
// threads list emptied and new threads reported "daemon is not responding".
//
// asserts: (1) the conflicting vixd exits non-zero with an "already listening"
// message on the wire (its stderr) and (2) the original TUI thread stays alive
// and answers a follow-up turn (socket untouched).
func TestSecondDaemonRefusesSocketTakeover(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "daemon",
		Subcategory: "daemon.socket_guard",
		Description: "a second vixd refuses to hijack a socket a live daemon owns",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	// One full turn so there is a live, working thread before the conflict.
	h.Mock.Enqueue(harness.Text("First reply: still here."))
	h.UI.Type("say hello")
	h.UI.Enter()
	h.UI.WaitFor("First reply: still here.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("live-thread")

	// Launch a second vixd against the same socket. The guard makes it refuse.
	output, exitCode := h.Daemon.StartConflicting()
	if exitCode == 0 {
		t.Fatalf("second vixd should have exited non-zero; output:\n%s", output)
	}
	if !strings.Contains(output, "already listening") {
		t.Fatalf("second vixd output should mention 'already listening'; got:\n%s", output)
	}

	// The original thread must be untouched: a follow-up turn still works.
	h.Mock.Enqueue(harness.Text("Second reply: connection intact."))
	h.UI.Type("are you still there")
	h.UI.Enter()
	h.UI.WaitFor("Second reply: connection intact.")
	h.UI.Shot("after-conflict")
}
