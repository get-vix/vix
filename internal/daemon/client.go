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

// CreateMessageThread asks the daemon to create a Vix-initiated message
// thread from spec (a raw MessageThreadSpec JSON object). Returns the new
// thread id. Backs `vix thread create`.
func (c *Client) CreateMessageThread(spec json.RawMessage) (string, error) {
	resp, err := c.sendRequest(map[string]any{
		"action": "message.create",
		"thread": spec,
	})
	if err != nil {
		return "", err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return "", fmt.Errorf("message.create failed: %s", msg)
	}
	id, _ := resp["thread_id"].(string)
	return id, nil
}

// RunJob asks the daemon to fire the job with the given id immediately, out of
// band from its schedule. Returns the run's thread id. Backs `vix job run`.
func (c *Client) RunJob(id string) (string, error) {
	resp, err := c.sendRequest(map[string]any{
		"action": "job.run",
		"id":     id,
	})
	if err != nil {
		return "", err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return "", fmt.Errorf("job.run failed: %s", msg)
	}
	threadID, _ := resp["thread_id"].(string)
	return threadID, nil
}

// TriggerHook asks the daemon to fire the hook with the given id immediately,
// out of band from its event. Returns the run's thread id (empty for command
// hooks, which have no thread) and the fire id. Backs `vix hook trigger`.
func (c *Client) TriggerHook(id string) (threadID, fireID string, err error) {
	resp, err := c.sendRequest(map[string]any{
		"action": "hook.trigger",
		"id":     id,
	})
	if err != nil {
		return "", "", err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return "", "", fmt.Errorf("hook.trigger failed: %s", msg)
	}
	threadID, _ = resp["thread_id"].(string)
	fireID, _ = resp["fire_id"].(string)
	return threadID, fireID, nil
}

// ListJobs returns the scheduled jobs (enabled and disabled) for the Jobs &
// Triggers tab. Jobs are daemon-global, so no cwd filter is applied.
func (c *Client) ListJobs() ([]protocol.JobSummary, error) {
	resp, err := c.sendRequest(map[string]any{"command": "job.list"})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(resp["jobs"])
	if err != nil {
		return nil, err
	}
	var out []protocol.JobSummary
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListHooks returns the lifecycle hooks (enabled and disabled) for the Jobs &
// Triggers tab.
func (c *Client) ListHooks() ([]protocol.HookSummary, error) {
	resp, err := c.sendRequest(map[string]any{"command": "hook.list"})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(resp["hooks"])
	if err != nil {
		return nil, err
	}
	var out []protocol.HookSummary
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetJobEnabled enables or disables a scheduled job by id (Space toggle in the
// Jobs & Triggers tab).
func (c *Client) SetJobEnabled(id string, enabled bool) error {
	resp, err := c.sendRequest(map[string]any{
		"command": "job.set_enabled",
		"id":      id,
		"enabled": enabled,
	})
	if err != nil {
		return err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return fmt.Errorf("job.set_enabled failed: %s", msg)
	}
	return nil
}

// ListMCPServers returns the configured MCP servers (with status, type, and tool
// count) for the MCP tab. MCP config is home-only, so no cwd filter is applied.
func (c *Client) ListMCPServers() ([]protocol.MCPServerSummary, error) {
	resp, err := c.sendRequest(map[string]any{"command": "mcp.list"})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(resp["servers"])
	if err != nil {
		return nil, err
	}
	var out []protocol.MCPServerSummary
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetMCPEnabled enables or disables an MCP server by name (Space toggle in the
// MCP tab).
func (c *Client) SetMCPEnabled(name string, enabled bool) error {
	resp, err := c.sendRequest(map[string]any{
		"command": "mcp.set_enabled",
		"name":    name,
		"enabled": enabled,
	})
	if err != nil {
		return err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return fmt.Errorf("mcp.set_enabled failed: %s", msg)
	}
	return nil
}

// AuthorizeMCP starts the interactive OAuth flow for an MCP server and returns
// the authorization URL to open in a browser. The exchange completes
// asynchronously in the daemon; poll ListMCPServers for the resulting auth
// state.
func (c *Client) AuthorizeMCP(name string) (string, error) {
	resp, err := c.sendRequest(map[string]any{
		"command": "mcp.authorize",
		"name":    name,
	})
	if err != nil {
		return "", err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return "", fmt.Errorf("mcp.authorize failed: %s", msg)
	}
	authURL, _ := resp["auth_url"].(string)
	return authURL, nil
}

// LogoutMCP deletes the stored OAuth token for an MCP server.
func (c *Client) LogoutMCP(name string) error {
	resp, err := c.sendRequest(map[string]any{
		"command": "mcp.logout",
		"name":    name,
	})
	if err != nil {
		return err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return fmt.Errorf("mcp.logout failed: %s", msg)
	}
	return nil
}

// SetHookEnabled enables or disables a lifecycle hook by id (Space toggle in the
// Jobs & Triggers tab).
func (c *Client) SetHookEnabled(id string, enabled bool) error {
	resp, err := c.sendRequest(map[string]any{
		"command": "hook.set_enabled",
		"id":      id,
		"enabled": enabled,
	})
	if err != nil {
		return err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return fmt.Errorf("hook.set_enabled failed: %s", msg)
	}
	return nil
}

// ListThreads returns the persisted open threads for cwd, so the TUI can
// reopen them on launch. Threads are stored globally (~/.vix/threads) and
// filtered by cwd daemon-side.
func (c *Client) ListThreads(cwd, configDir string) ([]protocol.ThreadSummary, error) {
	resp, err := c.sendRequest(map[string]any{
		"command":    "thread.list",
		"cwd":        cwd,
		"config_dir": configDir,
	})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(resp["threads"])
	if err != nil {
		return nil, err
	}
	var out []protocol.ThreadSummary
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListThreadDirs returns the working directories used by open user threads,
// ranked by thread count (then recency), for the welcome screen's recent-
// directories list. Unlike ListThreads it is not cwd-scoped.
func (c *Client) ListThreadDirs(cwd, configDir string) ([]protocol.DirUsage, error) {
	resp, err := c.sendRequest(map[string]any{
		"command":    "thread.dirs",
		"cwd":        cwd,
		"config_dir": configDir,
	})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(resp["dirs"])
	if err != nil {
		return nil, err
	}
	var out []protocol.DirUsage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ValidateAttachment asks the daemon whether a user-attached file (text or PDF)
// can be turned into prompt text for the given thread. It returns a status —
// "ok" (add a chip), "invalid" (alert + drop), or "error" — and a human-readable
// reason.
func (c *Client) ValidateAttachment(threadID, path string) (status, reason string, err error) {
	resp, err := c.sendRequest(map[string]any{
		"command":   "attachment.validate",
		"thread_id": threadID,
		"path":      path,
	})
	if err != nil {
		return "", "", err
	}
	status, _ = resp["status"].(string)
	reason, _ = resp["reason"].(string)
	if reason == "" {
		reason, _ = resp["message"].(string)
	}
	return status, reason, nil
}

// DismissThread archives a persisted thread record (open/ → closed/) without
func (c *Client) DismissThread(cwd, configDir, id string) error {
	resp, err := c.sendRequest(map[string]any{
		"command":    "thread.dismiss",
		"cwd":        cwd,
		"config_dir": configDir,
		"id":         id,
	})
	if err != nil {
		return err
	}
	if resp["status"] != "ok" {
		msg, _ := resp["message"].(string)
		return fmt.Errorf("thread.dismiss failed: %s", msg)
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

// --- ThreadClient: persistent bidirectional connection for agent threads ---

// ThreadClient manages a persistent connection to the daemon for agent threads.
type ThreadClient struct {
	socketPath string
	conn       net.Conn
	scanner    *bufio.Scanner
	mu         sync.Mutex // protects writes
	threadID   string
	startedAt  time.Time
	// whiteboardBase is the local web UI origin (e.g. "http://localhost:1337")
	// reported by the daemon in the thread_started event, or "" when the web UI
	// is disabled. The TUI uses it to build "See it on the whiteboard" links for
	// mermaid diagrams. Captured here because StartThread consumes the
	// thread_started event before the TUI's event loop can see it.
	whiteboardBase string
	// Shared-secret token stamped onto every outgoing ThreadCommand. Set
	// via SetAuthToken before Connect; matches the daemon's
	// -auth-token-path. Empty when the daemon side is also unauthenticated.
	authToken string
	// Client build version stamped into ThreadStartData so the daemon can
	// enforce the version gate. Defaults to the process-wide value set via
	// SetClientVersion.
	version string
}

// clientVersion is the build version stamped on every ThreadClient created by
// this process. Set once at startup (cmd/vix/main.go) via SetClientVersion so
// every connection — initial, restore, reconnect, fork — carries it.
var clientVersion string

// SetClientVersion records the process-wide client build version used by all
// subsequently created ThreadClients. Call once at startup, before any
// connection is opened.
func SetClientVersion(v string) {
	clientVersion = v
}

// NewThreadClient creates a new thread client (does not connect yet).
func NewThreadClient(socketPath string) *ThreadClient {
	return &ThreadClient{socketPath: socketPath, version: clientVersion}
}

// SetAuthToken stores the shared-secret token used to authenticate every
// ThreadCommand. Must be called before Connect if the daemon was started
// with -auth-token-path.
func (sc *ThreadClient) SetAuthToken(token string) {
	sc.authToken = token
}

// ThreadID returns the thread ID assigned by the daemon.
func (sc *ThreadClient) ThreadID() string {
	return sc.threadID
}

// StartedAt returns the time the daemon thread was created.
func (sc *ThreadClient) StartedAt() time.Time { return sc.startedAt }

// WhiteboardBase returns the local web UI origin reported by the daemon in the
// thread_started event, or "" when the web UI is disabled.
func (sc *ThreadClient) WhiteboardBase() string { return sc.whiteboardBase }

// Connect establishes a persistent connection and starts an agent thread.
func (sc *ThreadClient) Connect(cwd, configDir, model string, forceInit bool, enableAutomaticWritePermission bool, enableAutomaticDirectoryAccess bool, headless bool) error {
	return sc.connectWith(protocol.ThreadStartData{
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
// thread pre-seeded with the conversation history from forkThreadID up to
// and including the turn at forkTurnIdx (0-based).
func (sc *ThreadClient) ConnectFork(cwd, configDir, model string, forceInit bool, enableAutomaticWritePermission bool, enableAutomaticDirectoryAccess bool, headless bool, forkThreadID string, forkTurnIdx int) error {
	return sc.connectWith(protocol.ThreadStartData{
		CWD:                            cwd,
		ConfigDir:                      configDir,
		Model:                          model,
		ForceInit:                      forceInit,
		EnableAutomaticWritePermission: enableAutomaticWritePermission,
		EnableAutomaticDirectoryAccess: enableAutomaticDirectoryAccess,
		Headless:                       headless,
		ForkThreadID:                   forkThreadID,
		ForkTurnIdx:                    forkTurnIdx,
	})
}

// ErrThreadNotFound is returned by Attach when the daemon has no persisted
// record for the requested ID (e.g. it was lost in a daemon restart before its
// first flush). Callers should orphan the thread rather than retry.
var ErrThreadNotFound = errors.New("thread not found")

// ErrThreadBusy is returned by Attach when the requested thread is already
// open in another connection (exclusive single-writer ownership). Callers can
// retry later (the owner may disconnect) or restore/attach a different thread.
var ErrThreadBusy = errors.New("thread busy")

// ErrVersionMismatch is returned by Connect/Attach when the daemon refused the
// thread because the client and daemon builds differ. The wrapped message
// carries both versions and the remediation command.
var ErrVersionMismatch = errors.New("version mismatch")

// Attach establishes a persistent connection and resumes the persisted thread
// with the given ID. On success the daemon replays the conversation via
// event.replay. Returns ErrThreadNotFound when no record exists on disk.
func (sc *ThreadClient) Attach(cwd, configDir, model string, forceInit bool, enableAutomaticWritePermission bool, enableAutomaticDirectoryAccess bool, headless bool, attachThreadID string) error {
	return sc.connectWith(protocol.ThreadStartData{
		CWD:                            cwd,
		ConfigDir:                      configDir,
		Model:                          model,
		ForceInit:                      forceInit,
		EnableAutomaticWritePermission: enableAutomaticWritePermission,
		EnableAutomaticDirectoryAccess: enableAutomaticDirectoryAccess,
		Headless:                       headless,
		AttachThreadID:                 attachThreadID,
	})
}

// connectWith dials the daemon and starts a thread with the given start data.
func (sc *ThreadClient) connectWith(startData protocol.ThreadStartData) error {
	startData.ClientVersion = sc.version
	conn, err := net.Dial("unix", sc.socketPath)
	if err != nil {
		return fmt.Errorf("daemon connect: %w", err)
	}
	sc.conn = conn
	sc.scanner = bufio.NewScanner(conn)
	sc.scanner.Buffer(make([]byte, 0, 64*1024), protocol.MaxMessageSize)

	// Send thread.start command
	data, _ := json.Marshal(startData)
	if err := sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.start",
		Data: data,
	}); err != nil {
		conn.Close()
		return fmt.Errorf("send thread.start: %w", err)
	}

	// Read thread_started event
	event, err := sc.ReadEvent()
	if err != nil {
		conn.Close()
		return fmt.Errorf("read thread_started: %w", err)
	}
	if event.Type == "event.error" {
		conn.Close()
		raw, _ := json.Marshal(event.Data)
		var ee protocol.EventError
		json.Unmarshal(raw, &ee)
		if ee.Code == "thread_not_found" {
			return ErrThreadNotFound
		}
		if ee.Code == "thread_busy" {
			return ErrThreadBusy
		}
		if ee.Code == "version_mismatch" {
			return fmt.Errorf("%w: %s", ErrVersionMismatch, ee.Message)
		}
		if ee.Message != "" {
			return fmt.Errorf("thread start failed: %s", ee.Message)
		}
		return fmt.Errorf("thread start failed: %s", string(raw))
	}
	if event.Type == "event.thread_started" {
		data, _ := json.Marshal(event.Data)
		var started protocol.EventThreadStarted
		json.Unmarshal(data, &started)
		sc.threadID = started.ThreadID
		sc.whiteboardBase = started.WhiteboardBase
		if t, err := time.Parse(time.RFC3339, started.StartedAt); err == nil {
			sc.startedAt = t
		}
	}

	return nil
}

// SendInput sends user chat input with optional attachments.
func (sc *ThreadClient) SendInput(text string, attachments []protocol.Attachment) error {
	data, _ := json.Marshal(protocol.ThreadInputData{Text: text, Attachments: attachments})
	return sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.input",
		Data: data,
	})
}

// SendWorkflow sends a workflow execution request with a prompt.
func (sc *ThreadClient) SendWorkflow(name, text string) error {
	data, _ := json.Marshal(protocol.ThreadWorkflowData{Name: name, Text: text})
	return sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.workflow",
		Data: data,
	})
}

// SendWorkflowMessage enqueues a user message to be injected into the currently
// running workflow agent as soon as the current LLM turn ends.
func (sc *ThreadClient) SendWorkflowMessage(text string) error {
	data, _ := json.Marshal(protocol.ThreadWorkflowMessageData{Text: text})
	return sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.workflow_message",
		Data: data,
	})
}

// SendConfirm sends tool approval/denial.
func (sc *ThreadClient) SendConfirm(approved bool, persistDirs bool) error {
	data, _ := json.Marshal(protocol.ThreadConfirmData{Approved: approved, PersistDirs: persistDirs})
	return sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.confirm",
		Data: data,
	})
}

// SendPlanAction sends a plan review decision.
func (sc *ThreadClient) SendPlanAction(action string, text string) error {
	data, _ := json.Marshal(protocol.ThreadPlanActionData{Action: action, Text: text})
	return sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.plan_action",
		Data: data,
	})
}

// SendUserAnswer sends the user's answer to a question.
// The text parameter carries additional user input when has_user_input is used.
func (sc *ThreadClient) SendUserAnswer(answer string, text string) error {
	data, _ := json.Marshal(protocol.ThreadUserAnswerData{Answer: answer, Text: text})
	return sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.user_answer",
		Data: data,
	})
}

// SendUserAnswerBatch sends batch answers (question ID → answer) for multi-question mode.
func (sc *ThreadClient) SendUserAnswerBatch(answers map[string]string) error {
	data, _ := json.Marshal(protocol.ThreadUserAnswerData{Answers: answers})
	return sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.user_answer",
		Data: data,
	})
}

// SendSetModel requests that the daemon switch to a different LLM model.
func (sc *ThreadClient) SendSetModel(model string) error {
	data, _ := json.Marshal(protocol.ThreadSetModelData{Model: model})
	return sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.set_model",
		Data: data,
	})
}

// SendTrim instructs the daemon to trim the conversation history, keeping
// messages up to and including the turn at turnIdx (0-based).
func (sc *ThreadClient) SendTrim(turnIdx int) error {
	data, _ := json.Marshal(protocol.ThreadTrimData{TurnIdx: turnIdx})
	return sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.trim",
		Data: data,
	})
}

// SendMarkRead tells the daemon the user is viewing this thread: the
// persisted unread flag is cleared. Sent by the TUI when a thread gains
// focus and when a turn completes while focused.
func (sc *ThreadClient) SendMarkRead() error {
	return sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.mark_read",
	})
}

// SendCancel cancels the current work.
func (sc *ThreadClient) SendCancel() error {
	return sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.cancel",
	})
}

// SendUpdateQuit tells the daemon an in-app update finished installing: it
// broadcasts a quit to every attached vix instance and shuts itself down so the
// freshly-installed binaries take effect on relaunch.
func (sc *ThreadClient) SendUpdateQuit() error {
	return sc.sendCommand(protocol.ThreadCommand{
		Type: "update.quit",
	})
}

// SendClose ends the thread.
func (sc *ThreadClient) SendClose() error {
	err := sc.sendCommand(protocol.ThreadCommand{
		Type: "thread.close",
	})
	if sc.conn != nil {
		sc.conn.Close()
	}
	return err
}

// ReadEvent reads the next event from the daemon.
func (sc *ThreadClient) ReadEvent() (protocol.ThreadEvent, error) {
	if !sc.scanner.Scan() {
		err := sc.scanner.Err()
		if err == nil {
			err = fmt.Errorf("connection closed")
		}
		return protocol.ThreadEvent{}, err
	}

	var event protocol.ThreadEvent
	if err := json.Unmarshal(sc.scanner.Bytes(), &event); err != nil {
		return protocol.ThreadEvent{}, fmt.Errorf("parse event: %w", err)
	}
	return event, nil
}

// Close closes the underlying connection.
func (sc *ThreadClient) Close() {
	if sc.conn != nil {
		sc.conn.Close()
	}
}

func (sc *ThreadClient) sendCommand(cmd protocol.ThreadCommand) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.conn == nil {
		return fmt.Errorf("not connected")
	}

	// Stamp the auth token onto every outgoing command. This is the single
	// chokepoint for all thread messages (thread.start, thread.input,
	// thread.workflow, thread.confirm, thread.user_answer, …) so the
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
// how many vix processes are attached (independently of threads). The client
// sends a single instance.register command and then holds the connection open
// for the process lifetime; closing it (clean exit or process death) tells the
// daemon this instance is gone.
type InstanceClient struct {
	conn    net.Conn
	scanner *bufio.Scanner
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
	cmd := protocol.ThreadCommand{Type: "instance.register", AuthToken: authToken, Data: data}
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
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), protocol.MaxMessageSize)
	return &InstanceClient{conn: conn, scanner: scanner}, nil
}

// ReadEvent blocks until the daemon pushes the next process-level event
// (threads_changed, jobs_changed, quit) on the control channel, decoding one
// newline-delimited ThreadEvent frame. It returns an error when the connection
// closes, which the caller treats as the end of the control stream.
func (ic *InstanceClient) ReadEvent() (protocol.ThreadEvent, error) {
	if ic == nil || ic.scanner == nil {
		return protocol.ThreadEvent{}, fmt.Errorf("not connected")
	}
	if !ic.scanner.Scan() {
		err := ic.scanner.Err()
		if err == nil {
			err = fmt.Errorf("connection closed")
		}
		return protocol.ThreadEvent{}, err
	}
	var event protocol.ThreadEvent
	if err := json.Unmarshal(ic.scanner.Bytes(), &event); err != nil {
		return protocol.ThreadEvent{}, fmt.Errorf("parse event: %w", err)
	}
	return event, nil
}

// Close ends the instance registration, signalling the daemon that this process
// has detached.
func (ic *InstanceClient) Close() {
	if ic != nil && ic.conn != nil {
		ic.conn.Close()
	}
}
