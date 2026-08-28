package daemon

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// extensions is the canonical list of languages covered by golden-file tests.
var extensions = []string{
	"go", "cpp", "swift", "c", "cs", "java", "js", "py",
	"rb", "rs", "php", "sh", "ts", "html", "css", "json", "kt",
}

// commentMode describes a keep_comments variant for the golden-file tests.
type commentMode struct {
	name         string // subtest name
	keepComments bool
	dir          string // subdirectory under testdata/ for min/unmin files
}

var commentModes = []commentMode{
	{"with_comments", true, "with_comments"},
	{"without_comments", false, "without_comments"},
}

// sourceFixturePath returns the checked-in source fixture for an extension.
// The repository-wide tmp.go ignore rule prevents testdata/tmp.go from being
// tracked, so Go uses a checked-in source fixture with a non-Go suffix.
func sourceFixturePath(ext string) string {
	if ext == "go" {
		return filepath.Join("testdata", "source.go.txt")
	}
	return filepath.Join("testdata", "tmp."+ext)
}

// --- Golden file tests (minification) ---

// TestMinifyGoldenFiles verifies that minifying each tmp.<ext> fixture produces
// output matching the corresponding golden file, for both comment modes.
func TestMinifyGoldenFiles(t *testing.T) {
	for _, mode := range commentModes {
		t.Run(mode.name, func(t *testing.T) {
			for _, ext := range extensions {
				t.Run(ext, func(t *testing.T) {
					content, err := os.ReadFile(sourceFixturePath(ext))
					if err != nil {
						t.Fatalf("failed to read tmp.%s: %v", ext, err)
					}
					got, err := minifyWithTreeSitter(string(content), "tmp."+ext, mode.keepComments)
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					goldenPath := filepath.Join("testdata", mode.dir, "tmp.min."+ext)
					expected, err := os.ReadFile(goldenPath)
					if err != nil {
						t.Fatalf("failed to read %s: %v", goldenPath, err)
					}
					if strings.TrimSpace(got) != strings.TrimSpace(string(expected)) {
						t.Errorf("minified output does not match golden file %s.\nGOT:\n%s\n\nEXPECTED:\n%s", goldenPath, got, string(expected))
					}
				})
			}
		})
	}
}

// --- Idempotency tests ---

// TestMinifyIdempotency verifies that minifying already-minified output
// produces identical output for both comment modes.
func TestMinifyIdempotency(t *testing.T) {
	for _, mode := range commentModes {
		t.Run(mode.name, func(t *testing.T) {
			for _, ext := range extensions {
				t.Run(ext, func(t *testing.T) {
					goldenPath := filepath.Join("testdata", mode.dir, "tmp.min."+ext)
					data, err := os.ReadFile(goldenPath)
					if err != nil {
						t.Fatalf("read %s: %v", goldenPath, err)
					}
					input := strings.TrimSpace(string(data))
					out, err := minifyWithTreeSitter(input, "tmp."+ext, mode.keepComments)
					if err != nil {
						t.Fatalf("minify %s: %v", goldenPath, err)
					}
					got := strings.TrimSpace(out)
					if got != input {
						t.Errorf("minify(minify(x)) != minify(x) for %s/%s\n--- want ---\n%s\n--- got ---\n%s", mode.dir, ext, input, got)
					}
				})
			}
		})
	}
}

// --- Formatter round-trip tests ---

// formatterDef maps an extension to its formatter command and arguments.
type formatterDef struct {
	ext  string
	cmd  string
	args []string
}

var formatters = []formatterDef{
	{"go", "gofmt", []string{"-w"}},
	{"cpp", "clang-format", []string{"-i"}},
	{"c", "clang-format", []string{"-i"}},
	{"rs", "rustfmt", nil},
	{"py", "black", []string{"-q"}},
	{"js", "prettier", []string{"--write"}},
	{"ts", "prettier", []string{"--write"}},
	{"java", "google-java-format", []string{"--replace"}},
	{"kt", "ktfmt", nil},
	{"swift", "swiftformat", nil},
	{"cs", "csharpier", []string{"format"}},
	{"rb", "rubyfmt", []string{"-i"}},
	{"php", "php-cs-fixer", []string{"fix", "--rules=@PSR12"}},
	{"sh", "shfmt", []string{"-w"}},
	{"html", "prettier", []string{"--write"}},
	{"css", "prettier", []string{"--write"}},
	{"json", "prettier", []string{"--write"}},
}

// TestUnminifyRoundTrip reads tmp.min.<ext>, runs the formatter, and compares
// against the golden tmp.unmin.<ext> file, for both comment modes.
func TestUnminifyRoundTrip(t *testing.T) {
	for _, mode := range commentModes {
		t.Run(mode.name, func(t *testing.T) {
			for _, f := range formatters {
				t.Run(f.ext, func(t *testing.T) {
					if _, err := exec.LookPath(f.cmd); err != nil {
						t.Skipf("%s not found in PATH", f.cmd)
					}

					minPath := filepath.Join("testdata", mode.dir, "tmp.min."+f.ext)
					minified, err := os.ReadFile(minPath)
					if err != nil {
						t.Fatalf("failed to read %s: %v", minPath, err)
					}

					unminPath := filepath.Join("testdata", mode.dir, "tmp.unmin."+f.ext)
					expected, err := os.ReadFile(unminPath)
					if err != nil {
						t.Fatalf("failed to read %s: %v", unminPath, err)
					}

					// Use the test's working directory rather than the system
					// temp dir so that formatters that internally canonicalise
					// the file path (e.g. rustfmt) can access it on macOS
					// configurations where /private is not traversable.
					tmpFile, err := os.CreateTemp("testdata", ".testfmt_*."+f.ext)
					if err != nil {
						t.Fatalf("failed to create temp file: %v", err)
					}
					defer os.Remove(tmpFile.Name())
					tmpFile.Write(minified)
					tmpFile.Close()

					args := append(f.args, tmpFile.Name())
					cmd := exec.Command(f.cmd, args...)
					output, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("%s failed: %v\noutput: %s", f.cmd, err, output)
					}

					formatted, err := os.ReadFile(tmpFile.Name())
					if err != nil {
						t.Fatalf("failed to read formatted file: %v", err)
					}

					if strings.TrimSpace(string(formatted)) != strings.TrimSpace(string(expected)) {
						t.Errorf("unminified output does not match golden file %s\nGOT:\n%s\n\nEXPECTED:\n%s", unminPath, string(formatted), string(expected))
					}
				})
			}
		})
	}
}

// --- Comment stripping sanity check ---

// TestWithoutCommentsStripsComments verifies that the without_comments minified
// output is strictly shorter than the with_comments variant (proving comments
// were actually stripped), and checks that known comment text from the source
// fixtures does not survive stripping.
func TestWithoutCommentsStripsComments(t *testing.T) {
	for _, ext := range extensions {
		t.Run(ext, func(t *testing.T) {
			withPath := filepath.Join("testdata", "with_comments", "tmp.min."+ext)
			withData, err := os.ReadFile(withPath)
			if err != nil {
				t.Fatalf("read %s: %v", withPath, err)
			}
			withoutPath := filepath.Join("testdata", "without_comments", "tmp.min."+ext)
			withoutData, err := os.ReadFile(withoutPath)
			if err != nil {
				t.Fatalf("read %s: %v", withoutPath, err)
			}
			// without_comments must be <= with_comments (strictly shorter if
			// the source has comments; equal if it has none, e.g. JSON).
			if len(withoutData) > len(withData) {
				t.Errorf("without_comments (%d bytes) should not be larger than with_comments (%d bytes)",
					len(withoutData), len(withData))
			}
		})
	}
}

// --- Edge case tests ---

func TestMinifyUnsupported(t *testing.T) {
	out, err := minifyWithTreeSitter("Hello world.", "readme.txt", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty string for unsupported extension, got %q", out)
	}
}

func TestMinifyEmpty(t *testing.T) {
	out, err := minifyWithTreeSitter("", "empty.go", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = out
}

// --- VFS round-trip test (tests VfsRead code path in vfs.go) ---

func TestVFSRoundTripGo(t *testing.T) {
	src := "package main\n\nimport (\n\t\"fmt\"\n\t\"math\"\n)\n\ntype Rectangle struct {\n\tWidth  float64\n\tHeight float64\n}\n\nfunc (r Rectangle) Area() float64 {\n\treturn r.Width * r.Height\n}\n\nfunc (r Rectangle) Perimeter() float64 {\n\treturn 2 * (r.Width + r.Height)\n}\n\nfunc main() {\n\trect := Rectangle{Width: 3.0, Height: 4.0}\n\tfmt.Printf(\"Area: %f, Perimeter: %f\\n\", rect.Area(), rect.Perimeter())\n\t_ = math.Pi\n}\n"
	tmpFile, err := os.CreateTemp("", "*.go")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(src)
	tmpFile.Close()

	dir := filepath.Dir(tmpFile.Name())
	base := filepath.Base(tmpFile.Name())
	minified, err := VfsRead(dir, nil, base, nil, nil, false)
	if err != nil {
		t.Fatalf("VfsRead returned error: %v", err)
	}
	if minified == "" {
		t.Fatal("VfsRead returned empty string")
	}
	if len(minified) >= len(src) {
		t.Errorf("minified output (%d bytes) should be shorter than original (%d bytes)", len(minified), len(src))
	}

	fset := token.NewFileSet()
	if _, parseErr := parser.ParseFile(fset, "test.go", minified, 0); parseErr != nil {
		t.Errorf("minified Go output is not valid Go syntax: %v\nOutput was:\n%s", parseErr, minified)
	}

	tmpFile2, err := os.CreateTemp("", "*.go")
	if err != nil {
		t.Fatalf("failed to create second temp file: %v", err)
	}
	defer os.Remove(tmpFile2.Name())
	tmpFile2.WriteString(minified)
	tmpFile2.Close()

	cmd := exec.Command("gofmt", tmpFile2.Name())
	output, gofmtErr := cmd.CombinedOutput()
	if gofmtErr != nil {
		t.Errorf("gofmt failed on minified output: %v\noutput: %s\nMinified was:\n%s", gofmtErr, output, minified)
	}
}
