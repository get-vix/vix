package daemon

import (
	"testing"
)

// The whiteboard "See it on the whiteboard" link is built client-side from the
// web UI origin the daemon reports in the thread_started event. StartThread
// consumes that event during connect (the TUI event loop never sees it), so
// ThreadClient must capture WhiteboardBase and expose it. Regression guard for
// the bug where the link never rendered because the value was dropped here.
func TestThreadClient_CapturesWhiteboardBase(t *testing.T) {
	dir := t.TempDir()

	srv := newInstanceTestServer(t)
	srv.SetVersion("v-wb-test")
	srv.SetWebPort(4242)
	_, cancel := serve(t, srv)
	defer cancel()

	sc := NewThreadClient(srv.sockPath)
	sc.version = "v-wb-test"
	if err := sc.Connect(dir, dir, "test-model", false, false, false, true); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sc.Close()

	if got, want := sc.WhiteboardBase(), "http://localhost:4242"; got != want {
		t.Fatalf("WhiteboardBase() = %q, want %q", got, want)
	}
}

// When the web UI is disabled (web port 0), the daemon reports no origin and the
// client must surface an empty base so the TUI omits the link.
func TestThreadClient_WhiteboardBaseEmptyWhenWebDisabled(t *testing.T) {
	dir := t.TempDir()

	srv := newInstanceTestServer(t)
	srv.SetVersion("v-wb-test")
	// No SetWebPort → webPort stays 0 (web UI disabled).
	_, cancel := serve(t, srv)
	defer cancel()

	sc := NewThreadClient(srv.sockPath)
	sc.version = "v-wb-test"
	if err := sc.Connect(dir, dir, "test-model", false, false, false, true); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sc.Close()

	if got := sc.WhiteboardBase(); got != "" {
		t.Fatalf("WhiteboardBase() = %q, want empty when web UI disabled", got)
	}
}
