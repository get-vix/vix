package daemon

import (
	"context"
	"os/exec"
)

// Sandbox abstracts how a bash command is wrapped in the current platform's
// enforcement layer before execution.
//
// Today the daemon picks one of Seatbelt (macOS), Landlock+bwrap (Linux), or
// none, via a runtime switch on sandboxMode. This interface formalizes that
// seam so a later wave can add a Windows sandbox backend (AppContainer / Job
// Object) without touching call sites.
//
// Wave 0 moves the existing Unix implementations behind the interface and adds
// no Windows backend; behaviour on every platform is unchanged. The signature
// takes the context and the raw command string because each backend constructs
// a different exec.Cmd (sandbox-exec / bwrap / landlock-exec / bash) rather than
// mutating a pre-built one — this mirrors the existing sandboxedBashCmd shape
// exactly.
type Sandbox interface {
	// Wrap builds an exec.Cmd that runs command inside this sandbox. cwd is the
	// working directory; extraDirs are additional read-write paths the sandbox
	// must expose. The returned cmd is ready to Start.
	Wrap(ctx context.Context, command, cwd string, extraDirs []string) (*exec.Cmd, error)
}

// activeSandbox returns the Sandbox selected for the current platform (chosen
// once by detectSandbox). It never returns nil: the worst case is sandboxNone,
// which runs bash unsandboxed.
func activeSandbox() Sandbox {
	switch detectSandbox() {
	case sandboxSeatbelt:
		return seatbeltSandbox{}
	case sandboxLandlock:
		return landlockSandbox{}
	case sandboxBwrap:
		return bwrapSandbox{}
	default:
		return noneSandbox{}
	}
}
