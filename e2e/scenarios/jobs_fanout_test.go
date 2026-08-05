package scenarios

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// fanoutJobSpec is a future-dated job (never auto-fires) whose inline workflow
// exercises the fan_out/fan_in graph nodes end to end through the real daemon:
//
//	discover (agent, json_output) → fan_out → work (bash, one per item) → fan_in → report (bash)
//
// The single agent step emits a JSON array, so the MODEL sizes the fan-out (N =
// 3 here). Each branch is a bash step that writes its own file under
// $(workflow.dir) (per-branch files avoid any shared-file race), then a fan_in
// joins the barrier and the run continues to a terminal report step. Only the
// discover step calls the LLM, so exactly one mock response is needed and the
// scenario stays deterministic despite concurrent branches.
const fanoutJobSpec = `{
  "id": "e2e-fanout",
  "name": "E2E Fan Out",
  "enabled": true,
  "trigger": {"type": "at", "time": "2099-01-01T00:00:00Z"},
  "prompt": "unused",
  "cwd": "{{WORKDIR}}",
  "created_by": "vix",
  "permissions": {"auto_write": true, "auto_dirs": true},
  "workflow": {
    "name": "e2e-fanout-wf",
    "entry_point": {"id": "discover"},
    "steps": {
      "discover": {
        "type": "agent",
        "agent": "general",
        "json_output": true,
        "deny_tools": ["write_file", "edit_file", "delete_file", "bash"],
        "prompt": "Output ONLY a JSON array of item names.",
        "next_steps": [{"id": "fanout"}]
      },
      "fanout": {
        "type": "fan_out",
        "over": "$(step.discover)",
        "as": "item",
        "barrier_id": "b",
        "branch": {"id": "work"},
        "next_steps": [{"id": "join"}]
      },
      "work": {
        "type": "bash",
        "command": "echo processed > \"$(workflow.dir)/branch-$(item).txt\""
      },
      "join": {
        "type": "fan_in",
        "barrier_id": "b",
        "as": "results",
        "next_steps": [{"id": "report"}]
      },
      "report": {
        "type": "bash",
        "command": "echo done > \"$(workflow.dir)/done.txt\""
      }
    }
  }
}`

// TestJobFanOutFanIn verifies the fan_out/fan_in nodes: a model-emitted list
// drives a dynamic fan-out, each branch runs, the barrier joins, and the run
// continues to a terminal step — all through the real daemon and a real job run.
func TestJobFanOutFanIn(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.fan_out",
		Description: "a model-emitted list drives a dynamic fan_out; branches run and a fan_in joins them before the run continues",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-fanout/job.json", fanoutJobSpec),
	)

	// The single agent step emits the list the fan_out iterates.
	h.Mock.Enqueue(
		harness.Text(`["alpha","beta","gamma"]`),
	)
	h.UI.WaitStable(500 * time.Millisecond)

	out, err := h.RunCLI("job", "run", "e2e-fanout")
	if err != nil {
		t.Fatalf("vix job run failed: %v\n%s", err, out)
	}

	// Each branch wrote its own file, then the fan_in continuation wrote done.txt.
	want := []string{"branch-alpha.txt", "branch-beta.txt", "branch-gamma.txt", "done.txt"}
	if !pollUntil(30*time.Second, func() bool {
		for _, name := range want {
			if _, err := os.ReadFile(h.HomePath(".vix/jobs/e2e-fanout/" + name)); err != nil {
				return false
			}
		}
		return true
	}) {
		t.Fatalf("fan_out/fan_in did not produce all expected files under the job dir\n%s", h.Daemon.LogTail(80))
	}

	// The terminal report proves the fan_in routed onward after joining.
	done, err := os.ReadFile(h.HomePath(".vix/jobs/e2e-fanout/done.txt"))
	if err != nil || !strings.Contains(string(done), "done") {
		t.Fatalf("terminal report step did not run after fan_in; done.txt=%q err=%v", string(done), err)
	}
}
