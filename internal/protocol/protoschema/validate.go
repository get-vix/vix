package protoschema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/get-vix/vix/internal/protocol"
)

// This file exposes runtime validation of live wire messages against the
// generated schema, so integration tests (and, if useful, the daemon itself)
// can assert that what actually travels the socket conforms to the contract the
// Swift client depends on.

var (
	defsOnce   sync.Once
	cachedDefs map[string]any
)

// schemaDefs returns the schema's $defs table normalized to the generic JSON
// shape (map[string]any / []any / float64) so $ref resolution and validation
// see a uniform structure regardless of how the doc was built.
func schemaDefs() map[string]any {
	defsOnce.Do(func() {
		b, _ := json.Marshal(buildDoc()["$defs"])
		var d map[string]any
		_ = json.Unmarshal(b, &d)
		cachedDefs = d
	})
	return cachedDefs
}

// ValidateEvent checks that a daemon→client event's data payload conforms to the
// generated schema for its discriminator. A nil data (payload-less event such as
// event.agent_done) is accepted only for events registered without a payload.
func ValidateEvent(discriminator string, data any) error {
	return validatePayload(protocol.EventTypes, "event", discriminator, data)
}

// ValidateCommand is the client→daemon analog of ValidateEvent.
func ValidateCommand(discriminator string, data any) error {
	return validatePayload(protocol.CommandTypes, "command", discriminator, data)
}

// ValidateRPC checks that an RPC projection value (keyed by type name, e.g.
// "ThreadSummary") conforms to the generated schema.
func ValidateRPC(typeName string, data any) error {
	zero, ok := protocol.RPCTypes[typeName]
	if !ok {
		return fmt.Errorf("unknown RPC type %q", typeName)
	}
	defs := schemaDefs()
	sch, ok := defs[reflect.TypeOf(zero).Name()].(map[string]any)
	if !ok {
		return fmt.Errorf("no schema $def for %q", typeName)
	}
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal RPC payload: %w", err)
	}
	var inst any
	if err := json.Unmarshal(b, &inst); err != nil {
		return fmt.Errorf("normalize RPC payload: %w", err)
	}
	return validate(inst, sch, defs, typeName)
}

func validatePayload(reg map[string]any, kind, discriminator string, data any) error {
	zero, ok := reg[discriminator]
	if !ok {
		return fmt.Errorf("unknown %s discriminator %q", kind, discriminator)
	}
	if zero == nil {
		// Payload-less message: data must be null/absent on the wire.
		if data != nil {
			return fmt.Errorf("%s %q must carry null data, got %T", kind, discriminator, data)
		}
		return nil
	}
	name := reflect.TypeOf(zero).Name()
	defs := schemaDefs()
	sch, ok := defs[name].(map[string]any)
	if !ok {
		return fmt.Errorf("no schema $def for %q", name)
	}
	// Normalize the instance to generic JSON so validate sees the same shapes.
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", kind, err)
	}
	var inst any
	if err := json.Unmarshal(b, &inst); err != nil {
		return fmt.Errorf("normalize %s payload: %w", kind, err)
	}
	return validate(inst, sch, defs, discriminator)
}

// validate is a minimal JSON-Schema checker covering exactly the constructs the
// emitter produces: $ref, object (properties/required/additionalProperties),
// array (items), and the scalar types. null satisfies any schema (fields are
// treated as nullable), which is sufficient for smoke/conformance checking;
// strictness of the schema itself is guarded by the staleness golden test.
func validate(inst any, sch, defs map[string]any, path string) error {
	if ref, ok := sch["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		d, ok := defs[name].(map[string]any)
		if !ok {
			return fmt.Errorf("%s: dangling $ref %q", path, ref)
		}
		return validate(inst, d, defs, path)
	}
	if inst == nil {
		return nil
	}
	switch typ, _ := sch["type"].(string); typ {
	case "object":
		m, ok := inst.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected object, got %T", path, inst)
		}
		if req, ok := sch["required"].([]any); ok {
			for _, r := range req {
				key, _ := r.(string)
				if _, present := m[key]; !present {
					return fmt.Errorf("%s: missing required property %q", path, key)
				}
			}
		}
		props, _ := sch["properties"].(map[string]any)
		ap, _ := sch["additionalProperties"].(map[string]any)
		for k, v := range m {
			if ps, ok := props[k].(map[string]any); ok {
				if err := validate(v, ps, defs, path+"."+k); err != nil {
					return err
				}
			} else if ap != nil {
				if err := validate(v, ap, defs, path+"."+k); err != nil {
					return err
				}
			}
		}
	case "array":
		arr, ok := inst.([]any)
		if !ok {
			return fmt.Errorf("%s: expected array, got %T", path, inst)
		}
		if items, ok := sch["items"].(map[string]any); ok {
			for i, el := range arr {
				if err := validate(el, items, defs, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case "string":
		if _, ok := inst.(string); !ok {
			return fmt.Errorf("%s: expected string, got %T", path, inst)
		}
	case "integer", "number":
		if _, ok := inst.(float64); !ok {
			return fmt.Errorf("%s: expected number, got %T", path, inst)
		}
	case "boolean":
		if _, ok := inst.(bool); !ok {
			return fmt.Errorf("%s: expected boolean, got %T", path, inst)
		}
	case "":
		// No type constraint (any / json.RawMessage / free-form object values).
	}
	return nil
}
