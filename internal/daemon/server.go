package daemon

import (
	"bufio"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/protocol"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HandlerFunc is the type for daemon request handlers.
type HandlerFunc func(data map[string]any) (map[string]any, error)

// SessionInfo holds a snapshot of a live session for external consumers.
type SessionInfo struct {
	ID            string  `json:"id"`
	CWD           string  `json:"cwd"`
	InputTokens   int64   `json:"input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
	StartedAt     string  `json:"started_at"`      // RFC3339
	LastRequestAt *string `json:"last_request_at"` // RFC3339, null if no request yet
	ParentID      string  `json:"parent_id,omitempty"`
	ForkTurnIdx   int     `json:"fork_turn_idx,omitempty"`
}

// Server is the Unix socket daemon server with a handler registry.
type Server struct {
	mu        sync.RWMutex
	handlers  map[string]HandlerFunc
	sockPath  string
	accessDB  *sql.DB // Access stats database (nil if init failed)
	sessionID string  // Unique ID for this daemon session

	// Agent session support
	cred         config.Credential
	model        string
	pluginConfig PluginConfig
	sessions     map[string]*Session
	sessionMu    sync.Mutex
	serverCtx    context.Context

	// User-level config directory (~/.vix/)
	homeVixDir string

	// Shared-secret token validated on every incoming socket message. Loaded
	// once at daemon start from the file passed via vixd's -auth-token-path
	// flag and stored in memory only — never logged, never copied into a
	// subprocess environment. When empty (the default — flag unset, or
	// in-process test embedding), the validation is skipped, so the daemon
	// behaves exactly as it did before the auth feature existed. Set the
	// flag (and have the client load the same file) only when the daemon
	// shares a host with untrusted local processes.
	authToken string

	// Web UI pub/sub
	subscribers  []chan struct{}
	subscriberMu sync.Mutex

	// Instance counting + exit-with-clients. Each vix process (TUI or headless)
	// holds one long-lived "instance.register" connection for its lifetime, so
	// instanceCount tracks how many vix processes are attached to this daemon.
	// When exitWithClients is set, the daemon shuts itself down once the count
	// returns to 0 after having seen at least one instance (everHadInstance) —
	// guarded by a grace period so a quick quit→relaunch doesn't kill it. A
	// daemon launched directly (vixd) leaves exitWithClients false and runs
	// until signalled.
	exitWithClients bool
	instanceMu      sync.Mutex
	instanceCount   int
	everHadInstance bool
	exitGraceGen    int // generation guard for the grace timer

	// cancel cancels serverCtx; set in ListenAndServe so internal triggers
	// (e.g. exit-with-clients) can initiate a graceful shutdown.
	cancel context.CancelFunc
}

// NewServer creates a new daemon server.
func NewServer(sockPath string, cred config.Credential, sessionID, model string, daemonConfig *config.DaemonConfig, pluginCfg PluginConfig) *Server {
	s := &Server{
		handlers:     make(map[string]HandlerFunc),
		sockPath:     sockPath,
		sessionID:    sessionID,
		cred:         cred,
		model:        model,
		pluginConfig: pluginCfg,
		sessions:     make(map[string]*Session),
		homeVixDir:   daemonConfig.HomeVixDir,
		authToken:    daemonConfig.AuthToken,
	}

	// Set LLM log directory to ~/.vix/logs/
	if s.homeVixDir != "" {
		SetLLMLogDir(filepath.Join(s.homeVixDir, "logs"))
	}

	return s
}

// SetExitWithClients configures whether the daemon shuts itself down once the
// last attached vix instance disconnects (after a grace period). Off by default
// so a directly-launched vixd runs until signalled; vix sets it when it spawns a
// daemon. Must be called before ListenAndServe.
func (s *Server) SetExitWithClients(v bool) {
	s.exitWithClients = v
}

// exitGracePeriod is how long the daemon waits after its last vix instance
// disconnects before shutting down (when exitWithClients is set). The grace
// absorbs a quick quit→relaunch or a transient instance-channel re-open without
// tearing down a daemon another client is about to reuse. It is a var so tests
// can shorten it.
var exitGracePeriod = 60 * time.Second

// instanceConnected records a newly attached vix instance.
func (s *Server) instanceConnected() {
	s.instanceMu.Lock()
	s.instanceCount++
	s.everHadInstance = true
	s.exitGraceGen++ // cancel any pending grace timer: we have a client again
	n := s.instanceCount
	s.instanceMu.Unlock()
	LogInfo("vix instance connected (now %d attached)", n)
	s.notifySubscribers()
}

// instanceDisconnected records a detached vix instance and, when configured,
// arms the exit-with-clients grace timer if none remain.
func (s *Server) instanceDisconnected() {
	s.instanceMu.Lock()
	if s.instanceCount > 0 {
		s.instanceCount--
	}
	n := s.instanceCount
	arm := s.exitWithClients && s.everHadInstance && n == 0
	s.exitGraceGen++
	gen := s.exitGraceGen
	s.instanceMu.Unlock()
	LogInfo("vix instance disconnected (now %d attached)", n)
	s.notifySubscribers()
	if arm {
		s.armExitGrace(gen)
	}
}

// armExitGrace schedules a shutdown after exitGracePeriod, unless a new instance
// connects (or another disconnect re-arms) in the meantime — detected via the
// generation guard. Fires on a timer so a quick relaunch keeps the daemon alive.
func (s *Server) armExitGrace(gen int) {
	LogInfo("No vix instances attached — exiting in %s unless one reconnects.", exitGracePeriod)
	time.AfterFunc(exitGracePeriod, func() {
		s.instanceMu.Lock()
		stale := gen != s.exitGraceGen || s.instanceCount > 0
		s.instanceMu.Unlock()
		if stale {
			return
		}
		LogInfo("No vix instances attached for %s — shutting down.", exitGracePeriod)
		if s.cancel != nil {
			s.cancel()
		}
	})
}

// LogAccess logs a tool access event. Safe to call even if accessDB is nil.
func (s *Server) LogAccess(toolName string, params map[string]any) {
	if s.accessDB == nil {
		return
	}
	logToolAccess(s.accessDB, s.sessionID, toolName, params)
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
// Empty s.authToken disables the check entirely — that's the legacy mode
// (vixd run without -auth-token-path) and in-process test embeddings.
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

// ListenAndServe starts the Unix socket server and blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	// Wrap the incoming context so internal triggers (exit-with-clients) can
	// cancel the server the same way an external signal does.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.cancel = cancel
	s.serverCtx = ctx

	// Remove stale socket file
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

	// Check if this is a session.start message — upgrade to persistent session
	var cmd protocol.SessionCommand
	if err := json.Unmarshal(line, &cmd); err == nil && cmd.Type == "session.start" {
		// Auth gate: drop the connection before any session resources are
		// allocated. handleSession's per-message reader-loop check covers
		// follow-ups; this one covers the initial start.
		if !s.authOK(cmd.AuthToken) {
			LogError("Session start rejected: auth_token mismatch")
			s.writeError(conn, "auth")
			return
		}
		s.handleSession(conn, scanner, cmd)
		return
	}

	// instance.register: a vix process holds this connection open for its whole
	// lifetime so the daemon can count attached instances independently of
	// sessions. Block until the connection closes, then decrement.
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
	// SessionCommand.AuthToken; flat field on the request map.
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
// a vix process, counting it as an attached instance. It blocks reading until
// the peer closes the connection (clean exit or process death both deliver EOF
// on a local Unix socket), then records the disconnect — which may arm the
// exit-with-clients grace timer if no instances remain.
func (s *Server) handleInstance(conn net.Conn, scanner *bufio.Scanner) {
	s.instanceConnected()
	defer s.instanceDisconnected()
	// Drain anything the client sends (it normally sends nothing). The loop
	// exits when the connection closes.
	for scanner.Scan() {
	}
}

// handleSession upgrades a connection to a persistent bidirectional session.
func (s *Server) handleSession(conn net.Conn, scanner *bufio.Scanner, startCmd protocol.SessionCommand) {
	// Parse session start data
	var startData protocol.SessionStartData
	json.Unmarshal(startCmd.Data, &startData)

	cwd := startData.CWD
	if cwd == "" {
		cwd2, _ := os.Getwd()
		cwd = cwd2
	}

	model := startData.Model
	if model == "" {
		model = s.model
	}

	// The session resolves its own LLM client from the authoritative model
	// (chat-agent frontmatter, falling back to this initial spec) in initBrain.
	// We pass nil here; if no credential is available the session enters its
	// unconfigured state rather than fabricating a doomed client.
	var llmClient LLM

	// Attach: resume a persisted session by ID instead of minting a new one.
	// Load the record up front so we can reuse its ID and seed history; a
	// missing record is reported with a machine-readable code so the client can
	// orphan the session (offer /copy) rather than retry forever.
	var attachRec *sessionRecord
	if startData.AttachSessionID != "" {
		p := config.NewVixPaths(startData.ConfigDir, s.homeVixDir, cwd)
		rec, found, err := loadSessionRecord(p, startData.AttachSessionID)
		if err != nil {
			LogError("attach: failed to load session %s: %v", startData.AttachSessionID, err)
		}
		if !found {
			s.writeEvent(conn, protocol.SessionEvent{
				Type: "event.error",
				Data: protocol.EventError{
					Message: "session not found",
					Code:    "session_not_found",
				},
			})
			return
		}
		attachRec = &rec
	}

	sessionID := generateSessionID()
	if attachRec != nil {
		sessionID = attachRec.ID
	}
	session := NewSession(sessionID, s, llmClient, model, cwd, startData.ConfigDir, startData.ForceInit, startData.EnableAutomaticWritePermission, startData.EnableAutomaticDirectoryAccess, startData.Headless, s.serverCtx)

	if attachRec != nil {
		session.seedFromRecord(attachRec)
	}

	// Seed conversation history from a forked session if requested.
	// Must be done before session.Run() starts processing commands.
	if startData.ForkSessionID != "" {
		s.sessionMu.Lock()
		forkSrc := s.sessions[startData.ForkSessionID]
		s.sessionMu.Unlock()
		if forkSrc != nil {
			if msgs := forkSrc.snapshotMessagesForFork(startData.ForkTurnIdx); len(msgs) > 0 {
				session.messages = msgs
			}
		}
		session.parentID = startData.ForkSessionID
		session.forkTurnIdx = startData.ForkTurnIdx
	}

	// Initialize access stats database for this session.
	// The path depends on the session's config-dir override, if any, so we
	// resolve it via session.paths after construction.
	// Guard with the server mutex so concurrent sessions for the same project
	// share one connection instead of racing to overwrite s.accessDB.
	s.mu.Lock()
	if s.accessDB == nil {
		db, err := initAccessStatsDB(session.paths.AccessStatsDB())
		if err != nil {
			LogError("Failed to initialize access stats DB (continuing without stats): %v", err)
		} else {
			s.accessDB = db
		}
	}
	s.mu.Unlock()
	// Register the session, enforcing exclusive ownership: a persisted session
	// may be attached by only one connection at a time. If another live
	// connection already owns this ID (e.g. a second vix instance, or a stale
	// reconnect), refuse with a machine-readable code so the client can react
	// (retry, restore something else, or surface a message) instead of forking a
	// divergent in-memory copy that races to persist. The check and the insert
	// are done under the same lock so two concurrent attaches can't both win.
	// Ownership is released automatically when the owning connection drops (the
	// delete(s.sessions, …) in the teardown below).
	s.sessionMu.Lock()
	if attachRec != nil {
		if _, live := s.sessions[sessionID]; live {
			s.sessionMu.Unlock()
			LogInfo("Attach refused: session %s is already open in another connection", sessionID)
			s.writeEvent(conn, protocol.SessionEvent{
				Type: "event.error",
				Data: protocol.EventError{
					Message: "session is already open in another window",
					Code:    "session_busy",
				},
			})
			return
		}
	}
	s.sessions[sessionID] = session
	s.sessionMu.Unlock()
	s.notifySubscribers()

	// Persist the freshly-created (or attached) session to open/ so it survives
	// a crash and is reopened on next launch. Attached sessions are already on
	// disk; this is a no-op-equivalent rewrite. Skipped when persistence is off.
	session.persist()

	LogInfo("Session %s started (cwd=%s, model=%s)", sessionID, cwd, model)

	// Send session started event
	s.writeEvent(conn, protocol.SessionEvent{
		Type: "event.session_started",
		Data: protocol.EventSessionStarted{
			SessionID:   sessionID,
			StartedAt:   session.startTime.Format(time.RFC3339),
			ParentID:    session.parentID,
			ForkTurnIdx: session.forkTurnIdx,
		},
	})

	// Writer goroutine: reads from session.eventChan, writes NDJSON to socket
	writerDone := make(chan struct{})
	go func() {
		defer func() {
			LogInfo("Session %s: writer exited", sessionID)
			close(writerDone)
		}()
		for {
			select {
			case event, ok := <-session.eventChan:
				if !ok {
					return
				}
				s.writeEvent(conn, event)
			case <-session.ctx.Done():
				// Drain remaining events
				for {
					select {
					case event := <-session.eventChan:
						s.writeEvent(conn, event)
					default:
						return
					}
				}
			}
		}
	}()

	// Watchdog: close the conn when the session context is canceled so the
	// reader goroutine (blocked on scanner.Scan()) can unblock. Without this,
	// a recovered panic inside session.Run() would leak the reader and hang
	// handleSession forever on <-readerDone.
	go func() {
		<-session.ctx.Done()
		conn.Close()
	}()

	// Reader goroutine: reads NDJSON from socket, feeds session.commandChan
	readerDone := make(chan struct{})
	go func() {
		defer func() {
			LogInfo("Session %s: reader exited (scanner.Err=%v)", sessionID, scanner.Err())
			close(readerDone)
		}()
		for scanner.Scan() {
			var cmd protocol.SessionCommand
			if err := json.Unmarshal(scanner.Bytes(), &cmd); err != nil {
				LogError("Session %s: invalid command JSON: %v", sessionID, err)
				continue
			}

			// Per-message auth: a connection that authenticated for
			// session.start cannot then send unauthenticated follow-ups.
			// Mismatch → close the connection (and the session); the
			// watchdog at line ~275 already cleans up on conn close.
			if !s.authOK(cmd.AuthToken) {
				LogError("Session %s: command auth_token mismatch (type=%s) — closing", sessionID, cmd.Type)
				conn.Close()
				return
			}

			if cmd.Type == "session.cancel" {
				// Cancel the active stream immediately
				if session.cancelStream != nil {
					session.cancelStream()
				}
				// Cancel an active plan/workflow
				if session.planCancel != nil {
					session.planCancel()
				}
				// Cancel any in-flight background subagents
				session.backgroundTasks.CancelAll()
				// Reap any detached bash-tool jobs (`background: true`).
				session.bashJobs.KillAll()
			}

			if cmd.Type == "session.workflow_message" {
				var msgData protocol.SessionWorkflowMessageData
				json.Unmarshal(cmd.Data, &msgData)
				if msgData.Text != "" {
					// Non-blocking send: drop an older pending message if the channel is full
					select {
					case session.workflowMsgChan <- msgData.Text:
					default:
						// Replace the existing pending message with the newer one
						select {
						case <-session.workflowMsgChan:
						default:
						}
						session.workflowMsgChan <- msgData.Text
					}
				}
				// Do not forward to commandChan — the workflow loop polls workflowMsgChan directly
				continue
			}

			select {
			case session.commandChan <- cmd:
			case <-session.ctx.Done():
				return
			}

			if cmd.Type == "session.close" {
				return
			}
		}
		// Socket closed — cancel session
		session.cancel()
	}()

	// Run the agent loop (blocking)
	session.Run()

	// Wait for reader/writer to finish
	session.cancel()
	<-readerDone
	<-writerDone

	// Reap any detached bash-tool jobs before we drop the session — otherwise
	// a leaked `john` / `cargo build` would keep chewing CPU until the
	// container dies. Mirrors the Ctrl-C branch above.
	//
	// Opt-out for hosts that deliberately outlive the vix client and rely on
	// background jobs (e.g. a pypi-server or web server) staying alive for a
	// post-agent verifier. The Ctrl-C branch above still reaps on cancel —
	// this only affects clean session close.
	if os.Getenv("VIX_KEEP_BG_ON_SESSION_END") != "1" {
		session.bashJobs.KillAll()
	}

	// Remove session from map
	s.sessionMu.Lock()
	delete(s.sessions, sessionID)
	s.sessionMu.Unlock()
	s.notifySubscribers()

	// An explicit user close (the "x" action sends session.close) moves the
	// record open/ -> closed/ so it is not reopened on next launch. A bare
	// disconnect leaves it in open/ so the TUI restores it next run.
	if session.closedByUser {
		if err := moveSessionToClosed(session.paths, sessionID); err != nil {
			LogError("close session %s: move to closed failed: %v", sessionID, err)
		}
	}

	LogInfo("Session %s ended (run returned, reader done, writer done)", sessionID)
}

func (s *Server) writeEvent(conn net.Conn, event protocol.SessionEvent) {
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

// Sessions returns a snapshot of all currently active sessions.
func (s *Server) Sessions() []SessionInfo {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	infos := make([]SessionInfo, 0, len(s.sessions))
	for _, sess := range s.sessions {
		info := SessionInfo{
			ID:           sess.id,
			CWD:          sess.cwd,
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
	}
	return infos
}

// getSession returns the live session with the given ID, or nil if not found.
func (s *Server) getSession(id string) *Session {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	return s.sessions[id]
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
