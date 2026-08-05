package scenarios

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// Regression for the review-github-prs job failing with "unknown tool: skill":
// a workflow's `agent` step (and any subagent) dispatches tools through
// executeToolConfirmed, not the main-agent executeToolDirect path. The `skill`
// tool is dispatched inline, so before the fix the confirmed path had no branch
// for it and returned "unknown tool: skill" even though the step advertised the
// tool. This drove the real job into a retry/timeout spin.
//
// This scenario seeds a home skill and a one-shot job whose inline workflow has
// a single agent step that invokes that skill via the skill tool. The mock
// scripts the step: a skill tool call, then a final message. The regression
// signal is on the wire — the skill body must reach the model as the tool
// result, and "unknown tool: skill" must NOT.

const skillWorkflowJobSpec = `{
  "id": "e2e-skill-workflow",
  "name": "Skill workflow",
  "enabled": true,
  "trigger": {"type": "at", "time": "2000-01-01T00:00:00Z"},
  "prompt": "Review the thing.",
  "cwd": "{{WORKDIR}}",
  "permissions": {"auto_write": false, "auto_dirs": false},
  "created_by": "vix",
  "workflow": {
    "name": "skill-in-agent-step",
    "entry_point": {"id": "act"},
    "steps": {
      "act": {
        "type": "agent",
        "agent": "general",
        "deny_tools": ["write_file", "edit_file", "delete_file"],
        "prompt": "Invoke the review-demo skill by calling the skill tool: skill(name: \"review-demo\"). Follow its instructions, then reply with a one-line summary."
      }
    },
    "budget": {"max_tokens": 400000}
  }
}`

// TestSkillInWorkflowAgentStep proves a workflow agent step can invoke a skill
// through the confirmed-dispatch path. jobs.skill_workflow
func TestSkillInWorkflowAgentStep(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.skill_workflow",
		Description: "a workflow agent step invokes a skill via the skill tool (confirmed dispatch path)",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/skills/review-demo/SKILL.md",
			skillMD("review-demo", "A review skill for e2e.", "REVIEW_DEMO_BODY do the review steps")),
		harness.WithHomeFile(".vix/jobs/e2e-skill-workflow/job.json", skillWorkflowJobSpec),
	)

	// The single agent step makes two LLM calls: the skill tool call, then the
	// final summary text.
	h.Mock.Enqueue(
		harness.ToolUse("skill", `{"name":"review-demo"}`),
		harness.Text("Reviewed the thing."),
	)
	h.UI.WaitStable(500 * time.Millisecond)

	// Wait until the one-shot run has been recorded in the job's state.json.
	statePath := h.HomePath(".vix/jobs/e2e-skill-workflow/state.json")
	if !pollUntil(25*time.Second, func() bool {
		b, err := os.ReadFile(statePath)
		return err == nil && strings.Contains(string(b), `"recent_runs"`)
	}) {
		t.Fatalf("skill-workflow run never recorded; vixd log:\n%s", h.Daemon.LogTail(80))
	}

	// The skill body must have reached the model as the tool result — this is
	// the deterministic regression signal.
	if !anyToolResultContains(h, "REVIEW_DEMO_BODY") {
		t.Fatalf("skill body did not reach the model from the workflow agent step; requests=%d\nvixd log:\n%s",
			len(h.Mock.Requests()), h.Daemon.LogTail(80))
	}
	// The confirmed path must not have fallen through to "unknown tool: skill".
	if anyToolResultContains(h, "unknown tool") {
		t.Fatalf("workflow agent step got 'unknown tool: skill' — confirmed dispatch regressed\nvixd log:\n%s",
			h.Daemon.LogTail(80))
	}

	// The run should complete successfully (the step finished with final text).
	var state struct {
		RecentRuns []struct {
			Status string `json:"status"`
		} `json:"recent_runs"`
	}
	raw, _ := os.ReadFile(statePath)
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decoding state.json: %v\nraw:\n%s", err, raw)
	}
	if len(state.RecentRuns) == 0 {
		t.Fatalf("no recent runs recorded:\n%s", raw)
	}
	if got := state.RecentRuns[len(state.RecentRuns)-1].Status; got != "ok" {
		t.Fatalf("last run status = %q, want %q:\n%s", got, "ok", raw)
	}

	h.UI.Shot("skill-workflow-agent-step")
}
