# Greenline Doctrine

This is the master copy. `greenline setup` installs it into each repo. It is the
philosophical core of the gate: **main is always green and always deployed, one
serialized gate makes it so, and tests and code are co-designed so the gate can
run for real, in parallel, against live prod.**

## The five invariants

1. **`main` == deployed == green, always.** Every commit on `main` has passed the
   full `check` suite and been deployed healthy. There is no "deployed but
   untested" state. `last-green` records the last commit that passed the whole
   gate; `deployed` records the SHA prod runs.

   **The one sanctioned exception: `coalesce_deploys`.** A repo whose deploy
   restarts a service pays a real cost per deploy — in agentd3, 15 deploys in one
   day interrupted ~20% of all agent turns mid-work. With `coalesce_deploys =
   true`, a submission that finds other submissions already queued behind it
   skips only the DEPLOY; it still merges and fast-forwards main, and the last
   member of the burst deploys the accumulated tip. So "merged but not deployed"
   becomes a real, bounded, recorded state: bounded because it lasts only while
   the queue is busy, and recorded because `pending-deploy` names the SHA,
   `status` reports `DEPLOY DEFERRED`, and `doctor` fails if nothing is queued to
   finish the job. Every commit is still individually gated — only the restart is
   shared. Never enable it to paper over slow deploys; enable it only where a
   deploy interrupts live work.
   They agree with `main` or the repo is in drift and `doctor` says so.

2. **The canonical checkout is pristine.** It lives at the repo root (the parent of
   the shared git dir). It is always on `main`, always clean. **No human and no
   agent ever edits it, ever.** Only greenline writes to it — to fast-forward `main`
   during a successful gate, or to reset+redeploy during a rollback. If the
   canonical checkout is dirty, the gate refuses to run and tells the operator to
   resolve it — greenline never silently discards uncommitted work.

3. **All work happens in worktrees branched from last-green.** `greenline worktree
   NAME` creates `gl/NAME` off `last-green` (the known-good tip), in its own
   worktree. Agents work there in isolation. Many worktrees coexist; many agents
   work at once.

4. **Every merge goes through the serialized gate.** One exclusive lock. A submit
   squash-merges the candidate, runs the **real** `check`, fast-forwards `main`,
   runs the **real** `deploy`, and publishes — atomically from the operator's point
   of view. If check fails, `main` is untouched. If deploy fails, prod is rolled
   back to the previous commit and redeployed. No partial states leak out.

5. **Test data never touches prod datastores.** `check` runs against a test
   datastore; `deploy` touches prod. The two never cross. A test that writes to the
   prod store is a bug in the test, not a flaky gate.

## Test/code co-design

The gate runs `check` for real, and `deploy` restarts a **live** service that is
serving other work. So tests are not written in a vacuum — the code they test and
the tests themselves are designed **together** to survive this environment:

- **Tests run in parallel** — with each other, with other agents' `check` runs from
  other worktrees, and with a live prod instance. Nothing may assume it is alone.
- **Namespace every entity a test creates.** Unique IDs/prefixes per run (a uuid
  suffix on names, keys, table rows, temp dirs). Two `check` runs at once must never
  collide.
- **Never assert global state.** No "row count == N", no "the table is empty", no
  "this singleton does/doesn't exist". Other runs and prod are changing the world
  underneath you. Assert only about the entities *this run* created.
- **Never truncate or reset shared tables/stores.** No `TRUNCATE`, no `DROP`, no
  "clean slate" fixtures on anything shared. Create your own namespaced data and
  clean up only that.
- **Tolerate pre-existing and concurrently-changing data.** Reads may return other
  runs' rows; filter to your namespace. Counts drift between two reads; don't depend
  on them.
- **Bind servers to OS-assigned ports (port 0), never fixed ports.** Two `check`
  runs binding `:8080` is an instant, confusing failure. Ask the OS for a port and
  read back what you got.
- **Prefer per-test / per-worktree datastores where cheap.** A `tmp_path` sqlite
  file, a worktree-relative `local/` dir — isolation by construction beats
  careful namespacing.
- **Serialize on scarce shared resources.** A single local LLM inference server or
  one GPU is a serialization point: design those tests to tolerate latency and to
  run one-at-a-time (`pytest -p 1` for that group, or an explicit lock), and mark
  and document them so the constraint is visible.
- **Schema migrations must be backward-compatible one version.** Old code must run
  against the new schema, because a deploy rollback **redeploys the previous code
  against the already-migrated store** — it does not un-migrate. So never rename or
  drop a column the previous release still reads in the same deploy that starts
  writing the new one; do it in two releases.
- **`check` must be runnable concurrently from multiple worktrees.** This is the
  summary constraint that all the above serve. If two agents cannot both run
  `./run check` at the same moment without interfering, the check is broken.

## The five-minute check

**`check` must finish in under five minutes. Over that, the gate kills it and
fails the submission** — same verdict as a red test, and the whole process group
dies with it. This is a hard limit: there is no config knob, and there is no
repo whose suite is "just legitimately slow". A gate nobody can afford to wait
for is a gate that gets bypassed.

The remedy is never raising the limit. It is making the suite fast:

- **Parallelize.** The co-design rules above exist so tests can run at once —
  isolated schemas, namespaces, ports and temp dirs per test mean nothing has to
  be serialized for correctness. Run them wide.
- **Start from a known position.** Prebuilt fixtures, seeded snapshots, a
  restore-from-template database — never rebuild the world per test.
- **Keep the build warm.** The gate worktree is persistent on purpose: leave
  build/dependency caches (`target/`, `.venv`, node_modules, record/replay
  caches) in place so a submission compiles only its own diff.
- **Validate incrementally.** Cheap and decisive first — compile, then lint,
  then unit, then integration. Fail fast before paying for the slow stages.
- **Split monolithic integration tests.** One 4-minute end-to-end test is a
  single-threaded wall; several focused ones run concurrently.
- **Cache the external world** — see the next section.

## Real but fast: record/replay caches for external services

Tests must be **real** — mocking, faking, or stubbing another service is forbidden,
full stop. But real does not have to mean slow. The dominant cost in a real test
suite is usually calls to external services (an LLM inference server, a third-party
API). You cannot fake those services — but you **can assume an identical request
will get the same answer it got last time**, and cache accordingly:

- **Route all access to the external service through one choke point** in the app
  (one client function/module). The layer looks identical to callers.
- **Inside it, a content-addressed cache**: key = hash of a canonical serialization
  of (endpoint kind + model/service + the FULL request — everything semantically
  meaningful, including options like temperature). Store one human-readable JSON
  file per key — `{model, request, response}` — in a gitignored local cache dir
  (e.g. `local/<service>-cache/`), created idempotently on first use.
- **Miss → real call**, then write the entry atomically (temp file + rename).
  **Hit → the stored response, zero network.** A corrupt/partial entry is a miss
  that self-heals on rewrite.
- This is **not a mock**: every cached byte came from the real service. The first
  run of any test is a genuine end-to-end call; every rerun replays the real
  recorded answer in milliseconds.

Consequences: prod is unaffected (real traffic is effectively always novel, and a
cache-layer failure must never break the live call); test suites collapse from
minutes to seconds after their first run; and because the **gate worktree is
persistent**, the gate's cache stays warm across submissions — the serialized merge
gate stays fast. To re-record reality (service upgraded, model changed), delete the
cache dir; the next run repopulates it from live calls.

Caveat: don't cache a call whose *variability* is the thing under test (e.g.
sampling diversity), and always include every request field that changes the answer
in the key. Reference implementation: darrennn's `src/darrennn/endpoint_health.py`.

## The contract

- **`./run check`** — cwd = the worktree being gated. Builds, lints, and runs the
  FULL test suite against a TEST datastore. Its **exit code is the verdict** (0 =
  green). Must be safe to run concurrently from multiple worktrees.

- **`./run deploy`** — cwd = the canonical checkout. Rebuilds and restarts prod
  (e.g. `auto -q restart <svc>`). It **MUST health-check and exit nonzero on
  unhealthy**, and it **MUST be idempotent** — greenline re-runs it during rollback
  and as the default health probe, so running it twice must be safe.

- **`./run health`** *(optional)* — a probe only, no side effects. If absent,
  greenline re-runs `deploy` as the health probe (which is why `deploy` must be
  idempotent).
