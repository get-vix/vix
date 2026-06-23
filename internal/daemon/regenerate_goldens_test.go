//go:build cgo

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRegenerateGoldens regenerates the tmp.min.<ext> (and tmp.unmin.<ext>)
// golden files in testdata/ from the current tmp.<ext> sources. Only runs
// when REGEN_GOLDENS=1 is set. This is a dev utility — not a real test.
func TestRegenerateGoldens(t *testing.T) {
	if os.Getenv("REGEN_GOLDENS") != "1" {
		t.Skip("set REGEN_GOLDENS=1 to regenerate golden files")
	}
	exts := []string{"go", "cpp", "swift", "c", "cs", "java", "js", "py",
		"rb", "rs", "php", "sh", "ts", "html", "css", "json", "kt"}
	// Formatter per ext (matches TestUnminifyWithTreeSitter* tests).
	formatters := map[string]struct {
		cmd  string
		args []string
	}{
		"go":    {"gofmt", []string{"-w"}},
		"cpp":   {"clang-format", []string{"-i"}},
		"c":     {"clang-format", []string{"-i"}},
		"rs":    {"rustfmt", nil},
		"py":    {"black", []string{"-q"}},
		"js":    {"prettier", []string{"--write"}},
		"ts":    {"prettier", []string{"--write"}},
		"java":  {"google-java-format", []string{"--replace"}},
		"kt":    {"ktfmt", nil},
		"swift": {"swiftformat", nil},
		"cs":    {"csharpier", []string{"format"}},
		"rb":    {"rubyfmt", []string{"-i"}},
		"php":   {"php-cs-fixer", []string{"fix", "--rules=@PSR12"}},
		"sh":    {"shfmt", []string{"-w"}},
		"html":  {"prettier", []string{"--write"}},
		"css":   {"prettier", []string{"--write"}},
		"json":  {"prettier", []string{"--write"}},
	}
	for _, ext := range exts {
		srcPath := filepath.Join("testdata", "tmp."+ext)
		minPath := filepath.Join("testdata", "tmp.min."+ext)
		unminPath := filepath.Join("testdata", "tmp.unmin."+ext)
		src, err := os.ReadFile(srcPath)
		if err != nil {
			t.Errorf("read %s: %v", srcPath, err)
			continue
		}
		out, err := minifyWithTreeSitter(string(src), "tmp."+ext, true)
		if err != nil {
			t.Errorf("minify %s: %v", srcPath, err)
			continue
		}
		if out == "" {
			t.Errorf("empty minify output for %s", srcPath)
			continue
		}
		if err := os.WriteFile(minPath, []byte(out+"\n"), 0o644); err != nil {
			t.Errorf("write %s: %v", minPath, err)
			continue
		}
		t.Logf("regenerated %s", minPath)
		fc, ok := formatters[ext]
		if !ok {
			continue
		}
		if _, err := exec.LookPath(fc.cmd); err != nil {
			t.Logf("  skip unmin.%s — %s not in PATH", ext, fc.cmd)
			continue
		}
		tmpFile, err := os.CreateTemp("", "*."+ext)
		if err != nil {
			t.Errorf("mktemp: %v", err)
			continue
		}
		tmpName := tmpFile.Name()
		if _, err := tmpFile.Write([]byte(out + "\n")); err != nil {
			tmpFile.Close()
			os.Remove(tmpName)
			t.Errorf("write temp: %v", err)
			continue
		}
		tmpFile.Close()
		args := append(fc.args, tmpName)
		cmd := exec.Command(fc.cmd, args...)
		if fmtOut, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("  %s failed on %s: %v\n%s", fc.cmd, ext, err, fmtOut)
			os.Remove(tmpName)
			continue
		}
		formatted, err := os.ReadFile(tmpName)
		os.Remove(tmpName)
		if err != nil {
			t.Errorf("read formatted: %v", err)
			continue
		}
		if err := os.WriteFile(unminPath, formatted, 0o644); err != nil {
			t.Errorf("write %s: %v", unminPath, err)
			continue
		}
		t.Logf("regenerated %s", unminPath)
	}
}

// TestIdempotencyDiff shows where re-minification diverges from the input.
// Set DIFF_EXT=<ext> to run. Set DIFF_BYTE=<N> to also dump tokens near byte N.
func TestIdempotencyDiff(t *testing.T) {
	ext := os.Getenv("DIFF_EXT")
	if ext == "" {
		t.Skip("set DIFF_EXT=<ext> to run")
	}
	data, err := os.ReadFile(filepath.Join("testdata", "tmp.min."+ext))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	input := string(data)
	for strings.HasSuffix(input, "\n") || strings.HasSuffix(input, " ") {
		input = input[:len(input)-1]
	}
	if tb := os.Getenv("DIFF_BYTE"); tb != "" {
		var target uint
		fmt.Sscanf(tb, "%d", &target)
		parser := newParserForFile("tmp." + ext)
		defer parser.Close()
		tree := parser.Parse([]byte(input), nil)
		defer tree.Close()
		var tokens []minifyToken
		collectLeaves(tree.RootNode(), []byte(input), &tokens, true)
		annotateComments(tokens)
		switch "." + ext {
		case ".go":
			annotateGo(tokens)
		case ".swift":
			annotateSwift(tokens)
		case ".kt", ".kts":
			annotateKotlin(tokens)
		case ".sh", ".bash":
			annotateBash(tokens, []byte(input))
		case ".rb":
			annotateRuby(tokens)
		}
		if usesSemicolonSeparator("." + ext) {
			preserveSemicolons(tokens, []byte(input), "."+ext)
		}
		for i := range tokens {
			if tokens[i].byteStart+20 >= target && tokens[i].byteStart <= target+20 {
				t.Logf("tok[%d] text=%q byte=%d-%d sep=%q", i, tokens[i].text, tokens[i].byteStart, tokens[i].byteEnd, tokens[i].separator)
			}
		}
	}
	out, err := minifyWithTreeSitter(input, "tmp."+ext, true)
	if err != nil {
		t.Logf("minify error: %v", err)
	}
	a := []byte(input)
	b := []byte(out)
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	firstDiff := -1
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			firstDiff = i
			break
		}
	}
	t.Logf("len(a)=%d len(b)=%d firstDiff=%d", len(a), len(b), firstDiff)
	if firstDiff >= 0 {
		start := firstDiff - 40
		if start < 0 {
			start = 0
		}
		endA := firstDiff + 80
		if endA > len(a) {
			endA = len(a)
		}
		endB := firstDiff + 80
		if endB > len(b) {
			endB = len(b)
		}
		t.Logf("INPUT  : %q", string(a[start:endA]))
		t.Logf("OUTPUT : %q", string(b[start:endB]))
	} else if len(a) != len(b) {
		minLen := len(a)
		if len(b) < minLen {
			minLen = len(b)
		}
		start := minLen - 40
		if start < 0 {
			start = 0
		}
		t.Logf("TRUNCATED tail:")
		t.Logf("INPUT  : %q", string(a[start:]))
		t.Logf("OUTPUT : %q", string(b[start:]))
	}
}

var _ = fmt.Sprintf
var _ = exec.LookPath
