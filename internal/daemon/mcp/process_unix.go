//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

// configureStdioCommand gives the MCP server its own process group so shutdown
// does not leave descendants running.
func configureStdioCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killStdioProcess(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
