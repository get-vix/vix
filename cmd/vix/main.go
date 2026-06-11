package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	// Subcommand dispatch before flag parsing: `vix daemon start|stop|status`
	// manages the long-lived vixd process explicitly (vix never auto-spawns it).
	if len(os.Args) >= 2 && os.Args[1] == "daemon" {
		os.Exit(runDaemonCommand(os.Args[2:]))
	}

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
	socketPath := flag.String("socket-path", "", "Unix socket path for the vix↔vixd connection. Defaults to /tmp/vixd.sock. Must match the running vixd.")
	authTokenPath := flag.String("auth-token-path", "", "Path to a file holding the shared-secret token to authenticate every socket message. Must match the daemon's -auth-token-path. Empty disables auth on this client; the daemon must also be unauthenticated for that to work.")
	pprofPort := flag.Int("pprof-port", 0, "Port for the pprof HTTP server (GET /debug/pprof/*). 0 disables it. Env: VIX_PPROF_PORT.")
	flag.Parse()

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
		daemon.SetClientVersion(Version)
		client := daemon.NewClient(cfg.SocketPath)
		client.SetAuthToken(authToken)
		if !client.Ping() {
			// vix never spawns the daemon: surface an actionable error instead.
			if _, statErr := os.Stat(cfg.SocketPath); statErr == nil {
				fmt.Fprintf(os.Stderr, "Error: vixd is not responding (stale socket at %s — a previous daemon may have exited uncleanly).\nStart it with: vix daemon start\n", cfg.SocketPath)
			} else {
				fmt.Fprintf(os.Stderr, "Error: vixd is not running.\nStart it with: vix daemon start\n")
			}
			os.Exit(1)
		}

		// Version gate: refuse to talk to a daemon from a different build. The
		// daemon enforces the same rule on session start; this client-side check
		// fires first and produces the friendlier message. "dev" on either side
		// skips the gate (local development); an empty daemon version means a
		// pre-gate build, which is a mismatch for any released client.
		daemonVersion, _ := client.DaemonVersion()
		if Version != "dev" && daemonVersion != "dev" && daemonVersion != Version {
			dv := daemonVersion
			if dv == "" {
				dv = "(unknown, pre-gate build)"
			}
			fmt.Fprintf(os.Stderr, "Error: vix %s cannot talk to vixd %s.\nRestart the daemon: vix daemon stop && vix daemon start\n", Version, dv)
			os.Exit(1)
		}

		// Register this vix process as an attached instance for its whole
		// lifetime. The daemon counts these for observability (web UI vitals,
		// logging). Best-effort: if registration fails we still run.
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
					// Skip sessions another instance owns, and vix-initiated
					// records (job runs / alerts): those are browsed from the
					// sessions list, never auto-reopened as chat tabs.
					if !sum.Attached && sum.Origin != "vix" {
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

// runDaemonCommand implements the `vix daemon start|stop|status` subcommand
// group. vix never auto-spawns vixd: this is the explicit management surface.
// Returns the process exit code.
func runDaemonCommand(args []string) int {
	usage := func() {
		fmt.Fprintf(os.Stderr, `Usage: vix daemon <start|stop|status|install|uninstall> [flags]

  start      Launch vixd detached (no-op if already running)
  stop       Coordinated shutdown: all attached vix instances quit, then vixd exits
  status     Report whether vixd is running and its version
  install    Register vixd to start at login (macOS LaunchAgent / Linux systemd user unit)
  uninstall  Remove the login registration

Flags:
  -socket-path string      Unix socket path (env VIX_SOCKET_PATH, default /tmp/vixd.sock)
  -log-dir string          Directory for vixd log files (start only)
  -auth-token-path string  Shared-secret token file, must match the daemon's
`)
	}
	if len(args) == 0 {
		usage()
		return 1
	}
	sub := args[0]

	fs := flag.NewFlagSet("vix daemon "+sub, flag.ExitOnError)
	logDir := fs.String("log-dir", "", "Directory for vixd log files (vixd.log, vix-thinking.log, vix-bash-history.log). Defaults to the system temp dir.")
	socketPath := fs.String("socket-path", "", "Unix socket path for the vix↔vixd connection. Env: VIX_SOCKET_PATH. Default: /tmp/vixd.sock.")
	authTokenPath := fs.String("auth-token-path", "", "Path to a file holding the shared-secret token. Must match the daemon's -auth-token-path.")
	fs.Parse(args[1:])

	sock := *socketPath
	if sock == "" {
		if v := os.Getenv("VIX_SOCKET_PATH"); v != "" {
			sock = v
		} else {
			sock = "/tmp/vixd.sock"
		}
	}

	authToken := ""
	if *authTokenPath != "" {
		raw, err := os.ReadFile(*authTokenPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read --auth-token-path %q: %v\n", *authTokenPath, err)
			return 1
		}
		authToken = strings.TrimSpace(string(raw))
		if authToken == "" {
			fmt.Fprintf(os.Stderr, "Error: --auth-token-path %q is empty after trimming whitespace\n", *authTokenPath)
			return 1
		}
	}

	client := daemon.NewClient(sock)
	client.SetAuthToken(authToken)

	switch sub {
	case "start":
		if client.Ping() {
			v, _ := client.DaemonVersion()
			fmt.Printf("vixd is already running (version %s) on %s\n", orUnknown(v), sock)
			return 0
		}
		resolvedLogDir := ""
		if *logDir != "" {
			abs, err := filepath.Abs(*logDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: cannot resolve --log-dir %q: %v\n", *logDir, err)
				return 1
			}
			if err := os.MkdirAll(abs, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "Error: cannot create --log-dir %q: %v\n", abs, err)
				return 1
			}
			resolvedLogDir = abs
		}
		apiKey, _ := config.ResolveProviderKey("anthropic")
		if _, err := startDaemon(apiKey, resolvedLogDir, sock, *authTokenPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error starting vixd: %v\n", err)
			return 1
		}
		if !waitForDaemon(client, 5*time.Second) {
			fmt.Fprintf(os.Stderr, "Error: vixd did not start in time (check %s/vixd.log)\n", logFileDirOrTmp(resolvedLogDir))
			return 1
		}
		v, _ := client.DaemonVersion()
		fmt.Printf("vixd started (version %s) on %s\n", orUnknown(v), sock)
		return 0

	case "stop":
		if !client.Ping() {
			fmt.Println("vixd is not running")
			return 0
		}
		if err := client.StopDaemon(); err != nil {
			fmt.Fprintf(os.Stderr, "Error stopping vixd: %v\n", err)
			return 1
		}
		// Wait for the socket to actually go quiet so `stop && start` works.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if !client.Ping() {
				fmt.Println("vixd stopped")
				return 0
			}
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprintf(os.Stderr, "Error: vixd did not stop in time\n")
		return 1

	case "status":
		if !client.Ping() {
			if _, err := os.Stat(sock); err == nil {
				fmt.Printf("vixd is not responding (stale socket at %s)\n", sock)
			} else {
				fmt.Println("vixd is not running")
			}
			return 1
		}
		v, _ := client.DaemonVersion()
		fmt.Printf("vixd is running (version %s) on %s\n", orUnknown(v), sock)
		if Version != "dev" && v != "dev" && v != Version {
			fmt.Printf("WARNING: this vix is %s — version mismatch, sessions will be refused.\nRestart the daemon: vix daemon stop && vix daemon start\n", Version)
		}
		return 0

	case "install":
		return installDaemonService()

	case "uninstall":
		return uninstallDaemonService()

	default:
		usage()
		return 1
	}
}

// orUnknown substitutes a placeholder for an empty version string (pre-gate
// daemon builds report no version).
func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// daemonServicePaths returns the login-service definition for this platform:
// the file to write, its content, and the activation/deactivation commands.
func daemonServicePaths() (path, content string, activate, deactivate []string, err error) {
	daemonPath, err := findDaemon()
	if err != nil {
		return "", "", nil, nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", nil, nil, err
	}
	switch runtime.GOOS {
	case "darwin":
		path = filepath.Join(home, "Library", "LaunchAgents", "com.getvix.vixd.plist")
		content = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>com.getvix.vixd</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, daemonPath, filepath.Join(os.TempDir(), "vixd.log"), filepath.Join(os.TempDir(), "vixd.log"))
		activate = []string{"launchctl", "load", "-w", path}
		deactivate = []string{"launchctl", "unload", "-w", path}
		return path, content, activate, deactivate, nil
	case "linux":
		path = filepath.Join(home, ".config", "systemd", "user", "vixd.service")
		content = fmt.Sprintf(`[Unit]
Description=vix daemon

[Service]
ExecStart=%s
Restart=on-failure

[Install]
WantedBy=default.target
`, daemonPath)
		activate = []string{"systemctl", "--user", "enable", "--now", "vixd.service"}
		deactivate = []string{"systemctl", "--user", "disable", "--now", "vixd.service"}
		return path, content, activate, deactivate, nil
	default:
		return "", "", nil, nil, fmt.Errorf("login-service install is not supported on %s", runtime.GOOS)
	}
}

// installDaemonService registers vixd to start at login, printing exactly what
// it will write and asking for confirmation first.
func installDaemonService() int {
	path, content, activate, _, err := daemonServicePaths()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	fmt.Printf("This will write:\n  %s\n\n%s\nand run: %s\n\nProceed? [y/N] ", path, content, strings.Join(activate, " "))
	var answer string
	fmt.Scanln(&answer)
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		fmt.Println("Aborted.")
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	if runtime.GOOS == "linux" {
		exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	if out, err := exec.Command(activate[0], activate[1:]...).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Error activating service: %v\n%s", err, out)
		return 1
	}
	fmt.Println("Installed: vixd now starts at login and restarts on failure.")
	fmt.Println("Remove with: vix daemon uninstall")
	return 0
}

// uninstallDaemonService removes the login registration.
func uninstallDaemonService() int {
	path, _, _, deactivate, err := daemonServicePaths()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Println("Not installed (nothing to remove).")
		return 0
	}
	if out, err := exec.Command(deactivate[0], deactivate[1:]...).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: deactivation failed: %v\n%s", err, out)
	}
	if err := os.Remove(path); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	fmt.Printf("Removed %s. The running daemon (if any) is untouched; stop it with: vix daemon stop\n", path)
	return 0
}

// logFileDirOrTmp returns where the vixd.log redirect lands for a given
// resolved --log-dir (empty means the system temp dir).
func logFileDirOrTmp(logDir string) string {
	if logDir != "" {
		return logDir
	}
	return os.TempDir()
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

// startDaemon spawns the daemon process, detached, for `vix daemon start`.
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
	cmd := exec.Command(daemonPath, args...)
	// Detach the daemon from this client: start it in a new session (setsid) so
	// it is not in the client's process group and is unaffected by terminal
	// signals (SIGHUP on terminal close, SIGINT/SIGTERM to the foreground
	// group). The daemon is a shared, long-lived process that runs until
	// signalled or stopped via `vix daemon stop`.
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
