package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon"
	"github.com/get-vix/vix/internal/daemon/brain"
	"github.com/get-vix/vix/internal/providers"
	"github.com/get-vix/vix/internal/telemetry"
	"github.com/get-vix/vix/internal/update"
	"github.com/google/uuid"
)

var Version = "dev"

const defaultSocketPath = "/tmp/vixd.sock"

func main() {
	// Hidden self-exec subcommand: when sandbox.go's Landlock branch
	// invokes us with "landlock-exec", we apply the ruleset to ourself
	// and execve the rest of argv. Must run BEFORE any normal startup
	// (no daemon, no telemetry, no socket) — the helper is meant to
	// turn into bash promptly.
	if len(os.Args) >= 2 && os.Args[1] == "landlock-exec" {
		daemon.LandlockExecMain(os.Args[2:])
		return
	}

	logDir := flag.String("log-dir", "", "Directory for daemon log files (vix-thinking.log, vix-bash-history.log, vix-jobs/). Defaults to the system temp dir. Env: VIX_LOG_DIR.")
	socketPathFlag := flag.String("socket-path", "", "Unix socket path for the vix↔vixd connection. Env: VIX_SOCKET_PATH. Default: "+defaultSocketPath+".")
	authTokenPath := flag.String("auth-token-path", "", "Path to a file holding the shared-secret token required on every incoming socket message. Empty means no auth check (in-process tests / trusted-host runs only — production deployments must set this and put the file outside the agent's reachable path tree, e.g. on a Landlock-blocked location). Env: VIX_AUTH_TOKEN_PATH.")
	webPort := flag.Int("web-port", 1337, "Port for the local web UI. 0 disables it. Env: VIX_WEB_PORT.")
	noMissionControl := flag.Bool("no-mission-control", false, "Disable the mission-control web UI server. Env: VIX_NO_MISSION_CONTROL.")
	pprofPort := flag.Int("pprof-port", 0, "Port for the pprof HTTP server (GET /debug/pprof/*). 0 disables it. Env: VIX_PPROF_PORT.")
	exitWithClients := flag.Bool("exit-with-clients", false, "Shut down automatically once the last attached vix instance disconnects (after a short grace period). Off by default so a directly-launched vixd runs until signalled. vix sets this when it spawns a daemon. Env: VIX_EXIT_WITH_CLIENTS.")
	flag.Parse()

	// Env-var fallbacks for path-bearing flags. Precedence: explicit
	// flag > env var > built-in default. Env vars are preferred when
	// vixd runs inside a containerised sandbox because flag values
	// are visible in /proc/<pid>/cmdline to any same-namespace
	// process. Env vars keep the configuration out of ``ps`` output.
	if *logDir == "" {
		*logDir = os.Getenv("VIX_LOG_DIR")
	}
	if *socketPathFlag == "" {
		if v := os.Getenv("VIX_SOCKET_PATH"); v != "" {
			*socketPathFlag = v
		} else {
			*socketPathFlag = defaultSocketPath
		}
	}
	if *authTokenPath == "" {
		*authTokenPath = os.Getenv("VIX_AUTH_TOKEN_PATH")
	}
	if v := os.Getenv("VIX_WEB_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			*webPort = p
		}
	}
	if !*noMissionControl {
		if v := os.Getenv("VIX_NO_MISSION_CONTROL"); v == "1" || v == "true" {
			*noMissionControl = true
		}
	}
	if v := os.Getenv("VIX_PPROF_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			*pprofPort = p
		}
	}
	if !*exitWithClients {
		if v := os.Getenv("VIX_EXIT_WITH_CLIENTS"); v == "1" || v == "true" {
			*exitWithClients = true
		}
	}

	if *logDir != "" {
		abs, err := filepath.Abs(*logDir)
		if err == nil {
			if err := os.MkdirAll(abs, 0o755); err == nil {
				daemon.SetTmpLogDir(abs)
			}
		}
	}

	startupT0 := time.Now()
	log.SetFlags(log.Ltime | log.Lmsgprefix)
	log.SetPrefix("[vixd] ")
	defer log.Printf("vixd exiting")
	daemon.ProtectDaemon()
	cred := config.ResolveProviderCredential("anthropic")
	if cred.Value != "" {
		log.Printf("API key loaded (source: %s)", cred.Source)
	} else {
		log.Printf("WARNING: No API key found — brain semantic analysis will be skipped")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() { sig := <-sigCh; log.Printf("Received shutdown signal: %s", sig); cancel() }()
	// Model is resolved per-session from the active chat agent's `model:`
	// frontmatter (see session.go). The daemon keeps a fallback string so
	// the plugin loader and pre-session bootstrap have a stable identifier
	// to log against; it does NOT determine the actual session model.
	const model = "anthropic/claude-sonnet-4-5-20250929"
	daemonConfig, err := config.LoadDaemonConfig(Version)
	if err != nil {
		log.Printf("WARNING: Failed to load daemon config: %v", err)
		daemonConfig = &config.DaemonConfig{}
	}
	// Load the shared-secret socket auth token, if requested. -auth-token-path
	// is OPTIONAL: when unset, the daemon serves any caller that can connect
	// (legacy / single-user-host behaviour). When set, every incoming socket
	// message must carry a matching auth_token field. We fail-fast only on a
	// misconfigured path (file missing or empty) — silently running with no
	// auth despite the operator pointing at a file would defeat the purpose.
	if *authTokenPath != "" {
		raw, readErr := os.ReadFile(*authTokenPath)
		if readErr != nil {
			log.Fatalf("auth-token-path %q: %v", *authTokenPath, readErr)
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			log.Fatalf("auth-token-path %q is empty after trimming whitespace", *authTokenPath)
		}
		daemonConfig.AuthToken = token
		log.Printf("Socket auth: enabled (%d-byte token loaded from %s)", len(token), *authTokenPath)
	} else {
		log.Printf("Socket auth: not configured (no -auth-token-path) — daemon will accept any caller on %s", *socketPathFlag)
	}
	telemetry.Init(telemetry.Config{Version: Version, Mode: "daemon", Enabled: config.TelemetryEnabled()})
	defer telemetry.Shutdown()
	// Top-level crash handler: capture the panic as a PostHog exception and
	// flush synchronously (Shutdown is bounded by ShutdownTimeout) before the
	// process dies, then re-panic to preserve Go's crash output and exit code.
	// Registered after the Shutdown defer so it runs first on unwind; the
	// later Shutdown is a no-op (closeOnce). Only catches main-goroutine panics.
	defer func() {
		if r := recover(); r != nil {
			telemetry.TrackPanic("vixd.main", r, debug.Stack())
			telemetry.Shutdown()
			panic(r)
		}
	}()

	sessionID := uuid.New().String()

	cwd, _ := os.Getwd()
	pluginPaths := config.NewVixPaths("", config.HomeVixDir(), cwd)
	// Load the data-driven provider/model registry: embedded defaults overlaid
	// by ~/.vix and ./.vix providers.json. On error, log and fall back to the
	// embedded defaults (providers.Default() lazy-loads them).
	if err := providers.Configure(pluginPaths.Providers()); err != nil {
		log.Printf("[providers] using embedded defaults: %v", err)
	}
	pluginCfg := daemon.LoadPlugins(pluginPaths.Plugins(), Version, model)

	server := daemon.NewServer(*socketPathFlag, cred, sessionID, model, daemonConfig, pluginCfg)
	server.SetExitWithClients(*exitWithClients)
	// Background once-per-day update check. Best-effort: stores the result on
	// the server so sessions can surface it; never blocks startup.
	go func() {
		st := update.RunDailyCheck(Version, pluginPaths.StateFile(), update.LatestRelease)
		server.SetUpdateStatus(st.Current, st.Latest, st.URL, st.Method)
		if st.Latest != "" {
			log.Printf("update available: %s (current %s)", st.Latest, st.Current)
		}
	}()
	daemon.RegisterBuiltinHandlers(server)
	brain.RegisterBrainHandlers(func(cmd string, handler func(map[string]any) (map[string]any, error)) {
		server.RegisterHandler(cmd, handler)
	}, cred, ctx)
	daemon.RegisterToolHandlers(server)
	if *webPort > 0 && !*noMissionControl {
		go daemon.StartWebServer(ctx, server, *webPort)
	}
	if *pprofPort > 0 {
		go daemon.StartPprofServer(ctx, *pprofPort)
	}
	telemetry.TrackDaemonStarted(time.Since(startupT0).Milliseconds())
	if err := server.ListenAndServe(ctx); err != nil {
		log.Fatalf("Server error: %v", err)
	}
	server.Shutdown()
	log.Printf("vixd clean shutdown complete")
}
