package scenarios

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// selfRouteJobSpec is a one-shot job (past-dated "at", so it fires at startup)
// whose inline workflow has a bash step that branches on its OWN output via
// execute_if — the exact pattern the GitHub-plan job uses
// (`[[ "$(step.select)" != *NO_TODO* ]]`). `select` emits NO_TODO, so:
//   - the `detail` branch (guard: select != NO_TODO) must be SKIPPED, and
//   - the `wrapup` branch (guard: select == NO_TODO) must RUN.
//
// Regression: vars used to be snapshotted before the step ran, so the guard's
// `$(step.select)` was left unsubstituted and bash evaluated it as an empty
// command substitution — making the `detail` guard wrongly true (and `wrapup`
// wrongly false). The job then ran `detail` on NO_TODO and failed every cycle.
const selfRouteJobSpec = `{
  "id": "e2e-self-route",
  "name": "E2E Self Route",
  "enabled": true,
  "trigger": {"type": "at", "time": "2000-01-01T00:00:00Z"},
  "prompt": "self-route regression",
  "workflow": {
    "name": "e2e-self-route",
    "entry_point": {"id": "select"},
    "steps": {
      "select": {
        "type": "bash",
        "command": "echo NO_TODO",
        "next_steps": [
          {"id": "detail", "execute_if": "[[ \"$(step.select)\" != *NO_TODO* ]]"},
          {"id": "wrapup", "execute_if": "[[ \"$(step.select)\" == *NO_TODO* ]]"}
        ]
      },
      "detail": {"type": "bash", "command": "echo ran > detail-ran.txt; exit 1"},
      "wrapup": {"type": "bash", "command": "echo ok > done-ok.txt"}
    }
  },
  "cwd": "{{WORKDIR}}",
  "created_by": "vix"
}`

// TestWorkflowBashStepRoutesOnOwnOutput pins, end-to-end through a scheduled job
// run, that a bash step can branch on its own output via execute_if: the NO_TODO
// branch is taken and the != NO_TODO branch is skipped. Before the engine fix the
// routing inverted and the job failed on every run.
func TestWorkflowBashStepRoutesOnOwnOutput(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "workflow",
		Subcategory: "workflow.self_route",
		Description: "a workflow bash step branches on its own output via execute_if (NO_TODO guard), end-to-end through a job run",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-self-route/job.json", selfRouteJobSpec),
	)

	// Bash-only workflow: no agent step runs, so no mock turn is needed.
	h.UI.WaitStable(500 * time.Millisecond)

	// Wait until the run reaches a terminal branch (either the correct wrapup
	// marker or the buggy detail marker), then assert which one fired.
	if !pollUntil(20*time.Second, func() bool {
		return h.FS.Exists("done-ok.txt") || h.FS.Exists("detail-ran.txt")
	}) {
		t.Fatalf("self-route workflow produced no terminal marker; vixd log:\n%s", h.Daemon.LogTail(80))
	}

	if h.FS.Exists("detail-ran.txt") {
		t.Fatalf("the != NO_TODO branch (detail) ran on NO_TODO — execute_if mis-routed; vixd log:\n%s", h.Daemon.LogTail(80))
	}
	if !h.FS.Exists("done-ok.txt") {
		t.Fatalf("the NO_TODO branch (wrapup) did not run; vixd log:\n%s", h.Daemon.LogTail(80))
	}

	// The run finishes cleanly (detail's exit 1 is never reached).
	statePath := h.HomePath(".vix/jobs/e2e-self-route/state.json")
	if !pollUntil(10*time.Second, func() bool {
		b, err := os.ReadFile(statePath)
		return err == nil && !strings.Contains(string(b), `"last_status": "error"`)
	}) {
		b, _ := os.ReadFile(statePath)
		t.Fatalf("self-route run recorded an error status; state:\n%s", string(b))
	}

	h.UI.Shot("workflow-self-route")
}

// TestPlanWorkflowRunsOnConfiguredProvider is a staged acceptance spec for issue
// #34 ("Plan doesn't work — requires ANTHROPIC_API_KEY"). The Plan workflow is
// entered with Shift+Tab (not a slash command) and its agent steps inherit the
// thread model via buildRunnerClient. With an OpenAI-pinned thread the
// explore/plan steps must run on OpenAI, never failing with "no credential for
// anthropic".
//
// Skipped: #34 is open, and driving the workflow (Shift+Tab to the Plan mode,
// then the explore→plan→review step machine) is unvalidated here. Enable and
// refine after the first gate run.
//
// When enabled it proves: the plan workflow starts on the thread's provider and
// does not raise an anthropic-credential error. T1.1 · asserts screen (workflow
// starts, no anthropic-cred error) + wire (a request reached the OpenAI mock).
func TestPlanWorkflowRunsOnConfiguredProvider(t *testing.T) {
	meta := harness.Meta{
		Category:    "workflow",
		Subcategory: "workflow.plan_provider",
		Description: "the Plan workflow runs on the thread's provider, not anthropic (#34)",
		Wire:        harness.WireResponses,
		Variant:     "responses",
	}
	harness.SkipScenario(t, meta, "acceptance spec for #34 (open) + unvalidated Shift+Tab workflow driving; enable when fixed/validated")

	h := harness.Start(t, meta, harness.WithModel("openai/gpt-4o"))

	h.UI.WaitStable(400 * time.Millisecond)

	// Cycle workflow mode (Shift+Tab) until the Plan workflow is active.
	planActive := false
	for range 6 {
		h.UI.Key("shift-tab")
		h.UI.WaitStable(250 * time.Millisecond)
		if h.UI.Contains("Plan") {
			planActive = true
			break
		}
	}
	if !planActive {
		t.Fatalf("could not switch to the Plan workflow; screen:\n%s", h.UI.Snapshot())
	}

	// The explore + plan agent steps stream text; script a couple of replies so
	// the steps can complete up to the user-review gate.
	h.Mock.Enqueue(
		harness.Text("Explored the repository structure."),
		harness.Text("Drafted a plan with three steps."),
	)
	h.UI.Type("come up with a plan to add a greeting command")
	h.UI.Enter()
	h.UI.WaitStable(700 * time.Millisecond)
	h.UI.Shot("plan-running")

	if h.UI.Contains("no credential for anthropic") {
		t.Fatalf("Plan workflow demanded an anthropic credential; screen:\n%s", h.UI.Snapshot())
	}
	if len(h.Mock.Requests()) == 0 {
		t.Fatalf("Plan workflow made no request to the (OpenAI) mock")
	}
}
