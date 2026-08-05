package scenarios

import (
	"bytes"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestWorkflowOutputJoinsTranscript is an acceptance spec for the
// "Workflow → transcript" bridge: when a workflow run produces visible agent
// output, that output is mirrored into the thread's chat transcript
// (s.messages). Two consequences are asserted:
//
//  1. Persistence/replay — after a daemon restart the finished run's output is
//     still on screen (it was saved to the thread record, not merely streamed).
//  2. Continuation — a follow-up chat message carries the workflow output in the
//     request history, so the model can keep talking about what it produced.
//
// Skipped: driving a workflow via Shift+Tab end-to-end is unvalidated in the
// containerised harness (same constraint as TestPlanWorkflowRunsOnConfiguredProvider).
// Enable once the workflow-driving path is validated in a gate run.
func TestWorkflowOutputJoinsTranscript(t *testing.T) {
	meta := harness.Meta{
		Category:    "workflow",
		Subcategory: "workflow.transcript",
		Description: "a finished workflow's visible output is recorded into the chat transcript (replay + chat continuation)",
		Wire:        harness.WireMessages,
	}
	harness.SkipScenario(t, meta, "unvalidated Shift+Tab workflow driving in-container; enable after a gate run")

	const marker = "PLAN_MARKER_42"
	settings := `{
		"version": 1,
		"workflows": [{
			"name": "Echo Plan",
			"entry_point": { "id": "plan" },
			"steps": {
				"plan": { "type": "agent", "agent": "general", "prompt": "$(workflow.prompt)" }
			}
		}]
	}`

	h := harness.Start(t, meta, harness.WithSettings(settings))
	h.UI.WaitStable(400 * time.Millisecond)

	// Cycle workflow mode (Shift+Tab) until the custom workflow is active.
	active := false
	for range 6 {
		h.UI.Key("shift-tab")
		h.UI.WaitStable(250 * time.Millisecond)
		if h.UI.Contains("Echo Plan") {
			active = true
			break
		}
	}
	if !active {
		t.Fatalf("could not switch to the Echo Plan workflow; screen:\n%s", h.UI.Snapshot())
	}

	// The single visible agent step streams this text as the "plan".
	h.Mock.Enqueue(harness.Text(marker + ": do X then Y"))
	h.UI.Type("draft a plan")
	h.UI.Enter()
	h.UI.WaitFor(marker)
	h.UI.Shot("workflow-done")

	// (1) Persistence/replay: restart the daemon and confirm the output survives
	// — it must come from the persisted transcript, not the live stream.
	h.Daemon.Restart()
	h.UI.WaitStable(600 * time.Millisecond)
	if !h.UI.Contains(marker) {
		t.Fatalf("workflow output did not replay after restart; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("after-restart")

	// (2) Continuation: a normal chat follow-up must carry the workflow output in
	// the request history sent to the model.
	h.Mock.Enqueue(harness.Text("Sure — happy to refine it."))
	h.UI.Type("can you refine step X?")
	h.UI.Enter()
	h.UI.WaitFor("happy to refine")

	reqs := h.Mock.Requests()
	if len(reqs) == 0 {
		t.Fatal("no requests reached the mock")
	}
	last := reqs[len(reqs)-1]
	if !bytes.Contains(last.Body(), []byte(marker)) {
		t.Fatalf("follow-up chat request did not include the workflow output (%q); body:\n%s", marker, string(last.Body()))
	}
}
