package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// reportRoot is where per-test artifacts are appended. The container sets
// VIX_E2E_REPORT=/out/report; locally it defaults under the cwd.
func reportRoot() string {
	if v := os.Getenv("VIX_E2E_REPORT"); v != "" {
		return v
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, "out", "report")
}

// vixBinary / vixdBinary resolve the product binaries: explicit env override
// first (VIX_BIN / VIXD_BIN), then PATH.
func vixBinary() (string, error)  { return resolveBin("VIX_BIN", "vix") }
func vixdBinary() (string, error) { return resolveBin("VIXD_BIN", "vixd") }

func resolveBin(env, name string) (string, error) {
	if p := os.Getenv(env); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("%s=%q does not exist", env, p)
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s binary not found (set %s or add to PATH)", name, env)
	}
	return p, nil
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("e2e: mkdir %s: %v", dir, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("e2e: mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("e2e: write %s: %v", path, err)
	}
}

// copyTree copies a fixture directory into dst (files + subdirs).
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("e2e: copy fixture %s: %v", src, err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
