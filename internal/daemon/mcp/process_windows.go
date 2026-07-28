//go:build windows

package mcp

import "os/exec"

// Windows does not support Unix process groups. Process.Kill still ensures the
// MCP server itself is stopped; CommandContext handles cancellation as well.
func configureStdioCommand(cmd *exec.Cmd) {}

func killStdioProcess(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}
