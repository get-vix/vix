package scenarios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestMessageTimestampSurvivesDaemonRestart guards the per-message timestamp
// fix: send times used to be generated at render time, so after a restart every
// replayed user message collapsed to the relaunch time. The daemon now persists
// a timestamp per turn and the TUI renders the replay from the stored value.
//
// In test-render mode the displayed instant is frozen for byte-stable
// screenshots, so the deterministic assertions are:
//   - disk: the persisted user turn carries a real, non-zero RFC3339 timestamp;
//   - screen: the replayed user message still shows its "Sent at …" line after
//     the daemon restarts (the line is present, not dropped).
//
// The real "original time is preserved" behaviour is covered by unit tests
// (renderUserMessageAt / buildReplayMessages), which can assert concrete times
// without the frozen-clock determinism constraint.
func TestMessageTimestampSurvivesDaemonRestart(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "thread",
		Subcategory: "thread.persistence",
		Description: "per-message send timestamps are persisted and replayed after a daemon restart",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	// One full turn so the daemon persists the conversation.
	h.Mock.Enqueue(harness.Text("Noted."))
	h.UI.Type("remember the timestamp test")
	h.UI.Enter()
	h.UI.WaitFor("Noted.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("before-restart")

	// Screen: the live message renders a Sent-at line (frozen instant under
	// test-render mode).
	if !h.UI.Contains("Sent at 12:00 PM") {
		t.Fatalf("user message missing Sent-at line before restart; screen:\n%s", h.UI.Snapshot())
	}

	// Disk: the persisted user turn carries a real, non-zero timestamp.
	openDir := h.HomePath(".vix", "threads", "open")
	entries, err := os.ReadDir(openDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no persisted thread record under %s (err=%v)", openDir, err)
	}
	raw, err := os.ReadFile(filepath.Join(openDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read thread record: %v", err)
	}
	var rec struct {
		Messages []struct {
			Role      string    `json:"role"`
			Timestamp time.Time `json:"timestamp"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("unmarshal thread record: %v\n%s", err, raw)
	}
	var sawUserTimestamp bool
	for _, m := range rec.Messages {
		if m.Role == "user" && !m.Timestamp.IsZero() {
			sawUserTimestamp = true
			break
		}
	}
	if !sawUserTimestamp {
		t.Fatalf("persisted user message has no non-zero timestamp; record:\n%s", raw)
	}

	// Restart the whole stack on the same HOME + socket.
	h.Daemon.Restart()
	h.UI.WaitStable(700 * time.Millisecond)
	h.UI.Shot("after-restart")

	// Screen: the replayed user message still shows its Sent-at line (the
	// timestamp survived persistence and replay rather than being dropped).
	h.UI.WaitFor("Noted.")
	if !h.UI.Contains("remember the timestamp test") {
		t.Fatalf("user message not replayed after restart; screen:\n%s", h.UI.Snapshot())
	}
	if !h.UI.Contains("Sent at 12:00 PM") {
		t.Fatalf("replayed user message missing Sent-at line after restart; screen:\n%s", h.UI.Snapshot())
	}
}
