package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// sandboxMode describes how bash commands are executed.
//
// On Linux we prefer sandboxLandlock when the kernel supports it (Linux
// 5.13+, ABI ≥ 1) because Landlock works inside a default-seccomp Docker
// container (no unshare needed) and has no install footprint. sandboxBwrap
// is kept as a fallback for hosts where Landlock isn't compiled in or the
// LSM stack disables it. sandboxSeatbelt covers macOS. sandboxNone is the
// last resort — bash runs with no enforcement layer.
type sandboxMode int

const (
	sandboxNone     sandboxMode = iota // no sandbox available
	sandboxSeatbelt                    // macOS sandbox-exec
	sandboxLandlock                    // Linux Landlock LSM (preferred on Linux)
	sandboxBwrap                       // Linux bubblewrap (fallback)
)

var (
	detectedSandbox   sandboxMode
	detectSandboxOnce sync.Once
)

// detectSandbox checks which sandbox mechanism is available on the current platform.
func detectSandbox() sandboxMode {
	detectSandboxOnce.Do(func() {
		switch runtime.GOOS {
		case "darwin":
			if _, err := exec.LookPath("sandbox-exec"); err == nil {
				// Probe: verify sandbox-exec can actually apply a Seatbelt profile.
				// On some macOS environments (Docker, VMs, restricted SIP configs)
				// the binary exists but sandbox_apply returns EPERM (exit 71).
				pCtx, pCancel := context.WithTimeout(context.Background(), 3*time.Second)
				probe := exec.CommandContext(pCtx, "sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true")
				probeErr := probe.Run()
				pCancel()
				if probeErr == nil {
					detectedSandbox = sandboxSeatbelt
					LogInfo("[sandbox] using macOS sandbox-exec (Seatbelt)")
				} else {
					LogWarn("[sandbox] sandbox-exec present but sandbox_apply denied — running unsandboxed")
				}
			}
		case "linux":
			// Prefer Landlock — works inside containers and needs no
			// install. Probe via landlock_create_ruleset(NULL, 0,
			// VERSION) which returns the ABI version on success.
			if landlockSupported() {
				detectedSandbox = sandboxLandlock
				LogInfo("[sandbox] using Linux Landlock LSM")
			} else if _, err := exec.LookPath("bwrap"); err == nil {
				detectedSandbox = sandboxBwrap
				LogInfo("[sandbox] using bubblewrap (bwrap) — Landlock unavailable")
			}
		}
		if detectedSandbox == sandboxNone {
			LogWarn("[sandbox] no sandbox available — bash commands will run unsandboxed")
		}
	})
	return detectedSandbox
}

// seatbeltProfile generates a macOS Seatbelt profile from the darwin entry
// of platformPolicies. The profile allows read/write within cwd and $HOME,
// the platform's read-only system directories (binaries, libs, etc.), the
// platform's read-write system directories (/dev, /var, /tmp, ...), and
// each entry in extraDirs. Network is allowed unconditionally.
func seatbeltProfile(cwd string, extraDirs []string) string {
	realCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		realCwd = cwd
	}

	home := os.Getenv("HOME")
	if home == "" {
		home = "/Users"
	}

	policy := platformPolicies["darwin"]

	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n\n")

	b.WriteString(";; Allow process execution and standard operations\n")
	b.WriteString("(allow process-exec*)\n")
	b.WriteString("(allow process-fork)\n")
	b.WriteString("(allow signal)\n")
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString("(allow mach-lookup)\n")
	// mach-register lets child processes register mach services via the
	// bootstrap server. Needed by multi-process tools such as headless Chrome
	// (its crashpad handler does bootstrap_check_in); without it they fail to
	// start under the sandbox.
	b.WriteString("(allow mach-register)\n")
	b.WriteString("(allow ipc-posix-shm*)\n")
	// pseudo-tty allows PTY allocation (grantpt/TIOCPTYGRANT, forkpty).
	// Without it, tools that need a controlling terminal — tmux, script,
	// expect, some REPLs — fail with "fork failed: Operation not permitted"
	// even though process-fork is allowed, because the denial is on the pty
	// grant, not on fork itself.
	b.WriteString("(allow pseudo-tty)\n")
	// pseudo-tty is necessary but not sufficient: grantpt(3) issues an
	// ioctl(TIOCPTYGRANT) on /dev/ptmx, and that ioctl is a distinct
	// Seatbelt operation (file-ioctl) which file-write* does NOT imply. On
	// macOS 26+ the grant fails with EPERM without it, so tmux/script/expect
	// still can't allocate a controlling terminal. Mirror Apple's own
	// profiles (e.g. /System/Library/Sandbox/Profiles/application.sb) by
	// allowing file-ioctl on the pty master and the cloned /dev/ttys* slaves.
	b.WriteString("(allow file-ioctl (literal \"/dev/ptmx\"))\n")
	b.WriteString("(allow file-ioctl (regex #\"^/dev/ttys[0-9]*\"))\n\n")

	b.WriteString(";; cwd: read-write\n")
	fmt.Fprintf(&b, "(allow file-read* (subpath %q))\n", realCwd)
	fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n\n", realCwd)

	b.WriteString(";; System paths from platformPolicies[darwin].ReadOnly\n")
	for _, p := range policy.ReadOnly {
		fmt.Fprintf(&b, "(allow file-read* (subpath %q))\n", p)
	}
	b.WriteString("\n;; System paths from platformPolicies[darwin].ReadWrite\n")
	for _, p := range policy.ReadWrite {
		fmt.Fprintf(&b, "(allow file-read* (subpath %q))\n", p)
		fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n", p)
	}
	// Permit `ls /` etc. without exposing other root-children that the
	// policy doesn't list.
	b.WriteString("(allow file-read* (literal \"/\"))\n\n")

	b.WriteString(";; $HOME: read-write (tool configs, caches)\n")
	// Allow stat/traverse on each parent directory between / and home so
	// processes (e.g. git resolving relative worktree paths) can reach the
	// allowed subpath without being blocked on an intermediate component.
	for parent := filepath.Dir(home); parent != "/" && parent != "."; parent = filepath.Dir(parent) {
		fmt.Fprintf(&b, "(allow file-read* (literal %q))\n", parent)
	}
	fmt.Fprintf(&b, "(allow file-read* (subpath %q))\n", home)
	fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n\n", home)

	b.WriteString(";; Network (package managers, git, curl)\n")
	b.WriteString("(allow network*)\n\n")

	for _, dir := range extraDirs {
		realDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			realDir = dir
		}
		b.WriteString(";; Allowed extra directory\n")
		fmt.Fprintf(&b, "(allow file-read* (subpath %q))\n", realDir)
		fmt.Fprintf(&b, "(allow file-write* (subpath %q))\n", realDir)
	}

	return b.String()
}

// bashEnvBlocklist names environment variables that must not leak from the
// daemon into the bash commands it spawns (workflow/job steps, the bash tool).
// GODEBUG in particular is a vixd-only debug knob, but it makes every Go child
// binary a step invokes (e.g. the GitHub CLI) emit runtime trace lines such as
// "SCHED 0ms: gomaxprocs=..." on stderr. Those lines then contaminate the
// captured step output and corrupt downstream parsing (e.g. a URL list).
var bashEnvBlocklist = map[string]bool{
	"GODEBUG": true,
}

// sanitizedBashEnv returns the current process environment with daemon-only
// debug variables (see bashEnvBlocklist) removed, so the external tools a bash
// step runs start from a clean slate.
func sanitizedBashEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if bashEnvBlocklist[key] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// sandboxedBashCmd builds an exec.Cmd that runs a bash command inside the
// appropriate sandbox for the current platform. Falls back to a plain bash
// invocation if no sandbox is available.
func sandboxedBashCmd(ctx context.Context, command, cwd string, extraDirs []string) *exec.Cmd {
	mode := detectSandbox()

	switch mode {
	case sandboxLandlock:
		return landlockBashCmd(ctx, command, cwd, extraDirs)

	case sandboxSeatbelt:
		profile := seatbeltProfile(cwd, extraDirs)
		cmd := exec.CommandContext(ctx, "sandbox-exec", "-p", profile, "bash", "-c", command)
		cmd.Dir = cwd
		cmd.Env = sanitizedBashEnv()
		configureProcessGroup(cmd)
		cmd.WaitDelay = 2 * time.Second
		cmd.Cancel = func() error {
			pid := cmd.Process.Pid
			logProcessChildren(pid)
			log.Printf("[sandbox] sending SIGKILL to process group pgid=%d (mode=seatbelt)", pid)
			err := killProcessGroup(pid)
			if err != nil {
				log.Printf("[sandbox] SIGKILL failed for pgid=%d: %v", pid, err)
			}
			return err
		}
		return cmd

	case sandboxBwrap:
		realCwd, err := filepath.EvalSymlinks(cwd)
		if err != nil {
			realCwd = cwd
		}
		home := os.Getenv("HOME")
		if home == "" {
			home = "/home"
		}

		args := buildBwrapArgs(cwd, realCwd, home, extraDirs, command)

		cmd := exec.CommandContext(ctx, "bwrap", args...)
		cmd.Dir = cwd
		configureProcessGroup(cmd)
		cmd.WaitDelay = 2 * time.Second
		cmd.Cancel = func() error {
			pid := cmd.Process.Pid
			logProcessChildren(pid)
			log.Printf("[sandbox] sending SIGKILL to process group pgid=%d (mode=bwrap)", pid)
			err := killProcessGroup(pid)
			if err != nil {
				log.Printf("[sandbox] SIGKILL failed for pgid=%d: %v", pid, err)
			}
			return err
		}
		// Propagate env through bwrap
		cmd.Env = sanitizedBashEnv()
		return cmd

	default:
		cmd := exec.CommandContext(ctx, "bash", "-c", command)
		cmd.Dir = cwd
		cmd.Env = sanitizedBashEnv()
		configureProcessGroup(cmd)
		cmd.WaitDelay = 2 * time.Second
		cmd.Cancel = func() error {
			pid := cmd.Process.Pid
			logProcessChildren(pid)
			log.Printf("[sandbox] sending SIGKILL to process group pgid=%d (mode=none)", pid)
			err := killProcessGroup(pid)
			if err != nil {
				log.Printf("[sandbox] SIGKILL failed for pgid=%d: %v", pid, err)
			}
			return err
		}
		return cmd
	}
}

// logProcessChildren logs the immediate children of a process (best-effort).
// Called right before SIGKILL so we can see what the bash command spawned.
func logProcessChildren(pid int) {
	tctx, tcancel := context.WithTimeout(context.Background(), time.Second)
	defer tcancel()
	out, err := exec.CommandContext(tctx, "pgrep", "-lP", fmt.Sprintf("%d", pid)).Output()
	if err == nil && len(out) > 0 {
		log.Printf("[sandbox] children of pid %d:\n%s", pid, strings.TrimSpace(string(out)))
	}
}

// SandboxAvailable reports whether a sandbox mechanism was detected.
func SandboxAvailable() bool {
	return detectSandbox() != sandboxNone
}

// SandboxName returns a human-readable name for the active sandbox.
func SandboxName() string {
	switch detectSandbox() {
	case sandboxSeatbelt:
		return "macOS sandbox-exec"
	case sandboxLandlock:
		return "Linux Landlock"
	case sandboxBwrap:
		return "bubblewrap"
	default:
		return "none"
	}
}

// AllowedWritePaths returns the paths the sandbox permits writing to,
// given the working directory. Useful for user-facing descriptions.
func AllowedWritePaths(cwd string) []string {
	paths := []string{cwd, "/tmp"}
	if home := os.Getenv("HOME"); home != "" {
		paths = append(paths, home)
	}
	return paths
}

// buildBwrapArgs returns the argv passed to bwrap. Pulled out of
// sandboxedBashCmd so the assembly logic is unit-testable on macOS too —
// the security posture should not regress without a failing test, and the
// real bwrap binary only exists on Linux.
//
// Layout:
//   - When extraDirs contains "/" (--disable-automatic-directory-access not set),
//     mount the entire filesystem rw and skip the selective binds.
//   - Otherwise, expose only the system paths real tools need (read-only)
//     plus a managed /proc.
//   - Always rw-bind cwd, /tmp, /dev, /var, $HOME, and any extra dirs.
//   - If cwd is a symlink, also bind the symlink path so either form
//     resolves the same files inside the sandbox.
func buildBwrapArgs(cwd, realCwd, home string, extraDirs []string, command string) []string {
	fullRW := false
	for _, d := range extraDirs {
		if d == "/" {
			fullRW = true
			break
		}
	}

	var args []string
	if fullRW {
		args = []string{"--bind", "/", "/"}
	} else {
		// Read-only and read-write system paths come from
		// platformPolicies[linux]. --ro-bind-try silently skips a
		// missing source so distros without /lib32 or /nix still launch.
		policy := platformPolicies["linux"]
		for _, p := range policy.ReadOnly {
			args = append(args, "--ro-bind-try", p, p)
		}
		// bwrap mounts an isolated /proc inside the sandbox; this is a
		// special form, not a host bind, so it isn't part of the policy
		// table.
		args = append(args, "--proc", "/proc")
	}

	// Read-write: cwd and $HOME first, then platform rw paths, then the
	// caller's extras. (When fullRW, the rw-system loop below is harmless
	// extra args — bwrap accepts overlapping binds where the later one
	// wins.)
	args = append(args, "--bind", realCwd, realCwd, "--bind", home, home)
	if !fullRW {
		for _, p := range platformPolicies["linux"].ReadWrite {
			args = append(args, "--bind", p, p)
		}
	}

	// Extra allowed directories (skip "/" — already handled).
	for _, dir := range extraDirs {
		if dir == "/" {
			continue
		}
		realDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			realDir = dir
		}
		args = append(args, "--bind", realDir, realDir)
	}

	if realCwd != cwd {
		args = append(args, "--bind", cwd, cwd)
	}

	args = append(args,
		"--chdir", realCwd,
		// Note: network namespace is NOT unshared — outbound HTTP still
		// works, which the agent needs for git/npm/curl.
		"bash", "-c", command,
	)
	return args
}

// buildPathEnv returns a restricted PATH containing only standard system
// directories and common dev tool locations. This is an extra layer on top
// of the OS sandbox — it limits what the shell can find via PATH.
func buildRestrictedPath() string {
	candidates := []string{
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
		"/opt/homebrew/bin", // macOS ARM homebrew
		"/opt/homebrew/sbin",
		"/usr/local/go/bin", // Go toolchain
	}
	// Preserve any GOPATH/bin, cargo, etc. from the user's PATH
	existing := strings.Split(os.Getenv("PATH"), ":")
	for _, p := range existing {
		if strings.Contains(p, "go/bin") ||
			strings.Contains(p, ".cargo/bin") ||
			strings.Contains(p, ".local/bin") ||
			strings.Contains(p, "node_modules/.bin") ||
			strings.Contains(p, ".nvm") ||
			strings.Contains(p, ".pyenv") ||
			strings.Contains(p, ".rbenv") {
			candidates = append(candidates, p)
		}
	}

	// Deduplicate
	seen := make(map[string]bool)
	var result []string
	for _, p := range candidates {
		if !seen[p] {
			seen[p] = true
			result = append(result, p)
		}
	}
	return strings.Join(result, ":")
}
