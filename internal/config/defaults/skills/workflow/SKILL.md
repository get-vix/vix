---
name: workflow
description: Author and edit vix workflows — declarative multi-step pipelines (agent/bash/tool/if/fan_out/fan_in nodes) stored in config/workflow.json or embedded inline in a job/hook. Use when the user asks to create, modify, or understand a workflow, or to fan work out across many items and join the results.
---

# Vix workflows

A workflow is a **directed graph**: nodes do work, edges carry data. It is plain
JSON — the engine runs it, so routing between steps costs **zero model tokens**.
Workflows live in one of two places:

- **Named** — an entry in `config/workflow.json` (`{"workflows": [ ... ]}`),
  runnable by name and (unless `display_in_tui: false`) shown in the TUI
  switcher.
- **Inline** — a `workflow` object embedded directly in a job or hook spec.

Edit either with `write_file`/`edit_file`. Always keep the JSON valid; an
invalid workflow is skipped at load with a log line.

## Shape

```json
{
  "name": "My Workflow",
  "summary": "one-line description",
  "entry_point": { "id": "first_step", "params": { "goal": "$(workflow.prompt)" } },
  "budget": { "max_tokens": 400000, "max_iterations": 30, "on_exceeded": { "id": "wrap_up" } },
  "steps": {
    "first_step": { "type": "agent", "...": "..." }
  }
}
```

- `entry_point` — the first step (with optional `params`).
- `budget` — optional caps: `max_tokens`, `max_seconds`, `max_iterations`.
  When any trips, the run routes once to `on_exceeded` (or stops).
- `steps` — a map of `id → step`. Every referenced id must exist (or be the
  special sink `"stop"`).

## Variables and the `$(...)` syntax

Values flow across edges as **typed values** (strings, numbers, lists, objects).
Two surfaces are string-only — bash conditions and prompt text — and there a
value is *projected*: strings pass through, everything else becomes JSON.

- `$(workflow.prompt)` — the run's input prompt.
- `$(workflow.dir)` — the job directory (scheduled runs), else empty.
- `$(workflow.signal.status)` / `$(workflow.iteration)` / `$(workflow.tokens_remaining)` — live accounting.
- `$(step.<id>)` — a step's text output (or its parsed value if `json_output`).
- `$(step.<id>.<field>)` — a field of a step's parsed JSON (set `json_output: true`).
- `$(<param>)` — a param passed into the current step.
- `$(file:path/to.md)` — file contents (prompt templates).
- `$(bash:cmd)` — the trimmed stdout of a shell command.

## Step types

### `agent` — run an LLM agent
```json
{
  "type": "agent",
  "agent": "general",              // a .vix/agents/<name>; OR use "fork_from": "<step_id>"
  "effort": "adaptive",            // low | medium | high | max | adaptive
  "prompt": "$(file:prompts/x.md)",
  "json_output": true,             // parse the reply as JSON → $(step.<id>.<field>)
  "signal": true,                  // expose the workflow_signal tool (complete/blocked)
  "deny_tools": ["write_file"],
  "on_error": { "id": "wrap_up" }, // route here instead of failing the run
  "next_steps": [ { "id": "review" } ]
}
```

### `bash` — run a shell command
```json
{ "type": "bash", "command": "gh pr list --json url", "output": "prs.json",
  "timeout_sec": 60, "next_steps": [ { "id": "pick" } ] }
```

### `tool` — call a built-in tool (e.g. ask_question_to_user)
```json
{ "type": "tool", "tool": "ask_question_to_user", "question": "Approve?",
  "options": [ { "title": "Yes", "steps": [ { "id": "go" } ] } ] }
```

### `if` — branch on a condition (invisible, zero-cost routing)
```json
{ "type": "if",
  "condition": "[ \"$(step.classify.severity)\" = \"high\" ]",
  "then": { "id": "full_audit" },
  "else": { "id": "quick_review" } }
```
`condition` is a bash test (exit 0 = true). `else` is optional (absent = end).
Prefer an `if` node over piling `execute_if` guards on `next_steps` when a step
picks exactly one of a few paths. `if` nodes don't emit step events and don't
consume the iteration budget.

### `fan_out` — run one branch per element of a list (one → many)
```json
{ "type": "fan_out",
  "over": "$(step.discover.targets)",  // a typed LIST (e.g. an agent's json_output array)
  "as": "item",                        // each element is bound as $(item) / $(item.field)
  "barrier_id": "audit",               // pairs this fan_out with its fan_in
  "max_parallel": 8,                    // 0/absent = min(N, GOMAXPROCS)
  "branch": { "id": "audit_one" },     // entry step of the per-element branch chain
  "next_steps": [ { "id": "audit_join" } ] }
```
The list is **dynamic**: an upstream `agent` step with `json_output: true` can
emit `["a","b",...]` (or a list of objects) and the model thereby decides how
many branches run and what each works on. Each branch runs its own chain
starting at `branch.id` and may itself route through `if`/`next_steps` — so
different items can take different depths (a per-item pipeline). Branch steps may
be `bash`, `agent`, or `if` (no nested `fan_out`).

### `fan_in` — join a barrier's branches (many → one)
```json
{ "type": "fan_in",
  "barrier_id": "audit",         // must match exactly one fan_out
  "as": "results",               // the collected list is bound as $(results)
  "on_branch_error": "abort",    // "abort" (default) fails the run; "collect" drops failed branches
  "next_steps": [ { "id": "synthesize", "params": { "findings": "$(results)" } } ] }
```
`$(results)` is the ordered list of each branch's terminal value (its last
step's parsed `json_output`, or text). Pass it to a synthesis `agent` step.

## The canonical pattern: discover → fan_out → (branch pipeline) → fan_in → synthesize

```json
{
  "name": "Route auth audit",
  "entry_point": { "id": "discover" },
  "steps": {
    "discover": {
      "type": "agent", "agent": "general", "json_output": true,
      "prompt": "List every route file under src/routes as a JSON array of {\"path\": ...}.",
      "next_steps": [ { "id": "fanout" } ]
    },
    "fanout": {
      "type": "fan_out", "over": "$(step.discover)", "as": "route",
      "barrier_id": "audit", "branch": { "id": "audit_one" },
      "next_steps": [ { "id": "join" } ]
    },
    "audit_one": {
      "type": "agent", "agent": "general", "json_output": true,
      "prompt": "Audit $(route.path) for missing auth. Return {\"path\":...,\"finding\":...}."
    },
    "join": {
      "type": "fan_in", "barrier_id": "audit", "as": "findings",
      "on_branch_error": "collect",
      "next_steps": [ { "id": "report" } ]
    },
    "report": {
      "type": "agent", "agent": "general",
      "prompt": "Write a report from these findings:\n$(findings)"
    }
  }
}
```

## Rules and gotchas

- **Barriers pair 1:1** — every `fan_out.barrier_id` needs exactly one matching
  `fan_in.barrier_id`, and vice versa.
- **`over` must resolve to a list** — an agent step's `json_output` array, a
  prior `fan_in`'s `as`, or a bash step that printed a JSON array. A non-list is
  a runtime error.
- **Conditions test scalars** — `condition`/`execute_if` are bash; reference
  scalar fields like `$(item.risk)`, not whole lists/objects.
- **No nested `fan_out`** inside a branch (v1).
- **Resume is atomic per fan-out block** — an interrupted run re-runs a fan_out's
  branches; a run resumed at the `fan_in` recovers the joined list.
- Validate your edit by running the workflow (or a job that references it) and
  checking it loads without a `[workflow] invalid workflow` log line.
