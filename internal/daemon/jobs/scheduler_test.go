package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/workflow"
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

// validInlineWorkflow returns a minimal structurally-valid workflow definition
// for exercising the inline-workflow path.
func validInlineWorkflow() *workflow.Def {
	return &workflow.Def{
		Name:       "inline",
		EntryPoint: workflow.StepRef{ID: "s"},
		Steps:      map[string]workflow.StepDef{"s": {Type: "bash", Command: "true"}},
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
		{"workflow_id only", func(s *Spec) { s.WorkflowID = "review" }, false},
		{"inline workflow only", func(s *Spec) { s.Workflow = validInlineWorkflow() }, false},
		{"workflow_id and inline both", func(s *Spec) { s.WorkflowID = "review"; s.Workflow = validInlineWorkflow() }, true},
		{"invalid inline workflow", func(s *Spec) { s.Workflow = &workflow.Def{Name: "broken"} }, true},
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
	jobDir := filepath.Join(dir, s.ID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "job.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStoreLoadSpecs(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("good"))
	// A subdir with a malformed job.json is reported invalid.
	os.MkdirAll(filepath.Join(dir, "broken"), 0o755)
	os.WriteFile(filepath.Join(dir, "broken", "job.json"), []byte("{nope"), 0o644)
	// A subdir with a structurally-invalid spec is reported invalid.
	bad := validSpec("bad")
	bad.Prompt = ""
	writeSpec(t, dir, bad)
	// A stray top-level file and a subdir without job.json are both ignored,
	// not reported.
	os.WriteFile(filepath.Join(dir, "stray.json"), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(dir, "not-a-job"), 0o755)
	os.WriteFile(filepath.Join(dir, "not-a-job", "notes.txt"), []byte("ignored"), 0o644)

	st := NewStore(dir)
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

func TestStoreIDDefaultsToDirname(t *testing.T) {
	dir := t.TempDir()
	s := validSpec("ignored")
	s.ID = ""
	data, _ := json.Marshal(s)
	os.MkdirAll(filepath.Join(dir, "from-dir"), 0o755)
	os.WriteFile(filepath.Join(dir, "from-dir", "job.json"), data, 0o644)

	specs, invalid := NewStore(dir).LoadSpecs()
	if len(invalid) != 0 {
		t.Fatalf("invalid = %v", invalid)
	}
	if _, ok := specs["from-dir"]; !ok {
		t.Fatalf("id should default to directory name, got %v", specs)
	}
}

// TestStoreStateFileInSpecsDirIgnored: each job's state.json now lives inside
// its own subdirectory alongside job.json. LoadSpecs must read the spec and not
// be confused by the sibling state file.
func TestStoreStateFileInSpecsDirIgnored(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("real"))
	st := NewStore(dir)
	if err := st.SaveStateFor("real", &State{LastStatus: StatusOK}); err != nil {
		t.Fatal(err)
	}
	specs, invalid := st.LoadSpecs()
	if len(invalid) != 0 {
		t.Fatalf("state file must not be reported invalid: %v", invalid)
	}
	if len(specs) != 1 || specs["real"].ID != "real" {
		t.Fatalf("specs = %v, want only real", specs)
	}
}

func TestStoreStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	in := map[string]*State{
		"a": {LastStatus: StatusOK, ConsecutiveErrors: 0, SpecHash: "x"},
		"b": {LastStatus: StatusError, ConsecutiveErrors: 3},
	}
	for id, s := range in {
		if err := st.SaveStateFor(id, s); err != nil {
			t.Fatal(err)
		}
	}
	out := st.LoadState()
	if out["a"].LastStatus != StatusOK || out["b"].ConsecutiveErrors != 3 {
		t.Fatalf("round trip mismatch: %+v", out)
	}

	// DeleteState drops one job's file without touching the rest.
	if err := st.DeleteState("a"); err != nil {
		t.Fatal(err)
	}
	out = st.LoadState()
	if _, ok := out["a"]; ok {
		t.Fatalf("a should be gone after DeleteState, got %+v", out)
	}
	if out["b"] == nil {
		t.Fatal("b must survive DeleteState(a)")
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
	store := NewStore(dir)
	s := NewScheduler(store, runner.fn, nil, nil, 2)
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

func TestSchedulerPersistsPerJobStateFile(t *testing.T) {
	dir := t.TempDir()
	s := validSpec("solo")
	s.Trigger = Trigger{Type: "at", Time: time.Now().Add(-time.Minute).Format(time.RFC3339)}
	writeSpec(t, dir, s)

	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	now := time.Now()
	sched.reconcile(now)
	sched.tick(context.Background(), now)
	waitFor(t, "job to run", func() bool { return runner.count("solo") == 1 })

	statePath := filepath.Join(dir, "solo", "state.json")
	waitFor(t, "per-job state.json to land", func() bool {
		_, err := os.Stat(statePath)
		return err == nil
	})

	// No global state file is written anywhere under the jobs dir.
	if _, err := os.Stat(filepath.Join(dir, "jobs-state.json")); !os.IsNotExist(err) {
		t.Fatalf("global jobs-state.json must not exist, stat err = %v", err)
	}
}

// TestSchedulerRestartDoesNotRerunCompletedOneShot: a daemon restart must not
// forget that a one-shot already fired — the scheduler seeds its state from
// the persisted state file, so the completed job stays completed instead of
// being treated as a newly-created overdue one-shot and re-run.
func TestSchedulerRestartDoesNotRerunCompletedOneShot(t *testing.T) {
	dir := t.TempDir()
	s := validSpec("once")
	s.Trigger = Trigger{Type: "at", Time: time.Now().Add(-time.Minute).Format(time.RFC3339)}
	writeSpec(t, dir, s)

	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	now := time.Now()
	sched.reconcile(now)
	sched.tick(context.Background(), now)
	waitFor(t, "one-shot to run", func() bool { return runner.count("once") == 1 })
	waitFor(t, "completion to persist", func() bool {
		st := sched.store.LoadState()["once"]
		return st != nil && st.Completed
	})

	// Simulate a daemon restart: a fresh scheduler over the same store.
	restarted := newTestScheduler(t, dir, runner)
	now = time.Now()
	restarted.reconcile(now)
	restarted.tick(context.Background(), now)
	time.Sleep(50 * time.Millisecond)
	if got := runner.count("once"); got != 1 {
		t.Fatalf("completed one-shot ran %d times after restart, want 1", got)
	}
	restarted.mu.Lock()
	st := restarted.state["once"]
	restarted.mu.Unlock()
	if st == nil || !st.Completed {
		t.Fatalf("restarted state = %+v, want completed", st)
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
	store := NewStore(dir)
	// Pre-seed state as if a previous daemon had scheduled them (so they read
	// as "missed while down" rather than newly created).
	specs, _ := store.LoadSpecs()
	for id, sp := range specs {
		at := sp.AtTime()
		store.SaveStateFor(id, &State{NextRunAt: at, SpecHash: SpecHash(sp)})
	}

	runner := newTestRunner(nil)
	sched := NewScheduler(store, runner.fn, nil, nil, 2)
	sched.resolvePrompt = func(spec Spec) string { return spec.Prompt }

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
		t.Fatal("validation error must persist to the job's state.json")
	}
}

func TestSchedulerRemovedSpecDropsState(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("gone"))
	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	sched.reconcile(time.Now())

	os.RemoveAll(filepath.Join(dir, "gone"))
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

// ── RecentRuns history ──

// TestStateAppendRunCapsAndOrders verifies the per-job run history keeps the
// most recent maxRecentRuns entries, newest last.
func TestStateAppendRunCapsAndOrders(t *testing.T) {
	var st State
	for i := 0; i < maxRecentRuns+5; i++ {
		st.appendRun(RunRecord{At: time.Unix(int64(i), 0), Status: StatusOK})
	}
	if len(st.RecentRuns) != maxRecentRuns {
		t.Fatalf("len(RecentRuns) = %d, want %d", len(st.RecentRuns), maxRecentRuns)
	}
	// The oldest 5 must have been dropped: first kept is index 5, last is index 14.
	if got := st.RecentRuns[0].At.Unix(); got != 5 {
		t.Fatalf("oldest kept run At = %d, want 5", got)
	}
	if got := st.RecentRuns[len(st.RecentRuns)-1].At.Unix(); got != int64(maxRecentRuns+4) {
		t.Fatalf("newest run At = %d, want %d", got, maxRecentRuns+4)
	}
}

// TestSchedulerRecordsRecentRuns verifies that finished runs accumulate in
// State.RecentRuns with their status, thread id, and a duration string.
func TestSchedulerRecordsRecentRuns(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("rec"))
	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	sched.reconcile(time.Now())

	sched.applyResult(validSpec("rec"), RunResult{Status: StatusOK, ThreadID: "s1"}, false, 1500*time.Millisecond)
	sched.applyResult(validSpec("rec"), RunResult{Status: StatusError, Err: "boom", ThreadID: "s2"}, false, 2*time.Second)

	sched.mu.Lock()
	runs := append([]RunRecord(nil), sched.state["rec"].RecentRuns...)
	sched.mu.Unlock()

	if len(runs) != 2 {
		t.Fatalf("RecentRuns len = %d, want 2", len(runs))
	}
	if runs[0].Status != StatusOK || runs[0].ThreadID != "s1" || runs[0].Duration != "1.5s" {
		t.Fatalf("run[0] = %+v, want ok/s1/1.5s", runs[0])
	}
	if runs[1].Status != StatusError || runs[1].Error != "boom" || runs[1].ThreadID != "s2" {
		t.Fatalf("run[1] = %+v, want error/boom/s2", runs[1])
	}
	if runs[0].At.IsZero() {
		t.Fatal("run record must carry a timestamp")
	}
}

// TestSchedulerRecentRunsPersist verifies the history round-trips through the
// per-job state.json file.
func TestSchedulerRecentRunsPersist(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, validSpec("rec"))
	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	sched.reconcile(time.Now())
	sched.applyResult(validSpec("rec"), RunResult{Status: StatusOK, ThreadID: "s1"}, false, time.Second)

	st := NewStore(dir).LoadState()["rec"]
	if st == nil || len(st.RecentRuns) != 1 || st.RecentRuns[0].ThreadID != "s1" {
		t.Fatalf("loaded RecentRuns = %+v, want one record for s1", st)
	}
}

// TestSchedulerManualRunRecordsRecentRun verifies an on-demand run is recorded
// in the history while leaving the schedule untouched (no one-shot completion).
func TestSchedulerManualRunRecordsRecentRun(t *testing.T) {
	dir := t.TempDir()
	s := validSpec("once")
	s.Trigger = Trigger{Type: "at", Time: time.Now().Add(time.Hour).Format(time.RFC3339)}
	writeSpec(t, dir, s)
	runner := newTestRunner(nil)
	sched := newTestScheduler(t, dir, runner)
	sched.reconcile(time.Now())

	sched.applyResult(s, RunResult{Status: StatusOK, ThreadID: "m1"}, true, time.Second)

	sched.mu.Lock()
	st := sched.state["once"]
	sched.mu.Unlock()
	if len(st.RecentRuns) != 1 || st.RecentRuns[0].ThreadID != "m1" {
		t.Fatalf("manual run not recorded: %+v", st.RecentRuns)
	}
	if st.Completed {
		t.Fatal("manual run must not complete a one-shot")
	}
}

// TestSchedulerCatchupRecordsSkip verifies overdue runs dropped by the
// catch-up cap leave a skipped entry in the history.
func TestSchedulerCatchupRecordsSkip(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		s := validSpec(string(rune('a' + i)))
		s.Trigger = Trigger{Type: "at", Time: base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)}
		writeSpec(t, dir, s)
	}
	store := NewStore(dir)
	specs, _ := store.LoadSpecs()
	for id, sp := range specs {
		store.SaveStateFor(id, &State{NextRunAt: sp.AtTime(), SpecHash: SpecHash(sp)})
	}
	runner := newTestRunner(nil)
	sched := NewScheduler(store, runner.fn, nil, nil, 2)
	sched.resolvePrompt = func(spec Spec) string { return spec.Prompt }
	sched.reconcile(time.Now())

	sched.mu.Lock()
	skippedWithHistory := 0
	for _, st := range sched.state {
		if st.LastStatus == StatusSkipped && len(st.RecentRuns) == 1 && st.RecentRuns[0].Status == StatusSkipped {
			skippedWithHistory++
		}
	}
	sched.mu.Unlock()
	if skippedWithHistory != 3 {
		t.Fatalf("catch-up skip history count = %d, want 3", skippedWithHistory)
	}
}
