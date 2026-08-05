# greenline — vix

This repo uses **greenline**: a local, serialized, gated CI/CD tool. There is one
canonical checkout of `vix`; it is always on `main`, always clean,
and always identical to what prod (`vix`) runs. Every change reaches
`main` through a single quality gate that runs the full test suite and a
real production deploy, one submission at a time.

## The workflow

```
greenline worktree feature-x        # new worktree at /Volumes/Gumby/worktrees/greenline/vix/feature-x
cd /Volumes/Gumby/worktrees/greenline/vix/feature-x         # branch gl/feature-x off last-green
# ...edit, test, commit...
greenline submit                     # gate: squash-merge -> check -> ff main -> deploy -> publish
greenline done                       # remove the worktree + branch once merged
```

Diagnostics (no lock needed):

```
greenline status     # lock holder, SHA drift (main/origin/last-green/deployed), recent journal
greenline doctor     # invariant checks; `greenline doctor --fix` reconciles under the lock
```

If commits reached `main` **outside the gate** (a legacy-workflow session,
a hotfix), greenline refuses to run until they are gated — it never discards them.
Gate them in place with:

```
greenline adopt      # check + deploy the current main tip; never resets main
```

## What the gate does (`greenline submit`)

Under an exclusive lock (other submits queue behind it):

1. **Preflight reconcile** — recover any crashed prior gate from the journal;
   confirm the canonical checkout is clean and on `main`; reconcile
   `main`/last-green/origin; reset the gate worktree to the main tip.
2. **Squash-merge** your branch in the gate worktree. Conflict or empty diff → fail fast.
3. **Check** — run `./run check` with cwd = the gate worktree, under a hard **5-minute
   cap**. Nonzero *or* over the cap → main untouched, your branch preserved, log tail
   printed. A timed-out check is killed (whole process group) and counts as a gate
   FAIL; the fix is a faster suite, never a longer limit — see docs/DOCTRINE.md,
   "The five-minute check".
4. **Advance** — fast-forward the canonical `main` to the candidate.
5. **Deploy** — run `./run deploy` with cwd = the canonical checkout. Nonzero → **automatic rollback**: main resets to the previous commit and `./run deploy` re-runs to restore prod.
6. **Publish** — push `main` to origin (via a one-shot allow-push flag the pre-push hook honours) and advance `last-green`.

## The contract (you must implement in `./run`)

- **`./run check`** — cwd = the worktree being gated. Build + lint + FULL tests against a
  **test** datastore. Exit code is the verdict. Must run concurrently from multiple
  worktrees, and must complete in **under 5 minutes** (hard cap, no override).
- **`./run deploy`** — cwd = the canonical checkout. Rebuild/restart prod (e.g.
  `auto -q restart vix`), **health-check**, exit nonzero on unhealthy, and be
  **idempotent** (rollback re-runs it).
- **`./run health`** *(optional)* — probe only. If absent, greenline re-runs `./run deploy` as the health probe.

## Test/code co-design (from docs/DOCTRINE.md — read it)

Tests run in parallel with each other, with other agents' runs, and with live prod.
Therefore: namespace every entity a test creates (uuid suffixes); never assert global
state (counts, "table empty", singletons); never truncate/reset shared stores; tolerate
pre-existing and concurrently-changing data; bind servers to OS-assigned ports (port 0);
prefer per-test datastores (tmp sqlite, worktree-relative `local/`); keep schema
migrations backward-compatible one version (deploy rollback runs the previous code against
the new schema); serialize tests that share a scarce resource (a local LLM server, one GPU).

**Real but fast:** never mock another service — but make real tests quick with a
content-addressed record/replay cache: route all access to the external service through
one choke point; key = hash(endpoint + model + full request); store `{model, request,
response}` JSON per key in a gitignored `local/<service>-cache/` (idempotently created,
atomic writes, corrupt entry = self-healing miss). Miss → real call; hit → the real
recorded response in milliseconds. First run is genuinely end-to-end; every rerun is
instant, and the persistent gate worktree keeps the gate's cache warm. To re-record,
delete the cache dir. Don't cache calls whose variability is what's under test.

## State (in the shared git dir)

`.git/greenline/`: `lock`, `journal.jsonl`, `status.json`, `deployed` (SHA prod runs),
`logs/`, `allow-push` (transient, gate publish), `allow-main` (transient, PID-bound,
gate main-ref mutations). Ref `refs/greenline/last-green` = last fully-gated,
deployed-healthy commit.

**Main is hard-locked.** `greenline setup` installs three hooks:
- `reference-transaction` — refuses any local update of `refs/heads/main`
  unless `allow-main` exists and names a still-alive PID. Cannot be bypassed with
  `--no-verify`. This is what actually prevents working on main.
- `pre-commit` — early reject when HEAD is on `main` (friendly message;
  bypassable — the reference-transaction hook is the real lock).
- `pre-push` — refuses direct pushes to `main` unless the gate sets
  `allow-push`.
