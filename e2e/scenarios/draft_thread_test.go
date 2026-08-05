package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestDraftThreadStartsOnFirstMessage verifies the create-on-first-message
// lifecycle: on launch the TUI shows a draft welcome screen (working directory +
// Ctrl+O hint + "Draft" status) and no thread is created until the user sends
// the first message, which commits the thread in the shown directory and runs
// the agent there.
func TestDraftThreadStartsOnFirstMessage(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "thread",
		Subcategory: "thread.draft",
		Description: "launch shows a draft welcome; the first message starts the thread and writes a file in the workdir",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("draft-welcome")

	// The draft welcome advertises the working directory and the change-dir key,
	// and the status bar shows the not-started state.
	if !h.UI.Contains("working directory") {
		t.Fatalf("welcome should show the working directory; screen:\n%s", h.UI.Snapshot())
	}
	if !h.UI.Contains("Ctrl+o") {
		t.Fatalf("draft welcome should advertise Ctrl+o; screen:\n%s", h.UI.Snapshot())
	}
	if !h.UI.Contains("Draft") {
		t.Fatalf("status bar should show the Draft state; screen:\n%s", h.UI.Snapshot())
	}

	// Script the model: write a file, then confirm.
	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"draft.txt","content":"committed"}`),
		harness.Text("Started the thread and wrote draft.txt."),
	)

	// Sending the first message commits the draft (opens the connection) and
	// flushes the message.
	h.UI.Type("write draft.txt containing committed")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("wrote draft.txt")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-commit")

	// The file landed in the working directory the draft was showing.
	if got := string(h.FS.Read("draft.txt")); got != "committed" {
		t.Fatalf("draft.txt on disk = %q, want %q", got, "committed")
	}
	// Once committed the thread is live: the Threads tab lists it with a real
	// thread id instead of the "connecting…" placeholder shown for drafts.
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	if h.UI.Contains("connecting…") {
		t.Fatalf("thread still shows as connecting after the first message; screen:\n%s", h.UI.Snapshot())
	}
}

// TestDraftDirectoryPicker verifies the Ctrl+O working-directory picker on a
// draft thread: navigating into a subdirectory and committing there makes the
// agent operate in that directory (a written file lands under the subdir).
func TestDraftDirectoryPicker(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "thread",
		Subcategory: "thread.draft_picker",
		Description: "Ctrl+O picks a subdirectory as the working directory before the thread starts",
		Wire:        harness.WireMessages,
		// Seed one subdirectory so the dirs-only picker has a deterministic,
		// single, non-hidden entry to select.
	}, harness.WithWorkdirFile("sub/keep.txt", "seed"))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("draft-welcome")

	// Open the picker, choose the highlighted "sub" directory, and confirm.
	h.UI.Ctrl('o')
	h.UI.WaitFor("Select working directory")
	h.UI.Shot("picker-open")
	h.UI.Key("enter") // choose highlighted directory (sub)
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("picker-chosen")

	// Commit in the chosen directory by sending the first message.
	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"out.txt","content":"in-sub"}`),
		harness.Text("Wrote out.txt in the chosen directory."),
	)
	h.UI.Type("write out.txt containing in-sub")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Wrote out.txt")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-commit")

	// The file resolved relative to the chosen subdirectory.
	if got := string(h.FS.Read("sub/out.txt")); got != "in-sub" {
		t.Fatalf("sub/out.txt on disk = %q, want %q (workdir root = %q)", got, "in-sub", h.FS.Read("out.txt"))
	}
	if h.FS.Exists("out.txt") {
		t.Fatalf("out.txt should NOT be in the workdir root — the thread cwd should be sub/")
	}
}
