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

## Key Conventions

- **Go style** - follow standard Go conventions, use `gofmt`.
- **Error handling** - return errors, don't panic. Log with `log.Printf` in the daemon.
- **UI events** - the daemon emits events via `s.emit("event.name", data)` which the TUI consumes.
- **No over-engineering** - keep changes minimal and focused. Don't add abstractions for one-time operations.
- **Security** - sanitize all user inputs before shell execution. Be careful with tool execution paths.

## Environment

- **Go 1.26+** required
- **ANTHROPIC_API_KEY** environment variable or `.env` file for LLM access
- **OAuth login** (alternative to API keys): `vix login [provider]` runs an OAuth
  flow (Anthropic Claude Pro/Max, GitHub Copilot, or OpenAI Codex/ChatGPT) and
  stores the credentials in the OS keychain under `<provider>-oauth`. `vix logout
  <provider>` removes them. The flows live in `internal/auth/`; credential
  resolution (`internal/config/keyring.go`) consults stored OAuth logins after
  explicit API keys, and the daemon refreshes expired access tokens on demand
  via `config.ResolveProviderCredentialFresh`. Only the Anthropic OAuth token is
  wired into inference today (it reuses the existing Bearer path); Copilot/Codex
  logins are stored and ready but need provider adapters before they can serve
  requests.
- **LSP servers** (optional): gopls, pylsp, typescript-language-server for code intelligence
- **LSP config**: `.vix/settings.json` in project root

## Config directory resolution

By default vix merges config from two layered `.vix` directories: `~/.vix` (user defaults) and `./.vix` (project overrides). This covers `settings.json`, `agents/`, `skills/`, `AGENTS.md`, plus session state like `history.txt`, `plans/`, `access_stats.db`, and `logs/`.

All path resolution flows through `config.VixPaths` (internal/config/paths.go). Add new `.vix`-relative paths there rather than hardcoding `filepath.Join(cwd, ".vix", ...)`.

Pass `--config-dir /some/path` to use that directory as the sole `.vix` root. Neither `~/.vix` nor `./.vix` is consulted, and all session state (history, plans, access stats, LLM logs) is written inside the override directory. The directory is auto-created and bootstrapped with default settings on first run. This is useful for sandboxed/reproducible sessions without touching real user or project config.

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
