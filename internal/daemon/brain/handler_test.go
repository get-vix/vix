package brain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/brain/lsp"
)

func resetLanguageMapForTest(t *testing.T) {
	t.Helper()
	extMapMu.Lock()
	extMap = nil
	extMapMu.Unlock()
	InitLanguageMapFromConfigs([]lsp.LanguageConfig{{Name: "go", Extensions: []string{".go"}}})
}

func readTestIndex(t *testing.T, brainDir string) BrainIndex {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(brainDir, "index.json"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var index BrainIndex
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("unmarshal index: %v", err)
	}
	return index
}

func TestBrainUpdateFilesUpsertsIndex(t *testing.T) {
	resetLanguageMapForTest(t)
	root := t.TempDir()
	brainDir := filepath.Join(root, ".vix")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	resp, err := doBrainUpdateFiles(map[string]any{"params": map[string]any{
		"project_path": root,
		"brain_dir":    brainDir,
		"files":        []any{"main.go"},
	}}, config.Credential{})
	if err != nil {
		t.Fatalf("update files returned error: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("status = %v, want ok (resp=%v)", resp["status"], resp)
	}

	index := readTestIndex(t, brainDir)
	if got, want := index.Project.TotalFiles, 1; got != want {
		t.Fatalf("TotalFiles = %d, want %d", got, want)
	}
	if len(index.Files) != 1 || index.Files[0].Path != "main.go" {
		t.Fatalf("files = %#v, want main.go", index.Files)
	}
	if got, want := index.Project.Languages["go"], 1; got != want {
		t.Fatalf("go language count = %d, want %d", got, want)
	}

	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() { println(1) }\n"), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	resp, err = doBrainUpdateFiles(map[string]any{"params": map[string]any{
		"project_path": root,
		"brain_dir":    brainDir,
		"files":        []string{"main.go"},
	}}, config.Credential{})
	if err != nil || resp["status"] != "ok" {
		t.Fatalf("second update resp=%v err=%v", resp, err)
	}
	index = readTestIndex(t, brainDir)
	if len(index.Files) != 1 {
		t.Fatalf("duplicate file entries after upsert: %#v", index.Files)
	}
	if index.Files[0].SizeBytes == len("package main\n\nfunc main() {}\n") {
		t.Fatalf("file metadata did not refresh: %#v", index.Files[0])
	}
}

func TestBrainUpdateFilesRemovesDeletedFile(t *testing.T) {
	resetLanguageMapForTest(t)
	root := t.TempDir()
	brainDir := filepath.Join(root, ".vix")
	if err := os.MkdirAll(brainDir, 0o755); err != nil {
		t.Fatalf("mkdir brain dir: %v", err)
	}
	index := BrainIndex{
		Project: ProjectMeta{Name: filepath.Base(root), RootPath: root, TotalFiles: 1, Languages: map[string]int{"go": 1}},
		Files:   []FileInfo{{Path: "gone.go", Language: "go", SizeBytes: 10, LineCount: 1}},
		Symbols: []SymbolInfo{{ID: "gone.go:f", Name: "f", FilePath: "gone.go"}},
		Imports: []ImportInfo{{SourceFile: "gone.go", Module: "fmt", IsExternal: true}},
	}
	if err := saveBrainIndex(brainDir, &index); err != nil {
		t.Fatalf("seed index: %v", err)
	}

	resp, err := doBrainUpdateFiles(map[string]any{"params": map[string]any{
		"project_path": root,
		"brain_dir":    brainDir,
		"files":        []string{"gone.go"},
	}}, config.Credential{})
	if err != nil || resp["status"] != "ok" {
		t.Fatalf("delete update resp=%v err=%v", resp, err)
	}

	updated := readTestIndex(t, brainDir)
	if len(updated.Files) != 0 || len(updated.Symbols) != 0 || len(updated.Imports) != 0 {
		t.Fatalf("deleted file references remain: %#v", updated)
	}
	if updated.Project.TotalFiles != 0 {
		t.Fatalf("TotalFiles = %d, want 0", updated.Project.TotalFiles)
	}
}
