package perf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviousResultFile(t *testing.T) {
	// Newest-first, as cmd/perftool orders by git commit date.
	candidates := []string{
		"perf/results/cccccc.txt", // current
		"perf/results/bbbbbb.txt", // previous
		"perf/results/aaaaaa.txt",
		"perf/results/baseline.txt",
	}
	if got := PreviousResultFile(candidates, "cccccc"); got != "perf/results/bbbbbb.txt" {
		t.Errorf("PreviousResultFile = %q, want the bbbbbb result", got)
	}

	// Baseline must never be treated as "previous".
	onlyBaseline := []string{"perf/results/aaaaaa.txt", "perf/results/baseline.txt"}
	if got := PreviousResultFile(onlyBaseline, "aaaaaa"); got != "" {
		t.Errorf("PreviousResultFile with only baseline = %q, want empty", got)
	}

	// No candidates → no previous.
	if got := PreviousResultFile(nil, "aaaaaa"); got != "" {
		t.Errorf("PreviousResultFile(nil) = %q, want empty", got)
	}
}

func TestGate(t *testing.T) {
	dir := t.TempDir()

	// Missing results file → blocked.
	if r := Gate(dir, "deadbee", false); r.OK {
		t.Errorf("Gate with missing file should block, got OK (%s)", r.Reason)
	}

	// Present file but dirty tree → blocked.
	if err := os.WriteFile(filepath.Join(dir, ResultFileName("deadbee")), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := Gate(dir, "deadbee", true); r.OK {
		t.Errorf("Gate with dirty tree should block, got OK (%s)", r.Reason)
	}

	// Present file + clean tree → allowed.
	if r := Gate(dir, "deadbee", false); !r.OK {
		t.Errorf("Gate should allow (clean + present), blocked: %s", r.Reason)
	}

	// Empty commit → blocked.
	if r := Gate(dir, "  ", false); r.OK {
		t.Errorf("Gate with empty commit should block")
	}
}

func TestGenerateCorpus(t *testing.T) {
	root := t.TempDir()
	spec := CorpusSpec{Name: "small", NumFiles: 20, FileSize: 512, Exts: []string{".go", ".py"}}

	n, err := GenerateCorpus(root, spec)
	if err != nil {
		t.Fatalf("GenerateCorpus: %v", err)
	}
	if n != 20 {
		t.Errorf("wrote %d files, want 20", n)
	}

	// Count the generated files and check extensions cycle as specified.
	var goCount, pyCount int
	err = filepath.Walk(filepath.Join(root, "small"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() == corpusMarker {
			return err
		}
		switch filepath.Ext(p) {
		case ".go":
			goCount++
		case ".py":
			pyCount++
		default:
			t.Errorf("unexpected extension on %s", p)
		}
		if info.Size() < int64(spec.FileSize) {
			t.Errorf("file %s is %d bytes, want >= %d", p, info.Size(), spec.FileSize)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if goCount != 10 || pyCount != 10 {
		t.Errorf("ext distribution = go:%d py:%d, want 10/10", goCount, pyCount)
	}

	// Idempotent: a second run with the same spec writes nothing.
	n2, err := GenerateCorpus(root, spec)
	if err != nil {
		t.Fatalf("GenerateCorpus (rerun): %v", err)
	}
	if n2 != 0 {
		t.Errorf("rerun wrote %d files, want 0 (idempotent)", n2)
	}

	// A changed spec regenerates.
	spec2 := spec
	spec2.NumFiles = 6
	n3, err := GenerateCorpus(root, spec2)
	if err != nil {
		t.Fatalf("GenerateCorpus (changed spec): %v", err)
	}
	if n3 != 6 {
		t.Errorf("changed-spec run wrote %d files, want 6", n3)
	}
}

func TestGenerateCorpusRejectsBadSpec(t *testing.T) {
	if _, err := GenerateCorpus(t.TempDir(), CorpusSpec{Name: "x", NumFiles: 0, Exts: []string{".go"}}); err == nil {
		t.Error("expected error for zero-file spec")
	}
	if _, err := GenerateCorpus(t.TempDir(), CorpusSpec{Name: "x", NumFiles: 3}); err == nil {
		t.Error("expected error for spec with no extensions")
	}
}

func TestDefaultCorpora(t *testing.T) {
	got := DefaultCorpora(false, 0)
	if len(got) != 3 {
		t.Fatalf("DefaultCorpora returned %d specs, want 3", len(got))
	}
	byName := map[string]CorpusSpec{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if byName["big"].FileSize != 20<<20 {
		t.Errorf("default big file size = %d, want 20MiB", byName["big"].FileSize)
	}
	if byName["many"].NumFiles != 100_000 {
		t.Errorf("default many count = %d, want 100000", byName["many"].NumFiles)
	}
	if huge := DefaultCorpora(true, 1<<20); huge[2].NumFiles != 1_000_000 {
		t.Errorf("huge many count = %d, want 1000000", huge[2].NumFiles)
	}
	// signatures must be stable and distinct per corpus.
	if got[0].signature() == got[1].signature() {
		t.Error("distinct corpora should have distinct signatures")
	}
	if !strings.HasPrefix(ResultFileName("abc123"), "abc123") {
		t.Error("ResultFileName should be commit-prefixed")
	}
}
