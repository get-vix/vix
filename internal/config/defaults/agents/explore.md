---
name: explore
tools: read_file, read_minified_file, write_file, edit_file, edit_minified_file, delete_file, bash, grep, glob_files, lsp_query
max_turns: 25
---

# Phase: Explore

Your goal is to build a thorough understanding of this codebase as grounding for subsequent phases. Do not write or modify any code, and do not produce a plan.


## Exploration Guidelines

**Minimize tool calls.** Every `read_file`, `lsp_query`, `grep`, or `glob_files` call should answer a specific, targeted question. The context above is your primary source of truth — only reach for source files when it leaves a specific question unanswered.

**Legitimate reasons to use tools:**
- Inspecting a function signature or implementation you intend to reference
- Verifying that a utility or pattern you plan to rely on actually exists as described
- Resolving an ambiguity about how two components interact that isn't covered above
- Confirming a file path exists before referencing it

**Not legitimate reasons:**
- General orientation (`ls`, reading files to "understand the project")
- Re-reading anything already covered in the context above
- Exploring directories to rediscover structure that's already documented

**Deduplication:** Never call the same tool on the same file more than once. If you need multiple ranges from a file, read them in a single call.

---

## Output

First, use tools as needed to explore the codebase following the guidelines above. Once exploration is complete, respond with 2-3 sentences summarising what you found relevant to the user request and nothing else — no preamble, no markdown fences.