package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/get-vix/vix/internal/daemon/hooks"
	"github.com/get-vix/vix/internal/daemon/jobs"
	"github.com/get-vix/vix/internal/protocol"
)

// seedJobScheduler installs a scheduler with one enabled cron job on the test
// server and returns it.
func seedJobScheduler(t *testing.T, s *Server) *jobs.Scheduler {
	t.Helper()
	runner := func(context.Context, jobs.Spec, string) jobs.RunResult {
		return jobs.RunResult{Status: jobs.StatusOK}
	}
	sched := jobs.NewScheduler(jobs.NewStore(filepath.Join(s.homeVixDir, "jobs")), runner, nil, nil, 1)
	spec := jobs.Spec{
		ID:      "alpha",
		Name:    "Alpha",
		Enabled: true,
		Trigger: jobs.Trigger{Type: "cron", Expr: "@every 1m"},
		Prompt:  "hi",
		CWD:     t.TempDir(),
	}
	if err := sched.CreateJob(spec); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	s.jobScheduler = sched
	return sched
}

func TestJobListHandler(t *testing.T) {
	s := newRunTriggerTestServer(t)
	seedJobScheduler(t, s)

	resp, err := s.GetHandler("job.list")(map[string]any{})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("status = %v, want ok", resp["status"])
	}
	list, _ := resp["jobs"].([]protocol.JobSummary)
	if len(list) != 1 {
		t.Fatalf("jobs len = %d, want 1 (resp=%v)", len(list), resp)
	}
	j := list[0]
	if j.ID != "alpha" || !j.Enabled || j.Schedule != "@every 1m" {
		t.Fatalf("job summary = %+v", j)
	}
	if j.NextRunAt == "" {
		t.Fatal("enabled cron job summary must carry next_run_at")
	}
}

func TestJobSetEnabledHandler(t *testing.T) {
	s := newRunTriggerTestServer(t)
	seedJobScheduler(t, s)

	resp, err := s.GetHandler("job.set_enabled")(map[string]any{"id": "alpha", "enabled": false})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("status = %v, want ok", resp["status"])
	}

	// Persisted to disk.
	data, err := os.ReadFile(filepath.Join(s.homeVixDir, "jobs", "alpha", "job.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if spec["enabled"] != false {
		t.Fatalf("job.json enabled = %v, want false", spec["enabled"])
	}

	// Missing id errors.
	resp, _ = s.GetHandler("job.set_enabled")(map[string]any{"enabled": true})
	if resp["status"] != "error" {
		t.Fatalf("missing id should error, got %v", resp)
	}
}

func TestHookListAndSetEnabledHandlers(t *testing.T) {
	s := newRunTriggerTestServer(t)
	hooksDir := filepath.Join(s.homeVixDir, "hooks")
	hookDir := filepath.Join(hooksDir, "guard")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"guard","name":"Guard","enabled":true,"trigger":{"event":"PreToolUse","matcher":"bash"},"command":"true"}`
	if err := os.WriteFile(filepath.Join(hookDir, "hook.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s.hookRegistry = hooks.NewRegistry(hooks.NewStore(hooksDir))

	resp, err := s.GetHandler("hook.list")(map[string]any{})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	list, _ := resp["hooks"].([]protocol.HookSummary)
	if len(list) != 1 || list[0].ID != "guard" || list[0].Event != "PreToolUse" || !list[0].Enabled {
		t.Fatalf("hook summary = %+v", list)
	}

	resp, err = s.GetHandler("hook.set_enabled")(map[string]any{"id": "guard", "enabled": false})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("status = %v, want ok", resp["status"])
	}
	data, err := os.ReadFile(filepath.Join(hookDir, "hook.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if spec["enabled"] != false {
		t.Fatalf("hook.json enabled = %v, want false", spec["enabled"])
	}
}
