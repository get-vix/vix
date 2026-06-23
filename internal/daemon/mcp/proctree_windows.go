//go:build windows

package mcp

import "os/exec"

// applyProcessGroup is a no-op on Windows (syscall.SysProcAttr has no Setpgid
// field; Job-Object grouping is a later wave). The MCP client is a compile-only
// stub on Windows until then.
func applyProcessGroup(cmd *exec.Cmd) {}

// killProcessTree is a no-op Windows compile stub (real backend is a later wave).
func killProcessTree(pid int) {}
