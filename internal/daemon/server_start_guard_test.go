package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
)

// dialable reports whether a connection can be established to sock (a live
// listener). Used by the guard tests to assert liveness without depending on
// builtin handlers being registered on the minimal test server.
func dialable(sock string) bool {
	c, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// TestDaemonAlreadyListening covers the liveness probe that guards socket
// takeover: true only when a process is actively accepting connections.
func TestDaemonAlreadyListening(t *testing.T) {
	t.Run("nonexistent path", func(t *testing.T) {
		sock := filepath.Join("/tmp", fmt.Sprintf("vixd-guard-missing-%d.sock", time.Now().UnixNano()))
		if daemonAlreadyListening(sock) {
			t.Fatal("expected false for a nonexistent socket path")
		}
	})

	t.Run("stale socket file (no listener)", func(t *testing.T) {
		sock := filepath.Join("/tmp", fmt.Sprintf("vixd-guard-stale-%d.sock", time.Now().UnixNano()))
		if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil {
			t.Fatalf("write stale file: %v", err)
		}
		t.Cleanup(func() { os.Remove(sock) })
		if daemonAlreadyListening(sock) {
			t.Fatal("expected false for a stale socket file with no listener")
		}
	})

	t.Run("live listener", func(t *testing.T) {
		sock := filepath.Join("/tmp", fmt.Sprintf("vixd-guard-live-%d.sock", time.Now().UnixNano()))
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		t.Cleanup(func() { ln.Close(); os.Remove(sock) })
		if !daemonAlreadyListening(sock) {
			t.Fatal("expected true while a listener is accepting connections")
		}
	})
}

// TestListenAndServeRefusesTakeover: a second server on a socket a live daemon
// already owns must fail fast without unlinking the socket, leaving the running
// daemon reachable.
func TestListenAndServeRefusesTakeover(t *testing.T) {
	srvA := newInstanceTestServer(t)
	done, cancel := serve(t, srvA)
	defer func() { cancel(); <-done }()

	// Second server bound to the SAME socket path.
	srvB := NewServer(srvA.sockPath, config.Credential{}, "test-thread-b", "test-model", &config.DaemonConfig{}, nil)
	ctx, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	err := srvB.ListenAndServe(ctx)
	if err == nil {
		t.Fatal("second ListenAndServe should have refused to start")
	}
	if !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("error %q should mention 'already listening'", err.Error())
	}

	// The original daemon's socket must be untouched and still accepting.
	if !dialable(srvA.sockPath) {
		t.Fatal("original daemon should still be reachable after refused takeover")
	}
}

// TestListenAndServeReclaimsStaleSocket: a socket file left by a crashed daemon
// (no live listener) is removed and the new server starts normally.
func TestListenAndServeReclaimsStaleSocket(t *testing.T) {
	srv := newInstanceTestServer(t)
	if err := os.WriteFile(srv.sockPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	// serve() fails the test if the server never starts accepting, which it
	// only can if the stale socket file was reclaimed.
	done, cancel := serve(t, srv)
	defer func() { cancel(); <-done }()

	if !dialable(srv.sockPath) {
		t.Fatal("daemon should be reachable after reclaiming a stale socket")
	}
}
