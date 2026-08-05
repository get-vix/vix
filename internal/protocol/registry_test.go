package protocol_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/get-vix/vix/internal/protocol"
)

// nonWireTypes are struct types whose names match the wire-payload naming
// convention (Event*/*Data) but which are NOT top-level messages with their own
// discriminator — they are nested/helper types embedded in other payloads. They
// are exempt from the exhaustiveness check.
var nonWireTypes = map[string]bool{
	"EventQuestionOption": true, // nested in EventUserQuestion.rich_options
}

// TestRegistryConsistency guards the structural invariants of the registry:
// no discriminator is registered as both an event and a command, names carry
// the right prefix, and every non-nil payload is a struct.
func TestRegistryConsistency(t *testing.T) {
	for name := range protocol.EventTypes {
		if _, dup := protocol.CommandTypes[name]; dup {
			t.Errorf("discriminator %q registered as both an event and a command", name)
		}
		if !strings.HasPrefix(name, "event.") {
			t.Errorf("event discriminator %q must start with \"event.\"", name)
		}
	}
	for name := range protocol.CommandTypes {
		if strings.HasPrefix(name, "event.") {
			t.Errorf("command discriminator %q must not start with \"event.\"", name)
		}
	}
	for name, v := range protocol.EventTypes {
		if v != nil && reflect.TypeOf(v).Kind() != reflect.Struct {
			t.Errorf("event %q payload must be a struct, got %T", name, v)
		}
	}
	for name, v := range protocol.CommandTypes {
		if v != nil && reflect.TypeOf(v).Kind() != reflect.Struct {
			t.Errorf("command %q payload must be a struct, got %T", name, v)
		}
	}
}

// TestRegistryExhaustive scans the protocol package source for struct types that
// look like wire payloads (Event*/*Data) and fails if any is missing from the
// registry — catching a newly-added event/command that was never wired into the
// schema. Genuine nested helpers must be listed in nonWireTypes.
func TestRegistryExhaustive(t *testing.T) {
	registered := map[string]bool{}
	for _, v := range protocol.EventTypes {
		if v != nil {
			registered[reflect.TypeOf(v).Name()] = true
		}
	}
	for _, v := range protocol.CommandTypes {
		if v != nil {
			registered[reflect.TypeOf(v).Name()] = true
		}
	}
	rpcRegistered := map[string]bool{}
	for _, v := range protocol.RPCTypes {
		rpcRegistered[reflect.TypeOf(v).Name()] = true
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse protocol package: %v", err)
	}

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if _, ok := ts.Type.(*ast.StructType); !ok {
						continue
					}
					name := ts.Name.Name

					// RPC projection types (*Summary) must be in RPCTypes.
					if strings.HasSuffix(name, "Summary") {
						if !rpcRegistered[name] {
							t.Errorf("struct %q looks like an RPC projection (*Summary) but is absent from "+
								"protocol.RPCTypes — add it to registry.go", name)
						}
						continue
					}

					looksWire := strings.HasPrefix(name, "Event") || strings.HasSuffix(name, "Data")
					if !looksWire || nonWireTypes[name] {
						continue
					}
					if !registered[name] {
						t.Errorf("struct %q looks like a wire payload (Event*/*Data) but is absent from "+
							"protocol.EventTypes/CommandTypes — add it to registry.go (or to nonWireTypes if it is a nested helper)", name)
					}
				}
			}
		}
	}
}
