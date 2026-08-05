package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/get-vix/vix/internal/protocol"
)

// TestConfigWatcherJobStateIgnored pins that a write to a job's runtime state
// file (jobs/<id>/state.json), which lives inside the watched job subdirectory,
// does NOT trigger a scheduler reload — otherwise every run's state write would
// loop — while a write to a sibling job.json does.
func TestConfigWatcherJobStateIgnored(t *testing.T) {
	dir := t.TempDir()
	jobsDir := filepath.Join(dir, "jobs")
	if err := os.MkdirAll(filepath.Join(jobsDir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	reloaded := make(chan struct{}, 8)
	cw := &configWatcher{
		jobsDir:  jobsDir,
		w:        w,
		debounce: make(map[string]*time.Timer),
	}
	reload := func() { reloaded <- struct{}{} }

	// A write to <id>/state.json (inside a job subdir) belongs to the tree but
	// must be ignored.
	stateEv := fsnotify.Event{Name: filepath.Join(jobsDir, "demo", "state.json"), Op: fsnotify.Write}
	if !cw.handleSpecEvent(stateEv, jobsDir, "job.json", reload) {
		t.Fatal("state-file event should be recognised as part of the jobs tree")
	}
	select {
	case <-reloaded:
		t.Fatal("<id>/state.json write must not trigger a reload")
	case <-time.After(2 * configWatchDebounce):
	}

	// Its atomic-write temp file (state.*.tmp) must be ignored too.
	tmpEv := fsnotify.Event{Name: filepath.Join(jobsDir, "demo", "state.1234.tmp"), Op: fsnotify.Create}
	if !cw.handleSpecEvent(tmpEv, jobsDir, "job.json", reload) {
		t.Fatal("state temp-file event should be recognised as part of the jobs tree")
	}
	select {
	case <-reloaded:
		t.Fatal("state temp-file write must not trigger a reload")
	case <-time.After(2 * configWatchDebounce):
	}

	// A path outside the tree is not handled here.
	if cw.handleSpecEvent(fsnotify.Event{Name: filepath.Join(dir, "other"), Op: fsnotify.Write}, jobsDir, "job.json", reload) {
		t.Fatal("event outside the jobs tree must not be handled")
	}

	// A write to a nested job.json must trigger a reload.
	specEv := fsnotify.Event{Name: filepath.Join(jobsDir, "demo", "job.json"), Op: fsnotify.Write}
	cw.handleSpecEvent(specEv, jobsDir, "job.json", reload)
	select {
	case <-reloaded:
	case <-time.After(3 * time.Second):
		t.Fatal("nested job.json write should trigger a reload")
	}
}

// TestConfigWatcherJobMemoryIgnored pins that an arbitrary file a run writes
// inside its own job subdirectory (e.g. memory.md) does NOT trigger a scheduler
// reload — only the spec file (job.json) does. Without this, a job that keeps a
// memory file in its own directory would reload the scheduler on every run.
func TestConfigWatcherJobMemoryIgnored(t *testing.T) {
	dir := t.TempDir()
	jobsDir := filepath.Join(dir, "jobs")
	if err := os.MkdirAll(filepath.Join(jobsDir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	reloaded := make(chan struct{}, 8)
	cw := &configWatcher{
		jobsDir:  jobsDir,
		w:        w,
		debounce: make(map[string]*time.Timer),
	}
	reload := func() { reloaded <- struct{}{} }

	// A write to <id>/memory.md belongs to the tree but must be ignored.
	memEv := fsnotify.Event{Name: filepath.Join(jobsDir, "demo", "memory.md"), Op: fsnotify.Write}
	if !cw.handleSpecEvent(memEv, jobsDir, "job.json", reload) {
		t.Fatal("memory-file event should be recognised as part of the jobs tree")
	}
	select {
	case <-reloaded:
		t.Fatal("<id>/memory.md write must not trigger a reload")
	case <-time.After(2 * configWatchDebounce):
	}

	// The spec file in the same directory still reloads.
	specEv := fsnotify.Event{Name: filepath.Join(jobsDir, "demo", "job.json"), Op: fsnotify.Write}
	cw.handleSpecEvent(specEv, jobsDir, "job.json", reload)
	select {
	case <-reloaded:
	case <-time.After(3 * time.Second):
		t.Fatal("nested job.json write should trigger a reload")
	}
}

// TestConfigWatcherHookStateIgnored pins that a write to a hook's runtime state
// file (hooks/<id>/state.json), now written on every fire, does NOT trigger a
// hook registry reload — otherwise every fire's state write would loop — while
// a write to a sibling hook.json does.
func TestConfigWatcherHookStateIgnored(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(filepath.Join(hooksDir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	reloaded := make(chan struct{}, 8)
	cw := &configWatcher{
		hooksDir: hooksDir,
		w:        w,
		debounce: make(map[string]*time.Timer),
	}
	reload := func() { reloaded <- struct{}{} }

	// A write to <id>/state.json (inside a hook subdir) belongs to the tree but
	// must be ignored.
	stateEv := fsnotify.Event{Name: filepath.Join(hooksDir, "demo", "state.json"), Op: fsnotify.Write}
	if !cw.handleSpecEvent(stateEv, hooksDir, "hook.json", reload) {
		t.Fatal("state-file event should be recognised as part of the hooks tree")
	}
	select {
	case <-reloaded:
		t.Fatal("<id>/state.json write must not trigger a reload")
	case <-time.After(2 * configWatchDebounce):
	}

	// Its atomic-write temp file (state.*.tmp) must be ignored too.
	tmpEv := fsnotify.Event{Name: filepath.Join(hooksDir, "demo", "state.1234.tmp"), Op: fsnotify.Create}
	if !cw.handleSpecEvent(tmpEv, hooksDir, "hook.json", reload) {
		t.Fatal("state temp-file event should be recognised as part of the hooks tree")
	}
	select {
	case <-reloaded:
		t.Fatal("state temp-file write must not trigger a reload")
	case <-time.After(2 * configWatchDebounce):
	}

	// A write to a nested hook.json must trigger a reload.
	specEv := fsnotify.Event{Name: filepath.Join(hooksDir, "demo", "hook.json"), Op: fsnotify.Write}
	cw.handleSpecEvent(specEv, hooksDir, "hook.json", reload)
	select {
	case <-reloaded:
	case <-time.After(3 * time.Second):
		t.Fatal("nested hook.json write should trigger a reload")
	}
}

// an fsnotify event on workflow.json is debounced, the file is re-read, and
// every live thread receives a fresh event.workflows_available.
func TestConfigWatcherReloadsWorkflows(t *testing.T) {
	dir := t.TempDir()
	wfPath := filepath.Join(dir, "workflow.json")
	if err := os.WriteFile(wfPath, []byte(`{"workflows":[]}`), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Add(dir); err != nil {
		t.Fatal(err)
	}

	sess := &Thread{
		eventChan: make(chan protocol.ThreadEvent, 16),
		ctx:       context.Background(),
	}
	srv := &Server{threads: map[string]*Thread{"s1": sess}}
	cw := &configWatcher{
		server:   srv,
		wfPath:   wfPath,
		langPath: filepath.Join(dir, "languages.json"),
		w:        w,
		debounce: make(map[string]*time.Timer),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cw.run(ctx)

	// Save a valid workflow — this is the "user saved the file" event.
	const wf = `{"workflows":[{"name":"Plan","entry_point":{"id":"s1"},` +
		`"steps":{"s1":{"type":"agent","agent":"a","prompt":"x"}}}]}`
	if err := os.WriteFile(wfPath, []byte(wf), 0644); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-sess.eventChan:
			if ev.Type != "event.workflows_available" {
				continue
			}
			wa, ok := ev.Data.(protocol.EventWorkflowsAvailable)
			if !ok {
				t.Fatalf("unexpected event data type %T", ev.Data)
			}
			if len(wa.Workflows) == 1 && wa.Workflows[0].Name == "Plan" {
				return // success
			}
		case <-deadline:
			t.Fatal("timed out waiting for reloaded event.workflows_available")
		}
	}
}
