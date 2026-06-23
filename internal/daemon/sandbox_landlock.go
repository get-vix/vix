//go:build linux

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// resolveBashPath returns the absolute path to the bash binary, resolved
// once at first use and cached for the process lifetime.
//
// The historical hardcode of /usr/bin/bash broke on distros without the
// /usr-merge (Debian 11 / older Alpine bases ship bash only at
// /bin/bash), where every sandboxed command failed with
// `landlock-exec: exec /usr/bin/bash: no such file or directory`.
// exec.LookPath honours $PATH, so this works on any layout that puts
// bash anywhere reachable from the daemon's environment.
//
// If LookPath fails outright (no bash on $PATH at all), fall back to the
// historical /usr/bin/bash so the resulting error message still points
// at a concrete path rather than an empty string.
var resolveBashPath = sync.OnceValue(func() string {
	if p, err := exec.LookPath("bash"); err == nil {
		log.Printf("[sandbox] resolved bash → %s", p)
		return p
	} else {
		log.Printf("[sandbox] exec.LookPath(\"bash\") failed (%v) — falling back to /usr/bin/bash", err)
		return "/usr/bin/bash"
	}
})

// landlockRules is the JSON wire format passed to the helper subcommand
// via VIX_LANDLOCK_RULES. RO paths get read+execute; RW paths get the
// full write set on top.
type landlockRules struct {
	RO []string `json:"ro"`
	RW []string `json:"rw"`
}

// envLandlockRules is the environment variable used to pass the JSON
// ruleset from the parent vixd to the `vixd landlock-exec` helper. We use
// an env var (not argv) so the (potentially long) path list doesn't show
// up in `ps` output and isn't bounded by ARG_MAX.
const envLandlockRules = "VIX_LANDLOCK_RULES"

// Access masks. The kernel rejects rules whose allowed_access is not a
// subset of the ruleset's handled_access_fs, so we keep a single source
// of truth here and use it both for the ruleset attr and the per-path
// rules.
//
// Excluded on purpose:
//   - LANDLOCK_ACCESS_FS_IOCTL_DEV (v5): including it would force us to
//     allow it on every /dev path, which is busywork. ioctls stay
//     unrestricted.
//
// Conditionally handled (see applyLandlockRuleset, which gates on the
// runtime ABI):
//   - LANDLOCK_ACCESS_FS_TRUNCATE (v3): without it, open(O_TRUNC) on a
//     writable file fails with EACCES.
//   - LANDLOCK_ACCESS_FS_REFER (v2): without it, the kernel returns
//     EXDEV ("Invalid cross-device link") for any cross-directory
//     rename or hardlink — even within the same mount. apt's HTTP
//     method, editors' atomic-save, git, and most package managers
//     all do cross-dir renames (e.g. archives/partial/X → archives/X),
//     so a Landlocked process without REFER sees them all break.
const (
	fsAccessRead = unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_EXECUTE

	fsAccessWriteOnly = unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM
)

// landlockSupported reports whether the running kernel supports Landlock
// AND it's currently enabled in the LSM stack. Implemented as a probe
// call: landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION)
// returns the ABI version on success and ENOSYS / EOPNOTSUPP otherwise.
func landlockSupported() bool {
	abi, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0,
		0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION),
	)
	if errno != 0 {
		return false
	}
	return int(abi) >= 1
}

// landlockBashCmd builds an exec.Cmd that re-execs the running vixd
// binary with the `landlock-exec` subcommand. The helper child applies
// PR_SET_NO_NEW_PRIVS + the Landlock ruleset to itself, then execve's
// bash. By the time the user's command runs, the kernel is enforcing
// the allow-list — and because the restriction is irrevocable for that
// process, nothing the bash subtree does can lift it.
func landlockBashCmd(ctx context.Context, command, cwd string, extraDirs []string) *exec.Cmd {
	self, err := os.Executable()
	if err != nil {
		// os.Executable should never fail on Linux; if it does, fall
		// through to no-sandbox bash so the trial doesn't dead-stop.
		log.Printf("[sandbox] os.Executable failed (%v) — running bash unsandboxed", err)
		return unsandboxedBashCmd(ctx, command, cwd)
	}

	rules := buildLandlockRules(cwd, extraDirs)
	rulesJSON, err := json.Marshal(rules)
	if err != nil {
		log.Printf("[sandbox] landlock rules marshal failed (%v) — running bash unsandboxed", err)
		return unsandboxedBashCmd(ctx, command, cwd)
	}

	// argv[0]=self, argv[1]="landlock-exec", argv[2..]=the bash invocation.
	// We pass through the full bash invocation (binary + flags + command)
	// as-is so the helper just needs to syscall.Exec(argv[0], argv).
	cmd := exec.CommandContext(ctx, self, "landlock-exec", resolveBashPath(), "-c", command)
	cmd.Dir = cwd
	cmd.Env = append(sanitizedBashEnv(), envLandlockRules+"="+string(rulesJSON))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		logProcessChildren(pid)
		log.Printf("[sandbox] sending SIGKILL to process group pgid=%d (mode=landlock)", pid)
		err := syscall.Kill(-pid, syscall.SIGKILL)
		if err != nil {
			log.Printf("[sandbox] SIGKILL failed for pgid=%d: %v", pid, err)
		}
		return err
	}
	return cmd
}

// buildLandlockRules returns the path lists the helper should grant.
// The default RO/RW set comes from platformPolicies["linux"], so the
// allow surface stays in sync with what the dispatcher's outside-paths
// check considers acceptable. cwd is always RW; HOME is RW (uv/apt
// caches live there); extraDirs from the session are RW too.
//
// Anything not in either list is denied by absence — any path the
// operator hasn't explicitly opened up stays inaccessible.
func buildLandlockRules(cwd string, extraDirs []string) landlockRules {
	policy := platformPolicies["linux"]

	rw := append([]string{}, policy.ReadWrite...)
	if cwd != "" {
		rw = append(rw, cwd)
	}
	if home := userHomeDir(); home != "" {
		rw = append(rw, home)
	}
	for _, d := range extraDirs {
		// "/" is the --disable-automatic-directory-access escape hatch:
		// the operator has explicitly waived sandboxing. Honour it by
		// granting the whole tree RW.
		if d == "/" {
			return landlockRules{RW: []string{"/"}}
		}
		if d != "" {
			rw = append(rw, d)
		}
	}

	return landlockRules{
		RO: append([]string{}, policy.ReadOnly...),
		RW: rw,
	}
}

// LandlockExecMain is the entry point for the hidden `vixd landlock-exec`
// subcommand. cmd/vixd/main.go dispatches to it on argv[1]=="landlock-exec"
// before any normal startup happens — the helper is fork-light: no
// daemon, no telemetry, no socket. It applies Landlock to itself, then
// execve's the rest of argv.
//
// Failures are fatal and printed to stderr (which the parent's
// exec.Cmd captures and surfaces back to the LLM via the bash tool).
func LandlockExecMain(argv []string) {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "landlock-exec: no command")
		os.Exit(2)
	}

	rulesJSON := os.Getenv(envLandlockRules)
	if rulesJSON == "" {
		fmt.Fprintln(os.Stderr, "landlock-exec: missing "+envLandlockRules)
		os.Exit(2)
	}
	var rules landlockRules
	if err := json.Unmarshal([]byte(rulesJSON), &rules); err != nil {
		fmt.Fprintf(os.Stderr, "landlock-exec: bad %s: %v\n", envLandlockRules, err)
		os.Exit(2)
	}

	// PR_SET_NO_NEW_PRIVS is mandatory before landlock_restrict_self —
	// the kernel rejects the call otherwise. It also forbids running
	// setuid binaries from the wrapped command, which we accept (no
	// task in our surface needs setuid; we run as root).
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		fmt.Fprintf(os.Stderr, "landlock-exec: prctl(PR_SET_NO_NEW_PRIVS): %v\n", err)
		os.Exit(2)
	}

	if err := applyLandlockRuleset(rules); err != nil {
		fmt.Fprintf(os.Stderr, "landlock-exec: %v\n", err)
		os.Exit(2)
	}

	// argv[0] is the binary to exec; we pass argv unchanged so bash sees
	// itself as $0. Using syscall.Exec (not os/exec) so this process
	// becomes bash directly instead of forking yet again.
	if err := syscall.Exec(argv[0], argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "landlock-exec: exec %s: %v\n", argv[0], err)
		os.Exit(2)
	}
}

// applyLandlockRuleset creates the ruleset, adds path-beneath rules for
// each RO/RW entry, and applies it to the current process. Returns nil
// on success.
func applyLandlockRuleset(rules landlockRules) error {
	abi, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0,
		0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION),
	)
	if errno != 0 {
		return fmt.Errorf("landlock probe: %v", errno)
	}
	if int(abi) < 1 {
		return fmt.Errorf("landlock unsupported (abi=%d)", int(abi))
	}

	// Compose handled_access_fs based on what this ABI supports. Per-rule
	// allowed_access must be a subset of this — anything we include here
	// without granting on the RW rule below would deny that op even on
	// writable paths.
	handled := uint64(fsAccessRead | fsAccessWriteOnly)
	rwAccess := uint64(fsAccessRead | fsAccessWriteOnly)
	if int(abi) >= 2 {
		// REFER was added in ABI 2 (kernel 5.19). It governs cross-
		// directory rename/hardlink. Without it in handled_access_fs,
		// the kernel returns EXDEV ("Invalid cross-device link") on
		// every cross-dir rename inside the sandbox — even when source
		// and destination are on the same filesystem. apt's HTTP
		// method (archives/partial/X → archives/X), editors that
		// atomic-save via /tmp, git's index updates, and most package
		// managers all break this way. Granting REFER on every RW rule
		// is what makes those work transparently; we deliberately
		// don't grant it on RO rules since a read-only path can't be
		// the destination of a rename anyway. Observed on the
		// gcode-to-text trial in 2026-04-25__17-58-56, where the
		// agent's `apt-get install tesseract-ocr` failed with the
		// EXDEV error message even after pointing apt at a fresh
		// /tmp/aptcache subtree.
		handled |= unix.LANDLOCK_ACCESS_FS_REFER
		rwAccess |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if int(abi) >= 3 {
		// TRUNCATE was added in ABI 3 (kernel 6.2). It governs both
		// open(O_TRUNC) on an existing file and truncate(2). Without
		// granting it on RW rules, Python's `open(p, "w")` and similar
		// fail with EACCES on writable dirs — observed in the
		// 2026-04-25__14-42-56 run on /tmp and /app.
		handled |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
		rwAccess |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}

	// LandlockRulesetAttr is 24 bytes in x/sys (Access_fs + Access_net +
	// Scoped). We only use Access_fs, and older kernels reject sizes
	// they don't recognise — so pass size 8 (the v1 size) explicitly.
	// Setting Access_net=0 / Scoped=0 doesn't enforce anything in those
	// fields anyway.
	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	const v1Size = 8
	fd, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)),
		v1Size,
		0,
	)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %v", errno)
	}
	rulesetFd := int(fd)
	defer syscall.Close(rulesetFd)

	addPath := func(path string, access uint64) {
		// Resolve symlinks: Landlock matches on the resolved inode, and
		// EvalSymlinks gives us the same view that filepath checks at
		// the dispatcher use. Missing paths are silently skipped so a
		// distro without /lib32 (say) doesn't blow the whole call up.
		if path == "" {
			return
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return
		}
		f, err := os.Open(real)
		if err != nil {
			return
		}
		defer f.Close()
		ruleAttr := unix.LandlockPathBeneathAttr{
			Allowed_access: access,
			Parent_fd:      int32(f.Fd()),
		}
		_, _, errno := unix.Syscall6(
			unix.SYS_LANDLOCK_ADD_RULE,
			uintptr(rulesetFd),
			uintptr(unix.LANDLOCK_RULE_PATH_BENEATH),
			uintptr(unsafe.Pointer(&ruleAttr)),
			0, 0, 0,
		)
		if errno != 0 {
			// A failed add_rule is logged but doesn't abort the whole
			// ruleset — partial coverage is better than no sandbox.
			fmt.Fprintf(os.Stderr, "landlock-exec: add_rule(%q): %v\n", real, errno)
		}
	}

	for _, p := range rules.RO {
		addPath(p, uint64(fsAccessRead))
	}
	for _, p := range rules.RW {
		addPath(p, rwAccess)
	}

	if _, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_RESTRICT_SELF,
		uintptr(rulesetFd),
		0, 0,
	); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %v", errno)
	}
	return nil
}

// unsandboxedBashCmd is the same bash invocation the no-sandbox branch
// of sandboxedBashCmd builds. Pulled out so the Landlock branch can
// gracefully fall back when something pre-restriction goes wrong (e.g.
// os.Executable, JSON marshal). Identical wait/cancel shape so the
// downstream signal-handling treats it the same.
func unsandboxedBashCmd(ctx context.Context, command, cwd string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = cwd
	cmd.Env = sanitizedBashEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		logProcessChildren(pid)
		log.Printf("[sandbox] sending SIGKILL to process group pgid=%d (mode=none)", pid)
		err := syscall.Kill(-pid, syscall.SIGKILL)
		if err != nil {
			log.Printf("[sandbox] SIGKILL failed for pgid=%d: %v", pid, err)
		}
		return err
	}
	return cmd
}
