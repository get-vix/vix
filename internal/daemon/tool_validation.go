package daemon

import (
	"fmt"
	"sync"

	"github.com/get-vix/vix/internal/daemon/llm"
)

// Tool-input validation.
//
// Tool handlers parse params with comma-ok assertions
// (`path, _ := params["path"].(string)`), so a missing or wrong-typed field
// silently becomes a zero value and the handler proceeds with it — writing an
// empty file, editing against an empty match string, etc. — without ever
// surfacing the mistake to the model.
//
// validateToolInput closes that gap by checking each tool call against the
// JSON schema already declared in tool_schemas.go before the call reaches a
// handler. It enforces only two things: required fields must be present, and
// any present field must have a type the handler can actually use. Optional
// fields, unknown keys (the daemon injects cwd/_session/etc.), emptiness, and
// nested object/array element shapes are intentionally NOT checked here — those
// are either the daemon's own business or the impl layer's.

var (
	toolSchemaOnce  sync.Once
	toolSchemaIndex map[string]llm.ToolParam
)

// schemaFor returns the declared schema for a tool name, building the index
// once on first use. The second return is false for names with no local schema
// (MCP tools, session-method tools not in buildToolSchemas, unknown names).
func schemaFor(name string) (llm.ToolParam, bool) {
	toolSchemaOnce.Do(func() {
		schemas := ToolSchemas()
		toolSchemaIndex = make(map[string]llm.ToolParam, len(schemas))
		for _, t := range schemas {
			toolSchemaIndex[t.Name] = t
		}
	})
	t, ok := toolSchemaIndex[name]
	return t, ok
}

// advisoryFields are schema-"required" fields that exist purely to make the
// model explain itself (for the UI and logs). They do not affect tool
// execution: a missing `reason` never causes the silent zero-value failures
// this validator guards against. We therefore do NOT hard-reject a call for
// omitting them — the schema still advertises them as required to the model,
// but server-side enforcement is limited to functional correctness. Present
// values are still type-checked.
var advisoryFields = map[string]bool{
	"reason": true,
	"reason_to_use_instead_of_read_file_tool":  true,
	"reason_to_use_instead_of_edit_file_tool":  true,
	"reason_to_use_instead_of_glob_files_tool": true,
	"reason_to_increase_timeout":               true,
}

// validateToolInput checks input against the tool's declared schema. It returns
// a non-nil error describing the first problem found, or nil when the input is
// acceptable (including for tools that have no local schema, which are left for
// the existing handler-lookup path to handle).
func validateToolInput(name string, input map[string]any) error {
	schema, ok := schemaFor(name)
	if !ok {
		// No local schema (e.g. mcp__* tools, unknown names). Defer to the
		// MCP server / "unknown tool" path rather than rejecting here.
		return nil
	}

	required, _ := schema.InputSchema["required"].([]string)
	for _, key := range required {
		if advisoryFields[key] {
			continue
		}
		// An explicit nil is treated the same as a missing key: the handler
		// would read a zero value either way.
		if v, present := input[key]; !present || v == nil {
			return fmt.Errorf("invalid arguments for %s: missing required field %q", name, key)
		}
	}

	props, _ := schema.InputSchema["properties"].(map[string]any)
	for key, raw := range input {
		prop, ok := props[key].(map[string]any)
		if !ok {
			// Unknown key (daemon-injected param, or a field the model added
			// that the schema doesn't describe). Ignore it.
			continue
		}
		schemaType, _ := prop["type"].(string)
		if schemaType == "" {
			continue
		}
		// A nil optional value is equivalent to it being absent; skip it.
		if raw == nil {
			continue
		}
		if !matchesSchemaType(schemaType, raw) {
			return fmt.Errorf("invalid arguments for %s: field %q must be %s", name, key, schemaType)
		}
	}

	return nil
}

// matchesSchemaType reports whether v is a Go value the handler can consume for
// the given JSON-schema type. It is deliberately lenient in three places that
// reflect how inputs actually arrive:
//
//   - integer/number accept float64 (the JSON-decoded form) as well as int and
//     int64 (the forms Go-constructed test inputs and the timeout path use).
//     Fractional floats are accepted; handlers truncate via int(v).
//   - array accepts []any and []string, and also a bare string, because some
//     array params (glob_files pattern/path) are coerced from a scalar by the
//     handler via toStringList.
//   - element types of arrays and fields of objects are not inspected; only the
//     container kind is checked.
func matchesSchemaType(schemaType string, v any) bool {
	switch schemaType {
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "integer", "number":
		switch v.(type) {
		case float64, int, int64:
			return true
		}
		return false
	case "array":
		switch v.(type) {
		case []any, []string, string:
			return true
		}
		return false
	case "object":
		_, ok := v.(map[string]any)
		return ok
	default:
		// Unknown schema type: don't block on it.
		return true
	}
}
