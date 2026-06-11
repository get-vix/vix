package jobs

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/get-vix/vix/internal/daemon/prompt"
)

const (
	// maxWakeInterval bounds how long the timer loop sleeps: waking at least
	// once a minute recovers from host sleep / clock jumps without relying on
	// the timer surviving them.
	maxWakeInterval = 60 * time.Second
	// minRefireGap is the floor between consecutive wakes, breaking hot loops
	// when a due time stays in the past.
	minRefireGap = 2 * time.Second
	// maxConsecutiveErrors auto-disables a job once reached.
	maxConsecutiveErrors = 5
	// catchupCap bounds how many overdue jobs run immediately after a daemon
	// restart; the rest are recorded as skipped and rescheduled.
	catchupCap = 3
	// backoffBase/backoffMax bound the retry backoff after a failed run.
	backoffBase = 30 * time.Second
	backoffMax  = 60 * time.Minute
	// DefaultMaxConcurrentRuns bounds parallel job runs unless configured.
	DefaultMaxConcurrentRuns = 2
)

// RunResult is what a Runner reports back for one job run.
type RunResult struct {
	Status    string // ok | error | skipped | timeout
	Err       string
	SessionID string
}

// Runner executes one job run: an isolated session driving the resolved
// prompt, through spec.Workflow when set. ctx carries the per-run timeout;
// implementations must return when it is cancelled.
type Runner func(ctx context.Context, spec Spec, resolvedPrompt string) RunResult

// Scheduler owns the timer loop over the job store. One per daemon.
type Scheduler struct {
	store         *Store
	runner        Runner
	maxConcurrent int

	// notify broadcasts a job lifecycle event to attached clients. Nil-safe.
	notify func(eventType string, data any)
	// resolvePrompt expands $(file:) templates at fire time. Injectable for
	// tests; defaults to the shared prompt loader resolving against spec.CWD.
	resolvePrompt func(spec Spec) string

	mu       sync.Mutex
	specs    map[string]Spec
	state    map[string]*State
	running  map[string]bool
	reloadCh chan struct{}
	sem      chan struct{}
	caughtUp bool // startup catch-up applied
}

// NewScheduler builds a scheduler over the store. runner executes runs;
// notify (optional) broadcasts lifecycle events; maxConcurrent <= 0 uses the
// default.
func NewScheduler(store *Store, runner Runner, notify func(string, any), maxConcurrent int) *Scheduler {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrentRuns
	}
	return &Scheduler{
		store:         store,
		runner:        runner,
		maxConcurrent: maxConcurrent,
		notify:        notify,
		resolvePrompt: func(spec Spec) string {
			return prompt.GetLoader().Resolve(spec.Prompt, nil, spec.CWD, nil)
		},
		specs:    make(map[string]Spec),
		state:    make(map[string]*State),
		running:  make(map[string]bool),
		reloadCh: make(chan struct{}, 1),
		sem:      make(chan struct{}, maxConcurrent),
	}
}

// Reload asks the loop to re-read the spec directory (config watcher hook).
// Non-blocking; coalesces bursts.
func (s *Scheduler) Reload() {
	select {
	case s.reloadCh <- struct{}{}:
	default:
	}
}

// Start runs the timer loop until ctx is cancelled. Call in a goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	s.reconcile(time.Now())
	for {
		timer := time.NewTimer(s.nextWake(time.Now()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.reloadCh:
			timer.Stop()
			s.reconcile(time.Now())
		case <-timer.C:
			s.tick(ctx, time.Now())
		}
	}
}

// nextWake computes how long to sleep before the next due job, clamped to
// [minRefireGap, maxWakeInterval].
func (s *Scheduler) nextWake(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := maxWakeInterval
	for id := range s.specs {
		st := s.state[id]
		if st == nil || !s.runnableLocked(id) || st.NextRunAt.IsZero() {
			continue
		}
		if until := st.NextRunAt.Sub(now); until < d {
			d = until
		}
	}
	if d < minRefireGap {
		d = minRefireGap
	}
	return d
}

// runnableLocked reports whether the job may fire: enabled, valid, not
// auto-disabled, not completed, not already running. Caller holds s.mu.
func (s *Scheduler) runnableLocked(id string) bool {
	spec, ok := s.specs[id]
	if !ok || !spec.Enabled || s.running[id] {
		return false
	}
	st := s.state[id]
	if st == nil || st.ValidationError != "" || st.AutoDisabled || st.Completed {
		return false
	}
	return true
}

// reconcile re-reads the spec directory and aligns the state map: new jobs get
// a next-run time, edited specs reset their error/disable state, vanished
// specs drop their state, invalid specs surface a validation error. The first
// reconcile after start additionally applies the catch-up policy for runs
// missed while the daemon was down.
func (s *Scheduler) reconcile(now time.Time) {
	specs, invalid := s.store.LoadSpecs()

	s.mu.Lock()
	s.specs = specs

	// Invalid specs: park a state entry carrying the validation error so the
	// feedback loop (skill reads jobs-state.json) and the UI can surface it.
	for id, msg := range invalid {
		st := s.state[id]
		if st == nil {
			st = &State{}
			s.state[id] = st
		}
		if st.ValidationError != msg {
			st.ValidationError = msg
			s.notifyEvent("event.job_run", map[string]any{
				"job_id": id, "status": "invalid", "error": msg,
			})
		}
		st.NextRunAt = time.Time{}
	}

	// Valid specs: create/refresh state.
	for id, spec := range specs {
		hash := SpecHash(spec)
		st := s.state[id]
		if st == nil {
			st = &State{SpecHash: hash}
			s.state[id] = st
		} else if st.SpecHash != hash {
			// Spec edited: clear derived state so the job gets a fresh start.
			st.SpecHash = hash
			st.ValidationError = ""
			st.AutoDisabled = false
			st.Completed = false
			st.ConsecutiveErrors = 0
			st.NextRunAt = time.Time{}
		} else {
			st.ValidationError = ""
		}
		if s.running[id] {
			continue
		}
		if !spec.Enabled || st.AutoDisabled || st.Completed {
			st.NextRunAt = time.Time{}
			continue
		}
		if st.NextRunAt.IsZero() {
			if next, ok := spec.NextRun(now); ok {
				st.NextRunAt = next
			} else if at := spec.AtTime(); !at.IsZero() && !at.After(now) {
				// A newly-created (or just-edited) one-shot whose time already
				// passed: the user explicitly asked for it — run it now.
				st.NextRunAt = now
			}
		}
	}

	// Drop state for ids that no longer exist on disk (neither valid nor
	// invalid). Running jobs keep their entry until they finish.
	for id := range s.state {
		if _, ok := specs[id]; ok {
			continue
		}
		if _, ok := invalid[id]; ok {
			continue
		}
		if s.running[id] {
			continue
		}
		delete(s.state, id)
	}

	if !s.caughtUp {
		s.caughtUp = true
		s.applyCatchupLocked(now)
	}

	s.maybeNudgeLocked(specs)

	s.persistLocked()
	s.mu.Unlock()
}

// maybeNudgeLocked emits a one-time event.job_nudge the first time a
// user-created job (anything beyond the shipped heartbeat) appears, so the TUI
// can suggest `vix daemon install` (start vixd at login → schedules survive
// reboots). Guarded by a marker file next to the specs. Caller holds s.mu.
func (s *Scheduler) maybeNudgeLocked(specs map[string]Spec) {
	if s.store.specsDir == "" {
		return
	}
	hasUserJob := false
	for _, spec := range specs {
		if spec.CreatedBy != "vix" {
			hasUserJob = true
			break
		}
	}
	if !hasUserJob {
		return
	}
	marker := filepath.Join(s.store.specsDir, ".nudge-shown")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if err := os.WriteFile(marker, []byte("1\n"), 0o644); err != nil {
		return
	}
	s.notifyEvent("event.job_nudge", map[string]any{})
}

// applyCatchupLocked implements the restart policy: of the jobs whose next run
// passed while the daemon was down, the catchupCap most overdue run once
// (their NextRunAt stays in the past, so the first tick fires them); the rest
// are recorded as skipped and rescheduled to their next future occurrence.
// Caller holds s.mu.
func (s *Scheduler) applyCatchupLocked(now time.Time) {
	var overdue []string
	for id := range s.specs {
		st := s.state[id]
		if st == nil || st.NextRunAt.IsZero() || st.NextRunAt.After(now) {
			continue
		}
		if !s.runnableLocked(id) {
			continue
		}
		overdue = append(overdue, id)
	}
	sort.Slice(overdue, func(i, j int) bool {
		return s.state[overdue[i]].NextRunAt.Before(s.state[overdue[j]].NextRunAt)
	})
	if len(overdue) <= catchupCap {
		return
	}
	for _, id := range overdue[catchupCap:] {
		st := s.state[id]
		st.LastStatus = StatusSkipped
		st.LastError = "missed while the daemon was down"
		spec := s.specs[id]
		if next, ok := spec.NextRun(now); ok {
			st.NextRunAt = next
		} else {
			st.NextRunAt = time.Time{}
			if spec.Trigger.Type == "at" {
				st.Completed = true
			}
		}
	}
}

// tick fires every due job, bounded by the worker pool.
func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	s.mu.Lock()
	var due []string
	for id := range s.specs {
		st := s.state[id]
		if st == nil || st.NextRunAt.IsZero() || st.NextRunAt.After(now) {
			continue
		}
		if !s.runnableLocked(id) {
			continue
		}
		due = append(due, id)
	}
	for _, id := range due {
		s.running[id] = true
		s.state[id].LastRunAt = now
	}
	if len(due) > 0 {
		s.persistLocked()
	}
	specsByID := make(map[string]Spec, len(due))
	for _, id := range due {
		specsByID[id] = s.specs[id]
	}
	s.mu.Unlock()

	for _, id := range due {
		go s.execute(ctx, specsByID[id])
	}
}

// execute resolves the prompt and drives one run through the Runner, then
// applies the result. Runs in its own goroutine, bounded by s.sem.
func (s *Scheduler) execute(ctx context.Context, spec Spec) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		s.applyResult(spec, RunResult{Status: StatusSkipped, Err: "daemon shutting down"})
		return
	}

	resolved := s.resolvePrompt(spec)

	// Prompt-file failure handling, stricter than interactive workflows: the
	// loader inlines an error marker; never send that to the model.
	missingFile := strings.Contains(resolved, "[Error: file ")
	if spec.SkipIfEmpty && (missingFile || effectivelyEmpty(resolved)) {
		s.applyResult(spec, RunResult{Status: StatusSkipped})
		return
	}
	if missingFile {
		s.applyResult(spec, RunResult{Status: StatusError, Err: "prompt file not found"})
		return
	}

	s.notifyEvent("event.job_run", map[string]any{
		"job_id": spec.ID, "name": spec.Name, "status": "started",
	})

	runCtx, cancel := context.WithTimeout(ctx, spec.TimeoutDuration())
	defer cancel()
	res := s.runner(runCtx, spec, resolved)
	if runCtx.Err() == context.DeadlineExceeded && res.Status != StatusOK {
		res.Status = StatusTimeout
		if res.Err == "" {
			res.Err = "run exceeded timeout " + spec.TimeoutDuration().String()
		}
	}
	s.applyResult(spec, res)
}

// applyResult records a finished run and computes the next fire time.
func (s *Scheduler) applyResult(spec Spec, res RunResult) {
	now := time.Now()
	s.mu.Lock()
	delete(s.running, spec.ID)
	st := s.state[spec.ID]
	if st == nil {
		st = &State{SpecHash: SpecHash(spec)}
		s.state[spec.ID] = st
	}
	st.LastStatus = res.Status
	st.LastError = res.Err
	if res.SessionID != "" {
		st.LastSessionID = res.SessionID
	}

	failed := res.Status == StatusError || res.Status == StatusTimeout
	if failed {
		st.ConsecutiveErrors++
		if st.ConsecutiveErrors >= maxConsecutiveErrors {
			st.AutoDisabled = true
		}
	} else {
		st.ConsecutiveErrors = 0
	}

	// Next occurrence: one-shots complete after their attempt; recurring jobs
	// take the natural next slot, pushed out by exponential backoff after a
	// failure so a flapping job doesn't burn tokens every slot.
	if spec.Trigger.Type == "at" {
		st.Completed = true
		st.NextRunAt = time.Time{}
	} else if st.AutoDisabled {
		st.NextRunAt = time.Time{}
	} else {
		next, ok := spec.NextRun(now)
		if !ok {
			st.NextRunAt = time.Time{}
		} else if failed {
			if b := now.Add(backoffFor(st.ConsecutiveErrors)); b.After(next) {
				next = b
			}
			st.NextRunAt = next
		} else {
			st.NextRunAt = next
		}
	}
	autoDisabled := st.AutoDisabled
	s.persistLocked()
	s.mu.Unlock()

	// Skips are silent by design (cheap-poll rule, heartbeat OK, empty
	// whiteboard): nothing happened, so nobody is notified.
	if res.Status != StatusSkipped {
		s.notifyEvent("event.job_done", map[string]any{
			"job_id":     spec.ID,
			"name":       spec.Name,
			"status":     res.Status,
			"error":      res.Err,
			"session_id": res.SessionID,
		})
	}
	if autoDisabled {
		s.notifyEvent("event.job_run", map[string]any{
			"job_id": spec.ID, "name": spec.Name, "status": "auto_disabled",
			"error": "disabled after repeated failures",
		})
	}
}

// backoffFor returns the retry delay after n consecutive failures.
func backoffFor(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := backoffBase << (n - 1)
	if d > backoffMax || d <= 0 {
		return backoffMax
	}
	return d
}

// persistLocked writes the state file. Caller holds s.mu. Best-effort.
func (s *Scheduler) persistLocked() {
	s.store.SaveState(s.state)
}

// notifyEvent forwards to the notify hook when set.
func (s *Scheduler) notifyEvent(eventType string, data any) {
	if s.notify != nil {
		s.notify(eventType, data)
	}
}

// effectivelyEmpty reports whether resolved prompt text carries no actionable
// content: blank lines, markdown headers, and HTML comments don't count. This
// is what lets the shipped heartbeat.md stub (explainer + commented examples)
// skip without spending tokens.
func effectivelyEmpty(text string) bool {
	// Strip HTML comment blocks first (the stub keeps its examples inside one).
	for {
		start := strings.Index(text, "<!--")
		if start < 0 {
			break
		}
		end := strings.Index(text[start:], "-->")
		if end < 0 {
			text = text[:start]
			break
		}
		text = text[:start] + text[start+end+len("-->"):]
	}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		return false
	}
	return true
}
