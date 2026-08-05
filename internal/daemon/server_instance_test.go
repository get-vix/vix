package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/protocol"
)

// newInstanceTestServer builds a minimal Server bound to a short temp socket
// (Unix socket paths have a tight length limit, so t.TempDir() overflows it).
func newInstanceTestServer(t *testing.T) *Server {
	t.Helper()
	sock := filepath.Join("/tmp", fmt.Sprintf("vixd-inst-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { os.Remove(sock) })
	return NewServer(sock, config.Credential{}, "test-thread", "test-model", &config.DaemonConfig{}, nil)
}

// serve starts the server in a goroutine and waits until it is accepting
// connections. It returns the done channel (carrying ListenAndServe's result)
// and the cancel func.
func serve(t *testing.T, srv *Server) (<-chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()
	// Wait until the socket accepts connections.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", srv.sockPath)
		if err == nil {
			c.Close()
			return done, cancel
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("server did not start listening in time")
	return done, cancel
}

// registerInstance dials the server and sends an instance.register command,
// returning the open connection. Closing it signals the daemon the instance
// detached.
func registerInstance(t *testing.T, sock string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	data, _ := json.Marshal(protocol.InstanceRegisterData{Mode: "tui"})
	cmd := protocol.ThreadCommand{Type: "instance.register", Data: data}
	payload, _ := json.Marshal(cmd)
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		conn.Close()
		t.Fatalf("write register: %v", err)
	}
	return conn
}

// waitInstanceCount polls until the server's instance count equals want, or
// fails after a timeout.
func waitInstanceCount(t *testing.T, srv *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		srv.instanceMu.Lock()
		n := srv.instanceCount
		srv.instanceMu.Unlock()
		if n == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	srv.instanceMu.Lock()
	n := srv.instanceCount
	srv.instanceMu.Unlock()
	t.Fatalf("instance count = %d, want %d", n, want)
}

// TestInstanceCounting: register/disconnect cycles track the attached-instance
// count, and the daemon stays up regardless (it runs until signalled).
func TestInstanceCounting(t *testing.T) {
	srv := newInstanceTestServer(t)
	done, cancel := serve(t, srv)
	defer cancel()

	c1 := registerInstance(t, srv.sockPath)
	c2 := registerInstance(t, srv.sockPath)
	waitInstanceCount(t, srv, 2)

	c1.Close()
	waitInstanceCount(t, srv, 1)

	c2.Close()
	waitInstanceCount(t, srv, 0)

	// The daemon must not exit just because all instances left.
	select {
	case <-done:
		t.Fatal("daemon shut down after instances disconnected")
	case <-time.After(300 * time.Millisecond):
		// Still running, as expected.
	}

	cancel()
	<-done
}

// TestDaemonStopRPC: the daemon.stop handler shuts the server down.
func TestDaemonStopRPC(t *testing.T) {
	srv := newInstanceTestServer(t)
	RegisterBuiltinHandlers(srv)
	done, cancel := serve(t, srv)
	defer cancel()

	conn, err := net.Dial("unix", srv.sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"action":"daemon.stop"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not shut down after daemon.stop")
	}
}

// TestThreadVersionGate: a thread start from a mismatched client build is
// refused with code "version_mismatch"; only an exact match passes.
func TestThreadVersionGate(t *testing.T) {
	srv := newInstanceTestServer(t)
	srv.SetVersion("v1.2.3")
	_, cancel := serve(t, srv)
	defer cancel()

	startThread := func(clientVersion string) protocol.ThreadEvent {
		t.Helper()
		conn, err := net.Dial("unix", srv.sockPath)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		data, _ := json.Marshal(protocol.ThreadStartData{CWD: "/tmp", ClientVersion: clientVersion})
		cmd := protocol.ThreadCommand{Type: "thread.start", Data: data}
		payload, _ := json.Marshal(cmd)
		payload = append(payload, '\n')
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("write: %v", err)
		}
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		dec := json.NewDecoder(conn)
		var ev protocol.ThreadEvent
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("read event: %v", err)
		}
		return ev
	}

	// Mismatched client → refused.
	ev := startThread("v9.9.9")
	if ev.Type != "event.error" {
		t.Fatalf("mismatched client: got event %q, want event.error", ev.Type)
	}
	raw, _ := json.Marshal(ev.Data)
	var ee protocol.EventError
	json.Unmarshal(raw, &ee)
	if ee.Code != "version_mismatch" {
		t.Fatalf("mismatched client: got code %q, want version_mismatch", ee.Code)
	}

	// Empty client version (pre-gate build) → refused too.
	ev = startThread("")
	if ev.Type != "event.error" {
		t.Fatalf("empty client version: got event %q, want event.error", ev.Type)
	}

	// Matching client → thread starts.
	ev = startThread("v1.2.3")
	if ev.Type != "event.thread_started" {
		t.Fatalf("matching client: got event %q, want event.thread_started", ev.Type)
	}

	// Dev client against a stamped daemon → refused like any other mismatch.
	ev = startThread("dev")
	if ev.Type != "event.error" {
		t.Fatalf("dev client: got event %q, want event.error", ev.Type)
	}
}

// readInstanceEvent reads one event from ic with a deadline, failing the test on
// timeout or error.
func readInstanceEvent(t *testing.T, ic *InstanceClient) protocol.ThreadEvent {
	t.Helper()
	type res struct {
		ev  protocol.ThreadEvent
		err error
	}
	ch := make(chan res, 1)
	go func() {
		ev, err := ic.ReadEvent()
		ch <- res{ev, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("ReadEvent: %v", r.err)
		}
		return r.ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for instance event")
		return protocol.ThreadEvent{}
	}
}

// expectNoInstanceEvent asserts ic delivers no event within a short window.
func expectNoInstanceEvent(t *testing.T, ic *InstanceClient) {
	t.Helper()
	ch := make(chan protocol.ThreadEvent, 1)
	go func() {
		ev, err := ic.ReadEvent()
		if err == nil {
			ch <- ev
		}
	}()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected instance event %q", ev.Type)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestBroadcastToInstancesNoThread: a process-level broadcast reaches a
// registered instance even when no chat thread exists (the Group-2 fix).
func TestBroadcastToInstancesNoThread(t *testing.T) {
	srv := newInstanceTestServer(t)
	_, cancel := serve(t, srv)
	defer cancel()

	ic, err := RegisterInstance(srv.sockPath, "", "tui")
	if err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	defer ic.Close()
	waitInstanceCount(t, srv, 1)

	srv.broadcastThreadsChanged()

	ev := readInstanceEvent(t, ic)
	if ev.Type != "event.threads_changed" {
		t.Fatalf("got event %q, want event.threads_changed", ev.Type)
	}
}

// TestBroadcastToInstancesEachOnce: two registered instances each receive a
// single broadcast exactly once (removing the N-threads → N-notifications
// duplication).
func TestBroadcastToInstancesEachOnce(t *testing.T) {
	srv := newInstanceTestServer(t)
	_, cancel := serve(t, srv)
	defer cancel()

	ic1, err := RegisterInstance(srv.sockPath, "", "tui")
	if err != nil {
		t.Fatalf("RegisterInstance 1: %v", err)
	}
	defer ic1.Close()
	ic2, err := RegisterInstance(srv.sockPath, "", "tui")
	if err != nil {
		t.Fatalf("RegisterInstance 2: %v", err)
	}
	defer ic2.Close()
	waitInstanceCount(t, srv, 2)

	srv.broadcastJobsChanged()

	for i, ic := range []*InstanceClient{ic1, ic2} {
		ev := readInstanceEvent(t, ic)
		if ev.Type != "event.jobs_changed" {
			t.Fatalf("instance %d: got %q, want event.jobs_changed", i+1, ev.Type)
		}
		// No second copy of the same broadcast.
		expectNoInstanceEvent(t, ic)
	}
}

// TestBroadcastToInstancesNotOnThreads: a thread-carrying window is not
// double-notified — process-level broadcasts go to the instance channel only,
// never onto a live thread's event channel.
func TestBroadcastToInstancesNotOnThreads(t *testing.T) {
	srv := newInstanceTestServer(t)

	// A live thread with a buffered event channel: if the broadcast leaked onto
	// the thread fan-out, this channel would receive it.
	sess := &Thread{eventChan: make(chan protocol.ThreadEvent, 8)}
	srv.threadMu.Lock()
	srv.threads["s1"] = sess
	srv.threadMu.Unlock()

	srv.broadcastThreadsChanged()

	select {
	case ev := <-sess.eventChan:
		t.Fatalf("thread was double-notified with %q", ev.Type)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestBroadcastToInstancesConcurrent: concurrent broadcasts from many goroutines
// don't garble frames — each instance sees only well-formed, expected events
// (the per-connection serialized writer holds).
func TestBroadcastToInstancesConcurrent(t *testing.T) {
	srv := newInstanceTestServer(t)
	_, cancel := serve(t, srv)
	defer cancel()

	ic, err := RegisterInstance(srv.sockPath, "", "tui")
	if err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	defer ic.Close()
	waitInstanceCount(t, srv, 1)

	const n = 50
	go func() {
		for i := 0; i < n; i++ {
			go srv.BroadcastToInstances(protocol.ThreadEvent{Type: "event.threads_changed"})
		}
	}()

	// Read whatever arrives for a short window; every decoded frame must be a
	// valid, expected event (a garbled write would fail to decode → ReadEvent
	// error).
	deadline := time.Now().Add(1 * time.Second)
	got := 0
	for time.Now().Before(deadline) {
		type res struct {
			ev  protocol.ThreadEvent
			err error
		}
		ch := make(chan res, 1)
		go func() {
			ev, err := ic.ReadEvent()
			ch <- res{ev, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("ReadEvent after %d frames: %v", got, r.err)
			}
			if r.ev.Type != "event.threads_changed" {
				t.Fatalf("garbled frame: got %q", r.ev.Type)
			}
			got++
		case <-time.After(150 * time.Millisecond):
			// No more frames buffered; the non-blocking send may have dropped
			// some, which is acceptable (best-effort).
			return
		}
	}
}
