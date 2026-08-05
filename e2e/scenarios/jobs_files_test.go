package scenarios

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// A future-dated job (never auto-fires) that ships a helper script via the
// spec's `files` section and whose bash-only workflow executes it. This
// exercises the jobs-engine primitive end to end through the real daemon:
// the daemon materializes greet.sh (executable) into the job dir on load, and
// `vix job run` runs a workflow that invokes it via $(workflow.dir).
const filesJobSpec = `{
  "id": "e2e-files",
  "name": "E2E Files",
  "enabled": true,
  "trigger": {"type": "at", "time": "2099-01-01T00:00:00Z"},
  "prompt": "unused (bash-only workflow)",
  "cwd": "{{WORKDIR}}",
  "created_by": "vix",
  "files": [
    {"path": "greet.sh", "content": "#!/bin/sh\necho hello-from-helper\n", "mode": "0755"}
  ],
  "workflow": {
    "name": "e2e-files-wf",
    "entry_point": {"id": "run"},
    "steps": {
      "run": {"type": "bash", "command": "\"$(workflow.dir)/greet.sh\" > \"$(workflow.dir)/greeting.txt\""}
    }
  }
}`

// TestJobFilesMaterialize verifies the spec `files` section: the daemon writes
// the declared helper (executable) into the job dir on load, and a workflow can
// run it from $(workflow.dir).
func TestJobFilesMaterialize(t *testing.T) {
	h := harness.Start(t, runTriggerMeta("cli.job_files", "a job's `files` section ships an executable helper the workflow runs"),
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-files/job.json", filesJobSpec),
	)
	h.UI.WaitStable(500 * time.Millisecond)

	// The daemon materializes the declared file into the job dir on load.
	helper := h.HomePath(".vix/jobs/e2e-files/greet.sh")
	if !pollUntil(10*time.Second, func() bool {
		b, err := os.ReadFile(helper)
		return err == nil && strings.Contains(string(b), "hello-from-helper")
	}) {
		t.Fatalf("declared file %s was not materialized", helper)
	}
	fi, err := os.Stat(helper)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("helper mode = %v (err=%v), want 0755", fi.Mode().Perm(), err)
	}

	// Firing the job runs the bash-only workflow, which executes the helper.
	out, err := h.RunCLI("job", "run", "e2e-files")
	if err != nil {
		t.Fatalf("vix job run failed: %v\n%s", err, out)
	}
	greeting := h.HomePath(".vix/jobs/e2e-files/greeting.txt")
	if !pollUntil(20*time.Second, func() bool {
		b, err := os.ReadFile(greeting)
		return err == nil && strings.Contains(string(b), "hello-from-helper")
	}) {
		t.Fatalf("workflow did not run the shipped helper; %s missing/empty", filepath.Base(greeting))
	}
}
