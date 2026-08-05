---
name: jobs
description: Create and manage scheduled jobs (cron tasks, reminders, heartbeat checks, polling watchers) that vixd runs automatically. Use when the user asks to schedule, automate, monitor, watch, or be reminded about something.
---

# Scheduled jobs

vixd runs a scheduler over per-job directories in `~/.vix/jobs/`. Each job lives
in its own subdirectory holding a `job.json` spec; the directory is
hot-reloaded, so **creating a job = writing `~/.vix/jobs/<id>/job.json` with
`write_file`**. There is no dedicated tool.

Every run executes in an isolated headless thread: either a plain chat turn
with the general agent, or a workflow when `workflow_id` (a named workflow) or
`workflow` (an inline definition) is set. Finished runs appear in the user's
Threads tab under "Vix-initiated".

## Job spec — `~/.vix/jobs/<id>/job.json`

```json
{
  "id": "daily-dep-audit",
  "name": "Daily dependency audit",
  "enabled": true,
  "trigger": { "type": "cron", "expr": "0 9 * * *", "tz": "Europe/Paris" },
  "prompt": "$(file:tasks/daily-audit.md)",
  "workflow_id": "dep-audit",
  "cwd": "/absolute/path/to/project",
  "permissions": { "auto_write": true, "auto_dirs": true },
  "skip_if_empty": false,
  "timeout": "10m",
  "created_by": "agent:<your-thread-id>"
}
```

Field rules:

- `id` — defaults to the directory name; must be unique.
- `trigger` — exactly one of:
  - `{"type": "cron", "expr": "...", "tz": "..."}` — standard 5-field cron
    (`*/30 9-19 * * *`) **or** descriptors (`@every 30m`, `@hourly`, `@daily`).
    Hour windows live in the cron hour field (`*/30 9-19 * * *` = every 30 min,
    9am-7pm). There is no separate interval or active-hours field. An odd
    interval windowed to hours ("every 90m, 9-19 only") cannot be expressed in
    one job: use `*/45 9-19 * * *` or two jobs.
  - `{"type": "at", "time": "2026-01-10T09:00:00Z"}` — one-shot (RFC3339).
    Runs once, then the job is marked completed (kept on disk, not re-fired).
- `prompt` — required. Literal text, `$(file:relative/path)` (resolved against
  `cwd` **at fire time**, so editing the file changes the next run), or a mix.
- `workflow_id` / `workflow` — optional, **at most one**. `workflow_id` names a
  workflow defined in `config/workflow.json`; `workflow` is a self-contained
  inline definition (same schema as a `config/workflow.json` entry, embedded
  directly in the job — no separate file needed). Either way the run executes
  that workflow with the resolved prompt as `$(workflow.prompt)`. Setting both
  is a validation error. Both unset: a plain chat turn. Prefer inline `workflow`
  for a one-off pipeline used only by this job; use `workflow_id` to share one
  definition across jobs/hooks/the TUI.
- `cwd` — required, absolute. The project the run works in.
- `permissions` — both default **true** (unattended runs write without
  confirmation). Set `auto_write`/`auto_dirs` to `false` to restrict; denied
  operations are recorded in the run result instead of prompting.
- `skip_if_empty` — when true and the resolved prompt is effectively empty
  (only blank lines, `#` headings, HTML comments) or its file is missing, the
  run is skipped before any model call. Zero tokens.
- `timeout` — Go duration, default `10m`. The run is cancelled past it.
- `created_by` — set `"agent:<thread-id>"` when you create a job.

## After writing a job: verify it registered

The scheduler validates on (re)load and records each job's runtime state in
that job's own `~/.vix/jobs/<id>/state.json` (a sibling of `job.json`). Always
read it back after creating or editing a job and check the fields:

- `validation_error` non-empty → fix the spec.
- `next_run_at` set → scheduled correctly.
- Later: `last_status` (`ok | error | skipped | timeout`), `last_error`,
  `last_thread_id` (the run's thread), `consecutive_errors` (5 in a row
  auto-disables the job until the spec file is edited).
- `recent_runs` — the last 10 runs (newest last), each with `at`, `status`,
  `error`, `thread_id`, and `duration`. Useful for spotting a flapping job.

To test-fire a new job, give it a near-due schedule (an `at` a minute out, or
`@every 1m`), watch one run land, then set the real schedule.

## Heartbeat (already installed)

`~/.vix/jobs/heartbeat/job.json` ships enabled: every 30 minutes (9:00-19:59) it
reads `~/.vix/jobs/heartbeat/heartbeat.md` and follows it. The file is the *whiteboard*: add
or remove tasks there — never touch the job. While the file holds only
headings/comments the run skips with zero tokens. A run whose final answer is
`HEARTBEAT_OK` also leaves no trace; anything else surfaces in the Threads
tab. To give the user recurring checks, **append tasks to heartbeat.md**
rather than creating new jobs, unless the schedule must differ.

## Polling recipe (react to external events — no webhooks)

Combine a frequent job with a workflow whose first step is a **bash** step and
whose agent step is gated by `execute_if`. Nothing new → no model call, no
thread, recorded as skipped. Example workflow (in
`~/.vix/config/workflow.json`):

```json
{
  "name": "watch-prs",
  "display_in_tui": false,
  "entry_point": { "id": "poll" },
  "steps": {
    "poll": {
      "type": "bash",
      "command": "gh pr list --json number,title --search \"created:>$(date -v-2M '+%Y-%m-%dT%H:%M:%S')\" | grep -v '^\\[\\]$' || true",
      "silent": true,
      "next_steps": [
        { "id": "react", "params": { "prs": "$(step.poll)" }, "execute_if": "[ -n \"$(step.poll)\" ]" }
      ]
    },
    "react": {
      "type": "agent",
      "agent": "general",
      "input_params": { "prs": { "description": "New PRs found by the poll step" } },
      "prompt": "New pull requests were just opened:\n$(prs)\n\nReview each briefly and summarise anything risky.",
      "stream": true
    }
  }
}
```

Job: `{"trigger": {"type": "cron", "expr": "@every 2m"}, "workflow_id": "watch-prs", "prompt": "poll", ...}`.

For a one-off watcher you can skip `config/workflow.json` entirely and embed the
same definition inline under the job's `workflow` field instead of referencing
it by `workflow_id`.

## Workflow knobs that matter for jobs

- `display_in_tui: false` — hides the workflow from the user's Shift+Tab
  switcher (still runnable by jobs). Use for internal plumbing workflows.
- `budget` — `{"max_tokens": N, "max_seconds": N, "max_iterations": N}` caps a
  runaway run; strongly recommended for frequent jobs.
- per-step `deny_tools` — block specific tools in unattended runs.
- Plain-prompt jobs (no workflow) have **only** `timeout` and `permissions` as
  guardrails — prefer a workflow with a budget for anything frequent.

## Safety notes (tell the user when relevant)

- Permissions default to true: scheduled runs read/write unattended. The
  `deny_list` in settings.json and workflow `deny_tools` are the brakes.
- The scheduler lives in vixd: jobs fire only while the daemon runs. Runs
  missed while it was down are capped-caught-up at next start (up to 3
  immediate, the rest skipped). `vix daemon install` (macOS LaunchAgent /
  Linux systemd) makes vixd start at login.
- Kill switch: `"features": {"jobs": false}` in settings.json, or
  `VIX_DISABLE_JOBS=1`.
