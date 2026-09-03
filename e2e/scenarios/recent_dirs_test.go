package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// recordJSON is a minimal persisted open thread record. Only the fields the
// welcome screen's recent-directories aggregation reads (cwd, timestamps,
// origin) matter; the rest can be omitted.
func recordJSON(id, cwd, last string) string {
	return `{"schema_version":1,"id":"` + id + `","cwd":"` + cwd +
		`","model":"anthropic/claude-sonnet-4-6","started_at":"` + last +
		`","last_request_at":"` + last + `","messages":[]}`
}

// TestWelcomeRecentDirectorySelection verifies the welcome screen's recent-
// directories list: pre-seeded open thread records in two directories surface
// as a ranked "Recent" list on the draft welcome; focusing the welcome area
// (Tab) and pressing Down + Enter switches the draft's working directory to the
// highlighted entry AND returns focus to the editor, so the user can type and
// commit immediately (no extra Tab) and the write lands in the chosen dir.
func TestWelcomeRecentDirectorySelection(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "thread",
		Subcategory: "thread.recent_dirs",
		Description: "welcome lists recent working directories; Tab+Down+Enter selects one and the thread commits there",
		Wire:        harness.WireMessages,
	},
		// Two real subdirectories to serve as candidate working directories.
		harness.WithWorkdirFile("dirA/keep.txt", "seed"),
		harness.WithWorkdirFile("dirB/keep.txt", "seed"),
		// Open thread records rooted at those dirs. dirA has two threads (higher
		// count → ranked first, index 0); dirB has one (index 1).
		harness.WithHomeFile(".vix/threads/open/rec-a1.json",
			recordJSON("rec-a1", "{{WORKDIR}}/dirA", "2024-01-03T00:00:00Z")),
		harness.WithHomeFile(".vix/threads/open/rec-a2.json",
			recordJSON("rec-a2", "{{WORKDIR}}/dirA", "2024-01-02T00:00:00Z")),
		harness.WithHomeFile(".vix/threads/open/rec-b.json",
			recordJSON("rec-b", "{{WORKDIR}}/dirB", "2024-01-01T00:00:00Z")),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	// The recent-directories list is populated asynchronously on launch.
	h.UI.WaitFor("Recent")
	h.UI.Shot("welcome-recent-dirs")

	if !h.UI.Contains("Recent") {
		t.Fatalf("welcome should label the recent-directories section; screen:\n%s", h.UI.Snapshot())
	}

	// Focus the welcome area (Tab), move to the second entry (dirB), and select
	// it. dirA is ranked first (index 0), dirB second (index 1).
	h.UI.Key("tab")
	h.UI.WaitStable(200 * time.Millisecond)
	h.UI.Key("down")
	h.UI.Key("enter")
	h.UI.WaitStable(200 * time.Millisecond)
	h.UI.Shot("dir-selected")

	// Enter already returned focus to the editor, so type and commit directly —
	// no extra Tab. If focus had stayed on the welcome viewport, these keystrokes
	// would not reach the input and the draft would never commit.
	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"note.txt","content":"in-dirB"}`),
		harness.Text("Wrote note.txt in the selected directory."),
	)
	h.UI.Type("write note.txt containing in-dirB")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Wrote note.txt")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-commit")

	// The write resolved against the selected recent directory (dirB), not the
	// launch cwd or dirA.
	if got := string(h.FS.Read("dirB/note.txt")); got != "in-dirB" {
		t.Fatalf("dirB/note.txt = %q, want %q (workdir selection did not take effect)", got, "in-dirB")
	}
	if h.FS.Exists("note.txt") {
		t.Fatal("note.txt should not be in the launch cwd — the thread cwd should be dirB")
	}
	if h.FS.Exists("dirA/note.txt") {
		t.Fatal("note.txt should not be in dirA — the selected directory was dirB")
	}
}
