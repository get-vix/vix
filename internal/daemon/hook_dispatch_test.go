package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/daemon/hooks"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

// newHookThread builds a Thread whose server carries a hook registry loaded
// from dir, plus all tool handlers so executeToolDirect works.
func newHookThread(t *testing.T, cwd, hooksDir, origin string) *Thread {
	t.Helper()
	srv := &Server{handlers: make(map[string]HandlerFunc), serverCtx: context.Background()}
	RegisterToolHandlers(srv)
	srv.hookRegistry = hooks.NewRegistry(hooks.NewStore(hooksDir))
	return &Thread{
		server:                         srv,
		cwd:                            cwd,
		model:                          "test/model",
		headless:                       true,
		origin:                         origin,
		enableAutomaticWritePermission: true,
		enableAutomaticDirectoryAccess: true,
		readFiles:                      make(map[string]bool),
		startTime:                      time.Now(),
		projectConfig: ProjectConfig{
			ToolTimeouts: ToolTimeouts{Default: 30 * time.Second, Max: 60 * time.Second},
		},
	}
}

func writeHookSpec(t *testing.T, dir, name, body string) {
	t.Helper()
	id := strings.TrimSuffix(name, ".json")
	hookDir := filepath.Join(dir, id)
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "hook.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAnnounceStart_SourceClassification pins the regression where finalizeReplay
// (which clears attachRecord) ran before the ThreadStart source was computed,
// so every resumed thread fired as "startup" — wrongly tripping startup-gated
// hooks like the feedback counter. A fresh thread must fire only the "startup"
// hook; a resumed thread must fire only the "resume" hook.
func TestAnnounceStart_SourceClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		resume bool
		want   string
		absent string
	}{
		{"fresh thread fires startup", false, "startup.flag", "resume.flag"},
		{"resumed thread fires resume", true, "resume.flag", "startup.flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hd := t.TempDir()
			writeHookSpec(t, hd, "startup.json", `{"id":"startup","enabled":true,"mode":"async","trigger":{"event":"ThreadStart","matcher":"startup"},"command":"touch startup.flag"}`)
			writeHookSpec(t, hd, "resume.json", `{"id":"resume","enabled":true,"mode":"async","trigger":{"event":"ThreadStart","matcher":"resume"},"command":"touch resume.flag"}`)

			cwd := t.TempDir()
			s := newHookThread(t, cwd, hd, "")
			s.eventChan = make(chan protocol.ThreadEvent, 4)
			s.ctx, s.cancel = context.WithCancel(context.Background())
			defer s.cancel()
			if tc.resume {
				s.attachRecord = &threadRecord{ID: "r1", ThreadMode: "chat"}
			}

			s.announceStart()

			// Async hooks fire in goroutines; poll for the expected marker.
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(cwd, tc.want)); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := os.Stat(filepath.Join(cwd, tc.want)); err != nil {
				t.Fatalf("expected %s (the %q hook should have fired)", tc.want, strings.TrimSuffix(tc.want, ".flag"))
			}
			// The other source's hook started concurrently; by now its marker
			// would exist too if it had wrongly matched.
			if _, err := os.Stat(filepath.Join(cwd, tc.absent)); err == nil {
				t.Fatalf("%s should not exist (wrong-source hook fired)", tc.absent)
			}
		})
	}
}

func TestPreToolUseHook_BlockingDeny(t *testing.T) {
	hd := t.TempDir()
	// exit 2 with a reason on stderr → deny.
	writeHookSpec(t, hd, "block.json", `{"id":"block","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"PreToolUse","matcher":"write_file"},"command":"echo blocked-by-test >&2; exit 2"}`)

	s := newHookThread(t, t.TempDir(), hd, "")
	_, reason, denied := s.preToolUseHook(context.Background(), "write_file", map[string]any{"path": "x.txt"})
	if !denied {
		t.Fatal("expected write_file to be denied by hook")
	}
	if reason != "blocked-by-test" {
		t.Fatalf("reason = %q, want blocked-by-test", reason)
	}

	// A non-matching tool is unaffected.
	if _, _, denied := s.preToolUseHook(context.Background(), "read_file", map[string]any{"path": "x"}); denied {
		t.Fatal("read_file should not be denied")
	}
}

func TestPreToolUseHook_NonBlockingDenyDowngraded(t *testing.T) {
	hd := t.TempDir()
	// Same exit 2, but not blocking → must NOT deny (downgraded to context).
	writeHookSpec(t, hd, "warn.json", `{"id":"warn","enabled":true,"mode":"sync","trigger":{"event":"PreToolUse","matcher":"write_file"},"command":"echo nope >&2; exit 2"}`)
	s := newHookThread(t, t.TempDir(), hd, "")
	if _, _, denied := s.preToolUseHook(context.Background(), "write_file", map[string]any{"path": "x"}); denied {
		t.Fatal("non-blocking hook must not deny")
	}
}

func TestPreToolUseHook_Modify(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "mod.json", `{"id":"mod","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"PreToolUse","matcher":"write_file"},"command":"echo '{\"behavior\":\"modify\",\"input\":{\"path\":\"safe.txt\"}}'"}`)
	s := newHookThread(t, t.TempDir(), hd, "")
	newInput, _, denied := s.preToolUseHook(context.Background(), "write_file", map[string]any{"path": "danger.txt"})
	if denied {
		t.Fatal("modify should not deny")
	}
	if newInput == nil || newInput["path"] != "safe.txt" {
		t.Fatalf("expected rewritten path, got %v", newInput)
	}
}

func TestPostToolUseHook_AppendsContext(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "ctx.json", `{"id":"ctx","enabled":true,"mode":"sync","trigger":{"event":"PostToolUse","matcher":"bash"},"command":"echo extra-note"}`)
	s := newHookThread(t, t.TempDir(), hd, "")
	res := &ToolResult{Output: "original"}
	s.postToolUseHook(context.Background(), "bash", map[string]any{"command": "ls"}, res)
	if res.Output == "original" || !strings.Contains(res.Output, "extra-note") {
		t.Fatalf("expected context appended, got %q", res.Output)
	}
}

func TestUserPromptSubmitHook_Veto(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "veto.json", `{"id":"veto","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"UserPromptSubmit"},"command":"exit 2"}`)
	s := newHookThread(t, t.TempDir(), hd, "")
	_, _, denied := s.userPromptSubmitHook(context.Background(), "do something")
	if !denied {
		t.Fatal("expected prompt to be vetoed")
	}
}

func TestHooks_RecursionGuard(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "block.json", `{"id":"block","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"PreToolUse"},"command":"exit 2"}`)
	// origin "vix" marks a hook/job thread: it must not fire hooks.
	s := newHookThread(t, t.TempDir(), hd, "vix")
	if _, _, denied := s.preToolUseHook(context.Background(), "write_file", map[string]any{"path": "x"}); denied {
		t.Fatal("vix-origin threads must not fire hooks (recursion guard)")
	}
}

// TestDispatch_PreToolUseDenyShortCircuits drives the real dispatcher to confirm
// a blocking deny prevents the tool from executing at all.
func TestDispatch_PreToolUseDenyShortCircuits(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "block.json", `{"id":"block","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"PreToolUse","matcher":"write_file"},"command":"exit 2"}`)
	s := newHookThread(t, t.TempDir(), hd, "")

	executed := false
	opts := dispatchOptions{
		executeTool: func(name string, input map[string]any) *ToolResult {
			executed = true
			return &ToolResult{Output: "ran"}
		},
		beforeTool: s.preToolUseHook,
		afterTool:  s.postToolUseHook,
	}
	msg := &llm.Message{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "write_file", Input: map[string]any{"path": "x"}}}}
	results := dispatchToolCalls(context.Background(), msg, opts)
	if executed {
		t.Fatal("executeTool must not run when a blocking hook denies")
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

// waitForFlag polls for a marker file written by an async command hook.
func waitForFlag(t *testing.T, dir, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected marker %s to be created", name)
}

// TestSubagentHooks_FireStartAndStop proves SubagentStart/SubagentStop fire with
// the agent type as the matcher field, and that a hook scoped to a different
// agent type does not fire.
func TestSubagentHooks_FireStartAndStop(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "sa-start.json", `{"id":"sa-start","enabled":true,"mode":"async","trigger":{"event":"SubagentStart","matcher":"general"},"command":"touch start.flag"}`)
	writeHookSpec(t, hd, "sa-stop.json", `{"id":"sa-stop","enabled":true,"mode":"async","trigger":{"event":"SubagentStop","matcher":"general"},"command":"touch stop.flag"}`)
	writeHookSpec(t, hd, "sa-other.json", `{"id":"sa-other","enabled":true,"mode":"async","trigger":{"event":"SubagentStart","matcher":"reviewer"},"command":"touch other.flag"}`)

	cwd := t.TempDir()
	s := newHookThread(t, cwd, hd, "")
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()

	s.fireSubagentStart("general", "task_1", "do work")
	s.fireSubagentStop("general", "task_1", &SubagentResult{Output: "done"})

	waitForFlag(t, cwd, "start.flag")
	waitForFlag(t, cwd, "stop.flag")
	if _, err := os.Stat(filepath.Join(cwd, "other.flag")); err == nil {
		t.Fatal("a reviewer-matched SubagentStart fired for a general subagent")
	}
}

// TestSubagentHooks_RecursionGuard confirms a vix-origin thread (a hook/job
// run) does not fire subagent hooks.
func TestSubagentHooks_RecursionGuard(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "sa-start.json", `{"id":"sa-start","enabled":true,"mode":"async","trigger":{"event":"SubagentStart"},"command":"touch start.flag"}`)
	cwd := t.TempDir()
	s := newHookThread(t, cwd, hd, "vix")
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()

	s.fireSubagentStart("general", "task_1", "do work")
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(cwd, "start.flag")); err == nil {
		t.Fatal("vix-origin threads must not fire subagent hooks (recursion guard)")
	}
}

// TestPermissionRequestHook_BlockingDeny proves a blocking PermissionRequest
// hook denies the confirmation with its reason, and is scoped by tool name.
func TestPermissionRequestHook_BlockingDeny(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "perm.json", `{"id":"perm","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"PermissionRequest","matcher":"write_file"},"command":"echo no-writes >&2; exit 2"}`)
	s := newHookThread(t, t.TempDir(), hd, "")

	reason, denied := s.permissionRequestHook(context.Background(), "write_file", map[string]any{"path": "x"}, nil)
	if !denied {
		t.Fatal("expected permission to be denied by hook")
	}
	if reason != "no-writes" {
		t.Fatalf("reason = %q, want no-writes", reason)
	}
	if _, denied := s.permissionRequestHook(context.Background(), "bash", map[string]any{}, nil); denied {
		t.Fatal("bash should not be denied (matcher is write_file)")
	}
}

// TestPermissionRequestHook_NonBlockingDowngraded proves a non-blocking sync
// PermissionRequest hook cannot deny (its veto is downgraded).
func TestPermissionRequestHook_NonBlockingDowngraded(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "perm.json", `{"id":"perm","enabled":true,"mode":"sync","trigger":{"event":"PermissionRequest","matcher":"write_file"},"command":"echo nope >&2; exit 2"}`)
	s := newHookThread(t, t.TempDir(), hd, "")
	if _, denied := s.permissionRequestHook(context.Background(), "write_file", map[string]any{"path": "x"}, nil); denied {
		t.Fatal("a non-blocking permission hook must not deny")
	}
}

// TestCompactionHooks_FireWithTrigger proves PreCompact and PostCompact fire
// through compactMessages with the trigger ("auto"/"manual") as the matcher
// field, and that PostCompact only fires on a successful compaction.
func TestCompactionHooks_FireWithTrigger(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "pre.json", `{"id":"pre","enabled":true,"mode":"async","trigger":{"event":"PreCompact","matcher":"auto"},"command":"touch pre.flag"}`)
	writeHookSpec(t, hd, "post.json", `{"id":"post","enabled":true,"mode":"async","trigger":{"event":"PostCompact","matcher":"auto"},"command":"touch post.flag"}`)
	writeHookSpec(t, hd, "manual.json", `{"id":"manual","enabled":true,"mode":"async","trigger":{"event":"PreCompact","matcher":"manual"},"command":"touch manual.flag"}`)

	fake := &fakeCompactionLLM{summary: "SUMMARY"}
	s, _ := newCompactionTestThread(t, fake)
	cwd := t.TempDir()
	s.cwd = cwd
	srv := &Server{handlers: make(map[string]HandlerFunc), serverCtx: context.Background()}
	srv.hookRegistry = hooks.NewRegistry(hooks.NewStore(hd))
	s.server = srv

	s.compactMessages(4, 1, true) // auto compaction

	waitForFlag(t, cwd, "pre.flag")
	waitForFlag(t, cwd, "post.flag")
	if _, err := os.Stat(filepath.Join(cwd, "manual.flag")); err == nil {
		t.Fatal("a manual-matched PreCompact fired for an auto compaction")
	}
}
