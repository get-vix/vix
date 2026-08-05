package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Tests for the optional `mode` parameter on write_file / edit_file. The
// motivating case: agents on Linux containers needed to drop executable
// shell shims (e.g. /usr/bin/bash workaround on Debian 11) and had no
// path to set the executable bit, since write_file always wrote 0644
// and there was no chmod tool.

func TestParseFileMode(t *testing.T) {
	cases := []struct {
		in      string
		want    os.FileMode
		wantErr bool
	}{
		{"", 0, false},
		{"   ", 0, false},
		{"755", 0o755, false},
		{"0755", 0o755, false},
		{"0o755", 0o755, false},
		{"644", 0o644, false},
		{"0", 0, false},
		// suid (4xxx) / sgid (2xxx) / sticky (1xxx) must be masked off so a
		// typo'd "4755" doesn't silently grant setuid.
		{"4755", 0o755, false},
		{"2755", 0o755, false},
		{"1755", 0o755, false},
		{"7755", 0o755, false},
		// nonsense
		{"abc", 0, true},
		{"0xff", 0, true},
		{"-755", 0, true},
		{"999", 0, true}, // not octal
	}
	for _, tc := range cases {
		got, err := parseFileMode(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseFileMode(%q): expected error, got mode=%o", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseFileMode(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseFileMode(%q) = %o, want %o", tc.in, got, tc.want)
		}
	}
}

func TestWriteFileImpl_DefaultModeOnNewFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits not meaningful on Windows")
	}
	root := testRoot(t)
	target := filepath.Join(root, "new.sh")

	if _, err := writeFileImpl(root, []string{root}, target, "echo hi", 0); err != nil {
		t.Fatalf("writeFileImpl: %v", err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o644 {
		t.Errorf("default mode on new file = %o, want 0644", got)
	}
}

func TestWriteFileImpl_PreservesExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits not meaningful on Windows")
	}
	root := testRoot(t)
	target := filepath.Join(root, "script.sh")
	// pre-create as 0755 so we can verify a subsequent write doesn't
	// silently downgrade it.
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, err := writeFileImpl(root, []string{root}, target, "echo new", 0); err != nil {
		t.Fatalf("writeFileImpl: %v", err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o755 {
		t.Errorf("mode after re-write of 0755 file with mode=0 = %o, want 0755 (must not be silently demoted)", got)
	}
}

func TestWriteFileImpl_ExplicitModeApplies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits not meaningful on Windows")
	}
	root := testRoot(t)
	target := filepath.Join(root, "tool")

	if _, err := writeFileImpl(root, []string{root}, target, "#!/bin/sh\necho hi\n", 0o755); err != nil {
		t.Fatalf("writeFileImpl: %v", err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o755 {
		t.Errorf("explicit mode 0755 = %o, want 0755", got)
	}

	// Re-write the same file with mode=0o600 — chmod must take effect on
	// the existing inode (os.WriteFile only honours mode when *creating*).
	if _, err := writeFileImpl(root, []string{root}, target, "updated", 0o600); err != nil {
		t.Fatalf("writeFileImpl rewrite: %v", err)
	}
	st, _ = os.Stat(target)
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("explicit mode 0600 on existing file = %o, want 0600", got)
	}
}

func TestEditFileImpl_PreservesExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits not meaningful on Windows")
	}
	root := testRoot(t)
	target := filepath.Join(root, "script.sh")
	if err := os.WriteFile(target, []byte("alpha"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, _, err := editFileImpl(root, []string{root}, target, "alpha", "beta", 0); err != nil {
		t.Fatalf("editFileImpl: %v", err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o755 {
		t.Errorf("mode after edit_file with mode=0 = %o, want 0755 (must preserve)", got)
	}
}

func TestEditFileImpl_ExplicitModeApplies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits not meaningful on Windows")
	}
	root := testRoot(t)
	target := filepath.Join(root, "config")
	if err := os.WriteFile(target, []byte("alpha"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, _, err := editFileImpl(root, []string{root}, target, "alpha", "beta", 0o600); err != nil {
		t.Fatalf("editFileImpl: %v", err)
	}
	st, _ := os.Stat(target)
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("explicit mode 0600 on edit = %o, want 0600", got)
	}
}

func TestIntegration_WriteFile_ExecutableMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits not meaningful on Windows")
	}
	root := testRoot(t)
	target := filepath.Join(root, "shim.sh")
	s := newIntegrationThread(t, root, nil)

	res := s.executeToolDirect(context.Background(), "write_file", map[string]any{
		"path":    target,
		"content": "#!/bin/sh\nexec /bin/bash \"$@\"\n",
		"mode":    "0755",
	})
	if res == nil || res.IsError {
		t.Fatalf("write_file mode=0755 should succeed: %+v", res)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Errorf("expected executable bit, got mode=%o", st.Mode().Perm())
	}
}

func TestIntegration_WriteFile_BadMode(t *testing.T) {
	root := testRoot(t)
	target := filepath.Join(root, "x")
	s := newIntegrationThread(t, root, nil)

	res := s.executeToolDirect(context.Background(), "write_file", map[string]any{
		"path":    target,
		"content": "x",
		"mode":    "rwxr-xr-x", // symbolic — we accept octal only
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected error for bad mode, got %+v", res)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("file should not exist after bad mode rejection (stat err=%v)", err)
	}
}
