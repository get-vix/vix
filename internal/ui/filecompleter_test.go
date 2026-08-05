package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// --- extractAtQuery ---

func TestExtractAtQuery_NoAt(t *testing.T) {
	q, found := extractAtQuery("hello world")
	if found {
		t.Errorf("expected not found, got query=%q", q)
	}
}

func TestExtractAtQuery_AtEnd(t *testing.T) {
	q, found := extractAtQuery("hello @")
	if !found {
		t.Fatal("expected found")
	}
	if q != "" {
		t.Errorf("expected empty query, got %q", q)
	}
}

func TestExtractAtQuery_AtWithPath(t *testing.T) {
	q, found := extractAtQuery("hello @src/ui")
	if !found {
		t.Fatal("expected found")
	}
	if q != "src/ui" {
		t.Errorf("expected %q, got %q", "src/ui", q)
	}
}

func TestExtractAtQuery_AtFollowedBySpace(t *testing.T) {
	// @ followed by a space means the token has ended — not an active query.
	q, found := extractAtQuery("hello @ world")
	if found {
		t.Errorf("expected not found, got query=%q", q)
	}
}

func TestExtractAtQuery_AtTokenWithSpaceInsideToken(t *testing.T) {
	// "hello @foo bar" — the token "foo" is followed by a space, so not active.
	q, found := extractAtQuery("hello @foo bar")
	if found {
		t.Errorf("expected not found (space after token), got query=%q", q)
	}
}

func TestExtractAtQuery_UsesLastAt(t *testing.T) {
	// Only the last @ matters and it must have no space after it.
	q, found := extractAtQuery("email@example.com and @src/main")
	if !found {
		t.Fatal("expected found")
	}
	if q != "src/main" {
		t.Errorf("expected %q, got %q", "src/main", q)
	}
}

func TestExtractAtQuery_AtWithAbsolutePath(t *testing.T) {
	q, found := extractAtQuery("check @/etc/pas")
	if !found {
		t.Fatal("expected found")
	}
	if q != "/etc/pas" {
		t.Errorf("expected %q, got %q", "/etc/pas", q)
	}
}

func TestExtractAtQuery_OnlyAt(t *testing.T) {
	q, found := extractAtQuery("@")
	if !found {
		t.Fatal("expected found")
	}
	if q != "" {
		t.Errorf("expected empty query, got %q", q)
	}
}

// --- resolveAtDir ---

func TestResolveAtDir_EmptyQuery(t *testing.T) {
	dir, prefix := resolveAtDir("", "/project")
	if dir != "/project" {
		t.Errorf("dir: want %q, got %q", "/project", dir)
	}
	if prefix != "" {
		t.Errorf("prefix: want empty, got %q", prefix)
	}
}

func TestResolveAtDir_QueryNoSlash(t *testing.T) {
	dir, prefix := resolveAtDir("main", "/project")
	if dir != "/project" {
		t.Errorf("dir: want %q, got %q", "/project", dir)
	}
	if prefix != "main" {
		t.Errorf("prefix: want %q, got %q", "main", prefix)
	}
}

func TestResolveAtDir_QueryWithSlash(t *testing.T) {
	dir, prefix := resolveAtDir("src/ui", "/project")
	if dir != "/project/src" {
		t.Errorf("dir: want %q, got %q", "/project/src", dir)
	}
	if prefix != "ui" {
		t.Errorf("prefix: want %q, got %q", "ui", prefix)
	}
}

func TestResolveAtDir_QueryTrailingSlash(t *testing.T) {
	dir, prefix := resolveAtDir("src/", "/project")
	if dir != "/project/src" {
		t.Errorf("dir: want %q, got %q", "/project/src", dir)
	}
	if prefix != "" {
		t.Errorf("prefix: want empty, got %q", prefix)
	}
}

func TestResolveAtDir_AbsoluteQuery(t *testing.T) {
	dir, prefix := resolveAtDir("/etc/pas", "/project")
	if dir != "/etc" {
		t.Errorf("dir: want %q, got %q", "/etc", dir)
	}
	if prefix != "pas" {
		t.Errorf("prefix: want %q, got %q", "pas", prefix)
	}
}

func TestResolveAtDir_AbsoluteRoot(t *testing.T) {
	// query "/" → list root
	dir, prefix := resolveAtDir("/", "/project")
	if dir != "/" {
		t.Errorf("dir: want %q, got %q", "/", dir)
	}
	if prefix != "" {
		t.Errorf("prefix: want empty, got %q", prefix)
	}
}

// --- replaceAtToken ---

func TestReplaceAtToken_Basic(t *testing.T) {
	result := replaceAtToken("fix @src", "/project/src/")
	if result != "fix /project/src/" {
		t.Errorf("got %q", result)
	}
}

func TestReplaceAtToken_WithAtPrefix(t *testing.T) {
	// When descending into a directory, caller passes "@path/" to keep the @ active.
	result := replaceAtToken("fix @src", "@/project/src/")
	if result != "fix @/project/src/" {
		t.Errorf("got %q", result)
	}
}

func TestReplaceAtToken_EmptyToken(t *testing.T) {
	result := replaceAtToken("fix @", "/project/main.go")
	if result != "fix /project/main.go" {
		t.Errorf("got %q", result)
	}
}

func TestReplaceAtToken_NoAt(t *testing.T) {
	// Fallback: no @ in input — appends replacement.
	result := replaceAtToken("fix ", "path")
	if result != "fix path" {
		t.Errorf("got %q", result)
	}
}

func TestReplaceAtToken_AtWithQuery(t *testing.T) {
	result := replaceAtToken("hello @src/ma", "/abs/src/main.go")
	if result != "hello /abs/src/main.go" {
		t.Errorf("got %q", result)
	}
}

func TestReplaceAtToken_UsesLastAt(t *testing.T) {
	result := replaceAtToken("email@example.com and @src", "/abs/src/")
	if result != "email@example.com and /abs/src/" {
		t.Errorf("got %q", result)
	}
}

// --- FileCompleter (nil-safety and reload) ---

func TestFileCompleter_NilSafetyBeforeOpen(t *testing.T) {
	var f FileCompleter
	// All read operations on a zero-value completer must not panic.
	if f.IsVisible() {
		t.Error("zero-value completer should not be visible")
	}
	if f.SelectedEntry() != nil {
		t.Error("zero-value SelectedEntry should be nil")
	}
	if f.SelectedPath() != "" {
		t.Error("zero-value SelectedPath should be empty")
	}
	// MoveUp / MoveDown must not panic on nil entries.
	f.MoveUp()
	f.MoveDown()
}

func TestFileCompleter_OpenAndClose(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "util.go"), []byte(""), 0644)

	var f FileCompleter
	f.Open(dir, "")
	if !f.IsVisible() {
		t.Fatal("should be visible after Open")
	}
	if len(f.entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(f.entries))
	}

	f.Close()
	if f.IsVisible() {
		t.Error("should not be visible after Close")
	}
}

func TestFileCompleter_OpenNonExistentDir(t *testing.T) {
	var f FileCompleter
	// Opening a directory that does not exist must not panic; entries become nil.
	f.Open("/this/path/does/not/exist", "")
	if f.IsVisible() {
		// visible is set to true even on error — just entries is nil
	}
	if f.SelectedEntry() != nil {
		t.Error("SelectedEntry should be nil when directory doesn't exist")
	}
	// MoveUp/MoveDown must not panic with nil entries.
	f.MoveUp()
	f.MoveDown()
}

func TestFileCompleter_RefreshClampsSelection(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "aaa.go"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "bbb.go"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "ccc.go"), []byte(""), 0644)

	var f FileCompleter
	f.Open(dir, "")
	f.selected = 2 // point to "ccc.go"

	// Narrow filter to only "aaa" — selection must clamp to 0.
	f.Refresh("aaa")
	if len(f.entries) != 1 {
		t.Errorf("expected 1 entry after filter, got %d", len(f.entries))
	}
	if f.selected != 0 {
		t.Errorf("expected selected=0 after clamp, got %d", f.selected)
	}
}

func TestFileCompleter_RefreshNilEntries(t *testing.T) {
	// Simulate reload failure leaving entries nil, then Refresh must not panic.
	f := FileCompleter{
		visible:    true,
		currentDir: "/this/path/does/not/exist",
		entries:    nil,
		selected:   0,
	}
	f.Refresh("any")
	// entries stay nil; selected stays 0 — no panic.
	if f.selected != 0 {
		t.Errorf("expected selected=0, got %d", f.selected)
	}
}

func TestFileCompleter_HiddenFilesFiltered(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "visible.go"), []byte(""), 0644)

	var f FileCompleter
	f.Open(dir, "")
	for _, e := range f.entries {
		if e.Name() == ".hidden" {
			t.Error("hidden file should be filtered out when query doesn't start with '.'")
		}
	}
}

func TestFileCompleter_HiddenFilesShownWithDotQuery(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "visible.go"), []byte(""), 0644)

	var f FileCompleter
	f.Open(dir, ".")
	found := false
	for _, e := range f.entries {
		if e.Name() == ".hidden" {
			found = true
		}
	}
	if !found {
		t.Error("hidden file should appear when query starts with '.'")
	}
}

func TestFileCompleter_MoveUpDown(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "c.go"), []byte(""), 0644)

	var f FileCompleter
	f.Open(dir, "")
	if f.selected != 0 {
		t.Fatalf("expected initial selection 0, got %d", f.selected)
	}

	f.MoveDown()
	if f.selected != 1 {
		t.Errorf("expected 1 after MoveDown, got %d", f.selected)
	}
	f.MoveDown()
	f.MoveDown() // at end, should clamp
	if f.selected != 2 {
		t.Errorf("expected 2 (clamped at end), got %d", f.selected)
	}

	f.MoveUp()
	if f.selected != 1 {
		t.Errorf("expected 1 after MoveUp, got %d", f.selected)
	}
	f.MoveUp()
	f.MoveUp() // at start, should clamp
	if f.selected != 0 {
		t.Errorf("expected 0 (clamped at start), got %d", f.selected)
	}
}

func TestFileCompleter_SelectedPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(""), 0644)

	var f FileCompleter
	f.Open(dir, "")
	path := f.SelectedPath()
	if path != filepath.Join(dir, "main.go") {
		t.Errorf("expected %q, got %q", filepath.Join(dir, "main.go"), path)
	}
}

func TestFileCompleter_Descend(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "pkg")
	os.Mkdir(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "util.go"), []byte(""), 0644)

	var f FileCompleter
	f.Open(dir, "")

	// Find and descend into pkg/
	var pkgEntry os.DirEntry
	for _, e := range f.entries {
		if e.Name() == "pkg" {
			pkgEntry = e
		}
	}
	if pkgEntry == nil {
		t.Fatal("pkg directory not found in listing")
	}

	f.Descend(pkgEntry)
	if f.currentDir != subDir {
		t.Errorf("currentDir: want %q, got %q", subDir, f.currentDir)
	}
	if f.query != "" {
		t.Errorf("query should be empty after Descend, got %q", f.query)
	}
	if len(f.entries) != 1 || f.entries[0].Name() != "util.go" {
		t.Errorf("expected [util.go] after Descend, got %v", f.entries)
	}
}

// --- Directory picker (dirs-only mode) ---

func TestFileCompleter_OpenDir_FiltersToDirectories(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "pkg"), 0755)
	os.Mkdir(filepath.Join(dir, "cmd"), 0755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte(""), 0644)

	var f FileCompleter
	f.OpenDir(dir)
	if !f.IsVisible() {
		t.Fatal("picker should be visible after OpenDir")
	}
	if f.CurrentDir() != dir {
		t.Errorf("CurrentDir: want %q, got %q", dir, f.CurrentDir())
	}
	if len(f.entries) != 2 {
		t.Fatalf("expected 2 directory entries, got %d: %v", len(f.entries), f.entries)
	}
	for _, e := range f.entries {
		if !e.IsDir() {
			t.Errorf("entry %q is not a directory but appeared in dirs-only listing", e.Name())
		}
	}
}

func TestFileCompleter_OpenDir_SetsTitle(t *testing.T) {
	var f FileCompleter
	f.OpenDir(t.TempDir())
	if f.title == "" {
		t.Error("OpenDir should set a non-empty picker title")
	}
	// Switching back to file mode clears the title.
	f.Open(t.TempDir(), "")
	if f.title != "" {
		t.Errorf("Open should clear the title, got %q", f.title)
	}
	if f.dirsOnly {
		t.Error("Open should clear dirsOnly")
	}
}

func TestFileCompleter_Parent(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "child")
	os.Mkdir(sub, 0755)

	var f FileCompleter
	f.OpenDir(sub)
	f.Parent()
	if f.CurrentDir() != dir {
		t.Errorf("Parent: want %q, got %q", dir, f.CurrentDir())
	}
}

func TestFileCompleter_Parent_StopsAtRoot(t *testing.T) {
	var f FileCompleter
	f.OpenDir("/")
	f.Parent() // must not move above root or panic
	if f.CurrentDir() != "/" {
		t.Errorf("Parent at root: want %q, got %q", "/", f.CurrentDir())
	}
}

// --- resolveWorkDir ---

func TestResolveWorkDir_TildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := resolveWorkDir("~"); got != home {
		t.Errorf("resolveWorkDir(~): want %q, got %q", home, got)
	}
	if got := resolveWorkDir("~/sub"); got != filepath.Join(home, "sub") {
		t.Errorf("resolveWorkDir(~/sub): want %q, got %q", filepath.Join(home, "sub"), got)
	}
}

func TestResolveWorkDir_MakesAbsolute(t *testing.T) {
	got := resolveWorkDir("relative/path")
	if !filepath.IsAbs(got) {
		t.Errorf("resolveWorkDir should return an absolute path, got %q", got)
	}
}

func TestResolveWorkDir_Empty(t *testing.T) {
	wd, _ := os.Getwd()
	if got := resolveWorkDir("   "); got != wd {
		t.Errorf("resolveWorkDir(blank): want cwd %q, got %q", wd, got)
	}
}

// --- truncatePathLeft ---

func TestTruncatePathLeft(t *testing.T) {
	if got := truncatePathLeft("/a/b", 10); got != "/a/b" {
		t.Errorf("short path should be unchanged, got %q", got)
	}
	got := truncatePathLeft("/very/long/path/here", 8)
	if []rune(got)[0] != '…' {
		t.Errorf("truncated path should start with ellipsis, got %q", got)
	}
	if len([]rune(got)) != 8 {
		t.Errorf("truncated path should be width 8 runes, got %d (%q)", len([]rune(got)), got)
	}
}

// --- Integration: descend keeps @ active in input ---

// TestDescendKeepsAtActiveInInput verifies that when a directory is chosen from
// the completer, the resulting input value still contains an @ token so that
// extractAtQuery finds it and the completer remains open for further navigation.
func TestDescendKeepsAtActiveInInput(t *testing.T) {
	// Simulate: input value is "fix @src", user presses Tab on "src/" directory.
	// The dir entry name is "src" and after Descend currentDir becomes "/project/src".
	// model.go does: newPath := "@" + f.currentDir + "/"
	//                input := replaceAtToken(inputValue, newPath)
	inputValue := "fix @src"
	currentDir := "/project/src" // as set by Descend
	newPath := "@" + currentDir + "/"
	result := replaceAtToken(inputValue, newPath)

	// The result must contain @, so extractAtQuery finds an active token.
	query, found := extractAtQuery(result)
	if !found {
		t.Errorf("extractAtQuery(%q) returned not-found; completer would close. result=%q", result, result)
	}
	// The query should resolve to the correct directory.
	dir, prefix := resolveAtDir(query, "/project")
	if dir != currentDir {
		t.Errorf("resolveAtDir: want dir=%q, got %q (query=%q)", currentDir, dir, query)
	}
	if prefix != "" {
		t.Errorf("resolveAtDir: want empty prefix, got %q", prefix)
	}
}
