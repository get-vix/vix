<!-- Bundled offline snapshot of the Vix manual. Source: https://getvix.dev/manual/ (generated from vix-website src/pages/Docs.tsx). Refresh with the sync step in AGENTS.md. -->
# Vix Manual (offline snapshot)

# What is Vix?

> Section: Introduction · vix docs · https://getvix.dev/docs#what-is-vix

# What is Vix?

Vix is a terminal-native AI coding agent. You run it in your project directory, describe what you want, and it figures out how to do it — reading files, writing code, running tests, searching the web, querying your language server, and asking you questions when it needs to.

It is not a plugin for an editor. It is not a chat interface bolted onto a search tool. It is a full agent loop: the model plans, uses tools, observes results, and keeps going until the task is done.

shell

```
$ vix
> add rate limiting to the API endpoints, use Redis, write tests
```

That's it. Vix reads the relevant files, understands your project structure, writes the implementation, updates tests, and shows you exactly what changed — line by line.

## What makes it different

### It understands your codebase before you ask anything.

On first run, vix scans your project, extracts symbols, builds an import graph, detects your frameworks and dependencies, and generates a semantic summary using the LLM. This context is cached and injected into every thread — the agent starts with genuine knowledge of your code, not a blank slate.

### It has compiler-level code intelligence.

Vix integrates directly with Language Server Protocol servers (gopls, pylsp, typescript-language-server, and others). The agent can go-to-definition, find all references, inspect types, and read live diagnostics — the same information your editor has.

### It reads files efficiently.

Files can be read through the virtual file system with `read_minified_file`: vix parses the source with Tree-sitter, strips comments and whitespace, and returns a compact structural skeleton of the code — typically a 20–50% reduction in tokens. This matters on large codebases where context is the bottleneck.

### It can run multiple agents in parallel.

Complex tasks can be split across specialized sub-agents that run concurrently. Each sub-agent has its own conversation, tool set, and system prompt. Results are collected and synthesized by the parent agent.

### It has a workflow engine.

Repeatable multi-step processes — code review, feature scaffolding, release notes — can be defined as declarative JSON workflows and triggered by name. Steps chain outputs to inputs, can branch conditionally, and can pause to ask the user a question mid-run.

### It works without a UI.

Every feature is accessible headlessly: `vix -p "add error handling to auth.go"`. Pipe in prompts, pipe out JSON, trigger workflows from CI. The TUI is optional.

## What it is not

Vix is not magic. It makes mistakes, misreads intent, and occasionally goes in circles. The plan review mode, inline diffs, and per-write confirmation flag exist precisely because you should stay in the loop. Think of it as a very fast, very capable junior engineer who needs occasional course correction — not an autonomous system you walk away from.

---

# How it works

> Section: Introduction · vix docs · https://getvix.dev/docs#how-it-works

# How it works

## The two processes

Vix runs as two separate binaries: a **daemon** (`vixd`) and a **client** (`vix`).

The daemon is a long-running background process. It owns the LLM connection, executes all tools, manages the brain index, and maintains thread state. It listens on a Unix socket (`/tmp/vixd.sock`). The first time you run `vix`, it starts the daemon automatically as a subprocess. If you want it to persist across terminal threads — so the brain index stays warm and context is preserved — you can start it independently and leave it running.

The client is the thin terminal UI. It connects to the daemon over the socket, streams events to your screen, and forwards your input back. When you close the TUI, the daemon keeps running. When you reopen vix in the same project, you reconnect to the same thread.

┌─────────────┐          Unix socket          ┌──────────────────────┐
│  vix (TUI)  │  ◄──── JSON event stream ───► │  vixd                │
│             │                               │                      │
│  Bubble Tea │                               │  LLM · Tools · Brain │
│  Lipgloss   │                               │  LSP · Workflows     │
└─────────────┘                               └──────────────────────┘

## A thread, step by step

### 1\. Brain init

When you open a thread in a new project, the daemon scans your codebase: every source file is catalogued, symbols are extracted, an import graph is built, frameworks and dependencies are detected. Then the LLM generates a dense summary of the project and saves it to `.vix/context/`. On subsequent threads, this cache is reused — only modified files are re-indexed.

### 2\. System prompt assembly

Before the first message, the daemon assembles a system prompt from several sources: the base agent definition, runtime variables (working directory, OS, shell, model), the project summary from the brain, the top 10 most-recently accessed files, and any `CLAUDE.md` or `AGENTS.md` instruction files you've configured (loaded from `~/.vix/` first, then the project root, so user-global conventions are layered with project-specific ones). The assembled prompt is marked for Anthropic's prompt caching, so repeated turns reuse the cached version.

### 3\. The agent loop

You send a message. The daemon forwards it to the configured model provider's API with the full system prompt, conversation history, and tool schemas. The model streams back a response. If the response contains tool calls, the daemon executes them in parallel, collects results, and sends them back to the model. This continues until the model returns `end_turn` — or hits the agent's `max_turns` limit (the default chat agent allows up to 100; agents that omit the field fall back to 20).

### 4\. Tools

Every tool call is executed in-process by the daemon — no subprocess overhead for file operations, grep, glob, or LSP queries. Bash commands run in a sandboxed subprocess with a default 120-second timeout (raisable per call up to 600 seconds, and detachable as background jobs). Tool results are streamed to the TUI as they complete, with diffs for edits and previews for writes.

### 5\. Context management

The daemon tracks every file read across the thread. If the model tries to re-read a file it has already seen, the call is rejected with an error message explaining why — preventing the model from burning tokens on redundant reads. Files are also invalidated from this cache when they are written, so a write followed by a read always gets fresh content.

## The brain in more detail

The brain is vix's persistent understanding of your project. It lives in `.vix/` at the project root and is built in two phases:

-   •**Phase 1 (static):** file scan, symbol extraction, import graph, framework detection. Fast — runs in seconds even on large projects.
-   •**Phase 2 (semantic):** the LLM reads the static analysis output and writes a prose summary of what the project does and how it's structured. Saved to `.vix/context/project-summary.md`.

The brain updates incrementally: every file write triggers a background re-index of just that file. You can also force a full rebuild with `vix --force-init`.

LSP servers are initialized as part of the brain setup. If gopls, pylsp, or typescript-language-server are installed, vix starts them in the background and keeps them alive for the duration of the daemon process.

---

# Quick demo

> Section: Introduction · vix docs · https://getvix.dev/docs#quick-demo

# Quick demo

Here's what a typical Vix thread looks like — from launching the agent to reviewing an inline diff:

![Vix demo — a full thread from prompt to inline diff](/src/assets/demo.gif)

---

# Installation

> Section: Getting Started · vix docs · https://getvix.dev/docs#installation

# Installation

## Requirements

-   macOS 12 or later
-   An [Anthropic API key](https://console.anthropic.com/)
-   Go 1.26+ (only if building from source)

LSP-powered code intelligence (go-to-definition, diagnostics, etc.) requires language servers to be installed separately. This is optional — vix works without them.

## Homebrew (recommended)

shell

```
brew install vix
```

This installs two binaries: `vix` (the TUI client) and `vixd` (the background server). Both must be on your `PATH` — Homebrew handles this automatically.

Verify the installation:

shell

```
vix --version
```

## Build from source

Requires Go 1.26+.

shell

```
git clone https://github.com/get-vix/vix
cd vix
go build -o bin/vix ./cmd/vix
go build -o bin/vixd ./cmd/vixd
```

Add the `bin/` directory to your `PATH`, or copy both binaries to `/usr/local/bin`:

```
sudo cp bin/vix bin/vixd /usr/local/bin/
```

## Updating

shell

```
brew upgrade vix
```

---

# API key setup

> Section: Getting Started · vix docs · https://getvix.dev/docs#api-key-setup

# API Key Setup

Vix is multi-provider. You need a credential for at least one supported provider before you can do anything useful. Each agent and model is addressed by a provider-prefixed spec (e.g. `anthropic/claude-sonnet-4-6`, `openai/gpt-5.2`, `openrouter/qwen/qwen3-coder`), so vix resolves the credential for whichever provider the active model belongs to.

## Interactive setup (recommended)

The interactive flow is the easiest way to add a credential — it writes straight to your OS keychain, so you are never asked again on subsequent runs. Run `vix` and press `F3` to open the **Models** tab.

The tab has three areas — the **provider** column on the left, the **authentication** panel on the right, and the **model** grid below. Use `↑↓` to navigate, `Tab` to switch between areas, and `Enter` to select.

### Adding an API key

1.  Highlight the provider in the left column (e.g. **Xiaomi MiMo**).
2.  Press `Tab` to move to the authentication panel and select `[ Create key ]` with `Enter`.
3.  Paste your key into the `Paste your <provider> API key…` popup, then press `Enter` to save (`Esc` cancels). Surrounding whitespace is trimmed automatically.
4.  Confirm the row now reads `API Key: <prefix>…` and not `API Key: (empty)`.

Shortcut: you can also just pick a model in the grid. If that provider has no credential yet, the same key popup opens, remembers your choice, and activates the model as soon as you paste a key. Manage an existing key from the same panel with `[ Update key ]`, `[ Delete key ]`, and — when both an API key and an OAuth token exist — `[ Make it default ]`.

![Creating an API key in the Models tab](/src/assets/doc_create_api_key.gif)

### Signing in with OAuth

**Anthropic**, **OpenAI**, and **OpenRouter** support an interactive OAuth login instead of a raw API key — select `[ Create token ]` on the OAuth row and complete the flow in your browser. **MiniMax** and **Xiaomi MiMo** are API-key only and show `OAuth token: (not available)`.

OAuth tokens and the OS keychain

OAuth tokens are long-lived, auto-refreshing secrets, so vix prefers to store them in the OS keychain (macOS Keychain, or the Linux Secret Service via D-Bus). On a machine with no usable keychain — headless Linux, a minimal container, or WSL without gnome-keyring — vix falls back to writing the token to `~/.vix/auth.json` (mode `0600`), the same file API keys fall back to. The Models tab and the logs surface that the token is stored unencrypted on disk (`token will be stored in plaintext auth.json`).

If you would rather not keep refresh tokens on disk, run vix with a working keychain, or authenticate with an API-key environment variable (e.g. `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`) instead of an interactive login — env vars are read directly and never written to disk.

Key saved but still reported as missing?

If you select a model but vix reports `no credential for <provider>` — or the request fails with an auth error — the stored key is almost certainly wrong. Vix does **not** verify a key when you paste it: any non-empty value is saved as-is, so a typo or a truncated copy still shows a prefix. Check:

-   You pressed `Enter` in the popup — highlighting the field is not enough to save.
-   The key was pasted in full, with no missing characters at the start or end.
-   You added the key to the _same_ provider as the model — a `mimo/…` model needs a **Xiaomi MiMo** key, not MiniMax.
-   No stale environment variable is shadowing it. An env var (e.g. an empty or old `MIMO_API_KEY` in your shell profile) takes priority over the keychain — see the resolution order below.

To fix it, re-open the Models tab (`F3`), choose `[ Delete key ]`, and paste the key again.

## Supported providers

| Provider | API key env var | OAuth login |
| --- | --- | --- |
| Anthropic | ANTHROPIC\_API\_KEY (or CLAUDE\_CODE\_OAUTH\_TOKEN) | Yes (Claude) |
| OpenAI | OPENAI\_API\_KEY | Yes (Codex) |
| OpenRouter | OPENROUTER\_API\_KEY | Yes |
| MiniMax | MINIMAX\_API\_KEY | — |
| Xiaomi MiMo | MIMO\_API\_KEY | — |

## Environment variable

Set the relevant provider's env var in your shell profile (see the table above):

```
export ANTHROPIC_API_KEY=sk-ant-...
export OPENAI_API_KEY=sk-...
export OPENROUTER_API_KEY=sk-or-...
```

An env var takes priority over the keychain if set.

## .env file

Vix will pick up a `.env` file in your project directory or next to the `vix` binary:

```
# .env
ANTHROPIC_API_KEY=sk-ant-...
```

Useful for team setups where the key is committed to a secrets manager and written to `.env` at deploy time. Make sure `.env` is in your `.gitignore`.

## Key resolution order

| Priority | Source |
| --- | --- |
| 1 | Provider env var (e.g. `ANTHROPIC_API_KEY`) |
| 2 | OS keychain (incl. stored OAuth tokens) |
| 3 | `.env` next to the `vix` binary |
| 4 | `.env` in the current working directory |

## Removing a saved key

The easiest way is the Models tab (F3) → select the provider → **Delete**. Keys are stored in the OS keychain under service `vix`, with account `<provider>-api-key`. To delete from the command line on macOS:

```
# macOS Keychain Access → search for "vix" → delete
# or via the security CLI:
security delete-generic-password -s vix -a anthropic-api-key
```

---

# First run

> Section: Getting Started · vix docs · https://getvix.dev/docs#first-run

# First Run

## Start vix in your project

Navigate to your project directory and run:

```
cd ~/your-project
vix
```

The first time you run vix in a project, it initialises the **brain** — a one-time scan of your codebase that takes a few seconds to a minute depending on project size. You will see:

```
Initialising...
```

Under the hood, vix is:

1.  Scanning every source file and extracting symbols
2.  Building an import dependency graph
3.  Detecting your frameworks, dependencies, and test setup
4.  Starting any configured LSP servers
5.  Generating a semantic summary of the project with the LLM

The results are cached in `.vix/` at your project root. Subsequent threads start instantly — only files you've modified are re-indexed.

**Tip:** Add `.vix/` to your `.gitignore` unless you want to commit the brain cache. The `settings.json` config file inside it is worth committing; the `context/` and `access_stats.db` are not.

## Send your first message

Once initialisation is done, the input box is ready. Type anything:

```
> what does this project do?
```

Vix will read its generated project summary and give you a straight answer. Try something more concrete next:

```
> find all places where we're not handling errors from database calls
```

The agent will grep the codebase, read the relevant files, and give you a specific list.

## Try an edit

```
> add a TODO comment above every unhandled error you just found
```

The agent will call `edit_file` for each location. Each edit appears in the chat as a color-coded diff showing exactly what changed and on which line.

If something looks wrong, just say so:

```
> actually revert the last two edits, those functions are intentionally fire-and-forget
```

## The status bar

At the bottom of the screen you will always see:

```
claude-sonnet-4-6  ·  your-project  ·  2 341 in · 487 out · $0.004  ·  18s
```

From left to right: the active model, project name, input tokens, output tokens, estimated thread cost, and elapsed time for the last turn.

## Stopping and restarting

Press `Ctrl+C` to cancel an in-progress operation. Press `Ctrl+C` again (or `Ctrl+D`) to quit.

The daemon keeps running in the background after you close the TUI. When you run `vix` again in the same project, you reconnect to the existing thread — conversation history is preserved.

Each message keeps its original send time: the "Sent at …" timestamp on your messages is persisted with the thread, so a replayed conversation shows when each message was actually sent rather than when you relaunched. Threads saved before this was added simply omit the timestamp line.

To start fresh:

shell

```
vix --force-init   # rebuilds the brain index from scratch
```

Or clear just the conversation from within the TUI with the `/clear` slash command.

## Headless mode (no TUI)

If you just want a quick answer without the interactive interface:

shell

```
vix -p "summarise the recent changes to the auth module"
```

Output goes to stdout. Tool calls go to stderr.

---

# The daemon & client

> Section: Core Concepts · vix docs · https://getvix.dev/docs#daemon-client

# The Daemon & Client

Vix is split into two binaries that communicate over a Unix socket. Understanding this split explains a lot of vix's behaviour.

## vixd — the server

The daemon is a long-running background process that owns everything stateful:

-   The LLM connection and conversation history
-   Tool execution (file reads/writes, bash, grep, LSP queries)
-   The brain index and LSP server pool
-   Background sub-agents and workflow state
-   The scheduled-jobs engine (cron jobs, the heartbeat)
-   The access statistics database

It listens on `/tmp/vixd.sock`. Multiple clients can connect to the same daemon simultaneously, though each gets its own isolated thread.

The daemon has no UI. It logs to stdout:

shell

```
vixd
# [vixd] API key loaded (source: env)
# [vixd] Registered handler: brain.init
# [vixd] Registered handler: tool.read_file
# ...
```

## vix — the client

The client is the thin terminal UI. Its only job is to:

1.  Connect to the daemon over the socket
2.  Forward your input as JSON commands
3.  Render the stream of events the daemon sends back

It holds no state of its own. If the client crashes, the daemon — and your thread — keeps running.

## Lifecycle

The daemon is an explicitly-managed, long-lived process — `vix` never starts it for you. When no daemon is answering on the socket, vix exits with the command to run:

shell

```
vix daemon start    # launch vixd, detached
vix daemon status   # is it running, and which version?
vix daemon stop     # coordinated shutdown (all attached vix instances quit too)
```

The daemon runs until stopped or signalled — closing the TUI leaves it (and your threads, and any scheduled jobs) running. To make it survive reboots, register it as a login service:

shell

```
vix daemon install    # macOS LaunchAgent / Linux systemd user unit
vix daemon uninstall  # remove the registration
```

**Version gate:** client and daemon must be the exact same build. After upgrading vix, a long-running daemon from the old build refuses new threads with a clear error — run `vix daemon stop && vix daemon start` to recycle it.

## Why the split matters

**Threads survive TUI crashes.** If the client dies mid-task, the daemon keeps working. Reconnect and you'll see the results waiting for you.

**Multiple windows.** Open two terminals, both running `vix` in the same project — they share the same daemon.

**Headless use.** CI pipelines and scripts connect to the daemon exactly like the TUI does.

**Clean separation of concerns.** The TUI can be replaced, extended, or bypassed entirely without touching the agent logic.

## The socket path

The socket defaults to `/tmp/vixd.sock`. To run multiple isolated daemons (or relocate the socket), pass `--socket-path` to both `vix` and `vixd` (or set `VIX_SOCKET_PATH`). The `vix daemon` subcommands accept the same flag.

---

# The Brain (code intelligence)

> Section: Core Concepts · vix docs · https://getvix.dev/docs#brain

# The Brain

The brain is vix's persistent model of your codebase. It lives in `.vix/` at your project root and is built automatically on first run.

## What gets built

### Phase 1 — Static analysis

Runs in seconds. No LLM involved.

-   **File scan:** every source file is catalogued with its language, line count, SHA-256 hash, and flags. Respects `.gitignore`.
-   **Symbol extraction:** functions, classes, methods, and variables are parsed using language-aware parsers.
-   **Import graph:** inter-file dependencies are mapped. The most-imported "hub files" are ranked.
-   **Dependency detection:** external dependencies parsed from `package.json`, `go.mod`, `Cargo.toml`, and others.
-   **Framework detection:** dependencies and file patterns matched against known frameworks, testing, and CI/CD setups.

### Phase 2 — Semantic analysis

Runs after Phase 1. Requires an API key.

The LLM reads the static analysis output and writes a dense prose summary. Saved to `.vix/context/project-summary.md`.

```
myapi is a Go REST API built on net/http with a PostgreSQL backend
accessed via sqlx. It follows a layered architecture: handlers in
internal/api/, business logic in internal/service/, and data access
in internal/db/. Authentication uses JWT via the golang-jwt library.
```

## What it's used for

**System prompt injection.** The project summary gives the agent immediate context before you type a single word.

**Frequently accessed files.** The top 10 most-read files are pre-loaded into the system prompt (tracked in `.vix/access_stats.db`).

**LSP initialisation.** The brain setup also initialises the LSP server pool, so `lsp_query` is available immediately.

## Incremental updates

When the agent writes or edits a file, a background re-index is triggered automatically:

text

```
[vixd] Auto brain update for 1 file(s): [internal/api/auth.go]
[vixd] Brain update complete (0.1s)
```

## Force rebuild

shell

```
vix --force-init
```

Deletes `.vix/context/` and re-runs both phases. Config, agents, and workflows are preserved.

## What to commit

```
.vix/
├── settings.json          ✅ commit — your project config
├── agents/                ✅ commit — custom agent definitions
├── skills/                ✅ commit — custom skills
├── context/               ❌ gitignore — generated
└── access_stats.db        ❌ gitignore — local thread data
```

## Supported languages

| Language | Extension(s) |
| --- | --- |
| Go | .go |
| Python | .py |
| JavaScript | .js, .jsx |
| TypeScript | .ts, .tsx |
| Rust | .rs |
| Ruby | .rb |
| Java | .java |
| Kotlin | .kt, .kts |
| Swift | .swift |
| C / C++ | .c, .h, .cpp, .hpp |
| C# | .cs |
| PHP | .php |
| Lua | .lua |
| Shell | .sh, .bash, .zsh |

Config, markup, and data files (`.json`, `.yaml`, `.toml`, `.md`, `.html`, `.css`, `.scss`, `.sql`) are scanned but not parsed for symbols.

---

# Agents & tools

> Section: Core Concepts · vix docs · https://getvix.dev/docs#agents-tools

# Agents & Tools

## What is an agent?

An agent is an LLM instance running in a loop. It receives a system prompt, a conversation history, and a list of tools. It generates a response — which may include tool calls. The daemon executes those tool calls, feeds the results back, and the loop continues until the model signals it's done or hits `max_turns`.

Every thread is powered by an agent. By default this is the `general` agent, defined in `~/.vix/agents/general.md`.

## What is a tool?

A tool is a typed function the LLM can call. The daemon registers the tool's schema with the Anthropic API. The agent does not execute tools directly — it describes what it wants, and the daemon does the work.

## The tool loop

```
User message
     │
     ▼
LLM generates response
     │
     ├─ end_turn ──────────────► Done
     │
     └─ tool_use
           │
           ▼
   Daemon executes tools (in parallel)
           │
           ▼
   Results fed back to LLM
           │
          ...
```

Tool calls within a single response are executed in parallel.

## Available tools

| Tool | What it does |
| --- | --- |
| read\_file | Read a file from disk, optionally a line range. PDFs are auto-converted to Markdown. |
| read\_minified\_file | Read a file through the VFS, Tree-sitter–minified for fewer tokens. |
| write\_file | Write a full file to disk. Creates directories as needed. |
| write\_minified\_file | Write a file via the VFS from minified content; a formatter restores it. |
| edit\_file | Replace an exact unique string in a file. Shows a diff in the UI. |
| edit\_minified\_file | Edit a file via the VFS, matching on the minified representation. |
| delete\_file | Delete a file from disk. |
| bash | Run a shell command. Default 120s timeout (up to 600s); supports background jobs. |
| grep | Recursive regex search. Backend configurable (ripgrep optional). |
| glob\_files | Find paths matching glob pattern(s). Returns up to 1000 paths by default. |
| lsp\_query | Query LSP servers for definitions, references, hover info, diagnostics. |
| web\_fetch | Fetch a URL and return its content as readable text. |
| web\_search | Search the web via Brave Search API (needs BRAVE\_SEARCH\_API\_KEY). |
| spawn\_agent | Spawn a sub-agent with its own conversation, tools, and LLM instance. |
| task\_output | Retrieve the result of a background sub-agent. |
| ask\_question\_to\_user | Pause and ask the user one or more questions. |
| todo\_write | Replace the thread's TODO list (plan/track multi-step work). |
| todo\_read | Return the thread's current TODO list. |
| tool\_orchestrator | Execute a Python script that chains multiple tools in one round-trip. |

## Custom agents

Define specialised agents as Markdown files with YAML frontmatter:

yaml

```
---
name: reviewer
description: Reviews code for correctness, style, and security
model: anthropic/claude-opus-4-8
tools: read_file, grep, glob_files, lsp_query
max_turns: 10
---

You are a senior engineer conducting a thorough code review.
Focus on: correctness, error handling, security, and performance.
Be specific. Cite line numbers.
```

Save to `.vix/agents/reviewer.md`. Agents load from:

1.  `.vix/agents/` — project-local (takes precedence)
2.  `~/.vix/agents/` — user-global

## Tool filtering

The `tools` field is a whitelist. If specified, only those tools are available — the rest are hidden entirely.

```
tools: read_file, grep, glob_files, lsp_query
```

A read-only agent. It can explore and analyse but cannot change anything.

## Max turns & sub-agents

Each agent runs for at most `max_turns` LLM turns (default: 20). Any agent can spawn sub-agents via `spawn_agent` — fully independent, with their own conversation, tools, and turn limit. Use `background: true` for parallel execution and `task_output` to collect results.

---

# Models & providers

> Section: Core Concepts · vix docs · https://getvix.dev/docs#models-providers

# Models & Providers

Vix is multi-provider. Every model is addressed by a **provider-prefixed spec** of the form `provider/model-id` — for example `anthropic/claude-sonnet-4-6`, `openai/gpt-5.2`, or `openrouter/qwen/qwen3-coder`. vix resolves credentials for whichever provider the active model belongs to.

## Supported providers

| Provider | Prefix | API key env var | OAuth login |
| --- | --- | --- | --- |
| Anthropic | anthropic/ | ANTHROPIC\_API\_KEY | Yes (Claude) |
| OpenAI | openai/ | OPENAI\_API\_KEY | Yes (Codex) |
| OpenRouter | openrouter/ | OPENROUTER\_API\_KEY | Yes |
| MiniMax | minimax/ | MINIMAX\_API\_KEY | — |
| Xiaomi MiMo | mimo/ | MIMO\_API\_KEY | — |
| Ollama (local) | ollama/ | OLLAMA\_API\_KEY (optional) | — |
| llama.cpp (local) | llamacpp/ | LLAMACPP\_API\_KEY (optional) | — |
| Lemonade (local) | lemonade/ | LEMONADE\_API\_KEY (optional) | — |

Anthropic also accepts an OAuth bearer token via `CLAUDE_CODE_OAUTH_TOKEN`. Each provider ships a curated model catalogue — Anthropic Claude, OpenAI GPT / o-series, and hundreds of models through OpenRouter, plus MiniMax and Xiaomi MiMo.

## Choosing a model

The active model can be set in several places — the most specific wins:

-   **Models tab (F3)** — pick the thread's active model interactively.
-   An agent's `model:` frontmatter — overrides the thread model whenever that agent runs.
-   A skill's `model:` frontmatter — for that `/skill` invocation.
-   A workflow step's agent — different models per phase.

## Credentials

Credentials are resolved per provider in order: environment variable → OS keychain → `.env` next to the binary → `.env` in the working directory. Add or remove keys from the Models tab (F3). See [API Key Setup](/docs#api-key-setup) for the full reference.

## OAuth logins

Three providers support an interactive OAuth login instead of a raw API key — **Anthropic** (Claude subscription), **OpenAI** (Codex), and **OpenRouter**. Start the flow from the Models tab; tokens are stored in the OS keychain and refreshed automatically. When both an API key and an OAuth token exist for a provider, the Models tab lets you choose which one is the default.

Without a usable OS keychain, the OAuth token falls back to on-disk storage in `~/.vix/auth.json` (mode `0600`) automatically — the same fallback API keys use — and the Models tab notes that the token is stored in plaintext. To avoid on-disk tokens, use a working keychain or an API-key environment variable. See [API Key Setup](/docs#api-key-setup) for details.

## Effort / reasoning

Each provider declares an effort policy (Anthropic adaptive thinking, OpenAI-style reasoning effort, etc.). Set `effort: low | medium | high` in an agent's frontmatter and vix maps it to the provider's native control.

## Context window & compaction

Each catalogue entry carries its context-window size, which drives automatic [context compaction](/docs#compaction).

## Local models (Ollama, llama.cpp & Lemonade)

Vix ships three **local providers** — `ollama/`, `llamacpp/`, and `lemonade/` — for models you self-host. Point them at your server with an environment variable; vix then discovers the served models and lists them in the Models tab (F3):

```
# llama.cpp (defaults to http://localhost:8080/v1)
export LLAMACPP_BASE_URL="http://localhost:8080/v1"

# Ollama (defaults to http://localhost:11434/v1)
export OLLAMA_BASE_URL="http://localhost:11434/v1"

# Lemonade (defaults to http://localhost:13305/v1)
export LEMONADE_BASE_URL="http://localhost:13305/v1"
```

[Lemonade](https://lemonade-server.ai) is a local AI server that serves optimized LLMs from your own GPU/NPU over an OpenAI-compatible API; because it's a built-in provider, just start Lemonade Server and its models appear under the `lemonade/` prefix.

Because these endpoints are ones you control, the local providers may use **plain HTTP on any host** — loopback _or_ a beefier box on your LAN, e.g. `http://freyr.local:8080/v1`. Every other (remote) provider still requires HTTPS. HTTP sends traffic — including any `LLAMACPP_API_KEY`/`OLLAMA_API_KEY`/`LEMONADE_API_KEY` you set — in the clear, so keep it to a trusted network; if your server supports TLS, prefer an `https://` base URL. An API key is optional for local servers and only needed if yours requires one.

## Custom providers & models (providers.json)

The built-in registry can be extended or overridden with a `providers.json` file, layered home then project: `~/.vix/providers.json` and `./.vix/providers.json`. Use it to register a new provider, point a provider at a custom base URL, or override a provider's model list (e.g. to add a model or adjust its context window). On a parse error, vix falls back to the embedded defaults.

json

```
{
  "schema_version": 1,
  "providers": [
    {
      "id": "anthropic",
      "models": [
        { "spec": "anthropic/claude-sonnet-4-6", "context_window": 1000000 }
      ]
    }
  ]
}
```

---

# MCP servers

> Section: Core Concepts · vix docs · https://getvix.dev/docs#mcp

# MCP Servers

Vix supports the **Model Context Protocol (MCP)** — a standard that lets you connect external tool servers to the agent. Any MCP-compatible server (databases, APIs, custom tools) can be wired in and the agent will use its tools just like built-in ones.

## Configuration

Add an `mcp_servers` array to `.vix/settings.json`:

json

```
{
  "version": 1,
  "mcp_servers": [
    {
      "name": "postgres",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-postgres", "postgresql://localhost/mydb"]
    },
    {
      "name": "github",
      "type": "url",
      "url": "https://mcp.example.com/github",
      "headers": { "Authorization": "Bearer ${GITHUB_TOKEN}" }
    }
  ]
}
```

## Server fields

| Field | Required | Description |
| --- | --- | --- |
| name | ✅ | Unique identifier. Used to prefix tool names: \[MCP\] name.tool. |
| type | — | "stdio" (default) for subprocess servers, or "url" for HTTP/SSE servers. |
| command | stdio only | Executable to launch. E.g. npx, python, ./my-server. |
| args | — | Command-line arguments passed to the subprocess. |
| env | — | Extra environment variables for the subprocess. |
| url | url only | HTTP/SSE endpoint for remote servers. |
| headers | — | HTTP headers sent with every request. Values like "${VAR}" are expanded from the environment. |
| allowed\_tools | — | Whitelist of tool names from this server. Unlisted tools are hidden from the agent. |
| require\_confirmation | — | If true, every call to this server's tools requires explicit user approval. |

## How MCP tools appear in the TUI

MCP tool calls are displayed with a `[MCP]` prefix so they are visually distinct from built-in tools:

```
● 🔨 [MCP] postgres.query  SELECT * FROM users;
    ↳ 1 rows
```

The label format is `[MCP] <server>.<tool>`. The result line shows the row count instead of the raw output, keeping the chat compact.

## Tool naming

Internally, each MCP tool is registered under a qualified name:

```
mcp__<server>__<tool>   →   e.g. mcp__postgres__query
```

The agent uses these qualified names when calling tools. The TUI renders them as `[MCP] server.tool` for readability. You do not need to reference qualified names in your prompts — the agent picks the right tool automatically based on its description.

## Tool filtering

Use `allowed_tools` to expose only a subset of a server's tools:

json

```
{
  "name": "postgres",
  "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-postgres", "postgresql://localhost/mydb"],
  "allowed_tools": ["query"]
}
```

Unlisted tools are never registered with the LLM — they cannot be called even if the agent tries.

## Requiring confirmation

For servers that can mutate external state (e.g. write to a database, send a message), set `require_confirmation: true`. Every tool call from that server will pause and ask for user approval before executing:

json

```
{
  "name": "slack",
  "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-slack"],
  "require_confirmation": true
}
```

## Scope and precedence

MCP server configs are loaded from both config files and merged:

-   `~/.vix/settings.json` — user-global (lower precedence)
-   `.vix/settings.json` — project-local (overrides global on name conflict)

If both files define a server with the same `name`, the project-local entry wins.

## Deny list

URL-based MCP servers are subject to the `deny_list.urls` setting. If a server's URL matches a deny entry, it is skipped at startup and never connected.

---

# Stem Agents

> Section: Core Concepts · vix docs · https://getvix.dev/docs#stem-agents

# Stem Agents

A "stem agent" runs the Explore → Plan → Execute phases of a task on a **single agent with one generic system prompt**, instead of spawning a separately-prompted agent for each phase. Phase-specific instructions are delivered as _user messages_, not as new system prompts.

Because the system prompt never changes between phases, the entire conversation — including all exploration context — stays in the model's prompt cache as the task moves from phase to phase. This is the core idea behind vix's cost and speed advantage in plan mode.

## Why it matters

-   **Cross-phase cache reuse** — planning runs with the explore phase already cached, the single biggest cost lever versus a multi-prompt approach.
-   **No redundant exploration** — one agent explores sequentially rather than spawning duplicate explorers that re-read the same files.
-   **Specialization via user messages** — each phase's expected behavior is described in a user message; the quality impact is minimal (both are instructions the model is trained to follow) while the cost savings are substantial.

The default [Plan workflow](/docs#plan-mode) wires these phases together, and [context compaction](/docs#compaction) keeps the shared history within the model's window on long tasks.

---

# Virtual File System (VFS)

> Section: Core Concepts · vix docs · https://getvix.dev/docs#vfs

# Virtual File System (VFS)

The Virtual File System is vix's syntax-aware compression layer. When the agent reads a file through the VFS, the source is parsed, and whitespace, comments, and non-essential formatting are stripped — producing a compact structural skeleton that preserves all semantic content.

This typically achieves a **20–50% reduction in token count** compared to reading raw files, which directly translates to lower cost and faster exploration — especially on large codebases where the agent needs to build a mental model across many files.

## How it works

The VFS operates transparently in the read/write cycle:

-   **On read** — the file is parsed and minified before being sent to the LLM.
-   **On write** — the agent supplies minified content and a language-aware formatter (gofmt, black, prettier, rustfmt, etc.) restores proper indentation and style before persisting to disk.
-   **On edit** — the agent's minified `old_string` is matched in the minified view, projected back onto the exact byte range in the original file, and spliced in place. Only the matched region changes; surrounding formatting and comments are preserved byte-for-byte, so no formatter runs.

## Supported languages

Go, JavaScript, TypeScript, Python, Rust, C, C++, C#, Java, HTML, Swift, Kotlin, Ruby, and PHP. Language is auto-detected from the file extension.

The VFS is configured per-language in `settings.json`. Each language entry specifies whether minification is enabled and which formatter to use for restoration. See [Code Minification](/docs#code-minification) for the tools and the [settings.json reference](/docs#settings-json) for the full schema.

---

# Workflows

> Section: Core Concepts · vix docs · https://getvix.dev/docs#workflows

# Workflows

A workflow is a named, multi-step pipeline defined in `settings.json`. Encode the process explicitly — steps, order, inputs, outputs, branching — and trigger it by name.

## When to use a workflow vs. chat

Use **chat** when the task is open-ended or one-off. Use a **workflow** when:

-   The task has a fixed set of steps in a specific order
-   You want to reuse the process across projects or team members
-   You need human input at a specific point mid-process
-   You want different models or tool sets per phase
-   You want structured, auditable output (cost per step, duration)

## Structure

json

```
{
  "version": 1,
  "workflows": [{
    "name": "Code Review",
    "entry_point": { "id": "analyse" },
    "steps": {
      "analyse": {
        "type": "agent",
        "agent": "reviewer",
        "prompt": "Review the staged changes...",
        "next_steps": [{ "id": "report" }]
      },
      "report": {
        "type": "agent",
        "agent": "writer",
        "prompt": "Write a summary based on:\n\n$(step.analyse)",
        "next_steps": []
      }
    }
  }]
}
```

Trigger from the TUI with `Shift+Tab`, or headlessly:

shell

```
vix -w "Code Review" -p "review PR #42"
```

Two workflows ship by default: [Goal](/docs#goal-mode) (autonomous, budgeted loop with independent verification) and [Plan](/docs#plan-mode) (human-gated explore → plan → review → execute pipeline).

## Step types

**agent** — Runs an LLM agent with its own conversation, tools, and system prompt.

**tool** — Calls a tool directly without an LLM. Commonly used with `ask_question_to_user`.

**bash** — Runs a shell command. Output is captured as `$(step.<id>)`.

## Variable interpolation

Step outputs flow via `$(step.<id>)` tokens. Bash expressions embed inline with `$(bash:command)`.

```
"prompt": "Summarise the review:\n\n$(step.analyse)"

"prompt": "The branch is $(bash:git rev-parse --abbrev-ref HEAD)."
```

## Conditional steps & agent forking

Steps can be skipped via `execute_if` (bash expression, runs if exit 0). Steps can also fork from a previous step's agent using `fork_from`, inheriting its full conversation history.

A step's `next_steps` guards may branch on the step's **own** output — `$(step.<id>)` resolves to the result of the step that just ran. This is the natural way to route on a computed value, e.g. a selector that prints a work item or a `NO_TODO` sentinel:

```
"select": {
  "type": "bash",
  "command": "...print next item or NO_TODO...",
  "next_steps": [
    { "id": "process", "execute_if": "[[ \"$(step.select)\" != *NO_TODO* ]]" }
  ]
}
```

## Human-in-the-loop

A `tool` step with `ask_question_to_user` pauses execution, presents options, and routes to different next steps based on the answer. You can also inject messages into a running workflow from the TUI.

## Per-step cost tracking

```
Code Review complete  ·  3 steps  ·  12.4s

  analyse   claude-opus-4      4 821 in  · 1 203 out  · $0.038  · 8.1s
  report    claude-sonnet-4-6    892 in  ·   341 out  · $0.008  · 4.3s

  Total                                                 $0.046  · 12.4s
```

---

# Skills

> Section: Core Concepts · vix docs · https://getvix.dev/docs#skills

# Skills

A skill is a reusable prompt template invoked with a slash command. Single-shot prompts — parameterised, composable, and instantly accessible from the chat input.

## Invoking a skill

```
> /review internal/auth/jwt.go
> /explain $1 in simple terms
> /test src/components/Button.tsx
```

## Defining a skill

A skill is a directory under `.vix/skills/` containing a `SKILL.md` file:

```
.vix/
└── skills/
    └── review/
        └── SKILL.md
```

yaml

```
---
name: review
description: Review a file for correctness, security, and style
---

Review the following file thoroughly:

$ARGUMENTS

Focus on:
- Logic errors and edge cases
- Security issues
- Error handling
- Readability

Be specific. Cite line numbers. Skip nitpicks.
```

## Argument substitution

| Token | Replaced with |
| --- | --- |
| $ARGUMENTS | The full argument string as typed |
| $1, $2, ... | Individual positional arguments (shell-style splitting) |

## Dynamic context

Skills can run shell commands at invocation time using `` !`command` `` tokens:

yaml

```
---
name: standup
description: Summarise what I worked on today
---

Summarise my work today based on this git log:

!`git log --oneline --since="24 hours ago"`

Keep it to 3-5 bullet points. Write in past tense.
```

## Frontmatter options

yaml

```
---
name: review
description: ...
model: claude-opus-4        # override model (optional)
allowed-tools: read_file, grep, lsp_query  # tool whitelist (optional)
---
```

`allowed-tools` restricts which tools the agent can use. Useful for read-only skills.

## Global vs. project skills

| Location | Scope |
| --- | --- |
| ~/.vix/skills/ | Available in every project |
| .vix/skills/ | Available in this project only |

Project skills take precedence over global skills with the same name. Commit `.vix/skills/` to share with your team.

## Skills vs. workflows

|  | Skills | Workflows |
| --- | --- | --- |
| Trigger | /skill-name args | Shift+Tab or -w flag |
| Steps | Single prompt | Multi-step pipeline |
| Branching | No | Yes |
| Human input mid-run | No | Yes |
| Cost tracking | No | Yes (per-step) |
| Best for | Quick, repeatable prompts | Complex processes |

---

# Context compaction

> Section: Core Concepts · vix docs · https://getvix.dev/docs#compaction

# Context Compaction

Every turn you exchange with the agent — your messages, the model's replies, and every tool call and its output — accumulates in the conversation history and is re-sent to the model on the next turn. On a long thread this eventually fills the model's context window. **Compaction** keeps the thread alive by summarizing the oldest turns into a dense briefing and dropping the raw messages they replace.

The result is a much smaller prompt that still carries the important facts — goals, decisions, file paths, and open tasks — so the agent can keep working without re-reading everything.

## Automatic compaction

At the start of every turn, the daemon checks the size of the last prompt against the model's context window. When it crosses the configured **threshold** (default: 80% of the window), compaction runs automatically before the turn proceeds. You don't have to do anything.

Automatic compaction only happens when the model's context window is known. If the window size is unknown, vix falls back to doing nothing rather than guessing. If the threshold is breached but a single trailing turn already fills the window, there is nothing safe to compact — vix logs a warning and continues.

## Manual compaction with /compact

You can compact on demand from the chat input:

```
> /compact        # summarize older turns using the configured keep policy
> /compact 3      # summarize the first 3 turns, keep everything after
```

`/compact N` drops the first `N` turns into the summary and keeps every turn after them. `N` must leave at least one trailing turn, otherwise there is nothing to keep and the command is a no-op.

## What gets kept

When compaction is triggered automatically (or by a bare `/compact`), the keep policy decides how many recent turns survive untouched:

-   **keep\_last\_n\_turns** — when set to a positive number, exactly that many trailing turns are kept verbatim.
-   **keep ratio** — when `keep_last_n_turns` is not set, vix keeps a trailing fraction of the turns (default: the last 25%, always at least one turn).

Snapshots of the conversation are only taken at turn boundaries, so compaction never splits a `tool_use` / `tool_result` pair — the kept history is always a valid conversation.

## How the summary is produced

The dropped prefix is sent to the model in a single, tool-free call with a dedicated system prompt that asks for a dense, faithful briefing. The summary preserves, in order:

1.  The user's goals and any explicit instructions or constraints still in effect
2.  Key decisions made and their rationale
3.  Files created, edited, or read (exact paths) and the gist of important changes
4.  Important tool outputs, command results, errors, and their resolutions
5.  Open tasks, TODOs, and unresolved questions

The returned summary replaces the dropped messages as a single synthetic user message, labelled so the agent knows it is reading a condensed history:

```
[Summary of earlier conversation, compacted to save context]

...dense briefing of everything that was dropped...
```

The kept turns are appended after the summary, and the token counter is reset so compaction doesn't immediately retrigger on the next turn.

## What you see in the TUI

When compaction runs, a system line appears in the chat:

```
Auto-compacted 6 earlier turn(s) into a summary.
```

A manual `/compact` shows `Compacted N earlier turn(s) into a summary.` instead.

## Configuration

Tune compaction with a `compaction` block in `.vix/settings.json` (or the user-global file):

json

```
{
  "version": 1,
  "compaction": {
    "threshold": 0.8,
    "auto": true,
    "keep_last_n_turns": -1
  }
}
```

| Field | Default | Description |
| --- | --- | --- |
| threshold | 0.8 | Fraction of the context window (0–1\] that triggers automatic compaction. Out-of-range values fall back to the default. |
| auto | true | Master switch for automatic compaction. Set to false to compact only via `/compact`. |
| keep\_last\_n\_turns | \-1 | Positive value keeps exactly that many trailing turns. `-1` uses the trailing 25% ratio instead. |

Both `~/.vix/settings.json` and the project-local `.vix/settings.json` are honoured, with project settings taking precedence.

**Note:** Compaction is lossy by design — the raw text of the dropped turns is gone from the conversation once summarized. The summary is built to retain the actionable facts, but if you need the exact earlier wording, compact deliberately rather than relying on a very high threshold.

---

# The TUI (tabs, threads, keys)

> Section: Guides · vix docs · https://getvix.dev/docs#tui-basics

# The TUI: Tabs, Threads & Keys

The `vix` client is a tabbed terminal UI over the daemon. This page covers the tab layout, running multiple threads at once, slash commands, and the full keybinding reference.

## Tabs

Switch tabs with the function keys:

| Key | Tab | What it shows |
| --- | --- | --- |
| F1 | Threads | List of open threads; create, duplicate, or close them. |
| F2 | Workspace | The active chat thread — your main working view. |
| F3 | Models | Pick the active model and manage provider credentials. |
| F4 | Jobs & Triggers | Browse scheduled jobs and lifecycle hooks; space toggles one on/off. |
| F5 | Settings | Global preferences (e.g. toggle reasoning/thinking output). |

The Models tab is documented under [Models & Providers](/docs#models-providers).

## Starting a thread & the working directory

A brand-new tab (a fresh launch with nothing to restore, or one you open with `Ctrl+T`) starts as a **draft**: the welcome screen shows the target **working directory** and the status bar reads _Draft — not started_. No thread is created on the daemon until you send your first message. This lets you set the directory up front, and means quitting without typing leaves nothing behind.

While the tab is still a draft, press `Ctrl+o` to open a directory picker (`↑`/`↓` select, `→` open a folder, `←` go up, `Enter` choose, `Esc` cancel). Your **first message commits the thread** in the chosen directory, after which the working directory is **fixed for the life of that thread** — to work somewhere else, start another thread (`Ctrl+T`) and point it there.

The draft welcome also lists your **most-used working directories** — the top five directories ranked by how many open threads use each. Press `Tab` to focus the welcome area, then `↑`/`↓` to highlight a directory and `Enter` to make it the draft's working directory (no need to walk the picker). A brand-new tab defaults to the **most recently used** working directory; the very first tab on launch uses where you started `vix` (or the [`--workdir`](/docs#cli-flags) flag).

Note: when vix runs with an explicit `--config-dir`, changing the working directory only moves where files, shell commands and the code index resolve — your `.vix` config (skills, settings, agents) stays fixed at that config directory.

## Multiple threads

One daemon can drive many concurrent threads in the same project, each with its own conversation. Cycle between them without leaving the Workspace tab:

| Key | Action |
| --- | --- |
| Ctrl+T | New thread |
| Ctrl+N | Next thread |
| Ctrl+P | Previous thread |

On the **Threads tab (F1)**: `↑`/`↓` to navigate, `Enter` to open, `t` new, `d` duplicate (forks from the last completed turn), `x` close.

Below your own threads, a **Vix-initiated** group lists runs that vix started itself — [scheduled jobs](/docs#guide-jobs), heartbeat alerts, and failure reports. `Enter` opens a run, `x` dismisses it. The ● unread dot is persistent: it survives quitting vix, so work that happened while you were away is still flagged the next time you launch.

The **Jobs & Triggers tab (F4)** is a live catalogue of what vix runs for you: [scheduled jobs](/docs#guide-jobs) in one group and [lifecycle hooks](/docs#guide-hooks) in another. Each row shows whether it's enabled, its schedule (jobs) or event (hooks), and its last run; a job that is _currently executing_ shows a spinner, updated live. `↑`/`↓` to navigate and `space` to enable/disable the selected row — the change is written straight back to its `job.json`/`hook.json` and hot-reloaded. Hooks never show a running spinner (they fire and finish in milliseconds), only their last fire.

## Keybindings — Workspace (input focused)

| Key | Action |
| --- | --- |
| Enter | Send message / approve step |
| Shift+Enter · Alt+Enter · Ctrl+J | Insert a newline |
| Ctrl+Shift+U | Clear the input |
| Ctrl+R | Open the input-history panel |
| Shift+Tab | Cycle between Chat and configured workflows |
| Tab | Toggle focus between input and the chat pane (on a draft welcome, focuses the recent-directories list) |
| Ctrl+o | Change the working directory (draft thread, before it starts) |
| Esc | Cancel the running operation |
| @ | File-path autocomplete |
| / | Slash-command & skill menu |
| Ctrl+C / Ctrl+D | Quit (press twice to confirm) |

## Keybindings — chat pane (after Tab)

Press `Tab` to focus the chat history, then:

| Key | Action |
| --- | --- |
| ↑ / k · ↓ / j | Scroll one line |
| PgUp / b · PgDn / f | Scroll one page |
| Home / g | Jump to top |
| End / G | Jump to bottom |
| Tab | Return focus to the input |

## Keybindings — plan review

When the agent presents a plan and the input is empty (see [Using plan mode](/docs#plan-mode)):

| Key | Action |
| --- | --- |
| Enter / y | Approve the plan and start executing |
| Esc / n | Reject the plan |
| type feedback + Enter | Refine the plan |

Other overlays follow the same conventions: the history panel and attachment panel use `↑`/`↓` + `Enter`/`Esc`; multi-question prompts use `←`/`→` (or `Ctrl+H`/`Ctrl+L`) to switch tabs.

## Slash commands

Type `/` in the input to open the menu. Built-in commands:

| Command | What it does |
| --- | --- |
| /fork N | Fork a new thread from turn N |
| /trim N | Delete all messages after turn N |
| /copy \[N\] | Copy a turn, or the whole conversation |
| /goto N | Scroll to the start of turn N |
| /clear | Clear the conversation history |
| /compact \[N\] | Summarize older turns to free context |
| /skills | List available skills |

Your own [skills](/docs#writing-skills) appear in the same menu as `/skill-name`. See also [Context compaction](/docs#compaction) for `/compact`.

**Note:** there is no command-palette shortcut — `Ctrl+P` moves to the previous thread. Actions like clearing the conversation are slash commands (`/clear`).

---

# Using chat mode

> Section: Guides · vix docs · https://getvix.dev/docs#chat-mode

# Using Chat Mode

Chat mode is the default way to interact with vix. You type a message, the agent responds — using tools as needed — and the conversation continues until the task is done.

## Sending messages

Type in the input box and press `Enter`. Multi-line input: press `Shift+Enter` (or `Alt+Enter`, `Ctrl+J`). Clear input without submitting: `Ctrl+Shift+U`.

A new tab is a **draft** until your first message: it shows a welcome screen with the working directory and your most-used directories, and the thread is created on the daemon only when you send. You can change the directory first with `Ctrl+o` (or pick from the recent list via `Tab` then `↑`/`↓`/`Enter`); once the thread starts, its working directory is fixed. See [The TUI](/docs#tui-basics) for details.

## Interrupting the agent

Press `Ctrl+C` to cancel the current operation. The in-flight LLM call and any running tool are stopped immediately. Conversation history is preserved. If you send a message while the agent is working, the current operation is cancelled and your message is sent.

## Scrolling the chat

-   Press `Tab` to shift focus to the chat pane
-   Use `↑` / `↓` to scroll line by line
-   `PgUp`/`PgDn` to page; `Home`/`End` to jump to top/bottom
-   Press `Tab` again to return focus to the input

## Attaching images

Drag and drop an image file into the terminal, or type its path. Supported: PNG, JPEG, GIF, WEBP, BMP (max 20MB).

```
> what's wrong with this layout? /Users/me/screenshots/bug.png
```

## @ file completion

Type `@` to open a filesystem autocomplete popup. Navigate with `↑`/`↓`, select with `Enter`.

```
> can you explain @internal/auth/
```

## Referencing skills

Type `/` followed by a skill name:

```
> /review internal/api/handlers.go
> /test src/components/Button.tsx
```

## Input history

`Ctrl+R` opens the history panel — shows recent inputs. Navigate with `↑`/`↓`, select with `Enter`, close with `Esc`.

## Common actions

Frequent actions are mapped to keys and slash commands (full reference in [The TUI](/docs#tui-basics)):

| Action | How |
| --- | --- |
| Clear conversation | /clear |
| Search input history | Ctrl+R |
| Scroll to top / bottom | Tab to focus chat, then Home / End |
| Compact context | /compact |
| Quit | Ctrl+C or Ctrl+D |

## Beautiful code rendering

Code blocks in Vix are rendered with rich syntax highlighting, automatic language detection, and a polished glassmorphism container. Whether the agent outputs JSON configs, shell commands, or YAML workflows, the result is always easy to read and visually stunning.

Screenshot placeholder — beautiful code rendering preview

## Switching to a workflow

Press `Shift+Tab` to cycle through available workflows. A workflow run executes in its own isolated step agents, so a follow-up chat message does not inherit the workflow's internal reasoning.

However, a workflow's **visible output is recorded into the thread transcript** when the run finishes, is interrupted, **or fails**. Reopening the thread replays that output, and continuing in chat picks up with it as context — so you can read the result and then keep talking about it. A run that **fails** (for example, the API stays overloaded through every retry) replays exactly like a successful one: the agent's partial work plus the same `API overloaded — retrying … (attempt N/10)` notices you'd see live, so the failure tells its own story instead of vanishing.

## Quitting

Press `Ctrl+C` or `Ctrl+D` to open the quit dialog. The daemon keeps running after you quit. Conversation history lives in memory for the duration of the daemon process.

---

# Using plan mode

> Section: Guides · vix docs · https://getvix.dev/docs#plan-mode

# Using Plan Mode

For complex, multi-file tasks, the agent can propose a structured plan before doing any work. You review the plan, approve or modify it, then the agent executes it step by step.

## How it works

Plan mode is the built-in **Plan** workflow shipped in the default `settings.json`. Select it with `Shift+Tab` in the input box (or run it headlessly with `-w "Plan"`). It runs as a small pipeline: an _explore_ step (read-only) builds understanding, a _plan_ step drafts the implementation, a _review_ step pauses for your decision, and an _execute_ step carries out the approved plan. No files are written until you approve.

Because it's an ordinary workflow, you can copy and customize it — see [Running Workflows](/docs#guide-workflows) and the [Workflow Schema](/docs#workflow-schema). For unattended, self-verifying runs, see [Goal Mode](/docs#goal-mode).

The plan includes:

-   **Name** — a short title for the task
-   **Context** — what the agent understood about the problem
-   **Architecture** (optional) — relevant structural notes
-   **Files** — which files will be touched
-   **Risks** (optional) — things that could go wrong
-   **Steps** — ordered list of tasks with descriptions

## Reviewing the plan

The review step presents the plan and four choices:

-   **Accept** — approve as-is; execution begins immediately.
-   **Modify** — provide feedback; the plan is refined and shown again (iterate as many times as needed).
-   **Reject** — stop the workflow.
-   **Whiteboard** — generate a visual, voice-narrated walkthrough of the plan in the browser (experimental — see [Web UI](/docs#web-ui)).

With an empty input you can also use the keyboard: `Enter`/`y` to accept, `Esc`/`n` to reject, or type feedback and press `Enter` to refine.

```
▶ Step 1/4: Add rate limiter middleware
    ✓ Step 1/4: Add rate limiter middleware
  ▶ Step 2/4: Wire middleware into router
    ✓ Step 2/4: Wire middleware into router
  ...
```

## During execution

If you want to intervene mid-execution, type and press `Enter`. The current step is cancelled and your message is sent with the context of what has been completed so far.

## Plan files

Each approved plan is saved to `.vix/plans/` as a timestamped Markdown file. Useful for reviewing what was done or as documentation for a change.

```
.vix/plans/2025-01-15_143022.md
```

---

# Workflows

> Section: Guides · vix docs · https://getvix.dev/docs#guide-workflows

# Running Workflows

## Triggering a workflow

### From the TUI

Press `Shift+Tab` to cycle through available workflows. Type your message and press `Enter`.

```
── Code Review ──────────────────────────────────────────
> review the auth changes
```

### Headlessly

shell

```
vix -w "Code Review" -p "review the auth changes in PR #42"
vix -w "Code Review" -p "review auth changes" --output-format json
```

## Watching a workflow run

The workflow graph panel shows every step with a live status:

```
╭─ Workflow: Code Review (2/4) ──────────────────────────╮
│  ✓ fetch   Fetch changed files        (1.2s)           │
│  ▶ analyse Analyse for issues                          │
│  ○ report  Write review summary                        │
│  ○ post    Post to GitHub                              │
╰────────────────────────────────────────────────────────╯
```

## Defining workflows

Workflows are defined in `.vix/settings.json` (project-local) or `~/.vix/settings.json` (global).

json

```
{
  "version": 1,
  "workflows": [{
    "name": "My Workflow",
    "entry_point": { "id": "first-step" },
    "steps": { ... }
  }]
}
```

## Step types

**agent** — Runs an LLM agent. Fields: `agent`, `prompt`, `json_output`, `deny_tools`, `fork_from`.

**tool** — Calls a tool directly. Supports `ask_question_to_user` with option-based routing.

**bash** — Runs a shell command. Fields: `command`, `input`.

## Template tokens

| Token | Value |
| --- | --- |
| $(workflow.prompt) | The initial message that triggered the workflow |
| $(workflow.dir) | Absolute path to the job's own directory (~/.vix/jobs/<id>) for scheduled runs; empty otherwise. Use it to persist run-to-run state, e.g. a memory file. |
| $(step.<id>) | Full text output of a completed step |
| $(step.<id>.<key>) | A JSON key from a step with json\_output |
| $(bash:command) | Executes command, replaces with stdout |
| $(file:path) | Includes file contents from .vix/ |
| $(working\_directory) | Absolute path to project root |
| $(platform) | OS name (e.g. darwin) |

## Routing and branching

**Linear chain:** Each step has one `next_steps` entry.

**Conditional:** Use `execute_if` bash expressions. Exit 0 = step runs.

**Option-based:** `ask_question_to_user` routes based on user selection.

**Parallel:** Multiple passing `next_steps` run concurrently.

## Agent forking

Use `fork_from` to clone an existing agent's conversation history. More efficient than starting fresh — the forked agent has all exploration context.

## JSON output

Set `json_output: true` to parse the agent's response as JSON. Keys become `$(step.<id>.<key>)` variables. Use `display_key` to control what's shown in the workflow graph.

## Complete example

json

```
{
  "version": 1,
  "workflows": [{
    "name": "Code Review",
    "summary": "$(step.report)",
    "entry_point": { "id": "scope" },
    "steps": {
      "scope": {
        "type": "tool",
        "tool": "ask_question_to_user",
        "question": "What should be reviewed?",
        "options": [
          { "title": "Staged changes", "steps": [{ "id": "fetch-staged" }] },
          { "title": "Specific files", "has_user_input": true,
            "steps": [{ "id": "analyse", "params": { "target": "$(user_text)" } }] }
        ]
      },
      "fetch-staged": {
        "type": "bash",
        "command": "git diff --cached --name-only",
        "next_steps": [{ "id": "analyse", "params": { "target": "$(step.fetch-staged)" } }]
      },
      "analyse": {
        "type": "agent", "agent": "reviewer",
        "json_output": true, "display_key": "summary",
        "deny_tools": ["write_file", "edit_file", "delete_file"],
        "prompt": "Review these files:\n\n$(target)\n\nReturn JSON: summary, risk_level, issues.",
        "next_steps": [{ "id": "report" }]
      },
      "report": {
        "type": "agent", "fork_from": "analyse",
        "prompt": "Write a markdown review report.",
        "output": ".vix/last-review.md"
      }
    }
  }]
}
```

---

# Scheduled jobs

> Section: Guides · vix docs · https://getvix.dev/docs#guide-jobs

# Scheduled Jobs

Jobs let vix work for you on a schedule: audit your dependencies every morning, watch a repo for new pull requests, remind you about something next Tuesday, or check a whiteboard of tasks every half hour. Each run is a real agent thread — same tools, same model, same safety rules — that lands in your Threads tab when it has something to show.

The mental model is one sentence: **a job is a JSON file containing a schedule and a prompt.** Each job lives in its own directory (`~/.vix/jobs/<id>/job.json`), the daemon watches the directory, and saving a file is all it takes — no restart, no registration step.

## The fastest way: just ask

vix ships with a `jobs` skill, so the agent knows how to create and manage jobs itself. Describe what you want in plain language:

```
> every weekday at 9am, check this repo for stale branches older
  than a month and summarise them
```

vix writes the job file, verifies it registered correctly, and tells you when the first run will fire. You never have to touch JSON if you don't want to.

## Anatomy of a job

Here is the file the request above produces, at `~/.vix/jobs/stale-branches/job.json`:

json

```
{
  "id": "stale-branches",
  "name": "Stale branch report",
  "enabled": true,
  "trigger": { "type": "cron", "expr": "0 9 * * 1-5" },
  "prompt": "List branches not touched in 30+ days and summarise each.",
  "cwd": "/Users/you/Developer/myproject",
  "timeout": "10m"
}
```

-   **trigger** — when to run (cron expression or a one-shot timestamp).
-   **prompt** — what to do. It can also be a file: `"$(file:tasks/audit.md)"` is re-read at every fire, so editing the file changes the next run without touching the job.
-   **cwd** — which project the run works in.
-   **workflow\_id** / **workflow** (optional, at most one) — run a workflow instead of a plain chat turn, gaining budgets and tool restrictions. `workflow_id` names a workflow from `config/workflow.json`; `workflow` embeds a self-contained definition inline (no separate file).
-   **timeout** — wall-clock cap per run (default 10m).

Save the file and it's live. Edit it and the change applies to the next run. The full field list is in the [Job spec reference](/docs#job-spec).

## Schedules

Recurring jobs use standard 5-field cron syntax, plus friendly shortcuts:

| Expression | Meaning |
| --- | --- |
| 0 9 \* \* 1 | Every Monday at 09:00 |
| \*/30 9-19 \* \* \* | Every 30 minutes, between 9am and 7:59pm |
| @every 2h | Every two hours, from daemon start |
| @daily | Once a day at midnight |

Working hours live in the cron hour field — `*/30 9-19 * * *` is "every 30 minutes, but only during the day". For one-off reminders, use an `at` trigger instead:

json

```
"trigger": { "type": "at", "time": "2026-03-01T09:00:00Z" }
```

One-shot jobs run once and are marked completed — they stay on disk for inspection but never fire again.

## The heartbeat

vix ships with one job pre-installed: every 30 minutes during the day, it reads `~/.vix/jobs/heartbeat/heartbeat.md` and does whatever the file says. Think of the file as a whiteboard: write a task on it and the next heartbeat picks it up; wipe it clean and the heartbeat goes back to sleep.

```
# Heartbeat

- Check git status in ~/Developer/myproject; if there are uncommitted
  changes older than a day, summarise them.
- Read the last 20 lines of ~/logs/backup.log and alert me if the most
  recent backup failed.
```

**It costs nothing while idle.** When the file contains only headings and comments, the check is skipped _before any model call_ — zero tokens. And when a check runs but finds nothing to report, the agent answers `HEARTBEAT_OK` and the run leaves no trace. You only hear about it when something needs attention.

## Watching for events

There are no webhooks to configure. To react to external events — new PRs, a failing CI job, a file appearing — pair a frequent job with a workflow whose first step is a cheap `bash` check, and gate the agent step behind `execute_if`:

json

```
{
  "name": "watch-prs",
  "display_in_tui": false,
  "entry_point": { "id": "poll" },
  "steps": {
    "poll": {
      "type": "bash",
      "command": "gh pr list --json number,title --search \"created:>$(date -v-2M '+%Y-%m-%dT%H:%M:%S')\" | grep -v '^\\[\\]$' || true",
      "next_steps": [
        { "id": "react", "params": { "prs": "$(step.poll)" },
          "execute_if": "[ -n \"$(step.poll)\" ]" }
      ]
    },
    "react": {
      "type": "agent",
      "agent": "general",
      "prompt": "New pull requests:\n$(prs)\n\nReview each briefly."
    }
  }
}
```

Run it with `"trigger": { "type": "cron", "expr": "@every 2m" }`. When the poll finds nothing, the run ends after the bash step — **no model call, no thread, no cost**. When something shows up, the agent wakes with the results already in hand. That's webhook-grade reactivity with nothing exposed to the network.

The same shape powers the **"Plan GitHub issues"** job. Point it at a repository — paste either `owner/repo` or a full `https://github.com/owner/repo` URL (it's normalized to `owner/repo`) — choose whether to watch issues, pull requests, or both, and pick how often it runs: **hourly** or **daily**. A first `bash` step detects how it can reach GitHub — the `gh` CLI when it's signed in, otherwise the public REST API — and `execute_if` branches on that token: no access at all stops the run with a clear error, the API fallback adds a one-line reminder to install and authenticate `gh`, and either way the fetched issues (and/or pull requests) flow straight into the planning step via `$(step.fetch)` — no temporary file. A `bash` step then reconciles a `tracker.tsv` in the job's own directory — every open item as `todo`, dropping items that have since closed. The agent claims **a single open item per run** — it picks a `todo`, flips it to `doing` so any parallel run skips it, and marks it `done` when finished (reconciling newly-opened items in and closed ones out each run) — investigates it, and writes its findings to the run's thread: a short summary, a **legitimacy verdict** (is this a real, actionable bug / request / piece of feedback?), and a step-by-step implementation plan when the item is legit — or a note explaining why planning was skipped when it isn't. It never posts comments back to GitHub.

The bookkeeping is **resilient to transient failures**. If a fetch fails outright — a network blip, a rate limit, or the machine waking before its connection is up — the run detects it and **leaves the tracker untouched** rather than mistaking an empty response for "no open items"; a single bad fetch can never wipe the tracker and re-investigate every issue from scratch. And if a run is cut short before it can mark its item `done` (a hard timeout or cancel), the next run **re-arms that item** for another attempt — bounded to two tries before it's set aside — so nothing gets stuck half-done and nothing loops forever.

The findings open with an **H1 title** and a deterministic header naming the item, so the run's thread is titled after it — e.g. `[Plan GitHub issues (get-vix/vix)] Addressing issue #29 — ANTHROPIC_BASE_URL not resolved from .env files` rather than a generic timestamp. The thread keeps the **full working transcript** — the prompt, every tool call and result, and the final findings — so opening it shows exactly what vix did, and a follow-up message you send picks up grounded in that real history. Like every workflow step, the run also inherits your project context: `AGENTS.md`/`CLAUDE.md` and the skills catalog are in its system prompt, and it can invoke skills with the `skill` tool. Once a run finishes it reopens in plain chat mode, so you can simply reply to continue — there's no stale "workflow no longer exists" notice. A run that **fails** reopens the same way: its full transcript is kept — the agent's partial work plus the same `API overloaded — retrying … (attempt N/10)` notices an interactive run shows live — so you see exactly how it failed instead of an empty thread.

## Market research

The **"Market research"** job ships a recurring brief on a schedule. Pick a frequency (**daily**, **weekly**, or **monthly**), the sources to scan — **GitHub**, **Web**, **Reddit**, **Twitter/X** — a working directory, and a research prompt. The inline workflow runs four steps: a first **keyword** step distils your prompt into one search query (written to `keywords.txt`); a `bash` **detect** step checks the backends each selected source needs are installed; a **search** step queries each source through its tool — reading the query back from the file at runtime, never splicing model text into the shell; and a read-only **summarize** step writes a dated Markdown brief that ends in three actionable insights.

The search runs through [Agent Reach](https://github.com/Panniantong/agent-reach) and the upstream CLIs it installs (`gh`, Exa via `mcporter`, `opencli`/`rdt` for Reddit, `twitter-cli`). This job optimises for **quality over zero-install**: if a selected source's backend isn't ready, the run **stops with setup instructions** rather than producing a thin brief. Set it up once in a vix chat — just ask _"set up market research"_ and vix installs the tools (and walks you through the logins) with your confirmation. **Twitter/X** needs login cookies, so use a burner account. There's nothing to install for the scheduled run itself — the setup is a one-time, interactive step.

## Where results go

Finished runs appear on the **Threads tab (F1)** under a separate **Vix-initiated** group. Each titled run shows its bare title (for the GitHub-plan job, the per-item `[job] Addressing issue #N — …`); a failed run is flagged with a ⚠ marker. Press `enter` to open a run and read the full conversation, or `x` to dismiss it. The unread dot is persistent — quit vix, come back tomorrow, and anything that ran while you were away is still flagged. The group refreshes **live**: a run that lands while you're looking at the Threads tab appears on its own, even before you've started a thread in that window.

To see every job at a glance — including ones that haven't run yet — open the **Jobs & Triggers tab (F4)**. It lists each job with its schedule, next/last run, and a live spinner while it's executing, and lets you enable or disable any job (or [hook](/docs#guide-hooks)) with `space` — no need to hand-edit the spec file.

-   **Failures wait for you.** If a run fails while no vix is open, the failed run is written with its full conversation — the agent's partial work and the retry notices — so the next launch replays exactly what happened. Failed runs are never auto-dismissed.
-   **Old runs clean themselves up.** Only the latest few runs per job stay in the list; a deeper history is kept in the closed-threads archive.
-   **Quiet runs leave nothing.** Skipped checks and HEARTBEAT\_OK answers don't create threads at all.

## Run it now

You don't have to wait for the schedule. Fire a job immediately by id with the CLI:

shell

```
vix job run stale-branches   # prints the run's thread id
```

The run proceeds in the background and lands under **Vix-initiated** threads, exactly like a scheduled fire. It's handy for testing a new job, or for kicking one off out of band. A manual run _records its outcome_ but **leaves the schedule untouched** — it never advances the next cron slot or completes a one-shot — and it runs even when the job is disabled. The only thing it refuses is a job that's already running.

## Keeping it running

Jobs fire inside the daemon, so they run only while `vixd` is up. If the daemon was down when a job was due, it catches up at the next start (up to 3 overdue runs fire immediately; the rest are recorded as skipped). To make the schedule survive reboots, register the daemon to start at login:

shell

```
vix daemon install   # macOS LaunchAgent / Linux systemd user unit
vix daemon uninstall # remove it again
```

## Run logs

Every run is recorded as append-only JSONL at `~/.vix/logs/jobs/<date>.jsonl`, one daily file. Each run emits a `started` line, an optional `error` line for each failure (with a `source` such as `prompt_resolve`, `agent`, or `timeout`), and a `finished` line carrying the `status` and `thread_id`. Correlate a run by its `job_id`. Retention is `logs.retention_days` (default 10; 0 = keep forever).

shell

```
# Last finished run for a job
grep -h '"phase":"finished"' ~/.vix/logs/jobs/*.jsonl | jq -c 'select(.job_id=="stale-branches")' | tail -1
```

## Safety

Scheduled runs are unattended, so there is nobody to click "approve". By default they can read and write like a normal thread. The brakes:

-   **Per-job permissions** — set `"permissions": { "auto_write": false }` and write attempts are denied and recorded instead of executed.
-   **deny\_list** in settings.json always wins, exactly as in interactive threads.
-   **Workflow budgets and deny\_tools** — cap tokens, time, and iterations; block specific tools. Recommended for anything frequent.
-   **Auto-disable** — five consecutive failures disable a job until you edit its file.

**Kill switch:** set `"features": { "jobs": false }` in settings.json, or export `VIX_DISABLE_JOBS=1`, and the scheduler never starts.

---

# Lifecycle hooks

> Section: Guides · vix docs · https://getvix.dev/docs#guide-hooks

# Lifecycle Hooks

Hooks let you run your own code at specific points in the agent loop: before a tool runs, after it completes, when a prompt is submitted, when a thread starts, or when a turn finishes. Use them to enforce rules deterministically — block edits to protected files, auto-format after writes, validate prompts, send notifications — instead of hoping the model remembers.

Where a [job](/docs#guide-jobs) is fired by a clock, a hook is fired by an **event**. Like jobs, a hook is just a JSON file (`~/.vix/hooks/<id>/hook.json`); the daemon watches the directory, so saving a file is all it takes.

**Hooks are guardrails, not a security boundary.** The hard boundary is the deny list and permission system, which run _before_ any hook. A blocking hook can veto the common tool paths, but treat real enforcement as the job of [access control](/docs#security-access).

## Sync vs async

**Async** hooks (the default) fire-and-forget — the turn never waits. Use them to notify, log, or kick off background work; async workflow/prompt hooks show up in the Threads tab under "Vix-initiated". **Sync** hooks run inline and return a decision; only a sync hook with `"blocking": true` can actually veto or rewrite the action (and only on `PreToolUse`, `UserPromptSubmit`, and `PermissionRequest`). Sync hooks have a tight timeout and fail open, so a broken hook can never wedge the loop.

## Block writes to protected files

A blocking `PreToolUse` hook. The command reads the tool input on stdin and exits `2` to deny; the message on stderr is shown to the model.

json

```
{
  "id": "protect", "enabled": true,
  "mode": "sync", "blocking": true,
  "trigger": { "event": "PreToolUse", "matcher": "write_file|edit_file" },
  "command": "p=$(jq -r .tool_input.path); case \"$p\" in *.env) echo \"blocked: $p\" >&2; exit 2;; esac"
}
```

## Auto-format after edits

An async `PostToolUse` hook needs no decision — it just runs.

json

```
{
  "id": "fmt", "enabled": true, "mode": "async",
  "trigger": { "event": "PostToolUse", "matcher": "write_file|edit_file" },
  "command": "jq -r .tool_input.path | xargs -r prettier --write"
}
```

## Notify the user from a hook

When a hook needs to tell you something — a finding, a result, a nudge — it can create a one-message conversation with `vix thread create`. The new thread lands in the Threads tab under **Vix-initiated**; open it and reply and it continues as a normal chat. It spends zero tokens — the message is your literal text. The spec is a JSON object read from stdin (or `--json` / `--file`); `message` and `cwd` are required.

shell

```
echo '{
  "message": "Heads up: 3 dependencies have new security advisories.",
  "cwd": "/abs/project",
  "title": "Dependency advisory"
}' | vix thread create
```

The thread is created with `origin: "vix"`, so — like every vix-initiated thread — it never fires hooks itself. A **command** hook spawns no thread of its own, making it the cheap place to do bookkeeping (e.g. count events in a file) that only calls `vix thread create` once a threshold is crossed.

## The decision contract

A command hook decides via its exit code and stdout: exit 0 with no output allows; exit 0 with plain text injects that text as context for the model; exit 2 denies (reason from stderr); any other exit fails open. For richer control, print JSON on stdout:

json

```
{ "behavior": "deny",    "reason": "Destructive command blocked." }
{ "behavior": "modify",  "input": { "path": "safe.txt" } }
{ "behavior": "context", "context": "Note shown to the model." }
```

Workflow and prompt hooks decide via their final text — a JSON object as above, or a line starting with `BLOCK:` to deny. When several hooks match one event, the most restrictive wins (`deny > modify > context > allow`).

## Context passed to a hook

Every hook receives a JSON envelope (command hooks on stdin) with `thread_id`, `cwd`, `model`, `permission_mode`, `origin` (`user` or `vix`), plus event-specific fields like `tool_name` and `tool_input`. Filter inside the hook to scope it by directory, model, or who started the thread. Hooks never fire inside vix-initiated threads, so a hook's own tool calls can't re-trigger hooks.

Full field list and event table: [Hook spec reference](/docs#hook-spec). Disable the engine entirely with `"features": { "hooks": false }` or `VIX_DISABLE_HOOKS=1`.

## Trigger it now

Fire a hook on demand by id, out of band from its event:

shell

```
vix hook trigger nightly-summary
```

Since a manual trigger has no action to veto, it always runs **fire-and-forget regardless of mode**, even when the hook is disabled — handy for testing one. A workflow- or prompt-form hook runs in an isolated thread (its id is printed and it lands under **Vix-initiated**); a command hook runs its command and prints the fire id.

## Run logs

Every fire — sync and async alike — is recorded as append-only JSONL at `~/.vix/logs/hooks/<date>.jsonl`. Each fire emits a `fired` line, an optional `error` line (with a `source` such as `command_exec` or `agent`, plus the command `exit_code`), and a `finished` line with the resolved decision. Correlate a fire by its `fire_id`. Retention follows `logs.retention_days` (default 10; 0 = keep forever).

---

# Writing custom agents

> Section: Guides · vix docs · https://getvix.dev/docs#custom-agents

# Writing Custom Agents

A custom agent is a Markdown file with YAML frontmatter. It defines a specialised LLM persona with its own system prompt, model, tool set, and turn limit.

## File location

```
.vix/agents/my-agent.md       # project-local (commit this)
~/.vix/agents/my-agent.md     # user-global (every project)
```

Project-local agents take precedence over global ones when names conflict.

## Minimal example

yaml

```
---
name: reviewer
description: Reviews code for correctness and security
---

You are a senior engineer doing a focused code review.
Be specific. Cite file paths and line numbers.
Do not suggest style changes unless they affect correctness.
```

Save to `.vix/agents/reviewer.md`. Available immediately — no restart required.

## Frontmatter fields

| Field | Default | Description |
| --- | --- | --- |
| name | filename | The agent's identifier |
| description | — | One-line description shown in spawn\_agent listing |
| model | thread model | Override the LLM model for this agent |
| tools | all tools | Whitelist of tools (comma-separated) |
| effort | — | Effort level hint passed to the model (e.g. low, medium, high) |
| max\_turns | 20 | Max LLM turns before the agent stops |
| max\_tokens | — | Max tokens for each LLM response |

## Tool filtering

The `tools` field restricts what tools the agent can use. Unlisted tools don't exist from the model's perspective.

yaml

```
---
name: explorer
tools: read_file, grep, glob_files, lsp_query
---

You are a read-only code explorer. Analyse the codebase and answer questions.
Never modify files.
```

## System prompt template tokens

| Token | Value |
| --- | --- |
| $(working\_directory) | Absolute project root path |
| $(platform) | OS (e.g. darwin) |
| $(shell) | Shell (e.g. zsh) |
| $(model) | The active model name |
| $(is\_git\_repo) | "Yes" or "No" |
| $(file:path) | Include file contents from .vix/ |

## Setting the default chat agent

Set `"agent": "reviewer"` in `.vix/settings.json` to change the default for all chat threads in a project.

## Per-agent model selection

Define agents with different models for different workflow steps:

yaml

```
---
name: deep-analyst
model: anthropic/claude-opus-4-8
max_turns: 30
tools: read_file, grep, glob_files, lsp_query
---
You perform deep, thorough analysis. Take your time.
```

yaml

```
---
name: fast-writer
model: anthropic/claude-haiku-4-5-20251001
max_turns: 5
tools: write_file, edit_file
---
You write clean, minimal code quickly based on specs provided.
```

---

# Writing skills

> Section: Guides · vix docs · https://getvix.dev/docs#writing-skills

# Writing Skills

A skill is a reusable prompt template invoked with a `/skill-name` slash command from the chat input.

## File structure

```
.vix/skills/
└── review/
    └── SKILL.md
```

Each skill is a directory containing a `SKILL.md` file. The directory name is the default skill name.

## Example

yaml

```
---
name: review
description: Review a file for correctness and security
---

Review the following file thoroughly:

$ARGUMENTS

Focus on:
- Logic errors and edge cases
- Security vulnerabilities
- Error handling completeness

Be specific. Cite line numbers.
```

## Frontmatter fields

| Field | Default | Description |
| --- | --- | --- |
| name | directory name | The slash-command name |
| description | — | Shown in /skills list and system prompt |
| model | thread model | Override the model for this skill |
| allowed-tools | all tools | Restrict which tools can be called |

## Argument substitution

| Token | Replaced with |
| --- | --- |
| $ARGUMENTS | Full argument string as typed |
| $1, $2, ... | Positional arguments (shell-style, quotes respected) |

```
> /compare src/old.go src/new.go
# $1 → src/old.go, $2 → src/new.go
```

## Dynamic context with !

Use `` !`command` `` tokens to run shell commands at invocation time:

yaml

```
---
name: standup
description: Summarise my work from the last 24 hours
---

Write a standup summary based on this git activity:

!`git log --oneline --since="24 hours ago"`

Keep it to 3–5 bullets. Past tense. One line each.
```

## Read-only skills

Use `allowed-tools` to create skills that cannot modify files:

yaml

```
---
name: explain
description: Explain a piece of code in plain English
allowed-tools: read_file, grep, glob_files, lsp_query
---

Explain the following in plain English:

$ARGUMENTS

Cover: what it does, why it exists, and any non-obvious design decisions.
```

## Global vs. project skills

| Location | Scope |
| --- | --- |
| ~/.vix/skills/ | Available in every project |
| .vix/skills/ | This project only |

Project skills override global skills on name conflict.

---

# Writing plugins

> Section: Guides · vix docs · https://getvix.dev/docs#writing-plugins

# Writing Plugins

## What are plugins?

Plugins are executable scripts or binaries that vix runs once at daemon startup to configure how API calls are made. They can add or override HTTP request headers, strip SDK-injected headers, and prepend text to every system prompt. Plugins are language-agnostic: shell scripts with a shebang, Python scripts, compiled binaries — anything the OS can execute.

## When are plugins loaded?

Plugins are loaded once, when `vixd` starts, before any thread connects. They are not re-run per thread. Restart the daemon to pick up changes to plugins.

## Discovery

Plugins are discovered from two directories, merged in order (home first, project second):

text

```
~/.vix/plugins/         ← user-global, applies to every project
.vix/plugins/           ← this project only
```

Rules for discovery:

-   Files must have the executable bit set (`chmod +x`).
-   Files whose names start with `.` or `_` are skipped.
-   The OS kernel handles shebang lines — no extension filtering needed. A `#!/usr/bin/env python3` script works as long as it is executable.
-   In `--config-dir` override mode, only `<config-dir>/plugins/` is scanned.

## Output schema

Each plugin must print a single JSON object to stdout:

json

```
{
  "headers": {
    "Authorization": "Bearer sk-ant-oat01-...",
    "x-api-key": null
  },
  "system_prefix": "some text prepended to every system prompt"
}
```

| Field | Type | Description |
| --- | --- | --- |
| headers | object | String value = set or override that header. `null` = strip that header from every outgoing API request. |
| system\_prefix | string | Text prepended as the first block of the system prompt on every LLM call. Empty string is a no-op. |

## Context passed to plugins

vix sends a small JSON object to each plugin on stdin:

json

```
{"version": "1.2.3", "model": "claude-sonnet-4-5-20250929"}
```

Plugins can use or ignore this. It is provided on a single line followed by EOF.

## Merging

When multiple plugins are found across the home and project directories, all are run and their outputs merged:

-   **Headers**: last-writer-wins. A project plugin can override a home plugin's header value.
-   **system\_prefix**: values are joined with a newline (`\n`) separator.

## Error handling

A plugin that fails (non-zero exit, timeout, invalid JSON output) is logged and skipped — it never prevents the daemon from starting. Each plugin has a 5-second execution timeout.

text

```
[vixd] plugin loaded: /Users/you/.vix/plugins/team_tag.sh
[vixd] plugin /path/to/bad.sh failed: exit status 1
```

## Global vs. project scope

| Location | Scope |
| --- | --- |
| ~/.vix/plugins/ | Every project on this machine |
| .vix/plugins/ | This project only |

## Complete example: team & project tagging

This example adds a custom header and a short system prefix so responses can be tagged with your team or project name. It reads a `TEAM_NAME` environment variable and does nothing when it is not set.

Shell (`.vix/plugins/team_tag.sh`):

bash

```
#!/bin/sh
team="${TEAM_NAME:-}"
[ -z "$team" ] && echo '{}' && exit 0
printf '{"headers":{"X-Vix-Team":"%s"},"system_prefix":"[Team: %s] "}\n' "$team" "$team"
```

Python (`.vix/plugins/team_tag.py`):

python

```
#!/usr/bin/env python3
import os, json
team = os.environ.get("TEAM_NAME", "")
if not team:
    print("{}")
else:
    print(json.dumps({
        "headers": {"X-Vix-Team": team},
        "system_prefix": f"[Team: {team}] "
    }))
```

Then activate it:

bash

```
chmod +x .vix/plugins/team_tag.sh   # or team_tag.py
# Restart vixd to load the plugin
```

**Note:** A plugin file that ships without the execute bit (e.g. from a git clone) is silently skipped. Run `chmod +x` to activate it, then restart `vixd`.

---

# Headless & CI usage

> Section: Guides · vix docs · https://getvix.dev/docs#headless-ci

# Headless & CI Usage

Vix can run without a terminal UI — useful for scripts, CI pipelines, git hooks, and automation.

## Basic usage

shell

```
vix -p "add docstrings to all public functions in internal/api/"
```

Output goes to stdout. Tool calls and errors go to stderr.

## Reading from stdin

shell

```
echo "summarise the last 10 commits" | vix -p -
git diff HEAD~1 | vix -p "review this diff for issues"
cat error.log | vix -p "explain these errors and suggest fixes"
```

## Output formats

### text (default)

The agent's final text response to stdout. Tool calls to stderr.

### json

A single JSON object with result, thread ID, turn count, duration, and token usage.

shell

```
vix -p "list all TODO comments" --output-format json
```

### stream-json

Every daemon event as newline-delimited JSON in real time.

shell

```
vix -p "run tests and fix failures" --output-format stream-json
```

## Running workflows headlessly

shell

```
vix -w "Code Review" -p "review the staged changes"
vix -w "Release Notes" -p "v2.3.0" --output-format json
```

In headless mode: `ask_question_to_user` steps auto-select the first option, plan reviews are auto-approved.

## Persistent daemon for CI

```
# Start daemon in background
vixd &
DAEMON_PID=$!

# Run multiple prompts with warm brain index
vix -p "run tests"
vix -p "fix any failing tests"
vix -p "update the changelog"

kill $DAEMON_PID
```

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Error (API failure, daemon unavailable, invalid flags) |
| 130 | Interrupted by SIGINT/SIGTERM |

## GitHub Actions example

shell

```
- name: AI code review
  env:
    ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
  run: |
    vix -p "review the changes in this PR" \
        --output-format json \
        > review.json
    cat review.json | jq -r '.result'
```

**Security note:** In headless mode, all tool confirmations are auto-approved — including `write_file`, `edit_file`, `delete_file`, and `bash`. There is no human in the loop. Make sure you trust the prompt and agent configuration.

---

# Using LSP

> Section: Guides · vix docs · https://getvix.dev/docs#using-lsp

# Using LSP

Vix integrates with Language Server Protocol servers to give the agent compiler-level code intelligence — the same information your editor uses for go-to-definition, find-references, and inline diagnostics.

## Why it matters

With LSP, the agent can:

-   Jump to the exact definition of any symbol, across files and dependencies
-   Find every call site of a function in milliseconds
-   Read type signatures and documentation without opening files
-   See live compiler errors and warnings before running the code
-   Find all implementations of an interface

## Supported operations

| Operation | Description |
| --- | --- |
| go\_to\_definition | Find where a symbol is defined |
| find\_references | Find all usages of a symbol |
| hover | Get type info and documentation |
| document\_symbols | List all symbols in a file |
| workspace\_symbols | Search for symbols across the project |
| find\_implementations | Find all types implementing an interface |
| diagnostics | Get errors and warnings for a file |

## Setup

Configure LSP servers in `.vix/settings.json`:

json

```
{
  "version": 1,
  "languages": [
    { "name": "go", "extensions": [".go"], "lsp": { "command": "gopls", "args": [] } },
    { "name": "python", "extensions": [".py"], "lsp": { "command": "pylsp", "args": [] } },
    { "name": "typescript", "extensions": [".ts", ".tsx"], "lsp": { "command": "typescript-language-server", "args": ["--stdio"] } }
  ]
}
```

## Installing language servers

### Go

shell

```
go install golang.org/x/tools/gopls@latest
```

### Python

```
pip install python-lsp-server
```

### TypeScript / JavaScript

```
npm install -g typescript-language-server typescript
```

### Rust

```
rustup component add rust-analyzer
```

## Checking LSP status

json

```
[vixd] ✓ gopls started for go
[vixd] ✓ pylsp started for python
[vixd] ✗ typescript-language-server not found in PATH
```

## How the agent uses LSP

The agent uses `lsp_query` automatically when it needs precise code navigation. You can also ask directly:

```
> find all implementations of the UserRepository interface
> what are all the call sites of processPayment?
> show me the diagnostics for internal/api/handlers.go
```

---

# Web UI (Mission Control)

> Section: Guides · vix docs · https://getvix.dev/docs#web-ui

# Web UI (Mission Control)

**Experimental.** The web UI, whiteboard, and voice walkthrough are a preview and may change or be removed. Everything here is optional — vix works fully from the terminal.

`vixd` can serve a local browser dashboard ("Mission Control") for monitoring threads and walking through plans on an interactive whiteboard.

## Enabling & disabling

A standalone `vixd` serves the dashboard on `http://127.0.0.1:1337` by default.

-   `--web-port <n>` (env `VIX_WEB_PORT`) — change the port; `0` disables it.
-   `--no-mission-control` (env `VIX_NO_MISSION_CONTROL`) — turn it off entirely.

The dashboard is served by the daemon you manage with `vix daemon start` (or a directly-launched `vixd`).

## What it shows

A live list of threads with updates streamed over a WebSocket, plus basic host vitals. It is read-only monitoring — the agent is still driven from the TUI (or headless).

Dedicated **Jobs** and **Hooks** tabs list every registered [scheduled job](/docs#guide-jobs) and [lifecycle hook](/docs#guide-hooks), each with a detail page. Every field on those pages comes straight from the daemon over the same WebSocket: a job's trigger, workflow, working directory, timeout, permissions, and its **recent-run history** (status, duration, and a link to each run's thread); a hook's event/matcher, mode, blocking flag, command/workflow/prompt, timeout, description, and its **recent-fire history**. The history mirrors each item's `state.json` (newest first, capped at the last 10 runs), so a never-run job shows "No runs yet" rather than placeholder data.

## Whiteboard & voice walkthrough

The [Plan workflow](/docs#plan-mode)'s review step offers a **Whiteboard** option that generates scenes for the proposed plan and opens them in the web UI, narrated by an ElevenLabs voice agent. The voice agent is configured by the `elevenlabs` block in `settings.json`.

---

# Code Minification

> Section: Guides · vix docs · https://getvix.dev/docs#code-minification

# Code Minification

vix can read and write source through a **virtual file system (VFS)** that strips comments and non-essential whitespace using Tree-sitter, producing a compact but semantically complete representation of the code. This typically cuts **20–50% of tokens** — most of the saving comes during exploration, where the agent reads many files to build a mental model. For the conceptual overview see [Virtual File System](/docs#vfs).

## The minified tools

Three tools operate through the VFS. The agent works entirely in the minified representation; vix handles the round-trip:

-   `read_minified_file` — returns the file minified instead of verbatim.
-   `write_minified_file` — accepts minified content; a language formatter restores valid, properly formatted source before it lands on disk.
-   `edit_minified_file` — matches `old_string` against the minified source, then splices the change into the original file in place, preserving its formatting and comments byte-for-byte.

The match is found in the minified view and then projected back onto the exact byte range in the unminified file using a position map, so only the matched region changes — the rest of the file (whitespace, indentation, comments) is left untouched and no formatter run is required. The match must be unique. Editing only needs `vfs.enable` (symmetric with `read_minified_file`); no formatter is needed.

When the VFS is disabled for a language, these tools transparently fall back to the plain `read_file` / `edit_file` behavior.

## Supported languages

Go, JavaScript, TypeScript, Python, Rust, C, C++, C#, Java, HTML, Swift, Kotlin, Ruby, and PHP. Language is auto-detected from the file extension.

## Configuration

Minification is configured per language under the `languages` array in `settings.json`: a `vfs` block toggles it on and chooses whether to keep comments, and a `formatter` block names the command used to restore source on write.

json

```
{
  "version": 1,
  "languages": [
    {
      "name": "go",
      "extensions": [".go"],
      "vfs": { "enable": true, "keep_comments": false },
      "formatter": { "command": "gofmt", "args": [] }
    }
  ]
}
```

See the [settings.json reference](/docs#settings-json) for the full `languages` schema.

---

# Attaching images & files

> Section: Guides · vix docs · https://getvix.dev/docs#attaching-images

# Attaching Images & Files

Vix supports vision inputs and file attachments — you can send images, text files, and PDFs to the LLM alongside your messages. Useful for screenshots of bugs, UI mockups, error dialogs, architecture diagrams, log excerpts, source files, or reports.

## Supported formats

-   **Images** — PNG, JPEG, GIF, WEBP, BMP. Maximum 20MB per image. Sent to the model as vision input.
-   **Text** — common docs/data and source-code formats (`.txt`, `.md`, `.csv`, `.json`, `.yaml`, `.log`, `.go`, `.py`, `.ts`, and more). Embedded as UTF-8 text.
-   **PDF** — `.pdf`. Converted to text by vix's built-in PDF reader (headings, paragraphs, best-effort tables); no external tools required.

Text and PDF files are capped at 10MB by default. Change the limit with `attachments.max_text_bytes` in `settings.json`.

## Drag and drop

Drag a file from Finder into the terminal, or type its path. Vix detects file paths in your message and attaches them automatically:

```
> what's wrong with this layout? /Users/me/Desktop/screenshot.png
> summarize /Users/me/docs/report.pdf
```

The path is replaced with a placeholder in the chat display — `[Image #1]`, `[PDF #1]`, or `[File #1]`. Images are base64-encoded and sent as vision input; text and PDF files are read by the daemon and their extracted text is embedded into the message.

You can attach a file from _anywhere on disk_ — including paths outside your project, such as iCloud Drive documents under `~/Library/Mobile Documents/…`. Dragging a file is explicit intent, so attachment access isn't limited to the working directory the way the agent's own file tools are (deny-listed paths such as `~/.ssh` stay blocked). You can also attach while a thread is still connecting — the chip appears right away and the file is validated when you send.

## Attachment panel

1.  Paste or type a file path — it appears in the attachment panel, labelled by type
2.  Press `Tab` to focus the panel
3.  Navigate with `↑` / `↓`
4.  Press `Delete` or `Backspace` to remove
5.  Press `Esc` to return to input

## Multiple attachments

```
> before and after screenshots: /tmp/before.png /tmp/after.png — what changed?
> compare /tmp/spec.pdf against /tmp/notes.md
```

Each is attached and numbered per type, e.g. `[Image #1] [Image #2]` or `[PDF #1] [File #1]`.

## Practical uses

```
> the button is misaligned on mobile — see /tmp/bug.png — fix the CSS
> implement this component based on the mockup /tmp/mockup.png
> what are the action items in /tmp/meeting-notes.pdf?
> here's a failing test log /tmp/run.log — what's going wrong?
```

## Notes

-   Attachments are not persisted — they are sent inline and part of the LLM context for that turn
-   Very large files are rejected with an error. Compress, crop, or trim if needed
-   If a file can't be attached (too large, unreadable, or a password-protected/scanned PDF), vix surfaces the reason in a popup you dismiss with any key press
-   Password-protected or scanned/image-only PDFs (no text layer) can't be read — vix reports this and skips them; OCR is not performed. Permissions-only encrypted PDFs (those that open in a viewer without a password) are decrypted automatically
-   Vision quality depends on the model. Claude Sonnet and Opus handle images well

---

# CLI flags

> Section: Reference · vix docs · https://getvix.dev/docs#cli-flags

# CLI Flags

Complete reference for all command-line flags and environment variables.

## vix

The TUI client and headless runner.

shell

```
vix [flags]
```

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| \--version | bool | false | Print version and exit. |
| \--force-init | bool | false | Delete .vix/context/ and re-run full brain init before starting. |
| \-p <prompt> | string | — | Run a single prompt headlessly and exit. Use -p - to read from stdin. |
| \-w <name> | string | — | Workflow name to trigger. Must be used with -p. |
| \--output-format <fmt> | string | text | Headless output format: text, json, stream-json. |
| \--workdir <path> | string | $CWD | Set the project working directory for this thread. |
| \--config-dir <path> | string | — | Use this dir as the sole .vix config root (ignores ~/.vix and ./.vix). |
| \--disable-automatic-write-permission | bool | false | Require user confirmation before each write\_file / edit\_file / delete\_file. By default, writes execute without confirmation. |
| \--disable-automatic-directory-access | bool | false | Restrict tool calls to paths within the working directory. By default, other paths are reachable (with a prompt). |
| \--socket-path <path> | string | /tmp/vixd.sock | Unix socket for the vix↔vixd connection. Must match the running vixd. Env: VIX\_SOCKET\_PATH. |
| \--auth-token-path <path> | string | — | File holding the shared-secret token used to authenticate every socket message. |
| \--pprof-port <n> | int | 0 | Port for the pprof HTTP server. 0 disables. Env: VIX\_PPROF\_PORT. |
| \--vfs <cmd> … | — | — | Run a one-off VFS command, e.g. vix --vfs read\_file <path>. |
| \--test | bool | false | Fill chat with fake data for UI testing. Does not connect to daemon. |

## vix daemon

Subcommand group managing the long-lived daemon. vix never starts vixd implicitly — these are the only ways.

shell

```
vix daemon <start|stop|status|install|uninstall> [flags]
```

| Subcommand | Description |
| --- | --- |
| start | Launch vixd detached. No-op if already running. Accepts --log-dir for the daemon's log files. |
| stop | Coordinated shutdown: every attached vix instance quits, then vixd exits. Works across mismatched versions. |
| status | Report whether vixd is running and its version. Exit code 1 when not running. |
| install | Register vixd to start at login (macOS LaunchAgent / Linux systemd user unit). Prints what it will write and asks first. |
| uninstall | Remove the login registration. A running daemon is untouched. |

All subcommands accept `--socket-path` and `--auth-token-path` to target a non-default daemon.

## vix job / vix hook

Fire a scheduled job or a lifecycle hook immediately by id, out of band from its schedule or event. Both print the run's thread id and accept `--socket-path` / `--auth-token-path`.

shell

```
vix job run <id>        # run a job now (schedule untouched; runs even if disabled)
vix hook trigger <id>   # fire a hook now (fire-and-forget; runs even if disabled)
```

## vixd

The background server. Each flag has an env-var equivalent (preferred inside sandboxes, since flag values are visible in process listings).

shell

```
vixd [flags]
```

| Flag | Default | Env var | Description |
| --- | --- | --- | --- |
| \--socket-path | /tmp/vixd.sock | VIX\_SOCKET\_PATH | Unix socket the daemon listens on. |
| \--log-dir | $TMPDIR | VIX\_LOG\_DIR | Directory for daemon log files. |
| \--web-port | 1337 | VIX\_WEB\_PORT | Port for the local web UI. 0 disables it. |
| \--no-mission-control | false | VIX\_NO\_MISSION\_CONTROL | Disable the web UI server entirely. |
| \--auth-token-path | — | VIX\_AUTH\_TOKEN\_PATH | File holding the shared-secret socket auth token. |
| \--pprof-port | 0 | VIX\_PPROF\_PORT | Port for the pprof HTTP server. 0 disables it. |

## Credential & tool environment variables

There is no `VIX_MODEL` variable — the model is resolved per thread from the active agent's `model:` frontmatter and the Models tab (F3).

| Variable | Description |
| --- | --- |
| ANTHROPIC\_API\_KEY | Anthropic key (also resolvable via keychain / .env). |
| CLAUDE\_CODE\_OAUTH\_TOKEN | Anthropic OAuth bearer token alternative. |
| OPENAI\_API\_KEY | OpenAI key. |
| OPENROUTER\_API\_KEY | OpenRouter key. |
| MINIMAX\_API\_KEY | MiniMax key. |
| MIMO\_API\_KEY | Xiaomi MiMo key. |
| BRAVE\_SEARCH\_API\_KEY | Required for the web\_search tool. |

## Output formats

### text

The agent's final response is printed to stdout. Tool calls are printed to stderr:

json

```
[tool] read_file: internal/auth/jwt.go
[tool] bash: $ go test ./...
[tool error] bash: exit code 1
```

### json

A single JSON object printed after completion:

json

```
{
  "type": "result",
  "result": "...",
  "thread_id": "a3f2c891",
  "is_error": false,
  "num_turns": 4,
  "duration_ms": 12341,
  "usage": {
    "input_tokens": 8201,
    "output_tokens": 743,
    "cache_creation_tokens": 6100,
    "cache_read_tokens": 0
  }
}
```

### stream-json

Every daemon event emitted as a newline-delimited JSON object, followed by the final result object.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Error (invalid flags, daemon unreachable, API failure) |
| 130 | Interrupted by SIGINT or SIGTERM |

---

# settings.json

> Section: Reference · vix docs · https://getvix.dev/docs#settings-json

# settings.json Reference

`settings.json` is the main configuration file. It is read from two locations, merged in order:

1.  `~/.vix/settings.json` — user-global (lower precedence)
2.  `.vix/settings.json` — project-local (overrides global)

Pass `--config-dir <path>` to use a single directory as the sole config root (neither `~/.vix` nor `./.vix` is consulted) — useful for sandboxed or reproducible threads.

`version: 1` is required. Files with missing or mismatched version are ignored.

## Full schema

json

```
{
  "version": 1,
  "agent": "general",
  "theme": {
    "primary": "#BC63FC",
    "secondary": "#A3FC63"
  },
  "features": {
    "tool_orchestrator": false,
    "read_claude_md": true,
    "read_agents_md": true,
    "show_thinking": false,
    "telemetry": true,
    "jobs": true
  },
  "jobs": { "max_concurrent_runs": 2 },
  "logs": { "retention_days": 10 },
  "tools": {
    "grep": { "backend": "rg" },
    "glob": { "backend": "fd" }
  },
  "languages": [
    {
      "name": "go",
      "extensions": [".go"],
      "lsp": { "command": "gopls", "args": [] }
    }
  ],
  "compaction": { "threshold": 0.8, "auto": true, "keep_last_n_turns": -1 },
  "mcp_servers": [ ... ],
  "workflows": [ ... ]
}
```

## version

**Required.** Must be `1`. If absent or wrong, the entire file is skipped.

## agent

```
"agent": "general"
```

The agent to use for chat threads. Must match a file in `.vix/agents/` or `~/.vix/agents/`.

| Name | Description |
| --- | --- |
| general | Default interactive chat agent. File, search, LSP, web, sub-agent, and todo tools. 100 max turns. |
| explore | Exploration-phase agent: investigates the codebase without producing a plan. 25 max turns. |
| plan | Planning agent used by the built-in Plan workflow. 100 max turns. |
| implementer | Sole builder in the implement → review loop. 100 max turns. |
| reviewer | Read-only reviewer (no write tools). 40 max turns. |
| solver | Benchmark/terminal-bench solver. effort: high. 80 max turns. |

## theme

```
"theme": {
  "primary": "#BC63FC",
  "secondary": "#A3FC63"
}
```

Controls two brand colors used throughout the TUI: borders, highlights, workflow graph, active indicators.

| Field | Default | Used for |
| --- | --- | --- |
| primary | #BC63FC | Primary accent (borders, active elements) |
| secondary | #A3FC63 | Secondary accent (workflow graph, cost display) |

## features

```
"features": {
  "tool_orchestrator": false,
  "read_claude_md": true,
  "read_agents_md": true,
  "show_thinking": false,
  "telemetry": true,
  "jobs": true
}
```

| Flag | Default | Description |
| --- | --- | --- |
| tool\_orchestrator | false | Replaces tool set with only tool\_orchestrator, ask\_question\_to\_user, spawn\_agent, and task\_output. |
| read\_claude\_md | true | Reads CLAUDE.md and injects as system-reminder blocks. |
| read\_agents\_md | true | Reads AGENTS.md and injects as a system-reminder block. |
| show\_thinking | false | Shows the model's reasoning/thinking output in the TUI. |
| telemetry | true | Anonymous usage telemetry (opt-out): a handful of lifecycle/activity events and crash reports — never prompts, code, or file paths. Set false to disable (or export `VIX_TELEMETRY=off`). See [Telemetry & privacy](/docs#telemetry-privacy). |
| jobs | true | The scheduled-jobs engine in vixd. Set false (or export VIX\_DISABLE\_JOBS=1) to disable the scheduler entirely. |

## jobs

```
"jobs": { "max_concurrent_runs": 2 }
```

Scheduler tuning (user-global only). `max_concurrent_runs` bounds how many job runs execute in parallel (default 2). Job definitions themselves are not in settings.json — they are individual files under `~/.vix/jobs/`; see the [Job spec reference](/docs#job-spec).

## logs

```
"logs": { "retention_days": 10 }
```

Retention for the append-only job/hook run logs under `~/.vix/logs/{jobs,hooks}/<date>.jsonl`. `retention_days` (default 10) is how many days of daily log files the daemon keeps; it sweeps older files at startup and once a day. Set it to `0` (or a negative number) to keep logs forever. See the [jobs](/docs#guide-jobs) and [hooks](/docs#guide-hooks) guides for the line format.

## tools

```
"tools": {
  "grep": { "backend": "rg" },
  "glob": { "backend": "fd" }
}
```

Selects the backend binary for grep and glob\_files tools.

| Tool | Value | Binary | Notes |
| --- | --- | --- | --- |
| grep | "" | system grep | Always available |
| grep | "rg" | ripgrep | Faster. Falls back if not in PATH. |
| glob | "" | built-in Go glob | Always available |
| glob | "fd" | fd | Faster. Falls back if not in PATH. |

## languages

Language configuration is a top-level `languages` array (there is no top-level `lsp` key). Each entry maps a language `name` and file `extensions` to optional subsystems.

```
"languages": [
  {
    "name": "go",
    "extensions": [".go"],
    "lsp": { "command": "gopls", "args": [] },
    "formatter": { "command": "gofmt", "args": [] },
    "vfs": { "enable": true, "keep_comments": false }
  }
]
```

-   `lsp` — LSP server `command` + `args`. Starts lazily on first `lsp_query`.
-   `formatter` — command the VFS runs to restore a minified write to valid source.
-   `vfs` — per-language virtual-filesystem settings (`enable`, `keep_comments`).

Entries are merged across the home and project files by `name` (project wins). Languages registered by default:

| name | Extensions |
| --- | --- |
| go | .go |
| python | .py |
| javascript | .js, .jsx |
| typescript | .ts, .tsx |
| rust | .rs |
| ruby | .rb |
| java | .java |
| kotlin | .kt, .kts |
| swift | .swift |
| c | .c, .h |
| cpp | .cpp, .hpp |
| csharp | .cs |
| php | .php |
| lua | .lua |
| shell | .sh, .bash, .zsh |
| yaml | .yml, .yaml |
| json | .json |
| toml | .toml |
| markdown | .md |
| html | .html |
| css | .css |
| scss | .scss |
| sql | .sql |

None of the default entries ship an `lsp` block — add one per language to enable `lsp_query` for it.

## workflows

An array of workflow definitions. See the Workflow Schema Reference for the complete field reference.

```
"workflows": [
  {
    "name": "My Workflow",
    "entry_point": { "id": "first-step" },
    "steps": { ... }
  }
]
```

## compaction

Tunes automatic context compaction (`threshold`, `auto`, `keep_last_n_turns`). See [Context compaction](/docs#compaction) for the full reference.

```
"compaction": { "threshold": 0.8, "auto": true, "keep_last_n_turns": -1 }
```

## mcp\_servers

An array of Model Context Protocol servers to connect at thread start. See [MCP Servers](/docs#mcp) for the field reference.

## tool\_timeouts

Overrides the default and maximum per-call timeouts (in seconds) for `bash` and `glob_files`. Defaults: `default_sec` 120, `max_sec` 600. Both must be > 0 and `default_sec` ≤ `max_sec`, otherwise the block is ignored.

```
"tool_timeouts": { "default_sec": 120, "max_sec": 600 }
```

## allowed\_directories & deny\_list

Widen or restrict which paths and URLs the agent may touch. See [Security & access control](/docs#security-access) for full semantics.

```
"allowed_directories": ["../sibling-repo"],
"deny_list": {
  "paths": ["./secrets", "~/.ssh"],
  "urls":  ["bad.example.com"]
}
```

## elevenlabs

Configures the voice agent used by the experimental whiteboard walkthrough (`agent_id`, `auth_mode`). See [Web UI](/docs#web-ui).

## Minimal project config

json

```
{
  "version": 1
}
```

This inherits everything from `~/.vix/settings.json`.

## Recommended .gitignore

```
.vix/context/
.vix/access_stats.db
.vix/logs/
.vix/plans/
```

Keep: `settings.json`, `agents/`, `skills/`, `prompts/`.

---

# Agent frontmatter

> Section: Reference · vix docs · https://getvix.dev/docs#agent-frontmatter

# Agent Frontmatter Reference

Agent files are Markdown files with YAML frontmatter. They live in `.vix/agents/` (project-local) or `~/.vix/agents/` (user-global).

## File format

yaml

```
---
name: my-agent
description: Does a specific thing well
model: anthropic/claude-opus-4-8
tools: read_file, grep, glob_files, lsp_query
max_turns: 10
---

Your system prompt goes here.

Supports $(variable) substitution and $(file:path) inclusion.
```

## Frontmatter fields

| Field | Default | Description |
| --- | --- | --- |
| name | filename | Agent identifier. Used in spawn\_agent, workflow steps, and settings.json. |
| description | empty | One-line description. Shown in spawn\_agent listing and TUI. |
| model | thread model | Override the LLM model. Use a provider-prefixed spec, e.g. `anthropic/claude-opus-4-8` or `openai/gpt-5.2`. |
| tools | all tools | Comma-separated whitelist. The LLM only sees listed tools. |
| effort | — | Effort level hint passed to the model (e.g. low, medium, high). |
| max\_turns | 20 | Maximum LLM turns before the agent stops and returns its last output. |
| max\_tokens | — | Maximum tokens for each LLM response. |

## Available tools

| Tool | Category |
| --- | --- |
| read\_file | File I/O |
| read\_minified\_file | File I/O |
| write\_file | File I/O |
| write\_minified\_file | File I/O |
| edit\_file | File I/O |
| edit\_minified\_file | File I/O |
| delete\_file | File I/O |
| bash | Execution |
| grep | Search |
| glob\_files | Search |
| lsp\_query | Code intelligence |
| web\_fetch | Web |
| web\_search | Web |
| spawn\_agent | Orchestration |
| task\_output | Orchestration |
| ask\_question\_to\_user | Interaction |
| todo\_write | Planning |
| todo\_read | Planning |
| tool\_orchestrator | Orchestration |

## Template tokens

The system prompt body supports these template tokens, resolved up to 3 passes:

| Token | Replaced with |
| --- | --- |
| $(working\_directory) | Absolute path to the project root |
| $(platform) | OS name (darwin, linux) |
| $(shell) | Shell name (zsh, bash, sh) |
| $(model) | Active model name for this agent |
| $(os\_version) | Kernel version string |
| $(is\_git\_repo) | "Yes" or "No" |
| $(file:path) | Contents of a file from .vix/ or ~/.vix/ |
| $(call:name) | Output of a registered Go function (internal) |

## File inclusion

yaml

```
---
name: analyst
---

$(file:context/project-summary.md)

$(file:prompts/analysis-guidelines.md)

Focus on performance and correctness.
```

Files are searched in `.vix/` first, then `~/.vix/`. If not found, the token is replaced with an error message.

## Built-in agents

### general

Default interactive chat agent. File, search, LSP, web-fetch, sub-agent, and todo tools. 100 max turns.

### explore

Exploration-phase agent: investigates the codebase to build understanding without producing a plan. 25 max turns.

### plan

Planning agent used by the built-in Plan workflow to explore and draft an implementation plan. 100 max turns.

### implementer

Sole builder in the implement → review loop; produces the implementation, refines on reviewer feedback. 100 max turns.

### reviewer

Read-only reviewer (no write/edit/delete tools); judges whether the implementer completed the task. 40 max turns.

### solver

Benchmark/terminal-bench solver. effort: high, max\_tokens 40000, 80 max turns.

## Precedence

When both `~/.vix/agents/reviewer.md` and `.vix/agents/reviewer.md` exist, the **project-local file wins**.

---

# Workflow schema

> Section: Reference · vix docs · https://getvix.dev/docs#workflow-schema

# Workflow Schema Reference

Complete field reference for workflow definitions in `settings.json`.

## WorkflowDef

json

```
{
  "name": "string",
  "entry_point": { StepRef },
  "summary": "string",
  "display_in_tui": true,
  "steps": {
    "<step-id>": { WorkflowStepDef },
    ...
  }
}
```

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| name | string | ✅ | Display name. Must be unique. |
| entry\_point | StepRef | ✅ | First step to run. The id must reference a step in steps. |
| steps | object | ✅ | Map of step ID → WorkflowStepDef. At least one step required. |
| summary | string | — | Template string rendered on completion. Supports $(step.id) tokens. |
| display\_in\_tui | boolean | — | Default true. When false, the workflow is hidden from the Shift+Tab switcher and slash menu — it stays runnable by name (e.g. by scheduled jobs). Used by internal workflows like the shipped heartbeat. |

## WorkflowStepDef

The `type` field determines which other fields are valid: `agent`, `tool`, or `bash`.

### Common fields (all types)

| Field | Type | Description |
| --- | --- | --- |
| type | string ✅ | "agent", "tool", or "bash" |
| explanation | string | Human-readable label shown in the workflow graph panel. |
| input\_params | object | Declares expected input params. Available as $(key) in prompts. |
| next\_steps | StepRef\[\] | Steps to execute after completion. Empty = workflow ends. |
| output | string | File path to write the step's text output. |
| stream | boolean | Stream LLM output to TUI. Default: true. |

### Agent step

json

```
{
  "type": "agent",
  "agent": "string",
  "prompt": "string",
  "deny_tools": ["string"],
  "json_output": false,
  "display_key": "string"
}
```

| Field | Description |
| --- | --- |
| agent | Agent name (mutually exclusive with fork\_from). |
| fork\_from | Step ID whose agent to clone with full conversation history. |
| prompt | Message sent to the agent. Supports all template tokens. |
| deny\_tools | Tool names blocked from this step. |
| json\_output | Parse response as JSON. Keys become $(step.id.key) variables. |
| display\_key | JSON key used as display text in workflow graph panel. |

### Tool step

json

```
{
  "type": "tool",
  "tool": "ask_question_to_user",
  "question": "string",
  "category": "string",
  "options": [ StepOption, ... ]
}
```

### Bash step

json

```
{
  "type": "bash",
  "command": "string",
  "input": "string"
}
```

Runs via `bash -c`. Non-zero exit code fails the step and halts the workflow.

## StepRef

json

```
{
  "id": "step-id",
  "params": { "key": "value with $(tokens)" },
  "execute_if": "bash expression"
}
```

| Field | Description |
| --- | --- |
| id | Target step ID, or "stop" to end the workflow. |
| params | Key-value pairs passed to target step. Available as $(key). |
| execute\_if | Bash expression. Exit code 0 = step runs. |

**Routing:** If multiple next\_steps pass execute\_if, they run in **parallel**. If none pass, workflow ends. `"stop"` causes a clean end.

## StepOption

json

```
{
  "title": "string",
  "description": "string",
  "steps": [ StepRef, ... ],
  "has_user_input": false
}
```

## Template tokens

| Token | Value |
| --- | --- |
| $(workflow.prompt) | Initial message that triggered the workflow |
| $(workflow.dir) | Job's own directory (~/.vix/jobs/<id>) for scheduled runs; empty otherwise. Persist run-to-run state here (e.g. a memory file). |
| $(step.<id>) | Full text output of a completed step |
| $(step.<id>.<key>) | JSON key from a json\_output step |
| $(user\_text) | Free text from has\_user\_input option |
| $(working\_directory) | Absolute project root path |
| $(platform) | OS name (darwin, linux) |
| $(bash:cmd) | Run cmd, replace with stdout |
| $(file:path) | File contents from .vix/ search path |

## Validation rules

-   `name` must not be empty
-   `steps` must not be empty, no empty step IDs
-   `entry_point.id` must reference a step in steps
-   All next\_steps IDs must reference a step or be `"stop"`
-   `type: "agent"` requires either agent or fork\_from (not both), plus prompt
-   `type: "tool"` requires tool field
-   `type: "bash"` requires command, cannot have agent/fork\_from/prompt

---

# Job spec

> Section: Reference · vix docs · https://getvix.dev/docs#job-spec

# Job Spec Reference

Complete field reference for scheduled jobs. For the guided introduction, see [Scheduled jobs](/docs#guide-jobs).

## Files

| Path | Purpose |
| --- | --- |
| ~/.vix/jobs/<id>/job.json | One spec per job, in its own directory. User-authored, hot-reloaded on save. |
| ~/.vix/jobs/<id>/state.json | Machine-written runtime state (next/last run, statuses, error counters, and a `recent_runs` history of the last 10 runs), one per job alongside its spec. Never edit by hand. |
| ~/.vix/jobs/heartbeat/heartbeat.md | The whiteboard read by the shipped heartbeat job. |

Specs and state are split on purpose: the scheduler never rewrites your spec files, so they stay clean for hand-editing and version control.

## Spec fields

json

```
{
  "id": "string",
  "name": "string",
  "enabled": true,
  "trigger": { ... },
  "prompt": "string",
  "workflow_id": "string",
  "workflow": { "name": "...", "entry_point": { "id": "..." }, "steps": { } },
  "cwd": "/absolute/path",
  "permissions": { "auto_write": true, "auto_dirs": true },
  "skip_if_empty": false,
  "timeout": "10m",
  "created_by": "user"
}
```

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| id | string | — | Unique job id. Defaults to the directory name. |
| name | string | — | Display name used in notifications and the threads list. |
| enabled | boolean | ✅ | Disabled jobs are loaded but never fire. Toggle it from the Jobs & Triggers tab (F4) with space, or edit this field directly. |
| trigger | object | ✅ | When to fire — see Trigger below. |
| prompt | string | ✅ | What the run does. Literal text, $(file:path), or a mix; file references resolve against cwd at fire time. |
| workflow\_id | string | — | Name of a workflow in config/workflow.json; the resolved prompt becomes $(workflow.prompt). Mutually exclusive with workflow. |
| workflow | object | — | Inline workflow definition (same schema as a config/workflow.json entry), embedded in the job — no separate file. Mutually exclusive with workflow\_id. Both absent = plain chat turn with the general agent. |
| cwd | string | ✅ | Absolute path of the project the run works in. |
| permissions | object | — | auto\_write / auto\_dirs, both default true. When false, denied operations are recorded in the run result instead of prompting. |
| skip\_if\_empty | boolean | — | Skip (zero tokens, no thread) when the resolved prompt is effectively empty — only blank lines, headings, HTML comments — or its file is missing. |
| timeout | duration | — | Wall-clock cap per run (Go syntax: "90s", "10m"). Default 10m. |
| created\_by | string | — | "user", "vix" (shipped jobs), or "agent:<thread-id>" when the agent created it. |

## Trigger

json

```
{ "type": "cron", "expr": "*/30 9-19 * * *", "tz": "Europe/Paris" }
{ "type": "cron", "expr": "@every 2h" }
{ "type": "at",   "time": "2026-03-01T09:00:00Z" }
```

| Field | Description |
| --- | --- |
| type | "cron" (recurring) or "at" (one-shot). Required. |
| expr | cron only. Standard 5-field expressions (minute hour day month weekday) or descriptors: @every <duration>, @hourly, @daily, @weekly. |
| tz | cron only. IANA timezone for the expression (default: host timezone). Not valid with @-descriptors. |
| time | at only. RFC3339 timestamp. After the attempt the job is marked completed and never re-fires (the file stays for inspection). |

There is no separate "interval" or "active hours" field: hour windows belong in the cron hour field (`*/30 9-19 * * *`). One known limitation: an odd interval confined to an hour window ("every 90 minutes, 9–19 only") cannot be expressed in a single job — use `*/45 9-19 * * *` or two jobs.

## state.json

Each job's runtime state, written to `~/.vix/jobs/<id>/state.json` next to its spec. Read it to verify a job registered — this is also how the agent's `jobs` skill confirms its own work:

json

```
{
  "next_run_at": "2026-03-02T08:00:00Z",
  "last_run_at": "2026-03-01T08:00:11Z",
  "last_status": "ok",
  "last_error": "",
  "consecutive_errors": 0,
  "last_thread_id": "run-9f3a...",
  "validation_error": ""
}
```

| Field | Description |
| --- | --- |
| next\_run\_at | Next scheduled fire time. Empty for disabled/completed jobs. |
| last\_status | ok | error | skipped | timeout. |
| last\_error | Failure detail, or notes like denied operations on otherwise-ok runs. |
| consecutive\_errors | Reset on success; at 5 the job is auto-disabled until its spec file is edited. |
| last\_thread\_id | The persisted run thread — find it in the Threads tab. |
| validation\_error | Non-empty when the spec file failed to parse or validate. Fix the file; the watcher re-checks on save. |

## Scheduler behaviour

-   **Hot reload** — the jobs directory is watched; create/edit/delete takes effect within a second. Editing a spec resets its error counters and auto-disable state.
-   **Catch-up** — runs missed while the daemon was down fire once at the next start, capped at 3 (most overdue first); the rest are recorded as skipped.
-   **Retry backoff** — after a failure, the next attempt is pushed to at least 30s, doubling per consecutive failure up to 60m (never earlier than the natural next slot).
-   **Concurrency** — at most 2 runs execute in parallel by default (see settings below); a job never overlaps itself.
-   **Skip rules** — a run that executes no agent step (a gated poll workflow), answers HEARTBEAT\_OK, or trips skip\_if\_empty is recorded as skipped: no thread, no notification, no tokens.
-   **Unattended interaction policy** — confirmation prompts are auto-denied and recorded; questions take the first option; plans are auto-approved.

## Settings & kill switch

json

```
{
  "features": { "jobs": true },
  "jobs": { "max_concurrent_runs": 2 }
}
```

Both live in `~/.vix/settings.json`. Setting `features.jobs` to false — or exporting `VIX_DISABLE_JOBS=1` — disables the scheduler entirely.

---

# Hook spec

> Section: Reference · vix docs · https://getvix.dev/docs#hook-spec

# Hook Spec Reference

Complete field reference for lifecycle hooks. For the guided introduction, see [Lifecycle hooks](/docs#guide-hooks).

## Files

One spec per hook in `~/.vix/hooks/<id>/hook.json` (or a project's `.vix/hooks/`). User-authored and hot-reloaded on save. Any helper script the hook runs (e.g. `script.sh`) lives in the same directory. Each fire's outcome is recorded in a machine-written `~/.vix/hooks/<id>/state.json` (a sibling of the spec): last-fire summary plus a `recent_runs` history of the last 10 fires (status, event, async flag, duration, and thread id for async workflow/prompt hooks). Never edit it by hand. The full audit trail of every fire stays in the run log under `~/.vix/logs/hooks/<date>.jsonl`.

## Spec fields

json

```
{
  "id": "string",
  "name": "string",
  "enabled": true,
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "mode": "sync",
  "blocking": true,
  "command": "string",
  "workflow_id": "string",
  "workflow": { "name": "...", "entry_point": { "id": "..." }, "steps": { } },
  "prompt": "string",
  "cwd": "/absolute/path",
  "permissions": { "auto_write": true, "auto_dirs": true },
  "timeout": "5s",
  "created_by": "user"
}
```

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| id | string | — | Unique hook id. Defaults to the directory name. |
| enabled | boolean | ✅ | Disabled hooks are loaded but never fire. Toggle it from the Jobs & Triggers tab (F4) with space, or edit this field directly. |
| trigger.event | string | ✅ | One of PreToolUse, PostToolUse, UserPromptSubmit, PermissionRequest, ThreadStart, Stop, PreCompact, PostCompact, SubagentStart, SubagentStop. |
| trigger.matcher | regex | — | Anchored regex over the event's field (tool name; source for ThreadStart; trigger for compaction; agent type for subagent events). "" or "\*" matches all. |
| mode | string | — | "sync" (runs inline, returns a decision) or "async" (fire-and-forget, default). |
| blocking | boolean | — | Sync only. Lets a deny/modify take effect. Valid only on PreToolUse, UserPromptSubmit, and PermissionRequest. |
| command | string | one of | Shell command (run via bash -c). Receives the context envelope on stdin. |
| workflow\_id | string | one of | Name of a workflow in config/workflow.json to run in an isolated thread (a single bash step works for a fast veto). |
| workflow | object | one of | Inline workflow definition (same schema as a config/workflow.json entry), run in an isolated thread. Mutually exclusive with workflow\_id. |
| prompt | string | one of | Plain prompt evaluated by an LLM in an isolated thread. |
| cwd | string | — | Working directory. Defaults to the triggering thread's cwd. |
| timeout | duration | — | Per-run cap (Go syntax). Defaults: 5s sync, 10m async. Timeout fails open. |

Exactly one action is required: command, a workflow (workflow\_id or inline workflow), or prompt. workflow\_id and workflow are mutually exclusive.

## Events

| Event | Fires | Matcher | Can block |
| --- | --- | --- | --- |
| PreToolUse | before a tool runs | tool name | deny / modify |
| PostToolUse | after a tool completes | tool name | context only |
| UserPromptSubmit | before a prompt enters the turn | — | deny / modify |
| PermissionRequest | before the user confirms a tool | tool name | deny |
| ThreadStart | when a thread begins | source | no |
| Stop | when a turn finishes | — | no |
| PreCompact | before the conversation is compacted | trigger | no |
| PostCompact | after a successful compaction | trigger | no |
| SubagentStart | when a subagent is spawned | agent type | no |
| SubagentStop | when a subagent finishes | agent type | no |

## Decision contract

-   **command** — exit 0 = allow; exit 0 + text = context; exit 0 + JSON = explicit decision; exit 2 = deny (reason from stderr); other = fail-open.
-   **workflow / prompt** — final text: a JSON decision, or a `BLOCK: reason` line to deny, else context.
-   JSON shapes: `{"behavior":"deny","reason":"..."}`, `{"behavior":"modify","input":{...}}`, `{"behavior":"context","context":"..."}`.
-   Multiple matches: most restrictive wins (deny > modify > context > allow); contexts concatenate.

## Context envelope

Common fields on every event: `thread_id`, `hook_event_name`, `cwd`, `model`, `permission_mode`, `origin`, `headless`, `thread_mode`, `agent`, `turn_count`, and (for vix-initiated threads) `trigger_type`/`trigger_ref`. Event extras: `tool_name`/`tool_input` (tool events & PermissionRequest), `tool_response`/`is_error` (PostToolUse), `prompt` (UserPromptSubmit), `source` (ThreadStart), `trigger` (PreCompact/PostCompact), `agent_type`/`agent_id` (SubagentStart/SubagentStop).

## Disable

`"features": { "hooks": false }` in settings.json, or `VIX_DISABLE_HOOKS=1`. Hooks never fire inside vix-initiated threads (jobs and hook runs), which is the recursion guard.

---

# Skill schema

> Section: Reference · vix docs · https://getvix.dev/docs#skill-schema

# Skill Schema Reference

Skills are Markdown files with YAML frontmatter stored in a named subdirectory.

## File structure

```
.vix/skills/
└── <skill-name>/
    └── SKILL.md
```

The directory name is used as the default skill name if `name` is not set in frontmatter.

## Frontmatter fields

| Field | Default | Description |
| --- | --- | --- |
| name | directory name | Slash-command name. Invoke with /name in chat. |
| description | empty | One-line description. Shown in /skills listing. |
| model | thread model | Override the LLM model for this skill invocation. |
| allowed-tools | all tools | Comma-separated whitelist of tools available during this skill. |

## Argument substitution

| Token | Replaced with |
| --- | --- |
| $ARGUMENTS | Full argument string exactly as typed |
| $1, $2, $N | Positional arguments (shell-style splitting, quotes respected) |

```
> /compare "old version" "new version"
# $1 → old version, $2 → new version
```

## Dynamic context

```
!`shell command`
```

Executes the command at invocation time and replaces the token with trimmed stdout. Runs in `sh -c` in the project directory.

## Full example

yaml

```
---
name: coverage
description: Analyse test coverage gaps for a package
model: claude-sonnet-4-6
allowed-tools: bash, read_file, glob_files
---

Analyse test coverage for the package at `$1`.

Current coverage report:
!`go test -cover $1 2>&1 | tail -20`

Existing test files:
!`find $1 -name '*_test.go' -type f`

Identify which exported functions and methods lack tests.
```

## Skill precedence

| Location | Scope | Precedence |
| --- | --- | --- |
| ~/.vix/skills/ | All projects | Lower |
| .vix/skills/ | This project | Higher (wins) |

---

# Tool catalog

> Section: Reference · vix docs · https://getvix.dev/docs#tool-catalog

# Tool Catalog

All 19 tools available to agents. Read-only tools are available during plan exploration; write tools only after plan approval or in unrestricted chat mode. The minified variants operate through the virtual file system.

## read\_file

Read a file from disk. PDFs are converted to Markdown automatically.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| path | string | ✅ | File path. Absolute or relative. |
| reason | string | ✅ | Why this file is being read. |
| offset | integer | — | Start line (1-based). |
| limit | integer | — | Max lines to return. |

Output cap: 20,000 chars. Re-reading an unchanged file in the same thread is rejected. When the target is a PDF, vix extracts its text layer and returns Markdown (headings, paragraphs, best-effort tables) instead of raw bytes — no external tools required. Scanned/image-only PDFs (no text layer) and password-protected PDFs are reported as such; OCR is not performed. Permissions-only encrypted PDFs that open without a password are decrypted automatically.

## read\_minified\_file

Read a file through the VFS, minified with Tree-sitter (comments/whitespace stripped).

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| path | string | ✅ | File path. |
| reason | string | ✅ | Why this file is being read. |
| offset | integer | — | Start line (1-based). |
| limit | integer | — | Max lines to read. |

Typically 20–50% fewer tokens than read\_file. Falls back to read\_file when the VFS is disabled for the language.

## write\_file

Write full content to a file.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| path | string | ✅ | File path. |
| content | string | ✅ | Full file content to write. |
| mode | string | — | Octal Unix mode, e.g. "0755". Default 0644 for new files. |

Creates parent dirs automatically. Overwrites without warning. Triggers brain re-index.

## write\_minified\_file

Write a file via the VFS from minified content.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| path | string | ✅ | File path. |
| content | string | ✅ | Full content in minified format. |

A language formatter restores valid source before writing to disk.

## edit\_file

Replace an exact unique string in a file.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| path | string | ✅ | File path. |
| old\_string | string | ✅ | Exact text to find. Must appear exactly once. |
| new\_string | string | ✅ | Replacement text. |
| mode | string | — | Octal Unix mode, e.g. "0755". Default: preserve existing mode. |

Error if old\_string appears zero or more than once. Shows unified diff in TUI.

## edit\_minified\_file

Edit a file via the VFS, matching on the minified representation.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| path | string | ✅ | File path. |
| old\_string | string | ✅ | Exact text to find in the minified content (unique). |
| new\_string | string | ✅ | Replacement text in minified format. |

Match runs on the Tree-sitter-minified source, then is projected back onto the exact byte range in the unminified file and spliced in place — surrounding formatting and comments are preserved; no formatter is run.

## delete\_file

Delete a file from disk.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| path | string | ✅ | File path. Must be a file, not a directory. |

Error if path does not exist or is a directory.

## bash

Run a shell command.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| command | string | ✅ | The shell command to execute. |
| reason | string | ✅ | Why this command is being run. |
| timeout | integer | — | Per-call timeout in seconds. Default 120, hard cap 600. |
| background | boolean | — | Detach and return a job\_id immediately; poll with bash. |

Default 120s timeout (raisable to 600s). Background jobs run detached with their own log/rc files.

## grep

Recursive regex search.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| pattern | string | ✅ | Regex pattern. |
| reason | string | ✅ | Why this search is being performed. |
| path | string | — | Directory or file. Defaults to project root. |
| include | string | — | File glob filter. E.g. "\*.go". |

Uses ripgrep if configured, otherwise system grep. Output capped at 20,000 chars.

## glob\_files

Find paths matching one or more glob patterns.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| pattern | array | ✅ | Array of glob patterns. Supports \*\*. E.g. \["\*\*/\*.go"\]. |
| reason | string | ✅ | Why this search is being performed. |
| path | array | — | Array of base directories. Defaults to cwd. |
| type | string | — | "f", "d", or "any" (default). |
| include\_hidden | boolean | — | Include dotfiles. Default true. |
| max\_results | integer | — | Result cap. Default 1000. |

Returns up to max\_results (default 1000) sorted, deduplicated paths. Uses fd if configured.

## lsp\_query

Query LSP servers for code intelligence.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| operation | string | ✅ | go\_to\_definition, find\_references, hover, document\_symbols, workspace\_symbols, find\_implementations, diagnostics. |
| reason | string | ✅ | Why this query is being performed. |
| file | string | — | File path. Required for most operations. |
| line | integer | — | Line number (1-based). |
| character | integer | — | Character offset (1-based). |
| query | string | — | Symbol search string (for workspace\_symbols). |

Returns error if no LSP server configured for the language.

## web\_fetch

Fetch a URL and return content as text.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| url | string | ✅ | HTTP or HTTPS URL. |
| selector | string | — | "main", "article", or "body". |

Timeout: 30s. Body cap: 1MB → truncated to 20,000 chars. HTML is cleaned.

## web\_search

Search the web via Brave Search API.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| query | string | ✅ | Search query string. |
| count | integer | — | Results to return (1–20, default 5). |

Requires BRAVE\_SEARCH\_API\_KEY environment variable.

## spawn\_agent

Spawn a sub-agent with its own conversation and tools.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| prompt | string | ✅ | Task for the sub-agent. Must be self-contained. |
| agent\_type | string | — | Agent name from .vix/agents/. |
| background | boolean | — | If true, returns task\_id immediately. |

Foreground returns final output. Background returns { task\_id } for later polling.

## task\_output

Retrieve the result of a background sub-agent.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| task\_id | string | ✅ | Task ID from spawn\_agent with background: true. |

Blocks up to 30s. Returns 'still running' message if not complete.

## ask\_question\_to\_user

Pause the agent and present questions to the user.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| questions | array | ✅ | Array of question objects with id, category, question, options, default\_text. |

Multiple questions shown as tabs. Blocked in headless mode.

## todo\_write

Replace the thread's TODO list to plan and track multi-step work.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| todos | array | ✅ | Full replacement list; items have id, content, status, optional depends\_on. Empty array clears. |

Replace semantics. Validates duplicate ids, dependency cycles, and in-progress items with unmet deps.

## todo\_read

Return the thread's current TODO list.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |

Useful for recovering state after earlier turns were compacted out of context.

## tool\_orchestrator

Execute a Python script that chains multiple tool calls.

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| workflow | string | ✅ | Python script body with tool functions and CWD. |
| description | string | ✅ | Short summary shown in the TUI. |

Timeout: 5 min. Only available when features.tool\_orchestrator: true.

---

# Sandbox & write permissions

> Section: Reference · vix docs · https://getvix.dev/docs#sandbox

# Sandbox & Write Permissions

Vix runs `bash` tool calls inside an OS-level sandbox and can optionally require user confirmation before write operations. This page explains both mechanisms in detail.

## The bash sandbox

Every `bash` tool call is wrapped in a platform sandbox. The sandbox is a **write-protection boundary**: it restricts which paths on disk can be written to, while leaving reads largely unrestricted so that compilers, package managers, git, and other dev tools work normally.

### Detection

At daemon startup, vix probes for the best available sandbox mechanism (checked once, cached):

| Platform | Mechanism | Binary probed |
| --- | --- | --- |
| macOS | Apple Seatbelt (`sandbox-exec`) | /usr/bin/sandbox-exec |
| Linux | bubblewrap (`bwrap`) | bwrap on $PATH |
| Other / not found | None (unsandboxed) | — |

If no sandbox is found, the daemon logs a warning and runs bash commands without any OS-level confinement.

### macOS — Apple Seatbelt

On macOS, bash commands are executed as:

shell

```
sandbox-exec -p <profile> bash -c <command>
```

The profile is generated fresh for each thread with the project working directory (`cwd`) substituted in. It starts from **deny default** (everything denied) and then opens only what is needed:

#### Writable paths

| Path | Reason |
| --- | --- |
| <cwd> (and subpaths) | The project directory — the agent's primary workspace |
| /tmp, /private/tmp | Temporary files needed by build tools |
| /dev | Device files (/dev/null, /dev/tty, etc.) |
| /var, /private/var | Build caches, runtime state |
| $HOME | Tool configs and caches (.gitconfig, .npmrc, node\_modules, etc.) |

#### Read-only paths

| Path | Reason |
| --- | --- |
| /usr, /bin, /sbin | System binaries and libraries |
| /Library, /System, /Applications | macOS frameworks and apps |
| /opt | Homebrew and other package managers |
| /etc, /private/etc | System configuration |
| / (literal) | Root directory listing |

**Process operations** allowed: `process-exec`, `process-fork`, `signal`, `sysctl-read`, `mach-lookup`, IPC shared memory read/write.

**Network** access is fully allowed (`network*`) so that package managers, git remotes, and other network tools work.

Symlinks in `cwd` are resolved to their real path before the profile is generated so the rules match the actual filesystem location.

### Linux — bubblewrap

On Linux, bash commands are executed inside a `bwrap` (bubblewrap) container:

shell

```
bwrap --ro-bind / / --bind <cwd> <cwd> --bind /tmp /tmp \
      --bind /dev /dev --bind /var /var --bind $HOME $HOME \
      --chdir <cwd> bash -c <command>
```

The key difference from macOS is the approach: instead of an allow-list profile, bubblewrap first mounts the **entire filesystem read-only** (`--ro-bind / /`) and then **overmounts specific paths read-write**:

#### Read-write bind mounts

| Path | Reason |
| --- | --- |
| <cwd> | The project directory |
| /tmp | Temporary files |
| /dev | Device files |
| /var | Build caches, TMPDIR |
| $HOME | Tool configs and caches |

Everything else inherited from the `--ro-bind / /` base mount is read-only. The net effect is the same policy as macOS: writes are confined, reads are open.

The network namespace is **not** isolated, so network access works normally.

If `cwd` is a symlink, both the symlink path and the real path are bind-mounted read-write to avoid path resolution mismatches.

### Allowed directories

Both mechanisms support runtime-approved extra directories. When the agent runs a `bash` command (or a file tool) referencing a path outside `cwd`, vix detects this and pauses to ask the user for approval. If the user approves, the directory is added to the thread's **allowed directories** list for the rest of the thread and the sandbox profile is updated accordingly (read-write for bash, accessible for file tools).

Approved directories can optionally be persisted to `.vix/settings.json` so they are pre-approved for future threads.

In headless mode (`-p`), access to paths outside `cwd` is **denied automatically** without prompting.

### What the sandbox does NOT prevent

Because reads are unrestricted, a command like:

shell

```
find / -name "*.env"
cat /etc/passwd
```

will be **allowed** by the sandbox — it only reads the filesystem. The sandbox would block any _write_ to those paths. Keep this in mind: the sandbox protects your filesystem from being modified, not from being read.

## Write confirmation (`--disable-automatic-write-permission`)

Separately from the bash sandbox, vix can apply a **write confirmation gate** to the file tools: `write_file`, `edit_file`, `delete_file` (and their minified variants), plus `bash`.

**By default this gate is off** — write-class tools execute immediately. Pass `--disable-automatic-write-permission` to turn it on. With the gate on, every such call is intercepted before execution and the user is shown a confirmation prompt in the TUI with a preview of what will happen (diff for edits, first lines for writes, command for bash). The tool only executes once the user approves.

shell

```
vix --disable-automatic-write-permission
```

Leaving the gate off (the default) suits:

-   **Headless / CI** pipelines where there is no human in the loop (in fully headless `-p` mode, confirmations are auto-approved regardless of this flag).
-   **Power users** who trust the agent and find the confirmation prompts disruptive.

A related flag, `--disable-automatic-directory-access`, confines tool calls to the working directory instead of allowing (prompted) access to paths outside it.

**Warning:** With the default (gate off) the agent can overwrite and delete files without a human checkpoint. Enable `--disable-automatic-write-permission` when working in directories containing important data outside version control.

### Relationship to the sandbox

The write confirmation and the bash sandbox are independent layers:

| Layer | What it controls |
| --- | --- |
| Write confirmation | Whether the **user** must approve each write-class tool call before it runs |
| OS sandbox | Whether the **OS** allows the bash subprocess to write to a given path |

The write-confirmation gate and the OS sandbox are independent layers: toggling `--disable-automatic-write-permission` has no effect on the OS sandbox — bash commands are still confined to the permitted write paths either way.

---

# Security & access control

> Section: Reference · vix docs · https://getvix.dev/docs#security-access

# Security & Access Control

Beyond the bash sandbox and write confirmation (see [Sandbox & write permissions](/docs#sandbox)), vix gates which paths and URLs the agent can touch, supports an always-on deny list, and can require a shared-secret token on the daemon socket.

## Default access policy

A path is reachable without prompting when it sits under any of:

-   the working directory (`cwd`) — read + write;
-   `$HOME` — read + write;
-   the host's system directories (e.g. macOS `/usr`, `/bin`, `/Library`, `/etc` read-only and `/tmp`, `/var` read-write; the Linux set is analogous);
-   any entry in `allowed_directories`.

Anything outside that set surfaces a confirmation prompt in interactive threads, or an error in headless mode. The `deny_list` always wins, even over an auto-allowed path. Lock down sensitive subpaths of `$HOME` (e.g. `~/.aws`, `~/.ssh`) with the deny list.

## allowed\_directories

An array of extra directories that are pre-approved for the thread. Directories you approve at runtime (when the agent asks for a path outside the default set) can also be persisted here.

json

```
{
  "version": 1,
  "allowed_directories": ["/Users/me/shared", "../sibling-repo"]
}
```

## deny\_list

Paths and URLs that are always off-limits. Use the structured form (a legacy flat array is still parsed as paths-only):

json

```
{
  "version": 1,
  "deny_list": {
    "paths": ["./secrets", "~/.ssh", "/etc/passwd"],
    "urls":  ["bad.example.com", "https://example.org/admin"]
  }
}
```

-   **Paths** — a target is blocked when (after symlink resolution) it equals a deny entry or is a descendant of one. Entries may be absolute, start with `~` (expanded to your home directory), or be relative. A relative entry is matched against both the directory of the config file that declares it and the working directory (project root) — so `".envrc.private"` in `./.vix/settings.json` blocks the file at the repo root, not a phantom `./.vix/.envrc.private`.
-   **URLs with a scheme** (`https://example.com/admin`) — URL-prefix match; scheme/host case-insensitive, path case-sensitive.
-   **URLs without a scheme** (`example.com`) — hostname or dot-aligned suffix match (`api.example.com` matches, `notexample.com` does not).

Coverage: `read_file`/`write_file`/`edit_file`/`delete_file` (and minified variants) are refused before execution; `web_fetch` is refused on a denied URL; `bash` is refused when any path-like or URL token resolves into a denied entry; and `grep`/`glob_files` silently filter denied matches out of their output. Both lists are unioned across the home and project configs.

## Restricting to the working directory

Pass `--disable-automatic-directory-access` to confine tool calls to `cwd` instead of allowing (prompted) access to `$HOME` and system paths.

## Socket authentication

By default any local caller can connect to the daemon socket. For multi-user or shared hosts, point both `vixd` and `vix` at the same secret file with `--auth-token-path` (env `VIX_AUTH_TOKEN_PATH`). Every socket message must then carry the matching token. Keep the file outside the agent's reachable path tree.

## Linux: Landlock

On Linux, file-access confinement can additionally be enforced with the kernel's Landlock LSM, complementing the bubblewrap bash sandbox described in [Sandbox & write permissions](/docs#sandbox).

---

# Telemetry & privacy

> Section: Reference · vix docs · https://getvix.dev/docs#telemetry-privacy

# Telemetry & privacy

Vix sends a small amount of anonymous, opt-out usage telemetry to help us understand which platforms and models are in use and to catch crashes. It is strictly best-effort and fire-and-forget: **telemetry is never on the critical path**. If the tracking endpoint is unreachable — including when you block it at the DNS or firewall level — vix keeps working normally; events are simply dropped.

## What is collected

A handful of lifecycle and activity events, plus crash reports. Every event carries only the build/runtime context below — **no prompts, no code, no file paths, no file contents, no environment variables**.

| Event | When | Properties |
| --- | --- | --- |
| tui\_started | client launches | app mode (tui/headless), app version |
| tui\_ended | client exits | thread duration (seconds) |
| turn\_sent | you submit a prompt/workflow | model name (e.g. `anthropic/claude-…`) |
| daemon\_started | vixd starts | startup duration (ms) |
| $exception | a panic is recovered | recover site + goroutine stack (Go internals, not your data) |

Common properties attached to every event: vix version, OS, architecture, process mode, and a rotating thread id. The distinct identifier is a random device UUID generated once and stored in your OS keychain — it is not tied to your name, email, or account. Coarse location may be derived server-side from the request IP (standard GeoIP); no precise location is sent. Events go to PostHog at `us.i.posthog.com`.

## How to opt out

Any one of the following fully disables telemetry — no events are enqueued at all:

-   **Settings toggle** — flip _Telemetry_ off in the TUI Settings tab.
-   **Config file** — set `"features": { "telemetry": false }` in [settings.json](/docs#settings-json).
-   **Environment variable** — export `VIX_TELEMETRY=off` (also accepts `false` or `0`) before launching vix/vixd.

shell

```
# Disable telemetry for a single run
VIX_TELEMETRY=off vixd
VIX_TELEMETRY=off vix
```

Telemetry is also inert by construction in local/dev builds (the analytics key is embedded only in official release binaries), so a `go build` never phones home.

## Blocking at the network level

You can block `us.i.posthog.com` at your DNS resolver or firewall and vix will keep functioning — telemetry uploads fail quietly in the background and events are dropped. This is **not** the recommended way to opt out, though: use one of the switches above instead. With telemetry left enabled but the endpoint blocked, vix will still make a small, bounded number of upload attempts before giving up on each batch (retries are capped so a blocked host can't turn into a stream of requests hammering your resolver).

---

# Machine-readable docs

> Section: Reference · vix docs · https://getvix.dev/docs#machine-readable-docs

# Machine-readable docs

Every section of this documentation is also published as plain Markdown so that AI agents, scripts, and other tools can read it without executing JavaScript. The files are generated from this site at build time, so they never drift from what you see here.

## Where to find it

-   [/manual/index.md](https://getvix.dev/manual/index.md) — a table of contents linking every section.
-   `/manual/<section-id>.md` — one file per section, e.g. [/manual/tui-basics.md](https://getvix.dev/manual/tui-basics.md). The `section-id` is the same slug used in this page's URL hash.

## The vix-help skill

Vix ships a bundled `vix-help` skill that uses these files to answer questions about vix itself — keybindings, configuration, agents, providers, jobs, skills, workflows, and MCP — fetching the relevant section on demand, with an offline copy bundled for when the network is unavailable.

---

# The daemon protocol (custom clients)

> Section: Reference · vix docs · https://getvix.dev/docs#daemon-protocol

# The daemon protocol (custom clients)

The TUI is just one client of `vixd`. The daemon speaks a small, stable protocol over its Unix socket, so you can build your own client — a native app, an editor plugin, a script — that reuses everything the daemon does (LLM streaming, tool execution, the brain, threads, jobs). The repo ships a native macOS example under `apps/vix-mac`.

## Transport & framing

Connect to the `AF_UNIX` stream socket (`/tmp/vixd.sock` by default). Messages are newline-delimited JSON (NDJSON): one JSON object per line, in each direction. There are two envelopes:

json

```
// client -> daemon
{ "type": "thread.input", "auth_token": "<optional>", "data": { "text": "hi" } }

// daemon -> client
{ "type": "event.stream_chunk", "data": { "text": "Hello" } }
```

## Handshake & version

Open a thread by sending `thread.start` with a `client_version`. The daemon refuses any client whose version does not exactly match its own build. To connect to whatever daemon is running, first call the one-shot `ping` RPC — it returns the daemon's version — then stamp that into `thread.start`. The daemon replies with `event.thread_started` (or `event.error` with code `version_mismatch`).

## The event stream

After starting, the client consumes a stream of events: assistant text deltas (`event.stream_chunk`), extended-thinking deltas, tool calls and results, token accounting (`event.stream_done`), todo updates, and end-of-turn (`event.agent_done`).

## Interactive round-trips

Three events block the turn until the client answers — this is what makes a client interactive rather than a one-shot runner:

-   `event.confirm_request` → reply `thread.confirm` (tool permission)
-   `event.user_question` → reply `thread.user_answer`
-   `event.plan_proposed` → reply `thread.plan_action`

## Schema & codegen

The full message surface is generated from the Go structs into a committed JSON Schema (`internal/protocol/schema/vix-protocol.schema.json`, via `make proto-schema`). The same generator emits Swift models for the macOS app (`make mac-models`). Drift tests fail the build if either committed artifact falls out of sync with the structs, so a client generated from the schema stays in lockstep with the daemon.

This covers not just events and commands but also the RPC projection types returned by `thread.list` / `job.list` / `hook.list` (`ThreadSummary`, `JobSummary`, `HookSummary`) — so a custom client never hand-maintains any part of the wire contract.

---
