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

// These scenarios exercise the out-of-band CLI verbs that fire a job or a hook
// immediately by id, bypassing the schedule / event that would normally trigger
// them: `vix job run <id>` and `vix hook trigger <id>`. Each drives the real
// vix binary as a one-shot subcommand against the test's daemon (via
// h.RunCLI), then asserts the run lands as a persisted Vix-initiated thread.

func runTriggerMeta(sub, desc string) harness.Meta {
	return harness.Meta{
		Category:    "cli",
		Subcategory: sub,
		Description: desc,
		Wire:        harness.WireMessages,
	}
}

type vixRunRecord struct {
	ID        string `json:"id"`
	Origin    string `json:"origin"`
	JobStatus string `json:"job_status"`
	Trigger   struct {
		Type string `json:"type"`
		Ref  string `json:"ref"`
	} `json:"trigger"`
}

// vixRunsFor returns the persisted Vix-initiated thread records whose trigger
// ref matches the given id (a job or hook id).
func vixRunsFor(h *harness.Harness, ref string) []vixRunRecord {
	dir := h.HomePath(".vix/threads/open")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []vixRunRecord
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r vixRunRecord
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		if r.Origin == "vix" && r.Trigger.Ref == ref {
			out = append(out, r)
		}
	}
	return out
}

// onDemandJobSpec is a job whose trigger fires far in the future, so the
// scheduler never runs it on its own — only `vix job run` does.
const onDemandJobSpec = `{
  "id": "e2e-ondemand",
  "name": "E2E On-Demand",
  "enabled": true,
  "trigger": {"type": "at", "time": "2099-01-01T00:00:00Z"},
  "prompt": "Say hello.",
  "cwd": "{{WORKDIR}}",
  "created_by": "vix"
}`

// TestJobRunCLI verifies `vix job run <id>` fires a future-dated job on demand:
// it prints the run's thread id and the run lands as a Vix-initiated record.
func TestJobRunCLI(t *testing.T) {
	h := harness.Start(t, runTriggerMeta("cli.job_run", "`vix job run <id>` fires a future-dated job on demand"),
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-ondemand/job.json", onDemandJobSpec),
	)

	// The on-demand run makes exactly one turn against the mock.
	h.Mock.Enqueue(harness.Text("hello from the on-demand run"))
	h.UI.WaitStable(500 * time.Millisecond)

	out, err := h.RunCLI("job", "run", "e2e-ondemand")
	if err != nil {
		t.Fatalf("vix job run failed: %v\n%s", err, out)
	}
	threadID := strings.TrimSpace(out)
	if threadID == "" {
		t.Fatalf("expected a thread id on stdout, got empty")
	}

	if !pollUntil(20*time.Second, func() bool { return len(vixRunsFor(h, "e2e-ondemand")) == 1 }) {
		t.Fatalf("on-demand job run not persisted; stdout=%q", out)
	}
	rec := vixRunsFor(h, "e2e-ondemand")[0]
	if rec.ID != threadID {
		t.Fatalf("persisted run id %q != printed id %q", rec.ID, threadID)
	}
	if rec.JobStatus != "ok" {
		t.Fatalf("job run status = %q, want ok", rec.JobStatus)
	}

	h.UI.Key("f1")
	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("job-run-cli")
}

// onDemandHookSpec is a disabled prompt hook: it never fires on its Stop event,
// so the only way it runs is `vix hook trigger` (which runs disabled hooks).
const onDemandHookSpec = `{
  "id": "e2e-hook",
  "name": "E2E Hook",
  "enabled": false,
  "trigger": {"event": "Stop"},
  "prompt": "Say hello from the hook.",
  "cwd": "{{WORKDIR}}"
}`

// TestHookTriggerCLI verifies `vix hook trigger <id>` fires a disabled hook on
// demand: it prints the run's thread id and the run lands as a Vix-initiated
// record stamped with a "hook" trigger.
func TestHookTriggerCLI(t *testing.T) {
	h := harness.Start(t, runTriggerMeta("cli.hook_trigger", "`vix hook trigger <id>` fires a disabled hook on demand"),
		harness.WithHomeFile(".vix/hooks/e2e-hook/hook.json", onDemandHookSpec),
	)

	// The triggered hook's prompt makes one turn against the mock.
	h.Mock.Enqueue(harness.Text("hello from the triggered hook"))
	h.UI.WaitStable(500 * time.Millisecond)

	out, err := h.RunCLI("hook", "trigger", "e2e-hook")
	if err != nil {
		t.Fatalf("vix hook trigger failed: %v\n%s", err, out)
	}
	threadID := strings.TrimSpace(out)
	if threadID == "" {
		t.Fatalf("expected a thread id on stdout, got empty")
	}

	if !pollUntil(20*time.Second, func() bool { return len(vixRunsFor(h, "e2e-hook")) == 1 }) {
		t.Fatalf("triggered hook run not persisted; stdout=%q", out)
	}
	rec := vixRunsFor(h, "e2e-hook")[0]
	if rec.ID != threadID {
		t.Fatalf("persisted run id %q != printed id %q", rec.ID, threadID)
	}
	if rec.Trigger.Type != "hook" {
		t.Fatalf("trigger type = %q, want hook", rec.Trigger.Type)
	}

	h.UI.Key("f1")
	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("hook-trigger-cli")
}
