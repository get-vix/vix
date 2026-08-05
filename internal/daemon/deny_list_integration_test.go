package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newIntegrationThread builds a Thread backed by a Server with all tool
// handlers registered, so executeToolDirect can actually dispatch to
// read_file / write_file / edit_file / delete_file / bash / grep /
// glob_files. Only the fields exercised by the deny-list path are set.
func newIntegrationThread(t *testing.T, cwd string, deny []string) *Thread {
	return newIntegrationThreadFull(t, cwd, deny, nil)
}

// newIntegrationThreadFull is like newIntegrationThread but also seeds
// the URL deny list. Kept as a separate constructor so existing callers
// don't need updating.
func newIntegrationThreadFull(t *testing.T, cwd string, denyPaths, denyURLs []string) *Thread {
	t.Helper()
	srv := &Server{handlers: make(map[string]HandlerFunc)}
	RegisterToolHandlers(srv)
	s := &Thread{
		server:                         srv,
		cwd:                            cwd,
		headless:                       true,
		enableAutomaticWritePermission: true,
		enableAutomaticDirectoryAccess: true,
		denyList:                       append([]string(nil), denyPaths...),
		denyURLs:                       append([]string(nil), denyURLs...),
		readFiles:                      make(map[string]bool),
		projectConfig: ProjectConfig{
			ToolTimeouts: ToolTimeouts{
				Default: 30 * time.Second,
				Max:     60 * time.Second,
			},
		},
	}
	return s
}

func TestIntegration_ReadFile_Denied(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	api := filepath.Join(secrets, "api.txt")
	os.WriteFile(api, []byte("SUPER_SECRET"), 0o600)

	s := newIntegrationThread(t, root, []string{secrets})

	// Absolute form.
	res := s.executeToolDirect(context.Background(), "read_file", map[string]any{"path": api})
	if res == nil || !res.IsError {
		t.Fatalf("expected deny error, got %+v", res)
	}
	if strings.Contains(res.Output, "SUPER_SECRET") {
		t.Error("secret content leaked in error message")
	}

	// Relative form.
	res = s.executeToolDirect(context.Background(), "read_file", map[string]any{"path": "./secrets/api.txt"})
	if res == nil || !res.IsError {
		t.Fatalf("relative path: expected deny error, got %+v", res)
	}
}

func TestIntegration_ReadFile_SafeStillWorks(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	safeFile := filepath.Join(root, "README.md")
	os.WriteFile(safeFile, []byte("hello world"), 0o600)

	s := newIntegrationThread(t, root, []string{secrets})
	res := s.executeToolDirect(context.Background(), "read_file", map[string]any{"path": safeFile})
	if res == nil {
		t.Fatal("nil result")
	}
	if res.IsError {
		t.Fatalf("safe file should read: %s", res.Output)
	}
	if !strings.Contains(res.Output, "hello world") {
		t.Errorf("expected content, got %q", res.Output)
	}
}

func TestIntegration_WriteFile_Denied_NoDiskChange(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	target := filepath.Join(secrets, "new.txt")

	s := newIntegrationThread(t, root, []string{secrets})
	res := s.executeToolDirect(context.Background(), "write_file", map[string]any{"path": target, "content": "boom"})
	if res == nil || !res.IsError {
		t.Fatalf("expected deny, got %+v", res)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("file should not exist on disk after deny, stat err=%v", err)
	}
}

func TestIntegration_EditFile_Denied_NoDiskChange(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	target := filepath.Join(secrets, "existing.txt")
	original := "keep me"
	os.WriteFile(target, []byte(original), 0o600)

	s := newIntegrationThread(t, root, []string{secrets})
	// readFiles seeded so read-gate isn't the reason for denial.
	s.readFiles[target] = true

	res := s.executeToolDirect(context.Background(), "edit_file", map[string]any{
		"path":       target,
		"old_string": "keep me",
		"new_string": "boom",
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected deny, got %+v", res)
	}
	// File must be untouched.
	got, _ := os.ReadFile(target)
	if string(got) != original {
		t.Errorf("content changed: want %q got %q", original, string(got))
	}
}

func TestIntegration_DeleteFile_Denied_StillExists(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	target := filepath.Join(secrets, "keep.txt")
	os.WriteFile(target, []byte("alive"), 0o600)

	s := newIntegrationThread(t, root, []string{secrets})
	res := s.executeToolDirect(context.Background(), "delete_file", map[string]any{"path": target})
	if res == nil || !res.IsError {
		t.Fatalf("expected deny, got %+v", res)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("file was deleted despite deny: %v", err)
	}
}

func TestIntegration_Bash_Denied_SideEffectsBlocked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not expected on windows runners")
	}
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	target := filepath.Join(secrets, "keep.txt")
	os.WriteFile(target, []byte("alive"), 0o600)

	s := newIntegrationThread(t, root, []string{secrets})

	// rm -rf with absolute path
	res := s.executeToolDirect(context.Background(), "bash", map[string]any{
		"command": "rm -rf " + secrets,
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected deny, got %+v", res)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("file was removed despite deny: %v", err)
	}

	// Same with relative path
	res = s.executeToolDirect(context.Background(), "bash", map[string]any{
		"command": "rm -rf ./secrets",
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected deny on relative path, got %+v", res)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("file was removed despite deny (relative): %v", err)
	}
}

func TestIntegration_Bash_SafeCommandRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)

	s := newIntegrationThread(t, root, []string{secrets})
	res := s.executeToolDirect(context.Background(), "bash", map[string]any{
		"command": "echo hello",
	})
	if res == nil {
		t.Fatal("nil result")
	}
	if res.IsError {
		t.Fatalf("safe bash should succeed, got error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "hello") {
		t.Errorf("expected 'hello' in output, got %q", res.Output)
	}
}

func TestIntegration_Grep_FiltersDeniedMatches(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	safe := filepath.Join(root, "safe")
	os.MkdirAll(secrets, 0o755)
	os.MkdirAll(safe, 0o755)
	os.WriteFile(filepath.Join(secrets, "s.txt"), []byte("needle"), 0o600)
	os.WriteFile(filepath.Join(safe, "a.txt"), []byte("needle"), 0o600)

	s := newIntegrationThread(t, root, []string{secrets})
	res := s.executeToolDirect(context.Background(), "grep", map[string]any{
		"pattern": "needle",
		"path":    ".",
	})
	if res == nil {
		t.Fatal("nil result")
	}
	if res.IsError {
		t.Fatalf("grep errored: %s", res.Output)
	}
	if strings.Contains(res.Output, "secrets/") {
		t.Errorf("denied match leaked in grep output: %q", res.Output)
	}
	if !strings.Contains(res.Output, "safe/") && !strings.Contains(res.Output, "a.txt") {
		t.Errorf("expected safe match in output, got %q", res.Output)
	}
}

func TestIntegration_GlobFiles_FiltersDeniedPaths(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	safe := filepath.Join(root, "safe")
	os.MkdirAll(secrets, 0o755)
	os.MkdirAll(safe, 0o755)
	os.WriteFile(filepath.Join(secrets, "s.txt"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(safe, "a.txt"), []byte("y"), 0o600)

	s := newIntegrationThread(t, root, []string{secrets})
	res := s.executeToolDirect(context.Background(), "glob_files", map[string]any{
		"pattern": "**/*.txt",
	})
	if res == nil {
		t.Fatal("nil result")
	}
	if res.IsError {
		t.Fatalf("glob errored: %s", res.Output)
	}
	if strings.Contains(res.Output, "secrets/s.txt") {
		t.Errorf("denied path leaked in glob output: %q", res.Output)
	}
	if !strings.Contains(res.Output, "a.txt") {
		t.Errorf("expected safe path in output, got %q", res.Output)
	}
}

func TestIntegration_DenyBeatsAllow(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	target := filepath.Join(secrets, "api.txt")
	os.WriteFile(target, []byte("x"), 0o600)

	s := newIntegrationThread(t, root, []string{secrets})
	// Explicitly allow the same path via allowedDirs — deny must still win.
	s.addAllowedDir(secrets)

	res := s.executeToolDirect(context.Background(), "read_file", map[string]any{"path": target})
	if res == nil || !res.IsError {
		t.Fatalf("deny must beat allow; got %+v", res)
	}
}

// --- URL deny integration ----------------------------------------------

func TestIntegration_WebFetch_Denied_NoNetworkCall(t *testing.T) {
	// We don't need a real network call: the deny check fires before the
	// handler runs, so a denied URL must surface as IsError without ever
	// dialing out. (Negative-path test: the rejection itself is the only
	// observable side effect.)
	root := testRoot(t)
	s := newIntegrationThreadFull(t, root, nil, []string{"bad.example.com"})

	res := s.executeToolDirect(context.Background(), "web_fetch", map[string]any{
		"url": "https://api.bad.example.com/leak",
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected deny, got %+v", res)
	}
	if !strings.Contains(res.Output, "deny_list") {
		t.Errorf("error message should mention deny_list, got %q", res.Output)
	}
}

func TestIntegration_WebFetch_AllowedURL_HandlerRuns(t *testing.T) {
	// Verify a non-denied URL is NOT short-circuited. We don't assert on
	// the handler's network result (it would fetch a real site and be
	// flaky); we only assert the error message is not the deny-list one.
	root := testRoot(t)
	s := newIntegrationThreadFull(t, root, nil, []string{"bad.example.com"})

	res := s.executeToolDirect(context.Background(), "web_fetch", map[string]any{
		"url": "https://safe.example.org/x",
	})
	if res == nil {
		t.Fatal("nil result")
	}
	if res.IsError && strings.Contains(res.Output, "deny_list") {
		t.Errorf("safe url was wrongly blocked by deny_list: %s", res.Output)
	}
}

// Operators routinely worry that an attacker will bypass `bad.example.com`
// by appending a port. Verify the dispatcher rejects that form too, and
// that a userinfo-smuggled URL (`https://user@bad.example.com`) is also
// rejected.
func TestIntegration_WebFetch_PortAndUserinfoBypassAttempts(t *testing.T) {
	root := testRoot(t)
	s := newIntegrationThreadFull(t, root, nil, []string{"bad.example.com"})

	for _, attempt := range []string{
		"https://bad.example.com:8443/x",
		"https://api.bad.example.com:9000/x",
		"https://user:pass@bad.example.com/x",
		"https://bad.example.com/?token=abc#frag",
	} {
		res := s.executeToolDirect(context.Background(), "web_fetch", map[string]any{"url": attempt})
		if res == nil || !res.IsError {
			t.Errorf("attempt %q: expected deny, got %+v", attempt, res)
		}
	}
}

// A bash command containing one safe URL and one denied URL must still be
// blocked — the dispatcher cannot run partial commands.
func TestIntegration_Bash_MixedURLs_OneDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	root := testRoot(t)
	s := newIntegrationThreadFull(t, root, nil, []string{"bad.example.com"})

	res := s.executeToolDirect(context.Background(), "bash", map[string]any{
		"command": "curl https://safe.example.org/a; curl https://bad.example.com/b",
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected deny when any URL in command is denied, got %+v", res)
	}
}

// When neither path nor URL deny lists match, web_fetch and bash should
// not be short-circuited by the deny check. (The handler may still fail
// for other reasons — e.g. network — but the failure must not mention
// deny_list.)
func TestIntegration_NeitherListMatches_Passthrough(t *testing.T) {
	root := testRoot(t)
	s := newIntegrationThreadFull(t, root, []string{filepath.Join(root, "denied")}, []string{"bad.example.com"})

	res := s.executeToolDirect(context.Background(), "web_fetch", map[string]any{
		"url": "https://safe.example.org/x",
	})
	if res != nil && res.IsError && strings.Contains(res.Output, "deny_list") {
		t.Errorf("safe URL was wrongly blocked: %s", res.Output)
	}
}

func TestIntegration_Bash_DeniedURL_Refused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	root := testRoot(t)
	s := newIntegrationThreadFull(t, root, nil, []string{"bad.example.com"})

	res := s.executeToolDirect(context.Background(), "bash", map[string]any{
		"command": "curl https://bad.example.com/leak",
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected deny, got %+v", res)
	}
	if !strings.Contains(res.Output, "deny_list") {
		t.Errorf("error message should mention deny_list, got %q", res.Output)
	}
}

func TestIntegration_SubsequentSafeCallStillWorks(t *testing.T) {
	root := testRoot(t)
	secrets := filepath.Join(root, "secrets")
	os.MkdirAll(secrets, 0o755)
	os.WriteFile(filepath.Join(secrets, "x.txt"), []byte("x"), 0o600)
	safeFile := filepath.Join(root, "README.md")
	os.WriteFile(safeFile, []byte("readme"), 0o600)

	s := newIntegrationThread(t, root, []string{secrets})

	// First call: denied.
	r1 := s.executeToolDirect(context.Background(), "read_file", map[string]any{"path": filepath.Join(secrets, "x.txt")})
	if r1 == nil || !r1.IsError {
		t.Fatalf("expected first call denied, got %+v", r1)
	}

	// Second call: safe path must still succeed (no residual state).
	r2 := s.executeToolDirect(context.Background(), "read_file", map[string]any{"path": safeFile})
	if r2 == nil || r2.IsError {
		t.Fatalf("subsequent safe call failed: %+v", r2)
	}
	if !strings.Contains(r2.Output, "readme") {
		t.Errorf("expected readme content, got %q", r2.Output)
	}
}
