//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

// applyProcessGroup puts cmd's child in its own process group so the whole MCP
// server tree can be torn down together on shutdown. (Unix: Setpgid.)
func applyProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree sends SIGKILL to the process group containing pid.
func killProcessTree(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
