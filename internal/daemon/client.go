package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/get-vix/vix/internal/protocol"
)

// ToolResult holds the result of a tool execution from the daemon.
type ToolResult struct {
	Output            string
	IsError           bool
	NeedsConfirmation bool
	ToolName          string
	Params            map[string]any
	LineOffset        int
}

// Client communicates with the vix daemon over a Unix socket.
// Used for one-shot commands (ping, brain context, etc.)
type Client struct {
	socketPath string
	// Shared-secret token injected on every outgoing request as the
	// `auth_token` field. Set via SetAuthToken from cmd/vix/main.go after
	// reading the token file pointed at by -auth-token-path. Empty when the
	// daemon is also unauthenticated (the default when vixd was started
	// without -auth-token-path, or in-process test embeddings).
	authToken string
}

// NewClient creates a new daemon client.
func NewClient(path string) *Client {
	return &Client{socketPath: path}
}

// SetAuthToken stores the shared-secret token used to authenticate every
// request. Must be called before any RPC if the daemon was started with
// -auth-token-path.
func (c *Client) SetAuthToken(token string) {
	c.authToken = token
}

// sendRequest sends a JSON request to the daemon and returns the parsed response.
func (c *Client) sendRequest(data map[string]any) (map[string]any, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("daemon connect: %w", err)
	}
	defer conn.Close()

	// Set deadline to prevent permanent hangs (bash tool has 120s timeout + margin)
	if err := conn.SetDeadline(time.Now().Add(150 * time.Second)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	// Inject the auth token so the daemon's handleClient gate passes. This
	// is the single chokepoint for one-shot RPCs (Ping, ExecuteTool, brain
	// commands); every caller flows through here.
	if c.authToken != "" {
		data["auth_token"] = c.authToken
	}

	// Write JSON + newline
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Shutdown write side so daemon sees EOF
	if uc, ok := conn.(*net.UnixConn); ok {
		if err := uc.CloseWrite(); err != nil {
			return nil, fmt.Errorf("close write: %w", err)
		}
	}

	// Read full response
	buf, err := io.ReadAll(conn)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(buf, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return resp, nil
}

// Ping checks if the daemon is running.
func (c *Client) Ping() bool {
	resp, err := c.sendRequest(map[string]any{"action": "ping"})
	if err != nil {
		return false
	}
	return resp["status"] == "ok"
}

// DaemonVersion returns the running daemon's build version, as reported by the
// ping handler. Daemons predating the version field report "" — callers must
// treat that as a mismatch with any released client.
func (c *Client) DaemonVersion() (string, error) {
	resp, err := c.sendRequest(map[string]any{"action": "ping"})
	if err != nil {
		return "", err
	}
	v, _ := resp["version"].(string)
	return v, nil
}

// StopDaemon asks the daemon to perform a coordinated shutdown (every attached
// vix instance is told to quit, then the daemon exits). Exempt from the version
// gate so it works across mismatched builds.
func (c *Client) StopDaemon() error {
	resp, err := c.sendRequest(map[string]any{"action": "daemon.stop"})
	if err != nil {
		return err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return fmt.Errorf("daemon.stop failed: %s", msg)
	}
	return nil
}

// ListSessions returns the persisted open sessions for cwd, so the TUI can
// reopen them on launch. Sessions are stored globally (~/.vix/sessions) and
// filtered by cwd daemon-side.
func (c *Client) ListSessions(cwd, configDir string) ([]protocol.SessionSummary, error) {
	resp, err := c.sendRequest(map[string]any{
		"command":    "session.list",
		"cwd":        cwd,
		"config_dir": configDir,
	})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(resp["sessions"])
	if err != nil {
		return nil, err
	}
	var out []protocol.SessionSummary
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DismissSession archives a persisted session record (open/ → closed/) without
// attaching it. Used to dismiss vix-initiated run records from the TUI.
func (c *Client) DismissSession(cwd, configDir, id string) error {
	resp, err := c.sendRequest(map[string]any{
		"command":    "session.dismiss",
		"cwd":        cwd,
		"config_dir": configDir,
		"id":         id,
	})
	if err != nil {
		return err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return fmt.Errorf("session.dismiss failed: %s", msg)
	}
	return nil
}

// ExecuteTool sends a tool execution request to the daemon.
func (c *Client) ExecuteTool(name string, params map[string]any, cwd string) (*ToolResult, error) {
	p := make(map[string]any, len(params)+1)
	for k, v := range params {
		p[k] = v
	}
	if name == "bash" || name == "grep" || name == "glob_files" || name == "lsp_query" {
		p["cwd"] = cwd
	}

	resp, err := c.sendRequest(map[string]any{
		"command": "tool." + name,
		"params":  p,
	})
	if err != nil {
		return nil, err
	}

	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return &ToolResult{Output: fmt.Sprintf("Daemon error: %s", msg), IsError: true}, nil
	}

	data, _ := resp["data"].(map[string]any)

	if confirm, ok := data["confirm"].(bool); ok && confirm {
		return &ToolResult{
			NeedsConfirmation: true,
			ToolName:          name,
			Params:            params,
		}, nil
	}

	output, _ := data["output"].(string)
	isError, _ := data["is_error"].(bool)
	return &ToolResult{Output: output, IsError: isError}, nil
}

// ExecuteToolConfirmed re-sends a tool request with the confirmed flag.
func (c *Client) ExecuteToolConfirmed(name string, params map[string]any, cwd string) (*ToolResult, error) {
	p := make(map[string]any, len(params)+2)
	for k, v := range params {
		p[k] = v
	}
	p["confirmed"] = true
	if name == "bash" || name == "grep" || name == "glob_files" || name == "lsp_query" {
		p["cwd"] = cwd
	}

	resp, err := c.sendRequest(map[string]any{
		"command": "tool." + name,
		"params":  p,
	})
	if err != nil {
		return nil, err
	}

	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return &ToolResult{Output: fmt.Sprintf("Daemon error: %s", msg), IsError: true}, nil
	}

	data, _ := resp["data"].(map[string]any)
	output, _ := data["output"].(string)
	isError, _ := data["is_error"].(bool)
	return &ToolResult{Output: output, IsError: isError}, nil
}

// --- SessionClient: persistent bidirectional connection for agent sessions ---

// SessionClient manages a persistent connection to the daemon for agent sessions.
type SessionClient struct {
	socketPath string
	conn       net.Conn
	scanner    *bufio.Scanner
	mu         sync.Mutex // protects writes
	sessionID  string
	startedAt  time.Time
	// Shared-secret token stamped onto every outgoing SessionCommand. Set
	// via SetAuthToken before Connect; matches the daemon's
	// -auth-token-path. Empty when the daemon side is also unauthenticated.
	authToken string
	// Client build version stamped into SessionStartData so the daemon can
	// enforce the version gate. Defaults to the process-wide value set via
	// SetClientVersion.
	version string
}

// clientVersion is the build version stamped on every SessionClient created by
// this process. Set once at startup (cmd/vix/main.go) via SetClientVersion so
// every connection — initial, restore, reconnect, fork — carries it.
var clientVersion string

// SetClientVersion records the process-wide client build version used by all
// subsequently created SessionClients. Call once at startup, before any
// connection is opened.
func SetClientVersion(v string) {
	clientVersion = v
}

// NewSessionClient creates a new session client (does not connect yet).
func NewSessionClient(socketPath string) *SessionClient {
	return &SessionClient{socketPath: socketPath, version: clientVersion}
}

// SetAuthToken stores the shared-secret token used to authenticate every
// SessionCommand. Must be called before Connect if the daemon was started
// with -auth-token-path.
func (sc *SessionClient) SetAuthToken(token string) {
	sc.authToken = token
}

// SessionID returns the session ID assigned by the daemon.
func (sc *SessionClient) SessionID() string {
	return sc.sessionID
}

// StartedAt returns the time the daemon session was created.
func (sc *SessionClient) StartedAt() time.Time { return sc.startedAt }

// Connect establishes a persistent connection and starts an agent session.
func (sc *SessionClient) Connect(cwd, configDir, model string, forceInit bool, enableAutomaticWritePermission bool, enableAutomaticDirectoryAccess bool, headless bool) error {
	return sc.connectWith(protocol.SessionStartData{
		CWD:                            cwd,
		ConfigDir:                      configDir,
		Model:                          model,
		ForceInit:                      forceInit,
		EnableAutomaticWritePermission: enableAutomaticWritePermission,
		EnableAutomaticDirectoryAccess: enableAutomaticDirectoryAccess,
		Headless:                       headless,
	})
}

// ConnectFork establishes a persistent connection and starts a new agent
// session pre-seeded with the conversation history from forkSessionID up to
// and including the turn at forkTurnIdx (0-based).
func (sc *SessionClient) ConnectFork(cwd, configDir, model string, forceInit bool, enableAutomaticWritePermission bool, enableAutomaticDirectoryAccess bool, headless bool, forkSessionID string, forkTurnIdx int) error {
	return sc.connectWith(protocol.SessionStartData{
		CWD:                            cwd,
		ConfigDir:                      configDir,
		Model:                          model,
		ForceInit:                      forceInit,
		EnableAutomaticWritePermission: enableAutomaticWritePermission,
		EnableAutomaticDirectoryAccess: enableAutomaticDirectoryAccess,
		Headless:                       headless,
		ForkSessionID:                  forkSessionID,
		ForkTurnIdx:                    forkTurnIdx,
	})
}

// ErrSessionNotFound is returned by Attach when the daemon has no persisted
// record for the requested ID (e.g. it was lost in a daemon restart before its
// first flush). Callers should orphan the session rather than retry.
var ErrSessionNotFound = errors.New("session not found")

// ErrSessionBusy is returned by Attach when the requested session is already
// open in another connection (exclusive single-writer ownership). Callers can
// retry later (the owner may disconnect) or restore/attach a different session.
var ErrSessionBusy = errors.New("session busy")

// ErrVersionMismatch is returned by Connect/Attach when the daemon refused the
// session because the client and daemon builds differ. The wrapped message
// carries both versions and the remediation command.
var ErrVersionMismatch = errors.New("version mismatch")

// Attach establishes a persistent connection and resumes the persisted session
// with the given ID. On success the daemon replays the conversation via
// event.replay. Returns ErrSessionNotFound when no record exists on disk.
func (sc *SessionClient) Attach(cwd, configDir, model string, forceInit bool, enableAutomaticWritePermission bool, enableAutomaticDirectoryAccess bool, headless bool, attachSessionID string) error {
	return sc.connectWith(protocol.SessionStartData{
		CWD:                            cwd,
		ConfigDir:                      configDir,
		Model:                          model,
		ForceInit:                      forceInit,
		EnableAutomaticWritePermission: enableAutomaticWritePermission,
		EnableAutomaticDirectoryAccess: enableAutomaticDirectoryAccess,
		Headless:                       headless,
		AttachSessionID:                attachSessionID,
	})
}

// connectWith dials the daemon and starts a session with the given start data.
func (sc *SessionClient) connectWith(startData protocol.SessionStartData) error {
	startData.ClientVersion = sc.version
	conn, err := net.Dial("unix", sc.socketPath)
	if err != nil {
		return fmt.Errorf("daemon connect: %w", err)
	}
	sc.conn = conn
	sc.scanner = bufio.NewScanner(conn)
	sc.scanner.Buffer(make([]byte, 0, 64*1024), protocol.MaxMessageSize)

	// Send session.start command
	data, _ := json.Marshal(startData)
	if err := sc.sendCommand(protocol.SessionCommand{
		Type: "session.start",
		Data: data,
	}); err != nil {
		conn.Close()
		return fmt.Errorf("send session.start: %w", err)
	}

	// Read session_started event
	event, err := sc.ReadEvent()
	if err != nil {
		conn.Close()
		return fmt.Errorf("read session_started: %w", err)
	}
	if event.Type == "event.error" {
		conn.Close()
		raw, _ := json.Marshal(event.Data)
		var ee protocol.EventError
		json.Unmarshal(raw, &ee)
		if ee.Code == "session_not_found" {
			return ErrSessionNotFound
		}
		if ee.Code == "session_busy" {
			return ErrSessionBusy
		}
		if ee.Code == "version_mismatch" {
			return fmt.Errorf("%w: %s", ErrVersionMismatch, ee.Message)
		}
		if ee.Message != "" {
			return fmt.Errorf("session start failed: %s", ee.Message)
		}
		return fmt.Errorf("session start failed: %s", string(raw))
	}
	if event.Type == "event.session_started" {
		data, _ := json.Marshal(event.Data)
		var started protocol.EventSessionStarted
		json.Unmarshal(data, &started)
		sc.sessionID = started.SessionID
		if t, err := time.Parse(time.RFC3339, started.StartedAt); err == nil {
			sc.startedAt = t
		}
	}

	return nil
}

// SendInput sends user chat input with optional attachments.
func (sc *SessionClient) SendInput(text string, attachments []protocol.Attachment) error {
	data, _ := json.Marshal(protocol.SessionInputData{Text: text, Attachments: attachments})
	return sc.sendCommand(protocol.SessionCommand{
		Type: "session.input",
		Data: data,
	})
}

// SendWorkflow sends a workflow execution request with a prompt.
func (sc *SessionClient) SendWorkflow(name, text string) error {
	data, _ := json.Marshal(protocol.SessionWorkflowData{Name: name, Text: text})
	return sc.sendCommand(protocol.SessionCommand{
		Type: "session.workflow",
		Data: data,
	})
}

// SendWorkflowMessage enqueues a user message to be injected into the currently
// running workflow agent as soon as the current LLM turn ends.
func (sc *SessionClient) SendWorkflowMessage(text string) error {
	data, _ := json.Marshal(protocol.SessionWorkflowMessageData{Text: text})
	return sc.sendCommand(protocol.SessionCommand{
		Type: "session.workflow_message",
		Data: data,
	})
}

// SendConfirm sends tool approval/denial.
func (sc *SessionClient) SendConfirm(approved bool, persistDirs bool) error {
	data, _ := json.Marshal(protocol.SessionConfirmData{Approved: approved, PersistDirs: persistDirs})
	return sc.sendCommand(protocol.SessionCommand{
		Type: "session.confirm",
		Data: data,
	})
}

// SendPlanAction sends a plan review decision.
func (sc *SessionClient) SendPlanAction(action string, text string) error {
	data, _ := json.Marshal(protocol.SessionPlanActionData{Action: action, Text: text})
	return sc.sendCommand(protocol.SessionCommand{
		Type: "session.plan_action",
		Data: data,
	})
}

// SendUserAnswer sends the user's answer to a question.
// The text parameter carries additional user input when has_user_input is used.
func (sc *SessionClient) SendUserAnswer(answer string, text string) error {
	data, _ := json.Marshal(protocol.SessionUserAnswerData{Answer: answer, Text: text})
	return sc.sendCommand(protocol.SessionCommand{
		Type: "session.user_answer",
		Data: data,
	})
}

// SendUserAnswerBatch sends batch answers (question ID → answer) for multi-question mode.
func (sc *SessionClient) SendUserAnswerBatch(answers map[string]string) error {
	data, _ := json.Marshal(protocol.SessionUserAnswerData{Answers: answers})
	return sc.sendCommand(protocol.SessionCommand{
		Type: "session.user_answer",
		Data: data,
	})
}

// SendSetModel requests that the daemon switch to a different LLM model.
func (sc *SessionClient) SendSetModel(model string) error {
	data, _ := json.Marshal(protocol.SessionSetModelData{Model: model})
	return sc.sendCommand(protocol.SessionCommand{
		Type: "session.set_model",
		Data: data,
	})
}

// SendTrim instructs the daemon to trim the conversation history, keeping
// messages up to and including the turn at turnIdx (0-based).
func (sc *SessionClient) SendTrim(turnIdx int) error {
	data, _ := json.Marshal(protocol.SessionTrimData{TurnIdx: turnIdx})
	return sc.sendCommand(protocol.SessionCommand{
		Type: "session.trim",
		Data: data,
	})
}

// SendMarkRead tells the daemon the user is viewing this session: the
// persisted unread flag is cleared. Sent by the TUI when a session gains
// focus and when a turn completes while focused.
func (sc *SessionClient) SendMarkRead() error {
	return sc.sendCommand(protocol.SessionCommand{
		Type: "session.mark_read",
	})
}

// SendCancel cancels the current work.
func (sc *SessionClient) SendCancel() error {
	return sc.sendCommand(protocol.SessionCommand{
		Type: "session.cancel",
	})
}

// SendUpdateQuit tells the daemon an in-app update finished installing: it
// broadcasts a quit to every attached vix instance and shuts itself down so the
// freshly-installed binaries take effect on relaunch.
func (sc *SessionClient) SendUpdateQuit() error {
	return sc.sendCommand(protocol.SessionCommand{
		Type: "update.quit",
	})
}

// SendClose ends the session.
func (sc *SessionClient) SendClose() error {
	err := sc.sendCommand(protocol.SessionCommand{
		Type: "session.close",
	})
	if sc.conn != nil {
		sc.conn.Close()
	}
	return err
}

// ReadEvent reads the next event from the daemon.
func (sc *SessionClient) ReadEvent() (protocol.SessionEvent, error) {
	if !sc.scanner.Scan() {
		err := sc.scanner.Err()
		if err == nil {
			err = fmt.Errorf("connection closed")
		}
		return protocol.SessionEvent{}, err
	}

	var event protocol.SessionEvent
	if err := json.Unmarshal(sc.scanner.Bytes(), &event); err != nil {
		return protocol.SessionEvent{}, fmt.Errorf("parse event: %w", err)
	}
	return event, nil
}

// Close closes the underlying connection.
func (sc *SessionClient) Close() {
	if sc.conn != nil {
		sc.conn.Close()
	}
}

func (sc *SessionClient) sendCommand(cmd protocol.SessionCommand) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.conn == nil {
		return fmt.Errorf("not connected")
	}

	// Stamp the auth token onto every outgoing command. This is the single
	// chokepoint for all session messages (session.start, session.input,
	// session.workflow, session.confirm, session.user_answer, …) so the
	// daemon's per-message auth check sees a value on each one.
	if sc.authToken != "" {
		cmd.AuthToken = sc.authToken
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = sc.conn.Write(data)
	return err
}

// InstanceClient is a long-lived control connection that registers the running
// vix process as an "instance" with the daemon. The daemon counts these to know
// how many vix processes are attached (independently of sessions). The client
// sends a single instance.register command and then holds the connection open
// for the process lifetime; closing it (clean exit or process death) tells the
// daemon this instance is gone.
type InstanceClient struct {
	conn net.Conn
}

// RegisterInstance dials the daemon and registers this process as an attached
// instance, returning a handle whose Close ends the registration. mode is
// advisory ("tui" | "headless"). On any failure it returns an error and the
// caller may simply proceed without registration — the count is observability
// only (web UI vitals, logging).
func RegisterInstance(socketPath, authToken, mode string) (*InstanceClient, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("daemon connect: %w", err)
	}
	data, _ := json.Marshal(protocol.InstanceRegisterData{Mode: mode})
	cmd := protocol.SessionCommand{Type: "instance.register", AuthToken: authToken, Data: data}
	payload, err := json.Marshal(cmd)
	if err != nil {
		conn.Close()
		return nil, err
	}
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		conn.Close()
		return nil, fmt.Errorf("register instance: %w", err)
	}
	return &InstanceClient{conn: conn}, nil
}

// Close ends the instance registration, signalling the daemon that this process
// has detached.
func (ic *InstanceClient) Close() {
	if ic != nil && ic.conn != nil {
		ic.conn.Close()
	}
}
