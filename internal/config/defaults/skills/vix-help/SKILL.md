---
name: vix-help
description: Answer questions about vix itself — the TUI (keybindings, tabs, sessions, slash commands), configuration (settings.json, deny lists, permissions), agents & tools, models & providers, jobs, hooks, skills, workflows, MCP servers, the brain/code intelligence, headless/CLI usage, and installation. Use whenever the user asks how vix works or how to configure or operate it, instead of guessing or browsing.
---

# Vix help

Use this skill to answer questions about **vix itself** from its official
documentation, rather than guessing. The docs are published as plain Markdown,
one file per section, at `https://getvix.dev/manual/`.

## How to answer

1. **Find the right section.** Fetch the table of contents once:

   `web_fetch https://getvix.dev/manual/index.md`

   It lists every section with its URL. Pick the section id whose title best
   matches the question (e.g. keybindings → `tui-basics`, providers →
   `models-providers`, scheduled tasks → `guide-jobs`, MCP → `mcp`).

2. **Fetch that section** and answer from it, quoting the relevant part:

   `web_fetch https://getvix.dev/manual/<section-id>.md`

   Fetch more than one section if the question spans topics.

3. **Cite** the section title and its `https://getvix.dev/docs#<section-id>` link
   so the user can read more.

## Offline fallback

If `web_fetch` fails (no network, host blocked, deny list), read the bundled
offline snapshot instead and answer from it:

`read_file ${SKILL_DIR}/references/vix-manual.md`

It contains the same content, all sections concatenated in order, separated by
`---`. Search it for the relevant heading. When you fall back to it, tell the
user the answer is from a bundled snapshot that may be slightly out of date and
point them at `https://getvix.dev/docs`.

## Notes

- Prefer the live docs (step 1–2); they are always current. The bundled snapshot
  is only a fallback.
- Answer concisely and specifically. Don't dump a whole section — extract the
  keybinding, flag, config key, or steps the user actually asked for.
- If neither the live docs nor the snapshot cover the question, say so plainly
  rather than inventing behavior.
