//go:build windows

package main

import "os/exec"

// detachDaemon configures cmd so the spawned daemon runs detached from the
// client. On Windows, syscall.SysProcAttr has no Setsid field; the daemon
// lifecycle on Windows (Windows Service / Scheduled Task) is a later wave, so
// this is currently a no-op compile stub that preserves the call site. The
// daemon does not yet run on Windows.
func detachDaemon(cmd *exec.Cmd) {}
