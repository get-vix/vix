// Package protoschema generates a JSON Schema describing the vix daemon⇄client
// wire protocol by reflecting over the payload structs registered in
// internal/protocol (protocol.EventTypes / protocol.CommandTypes).
//
// The generated schema is the machine-readable contract that downstream clients
// (e.g. the native macOS app under apps/vix-mac) consume to stay in lockstep
// with the Go structs. cmd/protoschema writes it to
// internal/protocol/schema/vix-protocol.schema.json; a test in this package
// fails when the committed file drifts from the generator output.
package protoschema

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/get-vix/vix/internal/protocol"
)

// SchemaURI is the JSON Schema dialect the generated document declares.
const SchemaURI = "https://json-schema.org/draft/2020-12/schema"

// rawMessageType is used to special-case json.RawMessage (an opaque JSON blob)
// so the walker emits "any JSON" rather than a base64 byte-array string.
var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// generator accumulates named-struct schemas into a shared $defs table while
// walking the registered payload types.
type generator struct {
	defs map[string]any
}

// Generate builds the full protocol schema and returns it as indented JSON with
// a trailing newline. Output is deterministic (encoding/json sorts map keys),
// so it is safe to diff against a committed golden file.
func Generate() ([]byte, error) {
	b, err := json.MarshalIndent(buildDoc(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// buildDoc reflects over the registered payload types and returns the schema
// document as raw Go maps. Both the JSON emitter (Generate) and the Swift
// emitter (GenerateSwift) consume this, so the two outputs derive from the same
// walk and cannot diverge.
func buildDoc() map[string]any {
	g := &generator{defs: map[string]any{}}

	return map[string]any{
		"$schema":     SchemaURI,
		"$id":         "https://getvix.dev/schema/vix-protocol.schema.json",
		"title":       "Vix daemon protocol",
		"description": "Generated from internal/protocol Go structs by cmd/protoschema. DO NOT EDIT — run `make proto-schema`.",
		"envelopes": map[string]any{
			"command": g.schemaFor(reflect.TypeOf(protocol.ThreadCommand{})),
			"event":   g.schemaFor(reflect.TypeOf(protocol.ThreadEvent{})),
		},
		"commands": g.section(protocol.CommandTypes),
		"events":   g.section(protocol.EventTypes),
		"rpc":      g.rpcSection(protocol.RPCTypes),
		"$defs":    g.defs,
	}
}

// rpcSection maps each RPC projection type name to its schema $ref. Unlike
// section() these are keyed by type name (not a wire discriminator) and always
// carry a payload.
func (g *generator) rpcSection(reg map[string]any) map[string]any {
	out := make(map[string]any, len(reg))
	for name, zero := range reg {
		out[name] = g.schemaFor(reflect.TypeOf(zero))
	}
	return out
}

// section maps each discriminator in a registry to its payload schema. A nil
// registry value (payload-less message) becomes {"payload": null}.
func (g *generator) section(reg map[string]any) map[string]any {
	out := make(map[string]any, len(reg))
	for name, zero := range reg {
		if zero == nil {
			out[name] = map[string]any{"payload": nil}
			continue
		}
		out[name] = map[string]any{"payload": g.schemaFor(reflect.TypeOf(zero))}
	}
	return out
}

// schemaFor returns a JSON Schema node for a Go type. Named structs are hoisted
// into $defs and referenced via $ref so recursive/shared types are emitted once.
func (g *generator) schemaFor(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// json.RawMessage — opaque embedded JSON (e.g. ThreadWorkflowData.Workflow).
	if t == rawMessageType {
		return map[string]any{"description": "arbitrary JSON value"}
	}

	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			// []byte marshals to a base64 string in JSON.
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{"type": "array", "items": g.schemaFor(t.Elem())}
	case reflect.Map:
		obj := map[string]any{"type": "object"}
		if t.Elem().Kind() != reflect.Interface {
			obj["additionalProperties"] = g.schemaFor(t.Elem())
		}
		return obj
	case reflect.Interface:
		// any — no type constraint.
		return map[string]any{}
	case reflect.Struct:
		name := t.Name()
		if name == "" {
			return g.objectSchema(t)
		}
		if _, seen := g.defs[name]; !seen {
			g.defs[name] = map[string]any{} // reserve to break recursion
			g.defs[name] = g.objectSchema(t)
		}
		return map[string]any{"$ref": "#/$defs/" + name}
	default:
		return map[string]any{}
	}
}

// objectSchema builds an object schema from a struct's exported, JSON-visible
// fields. A field is required iff its json tag lacks `omitempty`.
func (g *generator) objectSchema(t reflect.Type) map[string]any {
	props := map[string]any{}
	var required []string

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := f.Name
		var opts string
		if tag != "" {
			parts := strings.SplitN(tag, ",", 2)
			if parts[0] != "" {
				name = parts[0]
			}
			if len(parts) > 1 {
				opts = parts[1]
			}
		}
		props[name] = g.schemaFor(f.Type)
		if !hasOption(opts, "omitempty") {
			required = append(required, name)
		}
	}

	sort.Strings(required)
	obj := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		obj["required"] = required
	}
	return obj
}

// hasOption reports whether a comma-separated json-tag option list contains opt.
func hasOption(opts, opt string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == opt {
			return true
		}
	}
	return false
}
