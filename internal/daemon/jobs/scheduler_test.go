package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func validSpec(id string) Spec {
	return Spec{
		ID:      id,
		Enabled: true,
		Trigger: Trigger{Type: "cron", Expr: "@every 1m"},
		Prompt:  "do the thing",
		CWD:     "/tmp",
	}
}

// ── Spec ──

func TestSpecValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Spec)
		wantErr bool
	}{
		{"valid cron", func(s *Spec) {}, false},
		{"valid descriptor", func(s *Spec) { s.Trigger.Expr = "@daily" }, false},
		{"valid 5-field with tz", func(s *Spec) { s.Trigger = Trigger{Type: "cron", Expr: "*/30 9-19 * * *", TZ: "Europe/Paris"} }, false},
		{"valid at", func(s *Spec) { s.Trigger = Trigger{Type: "at", Time: "2030-01-01T09:00:00Z"} }, false},
		{"missing prompt", func(s *Spec) { s.Prompt = " " }, true},
		{"missing cwd", func(s *Spec) { s.CWD = "" }, true},
		{"missing id", func(s *Spec) { s.ID = "" }, true},
		{"bad cron expr", func(s *Spec) { s.Trigger.Expr = "61 * * * *" }, true},
		{"cron with time field", func(s *Spec) { s.Trigger.Time = "2030-01-01T09:00:00Z" }, true},
		{"at with expr", func(s *Spec) { s.Trigger = Trigger{Type: "at", Time: "2030-01-01T09:00:00Z", Expr: "@daily"} }, true},
		{"at bad time", func(s *Spec) { s.Trigger = Trigger{Type: "at", Time: "tomorrow"} }, true},
		{"unknown trigger", func(s *Spec) { s.Trigger.Type = "every" }, true},
		{"bad timeout", func(s *Spec) { s.Timeout = "ten minutes" }, true},
		{"negative timeout", func(s *Spec) { s.Timeout = "-5m" }, true},
		{"good timeout", func(s *Spec) { s.Timeout = "90s" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSpec("j")
			tc.mutate(&s)
			err := s.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
		})
	}
}

func TestSpecNextRun(t *testing.T) {
	now := time.Date(2026, 2, 8, 10, 0, 0, 0, time.UTC)

	s := validSpec("j")
	next, ok := s.NextRun(now)
	if !ok || !next.Equal(now.Add(time.Minute)) {
		t.Fatalf("@every 1m: got %v ok=%v, want %v", next, ok, now.Add(time.Minute))
	}

	s.Trigger = Trigger{Type: "cron", Expr: "0 9 * * *"}
	next, ok = s.NextRun(now)
	if !ok || next.Hour() != 9 || !next.After(now) {
		t.Fatalf("0 9 * * *: got %v ok=%v", next, ok)
	}

	s.Trigger = Trigger{Type: "at", Time: "2026-02-08T12:00:00Z"}
	next, ok = s.NextRun(now)
	if !ok || !next.Equal(time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("future at: got %v ok=%v", next, ok)
	}

	s.Trigger = Trigger{Type: "at", Time: "2026-02-08T08:00:00Z"}
	if _, ok = s.NextRun(now); ok {
		t.Fatal("past at: want ok=false")
	}
}

func TestPermissionDefaults(t *testing.T) {
	s := validSpec("j")
	if !s.AutoWrite() || !s.AutoDirs() {
		t.Fatal("permissions must default to true")
	}
	f := false
	s.Permissions = Permissions{AutoWrite: &f}
	if s.AutoWrite() || !s.AutoDirs() {
		t.Fatal("explicit auto_write=false must stick, auto_dirs stays true")
	}
}

// ── Store ──

func writeSpec(t *testing.T, dir string, s Spec) {
	t.Helper()
	data, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, s.ID+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStoreLoadSpecs(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("good"))
	os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{nope"), 0o644)
	bad := validSpec("bad")
	bad.Prompt = ""
	writeSpec(t, dir, bad)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644)

	st := NewStore(dir, filepath.Join(dir, "state.json"))
	specs, invalid := st.LoadSpecs()
	if len(specs) != 1 || specs["good"].ID != "good" {
		t.Fatalf("specs = %v", specs)
	}
	if len(invalid) != 2 {
		t.Fatalf("invalid = %v", invalid)
	}
	if invalid["bad"] == "" || invalid["broken"] == "" {
		t.Fatalf("missing validation errors: %v", invalid)
	}
}

func TestStoreIDDefaultsToFilename(t *testing.T) {
	dir := t.TempDir()
	s := validSpec("ignored")
	s.ID = ""
	data, _ := json.Marshal(s)
	os.WriteFile(filepath.Join(dir, "from-file.json"), data, 0o644)

	specs, invalid := NewStore(dir, "").LoadSpecs()
	if len(invalid) != 0 {
		t.Fatalf("invalid = %v", invalid)
	}
	if _, ok := specs["from-file"]; !ok {
		t.Fatalf("id should default to filename stem, got %v", specs)
	}
}

func TestStoreStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir, filepath.Join(dir, "state.json"))
	in := map[string]*State{
		"a": {LastStatus: StatusOK, ConsecutiveErrors: 0, SpecHash: "x"},
		"b": {LastStatus: StatusError, ConsecutiveErrors: 3},
	}
	if err := st.SaveState(in); err != nil {
		t.Fatal(err)
	}
	out := st.LoadState()
	if out["a"].LastStatus != StatusOK || out["b"].ConsecutiveErrors != 3 {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

// ── Scheduler ──

// testRunner records run invocations and returns scripted results.
type testRunner struct {
	mu      sync.Mutex
	runs    []string // job ids in run order
	prompts map[string]string
	result  func(spec Spec) RunResult
}

func newTestRunner(result func(Spec) RunResult) *testRunner {
	return &testRunner{prompts: make(map[string]string), result: result}
}

func (r *testRunner) fn(ctx context.Context, spec Spec, resolved string) RunResult {
	r.mu.Lock()
	r.runs = append(r.runs, spec.ID)
	r.prompts[spec.ID] = resolved
	r.mu.Unlock()
	if r.result != nil {
		return r.result(spec)
	}
	return RunResult{Status: StatusOK}
}

func (r *testRunner) count(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, got := range r.runs {
		if got == id {
			n++
		}
	}
	return n
}

func newTestScheduler(t *testing.T, dir string, runner *testRunner) *Scheduler {
	t.Helper()
	store := NewStore(dir, filepath.Join(dir, "state.json"))
	s := NewScheduler(store, runner.fn, nil, 2)
	// No $(file:) usage in scheduler tests: identity resolution keeps them
	// independent of the prompt loader.
	s.resolvePrompt = func(spec Spec) string { return spec.Prompt }
	return s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSchedulerFiresDueJob(t *testing.T) {
	dir := t.TempDir()
	s := validSpec("due")
	s.Trigger = Trigger{Type: "at", Time: time.Now().Add(-time.Minute).Format(time.RFC3339)}
	writeSpec(t, dir, s)

	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)

	now := time.Now()
	sched.reconcile(now)
	sched.tick(context.Background(), now)
	waitFor(t, "due job to run", func() bool { return runner.count("due") == 1 })

	sched.mu.Lock()
	st := sched.state["due"]
	sched.mu.Unlock()
	if st.LastStatus != StatusOK || !st.Completed {
		t.Fatalf("state = %+v, want ok+completed", st)
	}
}

func TestSchedulerCronComputesNext(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("rec"))

	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	now := time.Now()
	sched.reconcile(now)

	sched.mu.Lock()
	st := sched.state["rec"]
	sched.mu.Unlock()
	if st.NextRunAt.IsZero() || st.NextRunAt.Before(now) {
		t.Fatalf("new cron job must get a future NextRunAt, got %v", st.NextRunAt)
	}
	if runner.count("rec") != 0 {
		t.Fatal("new cron job must not fire immediately")
	}
}

func TestSchedulerCatchupCap(t *testing.T) {
	dir := t.TempDir()
	// Six overdue one-shots; only catchupCap (3) may run, the rest are skipped.
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		s := validSpec(string(rune('a' + i)))
		s.Trigger = Trigger{Type: "at", Time: base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)}
		writeSpec(t, dir, s)
	}
	store := NewStore(dir, filepath.Join(dir, "state.json"))
	// Pre-seed state as if a previous daemon had scheduled them (so they read
	// as "missed while down" rather than newly created).
	pre := make(map[string]*State)
	specs, _ := store.LoadSpecs()
	for id, sp := range specs {
		at := sp.AtTime()
		pre[id] = &State{NextRunAt: at, SpecHash: SpecHash(sp)}
	}
	store.SaveState(pre)

	runner := newTestRunner(nil)
	sched := NewScheduler(store, runner.fn, nil, 2)
	sched.resolvePrompt = func(spec Spec) string { return spec.Prompt }
	sched.state = store.LoadState()

	now := time.Now()
	sched.reconcile(now)
	sched.tick(context.Background(), now)

	waitFor(t, "catch-up runs", func() bool {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		return len(runner.runs) == catchupCap
	})
	time.Sleep(50 * time.Millisecond)
	runner.mu.Lock()
	ran := len(runner.runs)
	runner.mu.Unlock()
	if ran != catchupCap {
		t.Fatalf("ran %d overdue jobs, want %d", ran, catchupCap)
	}

	skipped := 0
	sched.mu.Lock()
	for _, st := range sched.state {
		if st.LastStatus == StatusSkipped && st.Completed {
			skipped++
		}
	}
	sched.mu.Unlock()
	if skipped != 3 {
		t.Fatalf("skipped = %d, want 3", skipped)
	}
}

func TestSchedulerBackoffAndAutoDisable(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("flaky"))
	runner := newTestRunner(func(Spec) RunResult {
		return RunResult{Status: StatusError, Err: "boom"}
	})
	sched := newTestScheduler(t, dir, runner)
	now := time.Now()
	sched.reconcile(now)

	for i := 1; i <= maxConsecutiveErrors; i++ {
		sched.mu.Lock()
		sched.state["flaky"].NextRunAt = now // force due
		sched.mu.Unlock()
		sched.tick(context.Background(), now)
		waitFor(t, "failed run", func() bool { return runner.count("flaky") == i })
		waitFor(t, "result applied", func() bool {
			sched.mu.Lock()
			defer sched.mu.Unlock()
			return !sched.running["flaky"] && sched.state["flaky"].ConsecutiveErrors == i
		})
	}

	sched.mu.Lock()
	st := sched.state["flaky"]
	sched.mu.Unlock()
	if !st.AutoDisabled {
		t.Fatalf("want auto-disabled after %d errors, state=%+v", maxConsecutiveErrors, st)
	}
	if !st.NextRunAt.IsZero() {
		t.Fatal("auto-disabled job must have no next run")
	}

	// Editing the spec re-arms the job.
	edited := validSpec("flaky")
	edited.Name = "edited"
	writeSpec(t, dir, edited)
	sched.reconcile(time.Now())
	sched.mu.Lock()
	st = sched.state["flaky"]
	sched.mu.Unlock()
	if st.AutoDisabled || st.ConsecutiveErrors != 0 || st.NextRunAt.IsZero() {
		t.Fatalf("spec edit must reset disable state, got %+v", st)
	}
}

func TestSchedulerBackoffDelaysNextRun(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("retry")) // @every 1m
	runner := newTestRunner(func(Spec) RunResult {
		return RunResult{Status: StatusError, Err: "boom"}
	})
	sched := newTestScheduler(t, dir, runner)
	now := time.Now()
	sched.reconcile(now)
	sched.mu.Lock()
	sched.state["retry"].NextRunAt = now
	sched.mu.Unlock()
	sched.tick(context.Background(), now)
	waitFor(t, "first failure", func() bool { return runner.count("retry") == 1 })
	waitFor(t, "next run computed", func() bool {
		sched.mu.Lock()
		defer sched.mu.Unlock()
		return !sched.state["retry"].NextRunAt.IsZero()
	})

	sched.mu.Lock()
	next := sched.state["retry"].NextRunAt
	sched.mu.Unlock()
	// Natural next (1m) > first backoff (30s), so the natural slot wins.
	if until := time.Until(next); until < 30*time.Second || until > 2*time.Minute {
		t.Fatalf("next run %v away, want ~1m", until)
	}
}

func TestSchedulerSkipIfEmpty(t *testing.T) {
	dir := t.TempDir()
	s := validSpec("hb")
	s.SkipIfEmpty = true
	s.Trigger = Trigger{Type: "at", Time: time.Now().Add(-time.Second).Format(time.RFC3339)}
	writeSpec(t, dir, s)

	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	sched.resolvePrompt = func(spec Spec) string {
		return "# Heartbeat\n\n<!-- add tasks here -->\n\n# Nothing yet\n"
	}
	now := time.Now()
	sched.reconcile(now)
	sched.tick(context.Background(), now)

	waitFor(t, "skip applied", func() bool {
		sched.mu.Lock()
		defer sched.mu.Unlock()
		st := sched.state["hb"]
		return st != nil && st.LastStatus == StatusSkipped
	})
	if runner.count("hb") != 0 {
		t.Fatal("effectively-empty prompt must not reach the runner")
	}
}

func TestSchedulerMissingPromptFile(t *testing.T) {
	dir := t.TempDir()
	s := validSpec("nofile")
	s.Trigger = Trigger{Type: "at", Time: time.Now().Add(-time.Second).Format(time.RFC3339)}
	writeSpec(t, dir, s)

	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	sched.resolvePrompt = func(spec Spec) string {
		return "[Error: file 'tasks/x.md' doesn't exist]"
	}
	now := time.Now()
	sched.reconcile(now)
	sched.tick(context.Background(), now)

	waitFor(t, "error applied", func() bool {
		sched.mu.Lock()
		defer sched.mu.Unlock()
		st := sched.state["nofile"]
		return st != nil && st.LastStatus == StatusError
	})
	if runner.count("nofile") != 0 {
		t.Fatal("error marker must not reach the runner")
	}
	sched.mu.Lock()
	if got := sched.state["nofile"].LastError; got != "prompt file not found" {
		t.Fatalf("LastError = %q", got)
	}
	sched.mu.Unlock()
}

func TestSchedulerValidationErrorSurfaced(t *testing.T) {
	dir := t.TempDir()
	bad := validSpec("bad")
	bad.Trigger.Expr = "not a cron"
	writeSpec(t, dir, bad)

	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	sched.reconcile(time.Now())

	sched.mu.Lock()
	st := sched.state["bad"]
	sched.mu.Unlock()
	if st == nil || st.ValidationError == "" {
		t.Fatalf("validation error must surface in state, got %+v", st)
	}

	// The state file (the skill's feedback loop) must carry it too.
	onDisk := sched.store.LoadState()
	if onDisk["bad"] == nil || onDisk["bad"].ValidationError == "" {
		t.Fatal("validation error must persist to jobs-state.json")
	}
}

func TestSchedulerRemovedSpecDropsState(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("gone"))
	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	sched.reconcile(time.Now())

	os.Remove(filepath.Join(dir, "gone.json"))
	sched.reconcile(time.Now())

	sched.mu.Lock()
	_, ok := sched.state["gone"]
	sched.mu.Unlock()
	if ok {
		t.Fatal("state for a removed spec must be dropped")
	}
}

func TestEffectivelyEmpty(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"", true},
		{"   \n\n", true},
		{"# Heading\n## Another\n", true},
		{"<!-- a comment\nspanning lines -->", true},
		{"# Heading\n<!-- hidden task example -->\n", true},
		{"check the deploy", false},
		{"# Heading\n- check the deploy\n", false},
	}
	for _, tc := range cases {
		if got := effectivelyEmpty(tc.text); got != tc.want {
			t.Errorf("effectivelyEmpty(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestBackoffFor(t *testing.T) {
	if backoffFor(1) != 30*time.Second {
		t.Fatalf("backoff(1) = %v", backoffFor(1))
	}
	if backoffFor(3) != 2*time.Minute {
		t.Fatalf("backoff(3) = %v", backoffFor(3))
	}
	if backoffFor(20) != backoffMax {
		t.Fatalf("backoff(20) = %v", backoffFor(20))
	}
}
