package brain

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/get-vix/vix/internal/daemon/brain/lsp"
)

// benchQuiet silences the stdlib logger for the duration of a benchmark so log
// lines neither pollute the benchstat-parseable output nor skew timings. The
// previous writer is restored afterwards.
func benchQuiet(tb testing.TB) {
	tb.Helper()
	prev := log.Writer()
	log.SetOutput(io.Discard)
	tb.Cleanup(func() { log.SetOutput(prev) })
}

// Performance benchmarks for the code-analysis engine's hot paths.
//
// Disk benchmarks (ScanProject) run against the real generated corpora located
// via the VIX_PERF_CORPUS env var (see cmd/perftool gen-corpus); they skip
// themselves when the corpus is absent so `go test ./...` stays green without
// it. The model is not involved here — these are pure disk + CPU paths.

// benchLanguages seeds the ext→language map so ScanProject classifies the
// synthetic corpus files. InitLanguageMapFromConfigs is one-shot; in the
// dedicated perf run (only benchmarks execute) this call wins.
func benchLanguages() {
	InitLanguageMapFromConfigs([]lsp.LanguageConfig{
		{Name: "go", Extensions: []string{".go"}},
		{Name: "python", Extensions: []string{".py"}},
		{Name: "javascript", Extensions: []string{".js"}},
		{Name: "typescript", Extensions: []string{".ts"}},
	})
}

// corpusDir resolves <VIX_PERF_CORPUS>/<name>, or "" when unavailable.
func corpusDir(name string) string {
	root := os.Getenv("VIX_PERF_CORPUS")
	if root == "" {
		return ""
	}
	dir := filepath.Join(root, name)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return dir
}

func BenchmarkScanProject(b *testing.B) {
	benchQuiet(b)
	benchLanguages()
	if LanguageForExt(".go") == "" {
		b.Skip("language map not seeded with .go — run in the dedicated perf run")
	}
	for _, name := range []string{"small", "big", "many"} {
		b.Run(name, func(b *testing.B) {
			dir := corpusDir(name)
			if dir == "" {
				b.Skipf("corpus %q missing — run `make perf-corpus`", name)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				files := ScanProject(dir)
				if len(files) == 0 {
					b.Fatalf("scanned 0 files in %s", dir)
				}
			}
		})
	}
}

// goSourceWithImports builds a synthetic Go source string with n import lines,
// used for the in-memory import-extraction benchmark (no disk, no LSP).
func goSourceWithImports(n int) string {
	var b strings.Builder
	b.WriteString("package sample\n\nimport (\n")
	for i := 0; i < n; i++ {
		b.WriteString("\t\"github.com/example/module/pkg")
		b.WriteByte(byte('a' + i%26))
		b.WriteString("\"\n")
	}
	b.WriteString(")\n\nfunc F() {}\n")
	return b.String()
}

func BenchmarkExtractFileImports(b *testing.B) {
	benchQuiet(b)
	source := goSourceWithImports(50)
	paths := map[string]bool{}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		imports := ExtractFileImports(source, "sample.go", paths, "/root", "go")
		if len(imports) == 0 {
			b.Fatal("expected imports")
		}
	}
}

// --- smoke tests: run the benchmark bodies once under `go test` ---

func TestScanProject_Smoke(t *testing.T) {
	benchLanguages()
	for _, name := range []string{"small", "big", "many"} {
		dir := corpusDir(name)
		if dir == "" {
			continue // corpus not generated in this environment
		}
		if files := ScanProject(dir); len(files) == 0 {
			t.Errorf("ScanProject(%s) returned 0 files", dir)
		}
	}
}

func TestExtractFileImports_Smoke(t *testing.T) {
	imports := ExtractFileImports(goSourceWithImports(5), "sample.go", map[string]bool{}, "/root", "go")
	if len(imports) != 5 {
		t.Errorf("ExtractFileImports returned %d imports, want 5", len(imports))
	}
}
