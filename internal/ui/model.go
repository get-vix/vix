package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/ultraviolet/screen"

	"github.com/get-vix/vix/internal/auth"
	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon"
	"github.com/get-vix/vix/internal/protocol"
	"github.com/get-vix/vix/internal/telemetry"
)

// teaProgram holds the Bubble Tea program reference for event injection via Send().
var teaProgram *tea.Program

// SetProgram stores the tea.Program reference. Call before p.Run().
func SetProgram(p *tea.Program) { teaProgram = p }

// --- Internal message types ---

// sessionEventMsg carries a daemon session event tagged with the daemon session
// ID of the connection that produced it. Messages whose daemonSessionID no
// longer matches the session's current daemonSessionID are silently dropped
// (they came from a superseded connection's goroutine).
type sessionEventMsg struct {
	daemonSessionID string
	event           protocol.SessionEvent
}

// sessionDisconnectedMsg is sent when a session's daemon connection is lost.
type sessionDisconnectedMsg struct {
	daemonSessionID string
}

// updateInstallDoneMsg is delivered after the in-app update install command
// (run via tea.ExecProcess) finishes. A nil err means the new binaries are on
// disk and the user can quit-all to apply them.
type updateInstallDoneMsg struct {
	err error
}

// reconnectSuccessMsg is sent when reconnection succeeds.
// daemonSessionID is the ID of the session we were reconnecting for (the old
// one); client is the newly established connection with its own fresh ID.
type reconnectSuccessMsg struct {
	daemonSessionID string
	client          *daemon.SessionClient
}

// reconnectFailedMsg is sent when reconnection fails.
type reconnectFailedMsg struct {
	daemonSessionID string
}

// sessionOrphanedMsg is sent when an attach reconnect reports the session no
// longer exists on disk (lost in a daemon restart before its first flush). The
// session can't be continued; the handler orphans it and tells the user to
// /copy the conversation.
type sessionOrphanedMsg struct {
	daemonSessionID string
}

// resumeFromSleepMsg is sent when the process receives SIGCONT.
type resumeFromSleepMsg struct{}

// StatusMsgKind identifies the visual style of a transient status bar message.
type StatusMsgKind int

const (
	StatusMsgWarning StatusMsgKind = iota
	StatusMsgInfo
	StatusMsgError
)

// StatusMessage is a transient message shown on the second line of the status bar.
type StatusMessage struct {
	Text string
	Kind StatusMsgKind
	gen  int // stale-clear guard
}

// clearStatusMsgMsg clears the status bar message when its generation matches.
type clearStatusMsgMsg struct{ gen int }

// startCursorBlinkMsg triggers cursor blink on startup.
type startCursorBlinkMsg struct{}

func waitForResume() tea.Msg {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGCONT)
	<-sigCh
	signal.Stop(sigCh)
	return resumeFromSleepMsg{}
}

// startSessionEventLoop launches a goroutine that reads daemon events for one
// session and injects them into the Bubble Tea loop tagged with the daemon
// session ID captured at launch time. When a session reconnects it gets a new
// daemon session ID, so any in-flight messages from the old goroutine are
// naturally ignored by the handler's ID check — no generation counter needed.
func startSessionEventLoop(client *daemon.SessionClient) tea.Cmd {
	daemonSessionID := client.SessionID()
	return func() tea.Msg {
		if teaProgram == nil {
			return sessionDisconnectedMsg{daemonSessionID: daemonSessionID}
		}
		go func() {
			for {
				event, err := client.ReadEvent()
				if err != nil {
					teaProgram.Send(sessionDisconnectedMsg{daemonSessionID: daemonSessionID})
					return
				}
				teaProgram.Send(sessionEventMsg{daemonSessionID: daemonSessionID, event: event})
			}
		}()
		return nil
	}
}

// attemptReconnect tries to reconnect a session to the daemon.
// targetDaemonSessionID identifies which session this attempt is for; it is
// echoed back in the result message so the handler can match it to the right
// session. Pass an empty string for a session that has never connected — the
// handler will not retry on failure in that case.
func attemptReconnect(socketPath, cwd, configDir, model, authToken string, forceInit, enableWrite, enableDir bool, targetDaemonSessionID string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		if !client.Ping() {
			time.Sleep(2 * time.Second)
			return reconnectFailedMsg{daemonSessionID: targetDaemonSessionID}
		}
		session := daemon.NewSessionClient(socketPath)
		session.SetAuthToken(authToken)
		// A session that has connected before is resumed by ID (attach), so a
		// restarted daemon rebuilds it from disk. An empty target ID is a
		// brand-new session that has never connected — start it fresh.
		var err error
		if targetDaemonSessionID == "" {
			err = session.Connect(cwd, configDir, model, forceInit, enableWrite, enableDir, false)
		} else {
			err = session.Attach(cwd, configDir, model, forceInit, enableWrite, enableDir, false, targetDaemonSessionID)
			if errors.Is(err, daemon.ErrSessionNotFound) {
				// The daemon restarted and lost this session before it was
				// flushed. It can't be continued; orphan it (offer /copy).
				return sessionOrphanedMsg{daemonSessionID: targetDaemonSessionID}
			}
		}
		if err != nil {
			time.Sleep(2 * time.Second)
			return reconnectFailedMsg{daemonSessionID: targetDaemonSessionID}
		}
		return reconnectSuccessMsg{daemonSessionID: targetDaemonSessionID, client: session}
	}
}

// sessionRestoredMsg is sent when a persisted open session is successfully
// re-attached on launch. The handler adds a new SessionState for it.
type sessionRestoredMsg struct {
	summary protocol.SessionSummary
	client  *daemon.SessionClient
}

// sessionRestoreFailedMsg is sent when reopening a persisted session fails (the
// daemon is gone or the record vanished). The session is simply not restored.
type sessionRestoreFailedMsg struct {
	id string
}

// attachRestoreSession reopens a persisted session on launch by attaching to it
// by ID. Used for the open sessions beyond the first (which main attaches as the
// initial client).
func attachRestoreSession(socketPath, cwd, configDir, model, authToken string, enableWrite, enableDir bool, summary protocol.SessionSummary) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		if !client.Ping() {
			return sessionRestoreFailedMsg{id: summary.ID}
		}
		sc := daemon.NewSessionClient(socketPath)
		sc.SetAuthToken(authToken)
		if err := sc.Attach(cwd, configDir, model, false, enableWrite, enableDir, false, summary.ID); err != nil {
			return sessionRestoreFailedMsg{id: summary.ID}
		}
		return sessionRestoredMsg{summary: summary, client: sc}
	}
}

// connectFork starts a new forked session seeded from forkSessionID at forkTurnIdx.
func connectFork(socketPath, cwd, configDir, model, authToken string, enableWrite, enableDir bool, forkSessionID string, forkTurnIdx int, targetDaemonSessionID string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		if !client.Ping() {
			time.Sleep(2 * time.Second)
			return reconnectFailedMsg{daemonSessionID: targetDaemonSessionID}
		}
		session := daemon.NewSessionClient(socketPath)
		session.SetAuthToken(authToken)
		if err := session.ConnectFork(cwd, configDir, model, false, enableWrite, enableDir, false, forkSessionID, forkTurnIdx); err != nil {
			time.Sleep(2 * time.Second)
			return reconnectFailedMsg{daemonSessionID: targetDaemonSessionID}
		}
		return reconnectSuccessMsg{daemonSessionID: targetDaemonSessionID, client: session}
	}
}

// findSessionByDaemonID returns the index and pointer of the session with the
// given daemon session ID, or (-1, nil) if not found.
func (m *Model) findSessionByDaemonID(id string) (int, *SessionState) {
	for i, s := range m.sessions {
		if s.daemonSessionID == id {
			return i, s
		}
	}
	return -1, nil
}

// AppState represents the current state of the application.
type AppState int

const (
	StateWaitingForInput AppState = iota
	StateStreaming
	StateToolExecuting
	StateConfirmPending
	StatePlanReview
	StatePlanExecuting
	StateUserQuestion
	StateQuitConfirm
	StateTrimConfirm
	StateSessionCloseConfirm
	StateKeyDeleteConfirm
)

// modelsFocusArea identifies which area of the Models tab currently has the
// cursor: the provider list, the authentication panel, or the model grid.
type modelsFocusArea int

const (
	modelsFocusProviders modelsFocusArea = iota
	modelsFocusAuth
	modelsFocusModels
)

// pendingMsg holds a user message submitted while the agent was streaming.
type pendingMsg struct {
	text        string
	attachments []protocol.Attachment
}

// pendingPlanAction holds a plan action submitted while disconnected.
type pendingPlanAction struct {
	action string
	text   string
}

// Model is the root Bubble Tea model.
type Model struct {
	width, height int

	// Two visible tabs: Sessions list and Chat display.
	activeTab TabKind

	// All active sessions. Each accumulates messages independently.
	sessions        []*SessionState
	selectedSession int // index into sessions; which session the Chat tab shows

	// Global overlay dialog state (quit confirm, session close confirm).
	// Normal operation = StateWaitingForInput (no overlay).
	state                AppState
	quitSelected         int
	quitCloseAll         bool // quit-dialog checkbox: close all sessions on quit
	sessionCloseIdx      int
	sessionCloseSelected int

	// Sessions tab UI
	sessionsSelected int

	// Models tab UI
	modelsLoggedIn         []string                             // providers with a stored credential
	modelsAvailable        []string                             // providers without one
	modelsStatus           map[string]config.ProviderAuthStatus // per-provider auth status (refreshed on change)
	modelsProviderSel      int                                  // index into modelsLoggedIn ++ modelsAvailable
	modelsFocus            modelsFocusArea                      // which Models-tab area has the cursor
	modelsAuthRow          int                                  // credential-method row index (focus == auth)
	modelsAuthBtn          int                                  // button index within the focused auth row
	modelsModelSel         int                                  // index into the filtered model list for the selected provider
	modelsModelScroll      int                                  // index of the top visible grid row (windowed scrolling)
	modelsFilter           string                               // live type-to-filter query for the model grid
	modelsModelPending     string                               // model spec awaiting a credential
	modelsInKeyInput       bool                                 // key-entry popup open
	modelsKeyInputProvider string                               // provider the popup is entering a key for
	modelsKeyInputMethodID string                               // credential method the popup is entering a key for
	modelsKeyInputLabel    string                               // method label for the popup title
	modelsKeyInput         textinput.Model                      // popup text input (holds the real key value)
	modelsKeyInputBaseURL  bool                                 // popup also collects a user-supplied base URL
	modelsBaseURLInput     textinput.Model                      // popup base-URL input (RequiresBaseURL methods)
	modelsKeyInputFocus    int                                  // 0 = key field, 1 = base-URL field
	modelsLoginStatus      string                               // transient OAuth login progress/result text

	// Models tab credential-delete confirmation (driven by StateKeyDeleteConfirm)
	keyDeleteProvider string
	keyDeleteKind     string // "api_key" | "oauth"
	keyDeleteMethodID string // credential method id (api_key deletes)
	keyDeleteSelected int    // 0 = Yes, 1 = No

	// Shared rendering
	mdRenderer     *MarkdownRenderer
	commandPalette CommandPalette

	// lastChatWidth records the effective (panel-aware) chat width the markdown
	// renderer and cached messages were last reconciled at. reconcileChatWidth
	// uses it to detect panel/session/resize transitions and re-flow once.
	lastChatWidth int

	// Tab alert blink (Chat tab label pulses when a session needs attention)
	tabAlertActive   bool
	tabAlertBlinkOn  bool
	tabAlertBlinkGen int

	// Sessions-list loading spinner. A single shared ticker animates the
	// per-row indicator for sessions that are actively working. It runs only
	// while the Sessions tab is the active view AND at least one session is
	// busy, so it never animates for sessions the user can't see.
	sessionsSpinnerActive bool
	sessionsSpinnerStep   int
	sessionsSpinnerGen    int // bumped on stop; invalidates in-flight ticks

	// sessionsTabUnseen marks that a message arrived while the Sessions tab was
	// not focused; it tints the Sessions tab title secondary (static, not
	// blinking) and is cleared when the user visits the Sessions tab.
	sessionsTabUnseen bool

	// Transient status bar message (second line)
	statusMsg StatusMessage

	// Connection parameters (for reconnect / new sessions)
	socketPath                     string
	cwd                            string
	authToken                      string
	forceInit                      bool
	enableAutomaticWritePermission bool
	enableAutomaticDirectoryAccess bool

	// Global settings
	hasDarkBG      bool
	styles         Styles
	kittySupported bool
	cfg            *config.Config
	testMode       bool
	settingsCursor int // selected row in the Settings tab

	// Update status (from the daemon's daily release check, via
	// event.update_available) and in-app upgrade flow state.
	updateCurrent   string // running version
	updateLatest    string // newer release tag, "" when up-to-date/unknown
	updateURL       string
	updateMethod    string // "brew" | "script" | "unknown"
	updateInstalled bool   // install command completed successfully
	updateErr       string // last install error, if any

	// restoreSessions holds persisted open sessions (beyond the first, which is
	// the initial client) to reopen on Init.
	restoreSessions []protocol.SessionSummary
}

// SetRestoreSessions records the persisted open sessions the TUI should reopen
// on launch (attached lazily from Init). Called once by main before the program
// starts.
func (m *Model) SetRestoreSessions(s []protocol.SessionSummary) {
	m.restoreSessions = s
}

// SetInitialAwaitingReplay marks the initial session as one that was attached
// (restored) on launch and is still waiting for its event.replay. While true the
// chat area shows a "Restoring conversation…" placeholder instead of the welcome
// screen. Called once by main before the program starts.
func (m *Model) SetInitialAwaitingReplay(awaiting bool) {
	if awaiting && len(m.sessions) > 0 {
		m.sessions[0].awaitingReplay = true
	}
}

// currentSession returns the selected session, or nil if there is none.
func (m *Model) currentSession() *SessionState {
	if m.selectedSession < 0 || m.selectedSession >= len(m.sessions) {
		return nil
	}
	return m.sessions[m.selectedSession]
}

// NewModel creates a new root Model.
func NewModel(cfg *config.Config, client *daemon.SessionClient, testMode bool, authToken string, enableWrite, enableDir bool) Model {
	initialSession := newSessionState(cfg, client)

	m := Model{
		state:                          StateWaitingForInput,
		activeTab:                      TabKindChat,
		sessions:                       []*SessionState{initialSession},
		selectedSession:                0,
		commandPalette:                 NewCommandPalette(),
		hasDarkBG:                      true,
		styles:                         NewStyles(true),
		mdRenderer:                     NewMarkdownRenderer(80, true, NewStyles(true).CodeBoxBorderStyle),
		cfg:                            cfg,
		socketPath:                     cfg.SocketPath,
		cwd:                            cfg.CWD,
		forceInit:                      cfg.ForceInit,
		authToken:                      authToken,
		enableAutomaticWritePermission: enableWrite,
		enableAutomaticDirectoryAccess: enableDir,
		testMode:                       testMode,
	}

	if testMode {
		m.fillTestData()
	}

	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	if m.testMode {
		return nil
	}
	var cmds []tea.Cmd
	cmds = append(cmds, func() tea.Msg { return startCursorBlinkMsg{} })
	if sess := m.currentSession(); sess != nil && sess.client != nil {
		cmds = append(cmds, startSessionEventLoop(sess.client))
		// A restored initial session shows the "Restoring conversation…"
		// placeholder until its replay arrives; animate its spinner.
		if sess.awaitingReplay {
			cmds = append(cmds, sess.thinkingAnim.Start())
		}
	}
	// Reopen any persisted open sessions beyond the initial one.
	for _, sum := range m.restoreSessions {
		cmds = append(cmds, attachRestoreSession(m.socketPath, m.cwd, m.cfg.ConfigDir, m.cfg.Model, m.authToken, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, sum))
	}
	cmds = append(cmds, waitForResume, tea.RequestBackgroundColor)
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
// Update is the central tea.Model update entry point. It delegates to updateInner
// (the real message handler) and then reconciles the panel-aware chat width on
// the resulting model, so panel open/close, session switches, and resizes all
// re-flow width-cached content without each transition remembering to do so.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.updateInner(msg)
	if mm, ok := model.(Model); ok {
		mm.reconcileChatWidth()
		return mm, cmd
	}
	return model, cmd
}

func (m Model) updateInner(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		sess := m.currentSession()
		if sess != nil {
			sess.input.SetWidth(m.width - 4)
			sess.questionPanel.SetWidth(m.width)
		}
		m.updateChatWidth()
		return m, nil

	case tea.KeyPressMsg:
		// --- Global quit confirm overlay ---
		if msg.String() == "ctrl+c" || msg.String() == "ctrl+d" {
			if m.state == StateQuitConfirm {
				m.closeSessionsForQuit(m.quitCloseAll)
				return m, tea.Quit
			}
			m.state = StateQuitConfirm
			m.quitSelected = 0
			m.quitCloseAll = config.CloseAllSessionsOnQuit()
			return m, nil
		}

		// --- Quit / SessionClose / Trim dialogs intercept all keys ---
		if m.state == StateQuitConfirm || m.state == StateSessionCloseConfirm {
			return m.handleDialogKey(msg)
		}
		if m.state == StateKeyDeleteConfirm {
			return m.handleKeyDeleteKey(msg)
		}
		sess := m.currentSession()
		if sess != nil && sess.agentState == StateTrimConfirm {
			return m.handleTrimKey(msg)
		}

		// --- History panel (Chat tab only) ---
		if m.activeTab == TabKindChat && sess != nil && sess.historyPanel.IsVisible() {
			switch msg.String() {
			case "up", "k":
				sess.historyPanel.MoveUp()
			case "down", "j":
				sess.historyPanel.MoveDown(len(sess.history.entries))
			case "enter":
				if sess.historyPanel.selected >= 0 && sess.historyPanel.selected < len(sess.history.entries) {
					sess.input.Reset()
					sess.input.InsertString(sess.history.entries[sess.historyPanel.selected])
					sess.input.SetHeight(m.visualLineCount())
				}
				sess.historyPanel.Close()
			case "esc":
				sess.historyPanel.Close()
			default:
				sess.historyPanel.Close()
			}
			return m, nil
		}

		// --- Right panel (Chat tab only) ---
		if m.activeTab == TabKindChat && sess != nil && sess.rightPanel.IsVisible() && sess.focus == FocusRightPanel {
			if msg.String() == "tab" {
				sess.focus = FocusEditor
				sess.input.Focus()
				return m, nil
			}
			if sess.rightPanel.HandleKey(msg) == rpActionClose {
				sess.rightPanel.Close()
				m.updateChatWidth()
				sess.input.Focus()
				sess.focus = FocusEditor
			}
			return m, nil
		}

		// --- Command palette ---
		if m.commandPalette.IsVisible() {
			action, _ := m.commandPalette.Update(msg)
			cmds = append(cmds, m.handleCommandAction(action, sess)...)
			if !m.commandPalette.IsVisible() && sess != nil && sess.focus != FocusRightPanel && m.activeTab != TabKindSessions && m.activeTab != TabKindModels && m.activeTab != TabKindSettings {
				sess.input.Focus()
				sess.focus = FocusEditor
			}
			return m, tea.Batch(cmds...)
		}

		// --- Tab switching (F1–F4), shared across all tabs/focus ---
		switch msg.String() {
		case "f1":
			return m, m.switchTab(TabKindSessions)
		case "f2":
			return m, m.switchTab(TabKindChat)
		case "f3":
			return m, m.switchTab(TabKindModels)
		case "f4":
			return m, m.switchTab(TabKindSettings)
		}

		// --- Global workspace shortcuts ---
		switch msg.String() {
		case "ctrl+n":
			if m.selectedSession < len(m.sessions)-1 {
				m.selectedSession++
				m.activeTab = TabKindChat
				selSess := m.sessions[m.selectedSession]
				m.markSessionRead(selSess)
				selSess.input.SetWidth(m.width - 4)
				if selSess.client == nil && !selSess.reconnecting {
					selSess.reconnecting = true
					cmds = append(cmds, attemptReconnect(m.socketPath, m.cwd, m.cfg.ConfigDir, m.cfg.Model, m.authToken, false, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, selSess.daemonSessionID))
				}
				cmds = append(cmds, selSess.thinkingAnim.Resume())
				if !m.hasAlertSessions() {
					m.stopTabAlertBlink()
				}
			} else if curSess := m.currentSession(); curSess != nil {
				return m, m.emitStatusMsg("No next session", StatusMsgWarning)
			}
			return m, tea.Batch(cmds...)

		case "ctrl+p":
			if m.selectedSession > 0 {
				m.selectedSession--
				m.activeTab = TabKindChat
				selSess := m.sessions[m.selectedSession]
				m.markSessionRead(selSess)
				selSess.input.SetWidth(m.width - 4)
				if selSess.client == nil && !selSess.reconnecting {
					selSess.reconnecting = true
					cmds = append(cmds, attemptReconnect(m.socketPath, m.cwd, m.cfg.ConfigDir, m.cfg.Model, m.authToken, false, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, selSess.daemonSessionID))
				}
				cmds = append(cmds, selSess.thinkingAnim.Resume())
				if !m.hasAlertSessions() {
					m.stopTabAlertBlink()
				}
			} else if curSess := m.currentSession(); curSess != nil {
				return m, m.emitStatusMsg("No previous session", StatusMsgWarning)
			}
			return m, tea.Batch(cmds...)

		case "ctrl+t":
			newSess := newSessionState(m.cfg, nil)
			newSess.input.SetWidth(m.width - 4)
			newSess.reconnecting = true
			newIdx := len(m.sessions)
			m.sessions = append(m.sessions, newSess)
			m.selectedSession = newIdx
			m.activeTab = TabKindChat
			cmds = append(cmds, attemptReconnect(m.socketPath, m.cwd, m.cfg.ConfigDir, m.cfg.Model, m.authToken, false, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, newSess.daemonSessionID))
			return m, tea.Batch(cmds...)

		}

		// --- Sessions tab key handling ---
		if m.activeTab == TabKindSessions {
			switch msg.String() {
			case "up":
				if m.sessionsSelected > 0 {
					m.sessionsSelected--
				}
				return m, nil
			case "down":
				if n := m.sessionsVisibleCount(); m.sessionsSelected < n-1 {
					m.sessionsSelected++
				}
				return m, nil
			case "enter":
				if idx, ok := m.sessionsSelectedIdx(); ok {
					m.selectedSession = idx
					m.activeTab = TabKindChat
					selSess := m.sessions[idx]
					m.markSessionRead(selSess)
					selSess.input.SetWidth(m.width - 4)
					if selSess.client == nil && !selSess.reconnecting {
						selSess.reconnecting = true
						cmds = append(cmds, attemptReconnect(m.socketPath, m.cwd, m.cfg.ConfigDir, m.cfg.Model, m.authToken, false, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, selSess.daemonSessionID))
					}
					cmds = append(cmds, selSess.thinkingAnim.Resume())
					if !m.hasAlertSessions() {
						m.stopTabAlertBlink()
					}
				}
				return m, tea.Batch(cmds...)
			case "t":
				// Add a new session
				newSess := newSessionState(m.cfg, nil)
				newSess.input.SetWidth(m.width - 4)
				newSess.reconnecting = true
				newIdx := len(m.sessions)
				m.sessions = append(m.sessions, newSess)
				m.selectedSession = newIdx
				m.activeTab = TabKindChat
				cmds = append(cmds, attemptReconnect(m.socketPath, m.cwd, m.cfg.ConfigDir, m.cfg.Model, m.authToken, false, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, newSess.daemonSessionID))
				return m, tea.Batch(cmds...)
			case "d":
				// Duplicate the selected session into a new one.
				idx, ok := m.sessionsSelectedIdx()
				if !ok {
					return m, nil
				}
				srcSess := m.sessions[idx]
				if srcSess.client == nil {
					return m, m.emitStatusMsg("Session is still connecting; cannot duplicate", StatusMsgWarning)
				}
				seps := turnSeparatorInfos(srcSess.chatMessages, m.styles, m.mdRenderer.width)
				if len(seps) == 0 {
					return m, m.emitStatusMsg("Nothing to duplicate: no completed turns yet", StatusMsgWarning)
				}
				lastSep := seps[len(seps)-1]
				nm, c := m.doDuplicate(srcSess, lastSep)
				return nm, c
			case "x":
				if idx, ok := m.sessionsSelectedIdx(); ok {
					m.sessionCloseIdx = idx
					m.sessionCloseSelected = 1 // default No
					m.state = StateSessionCloseConfirm
				}
				return m, nil
			}
		}

		// --- Models tab key handling ---
		if m.activeTab == TabKindModels {
			return m.handleModelsKey(msg)
		}

		// --- Settings tab key handling ---
		if m.activeTab == TabKindSettings {
			switch msg.String() {
			case "up", "k":
				if m.settingsCursor > 0 {
					m.settingsCursor--
				}
			case "down", "j":
				if m.settingsCursor < int(settingsItemCount)-1 {
					m.settingsCursor++
				}
			case "enter", " ":
				if settingsItem(m.settingsCursor) == settingUpdateAction {
					if cmd := m.handleUpdateAction(); cmd != nil {
						cmds = append(cmds, cmd)
					}
				} else {
					m.toggleSetting(settingsItem(m.settingsCursor))
				}
			case "left", "h":
				if settingsItem(m.settingsCursor) == settingCompactionThreshold {
					m.adjustCompactionThreshold(-0.05)
				}
				if settingsItem(m.settingsCursor) == settingClosedRetention {
					m.adjustClosedRetention(-1)
				}
			case "right", "l":
				if settingsItem(m.settingsCursor) == settingCompactionThreshold {
					m.adjustCompactionThreshold(0.05)
				}
				if settingsItem(m.settingsCursor) == settingClosedRetention {
					m.adjustClosedRetention(1)
				}
			}
			return m, tea.Batch(cmds...)
		}

		// --- Chat tab key handling (session-specific) ---
		if sess == nil {
			return m, nil
		}

		// Attachment panel intercepts keys when focused
		if sess.attachmentPanel.IsFocused() {
			switch msg.String() {
			case "up", "k":
				sess.attachmentPanel.MoveUp()
			case "down", "j":
				sess.attachmentPanel.MoveDown()
			case "delete", "backspace":
				sess.attachmentPanel.Remove(sess.attachmentPanel.selected)
			case "enter":
				// prevent submit
			case "tab":
				sess.attachmentPanel.Unfocus()
				sess.focus = FocusChat
				sess.input.Blur()
			case "esc":
				sess.attachmentPanel.Unfocus()
				sess.focus = FocusEditor
				sess.input.Focus()
			default:
				sess.attachmentPanel.Unfocus()
				sess.input.Focus()
				goto processKey
			}
			return m, nil
		}
	processKey:

		// Slash menu
		if sess.slashMenu.IsVisible() {
			switch msg.String() {
			case "up":
				sess.slashMenu.MoveUp()
				return m, nil
			case "down":
				sess.slashMenu.MoveDown()
				return m, nil
			case "esc":
				sess.slashMenu.Close()
				return m, nil
			case "enter", "tab":
				action := sess.slashMenu.SelectedAction()
				sess.slashMenu.Close()
				// Parameterized commands are inserted into the input (with a
				// trailing space) so the user can type the turn number, rather
				// than executing immediately.
				if insert, ok := slashCommandInsertText(action); ok {
					sess.input.SetValue(insert)
					sess.input.MoveToEnd()
					sess.input.SetHeight(1)
					if sess.focus != FocusRightPanel && m.activeTab != TabKindSessions && m.activeTab != TabKindModels && m.activeTab != TabKindSettings {
						sess.input.Focus()
						sess.focus = FocusEditor
					}
					return m, nil
				}
				sess.input.SetValue("")
				sess.input.SetHeight(1)
				if action != "" {
					cmds = append(cmds, m.handleCommandAction(action, sess)...)
				}
				if sess.focus != FocusRightPanel && m.activeTab != TabKindSessions && m.activeTab != TabKindModels && m.activeTab != TabKindSettings {
					sess.input.Focus()
					sess.focus = FocusEditor
				}
				return m, tea.Batch(cmds...)
			}
		}

		// File completer
		if sess.fileCompleter.IsVisible() {
			switch msg.String() {
			case "up":
				sess.fileCompleter.MoveUp()
				return m, nil
			case "down":
				sess.fileCompleter.MoveDown()
				return m, nil
			case "esc":
				sess.fileCompleter.Close()
				return m, nil
			case "enter", "tab":
				entry := sess.fileCompleter.SelectedEntry()
				if entry == nil {
					sess.fileCompleter.Close()
					return m, nil
				}
				if entry.IsDir() {
					sess.fileCompleter.Descend(entry)
					newPath := "@" + sess.fileCompleter.currentDir + "/"
					sess.input.SetValue(replaceAtToken(sess.input.Value(), newPath))
					sess.input.MoveToEnd()
				} else {
					path := sess.fileCompleter.SelectedPath()
					sess.input.SetValue(replaceAtToken(sess.input.Value(), path))
					sess.input.MoveToEnd()
					sess.fileCompleter.Close()
				}
				newHeight := m.visualLineCount()
				if newHeight != sess.input.Height() {
					sess.input.SetHeight(newHeight)
				}
				return m, nil
			}
		}

		// Tab key: focus cycling
		if msg.String() == "tab" {
			if sess.agentState == StateWaitingForInput || sess.agentState == StatePlanReview ||
				sess.agentState == StateUserQuestion || sess.agentState == StateStreaming ||
				sess.agentState == StateToolExecuting || sess.agentState == StateConfirmPending {
				switch sess.focus {
				case FocusEditor:
					if sess.attachmentPanel.IsVisible() {
						sess.attachmentPanel.Focus()
						sess.input.Blur()
					} else {
						sess.focus = FocusChat
						sess.input.Blur()
					}
				case FocusChat:
					if sess.rightPanel.IsVisible() {
						sess.focus = FocusRightPanel
						sess.input.Blur()
					} else {
						sess.focus = FocusEditor
						sess.input.Focus()
					}
				case FocusRightPanel:
					sess.focus = FocusEditor
					sess.input.Focus()
				}
			}
			return m, nil
		}

		// Question / confirm panel
		if (sess.agentState == StateUserQuestion || sess.agentState == StateConfirmPending) &&
			sess.questionPanel.IsVisible() && sess.focus == FocusEditor {
			result, answer, batchAnswers := sess.questionPanel.HandleKey(msg)
			switch result {
			case QPSubmitted:
				if sess.agentState == StateConfirmPending {
					approved := answer == "Yes, allow" || answer == "Allow once" || answer == "Allow and remember"
					persistDirs := answer == "Allow and remember"
					question := sess.questionPanel.CurrentTab().Question
					pairs := []QAPair{{Category: "Permission", Question: question, Answer: answer}}
					sess.chatMessages = append(sess.chatMessages, renderQuestionAnswer(pairs, m.styles))
					if sess.client != nil {
						sess.client.SendConfirm(approved, persistDirs)
					}
					sess.questionPanel.Close()
					sess.agentState = StateToolExecuting
					return m, sess.thinkingAnim.Start()
				}
				if batchAnswers != nil {
					pairs := sess.questionPanel.GetAnsweredPairs()
					sess.chatMessages = append(sess.chatMessages, renderQuestionAnswer(pairs, m.styles))
					if sess.client != nil {
						sess.client.SendUserAnswerBatch(batchAnswers)
					}
				} else {
					answerText := sess.questionPanel.CurrentAnswerText()
					tab := sess.questionPanel.CurrentTab()
					displayAnswer := answer
					if answerText != "" {
						displayAnswer = answer + ": " + answerText
					}
					pairs := []QAPair{{Category: tab.Category, Question: tab.Question, Answer: displayAnswer}}
					sess.chatMessages = append(sess.chatMessages, renderQuestionAnswer(pairs, m.styles))
					if sess.client != nil {
						sess.client.SendUserAnswer(answer, answerText)
					}
				}
				sess.questionPanel.Close()
				sess.agentState = StateStreaming
				return m, sess.thinkingAnim.Start()
			case QPCancelled:
				if sess.agentState == StateConfirmPending {
					pairs := []QAPair{{Category: "Permission", Question: sess.questionPanel.CurrentTab().Question, Answer: "Deny"}}
					sess.chatMessages = append(sess.chatMessages, renderQuestionAnswer(pairs, m.styles))
					if sess.client != nil {
						sess.client.SendConfirm(false, false)
					}
					sess.questionPanel.Close()
					sess.agentState = StateToolExecuting
					return m, sess.thinkingAnim.Start()
				}
				if sess.client != nil {
					sess.client.SendUserAnswer("", "")
				}
				sess.questionPanel.Close()
				sess.agentState = StateStreaming
				return m, sess.thinkingAnim.Start()
			}
			return m, nil
		}

		// Shift+Enter / Alt+Enter: newline
		if msg.String() == "shift+enter" || msg.String() == "alt+enter" || msg.String() == "ctrl+j" {
			if sess.agentState == StateWaitingForInput || sess.agentState == StatePlanReview ||
				sess.agentState == StateStreaming || sess.agentState == StateToolExecuting || sess.agentState == StatePlanExecuting {
				sess.input.InsertString("\n")
				newHeight := m.visualLineCount()
				if newHeight != sess.input.Height() {
					sess.input.SetHeight(newHeight)
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "ctrl+shift+u":
			if sess.agentState == StateWaitingForInput || sess.agentState == StatePlanReview ||
				sess.agentState == StateStreaming || sess.agentState == StateToolExecuting || sess.agentState == StatePlanExecuting {
				sess.input.SetValue("")
				sess.input.SetHeight(1)
			}
			return m, nil

		case "ctrl+r":
			if sess.agentState == StateWaitingForInput && len(sess.history.entries) > 0 {
				sess.historyPanel.Open(len(sess.history.entries), m.height)
			}
			return m, nil

		case "shift+tab":
			if sess.agentState == StateWaitingForInput && len(sess.workflows) > 0 {
				sess.activeWorkflow = m.nextWorkflow(sess)
				sess.input.Placeholder = m.placeholderForMode(sess)
				m.updateInputPromptColor(sess)
				return m, m.emitStatusMsg("Context is not shared between Chat and workflows", StatusMsgInfo)
			}
			return m, nil

		case "enter":
			return m.handleEnter(sess)

		case "y", "Y":
			if sess.agentState == StatePlanReview && sess.input.Value() == "" {
				if sess.reconnecting {
					sess.pendingPlanAction = &pendingPlanAction{action: "approve"}
					return m, nil
				}
				if sess.client != nil {
					sess.client.SendPlanAction("approve", "")
				}
				sess.agentState = StateStreaming
				return m, sess.thinkingAnim.Start()
			}

		case "esc":
			if sess.agentState == StateStreaming || sess.agentState == StateToolExecuting || sess.agentState == StatePlanExecuting {
				sess.thinkingAnim.Stop()
				sess.pendingInput = nil
				if sess.client != nil {
					sess.client.SendCancel()
				}
				m.flushSessionBuf(sess)
				sess.chatMessages = append(sess.chatMessages, renderSystemMessage("Cancelled.", m.styles))
				return m, nil
			}
			if sess.agentState == StatePlanReview && sess.input.Value() == "" {
				if sess.reconnecting {
					sess.pendingPlanAction = &pendingPlanAction{action: "reject"}
					return m, nil
				}
				if sess.client != nil {
					sess.client.SendPlanAction("reject", "")
				}
				sess.agentState = StateWaitingForInput
				sess.input.Focus()
				return m, nil
			}

		case "n", "N":
			if sess.agentState == StatePlanReview && sess.input.Value() == "" {
				if sess.reconnecting {
					sess.pendingPlanAction = &pendingPlanAction{action: "reject"}
					return m, nil
				}
				if sess.client != nil {
					sess.client.SendPlanAction("reject", "")
				}
				sess.agentState = StateWaitingForInput
				sess.input.Focus()
				return m, nil
			}
		}

		// Chat viewport focus: scroll keys
		if sess.focus == FocusChat {
			switch msg.String() {
			case "up", "k":
				sess.chatScrollOffset += 3
			case "down", "j":
				sess.chatScrollOffset -= 3
			case "pgup", "b":
				sess.chatScrollOffset += 20
			case "pgdown", "f":
				sess.chatScrollOffset -= 20
			case "home", "g":
				sess.chatScrollOffset = m.sessionMaxScrollOffset(sess)
			case "end", "G":
				sess.chatScrollOffset = 0
			}
			m.clampScrollOffset(sess)
			return m, nil
		}

		if sess.agentState == StateWaitingForInput || sess.agentState == StatePlanReview ||
			sess.agentState == StateStreaming || sess.agentState == StateToolExecuting || sess.agentState == StatePlanExecuting {
			if msg.String() == "up" && sess.agentState == StateWaitingForInput &&
				sess.input.Line() == 0 && sess.input.Column() == 0 && len(sess.history.entries) > 0 {
				sess.historyPanel.Open(len(sess.history.entries), m.height)
				return m, nil
			}
			var cmd tea.Cmd
			sess.input, cmd = sess.input.Update(msg)

			query, found := extractAtQuery(sess.input.Value())
			if found {
				dir, prefix := resolveAtDir(query, m.cwd)
				if sess.fileCompleter.IsVisible() && dir == sess.fileCompleter.currentDir {
					sess.fileCompleter.Refresh(prefix)
				} else {
					sess.fileCompleter.Open(dir, prefix)
				}
			} else {
				sess.fileCompleter.Close()
			}

			slashQuery, slashFound := extractSlashQuery(sess.input.Value())
			if slashFound {
				if sess.slashMenu.IsVisible() {
					sess.slashMenu.Refresh(slashQuery)
				} else {
					sess.slashMenu.Open(sessionSlashCommands(sess), slashQuery)
				}
			} else {
				sess.slashMenu.Close()
			}

			newHeight := m.visualLineCount()
			if newHeight != sess.input.Height() {
				sess.input.SetHeight(newHeight)
			}
			return m, cmd
		}

		return m, nil

	// --- Session daemon events ---
	case sessionEventMsg:
		idx, sess := m.findSessionByDaemonID(msg.daemonSessionID)
		if sess != nil {
			evCmds := m.applyEventToSession(idx, msg.event)
			cmds = append(cmds, evCmds...)
			cmds = append(cmds, m.maybeStartTabAlertBlink())
			cmds = append(cmds, m.maybeStartSessionsSpinner())
		}
		return m, tea.Batch(cmds...)

	case loginStatusMsg:
		// Ignore status updates for a provider the user has navigated away from,
		// so a pending OAuth callback can't repaint a stale message.
		if msg.provider == m.modelsSelectedProvider() {
			m.modelsLoginStatus = msg.text
		}
		return m, nil

	case loginDoneMsg:
		if msg.err == nil {
			m.refreshModelsProviders()
		}
		// Only surface the result if the relevant provider is still selected.
		if msg.provider == m.modelsSelectedProvider() {
			if msg.err != nil {
				m.modelsLoginStatus = "Login failed: " + msg.err.Error()
			} else {
				m.modelsLoginStatus = "Logged in to " + msg.provider + "."
			}
		}
		return m, nil

	case updateInstallDoneMsg:
		if msg.err != nil {
			m.updateErr = msg.err.Error()
			m.updateInstalled = false
		} else {
			m.updateInstalled = true
			m.updateErr = ""
		}
		return m, nil

	case sessionDisconnectedMsg:
		_, sess := m.findSessionByDaemonID(msg.daemonSessionID)
		if sess != nil {
			if sess.closing {
				// Expected disconnect: the TUI sent session.close (quit flow).
				// Don't reconnect — attaching again would resurrect the
				// session the daemon just closed.
				return m, nil
			}
			sess.reconnecting = true
			sess.pendingInput = nil
			// If the connection dropped before the replay arrived, abandon the
			// restoring placeholder so we don't spin forever.
			sess.awaitingReplay = false
			sess.thinkingAnim.Stop()
			sess.chatMessages = append(sess.chatMessages, renderErrorMessage(fmt.Errorf("daemon connection lost")))
			if sess.agentState != StatePlanReview {
				sess.agentState = StateWaitingForInput
			}
			cmds = append(cmds, attemptReconnect(m.socketPath, m.cwd, m.cfg.ConfigDir, m.cfg.Model, m.authToken, m.forceInit, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, msg.daemonSessionID))
		}
		return m, tea.Batch(cmds...)

	case reconnectSuccessMsg:
		_, sess := m.findSessionByDaemonID(msg.daemonSessionID)
		if sess == nil {
			// Session was closed while the reconnect goroutine was in flight.
			// Close the new client to avoid leaking a daemon-side session.
			msg.client.Close()
			return m, nil
		}
		// Close the previous client before replacing it so the old event-loop
		// goroutine unblocks and exits cleanly.
		if sess.client != nil {
			sess.client.Close()
		}
		sess.client = msg.client
		sess.daemonSessionID = msg.client.SessionID()
		sess.reconnecting = false
		if len(sess.chatMessages) > 0 {
			sess.chatMessages = append(sess.chatMessages, renderSystemSuccessMessage("Reconnected to daemon."))
		}
		if sess.pendingPlanAction != nil {
			pending := sess.pendingPlanAction
			sess.pendingPlanAction = nil
			sess.client.SendPlanAction(pending.action, pending.text)
			sess.agentState = StateStreaming
			return m, tea.Batch(startSessionEventLoop(msg.client), sess.thinkingAnim.Start())
		}
		return m, startSessionEventLoop(msg.client)

	case reconnectFailedMsg:
		// Don't retry if the session has never successfully connected — there
		// is no stable daemonSessionID to match against, and a brand-new
		// session that failed its first attempt should not loop indefinitely.
		if msg.daemonSessionID == "" {
			return m, nil
		}
		_, sess := m.findSessionByDaemonID(msg.daemonSessionID)
		if sess != nil && sess.reconnecting {
			return m, attemptReconnect(m.socketPath, m.cwd, m.cfg.ConfigDir, m.cfg.Model, m.authToken, m.forceInit, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, msg.daemonSessionID)
		}
		return m, nil

	case sessionOrphanedMsg:
		_, sess := m.findSessionByDaemonID(msg.daemonSessionID)
		if sess == nil {
			return m, nil
		}
		sess.reconnecting = false
		sess.orphaned = true
		sess.awaitingReplay = false
		sess.client = nil
		sess.pendingInput = nil
		sess.pendingPlanAction = nil
		sess.agentState = StateWaitingForInput
		sess.thinkingAnim.Stop()
		sess.chatMessages = append(sess.chatMessages, renderErrorMessage(fmt.Errorf("This conversation was lost when the daemon restarted and can't be continued. Use /copy to save it before it's gone.")))
		return m, nil

	case sessionRestoredMsg:
		// A persisted open session was re-attached on launch. Add it as a new
		// session; its viewport is rebuilt from the daemon's event.replay.
		if _, existing := m.findSessionByDaemonID(msg.summary.ID); existing != nil {
			msg.client.Close()
			return m, nil
		}
		restored := newSessionState(m.cfg, msg.client)
		if msg.summary.Model != "" {
			restored.setModel(msg.summary.Model)
		}
		// Restored sessions are waiting for their replay; show the placeholder
		// (with an animated spinner) until it arrives.
		restored.awaitingReplay = true
		// Restore attaches run concurrently and complete in arbitrary order, so
		// insert by creation time instead of appending: place the session before
		// the first one that started later, keeping the list in the order the
		// user started the conversations.
		idx := len(m.sessions)
		for i, s := range m.sessions {
			if s.client != nil && s.client.StartedAt().After(msg.client.StartedAt()) {
				idx = i
				break
			}
		}
		m.sessions = append(m.sessions, nil)
		copy(m.sessions[idx+1:], m.sessions[idx:])
		m.sessions[idx] = restored
		if idx <= m.selectedSession {
			m.selectedSession++
		}
		m.syncSessionsSelected()
		return m, tea.Batch(startSessionEventLoop(msg.client), restored.thinkingAnim.Start())

	case sessionRestoreFailedMsg:
		// Best-effort: a persisted session could not be reopened. Leave it on
		// disk; it will be offered again on the next launch.
		return m, nil

	case tea.PasteMsg:
		if m.activeTab == TabKindModels && m.modelsInKeyInput {
			if m.modelsKeyInputBaseURL && m.modelsKeyInputFocus == 1 {
				m.modelsBaseURLInput, _ = m.modelsBaseURLInput.Update(msg)
			} else {
				m.modelsKeyInput, _ = m.modelsKeyInput.Update(msg)
			}
			return m, nil
		}
		sess := m.currentSession()
		if sess == nil {
			return m, nil
		}
		if sess.agentState == StateWaitingForInput || sess.agentState == StatePlanReview ||
			sess.agentState == StateStreaming || sess.agentState == StateToolExecuting || sess.agentState == StatePlanExecuting {
			var cmd tea.Cmd
			sess.input, cmd = sess.input.Update(msg)
			val := sess.input.Value()
			_, atts, _ := extractImageAttachments(val)
			if len(atts) > 0 {
				for i := range atts {
					sess.attachmentPanel.Add(atts[i])
				}
				stripped := imagePathPattern.ReplaceAllString(val, "")
				stripped = strings.TrimSpace(stripped)
				sess.input.SetValue(stripped)
			}
			newHeight := m.visualLineCount()
			if newHeight != sess.input.Height() {
				sess.input.SetHeight(newHeight)
			}
			sess.input.MoveToBegin()
			sess.input.MoveToEnd()
			return m, cmd
		}

	case tea.KeyboardEnhancementsMsg:
		m.kittySupported = msg.SupportsKeyDisambiguation()

	case tea.BackgroundColorMsg:
		m.hasDarkBG = msg.IsDark()
		m.styles = NewStyles(m.hasDarkBG)
		m.mdRenderer = NewMarkdownRenderer(m.mdRenderer.width, m.hasDarkBG, m.styles.CodeBoxBorderStyle)
		return m, nil

	case resumeFromSleepMsg:
		return m, tea.Batch(tea.ClearScreen, tea.RequestWindowSize, waitForResume)

	case clearStatusMsgMsg:
		if msg.gen == m.statusMsg.gen {
			m.statusMsg = StatusMessage{}
		}
		return m, nil

	case startCursorBlinkMsg:
		sess := m.currentSession()
		if sess != nil {
			blinkCmd := sess.input.Focus()
			return m, blinkCmd
		}
		return m, nil

	case animStepMsg:
		// Route to whichever session owns this generation tick.
		for _, sess := range m.sessions {
			if cmd := sess.thinkingAnim.Advance(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)

	case tabBlinkMsg:
		if msg.gen != m.tabAlertBlinkGen {
			return m, nil
		}
		m.tabAlertBlinkOn = !m.tabAlertBlinkOn
		if m.hasAlertSessions() {
			return m, m.tabBlinkTick()
		}
		m.tabAlertActive = false
		m.tabAlertBlinkOn = false
		m.tabAlertBlinkGen++
		return m, nil

	case sessionsSpinnerMsg:
		if msg.gen != m.sessionsSpinnerGen {
			return m, nil
		}
		m.sessionsSpinnerStep++
		// Re-gate every frame: keep ticking only while the list is visible and
		// work is ongoing, otherwise stop (and bump gen so this loop dies).
		if m.activeTab == TabKindSessions && m.hasBusySessions() {
			return m, m.sessionsSpinnerTick()
		}
		m.stopSessionsSpinner()
		return m, nil
	}

	// Forward unhandled messages to the active input for cursor blink
	sess := m.currentSession()
	if sess != nil {
		var cmd tea.Cmd
		sess.input, cmd = sess.input.Update(msg)
		if cmd != nil {
			return m, cmd
		}
	}
	return m, nil
}

// hasUnreadSessions reports whether any session still has unread agent activity.
func (m *Model) hasUnreadSessions() bool {
	for _, s := range m.sessions {
		if s.unreadCount > 0 {
			return true
		}
	}
	return false
}

// markSessionRead clears a session's unread counter and, once no session has
// unread activity left, lowers the Sessions-tab highlight latch. This lets the
// highlight clear when the last unread conversation is opened directly, without
// having to visit the Sessions tab.
func (m *Model) markSessionRead(sess *SessionState) {
	sess.unreadCount = 0
	if !m.hasUnreadSessions() {
		m.sessionsTabUnseen = false
	}
}

// switchTab changes the active tab and performs per-tab entry side effects,
// returning any command to run (e.g. resuming the chat thinking animation).
func (m *Model) switchTab(k TabKind) tea.Cmd {
	m.activeTab = k
	if k != TabKindSessions {
		// Leaving the Sessions tab: no reason to animate a list nobody sees.
		m.stopSessionsSpinner()
	}
	switch k {
	case TabKindSessions:
		m.sessionsTabUnseen = false
		m.syncSessionsSelected()
		return m.maybeStartSessionsSpinner()
	case TabKindChat:
		if sess := m.currentSession(); sess != nil {
			m.markSessionRead(sess)
			return sess.thinkingAnim.Resume()
		}
	case TabKindModels:
		m.enterModelsTab()
	}
	return nil
}

// enterModelsTab initializes Models-tab state on entry: refreshes provider
// credential status and places the cursor on the provider that owns the active
// model.
func (m *Model) enterModelsTab() {
	m.modelsFocus = modelsFocusProviders
	m.modelsAuthRow = 0
	m.modelsAuthBtn = 0
	m.modelsModelPending = ""
	m.modelsInKeyInput = false
	m.modelsLoginStatus = ""
	m.modelsFilter = ""
	m.modelsModelScroll = 0
	m.refreshModelsProviders()
	active := m.activeModelSpec()
	prov := ProviderOf(active)
	m.modelsProviderSel = m.providerFlatIndex(prov)
	m.modelsModelSel = modelIndexForActive(prov, active)
	m.clampModelsScroll()
}

// credClient returns a connection-level daemon client for credential RPCs.
// Credentials are daemon-owned: the TUI never touches the keychain/auth.json
// directly, it asks the daemon to store/read them (config_dir-agnostic, since
// credentials are user-global).
func (m *Model) credClient() *daemon.Client {
	c := daemon.NewClient(m.socketPath)
	c.SetAuthToken(m.authToken)
	return c
}

// providerHasCredential reports whether a provider has any stored/available
// credential, preferring the cached status and falling back to a fresh daemon
// query on a cache miss.
func (m *Model) providerHasCredential(provider string) bool {
	if st, ok := m.modelsStatus[provider]; ok {
		return st.HasCredential()
	}
	if cs, err := m.credClient().ProviderCredStatus(); err == nil {
		m.modelsStatus = cs.Providers
		return cs.Providers[provider].HasCredential()
	}
	return false
}

// refreshModelsProviders recomputes the logged-in / available provider split and
// per-provider auth status, clamping the provider cursor to the new bounds. The
// status is read from the daemon (the credential owner); on RPC failure the
// last-known status is reused so the panel doesn't flicker to "no credential".
func (m *Model) refreshModelsProviders() {
	// Snapshot the provider under the cursor before the split is rebuilt.
	// Granting or removing a credential moves a provider between the
	// logged-in and available groups, which reorders the flat list; without
	// re-anchoring, the positional cursor would land on a different provider.
	prevProvider := m.modelsSelectedProvider()

	m.modelsLoggedIn = m.modelsLoggedIn[:0]
	m.modelsAvailable = m.modelsAvailable[:0]
	if m.modelsStatus == nil {
		m.modelsStatus = map[string]config.ProviderAuthStatus{}
	}
	if cs, err := m.credClient().ProviderCredStatus(); err == nil {
		m.modelsStatus = cs.Providers
	}
	for _, p := range AvailableProviders() {
		st := m.modelsStatus[p.Name]
		if st.HasCredential() {
			m.modelsLoggedIn = append(m.modelsLoggedIn, p.Name)
		} else {
			m.modelsAvailable = append(m.modelsAvailable, p.Name)
		}
	}
	if prevProvider != "" {
		m.modelsProviderSel = m.providerFlatIndex(prevProvider)
	}
	total := len(m.modelsLoggedIn) + len(m.modelsAvailable)
	if m.modelsProviderSel >= total {
		m.modelsProviderSel = total - 1
	}
	if m.modelsProviderSel < 0 {
		m.modelsProviderSel = 0
	}
}

// modelsFlat returns the provider names in display order (logged in, then
// available) — the order the provider cursor navigates.
func (m *Model) modelsFlat() []string {
	return append(append([]string{}, m.modelsLoggedIn...), m.modelsAvailable...)
}

// modelsSelectedProvider returns the provider name under the provider cursor.
func (m *Model) modelsSelectedProvider() string {
	flat := m.modelsFlat()
	if m.modelsProviderSel >= 0 && m.modelsProviderSel < len(flat) {
		return flat[m.modelsProviderSel]
	}
	return ""
}

// providerFlatIndex returns the index of provider in the flat provider list, or
// 0 when not found.
func (m *Model) providerFlatIndex(provider string) int {
	for i, p := range m.modelsFlat() {
		if p == provider {
			return i
		}
	}
	return 0
}

// modelIndexForActive returns the grid index of spec within a provider's models,
// or 0 when absent.
func modelIndexForActive(provider, spec string) int {
	for i, mod := range DisplayModelsForProvider(provider) {
		if mod.Spec == spec {
			return i
		}
	}
	return 0
}

// activeModelSpec returns the model spec currently in effect for the active
// session, falling back to the configured default.
func (m *Model) activeModelSpec() string {
	spec := m.cfg.Model
	if sess := m.currentSession(); sess != nil && sess.modelName != "" {
		spec = sess.modelName
	}
	return spec
}

// applyModelSelection makes spec the default chat model and pushes it to the
// active session (and daemon) when connected.
func (m *Model) applyModelSelection(spec string) {
	m.cfg.Model = spec
	if sess := m.currentSession(); sess != nil {
		sess.setModel(spec)
		if sess.client != nil {
			_ = sess.client.SendSetModel(spec)
		}
	}
}

// openModelsKeyInput opens the credential-entry popup for a specific credential
// method of a provider. When the method carries a user-supplied endpoint
// (RequiresBaseURL), the popup also collects a base URL, prefilled with any
// stored value for update.
func (m *Model) openModelsKeyInput(provider, methodID string) {
	st := m.modelsStatus[provider]
	var ms config.MethodStatus
	for _, c := range st.Methods {
		if c.ID == methodID {
			ms = c
			break
		}
	}

	ti := textinput.New()
	ti.Placeholder = "Paste your " + DisplayNameForProvider(provider) + " API key..."
	ti.Focus()
	m.modelsKeyInput = ti
	m.modelsKeyInputProvider = provider
	m.modelsKeyInputMethodID = methodID
	m.modelsKeyInputLabel = ms.Label
	if m.modelsKeyInputLabel == "" {
		m.modelsKeyInputLabel = "API Key"
	}
	m.modelsKeyInputBaseURL = ms.RequiresBaseURL
	m.modelsKeyInputFocus = 0
	if ms.RequiresBaseURL {
		bi := textinput.New()
		bi.Placeholder = "https://…/v1 (from your subscription page)"
		bi.SetValue(ms.BaseURL)
		m.modelsBaseURLInput = bi
	}
	m.modelsInKeyInput = true
}

// clampModelsAuth keeps the focused auth row and button index within range after
// the method/button set changes (e.g. a credential was added/removed or made
// default).
func (m *Model) clampModelsAuth() {
	st := m.modelsStatus[m.modelsSelectedProvider()]
	if m.modelsAuthRow >= len(st.Methods) {
		m.modelsAuthRow = len(st.Methods) - 1
	}
	if m.modelsAuthRow < 0 {
		m.modelsAuthRow = 0
	}
	btns := m.authButtonsForRow(st, m.modelsAuthRow)
	if m.modelsAuthBtn >= len(btns) {
		m.modelsAuthBtn = len(btns) - 1
	}
	if m.modelsAuthBtn < 0 {
		m.modelsAuthBtn = 0
	}
}

// authButtonsForRow returns the buttons for the credential-method row at index
// row, or nil when out of range.
func (m *Model) authButtonsForRow(st config.ProviderAuthStatus, row int) []authButton {
	if row < 0 || row >= len(st.Methods) {
		return nil
	}
	return authButtonsFor(st.Methods[row])
}

// handleModelsKey handles all key input for the Models tab.
func (m Model) handleModelsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// Credential-entry popup intercepts all keys while open.
	if m.modelsInKeyInput {
		switch msg.String() {
		case "esc":
			m.modelsInKeyInput = false
			m.modelsModelPending = ""
		case "tab", "shift+tab":
			if m.modelsKeyInputBaseURL {
				m.modelsKeyInputFocus ^= 1
				if m.modelsKeyInputFocus == 1 {
					m.modelsKeyInput.Blur()
					m.modelsBaseURLInput.Focus()
				} else {
					m.modelsBaseURLInput.Blur()
					m.modelsKeyInput.Focus()
				}
			}
		case "enter":
			val := strings.TrimSpace(m.modelsKeyInput.Value())
			baseURL := ""
			if m.modelsKeyInputBaseURL {
				baseURL = strings.TrimSpace(m.modelsBaseURLInput.Value())
			}
			if val == "" {
				m.modelsLoginStatus = "API key is required."
				return m, tea.Batch(cmds...)
			}
			if m.modelsKeyInputBaseURL && baseURL == "" {
				m.modelsLoginStatus = "Base URL is required for this plan."
				return m, tea.Batch(cmds...)
			}
			if _, err := m.credClient().StoreProviderMethodKey(m.modelsKeyInputProvider, m.modelsKeyInputMethodID, val, baseURL); err != nil {
				m.modelsLoginStatus = "Could not store credential: " + err.Error()
				m.modelsInKeyInput = false
				return m, tea.Batch(cmds...)
			}
			m.modelsLoginStatus = ""
			m.modelsInKeyInput = false
			m.refreshModelsProviders()
			m.clampModelsAuth()
			if m.modelsModelPending != "" {
				m.applyModelSelection(m.modelsModelPending)
			}
			m.modelsModelPending = ""
		default:
			var cmd tea.Cmd
			if m.modelsKeyInputBaseURL && m.modelsKeyInputFocus == 1 {
				m.modelsBaseURLInput, cmd = m.modelsBaseURLInput.Update(msg)
			} else {
				m.modelsKeyInput, cmd = m.modelsKeyInput.Update(msg)
			}
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	}

	switch m.modelsFocus {
	case modelsFocusProviders:
		switch msg.String() {
		case "up", "k":
			if m.modelsProviderSel > 0 {
				m.modelsProviderSel--
				m.modelsModelSel = 0
				m.modelsModelScroll = 0
				m.modelsFilter = ""
				m.modelsAuthRow = 0
				m.modelsAuthBtn = 0
				m.modelsLoginStatus = ""
			}
		case "down", "j":
			if m.modelsProviderSel < len(m.modelsFlat())-1 {
				m.modelsProviderSel++
				m.modelsModelSel = 0
				m.modelsModelScroll = 0
				m.modelsFilter = ""
				m.modelsAuthRow = 0
				m.modelsAuthBtn = 0
				m.modelsLoginStatus = ""
			}
		case "right", "l", "enter", "tab":
			m.modelsFocus = modelsFocusAuth
			m.modelsAuthRow = 0
			m.modelsAuthBtn = 0
		}
	case modelsFocusAuth:
		st := m.modelsStatus[m.modelsSelectedProvider()]
		switch msg.String() {
		case "left", "h":
			if m.modelsAuthBtn > 0 {
				m.modelsAuthBtn--
			} else {
				m.modelsFocus = modelsFocusProviders
			}
		case "right", "l":
			if btns := m.authButtonsForRow(st, m.modelsAuthRow); m.modelsAuthBtn < len(btns)-1 {
				m.modelsAuthBtn++
			}
		case "up", "k":
			if m.modelsAuthRow > 0 {
				m.modelsAuthRow--
				m.modelsAuthBtn = 0
			} else {
				m.modelsFocus = modelsFocusProviders
			}
		case "down", "j":
			if m.modelsAuthRow < len(st.Methods)-1 {
				m.modelsAuthRow++
				m.modelsAuthBtn = 0
			} else {
				m.modelsFocus = modelsFocusModels
				m.modelsModelSel = 0
			}
		case "tab":
			m.modelsFocus = modelsFocusModels
			m.modelsModelSel = 0
		case "enter":
			return m.activateAuthButton()
		}
	case modelsFocusModels:
		models := FilterModels(DisplayModelsForProvider(m.modelsSelectedProvider()), m.modelsFilter)
		switch msg.String() {
		case "up":
			if m.modelsModelSel >= modelGridCols {
				m.modelsModelSel -= modelGridCols
			} else {
				m.modelsFocus = modelsFocusAuth
			}
		case "down":
			if m.modelsModelSel+modelGridCols < len(models) {
				m.modelsModelSel += modelGridCols
			}
		case "left":
			if m.modelsModelSel%modelGridCols > 0 {
				m.modelsModelSel--
			}
		case "right":
			if m.modelsModelSel%modelGridCols < modelGridCols-1 && m.modelsModelSel+1 < len(models) {
				m.modelsModelSel++
			}
		case "tab":
			m.modelsFocus = modelsFocusProviders
		case "enter":
			if m.modelsModelSel >= 0 && m.modelsModelSel < len(models) {
				return m.selectModel(models[m.modelsModelSel])
			}
		case "esc":
			if m.modelsFilter != "" {
				m.modelsFilter = ""
				m.modelsModelSel = 0
				m.modelsModelScroll = 0
			}
		case "backspace":
			if m.modelsFilter != "" {
				r := []rune(m.modelsFilter)
				m.modelsFilter = string(r[:len(r)-1])
				m.modelsModelSel = 0
				m.modelsModelScroll = 0
			}
		default:
			// Type-to-filter: printable text narrows the grid. msg.Text is empty
			// for non-text keys (arrows, modifiers), so navigation is unaffected.
			if t := msg.Text; t != "" {
				m.modelsFilter += t
				m.modelsModelSel = 0
				m.modelsModelScroll = 0
			}
		}
		m.clampModelsScroll()
	}
	return m, tea.Batch(cmds...)
}

// clampModelsScroll keeps the model-grid scroll offset so the selected model
// stays within the visible window. It mirrors the renderer's row math via the
// shared modelsGridRows helper.
func (m *Model) clampModelsScroll() {
	st := m.modelsStatus[m.modelsSelectedProvider()]
	gridRows := modelsGridRows(m.modelsViewportHeight(), st, m.modelsLoginStatus)
	selRow := m.modelsModelSel / modelGridCols
	if selRow < m.modelsModelScroll {
		m.modelsModelScroll = selRow
	}
	if selRow >= m.modelsModelScroll+gridRows {
		m.modelsModelScroll = selRow - gridRows + 1
	}
	if m.modelsModelScroll < 0 {
		m.modelsModelScroll = 0
	}
}

// modelsViewportHeight returns the Models-tab viewport height, matching the
// value View() passes to renderModelsView (full height minus the tab bar and
// status bar).
func (m Model) modelsViewportHeight() int {
	h := m.height - 5 // tab bar (3) + status bar (2)
	if h < 1 {
		h = 1
	}
	return h
}

// selectModel applies the chosen model when its provider has a resolvable
// credential, otherwise opens the key popup (for the provider's default method)
// and remembers the pending model.
func (m Model) selectModel(mod ModelInfo) (tea.Model, tea.Cmd) {
	if m.providerHasCredential(mod.Provider) {
		m.applyModelSelection(mod.Spec)
		return m, nil
	}
	m.modelsModelPending = mod.Spec
	st := m.modelsStatus[mod.Provider]
	methodID := st.Default()
	if methodID == "" && len(st.Methods) > 0 {
		methodID = st.Methods[0].ID
	}
	m.openModelsKeyInput(mod.Provider, methodID)
	return m, nil
}

// activateAuthButton runs the action of the focused authentication button.
func (m Model) activateAuthButton() (tea.Model, tea.Cmd) {
	provider := m.modelsSelectedProvider()
	if provider == "" {
		return m, nil
	}
	st := m.modelsStatus[provider]
	if m.modelsAuthRow < 0 || m.modelsAuthRow >= len(st.Methods) {
		return m, nil
	}
	method := st.Methods[m.modelsAuthRow]
	btns := authButtonsFor(method)
	if m.modelsAuthBtn < 0 || m.modelsAuthBtn >= len(btns) {
		return m, nil
	}
	switch btns[m.modelsAuthBtn].id {
	case "set_key":
		m.openModelsKeyInput(provider, method.ID)
	case "del_key":
		m.keyDeleteProvider = provider
		m.keyDeleteKind = "api_key"
		m.keyDeleteMethodID = method.ID
		m.keyDeleteSelected = 1
		m.state = StateKeyDeleteConfirm
	case "default_key":
		_, _ = m.credClient().SetProviderAuthDefault(provider, method.ID)
		m.refreshModelsProviders()
		m.clampModelsAuth()
	case "set_token":
		if ProviderSupportsLogin(provider) {
			m.modelsLoginStatus = "Starting " + provider + " login…"
			startProviderLogin(provider)
		}
	case "del_token":
		m.keyDeleteProvider = provider
		m.keyDeleteKind = "oauth"
		m.keyDeleteMethodID = method.ID
		m.keyDeleteSelected = 1
		m.state = StateKeyDeleteConfirm
	case "default_token":
		_, _ = m.credClient().SetProviderAuthDefault(provider, method.ID)
		m.refreshModelsProviders()
		m.clampModelsAuth()
	}
	return m, nil
}

// handleKeyDeleteKey handles keys for the credential-deletion confirm dialog.
func (m Model) handleKeyDeleteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "right", "tab":
		if m.keyDeleteSelected == 0 {
			m.keyDeleteSelected = 1
		} else {
			m.keyDeleteSelected = 0
		}
	case "enter":
		if m.keyDeleteSelected == 0 {
			m.doKeyDelete()
		}
		m.state = StateWaitingForInput
	case "y", "Y":
		m.doKeyDelete()
		m.state = StateWaitingForInput
	case "n", "N", "esc":
		m.state = StateWaitingForInput
	}
	return m, nil
}

// doKeyDelete removes the credential targeted by the confirm dialog and refreshes
// the provider status.
func (m *Model) doKeyDelete() {
	switch m.keyDeleteKind {
	case "api_key":
		_, _ = m.credClient().DeleteProviderMethodKey(m.keyDeleteProvider, m.keyDeleteMethodID)
	case "oauth":
		if loginID, ok := oauthLoginID(m.keyDeleteProvider); ok {
			_ = auth.DefaultStorage().Remove(loginID)
		}
		_, _ = m.credClient().SetProviderAuthDefault(m.keyDeleteProvider, "")
	}
	m.refreshModelsProviders()
	m.clampModelsAuth()
}

// handleDialogKey handles keys for the global quit/session-close dialogs.
func (m Model) handleDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "right", "tab":
		if m.state == StateQuitConfirm {
			if m.quitSelected == 0 {
				m.quitSelected = 1
			} else {
				m.quitSelected = 0
			}
		} else {
			if m.sessionCloseSelected == 0 {
				m.sessionCloseSelected = 1
			} else {
				m.sessionCloseSelected = 0
			}
		}
	case "enter":
		if m.state == StateQuitConfirm {
			if m.quitSelected == 0 {
				m.closeSessionsForQuit(m.quitCloseAll)
				return m, tea.Quit
			}
			m.state = StateWaitingForInput
		} else {
			if m.sessionCloseSelected == 0 {
				return m.doCloseSession(m.sessionCloseIdx)
			}
			m.state = StateWaitingForInput
		}
	case "space", " ":
		if m.state == StateQuitConfirm {
			m.quitCloseAll = !m.quitCloseAll
			_ = config.SetCloseAllSessionsOnQuit(m.quitCloseAll)
		}
	case "y", "Y":
		if m.state == StateQuitConfirm {
			m.closeSessionsForQuit(m.quitCloseAll)
			return m, tea.Quit
		}
		if m.state == StateSessionCloseConfirm {
			return m.doCloseSession(m.sessionCloseIdx)
		}
	case "n", "N", "esc":
		m.state = StateWaitingForInput
	}
	return m, nil
}

// handleTrimKey handles keys for the per-session trim confirm dialog.
func (m Model) handleTrimKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	sess := m.currentSession()
	if sess == nil {
		return m, nil
	}
	switch msg.String() {
	case "left", "right", "tab":
		if sess.trimSelected == 0 {
			sess.trimSelected = 1
		} else {
			sess.trimSelected = 0
		}
	case "enter":
		if sess.trimSelected == 0 {
			return m.doTrim(sess.trimSep)
		}
		sess.agentState = sess.trimPrevState
	case "y", "Y":
		return m.doTrim(sess.trimSep)
	case "n", "N", "esc":
		sess.agentState = sess.trimPrevState
	}
	return m, nil
}

// handleEnter handles the Enter key in the Chat tab.
func (m Model) handleEnter(sess *SessionState) (tea.Model, tea.Cmd) {
	if sess.agentState == StateConfirmPending {
		if sess.client != nil {
			sess.client.SendConfirm(true, false)
		}
		sess.agentState = StateToolExecuting
		return m, sess.thinkingAnim.Start()
	}

	if sess.agentState == StatePlanReview {
		text := strings.TrimSpace(sess.input.Value())
		action := "approve"
		if text != "" {
			action = "modify"
		}
		if sess.reconnecting {
			sess.pendingPlanAction = &pendingPlanAction{action: action, text: text}
			if text != "" {
				sess.input.Reset()
				sess.input.SetHeight(1)
			}
			return m, nil
		}
		if text == "" {
			if sess.client != nil {
				sess.client.SendPlanAction("approve", "")
			}
			sess.agentState = StateStreaming
		} else {
			sess.input.Reset()
			sess.input.SetHeight(1)
			if sess.client != nil {
				sess.client.SendPlanAction("modify", text)
			}
			sess.agentState = StateStreaming
		}
		return m, sess.thinkingAnim.Start()
	}

	if sess.agentState == StateStreaming || sess.agentState == StateToolExecuting || sess.agentState == StatePlanExecuting {
		text := strings.TrimSpace(sess.input.Value())
		if text == "" && sess.attachmentPanel.Count() == 0 {
			return m, nil
		}
		if text != "" {
			sess.history.Save(text)
		}
		sess.input.Reset()
		sess.input.SetHeight(1)

		panelAtts := sess.attachmentPanel.Clear()
		displayText, textAtts, imgErrs := extractImageAttachments(text)
		for _, e := range imgErrs {
			sess.chatMessages = append(sess.chatMessages, renderErrorMessage(fmt.Errorf("%s", e)))
		}
		attachments := append(panelAtts, textAtts...)

		sess.chatMessages = append(sess.chatMessages, renderUserMessage(displayText, m.mdRenderer.width))
		sess.chatScrollOffset = 0

		if sess.activeWorkflow != "" && !strings.HasPrefix(displayText, "/") && sess.agentState != StatePlanExecuting {
			if sess.client != nil {
				sess.client.SendWorkflowMessage(displayText)
			}
		} else {
			sess.pendingInput = &pendingMsg{text: displayText, attachments: attachments}
			if sess.client != nil {
				sess.client.SendCancel()
			}
		}
		return m, nil
	}

	if sess.agentState == StateWaitingForInput {
		text := strings.TrimSpace(sess.input.Value())
		if text == "" && sess.attachmentPanel.Count() == 0 {
			return m, nil
		}

		// Client-side slash commands (/fork, /trim, /copy) act on the local
		// conversation and are never sent to the daemon.
		if handled, model, cmd := m.tryLocalCommand(sess); handled {
			return model, cmd
		}

		// Orphaned sessions have no daemon-side history and can't be continued.
		// Local commands above (e.g. /copy) still work; anything else is refused
		// with a reminder rather than spinning forever with no daemon.
		if sess.orphaned {
			sess.chatMessages = append(sess.chatMessages, renderSystemMessage("This conversation can't be continued. Use /copy to save it.", m.styles))
			return m, nil
		}

		if text != "" {
			sess.history.Save(text)
		}
		sess.input.Reset()
		sess.input.SetHeight(1)

		panelAtts := sess.attachmentPanel.Clear()
		displayText, textAtts, imgErrs := extractImageAttachments(text)
		for _, e := range imgErrs {
			sess.chatMessages = append(sess.chatMessages, renderErrorMessage(fmt.Errorf("%s", e)))
		}
		attachments := append(panelAtts, textAtts...)

		sess.chatMessages = append(sess.chatMessages, renderUserMessage(displayText, m.mdRenderer.width))
		sess.chatScrollOffset = 0

		sess.agentState = StateStreaming
		animCmd := sess.thinkingAnim.Start()

		if sess.client != nil {
			telemetry.TrackTurn(sess.modelName)
			if sess.activeWorkflow != "" && !strings.HasPrefix(displayText, "/") {
				sess.client.SendWorkflow(sess.activeWorkflow, displayText)
			} else {
				sess.client.SendInput(displayText, attachments)
			}
		}
		return m, animCmd
	}
	return m, nil
}

// applyEventToSession processes a single daemon event for the session at idx.
func (m *Model) applyEventToSession(idx int, event protocol.SessionEvent) []tea.Cmd {
	sess := m.sessions[idx]
	var cmds []tea.Cmd

	switch event.Type {
	case "event.session_started":
		data := marshalData(event.Data)
		var started protocol.EventSessionStarted
		json.Unmarshal(data, &started)
		sess.parentID = started.ParentID
		sess.forkTurnIdx = started.ForkTurnIdx

	case "event.replay":
		data := marshalData(event.Data)
		var rep protocol.EventReplay
		json.Unmarshal(data, &rep)
		m.applyReplay(sess, rep)
		// The viewport is now rebuilt; drop the restoring placeholder and stop
		// its spinner.
		sess.awaitingReplay = false
		sess.thinkingAnim.Stop()

	case "event.init_state":
		data := marshalData(event.Data)
		var state protocol.EventInitState
		json.Unmarshal(data, &state)
		sess.initState = protocol.InitState(state.State)
		if state.Model != "" {
			sess.setModel(state.Model)
		}

	case "event.workflows_available":
		data := marshalData(event.Data)
		var wa protocol.EventWorkflowsAvailable
		json.Unmarshal(data, &wa)
		sess.workflows = wa.Workflows
		if sess.activeWorkflow != "" {
			found := false
			for _, w := range sess.workflows {
				if w.Name == sess.activeWorkflow {
					found = true
					break
				}
			}
			if !found {
				sess.activeWorkflow = ""
			}
		}

	case "event.skills_available":
		data := marshalData(event.Data)
		var sa protocol.EventSkillsAvailable
		json.Unmarshal(data, &sa)
		sess.skills = sa.Skills

	case "event.update_available":
		data := marshalData(event.Data)
		var ua protocol.EventUpdateAvailable
		json.Unmarshal(data, &ua)
		m.updateCurrent = ua.Current
		m.updateLatest = ua.Latest
		m.updateURL = ua.URL
		m.updateMethod = ua.Method

	case "event.stream_chunk":
		data := marshalData(event.Data)
		var chunk protocol.EventStreamChunk
		json.Unmarshal(data, &chunk)
		sess.assistantBuf += chunk.Text
		sess.assistantRendered = m.mdRenderer.Render(sess.assistantBuf)

	case "event.thinking_chunk":
		data := marshalData(event.Data)
		var chunk protocol.EventThinkingChunk
		json.Unmarshal(data, &chunk)
		sess.thinkingBuf += chunk.Text
		if sess.showThinking {
			sess.thinkingRendered = renderThinkingText(sess.thinkingBuf, m.styles, m.mdRenderer.width+4)
		}

	case "event.stream_done":
		data := marshalData(event.Data)
		var done protocol.EventStreamDone
		json.Unmarshal(data, &done)
		sess.inputTokens += done.InputTokens
		sess.outputTokens += done.OutputTokens
		sess.cacheCreationTokens += done.CacheCreationTokens
		sess.cacheReadTokens += done.CacheReadTokens
		if done.ElapsedMs > 0 {
			sess.lastOutputTokens = done.OutputTokens
			sess.elapsed = time.Duration(done.ElapsedMs) * time.Millisecond
		}
		sess.lastInputTokens = done.InputTokens + done.CacheReadTokens + done.CacheCreationTokens

	case "event.tool_call":
		m.flushSessionBuf(sess)
		sess.agentState = StateToolExecuting
		data := marshalData(event.Data)
		var tc protocol.EventToolCall
		json.Unmarshal(data, &tc)
		chatIdx := len(sess.chatMessages)
		sess.chatMessages = append(sess.chatMessages, renderToolCall(tc.Name, tc.Summary, tc.Reason,
			[4]string{tc.ReasonNotReadFile, tc.ReasonNotEditFile, tc.ReasonNotGlobFiles, tc.ReasonToIncreaseTimeout}, m.styles))
		if tc.ToolID != "" {
			if sess.pendingTools == nil {
				sess.pendingTools = make(map[string]int)
			}
			sess.pendingTools[tc.ToolID] = chatIdx
		}

	case "event.tool_result":
		data := marshalData(event.Data)
		var tr protocol.EventToolResult
		json.Unmarshal(data, &tr)
		detail := tr.Detail
		if sess.confirmDetailShown && tr.Name == sess.confirmToolName {
			detail = ""
			sess.confirmDetailShown = false
		}
		result := renderToolResultWithContext(tr.Name, tr.Output, tr.IsError, false, detail, m.styles, m.mdRenderer, m.mdRenderer.width)

		if tr.ToolID != "" && sess.pendingTools != nil {
			if callIdx, ok := sess.pendingTools[tr.ToolID]; ok {
				insertIdx := callIdx + 1
				delete(sess.pendingTools, tr.ToolID)
				if insertIdx <= len(sess.chatMessages) {
					sess.chatMessages = append(sess.chatMessages, ChatMessage{})
					copy(sess.chatMessages[insertIdx+1:], sess.chatMessages[insertIdx:])
					sess.chatMessages[insertIdx] = result
					for id, idx2 := range sess.pendingTools {
						if idx2 >= insertIdx {
							sess.pendingTools[id] = idx2 + 1
						}
					}
				} else {
					sess.chatMessages = append(sess.chatMessages, result)
				}
			} else {
				sess.chatMessages = append(sess.chatMessages, result)
			}
		} else {
			sess.chatMessages = append(sess.chatMessages, result)
		}

	case "event.confirm_request":
		sess.agentState = StateConfirmPending
		data := marshalData(event.Data)
		var cr protocol.EventConfirmRequest
		json.Unmarshal(data, &cr)
		sess.confirmToolName = cr.ToolName
		sess.confirmDetailShown = false
		sess.thinkingAnim.Stop()
		if cr.Detail != "" {
			sess.chatMessages = append(sess.chatMessages,
				renderToolResultWithContext(cr.ToolName, "", false, false, cr.Detail, m.styles, m.mdRenderer, m.mdRenderer.width))
			sess.confirmDetailShown = true
		}
		question := buildConfirmQuestion(cr.ToolName, cr.Params)
		if len(cr.RequestedDirs) > 0 {
			question = buildDirAccessQuestion(cr.RequestedDirs)
		}
		sess.chatMessages = append(sess.chatMessages,
			renderQuestionMessage("Permission", question, m.mdRenderer.width+4, m.mdRenderer))
		sess.questionPanel.OpenConfirm(cr.ToolName, cr.Params, cr.RequestedDirs, m.width, m.mdRenderer)
		sess.focus = FocusEditor

	case "event.user_question":
		data := marshalData(event.Data)
		var uq protocol.EventUserQuestion
		json.Unmarshal(data, &uq)
		sess.questionPanel.Open(uq, m.width, m.mdRenderer)
		sess.agentState = StateUserQuestion
		sess.thinkingAnim.Stop()
		sess.focus = FocusEditor
		sess.input.Blur()

	case "event.todo_list_updated":
		data := marshalData(event.Data)
		var tu protocol.EventTodoListUpdated
		json.Unmarshal(data, &tu)
		sess.todos = tu.Todos
		switch sess.rightPanel.mode {
		case rpModeWorkflow:
			// Todos render below workflow steps automatically.
		case rpModeTodos:
			if !hasPendingTodos(sess.todos) {
				sess.rightPanel.Close()
				m.updateChatWidth()
			}
		default:
			if !sess.rightPanel.IsVisible() && hasPendingTodos(sess.todos) {
				sess.rightPanel.OpenTodos(m.height)
				m.updateChatWidth()
			}
		}

	case "event.plan_proposed":
		data := marshalData(event.Data)
		var pp protocol.EventPlanProposed
		json.Unmarshal(data, &pp)
		sess.activePlan = pp.Plan
		sess.agentState = StatePlanReview
		sess.chatMessages = append(sess.chatMessages, renderPlanProposal(pp.Plan, m.styles))
		sess.input.Focus()
		sess.input.Placeholder = "Type modifications (Enter to send, Shift+Enter or Alt+Enter for new line) or press y/n..."

	case "event.plan_task_start":
		sess.agentState = StatePlanExecuting
		data := marshalData(event.Data)
		var pts protocol.EventPlanTaskStart
		json.Unmarshal(data, &pts)
		sess.chatMessages = append(sess.chatMessages, renderPlanTaskStart(pts.TaskIdx, pts.Title, pts.Total))
		cmds = append(cmds, sess.thinkingAnim.Start())

	case "event.plan_task_done":
		sess.thinkingAnim.Stop()
		data := marshalData(event.Data)
		var ptd protocol.EventPlanTaskDone
		json.Unmarshal(data, &ptd)
		sess.chatMessages = append(sess.chatMessages, renderPlanTaskDone(ptd.TaskIdx, ptd.Title, ptd.Success, ptd.Summary, m.styles))

	case "event.plan_complete":
		data := marshalData(event.Data)
		var pc protocol.EventPlanComplete
		json.Unmarshal(data, &pc)
		sess.activePlan = nil
		sess.chatMessages = append(sess.chatMessages, renderPlanSummary(pc.Plan))

	case "event.workflow_start":
		data := marshalData(event.Data)
		var ps protocol.EventWorkflowStart
		json.Unmarshal(data, &ps)
		sess.chatMessages = append(sess.chatMessages, renderWorkflowStart(ps.WorkflowName, ps.TotalSteps, m.styles))
		sess.workflowGraphPanel.Start(ps.WorkflowName, ps.TotalSteps, ps.Steps)
		sess.rightPanel.OpenWorkflow(m.height)
		m.updateChatWidth()

	case "event.workflow_step_start":
		sess.agentState = StateStreaming
		data := marshalData(event.Data)
		var pss protocol.EventWorkflowStepStart
		json.Unmarshal(data, &pss)
		sess.chatMessages = append(sess.chatMessages, renderWorkflowStepStart(pss.StepID, pss.StepIdx, pss.Total, pss.Explanation))
		sess.workflowGraphPanel.StepStart(pss.StepID, pss.StepIdx, pss.Explanation)
		cmds = append(cmds, sess.thinkingAnim.Start())

	case "event.workflow_step_done":
		sess.thinkingAnim.Stop()
		m.flushSessionBuf(sess)
		data := marshalData(event.Data)
		var psd protocol.EventWorkflowStepDone
		json.Unmarshal(data, &psd)
		sess.chatMessages = append(sess.chatMessages, renderWorkflowStepDone(psd.StepID, psd.StepIdx, psd.Total, psd.Success, psd.Display, psd.Command, psd.BashOutput, psd.ToolStats, m.mdRenderer, m.styles))
		sess.workflowGraphPanel.StepDone(psd.StepID, psd.Success, psd.DurationMs)

	case "event.workflow_complete":
		data := marshalData(event.Data)
		var pc protocol.EventWorkflowComplete
		json.Unmarshal(data, &pc)
		sess.chatMessages = append(sess.chatMessages, renderWorkflowComplete(pc.WorkflowName, pc.Success, pc.Summary, pc.StepCosts, pc.DurationMs, m.styles))
		sess.workflowGraphPanel.Reset()
		if hasPendingTodos(sess.todos) {
			sess.rightPanel.OpenTodos(m.height)
		} else {
			sess.rightPanel.Close()
			m.updateChatWidth()
		}

	case "event.workflow_status":
		data := marshalData(event.Data)
		var ws protocol.EventWorkflowStatus
		json.Unmarshal(data, &ws)
		// "running" transitions (resume) are visible via step events already;
		// only surface the noteworthy stops.
		if ws.Status != "running" {
			sess.chatMessages = append(sess.chatMessages, renderWorkflowStatus(ws.WorkflowName, ws.Status, ws.StepID, ws.Iteration, ws.TokensUsed, ws.TokenBudget, ws.Note, m.styles))
		}

	case "event.agent_done":
		sess.thinkingAnim.Stop()
		m.flushSessionBuf(sess)
		if idx != m.selectedSession || m.activeTab != TabKindChat {
			sess.unreadCount++
			if m.activeTab != TabKindSessions {
				m.sessionsTabUnseen = true
			}
		}
		turnInput := sess.inputTokens - sess.turnStartInputTokens
		turnOutput := sess.outputTokens - sess.turnStartOutputTokens
		turnCacheCreation := sess.cacheCreationTokens - sess.turnStartCacheCreationTokens
		turnCacheRead := sess.cacheReadTokens - sess.turnStartCacheReadTokens
		cost := protocol.CalculateCost(sess.modelName, turnInput, turnOutput, turnCacheCreation, turnCacheRead)
		turnNum := countTurnSeparators(sess.chatMessages) + 1
		sess.chatMessages = append(sess.chatMessages, renderTurnInfo(sess.modelName, sess.elapsed, cost, turnNum, m.mdRenderer.width+4, m.styles))
		sess.turnStartInputTokens = sess.inputTokens
		sess.turnStartOutputTokens = sess.outputTokens
		sess.turnStartCacheCreationTokens = sess.cacheCreationTokens
		sess.turnStartCacheReadTokens = sess.cacheReadTokens
		if sess.pendingInput != nil {
			pending := sess.pendingInput
			sess.pendingInput = nil
			if sess.client != nil {
				telemetry.TrackTurn(sess.modelName)
				if sess.activeWorkflow != "" && !strings.HasPrefix(pending.text, "/") {
					sess.client.SendWorkflow(sess.activeWorkflow, pending.text)
				} else {
					sess.client.SendInput(pending.text, pending.attachments)
				}
			}
			sess.agentState = StateStreaming
			cmds = append(cmds, sess.thinkingAnim.Start())
		} else {
			sess.agentState = StateWaitingForInput
			sess.input.Focus()
			sess.input.Placeholder = "Ask the agent anything... (Enter to send, Shift+Enter or Alt+Enter for new line)"
		}

	case "event.clear":
		m.flushSessionBuf(sess)
		sess.chatMessages = nil
		sess.pendingTools = nil
		sess.inputTokens = 0
		sess.outputTokens = 0
		sess.cacheCreationTokens = 0
		sess.cacheReadTokens = 0
		sess.turnStartInputTokens = 0
		sess.turnStartOutputTokens = 0
		sess.turnStartCacheCreationTokens = 0
		sess.turnStartCacheReadTokens = 0
		sess.elapsed = 0
		sess.lastInputTokens = 0
		sess.chatMessages = append(sess.chatMessages, renderSystemMessage("Conversation cleared.", m.styles))

	case "event.compacted":
		data := marshalData(event.Data)
		var c protocol.EventCompacted
		json.Unmarshal(data, &c)
		m.flushSessionBuf(sess)
		sess.lastInputTokens = 0
		verb := "Compacted"
		if c.Auto {
			verb = "Auto-compacted"
		}
		label := fmt.Sprintf("%s %d earlier turn(s) into a summary.", verb, c.SummarizedTurns)
		sess.chatMessages = append(sess.chatMessages, renderSystemMessage(label, m.styles))

	case "event.retry":
		data := marshalData(event.Data)
		var retry protocol.EventRetry
		json.Unmarshal(data, &retry)
		m.flushSessionBuf(sess)
		sess.chatMessages = append(sess.chatMessages, renderRetryMessage(retry))

	case "event.error":
		data := marshalData(event.Data)
		var errEvent protocol.EventError
		json.Unmarshal(data, &errEvent)
		sess.chatMessages = append(sess.chatMessages, renderErrorMessage(fmt.Errorf("%s", errEvent.Message)))

	case "event.quit":
		// Daemon-driven quit-all (post-update restart). Intentionally no
		// closeSessionsForQuit: the bare disconnect leaves every record in
		// open/ so all sessions restore on relaunch.
		cmds = append(cmds, tea.Quit)
	}

	return cmds
}

// View implements tea.Model — builds all content fresh each frame.
func (m Model) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("Initializing...")
		v.AltScreen = true
		return v
	}

	sess := m.currentSession()

	// Layout
	var panelHeights []int
	if sess != nil && sess.attachmentPanel.IsVisible() {
		panelHeights = append(panelHeights, sess.attachmentPanel.Count()+3)
	}
	if sess != nil && sess.historyPanel.IsVisible() {
		panelHeights = append(panelHeights, sess.historyPanel.maxHeight+2)
	}

	inputLines := m.visualLineCount()
	if sess != nil && (sess.agentState == StateUserQuestion || sess.agentState == StateConfirmPending) && sess.questionPanel.IsVisible() {
		inputLines = sess.questionPanel.Height()
	}
	layout := computeLayout(m.width, m.height, inputLines, panelHeights...)

	if sess != nil && sess.rightPanel.IsVisible() {
		layout.ChatWidth = m.effectiveChatWidth()
	}

	canvas := uv.NewScreenBuffer(m.width, m.height)
	screen.Clear(canvas)

	y := 0

	// Tab bar
	viewportFocused := m.activeTab == TabKindSessions || m.activeTab == TabKindModels || m.activeTab == TabKindSettings || (sess != nil && sess.focus == FocusChat)
	tabBarWidth := layout.ChatWidth
	if m.activeTab == TabKindSessions || m.activeTab == TabKindModels || m.activeTab == TabKindSettings {
		tabBarWidth = m.width
	}
	tabBar := renderTabBar(m.activeTab, tabBarWidth, m.styles, viewportFocused, m.hasAlertSessions(), m.tabAlertBlinkOn, m.sessionsTabUnseen, m.updateLatest != "")
	uv.NewStyledString(tabBar).Draw(canvas, image.Rect(0, y, tabBarWidth, y+layout.TabBarHeight))
	y += layout.TabBarHeight

	switch m.activeTab {
	case TabKindSessions:
		sessionsHeight := m.height - layout.TabBarHeight - layout.StatusBarHeight
		spinnerFrame := ""
		if m.sessionsSpinnerActive {
			spinnerFrame = string(animFrames[m.sessionsSpinnerStep%len(animFrames)])
		}
		sv := renderSessionsView(m.sessions, m.width, sessionsHeight, m.styles, m.sessionsSelected, spinnerFrame)
		uv.NewStyledString(sv).Draw(canvas, image.Rect(0, y, m.width, y+sessionsHeight))
		y += sessionsHeight

	case TabKindChat:
		// Chat content
		innerWidth := layout.ChatWidth - 4
		var chatContent string
		if sess != nil {
			chatContent = buildRenderedChat(sess.chatMessages, m.styles, innerWidth)
			if sess.showThinking && sess.thinkingRendered != "" {
				chatContent += sess.thinkingRendered + "\n"
			}
			if sess.assistantRendered != "" {
				chatContent += sess.assistantRendered
			} else if animFrame := sess.thinkingAnim.View(); animFrame != "" {
				chatContent += animFrame + "\n"
			}
		}
		if chatContent == "" && !m.testMode {
			if sess != nil && sess.awaitingReplay {
				chatContent = renderRestoringInline(innerWidth, layout.ChatHeight-1, m.styles, sess.thinkingAnim.View())
			} else {
				chatContent = renderWelcomeInline(innerWidth, layout.ChatHeight-1, m.styles)
			}
		}

		contentHeight := layout.ChatHeight - 1
		allLines := strings.Split(chatContent, "\n")

		visualRowStart := make([]int, len(allLines)+1)
		for i, line := range allLines {
			visualRowStart[i+1] = visualRowStart[i] + visualRows(line, innerWidth)
		}
		totalVisualRows := visualRowStart[len(allLines)]

		chatScrollOffset := 0
		if sess != nil {
			chatScrollOffset = sess.chatScrollOffset
		}
		endVisRow := totalVisualRows - chatScrollOffset
		if endVisRow < contentHeight {
			endVisRow = contentHeight
		}
		if endVisRow > totalVisualRows {
			endVisRow = totalVisualRows
		}

		endLogical := 0
		for endLogical < len(allLines) && visualRowStart[endLogical+1] <= endVisRow {
			endLogical++
		}
		accVisRows := 0
		startLogical := endLogical
		for startLogical > 0 {
			rows := visualRows(allLines[startLogical-1], innerWidth)
			if accVisRows+rows > contentHeight {
				break
			}
			accVisRows += rows
			startLogical--
		}

		chatLines := allLines[startLogical:endLogical]

		var chatBorderStyle lipgloss.Style
		if sess != nil && sess.focus == FocusChat {
			chatBorderStyle = m.styles.ViewportFocusedStyle
		} else if sess != nil && sess.focus == FocusRightPanel {
			chatBorderStyle = m.styles.ViewportBlurredStyle
		} else {
			chatBorderStyle = m.styles.ViewportBlurredStyle
		}
		chatBox := chatBorderStyle.Width(layout.ChatWidth).Height(layout.ChatHeight).
			Render(strings.Join(chatLines, "\n"))
		uv.NewStyledString(chatBox).Draw(canvas, image.Rect(0, y, layout.ChatWidth, y+layout.ChatHeight))

		// Right panel
		if sess != nil && sess.rightPanel.IsVisible() {
			rpHeight := layout.ChatHeight + 1
			rpView := sess.rightPanel.View(rpHeight, m.styles, sess.focus == FocusRightPanel, &sess.workflowGraphPanel, sess.todos)
			rpX := m.width - sess.rightPanel.PanelWidth()
			uv.NewStyledString(rpView).Draw(canvas, image.Rect(rpX, y-1, m.width, y-1+rpHeight))
		}

		y += layout.ChatHeight

		// Panels between chat and input
		if sess != nil && sess.attachmentPanel.IsVisible() {
			panel := renderAttachmentPanel(&sess.attachmentPanel, m.width, m.styles)
			ph := sess.attachmentPanel.Count() + 3
			uv.NewStyledString(panel).Draw(canvas, image.Rect(0, y, m.width, y+ph))
			y += ph
		}
		if sess != nil && sess.historyPanel.IsVisible() {
			panel := renderHistoryPanel(sess.history.entries, sess.history.times, &sess.historyPanel, m.width, true, m.styles)
			ph := sess.historyPanel.maxHeight + 2
			uv.NewStyledString(panel).Draw(canvas, image.Rect(0, y, m.width, y+ph))
			y += ph
		}

		// Input section
		var inputSection string
		if sess != nil && (sess.agentState == StateUserQuestion || sess.agentState == StateConfirmPending) && sess.questionPanel.IsVisible() {
			inputSection = sess.questionPanel.Render(m.styles, sess.focus == FocusEditor, m.mdRenderer)
		} else if m.state == StateQuitConfirm {
			modeName := "Chat"
			if sess != nil {
				modeName = m.currentModeName(sess)
			}
			inputSection = renderInputBox(modeName, sess != nil && sess.activeWorkflow != "", "", m.width, false, m.styles.ColorBlurBorder)
		} else if sess != nil {
			inputSection = renderInputBox(m.currentModeName(sess), sess.activeWorkflow != "", sess.input.View(), m.width, sess.focus == FocusEditor, m.styles.ColorBlurBorder)
		} else {
			inputSection = renderInputBox("Chat", false, "", m.width, false, m.styles.ColorBlurBorder)
		}
		uv.NewStyledString(inputSection).Draw(canvas, image.Rect(0, y, m.width, y+layout.InputHeight))
		y += layout.InputHeight

	case TabKindModels:
		modelsHeight := m.height - layout.TabBarHeight - layout.StatusBarHeight
		mv := renderModelsView(m.width, modelsHeight, m.styles,
			m.modelsLoggedIn, m.modelsAvailable, m.modelsStatus,
			m.modelsProviderSel, m.modelsFocus,
			m.modelsAuthRow, m.modelsAuthBtn, m.modelsModelSel, m.modelsModelScroll,
			m.modelsFilter, m.activeModelSpec(), m.modelsLoginStatus)
		uv.NewStyledString(mv).Draw(canvas, image.Rect(0, y, m.width, y+modelsHeight))
		y += modelsHeight

	case TabKindSettings:
		settingsHeight := m.height - layout.TabBarHeight - layout.StatusBarHeight
		st := settingsState{
			cursor:              m.settingsCursor,
			showThinking:        config.ShowThinking(),
			readAgentsMD:        config.ReadAgentsMD(),
			readClaudeMD:        config.ReadClaudeMD(),
			telemetry:           config.TelemetryEnabled(),
			compactionAuto:      config.CompactionAuto(),
			compactionThreshold: config.CompactionThreshold(),
			closedRetentionMins: config.ClosedSessionRetentionMinutes(),
			updateCheck:         config.UpdateCheckEnabled(),
			updateCurrent:       m.updateCurrent,
			updateLatest:        m.updateLatest,
			updateMethod:        m.updateMethod,
			updateInstalled:     m.updateInstalled,
			updateErr:           m.updateErr,
		}
		if settSess := m.currentSession(); settSess != nil {
			st.showThinking = settSess.showThinking
		}
		sv := renderSettingsView(m.width, settingsHeight, m.styles, st)
		uv.NewStyledString(sv).Draw(canvas, image.Rect(0, y, m.width, y+settingsHeight))
		y += settingsHeight
	}

	// Status bar — global: connected if any session is up, reconnecting if none
	// are connected but at least one is trying.
	var connected, reconnecting bool
	for _, s := range m.sessions {
		if !s.reconnecting && s.client != nil {
			connected = true
			break
		}
		if s.reconnecting {
			reconnecting = true
		}
	}
	statusFocus := FocusEditor
	var statusInputTokens, statusContextWindow int64
	if sess != nil {
		statusFocus = sess.focus
		statusInputTokens = sess.lastInputTokens
		statusContextWindow = sess.contextWindow
	}
	statusBar := renderStatusBar(m.width, connected, reconnecting, m.statusMsg, m.styles, m.activeTab, statusFocus, statusInputTokens, statusContextWindow)
	uv.NewStyledString(statusBar).Draw(canvas, image.Rect(0, y, m.width, m.height))

	// Command palette overlay
	if m.commandPalette.IsVisible() {
		overlay := m.commandPalette.View(m.width, m.height, m.styles)
		w, h := lipgloss.Size(overlay)
		center := centerRect(canvas.Bounds(), w, h)
		uv.NewStyledString(overlay).Draw(canvas, center)
	}

	// Quit confirm overlay
	if m.state == StateQuitConfirm {
		overlay := renderQuitDialog(m.width, m.height, m.styles, m.quitSelected, m.quitCloseAll)
		w, h := lipgloss.Size(overlay)
		center := centerRect(canvas.Bounds(), w, h)
		uv.NewStyledString(overlay).Draw(canvas, center)
	}

	// Trim confirm overlay
	if sess != nil && sess.agentState == StateTrimConfirm {
		overlay := renderTrimDialog(m.width, m.height, m.styles, sess.trimSelected)
		w, h := lipgloss.Size(overlay)
		center := centerRect(canvas.Bounds(), w, h)
		uv.NewStyledString(overlay).Draw(canvas, center)
	}

	// Session close confirm overlay
	if m.state == StateSessionCloseConfirm {
		sessionID := ""
		if m.sessionCloseIdx >= 0 && m.sessionCloseIdx < len(m.sessions) {
			if s := m.sessions[m.sessionCloseIdx]; s.client != nil {
				sessionID = s.client.SessionID()
			}
		}
		overlay := renderSessionCloseDialog(m.width, m.height, m.styles, m.sessionCloseSelected, sessionID)
		w, h := lipgloss.Size(overlay)
		center := centerRect(canvas.Bounds(), w, h)
		uv.NewStyledString(overlay).Draw(canvas, center)
	}

	// Credential-entry popup overlay (Models tab)
	if m.modelsInKeyInput {
		overlay := renderKeyInputDialog(m.width, m.height, m.styles, keyInputDialog{
			Provider:     DisplayNameForProvider(m.modelsKeyInputProvider),
			MethodLabel:  m.modelsKeyInputLabel,
			KeyMasked:    maskSecret(m.modelsKeyInput.Value()),
			NeedsBaseURL: m.modelsKeyInputBaseURL,
			BaseURL:      m.modelsBaseURLInput.Value(),
			Focus:        m.modelsKeyInputFocus,
		})
		w, h := lipgloss.Size(overlay)
		center := centerRect(canvas.Bounds(), w, h)
		uv.NewStyledString(overlay).Draw(canvas, center)
	}

	// Credential-delete confirm overlay (Models tab)
	if m.state == StateKeyDeleteConfirm {
		overlay := renderKeyDeleteDialog(m.width, m.height, m.styles, DisplayNameForProvider(m.keyDeleteProvider), m.keyDeleteKind, m.keyDeleteSelected)
		w, h := lipgloss.Size(overlay)
		center := centerRect(canvas.Bounds(), w, h)
		uv.NewStyledString(overlay).Draw(canvas, center)
	}

	// File completer overlay
	if sess != nil && sess.fileCompleter.IsVisible() {
		popupWidth := 40
		if popupWidth > m.width-4 {
			popupWidth = m.width - 4
		}
		overlay := sess.fileCompleter.View(popupWidth, 8, m.styles)
		if overlay != "" {
			_, h := lipgloss.Size(overlay)
			inputTop := m.height - layout.StatusBarHeight - layout.InputHeight
			popupY := inputTop - h
			if popupY < 0 {
				popupY = 0
			}
			uv.NewStyledString(overlay).Draw(canvas, image.Rect(2, popupY, 2+popupWidth, popupY+h))
		}
	}

	// Slash menu overlay
	if sess != nil && sess.slashMenu.IsVisible() {
		popupWidth := 80
		overlay := sess.slashMenu.View(popupWidth, 8, m.styles)
		if overlay != "" {
			_, h := lipgloss.Size(overlay)
			inputTop := m.height - layout.StatusBarHeight - layout.InputHeight
			popupY := inputTop - h
			if popupY < 0 {
				popupY = 0
			}
			uv.NewStyledString(overlay).Draw(canvas, image.Rect(2, popupY, 2+popupWidth, popupY+h))
		}
	}

	content := strings.ReplaceAll(canvas.Render(), "\r\n", "\n")
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// --- Helper methods ---

// handleCommandAction executes the command identified by action and returns any
// resulting tea.Cmd values. It is shared by the command palette and slash menu.
func (m *Model) handleCommandAction(action string, sess *SessionState) []tea.Cmd {
	var cmds []tea.Cmd
	switch action {
	case "clear":
		if sess != nil && sess.client != nil {
			sess.client.SendCancel()
		}
		if sess != nil {
			m.flushSessionBuf(sess)
			sess.chatMessages = nil
		}
	case "copy_conversation":
		if sess == nil || len(sess.chatMessages) == 0 {
			if sess != nil {
				sess.chatMessages = append(sess.chatMessages, renderSystemMessage("No conversation to copy.", m.styles))
			}
		} else {
			text := formatConversationPlainText(sess.chatMessages)
			count := len(sess.chatMessages)
			if err := clipboard.WriteAll(text); err != nil {
				sess.chatMessages = append(sess.chatMessages, renderErrorMessage(fmt.Errorf("failed to copy to clipboard: %w", err)))
			} else {
				sess.chatMessages = append(sess.chatMessages, renderSystemMessage(fmt.Sprintf("Copied %d messages to clipboard.", count), m.styles))
			}
		}
	case "slash_clear":
		if sess != nil && sess.client != nil {
			sess.chatMessages = append(sess.chatMessages, renderUserMessage("/clear", m.mdRenderer.width))
			sess.chatScrollOffset = 0
			sess.agentState = StateStreaming
			cmds = append(cmds, sess.thinkingAnim.Start())
			sess.client.SendInput("/clear", nil)
		}
	case "slash_skills":
		if sess != nil && sess.client != nil {
			sess.chatMessages = append(sess.chatMessages, renderUserMessage("/skills", m.mdRenderer.width))
			sess.chatScrollOffset = 0
			sess.agentState = StateStreaming
			cmds = append(cmds, sess.thinkingAnim.Start())
			sess.client.SendInput("/skills", nil)
		}
	case "history":
		if sess != nil && len(sess.history.entries) > 0 {
			sess.historyPanel.Open(len(sess.history.entries), m.height)
		}
	case "scroll_top":
		if sess != nil {
			sess.chatScrollOffset = m.sessionMaxScrollOffset(sess)
			sess.focus = FocusChat
		}
	case "scroll_bottom":
		if sess != nil {
			sess.chatScrollOffset = 0
			sess.focus = FocusChat
		}
	case "toggle_thinking":
		if sess != nil {
			sess.showThinking = !sess.showThinking
			if sess.showThinking && sess.thinkingBuf != "" {
				sess.thinkingRendered = renderThinkingText(sess.thinkingBuf, m.styles, m.mdRenderer.width+4)
			} else {
				sess.thinkingRendered = ""
			}
			_ = config.SetShowThinking(sess.showThinking)
		}
	case "quit":
		m.closeSessionsForQuit(config.CloseAllSessionsOnQuit())
		cmds = append(cmds, tea.Quit)
	default:
		if strings.HasPrefix(action, "switch_tab_") {
			idxStr := strings.TrimPrefix(action, "switch_tab_")
			if i, err := strconv.Atoi(idxStr); err == nil {
				switch TabKind(i) {
				case TabKindSessions:
					cmds = append(cmds, m.switchTab(TabKindSessions))
				case TabKindChat:
					cmds = append(cmds, m.switchTab(TabKindChat))
				case TabKindModels:
					cmds = append(cmds, m.switchTab(TabKindModels))
				case TabKindSettings:
					cmds = append(cmds, m.switchTab(TabKindSettings))
				}
			}
		}
	}
	return cmds
}

// flushSessionBuf commits the streaming assistant buffer to the session's chatMessages.
func (m *Model) flushSessionBuf(sess *SessionState) {
	if sess.showThinking && sess.thinkingBuf != "" {
		sess.chatMessages = append(sess.chatMessages, renderThinkingMessage(sess.thinkingBuf, m.styles, m.mdRenderer.width+4))
	}
	if sess.assistantBuf != "" {
		sess.chatMessages = append(sess.chatMessages, renderAssistantMessage(sess.assistantBuf, m.mdRenderer))
	}
	sess.assistantBuf = ""
	sess.assistantRendered = ""
	sess.thinkingBuf = ""
	sess.thinkingRendered = ""
}

// applyReplay rebuilds a session's viewport and restores its mode/model/todos
// from a daemon event.replay (sent when attaching to a persisted session).
// Restore-time warnings are appended as system messages.
func (m *Model) applyReplay(sess *SessionState, rep protocol.EventReplay) {
	sess.chatMessages = m.buildReplayChatMessages(rep)
	sess.todos = rep.Todos
	sess.activePlan = rep.ActivePlan
	if rep.Model != "" {
		sess.setModel(rep.Model)
	}
	sess.activeWorkflow = rep.ActiveWorkflow
	for _, w := range rep.Warnings {
		sess.chatMessages = append(sess.chatMessages, renderSystemMessage(w, m.styles))
	}
	if sess.agentState == StateStreaming || sess.agentState == StateToolExecuting {
		sess.agentState = StateWaitingForInput
	}
}

// buildReplayChatMessages reconstructs rendered ChatMessages from a replayed
// conversation. Tool results are matched to their preceding tool_use by ID so
// the result line carries the right tool name.
func (m *Model) buildReplayChatMessages(rep protocol.EventReplay) []ChatMessage {
	var out []ChatMessage
	toolNames := map[string]string{}
	for _, msg := range rep.Messages {
		for _, b := range msg.Blocks {
			switch b.Kind {
			case "text":
				if msg.Role == "user" {
					out = append(out, renderUserMessage(b.Text, m.width))
				} else {
					out = append(out, renderAssistantMessage(b.Text, m.mdRenderer))
				}
			case "tool_use":
				if b.ToolID != "" {
					toolNames[b.ToolID] = b.ToolName
				}
				out = append(out, renderToolCall(b.ToolName, replayToolSummary(b.Input), "", [4]string{}, m.styles))
			case "tool_result":
				name := toolNames[b.ToolID]
				out = append(out, renderToolResultWithContext(name, b.Output, b.IsError, false, "", m.styles, m.mdRenderer, m.mdRenderer.width))
			}
		}
	}
	return out
}

// replayToolSummary derives a short one-line summary from a tool's input for
// the replayed tool-call line (the live summary is computed daemon-side and not
// persisted).
func replayToolSummary(input map[string]any) string {
	if input == nil {
		return ""
	}
	for _, k := range []string{"path", "command", "pattern", "query", "url", "name", "id"} {
		if v, ok := input[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// visualLineCount returns the display line count for the current session's input.
func (m *Model) visualLineCount() int {
	sess := m.currentSession()
	if sess == nil {
		return 1
	}
	val := sess.input.Value()
	if val == "" {
		return 1
	}
	availWidth := m.width - 4 - 2
	if availWidth <= 0 {
		return sess.input.LineCount()
	}
	total := 0
	for _, line := range strings.Split(val, "\n") {
		w := lipgloss.Width(line)
		total += w/availWidth + 1
	}
	if total < 1 {
		total = 1
	}
	if sess.input.MaxHeight > 0 && total > sess.input.MaxHeight {
		total = sess.input.MaxHeight
	}
	return total
}

// sessionMaxScrollOffset returns the max scroll offset for a session's chat.
func (m *Model) sessionMaxScrollOffset(sess *SessionState) int {
	layout := computeLayout(m.width, m.height, m.visualLineCount())
	contentHeight := layout.ChatHeight - 1
	chatWidth := layout.ChatWidth
	if sess.rightPanel.IsVisible() {
		chatWidth = m.width - sess.rightPanel.PanelWidth()
		if chatWidth < 10 {
			chatWidth = 10
		}
	}
	innerWidth := chatWidth - 4
	chatContent := buildRenderedChat(sess.chatMessages, m.styles, innerWidth)
	if sess.showThinking && sess.thinkingRendered != "" {
		chatContent += sess.thinkingRendered + "\n"
	}
	if sess.assistantRendered != "" {
		chatContent += sess.assistantRendered
	}
	if chatContent == "" && !m.testMode {
		chatContent = renderWelcomeInline(innerWidth, contentHeight, m.styles)
	}
	totalVisualRows := 0
	for _, line := range strings.Split(chatContent, "\n") {
		totalVisualRows += visualRows(line, innerWidth)
	}
	maxOff := totalVisualRows - contentHeight
	if maxOff < 0 {
		return 0
	}
	return maxOff
}

// clampScrollOffset ensures the session's chatScrollOffset is within valid bounds.
func (m *Model) clampScrollOffset(sess *SessionState) {
	if sess.chatScrollOffset < 0 {
		sess.chatScrollOffset = 0
	}
	if max := m.sessionMaxScrollOffset(sess); sess.chatScrollOffset > max {
		sess.chatScrollOffset = max
	}
}

// turnSepByNumber returns the separator info for the given 1-based turn number.
func (m *Model) turnSepByNumber(sess *SessionState, turnNum int) (TurnSepInfo, bool) {
	for _, s := range turnSeparatorInfos(sess.chatMessages, m.styles, m.mdRenderer.width) {
		if s.TurnIdx == turnNum-1 {
			return s, true
		}
	}
	return TurnSepInfo{}, false
}

// parseTurnArg extracts a positive turn number from the second field of a
// command, e.g. ["/fork", "4"] -> 4.
func parseTurnArg(fields []string) (int, bool) {
	if len(fields) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// appendCommandError appends a system message describing a command error.
func (m *Model) appendCommandError(sess *SessionState, text string) {
	sess.input.Reset()
	sess.input.SetHeight(1)
	sess.chatMessages = append(sess.chatMessages, renderSystemMessage(text, m.styles))
	sess.chatScrollOffset = 0
}

// tryLocalCommand intercepts client-side slash commands (/fork, /trim, /copy)
// typed into the input. When the input is a recognized local command it is
// consumed (never sent to the daemon) and handled is true; the returned
// model/cmd should then be used as handleEnter's result.
func (m Model) tryLocalCommand(sess *SessionState) (handled bool, model tea.Model, cmd tea.Cmd) {
	text := strings.TrimSpace(sess.input.Value())
	if !strings.HasPrefix(text, "/") {
		return false, m, nil
	}
	fields := strings.Fields(text)
	switch fields[0] {
	case "/fork":
		n, ok := parseTurnArg(fields)
		if !ok {
			m.appendCommandError(sess, "Usage: /fork N  (N = turn number)")
			return true, m, nil
		}
		sep, ok := m.turnSepByNumber(sess, n)
		if !ok {
			m.appendCommandError(sess, fmt.Sprintf("No such turn: %d", n))
			return true, m, nil
		}
		sess.input.Reset()
		sess.input.SetHeight(1)
		nm, c := m.doFork(sep)
		return true, nm, c

	case "/trim":
		n, ok := parseTurnArg(fields)
		if !ok {
			m.appendCommandError(sess, "Usage: /trim N  (deletes all messages AFTER turn N)")
			return true, m, nil
		}
		sep, ok := m.turnSepByNumber(sess, n)
		if !ok {
			m.appendCommandError(sess, fmt.Sprintf("No such turn: %d", n))
			return true, m, nil
		}
		sess.input.Reset()
		sess.input.SetHeight(1)
		sess.trimPrevState = sess.agentState
		sess.trimSelected = 0
		sess.trimSep = sep
		sess.agentState = StateTrimConfirm
		return true, m, nil

	case "/copy":
		// Bare /copy copies the whole conversation.
		if len(fields) == 1 {
			sess.input.Reset()
			sess.input.SetHeight(1)
			cmds := m.handleCommandAction("copy_conversation", sess)
			return true, m, tea.Batch(cmds...)
		}
		n, ok := parseTurnArg(fields)
		if !ok {
			m.appendCommandError(sess, "Usage: /copy [N]  (N = turn number; omit to copy all)")
			return true, m, nil
		}
		sep, ok := m.turnSepByNumber(sess, n)
		if !ok {
			m.appendCommandError(sess, fmt.Sprintf("No such turn: %d", n))
			return true, m, nil
		}
		sess.input.Reset()
		sess.input.SetHeight(1)
		m.copyTurn(sess, n, sep)
		return true, m, nil

	case "/goto":
		n, ok := parseTurnArg(fields)
		if !ok {
			m.appendCommandError(sess, "Usage: /goto N  (N = turn number)")
			return true, m, nil
		}
		if n > countTurnSeparators(sess.chatMessages) {
			m.appendCommandError(sess, fmt.Sprintf("No such turn: %d", n))
			return true, m, nil
		}
		sess.input.Reset()
		sess.input.SetHeight(1)
		m.gotoTurn(sess, n)
		return true, m, nil
	}
	return false, m, nil
}

// copyTurn copies just the messages belonging to the given 1-based turn number
// to the clipboard. The turn's messages are those between the previous turn
// separator and this one (excluding the separator line itself).
func (m *Model) copyTurn(sess *SessionState, turnNum int, sep TurnSepInfo) {
	start := 0
	if prev, ok := m.turnSepByNumber(sess, turnNum-1); ok {
		start = prev.MsgIdx + 1
	}
	end := sep.MsgIdx // exclusive: skip the separator message
	if start < 0 {
		start = 0
	}
	if end > len(sess.chatMessages) {
		end = len(sess.chatMessages)
	}
	if start >= end {
		sess.chatMessages = append(sess.chatMessages, renderSystemMessage(fmt.Sprintf("Turn %d has no messages to copy.", turnNum), m.styles))
		sess.chatScrollOffset = 0
		return
	}
	text := formatConversationPlainText(sess.chatMessages[start:end])
	if err := clipboard.WriteAll(text); err != nil {
		sess.chatMessages = append(sess.chatMessages, renderErrorMessage(fmt.Errorf("failed to copy to clipboard: %w", err)))
	} else {
		sess.chatMessages = append(sess.chatMessages, renderSystemMessage(fmt.Sprintf("Copied turn %d to clipboard.", turnNum), m.styles))
	}
	sess.chatScrollOffset = 0
}

// gotoTurn scrolls the chat so the first message of the given 1-based turn
// number sits at the top of the viewport. Turn N's content starts on the line
// immediately after turn separator N-1 (or at the very top for turn 1).
func (m *Model) gotoTurn(sess *SessionState, turnNum int) {
	innerWidth := m.effectiveChatWidth() - 4
	if innerWidth < 1 {
		innerWidth = 1
	}

	// Logical line (in the rendered chat) where turn turnNum begins.
	targetLine := 0
	if turnNum > 1 {
		for _, sep := range turnSeparatorInfos(sess.chatMessages, m.styles, innerWidth) {
			if sep.TurnIdx == turnNum-2 {
				targetLine = sep.LineIdx + strings.Count(sess.chatMessages[sep.MsgIdx].Rendered, "\n")
				break
			}
		}
	}

	// Rebuild the rendered chat and a visual-row prefix sum, mirroring the
	// renderer (and sessionMaxScrollOffset), to convert the logical line into a
	// from-bottom scroll offset.
	chatContent := buildRenderedChat(sess.chatMessages, m.styles, innerWidth)
	if sess.showThinking && sess.thinkingRendered != "" {
		chatContent += sess.thinkingRendered + "\n"
	}
	if sess.assistantRendered != "" {
		chatContent += sess.assistantRendered
	}
	allLines := strings.Split(chatContent, "\n")
	if targetLine > len(allLines) {
		targetLine = len(allLines)
	}
	visualRowStart := make([]int, len(allLines)+1)
	for i, line := range allLines {
		visualRowStart[i+1] = visualRowStart[i] + visualRows(line, innerWidth)
	}
	totalVisualRows := visualRowStart[len(allLines)]
	startVisRow := visualRowStart[targetLine]

	layout := computeLayout(m.width, m.height, m.visualLineCount())
	contentHeight := layout.ChatHeight - 1

	sess.chatScrollOffset = totalVisualRows - contentHeight - startVisRow
	m.clampScrollOffset(sess)
	sess.focus = FocusChat
}

// doFork creates a new session seeded with history up to sep, and connects a fork.
func (m *Model) doFork(sep TurnSepInfo) (Model, tea.Cmd) {
	sess := m.currentSession()

	newSess := newSessionState(m.cfg, nil)
	newSess.reconnecting = true
	forkedMsgs := make([]ChatMessage, sep.MsgIdx+1)
	copy(forkedMsgs, sess.chatMessages[:sep.MsgIdx+1])
	newSess.chatMessages = forkedMsgs

	forkSessionID := ""
	if sess.client != nil {
		forkSessionID = sess.client.SessionID()
	}

	newIdx := len(m.sessions)
	m.sessions = append(m.sessions, newSess)
	m.selectedSession = newIdx

	return *m, connectFork(
		m.socketPath, m.cwd, m.cfg.ConfigDir, m.cfg.Model, m.authToken,
		m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess,
		forkSessionID, sep.TurnIdx, newSess.daemonSessionID,
	)
}

// doDuplicate creates a new session that is a full copy of srcSess, seeded with
// the source's conversation history up to its last completed turn (sep), and
// connects it. Mirrors doFork but operates on an explicit source session (so it
// can be triggered from the Sessions tab against the highlighted row).
func (m *Model) doDuplicate(srcSess *SessionState, sep TurnSepInfo) (Model, tea.Cmd) {
	newSess := newSessionState(m.cfg, nil)
	newSess.reconnecting = true
	copiedMsgs := make([]ChatMessage, sep.MsgIdx+1)
	copy(copiedMsgs, srcSess.chatMessages[:sep.MsgIdx+1])
	newSess.chatMessages = copiedMsgs

	forkSessionID := ""
	if srcSess.client != nil {
		forkSessionID = srcSess.client.SessionID()
	}

	newIdx := len(m.sessions)
	m.sessions = append(m.sessions, newSess)
	m.selectedSession = newIdx
	m.syncSessionsSelected()

	return *m, connectFork(
		m.socketPath, m.cwd, m.cfg.ConfigDir, m.cfg.Model, m.authToken,
		m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess,
		forkSessionID, sep.TurnIdx, newSess.daemonSessionID,
	)
}

// doTrim trims the current session's history to sep and tells the daemon to match.
func (m *Model) doTrim(sep TurnSepInfo) (Model, tea.Cmd) {
	sess := m.currentSession()
	trimmed := make([]ChatMessage, sep.MsgIdx+1)
	copy(trimmed, sess.chatMessages[:sep.MsgIdx+1])
	sess.chatMessages = trimmed
	sess.chatScrollOffset = 0
	m.clampScrollOffset(sess)
	sess.agentState = sess.trimPrevState
	client := sess.client
	turnIdx := sep.TurnIdx
	cmd := func() tea.Msg {
		if client != nil {
			client.SendTrim(turnIdx)
		}
		return nil
	}
	return *m, cmd
}

// closeSessionsForQuit runs right before the TUI exits. When closeAll is set
// (the quit-dialog checkbox / persisted preference), every session is
// explicitly closed: session.close moves each record open/ -> closed/ in the
// daemon, so nothing is restored on next launch. When unset, it sends nothing —
// the process exit bare-disconnects every connection; the daemon cancels any
// running agent on EOF and leaves all records in open/ for next-run restore.
//
// Deliberately not called from the update quit-all flow (handleUpdateAction,
// event.quit): an update quit is a restart, not an exit, so sessions must
// survive it regardless of the preference.
func (m *Model) closeSessionsForQuit(closeAll bool) {
	if !closeAll {
		return
	}
	for _, sess := range m.sessions {
		if sess.client != nil {
			// Mark the session so the disconnect that follows session.close is
			// treated as expected rather than triggering a reconnect (which
			// would race the daemon's open/ -> closed/ move and resurrect the
			// record in open/).
			sess.closing = true
			sess.client.SendCancel()
			sess.client.SendClose()
		}
	}
}

// doCloseSession closes the session at sessionIdx and returns to the Sessions tab.
func (m *Model) doCloseSession(sessionIdx int) (Model, tea.Cmd) {
	if sessionIdx < 0 || sessionIdx >= len(m.sessions) {
		m.state = StateWaitingForInput
		return *m, nil
	}

	sess := m.sessions[sessionIdx]
	if sess.client != nil {
		sess.client.SendCancel()
		sess.client.SendClose()
	}

	m.sessions = append(m.sessions[:sessionIdx], m.sessions[sessionIdx+1:]...)

	if m.selectedSession >= len(m.sessions) {
		m.selectedSession = len(m.sessions) - 1
	}
	if m.selectedSession < 0 {
		m.selectedSession = 0
	}

	var reconnectCmd tea.Cmd
	if len(m.sessions) == 0 {
		newSess := newSessionState(m.cfg, nil)
		newSess.reconnecting = true
		m.sessions = append(m.sessions, newSess)
		m.selectedSession = 0
		reconnectCmd = attemptReconnect(m.socketPath, m.cwd, m.cfg.ConfigDir, m.cfg.Model, m.authToken, false, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, newSess.daemonSessionID)
	}

	if n := m.sessionsVisibleCount(); n > 0 && m.sessionsSelected >= n {
		m.sessionsSelected = n - 1
	}

	m.activeTab = TabKindSessions
	m.syncSessionsSelected()
	m.state = StateWaitingForInput
	return *m, tea.Batch(reconnectCmd, m.maybeStartSessionsSpinner())
}

// effectiveChatWidth returns the panel-aware total chat width — the single
// source of truth for every width-sensitive render. When the right panel is
// visible it reserves panelWidth columns; otherwise it is the plain layout
// chat width. Inner content width is effectiveChatWidth() - 4.
func (m *Model) effectiveChatWidth() int {
	chatWidth := computeLayout(m.width, m.height, m.visualLineCount()).ChatWidth
	if sess := m.currentSession(); sess != nil && sess.rightPanel.IsVisible() {
		chatWidth = m.width - sess.rightPanel.PanelWidth()
		if chatWidth < 10 {
			chatWidth = 10
		}
	}
	return chatWidth
}

// updateChatWidth updates the markdown renderer width to match the current
// effective chat width and re-renders the session's cached messages.
func (m *Model) updateChatWidth() {
	m.mdRenderer.UpdateWidth(m.effectiveChatWidth() - 4)
	m.rerenderSessionMessages()
	m.lastChatWidth = m.effectiveChatWidth()
}

// reconcileChatWidth re-flows width-cached content (the glamour code box and
// cached message renders) whenever the effective panel-aware chat width has
// changed since the last reconciliation. Called centrally from Update so panel
// open/close, session switches, and resizes all self-heal without each
// transition having to remember to call updateChatWidth.
func (m *Model) reconcileChatWidth() {
	if m.width == 0 {
		return
	}
	if w := m.effectiveChatWidth(); w != m.lastChatWidth {
		m.updateChatWidth()
	}
}

// rerenderSessionMessages re-renders the current session's chat messages at the current width.
func (m *Model) rerenderSessionMessages() {
	sess := m.currentSession()
	if sess == nil {
		return
	}
	width := m.mdRenderer.width + 4
	for i, msg := range sess.chatMessages {
		sess.chatMessages[i] = msg.rerender(m.mdRenderer, m.styles, width)
	}
}

// visibleSessionIndices returns the indices of all sessions.
func (m *Model) visibleSessionIndices() []int {
	indices := make([]int, len(m.sessions))
	for i := range m.sessions {
		indices[i] = i
	}
	return indices
}

// sessionsVisibleCount returns the number of visible sessions (after filter).
func (m *Model) sessionsVisibleCount() int {
	return len(m.visibleSessionIndices())
}

// syncSessionsSelected sets sessionsSelected to the visible row that corresponds
// to the currently active workspace session (selectedSession).
func (m *Model) syncSessionsSelected() {
	for i, idx := range m.visibleSessionIndices() {
		if idx == m.selectedSession {
			m.sessionsSelected = i
			return
		}
	}
}

// sessionsSelectedIdx returns the session index for the highlighted row.
func (m *Model) sessionsSelectedIdx() (int, bool) {
	indices := m.visibleSessionIndices()
	if m.sessionsSelected < 0 || m.sessionsSelected >= len(indices) {
		return 0, false
	}
	return indices[m.sessionsSelected], true
}

// hasAlertSessions reports whether any session is waiting for user input.
func (m *Model) hasAlertSessions() bool {
	for _, sess := range m.sessions {
		if sess.agentState == StateConfirmPending || sess.agentState == StateUserQuestion {
			return true
		}
	}
	return false
}

// maybeStartTabAlertBlink starts the tab alert blink if any session needs attention.
func (m *Model) maybeStartTabAlertBlink() tea.Cmd {
	if m.tabAlertActive || !m.hasAlertSessions() {
		return nil
	}
	m.tabAlertActive = true
	m.tabAlertBlinkOn = true
	return m.tabBlinkTick()
}

// stopTabAlertBlink halts the blink loop.
func (m *Model) stopTabAlertBlink() {
	m.tabAlertActive = false
	m.tabAlertBlinkOn = false
	m.tabAlertBlinkGen++
}

// tabBlinkTick schedules the next tab blink toggle.
func (m *Model) tabBlinkTick() tea.Cmd {
	gen := m.tabAlertBlinkGen
	return tea.Tick(tabBlinkHalfPeriod, func(time.Time) tea.Msg {
		return tabBlinkMsg{gen: gen}
	})
}

// hasBusySessions reports whether any session is actively working (streaming,
// running a tool, or executing a plan) — the states the sessions-list spinner
// animates.
func (m *Model) hasBusySessions() bool {
	for _, sess := range m.sessions {
		switch sess.agentState {
		case StateStreaming, StateToolExecuting, StatePlanExecuting:
			return true
		}
	}
	return false
}

// maybeStartSessionsSpinner starts the sessions-list loading spinner when the
// Sessions tab is active and at least one session is busy. No-op otherwise (and
// when already running), so it is safe to call on every relevant state change.
func (m *Model) maybeStartSessionsSpinner() tea.Cmd {
	if m.sessionsSpinnerActive || m.activeTab != TabKindSessions || !m.hasBusySessions() {
		return nil
	}
	m.sessionsSpinnerActive = true
	return m.sessionsSpinnerTick()
}

// stopSessionsSpinner halts the spinner loop and bumps the generation so any
// in-flight tick already queued in Bubble Tea's channel is ignored on arrival.
func (m *Model) stopSessionsSpinner() {
	m.sessionsSpinnerActive = false
	m.sessionsSpinnerGen++
}

// sessionsSpinnerTick schedules the next sessions-list spinner frame.
func (m *Model) sessionsSpinnerTick() tea.Cmd {
	gen := m.sessionsSpinnerGen
	return tea.Tick(sessionsSpinnerPeriod, func(time.Time) tea.Msg {
		return sessionsSpinnerMsg{gen: gen}
	})
}

// nextWorkflow cycles through available workflows for a session.
func (m *Model) nextWorkflow(sess *SessionState) string {
	if sess.activeWorkflow == "" {
		if len(sess.workflows) > 0 {
			return sess.workflows[0].Name
		}
		return ""
	}
	for i, w := range sess.workflows {
		if w.Name == sess.activeWorkflow {
			if i+1 < len(sess.workflows) {
				return sess.workflows[i+1].Name
			}
			return ""
		}
	}
	return ""
}

// currentModeName returns "Chat" or the active workflow name.
func (m *Model) currentModeName(sess *SessionState) string {
	if sess.activeWorkflow == "" {
		return "Chat"
	}
	for _, w := range sess.workflows {
		if w.Name == sess.activeWorkflow {
			return w.Name
		}
	}
	return "Chat"
}

// emitStatusMsg sets the global transient status bar message and returns a
// tea.Cmd that clears it after 3 seconds. Rapid successive calls are safe
// because each call bumps the generation counter; only the matching clear fires.
func (m *Model) emitStatusMsg(text string, kind StatusMsgKind) tea.Cmd {
	m.statusMsg.gen++
	m.statusMsg.Text = text
	m.statusMsg.Kind = kind
	gen := m.statusMsg.gen
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return clearStatusMsgMsg{gen: gen}
	})
}

// placeholderForMode returns mode-specific placeholder text.
func (m *Model) placeholderForMode(sess *SessionState) string {
	if sess.activeWorkflow == "" {
		return "Ask the agent anything... (Enter to send, Shift+Enter or Alt+Enter for new line)"
	}
	for _, w := range sess.workflows {
		if w.Name == sess.activeWorkflow {
			return fmt.Sprintf("Describe your %s... (Enter to send, Shift+Enter or Alt+Enter for new line)", w.Name)
		}
	}
	return "Enter your request... (Enter to send, Shift+Enter or Alt+Enter for new line)"
}

// updateInputPromptColor sets the textarea text style to match the current mode.
func (m *Model) updateInputPromptColor(sess *SessionState) {
	whiteStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	s := sess.input.Styles()
	s.Focused.Text = whiteStyle
	s.Focused.CursorLine = whiteStyle
	s.Blurred.Text = lipgloss.NewStyle().Foreground(colorDim)
	sess.input.SetStyles(s)
}

// marshalData converts event.Data back to bytes.
func marshalData(data any) []byte {
	b, _ := json.Marshal(data)
	return b
}

// fillTestData populates the current session with fake messages for UI testing.
func (m *Model) fillTestData() {
	sess := m.currentSession()
	if sess == nil {
		return
	}
	sess.chatMessages = append(sess.chatMessages,
		renderSystemMessage("Test mode -- fake data for UI scroll testing", m.styles),
		renderUserMessage("Can you help me refactor the authentication module?", m.mdRenderer.width),
		renderAssistantMessage("Sure! Let me start by reading the current auth implementation.", m.mdRenderer),
		renderToolCall("read_file", "internal/auth/handler.go", "", [4]string{}, m.styles),
		renderToolResult("read_file", "package auth\n\n// handler code...", false, m.styles, m.mdRenderer, m.mdRenderer.width),
		renderAssistantMessage("I can see the auth module. Here's what I'd suggest for the refactor.", m.mdRenderer),
	)
}
