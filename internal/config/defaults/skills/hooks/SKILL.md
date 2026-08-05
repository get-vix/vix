---
name: hooks
description: Create and manage lifecycle hooks that vixd fires automatically on agent-loop events (a tool about to run, a prompt submitted, a thread starting, a turn finishing). Use when the user wants to enforce a rule, block or rewrite a tool call, validate prompts, auto-format, notify, or react to what the agent does.
---

# Lifecycle hooks

vixd fires user-authored hooks on agent-loop events. Each hook lives in its own
subdirectory under `~/.vix/hooks/` holding a `hook.json` spec; the directory is
hot-reloaded, so **creating a hook = writing `~/.vix/hooks/<id>/hook.json` with
`write_file`**. There is no dedicated tool. Keep any helper script the hook runs
(e.g. `script.sh`) in the same directory.

A hook either runs **synchronously** and returns a decision that can veto or
rewrite the triggering action (`"mode": "sync"`), or **asynchronously**,
fire-and-forget, in an isolated thread (`"mode": "async"`, the default).

> Hooks are guardrails, not a security boundary. The hard boundary is the
> `deny_list` + permission system, which runs *before* any hook. A blocking hook
> can veto the common tool paths, but don't rely on it as airtight enforcement.

## Hook spec — `~/.vix/hooks/<id>/hook.json`

```json
{
  "id": "block-env-writes",
  "name": "Block .env writes",
  "enabled": true,
  "trigger": { "event": "PreToolUse", "matcher": "write_file|edit_file" },
  "mode": "sync",
  "blocking": true,
  "command": "$HOME/.vix/hooks/block-env-writes/script.sh",
  "cwd": "",
  "timeout": "5s",
  "created_by": "agent:<your-thread-id>"
}
```

Exactly one action runs — `command`, a workflow (`workflow_id` or `workflow`),
or `prompt`:

- `command` — a shell command (run via `bash -c`). The fast, deterministic path;
  recommended for sync/blocking hooks.
- `workflow_id` — a workflow name defined in `config/workflow.json` (a single
  bash step works great for a fast veto).
- `workflow` — an inline workflow definition (same schema as a
  `config/workflow.json` entry) embedded directly in the hook, for a one-off
  pipeline that needs no separate file. Mutually exclusive with `workflow_id`.
- `prompt` — a plain prompt evaluated by an LLM in an isolated thread.

### Events (`trigger.event`)

| Event | When | Matcher filters | Can block |
|-------|------|-----------------|-----------|
| `PreToolUse` | before a tool runs | tool name | yes (deny / rewrite input) |
| `PostToolUse` | after a tool completes | tool name | no (append context only) |
| `UserPromptSubmit` | before a prompt enters the turn | — | yes (deny / rewrite prompt) |
| `PermissionRequest` | before the user is asked to confirm a tool | tool name | yes (deny → skip prompt) |
| `ThreadStart` | when a thread begins | source (`startup`/`resume`) | no |
| `Stop` | when a turn finishes | — | no |
| `PreCompact` | before the conversation is compacted | trigger (`auto`/`manual`) | no |
| `PostCompact` | after a successful compaction | trigger (`auto`/`manual`) | no |
| `SubagentStart` | when a subagent is spawned | agent type | no |
| `SubagentStop` | when a subagent finishes | agent type | no |

`matcher` is a regex anchored to the whole field; `""` or `"*"` matches all.
Examples: `bash`, `write_file|edit_file`, `mcp__.*`.

### Modes & blocking

- `mode: "async"` (default) — fire-and-forget; cannot block. Async
  workflow/prompt hooks land in the Threads tab under "Vix-initiated".
- `mode: "sync"` — runs inline; the agent waits. Only sync hooks can return a
  decision. Add `"blocking": true` to let a deny/modify actually take effect
  (only valid for `PreToolUse`, `UserPromptSubmit`, and `PermissionRequest`). A
  non-blocking sync hook can still inject context but cannot veto.

Sync hooks have a tight default timeout (5s; async 10m). A timed-out or broken
command **fails open** (the action proceeds) so a bad hook never wedges the loop.

## The decision contract

**Command hooks** decide via exit code and stdout:

- exit `0`, no output → allow
- exit `0`, plain text → that text is injected as context for the model
- exit `0`, JSON → an explicit decision (below)
- exit `2` → deny; the reason is stderr (or stdout)
- any other exit → allow (fail-open)

JSON decision (stdout):

```json
{ "behavior": "deny",    "reason": "Destructive command blocked." }
{ "behavior": "modify",  "input": { "path": "safe.txt" } }
{ "behavior": "context", "context": "Note shown to the model." }
{ "behavior": "allow" }
```

For `PreToolUse`, `modify.input` replaces the tool's arguments (the modified
call is still checked against the deny_list). For `UserPromptSubmit`,
`modify.input.prompt` rewrites the prompt text.

**Workflow / prompt hooks** decide via their final text: a JSON object as above,
or a line starting with `BLOCK: <reason>` to deny, otherwise the text becomes
context.

When several hooks match one event, the most restrictive wins
(`deny > modify > context > allow`); context from every hook is concatenated.

## Hook context (stdin / `$()`)

Every hook receives a JSON envelope (command hooks: on stdin; workflow/prompt
hooks: appended to the message). Use it to filter inside the hook body:

```json
{
  "thread_id": "...", "hook_event_name": "PreToolUse", "cwd": "/path",
  "model": "anthropic/...", "permission_mode": "default", "origin": "user",
  "headless": false, "thread_mode": "chat", "agent": "general", "turn_count": 3,
  "vix_bin": "/usr/local/bin/vix", "socket_path": "/tmp/vixd.sock",
  "tool_name": "write_file", "tool_input": { "path": "..." }
}
```

`origin` is `"user"` for user-started threads or `"vix"` for daemon-initiated
ones; `trigger_type`/`trigger_ref` identify the job/hook that started a vix
thread. `vix_bin` and `socket_path` let a hook call back into *this* daemon
without guessing the binary path or socket — e.g.
`"$vix_bin" thread create --socket-path "$socket_path"`. Event-specific
fields: `tool_name`/`tool_input` (tool events and `PermissionRequest`, plus
`requested_dirs` when directory access is requested), `tool_response`/`is_error`
(PostToolUse), `prompt` (UserPromptSubmit), `source` (ThreadStart),
`trigger` (`PreCompact`/`PostCompact`, with `summarized_turns`/`from_tokens` on
`PostCompact`), `agent_type`/`agent_id` (`SubagentStart`/`SubagentStop`, plus
`prompt` on start and `result`/`is_error` on stop).

> `PermissionRequest` fires only when vix is about to ask you to confirm a tool
> call (interactive threads). A blocking deny skips the prompt and rejects the
> tool; allow / no-opinion lets the normal prompt proceed.

> Recursion guard: hooks never fire inside vix-initiated threads (`origin
> == "vix"`), so a hook's own tool calls can't re-trigger hooks.

## Create a conversation (notify the user)

A hook often needs to *tell the user something* — surface a finding, ask for
feedback, flag a result. Use `vix thread create` to drop a one-message
conversation into the Threads tab under "Vix-initiated" without re-encoding any
on-disk format. It reads a JSON spec from stdin (or `--json` / `--file`), and
should call back through the daemon using `vix_bin`/`socket_path` from the
context:

```bash
ctx=$(cat)
vix_bin=$(printf '%s' "$ctx" | sed -n 's/.*"vix_bin":"\([^"]*\)".*/\1/p')
sock=$(printf '%s' "$ctx" | sed -n 's/.*"socket_path":"\([^"]*\)".*/\1/p')
"${vix_bin:-vix}" thread create --socket-path "$sock" <<JSON
{ "message": "Heads up: 3 dependencies have new security advisories.",
  "cwd": "$HOME", "title": "Dependency advisory" }
JSON
```

Spec fields: exactly one of `message` or `message_file` (an absolute path whose
contents become the message — handy for multi-line markdown without JSON
escaping), plus a required `cwd` (must be an existing directory); `title` and
`unread` (default true) are optional. The thread is created with
`origin: "vix"`, so it groups under "Vix-initiated" and — thanks to the
recursion guard — never fires hooks itself. When the user opens it and replies,
it continues as a normal conversation. Zero LLM tokens: the message is your
literal text.

This is the cheap way for a **command** hook to reach the user. Command hooks
spawn no thread of their own, so they're ideal for bookkeeping (e.g. count
events in a file) that only occasionally calls `vix thread create` once a
threshold is crossed. (An async workflow/prompt hook also lands in
"Vix-initiated", but it spends a thread/LLM turn every time it fires.)

## Example: block writes to protected files (deterministic)

`~/.vix/hooks/protect/hook.json`:

```json
{
  "id": "protect", "enabled": true, "mode": "sync", "blocking": true,
  "trigger": { "event": "PreToolUse", "matcher": "write_file|edit_file|delete_file" },
  "command": "p=$(jq -r .tool_input.path); case \"$p\" in *.env|*/secrets/*) echo \"protected: $p\" >&2; exit 2;; esac; exit 0"
}
```

## Example: notify when a subagent finishes (async)

`~/.vix/hooks/subagent-done/hook.json`:

```json
{
  "id": "subagent-done", "enabled": true, "mode": "async",
  "trigger": { "event": "SubagentStop" },
  "command": "printf '%s finished\\n' \"$(jq -r .agent_type)\" >> ~/.vix/subagents.log"
}
```

## Verification

After writing a hook file, read it back and confirm it parses. To check it is
active, trigger its event (e.g. ask the agent to write a matching file) and
confirm the deny/modify/context behaviour. Invalid specs are skipped; common
mistakes: setting more than one action (command / workflow_id / workflow /
prompt — and `workflow_id` and `workflow` are mutually exclusive), `blocking`
without `"mode": "sync"`, or `blocking` on a non-blockable event.

After a hook fires, its outcome is recorded in that hook's own
`~/.vix/hooks/<id>/state.json` (a sibling of `hook.json`, machine-written —
never hand-edit). It carries `last_fired_at` / `last_status` / `last_error` and
a `recent_runs` history of the last 10 fires, each with `at`, `status` (sync:
`allow | deny | context | modify`; async: `done | error`), `async`, `event`,
`error`, `thread_id` (async workflow/prompt hooks only), and `duration`. A
manual `vix hook trigger <id>` is recorded too (as an async fire). Read it back
to confirm a hook is firing and how it is resolving. The full audit trail of
every fire stays in the run log under `~/.vix/logs/hooks/<date>.jsonl`.

## Kill switch

Disable the whole engine with `"features": { "hooks": false }` in
`settings.json`, or `VIX_DISABLE_HOOKS=1`.
