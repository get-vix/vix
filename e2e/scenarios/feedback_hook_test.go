package scenarios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// Feedback-hook e2e scenarios. vixd seeds a shipped ThreadStart hook
// (feedback-at-10) the first time it runs (tracked by a sentinel). It counts
// fresh threads and, on the 10th, opens a one-time "Vix-initiated" conversation
// by calling back into the daemon via `vix thread create` — exercising the
// vix_bin/socket_path hook-context fields end to end.
//
// To control the threshold without driving 10 real threads, each test seeds
// the hook's counter at ~/.vix/hooks/feedback-at-10/count.log. The seed sentinel
// is NOT pre-written, so the daemon still seeds the real shipped hook under test.

const feedbackTitle = "vix needs your feedback"

type feedbackRec struct {
	Origin   string `json:"origin"`
	Title    string `json:"title"`
	Messages []struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
}

// feedbackThreads returns the persisted Vix-initiated feedback records.
func feedbackThreads(h *harness.Harness) []feedbackRec {
	dir := h.HomePath(".vix/threads/open")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []feedbackRec
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r feedbackRec
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		if r.Origin == "vix" && r.Title == feedbackTitle {
			out = append(out, r)
		}
	}
	return out
}

// countLogLines returns the number of recorded threads in the feedback counter.
func countLogLines(h *harness.Harness) int {
	b, err := os.ReadFile(h.HomePath(".vix/hooks/feedback-at-10/count.log"))
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

// TestFeedbackHookFiresAtThreshold seeds the counter to 9 so the harness's own
// thread is the 10th: the seeded hook delivers exactly one feedback
// conversation carrying the form link, and writes the once-only marker.
func TestFeedbackHookFiresAtThreshold(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.feedback_fires",
		Description: "after 10 fresh threads the seeded hook opens a one-time feedback conversation via `vix thread create`",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/feedback-at-10/count.log", strings.Repeat("1\n", 9)))

	h.UI.WaitStable(400 * time.Millisecond)
	startThread(h)
	h.UI.Shot("feedback-threshold")

	if !pollUntil(15*time.Second, func() bool { return len(feedbackThreads(h)) == 1 }) {
		t.Fatalf("feedback conversation never created (found %d)", len(feedbackThreads(h)))
	}
	recs := feedbackThreads(h)
	if len(recs[0].Messages) == 0 || len(recs[0].Messages[0].Content) == 0 ||
		!strings.Contains(recs[0].Messages[0].Content[0].Text, "forms.gle/ADEVrtP2xRsKpxtdA") {
		t.Fatalf("feedback message missing the form link: %+v", recs[0].Messages)
	}
	if _, err := os.Stat(h.HomePath(".vix/hooks/feedback-at-10/asked")); err != nil {
		t.Fatalf("once-only marker not written: %v", err)
	}
}

// TestFeedbackHookFiresOnlyOnce seeds the counter past the threshold AND the
// "asked" marker (as if it already fired): a new thread keeps counting but
// never delivers a second feedback conversation.
func TestFeedbackHookFiresOnlyOnce(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.feedback_once",
		Description: "with the once-only marker already present, crossing the threshold again delivers nothing",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile(".vix/hooks/feedback-at-10/count.log", strings.Repeat("1\n", 12)),
		harness.WithHomeFile(".vix/hooks/feedback-at-10/asked", ""),
	)

	h.UI.WaitStable(400 * time.Millisecond)
	startThread(h)

	// Wait until the hook has run (the counter grew past the seeded 12), then
	// confirm it delivered nothing.
	if !pollUntil(15*time.Second, func() bool { return countLogLines(h) >= 13 }) {
		t.Fatalf("feedback hook never ran (count=%d)", countLogLines(h))
	}
	h.UI.Shot("feedback-once")
	if n := len(feedbackThreads(h)); n != 0 {
		t.Fatalf("marker present but %d feedback conversation(s) delivered, want 0", n)
	}
}

// TestFeedbackHookBelowThreshold proves the hook counts every fresh thread but
// stays silent below the threshold.
func TestFeedbackHookBelowThreshold(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.feedback_below",
		Description: "below the threshold the hook counts the thread but delivers nothing",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/feedback-at-10/count.log", strings.Repeat("1\n", 3)))

	h.UI.WaitStable(400 * time.Millisecond)
	startThread(h)

	if !pollUntil(15*time.Second, func() bool { return countLogLines(h) >= 4 }) {
		t.Fatalf("feedback hook never counted this thread (count=%d)", countLogLines(h))
	}
	h.UI.Shot("feedback-below")
	if n := len(feedbackThreads(h)); n != 0 {
		t.Fatalf("delivered %d feedback conversation(s) below threshold, want 0", n)
	}
	if _, err := os.Stat(h.HomePath(".vix/hooks/feedback-at-10/asked")); err == nil {
		t.Fatal("once-only marker written below threshold")
	}
}
