# vix end-to-end tests

Containerised, imperative end-to-end tests that drive the **real** `vix` TUI and
`vixd` daemon against a **mock LLM server**, exercise the **Landlock sandbox**,
and produce a self-documenting **HTML report** with screenshots.

This is a **separate Go module** (`e2e/go.mod`). Because Go never descends into
nested modules, `go test ./...` and `make test` at the repo root **never** run
these tests. They run hours-long, real-process scenarios and are meant as a
**pre-release gate**, not part of the unit-test loop.

## Run it

```bash
make test-e2e
```

That target:

1. `script/build.sh` — builds static Linux `vix`/`vixd` (build on host).
2. `e2e/build-e2e.sh` — cross-compiles `e2e/bin/e2e.test` + `e2e/bin/report`.
3. `docker build` — copies those binaries + `freeze` into a minimal image
   (`tmux`, `bash`, `bubblewrap`; no Go toolchain inside).
4. `docker run --network none --security-opt seccomp=e2e/seccomp-landlock.json`
   — runs the suite offline, then renders + zips the report.

### Faster: shard across containers

```bash
make test-e2e-sharded SHARDS=4
```

Fans the suite across `SHARDS` isolated containers in parallel (each gated by
`SHARD_INDEX`/`SHARD_TOTAL`, writing to `e2e/out/shard-<k>/report`), then merges
all shards into one `e2e/out/report` and zips it. Each shard is fully isolated —
sharding trades machine resources for wall-clock time without weakening
isolation.

Output lands in `e2e/out/`:

- `e2e/out/report/index.html` — the report (open in a browser)
- `e2e/out/e2e-report.zip` — the same, packaged with images

## How it works

- **Host isolation:** the container is the boundary. Each test gets its own
  `HOME`, workdir, socket, `vixd`, `vix` (in a tmux session), and mock server;
  nothing touches your machine. `--network none` guarantees no real provider is
  ever contacted (a fake API key + `ANTHROPIC_BASE_URL` point at the in-process
  mock).
- **TUI driver — tmux:** the harness drives the real `vix` TUI inside a
  per-test, isolated **tmux** server/session (`send-keys` for input,
  `capture-pane` for the rendered screen). tmux is a complete, faithful terminal
  (the same one VHS uses), so Bubble Tea v2 renders to it reliably. A hand-rolled
  PTY + emulator was tried first and proved too fragile for Bubble Tea v2's
  renderer — see `debug.md` for the full root-cause investigation.
- **Sandbox under test:** vix selects **Landlock** inside the container (no
  privilege needed). The harness asserts the selected mode is not `none`
  (override with `VIX_E2E_ALLOW_NO_SANDBOX=1`).
- **Determinism:** the image pins locale/`TERM`; the `vix` process runs with
  `VIX_TEST_RENDER=1`, which freezes spinners and elapsed-time readouts so
  screenshots are byte-stable. Scenarios sync on conditions
  (`WaitFor`/`WaitStable`), never `time.Sleep`.
- **Report:** tests never render; each appends immutable, slug-keyed artifacts
  under `out/report/results/` (a `.start.json` marker + a `.json` final). The
  standalone `report` binary renders them idempotently, so a crash never loses
  earlier results, re-runs update in place, and shard outputs can be merged.

## Writing a scenario

Add a `Test*` function to `e2e/scenarios/`. The test *is* the model and the
user:

```go
func TestWriteThenLs(t *testing.T) {
    h := harness.Start(t, harness.Meta{
        Category:    "files",
        Subcategory: "files.write",
        Description: "model writes a file; a real ls feeds the listing back",
        Wire:        harness.WireMessages,
    })

    h.UI.WaitStable(500 * time.Millisecond)
    h.UI.Shot("initial")

    // Script the model's turns. Enqueue = served with no wait (blind);
    // use h.Mock.Next()/Reply() instead when you want to inspect a request.
    h.Mock.Enqueue(
        harness.ToolUse("write_file", `{"path":"hello.txt","content":"hi"}`),
        harness.ToolUse("bash", `{"command":"ls"}`),
        harness.Text("Created hello.txt and listed the directory."),
    )

    h.UI.Type("create hello.txt containing hi, then list the directory")
    h.UI.Enter()
    h.UI.WaitFor("listed the directory")
    h.UI.Shot("after-run")

    // disk · wire · screen
    if string(h.FS.Read("hello.txt")) != "hi" { t.Fatal("file not written") }
    // ... assert h.Mock.Requests() carried the real ls result, screen rendered ...
}
```

Handles: `h.Mock` (act as the model), `h.UI` (keys + screen + `Shot`),
`h.FS` (inspect the workdir), `h.Daemon` (sandbox mode, vixd log).

> Tip: the `write-e2e-test` skill (`.vix/skills/write-e2e-test/SKILL.md`, invoke
> with `/write-e2e-test`) automates this workflow — it reads this README, mirrors
> the closest existing scenario, and runs the suite to verify.

## Local dev (without Docker)

You can iterate on a single scenario with prebuilt binaries on your machine
(requires `tmux` installed locally):

```bash
make build                              # produces bin/vix, bin/vixd (host arch)
cd e2e
VIX_E2E=1 VIX_BIN=../bin/vix VIXD_BIN=../bin/vixd \
  VIX_E2E_REPORT="$PWD/out/report" \
  go test ./scenarios -run TestWriteThenLs -v
go run ./cmd/report render --in out/report   # build the HTML
```

Without `VIX_E2E=1` (and resolvable binaries) every scenario **skips**, so the
suite is safe to keep in the tree. Scenarios also skip if `tmux` isn't found.

## Status

Verified green in the live containerised run (deterministic across repeated
runs):

- `files.write` — write→ls across **disk · wire · screen**.
- `wires.write` — the same write scenario across the wire matrix.
- `sandbox.mode` — a real sandbox backend is enforced for bash.
- `sandbox.deny_list` — a protected file's contents never reach the model.

Wire dialects: Anthropic **Messages** and OpenAI **Responses** run live (routed
at the mock via `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL`); both SSE renderers are
confirmed against the real SDKs. **Chat Completions** is implemented (renderer +
extractor, unit-tested) but its scenario currently **skips**: routing an
`http://` loopback mock through a cloud provider trips vix's providers-config
HTTPS validation. It needs a TLS mock (or a local-provider credential path) to
run live — see `harness.WireOptions`. Bedrock is intentionally out of scope.

The wire codecs (SSE renderers + per-wire request extractors) have unit tests
that run with a plain `go test ./harness` — no container needed.

`harness.EachWire(...)` runs one scenario across all wires (Chat Completions
skips for now).

> The interactive TUI may prompt to confirm some tool calls (e.g. bash outside
> policy). Use `h.UI.ResolveToolPrompts("<final text>")` instead of a bare
> `WaitFor` — it waits for the result while auto-approving any "Permission"
> panel. `h.UI.DenyToolPrompt()` is available for sandbox/denial scenarios. On
> failure, the harness dumps the final screen, vixd log, and (on timeout) the
> harness goroutine stacks alongside the report under `out/report/logs/`.

## Coverage

Scenarios in the suite, by area (subcategory IDs match `harness.Meta`):

- **Harness primitives** — scripted text/tool turns (`Text`, `ToolUse`),
  multi-delta streaming (`TextChunks`), thinking/reasoning blocks (`Thinking`),
  usage override (`WithUsage`), typed provider errors (`HTTPError`), daemon
  restart (`Daemon.Restart`), slash commands (`UI.Slash`), file/env seeding
  (`WithWorkdirFile`, `WithHomeFile`, `{{MOCK_URL}}`, `WithoutDefaultCreds`), and
  local-provider discovery endpoints on the mock.
- **Files & search** — `files.write`, `files.read` (+offset/limit),
  `files.read_minified`, `files.edit` (+non-unique), `files.edit_minified`,
  `files.write_modes`, `files.delete`, `search.grep`, `search.glob`.
- **Sandbox & policy** — `sandbox.mode`, `sandbox.deny_list`,
  `sandbox.deny_paths`, `sandbox.deny_urls`, `sandbox.search_filter`,
  `sandbox.home_subpath`, `sandbox.bash_tokens`.
- **Skills** — `skills.implicit`, `skills.explicit`, `skills.override`.
- **Wires** — `wires.write` (Messages + Responses matrix),
  `wires.streaming_continuation` (#34).
- **Session & context** — `session.persistence` (#22),
  `context.auto_compact` (#19), `context.manual` (`/clear`).
- **Auth & credentials** — `models.oauth_no_keychain` (#53): activating an OAuth
  "Create token" without a usable OS keychain surfaces an error instead of
  freezing the TUI (skips where a keychain is present).
- **Streaming & resilience** — `stream.chunks`, `stream.thinking`,
  `stream.retry` (retryable status matrix), `stream.error` (fail-fast matrix).
- **Validation** — `tools.validation` (#21).

Scenarios for still-open issues or features not yet on `main` ship as
`t.Skip`ped acceptance specs (via `harness.SkipScenario`) and appear as
**skipped** in the report rather than silently missing.
