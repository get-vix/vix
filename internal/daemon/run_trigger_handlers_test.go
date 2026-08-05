package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/daemon/hooks"
	"github.com/get-vix/vix/internal/daemon/jobs"
)

func newRunTriggerTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{handlers: make(map[string]HandlerFunc), homeVixDir: t.TempDir()}
	RegisterBuiltinHandlers(s)
	return s
}

// waitForFileContains polls until path exists and contains substr, so a test
// can wait for an async run to persist before TempDir cleanup races it.
func waitForFileContains(t *testing.T, path, substr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), substr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to contain %q", path, substr)
}

func TestJobRunHandlerErrors(t *testing.T) {
	s := newRunTriggerTestServer(t)
	h := s.GetHandler("job.run")
	if h == nil {
		t.Fatal("job.run handler not registered")
	}

	resp, _ := h(map[string]any{})
	if resp["status"] != "error" {
		t.Fatalf("missing id should error, got %v", resp)
	}

	// No scheduler enabled → jobs engine disabled.
	resp, _ = h(map[string]any{"id": "x"})
	if resp["status"] != "error" {
		t.Fatalf("disabled jobs engine should error, got %v", resp)
	}
}

func TestJobRunHandlerSuccess(t *testing.T) {
	s := newRunTriggerTestServer(t)

	fakeRunner := func(context.Context, jobs.Spec, string) jobs.RunResult {
		return jobs.RunResult{Status: jobs.StatusOK}
	}
	sched := jobs.NewScheduler(jobs.NewStore(filepath.Join(s.homeVixDir, "jobs")), fakeRunner, nil, nil, 1)
	spec := jobs.Spec{
		ID:      "j",
		Enabled: true,
		Trigger: jobs.Trigger{Type: "cron", Expr: "@every 1m"},
		Prompt:  "hi",
		CWD:     t.TempDir(),
	}
	if err := sched.CreateJob(spec); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	s.jobScheduler = sched

	resp, err := s.GetHandler("job.run")(map[string]any{"id": "j"})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("status = %v, want ok (resp=%v)", resp["status"], resp)
	}
	if sid, _ := resp["thread_id"].(string); sid == "" {
		t.Fatalf("expected a non-empty thread_id, got %v", resp)
	}

	// Let the background run finish writing state before TempDir cleanup.
	waitForFileContains(t, filepath.Join(s.homeVixDir, "jobs", "j", "state.json"), "last_status")

	// Unknown id surfaces an error.
	resp, _ = s.GetHandler("job.run")(map[string]any{"id": "ghost"})
	if resp["status"] != "error" {
		t.Fatalf("unknown id should error, got %v", resp)
	}
}

func TestHookTriggerHandlerErrors(t *testing.T) {
	s := newRunTriggerTestServer(t)
	h := s.GetHandler("hook.trigger")
	if h == nil {
		t.Fatal("hook.trigger handler not registered")
	}

	resp, _ := h(map[string]any{})
	if resp["status"] != "error" {
		t.Fatalf("missing id should error, got %v", resp)
	}

	// No registry enabled → hooks engine disabled.
	resp, _ = h(map[string]any{"id": "x"})
	if resp["status"] != "error" {
		t.Fatalf("disabled hooks engine should error, got %v", resp)
	}
}

func TestHookTriggerHandlerUnknownID(t *testing.T) {
	s := newRunTriggerTestServer(t)
	s.hookRegistry = hooks.NewRegistry(hooks.NewStore(filepath.Join(s.homeVixDir, "hooks")))

	resp, _ := s.GetHandler("hook.trigger")(map[string]any{"id": "ghost"})
	if resp["status"] != "error" {
		t.Fatalf("unknown hook id should error, got %v", resp)
	}
}
