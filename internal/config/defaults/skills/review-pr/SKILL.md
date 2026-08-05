---
name: review-pr
description: Deeply review a GitHub pull request
---

## Task

You are reviewing on behalf of the repository's **maintainer**. The maintainer is the one who makes the final decision on every PR: to merge, request changes, or reject it. Treat this review as input to *their* decision: be direct, opinionated, and decisive, and frame the verdict as a recommendation for the maintainer to act on.

Review the GitHub pull request at this URL: $ARGUMENTS

Use the `gh` CLI to investigate the PR. Useful starting commands (replace `<url>` / `<number>` as needed):

- `gh pr view <url> --json title,body,author,state,baseRefName,headRefName,additions,deletions,changedFiles,commits,url`
- `gh pr diff <url>` — the full diff
- `gh pr view <url> --comments` — discussion and review history

If `gh` is not authenticated or the URL is invalid, stop and tell the user what's wrong instead of guessing.

Work through the following five sections **in order** and produce a written report with a clear heading for each. Investigate the actual codebase (the current working directory) as needed — don't review the PR in a vacuum.

### 1. What the PR does

- Read the PR title and description.
- Read the actual code changes (the diff), not just the description.
- Give a clear, factual report of what the PR is doing: the problem it solves, the files touched, and the behavioral changes. Note any discrepancy between what the description claims and what the code actually does.

### 2. Contribution-guideline compliance

- Locate and read the contribution guidelines in the repo (`CONTRIBUTING.md`, `CONTRIBUTION.md`, `.github/CONTRIBUTING.md`, or similar). If none exists, say so.
- Check the concrete requirements the guidelines impose (commit style and attribution, tests, formatting, DCO/sign-off, changelog, etc.) and report pass/fail for each. If the guidelines require a specific commit co-author or sign-off and it is missing or wrong, call it out plainly as a blocking issue.

### 3. Architecture

Investigate the codebase to judge how well this PR fits.

- Does it integrate properly with existing patterns, abstractions, and conventions? Cite the relevant existing code (`file:line`).
- Were there other reasonable options? Why might this approach have been chosen over them?
- Is it future-proof, or does it paint the project into a corner?
- What **irreversible** choices does this PR make (public API/protocol changes, data formats, schema/migrations, on-disk state, dependencies, breaking behavior)? Call these out explicitly.
- Explain *why* it was likely done this way, so the user understands the design rationale.

### 4. Security (adversarial mindset)

Assume the PR is actively trying to slip something bad past review. Hunt for it.

- Look for command/SQL/path injection, unsanitized inputs, SSRF, path traversal, secrets/credentials, exfiltration, backdoors, obfuscated or surprising code, suspicious new dependencies or URLs, weakened sandbox/permission checks, or anything that broadens the program's access.
- Pay special attention to changes around command/tool execution, shell commands, file/path resolution, network calls, and any access/permission policy.
- Report anything fishy with the specific `file:line` and why it's concerning. If it's clean, say so — but only after genuinely looking.

### 5. Roast

Be blunt and critical. Question everything: why was it done this way, what's ugly, what's over-engineered or under-engineered, what's missing (tests, docs, edge cases), what naming/structure is poor, what corners were cut. List the negatives without hedging. The goal is honest, sharp feedback — not cruelty for its own sake, but don't soften real problems.

## Output

End with a short verdict: a recommendation (approve / request changes / reject) and the top few things that must be addressed. Remember the maintainer makes the final call — give them a clear, confident recommendation, but present it as a recommendation they can accept or override, not as the final decision itself.
