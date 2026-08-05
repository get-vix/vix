package harness

import (
	"os"
	"path/filepath"
)

// FS gives a scenario free read access to the per-test workdir. Assertions are
// plain Go (testify/stdlib) against what these return.
type FS struct{ root string }

// Path resolves a workdir-relative path to an absolute one.
func (f *FS) Path(rel string) string { return filepath.Join(f.root, rel) }

// Read returns the file contents, or nil if it can't be read.
func (f *FS) Read(rel string) []byte {
	b, err := os.ReadFile(f.Path(rel))
	if err != nil {
		return nil
	}
	return b
}

// Write creates or overwrites a workdir-relative file (seeding test state).
func (f *FS) Write(rel, content string) error {
	p := f.Path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// Exists reports whether the workdir-relative path exists.
func (f *FS) Exists(rel string) bool {
	_, err := os.Stat(f.Path(rel))
	return err == nil
}

// Stat returns FileInfo for a workdir-relative path.
func (f *FS) Stat(rel string) (os.FileInfo, error) { return os.Stat(f.Path(rel)) }

// Entries lists the names directly under a workdir-relative directory.
func (f *FS) Entries(rel string) []string {
	des, err := os.ReadDir(f.Path(rel))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(des))
	for _, d := range des {
		names = append(names, d.Name())
	}
	return names
}
