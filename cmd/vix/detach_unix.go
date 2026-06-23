//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detachDaemon configures cmd so the spawned daemon runs detached from the
// client's session/process group, surviving terminal signals (SIGHUP/SIGINT).
// On Unix this starts a new session (setsid).
func detachDaemon(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
