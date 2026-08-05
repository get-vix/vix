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

// preflightFailJobSpec is a future-dated job carrying a self-contained
// bash-only inline workflow whose single (entry-point) step is a preflight
// check that aborts the run with a non-zero exit — exactly the shape of the
// GitHub watch jobs' detect→deny path when the environment can't reach GitHub.
// No agent step runs, so no mock turn is needed. The echoed marker is a single
// unbroken token so it survives terminal soft-wrapping when asserted on screen.
const preflightFailJobSpec = `{
  "id": "e2e-preflight-fail",
  "name": "Watch GitHub repo (get-vix/vix)",
  "enabled": true,
  "trigger": {"type": "at", "time": "2099-01-01T00:00:00Z"},
  "prompt": "Watch get-vix/vix.",
  "cwd": "{{WORKDIR}}",
  "created_by": "web",
  "permissions": {"auto_write": true, "auto_dirs": true},
  "workflow": {
    "name": "e2e-preflight",
    "entry_point": {"id": "deny"},
    "steps": {
      "deny": {"type": "bash", "command": "echo PREFLIGHTDENYMARKER && exit 1"}
    }
  }
}`

type failNoticeRunRecord struct {
	Origin    string `json:"origin"`
	JobStatus string `json:"job_status"`
	Trigger   struct {
		Ref string `json:"ref"`
	} `json:"trigger"`
	Messages       []json.RawMessage `json:"messages"`
	FailureNotices []struct {
		StepID string `json:"step_id"`
		Reason string `json:"reason"`
	} `json:"failure_notices"`
}

func failNoticeRunFor(h *harness.Harness, ref string) (failNoticeRunRecord, bool) {
	dir := h.HomePath(".vix/threads/open")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return failNoticeRunRecord{}, false
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r failNoticeRunRecord
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		if r.Origin == "vix" && r.Trigger.Ref == ref {
			return r, true
		}
	}
	return failNoticeRunRecord{}, false
}

// TestJobPreflightFailureShowsErrorOnOpen guards the "blank failed thread" bug:
// a scheduled run whose workflow aborts at a bash preflight step used to persist
// with zero messages and no other trace, so opening it showed nothing. Now the
// failure is captured as a persisted failure notice (naming the step and
// carrying its output) and replays as an error line, so reopening the run shows
// WHY it failed instead of an empty transcript.
func TestJobPreflightFailureShowsErrorOnOpen(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.preflight_failure",
		Description: "a job whose workflow aborts at a bash preflight step persists a failure notice and replays it as an error line, so the reopened run isn't blank",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-preflight-fail/job.json", preflightFailJobSpec),
	)

	h.UI.WaitStable(500 * time.Millisecond)

	out, err := h.RunCLI("job", "run", "e2e-preflight-fail")
	if err != nil {
		t.Fatalf("vix job run failed: %v\n%s", err, out)
	}

	var rec failNoticeRunRecord
	if !pollUntil(60*time.Second, func() bool {
		r, ok := failNoticeRunFor(h, "e2e-preflight-fail")
		if ok && r.JobStatus != "" {
			rec = r
			return true
		}
		return false
	}) {
		t.Fatalf("failed preflight run not persisted; stdout=%q\n%s", out, h.Daemon.LogTail(120))
	}

	if rec.JobStatus != "error" {
		t.Fatalf("job status = %q, want error\n%s", rec.JobStatus, h.Daemon.LogTail(120))
	}
	// The bug: a preflight-aborted run left zero messages and no trace.
	if len(rec.Messages) != 0 {
		t.Errorf("expected no chat messages for a bash-only preflight abort, got %d", len(rec.Messages))
	}
	// The fix: the failure is captured as a persisted notice naming the step and
	// carrying its output.
	if len(rec.FailureNotices) != 1 {
		t.Fatalf("expected 1 persisted failure notice, got %d (%+v)", len(rec.FailureNotices), rec.FailureNotices)
	}
	fn := rec.FailureNotices[0]
	if fn.StepID != "deny" {
		t.Errorf("failure notice step_id = %q, want deny", fn.StepID)
	}
	if !strings.Contains(fn.Reason, "PREFLIGHTDENYMARKER") {
		t.Errorf("failure notice reason lost the step output: %q", fn.Reason)
	}

	// Open the failed run in the TUI: its replay surfaces the failure as an error
	// line carrying the captured reason, not a blank screen.
	h.UI.Key("f1")
	h.UI.WaitFor("Vix-initiated")
	for i := 0; i < 12; i++ {
		h.UI.Key("down") // clamp the selection onto the (last) vix-initiated run
	}
	h.UI.Enter()
	if !pollUntil(10*time.Second, func() bool { return h.UI.Contains("PREFLIGHTDENYMARKER") }) {
		t.Fatalf("reopened failed run did not show the failure reason; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("preflight-failure-reopened")
}
