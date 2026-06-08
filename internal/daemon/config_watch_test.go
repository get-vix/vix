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

// TestConfigWatcherReloadsWorkflows exercises the full hot-reload path:
// an fsnotify event on workflow.json is debounced, the file is re-read, and
// every live session receives a fresh event.workflows_available.
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

	sess := &Session{
		eventChan: make(chan protocol.SessionEvent, 16),
		ctx:       context.Background(),
	}
	srv := &Server{sessions: map[string]*Session{"s1": sess}}
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
