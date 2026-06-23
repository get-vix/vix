//go:build !windows

package daemon

import (
	"os/exec"
	"syscall"
)

func init() {
	processTree = unixProcessTree{}
}

// unixProcessTree tears down a process tree via POSIX process groups: the child
// was placed in its own group by applyProcessGroup (Setpgid), so signalling the
// group id (the child's pid, negated) reaches every descendant.
type unixProcessTree struct{}

func (unixProcessTree) KillTree(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// applyProcessGroup puts cmd's child in its own process group so KillTree can
// target it (and its descendants) as a unit. Mirrors the prior inline
// `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` at every call site.
func applyProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// processGroupID returns the process-group id of pid, falling back to pid when
// it cannot be determined (matching the prior inline `syscall.Getpgid` usage).
func processGroupID(pid int) int {
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid <= 0 {
		return pid
	}
	return pgid
}
