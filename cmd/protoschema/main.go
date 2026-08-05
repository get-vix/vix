// Command protoschema regenerates the machine-readable vix daemon⇄client
// protocol from the Go structs registered in internal/protocol.
//
// Usage (from the repo root):
//
//	go run ./cmd/protoschema                 # write the JSON schema
//	go run ./cmd/protoschema -lang swift     # write the Swift Codable models
//	go run ./cmd/protoschema -out -          # write to stdout
//
// The `make proto-schema` and `make mac-models` targets wrap these. Drift tests
// in internal/protocol/protoschema fail when a committed output is stale, so run
// the relevant target after changing any protocol payload struct and commit the
// regenerated file(s).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/get-vix/vix/internal/protocol/protoschema"
)

// defaultSchemaPath and defaultSwiftPath are repo-root-relative; the tool is
// meant to be run via `go run ./cmd/protoschema` from the repository root.
const (
	defaultSchemaPath = "internal/protocol/schema/vix-protocol.schema.json"
	defaultSwiftPath  = "apps/vix-mac/Sources/VixProtocol/Generated.swift"
)

func main() {
	lang := flag.String("lang", "schema", "output to generate: schema | swift")
	out := flag.String("out", "", "output path; \"-\" for stdout (default depends on -lang)")
	flag.Parse()

	var (
		b       []byte
		err     error
		defPath string
	)
	switch *lang {
	case "schema":
		b, err = protoschema.Generate()
		defPath = defaultSchemaPath
	case "swift":
		b, err = protoschema.GenerateSwift()
		defPath = defaultSwiftPath
	default:
		fmt.Fprintf(os.Stderr, "protoschema: unsupported -lang %q (want \"schema\" or \"swift\")\n", *lang)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "protoschema: generate %s: %v\n", *lang, err)
		os.Exit(1)
	}

	if *out == "-" {
		os.Stdout.Write(b)
		return
	}
	path := *out
	if path == "" {
		path = defPath
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "protoschema: write %s: %v\n", path, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "protoschema: wrote %s\n", path)
}
