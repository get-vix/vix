# AGENTS.md

This file provides guidance to AI coding agents when working with code in this repository.

## Project Overview

Vix is an AI coding agent built in Go. It consists of a daemon backend that handles LLM interactions, tool execution, and code analysis, paired with a TUI client for user interaction.

## Architecture

```
cmd/
  vix/            # TUI client entry point
  vixd/           # Daemon server entry point
internal/
  agent/          # Agent loop, LLM streaming, tool schemas
  config/         # API key and configuration loading
  daemon/         # Unix socket server, session management, tool handlers
    brain/        # Code analysis engine (scanner, parser, semantic analysis)
      lsp/        # Language server protocol integration
  headless/       # Headless mode (no TUI)
  protocol/       # Shared types between client and daemon
  ui/             # Bubble Tea TUI components
```

The daemon listens on a Unix socket (`/tmp/vixd.sock`). The TUI client connects to it and exchanges JSON events.

## Development Commands

```bash
# Build the web UI then both binaries (standard dev workflow)
make build

# Build the web UI only
make build-web

# Build for all release platforms (darwin-arm64, linux-amd64, linux-arm64)
make build-all

# Run tests
make test

# Publish a release
make release VERSION=v1.x.x

# Run a specific test
go test ./internal/daemon/... -run TestSessionHandlePlan -v
```

The web UI source (`internal/daemon/web/source/`) is a Vite + React + TypeScript
project kept in a **private git submodule**, so it isn't present in a public
clone. Its built output (`internal/daemon/web/dist/`) is committed to git and
embedded into the `vixd` binary at compile time via `//go:embed web/dist`.

Because `dist/` is committed, `make build` works without the source — `build-web`
no-ops and the existing `dist/` is embedded as-is. Maintainers with submodule
access run `make web-source` once to fetch the source, then `make build-web`
after any frontend changes and commit the regenerated `dist/`. Use `make pull` to
sync the latest source from the submodule and rebuild.

## Running

Start the daemon and client in separate terminals:

```bash
./bin/vixd
./bin/vix
```

`vix` never spawns the daemon: when the socket is unreachable it errors with
"vixd is not running — start it with `vix daemon start`". The subcommand group
`vix daemon start|stop|status|install|uninstall` manages the long-lived daemon
(`install` registers a login LaunchAgent/systemd user unit). Client and daemon
enforce an exact version match (hard gate; `dev` builds skip it) — a mismatch
means restart the daemon: `vix daemon stop && vix daemon start`.

## Scheduled jobs

vixd runs a scheduler over `~/.vix/jobs/*.json` (hot-reloaded; runtime state in
`~/.vix/jobs-state.json` — spec/state split so user files never churn). Each
run executes in an isolated headless session (plain prompt, or a workflow via
the `workflow` field) and lands in the Sessions tab under "Vix-initiated".
Triggers: `cron` (robfig syntax incl. `@every`) and one-shot `at`. The shipped
`heartbeat` job reads `~/.vix/heartbeat.md` every 30 minutes and skips with
zero tokens while the file is effectively empty (or the run answers
HEARTBEAT_OK). The model-facing surface is the `jobs` skill (no tool, no slash
command): the agent writes job files directly and verifies via
jobs-state.json. Engine: `internal/daemon/jobs/`; runner:
`internal/daemon/job_runner.go`. Kill switch: `"features": {"jobs": false}` or
`VIX_DISABLE_JOBS=1`.

### Detecting whether `vixd` is running (sandbox caveat)

When you (an AI coding agent) are working inside a live vix session, **a `vixd`
daemon is by definition already running** — it is the process serving the LLM
turns you are responding to. Do not conclude it is dead just because you can't
find it.

The agent's `bash` tool runs inside vix's sandbox (Seatbelt on macOS, bwrap on
Linux). From in there the **process table view is partial and inconsistent**: it
does not reliably show the host process that launched the session, even though it
*does* show processes the tool spawns itself. So:

- `pgrep -fl vixd` returning nothing means "not visible from inside the
  sandbox", **not** "not running". `pgrep` and `ps` can even disagree with each
  other in the same session.
- The reliable signal is the **Unix socket plus a working session**: a live
  `/tmp/vixd.sock` (`srwxr-xr-x`) and the fact that the chat is responding are
  far stronger evidence than `pgrep`.
- **Never `rm` the socket or kill/respawn `vixd` based on a `pgrep` miss.**
  Removing `/tmp/vixd.sock` or starting a second daemon can disrupt the running
  session. If you genuinely need a daemon for an out-of-band task (e.g. driving
  the TUI for a VHS recording), prefer an explicit, isolated instance (e.g. a
  separate `--config-dir` and socket path) rather than touching the default one.

## Key Conventions

- **Go style** - follow standard Go conventions, use `gofmt`.
- **Error handling** - return errors, don't panic. Log with `log.Printf` in the daemon.
- **UI events** - the daemon emits events via `s.emit("event.name", data)` which the TUI consumes.
- **No over-engineering** - keep changes minimal and focused. Don't add abstractions for one-time operations.
- **Security** - sanitize all user inputs before shell execution. Be careful with tool execution paths.

## Todo list (user-facing)

The `todo_write`/`todo_read` tools back a live progress panel in the TUI, so the
todo list is **user-facing**: it's how the user follows what the agent is doing
in real time. Treat it as shared state that must always reflect reality, not a
private scratchpad.

- Update the list **every time** you add an item or change one. Mark an item
  `in_progress` right before starting it and `completed` immediately after
  finishing — don't batch status flips or let the panel lag behind actual work.
- `todo_write` has replace semantics: send the full list each time, keeping `id`
  values stable so items map to the same UI row across updates.
- Keep at most one item `in_progress` at a time, and clear the list (send `[]`)
  once the work is done so the user isn't left looking at stale entries.

The daemon nudges the model if it finishes a turn with pending/in-progress todos
(`internal/daemon/session.go`), and the panel renders from `event.todo_list_updated`
(`internal/ui/todopanel.go`, `internal/ui/rightpanel.go`).

## Environment

- **Go 1.26+** required
- **ANTHROPIC_API_KEY** environment variable or `.env` file for LLM access
- **LSP servers** (optional): gopls, pylsp, typescript-language-server for code intelligence
- **LSP config**: `.vix/settings.json` in project root

## Config directory resolution

By default vix merges config from two layered `.vix` directories: `~/.vix` (user defaults) and `./.vix` (project overrides). This covers `settings.json`, `agents/`, `skills/`, `AGENTS.md`, plus session state like `history.txt`, `plans/`, `access_stats.db`, and `logs/`.

All path resolution flows through `config.VixPaths` (internal/config/paths.go). Add new `.vix`-relative paths there rather than hardcoding `filepath.Join(cwd, ".vix", ...)`.

Pass `--config-dir /some/path` to use that directory as the sole `.vix` root. Neither `~/.vix` nor `./.vix` is consulted, and all session state (history, plans, access stats, LLM logs) is written inside the override directory. The directory is auto-created and bootstrapped with default settings on first run. This is useful for sandboxed/reproducible sessions without touching real user or project config.

## Skills

Skills are reusable, task-specific instruction sets. Each skill is a directory under `.vix/skills/<name>/` containing a `SKILL.md` file (YAML frontmatter + markdown body) and, optionally, supporting files (`reference.md`, `scripts/`, etc.). Project skills override user skills on name collision. The engine lives in `internal/agent/skills.go`.

Frontmatter fields parsed today: `name`, `description`, `model`, `allowed-tools`. Body templating supports `$ARGUMENTS`, positional `$1`/`$2`, `` !`cmd` `` dynamic command injection, and `${SKILL_DIR}` (absolute path to the skill directory).

Skills use **progressive disclosure** — three layers loaded only as needed:

1. **Metadata (always present)** — each skill's name + description is injected into the system prompt via `SkillRegistry.FormatForSystemPrompt` (wired in `Session.buildSystemPrompt`). Cheap; lets the model know what exists.
2. **Body (on demand)** — the full `SKILL.md` body loads only when a skill is invoked, via `Skill.LoadForTool` (body with args substituted + a listing of bundled files).
3. **Bundled files (on demand)** — the model reads `reference.md` / runs `scripts/*` with the normal `read_file`/`bash` tools using the absolute paths listed in layer 2.

Two invocation paths, both calling `LoadForTool` under the hood:

- **Implicit (model-driven)** — the `skill` tool (`SkillToolSchema`, dispatched inline in `Session.executeToolDirect`). Appended to the session tool list only when at least one skill is loaded. The model calls `skill(name, arguments?)` when a task matches an advertised skill.
- **Explicit (user-driven)** — typing `/<skill-name> [args]`, intercepted in the input handler before the turn starts and rendered into the user message. Skill names are advertised to the TUI via `event.skills_available` so they autocomplete in the slash menu.

`/skills` lists all loaded skills.

## Default access policy

The agent decides whether a path is accessible by default by checking, in order: cwd, `$HOME`, the host's system directories (per platform), or any entry in `allowed_directories`. Anything outside that set surfaces as a confirmation prompt (interactive sessions) or an error (headless). The `deny_list` always wins, even if the path matches one of the auto-allow categories.

The platform's system directories live in `internal/daemon/platform_policy.go` as a single source of truth shared between the dispatcher's prompt-skip logic and the sandbox profile builders (Seatbelt on macOS, bwrap on Linux). Update one place to widen or tighten what the agent can touch on a given OS.

`$HOME` is auto-allowed in full (read + write). Lock down sensitive subpaths via `deny_list.paths` (e.g. `~/.aws`, `~/.ssh`, `~/.config/op`, `~/.kube`).

## Deny list

`settings.json` supports `deny_list` — paths and URLs that are always off-limits. Use the structured form:

```json
"deny_list": {
  "paths": ["./secrets", "/etc/passwd"],
  "urls":  ["bad.example.com", "https://example.org/admin"]
}
```

The legacy flat-array form (`"deny_list": ["./secrets"]`) still parses and is treated as paths-only. Deny takes precedence over `allowed_directories`: a path that matches both is blocked. Path entries may be absolute or relative to the config file that declares them. Both lists are unioned across layered configs (home + project).

**Path match semantics**: a target path is blocked iff (after symlink resolution and `Clean`) it equals a deny entry or is a descendant of one.

**URL match semantics**:
- Entry with a scheme (e.g. `https://example.com/admin`) — URL-prefix match. Scheme and host are case-insensitive; path is case-sensitive and must align on `/`.
- Entry without a scheme (e.g. `example.com`) — hostname or dot-aligned suffix match (`api.example.com` matches `example.com`; `notexample.com` does not).

Coverage:
- `read_file` / `write_file` / `edit_file` / `delete_file` (and the minified variants): refused before execution when the target path is denied.
- `web_fetch`: refused when the `url` parameter matches a URL deny entry.
- `bash`: refused when any path-like token (a token that contains `/`) in the command resolves inside a denied path, or when any token containing `://` resolves to a denied URL. Bare words without `/` are not treated as paths, so prose like `echo 'no secrets here'` is allowed. Variable expansion, heredocs, and reassembly across variables are **not** analyzed (best-effort v1).
- `grep` / `glob_files`: matches inside a denied path are silently filtered from the output.
