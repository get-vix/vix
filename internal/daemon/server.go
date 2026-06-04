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
	"sync"
	"time"
	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
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
	sessions  map[string]*Session
	sessionMu sync.Mutex
	serverCtx context.Context

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
	subscribers   []chan struct{}
	subscriberMu  sync.Mutex
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

	llmClient, err := llm.NewFromModel(model, s.pluginConfig, llm.DefaultEffortFromSpec(model), 0)
	if err != nil {
		// Fall back to a default Anthropic client if the model spec can't be
		// resolved (e.g. no credential for the target provider). initBrain
		// will attempt to resolve again from the agent frontmatter.
		llmClient = NewLLM(s.cred, model, defaultSessionEffort(model), 0, s.pluginConfig)
	}

	sessionID := generateSessionID()
	session := NewSession(sessionID, s, llmClient, model, cwd, startData.ConfigDir, startData.ForceInit, startData.EnableAutomaticWritePermission, startData.EnableAutomaticDirectoryAccess, startData.Headless, s.serverCtx)

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
	s.sessionMu.Lock()
	s.sessions[sessionID] = session
	s.sessionMu.Unlock()
	s.notifySubscribers()

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
