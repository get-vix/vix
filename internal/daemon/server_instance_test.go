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
	return NewServer(sock, config.Credential{}, "test-session", "test-model", &config.DaemonConfig{}, PluginConfig{})
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
	cmd := protocol.SessionCommand{Type: "instance.register", Data: data}
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

// TestSessionVersionGate: a session start from a mismatched client build is
// refused with code "version_mismatch"; matching and dev builds pass.
func TestSessionVersionGate(t *testing.T) {
	srv := newInstanceTestServer(t)
	srv.SetVersion("v1.2.3")
	_, cancel := serve(t, srv)
	defer cancel()

	startSession := func(clientVersion string) protocol.SessionEvent {
		t.Helper()
		conn, err := net.Dial("unix", srv.sockPath)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		data, _ := json.Marshal(protocol.SessionStartData{CWD: "/tmp", ClientVersion: clientVersion})
		cmd := protocol.SessionCommand{Type: "session.start", Data: data}
		payload, _ := json.Marshal(cmd)
		payload = append(payload, '\n')
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("write: %v", err)
		}
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		dec := json.NewDecoder(conn)
		var ev protocol.SessionEvent
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("read event: %v", err)
		}
		return ev
	}

	// Mismatched client → refused.
	ev := startSession("v9.9.9")
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
	ev = startSession("")
	if ev.Type != "event.error" {
		t.Fatalf("empty client version: got event %q, want event.error", ev.Type)
	}

	// Matching client → session starts.
	ev = startSession("v1.2.3")
	if ev.Type != "event.session_started" {
		t.Fatalf("matching client: got event %q, want event.session_started", ev.Type)
	}

	// Dev client → gate skipped.
	ev = startSession("dev")
	if ev.Type != "event.session_started" {
		t.Fatalf("dev client: got event %q, want event.session_started", ev.Type)
	}
}
