package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon"
	"github.com/get-vix/vix/internal/daemon/brain"
	"github.com/get-vix/vix/internal/headless"
	"github.com/get-vix/vix/internal/protocol"
	"github.com/get-vix/vix/internal/providers"
	"github.com/get-vix/vix/internal/telemetry"

	"github.com/get-vix/vix/internal/ui"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	versionFlag := flag.Bool("version", false, "Print version and exit")
	forceInit := flag.Bool("force-init", false, "Delete and re-create the .vix directory")
	testMode := flag.Bool("test", false, "Fill chat with fake data for UI testing")
	prompt := flag.String("p", "", "Run a single prompt non-interactively (headless mode). Use '-' to read from stdin.")
	workflow := flag.String("w", "", "Workflow name to run (e.g. 'Plan Workflow'). Requires -p.")
	outputFormat := flag.String("output-format", "text", "Output format for headless mode: text, json, stream-json")
	workdir := flag.String("workdir", "", "Set the working directory for this session")
	configDir := flag.String("config-dir", "", "Use this directory as the sole .vix config root (ignores ~/.vix and ./.vix)")
	disableWritePermission := flag.Bool("disable-automatic-write-permission", false, "Require user confirmation for write_file, edit_file, and delete_file calls (by default, writes execute without confirmation)")
	disableDirAccess := flag.Bool("disable-automatic-directory-access", false, "Restrict tool calls to paths within the working directory (by default, all paths are accessible)")
	vfsFlag := flag.Bool("vfs", false, "Run a VFS command (e.g. vix --vfs read_file <path>)")
	logDir := flag.String("log-dir", "", "Directory for log files (vixd.log, vix-thinking.log, vix-bash-history.log, vix-jobs/). Defaults to the system temp dir. Forwarded to the spawned vixd.")
	socketPath := flag.String("socket-path", "", "Unix socket path for the vix↔vixd connection. Defaults to /tmp/vixd.sock. Forwarded to the spawned vixd.")
	authTokenPath := flag.String("auth-token-path", "", "Path to a file holding the shared-secret token to authenticate every socket message. Must match the daemon's -auth-token-path. Empty disables auth on this client; the daemon must also be unauthenticated for that to work.")
	pprofPort := flag.Int("pprof-port", 0, "Port for the pprof HTTP server (GET /debug/pprof/*). 0 disables it. Env: VIX_PPROF_PORT.")
	flag.Parse()

	// Resolve --log-dir to an absolute path once so the value passed
	// through to vixd matches what vix uses for the daemon stdout
	// redirect. Empty stays empty (default behaviour preserved).
	resolvedLogDir := ""
	if *logDir != "" {
		abs, err := filepath.Abs(*logDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot resolve --log-dir %q: %v\n", *logDir, err)
			os.Exit(1)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot create --log-dir %q: %v\n", abs, err)
			os.Exit(1)
		}
		resolvedLogDir = abs
	}

	if v := os.Getenv("VIX_PPROF_PORT"); v != "" && *pprofPort == 0 {
		if p, err := strconv.Atoi(v); err == nil {
			*pprofPort = p
		}
	}
	if *pprofPort > 0 {
		pprofCtx, pprofCancel := context.WithCancel(context.Background())
		defer pprofCancel()
		go daemon.StartPprofServer(pprofCtx, *pprofPort)
	}

	if *versionFlag {
		fmt.Println("vix " + Version)
		return
	}

	// VFS subcommands
	if *vfsFlag {
		args := flag.Args()
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: vix --vfs read_file <path>\n       vix --vfs edit_file <path> <old_string> <new_string>\n")
			os.Exit(1)
		}
		cwd, _ := os.Getwd()
		if *workdir != "" {
			cwd = *workdir
		}
		vfsPaths := config.NewVixPaths(*configDir, config.HomeVixDir(), cwd)
		brain.InitLanguageMap(vfsPaths.Settings())

		// For legacy VfsEdit callers that expect a home dir, pass the override
		// in override mode so formatter configs resolve from there.
		vfsHomeDir := config.HomeVixDir()
		if *configDir != "" {
			vfsHomeDir = *configDir
		}

		switch args[0] {
		case "read_file":
			output, err := daemon.VfsRead(cwd, nil, args[1], nil, nil, false)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			fmt.Print(output)
		case "edit_file":
			if len(args) < 4 {
				fmt.Fprintf(os.Stderr, "Usage: vix --vfs edit_file <path> <old_string> <new_string>\n")
				os.Exit(1)
			}
			msg, _, err := daemon.VfsEdit(cwd, nil, vfsHomeDir, args[1], args[2], args[3], false)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			fmt.Println(msg)
		default:
			fmt.Fprintf(os.Stderr, "Unknown vfs command: %s\nUsage: vix --vfs read_file <path>\n       vix --vfs edit_file <path> <old_string> <new_string>\n", args[0])
			os.Exit(1)
		}
		return
	}

	// Validate flags
	format := headless.OutputFormat(*outputFormat)
	if *prompt == "" && *outputFormat != "text" {
		fmt.Fprintf(os.Stderr, "Error: --output-format requires -p\n")
		os.Exit(1)
	}
	if *workflow != "" && *prompt == "" {
		fmt.Fprintf(os.Stderr, "Error: -w requires -p\n")
		os.Exit(1)
	}
	if *prompt != "" && !format.Valid() {
		fmt.Fprintf(os.Stderr, "Error: invalid --output-format %q (must be text, json, or stream-json)\n", *outputFormat)
		os.Exit(1)
	}

	// Read prompt from stdin if -p -
	if *prompt == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
			os.Exit(1)
		}
		text := string(data)
		if text == "" {
			fmt.Fprintf(os.Stderr, "Error: empty prompt from stdin\n")
			os.Exit(1)
		}
		prompt = &text
	}

	// Pre-flight credential resolution. The session resolves the actual
	// per-provider credential when the daemon constructs the LLM (based on
	// the active chat agent's `model:` frontmatter); this check just makes
	// sure the user has at least one usable key configured, failing fast in
	// headless mode when none is set. In interactive mode a missing credential
	// for the selected model is surfaced as an error in the UI by the daemon.
	// Users must set their provider's env var (ANTHROPIC_API_KEY /
	// CLAUDE_CODE_OAUTH_TOKEN / OPENAI_API_KEY / OPENROUTER_API_KEY /
	// MINIMAX_API_KEY / MIMO_API_KEY) themselves.
	var apiKey string
	apiKey, _ = config.ResolveProviderKey("anthropic") // includes CLAUDE_CODE_OAUTH_TOKEN fallback
	hasNonAnthropicKey := func() bool {
		for _, p := range []string{"bedrock", "openai", "openrouter", "minimax", "mimo"} {
			if k, _ := config.ResolveProviderKey(p); k != "" {
				return true
			}
		}
		return false
	}
	if apiKey == "" && !hasNonAnthropicKey() && *prompt != "" {
		fmt.Fprintf(os.Stderr, "Error: no API key found. Set ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN, OPENAI_API_KEY, OPENROUTER_API_KEY, MINIMAX_API_KEY, or MIMO_API_KEY.\n")
		os.Exit(1)
	}

	cfg, err := config.Load(*forceInit, *workdir, *configDir, *socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// When --config-dir is set, make sure the directory exists and is
	// bootstrapped with default settings/agents so the session starts with a
	// working config.
	if cfg.ConfigDir != "" {
		if err := os.MkdirAll(cfg.ConfigDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating --config-dir %q: %v\n", cfg.ConfigDir, err)
			os.Exit(1)
		}
		if err := config.BootstrapHomeVixDir(cfg.ConfigDir, Version); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: bootstrap of --config-dir failed: %v\n", err)
		}
	}

	// Load the data-driven provider/model registry so the model picker reflects
	// embedded defaults plus any ~/.vix and ./.vix providers.json overlays. On
	// error, fall back to the embedded defaults.
	if err := providers.Configure(cfg.Paths.Providers()); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: providers config failed, using embedded defaults: %v\n", err)
	}

	appMode := "tui"
	if *prompt != "" {
		appMode = "headless"
	}
	telemetry.Init(telemetry.Config{Version: Version, Mode: appMode, Enabled: config.TelemetryEnabled()})
	defer telemetry.Shutdown()
	// Top-level crash handler: capture the panic as a PostHog exception and
	// flush synchronously (Shutdown is bounded by ShutdownTimeout) before the
	// process dies, then re-panic to preserve Go's crash output and exit code.
	// Registered after the Shutdown defer so it runs first on unwind; the
	// later Shutdown is a no-op (closeOnce). Only catches main-goroutine panics.
	defer func() {
		if r := recover(); r != nil {
			telemetry.TrackPanic("vix.main", r, debug.Stack())
			telemetry.Shutdown()
			panic(r)
		}
	}()
	telemetry.TrackTUIStarted(appMode, Version)
	// Record session end on shutdown. Registered after the Shutdown defer so it
	// runs first on unwind (before Shutdown flushes); the endOnce guard inside
	// keeps it single-fire even on the panic path above.
	defer telemetry.TrackTUIEnded()
	ui.Version = Version

	var session *daemon.SessionClient

	// restoreSessions holds the persisted open sessions (beyond the first,
	// which becomes the initial client) that the TUI reopens on Init.
	var restoreSessions []protocol.SessionSummary

	// initialAttached is true when the initial session client resumed a
	// persisted session (Attach) rather than starting fresh (Connect). The TUI
	// uses it to show a "Restoring conversation…" placeholder until the replay
	// arrives, instead of flashing the welcome screen.
	var initialAttached bool

	// Load the socket auth token (if -auth-token-path was given) once,
	// before any daemon RPC. Same file the spawned vixd will read on the
	// other side, so client and daemon arrive at identical bytes. We
	// fail-fast on a misconfigured path: silently dropping auth would
	// defeat the purpose of pointing at it.
	authToken := ""
	if *authTokenPath != "" {
		raw, err := os.ReadFile(*authTokenPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read --auth-token-path %q: %v\n", *authTokenPath, err)
			os.Exit(1)
		}
		authToken = strings.TrimSpace(string(raw))
		if authToken == "" {
			fmt.Fprintf(os.Stderr, "Error: --auth-token-path %q is empty after trimming whitespace\n", *authTokenPath)
			os.Exit(1)
		}
	}

	if !*testMode {
		client := daemon.NewClient(cfg.SocketPath)
		client.SetAuthToken(authToken)
		if !client.Ping() {
			// No daemon answered the ping — spawn a detached, long-lived daemon
			// (silently, in both interactive and headless mode). It is NOT tied
			// to this client's lifecycle: it runs in its own session (setsid) and
			// survives this process exiting, so other clients sharing it keep
			// working. It self-terminates once its last attached vix instance
			// disconnects (--exit-with-clients; see startDaemon). An already
			// running daemon is reused.
			if _, err := startDaemon(apiKey, resolvedLogDir, cfg.SocketPath, *authTokenPath); err != nil {
				fmt.Fprintf(os.Stderr, "Error starting daemon: %v\n", err)
				os.Exit(1)
			}
			if !waitForDaemon(client, 5*time.Second) {
				fmt.Fprintf(os.Stderr, "Error: daemon did not start in time\n")
				os.Exit(1)
			}
		}

		// Connect session if daemon is running
		if client.Ping() {
			// Register this vix process as an attached instance for its whole
			// lifetime. The daemon counts these (independently of sessions) so a
			// vix-spawned daemon can shut down once its last client leaves
			// (--exit-with-clients). Best-effort: if registration fails we still
			// run — the daemon just won't auto-exit on our account.
			instanceMode := "tui"
			if *prompt != "" {
				instanceMode = "headless"
			}
			if ic, err := daemon.RegisterInstance(cfg.SocketPath, authToken, instanceMode); err == nil {
				defer ic.Close()
			}

			session = daemon.NewSessionClient(cfg.SocketPath)
			session.SetAuthToken(authToken)

			// TUI mode: reopen previously-open sessions for this cwd. Sessions
			// already live in the daemon (Attached) are owned by another vix
			// instance — skip them, since exclusive ownership would refuse the
			// attach anyway. The first non-attached session becomes the initial
			// client; the rest are attached by the TUI on Init. Headless mode
			// (prompt set) always starts fresh.
			attached := false
			if *prompt == "" {
				if sums, err := client.ListSessions(cfg.CWD, cfg.ConfigDir); err == nil {
					var claimable []protocol.SessionSummary
					for _, sum := range sums {
						if !sum.Attached {
							claimable = append(claimable, sum)
						}
					}
					if len(claimable) > 0 {
						if err := session.Attach(cfg.CWD, cfg.ConfigDir, cfg.Model, cfg.ForceInit, !*disableWritePermission, !*disableDirAccess, false, claimable[0].ID); err == nil {
							restoreSessions = claimable[1:]
							attached = true
							initialAttached = true
						}
					}
				}
			}
			if !attached {
				if err := session.Connect(cfg.CWD, cfg.ConfigDir, cfg.Model, cfg.ForceInit, !*disableWritePermission, !*disableDirAccess, *prompt != ""); err != nil {
					fmt.Fprintf(os.Stderr, "Error connecting to daemon: %v\n", err)
					os.Exit(1)
				}
			}
			// Headless sessions are one-shot: close the record explicitly so
			// it isn't restored by the next TUI launch. TUI exits must NOT
			// send session.close here — the bare disconnect leaves records in
			// open/ so they restore on relaunch; an explicit close-all is
			// handled by the quit dialog (closeSessionsForQuit) when the user
			// opts in.
			if *prompt != "" {
				defer session.SendClose()
			}
		}
	}

	// Headless mode: send prompt and print result
	if *prompt != "" {
		if session == nil {
			fmt.Fprintf(os.Stderr, "Error: headless mode requires a daemon connection (cannot use --test)\n")
			os.Exit(1)
		}
		if err := headless.Run(session, *prompt, format, *workflow, cfg.Model); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	ui.ApplyTheme(config.LoadThemeConfig(cfg.Paths))

	model := ui.NewModel(cfg, session, *testMode, authToken, !*disableWritePermission, !*disableDirAccess)
	model.SetRestoreSessions(restoreSessions)
	model.SetInitialAwaitingReplay(initialAttached)

	p := tea.NewProgram(model)
	ui.SetProgram(p)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// findDaemon returns the path to the vixd binary.
// It prefers the vixd sitting next to the current executable so the client
// and daemon always come from the same build; an unrelated vixd earlier on
// $PATH (e.g. a stale install) would otherwise be spawned with flags it may
// not understand. Falls back to $PATH only when no sibling binary exists.
func findDaemon() (string, error) {
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), "vixd")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if p, err := exec.LookPath("vixd"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("vixd not found next to the vix binary or in $PATH")
}

// startDaemon spawns the daemon process as a subprocess.
// If apiKey is non-empty, it is injected into the subprocess environment.
// The daemon's stdout and stderr are redirected to <logDir>/vixd.log so
// that logs, panics, and crash traces are recoverable after the fact.
// If logDir is empty, os.TempDir() is used (the legacy /tmp default).
// logDir, when non-empty, is also forwarded to the spawned vixd via
// --log-dir so the daemon's own log files land in the same directory.
// socketPath is always forwarded to vixd so client and daemon agree on
// the socket location.
// authTokenPath, when non-empty, is forwarded so the daemon enforces
// shared-secret auth on every incoming socket message. The same path is
// read by the client (vix CLI) so both sides see the same token.
func startDaemon(apiKey, logDir, socketPath, authTokenPath string) (*exec.Cmd, error) {
	daemonPath, err := findDaemon()
	if err != nil {
		return nil, err
	}
	args := []string{}
	if logDir != "" {
		args = append(args, "--log-dir", logDir)
	}
	if socketPath != "" {
		args = append(args, "--socket-path", socketPath)
	}
	if authTokenPath != "" {
		args = append(args, "--auth-token-path", authTokenPath)
	}
	// A daemon spawned by vix is private to this instance, so it must not
	// serve the mission-control web UI (which would otherwise contend for
	// the fixed web port). Standalone `vixd` keeps serving it.
	args = append(args, "--no-mission-control")
	// A vix-spawned daemon is detached and long-lived (see SysProcAttr below),
	// but it should not outlive the vix processes using it: --exit-with-clients
	// makes it shut down shortly after the last attached vix instance leaves. A
	// directly-launched vixd omits this and runs until signalled.
	args = append(args, "--exit-with-clients")
	cmd := exec.Command(daemonPath, args...)
	// Detach the daemon from this client: start it in a new session (setsid) so
	// it is not in the client's process group and is unaffected by terminal
	// signals (SIGHUP on terminal close, SIGINT/SIGTERM to the foreground
	// group). Combined with not killing it on exit, this makes the daemon a
	// shared, long-lived process that outlives the instance that spawned it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if apiKey != "" {
		cmd.Env = append(os.Environ(), "ANTHROPIC_API_KEY="+apiKey)
	}
	logFileDir := logDir
	if logFileDir == "" {
		logFileDir = os.TempDir()
	}
	if logFile, err := os.OpenFile(filepath.Join(logFileDir, "vixd.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Log the daemon's exit status asynchronously so we can distinguish a
	// crash / signal kill (e.g. OOM "signal: killed") from a clean shutdown.
	go func() {
		if err := cmd.Wait(); err != nil {
			fmt.Fprintf(os.Stderr, "[vix] daemon exited: %v\n", err)
		}
	}()
	return cmd, nil
}

// waitForDaemon polls until the daemon responds to ping or timeout.
func waitForDaemon(client *daemon.Client, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if client.Ping() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
