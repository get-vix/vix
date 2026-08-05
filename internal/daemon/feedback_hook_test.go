package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/hooks"
)

func TestSeedDefaultFeedbackHook(t *testing.T) {
	dir := t.TempDir()
	seedDefaultFeedbackHook(dir)
	hookDir := filepath.Join(dir, feedbackHookID)

	// Spec: present, valid, and wired to ThreadStart/startup async.
	raw, err := os.ReadFile(filepath.Join(hookDir, "hook.json"))
	if err != nil {
		t.Fatalf("spec not written: %v", err)
	}
	var spec hooks.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("spec is not valid JSON: %v", err)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("seeded spec fails validation: %v", err)
	}
	if spec.Trigger.Event != hooks.EventThreadStart || spec.Trigger.Matcher != "startup" {
		t.Errorf("trigger = %+v, want ThreadStart/startup", spec.Trigger)
	}
	if spec.EffectiveMode() != hooks.ModeAsync {
		t.Errorf("mode = %q, want async", spec.EffectiveMode())
	}
	if !strings.Contains(spec.Command, filepath.Join(feedbackHookID, "script.sh")) {
		t.Errorf("command %q does not reference the script", spec.Command)
	}

	// Script: executable and a shell script.
	scriptPath := filepath.Join(hookDir, "script.sh")
	fi, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("script not written: %v", err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("script mode = %v, want executable", fi.Mode().Perm())
	}
	body, _ := os.ReadFile(scriptPath)
	if !strings.HasPrefix(string(body), "#!") {
		t.Error("script missing shebang")
	}

	// message.md: present and carries the feedback form link.
	msg, err := os.ReadFile(filepath.Join(hookDir, "message.md"))
	if err != nil {
		t.Fatalf("message.md not written: %v", err)
	}
	if !strings.Contains(string(msg), "forms.gle/ADEVrtP2xRsKpxtdA") {
		t.Error("message.md missing the feedback form link")
	}

	// Sentinel written so seeding runs at most once.
	if _, err := os.Stat(filepath.Join(dir, feedbackSeedSentinel)); err != nil {
		t.Fatalf("seed sentinel not written: %v", err)
	}

	// Re-seeding must not clobber a user's edited/disabled artifacts.
	edited := []byte(`{"id":"feedback-at-10","enabled":false}`)
	if err := os.WriteFile(filepath.Join(hookDir, "hook.json"), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	seedDefaultFeedbackHook(dir)
	if got, _ := os.ReadFile(filepath.Join(hookDir, "hook.json")); string(got) != string(edited) {
		t.Errorf("re-seed clobbered edited spec: got %s", got)
	}
}

func TestBuildHookContextIncludesVixBinAndSocket(t *testing.T) {
	sess := &Thread{
		id:     "sess-1",
		server: &Server{vixBin: "/opt/vix/bin/vix", sockPath: "/tmp/vixd-test.sock"},
	}
	ctx := sess.buildHookContext(hooks.EventThreadStart, map[string]any{"source": "startup"})
	if ctx["vix_bin"] != "/opt/vix/bin/vix" {
		t.Errorf("vix_bin = %v, want the server's binary path", ctx["vix_bin"])
	}
	if ctx["socket_path"] != "/tmp/vixd-test.sock" {
		t.Errorf("socket_path = %v, want the server's socket", ctx["socket_path"])
	}
}

func TestCreateMessageThreadFromFile(t *testing.T) {
	s, home := newMessageTestServer(t)
	cwd := t.TempDir()

	msgPath := filepath.Join(t.TempDir(), "message.md")
	if err := os.WriteFile(msgPath, []byte("# Hi\n\nfrom a file"), 0o644); err != nil {
		t.Fatal(err)
	}

	id, err := s.createMessageThread(MessageThreadSpec{MessageFile: msgPath, CWD: cwd, Title: "T"})
	if err != nil {
		t.Fatalf("createMessageThread(message_file): %v", err)
	}
	rec, found, _ := loadOpenThreadRecord(config.NewVixPaths("", home, cwd), id)
	if !found || rec.Messages[0].Content[0].Text != "# Hi\n\nfrom a file" {
		t.Fatalf("message_file contents not used as the message: %+v", rec.Messages)
	}

	// Errors: both set, and a missing file.
	if _, err := s.createMessageThread(MessageThreadSpec{Message: "x", MessageFile: msgPath, CWD: cwd}); err == nil {
		t.Error("expected error when both message and message_file are set")
	}
	if _, err := s.createMessageThread(MessageThreadSpec{MessageFile: "/no/such/file.md", CWD: cwd}); err == nil {
		t.Error("expected error when message_file is missing")
	}
}

// runFeedbackScript runs the shipped feedback script once with HOME=home and the
// given context JSON on stdin. fakeVix is a script path substituted for the vix
// binary in the context. Returns combined output and any run error.
func runFeedbackScript(t *testing.T, scriptPath, home, fakeVix string) {
	t.Helper()
	ctx := `{"source":"startup","vix_bin":"` + fakeVix + `","socket_path":"/tmp/x.sock"}`
	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader(ctx)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script run failed: %v\n%s", err, out)
	}
}

// writeFeedbackFixtures writes the real feedback script plus a fake `vix` that
// records each `thread create` invocation to callLog. Returns (scriptPath,
// fakeVixPath, callLog).
func writeFeedbackFixtures(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, feedbackHookID+".sh")
	if err := os.WriteFile(scriptPath, []byte(feedbackScript), 0o755); err != nil {
		t.Fatal(err)
	}
	callLog := filepath.Join(dir, "calls.log")
	fakeVix := filepath.Join(dir, "fakevix")
	fake := "#!/usr/bin/env bash\nprintf 'CALL\\n' >> " + shellQuote(callLog) + "\ncat >> " + shellQuote(callLog) + "\nprintf '\\n' >> " + shellQuote(callLog) + "\n"
	if err := os.WriteFile(fakeVix, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	return scriptPath, fakeVix, callLog
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func countCalls(t *testing.T, callLog string) int {
	t.Helper()
	b, err := os.ReadFile(callLog)
	if err != nil {
		return 0 // never created = no calls
	}
	return strings.Count(string(b), "CALL")
}

func TestFeedbackScriptCountsAndFiresOnce(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	scriptPath, fakeVix, callLog := writeFeedbackFixtures(t)
	home := t.TempDir()
	// State now lives directly in the script's own dir (the hook dir).
	feedbackDir := filepath.Dir(scriptPath)

	// Runs 1..9: below threshold, nothing delivered.
	for i := 1; i <= 9; i++ {
		runFeedbackScript(t, scriptPath, home, fakeVix)
	}
	if n := countCalls(t, callLog); n != 0 {
		t.Fatalf("delivered %d times before threshold, want 0", n)
	}
	if _, err := os.Stat(filepath.Join(feedbackDir, "asked")); err == nil {
		t.Fatal("asked marker created before threshold")
	}

	// Run 10: fires exactly once, with the message_file spec.
	runFeedbackScript(t, scriptPath, home, fakeVix)
	if n := countCalls(t, callLog); n != 1 {
		t.Fatalf("delivered %d times at threshold, want 1", n)
	}
	body, _ := os.ReadFile(callLog)
	if !strings.Contains(string(body), "message_file") || !strings.Contains(string(body), "vix needs your feedback") {
		t.Errorf("delivered spec missing message_file/title: %s", body)
	}

	// Runs 11..15: never again.
	for i := 11; i <= 15; i++ {
		runFeedbackScript(t, scriptPath, home, fakeVix)
	}
	if n := countCalls(t, callLog); n != 1 {
		t.Fatalf("delivered %d times total, want exactly 1", n)
	}
}

func TestFeedbackScriptConcurrentFiresOnce(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	scriptPath, fakeVix, callLog := writeFeedbackFixtures(t)
	home := t.TempDir()
	feedbackDir := filepath.Dir(scriptPath)
	if err := os.MkdirAll(feedbackDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-seed the counter to 9 so every concurrent run crosses the threshold.
	if err := os.WriteFile(filepath.Join(feedbackDir, "count.log"), []byte(strings.Repeat("1\n", 9)), 0o644); err != nil {
		t.Fatal(err)
	}

	const n = 8
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			ctx := `{"source":"startup","vix_bin":"` + fakeVix + `","socket_path":"/tmp/x.sock"}`
			cmd := exec.Command("bash", scriptPath)
			cmd.Env = append(os.Environ(), "HOME="+home)
			cmd.Stdin = strings.NewReader(ctx)
			cmd.CombinedOutput()
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	if c := countCalls(t, callLog); c != 1 {
		t.Fatalf("concurrent crossings delivered %d times, want exactly 1", c)
	}
}
