//go:build windows

package daemon

import "os/exec"

func init() {
	processTree = windowsProcessTree{}
}

// windowsProcessTree is a Wave 0 compile stub. The real Windows backend (a Job
// Object that groups the child and its descendants, killed as a unit) arrives
// in a later wave. The daemon does not yet run on Windows, so this no-op keeps
// the package compiling without adding a backend or changing Unix behaviour.
type windowsProcessTree struct{}

func (windowsProcessTree) KillTree(pid int) error { return nil }

// applyProcessGroup is a no-op on Windows: syscall.SysProcAttr has no Setpgid
// field, and Job-Object assignment is a later wave.
func applyProcessGroup(cmd *exec.Cmd) {}

// processGroupID has no process-group analog on Windows; return pid unchanged.
func processGroupID(pid int) int { return pid }
