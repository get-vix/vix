package jobs

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeRunLogger records calls for assertions.
type fakeRunLogger struct {
	mu       sync.Mutex
	started  []string
	finished []RunResult
	errors   []RunError // Source/Message pairs
}

func (l *fakeRunLogger) Started(spec Spec) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started = append(l.started, spec.ID)
}

func (l *fakeRunLogger) Error(spec Spec, threadID, source, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, RunError{Source: source, Message: msg})
}

func (l *fakeRunLogger) Finished(spec Spec, res RunResult, dur time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.finished = append(l.finished, res)
}

// newLoggedScheduler builds a scheduler wired to the fake logger with identity
// prompt resolution (no $(file:) loader).
func newLoggedScheduler(t *testing.T, runner *testRunner, logger RunLogger) *Scheduler {
	t.Helper()
	s := NewScheduler(NewStore(t.TempDir()), runner.fn, nil, logger, 2)
	s.resolvePrompt = func(spec Spec) string { return spec.Prompt }
	return s
}

func TestSchedulerLogsStartedAndFinished(t *testing.T) {
	logger := &fakeRunLogger{}
	runner := newTestRunner(func(Spec) RunResult {
		return RunResult{Status: StatusOK, ThreadID: "sess-1", AgentTurns: 2}
	})
	s := newLoggedScheduler(t, runner, logger)

	s.execute(context.Background(), validSpec("alpha"), false)

	logger.mu.Lock()
	defer logger.mu.Unlock()
	if len(logger.started) != 1 || logger.started[0] != "alpha" {
		t.Fatalf("started = %v, want [alpha]", logger.started)
	}
	if len(logger.finished) != 1 {
		t.Fatalf("finished count = %d, want 1", len(logger.finished))
	}
	if logger.finished[0].Status != StatusOK || logger.finished[0].ThreadID != "sess-1" {
		t.Errorf("finished = %+v", logger.finished[0])
	}
	if len(logger.errors) != 0 {
		t.Errorf("unexpected errors: %v", logger.errors)
	}
}

func TestSchedulerLogsRunErrors(t *testing.T) {
	logger := &fakeRunLogger{}
	runner := newTestRunner(func(Spec) RunResult {
		return RunResult{
			Status:   StatusError,
			Err:      "boom",
			ThreadID: "sess-2",
			Errors:   []RunError{{Source: "agent", Message: "boom"}},
		}
	})
	s := newLoggedScheduler(t, runner, logger)

	s.execute(context.Background(), validSpec("beta"), false)

	logger.mu.Lock()
	defer logger.mu.Unlock()
	if len(logger.errors) != 1 || logger.errors[0].Source != "agent" {
		t.Fatalf("errors = %v, want one agent error", logger.errors)
	}
	if len(logger.finished) != 1 || logger.finished[0].Status != StatusError {
		t.Fatalf("finished = %+v, want one error finish", logger.finished)
	}
}

func TestSchedulerLogsPromptResolveError(t *testing.T) {
	logger := &fakeRunLogger{}
	runner := newTestRunner(nil)
	s := newLoggedScheduler(t, runner, logger)
	// Simulate the prompt loader inlining a missing-file marker.
	s.resolvePrompt = func(spec Spec) string { return "[Error: file foo.md not found]" }

	spec := validSpec("gamma")
	spec.SkipIfEmpty = false
	s.execute(context.Background(), spec, false)

	logger.mu.Lock()
	defer logger.mu.Unlock()
	if len(logger.errors) != 1 || logger.errors[0].Source != "prompt_resolve" {
		t.Fatalf("errors = %v, want one prompt_resolve error", logger.errors)
	}
	// Runner must not have been invoked on a prompt-resolution failure.
	if runner.count("gamma") != 0 {
		t.Error("runner should not run when the prompt failed to resolve")
	}
	if len(logger.started) != 0 {
		t.Errorf("started should not be logged on prompt failure, got %v", logger.started)
	}
}
