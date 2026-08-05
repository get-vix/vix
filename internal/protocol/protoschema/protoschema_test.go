package protoschema

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/get-vix/vix/internal/protocol"
)

// committedSchemaPath is the golden file relative to this package directory.
const committedSchemaPath = "../schema/vix-protocol.schema.json"

// TestSchemaNotStale fails when the committed schema drifts from the generator
// output — the drift gate that keeps downstream (Swift) clients honest. Run
// `make proto-schema` and commit the result to fix.
func TestSchemaNotStale(t *testing.T) {
	got, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want, err := os.ReadFile(committedSchemaPath)
	if err != nil {
		t.Fatalf("read committed schema (%s): %v — run `make proto-schema`", committedSchemaPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("committed schema is stale (%s differs from generator output).\n"+
			"Run `make proto-schema` and commit the regenerated file.", filepath.Clean(committedSchemaPath))
	}
}

// committedSwiftPath is the generated Swift models file, relative to this
// package directory (internal/protocol/protoschema → repo root → apps/…).
const committedSwiftPath = "../../../apps/vix-mac/Sources/VixProtocol/Generated.swift"

// TestSwiftNotStale keeps the committed Swift models in lockstep with the Go
// structs: it fails when apps/vix-mac/.../Generated.swift drifts from the Swift
// emitter output. This runs in the normal `go test` loop, so a protocol change
// that forgets `make mac-models` is caught without a separate Swift CI job. Run
// `make mac-models` and commit to fix.
func TestSwiftNotStale(t *testing.T) {
	got, err := GenerateSwift()
	if err != nil {
		t.Fatalf("GenerateSwift: %v", err)
	}
	want, err := os.ReadFile(committedSwiftPath)
	if err != nil {
		t.Fatalf("read committed Swift models (%s): %v — run `make mac-models`", committedSwiftPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("committed Swift models are stale (%s differs from generator output).\n"+
			"Run `make mac-models` and commit the regenerated file.", filepath.Clean(committedSwiftPath))
	}
}

// TestRoundTrip marshals a zero value of every registered payload and validates
// it against that type's generated $def, proving the schema actually matches the
// JSON the Go structs emit (no missing property, no type mismatch).
func TestRoundTrip(t *testing.T) {
	b, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal generated schema: %v", err)
	}
	defs, _ := doc["$defs"].(map[string]any)
	if defs == nil {
		t.Fatal("generated schema has no $defs")
	}

	check := func(kind string, reg map[string]any) {
		for disc, zero := range reg {
			if zero == nil {
				continue
			}
			t.Run(kind+"/"+disc, func(t *testing.T) {
				raw, err := json.Marshal(zero)
				if err != nil {
					t.Fatalf("marshal zero value: %v", err)
				}
				var inst any
				if err := json.Unmarshal(raw, &inst); err != nil {
					t.Fatalf("unmarshal instance: %v", err)
				}
				name := reflect.TypeOf(zero).Name()
				sch, ok := defs[name].(map[string]any)
				if !ok {
					t.Fatalf("no $def for payload type %q", name)
				}
				if err := validate(inst, sch, defs, name); err != nil {
					t.Fatalf("%s does not validate against its schema: %v", name, err)
				}
			})
		}
	}
	check("event", protocol.EventTypes)
	check("command", protocol.CommandTypes)
	check("rpc", protocol.RPCTypes)
}
