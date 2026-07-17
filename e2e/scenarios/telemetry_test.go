package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestFunctionsWithTelemetryBlocked guards issue #47: a user who blocks the
// PostHog tracking endpoints (e.g. at the DNS level) reported that vix "doesn't
// function" without them. Telemetry is fire-and-forget and must never sit on the
// critical path — a normal turn must complete even when every tracking endpoint
// is unreachable.
//
// The e2e container is the reproduction: it runs with `--network none`, so the
// real PostHog host (us.i.posthog.com) is unreachable by construction — exactly
// the reporter's environment. We additionally set VIX_TELEMETRY=off to exercise
// the documented opt-out and prove it doesn't break anything. The turn must go
// through end-to-end: the file lands on disk, the model's result flows over the
// wire, and the final text renders on screen.
//
// asserts disk (file written) · wire (turn ran) · screen (final answer).
func TestFunctionsWithTelemetryBlocked(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "telemetry",
		Subcategory: "telemetry.blocked_endpoints",
		Description: "a normal turn completes with tracking endpoints unreachable and VIX_TELEMETRY=off (#47)",
		Wire:        harness.WireMessages,
	}, harness.WithEnv("VIX_TELEMETRY", "off"))

	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Shot("initial")

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"note.txt","content":"telemetry blocked, still working"}`),
		harness.Text("Wrote note.txt while telemetry was disabled."),
	)

	h.UI.Type("write note.txt with a short message")
	h.UI.Enter()
	h.UI.WaitFor("while telemetry was disabled")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-run")

	// Disk: the tool ran and the file landed despite telemetry being off/blocked.
	if got := string(h.FS.Read("note.txt")); got != "telemetry blocked, still working" {
		t.Fatalf("note.txt not written as expected, got %q", got)
	}
	// Wire: the turn actually reached the model (at least the initial request).
	if got := len(h.Mock.Requests()); got < 1 {
		t.Fatalf("expected the turn to reach the model, got %d requests", got)
	}
	// Screen: the final answer rendered — the session is fully functional.
	if !h.UI.Contains("while telemetry was disabled") {
		t.Fatalf("final answer not rendered; screen:\n%s", h.UI.Snapshot())
	}
}
