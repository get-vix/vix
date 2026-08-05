package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJobSnapshotNextRunAndRunning verifies Snapshot surfaces the scheduled
// next-run time and the live running flag.
func TestJobSnapshotNextRunAndRunning(t *testing.T) {
	st := newTestStore(t)
	sch := NewScheduler(st, noopRunner, nil, nil, 1)
	if err := sch.CreateJob(validSpec("alpha")); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	snap := sch.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	if snap[0].NextRunAt.IsZero() {
		t.Fatal("enabled cron job must carry a future NextRunAt in its snapshot")
	}
	if snap[0].Running {
		t.Fatal("idle job must not report Running")
	}

	// Mark the job in-flight and confirm the snapshot reflects it.
	sch.mu.Lock()
	sch.running["alpha"] = true
	sch.mu.Unlock()
	if !sch.Snapshot()[0].Running {
		t.Fatal("running job must report Running in its snapshot")
	}
}

// TestStoreSetEnabledSurgical verifies SetEnabled rewrites only the enabled
// value, preserving other keys, their order, and unknown fields.
func TestStoreSetEnabledSurgical(t *testing.T) {
	st := newTestStore(t)
	dir := filepath.Join(st.SpecsDir(), "alpha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Hand-authored file: enabled in the middle, plus a field the Spec struct
	// does not model ("custom_note") that must survive the edit.
	orig := `{
  "id": "alpha",
  "enabled": true,
  "custom_note": "keep me",
  "trigger": { "type": "cron", "expr": "@every 1m" },
  "prompt": "do the thing",
  "cwd": "/tmp"
}`
	path := filepath.Join(dir, "job.json")
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := st.SetEnabled("alpha", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, `"enabled": false`) {
		t.Fatalf("enabled not flipped to false:\n%s", s)
	}
	if !strings.Contains(s, `"custom_note": "keep me"`) {
		t.Fatalf("unknown field dropped by surgical edit:\n%s", s)
	}
	// Order preserved: id before enabled before custom_note before trigger.
	if !(strings.Index(s, `"id"`) < strings.Index(s, `"enabled"`) &&
		strings.Index(s, `"enabled"`) < strings.Index(s, `"custom_note"`) &&
		strings.Index(s, `"custom_note"`) < strings.Index(s, `"trigger"`)) {
		t.Fatalf("field order not preserved:\n%s", s)
	}
}

func TestStoreSetEnabledMissing(t *testing.T) {
	st := newTestStore(t)
	if err := st.SetEnabled("nope", false); err == nil {
		t.Fatal("want error setting enabled on a missing job.json")
	}
	if err := NewStore("").SetEnabled("x", false); err == nil {
		t.Fatal("want error with no spec dir")
	}
}

// TestSchedulerSetEnabled verifies SetEnabled flips the spec on disk and
// reschedules: a disabled job loses its NextRunAt, and re-enabling restores it.
func TestSchedulerSetEnabled(t *testing.T) {
	st := newTestStore(t)
	sch := NewScheduler(st, noopRunner, nil, nil, 1)
	if err := sch.CreateJob(validSpec("alpha")); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if err := sch.SetEnabled("alpha", false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	snap := sch.Snapshot()
	if snap[0].Enabled {
		t.Fatal("job should be disabled after SetEnabled(false)")
	}
	if !snap[0].NextRunAt.IsZero() {
		t.Fatal("disabled job must not carry a NextRunAt")
	}

	if err := sch.SetEnabled("alpha", true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	snap = sch.Snapshot()
	if !snap[0].Enabled || snap[0].NextRunAt.IsZero() {
		t.Fatalf("re-enabled job must reschedule: %+v", snap[0])
	}
}

// TestSchedulerSetEnabledNotifies verifies SetEnabled fires the jobs-changed
// notify hook so attached clients refresh.
func TestSchedulerSetEnabledNotifies(t *testing.T) {
	st := newTestStore(t)
	var changed int
	notify := func(eventType string, _ any) {
		if eventType == "event.jobs_changed" {
			changed++
		}
	}
	sch := NewScheduler(st, noopRunner, notify, nil, 1)
	if err := sch.CreateJob(validSpec("alpha")); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	before := changed
	if err := sch.SetEnabled("alpha", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if changed <= before {
		t.Fatal("SetEnabled must emit event.jobs_changed via reconcile")
	}
}
