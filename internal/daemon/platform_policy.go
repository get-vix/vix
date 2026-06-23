package daemon

import (
	"os"
	"path/filepath"
	"runtime"
)

// platformPolicy describes the system paths that are accessible by default
// on a given OS. ReadOnly paths are bound read-only inside the sandbox and
// flow silently through the dispatcher's path-prompt heuristic. ReadWrite
// paths are bound read-write and likewise no-prompt.
//
// This is the single source of truth for "what does the agent see by
// default". Both layers — the dispatcher's outside-paths check and the
// sandbox profile builders (Seatbelt + bwrap) — derive their behaviour
// from it.
type platformPolicy struct {
	ReadOnly  []string
	ReadWrite []string
}

// platformPolicies holds the per-OS defaults. Keys are runtime.GOOS values.
// Anything not listed here is invisible inside the sandbox and triggers a
// prompt outside it.
var platformPolicies = map[string]platformPolicy{
	"darwin": {
		ReadOnly: []string{
			"/usr",
			"/bin",
			"/sbin",
			"/Library",
			"/System",
			"/Applications",
			"/opt",
			"/etc",
			"/private/etc",
		},
		ReadWrite: []string{
			"/dev",
			"/var",
			"/private/var",
			"/tmp",
			"/private/tmp",
		},
	},
	"linux": {
		ReadOnly: []string{
			"/usr",
			"/lib",
			"/lib64",
			"/lib32",
			"/bin",
			"/sbin",
			"/etc",
			"/opt",
			"/nix",
			"/sys",
			// /proc: chromium's startup CPU probe reads /proc/cpuinfo and
			// /proc/self/status; runtimes (JVM, Go, Node) read /proc/meminfo
			// and /proc/stat for box-sizing. Landlock RO is safe here —
			// writes to /proc/sys/* are blocked, and the dangerous reads
			// (/proc/kcore, cross-pid /proc/<pid>/mem) are gated by
			// CAP_SYS_RAWIO / CAP_SYS_PTRACE which the container lacks.
			"/proc",
		},
		ReadWrite: []string{
			"/dev",
			"/var",
			"/tmp",
		},
	},
}

// currentPlatformPolicy returns the policy for the host OS, or an empty
// policy on platforms we haven't characterised. An empty policy is the
// safe default — no system paths auto-allowed — though in practice the
// agent only runs on darwin/linux.
func currentPlatformPolicy() platformPolicy {
	if p, ok := platformPolicies[runtime.GOOS]; ok {
		return p
	}
	return platformPolicy{}
}

// isSystemPath reports whether absPath sits inside one of the host's
// declared system directories (either ro or rw).
func isSystemPath(absPath string) bool {
	if absPath == "" {
		return false
	}
	p := currentPlatformPolicy()
	for _, prefix := range p.ReadOnly {
		if pathHasAncestor(absPath, prefix) {
			return true
		}
	}
	for _, prefix := range p.ReadWrite {
		if pathHasAncestor(absPath, prefix) {
			return true
		}
	}
	return false
}

// userHomeDir returns the user's home directory or "" if unknown. Callers
// must treat "" as "no auto-allow for HOME" — never as "auto-allow
// everywhere".
//
// $HOME is honoured when set (preserving the prior os.Getenv("HOME") behaviour
// exactly, including test overrides via t.Setenv("HOME", ...)). When $HOME is
// unset we fall back to os.UserHomeDir, which resolves %USERPROFILE% on Windows
// and the passwd entry on Unix — so the daemon is no longer blind to the home
// directory on Windows where HOME is typically empty.
func userHomeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}


// isAccessibleByDefault is the unified check that decides whether a path
// can be touched without bothering the user. It returns true when the
// path is:
//   - under cwd,
//   - under $HOME,
//   - inside a platform system directory, or
//   - inside one of the runtime allowedDirs.
//
// deny_list is checked separately, earlier in the dispatcher, so a path
// that satisfies this predicate may still be blocked by a deny entry —
// the deny list always wins.
func isAccessibleByDefault(absPath, cwd string, allowedDirs []string) bool {
	if absPath == "" {
		return false
	}
	absPath = filepath.Clean(absPath)
	if pathHasAncestor(absPath, cwd) {
		return true
	}
	if home := userHomeDir(); home != "" && pathHasAncestor(absPath, home) {
		return true
	}
	if isSystemPath(absPath) {
		return true
	}
	for _, dir := range allowedDirs {
		if pathHasAncestor(absPath, dir) {
			return true
		}
	}
	return false
}
