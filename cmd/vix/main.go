package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-isatty"

	"github.com/get-vix/vix/internal/auth"
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

	// `vix thread create` — create a Vix-initiated message thread in the
	// running daemon. A sibling verb group to `vix daemon`, dispatched before
	// flag parsing.
	if len(os.Args) >= 2 && os.Args[1] == "thread" {
		os.Exit(runThreadCommand(os.Args[2:]))
	}

	// `vix job run <id>` — fire a scheduled job immediately by id, out of band
	// from its schedule. Sibling verb group to `vix daemon`/`vix thread`.
	if len(os.Args) >= 2 && os.Args[1] == "job" {
		os.Exit(runJobCommand(os.Args[2:]))
	}

	// `vix hook trigger <id>` — fire a lifecycle hook immediately by id, out of
	// band from its event.
	if len(os.Args) >= 2 && os.Args[1] == "hook" {
		os.Exit(runHookCommand(os.Args[2:]))
	}

	// `vix mcp auth|logout <server>` — manage OAuth authentication for a url MCP
	// server. Sibling verb group to `vix daemon`/`vix thread`.
	if len(os.Args) >= 2 && os.Args[1] == "mcp" {
		os.Exit(runMcpCommand(os.Args[2:]))
	}

	versionFlag := flag.Bool("version", false, "Print version and exit")
	forceInit := flag.Bool("force-init", false, "Delete and re-create the .vix directory")
	testMode := flag.Bool("test", false, "Fill chat with fake data for UI testing")
	prompt := flag.String("p", "", "Run a single prompt non-interactively (headless mode). Use '-' to read from stdin.")
	workflow := flag.String("w", "", "Workflow name to run (e.g. 'Plan Workflow'). Requires -p.")
	outputFormat := flag.String("output-format", "text", "Output format for headless mode: text, json, stream-json")
	workdir := flag.String("workdir", "", "Set the working directory for this thread")
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

	// Pre-flight credential resolution. The thread resolves the actual
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
	// bootstrapped with default settings/agents so the thread starts with a
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
	// OAuth logins persist their token to the OS keychain, or to the plaintext,
	// home-global auth.json (shared with API-key credentials) when the OS
	// keychain is unusable (headless Linux/WSL/containers). The UI and logs
	// surface that tokens then live unencrypted on disk.
	auth.SetAuthFilePath(config.NewVixPaths("", config.HomeVixDir(), "").AuthFile())
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
	// Record thread end on shutdown. Registered after the Shutdown defer so it
	// runs first on unwind (before Shutdown flushes); the endOnce guard inside
	// keeps it single-fire even on the panic path above.
	defer telemetry.TrackTUIEnded()
	ui.Version = Version

	var thread *daemon.ThreadClient

	// instanceClient is the window's long-lived control channel to the daemon,
	// carrying process-level events (threads_changed, jobs_changed, quit)
	// independent of any chat thread. Held for the process lifetime; passed to
	// the TUI model so it can read the channel. nil when registration failed.
	var instanceClient *daemon.InstanceClient

	// restoreThreads holds the persisted open threads (beyond the first,
	// which becomes the initial client) that the TUI reopens on Init.
	var restoreThreads []protocol.ThreadSummary

	// initialAttached is true when the initial thread client resumed a
	// persisted thread (Attach) rather than starting fresh (Connect). The TUI
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
			_, statErr := os.Stat(cfg.SocketPath)
			printDaemonError(statErr == nil, cfg.SocketPath)
			os.Exit(1)
		}

		// Version gate: refuse to talk to a daemon from a different build. The
		// daemon enforces the same rule on thread start; this client-side check
		// fires first and produces the friendlier message. An empty daemon
		// version means a pre-gate build, which is a mismatch for any client.
		daemonVersion, _ := client.DaemonVersion()
		if daemonVersion != Version {
			dv := daemonVersion
			if dv == "" {
				dv = "(unknown, pre-gate build)"
			}
			fmt.Fprintf(os.Stderr, "Error: vix %s cannot talk to vixd %s.\nRestart the daemon: vix daemon stop && vix daemon start\n", Version, dv)
			os.Exit(1)
		}

		instanceMode := "tui"
		if *prompt != "" {
			instanceMode = "headless"
		}
		// Register this vix process as an attached instance for its whole
		// lifetime. Besides the observability count (web UI vitals, logging),
		// the connection is the window's control channel: the daemon pushes
		// process-level events (threads_changed, jobs_changed, quit) to it,
		// independent of any chat thread, so a launch-time draft still refreshes
		// live. Best-effort: if registration fails we still run (just without
		// live process-level updates). Only the TUI reads the channel; headless
		// holds it open purely for the count.
		if ic, err := daemon.RegisterInstance(cfg.SocketPath, authToken, instanceMode); err == nil {
			instanceClient = ic
			defer ic.Close()
		}

		thread = daemon.NewThreadClient(cfg.SocketPath)
		thread.SetAuthToken(authToken)

		// TUI mode: reopen previously-open threads for this cwd. Threads
		// already live in the daemon (Attached) are owned by another vix
		// instance — skip them, since exclusive ownership would refuse the
		// attach anyway. The first non-attached thread becomes the initial
		// client; the rest are attached by the TUI on Init. Headless mode
		// (prompt set) always starts fresh.
		attached := false
		if *prompt == "" {
			if sums, err := client.ListThreads(cfg.CWD, cfg.ConfigDir); err == nil {
				var claimable []protocol.ThreadSummary
				for _, sum := range sums {
					// Only auto-reopen user threads rooted at this cwd:
					// thread.list now returns every directory's threads (so
					// the TUI can group them), but launch restore stays
					// project-scoped. Skip threads another instance owns
					// (Attached) and vix-initiated records (job runs / alerts),
					// which are browsed from the threads list, never
					// auto-reopened as chat tabs.
					if !sum.Attached && sum.Origin != "vix" && sum.CWD == cfg.CWD {
						claimable = append(claimable, sum)
					}
				}
				if len(claimable) > 0 {
					if err := thread.Attach(cfg.CWD, cfg.ConfigDir, cfg.Model, cfg.ForceInit, !*disableWritePermission, !*disableDirAccess, false, claimable[0].ID); err == nil {
						restoreThreads = claimable[1:]
						attached = true
						initialAttached = true
					}
				}
			}
		}
		if !attached {
			if *prompt != "" {
				// Headless mode always needs a live connection up front.
				if err := thread.Connect(cfg.CWD, cfg.ConfigDir, cfg.Model, cfg.ForceInit, !*disableWritePermission, !*disableDirAccess, true); err != nil {
					fmt.Fprintf(os.Stderr, "Error connecting to daemon: %v\n", err)
					os.Exit(1)
				}
			} else {
				// TUI mode with nothing to restore: start as a draft. No
				// thread.start is sent until the user submits the first
				// message, which lets them pick the working directory first and
				// avoids creating a ghost empty thread on quit. Passing a nil
				// client to NewModel yields a draft initial thread.
				thread = nil
			}
		}
		// Headless threads are one-shot: close the record explicitly so
		// it isn't restored by the next TUI launch. TUI exits must NOT
		// send thread.close here — the bare disconnect leaves records in
		// open/ so they restore on relaunch; an explicit close-all is
		// handled by the quit dialog (closeThreadsForQuit) when the user
		// opts in.
		if *prompt != "" {
			defer thread.SendClose()
		}
	}

	// Headless mode: send prompt and print result
	if *prompt != "" {
		if thread == nil {
			fmt.Fprintf(os.Stderr, "Error: headless mode requires a daemon connection (cannot use --test)\n")
			os.Exit(1)
		}
		if err := headless.Run(thread, *prompt, format, *workflow, cfg.Model); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	ui.ApplyTheme(config.LoadThemeConfig(cfg.Paths))

	model := ui.NewModel(cfg, thread, *testMode, authToken, !*disableWritePermission, !*disableDirAccess)
	model.SetRestoreThreads(restoreThreads)
	model.SetInitialAwaitingReplay(initialAttached)
	model.SetInstanceClient(instanceClient)

	p := tea.NewProgram(model)
	ui.SetProgram(p)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// printDaemonError writes a styled, informational message to stderr when vix
// can't reach vixd. stale=true means a socket file exists but nothing answers
// (a daemon that exited uncleanly); stale=false means no daemon at all. Colors
// come from the TUI brand palette (internal/ui/styles.go) and degrade to plain
// text when stderr isn't a terminal or NO_COLOR is set.
func printDaemonError(stale bool, socketPath string) {
	color := os.Getenv("NO_COLOR") == "" && isatty.IsTerminal(os.Stderr.Fd())

	coral := lipgloss.NewStyle().Foreground(lipgloss.Color("#FC6F63")).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	cmd := lipgloss.NewStyle().Foreground(lipgloss.Color("#63F0FC")).Bold(true)

	paint := func(st lipgloss.Style, s string) string {
		if !color {
			return s
		}
		return st.Render(s)
	}

	var headline string
	var body []string
	var actions [][2]string // label, command

	if stale {
		headline = "⬣ vixd isn't responding"
		body = []string{
			fmt.Sprintf("Found a socket at %s, but nothing is listening — a", socketPath),
			"previous daemon probably exited uncleanly.",
		}
		actions = [][2]string{
			{"Restart it", "vix daemon stop && vix daemon start"},
			{"Check it", "vix daemon status"},
		}
	} else {
		headline = "⬣ vixd isn't running"
		body = []string{
			"vix is just the client — it talks to vixd, the background",
			"daemon that runs your threads, tools, and code analysis.",
		}
		actions = [][2]string{
			{"Start it", "vix daemon start"},
			{"Check it", "vix daemon status"},
		}
	}

	labelWidth := 0
	for _, a := range actions {
		if len(a[0]) > labelWidth {
			labelWidth = len(a[0])
		}
	}

	var b strings.Builder
	b.WriteString("\n  " + paint(coral, headline) + "\n\n")
	for _, line := range body {
		b.WriteString("  " + paint(dim, line) + "\n")
	}
	b.WriteString("\n")
	for _, a := range actions {
		label := fmt.Sprintf("%-*s", labelWidth, a[0])
		b.WriteString("  " + paint(dim, "→  "+label+"  ") + paint(cmd, a[1]) + "\n")
	}
	b.WriteString("\n")

	fmt.Fprint(os.Stderr, b.String())
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
		if v != Version {
			fmt.Printf("WARNING: this vix is %s — version mismatch, threads will be refused.\nRestart the daemon: vix daemon stop && vix daemon start\n", Version)
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

// runThreadCommand implements the `vix thread create` subcommand: create a
// Vix-initiated message thread in the running daemon from a whole-JSON spec
// (MessageThreadSpec) read from --json, --file, or stdin. Returns the exit
// code. The created thread lands in the Threads tab under "Vix-initiated".
func runThreadCommand(args []string) int {
	usage := func() {
		fmt.Fprintf(os.Stderr, `Usage: vix thread create [flags]

  create   Create a Vix-initiated message thread in the running daemon.
           The spec is a JSON object read from --json, --file, or stdin:
             { "message": "…", "cwd": "/abs/project", "title": "…" }
           message and cwd are required. Prints the new thread id.

Flags:
  -json string             Inline JSON spec
  -file string             Read the JSON spec from this file ("-" = stdin)
  -socket-path string      Unix socket path (env VIX_SOCKET_PATH, default /tmp/vixd.sock)
  -auth-token-path string  Shared-secret token file, must match the daemon's
`)
	}
	if len(args) == 0 || args[0] != "create" {
		usage()
		return 1
	}

	fs := flag.NewFlagSet("vix thread create", flag.ExitOnError)
	jsonFlag := fs.String("json", "", "Inline JSON thread spec.")
	fileFlag := fs.String("file", "", `Read the JSON thread spec from this file ("-" for stdin).`)
	socketPath := fs.String("socket-path", "", "Unix socket path for the vix↔vixd connection. Env: VIX_SOCKET_PATH. Default: /tmp/vixd.sock.")
	authTokenPath := fs.String("auth-token-path", "", "Path to a file holding the shared-secret token. Must match the daemon's -auth-token-path.")
	fs.Parse(args[1:])

	// Resolve the spec from exactly one source: --json, --file, else stdin.
	var rawSpec []byte
	switch {
	case *jsonFlag != "":
		rawSpec = []byte(*jsonFlag)
	case *fileFlag != "" && *fileFlag != "-":
		b, err := os.ReadFile(*fileFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read --file %q: %v\n", *fileFlag, err)
			return 1
		}
		rawSpec = b
	default:
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read spec from stdin: %v\n", err)
			return 1
		}
		rawSpec = b
	}

	// Validate it's a JSON object locally so the user gets a clear error before
	// a round-trip to the daemon.
	var probe map[string]any
	if err := json.Unmarshal(rawSpec, &probe); err != nil {
		fmt.Fprintf(os.Stderr, "Error: spec is not a valid JSON object: %v\n", err)
		return 1
	}

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
	if !client.Ping() {
		fmt.Fprintf(os.Stderr, "Error: vixd is not running — start it with `vix daemon start`\n")
		return 1
	}

	id, err := client.CreateMessageThread(json.RawMessage(rawSpec))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	fmt.Println(id)
	return 0
}

// dialDaemon resolves the socket path (flag, else VIX_SOCKET_PATH, else the
// default) and an optional auth token, returning a pinged client ready for a
// one-shot RPC. On failure it prints the reason to stderr and returns
// (nil, exitCode).
func dialDaemon(socketPath, authTokenPath string) (*daemon.Client, int) {
	sock := socketPath
	if sock == "" {
		if v := os.Getenv("VIX_SOCKET_PATH"); v != "" {
			sock = v
		} else {
			sock = "/tmp/vixd.sock"
		}
	}

	authToken := ""
	if authTokenPath != "" {
		raw, err := os.ReadFile(authTokenPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read --auth-token-path %q: %v\n", authTokenPath, err)
			return nil, 1
		}
		authToken = strings.TrimSpace(string(raw))
		if authToken == "" {
			fmt.Fprintf(os.Stderr, "Error: --auth-token-path %q is empty after trimming whitespace\n", authTokenPath)
			return nil, 1
		}
	}

	client := daemon.NewClient(sock)
	client.SetAuthToken(authToken)
	if !client.Ping() {
		fmt.Fprintf(os.Stderr, "Error: vixd is not running — start it with `vix daemon start`\n")
		return nil, 1
	}
	return client, 0
}

// runJobCommand implements `vix job run <id>`: fire a scheduled job immediately
// by id in the running daemon, out of band from its schedule. The run proceeds
// in the background; the command prints the run's thread id. Returns the exit
// code.
func runJobCommand(args []string) int {
	usage := func() {
		fmt.Fprintf(os.Stderr, `Usage: vix job run <id> [flags]

  run <id>   Fire the job with the given id immediately, out of band from its
             schedule. A manual run records its outcome but does not advance the
             schedule or complete a one-shot, and runs even when the job is
             disabled. The run proceeds in the background and lands under
             "Vix-initiated" threads. Prints the run's thread id.

Flags:
  -socket-path string      Unix socket path (env VIX_SOCKET_PATH, default /tmp/vixd.sock)
  -auth-token-path string  Shared-secret token file, must match the daemon's
`)
	}
	if len(args) == 0 || args[0] != "run" {
		usage()
		return 1
	}

	fs := flag.NewFlagSet("vix job run", flag.ExitOnError)
	socketPath := fs.String("socket-path", "", "Unix socket path for the vix↔vixd connection. Env: VIX_SOCKET_PATH. Default: /tmp/vixd.sock.")
	authTokenPath := fs.String("auth-token-path", "", "Path to a file holding the shared-secret token. Must match the daemon's -auth-token-path.")
	fs.Parse(args[1:])
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: missing job id\n\n")
		usage()
		return 1
	}
	id := fs.Arg(0)

	client, code := dialDaemon(*socketPath, *authTokenPath)
	if client == nil {
		return code
	}

	threadID, err := client.RunJob(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	fmt.Println(threadID)
	return 0
}

// runHookCommand implements `vix hook trigger <id>`: fire a lifecycle hook
// immediately by id in the running daemon, out of band from its event. Prints
// the run's thread id (workflow/prompt hooks) or the fire id (command hooks).
// Returns the exit code.
func runHookCommand(args []string) int {
	usage := func() {
		fmt.Fprintf(os.Stderr, `Usage: vix hook trigger <id> [flags]

  trigger <id>  Fire the hook with the given id immediately, out of band from
                its event. It runs fire-and-forget regardless of mode, even when
                the hook is disabled. Workflow/prompt hooks run in an isolated
                thread (its id is printed); command hooks print their fire id.

Flags:
  -socket-path string      Unix socket path (env VIX_SOCKET_PATH, default /tmp/vixd.sock)
  -auth-token-path string  Shared-secret token file, must match the daemon's
`)
	}
	if len(args) == 0 || args[0] != "trigger" {
		usage()
		return 1
	}

	fs := flag.NewFlagSet("vix hook trigger", flag.ExitOnError)
	socketPath := fs.String("socket-path", "", "Unix socket path for the vix↔vixd connection. Env: VIX_SOCKET_PATH. Default: /tmp/vixd.sock.")
	authTokenPath := fs.String("auth-token-path", "", "Path to a file holding the shared-secret token. Must match the daemon's -auth-token-path.")
	fs.Parse(args[1:])
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: missing hook id\n\n")
		usage()
		return 1
	}
	id := fs.Arg(0)

	client, code := dialDaemon(*socketPath, *authTokenPath)
	if client == nil {
		return code
	}

	threadID, fireID, err := client.TriggerHook(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	if threadID != "" {
		fmt.Println(threadID)
	} else {
		fmt.Println(fireID)
	}
	return 0
}

// runMcpCommand implements `vix mcp auth <server>` and `vix mcp logout <server>`:
// manage OAuth authentication for a url MCP server. `auth` starts the browser
// consent flow (the daemon holds the loopback listener and exchanges the token)
// and polls until the server reports authenticated. Returns the exit code.
func runMcpCommand(args []string) int {
	usage := func() {
		fmt.Fprintf(os.Stderr, `Usage: vix mcp <auth|logout> <server> [flags]

  auth <server>     Authenticate an OAuth MCP server. Opens your browser to the
                    provider's consent screen; on approval the token is stored
                    and the server's tools become available to new threads.
  logout <server>   Delete the stored OAuth token for a server.

Flags:
  -socket-path string      Unix socket path (env VIX_SOCKET_PATH, default /tmp/vixd.sock)
  -auth-token-path string  Shared-secret token file, must match the daemon's
`)
	}
	if len(args) == 0 || (args[0] != "auth" && args[0] != "logout") {
		usage()
		return 1
	}
	action := args[0]

	fs := flag.NewFlagSet("vix mcp "+action, flag.ExitOnError)
	socketPath := fs.String("socket-path", "", "Unix socket path for the vix↔vixd connection. Env: VIX_SOCKET_PATH. Default: /tmp/vixd.sock.")
	authTokenPath := fs.String("auth-token-path", "", "Path to a file holding the shared-secret token. Must match the daemon's -auth-token-path.")
	fs.Parse(args[1:])
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: missing MCP server name\n\n")
		usage()
		return 1
	}
	name := fs.Arg(0)

	client, code := dialDaemon(*socketPath, *authTokenPath)
	if client == nil {
		return code
	}

	if action == "logout" {
		if err := client.LogoutMCP(name); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
		fmt.Printf("Signed out of MCP server %q.\n", name)
		return 0
	}

	authURL, err := client.AuthorizeMCP(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	fmt.Printf("Opening your browser to authorize %q.\nIf it doesn't open, visit:\n\n  %s\n\n", name, authURL)
	openURLBestEffort(authURL)

	// Poll until the server reports authenticated (or we give up).
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		servers, err := client.ListMCPServers()
		if err != nil {
			continue
		}
		for _, srv := range servers {
			if srv.Name == name && srv.Auth == "authenticated" {
				fmt.Printf("Authorized %q. Its tools are available to new threads.\n", name)
				return 0
			}
		}
	}
	fmt.Fprintf(os.Stderr, "Timed out waiting for authorization of %q.\n", name)
	return 1
}

// openURLBestEffort opens url in the default browser, ignoring any error (the
// caller has already printed the URL as a fallback).
func openURLBestEffort(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
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
	pathEnv := daemonServiceSearchPath(home)
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
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key><string>%s</string>
	</dict>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, xmlEscape(daemonPath), xmlEscape(pathEnv), xmlEscape(filepath.Join(os.TempDir(), "vixd.log")), xmlEscape(filepath.Join(os.TempDir(), "vixd.log")))
		activate = []string{"launchctl", "load", "-w", path}
		deactivate = []string{"launchctl", "unload", "-w", path}
		return path, content, activate, deactivate, nil
	case "linux":
		path = filepath.Join(home, ".config", "systemd", "user", "vixd.service")
		content = fmt.Sprintf(`[Unit]
Description=vix daemon

[Service]
ExecStart=%s
Environment="%s"
Restart=on-failure

[Install]
WantedBy=default.target
`, daemonPath, systemdEscapeQuotedEnv("PATH="+pathEnv))
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

// daemonSearchPath builds a stable PATH for the daemon and any MCP/tool
// subprocesses it launches. GUI/login-service starts often inherit a stripped
// environment, so relying on the parent PATH makes node/npx, rg, and fd
// disappear even when they are installed for the user.
func daemonSearchPath() string {
	home, _ := os.UserHomeDir()
	return buildDaemonPath(os.Getenv("PATH"), home)
}

func daemonServiceSearchPath(home string) string {
	return buildDaemonPath("", home)
}

func buildDaemonPath(existingPath, home string) string {
	entries := append([]string{}, splitPathList(existingPath)...)

	if runtime.GOOS == "darwin" {
		entries = append(entries,
			"/opt/homebrew/bin",
			"/opt/homebrew/sbin",
			"/usr/local/bin",
		)
	}

	if home != "" {
		if matches, err := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin")); err == nil {
			sort.Sort(sort.Reverse(sort.StringSlice(matches)))
			entries = append(entries, matches...)
		}
		for _, p := range []string{
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, ".local", "bin"),
		} {
			if isDir(p) {
				entries = append(entries, p)
			}
		}
	}

	entries = append(entries, "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	return strings.Join(dedupeStrings(entries), string(os.PathListSeparator))
}

func splitPathList(pathValue string) []string {
	var entries []string
	for _, entry := range filepath.SplitList(pathValue) {
		if entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func daemonEnv(apiKey string) []string {
	env := upsertEnv(os.Environ(), "PATH", daemonSearchPath())
	if apiKey != "" {
		env = upsertEnv(env, "ANTHROPIC_API_KEY", apiKey)
	}
	return env
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, prefix+value)
}

func xmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	).Replace(s)
}

func systemdEscapeQuotedEnv(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
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
	// Detach the daemon from this client: start it in a new thread (setsid) so
	// it is not in the client's process group and is unaffected by terminal
	// signals (SIGHUP on terminal close, SIGINT/SIGTERM to the foreground
	// group). The daemon is a shared, long-lived process that runs until
	// signalled or stopped via `vix daemon stop`.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = daemonEnv(apiKey)
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
