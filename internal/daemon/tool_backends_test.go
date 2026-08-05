package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewGrepRunnerDefault(t *testing.T) {
	runner := newGrepRunner("")
	if _, ok := runner.(*systemGrepBackend); !ok {
		t.Errorf("expected *systemGrepBackend, got %T", runner)
	}
}

func TestNewGrepRunnerGrep(t *testing.T) {
	runner := newGrepRunner("grep")
	if _, ok := runner.(*systemGrepBackend); !ok {
		t.Errorf("expected *systemGrepBackend, got %T", runner)
	}
}

func TestNewGrepRunnerRg(t *testing.T) {
	runner := newGrepRunner("rg")
	// Result depends on whether rg is installed; just verify it returns a valid runner
	if runner == nil {
		t.Error("expected non-nil runner")
	}
}

func TestNewGlobRunnerDefault(t *testing.T) {
	runner := newGlobRunner("")
	if _, ok := runner.(*builtinGlobBackend); !ok {
		t.Errorf("expected *builtinGlobBackend, got %T", runner)
	}
}

func TestNewGlobRunnerBuiltin(t *testing.T) {
	runner := newGlobRunner("builtin")
	if _, ok := runner.(*builtinGlobBackend); !ok {
		t.Errorf("expected *builtinGlobBackend, got %T", runner)
	}
}

func TestNewGlobRunnerFd(t *testing.T) {
	runner := newGlobRunner("fd")
	// Result depends on whether fd is installed; just verify it returns a valid runner
	if runner == nil {
		t.Error("expected non-nil runner")
	}
}

func TestBackendNames(t *testing.T) {
	if got := (&systemGrepBackend{}).Name(); got != "grep" {
		t.Errorf("systemGrepBackend.Name() = %q, want grep", got)
	}
	if got := (&rgBackend{}).Name(); got != "rg" {
		t.Errorf("rgBackend.Name() = %q, want rg", got)
	}
	if got := (&builtinGlobBackend{}).Name(); got != "builtin" {
		t.Errorf("builtinGlobBackend.Name() = %q, want builtin", got)
	}
	if got := (&fdGlobBackend{}).Name(); got != "fd" {
		t.Errorf("fdGlobBackend.Name() = %q, want fd", got)
	}
}

// TestNewRunnerNameFallback verifies the effective Name() reflects PATH fallback:
// with an empty PATH, rg/fd are unresolvable, so the runners resolve to the
// builtin/system defaults.
func TestNewRunnerNameFallback(t *testing.T) {
	t.Setenv("PATH", "")
	if got := newGrepRunner("rg").Name(); got != "grep" {
		t.Errorf("newGrepRunner(rg).Name() with empty PATH = %q, want grep", got)
	}
	if got := newGlobRunner("fd").Name(); got != "builtin" {
		t.Errorf("newGlobRunner(fd).Name() with empty PATH = %q, want builtin", got)
	}
}

// TestNewRunnerNamePresent verifies the effective Name() is the requested
// backend when the tool is on PATH. Skips the assertion when not installed.
func TestNewRunnerNamePresent(t *testing.T) {
	if _, err := exec.LookPath("rg"); err == nil {
		if got := newGrepRunner("rg").Name(); got != "rg" {
			t.Errorf("newGrepRunner(rg).Name() = %q, want rg", got)
		}
	}
	if _, err := exec.LookPath("fd"); err == nil {
		if got := newGlobRunner("fd").Name(); got != "fd" {
			t.Errorf("newGlobRunner(fd).Name() = %q, want fd", got)
		}
	}
}

func TestLoadToolsConfigMissing(t *testing.T) {
	cfg := loadToolsConfig([]string{"/nonexistent/path/settings.json"})
	if cfg.Grep.Backend != "" || cfg.Glob.Backend != "" {
		t.Errorf("expected empty defaults, got grep=%q glob=%q", cfg.Grep.Backend, cfg.Glob.Backend)
	}
}

func TestLoadToolsConfigValid(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".vix")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{"tools": {"grep": {"backend": "rg"}, "glob": {"backend": "fd"}}}`
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := loadToolsConfig([]string{filepath.Join(configDir, "settings.json")})
	if cfg.Grep.Backend != "rg" {
		t.Errorf("expected grep backend 'rg', got %q", cfg.Grep.Backend)
	}
	if cfg.Glob.Backend != "fd" {
		t.Errorf("expected glob backend 'fd', got %q", cfg.Glob.Backend)
	}
}

func TestLoadToolsConfigNoToolsSection(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".vix")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{"lsp": {}}`
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := loadToolsConfig([]string{filepath.Join(configDir, "settings.json")})
	if cfg.Grep.Backend != "" || cfg.Glob.Backend != "" {
		t.Errorf("expected empty defaults, got grep=%q glob=%q", cfg.Grep.Backend, cfg.Glob.Backend)
	}
}

func TestSystemGrepBackendArgs(t *testing.T) {
	backend := &systemGrepBackend{}
	// Test with a pattern that won't match anything in a temp dir
	dir := t.TempDir()
	result, err := backend.Run(context.Background(), "nonexistent_pattern_xyz", ".", "", dir)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "(no matches)" {
		t.Errorf("expected '(no matches)', got %q", result)
	}
}

func TestBuiltinGlobBackendNoMatches(t *testing.T) {
	backend := &builtinGlobBackend{}
	dir := t.TempDir()
	result, err := backend.Run(context.Background(), []string{"*.nonexistent_ext_xyz"}, nil, dir, "", false, 1000)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "(no matches)" {
		t.Errorf("expected '(no matches)', got %q", result)
	}
}

func TestBuiltinGlobBackendWithMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := &builtinGlobBackend{}
	result, err := backend.Run(context.Background(), []string{"*.txt"}, nil, dir, "", false, 1000)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result == "(no matches)" {
		t.Error("expected matches, got '(no matches)'")
	}
}

func TestBuiltinGlobBackendMultiplePatterns(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.md", "c.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	backend := &builtinGlobBackend{}
	result, err := backend.Run(context.Background(), []string{"*.txt", "*.md"}, nil, dir, "", true, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "a.txt") || !strings.Contains(result, "b.md") {
		t.Errorf("expected both a.txt and b.md in output, got %q", result)
	}
	if strings.Contains(result, "c.go") {
		t.Errorf("did not expect c.go in output, got %q", result)
	}
}

func TestBuiltinGlobBackendMultiplePaths(t *testing.T) {
	root := t.TempDir()
	subA := filepath.Join(root, "a")
	subB := filepath.Join(root, "b")
	for _, d := range []string{subA, subB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "file.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	backend := &builtinGlobBackend{}
	result, err := backend.Run(context.Background(), []string{"*.txt"}, []string{subA, subB}, root, "", true, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, filepath.Join(subA, "file.txt")) {
		t.Errorf("missing subA match in %q", result)
	}
	if !strings.Contains(result, filepath.Join(subB, "file.txt")) {
		t.Errorf("missing subB match in %q", result)
	}
}

func TestBuiltinGlobBackendDedup(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := &builtinGlobBackend{}
	// Same match reachable via two overlapping patterns.
	result, err := backend.Run(context.Background(), []string{"*.txt", "file.*"}, []string{dir}, dir, "", true, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 deduped line, got %d: %v", len(lines), lines)
	}
}

func TestFdGlobBackendMultiplePatterns(t *testing.T) {
	if _, err := exec.LookPath("fd"); err != nil {
		t.Skip("fd not installed")
	}
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.md", "c.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	backend := &fdGlobBackend{}
	result, err := backend.Run(context.Background(), []string{"*.txt", "*.md"}, nil, dir, "", true, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "a.txt") || !strings.Contains(result, "b.md") {
		t.Errorf("expected both a.txt and b.md in output, got %q", result)
	}
	if strings.Contains(result, "c.go") {
		t.Errorf("did not expect c.go in output, got %q", result)
	}
}

// globParityTree writes a small nested tree used to compare backends. Returns
// the root dir.
func globParityTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{
		"top.go",
		"README.md",
		"a/c.go",
		"a/b/SKILL.md",
		"pkg/skills/hooks/SKILL.md",
		"pkg/skills/jobs/SKILL.md",
		"x/y/z/deep.txt",
	} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestGlobBackendParity locks the fd backend to identical output as the builtin
// doublestar backend across representative patterns — including the
// `**`-in-the-middle shape (`**/skills/**/SKILL.md`) that regressed the
// review-github-prs job, plus anchored (`a/**/...`) and top-level (`*.go`)
// patterns fd's native `--glob` gets wrong.
func TestGlobBackendParity(t *testing.T) {
	if _, err := exec.LookPath("fd"); err != nil {
		t.Skip("fd not installed")
	}
	dir := globParityTree(t)
	builtin := &builtinGlobBackend{}
	fd := &fdGlobBackend{}

	for _, pat := range []string{
		"**/SKILL.md",
		"**/skills/**/SKILL.md",
		"**/*.go",
		"a/**/SKILL.md",
		"*.go",
		"**/*/deep.txt",
		"nomatch/**/*.zzz",
	} {
		bOut, err := builtin.Run(context.Background(), []string{pat}, []string{dir}, dir, "", true, 1000)
		if err != nil {
			t.Fatalf("builtin %q: %v", pat, err)
		}
		fOut, err := fd.Run(context.Background(), []string{pat}, []string{dir}, dir, "", true, 1000)
		if err != nil {
			t.Fatalf("fd %q: %v", pat, err)
		}
		if bOut != fOut {
			t.Errorf("backend mismatch for %q:\n--- builtin ---\n%s\n--- fd ---\n%s", pat, bOut, fOut)
		}
	}
}

// TestFdGlobBackendMatchesNestedGlobstar is the direct regression for the
// reported bug: a `**`-in-the-middle pattern must match under the fd backend.
func TestFdGlobBackendMatchesNestedGlobstar(t *testing.T) {
	if _, err := exec.LookPath("fd"); err != nil {
		t.Skip("fd not installed")
	}
	dir := globParityTree(t)
	target := filepath.Join(dir, "pkg", "skills", "hooks", "SKILL.md")

	fd := &fdGlobBackend{}
	out, err := fd.Run(context.Background(), []string{"**/skills/**/SKILL.md"}, []string{dir}, dir, "", true, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, target) {
		t.Errorf("fd failed to match **-in-the-middle pattern; got %q, want to contain %s", out, target)
	}
	if strings.Contains(out, "no matches") {
		t.Errorf("fd returned no matches for a pattern that should match: %q", out)
	}
}

func TestBuiltinGlobBackendHiddenFilterMultiPath(t *testing.T) {
	root := t.TempDir()
	visible := filepath.Join(root, "visible")
	hiddenDir := filepath.Join(root, ".hidden")
	for _, d := range []string{visible, hiddenDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "x.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Also a hidden file inside a visible dir.
	if err := os.WriteFile(filepath.Join(visible, ".secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := &builtinGlobBackend{}
	// Search both directories as base paths; include_hidden=false should drop
	// the .secret.txt under visible/. The x.txt under .hidden/ is reached from
	// a base *inside* the dotted dir, so its rel path does not contain a dotted
	// segment and should remain.
	result, err := backend.Run(context.Background(), []string{"*.txt"}, []string{visible, hiddenDir}, root, "", false, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, ".secret.txt") {
		t.Errorf("expected .secret.txt to be filtered, got %q", result)
	}
	if !strings.Contains(result, filepath.Join(visible, "x.txt")) {
		t.Errorf("expected visible/x.txt in result, got %q", result)
	}
	if !strings.Contains(result, filepath.Join(hiddenDir, "x.txt")) {
		t.Errorf("expected .hidden/x.txt in result (base itself is search root), got %q", result)
	}
}
