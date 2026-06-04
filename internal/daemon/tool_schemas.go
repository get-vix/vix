package daemon

import (
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/telemetry"
)

// ToolSchemas returns the tool definitions in the provider-neutral
// llm.ToolParam shape, using the package-level default timeout bounds.
// Sessions that want bounds-aware descriptions should use
// ToolSchemasWithBounds instead.
func ToolSchemas() []llm.ToolParam {
	return toolSchemasToNeutral(buildToolSchemas(defaultToolTimeoutDefault, defaultToolTimeoutMax))
}

// ToolSchemasWithBounds returns the neutral tool definitions with the
// bash/glob_files timeout descriptions rendered against the given floor
// and cap.
//
// Passing zero for either value falls back to the package-level defaults.
func ToolSchemasWithBounds(def, max time.Duration) []llm.ToolParam {
	if def <= 0 {
		def = defaultToolTimeoutDefault
	}
	if max <= 0 {
		max = defaultToolTimeoutMax
	}
	return toolSchemasToNeutral(buildToolSchemas(def, max))
}

// toolSchemasToNeutral converts an anthropic-shaped tool list into the
// neutral llm.ToolParam shape. The schema bodies are JSON Schema dicts
// passed through verbatim; each provider adapter translates them to its
// own wire format.
func toolSchemasToNeutral(tools []anthropic.ToolUnionParam) []llm.ToolParam {
	out := make([]llm.ToolParam, 0, len(tools))
	for _, t := range tools {
		if t.OfTool == nil {
			continue
		}
		props, _ := t.OfTool.InputSchema.Properties.(map[string]any)
		schema := map[string]any{
			"type":       "object",
			"properties": props,
		}
		if len(t.OfTool.InputSchema.Required) > 0 {
			schema["required"] = t.OfTool.InputSchema.Required
		}
		desc := ""
		if t.OfTool.Description.Valid() {
			desc = t.OfTool.Description.Value
		}
		out = append(out, llm.ToolParam{
			Name:        t.OfTool.Name,
			Description: desc,
			InputSchema: schema,
		})
	}
	return out
}

// buildToolSchemas is the internal workhorse that actually constructs the
// schema slice. def and max are the tool-call floor and cap used to render
// the bash/glob_files description strings. Caller guarantees both > 0.
func buildToolSchemas(def, max time.Duration) []anthropic.ToolUnionParam {
	defSec := int(def.Seconds())
	maxSec := int(max.Seconds())
	maxMin := int(max.Minutes())

	bashDesc := fmt.Sprintf(
		"Run a shell command and return stdout+stderr. Times out after %d seconds by default; can be raised up to a hard cap of %d seconds (%d minutes) via the `timeout` param. For finding files by pattern, use glob_files instead — it's much faster.",
		defSec, maxSec, maxMin,
	)
	bashTimeoutDesc := fmt.Sprintf(
		"Timeout in seconds. Optional; defaults to %d. Hard-capped at %d (%d minutes). When the timeout is reached the command is killed and an error is returned. Increasing the timeout is exponentially penalizing — only do so when strictly necessary.",
		defSec, maxSec, maxMin,
	)
	bashReasonTimeoutDesc := fmt.Sprintf(
		"ONLY fill this if timeout exceeds %d seconds — in that case, explain why the command cannot complete within the default timeout. Write exactly the two characters 'N/A' in every other case. Hard-capped at %d seconds (%d minutes). Abuse will be penalized.",
		defSec, maxSec, maxMin,
	)
	globTimeoutDesc := fmt.Sprintf(
		"Timeout in seconds. Optional; defaults to %d. Hard-capped at %d (%d minutes). When the timeout is reached the walk is cancelled and an error is returned. Increasing the timeout is exponentially penalizing — only do so when strictly necessary; prefer narrowing the pattern or path first.",
		defSec, maxSec, maxMin,
	)
	globReasonTimeoutDesc := fmt.Sprintf(
		"ONLY fill this if timeout exceeds %d seconds — in that case, explain why the glob cannot complete within the default timeout (e.g. very large tree that genuinely needs the longer wall clock). Write exactly the two characters 'N/A' in every other case. Abuse will be penalized.",
		defSec,
	)

	bashRequired := []string{"command", "reason"}
	globRequired := []string{"pattern", "reason"}
	if telemetry.Version() == "dev" {
		bashRequired = append(bashRequired,
			"reason_to_use_instead_of_read_file_tool",
			"reason_to_use_instead_of_edit_file_tool",
			"reason_to_use_instead_of_glob_files_tool",
			"reason_to_increase_timeout",
		)
		globRequired = append(globRequired, "reason_to_increase_timeout")
	}
	return []anthropic.ToolUnionParam{
		{OfTool: &anthropic.ToolParam{
			Name:        "read_file",
			Description: anthropic.String("Read a file from disk. Returns content with line numbers. Use offset/limit for large files."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"path":   map[string]any{"type": "string", "description": "The absolute path to the file."},
					"offset": map[string]any{"type": "integer", "description": "Start reading from this line (1-based). Optional."},
					"limit":  map[string]any{"type": "integer", "description": "Max number of lines to return. Optional."},
					"reason": map[string]any{
						"type":        "string",
						"description": "Explain: (1) why you chose this specific file/pattern, (2) what information you expect to find, and (3) how that information will help you accomplish your current goal.",
					},
				},
				Required: []string{"path", "reason"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "read_minified_file",
			Description: anthropic.String("Read a file from disk and automatically minify it using Tree-sitter (stripping comments, collapsing whitespace) for token-efficient output. The minified content is exactly the code that is on disk, just with whitespace and comments removed. Optionally extract a line range before minifying."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"path":   map[string]any{"type": "string", "description": "The absolute path to the file."},
					"offset": map[string]any{"type": "integer", "description": "Start reading from this line (1-based). Optional, defaults to start of file."},
					"limit":  map[string]any{"type": "integer", "description": "Max number of lines to read. Optional, defaults to entire file."},
					"reason": map[string]any{
						"type":        "string",
						"description": "Explain: (1) why you chose this specific file/pattern, (2) what information you expect to find, and (3) how that information will help you accomplish your current goal.",
					},
				},
				Required: []string{"path", "reason"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "write_file",
			Description: anthropic.String("Write content to a file. Creates parent directories if needed."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"path":    map[string]any{"type": "string", "description": "The absolute path to the file."},
					"content": map[string]any{"type": "string", "description": "The full content to write."},
					"mode":    map[string]any{"type": "string", "description": "Optional Unix file mode as an octal string (e.g. \"0755\"). Default: 0644 for new files; preserve existing mode when overwriting. Pass \"0755\" when writing an executable script — without this, the file is not executable."},
				},
				Required: []string{"path", "content"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "write_minified_file",
			Description: anthropic.String("Write content to a file through the virtual filesystem. The content must be provided in the minified format (as returned by read_minified_file); after writing, a formatter restores valid source. Creates parent directories if needed."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"path":    map[string]any{"type": "string", "description": "The absolute path to the file."},
					"content": map[string]any{"type": "string", "description": "The full content to write, in minified format."},
				},
				Required: []string{"path", "content"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "edit_file",
			Description: anthropic.String("Edit a file by replacing an exact string match. old_string must appear exactly once in the file."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"path":       map[string]any{"type": "string", "description": "The absolute path to the file."},
					"old_string": map[string]any{"type": "string", "description": "The exact text to find (must be unique in the file)."},
					"new_string": map[string]any{"type": "string", "description": "The replacement text."},
					"mode":       map[string]any{"type": "string", "description": "Optional Unix file mode as an octal string (e.g. \"0755\"). Default: preserve the existing file's mode. Only set this when you need to change permissions as part of the edit."},
				},
				Required: []string{"path", "old_string", "new_string"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "edit_minified_file",
			Description: anthropic.String("Edit a file through the virtual filesystem. The file is minified with Tree-sitter, the match is performed on the minified representation, and a formatter restores valid source. Both old_string and new_string must use the minified format (as returned by read_minified_file)."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"path":       map[string]any{"type": "string", "description": "The absolute path to the file."},
					"old_string": map[string]any{"type": "string", "description": "The exact text to find in the minified content (must be unique)."},
					"new_string": map[string]any{"type": "string", "description": "The replacement text in minified format."},
				},
				Required: []string{"path", "old_string", "new_string"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "delete_file",
			Description: anthropic.String("Delete a file from disk."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"path": map[string]any{"type": "string", "description": "The absolute path to the file."},
				},
				Required: []string{"path"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "bash",
			Description: anthropic.String(bashDesc),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"command": map[string]any{"type": "string", "description": "The shell command to execute."},
					"timeout": map[string]any{"type": "integer", "description": bashTimeoutDesc},
					"reason": map[string]any{
						"type":        "string",
						"description": "Explain why this command needs to be run.",
					},
					"reason_to_use_instead_of_read_file_tool": map[string]any{
						"type":        "string",
						"description": "ONLY fill this if the command invokes cat/head/tail/less/more/bat to view file contents — in that case, explain what you need that read_file/read_minified_file cannot give you. Write exactly the two characters 'N/A' in every other case. Abuse will be rejected.",
					},
					"reason_to_use_instead_of_edit_file_tool": map[string]any{
						"type":        "string",
						"description": "ONLY fill this if the command invokes sed/awk/perl -i/tr to modify files — in that case, explain what you need that edit_file/edit_minified_file cannot give you. Write exactly the two characters 'N/A' in every other case. Abuse will be rejected.",
					},
					"reason_to_use_instead_of_glob_files_tool": map[string]any{
						"type":        "string",
						"description": "ONLY fill this if the command invokes find/fd/ls/tree to list files — in that case, explain what you need that glob_files cannot give you. Write exactly the two characters 'N/A' in every other case. Abuse will be rejected.",
					},
					"reason_to_increase_timeout": map[string]any{
						"type":        "string",
						"description": bashReasonTimeoutDesc,
					},
					"background": map[string]any{
						"type":        "boolean",
						"description": "Optional. If true, spawn the command detached and return immediately with a `job_id` plus paths to a log file and an rc file. Poll with ordinary bash: `test -f <rc> && cat <rc>` (empty = still running), `tail -n 50 <log>`. Terminate with `kill -TERM -<pgid>` (pgid is in the result). When `background: true`, the `timeout` param becomes a wall-clock cap on the detached child (default 3600 s), NOT a cap on this tool call — the tool call returns in under a second. Use this when a command will exceed the foreground cap, or when you want to race multiple approaches in parallel (subsequent bash tool calls can issue more work while the job runs). Default false.",
					},
				},
				Required: bashRequired,
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "grep",
			Description: anthropic.String("Search file contents recursively. Always returns line numbers. Output format is filepath:linenum:matching line. There is no need to use bash grep — this tool already provides line numbers."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"pattern": map[string]any{"type": "string", "description": "Regex pattern to search for."},
					"path":    map[string]any{"type": "string", "description": "Directory or file to search in. Defaults to cwd."},
					"include": map[string]any{"type": "string", "description": "File glob filter, e.g. '*.py'. Optional."},
					"reason": map[string]any{
						"type":        "string",
						"description": "Explain: (1) why you chose this specific file/pattern, (2) what information you expect to find, and (3) how that information will help you accomplish your current goal.",
					},
				},
				Required: []string{"pattern", "reason"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "glob_files",
			Description: anthropic.String("Find paths matching one or more glob patterns. Returns absolute paths, sorted alphabetically and deduplicated, up to `max_results` results (default 1000). If the walker hits the cap it stops early and reports `(capped at N)` in the output — raise max_results or narrow the pattern if you need more. By default returns both files and directories; use the 'type' parameter to filter. Hidden entries (names starting with '.') are included by default; set include_hidden=false to skip them. Both 'pattern' and 'path' are arrays — always pass a list, even for a single value. Multiple patterns/paths are unioned in a single call, so prefer one call with many patterns over multiple calls. Examples: ['**/*.py'] for all Python files, ['*.xcodeproj', '*.xcworkspace', 'Package.swift'] to union several name patterns, ['src/**/test_*.go'] for tests under a subtree."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"pattern": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"minItems":    1,
						"description": "Array of glob patterns (unioned). Use '**' to recurse into subdirectories, '*' to match one path segment. Examples: ['**/*.py'], ['*.xcodeproj', '*.xcworkspace', 'Package.swift']. Always an array, even for a single pattern.",
					},
					"path": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"minItems":    1,
						"description": "Array of base directories to glob within. Defaults to cwd if omitted. Always an array when provided.",
					},
					"type":           map[string]any{"type": "string", "enum": []string{"f", "d", "any"}, "description": "Filter by entry type: 'f' = files only, 'd' = directories only, 'any' = both. Defaults to 'any'."},
					"include_hidden": map[string]any{"type": "boolean", "description": "Include hidden files and directories (those starting with '.'). Defaults to true."},
					"max_results": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"description": "Maximum number of results (files + directories) to return. Defaults to 1000. The walker stops as soon as this many unique paths are collected — raise it deliberately if you're searching a large tree and need more, but prefer narrowing the pattern or path instead. Full-filesystem walks without a tight pattern can still be slow even below the cap.",
					},
					"timeout": map[string]any{
						"type":        "integer",
						"description": globTimeoutDesc,
					},
					"reason_to_increase_timeout": map[string]any{
						"type":        "string",
						"description": globReasonTimeoutDesc,
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Explain: (1) why you chose this specific file/pattern, (2) what information you expect to find, and (3) how that information will help you accomplish your current goal.",
					},
				},
				Required: globRequired,
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "lsp_query",
			Description: anthropic.String("Query LSP servers for code intelligence. Use for precise code navigation: finding definitions, references, type info, compile errors, and interface implementations. Prefer over grep for structural code queries."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"operation": map[string]any{
						"type":        "string",
						"enum":        []string{"go_to_definition", "find_references", "hover", "document_symbols", "workspace_symbols", "find_implementations", "diagnostics"},
						"description": "The LSP operation to perform.",
					},
					"file": map[string]any{
						"type":        "string",
						"description": "The absolute file path. Required for all operations except workspace_symbols.",
					},
					"line": map[string]any{
						"type":        "integer",
						"description": "Line number (1-based). Required for go_to_definition, find_references, hover, find_implementations.",
					},
					"character": map[string]any{
						"type":        "integer",
						"description": "Character offset (1-based). Required for go_to_definition, find_references, hover, find_implementations.",
					},
					"query": map[string]any{
						"type":        "string",
						"description": "Search query for workspace_symbols. Required for workspace_symbols operation.",
					},
					"include_declaration": map[string]any{
						"type":        "boolean",
						"description": "Include the declaration in find_references results. Defaults to true.",
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Explain: (1) why you chose this specific file/pattern, (2) what information you expect to find, and (3) how that information will help you accomplish your current goal.",
					},
				},
				Required: []string{"operation", "reason"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "spawn_agent",
			Description: anthropic.String("Spawn a subagent to handle a task autonomously. The subagent gets its own conversation, tools, and LLM. Use background=true to run in parallel and retrieve results later with task_output."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"agent_type": map[string]any{
						"type":        "string",
						"description": "The agent type to spawn. See tool description for available types.",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "The task or question for the subagent. Be specific and self-contained — the subagent has no access to the parent conversation.",
					},
					"background": map[string]any{
						"type":        "boolean",
						"description": "If true, run in the background and return a task ID immediately. Retrieve results later with task_output. Defaults to false.",
					},
				},
				Required: []string{"prompt"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "ask_question_to_user",
			Description: anthropic.String("Ask the user one or more questions and wait for their response. Pass a single-element array for one question, or multiple elements to present a tabbed interface. Each question has its own category, text, and optional suggested options."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"questions": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id":       map[string]any{"type": "string", "description": "Unique identifier for this question."},
								"category": map[string]any{"type": "string", "description": "Short tab label, e.g. 'Language'."},
								"question": map[string]any{"type": "string", "description": "The question text."},
								"options": map[string]any{
									"type":        "array",
									"items":       map[string]any{"type": "string"},
									"description": "Suggested options for the user to choose from.",
								},
								"default_text": map[string]any{
									"type":        "string",
									"description": "Suggestion/placeholder for the free-text input.",
								},
							},
							"required": []string{"id", "category", "question"},
						},
						"description": "Array of questions to present. Use one element for a single question, multiple for a tabbed interface.",
					},
				},
				Required: []string{"questions"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "task_output",
			Description: anthropic.String("Retrieve the result of a background subagent task. If the task is still running, waits up to 30 seconds before returning a 'still running' message."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"task_id": map[string]any{
						"type":        "string",
						"description": "The task ID returned by spawn_agent with background=true.",
					},
				},
				Required: []string{"task_id"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "web_fetch",
			Description: anthropic.String("Fetch a web page and return its content as text. HTML is automatically converted to readable text. Supports JSON and plain text responses too."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"url":      map[string]any{"type": "string", "description": "The URL to fetch (must be http or https)."},
					"selector": map[string]any{"type": "string", "description": "Content selector hint: 'main', 'article', or 'body'. If omitted, auto-detects <main> or <article>, falling back to <body>."},
				},
				Required: []string{"url"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "web_search",
			Description: anthropic.String("Search the web using Brave Search API. Returns a numbered list of results with titles, URLs, and descriptions. Requires BRAVE_SEARCH_API_KEY environment variable."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"query": map[string]any{"type": "string", "description": "The search query."},
					"count": map[string]any{"type": "integer", "description": "Number of results to return (1-20, default 5)."},
				},
				Required: []string{"query"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "todo_write",
			Description: anthropic.String("Replace the session's TODO list with the provided items. Use this as a scratchpad to plan and track multi-step work: write the full list, then call again later to flip statuses, add items, or remove finished ones. Replace semantics — the list you send fully overwrites the previous one. Keep `id` values stable across updates so you can refer back to the same item. Each item has an optional `depends_on` listing other ids that must be `completed` before this item may transition to `in_progress`. Server-side validation rejects: duplicate ids, self-dependencies, dangling references, dependency cycles, and any item marked `in_progress` whose dependencies are not yet `completed`. Prefer keeping at most one item `in_progress` at a time."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"todos": map[string]any{
						"type":        "array",
						"description": "Full replacement list of TODO items. Send an empty array to clear the list.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id":      map[string]any{"type": "string", "description": "Stable id chosen by you. Reuse across updates to refer to the same item."},
								"content": map[string]any{"type": "string", "description": "Short description of the step."},
								"status":  map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
								"depends_on": map[string]any{
									"type":        "array",
									"description": "Optional list of ids of items that must be completed before this item can become in_progress.",
									"items":       map[string]any{"type": "string"},
								},
							},
							"required": []string{"id", "content", "status"},
						},
					},
				},
				Required: []string{"todos"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "todo_read",
			Description: anthropic.String("Return the session's current TODO list. Use this to recover authoritative state when prior turns may have been compacted out of context, or whenever you want to double-check what is pending, in progress, completed, or blocked by an unmet dependency."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{},
				Required:   []string{},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        "tool_orchestrator",
			Description: anthropic.String("Execute a Python workflow that chains multiple tool calls (read_file, grep, glob_files, lsp_query, bash, edit_file, write_file, delete_file) in a single round-trip. The workflow script has access to tool functions and must return a dict with results. A CWD variable is available with the project root path. Use relative paths (resolved against CWD) or os.path.join(CWD, ...) for file operations."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"workflow": map[string]any{
						"type":        "string",
						"description": "Python script body (without def/indent). Has access to: read_file(), grep(), glob_files(), lsp_query(), bash(), edit_file(), write_file(), delete_file(). Must return a dict.",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "Short summary of what the workflow does.",
					},
				},
				Required: []string{"workflow", "description"},
			},
		}},
	}
}

// readOnlyToolNames is the set of tools safe to use during plan exploration.
var readOnlyToolNames = map[string]bool{
	"read_file":          true,
	"read_minified_file": true,
	"grep":               true,
	"glob_files":         true,
	"lsp_query":          true,
	"web_fetch":   true,
	"web_search":  true,
}

// ReadOnlyToolSchemas returns only the read-only tool schemas (for plan exploration).
func ReadOnlyToolSchemas() []llm.ToolParam {
	all := ToolSchemas()
	var readonly []llm.ToolParam
	for _, t := range all {
		if readOnlyToolNames[t.Name] {
			readonly = append(readonly, t)
		}
	}
	return readonly
}

// IsReadOnlyTool returns true if the tool name is read-only (safe for planning).
func IsReadOnlyTool(name string) bool {
	return readOnlyToolNames[name]
}

// SummarizeToolInput returns a one-line human summary of tool input.
func SummarizeToolInput(name string, input map[string]any) string {
	switch name {
	case "read_file":
		p, _ := input["path"].(string)
		mode, _ := input["mode"].(string)
		prefix := ""
		if mode == "minify" {
			prefix = "[minify] "
		}
		offset, hasOffset := input["offset"].(float64)
		limit, hasLimit := input["limit"].(float64)
		if hasOffset && offset > 0 {
			start := int(offset)
			if hasLimit && limit > 0 {
				end := start + int(limit) - 1
				return fmt.Sprintf("%s%s:%d-%d", prefix, p, start, end)
			}
			return fmt.Sprintf("%s%s:%d-", prefix, p, start)
		}
		return prefix + p
	case "read_minified_file":
		p, _ := input["path"].(string)
		offset, hasOffset := input["offset"].(float64)
		limit, hasLimit := input["limit"].(float64)
		if hasOffset && offset > 0 {
			start := int(offset)
			if hasLimit && limit > 0 {
				end := start + int(limit) - 1
				return fmt.Sprintf("%s:%d-%d", p, start, end)
			}
			return fmt.Sprintf("%s:%d-", p, start)
		}
		return p
	case "write_file", "write_minified_file":
		p, _ := input["path"].(string)
		c, _ := input["content"].(string)
		return fmt.Sprintf("%s (%d chars)", p, len(c))
	case "edit_file", "edit_minified_file":
		p, _ := input["path"].(string)
		oldStr, _ := input["old_string"].(string)
		newStr, _ := input["new_string"].(string)
		oldLines := strings.Count(oldStr, "\n") + 1
		newLines := strings.Count(newStr, "\n") + 1
		diff := newLines - oldLines
		if diff == 0 {
			return fmt.Sprintf("%s (%d lines changed)", p, oldLines)
		} else if diff > 0 {
			return fmt.Sprintf("%s (%d lines changed, +%d)", p, oldLines, diff)
		}
		return fmt.Sprintf("%s (%d lines changed, %d)", p, oldLines, diff)
	case "delete_file":
		p, _ := input["path"].(string)
		return p
	case "bash":
		cmd, _ := input["command"].(string)
		if len(cmd) > 500 {
			cmd = cmd[:500] + "..."
		}
		prefix := "$ "
		if bg, _ := input["background"].(bool); bg {
			prefix = "[bg] $ "
		}
		return prefix + cmd
	case "grep":
		p, _ := input["pattern"].(string)
		return p
	case "glob_files":
		var pattern string
		switch v := input["pattern"].(type) {
		case string:
			pattern = v
		case []any:
			parts := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok {
					parts = append(parts, s)
				}
			}
			pattern = strings.Join(parts, ", ")
		case []string:
			pattern = strings.Join(v, ", ")
		}
		if paths, ok := input["path"].([]any); ok && len(paths) > 0 {
			pathParts := make([]string, 0, len(paths))
			for _, p := range paths {
				if s, ok := p.(string); ok {
					pathParts = append(pathParts, s)
				}
			}
			if len(pathParts) > 0 {
				pattern = fmt.Sprintf("%s in %s", pattern, strings.Join(pathParts, ", "))
			}
		} else if p, ok := input["path"].(string); ok && p != "" {
			pattern = fmt.Sprintf("%s in %s", pattern, p)
		}
		return pattern
	case "lsp_query":
		op, _ := input["operation"].(string)
		if f, ok := input["file"].(string); ok && f != "" {
			return fmt.Sprintf("%s %s", op, f)
		}
		if q, ok := input["query"].(string); ok && q != "" {
			return fmt.Sprintf("%s '%s'", op, q)
		}
		return op
	case "spawn_agent":
		agentType, _ := input["agent_type"].(string)
		if agentType == "" {
			agentType = "general"
		}
		bg := ""
		if b, ok := input["background"].(bool); ok && b {
			bg = " (background)"
		}
		prompt, _ := input["prompt"].(string)
		if len(prompt) > 60 {
			prompt = prompt[:60] + "..."
		}
		return fmt.Sprintf("%s%s: %s", agentType, bg, prompt)
	case "ask_question_to_user":
		if qs, ok := input["questions"].([]any); ok {
			if len(qs) == 1 {
				if qMap, ok := qs[0].(map[string]any); ok {
					q, _ := qMap["question"].(string)
					if len(q) > 60 {
						q = q[:60] + "..."
					}
					return q
				}
			}
			return fmt.Sprintf("%d questions", len(qs))
		}
		return "question"
	case "task_output":
		id, _ := input["task_id"].(string)
		return id
	case "web_fetch":
		u, _ := input["url"].(string)
		return u
	case "web_search":
		q, _ := input["query"].(string)
		return q
	case "tool_orchestrator":
		desc, _ := input["description"].(string)
		if len(desc) > 500 {
			desc = desc[:500] + "..."
		}
		return desc
	default:
		// MCP tools: "mcp__<server>__<tool>" → display as "<key arg>" (name already shows server.tool)
		if strings.HasPrefix(name, "mcp__") {
			for _, key := range []string{"path", "query", "sql", "url", "name", "id", "command", "input"} {
				if v, ok := input[key].(string); ok && v != "" {
					if len(v) > 80 {
						v = v[:80] + "..."
					}
					return v
				}
			}
			// Fallback: use the first non-empty string value in the input map.
			for _, v := range input {
				if s, ok := v.(string); ok && s != "" {
					if len(s) > 80 {
						s = s[:80] + "..."
					}
					return s
				}
			}
			return ""
		}
		return ""
	}
}

// PatchSpawnAgentDescription updates the spawn_agent tool description in the given
// tool list based entirely on the loaded agent definitions.
func PatchSpawnAgentDescription(tools []llm.ToolParam, customAgents map[string]SubagentConfig) {
	for i, t := range tools {
		if t.Name != "spawn_agent" {
			continue
		}

		// Build agent list from loaded configs
		var agentEntries []string
		for _, ag := range customAgents {
			entry := "'" + ag.Name + "'"
			if ag.Description != "" {
				entry += " — " + ag.Description
			}
			agentEntries = append(agentEntries, entry)
		}

		// Build agent_type property description
		agentTypeDesc := "The agent type to spawn."
		if len(agentEntries) > 0 {
			agentTypeDesc += " Available: " + strings.Join(agentEntries, ", ") + "."
		}

		if props, ok := t.InputSchema["properties"].(map[string]any); ok {
			if atProp, ok := props["agent_type"].(map[string]any); ok {
				atProp["description"] = agentTypeDesc
			}
		}

		// Build top-level tool description
		topDesc := "Spawn a subagent to handle a task autonomously. The subagent gets its own conversation, tools, and LLM."
		if len(agentEntries) > 0 {
			topDesc += " Available agents: " + strings.Join(agentEntries, ", ") + "."
		}
		topDesc += " Use background=true to run in parallel and retrieve results later with task_output."
		tools[i].Description = topDesc

		break
	}
}

// GetToolSchema returns a single tool schema by name, or nil if not found.
func GetToolSchema(name string) *llm.ToolParam {
	for _, t := range ToolSchemas() {
		if t.Name == name {
			return &t
		}
	}
	return nil
}

// ExcludeTools removes tools with the given names from a tool list.
func ExcludeTools(tools []llm.ToolParam, names ...string) []llm.ToolParam {
	if len(names) == 0 {
		return tools
	}
	exclude := make(map[string]bool, len(names))
	for _, n := range names {
		exclude[n] = true
	}
	var filtered []llm.ToolParam
	for _, t := range tools {
		if exclude[t.Name] {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// FilterToolSchemas returns only the tool schemas whose names appear in the
// allowed list, using the package-level default timeout bounds. Sessions that
// want the schemas to reflect their configured tool_timeouts should use
// FilterToolSchemasWithBounds instead.
// If allowed is nil, returns all tools.
func FilterToolSchemas(allowed []string) []llm.ToolParam {
	return filterFrom(ToolSchemas(), allowed)
}

// FilterToolSchemasWithBounds is the bounds-aware variant of
// FilterToolSchemas. The bash/glob_files timeout descriptions are rendered
// against the given floor and cap so the LLM sees the actual, configurable
// window instead of the hard-coded defaults.
func FilterToolSchemasWithBounds(allowed []string, def, max time.Duration) []llm.ToolParam {
	return filterFrom(ToolSchemasWithBounds(def, max), allowed)
}

// filterFrom is the shared filter impl used by both FilterToolSchemas and
// FilterToolSchemasWithBounds. The caller supplies a pre-built schema slice
// and an allow-list; if allow is nil, the slice is returned as-is.
func filterFrom(schemas []llm.ToolParam, allowed []string) []llm.ToolParam {
	if allowed == nil {
		return schemas
	}
	allowSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowSet[name] = true
	}
	var filtered []llm.ToolParam
	for _, t := range schemas {
		if allowSet[t.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}
