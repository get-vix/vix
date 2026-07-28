//go:build windows

package daemon

import (
	"os"
	"os/exec"
)

// Windows has no Unix-style process groups. Killing the process is the closest
// supported equivalent for the command lifecycle used by the daemon.
func configureProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func processGroupID(pid int) (int, error) {
	return pid, nil
}
