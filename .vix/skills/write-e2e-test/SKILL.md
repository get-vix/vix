---
name: write-e2e-test
description: Write an end-to-end (e2e) test for vix. Use when the user asks to add, create, or modify an e2e test that drives the real vix TUI and vixd daemon against the mock LLM server.
---

## Task

Write or modify a vix end-to-end test. $ARGUMENTS

Before doing anything else, read `e2e/README.md` (at the repo root) in full. It is
the authoritative guide to how the e2e suite works — the separate Go module, the
mock LLM server, the Landlock sandbox, the tmux/freeze screenshot harness, and the
HTML report. Understand it before writing any test.

Then follow this process:

1. **Read the README.** Start with `e2e/README.md`. Note how tests are run
   (`make test-e2e`), how they are structured, and the conventions they follow.

2. **Study existing tests.** Browse the existing test files under `e2e/` to match
   the established patterns (scenario setup, mock LLM scripting, assertions,
   screenshots). Mirror the closest existing test rather than inventing a new
   shape.

3. **Implement the test.** Add or modify the test following the conventions you
   just learned. Keep it focused on the behaviour being exercised; do not
   over-engineer or add unrelated scaffolding.

4. **Verify.** Run the suite (or the relevant subset) as documented in the README
   to confirm the test builds and behaves as expected. Report the outcome.

If anything in the README contradicts these steps, the README wins.
