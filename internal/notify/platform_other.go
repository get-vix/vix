//go:build !darwin && !linux

package notify

import "os/exec"

// playerCmd has no known player on unsupported platforms; playback is a no-op.
func playerCmd(path string) *exec.Cmd { return nil }

// systemSounds returns nothing on unsupported platforms; only bundled sounds
// are offered.
func systemSounds() []Sound { return nil }
