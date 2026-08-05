package daemon

import (
	"bufio"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/hooks"
	"github.com/get-vix/vix/internal/daemon/jobs"
	"github.com/get-vix/vix/internal/daemon/mcp"
	"github.com/get-vix/vix/internal/protocol"
	"github.com/get-vix/vix/internal/whiteboard"
)

// HandlerFunc is the type for daemon request handlers.
type HandlerFunc func(data map[string]any) (map[string]any, error)

// ThreadInfo holds a snapshot of a live thread for external consumers.
type ThreadInfo struct {
	ID            string  `json:"id"`
	CWD           string  `json:"cwd"`
	Model         string  `json:"model,omitempty"`
	Title         string  `json:"title,omitempty"`
	Origin        string  `json:"origin,omitempty"`
	Unread        bool    `json:"unread,omitempty"`
	Attached      bool    `json:"attached"`
	InputTokens   int64   `json:"input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
	StartedAt     string  `json:"started_at"`      // RFC3339
	LastRequestAt *string `json:"last_request_at"` // RFC3339, null if no request yet
	ParentID      string  `json:"parent_id,omitempty"`
	ForkTurnIdx   int     `json:"fork_turn_idx,omitempty"`
}

// Server is the Unix socket daemon server with a handler registry.
type Server struct {
	mu       sync.RWMutex
	handlers map[string]HandlerFunc
	sockPath string
	accessDB *sql.DB // Access stats database (nil if init failed)
	threadID string  // Unique ID for this daemon thread

	// Agent thread support
	cred      config.Credential
	model     string
	plugins   PluginSource
	threads   map[string]*Thread
	threadMu  sync.Mutex
	serverCtx context.Context

	// User-level config directory (~/.vix/)
	homeVixDir string

	// webPort is the local web UI port (vixd's --web-port), or 0 when the web
	// UI is disabled. Used to build whiteboard links handed to clients via
	// event.thread_started. Set once at startup with SetWebPort.
	webPort int

	// cwd is vixd's own working directory, captured once at construction. Used
	// as the default working directory offered to web-UI job creation (the
	// `default_cwd` field of the WebSocket payload) so a created job runs where
	// the daemon was launched unless the user overrides it.
	cwd string

	// vixBin is the path to the vix CLI binary, resolved once at construction as
	// the sibling of the running vixd executable (falling back to "vix" on
	// PATH). Exposed to hooks via the context envelope so a hook can call back
	// into this daemon (e.g. `vix thread create`) without guessing the path.
	vixBin string

	// Shared-secret token validated on every incoming socket message. Loaded
	// once at daemon start from daz-secrets account vix/daemon-auth-token and
	// stored in memory only — never logged or copied into a subprocess
	// environment. When empty (the account is absent, or
	// in-process test embedding), the validation is skipped, so the daemon
	// behaves exactly as it did before the auth feature existed. Set the
	// flag (and have the client load the same file) only when the daemon
	// shares a host with untrusted local processes.
	authToken string

	// Web UI pub/sub
	subscribers  []chan struct{}
	subscriberMu sync.Mutex

	// Instance counting. Each vix process (TUI or headless) holds one
	// long-lived "instance.register" connection for its lifetime, so
	// instanceCount tracks how many vix processes are attached to this daemon.
	// Purely observability (web UI vitals, logging): the daemon runs until
	// signalled or told to stop, regardless of attached instances.
	instanceMu    sync.Mutex
	instanceCount int

	// instances holds the live instance control connections — one per vix
	// window, opened at launch and held for the process lifetime (see
	// handleInstance). Process-level events (threads_changed, jobs_changed,
	// quit) are pushed to each exactly once via BroadcastToInstances,
	// independent of any chat thread, so a draft window with no thread still
	// receives them. Guarded by instanceRegMu.
	instanceRegMu sync.Mutex
	instances     map[*instanceConn]struct{}

	// mcpAuthFlows holds in-progress MCP OAuth authorizations keyed by their
	// opaque state token, awaiting the browser redirect to the shared
	// mission-control callback route (/mcp/oauth/callback). Guarded by mcpAuthMu.
	mcpAuthMu    sync.Mutex
	mcpAuthFlows map[string]*mcp.AuthFlow

	// version is the daemon build version (vixd's main.Version). Threads from
	// clients with a different version are refused (see handleThread). Empty
	// in in-process test embeddings, which disables the gate.
	version string

	// cancel cancels serverCtx; set in ListenAndServe so internal triggers
	// (QuitAll — in-app update or `vix daemon stop`) can initiate a graceful
	// shutdown.
	cancel context.CancelFunc

	// jobScheduler is the scheduled-jobs engine, nil when disabled (feature
	// flag, VIX_DISABLE_JOBS, or no home directory). Set before ListenAndServe
	// via StartJobScheduler; the config watcher uses it for hot reload.
	jobScheduler *jobs.Scheduler

	// hookRegistry is the lifecycle-hooks engine, nil when disabled (feature
	// flag, VIX_DISABLE_HOOKS, or no home directory). Set before ListenAndServe
	// via EnableHooks; the config watcher uses it for hot reload.
	hookRegistry *hooks.Registry

	// Update status: the latest GitHub release seen by the once-per-day check,
	// versus the running daemon Version. Populated by a background goroutine in
	// vixd's main and read when emitting event.update_available at thread init.
	// Guarded by mu.
	updateCurrent string
	updateLatest  string // "" when up-to-date / check disabled / unreachable
	updateURL     string
	updateMethod  string

	// Tool backends resolved once at daemon start in RegisterToolHandlers.
	// *Effective names reflect PATH fallback (e.g. configured "fd" resolves to
	// "builtin" when fd is absent); *Configured records the requested backend
	// so the Settings tab can flag a fallback. Set once before any thread
	// starts, then read-only.
	grepBackendEffective  string
	grepBackendConfigured string
	globBackendEffective  string
	globBackendConfigured string
}

// SetUpdateStatus records the result of the daily release check so it can be
// emitted to every thread at init. Safe for concurrent use.
func (s *Server) SetUpdateStatus(current, latest, url, method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateCurrent = current
	s.updateLatest = latest
	s.updateURL = url
	s.updateMethod = method
}

// UpdateStatus returns the last recorded release-check result.
func (s *Server) UpdateStatus() (current, latest, url, method string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updateCurrent, s.updateLatest, s.updateURL, s.updateMethod
}

// BroadcastEvent pushes ev onto every live thread's event channel (best-effort,
// non-blocking). Used to reach all attached TUIs at once — e.g. the coordinated
// quit-all that follows an in-app update.
func (s *Server) BroadcastEvent(ev protocol.ThreadEvent) {
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	for _, sess := range s.threads {
		select {
		case sess.eventChan <- ev:
		default:
		}
	}
}

// instanceConn is one live instance control connection. Writes are serialized
// through sendCh, drained by a single writer goroutine (handleInstance), so
// concurrent BroadcastToInstances calls never interleave frames on the wire.
type instanceConn struct {
	conn   net.Conn
	sendCh chan []byte
}

// registerInstanceConn adds ic to the live-instance registry.
func (s *Server) registerInstanceConn(ic *instanceConn) {
	s.instanceRegMu.Lock()
	s.instances[ic] = struct{}{}
	s.instanceRegMu.Unlock()
}

// deregisterInstanceConn removes ic from the registry and closes its send
// channel so the writer goroutine returns. Idempotent: the close happens under
// the same lock that guards sends, so a concurrent BroadcastToInstances can
// never send on a closed channel.
func (s *Server) deregisterInstanceConn(ic *instanceConn) {
	s.instanceRegMu.Lock()
	defer s.instanceRegMu.Unlock()
	if _, ok := s.instances[ic]; ok {
		delete(s.instances, ic)
		close(ic.sendCh)
	}
}

// BroadcastToInstances pushes ev to every live instance control connection
// (best-effort, non-blocking, once per window). Process-level events
// (threads_changed, jobs_changed, quit) travel this path so they reach every
// window exactly once — including a launch-time draft that has no thread yet.
func (s *Server) BroadcastToInstances(ev protocol.ThreadEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		LogError("Marshal instance event error: %v", err)
		return
	}
	data = append(data, '\n')
	s.instanceRegMu.Lock()
	defer s.instanceRegMu.Unlock()
	for ic := range s.instances {
		select {
		case ic.sendCh <- data:
		default:
		}
	}
}

// QuitAll tells every attached vix instance to quit, then shuts the daemon
// down. Invoked after an in-app update so all old processes exit and the
// freshly-installed binaries take effect on relaunch (brew keep_alive restart
// or vix auto-spawn). The short delay lets the per-instance writer goroutines
// flush the quit event onto their sockets before the context is cancelled.
func (s *Server) QuitAll() {
	LogInfo("update: broadcasting quit to all instances, then shutting down")
	s.BroadcastToInstances(protocol.ThreadEvent{Type: "event.quit"})
	time.AfterFunc(500*time.Millisecond, func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
}

// resolveVixBin returns the path to the vix CLI binary, resolved as the sibling
// of the running vixd executable so hooks can call back into the daemon without
// relying on PATH. Falls back to "vix" when the sibling can't be determined or
// doesn't exist (e.g. in-process test embeddings).
func resolveVixBin() string {
	exe, err := os.Executable()
	if err != nil {
		return "vix"
	}
	cand := filepath.Join(filepath.Dir(exe), "vix")
	if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
		return cand
	}
	return "vix"
}

// NewServer creates a new daemon server.
func NewServer(sockPath string, cred config.Credential, threadID, model string, daemonConfig *config.DaemonConfig, plugins PluginSource) *Server {
	s := &Server{
		handlers:   make(map[string]HandlerFunc),
		sockPath:   sockPath,
		threadID:   threadID,
		cred:       cred,
		model:      model,
		plugins:    plugins,
		threads:    make(map[string]*Thread),
		instances:  make(map[*instanceConn]struct{}),
		homeVixDir: daemonConfig.HomeVixDir,
		authToken:  daemonConfig.AuthToken,
		vixBin:     resolveVixBin(),
	}
	if wd, err := os.Getwd(); err == nil {
		s.cwd = wd
	}

	// Set LLM log directory to ~/.vix/logs/
	if s.homeVixDir != "" {
		SetLLMLogDir(filepath.Join(s.homeVixDir, "logs"))
	}

	return s
}

// SetVersion records the daemon build version. Threads started by clients
// whose version differs are refused (hard gate, see handleThread). Must be
// called before ListenAndServe. Leaving it unset (in-process test embeddings)
// disables the gate.
func (s *Server) SetVersion(v string) {
	s.version = v
}

// Version returns the daemon build version recorded via SetVersion.
func (s *Server) Version() string {
	return s.version
}

// SetWebPort records the local web UI port (vixd's --web-port), or 0 when the
// web UI is disabled. Used to build whiteboard links handed to clients via
// event.thread_started. Call before ListenAndServe.
func (s *Server) SetWebPort(p int) {
	s.webPort = p
}

// instanceConnected records a newly attached vix instance.
func (s *Server) instanceConnected() {
	s.instanceMu.Lock()
	s.instanceCount++
	n := s.instanceCount
	s.instanceMu.Unlock()
	LogInfo("vix instance connected (now %d attached)", n)
	s.notifySubscribers()
}

// instanceDisconnected records a detached vix instance.
func (s *Server) instanceDisconnected() {
	s.instanceMu.Lock()
	if s.instanceCount > 0 {
		s.instanceCount--
	}
	n := s.instanceCount
	s.instanceMu.Unlock()
	LogInfo("vix instance disconnected (now %d attached)", n)
	s.notifySubscribers()
}

// LogAccess logs a tool access event. Safe to call even if accessDB is nil.
func (s *Server) LogAccess(toolName string, params map[string]any) {
	if s.accessDB == nil {
		return
	}
	logToolAccess(s.accessDB, s.threadID, toolName, params)
}

// RegisterHandler registers a handler for the given command.
func (s *Server) RegisterHandler(command string, handler HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[command] = handler
	LogInfo("Registered handler: %s", command)
}

// GetHandler returns the handler for the given command, or nil.
func (s *Server) GetHandler(command string) HandlerFunc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handlers[command]
}

// authOK reports whether the supplied token authenticates the caller.
// Empty s.authToken disables the check entirely — that's the mode when the
// daz-secrets account is absent and for in-process test embeddings.
// Comparison is constant-time so a network attacker can't time-leak the
// token byte-by-byte; the AF_UNIX socket itself isn't exposed to the
// network, but the constant-time path is cheap and removes one less thing
// to think about if the transport ever changes.
func (s *Server) authOK(token string) bool {
	if s.authToken == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.authToken)) == 1
}

// EnableJobScheduler constructs the scheduled-jobs engine over the global job
// store (~/.vix/jobs). Must be called before ListenAndServe, which starts the
// timer loop; the config watcher hot-reloads the spec directory. No-op when
// the home directory is unavailable.
func (s *Server) EnableJobScheduler() {
	paths := config.NewVixPaths("", s.homeVixDir, "")
	dir := paths.Jobs()
	if dir == "" {
		return
	}
	// First enable (no jobs directory yet): seed the default heartbeat job and
	// its whiteboard stub. One-time — users may edit or disable both freely.
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			LogError("jobs: cannot create %s: %v", dir, err)
			return
		}
		seedDefaultHeartbeat(dir, paths.HeartbeatMD())
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		LogError("jobs: cannot create %s: %v", dir, err)
		return
	}
	notify := func(eventType string, data any) {
		ev := protocol.ThreadEvent{Type: eventType, Data: data}
		// Process-level events go to the instance control channels (once per
		// window); thread-scoped events (job_run/job_done status lines) stay on
		// the per-thread fan-out.
		switch eventType {
		case "event.threads_changed", "event.jobs_changed":
			s.BroadcastToInstances(ev)
		default:
			s.BroadcastEvent(ev)
		}
		s.notifySubscribers()
	}
	logger := jobRunLogger{dir: s.jobsLogDir}
	s.jobScheduler = jobs.NewScheduler(jobs.NewStore(dir), s.JobRunner(), notify, logger, config.JobsMaxConcurrentRuns())
}

// EnableHooks builds the lifecycle-hooks registry from ~/.vix/hooks. Safe to
// call once before ListenAndServe; the config watcher hot-reloads the spec
// directory. No-op when the home directory is unavailable.
func (s *Server) EnableHooks() {
	paths := config.NewVixPaths("", s.homeVixDir, "")
	dir := paths.Hooks()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		LogError("hooks: cannot create %s: %v", dir, err)
		return
	}
	// Seed the shipped default feedback hook once, tracked by a sentinel file
	// (not directory existence) so the hook's own state dir can be pre-created
	// without suppressing the seed, and so it never resurrects after the user
	// edits/disables/deletes it. Skipped on an auth-enabled daemon, where the
	// feedback hook's `vix thread create` callback can't present the secret.
	if s.authToken == "" {
		sentinel := filepath.Join(dir, feedbackSeedSentinel)
		if _, err := os.Stat(sentinel); os.IsNotExist(err) {
			seedDefaultFeedbackHook(dir)
		}
	}
	s.hookRegistry = hooks.NewRegistry(hooks.NewStore(dir))
}

// seedDefaultHeartbeat writes the shipped heartbeat job spec and the
// effectively-empty heartbeat.md stub. The stub is headings/comments only, so
// the job fires, skips in microseconds, and spends zero tokens until the user
// writes a real task into the file.
func seedDefaultHeartbeat(jobsDir, heartbeatPath string) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	job := jobs.Spec{
		ID:      "heartbeat",
		Name:    "Heartbeat",
		Enabled: true,
		Trigger: jobs.Trigger{Type: "cron", Expr: "*/30 9-19 * * *"},
		Prompt: "$(file:.vix/jobs/heartbeat/heartbeat.md)\n\n" +
			"Follow it strictly. Do not infer tasks from prior conversations. " +
			"If nothing needs attention, reply HEARTBEAT_OK.",
		WorkflowID:  "heartbeat",
		CWD:         home,
		SkipIfEmpty: true,
		Timeout:     "10m",
		CreatedBy:   "vix",
	}
	data, _ := json.MarshalIndent(job, "", "  ")
	heartbeatDir := filepath.Join(jobsDir, "heartbeat")
	if err := os.MkdirAll(heartbeatDir, 0o755); err != nil {
		LogError("jobs: cannot create %s: %v", heartbeatDir, err)
		return
	}
	target := filepath.Join(heartbeatDir, "job.json")
	if err := os.WriteFile(target, append(data, '\n'), 0o644); err != nil {
		LogError("jobs: seed heartbeat job: %v", err)
	} else {
		LogInfo("jobs: seeded default heartbeat job at %s", target)
	}

	if heartbeatPath == "" {
		return
	}
	if _, err := os.Stat(heartbeatPath); err == nil {
		return // user already has one
	}
	stub := `# Heartbeat

<!--
vix reads this file on the "heartbeat" job's schedule (every 30 minutes,
9:00-19:59 by default — edit ~/.vix/jobs/heartbeat/job.json to change it).

While this file only contains headings and comments, the check is skipped
BEFORE any model call: zero tokens spent. Write a task below to put the
heartbeat to work, for example:

- Check git status in ~/Developer/myproject; if there are uncommitted
  changes older than a day, summarise them.
- Read the last 20 lines of ~/logs/backup.log and alert me if the most
  recent backup failed.

When a check finds nothing to report, the agent answers HEARTBEAT_OK and the
run leaves no trace. Anything else shows up in the Threads tab under
"Vix-initiated".
-->
`
	if err := os.WriteFile(heartbeatPath, []byte(stub), 0o644); err != nil {
		LogError("jobs: seed heartbeat.md: %v", err)
	} else {
		LogInfo("jobs: seeded heartbeat whiteboard at %s", heartbeatPath)
	}
}

// daemonAlreadyListening reports whether a process is actively accepting
// connections on sockPath (a live daemon), as opposed to a stale socket file
// left behind by a crashed one. A successful dial proves a listener is up; a
// stale file refuses the connection. Auth is irrelevant here — we never send a
// request, only test connectivity.
func daemonAlreadyListening(sockPath string) bool {
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ListenAndServe starts the Unix socket server and blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	// Wrap the incoming context so internal triggers (QuitAll) can cancel the
	// server the same way an external signal does.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.cancel = cancel
	s.serverCtx = ctx

	// Refuse to hijack a socket a live daemon already owns. Unconditionally
	// removing it would orphan the running daemon's in-flight threads and
	// silently point new client dials at this (possibly different-store)
	// process — the exact failure where threads vanish from the list and new
	// threads report "daemon is not responding".
	if daemonAlreadyListening(s.sockPath) {
		return fmt.Errorf("another vixd is already listening on %s — stop it first (vix daemon stop) or use a different --socket-path", s.sockPath)
	}

	// Remove a stale socket file left by a crashed daemon (no live listener).
	if _, err := os.Stat(s.sockPath); err == nil {
		os.Remove(s.sockPath)
	}

	listener, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() {
		listener.Close()
		os.Remove(s.sockPath)
	}()

	LogInfo("Daemon listening on %s", s.sockPath)

	// Hot-reload workflow.json / languages.json on save.
	s.startConfigWatcher()

	// Scheduled-jobs engine (when enabled): timer loop with startup catch-up.
	if s.jobScheduler != nil {
		go s.jobScheduler.Start(ctx)
		LogInfo("jobs: scheduler started")
	}

	// Run-log retention: prune job/hook run logs older than the configured
	// number of days at startup, then once a day while the daemon is up.
	go s.runLogSweepLoop(ctx)

	// One-time migration of the pre-rename global store (~/.vix/sessions ->
	// ~/.vix/threads). Runs synchronously before the accept loop so no
	// connection reads/writes the store mid-move. Config-dir override stores are
	// not touched, mirroring the closed-record trim below.
	migrateLegacyThreadsDir(config.NewVixPaths("", config.HomeVixDir(), ""))

	// One-shot stale trim of closed thread records in the default global
	// store (~/.vix/threads/closed). Retention comes from settings.json
	// (threads.closed_retention_minutes, default one week, 0 = never).
	// Config-dir override stores are not touched.
	go func() {
		mins := config.ClosedThreadRetentionMinutes()
		paths := config.NewVixPaths("", config.HomeVixDir(), "")
		trimStaleClosedThreads(paths, time.Duration(mins)*time.Minute)
	}()

	// Accept loop with context cancellation
	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				LogInfo("Daemon shutting down.")
				return nil
			default:
				LogError("Accept error: %v", err)
				continue
			}
		}
		go s.handleClient(conn)
	}
}

func (s *Server) handleClient(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), protocol.MaxMessageSize)
	if !scanner.Scan() {
		s.writeError(conn, "empty request")
		return
	}
	line := scanner.Bytes()

	// Check if this is a thread.start message — upgrade to persistent thread
	var cmd protocol.ThreadCommand
	if err := json.Unmarshal(line, &cmd); err == nil && cmd.Type == "thread.start" {
		// Auth gate: drop the connection before any thread resources are
		// allocated. handleThread's per-message reader-loop check covers
		// follow-ups; this one covers the initial start.
		if !s.authOK(cmd.AuthToken) {
			LogError("Thread start rejected: auth_token mismatch")
			s.writeError(conn, "auth")
			return
		}
		s.handleThread(conn, scanner, cmd)
		return
	}

	// instance.register: a vix process holds this connection open for its whole
	// lifetime so the daemon can count attached instances independently of
	// threads. Block until the connection closes, then decrement.
	if cmd.Type == "instance.register" {
		if !s.authOK(cmd.AuthToken) {
			LogError("Instance register rejected: auth_token mismatch")
			s.writeError(conn, "auth")
			return
		}
		s.handleInstance(conn, scanner)
		return
	}

	var request map[string]any
	if err := json.Unmarshal(line, &request); err != nil {
		s.writeError(conn, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	// Auth gate for one-shot RPCs (ping, tool.*, etc.). Same token shape as
	// ThreadCommand.AuthToken; flat field on the request map.
	reqToken, _ := request["auth_token"].(string)
	if !s.authOK(reqToken) {
		LogError("RPC rejected: auth_token mismatch")
		s.writeError(conn, "auth")
		return
	}

	// Route by action or command field
	action, _ := request["action"].(string)
	if action == "" {
		action, _ = request["command"].(string)
	}

	LogInfo("Received action=%s", action)

	handler := s.GetHandler(action)
	if handler == nil {
		s.writeResponse(conn, map[string]any{
			"status":  "error",
			"message": fmt.Sprintf("unknown action: %s", action),
		})
		return
	}

	response, err := handler(request)
	if err != nil {
		LogError("Handler error for %s: %v", action, err)
		s.writeResponse(conn, map[string]any{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	LogInfo("Completed action=%s status=%v", action, response["status"])
	s.writeResponse(conn, response)
}

// handleInstance holds an instance.register connection open for the lifetime of
// a vix process. It counts the instance (web-UI vitals) and registers a
// bidirectional control channel: process-level events (threads_changed,
// jobs_changed, quit) are pushed to it via BroadcastToInstances, one serialized
// writer goroutine per connection. It blocks reading until the peer closes the
// connection (clean exit or process death both deliver EOF on a local Unix
// socket), then deregisters and records the disconnect.
func (s *Server) handleInstance(conn net.Conn, scanner *bufio.Scanner) {
	s.instanceConnected()
	defer s.instanceDisconnected()

	ic := &instanceConn{conn: conn, sendCh: make(chan []byte, 32)}
	s.registerInstanceConn(ic)

	// Writer goroutine: the sole writer to this connection, so frames pushed by
	// concurrent BroadcastToInstances calls never interleave. Exits when the
	// send channel is closed by deregisterInstanceConn.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for data := range ic.sendCh {
			conn.Write(data)
		}
	}()

	// Drain anything the client sends (it normally sends nothing). The loop
	// exits when the connection closes.
	for scanner.Scan() {
	}

	// Deregister (idempotent) closes the send channel, unblocking the writer.
	s.deregisterInstanceConn(ic)
	<-writerDone
}

// handleThread upgrades a connection to a persistent bidirectional thread.
func (s *Server) handleThread(conn net.Conn, scanner *bufio.Scanner, startCmd protocol.ThreadCommand) {
	// Parse thread start data
	var startData protocol.ThreadStartData
	json.Unmarshal(startCmd.Data, &startData)

	// Version gate: a long-lived daemon must never serve a client from a
	// different build. Exact match required; an empty daemon version means an
	// in-process test embedding (gate off). Old clients that predate the gate
	// send no ClientVersion and are refused like any other mismatch.
	if s.version != "" && startData.ClientVersion != s.version {
		LogError("Thread refused: client version %q != daemon version %q", startData.ClientVersion, s.version)
		s.writeEvent(conn, protocol.ThreadEvent{
			Type: "event.error",
			Data: protocol.EventError{
				Message: fmt.Sprintf("vix %s cannot talk to vixd %s. Restart the daemon: vix daemon stop && vix daemon start", startData.ClientVersion, s.version),
				Code:    "version_mismatch",
			},
		})
		return
	}

	cwd := startData.CWD
	if cwd == "" {
		cwd2, _ := os.Getwd()
		cwd = cwd2
	}

	model := startData.Model
	if model == "" {
		model = s.model
	}

	// The thread resolves its own LLM client from the authoritative model
	// (chat-agent frontmatter, falling back to this initial spec) in initBrain.
	// We pass nil here; if no credential is available the thread enters its
	// unconfigured state rather than fabricating a doomed client.
	var llmClient LLM

	// Attach: resume a persisted thread by ID instead of minting a new one.
	// Load the record up front so we can reuse its ID and seed history; a
	// missing record is reported with a machine-readable code so the client can
	// orphan the thread (offer /copy) rather than retry forever. Only open/
	// records are attachable — a record in closed/ was explicitly closed by the
	// user and must not be resurrected by a stale reconnect.
	var attachRec *threadRecord
	if startData.AttachThreadID != "" {
		p := config.NewVixPaths(startData.ConfigDir, s.homeVixDir, cwd)
		rec, found, err := loadOpenThreadRecord(p, startData.AttachThreadID)
		if err != nil {
			LogError("attach: failed to load thread %s: %v", startData.AttachThreadID, err)
		}
		if !found {
			s.writeEvent(conn, protocol.ThreadEvent{
				Type: "event.error",
				Data: protocol.EventError{
					Message: "thread not found",
					Code:    "thread_not_found",
				},
			})
			return
		}
		attachRec = &rec
	}

	threadID := generateThreadID()
	if attachRec != nil {
		threadID = attachRec.ID
	}
	thread := NewThread(threadID, s, llmClient, model, cwd, startData.ConfigDir, startData.ForceInit, startData.EnableAutomaticWritePermission, startData.EnableAutomaticDirectoryAccess, startData.Headless, s.serverCtx)

	if attachRec != nil {
		thread.seedFromRecord(attachRec)
	}

	// Seed conversation history from a forked thread if requested.
	// Must be done before thread.Run() starts processing commands.
	if startData.ForkThreadID != "" {
		s.threadMu.Lock()
		forkSrc := s.threads[startData.ForkThreadID]
		s.threadMu.Unlock()
		if forkSrc != nil {
			if msgs := forkSrc.snapshotMessagesForFork(startData.ForkTurnIdx); len(msgs) > 0 {
				thread.messages = msgs
				// Rebuild the read-gate set from the forked history so the new
				// thread can edit files the parent had already read.
				thread.rebuildReadFilesFromHistory(msgs)
				// Rebuild the fork snapshots so this seeded thread is itself
				// forkable/trimmable — otherwise a duplicate-of-a-duplicate would
				// find no history to copy and start empty.
				thread.turnSnapshots = rebuildTurnSnapshots(msgs)
			}
		}
		thread.parentID = startData.ForkThreadID
		thread.forkTurnIdx = startData.ForkTurnIdx
	}

	// Initialize access stats database for this thread.
	// The path depends on the thread's config-dir override, if any, so we
	// resolve it via thread.paths after construction.
	// Guard with the server mutex so concurrent threads for the same project
	// share one connection instead of racing to overwrite s.accessDB.
	s.mu.Lock()
	if s.accessDB == nil {
		db, err := initAccessStatsDB(thread.paths.AccessStatsDB())
		if err != nil {
			LogError("Failed to initialize access stats DB (continuing without stats): %v", err)
		} else {
			s.accessDB = db
		}
	}
	s.mu.Unlock()
	// Register the thread, enforcing exclusive ownership: a persisted thread
	// may be attached by only one connection at a time. If another live
	// connection already owns this ID (e.g. a second vix instance, or a stale
	// reconnect), refuse with a machine-readable code so the client can react
	// (retry, restore something else, or surface a message) instead of forking a
	// divergent in-memory copy that races to persist. The check and the insert
	// are done under the same lock so two concurrent attaches can't both win.
	// Ownership is released automatically when the owning connection drops (the
	// delete(s.threads, …) in the teardown below).
	s.threadMu.Lock()
	if attachRec != nil {
		if _, live := s.threads[threadID]; live {
			s.threadMu.Unlock()
			LogInfo("Attach refused: thread %s is already open in another connection", threadID)
			s.writeEvent(conn, protocol.ThreadEvent{
				Type: "event.error",
				Data: protocol.EventError{
					Message: "thread is already open in another window",
					Code:    "thread_busy",
				},
			})
			return
		}
	}
	s.threads[threadID] = thread
	s.threadMu.Unlock()
	s.notifySubscribers()

	// Persist the freshly-created (or attached) thread to open/ so it survives
	// a crash and is reopened on next launch. Attached threads are already on
	// disk; this is a no-op-equivalent rewrite. Forked/duplicated threads carry
	// seeded history and are persisted too.
	//
	// A brand-new thread with NO messages is deliberately NOT persisted here:
	// doing so leaves a ghost empty record if the connection drops before the
	// first message ever arrives (the user quits, or a turn is cancelled in the
	// connect→first-turn window). The thread's first turn persists it
	// (s.persist() after the turn), so nothing with real content is ever lost.
	if attachRec != nil || len(thread.messages) > 0 {
		thread.persist()
	}

	LogInfo("Thread %s started (cwd=%s, model=%s)", threadID, cwd, model)

	// Send thread started event
	s.writeEvent(conn, protocol.ThreadEvent{
		Type: "event.thread_started",
		Data: protocol.EventThreadStarted{
			ThreadID:       threadID,
			StartedAt:      thread.startTime.Format(time.RFC3339),
			ParentID:       thread.parentID,
			ForkTurnIdx:    thread.forkTurnIdx,
			WhiteboardBase: whiteboard.WhiteboardBase(s.webPort),
		},
	})

	// Writer goroutine: reads from thread.eventChan, writes NDJSON to socket
	writerDone := make(chan struct{})
	go func() {
		defer func() {
			LogInfo("Thread %s: writer exited", threadID)
			close(writerDone)
		}()
		for {
			select {
			case event, ok := <-thread.eventChan:
				if !ok {
					return
				}
				s.writeEvent(conn, event)
			case <-thread.ctx.Done():
				// Drain remaining events
				for {
					select {
					case event := <-thread.eventChan:
						s.writeEvent(conn, event)
					default:
						return
					}
				}
			}
		}
	}()

	// Watchdog: close the conn when the thread context is canceled so the
	// reader goroutine (blocked on scanner.Scan()) can unblock. Without this,
	// a recovered panic inside thread.Run() would leak the reader and hang
	// handleThread forever on <-readerDone.
	go func() {
		<-thread.ctx.Done()
		conn.Close()
	}()

	// Reader goroutine: reads NDJSON from socket, feeds thread.commandChan
	readerDone := make(chan struct{})
	go func() {
		defer func() {
			LogInfo("Thread %s: reader exited (scanner.Err=%v)", threadID, scanner.Err())
			close(readerDone)
		}()
		for scanner.Scan() {
			var cmd protocol.ThreadCommand
			if err := json.Unmarshal(scanner.Bytes(), &cmd); err != nil {
				LogError("Thread %s: invalid command JSON: %v", threadID, err)
				continue
			}

			// Per-message auth: a connection that authenticated for
			// thread.start cannot then send unauthenticated follow-ups.
			// Mismatch → close the connection (and the thread); the
			// watchdog at line ~275 already cleans up on conn close.
			if !s.authOK(cmd.AuthToken) {
				LogError("Thread %s: command auth_token mismatch (type=%s) — closing", threadID, cmd.Type)
				conn.Close()
				return
			}

			if cmd.Type == "thread.cancel" {
				// Cancel the active stream immediately
				if thread.cancelStream != nil {
					thread.cancelStream()
				}
				// Cancel an active plan/workflow
				if thread.planCancel != nil {
					thread.planCancel()
				}
				// Cancel any in-flight background subagents
				thread.backgroundTasks.CancelAll()
				// Reap any detached bash-tool jobs (`background: true`).
				thread.bashJobs.KillAll()
			}

			if cmd.Type == "thread.workflow_message" {
				var msgData protocol.ThreadWorkflowMessageData
				json.Unmarshal(cmd.Data, &msgData)
				if msgData.Text != "" {
					// Non-blocking send: drop an older pending message if the channel is full
					select {
					case thread.workflowMsgChan <- msgData.Text:
					default:
						// Replace the existing pending message with the newer one
						select {
						case <-thread.workflowMsgChan:
						default:
						}
						thread.workflowMsgChan <- msgData.Text
					}
				}
				// Do not forward to commandChan — the workflow loop polls workflowMsgChan directly
				continue
			}

			if cmd.Type == "update.quit" {
				// An in-app update finished installing in the initiating TUI.
				// Tell every attached instance to quit and shut the daemon
				// down so the new binaries take effect on relaunch.
				s.QuitAll()
				return
			}

			select {
			case thread.commandChan <- cmd:
			case <-thread.ctx.Done():
				return
			}

			if cmd.Type == "thread.close" {
				return
			}
		}
		// Socket closed — cancel thread
		thread.cancel()
	}()

	// Run the agent loop (blocking)
	thread.Run()

	// Wait for reader/writer to finish
	thread.cancel()
	<-readerDone
	<-writerDone

	// Reap any detached bash-tool jobs before we drop the thread — otherwise
	// a leaked `john` / `cargo build` would keep chewing CPU until the
	// container dies. Mirrors the Ctrl-C branch above.
	//
	// Opt-out for hosts that deliberately outlive the vix client and rely on
	// background jobs (e.g. a pypi-server or web server) staying alive for a
	// post-agent verifier. The Ctrl-C branch above still reaps on cancel —
	// this only affects clean thread close.
	if os.Getenv("VIX_KEEP_BG_ON_THREAD_END") != "1" {
		thread.bashJobs.KillAll()
	}

	// Remove thread from map
	s.threadMu.Lock()
	delete(s.threads, threadID)
	s.threadMu.Unlock()
	s.notifySubscribers()

	// An explicit user close (the "x" action sends thread.close) moves the
	// record open/ -> closed/ so it is not reopened on next launch. A bare
	// disconnect leaves it in open/ so the TUI restores it next run. Empty
	// conversations (no turn ever happened) are deleted outright — there is
	// nothing worth archiving.
	if thread.closedByUser {
		thread.mu.Lock()
		empty := len(thread.messages) == 0
		thread.mu.Unlock()
		if empty {
			if err := deleteThreadRecord(thread.paths, threadID); err != nil {
				LogError("close thread %s: delete empty record failed: %v", threadID, err)
			}
		} else if err := moveThreadToClosed(thread.paths, threadID); err != nil {
			LogError("close thread %s: move to closed failed: %v", threadID, err)
		}
	}

	LogInfo("Thread %s ended (run returned, reader done, writer done)", threadID)
}

func (s *Server) writeEvent(conn net.Conn, event protocol.ThreadEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		LogError("Marshal event error: %v", err)
		return
	}
	data = append(data, '\n')
	conn.Write(data)
}

func (s *Server) writeResponse(conn net.Conn, resp map[string]any) {
	data, err := json.Marshal(resp)
	if err != nil {
		LogError("Marshal response error: %v", err)
		return
	}
	data = append(data, '\n')
	conn.Write(data)
}

func (s *Server) writeError(conn net.Conn, msg string) {
	s.writeResponse(conn, map[string]any{
		"status":  "error",
		"message": msg,
	})
}

// Subscribe registers a new web-UI subscriber and returns a notification channel.
func (s *Server) Subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	s.subscriberMu.Lock()
	s.subscribers = append(s.subscribers, ch)
	s.subscriberMu.Unlock()
	return ch
}

// Unsubscribe removes a previously registered subscriber channel.
func (s *Server) Unsubscribe(ch chan struct{}) {
	s.subscriberMu.Lock()
	defer s.subscriberMu.Unlock()
	for i, c := range s.subscribers {
		if c == ch {
			s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
			return
		}
	}
}

// notifySubscribers sends a non-blocking ping to every registered subscriber.
func (s *Server) notifySubscribers() {
	s.subscriberMu.Lock()
	defer s.subscriberMu.Unlock()
	for _, ch := range s.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Threads returns a snapshot of live threads plus persisted open threads.
func (s *Server) Threads() []ThreadInfo {
	s.threadMu.Lock()
	infos := make([]ThreadInfo, 0, len(s.threads))
	liveIDs := make(map[string]struct{}, len(s.threads))
	for _, sess := range s.threads {
		info := ThreadInfo{
			ID:           sess.id,
			CWD:          sess.cwd,
			Model:        sess.model,
			Title:        sess.title,
			Origin:       sess.origin,
			Unread:       sess.unread,
			Attached:     true,
			InputTokens:  sess.totalInputTokens,
			OutputTokens: sess.totalOutputTokens,
			StartedAt:    sess.startTime.Format(time.RFC3339),
			ParentID:     sess.parentID,
			ForkTurnIdx:  sess.forkTurnIdx,
		}
		if !sess.lastRequestAt.IsZero() {
			t := sess.lastRequestAt.Format(time.RFC3339)
			info.LastRequestAt = &t
		}
		infos = append(infos, info)
		liveIDs[sess.id] = struct{}{}
	}
	s.threadMu.Unlock()

	paths := config.NewVixPaths("", s.homeVixDir, "")
	for _, rec := range listOpenThreadRecords(paths) {
		if _, live := liveIDs[rec.ID]; live {
			continue
		}
		info := ThreadInfo{
			ID:           rec.ID,
			CWD:          rec.CWD,
			Model:        rec.Model,
			Title:        rec.Title,
			Origin:       rec.Origin,
			Unread:       rec.Unread,
			InputTokens:  rec.TotalInputTokens,
			OutputTokens: rec.TotalOutputTokens,
			ParentID:     rec.ParentID,
			ForkTurnIdx:  rec.ForkTurnIdx,
		}
		if !rec.StartedAt.IsZero() {
			info.StartedAt = rec.StartedAt.Format(time.RFC3339)
		}
		if !rec.LastRequestAt.IsZero() {
			t := rec.LastRequestAt.Format(time.RFC3339)
			info.LastRequestAt = &t
		}
		infos = append(infos, info)
	}

	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].StartedAt < infos[j].StartedAt
	})
	return infos
}

// Jobs returns a snapshot of the scheduled jobs for external consumers (the web
// UI). Empty when the scheduler is disabled (no home directory / feature off).
func (s *Server) Jobs() []jobs.JobSnapshot {
	if s.jobScheduler == nil {
		return []jobs.JobSnapshot{}
	}
	return s.jobScheduler.Snapshot()
}

// Hooks returns a snapshot of the lifecycle hooks for external consumers (the
// web UI). Empty when the hooks engine is disabled (no home directory / feature
// off).
func (s *Server) Hooks() []hooks.HookSnapshot {
	if s.hookRegistry == nil {
		return []hooks.HookSnapshot{}
	}
	return s.hookRegistry.Snapshot()
}

// JobSummaries projects the scheduled jobs into the lightweight wire form
// consumed by the TUI's Jobs & Triggers tab (job.list RPC).
func (s *Server) JobSummaries() []protocol.JobSummary {
	snaps := s.Jobs()
	out := make([]protocol.JobSummary, 0, len(snaps))
	for _, j := range snaps {
		sum := protocol.JobSummary{
			ID:          j.ID,
			Name:        j.Name,
			Enabled:     j.Enabled,
			TriggerType: j.Trigger.Type,
			Schedule:    jobScheduleLabel(j.Trigger),
			LastStatus:  j.LastStatus,
			Running:     j.Running,
			CreatedBy:   j.CreatedBy,
		}
		if !j.NextRunAt.IsZero() {
			sum.NextRunAt = j.NextRunAt.Format(time.RFC3339)
		}
		if !j.LastRunAt.IsZero() {
			sum.LastRunAt = j.LastRunAt.Format(time.RFC3339)
		}
		out = append(out, sum)
	}
	return out
}

// jobScheduleLabel renders a trigger as a short human-readable schedule string:
// the cron expression (with timezone when set) for recurring jobs, or
// "at <time>" for one-shots.
func jobScheduleLabel(t jobs.Trigger) string {
	switch t.Type {
	case "cron":
		if t.TZ != "" {
			return t.Expr + " (" + t.TZ + ")"
		}
		return t.Expr
	case "at":
		return "at " + t.Time
	}
	return t.Type
}

// HookSummaries projects the lifecycle hooks into the lightweight wire form
// consumed by the TUI's Jobs & Triggers tab (hook.list RPC). LastStatus is taken
// from the most recent run record.
func (s *Server) HookSummaries() []protocol.HookSummary {
	snaps := s.Hooks()
	out := make([]protocol.HookSummary, 0, len(snaps))
	for _, h := range snaps {
		sum := protocol.HookSummary{
			ID:        h.ID,
			Name:      h.Name,
			Enabled:   h.Enabled,
			Event:     h.Trigger.Event,
			Matcher:   h.Trigger.Matcher,
			Mode:      h.Mode,
			CreatedBy: h.CreatedBy,
		}
		if !h.LastFiredAt.IsZero() {
			sum.LastFiredAt = h.LastFiredAt.Format(time.RFC3339)
		}
		if n := len(h.RecentRuns); n > 0 {
			sum.LastStatus = h.RecentRuns[n-1].Status
		}
		out = append(out, sum)
	}
	return out
}

// SetJobEnabled enables or disables a scheduled job by id, persisting the change
// to its job.json and notifying attached clients. Errors when the jobs engine is
// disabled or the spec cannot be patched.
func (s *Server) SetJobEnabled(id string, enabled bool) error {
	if s.jobScheduler == nil {
		return fmt.Errorf("jobs engine is disabled")
	}
	if err := s.jobScheduler.SetEnabled(id, enabled); err != nil {
		return err
	}
	s.broadcastJobsChanged()
	return nil
}

// SetHookEnabled enables or disables a lifecycle hook by id, persisting the
// change to its hook.json and notifying attached clients. Errors when the hooks
// engine is disabled or the spec cannot be patched.
func (s *Server) SetHookEnabled(id string, enabled bool) error {
	if s.hookRegistry == nil {
		return fmt.Errorf("hooks engine is disabled")
	}
	if err := s.hookRegistry.SetEnabled(id, enabled); err != nil {
		return err
	}
	s.broadcastJobsChanged()
	return nil
}

// DefaultCWD returns vixd's own working directory, offered to the web UI as the
// default working directory for newly created jobs.
func (s *Server) DefaultCWD() string {
	return s.cwd
}

// CreateJob persists and schedules a new job from the web UI. It assigns a
// unique id derived from the job name when the spec doesn't carry one, then
// validates + writes via the scheduler and notifies web subscribers so the Jobs
// tab refreshes. Returns the assigned id.
func (s *Server) CreateJob(spec jobs.Spec) (string, error) {
	if s.jobScheduler == nil {
		return "", fmt.Errorf("jobs engine is disabled")
	}
	if spec.ID == "" {
		base := spec.Name
		if base == "" {
			base = "job"
		}
		spec.ID = s.jobScheduler.UniqueID(base)
	}
	if err := s.jobScheduler.CreateJob(spec); err != nil {
		return "", err
	}
	s.notifySubscribers()
	return spec.ID, nil
}

// RunJob fires the job with the given id immediately, out of band from the
// schedule, mirroring `vix job run <id>`. It generates the run's thread id up
// front and returns it once the run has been accepted; the run itself proceeds
// in the background (its outcome lands under "Vix-initiated" threads and the
// run log). Errors surface synchronously for an unknown id, a run already in
// flight, or a disabled jobs engine.
func (s *Server) RunJob(id string) (string, error) {
	if s.jobScheduler == nil {
		return "", fmt.Errorf("jobs engine is disabled")
	}
	runID := generateThreadID()
	if err := s.jobScheduler.RunNow(s.serverCtx, id, runID); err != nil {
		return "", err
	}
	return runID, nil
}

func (s *Server) getThread(id string) *Thread {
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	return s.threads[id]
}

// threadForWebCall returns a live thread when one is attached, otherwise it
// restores an open persisted thread into a short-lived headless thread for
// web-only operations such as whiteboard code exploration.
func (s *Server) threadForWebCall(id string) (*Thread, func(), error) {
	if sess := s.getThread(id); sess != nil {
		return sess, nil, nil
	}

	paths := config.NewVixPaths("", s.homeVixDir, "")
	rec, found, err := loadOpenThreadRecord(paths, id)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, nil
	}

	cwd := rec.CWD
	if cwd == "" {
		cwd = s.cwd
	}
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}

	model := rec.Model
	if model == "" {
		model = s.model
	}

	parentCtx := s.serverCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	sess := NewThread(rec.ID, s, nil, model, cwd, "", false, false, false, true, parentCtx)
	sess.seedFromRecord(&rec)

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-sess.eventChan:
			case <-sess.ctx.Done():
				return
			}
		}
	}()

	sess.initBrain()

	cleanup := func() {
		sess.cancel()
		sess.closeThinkingLog()
		if sess.mcpPool != nil {
			sess.mcpPool.Shutdown()
		}
		select {
		case <-drained:
		case <-time.After(time.Second):
		}
	}

	return sess, cleanup, nil
}

// Shutdown gracefully closes all server resources.
func (s *Server) Shutdown() {
	if s.accessDB != nil {
		if err := s.accessDB.Close(); err != nil {
			LogError("Error closing access stats DB: %v", err)
		} else {
			LogInfo("Access stats database closed")
		}
	}
}
