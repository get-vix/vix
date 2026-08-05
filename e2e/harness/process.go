package harness

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// startDaemon spawns vixd against the isolated env, waits for its socket, and
// records the selected sandbox mode (failing loudly on a silent "none").
func (h *Harness) startDaemon(cfg *config) {
	t := h.t
	bin, err := vixdBinary()
	if err != nil {
		t.Fatalf("e2e: %v", err)
	}
	// Append so a Restart preserves the previous run's log for post-mortem.
	logf, err := os.OpenFile(h.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("e2e: open vixd log: %v", err)
	}

	cmd := exec.Command(bin)
	cmd.Dir = h.workdir
	cmd.Env = h.daemonEnv(cfg, nil)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("e2e: start vixd: %v", err)
	}
	h.vixd = cmd

	// Wait for the socket to appear (or the daemon to die / deadline to pass).
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(h.socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("e2e: vixd socket never appeared at %s\n--- log ---\n%s", h.socket, h.Daemon.LogTail(80))
		}
		select {
		case <-h.ctx.Done():
			t.Fatalf("e2e: context expired waiting for vixd socket\n%s", h.Daemon.LogTail(80))
		case <-time.After(50 * time.Millisecond):
		}
	}

	mode := parseSandboxMode(readFileString(h.logFile))
	_ = mode // sandbox backend is detected lazily on first bash; SandboxMode()
	// reads the live log later. We no longer fail here (mode is "unknown" at
	// startup), so scenarios decide how strict to be.
}

// stopDaemon SIGTERMs vixd, waits for it to exit (bounded), and clears the stale
// socket so the next startDaemon waits for the fresh one.
func (h *Harness) stopDaemon() {
	if h.vixd == nil || h.vixd.Process == nil {
		return
	}
	_ = h.vixd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = h.vixd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = h.vixd.Process.Kill()
		<-done
	}
	h.vixd = nil
	_ = os.Remove(h.socket)
}

// Daemon exposes daemon introspection to scenarios.
type Daemon struct{ h *Harness }

// Restart tears down the TUI, stops vixd, then brings both back up on the same
// HOME + socket. A persisted ("open") thread for the workdir is auto-attached
// by the freshly launched TUI, which replays the prior conversation — so
// scenarios can prove state survives a daemon restart (issue #22). Drive at
// least one full turn (and let it complete) before calling Restart: the daemon
// persists per-turn, and SIGTERM does not flush an in-flight turn.
func (d *Daemon) Restart() {
	h := d.h
	if h.UI != nil {
		h.UI.close()
		h.UI = nil
	}
	h.stopDaemon()
	h.startDaemon(h.cfg)
	h.startTUI(h.cfg)
}

// StartConflicting launches a second vixd against the SAME socket + env while
// the primary daemon is still running, and returns its combined stdout+stderr
// and exit code once it terminates. The socket-ownership guard in
// ListenAndServe makes the second daemon refuse and exit non-zero without
// touching the live socket. Without that guard the second daemon would unlink
// the socket and block forever, so the wait is bounded and fails loudly if the
// process does not exit (a regression: it hijacked the socket).
func (d *Daemon) StartConflicting() (output string, exitCode int) {
	h := d.h
	bin, err := vixdBinary()
	if err != nil {
		h.t.Fatalf("e2e: %v", err)
	}
	cmd := exec.Command(bin)
	cmd.Dir = h.workdir
	cmd.Env = h.daemonEnv(h.cfg, nil)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("e2e: start conflicting vixd: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case werr := <-done:
		if werr == nil {
			return buf.String(), 0
		}
		if ee, ok := werr.(*exec.ExitError); ok {
			return buf.String(), ee.ExitCode()
		}
		h.t.Fatalf("e2e: conflicting vixd wait: %v", werr)
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		h.t.Fatalf("e2e: conflicting vixd did not exit within 15s — it may have hijacked the socket\n--- output ---\n%s", buf.String())
	}
	return buf.String(), -1
}

// SandboxMode returns the sandbox backend vixd selected
// ("landlock" | "bubblewrap" | "macOS sandbox-exec" | "none").
func (d *Daemon) SandboxMode() string {
	return parseSandboxMode(readFileString(d.h.logFile))
}

// LogTail returns the last n lines of the vixd log.
func (d *Daemon) LogTail(n int) string {
	lines := strings.Split(readFileString(d.h.logFile), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func parseSandboxMode(log string) string {
	switch {
	case strings.Contains(log, "using Linux Landlock"):
		return "landlock"
	case strings.Contains(log, "using bubblewrap"):
		return "bubblewrap"
	case strings.Contains(log, "using macOS sandbox-exec"):
		return "macOS sandbox-exec"
	case strings.Contains(log, "no sandbox available"), strings.Contains(log, "running unsandboxed"):
		return "none"
	default:
		return "unknown"
	}
}

func readFileString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
