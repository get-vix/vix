package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestGithubPlanJobAccessBranches is a staged acceptance spec for the mission
// control "Plan GitHub issues" job (inline workflow githubIssuePlanWorkflow).
// It pins the runtime GitHub-access contract the workflow must honour:
//
//		detect → deny | fetch → nag | select → detail → plan → mark_done
//
//	  - gh signed in   → fetch via `gh`, then `select`/`detail`/`plan` (the plan
//	    appears in the thread; nothing is posted back to GitHub).
//	  - gh missing/unauth but the public API reachable → fetch via `curl`, a `nag`
//	    reminding the user to install + `gh auth login`, then select/detail/plan.
//	  - no access at all (or a missing coreutil: grep/sort/cut/mv) → `deny` prints
//	    a clear error and exits non-zero, so the run is recorded as failed with
//	    that message and no plan is attempted.
//
// Item selection is deterministic and lives in bash, NOT in the agent. A
// line-based tracker file at $(workflow.dir)/tracker.tsv — the run's own job
// directory (~/.vix/jobs/<id>) surfaced to the workflow as the daemon-resolved
// $(workflow.dir) variable — holds one "<state>\t<url>" line per item, with no
// jq or any other JSON dependency. The `select` step reconciles it against the
// freshly fetched items (append new opens as "todo", drop closed/merged), then
// claims EXACTLY ONE "todo" (flipping it to "doing") and prints its URL — or
// "NO_TODO", in which case the graph ends before `plan`, so the run is skipped
// with no thread. `detail` fetches just that one item; `plan` investigates it
// (write tools denied — it never touches the tracker); `mark_done` flips the
// item to "done" whether the agent succeeded (next_steps) or failed (on_error),
// so a failed run still marks the item addressed and it is never retried in a
// loop. The planning instructions are baked into the plan step itself (the job's
// prompt is only a label), so the run does not depend on a user-supplied prompt.
//
// Skipped: scheduled-job runs execute in the daemon's scheduler, not through the
// TUI, and the harness has no job-run primitive (no /api/jobs driver, no way to
// fire a trigger and read back the Vix-initiated thread). The branch logic is
// covered today by the builder's vitest unit tests
// (internal/daemon/web/source/src/data/jobWorkflows.test.ts), and $(workflow.dir)
// resolution + the tracker-file-aware watcher by the workflow engine's Go tests.
// Enable this once the harness can create + run a job and surface its thread
// transcript.
//
// When enabled it proves, per branch, that the right path runs and the plan (or
// error/nag) lands in the run's thread, and that the tracker file under
// $(workflow.dir) is created/updated/trimmed across consecutive runs. The body
// below seeds the gh/curl shims the three branches switch on; the trigger +
// transcript read-back is the missing piece.
func TestGithubPlanJobAccessBranches(t *testing.T) {
	meta := harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.github_plan",
		Description: "the GitHub plan job picks gh/API/none, deterministically claims one todo (skipping with no thread when there's none), shows its framed findings in a per-item-titled thread, and tracks items in $(workflow.dir)/tracker.tsv",
		Wire:        harness.WireMessages,
	}
	harness.SkipScenario(t, meta, "branch matrix needs the frontend-generated githubIssuePlanWorkflow JSON + gh/curl shims; the daemon-side title/transcript/chat-mode are covered live by jobs.plan_thread (jobs_plan_thread_test.go)")

	// "no access" shim: gh absent (not installed) and curl always fails — the
	// detect step must resolve to `none` and route to `deny`.
	ghStub := "#!/bin/sh\nexit 127\n"
	curlFail := "#!/bin/sh\nexit 7\n"

	h := harness.Start(t, meta,
		harness.WithWorkdirFile("bin/gh", ghStub),
		harness.WithWorkdirFile("bin/curl", curlFail),
		harness.WithEnv("PATH", "./bin:/usr/bin:/bin"),
	)

	h.UI.WaitStable(400 * time.Millisecond)

	// The plan step (only reached on the gh/api branches) streams the framed
	// findings, which open with the deterministic header the daemon parses to
	// title the thread.
	h.Mock.Enqueue(harness.Text("Hi, I investigated issue #29 — ANTHROPIC_BASE_URL not resolved from .env files — on GitHub. Here are my findings:\n\nhttps://github.com/get-vix/vix/issues/29\n\n**Summary**\nThe base URL isn't read from .env.\n\n**My take**\nLegit, actionable bug.\n\n**Plan**\n1. Resolve ANTHROPIC_BASE_URL in config loading."))

	// TODO(jobs-harness): create the job (inline githubIssuePlanWorkflow) and
	// fire its trigger, then assert the resulting Vix-initiated thread:
	//   - for the no-access shims above, shows the deny error ("can't reach
	//     GitHub"); for an api shim, the nag + framed findings; for a gh shim,
	//     the framed findings only;
	//   - is titled after the picked item, e.g.
	//     "[Plan GitHub issues (get-vix/vix)] Addressing issue #29 — ANTHROPIC_BASE_URL not resolved from .env files"
	//     (parsed from the findings' deterministic header line);
	//   - keeps the FULL working transcript — the plan-step prompt, the agent's
	//     tool_use/tool_result turns, and the final findings — not a text-only
	//     summary, so a follow-up turn is grounded in the real tool calls.
	// Also assert that after a run the tracker file at ~/.vix/jobs/<id>/tracker.tsv
	// records the claimed item as "done" (even if the agent failed), that a second
	// run claims a different open item, and that a closed item's line is trimmed
	// from the file.
	_ = h.UI.Snapshot()
}
