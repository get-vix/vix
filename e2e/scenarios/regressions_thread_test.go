package scenarios

import (
	"os"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestConversationSurvivesDaemonRestart guards issue #22: thread state used to
// live only in memory, so a daemon crash/restart lost the conversation. The
// daemon now persists each turn to ~/.vix/threads/open/<id>.json; a freshly
// launched TUI auto-attaches the open thread for the workdir and replays it.
//
// T1.8 · asserts disk (record written) + screen (conversation replayed after
// restart).
func TestConversationSurvivesDaemonRestart(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "thread",
		Subcategory: "thread.persistence",
		Description: "a conversation is replayed after the daemon restarts (#22)",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	// One full turn so the daemon persists the conversation (persist runs per
	// turn, right before event.agent_done).
	h.Mock.Enqueue(harness.Text("Acknowledged: the number is 42."))
	h.UI.Type("please remember the number 42")
	h.UI.Enter()
	h.UI.WaitFor("Acknowledged: the number is 42.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("before-restart")

	// Disk: the open thread record exists before we restart.
	openDir := h.HomePath(".vix", "threads", "open")
	if entries, err := os.ReadDir(openDir); err != nil || len(entries) == 0 {
		t.Fatalf("no persisted thread record under %s (err=%v)", openDir, err)
	}

	// Restart the whole stack on the same HOME + socket.
	h.Daemon.Restart()
	h.UI.WaitStable(700 * time.Millisecond)
	h.UI.Shot("after-restart")

	// Screen: the freshly launched TUI auto-attached the open thread and
	// replayed the prior turn.
	h.UI.WaitFor("Acknowledged: the number is 42.")
	if !h.UI.Contains("please remember the number 42") {
		t.Fatalf("user message not replayed after restart; screen:\n%s", h.UI.Snapshot())
	}
}

// TestReadGateSurvivesDaemonRestart guards the read-gate rebuild: the
// edit_file/edit_minified_file gate is backed by an in-memory "files read this
// thread" set that persistence does not carry. Before the fix, a file read in
// one thread but edited after a daemon restart was wrongly blocked with
// "has not been read in this thread yet", because the restored thread started
// with an empty read set. The daemon now rebuilds that set from the restored
// message history, so an edit on a previously-read file proceeds.
//
// asserts: (1) the edit is NOT blocked (no gate error on the wire) and
// (2) disk reflects the applied edit.
func TestReadGateSurvivesDaemonRestart(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "thread",
		Subcategory: "thread.persistence",
		Description: "an edit on a file read before a daemon restart is not blocked by the read gate",
		Wire:        harness.WireMessages,
	})
	mustSeed(t, h, "gate.txt", "hello world\n")
	h.UI.WaitStable(400 * time.Millisecond)

	// Turn 1 (before restart): the model reads the file. This marks it read in
	// the thread's in-memory set and the turn is persisted to disk.
	h.Mock.Enqueue(
		harness.ToolUse("read_file", `{"path":"gate.txt"}`),
		harness.Text("Read gate.txt."),
	)
	h.UI.Type("read gate.txt")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Read gate.txt.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("before-restart")

	// Restart the whole stack: the in-memory read set is lost and must be
	// rebuilt from the restored history.
	h.Daemon.Restart()
	h.UI.WaitStable(700 * time.Millisecond)
	h.UI.WaitFor("Read gate.txt.")
	h.UI.Shot("after-restart")

	// Turn 2 (after restart): edit the file that was only read pre-restart.
	h.Mock.Enqueue(
		harness.ToolUse("edit_file", `{"path":"gate.txt","old_string":"world","new_string":"there"}`),
		harness.Text("Edited gate.txt."),
	)
	h.UI.Type("change world to there in gate.txt")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Edited gate.txt.")

	// Wire: the gate must not have blocked the edit.
	if anyToolResultContains(h, "has not been read in this thread yet") {
		t.Fatalf("edit was blocked by the read gate after restart; screen:\n%s", h.UI.Snapshot())
	}
	// Disk: the edit was applied.
	if got := string(h.FS.Read("gate.txt")); got != "hello there\n" {
		t.Fatalf("gate.txt = %q, want %q (edit did not apply)", got, "hello there\n")
	}
}
