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
  daemon/         # Unix socket server, thread management, tool handlers
    brain/        # Code analysis engine (scanner, parser, semantic analysis)
      lsp/        # Language server protocol integration
  headless/       # Headless mode (no TUI)
  protocol/       # Shared types between client and daemon
  ui/             # Bubble Tea TUI components
```

The daemon listens on a Unix socket (`/tmp/vixd.sock`). The TUI client connects to it and exchanges JSON events.

### Instance control channel

Besides per-thread connections, each vix window (TUI **instance**) holds one
long-lived `instance.register` connection to the daemon for its whole lifetime.
This is the window's **control channel**: the daemon pushes **process-level**
events — `threads_changed`, `jobs_changed`, and the coordinated `quit` — to it
**once per window**, independent of any chat thread. A launch-time draft (no
thread yet) therefore still refreshes the Threads tab's *Vix-initiated* group
live, and windows aren't notified once per open thread.

The daemon keeps a registry of live instance connections (each drained by a
single serialized writer goroutine) and fans these events out via
`Server.BroadcastToInstances` (`internal/daemon/server.go`); thread-scoped
events (`job_run`/`job_done` status lines) still travel per-thread via
`BroadcastEvent`. The TUI reads the channel from launch
(`startInstanceEventLoop`, `internal/ui/model.go`) and routes events into the
existing `fetchVixThreads` / `fetchJobsAndHooks` / quit handlers. The web-UI
(mission-control) path is separate — it uses `notifySubscribers`, untouched.

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
go test ./internal/daemon/... -run TestThreadHandlePlan -v
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

## Whiteboard diagrams (mermaid-ascii fork)

Terminal Mermaid rendering (`internal/whiteboard`, `RenderASCII`) delegates to the
vendored `github.com/pgavlin/mermaid-ascii`. We currently pin it to a **personal
fork** (`github.com/kirby88/mermaid-ascii`) via a `replace` in `go.mod`, carrying
a diamond/decision-node rendering fix that is **upstreamed as
[pgavlin/mermaid-ascii#1](https://github.com/pgavlin/mermaid-ascii/pull/1)**.

Because the fork is committed in `vendor/` + `go.sum`, ordinary builds and CI use
the vendored copy and never contact `sum.golang.org` — the checksum-DB dance below
is only needed by a **maintainer re-vendoring** after a fork change.

**Iterating on the fork:**

1. Edit the fork clone (branch `fix-diamond-rendering`), `go test ./...`, commit,
   `git push` (this also updates PR #1).
2. Repoint vix and re-vendor:

   ```bash
   go mod edit -replace github.com/pgavlin/mermaid-ascii=github.com/kirby88/mermaid-ascii@<newcommit>
   GOSUMDB=off GOFLAGS=-mod=mod go mod download github.com/pgavlin/mermaid-ascii && go mod vendor
   ```

   `GOSUMDB=off` bypasses the `500` that `sum.golang.org` returns for freshly
   pushed fork commits it hasn't indexed yet; the hash lands in `go.sum` on first
   use, so it's a one-shot per commit.

**When PR #1 merges upstream:** drop the `replace` block from `go.mod`, bump the
`github.com/pgavlin/mermaid-ascii` `require` to pgavlin's new commit, and
`go mod vendor`. (The `replace` line in `go.mod` carries the same reminder.)

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
enforce an exact version match (hard gate, no `dev` exemption; two local `dev`
builds match each other literally) — a mismatch
means restart the daemon: `vix daemon stop && vix daemon start`.

## MCP servers

MCP (Model Context Protocol) servers are configured **home-only** under
`mcp_servers` in `~/.vix/settings.json` (never project-local). Each entry is an
`mcp.ServerConfig` (`internal/daemon/mcp/types.go`): `name`, `type`
(`stdio`/`url`), transport fields (`command`/`args`/`env` or `url`/`headers`),
`allowed_tools`, `require_confirmation`, and `enabled` (a `*bool`; omitted =
enabled, the opt-out default). Their tools are exposed to the agent as
`mcp__<server>__<tool>`.

Connections are **per-thread**: `mcp.Pool` (`internal/daemon/mcp/pool.go`) is
built in `Thread.initBrain`, skipping disabled servers and (for URL servers)
deny-listed addresses.

### OAuth for url servers

A `url` server may add an `oauth` block (`mcp.OAuthConfig`, `types.go`) to
authenticate via the OAuth 2.0 authorization-code flow (PKCE + loopback
redirect) instead of static headers:

```json
{"name": "drive", "type": "url", "url": "https://drivemcp.googleapis.com/mcp/v1",
 "oauth": {"client_id": "…", "client_secret": "${GDRIVE_SECRET}", "scopes": ["…"]}}
```

`auth_url`/`token_url` are optional — when omitted vix **auto-discovers** them
from the server (RFC 9728 protected-resource metadata → RFC 8414 authorization-
server metadata; `oauth_discovery.go`). `client_secret` accepts `${VAR}`.

The **redirect URI** is hosted on the mission-control web server: a single fixed
`http://127.0.0.1:<web-port>/mcp/oauth/callback` (default port 1337) that is
**registered once and reused for every OAuth MCP server**
(`Server.handleMCPOAuthCallback`, route added in `webserver.go`). The route is
unauthenticated by design (the provider's browser redirect carries no vix token)
but only completes a flow whose unguessable `state` matches a live pending entry
in the daemon's `state → *mcp.AuthFlow` registry (`registerMCPAuthFlow` /
`takeMCPAuthFlow`, single-use, expiring). When the web UI is **disabled**
(`--web-port 0`) vix falls back to a **self-hosted ephemeral loopback listener**
(`mcp.Authorize`); pin its port with `redirect_port` and register
`http://127.0.0.1:<port>/callback` instead.

Tokens are stored through the vix credential store (OS keyring, `0600`
`auth.json` fallback) under key `mcp-oauth-<server>` (`mcp_oauth.go`,
`mcpTokenStore`), refreshed automatically, and injected as `Authorization:
Bearer`; a `401` triggers one refresh+retry (`client_http.go`). Consider
deny-listing the credential file. The interactive flow is a one-time step —
headless job threads never open a browser; they only consume/refresh a
previously stored token (else `ErrNeedsAuth`).

Authenticate out of band with **`vix mcp auth <server>`** (or `vix mcp logout
<server>`), or from the **F4 MCP tab**: an OAuth server with no token shows
`needs auth`; press `a` to authenticate, `o` to sign out. The daemon owns the
loopback listener and token exchange (`Server.BeginMCPAuth`/`LogoutMCP`,
`mcp.authorize`/`mcp.logout` handlers); the CLI/TUI just open the browser and
poll `mcp.list`, whose `MCPServerSummary.Auth` is `authenticated` / `needs_auth`
/ `""`.

The TUI **MCP tab (F4)** lists every configured server with its transport type,
status (`connected`/`error`/`disabled`), and tool count, and toggles a server's
`enabled` field with Space. It is daemon-global (MCP is home-only). Data flow
mirrors the Jobs tab: `mcp.list` / `mcp.set_enabled` handlers (`handlers.go`) →
`Server.MCPServerSummaries` / `Server.SetMCPEnabled`
(`internal/daemon/mcp_servers.go`) → `protocol.MCPServerSummary`. `mcp.list`
probes enabled servers on demand (`mcp.ProbeServers`, bounded timeout);
`mcp.set_enabled` surgically edits the home `settings.json` and broadcasts
`event.mcp_changed` so open tabs refresh. TUI wiring lives in
`internal/ui/mcp.go` (fetch/toggle/render) and `internal/ui/model.go`
(`TabKindMcp`, F-keys). The F-key order is Threads F1, Workspace F2, Models F3,
**MCP F4**, Jobs & Triggers F5, Settings F6.

## Workflows

Workflows are declarative multi-step graphs (`internal/workflow` = data model +
`Load`/`Validate`; `internal/daemon/workflow.go` = executor). They live in
`config/workflow.json` (named, layered/managed) or inline in a job/hook spec.
Nodes do work; edges (`next_steps` with `params`) carry data. Routing runs in
the Go engine (zero model tokens). The shipped **Goal** and **Plan** workflows
live in `internal/config/defaults/config/workflow.json`; the model-facing surface
is the `workflow` skill (`internal/config/defaults/skills/workflow/`).

Values crossing edges are **typed** (`StepResult.Value any`); a string
projection is derived only for the two string-only surfaces — bash
`condition`/`execute_if` and prompt templating (scalars pass through,
lists/objects become JSON). Helpers: `buildStepVars` (string pool),
`buildTypedStepVars` (typed pool), `projectToString`.

Step `type`s:

- `agent` / `bash` / `tool` — the original work nodes.
- `if` — `{condition, then, else}`; invisible zero-cost routing (no step event,
  no iteration-budget cost). Prefer over piling `execute_if` on `next_steps`.
- `fan_out` — `{over, as, barrier_id, branch, max_parallel, next_steps}`. Runs
  one branch chain per element of the typed list `over` resolves to (dynamic N;
  an upstream agent `json_output` array lets the model size it). Each branch
  binds the element as `$(<as>)` and runs its own chain (bash/agent/if — no
  nested fan_out), so branches can take different depths. Bounded by
  `max_parallel` (default `min(N, GOMAXPROCS)`). Branch execution:
  `Thread.runBranchChain`.
- `fan_in` — `{barrier_id, as, on_branch_error, next_steps}`. Joins the matching
  barrier into the ordered list `$(<as>)`. `on_branch_error` is `abort` (default,
  fail the run) or `collect` (drop failed branches). Barriers pair 1:1 (checked
  in `Validate`).

Resume is **atomic per fan-out block**: an interrupted run re-runs a fan_out's
branches; `fan_out` also persists the joined list under the `fan_in`'s step id so
a resume landing on the `fan_in` recovers it.

## Scheduled jobs

vixd runs a scheduler over `~/.vix/jobs/<id>/job.json` (one subdirectory per job,
hot-reloaded; machine-written runtime state in `~/.vix/jobs/<id>/state.json` —
one per job, sibling of `job.json`, spec/state split so user files never churn).
Each
run executes in an isolated headless thread (plain prompt, or a workflow named
via `workflow_id` or embedded inline via `workflow` — at most one) and lands in
the Threads tab under "Vix-initiated".
Triggers: `cron` (robfig syntax incl. `@every`) and one-shot `at`. The shipped
`heartbeat` job reads `~/.vix/jobs/heartbeat/heartbeat.md` every 30 minutes and skips with
zero tokens while the file is effectively empty (or the run answers
HEARTBEAT_OK). The model-facing surface is the `jobs` skill (no tool, no slash
command): the agent writes job files directly and verifies via each job's
`state.json`. Engine: `internal/daemon/jobs/`; runner:
`internal/daemon/job_runner.go`. Kill switch: `"features": {"jobs": false}` or
`VIX_DISABLE_JOBS=1`.

### Running a job/hook on demand

`vix job run <id>` and `vix hook trigger <id>` fire a job or lifecycle hook
immediately by id, out of band from its schedule/event. Both are sibling CLI
verb groups to `vix daemon`/`vix thread` (dispatched in `cmd/vix/main.go`
before flag parsing), talk to the daemon over the socket
(`Client.RunJob`/`Client.TriggerHook` → `job.run`/`hook.trigger` handlers in
`handlers.go`), and print the run's thread id. The run proceeds in the
background and lands under "Vix-initiated". A manual job run records its outcome
but **does not** reschedule or complete a one-shot (`Scheduler.RunNow` +
the manual branch of `applyResult`); a manual hook trigger runs fire-and-forget
regardless of mode (`Server.TriggerHook` → `fireHookAsync`). Both run even when
the job/hook is disabled; a job run is refused only when one is already in
flight. The run's thread id is threaded through the run context
(`jobs.WithRunID`/`jobs.RunIDFromContext`) so the CLI learns it up front.

### Detecting whether `vixd` is running (sandbox caveat)

When you (an AI coding agent) are working inside a live vix thread, **a `vixd`
daemon is by definition already running** — it is the process serving the LLM
turns you are responding to. Do not conclude it is dead just because you can't
find it.

The agent's `bash` tool runs inside vix's sandbox (Seatbelt on macOS, bwrap on
Linux). From in there the **process table view is partial and inconsistent**: it
does not reliably show the host process that launched the thread, even though it
*does* show processes the tool spawns itself. So:

- `pgrep -fl vixd` returning nothing means "not visible from inside the
  sandbox", **not** "not running". `pgrep` and `ps` can even disagree with each
  other in the same thread.
- The reliable signal is the **Unix socket plus a working thread**: a live
  `/tmp/vixd.sock` (`srwxr-xr-x`) and the fact that the chat is responding are
  far stronger evidence than `pgrep`.
- **Never `rm` the socket or kill/respawn `vixd` based on a `pgrep` miss.**
  Removing `/tmp/vixd.sock` or starting a second daemon can disrupt the running
  thread. If you genuinely need a daemon for an out-of-band task (e.g. driving
  the TUI for a VHS recording), prefer an explicit, isolated instance (e.g. a
  separate `--config-dir` and socket path) rather than touching the default one.

## Run logs (jobs & hooks)

Every job run and hook fire is recorded as append-only JSONL under
`~/.vix/logs/` (resolved via `VixPaths.JobsLog()` / `VixPaths.HooksLog()`), one
daily file per subsystem:

```
~/.vix/logs/jobs/<YYYY-MM-DD>.jsonl     # one line per job lifecycle event
~/.vix/logs/hooks/<YYYY-MM-DD>.jsonl    # one line per hook lifecycle event
```

Each line is a JSON object with a `phase` field. Jobs emit `started` → optional
`error` → `finished` (correlate by `job_id`, and `thread_id` once the run has
one). Hooks emit `fired` → optional `error` → `finished` (correlate by
`fire_id`). Error lines carry a `source` naming where the failure came from
(`prompt_resolve`, `agent`, `timeout`, `start_refused`, `auto_disable`,
`command_exec`). Errors are also mirrored to `vixd.log` via `LogError`. Writers
live in `internal/daemon/run_log.go`; the scheduler logs jobs through an injected
`jobs.RunLogger`, and `hook_runner.go` logs hooks. Retention is
`logs.retention_days` in `settings.json` (default 10, `0`/negative = keep
forever); the daemon sweeps whole stale daily files at startup and every 24h.

These files are line-delimited JSON, so prefer `jq` over hand-parsing. Useful
queries (substitute the date as needed; logs are UTC):

```bash
# Last run for a given job id (most recent finished line)
grep -h '"phase":"finished"' ~/.vix/logs/jobs/*.jsonl \
  | jq -c 'select(.job_id=="stale-branches")' | tail -1

# Full timeline for one job today (started/error/finished in order)
jq -c 'select(.job_id=="heartbeat")' ~/.vix/logs/jobs/$(date -u +%F).jsonl

# Latest job errors across all jobs (most recent 20)
grep -h '"phase":"error"' ~/.vix/logs/jobs/*.jsonl | jq -c '{ts,job_id,source,error}' | tail -20

# All failed/timed-out job runs in the last few days
jq -c 'select(.phase=="finished" and (.status=="error" or .status=="timeout"))' \
  ~/.vix/logs/jobs/*.jsonl

# Reconstruct one hook fire by its fire_id
jq -c 'select(.fire_id=="f9e2c1d0")' ~/.vix/logs/hooks/*.jsonl

# Latest hook errors (e.g. command_exec failures with exit codes)
grep -h '"phase":"error"' ~/.vix/logs/hooks/*.jsonl \
  | jq -c '{ts,hook_id,source,error,exit_code}' | tail -20
```

When the daemon runs with `--config-dir <dir>`, the logs live under
`<dir>/logs/{jobs,hooks}/` instead of `~/.vix/logs/`.

## Planning

When the user asks for a plan, it must include **unit tests** and **end-to-end
tests** covering the new or changed behaviour. Author the end-to-end tests with
the `write-e2e-test` skill (project-local, `./.vix/skills/write-e2e-test/`),
which drives the real vix TUI and vixd daemon against the mock LLM server; see
`e2e/README.md` for the harness details. When the change touches a
performance-sensitive hot path (code scanning/parsing, LLM stream decoding,
thread persistence, access stats), the plan should also add or update a
**benchmark** — see "Performance tests" below.

## Performance tests

In addition to unit and e2e tests, vix has Go `testing.B` **benchmarks** for its
hot paths, plus tooling that records one result per commit and compares releases
with `benchstat`. Everything lives under `perf/` (see `perf/README.md`); the
reusable, unit-tested logic is `internal/perf` and the orchestration is
`cmd/perftool`.

Conventions:

- **The model is always stubbed** — an `httptest` server replaying canned SSE
  (`internal/daemon/llm/bench_test.go`) or the in-process `fakeCompactionLLM`
  (`internal/daemon/bench_test.go`). Benchmarks never hit the network.
- **Disk is real, not abstracted** — benchmarks run against isolated
  `b.TempDir()` dirs and generated corpora (`make perf-corpus`), and access
  stats against an in-memory SQLite (`:memory:`). No FS abstraction is injected
  into production code, so nothing extra ships in the binaries.
- Each benchmark calls `benchQuiet(b)` to silence the stdlib logger (keeps
  output benchstat-parseable and timings clean) and has a `*_Smoke` test that
  runs its body once, so `make test` guards against breakage.

Benchmarks live in `internal/daemon`, `internal/daemon/brain`, and
`internal/daemon/llm` (`bench_test.go` in each; the package list is
`perf.BenchPackages`).

Make targets:

```bash
make perf-corpus     # generate on-disk corpora once (idempotent; gitignored)
make test-perf       # run benchmarks, write perf/results/<commit>.txt, benchstat vs baseline + previous (no commit)
make perf-baseline   # write perf/results/baseline.txt (frozen reference — commit it once)
make perf-smoke      # run every benchmark once (-benchtime=1x) as a breakage guard
make test-all        # unit tests, then e2e (Docker), then perf
```

Results are keyed by commit hash. `make test-perf` writes
`perf/results/<HEAD>.txt`; that file is the "this commit was benchmarked"
marker. **`make release` refuses to proceed** (`perftool gate`) until it exists
and the working tree is clean, then commits it as the release's recorded result
(`perftool record`). `COUNT=N` sets the benchstat repetition count (default 10).
Numbers are machine-dependent — run releases from a consistent host and rely on
benchstat *deltas*, not absolute values.

## Consent before implementation

**Always ask for explicit user consent before writing or modifying any code.** When a user describes a problem, asks a question, or discusses a potential change, treat it as a conversation — not a request to implement. Present your understanding of the problem and your proposed approach, then wait for the user to confirm they want you to proceed.

- If the user says "how would you fix X?" or "what do you think about Y?", respond with an explanation or plan, not with code changes.
- If the intent is ambiguous, ask: "Would you like me to implement this?" before touching any files.
- Only skip this step when the user's message unambiguously requests implementation (e.g. "fix this", "implement that", "make it so").

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
(`internal/daemon/thread.go`), and the panel renders from `event.todo_list_updated`
(`internal/ui/todopanel.go`, `internal/ui/rightpanel.go`).

## Environment

- **Go 1.26+** required
- **ANTHROPIC_API_KEY** environment variable or `.env` file for LLM access
- **LSP servers** (optional): gopls, pylsp, typescript-language-server for code intelligence
- **LSP config**: `.vix/settings.json` in project root

## Config directory resolution

By default vix merges config from two layered `.vix` directories: `~/.vix` (user defaults) and `./.vix` (project overrides). This covers `settings.json`, `agents/`, `skills/`, plus thread state like `history.txt`, `plans/`, `access_stats.db`, and `logs/`.

Instruction files (`CLAUDE.md`, `AGENTS.md`) are also layered, but follow a slightly different convention: the user-global copy lives at `~/.vix/CLAUDE.md` / `~/.vix/AGENTS.md`, while the project copy lives at the **project root** (`./CLAUDE.md`, `./AGENTS.md`), not inside `./.vix`. Both load when the corresponding feature flag is enabled, home first then project (see `VixPaths.ClaudeMD()` / `VixPaths.AgentsMD()` and `Thread.discoverInstructionFiles`).

All path resolution flows through `config.VixPaths` (internal/config/paths.go). Add new `.vix`-relative paths there rather than hardcoding `filepath.Join(cwd, ".vix", ...)`.

Pass `--config-dir /some/path` to use that directory as the sole `.vix` root. Neither `~/.vix` nor `./.vix` is consulted, and all thread state (history, plans, access stats, LLM logs) is written inside the override directory. The directory is auto-created and bootstrapped with default settings on first run. This is useful for sandboxed/reproducible threads without touching real user or project config.

## Skills

Skills are reusable, task-specific instruction sets. Each skill is a directory under `.vix/skills/<name>/` containing a `SKILL.md` file (YAML frontmatter + markdown body) and, optionally, supporting files (`reference.md`, `scripts/`, etc.). Project skills override user skills on name collision. The engine lives in `internal/agent/skills.go`.

Frontmatter fields parsed today: `name`, `description`, `model`, `allowed-tools`. Body templating supports `$ARGUMENTS`, positional `$1`/`$2`, `` !`cmd` `` dynamic command injection, and `${SKILL_DIR}` (absolute path to the skill directory).

Skills use **progressive disclosure** — three layers loaded only as needed:

1. **Metadata (always present)** — each skill's name + description is injected into the system prompt via `SkillRegistry.FormatForSystemPrompt` (wired in `Thread.buildSystemPrompt`). Cheap; lets the model know what exists.
2. **Body (on demand)** — the full `SKILL.md` body loads only when a skill is invoked, via `Skill.LoadForTool` (body with args substituted + a listing of bundled files).
3. **Bundled files (on demand)** — the model reads `reference.md` / runs `scripts/*` with the normal `read_file`/`bash` tools using the absolute paths listed in layer 2.

Two invocation paths, both calling `LoadForTool` under the hood:

- **Implicit (model-driven)** — the `skill` tool (`SkillToolSchema`, dispatched inline in `Thread.executeToolDirect`). Appended to the thread tool list only when at least one skill is loaded. The model calls `skill(name, arguments?)` when a task matches an advertised skill.
- **Explicit (user-driven)** — typing `/<skill-name> [args]`, intercepted in the input handler before the turn starts and rendered into the user message. Skill names are advertised to the TUI via `event.skills_available` so they autocomplete in the slash menu.

`/skills` lists all loaded skills.

### Custom skill directories

In addition to default `.vix/skills/`, custom skill directories can be loaded via `skills_dir` (single path string) or `skills_dirs` (array of path strings) in `settings.json` (both global `~/.vix/settings.json` and project `.vix/settings.json`):

```json
"skills_dir": "../shared-skills",
"skills_dirs": ["~/team-skills", "/opt/custom-skills"]
```

- Paths may be absolute, relative (resolved against the directory of the `settings.json` file), or prefixed with `~` (expanded to `$HOME`).
- Precedence on name collision: project custom skills -> project default `.vix/skills` -> user custom skills -> user default `~/.vix/skills`.

### Bundled skills

A few skills ship with vix under `internal/config/defaults/skills/` (embedded via
`//go:embed defaults` and bootstrapped into `~/.vix` on startup): `jobs`,
`hooks`, and `vix-help`. To make one refresh on upgrade, list its files in
`managedDefaultFiles` (`internal/config/bootstrap.go`); first-run installs get
the whole tree via `seedAllDefaults`.

`vix-help` answers questions about vix itself from the official docs. Its
primary source is the machine-readable manual published at
`https://getvix.dev/manual/<section-id>.md` (fetched with `web_fetch`), with a
bundled offline snapshot at
`internal/config/defaults/skills/vix-help/references/vix-manual.md`. That manual
is **generated** in the `vix-website` repo (`npm run generate:manual` renders
`src/pages/Docs.tsx` to `public/manual/*.md`; a vitest staleness guard fails if
it drifts). **Syncing the offline snapshot** is a manual/CI step: regenerate the
website manual, then concatenate `public/manual/*.md` (in `index.md` order) into
the vix `references/vix-manual.md`. Refresh it whenever the docs change so the
offline fallback stays current.

## Default access policy

The agent decides whether a path is accessible by default by checking, in order: cwd, `$HOME`, the host's system directories (per platform), or any entry in `allowed_directories`. Anything outside that set surfaces as a confirmation prompt (interactive threads) or an error (headless). The `deny_list` always wins, even if the path matches one of the auto-allow categories.

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

The legacy flat-array form (`"deny_list": ["./secrets"]`) still parses and is treated as paths-only. Deny takes precedence over `allowed_directories`: a path that matches both is blocked. Path entries may be absolute, `~`-prefixed (expanded to the user's home directory), or relative. A relative entry is resolved against **both** the directory of the config file that declares it **and** the thread's working directory (project root), and both interpretations are added to the deny list. This dual resolution is why a `deny_list.paths` entry like `.envrc.private` in `./.vix/settings.json` blocks `<project>/.envrc.private` (the file the user means) rather than only the phantom `<project>/.vix/.envrc.private` that config-dir-relative resolution alone would produce. Both lists are unioned across layered configs (home + project).

Resolution lives in `LoadProjectConfig` (`internal/daemon/workflow.go`): `~` expansion + config-dir-relative form + raw relative entries recorded in `ProjectConfig.DenyPathsRel`; the cwd-relative form is added when the thread seeds its deny list via `combineDenyPaths` (`internal/daemon/deny_list.go`).

**Path match semantics**: a target path is blocked iff (after symlink resolution and `Clean`) it equals a deny entry or is a descendant of one.

**URL match semantics**:
- Entry with a scheme (e.g. `https://example.com/admin`) — URL-prefix match. Scheme and host are case-insensitive; path is case-sensitive and must align on `/`.
- Entry without a scheme (e.g. `example.com`) — hostname or dot-aligned suffix match (`api.example.com` matches `example.com`; `notexample.com` does not).

Coverage:
- `read_file` / `write_file` / `edit_file` / `delete_file` (and the minified variants): refused before execution when the target path is denied.
- `web_fetch`: refused when the `url` parameter matches a URL deny entry.
- `bash`: refused when any path-like token (a token that contains `/`) in the command resolves inside a denied path, or when any token containing `://` resolves to a denied URL. Bare words without `/` are not treated as paths, so prose like `echo 'no secrets here'` is allowed. Variable expansion, heredocs, and reassembly across variables are **not** analyzed (best-effort v1).
- `grep` / `glob_files`: matches inside a denied path are silently filtered from the output.
