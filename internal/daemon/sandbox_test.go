package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- buildBwrapArgs tests ---
//
// These run on every platform because the helper is pure string assembly.
// They protect the Linux sandbox posture: anyone who relaxes a binding
// should have to update a test that says "this path was meant to be
// invisible by default".

func TestBuildBwrapArgs_DefaultPosture_RestrictsRoot(t *testing.T) {
	args := buildBwrapArgs("/work", "/work", "/home/u", nil, "echo hi")
	joined := strings.Join(args, " ")

	// The whole filesystem must NOT be ro-bound. The point of the change
	// is that paths like /opt/secret or /srv become invisible in-sandbox.
	if strings.Contains(joined, "--ro-bind / /") || strings.Contains(joined, "--bind / /") {
		t.Errorf("default posture should not bind / : %s", joined)
	}

	// System paths the agent actually needs must be read-only bound.
	for _, p := range []string{"/usr", "/lib", "/lib64", "/bin", "/sbin", "/etc", "/opt", "/sys"} {
		if !strings.Contains(joined, "--ro-bind-try "+p+" "+p) {
			t.Errorf("expected --ro-bind-try %s %s in args: %s", p, p, joined)
		}
	}

	// /proc gets a fresh bwrap-managed mount, not a host bind.
	if !strings.Contains(joined, "--proc /proc") {
		t.Errorf("expected --proc /proc in args: %s", joined)
	}

	// Read-write paths the agent writes to.
	for _, p := range []string{"/tmp", "/dev", "/var", "/home/u", "/work"} {
		if !strings.Contains(joined, "--bind "+p+" "+p) {
			t.Errorf("expected --bind %s %s in args: %s", p, p, joined)
		}
	}

	// Final bash invocation.
	if args[len(args)-3] != "bash" || args[len(args)-2] != "-c" || args[len(args)-1] != "echo hi" {
		t.Errorf("expected args to end in bash -c <command>, got %v", args[len(args)-3:])
	}
}

func TestBuildBwrapArgs_FullRW_ShortCircuit(t *testing.T) {
	// --disable-automatic-directory-access surfaces as extraDirs=["/"].
	// We mount everything rw and skip the selective binds.
	args := buildBwrapArgs("/work", "/work", "/home/u", []string{"/"}, "echo hi")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--bind / /") {
		t.Errorf("fullRW posture should bind / rw: %s", joined)
	}
	// No selective ro-binds in fullRW mode — they would be redundant and
	// also confuse bwrap when stacked under --bind / /.
	if strings.Contains(joined, "--ro-bind-try /usr /usr") {
		t.Errorf("fullRW posture should not include selective ro binds: %s", joined)
	}
	// The literal "/" entry must not be re-emitted as a per-extra bind.
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--bind" && args[i+1] == "/" && i > 0 {
			// Allowed exactly once at the start.
			if i != 0 {
				t.Errorf("--bind / appears twice in args: %s", joined)
			}
		}
	}
}

func TestBuildBwrapArgs_ExtraDirs_BoundIndividually(t *testing.T) {
	tmp := t.TempDir() // exists, so EvalSymlinks succeeds
	args := buildBwrapArgs("/work", "/work", "/home/u", []string{tmp}, "echo hi")
	joined := strings.Join(args, " ")
	// EvalSymlinks may canonicalize /var/folders/... to /private/var/...
	// on macOS. Accept either form.
	real, _ := filepath.EvalSymlinks(tmp)
	want := "--bind " + real + " " + real
	wantOriginal := "--bind " + tmp + " " + tmp
	if !strings.Contains(joined, want) && !strings.Contains(joined, wantOriginal) {
		t.Errorf("expected extra dir to be bound: %s", joined)
	}
}

func TestBuildBwrapArgs_ExtraDirs_RootEntryNotDuplicated(t *testing.T) {
	args := buildBwrapArgs("/work", "/work", "/home/u", []string{"/", "/data"}, "echo hi")
	count := 0
	for i := 0; i < len(args)-1; i++ {
		if (args[i] == "--bind" || args[i] == "--ro-bind") && args[i+1] == "/" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one bind of /, got %d in %v", count, args)
	}
}

func TestBuildBwrapArgs_SymlinkedCwd_BindsBothPaths(t *testing.T) {
	args := buildBwrapArgs("/sym/project", "/real/project", "/home/u", nil, "echo hi")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--bind /real/project /real/project") {
		t.Errorf("expected real cwd bind: %s", joined)
	}
	if !strings.Contains(joined, "--bind /sym/project /sym/project") {
		t.Errorf("expected symlink cwd bind: %s", joined)
	}
}

func TestBuildBwrapArgs_ChdirToRealCwd(t *testing.T) {
	args := buildBwrapArgs("/sym", "/real", "/home/u", nil, "echo hi")
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--chdir" {
			if args[i+1] != "/real" {
				t.Errorf("expected --chdir /real, got --chdir %s", args[i+1])
			}
			return
		}
	}
	t.Errorf("--chdir not found in args: %v", args)
}

// --- resolvePathInCwd tests ---

func TestResolvePathInCwd_RelativePath(t *testing.T) {
	cwd := t.TempDir()

	// Create a file to resolve
	os.WriteFile(filepath.Join(cwd, "foo.txt"), []byte("x"), 0o644)

	got, err := resolvePathInCwd(cwd, "foo.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != filepath.Join(cwd, "foo.txt") {
		t.Errorf("got %s, want %s", got, filepath.Join(cwd, "foo.txt"))
	}
}

func TestResolvePathInCwd_NestedRelativePath(t *testing.T) {
	cwd := t.TempDir()
	os.MkdirAll(filepath.Join(cwd, "src", "pkg"), 0o755)
	os.WriteFile(filepath.Join(cwd, "src", "pkg", "main.go"), []byte("x"), 0o644)

	got, err := resolvePathInCwd(cwd, "src/pkg/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != filepath.Join(cwd, "src", "pkg", "main.go") {
		t.Errorf("got %s, want %s", got, filepath.Join(cwd, "src", "pkg", "main.go"))
	}
}

func TestResolvePathInCwd_AbsolutePathInsideCwd(t *testing.T) {
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "bar.txt"), []byte("x"), 0o644)

	absPath := filepath.Join(cwd, "bar.txt")
	got, err := resolvePathInCwd(cwd, absPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != absPath {
		t.Errorf("got %s, want %s", got, absPath)
	}
}

func TestResolvePathInCwd_AbsolutePathOutsideCwd_Rejected(t *testing.T) {
	cwd := t.TempDir()
	// Use a path that is outside cwd and outside every platform system
	// directory (not under /tmp, /var, /etc, /usr, etc.) so that the
	// isSystemPath check does not accidentally allow it.
	_, err := resolvePathInCwd(cwd, "/vix_test_nonexistent_dir/file.txt")
	if err == nil {
		t.Fatal("expected error for path outside cwd, got nil")
	}
	if !strings.Contains(err.Error(), "outside working directory") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestResolvePathInCwd_DotDotTraversal_Rejected(t *testing.T) {
	// Use a fake cwd with enough depth so that ../.. still resolves to a
	// path outside all platform system directories.  A real t.TempDir() on
	// macOS lives under /var/folders/... which is a ReadWrite system path,
	// so traversal from there can land inside /var — use a synthetic root
	// that cannot alias any real system directory.
	cwd := "/vix_test_fake_cwd/project/work"
	_, err := resolvePathInCwd(cwd, "../../escape/file.txt")
	if err == nil {
		t.Fatal("expected error for ../ traversal, got nil")
	}
	if !strings.Contains(err.Error(), "outside working directory") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestResolvePathInCwd_SymlinkEscape_Rejected(t *testing.T) {
	cwd := t.TempDir()
	// Point the symlink at a synthetic absolute path that cannot alias any
	// real system directory. A real t.TempDir() target won't work here: on
	// macOS it lives under /var/folders/... and on Linux under /tmp/..., both
	// of which are policy-allowed system paths, so resolving into them is not
	// an escape. Using a fake root makes the escape unambiguous on every OS.
	target := "/vix_test_fake_target"

	link := filepath.Join(cwd, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err := resolvePathInCwd(cwd, "escape/secret.txt")
	if err == nil {
		t.Fatal("expected error for symlink escape, got nil")
	}
	if !strings.Contains(err.Error(), "outside working directory") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestResolvePathInCwd_NewFileInCwd_Allowed(t *testing.T) {
	cwd := t.TempDir()
	// File doesn't exist yet — should still be allowed since parent dir is in cwd
	got, err := resolvePathInCwd(cwd, "newfile.txt")
	if err != nil {
		t.Fatalf("unexpected error for new file in cwd: %v", err)
	}
	if got != filepath.Join(cwd, "newfile.txt") {
		t.Errorf("got %s, want %s", got, filepath.Join(cwd, "newfile.txt"))
	}
}

func TestResolvePathInCwd_NewFileInSubdir_Allowed(t *testing.T) {
	cwd := t.TempDir()
	os.MkdirAll(filepath.Join(cwd, "sub"), 0o755)

	got, err := resolvePathInCwd(cwd, "sub/newfile.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != filepath.Join(cwd, "sub", "newfile.txt") {
		t.Errorf("got %s, want %s", got, filepath.Join(cwd, "sub", "newfile.txt"))
	}
}

// --- resolvePathInAllowed: $HOME tests ---

func TestResolvePathInAllowed_HomePathAllowed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	// Create a file in the fake $HOME.
	target := filepath.Join(home, "notes.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolvePathInAllowed(cwd, []string{home}, target)
	if err != nil {
		t.Fatalf("expected no error for $HOME path, got: %v", err)
	}
	if got != target {
		t.Errorf("got %s, want %s", got, target)
	}
}

// --- Sandbox command construction tests ---

func TestSandboxedBashCmd_SetsWorkingDir(t *testing.T) {
	cwd := t.TempDir()
	ctx := context.Background()

	cmd := sandboxedBashCmd(ctx, "echo hello", cwd, nil)
	if cmd.Dir != cwd {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, cwd)
	}
}

func TestSandboxedBashCmd_ExecutesCommand(t *testing.T) {
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := sandboxedBashCmd(ctx, "echo sandboxed", cwd, nil)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), "sandboxed") {
		t.Errorf("expected output to contain 'sandboxed', got: %s", out)
	}
}

func TestSandboxedBashCmd_CanReadWriteInCwd(t *testing.T) {
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Write a file inside cwd
	cmd := sandboxedBashCmd(ctx, "echo test > testfile.txt && cat testfile.txt", cwd, nil)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), "test") {
		t.Errorf("expected output 'test', got: %s", out)
	}
}

func TestSandboxedBashCmd_CannotWriteOutsideCwd(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec test only runs on macOS")
	}
	if detectSandbox() != sandboxSeatbelt {
		t.Skip("sandbox-exec not available")
	}

	cwd := t.TempDir()
	// Use /Applications as the "outside" target — it's read-only in the
	// Seatbelt profile. (We can't use /tmp, /var, /dev, or $HOME as those
	// are writable by design.)
	escapePath := "/Applications/vix_sandbox_escape_test.txt"
	// Make sure we're not being fooled by a pre-existing file.
	os.Remove(escapePath)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := sandboxedBashCmd(ctx, "echo pwned > "+escapePath, cwd, nil)
	out, err := cmd.CombinedOutput()
	// sandbox-exec should block the write — we expect either an error or
	// the file to not exist.
	if err == nil {
		if _, statErr := os.Stat(escapePath); statErr == nil {
			os.Remove(escapePath)
			t.Fatalf("sandbox allowed writing outside cwd!\noutput: %s", out)
		}
	}
}

func TestSeatbeltProfile_ContainsCwd(t *testing.T) {
	cwd := "/Users/test/myproject"
	profile := seatbeltProfile(cwd, nil)

	if !strings.Contains(profile, cwd) {
		t.Error("profile should contain the working directory")
	}
	if !strings.Contains(profile, "(allow file-write*") {
		t.Error("profile should allow file writes")
	}
	if !strings.Contains(profile, "(deny default)") {
		t.Error("profile should deny by default")
	}
}

// TestSeatbeltProfile_AllowsPTY guards the full set of directives a
// controlling terminal needs on macOS. process-fork + pseudo-tty are
// necessary but NOT sufficient: grantpt(3) issues an ioctl(TIOCPTYGRANT)
// on /dev/ptmx, and that is a distinct Seatbelt file-ioctl operation which
// file-write* does not imply. Without the file-ioctl directives, tmux /
// script / expect fail with a misleading "fork failed: Operation not
// permitted" even though fork itself works.
func TestSeatbeltProfile_AllowsPTY(t *testing.T) {
	profile := seatbeltProfile("/Users/test/myproject", nil)

	for _, want := range []string{
		"(allow process-fork)",
		"(allow pseudo-tty)",
		`(allow file-ioctl (literal "/dev/ptmx"))`,
		`(allow file-ioctl (regex #"^/dev/ttys[0-9]*"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing pty directive %q\nprofile:\n%s", want, profile)
		}
	}
}

// TestSeatbeltProfile_AllowsIOKitOpen guards the iokit-open allowance that
// headless browsers (Chromium/WebKit, and thus Playwright e2e) require. They
// open IOKit graphics user clients (e.g. IOSurfaceRootUserClient) during early
// startup even in --headless mode; under (deny default) that IOServiceOpen
// returns kIOReturnNotPermitted and the browser dereferences the NULL client,
// crashing with SIGSEGV before launch. The token must be "iokit-open" (the
// SBPL operation), not the runtime API's "iokit-open-user-client".
func TestSeatbeltProfile_AllowsIOKitOpen(t *testing.T) {
	profile := seatbeltProfile("/Users/test/myproject", nil)

	if !strings.Contains(profile, "(allow iokit-open)") {
		t.Errorf("profile missing iokit-open allowance (headless browsers crash without it)\nprofile:\n%s", profile)
	}
}

// TestSandboxedBashCmd_CanAllocatePTY actually allocates a pseudo-terminal
// inside the real Seatbelt sandbox and asserts the grant succeeds. This is
// the runtime counterpart to TestSeatbeltProfile_AllowsPTY: the Go test
// runner is not itself sandboxed, so it can apply the profile (unlike a
// process already running under a deny-default profile, which cannot nest
// sandbox_apply). Uses python3's os.openpty(), which performs the full
// posix_openpt -> grantpt -> unlockpt handshake that tmux relies on.
func TestSandboxedBashCmd_CanAllocatePTY(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt pty test only runs on macOS")
	}
	if detectSandbox() != sandboxSeatbelt {
		t.Skip("sandbox-exec not available")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	script := `python3 -c 'import os; m,s=os.openpty(); os.close(m); os.close(s); print("PTY_OK")'`
	cmd := sandboxedBashCmd(ctx, script, cwd, nil)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pty allocation failed under sandbox: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), "PTY_OK") {
		t.Errorf("expected PTY_OK, got: %s", out)
	}
}

func TestSandboxName_ReturnsString(t *testing.T) {
	name := SandboxName()
	if name == "" {
		t.Error("SandboxName should return a non-empty string")
	}
	valid := map[string]bool{"macOS sandbox-exec": true, "bubblewrap": true, "none": true}
	if !valid[name] {
		t.Errorf("unexpected sandbox name: %s", name)
	}
}

// TestSandboxedBashCmd_WaitDelay_BackgroundChild verifies that cmd.Wait()
// does not block indefinitely when a bash command backgrounds a child process
// that inherits stdout. Without WaitDelay, Go's internal pipe-copying
// goroutine blocks until the child closes the pipe, causing Wait to hang
// long after bash itself exits.
func TestSandboxedBashCmd_WaitDelay_BackgroundChild(t *testing.T) {
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// bash exits immediately after echo, but "sleep 30" keeps the pipe open.
	cmd := sandboxedBashCmd(ctx, "sleep 30 >/dev/null 2>&1 & echo fast", cwd, nil)

	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		// Wait returned. err may be nil or ErrWaitDelay — both are acceptable.
		if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
			t.Fatalf("unexpected error from Wait: %v", err)
		}
		if !strings.Contains(outBuf.String(), "fast") {
			t.Errorf("expected output to contain 'fast', got: %q", outBuf.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cmd.Wait() blocked for >5s — WaitDelay is not working")
	}
}

// TestSanitizedBashEnv_StripsGODEBUG verifies that the daemon-only GODEBUG
// debug knob is removed from the env handed to bash steps, while unrelated
// variables pass through untouched.
func TestSanitizedBashEnv_StripsGODEBUG(t *testing.T) {
	t.Setenv("GODEBUG", "schedtrace=5000")
	t.Setenv("VIX_TEST_SENTINEL", "keepme")

	env := sanitizedBashEnv()

	var sawSentinel bool
	for _, kv := range env {
		if strings.HasPrefix(kv, "GODEBUG=") {
			t.Errorf("GODEBUG leaked into sanitized env: %q", kv)
		}
		if kv == "VIX_TEST_SENTINEL=keepme" {
			sawSentinel = true
		}
	}
	if !sawSentinel {
		t.Error("sanitizedBashEnv dropped an unrelated variable (VIX_TEST_SENTINEL)")
	}
}

// TestRunBashWithContext_DoesNotLeakGODEBUG proves the end-to-end effect: a
// bash step spawned by the workflow/job runner does not inherit GODEBUG, so a
// Go child binary it invokes won't emit "SCHED ..." trace lines that would
// contaminate the captured output.
func TestRunBashWithContext_DoesNotLeakGODEBUG(t *testing.T) {
	t.Setenv("GODEBUG", "schedtrace=5000")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := runBashWithContext(ctx, `echo "GODEBUG=[${GODEBUG:-unset}]"`, t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("runBashWithContext failed: %v", err)
	}
	if !strings.Contains(out, "GODEBUG=[unset]") {
		t.Errorf("expected GODEBUG to be unset in the bash step, got: %q", out)
	}
}
