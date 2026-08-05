package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// baseFileSpec returns a minimal valid spec carrying the given files.
func baseFileSpec(files []SpecFile) Spec {
	return Spec{
		ID:      "j",
		Enabled: true,
		Trigger: Trigger{Type: "cron", Expr: "*/5 * * * *"},
		Prompt:  "p",
		CWD:     "/tmp",
		Files:   files,
	}
}

func TestSpecFilesValidate(t *testing.T) {
	cases := []struct {
		name    string
		files   []SpecFile
		wantErr bool
	}{
		{"ok simple", []SpecFile{{Path: "tracker.sh", Content: "x", Mode: "0755"}}, false},
		{"ok nested", []SpecFile{{Path: "bin/tracker.sh", Content: "x"}}, false},
		{"ok default mode", []SpecFile{{Path: "a.txt", Content: "x"}}, false},
		{"empty path", []SpecFile{{Path: "", Content: "x"}}, true},
		{"absolute path", []SpecFile{{Path: "/etc/passwd", Content: "x"}}, true},
		{"dotdot escape", []SpecFile{{Path: "../evil.sh", Content: "x"}}, true},
		{"nested dotdot escape", []SpecFile{{Path: "a/../../evil", Content: "x"}}, true},
		{"bad mode", []SpecFile{{Path: "a.sh", Content: "x", Mode: "nope"}}, true},
		{"dup path", []SpecFile{{Path: "a.sh", Content: "1"}, {Path: "./a.sh", Content: "2"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := baseFileSpec(tc.files)
			err := s.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestSpecFileFileMode(t *testing.T) {
	if got := (SpecFile{}).FileMode(); got != 0o644 {
		t.Errorf("default mode = %o, want 0644", got)
	}
	if got := (SpecFile{Mode: "0755"}).FileMode(); got != 0o755 {
		t.Errorf("mode = %o, want 0755", got)
	}
}

func TestMaterializeSpecFiles(t *testing.T) {
	dir := t.TempDir()
	files := []SpecFile{
		{Path: "tracker.sh", Content: "#!/bin/sh\necho hi\n", Mode: "0755"},
		{Path: "sub/data.txt", Content: "payload"},
	}
	if err := materializeSpecFiles(dir, files); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	// Content written, executable bit honored.
	sh := filepath.Join(dir, "tracker.sh")
	b, err := os.ReadFile(sh)
	if err != nil || string(b) != files[0].Content {
		t.Fatalf("tracker.sh content = %q err=%v", string(b), err)
	}
	fi, _ := os.Stat(sh)
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("tracker.sh mode = %o, want 0755", fi.Mode().Perm())
	}
	// Nested dir created; default mode.
	nb, err := os.ReadFile(filepath.Join(dir, "sub", "data.txt"))
	if err != nil || string(nb) != "payload" {
		t.Fatalf("nested content = %q err=%v", string(nb), err)
	}

	// write-if-changed: an unchanged file keeps its mtime.
	before, _ := os.Stat(sh)
	if err := materializeSpecFiles(dir, files); err != nil {
		t.Fatalf("re-materialize: %v", err)
	}
	after, _ := os.Stat(sh)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("unchanged file was rewritten (mtime moved %v -> %v)", before.ModTime(), after.ModTime())
	}

	// Changed content is overwritten.
	files[0].Content = "#!/bin/sh\necho bye\n"
	if err := materializeSpecFiles(dir, files); err != nil {
		t.Fatalf("materialize changed: %v", err)
	}
	b, _ = os.ReadFile(sh)
	if string(b) != files[0].Content {
		t.Errorf("changed content not written: %q", string(b))
	}

	// Mode drift is corrected without a content change.
	os.Chmod(sh, 0o600)
	if err := materializeSpecFiles(dir, files); err != nil {
		t.Fatalf("materialize mode-fix: %v", err)
	}
	fi, _ = os.Stat(sh)
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode drift not corrected: %o", fi.Mode().Perm())
	}
}

func TestMaterializeSpecFilesConfined(t *testing.T) {
	dir := t.TempDir()
	if err := materializeSpecFiles(dir, []SpecFile{{Path: "../escape.sh", Content: "x"}}); err == nil {
		t.Fatal("expected error for path escaping the job dir")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.sh")); err == nil {
		t.Fatal("file was written outside the job dir")
	}
}

func TestSaveSpecMaterializesFiles(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	spec := baseFileSpec([]SpecFile{{Path: "tracker.sh", Content: "echo x\n", Mode: "0755"}})
	if err := st.SaveSpec(spec); err != nil {
		t.Fatalf("SaveSpec: %v", err)
	}
	p := filepath.Join(root, "j", "tracker.sh")
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "echo x\n" {
		t.Fatalf("SaveSpec did not materialize file: %q err=%v", string(b), err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 0755", fi.Mode().Perm())
	}
}

func TestLoadSpecsMaterializesFiles(t *testing.T) {
	root := t.TempDir()
	jobDir := filepath.Join(root, "myjob")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Hand-write a job.json (as WithHomeFile / a user would), then load it.
	spec := baseFileSpec([]SpecFile{{Path: "tracker.sh", Content: "echo loaded\n", Mode: "0755"}})
	spec.ID = "myjob"
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "job.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	st := NewStore(root)
	specs, invalid := st.LoadSpecs()
	if len(invalid) != 0 {
		t.Fatalf("unexpected invalid: %v", invalid)
	}
	if _, ok := specs["myjob"]; !ok {
		t.Fatalf("spec not loaded: %v", specs)
	}
	b, err := os.ReadFile(filepath.Join(jobDir, "tracker.sh"))
	if err != nil || string(b) != "echo loaded\n" {
		t.Fatalf("LoadSpecs did not materialize file: %q err=%v", string(b), err)
	}
}
