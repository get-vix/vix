package ui

import "testing"

// A draft thread (never connected, empty daemonThreadID) must NOT be
// reconnected when switched to. Reconnecting a draft calls thread.Connect,
// which starts a fresh daemon thread with no message — creating an empty
// ghost thread. Regression for: create two drafts, hit enter (no-op), switch
// back to the first draft, and it gets committed empty.
func TestStepWorkspaceThread_DraftNotReconnected(t *testing.T) {
	cfg := testCfg(t.TempDir())
	a := newThreadState(cfg, nil) // draft, daemonThreadID == ""
	b := newThreadState(cfg, nil) // draft
	m := &Model{cfg: cfg, threads: []*ThreadState{a, b}, selectedThread: 1, width: 100}

	m.stepWorkspaceThread(-1) // switch back to thread 0 (a draft)

	if m.selectedThread != 0 {
		t.Fatalf("expected to switch to thread 0, got %d", m.selectedThread)
	}
	if a.reconnecting {
		t.Fatal("switching to a draft must not set reconnecting (would connect an empty thread)")
	}
}

// A previously-live thread that lost its client (has a daemonThreadID) IS
// reconnected on switch — the fix must not regress genuine reconnects.
func TestStepWorkspaceThread_LiveThreadReconnects(t *testing.T) {
	cfg := testCfg(t.TempDir())
	a := newThreadState(cfg, nil)
	a.phase = phaseLive
	a.daemonThreadID = "sess-123" // connected before, client since dropped
	b := newThreadState(cfg, nil)
	m := &Model{cfg: cfg, threads: []*ThreadState{a, b}, selectedThread: 1, width: 100}

	m.stepWorkspaceThread(-1) // switch back to thread 0

	if !a.reconnecting {
		t.Fatal("a previously-connected thread should reconnect on switch")
	}
}
