package scenarios

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// This file exercises the scheduled-jobs engine's per-job runtime state: each
// job's machine-written state lands in its own ~/.vix/jobs/<id>/state.json
// (a sibling of job.json), and no global jobs-state.json is ever written.

// perJobStateSpec is a one-shot job whose fire time is in the past, so the
// scheduler runs it immediately at startup and then persists its run state.
const perJobStateSpec = `{
  "id": "e2e-state",
  "name": "E2E State",
  "enabled": true,
  "trigger": {"type": "at", "time": "2000-01-01T00:00:00Z"},
  "prompt": "Say hello.",
  "cwd": "{{WORKDIR}}",
  "created_by": "vix"
}`

// TestJobsPerJobStateFile verifies that after a scheduled job runs, its runtime
// state is written to ~/.vix/jobs/<id>/state.json (carrying last_status / the
// completed flag for the one-shot), and that no legacy global
// ~/.vix/jobs/jobs-state.json is produced.
func TestJobsPerJobStateFile(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.state",
		Description: "a job run persists per-job state.json; no global jobs-state.json",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-state/job.json", perJobStateSpec),
	)

	// The job fires at startup and runs against the mock (one turn → persisted).
	h.Mock.Enqueue(harness.Text("hello from the scheduled job"))

	h.UI.WaitStable(500 * time.Millisecond)

	// The per-job state file must appear alongside the spec once the run lands.
	statePath := h.HomePath(".vix/jobs/e2e-state/state.json")
	var state string
	if !pollUntil(20*time.Second, func() bool {
		b, err := os.ReadFile(statePath)
		if err != nil {
			return false
		}
		state = string(b)
		return strings.Contains(state, `"last_status": "ok"`)
	}) {
		t.Fatalf("per-job state.json missing or not ok at %s; got:\n%s\nvixd log:\n%s",
			statePath, state, h.Daemon.LogTail(80))
	}

	// One-shot "at" jobs complete after firing.
	if !strings.Contains(state, `"completed": true`) {
		t.Fatalf("one-shot state must be completed; got:\n%s", state)
	}

	// The legacy global state file must never be written.
	if _, err := os.Stat(h.HomePath(".vix/jobs/jobs-state.json")); !os.IsNotExist(err) {
		t.Fatalf("global jobs-state.json must not exist, stat err = %v", err)
	}

	h.UI.Shot("per-job-state")
}

// recentRunsSpec is a one-shot job whose fire time is in the past, so the
// scheduler runs it immediately at startup and records it in the run history.
const recentRunsSpec = `{
  "id": "e2e-recent",
  "name": "E2E Recent Runs",
  "enabled": true,
  "trigger": {"type": "at", "time": "2000-01-01T00:00:00Z"},
  "prompt": "Say hello.",
  "cwd": "{{WORKDIR}}",
  "created_by": "vix"
}`

// TestJobsRecentRunsHistory verifies that after a job runs, the run is appended
// to the job's recent-run history (state.json "recent_runs"), carrying the run
// status and the thread id.
func TestJobsRecentRunsHistory(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.recent_runs",
		Description: "a job run is appended to state.json recent_runs history",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-recent/job.json", recentRunsSpec),
	)

	h.Mock.Enqueue(harness.Text("hello from the scheduled job"))

	h.UI.WaitStable(500 * time.Millisecond)

	statePath := h.HomePath(".vix/jobs/e2e-recent/state.json")
	var state string
	if !pollUntil(20*time.Second, func() bool {
		b, err := os.ReadFile(statePath)
		if err != nil {
			return false
		}
		state = string(b)
		return strings.Contains(state, `"recent_runs"`) &&
			strings.Contains(state, `"status": "ok"`)
	}) {
		t.Fatalf("recent_runs history missing or not ok at %s; got:\n%s\nvixd log:\n%s",
			statePath, state, h.Daemon.LogTail(80))
	}

	h.UI.Shot("recent-runs")
}
