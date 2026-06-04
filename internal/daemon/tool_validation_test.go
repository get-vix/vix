package daemon

import (
	"strings"
	"testing"
)

// These tests double as living documentation of exactly how lenient
// validateToolInput is. Each group pins down one edge of the acceptance
// frontier; if someone later tightens a rule, the corresponding assertion here
// will fail and spell out the original intent.

// errContains asserts on whether validateToolInput returned an error, and that
// the message mentions the offending field when one is expected.
func wantErr(t *testing.T, name string, input map[string]any, wantField string) {
	t.Helper()
	err := validateToolInput(name, input)
	if err == nil {
		t.Fatalf("%s(%v): expected error, got nil", name, input)
	}
	if wantField != "" && !strings.Contains(err.Error(), wantField) {
		t.Fatalf("%s(%v): error %q should mention %q", name, input, err, wantField)
	}
}

func wantOK(t *testing.T, name string, input map[string]any) {
	t.Helper()
	if err := validateToolInput(name, input); err != nil {
		t.Fatalf("%s(%v): expected nil error, got %v", name, input, err)
	}
}

// Group A: integer/number ↔ Go numeric duality, via read_file.offset (an
// optional integer field). Required path+reason are always supplied.
func TestValidate_IntegerDuality(t *testing.T) {
	base := func(offset any) map[string]any {
		return map[string]any{"path": "/x", "reason": "r", "offset": offset}
	}
	// Accept every numeric kind, including a fractional float (handlers
	// truncate via int(v); we do not enforce integrality).
	for _, v := range []any{float64(120), int(120), int64(120), 0.5} {
		wantOK(t, "read_file", base(v))
	}
	// Reject non-numeric kinds.
	wantErr(t, "read_file", base("120"), "offset")
	wantErr(t, "read_file", base(true), "offset")
}

// Group B: array tolerance, via glob_files.pattern (a required array field that
// the handler also coerces from a bare string through toStringList).
func TestValidate_ArrayTolerance(t *testing.T) {
	base := func(pattern any) map[string]any {
		return map[string]any{
			"pattern":                    pattern,
			"reason":                     "r",
			"reason_to_increase_timeout": "N/A",
		}
	}
	// Accept []any, []string, and a bare string.
	wantOK(t, "glob_files", base([]any{"**/*.go"}))
	wantOK(t, "glob_files", base([]string{"**/*.go"}))
	wantOK(t, "glob_files", base("**/*.go"))
	// Array element types are NOT inspected: an array of non-strings still
	// passes the validation layer (the container kind is all we check).
	wantOK(t, "glob_files", base([]any{1, 2}))
	// Reject non-array, non-string scalars and objects.
	wantErr(t, "glob_files", base(42), "pattern")
	wantErr(t, "glob_files", base(map[string]any{}), "pattern")
}

// Group C: object/array-of-object fields, via ask_question_to_user.questions
// and todo_write.todos.
func TestValidate_ObjectsAndNestedItems(t *testing.T) {
	// Array of objects: accepted.
	wantOK(t, "ask_question_to_user", map[string]any{
		"questions": []any{map[string]any{"id": "a", "category": "c", "question": "q"}},
	})
	// Nested item fields are NOT validated here: an array of bare strings for
	// `questions` passes the validation layer even though items should be
	// objects. (Element/field shape is the impl layer's concern.)
	wantOK(t, "ask_question_to_user", map[string]any{
		"questions": []any{"not-an-object"},
	})
	// Empty array is valid for todo_write ("send an empty array to clear").
	wantOK(t, "todo_write", map[string]any{"todos": []any{}})
	// A map where an array is required is rejected.
	wantErr(t, "todo_write", map[string]any{"todos": map[string]any{}}, "todos")
}

// Group D: string/boolean exactness, plus the explicit decision that an empty
// string is accepted at the validation layer.
func TestValidate_StringAndBoolExactness(t *testing.T) {
	// string field
	wantOK(t, "write_file", map[string]any{"path": "/x", "content": "c"})
	wantErr(t, "write_file", map[string]any{"path": 42, "content": "c"}, "path")
	// Empty string is present + correctly typed → accepted here; emptiness is
	// the impl layer's job (resolvePathInAllowed rejects "").
	wantOK(t, "write_file", map[string]any{"path": "", "content": ""})

	// boolean field, via glob_files.include_hidden (optional bool).
	gb := func(v any) map[string]any {
		return map[string]any{
			"pattern":                    []any{"*"},
			"reason":                     "r",
			"reason_to_increase_timeout": "N/A",
			"include_hidden":             v,
		}
	}
	wantOK(t, "glob_files", gb(true))
	wantErr(t, "glob_files", gb("true"), "include_hidden")
}

// Group E: required-presence frontier — missing, explicit nil, unknown keys.
func TestValidate_RequiredPresence(t *testing.T) {
	// Missing required field.
	wantErr(t, "write_file", map[string]any{"path": "/x"}, "content")
	wantErr(t, "edit_file", map[string]any{"path": "/x", "new_string": "n"}, "old_string")
	// Explicit nil is treated the same as missing.
	wantErr(t, "write_file", map[string]any{"path": "/x", "content": nil}, "content")
	// Optional field absent → fine.
	wantOK(t, "read_file", map[string]any{"path": "/x", "reason": "r"})
	// Unknown/extra key is ignored (the daemon injects cwd/_session/etc.).
	wantOK(t, "write_file", map[string]any{"path": "/x", "content": "c", "bogus": 1, "cwd": "/y"})
}

// Group F: tools with no local schema are passed through (nil error).
func TestValidate_SchemaSkip(t *testing.T) {
	// MCP tools validate at the MCP server, not here.
	wantOK(t, "mcp__postgres__query", map[string]any{})
	// Genuinely unknown names defer to the existing "unknown tool" path.
	wantOK(t, "totally_unknown_tool", map[string]any{"whatever": true})
}

// Group G: advisory justification fields (reason, reason_to_*) are NOT
// hard-required server-side, regardless of build, because they don't affect
// tool execution. Functional fields still are. (The schema still advertises
// them as required to the model.)
func TestValidate_AdvisoryFieldsNotRequired(t *testing.T) {
	// bash with command but no reason / reason_to_* fields → accepted.
	wantOK(t, "bash", map[string]any{"command": "ls"})
	// read_file with path but no reason → accepted.
	wantOK(t, "read_file", map[string]any{"path": "/x"})
	// But the functional required field is still enforced.
	wantErr(t, "bash", map[string]any{"reason": "list"}, "command")
	// A present advisory field with the wrong type is still flagged.
	wantErr(t, "read_file", map[string]any{"path": "/x", "reason": 42}, "reason")
}
