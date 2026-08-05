package harness

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

// sourceFS holds the embedded scenario sources so the harness can capture each
// test function's body into its artifact at runtime — the report binary then
// never needs the source tree. Scenarios register it from TestMain via
// RegisterSources.
var sourceFS fs.FS

// RegisterSources is called once (from the scenarios package's TestMain) with
// an embed.FS of the scenario .go files. Optional: if unset, artifacts simply
// carry no source body and the report shows none.
func RegisterSources(f fs.FS) { sourceFS = f }

// captureSource extracts the Go source of the named function from the embedded
// scenario file (matched by basename), returning "" when unavailable.
func captureSource(sourceFile, funcName string) string {
	if sourceFS == nil {
		return ""
	}
	// Subtests carry a "/sub" suffix; the enclosing func is the first segment.
	if i := strings.IndexByte(funcName, '/'); i >= 0 {
		funcName = funcName[:i]
	}
	base := filepath.Base(sourceFile)
	data, err := fs.ReadFile(sourceFS, base)
	if err != nil {
		return ""
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, base, data, parser.ParseComments)
	if err != nil {
		return ""
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != funcName {
			continue
		}
		start := fset.Position(fn.Pos()).Offset
		end := fset.Position(fn.End()).Offset
		if start >= 0 && end <= len(data) && start < end {
			return string(data[start:end])
		}
	}
	return ""
}
