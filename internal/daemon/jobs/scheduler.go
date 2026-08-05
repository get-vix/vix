package jobs

import (
	"context"
	"fmt"
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
	Status   string // ok | error | skipped | timeout
	Err      string
	ThreadID string

	// Detail surfaced back to the scheduler's RunLogger so all run-log lines
	// are emitted from one place. AgentTurns and Denials enrich the "finished"
	// line; Errors become one "error" line each.
	AgentTurns int
	Denials    []string
	Errors     []RunError
}

// RunError is a single error captured during a run, with source naming where it
// came from (e.g. "agent", "start_refused", "timeout", "persist").
type RunError struct {
	Source  string
	Message string
}

// Runner executes one job run: an isolated thread driving the resolved
// prompt, through spec.Workflow when set. ctx carries the per-run timeout;
// implementations must return when it is cancelled.
type Runner func(ctx context.Context, spec Spec, resolvedPrompt string) RunResult

// RunLogger records structured job-run lifecycle and error entries. It is
// injected into the scheduler (nil-safe: every call is guarded) so the jobs
// package stays free of filesystem/path concerns — the daemon supplies the
// implementation that writes the daily JSONL files.
type RunLogger interface {
	// Started is called just before the runner is invoked.
	Started(spec Spec)
	// Error records one error encountered during the run. threadID may be empty
	// (e.g. a prompt-resolution failure before any thread exists).
	Error(spec Spec, threadID, source, msg string)
	// Finished is called once the run completes, with its total wall-clock time.
	Finished(spec Spec, res RunResult, dur time.Duration)
}

// Scheduler owns the timer loop over the job store. One per daemon.
type Scheduler struct {
	store         *Store
	runner        Runner
	maxConcurrent int

	// notify broadcasts a job lifecycle event to attached clients. Nil-safe.
	notify func(eventType string, data any)
	// logger records structured run-log entries. Nil-safe (guarded by the
	// logStarted/logError/logFinished helpers).
	logger RunLogger
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
// notify (optional) broadcasts lifecycle events; logger (optional) records
// structured run-log entries; maxConcurrent <= 0 uses the default. Runtime
// state is seeded from the store's persisted state file so
// restarts don't forget completed one-shots, auto-disabled jobs, or pending
// next-run times; reconcile drops entries whose spec vanished and resets
// those whose spec changed (SpecHash).
func NewScheduler(store *Store, runner Runner, notify func(string, any), logger RunLogger, maxConcurrent int) *Scheduler {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrentRuns
	}
	return &Scheduler{
		store:         store,
		runner:        runner,
		maxConcurrent: maxConcurrent,
		notify:        notify,
		logger:        logger,
		resolvePrompt: func(spec Spec) string {
			return prompt.GetLoader().Resolve(spec.Prompt, nil, spec.CWD, nil)
		},
		specs:    make(map[string]Spec),
		state:    store.LoadState(),
		running:  make(map[string]bool),
		reloadCh: make(chan struct{}, 1),
		sem:      make(chan struct{}, maxConcurrent),
	}
}

// JobSnapshot is a read-only view of a job for external consumers (the web UI
// jobs tab). It carries the spec fields the UI renders, with permissions
// resolved to their effective booleans and the timeout defaulted.
type JobSnapshot struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Enabled        bool    `json:"enabled"`
	Trigger        Trigger `json:"trigger"`
	WorkflowID     string  `json:"workflow_id"`     // named workflow reference, if any
	WorkflowInline bool    `json:"workflow_inline"` // true when the job carries an inline workflow definition
	Prompt         string  `json:"prompt"`
	CWD            string  `json:"cwd"`
	AutoWrite      bool    `json:"auto_write"`
	AutoDirs       bool    `json:"auto_dirs"`
	Timeout        string  `json:"timeout"`
	CreatedBy      string  `json:"created_by"`

	// Runtime history, attached from the job's persisted State. LastRunAt is the
	// zero time when the job has never run; RecentRuns is newest-last, capped at
	// maxRecentRuns. NextRunAt is the next scheduled fire (zero when disabled,
	// completed, or auto-disabled). Running is true while a run is in flight.
	NextRunAt  time.Time   `json:"next_run_at,omitempty"`
	LastRunAt  time.Time   `json:"last_run_at,omitempty"`
	LastStatus string      `json:"last_status,omitempty"`
	Running    bool        `json:"running,omitempty"`
	RecentRuns []RunRecord `json:"recent_runs,omitempty"`
}

// Snapshot returns the current set of job specs as read-only views, sorted by
// id for stable rendering. Safe to call concurrently with the timer loop.
func (s *Scheduler) Snapshot() []JobSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JobSnapshot, 0, len(s.specs))
	for id, spec := range s.specs {
		timeout := spec.Timeout
		if timeout == "" {
			timeout = "10m" // documented default (DefaultTimeout)
		}
		snap := JobSnapshot{
			ID:             id,
			Name:           spec.Name,
			Enabled:        spec.Enabled,
			Trigger:        spec.Trigger,
			WorkflowID:     spec.WorkflowID,
			WorkflowInline: spec.Workflow != nil,
			Prompt:         spec.Prompt,
			CWD:            spec.CWD,
			AutoWrite:      spec.AutoWrite(),
			AutoDirs:       spec.AutoDirs(),
			Timeout:        timeout,
			CreatedBy:      spec.CreatedBy,
		}
		if st := s.state[id]; st != nil {
			snap.NextRunAt = st.NextRunAt
			snap.LastRunAt = st.LastRunAt
			snap.LastStatus = st.LastStatus
			snap.RecentRuns = append([]RunRecord(nil), st.RecentRuns...)
		}
		snap.Running = s.running[id]
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Reload asks the loop to re-read the spec directory (config watcher hook).
// Non-blocking; coalesces bursts.
func (s *Scheduler) Reload() {
	select {
	case s.reloadCh <- struct{}{}:
	default:
	}
}

// SetEnabled flips a job spec's `enabled` field on disk and reschedules. The
// edit is surgical — only the enabled value is rewritten via the store, so the
// rest of the user's job.json (field order, formatting, unknown keys) is
// preserved. Reconcile applies the change to the timer loop synchronously (so a
// Snapshot right after reflects it) and notifies attached clients.
func (s *Scheduler) SetEnabled(id string, enabled bool) error {
	if err := s.store.SetEnabled(id, enabled); err != nil {
		return err
	}
	s.reconcile(time.Now())
	s.Reload()
	return nil
}

// CreateJob validates, persists, and schedules a new job spec. The spec must
// carry a non-empty ID that is not already in use (derive one with UniqueID).
// It returns an error if the spec is invalid, the ID is taken, or persistence
// fails. On success the new job is reconciled into the timer loop synchronously
// (so a Snapshot taken right after includes it) and the loop is woken to
// recompute its next wake.
func (s *Scheduler) CreateJob(spec Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if s.idTaken(spec.ID) {
		return fmt.Errorf("job id %q already exists", spec.ID)
	}
	if err := s.store.SaveSpec(spec); err != nil {
		return err
	}
	s.reconcile(time.Now())
	s.Reload()
	return nil
}

// runIDKey carries a pre-generated run/thread id through a run's context.
type runIDKey struct{}

// WithRunID stamps a pre-generated run/thread id onto ctx so an on-demand run
// adopts it instead of minting its own. The daemon's Runner reads it via
// RunIDFromContext, letting RunNow's caller learn the thread id up front.
func WithRunID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, runIDKey{}, id)
}

// RunIDFromContext returns the run id stamped by WithRunID, or "" when absent.
func RunIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(runIDKey{}).(string)
	return id
}

// RunNow fires the job with the given id immediately, out of band from the
// schedule. Unlike a scheduled run it does not advance NextRunAt or complete a
// one-shot — it only records the outcome — and it runs even when the job is
// disabled or already completed. It refuses only when a run for that id is
// already in flight. The run executes in the background using runID as its
// thread id (threaded through ctx); validation errors surface synchronously.
func (s *Scheduler) RunNow(ctx context.Context, id, runID string) error {
	s.mu.Lock()
	spec, ok := s.specs[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("job %q not found", id)
	}
	if s.running[id] {
		s.mu.Unlock()
		return fmt.Errorf("job %q is already running", id)
	}
	s.running[id] = true
	st := s.state[id]
	if st == nil {
		st = &State{SpecHash: SpecHash(spec)}
		s.state[id] = st
	}
	st.LastRunAt = time.Now()
	s.persistLocked()
	s.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	s.notifyJobsChanged()
	go s.execute(WithRunID(ctx, runID), spec, true)
	return nil
}

// UniqueID derives a filesystem-safe job id from base (typically the job name),
// guaranteed not to collide with an existing spec. Falls back to "job" when
// base slugifies to empty; appends -2, -3, … on collision.
func (s *Scheduler) UniqueID(base string) string {
	id := slugify(base)
	if id == "" {
		id = "job"
	}
	candidate := id
	for i := 2; s.idTaken(candidate); i++ {
		candidate = fmt.Sprintf("%s-%d", id, i)
	}
	return candidate
}

// idTaken reports whether a job id is already used, in memory or on disk.
func (s *Scheduler) idTaken(id string) bool {
	s.mu.Lock()
	_, inMem := s.specs[id]
	s.mu.Unlock()
	return inMem || s.store.SpecExists(id)
}

// slugify reduces s to a lowercase, dash-separated id of [a-z0-9-].
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
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
	// feedback loop (skill reads <id>/state.json) and the UI can surface it.
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
		s.store.DeleteState(id)
		delete(s.state, id)
	}

	if !s.caughtUp {
		s.caughtUp = true
		s.applyCatchupLocked(now)
	}

	s.maybeNudgeLocked(specs)

	s.persistLocked()
	s.mu.Unlock()
	s.notifyJobsChanged()
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
		st.appendRun(RunRecord{At: now, Status: StatusSkipped, Error: "missed while the daemon was down"})
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

	if len(due) > 0 {
		s.notifyJobsChanged()
	}

	for _, id := range due {
		go s.execute(ctx, specsByID[id], false)
	}
}

// execute resolves the prompt and drives one run through the Runner, then
// applies the result. Runs in its own goroutine, bounded by s.sem. manual is
// true for on-demand runs (RunNow): those record their outcome but leave the
// schedule untouched (see applyResult).
func (s *Scheduler) execute(ctx context.Context, spec Spec, manual bool) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		s.applyResult(spec, RunResult{Status: StatusSkipped, Err: "daemon shutting down"}, manual, 0)
		return
	}

	resolved := s.resolvePrompt(spec)

	// Prompt-file failure handling, stricter than interactive workflows: the
	// loader inlines an error marker; never send that to the model.
	missingFile := strings.Contains(resolved, "[Error: file ")
	if spec.SkipIfEmpty && (missingFile || effectivelyEmpty(resolved)) {
		s.applyResult(spec, RunResult{Status: StatusSkipped}, manual, 0)
		return
	}
	if missingFile {
		s.logError(spec, "", "prompt_resolve", "prompt file not found")
		s.applyResult(spec, RunResult{Status: StatusError, Err: "prompt file not found"}, manual, 0)
		return
	}

	s.notifyEvent("event.job_run", map[string]any{
		"job_id": spec.ID, "name": spec.Name, "status": "started",
	})
	s.logStarted(spec)

	start := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, spec.TimeoutDuration())
	defer cancel()
	res := s.runner(runCtx, spec, resolved)
	if runCtx.Err() == context.DeadlineExceeded && res.Status != StatusOK {
		res.Status = StatusTimeout
		if res.Err == "" {
			res.Err = "run exceeded timeout " + spec.TimeoutDuration().String()
		}
	}
	for _, e := range res.Errors {
		s.logError(spec, res.ThreadID, e.Source, e.Message)
	}
	s.logFinished(spec, res, time.Since(start))
	s.applyResult(spec, res, manual, time.Since(start))
}

// logStarted/logError/logFinished forward to the injected RunLogger, guarding
// nil so tests and logger-less embeddings stay silent.
func (s *Scheduler) logStarted(spec Spec) {
	if s.logger != nil {
		s.logger.Started(spec)
	}
}

func (s *Scheduler) logError(spec Spec, threadID, source, msg string) {
	if s.logger != nil {
		s.logger.Error(spec, threadID, source, msg)
	}
}

func (s *Scheduler) logFinished(spec Spec, res RunResult, dur time.Duration) {
	if s.logger != nil {
		s.logger.Finished(spec, res, dur)
	}
}

// applyResult records a finished run and computes the next fire time. The run
// is appended to State.RecentRuns (newest last, capped at maxRecentRuns) with
// its wall-clock duration. When manual is true (an on-demand RunNow), the run's
// outcome is recorded but the schedule is left untouched: no rescheduling, no
// one-shot completion, and no error-streak/auto-disable bookkeeping — a
// user-initiated test run must not consume a one-shot or shift the next cron
// slot.
func (s *Scheduler) applyResult(spec Spec, res RunResult, manual bool, dur time.Duration) {
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
	if res.ThreadID != "" {
		st.LastThreadID = res.ThreadID
	}
	rec := RunRecord{At: now, Status: res.Status, Error: res.Err, ThreadID: res.ThreadID}
	if dur > 0 {
		rec.Duration = dur.String()
	}
	st.appendRun(rec)

	if manual {
		s.persistLocked()
		s.mu.Unlock()
		s.notifyJobsChanged()
		if res.Status != StatusSkipped {
			s.notifyEvent("event.job_done", map[string]any{
				"job_id":    spec.ID,
				"name":      spec.Name,
				"status":    res.Status,
				"error":     res.Err,
				"thread_id": res.ThreadID,
			})
		}
		return
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
	s.notifyJobsChanged()

	// Skips are silent by design (cheap-poll rule, heartbeat OK, empty
	// whiteboard): nothing happened, so nobody is notified.
	if res.Status != StatusSkipped {
		s.notifyEvent("event.job_done", map[string]any{
			"job_id":    spec.ID,
			"name":      spec.Name,
			"status":    res.Status,
			"error":     res.Err,
			"thread_id": res.ThreadID,
		})
	}
	if autoDisabled {
		s.logError(spec, res.ThreadID, "auto_disable", "disabled after repeated failures")
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

// persistLocked writes every job's state file (<id>/state.json). Caller holds
// s.mu. Best-effort: write errors are ignored (state is reconstructible).
func (s *Scheduler) persistLocked() {
	for id, st := range s.state {
		s.store.SaveStateFor(id, st)
	}
}

// notifyEvent forwards to the notify hook when set.
func (s *Scheduler) notifyEvent(eventType string, data any) {
	if s.notify != nil {
		s.notify(eventType, data)
	}
}

// notifyJobsChanged tells attached clients the jobs list changed (a run started
// or finished, a spec was enabled/disabled, or the directory was reloaded) so
// the Jobs & Triggers tab re-fetches. Best-effort; nil-safe via notifyEvent.
func (s *Scheduler) notifyJobsChanged() {
	s.notifyEvent("event.jobs_changed", map[string]any{})
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
