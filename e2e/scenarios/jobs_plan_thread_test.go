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

// This scenario exercises the daemon-side behaviour of a scheduled GitHub-plan
// style job run end to end: it fires an inline-workflow job via `vix job run`,
// then asserts the persisted Vix-initiated thread record reflects the three
// fixes —
//
//   - the thread is titled after the item the run picked, parsed from the
//     findings' deterministic header ("Hi, I investigated … #N — <title> …");
//   - the FULL working transcript is kept (the agent's tool_use/tool_result
//     turns, not a text-only summary), so a follow-up turn is grounded;
//   - a finished inline-workflow run drops back to chat mode (thread_mode ==
//     "chat", no active_workflow), so reopening it never warns that the
//     transient workflow "no longer exists".
//
// The gh/API/none access-branch matrix of the real githubIssuePlanWorkflow
// remains the (still-skipped) spec in jobs_github_plan_test.go; replicating that
// builder's generated workflow JSON here would couple this test to the
// frontend. This focuses on the daemon mechanics the fixes changed, which are
// workflow-shape-agnostic.

// planJobSpec is a future-dated job carrying a self-contained single-agent
// inline workflow (so only `vix job run` ever fires it). The agent makes one
// tool call and then emits the framed findings.
const planJobSpec = `{
  "id": "e2e-plan",
  "name": "Plan GitHub issues (get-vix/vix)",
  "enabled": true,
  "trigger": {"type": "at", "time": "2099-01-01T00:00:00Z"},
  "prompt": "Plan open GitHub issues and pull requests for get-vix/vix.",
  "cwd": "{{WORKDIR}}",
  "created_by": "web",
  "permissions": {"auto_write": true, "auto_dirs": true},
  "workflow": {
    "name": "e2e-plan-issues",
    "entry_point": {"id": "plan"},
    "steps": {
      "plan": {
        "type": "agent",
        "agent": "general",
        "prompt": "Investigate one open issue and report your findings."
      }
    }
  }
}`

type planRunRecord struct {
	ID             string `json:"id"`
	Origin         string `json:"origin"`
	JobStatus      string `json:"job_status"`
	Title          string `json:"title"`
	ThreadMode     string `json:"session_mode"`
	ActiveWorkflow string `json:"active_workflow"`
	Trigger        struct {
		Ref string `json:"ref"`
	} `json:"trigger"`
	Messages []struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	} `json:"messages"`
	RetryNotices []struct {
		Attempt    int    `json:"attempt"`
		MaxRetries int    `json:"max_retries"`
		Reason     string `json:"reason"`
	} `json:"retry_notices"`
}

func planRunFor(h *harness.Harness, ref string) (planRunRecord, bool) {
	dir := h.HomePath(".vix/threads/open")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return planRunRecord{}, false
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r planRunRecord
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		if r.Origin == "vix" && r.Trigger.Ref == ref {
			return r, true
		}
	}
	return planRunRecord{}, false
}

func (r planRunRecord) hasToolBlocks() bool {
	for _, m := range r.Messages {
		for _, c := range m.Content {
			if c.Type == "tool_use" || c.Type == "tool_result" {
				return true
			}
		}
	}
	return false
}

// TestJobPlanThreadShape fires the inline-workflow plan job and asserts the
// per-item title, the preserved tool transcript, and the chat-mode reset.
func TestJobPlanThreadShape(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.plan_thread",
		Description: "a GitHub-plan job run is titled per item, keeps its full tool transcript, and reopens in chat mode",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-plan/job.json", planJobSpec),
	)

	// The plan step makes one real tool call, then emits the framed findings
	// whose H1 title and header line the daemon parses into the thread title.
	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"echo investigating"}`),
		harness.Text("# [Plan GitHub issues (get-vix/vix)] Addressing issue #29 — ANTHROPIC_BASE_URL not resolved from .env files\n\nHi, I investigated issue #29 — ANTHROPIC_BASE_URL not resolved from .env files — on GitHub. Here are my findings:\n\nhttps://github.com/get-vix/vix/issues/29\n\n**Summary**\nThe base URL isn't read from .env files.\n\n**My take**\nLegit, actionable bug.\n\n**Plan**\n1. Resolve ANTHROPIC_BASE_URL during config loading."),
	)
	h.UI.WaitStable(500 * time.Millisecond)

	out, err := h.RunCLI("job", "run", "e2e-plan")
	if err != nil {
		t.Fatalf("vix job run failed: %v\n%s", err, out)
	}
	threadID := strings.TrimSpace(out)
	if threadID == "" {
		t.Fatalf("expected a thread id on stdout, got empty")
	}

	var rec planRunRecord
	if !pollUntil(30*time.Second, func() bool {
		r, ok := planRunFor(h, "e2e-plan")
		if ok && r.JobStatus != "" {
			rec = r
			return true
		}
		return false
	}) {
		t.Fatalf("plan job run not persisted; stdout=%q\n%s", out, h.Daemon.LogTail(80))
	}

	if rec.JobStatus != "ok" {
		t.Fatalf("job status = %q, want ok\n%s", rec.JobStatus, h.Daemon.LogTail(80))
	}
	// Fix B: per-item title parsed from the findings header.
	wantTitle := "[Plan GitHub issues (get-vix/vix)] Addressing issue #29 — ANTHROPIC_BASE_URL not resolved from .env files"
	if rec.Title != wantTitle {
		t.Errorf("title = %q,\n want %q", rec.Title, wantTitle)
	}
	// Fix C (prior): the real tool transcript is kept, not a text-only summary.
	if !rec.hasToolBlocks() {
		t.Errorf("expected tool_use/tool_result blocks in the persisted transcript; messages=%+v", rec.Messages)
	}
	// Fix 3: a finished inline-workflow run reopens in chat mode.
	if rec.ThreadMode != "chat" {
		t.Errorf("thread_mode = %q, want chat", rec.ThreadMode)
	}
	if rec.ActiveWorkflow != "" {
		t.Errorf("active_workflow = %q, want empty after a finished inline run", rec.ActiveWorkflow)
	}

	// The Threads tab renders the bare title (no "<job-id> · ok" prefix).
	h.UI.Key("f1")
	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("plan-thread")
}

// planFailJobSpec is a single-agent inline-workflow job whose agent step is
// driven into a terminal API failure (after retryable overloads) by the mock.
const planFailJobSpec = `{
  "id": "e2e-plan-fail",
  "name": "Plan GitHub issues (get-vix/vix)",
  "enabled": true,
  "trigger": {"type": "at", "time": "2099-01-01T00:00:00Z"},
  "prompt": "Plan open GitHub issues and pull requests for get-vix/vix.",
  "cwd": "{{WORKDIR}}",
  "created_by": "web",
  "permissions": {"auto_write": true, "auto_dirs": true},
  "workflow": {
    "name": "e2e-plan-issues",
    "entry_point": {"id": "plan"},
    "steps": {
      "plan": {
        "type": "agent",
        "agent": "general",
        "prompt": "Investigate one open issue and report your findings."
      }
    }
  }
}`

// TestJobPlanThreadFailureReplaysRetries drives the plan job's agent step into
// a terminal API failure after two retryable overloads. It proves a *failed*
// scheduled run behaves like a successful one for the reader: the full
// transcript (the agent's tool_use/tool_result before it died) plus the retry
// notices are persisted, the thread drops to chat mode (so reopening never
// warns the inline workflow "no longer exists"), and opening it replays the
// conversation mid-flight — the same "API overloaded — retrying … (attempt
// N/10)" lines an interactive workflow shows live.
func TestJobPlanThreadFailureReplaysRetries(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.plan_thread_failure",
		Description: "a failed plan job keeps its full transcript + retry notices, reopens in chat mode, and replays the mid-flight conversation",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-plan-fail/job.json", planFailJobSpec),
	)

	// Turn 1: a real tool call (partial work). Turn 2: two retryable 529s (each
	// surfaces a retry notice), then a non-retryable 400 → terminal failure.
	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"echo MIDFLIGHT-PROBE"}`),
		harness.HTTPError(529, "overloaded, try again"),
		harness.HTTPError(529, "overloaded, try again"),
		harness.HTTPError(400, "bad request"),
	)
	h.UI.WaitStable(500 * time.Millisecond)

	out, err := h.RunCLI("job", "run", "e2e-plan-fail")
	if err != nil {
		t.Fatalf("vix job run failed: %v\n%s", err, out)
	}

	var rec planRunRecord
	if !pollUntil(60*time.Second, func() bool {
		r, ok := planRunFor(h, "e2e-plan-fail")
		if ok && r.JobStatus != "" {
			rec = r
			return true
		}
		return false
	}) {
		t.Fatalf("failed plan run not persisted; stdout=%q\n%s", out, h.Daemon.LogTail(120))
	}

	if rec.JobStatus != "error" {
		t.Fatalf("job status = %q, want error\n%s", rec.JobStatus, h.Daemon.LogTail(120))
	}
	// A failed inline run still drops to chat mode — no "no longer exists".
	if rec.ThreadMode != "chat" {
		t.Errorf("thread_mode = %q, want chat after a failed inline run", rec.ThreadMode)
	}
	if rec.ActiveWorkflow != "" {
		t.Errorf("active_workflow = %q, want empty after a failed inline run", rec.ActiveWorkflow)
	}
	// The full transcript is kept: the agent's tool_use/tool_result from before
	// the failure (not a text-only summary, not an empty conversation).
	if !rec.hasToolBlocks() {
		t.Errorf("expected the failed step's tool transcript; messages=%+v", rec.Messages)
	}
	// The two overload retries are persisted for replay.
	if len(rec.RetryNotices) < 2 {
		t.Fatalf("expected >=2 persisted retry notices, got %d (%+v)", len(rec.RetryNotices), rec.RetryNotices)
	}

	// Open the failed run in the TUI. Its replay shows the agent's partial work
	// and the retry notices — the failed conversation, mid-flight.
	h.UI.Key("f1")
	h.UI.WaitFor("Vix-initiated")
	for i := 0; i < 12; i++ {
		h.UI.Key("down") // clamp the selection onto the (last) vix-initiated run
	}
	h.UI.Enter()
	if !pollUntil(10*time.Second, func() bool { return h.UI.Contains("attempt 1/10") }) {
		t.Fatalf("failed run replay did not show retry notices; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("failed-workflow-midflight")
}
