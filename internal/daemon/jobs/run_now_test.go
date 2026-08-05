package jobs

import (
	"context"
	"testing"
	"time"
)

// TestRunNowFiresAndPreservesSchedule verifies an on-demand run executes the
// job and records its outcome, but leaves the recurring schedule untouched.
func TestRunNowFiresAndPreservesSchedule(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("alpha")) // cron "@every 1m"

	runner := newTestRunner(func(Spec) RunResult {
		return RunResult{Status: StatusOK, ThreadID: "sess-x"}
	})
	sched := newTestScheduler(t, dir, runner)
	sched.reconcile(time.Now())

	sched.mu.Lock()
	before := sched.state["alpha"].NextRunAt
	sched.mu.Unlock()
	if before.IsZero() {
		t.Fatal("precondition: cron job should have a future NextRunAt")
	}

	if err := sched.RunNow(context.Background(), "alpha", "sess-x"); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	waitFor(t, "manual run", func() bool { return runner.count("alpha") == 1 })
	waitFor(t, "result applied", func() bool {
		sched.mu.Lock()
		defer sched.mu.Unlock()
		return !sched.running["alpha"] && sched.state["alpha"].LastStatus == StatusOK
	})

	sched.mu.Lock()
	st := sched.state["alpha"]
	sched.mu.Unlock()
	if st.LastThreadID != "sess-x" {
		t.Fatalf("LastThreadID = %q, want sess-x", st.LastThreadID)
	}
	if !st.NextRunAt.Equal(before) {
		t.Fatalf("manual run changed the schedule: NextRunAt %v -> %v", before, st.NextRunAt)
	}
}

// TestRunNowUsesProvidedRunID checks the run id threaded through ctx reaches the
// runner via RunIDFromContext.
func TestRunNowUsesProvidedRunID(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("alpha"))

	var gotID string
	done := make(chan struct{})
	runner := newTestRunner(nil)
	// Wrap the runner to capture the context-threaded run id.
	wrapped := func(ctx context.Context, spec Spec, resolved string) RunResult {
		gotID = RunIDFromContext(ctx)
		close(done)
		return runner.fn(ctx, spec, resolved)
	}
	sched := NewScheduler(NewStore(dir), wrapped, nil, nil, 2)
	sched.resolvePrompt = func(spec Spec) string { return spec.Prompt }
	sched.reconcile(time.Now())

	if err := sched.RunNow(context.Background(), "alpha", "run-42"); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runner was not invoked")
	}
	if gotID != "run-42" {
		t.Fatalf("run id in ctx = %q, want run-42", gotID)
	}
	// Wait for the run to settle so state persistence doesn't race cleanup.
	waitFor(t, "run settled", func() bool {
		sched.mu.Lock()
		defer sched.mu.Unlock()
		return !sched.running["alpha"]
	})
}

// TestRunNowOneShotNotCompleted verifies a manual run of a one-shot "at" job
// does not mark it completed, so the scheduled fire still happens.
func TestRunNowOneShotNotCompleted(t *testing.T) {
	dir := t.TempDir()
	s := validSpec("once")
	s.Trigger = Trigger{Type: "at", Time: time.Now().Add(time.Hour).Format(time.RFC3339)}
	writeSpec(t, dir, s)

	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	sched.reconcile(time.Now())

	if err := sched.RunNow(context.Background(), "once", "sess-1"); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	waitFor(t, "manual run", func() bool { return runner.count("once") == 1 })
	waitFor(t, "result applied", func() bool {
		sched.mu.Lock()
		defer sched.mu.Unlock()
		return !sched.running["once"]
	})

	sched.mu.Lock()
	st := sched.state["once"]
	sched.mu.Unlock()
	if st.Completed {
		t.Fatal("manual run must not complete a one-shot job")
	}
	if st.NextRunAt.IsZero() {
		t.Fatal("manual run must not clear the one-shot's scheduled fire")
	}
}

// TestRunNowDisabledJobRuns verifies a disabled job can still be run on demand.
func TestRunNowDisabledJobRuns(t *testing.T) {
	dir := t.TempDir()
	s := validSpec("off")
	s.Enabled = false
	writeSpec(t, dir, s)

	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	sched.reconcile(time.Now())

	if err := sched.RunNow(context.Background(), "off", "sess-1"); err != nil {
		t.Fatalf("RunNow on disabled job: %v", err)
	}
	waitFor(t, "manual run of disabled job", func() bool { return runner.count("off") == 1 })
	waitFor(t, "run settled", func() bool {
		sched.mu.Lock()
		defer sched.mu.Unlock()
		return !sched.running["off"]
	})
}

// TestRunNowErrors covers the synchronous validation failures.
func TestRunNowErrors(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("alpha"))
	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	sched.reconcile(time.Now())

	if err := sched.RunNow(context.Background(), "ghost", "s"); err == nil {
		t.Fatal("want error for unknown job id")
	}

	sched.mu.Lock()
	sched.running["alpha"] = true
	sched.mu.Unlock()
	if err := sched.RunNow(context.Background(), "alpha", "s"); err == nil {
		t.Fatal("want error when a run is already in flight")
	}
	if n := runner.count("alpha"); n != 0 {
		t.Fatalf("runner should not have fired for an in-flight job, got %d", n)
	}
}
