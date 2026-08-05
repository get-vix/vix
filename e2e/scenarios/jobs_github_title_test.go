package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// These scenarios pin the per-item thread title for successful GitHub triage
// and review job runs: the daemon parses the run's deterministic findings
// header ("Triaging issue #N: <title>" / "Reviewing pull request #N: <title>")
// into a title of the form "[owner/repo] <job action> #N - <first six words>".
// A self-contained single-agent inline workflow stands in for the real
// githubWatchWorkflow (whose gh/API branch matrix is covered elsewhere); this
// focuses on the daemon-side title derivation, which is workflow-shape-agnostic.

const triageTitleJobSpec = `{
  "id": "e2e-triage-title",
  "name": "Triage GitHub issues (get-vix/vix)",
  "enabled": true,
  "trigger": {"type": "at", "time": "2099-01-01T00:00:00Z"},
  "prompt": "Triage the next open issue for get-vix/vix.",
  "cwd": "{{WORKDIR}}",
  "created_by": "web",
  "permissions": {"auto_write": true, "auto_dirs": true},
  "workflow": {
    "name": "e2e-triage",
    "entry_point": {"id": "act"},
    "steps": {
      "act": {"type": "agent", "agent": "general", "prompt": "Triage one issue."}
    }
  }
}`

const reviewTitleJobSpec = `{
  "id": "e2e-review-title",
  "name": "Review GitHub PRs (get-vix/vix)",
  "enabled": true,
  "trigger": {"type": "at", "time": "2099-01-01T00:00:00Z"},
  "prompt": "Review the next open pull request for get-vix/vix.",
  "cwd": "{{WORKDIR}}",
  "created_by": "web",
  "permissions": {"auto_write": true, "auto_dirs": true},
  "workflow": {
    "name": "e2e-review",
    "entry_point": {"id": "act"},
    "steps": {
      "act": {"type": "agent", "agent": "general", "prompt": "Review one PR."}
    }
  }
}`

// TestJobTriageThreadTitle fires a triage-style job whose agent emits the
// deterministic issue header and asserts the persisted thread title uses the
// per-item "[repo] <action> #N - <title>" form (long title trimmed to six
// words with an ellipsis).
func TestJobTriageThreadTitle(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.triage_title",
		Description: "a successful GitHub triage run is titled '[repo] Triage GitHub issues #N - <first six words>'",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-triage-title/job.json", triageTitleJobSpec),
	)

	h.Mock.Enqueue(
		harness.Text("# [Triage GitHub issues (get-vix/vix)] Triaging issue #53: Retry backoff grows without any upper bound\n\nHi, I looked at issue #53: Retry backoff grows without any upper bound. Here is my triage:\n\n**In plain English**\nThe waiting time keeps doubling forever.\n\n**Draft reply**\nThanks, confirmed."),
	)
	h.UI.WaitStable(500 * time.Millisecond)

	out, err := h.RunCLI("job", "run", "e2e-triage-title")
	if err != nil {
		t.Fatalf("vix job run failed: %v\n%s", err, out)
	}

	var rec planRunRecord
	if !pollUntil(30*time.Second, func() bool {
		r, ok := planRunFor(h, "e2e-triage-title")
		if ok && r.JobStatus != "" {
			rec = r
			return true
		}
		return false
	}) {
		t.Fatalf("triage job run not persisted; stdout=%q\n%s", out, h.Daemon.LogTail(80))
	}
	if rec.JobStatus != "ok" {
		t.Fatalf("job status = %q, want ok\n%s", rec.JobStatus, h.Daemon.LogTail(80))
	}
	wantTitle := "[get-vix/vix] Triage GitHub issues #53 - Retry backoff grows without any upper…"
	if rec.Title != wantTitle {
		t.Errorf("title = %q,\n want %q", rec.Title, wantTitle)
	}

	h.UI.Key("f1")
	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("triage-title")
}

// TestJobReviewThreadTitle is the pull-request counterpart: the review header
// yields "[repo] Review GitHub PRs #N - <title>".
func TestJobReviewThreadTitle(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.review_title",
		Description: "a successful GitHub PR review run is titled '[repo] Review GitHub PRs #N - <first six words>'",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-review-title/job.json", reviewTitleJobSpec),
	)

	h.Mock.Enqueue(
		harness.Text("# [Review GitHub PRs (get-vix/vix)] Reviewing pull request #42: Bump mock timeout\n\nHi, I looked at pull request #42: Bump mock timeout. Here is my review:\n\n**Verdict**\nLooks good."),
	)
	h.UI.WaitStable(500 * time.Millisecond)

	out, err := h.RunCLI("job", "run", "e2e-review-title")
	if err != nil {
		t.Fatalf("vix job run failed: %v\n%s", err, out)
	}

	var rec planRunRecord
	if !pollUntil(30*time.Second, func() bool {
		r, ok := planRunFor(h, "e2e-review-title")
		if ok && r.JobStatus != "" {
			rec = r
			return true
		}
		return false
	}) {
		t.Fatalf("review job run not persisted; stdout=%q\n%s", out, h.Daemon.LogTail(80))
	}
	if rec.JobStatus != "ok" {
		t.Fatalf("job status = %q, want ok\n%s", rec.JobStatus, h.Daemon.LogTail(80))
	}
	// "Bump mock timeout" is four words: kept whole, no ellipsis.
	wantTitle := "[get-vix/vix] Review GitHub PRs #42 - Bump mock timeout"
	if rec.Title != wantTitle {
		t.Errorf("title = %q,\n want %q", rec.Title, wantTitle)
	}

	h.UI.Key("f1")
	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("review-title")
}
