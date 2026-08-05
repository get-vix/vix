package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Install method identifiers carried in EventUpdateAvailable.Method and used to
// pick the upgrade command.
const (
	MethodBrew    = "brew"    // installed via the Homebrew tap; upgrade with `brew upgrade`
	MethodScript  = "script"  // installed via getvix.dev/install.sh into a writable/sudo path
	MethodUnknown = "unknown" // origin couldn't be determined; offer manual instructions only
)

// installScriptCmd is the one-liner the script-install path runs to upgrade. It
// reuses the official installer so checksum/GPG verification is inherited rather
// than re-implemented here.
const installScriptCmd = "curl -fsSL https://getvix.dev/install.sh | bash -s --"

// DetectMethod infers how the running binary was installed by inspecting the
// path of the current executable. A path under a Homebrew prefix means brew;
// anything else is assumed to be a script install. It resolves symlinks because
// Homebrew links binaries from the Cellar into bin directories on PATH.
func DetectMethod() string {
	exe, err := os.Executable()
	if err != nil {
		return MethodUnknown
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if isBrewPath(exe) {
		return MethodBrew
	}
	return MethodScript
}

// isBrewPath reports whether p sits inside a Homebrew installation tree.
func isBrewPath(p string) bool {
	p = filepath.ToSlash(p)
	for _, marker := range []string{"/Cellar/", "/opt/homebrew/", "/home/linuxbrew/", "/.linuxbrew/"} {
		if strings.Contains(p, marker) {
			return true
		}
	}
	return false
}

// InstallCommand returns the command that upgrades vix to version for the given
// install method. The command is intended to run in the foreground terminal of
// the TUI (via bubbletea's ExecProcess) so that interactive prompts — sudo for
// the script path — can reach the user's TTY. Returns nil for MethodUnknown.
func InstallCommand(method, version string) *exec.Cmd {
	switch method {
	case MethodBrew:
		return exec.Command("brew", "upgrade", "vix")
	case MethodScript:
		line := installScriptCmd
		if version != "" {
			line += " " + version
		}
		return exec.Command("bash", "-c", line)
	default:
		return nil
	}
}

// ManualInstruction returns the command a user should run by hand when an
// automatic in-app update isn't offered (e.g. MethodUnknown).
func ManualInstruction(method string) string {
	if method == MethodBrew {
		return "brew upgrade vix"
	}
	return "curl -fsSL https://getvix.dev/install.sh | bash"
}

// SelfExecPath returns the path to re-exec after an update completes. It prefers
// the resolved current executable so a relaunch picks up the freshly-swapped
// binary on disk.
func SelfExecPath() string {
	exe, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	if runtime.GOOS != "windows" {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return resolved
		}
	}
	return exe
}
