package scenarios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// This file exercises the Threads tab: its chrome and shortcuts, the per-row
// loading / unread / waiting indicators, navigation + open, the tab-title
// highlight (a background message) vs. blink (a thread waiting for input), and
// the Vix-initiated group produced by a scheduled job. Each test drives the
// real TUI through tmux and acts as the model via the mock LLM.

const askQuestion = `{"questions":[{"id":"q1","category":"Choose","question":"Pick one please?","options":["Yes","No"]}]}`

func threadsMeta(desc string) harness.Meta {
	return harness.Meta{
		Category:    "ui",
		Subcategory: "ui.threads",
		Description: desc,
		Wire:        harness.WireMessages,
	}
}

// pollUntil polls cond up to timeout, returning true as soon as it holds.
func pollUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

// distinctFgOf samples the foreground color in effect where label renders, over
// a window, and returns the set of distinct values seen. Used to tell a static
// tint (one value) from a blink (two values, toggling). The sampling window is
// inherent to the behaviour under test — the tab blink toggles on a real timer.
func distinctFgOf(h *harness.Harness, label string, samples int, interval time.Duration) map[string]int {
	seen := map[string]int{}
	for i := 0; i < samples; i++ {
		if c, ok := h.UI.FgColorOf(label); ok {
			seen[c]++
		}
		time.Sleep(interval)
	}
	return seen
}

// TestThreadsTabChrome verifies F1 opens the Threads tab and that its static
// chrome renders: the group header, the column headers, and the footer
// shortcuts that advertise the tab's keys.
func TestThreadsTabChrome(t *testing.T) {
	h := harness.Start(t, threadsMeta("F1 opens the Threads tab; group/column headers and shortcut hints render"))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("threads-tab")

	for _, want := range []string{
		"Threads [F1]", "Workspace [F2]", // tab bar
		"User-initiated",             // group header
		"Thread", "Title", "Running", // column headers
		"New Thread", "Duplicate Thread", "Delete Thread", "Open / Fold Dir", // footer hints (Title Case as rendered)
	} {
		if !h.UI.Contains(want) {
			t.Fatalf("Threads tab missing %q; screen:\n%s", want, h.UI.Snapshot())
		}
	}
}

// TestThreadsLoadingSpinner verifies a thread that is actively working shows
// the loading spinner glyph in its row (and no waiting badge) on the Threads
// tab, and that the spinner clears once the turn completes.
func TestThreadsLoadingSpinner(t *testing.T) {
	h := harness.Start(t, threadsMeta("a busy thread shows the loading spinner on the Threads tab"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Start a turn but do not reply yet: the thread stays in-flight (busy).
	h.UI.Type("do something slow")
	h.UI.Enter()
	h.Mock.Next() // the request is received; the mock handler now parks.

	h.UI.Key("f1")
	if !pollUntil(8*time.Second, func() bool { return h.UI.Contains("⠋") }) {
		t.Fatalf("loading spinner glyph not shown for the busy thread; screen:\n%s", h.UI.Snapshot())
	}
	if h.UI.Contains("Waiting for input") {
		t.Fatalf("busy thread must not show the waiting badge; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("busy-spinner")

	// Finish the turn; the spinner must clear.
	h.Mock.Reply(harness.Text("all done now"))
	if !pollUntil(8*time.Second, func() bool { return !h.UI.Contains("⠋") }) {
		t.Fatalf("spinner did not clear after the turn completed; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("spinner-cleared")
}

// TestThreadsWaitingBadge verifies a thread waiting for user input shows the
// "Waiting for input" badge on the Threads tab.
func TestThreadsWaitingBadge(t *testing.T) {
	h := harness.Start(t, threadsMeta("a thread waiting for input shows the waiting badge on the Threads tab"))

	h.UI.WaitStable(500 * time.Millisecond)

	h.Mock.Enqueue(harness.ToolUse("ask_question_to_user", askQuestion))
	h.UI.Type("ask me a question")
	h.UI.Enter()
	h.UI.WaitFor("Pick one please?") // the question panel is up (StateUserQuestion).

	h.UI.Key("f1")
	h.UI.WaitFor("Waiting for input")
	h.UI.Shot("waiting-badge")
}

// TestThreadsUnreadIndicatorClears verifies the unread dot appears when a turn
// completes while the conversation isn't being viewed, and is removed once the
// conversation is selected and we return to the Threads list.
func TestThreadsUnreadIndicatorClears(t *testing.T) {
	h := harness.Start(t, threadsMeta("unread dot appears off-view and clears after the conversation is opened"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Start a turn, then leave the conversation (go to the Threads tab) before
	// it completes, so the completion lands as unread.
	h.UI.Type("background turn")
	h.UI.Enter()
	h.Mock.Next()
	h.UI.Key("f1")
	h.Mock.Reply(harness.Text("turn finished"))

	if !pollUntil(8*time.Second, func() bool { return h.UI.Contains("●") }) {
		t.Fatalf("unread dot not shown after off-view completion; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("unread-dot")

	// Open the conversation (enter on the selected row), then come back.
	h.UI.Enter()
	h.UI.WaitFor("turn finished")
	h.UI.Key("f1")
	if !pollUntil(8*time.Second, func() bool { return !h.UI.Contains("●") }) {
		t.Fatalf("unread dot not cleared after opening the conversation; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("unread-cleared")
}

// TestThreadsNavigateAndOpen verifies ↑/↓ move the selection and Enter opens
// the highlighted thread in the workspace.
func TestThreadsNavigateAndOpen(t *testing.T) {
	h := harness.Start(t, threadsMeta("arrow keys navigate rows; Enter opens the selected thread in the workspace"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Thread A (the initial one): one completed turn with a distinctive reply.
	h.Mock.Enqueue(harness.Text("ALPHA-REPLY"))
	h.UI.Type("alpha prompt")
	h.UI.Enter()
	h.UI.WaitFor("ALPHA-REPLY")

	// Thread B: a second thread with its own distinctive reply.
	h.UI.Ctrl('t')
	h.UI.WaitStable(800 * time.Millisecond)
	h.Mock.Enqueue(harness.Text("BRAVO-REPLY"))
	h.UI.Type("bravo prompt")
	h.UI.Enter()
	h.UI.WaitFor("BRAVO-REPLY")

	// On the Threads tab, the cursor syncs onto the active thread (B). Move up one
	// row onto A (the row directly above is A, not the directory header) and open it.
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	h.UI.Key("up")
	h.UI.Enter()
	if !pollUntil(8*time.Second, func() bool { return h.UI.Contains("ALPHA-REPLY") }) {
		t.Fatalf("Enter on the top row did not open thread A; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("opened-alpha")

	// Back to the list, move down one row (B) and open it.
	h.UI.Key("f1")
	h.UI.Key("down")
	h.UI.Enter()
	if !pollUntil(8*time.Second, func() bool { return h.UI.Contains("BRAVO-REPLY") }) {
		t.Fatalf("down+Enter did not open thread B; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("opened-bravo")
}

// TestThreadsCtrlPNavigatesBetweenThreads verifies the workspace thread cycle
// with two committed (live) threads: after creating A (send a turn), then B
// (send a turn), ctrl+p steps from the newer B back to the older A, and ctrl+n
// steps forward again. Both threads have real start times, so they sort
// [A, B] and the previous/next navigation is well-defined — the positive
// counterpart to the highlight scenario, which uses an uncommitted draft.
func TestThreadsCtrlPNavigatesBetweenThreads(t *testing.T) {
	h := harness.Start(t, threadsMeta("ctrl+p/ctrl+n cycle between two committed workspace threads"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Thread A (the initial one): one completed turn with a distinctive reply.
	h.Mock.Enqueue(harness.Text("ALPHA-REPLY"))
	h.UI.Type("alpha prompt")
	h.UI.Enter()
	h.UI.WaitFor("ALPHA-REPLY")

	// Thread B: created with ctrl+t, then a second committed turn. We are now
	// viewing B (its transcript shows BRAVO-REPLY, not ALPHA-REPLY).
	h.UI.Ctrl('t')
	h.UI.WaitStable(800 * time.Millisecond)
	h.Mock.Enqueue(harness.Text("BRAVO-REPLY"))
	h.UI.Type("bravo prompt")
	h.UI.Enter()
	h.UI.WaitFor("BRAVO-REPLY")

	// ctrl+p goes from B back to A: the workspace now shows A's transcript.
	h.UI.Ctrl('p')
	if !pollUntil(8*time.Second, func() bool {
		return h.UI.Contains("ALPHA-REPLY") && !h.UI.Contains("BRAVO-REPLY")
	}) {
		t.Fatalf("ctrl+p did not navigate from B to A; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("ctrlp-on-a")

	// ctrl+n goes forward from A back to B.
	h.UI.Ctrl('n')
	if !pollUntil(8*time.Second, func() bool {
		return h.UI.Contains("BRAVO-REPLY") && !h.UI.Contains("ALPHA-REPLY")
	}) {
		t.Fatalf("ctrl+n did not navigate from A to B; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("ctrln-on-b")
}

// TestThreadsTabHighlightOnBackgroundMessage verifies that, while the user is
// in the workspace on one thread, a message completing on another thread
// statically highlights (tints) the Threads tab title — and does not blink.
func TestThreadsTabHighlightOnBackgroundMessage(t *testing.T) {
	h := harness.Start(t, threadsMeta("a background message statically highlights the Threads tab title"))

	h.UI.WaitStable(500 * time.Millisecond)
	fgBase, ok := h.UI.FgColorOf("Threads [F1]")
	if !ok {
		t.Fatalf("could not read the Threads tab title color; screen:\n%s", h.UI.Snapshot())
	}

	// Create thread B, start a turn on it, then switch back to A (still in the
	// workspace) and let B's turn complete in the background. B has committed
	// (real start time) while A is still an uncommitted draft, so rows sort
	// [B, A] (drafts sort last) — the launch draft A is reached with ctrl+n
	// (next), not ctrl+p.
	h.UI.Ctrl('t')
	h.UI.WaitStable(800 * time.Millisecond)
	h.UI.Type("background work")
	h.UI.Enter()
	h.Mock.Next()
	h.UI.Ctrl('n') // to thread A (the draft, which sorts last); still on Workspace.
	h.Mock.Reply(harness.Text("background finished"))

	// The Threads title should tint (differ from the inactive baseline).
	if !pollUntil(8*time.Second, func() bool {
		c, ok := h.UI.FgColorOf("Threads [F1]")
		return ok && c != fgBase
	}) {
		t.Fatalf("Threads tab title was not highlighted after a background message (base=%q); screen:\n%s", fgBase, h.UI.Snapshot())
	}
	h.UI.Shot("tab-highlighted")

	// A plain background message tints statically — it must not blink: sampled
	// over a blink period the title color stays constant, and no thread is
	// waiting for input.
	seen := distinctFgOf(h, "Threads [F1]", 8, 150*time.Millisecond)
	if len(seen) != 1 {
		t.Fatalf("expected a stable (non-blinking) highlight, saw %d distinct colors: %v", len(seen), seen)
	}
}

// TestThreadsTabBlinkOnWaitingInput verifies that, with the same workspace
// setup, a background thread waiting for input makes the Threads tab title
// blink (its color toggles) — distinct from the static highlight above.
func TestThreadsTabBlinkOnWaitingInput(t *testing.T) {
	h := harness.Start(t, threadsMeta("a background thread waiting for input blinks the Threads tab title"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Thread B asks a question, then we switch back to A in the workspace,
	// leaving B waiting for input.
	h.UI.Ctrl('t')
	h.UI.WaitStable(800 * time.Millisecond)
	h.Mock.Enqueue(harness.ToolUse("ask_question_to_user", askQuestion))
	h.UI.Type("please ask")
	h.UI.Enter()
	h.UI.WaitFor("Pick one please?")
	h.UI.Ctrl('p') // back to thread A; B stays in the waiting state.

	// Sampled across a blink period, the title color toggles → ≥2 distinct
	// values. (A static highlight would yield exactly one.)
	seen := distinctFgOf(h, "Threads [F1]", 18, 120*time.Millisecond)
	if len(seen) < 2 {
		t.Fatalf("expected the Threads tab title to blink (≥2 colors), saw %d: %v; screen:\n%s", len(seen), seen, h.UI.Snapshot())
	}
	h.UI.Shot("tab-blinking")

	// And the waiting state is what drives it: the badge shows on the tab.
	h.UI.Key("f1")
	h.UI.WaitFor("Waiting for input")
	h.UI.Shot("waiting-on-list")
}

// TestThreadsTabNoBlinkWhileViewingAsker verifies that when the model asks a
// question in the thread the user is currently viewing (on the Workspace tab),
// the Threads tab title does NOT blink — the question panel is already on
// screen, so no alert is needed. This is the counterpart to
// TestThreadsTabBlinkOnWaitingInput, where the asker is a background thread.
func TestThreadsTabNoBlinkWhileViewingAsker(t *testing.T) {
	h := harness.Start(t, threadsMeta("no Threads-tab blink while viewing the thread that's asking"))

	h.UI.WaitStable(500 * time.Millisecond)

	// The single, currently-viewed thread asks a question and stays waiting,
	// with its panel on screen (we never switch away from the Workspace tab).
	h.Mock.Enqueue(harness.ToolUse("ask_question_to_user", askQuestion))
	h.UI.Type("please ask")
	h.UI.Enter()
	h.UI.WaitFor("Pick one please?")

	// Sampled across a blink period, the Threads title color stays constant —
	// exactly one distinct value. (A blink would toggle → ≥2.)
	seen := distinctFgOf(h, "Threads [F1]", 18, 120*time.Millisecond)
	if len(seen) != 1 {
		t.Fatalf("expected the Threads tab title to stay stable (no blink) while viewing the asker, saw %d colors: %v; screen:\n%s", len(seen), seen, h.UI.Snapshot())
	}
	h.UI.Shot("tab-no-blink-while-viewing")
}

// TestThreadsTitle verifies the Title column reflects the conversation: with no
// auto-title yet (a single turn is below the threshold), it falls back to the
// first user message.
func TestThreadsTitle(t *testing.T) {
	h := harness.Start(t, threadsMeta("the Title column falls back to the first user message before auto-titling"))

	h.UI.WaitStable(500 * time.Millisecond)

	h.Mock.Enqueue(harness.Text("acknowledged"))
	h.UI.Type("RENAME-THE-WIDGET")
	h.UI.Enter()
	h.UI.WaitFor("acknowledged")

	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	if !pollUntil(8*time.Second, func() bool { return h.UI.Contains("RENAME-THE-WIDGET") }) {
		t.Fatalf("Title column did not show the first user message; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("title-from-message")
}

// TestThreadsNewAndOrdering verifies `t` adds a thread from the Threads tab
// and that user-initiated rows render in creation order.
func TestThreadsNewAndOrdering(t *testing.T) {
	h := harness.Start(t, threadsMeta("`t` adds a thread; user rows render in creation order"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Give the initial thread (A) a distinctive first message.
	h.Mock.Enqueue(harness.Text("ack-one"))
	h.UI.Type("FIRST-THREAD")
	h.UI.Enter()
	h.UI.WaitFor("ack-one")

	// Add a second thread from the Threads tab with `t`, then give it a
	// distinctive first message too.
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	h.UI.Type("t")
	h.UI.WaitStable(800 * time.Millisecond)
	h.Mock.Enqueue(harness.Text("ack-two"))
	h.UI.Type("SECOND-THREAD")
	h.UI.Enter()
	h.UI.WaitFor("ack-two")

	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	snap := ""
	ok := pollUntil(8*time.Second, func() bool {
		snap = h.UI.Snapshot()
		return strings.Contains(snap, "FIRST-THREAD") && strings.Contains(snap, "SECOND-THREAD")
	})
	if !ok {
		t.Fatalf("both threads not listed; screen:\n%s", snap)
	}
	// Creation order: the first thread must appear above the second.
	if strings.Index(snap, "FIRST-THREAD") > strings.Index(snap, "SECOND-THREAD") {
		t.Fatalf("user rows not in creation order; screen:\n%s", snap)
	}
	h.UI.Shot("two-threads-ordered")
}

// TestThreadsDuplicate verifies `d` duplicates the selected thread: a second
// thread record lands on disk whose conversation is identical to the source's.
func TestThreadsDuplicate(t *testing.T) {
	h := harness.Start(t, threadsMeta("`d` writes a duplicate thread record identical to the source on disk"))

	h.UI.WaitStable(500 * time.Millisecond)

	// One completed turn is required before a thread can be duplicated, and it
	// gives the record a non-trivial conversation to compare.
	h.Mock.Enqueue(harness.Text("ok-to-fork"))
	h.UI.Type("seed turn")
	h.UI.Enter()
	h.UI.WaitFor("ok-to-fork")

	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	// The cursor syncs onto the seeded (active) thread; no navigation needed.
	h.UI.Type("d")

	openDir := h.HomePath(".vix", "threads", "open")
	var recs []threadRec
	if !pollUntil(8*time.Second, func() bool {
		recs = readThreadRecords(openDir)
		return len(recs) == 2 && len(recs[0].Messages) > 0 && len(recs[1].Messages) > 0
	}) {
		t.Fatalf("expected two thread records on disk after duplicate, got %d in %s", len(recs), openDir)
	}

	// The duplicate's conversation must be identical to the source's.
	if !jsonEqual(recs[0].Messages, recs[1].Messages) {
		t.Fatalf("duplicated thread is not identical to the source:\nA=%s\nB=%s", recs[0].Messages, recs[1].Messages)
	}
	h.UI.Shot("duplicated-thread")
}

// TestThreadsDuplicateOfDuplicate guards the duplicate-of-a-duplicate
// regression: a thread created by duplication had its messages seeded but its
// per-turn fork snapshots left empty, so duplicating it again produced a
// grandchild that started from an EMPTY conversation on the daemon even though
// the UI showed the copied history. Sending a message then went to the model
// with no prior context.
//
// It asserts, across disk and wire: every duplicated record carries the same
// non-empty history, and a follow-up from the grandchild is sent WITH the
// original turn rather than from scratch.
func TestThreadsDuplicateOfDuplicate(t *testing.T) {
	h := harness.Start(t, threadsMeta("duplicating a duplicate seeds the full history so a follow-up isn't sent from an empty conversation"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Seed one completed turn on the original thread so there is a turn to fork
	// and a distinctive user message ("seed turn") to look for on the wire.
	h.Mock.Enqueue(harness.Text("ok-to-fork"))
	h.UI.Type("seed turn")
	h.UI.Enter()
	h.UI.WaitFor("ok-to-fork")

	openDir := h.HomePath(".vix", "threads", "open")

	// First duplicate: source (top row) -> duplicate B. doDuplicate selects the
	// new thread and syncs the Threads cursor onto it, so the highlight is now
	// on B and we stay on the Threads tab.
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	// The cursor syncs onto the source (active) thread; no navigation needed.
	h.UI.Type("d")
	if !pollUntil(10*time.Second, func() bool { return len(readThreadRecords(openDir)) >= 2 }) {
		t.Fatalf("first duplicate never persisted; got %d records in %s", len(readThreadRecords(openDir)), openDir)
	}

	// Second duplicate: duplicate B into C. Rather than blind-press-and-retry
	// (which can over-duplicate into a fourth thread when a `d` lands before the
	// previous duplicate's record is observed), first prove B's client is live —
	// a connected thread appends "Reconnected to daemon." to its transcript —
	// then duplicate it exactly once. Opening B, then returning to the list,
	// re-syncs the cursor onto the active thread (B) via syncThreadsSelected.
	h.UI.Enter() // open the highlighted B
	h.UI.WaitFor("Reconnected to daemon.")
	h.UI.Key("f1") // back to the list; the cursor re-syncs onto B
	h.UI.WaitFor("User-initiated")
	h.UI.Type("d")
	if !pollUntil(10*time.Second, func() bool { return len(readThreadRecords(openDir)) >= 3 }) {
		t.Fatalf("duplicate-of-a-duplicate never persisted; got %d records in %s", len(readThreadRecords(openDir)), openDir)
	}
	h.UI.Shot("duplicate-of-duplicate")

	// Disk: every duplicated record must carry the seeded history. Before the fix
	// the grandchild's messages were empty.
	recs := readThreadRecords(openDir)
	for _, r := range recs {
		if !strings.Contains(string(r.Messages), "seed turn") {
			t.Fatalf("duplicated record id=%s (parent=%s) is missing the seeded history: %s", r.ID, r.ParentID, r.Messages)
		}
		if !jsonEqual(recs[0].Messages, r.Messages) {
			t.Fatalf("duplicated records diverge:\nfirst=%s\nid=%s=%s", recs[0].Messages, r.ID, r.Messages)
		}
	}

	// Wire: open the grandchild (the highlighted, newest thread) and send a
	// follow-up. The outgoing request must include the original turn — the exact
	// symptom the user hit ("starting from an empty discussion").
	h.UI.Enter()
	h.UI.WaitFor("ok-to-fork")
	// Wait until the grandchild's own fork-connection is live before the
	// follow-up, so the message goes through the seeded client rather than
	// committing a fresh (empty) draft thread.
	h.UI.WaitFor("Reconnected to daemon.")

	h.Mock.Enqueue(harness.Text("second-fork-reply"))
	h.UI.Type("continue please")
	h.UI.Enter()
	h.UI.WaitFor("second-fork-reply")

	reqs := h.Mock.Requests()
	last := string(reqs[len(reqs)-1].Body())
	if !strings.Contains(last, "continue please") {
		t.Fatalf("sanity: the last request is not the follow-up:\n%s", last)
	}
	if !strings.Contains(last, "seed turn") {
		t.Fatalf("duplicate-of-a-duplicate sent an empty history: the follow-up request omitted the original turn:\n%s", last)
	}
	h.UI.Shot("follow-up-carries-history")
}

// TestThreadsDuplicateWithStrayDraft guards the "appears duplicated but empty"
// bug: the fork/duplicate tab has no daemonThreadID yet, so the connection
// result must be correlated back to it by its stable clientKey. When another
// draft (an unsent Ctrl+T tab) already sits earlier in the list with an equally
// empty daemonThreadID, matching by daemon id would adopt the fork client onto
// that stray draft — leaving the duplicate showing the copied messages with no
// live daemon history behind them, so a follow-up goes to the model empty.
//
// This asserts, across disk and wire: the duplicate carries the seeded history
// even with the stray draft present, and its follow-up request includes the
// original turn.
func TestThreadsDuplicateWithStrayDraft(t *testing.T) {
	h := harness.Start(t, threadsMeta("duplicating with a stray empty draft present still seeds the duplicate's history (clientKey correlation)"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Seed one completed turn on the initial thread A so it commits (gains a
	// daemonThreadID) and has a distinctive "seed turn" message to look for.
	h.Mock.Enqueue(harness.Text("ok-to-fork"))
	h.UI.Type("seed turn")
	h.UI.Enter()
	h.UI.WaitFor("ok-to-fork")

	// Create a second tab B with Ctrl+T and leave it empty: an uncommitted draft
	// whose daemonThreadID stays "". It sits before the duplicate in the thread
	// list, so a daemon-id match would wrongly pick it up.
	h.UI.Ctrl('t')
	h.UI.WaitStable(800 * time.Millisecond)

	openDir := h.HomePath(".vix", "threads", "open")

	// Duplicate A. On the Threads tab A (committed) sorts above B (draft); select
	// the top row and press d. Only A is persisted so far (B is an empty draft).
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	h.UI.Key("up") // select A (top row)
	h.UI.Type("d")
	if !pollUntil(10*time.Second, func() bool { return len(readThreadRecords(openDir)) >= 2 }) {
		t.Fatalf("duplicate never persisted; got %d records in %s", len(readThreadRecords(openDir)), openDir)
	}
	h.UI.Shot("duplicate-with-stray-draft")

	// Disk: the duplicate must carry the seeded history. Before the fix it was
	// empty (the fork client was adopted by the stray draft instead).
	recs := readThreadRecords(openDir)
	for _, r := range recs {
		if !strings.Contains(string(r.Messages), "seed turn") {
			t.Fatalf("record id=%s (parent=%s) is missing the seeded history: %s", r.ID, r.ParentID, r.Messages)
		}
	}

	// Wire: open the duplicate (the highlighted, newest thread) and send a
	// follow-up. The outgoing request must include the original turn.
	h.UI.Enter()
	h.UI.WaitFor("ok-to-fork")
	// Wait until the duplicate's own fork-connection is live before the follow-up
	// so the message goes through the seeded client rather than an empty draft.
	h.UI.WaitFor("Reconnected to daemon.")

	h.Mock.Enqueue(harness.Text("stray-draft-reply"))
	h.UI.Type("continue please")
	h.UI.Enter()
	h.UI.WaitFor("stray-draft-reply")

	reqs := h.Mock.Requests()
	last := string(reqs[len(reqs)-1].Body())
	if !strings.Contains(last, "continue please") {
		t.Fatalf("sanity: the last request is not the follow-up:\n%s", last)
	}
	if !strings.Contains(last, "seed turn") {
		t.Fatalf("duplicate with a stray draft sent an empty history: the follow-up omitted the original turn:\n%s", last)
	}
	h.UI.Shot("stray-draft-follow-up-carries-history")
}

// TestThreadsDuplicateAfterRestart guards the "no completed turns yet" refusal
// on a restored thread. Turn separators (which gate /fork, /trim and duplicate)
// are UI-only markers appended live at turn end; they are not persisted nor
// re-sent in event.replay. Before the fix, a thread restored on relaunch had a
// transcript with zero separators, so pressing `d` on it was refused with
// "Nothing to duplicate: no completed turns yet" even though it had a completed
// turn. The replay path now reconstructs the separators, so duplicate works.
func TestThreadsDuplicateAfterRestart(t *testing.T) {
	h := harness.Start(t, threadsMeta("duplicating a thread restored after a daemon restart is not refused as having no completed turns"))

	h.UI.WaitStable(500 * time.Millisecond)

	// One completed turn so the daemon persists a forkable conversation.
	h.Mock.Enqueue(harness.Text("ok-to-fork"))
	h.UI.Type("seed turn")
	h.UI.Enter()
	h.UI.WaitFor("ok-to-fork")
	h.UI.WaitStable(300 * time.Millisecond)

	openDir := h.HomePath(".vix", "threads", "open")
	if len(readThreadRecords(openDir)) != 1 {
		t.Fatalf("expected exactly one record before restart, got %d", len(readThreadRecords(openDir)))
	}

	// Relaunch: restart the whole stack on the same HOME + socket. The TUI
	// auto-attaches the open thread and replays it (rebuilding the transcript
	// from event.replay — the path that lost the separators).
	h.Daemon.Restart()
	h.UI.WaitStable(700 * time.Millisecond)
	h.UI.WaitFor("ok-to-fork") // conversation replayed

	// Duplicate the restored thread from the Threads tab.
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	// The cursor syncs onto the restored (active) thread; no navigation needed.
	h.UI.Type("d")

	// Before the fix `d` was refused (no separators) and no new record appeared.
	// After the fix the duplicate persists with the seeded history.
	var recs []threadRec
	if !pollUntil(10*time.Second, func() bool {
		recs = readThreadRecords(openDir)
		return len(recs) == 2
	}) {
		t.Fatalf("duplicate after restart never persisted (still refused as having no completed turns); got %d records in %s\nscreen:\n%s",
			len(recs), openDir, h.UI.Snapshot())
	}
	if h.UI.Contains("Nothing to duplicate") {
		t.Fatalf("duplicate was refused after restart; screen:\n%s", h.UI.Snapshot())
	}
	for _, r := range recs {
		if !strings.Contains(string(r.Messages), "seed turn") {
			t.Fatalf("record id=%s missing the seeded history after restart-duplicate: %s", r.ID, r.Messages)
		}
	}
	h.UI.Shot("duplicate-after-restart")
}

// TestThreadsRename verifies the `r` rename flow: it opens a dialog, the typed
// title is persisted (Title + TitleManual on disk) and shown in the list, and
// re-opening the dialog pre-fills the current title.
func TestThreadsRename(t *testing.T) {
	h := harness.Start(t, threadsMeta("`r` opens a rename dialog; the new title is persisted and pre-fills on re-open"))

	h.UI.WaitStable(500 * time.Millisecond)

	// One turn commits the thread (gives it a live connection to rename over).
	h.Mock.Enqueue(harness.Text("committed"))
	h.UI.Type("hello there")
	h.UI.Enter()
	h.UI.WaitFor("committed")

	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	// The cursor syncs onto the committed (active) thread; no navigation needed.
	h.UI.Type("r")
	h.UI.WaitFor("Rename conversation")
	h.UI.Shot("rename-dialog")

	h.UI.Type("My Renamed Chat")
	h.UI.Enter()

	// Disk: the record carries the manual title and the pin flag.
	openDir := h.HomePath(".vix", "threads", "open")
	if !pollUntil(8*time.Second, func() bool {
		for _, r := range readThreadRecords(openDir) {
			if r.Title == "My Renamed Chat" && r.TitleManual {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("renamed title not persisted with TitleManual; records=%+v", readThreadRecords(openDir))
	}

	// Screen: the list shows the new title.
	h.UI.WaitFor("My Renamed Chat")

	// Re-opening the dialog pre-fills the current (renamed) title. The cursor is
	// still on the renamed thread.
	h.UI.Type("r")
	h.UI.WaitFor("Rename conversation")
	if !h.UI.Contains("My Renamed Chat") {
		t.Fatalf("rename dialog should pre-fill the current title; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Key("esc")
	h.UI.Shot("rename-prefill")
}

// TestThreadsRenameSuppressesAutoTitle verifies that once a conversation is
// manually renamed, the auto-titling pass never runs — even after crossing the
// 3-completed-turns threshold. It asserts on the wire (no summarization request
// is ever sent) and on disk (the manual title survives).
func TestThreadsRenameSuppressesAutoTitle(t *testing.T) {
	h := harness.Start(t, threadsMeta("a manual rename pins the title: auto-titling never runs after 3 turns"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Turn 1 commits the thread.
	h.Mock.Enqueue(harness.Text("reply one"))
	h.UI.Type("first message")
	h.UI.Enter()
	h.UI.WaitFor("reply one")

	// Rename it from the Threads tab.
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	// The cursor syncs onto the committed (active) thread; no navigation needed.
	h.UI.Type("r")
	h.UI.WaitFor("Rename conversation")
	h.UI.Type("Pinned Title")
	h.UI.Enter()
	h.UI.WaitFor("Pinned Title")

	// Open it back in the workspace and drive two more turns, crossing the
	// titleEndTurnThreshold (3). A non-pinned thread would auto-title here. The
	// cursor is still on the renamed thread, so Enter opens it.
	h.UI.Enter()
	h.UI.WaitFor("reply one") // transcript restored in the workspace

	h.Mock.Enqueue(harness.Text("reply two"))
	h.UI.Type("second message")
	h.UI.Enter()
	h.UI.WaitFor("reply two")

	h.Mock.Enqueue(harness.Text("reply three"))
	h.UI.Type("third message")
	h.UI.Enter()
	h.UI.WaitFor("reply three")
	h.UI.WaitStable(500 * time.Millisecond) // let any (unwanted) async title pass run

	// Wire: no summarization request was ever sent.
	for _, req := range h.Mock.Requests() {
		if strings.Contains(string(req.Body()), "Summarize the following conversation") {
			t.Fatalf("auto-titling ran after a manual rename — a summarization request was sent:\n%s", req.Body())
		}
	}

	// Disk: the manual title survived and stays pinned.
	openDir := h.HomePath(".vix", "threads", "open")
	recs := readThreadRecords(openDir)
	if len(recs) != 1 {
		t.Fatalf("want exactly one record, got %d", len(recs))
	}
	if recs[0].Title != "Pinned Title" || !recs[0].TitleManual {
		t.Fatalf("record title=%q manual=%v, want Pinned Title/true", recs[0].Title, recs[0].TitleManual)
	}
	h.UI.Shot("rename-suppresses-autotitle")
}

// unreadThreadRecord is a persisted open thread marked unread, seeded into
// open/ before launch. {{WORKDIR}} is expanded to the per-test cwd so it is
// auto-restored on launch (thread.list returns all directories; launch attaches
// the ones rooted at the current cwd).
const unreadThreadRecord = `{
  "schema_version": 1,
  "id": "22222222-2222-2222-2222-222222222222",
  "cwd": "{{WORKDIR}}",
  "session_mode": "chat",
  "unread": true,
  "started_at": "2024-01-02T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "UNREAD-ONE"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "unread reply"}]}
  ]
}`

// readThreadRecordSeed is an older, already-read thread. Being the oldest, it
// becomes the focused initial thread on launch, leaving the unread one to be
// restored in the background (where its unread state survives).
const readThreadRecordSeed = `{
  "schema_version": 1,
  "id": "11111111-1111-1111-1111-111111111111",
  "cwd": "{{WORKDIR}}",
  "session_mode": "chat",
  "started_at": "2024-01-01T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "OLD-READ"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "old reply"}]}
  ]
}`

// TestThreadsUnreadOnLaunch verifies that launching with an unread thread in
// open/ highlights the Threads tab title and shows the unread marker for that
// thread in the list.
func TestThreadsUnreadOnLaunch(t *testing.T) {
	h := harness.Start(t, threadsMeta("an unread thread in open/ highlights the Threads tab and marks the row on launch"),
		harness.WithHomeFile(".vix/threads/open/11111111-1111-1111-1111-111111111111.json", readThreadRecordSeed),
		harness.WithHomeFile(".vix/threads/open/22222222-2222-2222-2222-222222222222.json", unreadThreadRecord),
	)

	// On launch the TUI opens on the Workspace tab; the background-restored
	// unread thread tints the (inactive) Threads title. Compare against the
	// Models tab title, which stays the plain inactive color — they must differ.
	highlighted := pollUntil(12*time.Second, func() bool {
		fgThreads, ok1 := h.UI.FgColorOf("Threads [F1]")
		fgModels, ok2 := h.UI.FgColorOf("Models [F3]")
		return ok1 && ok2 && fgThreads != fgModels
	})
	if !highlighted {
		fs, _ := h.UI.FgColorOf("Threads [F1]")
		fm, _ := h.UI.FgColorOf("Models [F3]")
		t.Fatalf("Threads tab not highlighted on launch (Threads fg=%q, Models fg=%q); screen:\n%s", fs, fm, h.UI.Snapshot())
	}
	h.UI.Shot("tab-highlighted-on-launch")

	// Visiting the Threads tab clears the title highlight but keeps the per-row
	// unread marker, which must show on the restored unread thread.
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	if !pollUntil(8*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "●") && strings.Contains(s, "UNREAD-ONE")
	}) {
		t.Fatalf("unread marker not shown for the restored thread; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("unread-marker-in-list")
}

// legacyThreadRecord is a persisted open thread written under the pre-rename
// sessions/ store. Launching vixd must migrate sessions/ -> threads/ so the
// record surfaces in the Threads tab. Note the on-disk JSON keeps the legacy
// "session_mode" key, which the daemon still decodes after the rename.
const legacyThreadRecord = `{
  "schema_version": 1,
  "id": "aaaaaaaa-1111-2222-3333-444444444444",
  "cwd": "{{WORKDIR}}",
  "session_mode": "chat",
  "started_at": "2024-01-01T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "LEGACY-MIGRATED"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "hello from before the rename"}]}
  ]
}`

// TestSessionsDirMigrates seeds a record under the old
// ~/.vix/sessions/open store and asserts the daemon's one-time startup
// migration relocates it to ~/.vix/threads/open: it appears in the Threads tab
// and the legacy sessions/ directory is gone.
func TestSessionsDirMigrates(t *testing.T) {
	h := harness.Start(t, threadsMeta("a record under the legacy sessions/ store migrates to threads/ and shows in the Threads tab"),
		harness.WithHomeFile(".vix/sessions/open/aaaaaaaa-1111-2222-3333-444444444444.json", legacyThreadRecord),
	)

	// The migrated record is restored on launch; open the Threads tab and
	// confirm its first message renders.
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	if !pollUntil(10*time.Second, func() bool {
		return strings.Contains(h.UI.Snapshot(), "LEGACY-MIGRATED")
	}) {
		t.Fatalf("migrated legacy thread not shown in Threads tab; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("legacy-thread-migrated")

	// The store was physically moved: threads/open holds the record and the
	// legacy sessions/ dir no longer exists.
	if _, err := os.Stat(filepath.Join(h.HomePath(".vix", "threads", "open"), "aaaaaaaa-1111-2222-3333-444444444444.json")); err != nil {
		t.Fatalf("record not found under threads/open after migration: %v", err)
	}
	if _, err := os.Stat(h.HomePath(".vix", "sessions")); !os.IsNotExist(err) {
		t.Errorf("legacy sessions/ dir still present after migration (err=%v)", err)
	}
}

// threadRec is the slice of a persisted thread record this suite inspects.
type threadRec struct {
	ID          string          `json:"id"`
	ParentID    string          `json:"parent_id"`
	Title       string          `json:"title"`
	TitleManual bool            `json:"title_manual"`
	Messages    json.RawMessage `json:"messages"`
}

// readThreadRecords parses every *.json thread record in dir.
func readThreadRecords(dir string) []threadRec {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []threadRec
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r threadRec
		if json.Unmarshal(data, &r) != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// jsonEqual reports whether two JSON documents are semantically equal
// (independent of key order / formatting).
func jsonEqual(a, b json.RawMessage) bool {
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// TestThreadsDeleteConfirm verifies `x` opens the close-confirmation dialog and
// that declining keeps the thread.
func TestThreadsDeleteConfirm(t *testing.T) {
	h := harness.Start(t, threadsMeta("`x` opens the close-confirmation dialog; declining keeps the thread"))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")

	h.UI.Type("x")
	h.UI.WaitFor("Close thread?")
	if !h.UI.Contains("The thread will be terminated.") {
		t.Fatalf("close dialog body not shown; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("close-confirm")

	// "No" is the default; Enter dismisses without closing.
	h.UI.Enter()
	if !pollUntil(5*time.Second, func() bool { return !h.UI.Contains("Close thread?") }) {
		t.Fatalf("close dialog did not dismiss; screen:\n%s", h.UI.Snapshot())
	}
	if !h.UI.Contains("User-initiated") {
		t.Fatalf("thread row gone after declining close; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("close-declined")
}

// jobSpec is a one-shot scheduled job whose fire time is in the past, so the
// scheduler runs it immediately at startup. The run executes against the mock
// and persists a Vix-initiated thread record.
const jobSpec = `{
  "id": "e2e-demo",
  "name": "E2E Demo",
  "enabled": true,
  "trigger": {"type": "at", "time": "2000-01-01T00:00:00Z"},
  "prompt": "Say hello.",
  "cwd": "{{WORKDIR}}",
  "created_by": "vix"
}`

// TestThreadsVixInitiated verifies that a scheduled job run lands in the
// Threads tab's Vix-initiated group, labelled with its run title.
func TestThreadsVixInitiated(t *testing.T) {
	h := harness.Start(t, threadsMeta("a scheduled job run appears in the Vix-initiated group"),
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-demo/job.json", jobSpec),
	)

	// The job fires at startup and runs against the mock (one turn → persisted).
	h.Mock.Enqueue(harness.Text("hello from the scheduled job"))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	// The Vix-initiated row is labelled with the job's run title ("<name> -
	// <timestamp>"), derived from the job's name "E2E Demo".
	if !pollUntil(20*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "Vix-initiated") && strings.Contains(s, "E2E Demo")
	}) {
		t.Fatalf("Vix-initiated job run not listed; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("vix-initiated")
}

// vixAlignErrorRecord and vixAlignOkRecord are two persisted Vix-initiated run
// records seeded into open/: a failed run (job_status "error" → the ⚠ marker in
// the Title column) and a successful one (no marker). They guard the Threads-tab
// column alignment fix — the warning marker is exactly one display cell wide, so
// the Running column must line up across both rows. {{WORKDIR}} is the per-test
// cwd so the seeded records are shown.
const vixAlignErrorRecord = `{
  "schema_version": 1,
  "id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
  "cwd": "{{WORKDIR}}",
  "session_mode": "chat",
  "origin": "vix",
  "trigger": {"type": "cron", "ref": "align-job"},
  "job_status": "error",
  "title": "ALIGN-ERR-ROW",
  "started_at": "2024-01-01T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "err run"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "boom"}]}
  ]
}`

const vixAlignOkRecord = `{
  "schema_version": 1,
  "id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
  "cwd": "{{WORKDIR}}",
  "session_mode": "chat",
  "origin": "vix",
  "trigger": {"type": "cron", "ref": "align-job"},
  "job_status": "ok",
  "title": "ALIGN-OK-ROW",
  "started_at": "2024-01-02T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "ok run"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "fine"}]}
  ]
}`

// runeCol returns the display column (rune offset) at which sub first appears in
// line, or -1. The Threads-list rows it inspects contain only width-1 glyphs
// (ASCII plus the one-cell ⚠ marker), so a rune offset equals the screen column.
func runeCol(line, sub string) int {
	i := strings.Index(line, sub)
	if i < 0 {
		return -1
	}
	return len([]rune(line[:i]))
}

// TestThreadsVixInitiatedAlignment guards the Threads-tab column alignment: a
// failed Vix-initiated run is flagged with a ⚠ marker in the Title column, and
// the marker must not shift the Running column relative to an unflagged row. The
// marker is the plain warning sign (U+26A0, no U+FE0F): one display cell that
// lipgloss and the terminal agree on. The emoji-presentation "⚠️" measures two
// cells in lipgloss but renders as one here, which used to push the Running
// column left on flagged rows.
func TestThreadsVixInitiatedAlignment(t *testing.T) {
	h := harness.Start(t, threadsMeta("the ⚠ marker keeps the Running column aligned across failed and successful Vix-initiated rows"),
		harness.WithHomeFile(".vix/threads/open/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa.json", vixAlignErrorRecord),
		harness.WithHomeFile(".vix/threads/open/bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb.json", vixAlignOkRecord),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	h.UI.WaitFor("Vix-initiated")

	var errLine, okLine string
	if !pollUntil(8*time.Second, func() bool {
		errLine, okLine = "", ""
		for _, ln := range strings.Split(h.UI.Snapshot(), "\n") {
			if strings.Contains(ln, "ALIGN-ERR-ROW") {
				errLine = ln
			}
			if strings.Contains(ln, "ALIGN-OK-ROW") {
				okLine = ln
			}
		}
		return errLine != "" && okLine != ""
	}) {
		t.Fatalf("both Vix-initiated rows not listed; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("vix-initiated-alignment")

	// Only the failed row carries the warning marker.
	if !strings.Contains(errLine, "⚠") {
		t.Fatalf("failed row missing the ⚠ marker: %q", errLine)
	}
	if strings.Contains(okLine, "⚠") {
		t.Fatalf("successful row should not carry the ⚠ marker: %q", okLine)
	}

	// The Running column ("0s ago", frozen by VIX_TEST_RENDER) must start at the
	// same screen column on both rows — the regression under test.
	errCol := runeCol(errLine, "0s ago")
	okCol := runeCol(okLine, "0s ago")
	if errCol < 0 || okCol < 0 {
		t.Fatalf("Running column not found (err=%d ok=%d):\nERR %q\nOK  %q", errCol, okCol, errLine, okLine)
	}
	if errCol != okCol {
		t.Fatalf("Running column misaligned: ⚠ row at col %d, clean row at col %d\nERR %q\nOK  %q", errCol, okCol, errLine, okLine)
	}
}

// inlineWorkflowJobSpec is a one-shot job that runs a self-contained inline
// workflow (no entry in config/workflow.json). The single agent step streams a
// reply through the mock, so the run produces a persisted Vix-initiated thread
// — proving the inline-workflow dispatch path end-to-end.
const inlineWorkflowJobSpec = `{
  "id": "e2e-inline",
  "name": "E2E Inline",
  "enabled": true,
  "trigger": {"type": "at", "time": "2000-01-01T00:00:00Z"},
  "prompt": "Say hello from the inline workflow.",
  "workflow": {
    "name": "e2e-inline-wf",
    "entry_point": {"id": "do"},
    "steps": {
      "do": {"type": "agent", "agent": "general", "prompt": "$(workflow.prompt)"}
    }
  },
  "cwd": "{{WORKDIR}}",
  "created_by": "vix"
}`

// TestThreadsVixInitiatedInlineWorkflow verifies that a scheduled job carrying
// an inline workflow definition (rather than a named workflow_id) runs that
// workflow and lands in the Vix-initiated group.
func TestThreadsVixInitiatedInlineWorkflow(t *testing.T) {
	h := harness.Start(t, threadsMeta("a scheduled job with an inline workflow runs and appears in the Vix-initiated group"),
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-inline/job.json", inlineWorkflowJobSpec),
	)

	// The inline workflow's single agent step calls the mock once.
	h.Mock.Enqueue(harness.Text("hello from the inline workflow step"))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	// The Vix-initiated row is labelled with the job's run title ("<name> -
	// <timestamp>"), derived from the job's name "E2E Inline".
	if !pollUntil(20*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "Vix-initiated") && strings.Contains(s, "E2E Inline")
	}) {
		t.Fatalf("Vix-initiated inline-workflow run not listed; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("vix-initiated-inline")
}

// TestThreadsListRefreshesInDraft proves the instance control channel end to
// end: a launch-time draft window (no thread ever started) is watching the
// Threads tab when a job is fired out of band via `vix job run`. The run's
// persisted Vix-initiated record must appear live — pushed over the window's
// control connection (event.threads_changed → fetchVixThreads), never by
// re-entering the tab or sending a first message. Reuses onDemandJobSpec (a
// future-dated job the scheduler never fires on its own; see run_trigger_test.go).
func TestThreadsListRefreshesInDraft(t *testing.T) {
	h := harness.Start(t, threadsMeta("a draft window's Threads tab refreshes live when a job is fired via `vix job run`"),
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-ondemand/job.json", onDemandJobSpec),
	)

	// Stay a draft (never send a first message). Open the Threads tab; the
	// initial fetch runs while no Vix-initiated record exists yet.
	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	h.UI.WaitStable(500 * time.Millisecond)
	if h.UI.Contains("E2E On-Demand") {
		t.Fatalf("Vix-initiated run present before the job was fired; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("draft-before-run")

	// Fire the job out of band. Its single turn calls the mock once and persists
	// a Vix-initiated thread, which broadcasts threads_changed to the window's
	// control channel.
	h.Mock.Enqueue(harness.Text("hello on demand"))
	if out, err := h.RunCLI("job", "run", "e2e-ondemand"); err != nil {
		t.Fatalf("vix job run failed: %v\n%s", err, out)
	}

	// The row must appear live while the window is still a draft on the Threads
	// tab — no tab re-entry, no first message. This only happens if the control
	// channel delivered event.threads_changed.
	if !pollUntil(20*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "Vix-initiated") && strings.Contains(s, "E2E On-Demand")
	}) {
		t.Fatalf("draft window did not refresh the Vix-initiated group after `vix job run`; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("draft-refreshed-live")
}

// groupUserCurrentRecord and groupUserOtherRecord are two persisted, user-initiated
// open threads in different working directories: one rooted at the launch cwd
// (auto-restored as a live thread) and one rooted at a sibling directory
// ({{WORKDIR}}/otherproj), which stays a not-attached record. They prove the
// Threads tab now surfaces threads from every directory (no cwd filter),
// grouped by working directory with a path subtitle for each.
const groupUserCurrentRecord = `{
  "schema_version": 1,
  "id": "cccccccc-cccc-cccc-cccc-cccccccccccc",
  "cwd": "{{WORKDIR}}",
  "session_mode": "chat",
  "started_at": "2024-01-01T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "CURRENT-DIR-THREAD"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "current dir reply"}]}
  ]
}`

const groupUserOtherRecord = `{
  "schema_version": 1,
  "id": "dddddddd-dddd-dddd-dddd-dddddddddddd",
  "cwd": "{{WORKDIR}}/otherproj",
  "session_mode": "chat",
  "started_at": "2024-01-02T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "OTHER-DIR-THREAD"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "other dir reply"}]}
  ]
}`

// TestThreadsGroupedByDirectory verifies the User-initiated group lists threads
// from every working directory (not just the launch cwd), grouped by directory
// with a per-directory path subtitle, and that opening a thread rooted in
// another directory attaches it (its conversation replays in the workspace).
func TestThreadsGroupedByDirectory(t *testing.T) {
	h := harness.Start(t, threadsMeta("the Threads tab groups user threads from every directory under a path subtitle; opening a cross-directory thread attaches it"),
		// A sibling directory to root the other-directory thread at.
		harness.WithWorkdirFile("otherproj/keep.txt", "seed"),
		harness.WithHomeFile(".vix/threads/open/cccccccc-cccc-cccc-cccc-cccccccccccc.json", groupUserCurrentRecord),
		harness.WithHomeFile(".vix/threads/open/dddddddd-dddd-dddd-dddd-dddddddddddd.json", groupUserOtherRecord),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")

	// Both directories' threads must be listed — the cross-directory thread is
	// the behavior the cwd-filter removal enables.
	if !pollUntil(10*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "CURRENT-DIR-THREAD") && strings.Contains(s, "OTHER-DIR-THREAD")
	}) {
		t.Fatalf("both directories' threads not listed; screen:\n%s", h.UI.Snapshot())
	}
	// The other directory's path subtitle is shown (always rendered per group).
	if !h.UI.Contains("otherproj") {
		t.Fatalf("per-directory path subtitle (otherproj) not shown; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("threads-grouped-by-dir")

	// The current-cwd thread (auto-restored, live) sorts above the other
	// directory's not-attached record.
	snap := h.UI.Snapshot()
	if strings.Index(snap, "CURRENT-DIR-THREAD") > strings.Index(snap, "OTHER-DIR-THREAD") {
		t.Fatalf("current cwd block should render above the other directory; screen:\n%s", snap)
	}

	// Opening the cross-directory thread attaches it in its own directory. The
	// cursor syncs onto the current-dir thread; move down past the other
	// directory's header onto its thread row and open it. Its conversation must
	// replay in the workspace.
	h.UI.Key("down")
	h.UI.Key("down")
	h.UI.Enter()
	if !pollUntil(10*time.Second, func() bool { return h.UI.Contains("other dir reply") }) {
		t.Fatalf("opening the cross-directory thread did not replay its conversation; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("cross-dir-thread-opened")
}

// TestThreadsFoldDirectory verifies the foldable directory headers on the
// Threads tab: pressing Enter on a directory's path header hides that
// directory's thread rows (the header stays), and pressing Enter again unfolds
// them. Fold state is session-only. It reuses the two-directory seed from
// TestThreadsGroupedByDirectory so folding one block leaves the other visible.
func TestThreadsFoldDirectory(t *testing.T) {
	h := harness.Start(t, threadsMeta("Enter on a directory path header folds/unfolds that directory's threads"),
		harness.WithWorkdirFile("otherproj/keep.txt", "seed"),
		harness.WithHomeFile(".vix/threads/open/cccccccc-cccc-cccc-cccc-cccccccccccc.json", groupUserCurrentRecord),
		harness.WithHomeFile(".vix/threads/open/dddddddd-dddd-dddd-dddd-dddddddddddd.json", groupUserOtherRecord),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	if !pollUntil(10*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "CURRENT-DIR-THREAD") && strings.Contains(s, "OTHER-DIR-THREAD")
	}) {
		t.Fatalf("both directories' threads not listed; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("fold-before")

	// Selectable rows: 0=cwd header, 1=CURRENT, 2=otherproj header, 3=OTHER.
	// Clamp to the very top with repeated up (a header is a valid stop but Enter
	// only folds — up never triggers it), then step down twice onto the otherproj
	// header deterministically.
	h.UI.Key("up")
	h.UI.Key("up")
	h.UI.Key("up")
	h.UI.Key("down")
	h.UI.Key("down")
	h.UI.Enter() // fold the otherproj block

	// The other directory's thread row is hidden; its header (otherproj) and the
	// other directory's thread stay visible.
	if !pollUntil(8*time.Second, func() bool {
		s := h.UI.Snapshot()
		return !strings.Contains(s, "OTHER-DIR-THREAD") &&
			strings.Contains(s, "otherproj") &&
			strings.Contains(s, "CURRENT-DIR-THREAD")
	}) {
		t.Fatalf("folding the otherproj block did not hide its thread row; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("fold-collapsed")

	// Enter again on the same header unfolds it: the thread row reappears.
	h.UI.Enter()
	if !pollUntil(8*time.Second, func() bool {
		return strings.Contains(h.UI.Snapshot(), "OTHER-DIR-THREAD")
	}) {
		t.Fatalf("unfolding the otherproj block did not restore its thread row; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("fold-expanded")
}

// TestThreadsFoldWithSpace verifies that Space folds/unfolds a directory the
// same way Enter does, and that it works even when the cursor is on a thread row
// (it folds that row's enclosing directory). It reuses the two-directory seed
// from TestThreadsGroupedByDirectory.
func TestThreadsFoldWithSpace(t *testing.T) {
	h := harness.Start(t, threadsMeta("Space folds/unfolds a directory, including from a thread row under it"),
		harness.WithWorkdirFile("otherproj/keep.txt", "seed"),
		harness.WithHomeFile(".vix/threads/open/cccccccc-cccc-cccc-cccc-cccccccccccc.json", groupUserCurrentRecord),
		harness.WithHomeFile(".vix/threads/open/dddddddd-dddd-dddd-dddd-dddddddddddd.json", groupUserOtherRecord),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	if !pollUntil(10*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "CURRENT-DIR-THREAD") && strings.Contains(s, "OTHER-DIR-THREAD")
	}) {
		t.Fatalf("both directories' threads not listed; screen:\n%s", h.UI.Snapshot())
	}

	// Selectable rows: 0=cwd header, 1=CURRENT, 2=otherproj header, 3=OTHER.
	// Land on the OTHER-DIR-THREAD row (a thread row, not a header).
	h.UI.Key("up")
	h.UI.Key("up")
	h.UI.Key("up")
	h.UI.Key("down")
	h.UI.Key("down")
	h.UI.Key("down")

	// Space on the thread row folds its enclosing otherproj directory.
	h.UI.Key("space")
	if !pollUntil(8*time.Second, func() bool {
		s := h.UI.Snapshot()
		return !strings.Contains(s, "OTHER-DIR-THREAD") &&
			strings.Contains(s, "otherproj") &&
			strings.Contains(s, "CURRENT-DIR-THREAD")
	}) {
		t.Fatalf("Space on a thread row did not fold its enclosing directory; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("space-collapsed")

	// The cursor re-anchored on the otherproj header, so Space again unfolds it.
	h.UI.Key("space")
	if !pollUntil(8*time.Second, func() bool {
		return strings.Contains(h.UI.Snapshot(), "OTHER-DIR-THREAD")
	}) {
		t.Fatalf("Space did not unfold the otherproj block; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("space-expanded")
}

// TestThreadsLeftArrowSelectsDir verifies that the Left arrow moves the cursor
// from a thread row up to its enclosing directory header. It proves the move by
// pressing Enter afterwards: Enter on a header folds the block (whereas Enter on
// a thread row would open the thread), so the thread row disappearing confirms
// the cursor landed on the header.
func TestThreadsLeftArrowSelectsDir(t *testing.T) {
	h := harness.Start(t, threadsMeta("Left arrow moves the cursor from a thread row to its enclosing directory header"),
		harness.WithWorkdirFile("otherproj/keep.txt", "seed"),
		harness.WithHomeFile(".vix/threads/open/cccccccc-cccc-cccc-cccc-cccccccccccc.json", groupUserCurrentRecord),
		harness.WithHomeFile(".vix/threads/open/dddddddd-dddd-dddd-dddd-dddddddddddd.json", groupUserOtherRecord),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	if !pollUntil(10*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "CURRENT-DIR-THREAD") && strings.Contains(s, "OTHER-DIR-THREAD")
	}) {
		t.Fatalf("both directories' threads not listed; screen:\n%s", h.UI.Snapshot())
	}

	// Land on the OTHER-DIR-THREAD row (row 3).
	h.UI.Key("up")
	h.UI.Key("up")
	h.UI.Key("up")
	h.UI.Key("down")
	h.UI.Key("down")
	h.UI.Key("down")

	// Left moves the cursor up to the otherproj header; Enter then folds it.
	h.UI.Key("left")
	h.UI.Enter()
	if !pollUntil(8*time.Second, func() bool {
		s := h.UI.Snapshot()
		return !strings.Contains(s, "OTHER-DIR-THREAD") &&
			strings.Contains(s, "otherproj") &&
			strings.Contains(s, "CURRENT-DIR-THREAD")
	}) {
		t.Fatalf("Left did not move the cursor to the enclosing dir header (Enter did not fold); screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("left-then-fold")
}

// orderCurrentRecord is the launch-cwd thread (auto-restored as a live thread).
// otherOldRecord and otherNewRecord are two persisted threads in the same
// sibling directory ({{WORKDIR}}/otherproj), created on 2024-01-01 and
// 2024-01-03 respectively. The test opens the NEWER one so it attaches (becomes
// live) and verifies it still renders below the older, not-attached record —
// i.e. rows order by creation time, a live thread is not hoisted to the top.
const orderCurrentRecord = `{
  "schema_version": 1,
  "id": "c1111111-1111-1111-1111-111111111111",
  "cwd": "{{WORKDIR}}",
  "session_mode": "chat",
  "started_at": "2024-01-02T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "CURRENT-DIR-THREAD"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "current dir reply"}]}
  ]
}`

const orderOtherOldRecord = `{
  "schema_version": 1,
  "id": "a1111111-1111-1111-1111-111111111111",
  "cwd": "{{WORKDIR}}/otherproj",
  "session_mode": "chat",
  "started_at": "2024-01-01T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "OTHER-OLD-THREAD"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "other old reply"}]}
  ]
}`

const orderOtherNewRecord = `{
  "schema_version": 1,
  "id": "b1111111-1111-1111-1111-111111111111",
  "cwd": "{{WORKDIR}}/otherproj",
  "session_mode": "chat",
  "started_at": "2024-01-03T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "OTHER-NEW-THREAD"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "other new reply"}]}
  ]
}`

// TestThreadsOrderedByCreationTime verifies that within a working-directory
// block the rows are ordered by creation time — a live (attached) thread is NOT
// hoisted above an older not-attached record. It seeds two threads in a sibling
// directory (old = 2024-01-01, new = 2024-01-03), opens the newer one so it
// attaches (becomes live), and asserts the older record still renders above it.
func TestThreadsOrderedByCreationTime(t *testing.T) {
	h := harness.Start(t, threadsMeta("within a directory block, rows order by creation time; opening the newer thread does not hoist it above the older record"),
		harness.WithWorkdirFile("otherproj/keep.txt", "seed"),
		harness.WithHomeFile(".vix/threads/open/c1111111-1111-1111-1111-111111111111.json", orderCurrentRecord),
		harness.WithHomeFile(".vix/threads/open/a1111111-1111-1111-1111-111111111111.json", orderOtherOldRecord),
		harness.WithHomeFile(".vix/threads/open/b1111111-1111-1111-1111-111111111111.json", orderOtherNewRecord),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")

	// Both otherproj threads are listed as not-attached records, older above newer.
	if !pollUntil(10*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "OTHER-OLD-THREAD") && strings.Contains(s, "OTHER-NEW-THREAD")
	}) {
		t.Fatalf("both otherproj threads not listed; screen:\n%s", h.UI.Snapshot())
	}
	snap := h.UI.Snapshot()
	if strings.Index(snap, "OTHER-OLD-THREAD") > strings.Index(snap, "OTHER-NEW-THREAD") {
		t.Fatalf("older record should render above newer record; screen:\n%s", snap)
	}
	h.UI.Shot("order-before-attach")

	// Open the NEWER otherproj thread so it attaches (becomes live). Selectable
	// rows: 0=cwd header, 1=CURRENT (cwd), 2=otherproj header, 3=OTHER-OLD,
	// 4=OTHER-NEW. The cursor syncs onto CURRENT; move down three times onto the
	// newer row and open it.
	h.UI.Key("down")
	h.UI.Key("down")
	h.UI.Key("down")
	h.UI.Enter()
	if !pollUntil(10*time.Second, func() bool { return h.UI.Contains("other new reply") }) {
		t.Fatalf("opening the newer thread did not replay its conversation; screen:\n%s", h.UI.Snapshot())
	}

	// Back to the Threads tab: the newer thread is now live, but must still
	// render BELOW the older not-attached record (ordered by creation time, not
	// live-first). Before the fix, the live row was hoisted to the top.
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	if !pollUntil(10*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "OTHER-OLD-THREAD") && strings.Contains(s, "OTHER-NEW-THREAD")
	}) {
		t.Fatalf("both otherproj rows not listed after attach; screen:\n%s", h.UI.Snapshot())
	}
	snap = h.UI.Snapshot()
	if strings.Index(snap, "OTHER-OLD-THREAD") > strings.Index(snap, "OTHER-NEW-THREAD") {
		t.Fatalf("attached (live) newer thread was hoisted above the older record; rows must order by creation time; screen:\n%s", snap)
	}
	h.UI.Shot("order-after-attach")
}
