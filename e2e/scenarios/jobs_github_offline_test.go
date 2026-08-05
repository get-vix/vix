package scenarios

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// githubDetectRouteJobSpec builds a one-shot job (past-dated "at", so it fires
// at startup) whose inline workflow reproduces the GitHub watch job's
// detect → deny | offline | begin routing verbatim. The detect step's output is
// forced to `token` so the three mutually-exclusive execute_if guards can be
// exercised in isolation (the real detect probes GitHub, which the offline e2e
// container cannot reach).
//
//   - none    → deny    (exit 1): a genuine setup problem, recorded as an error.
//   - offline → offline (exit 0): transient no-network, a silent skip.
//   - gh/api  → begin           : proceed (must NOT run for none/offline).
func githubDetectRouteJobSpec(id, token string) string {
	return `{
  "id": "` + id + `",
  "name": "E2E GitHub Detect Route",
  "enabled": true,
  "trigger": {"type": "at", "time": "2000-01-01T00:00:00Z"},
  "prompt": "detect-route",
  "workflow": {
    "name": "` + id + `",
    "entry_point": {"id": "detect"},
    "steps": {
      "detect": {
        "type": "bash",
        "command": "echo ` + token + `",
        "next_steps": [
          {"id": "deny", "execute_if": "[[ \"$(step.detect)\" == *none* ]]"},
          {"id": "offline", "execute_if": "[[ \"$(step.detect)\" == *offline* ]]"},
          {"id": "begin", "execute_if": "[[ \"$(step.detect)\" == *gh* || \"$(step.detect)\" == *api* ]]"}
        ]
      },
      "deny": {"type": "bash", "command": "echo denied > deny-ran.txt; exit 1"},
      "offline": {"type": "bash", "command": "echo skipping > offline-ran.txt; exit 0"},
      "begin": {"type": "bash", "command": "echo proceeded > begin-ran.txt"}
    }
  },
  "cwd": "{{WORKDIR}}",
  "created_by": "vix"
}`
}

// TestGithubJobOfflineSkips pins A1: when the GitHub preflight cannot reach
// GitHub (transient offline, e.g. a scheduled run firing at wake before the
// network is up), detect emits `offline`, which routes to a terminal exit-0
// step. The run must be recorded as SKIPPED — not a hard error — and neither the
// deny nor the begin branch may run.
func TestGithubJobOfflineSkips(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.github_offline_skip",
		Description: "an unreachable-GitHub preflight (offline) skips the run instead of erroring (A1)",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-gh-offline/job.json", githubDetectRouteJobSpec("e2e-gh-offline", "offline")),
	)

	// Bash-only workflow: no agent step runs, so no mock turn is needed.
	h.UI.WaitStable(500 * time.Millisecond)

	// Wait until the offline branch has fired.
	if !pollUntil(20*time.Second, func() bool {
		return h.FS.Exists("offline-ran.txt")
	}) {
		t.Fatalf("offline branch never ran; vixd log:\n%s", h.Daemon.LogTail(80))
	}

	// Mutually-exclusive routing: only the offline branch fired.
	if h.FS.Exists("deny-ran.txt") {
		t.Fatalf("deny branch ran on the offline token — detect mis-routed; vixd log:\n%s", h.Daemon.LogTail(80))
	}
	if h.FS.Exists("begin-ran.txt") {
		t.Fatalf("begin branch ran on the offline token — detect mis-routed; vixd log:\n%s", h.Daemon.LogTail(80))
	}

	// The run is recorded as skipped, not an error: a wake-before-network run
	// must leave no red failure behind.
	statePath := h.HomePath(".vix/jobs/e2e-gh-offline/state.json")
	if !pollUntil(10*time.Second, func() bool {
		b, err := os.ReadFile(statePath)
		return err == nil && strings.Contains(string(b), `"last_status": "skipped"`)
	}) {
		b, _ := os.ReadFile(statePath)
		t.Fatalf("offline run was not recorded as skipped; state:\n%s", string(b))
	}
	b, _ := os.ReadFile(statePath)
	if strings.Contains(string(b), `"last_status": "error"`) {
		t.Fatalf("offline run recorded an error status; state:\n%s", string(b))
	}

	h.UI.Shot("github-job-offline-skip")
}

// TestGithubJobMisconfigDenies pins the other side of A1: a genuine setup
// problem (detect emits `none`) still fails loudly via the deny step, so a
// real misconfiguration is never silently swallowed.
func TestGithubJobMisconfigDenies(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.github_misconfig_deny",
		Description: "a genuine preflight setup problem (none) still fails loudly via deny",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-gh-deny/job.json", githubDetectRouteJobSpec("e2e-gh-deny", "none")),
	)

	h.UI.WaitStable(500 * time.Millisecond)

	if !pollUntil(20*time.Second, func() bool {
		return h.FS.Exists("deny-ran.txt")
	}) {
		t.Fatalf("deny branch never ran; vixd log:\n%s", h.Daemon.LogTail(80))
	}
	if h.FS.Exists("offline-ran.txt") || h.FS.Exists("begin-ran.txt") {
		t.Fatalf("a non-deny branch ran on the none token — detect mis-routed; vixd log:\n%s", h.Daemon.LogTail(80))
	}

	statePath := h.HomePath(".vix/jobs/e2e-gh-deny/state.json")
	if !pollUntil(10*time.Second, func() bool {
		b, err := os.ReadFile(statePath)
		return err == nil && strings.Contains(string(b), `"last_status": "error"`)
	}) {
		b, _ := os.ReadFile(statePath)
		t.Fatalf("deny run was not recorded as an error; state:\n%s", string(b))
	}

	h.UI.Shot("github-job-misconfig-deny")
}
