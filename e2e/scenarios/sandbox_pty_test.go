package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestSandboxAllowsPTY proves a controlling-terminal tool can allocate a
// pseudo-terminal through the sandboxed bash tool. tmux forks a server that
// opens a PTY for its window; if the sandbox denies the pty grant, tmux fails
// with the misleading "create window failed: fork failed: Operation not
// permitted" and the success marker never appears.
//
// This is the end-to-end counterpart to the macOS Seatbelt unit tests
// (TestSeatbeltProfile_AllowsPTY / TestSandboxedBashCmd_CanAllocatePTY in
// internal/daemon). The e2e suite runs under Linux/Landlock, which does not
// gate ioctls, so this guards the user-visible capability ("tmux works in the
// sandbox") across the real TUI → daemon → sandbox path rather than the
// platform-specific directive.
func TestSandboxAllowsPTY(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "sandbox",
		Subcategory: "sandbox.pty",
		Description: "a controlling-terminal tool (tmux) can allocate a PTY through the sandboxed bash tool",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	// tmux -L uses an isolated server socket under /tmp (writable in the
	// sandbox). new-session forks a server that must allocate a PTY for the
	// window; success prints the marker, which is fed back to the model.
	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"tmux -L e2e_pty new-session -d 'true' && echo PTY_ALLOC_OK; tmux -L e2e_pty kill-server 2>/dev/null || true"}`),
		harness.Text("Allocated a PTY via tmux."),
	)
	h.UI.Type("start a detached tmux session")
	h.UI.Enter()
	h.WaitForLLMRequests(2) // bash (allocates the pty) + final turn
	h.UI.Shot("after-pty")

	if mode := h.Daemon.SandboxMode(); mode == "none" {
		t.Skipf("no real sandbox active (mode=%q) — pty-under-sandbox assertion is only meaningful with a backend", mode)
	}

	// The bash tool result (carrying PTY_ALLOC_OK) is echoed back to the model
	// on the follow-up request. If pty allocation was denied, the marker is
	// absent and tmux's fork error appears instead.
	var sawMarker bool
	for _, r := range h.Mock.Requests() {
		if strings.Contains(string(r.Body()), "PTY_ALLOC_OK") {
			sawMarker = true
		}
		if strings.Contains(string(r.Body()), "fork failed") {
			t.Fatalf("pty allocation denied under sandbox: tmux reported a fork failure")
		}
	}
	if !sawMarker {
		t.Fatal("expected PTY_ALLOC_OK in the tool result fed back to the model; tmux could not allocate a PTY")
	}
}
