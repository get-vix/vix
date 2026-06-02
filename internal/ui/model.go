package ui

import (
	"encoding/json"
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

	"github.com/kirby88/vix/internal/config"
	"github.com/kirby88/vix/internal/daemon"
	"github.com/kirby88/vix/internal/protocol"
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

// modelsLoadedMsg carries the result of an async model-catalogue fetch.
type modelsLoadedMsg struct {
	catalog map[string]providerCatalog
	err     error
}

// loadModelsCmd fetches the live model catalogue from the daemon. It runs on a
// background goroutine (one-shot RPC) and reports back via modelsLoadedMsg.
func loadModelsCmd(socketPath, authToken string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		res, err := client.ListModels()
		if err != nil {
			return modelsLoadedMsg{err: err}
		}
		out := make(map[string]providerCatalog, len(res))
		for provider, pm := range res {
			arr := make([]ModelInfo, 0, len(pm.Models))
			for _, mi := range pm.Models {
				name := mi.DisplayName
				if name == "" {
					name = mi.Spec
				}
				arr = append(arr, ModelInfo{Spec: mi.Spec, Provider: mi.Provider, DisplayName: name})
			}
			out[provider] = providerCatalog{authenticated: pm.Authenticated, models: arr}
		}
		return modelsLoadedMsg{catalog: out}
	}
}

// modelsForProvider returns the model list to show for a provider. Priority:
// live models > curated fallback (only when the provider is authenticated but
// returned nothing, for resilience) > nil (unauthenticated — the caller shows
// an auth hint instead).
func (m Model) modelsForProvider(provider string) []ModelInfo {
	if m.modelsLoaded {
		if cat, ok := m.dynModels[provider]; ok {
			if len(cat.models) > 0 {
				return cat.models
			}
			if !cat.authenticated {
				return nil
			}
			// Authenticated but no live models (fetch failed/empty): fall
			// through to the curated list so the picker still works.
		}
	}
	return ModelsForProvider(provider)
}

// providerEmptyHint returns the lines to show when a provider's model column is
// empty: how to authenticate (when no credential) or a neutral note otherwise.
func (m Model) providerEmptyHint(provider string) []string {
	authed := false
	if m.modelsLoaded {
		if cat, ok := m.dynModels[provider]; ok {
			authed = cat.authenticated
		}
	}
	if !authed {
		return providerAuthHint(provider)
	}
	return []string{"no models available"}
}

// allModelsFlat returns every model across all providers as a single flat list
// (provider order, newest-first within each), used by the right-panel quick
// model switcher. Uses the dynamic catalogue where available.
func (m Model) allModelsFlat() []ModelInfo {
	var out []ModelInfo
	for _, p := range AvailableProviders {
		out = append(out, m.modelsForProvider(p.Name)...)
	}
	return out
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
		if err := session.Connect(cwd, configDir, model, forceInit, enableWrite, enableDir, false); err != nil {
			time.Sleep(2 * time.Second)
			return reconnectFailedMsg{daemonSessionID: targetDaemonSessionID}
		}
		return reconnectSuccessMsg{daemonSessionID: targetDaemonSessionID, client: session}
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
	sessionCloseIdx      int
	sessionCloseSelected int

	// Sessions tab UI
	sessionsInput    textinput.Model
	sessionsSelected int

	// Settings tab UI
	settingsActiveSection    int    // 0=model section, 1=keys section, 2=display
	settingsProviderSel      int    // row in AvailableProviders (Model section, column 0)
	settingsModelSel         int    // row in ModelsForProvider(AvailableProviders[settingsProviderSel].Name) (Model section, column 1)
	settingsModelColumn      int    // 0 = provider column focused, 1 = model column focused
	settingsModelPending     string // model spec awaiting an API key
	settingsKeySel           int
	settingsKeys             []config.ProviderKey
	settingsKeyInputProvider string
	settingsKeyInput         textinput.Model
	settingsInKeyInput       bool

	// Dynamic model catalogue, fetched from the daemon each time the model
	// list opens. dynModels is keyed by provider id and carries each
	// provider's auth status plus its live models.
	modelsLoading bool
	modelsLoaded  bool
	dynModels     map[string]providerCatalog
	modelsErr     error

	// Shared rendering
	mdRenderer     *MarkdownRenderer
	commandPalette CommandPalette

	// Tab alert blink (Chat tab label pulses when a session needs attention)
	tabAlertActive   bool
	tabAlertBlinkOn  bool
	tabAlertBlinkGen int

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
		sessionsInput:                  newSessionsInput(),
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
	}
	cmds = append(cmds, waitForResume, tea.RequestBackgroundColor)
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		m.sessionsInput.SetWidth(m.width - 6)
		m.updateChatWidth()
		return m, nil

	case tea.KeyPressMsg:
		// --- Global quit confirm overlay ---
		if msg.String() == "ctrl+c" || msg.String() == "ctrl+d" {
			if m.state == StateQuitConfirm {
				sess := m.currentSession()
				if sess != nil && sess.client != nil {
					sess.client.SendCancel()
					sess.client.SendClose()
				}
				return m, tea.Quit
			}
			m.state = StateQuitConfirm
			m.quitSelected = 0
			return m, nil
		}

		// --- Quit / SessionClose / Trim dialogs intercept all keys ---
		if m.state == StateQuitConfirm || m.state == StateSessionCloseConfirm {
			return m.handleDialogKey(msg)
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
			action, payload := sess.rightPanel.HandleKey(msg)
			switch action {
			case rpActionClose:
				sess.rightPanel.Close()
				m.updateChatWidth()
				sess.input.Focus()
				sess.focus = FocusEditor
			case rpActionModelSelected:
				sess.modelName = payload
				if sess.client != nil {
					_ = sess.client.SendSetModel(payload)
				}
				sess.rightPanel.Close()
				m.updateChatWidth()
				sess.input.Focus()
				sess.focus = FocusEditor
			case rpActionNeedKey:
				parts := strings.SplitN(payload, ":", 2)
				if len(parts) == 2 {
					sess.rightPanel.OpenKeyInput(parts[0], parts[1], m.height)
				}
			case rpActionKeyStored:
				parts := strings.SplitN(payload, ":", 2)
				if len(parts) == 2 {
					provider, key := parts[0], parts[1]
					_ = config.StoreProviderKey(provider, key)
					pendingModel := sess.rightPanel.keyInputPending
					if pendingModel != "" {
						sess.modelName = pendingModel
						if sess.client != nil {
							_ = sess.client.SendSetModel(pendingModel)
						}
						sess.rightPanel.Close()
						m.updateChatWidth()
						sess.input.Focus()
						sess.focus = FocusEditor
					} else {
						if sess.client != nil && sess.modelName != "" {
							_ = sess.client.SendSetModel(sess.modelName)
						}
						sess.rightPanel.OpenKeyManager(m.height)
						sess.focus = FocusRightPanel
						sess.input.Blur()
					}
				}
			case rpActionKeyDeleted:
				_ = config.DeleteProviderKey(payload)
				sess.rightPanel.OpenKeyManager(m.height)
				sess.focus = FocusRightPanel
				sess.input.Blur()
			}
			return m, nil
		}

		// --- Command palette ---
		if m.commandPalette.IsVisible() {
			action, _ := m.commandPalette.Update(msg)
			cmds = append(cmds, m.handleCommandAction(action, sess)...)
			if !m.commandPalette.IsVisible() && sess != nil && sess.focus != FocusRightPanel && m.activeTab != TabKindSessions && m.activeTab != TabKindSettings {
				sess.input.Focus()
				sess.focus = FocusEditor
			}
			return m, tea.Batch(cmds...)
		}

		// --- Global workspace shortcuts ---
		switch msg.String() {
		case "ctrl+n":
			if m.selectedSession < len(m.sessions)-1 {
				m.selectedSession++
				m.activeTab = TabKindChat
				selSess := m.sessions[m.selectedSession]
				selSess.unreadCount = 0
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
				selSess.unreadCount = 0
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
					selSess.unreadCount = 0
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
			case "a":
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
			case "x":
				if idx, ok := m.sessionsSelectedIdx(); ok {
					m.sessionCloseIdx = idx
					m.sessionCloseSelected = 1 // default No
					m.state = StateSessionCloseConfirm
				}
				return m, nil
			case "f1":
				return m, nil // already on Sessions tab
			case "f2":
				m.activeTab = TabKindChat
				if sess := m.currentSession(); sess != nil {
					sess.unreadCount = 0
					cmds = append(cmds, sess.thinkingAnim.Resume())
				}
				return m, tea.Batch(cmds...)
			case "f3":
				m.activeTab = TabKindSettings
				m.settingsKeys = config.ListStoredProviderKeys()
				m.settingsKeySel = 0
				m.settingsInKeyInput = false
				m.settingsActiveSection = 0
				m.settingsModelColumn = 0
				m.settingsProviderSel = 0
				m.settingsModelSel = 0
				m.settingsModelPending = ""
				initActiveModel := m.cfg.Model
				if initSess := m.currentSession(); initSess != nil && initSess.modelName != "" {
					initActiveModel = initSess.modelName
				}
				m.settingsProviderSel, m.settingsModelSel = locateActiveModel(initActiveModel)
				m.modelsLoading = true
				cmds = append(cmds, loadModelsCmd(m.socketPath, m.authToken))
				return m, tea.Batch(cmds...)
			default:
				var cmd tea.Cmd
				m.sessionsInput, cmd = m.sessionsInput.Update(msg)
				if n := m.sessionsVisibleCount(); n > 0 && m.sessionsSelected >= n {
					m.sessionsSelected = n - 1
				}
				return m, cmd
			}
		}

		// --- Settings tab key handling ---
		if m.activeTab == TabKindSettings {
			if m.settingsInKeyInput {
				switch msg.String() {
				case "esc":
					m.settingsInKeyInput = false
					m.settingsModelPending = ""
					m.settingsKeys = config.ListStoredProviderKeys()
				case "enter":
					val := strings.TrimSpace(m.settingsKeyInput.Value())
					if val != "" {
						_ = config.StoreProviderKey(m.settingsKeyInputProvider, val)
					}
					m.settingsInKeyInput = false
					m.settingsKeys = config.ListStoredProviderKeys()
					if m.settingsModelPending != "" && val != "" {
						pending := m.settingsModelPending
						m.settingsModelPending = ""
						m.cfg.Model = pending
						if pendSess := m.currentSession(); pendSess != nil {
							pendSess.modelName = pending
							if pendSess.client != nil {
								_ = pendSess.client.SendSetModel(pending)
							}
						}
					} else {
						m.settingsModelPending = ""
					}
				default:
					var cmd tea.Cmd
					m.settingsKeyInput, cmd = m.settingsKeyInput.Update(msg)
					cmds = append(cmds, cmd)
				}
			} else if m.settingsActiveSection == 0 {
				// Model section — two columns: providers (column 0) and
				// per-provider model list (column 1).
				clampProviderSel := func() {
					if m.settingsProviderSel < 0 {
						m.settingsProviderSel = 0
					}
					if m.settingsProviderSel >= len(AvailableProviders) {
						m.settingsProviderSel = len(AvailableProviders) - 1
					}
				}
				clampModelSel := func() {
					models := m.modelsForProvider(AvailableProviders[m.settingsProviderSel].Name)
					if m.settingsModelSel < 0 {
						m.settingsModelSel = 0
					}
					if len(models) == 0 {
						m.settingsModelSel = 0
						return
					}
					if m.settingsModelSel >= len(models) {
						m.settingsModelSel = len(models) - 1
					}
				}
				switch msg.String() {
				case "up", "k":
					if m.settingsModelColumn == 0 {
						if m.settingsProviderSel > 0 {
							m.settingsProviderSel--
							m.settingsModelSel = 0
						}
					} else {
						if m.settingsModelSel > 0 {
							m.settingsModelSel--
						}
					}
				case "down", "j":
					if m.settingsModelColumn == 0 {
						if m.settingsProviderSel < len(AvailableProviders)-1 {
							m.settingsProviderSel++
							m.settingsModelSel = 0
						}
					} else {
						models := m.modelsForProvider(AvailableProviders[m.settingsProviderSel].Name)
						if m.settingsModelSel < len(models)-1 {
							m.settingsModelSel++
						}
					}
				case "left", "h":
					m.settingsModelColumn = 0
				case "right", "l":
					m.settingsModelColumn = 1
					clampModelSel()
				case "enter":
					clampProviderSel()
					if m.settingsModelColumn == 0 {
						// Enter on the provider column jumps to the model column.
						m.settingsModelColumn = 1
						m.settingsModelSel = 0
						clampModelSel()
					} else {
						models := m.modelsForProvider(AvailableProviders[m.settingsProviderSel].Name)
						if len(models) > 0 && m.settingsModelSel < len(models) {
							mod := models[m.settingsModelSel]
							apiKey, _ := config.ResolveProviderKey(mod.Provider, true)
							if apiKey != "" {
								m.cfg.Model = mod.Spec
								if settSess := m.currentSession(); settSess != nil {
									settSess.modelName = mod.Spec
									if settSess.client != nil {
										_ = settSess.client.SendSetModel(mod.Spec)
									}
								}
							} else {
								m.settingsModelPending = mod.Spec
								ti := textinput.New()
								ti.Placeholder = "Paste your " + mod.Provider + " API key..."
								ti.EchoMode = textinput.EchoPassword
								ti.Focus()
								m.settingsKeyInput = ti
								m.settingsKeyInputProvider = mod.Provider
								m.settingsInKeyInput = true
							}
						}
					}
				case "tab":
					m.settingsActiveSection = 1
				case "f1":
					m.activeTab = TabKindSessions
					m.syncSessionsSelected()
					cmds = append(cmds, m.sessionsInput.Focus())
				case "f2":
					m.activeTab = TabKindChat
					if s := m.currentSession(); s != nil {
						s.unreadCount = 0
						cmds = append(cmds, s.thinkingAnim.Resume())
					}
				}
			} else if m.settingsActiveSection == 1 {
				// Keys section
				switch msg.String() {
				case "up", "k":
					if m.settingsKeySel > 0 {
						m.settingsKeySel--
					}
				case "down", "j":
					if m.settingsKeySel < len(m.settingsKeys)-1 {
						m.settingsKeySel++
					}
				case "enter":
					if m.settingsKeySel < len(m.settingsKeys) {
						provider := m.settingsKeys[m.settingsKeySel].Provider
						ti := textinput.New()
						ti.Placeholder = "Paste your " + provider + " API key..."
						ti.EchoMode = textinput.EchoPassword
						ti.Focus()
						m.settingsKeyInput = ti
						m.settingsKeyInputProvider = provider
						m.settingsInKeyInput = true
					}
				case "delete", "backspace":
					if m.settingsKeySel < len(m.settingsKeys) {
						_ = config.DeleteProviderKey(m.settingsKeys[m.settingsKeySel].Provider)
						m.settingsKeys = config.ListStoredProviderKeys()
						if m.settingsKeySel >= len(m.settingsKeys) && m.settingsKeySel > 0 {
							m.settingsKeySel--
						}
					}
				case "tab":
					m.settingsActiveSection = 2
				case "f1":
					m.activeTab = TabKindSessions
					m.syncSessionsSelected()
					cmds = append(cmds, m.sessionsInput.Focus())
				case "f2":
					m.activeTab = TabKindChat
					if s := m.currentSession(); s != nil {
						s.unreadCount = 0
						cmds = append(cmds, s.thinkingAnim.Resume())
					}
				}
			} else {
				// Display section
				switch msg.String() {
				case "enter":
					if sess := m.currentSession(); sess != nil {
						sess.showThinking = !sess.showThinking
						if sess.showThinking && sess.thinkingBuf != "" {
							sess.thinkingRendered = renderThinkingText(sess.thinkingBuf, m.styles, m.mdRenderer.width+4)
						} else {
							sess.thinkingRendered = ""
						}
						_ = config.SetShowThinking(sess.showThinking)
					}
				case "tab":
					m.settingsActiveSection = 0
				case "f1":
					m.activeTab = TabKindSessions
					m.syncSessionsSelected()
					cmds = append(cmds, m.sessionsInput.Focus())
				case "f2":
					m.activeTab = TabKindChat
					if s := m.currentSession(); s != nil {
						s.unreadCount = 0
						cmds = append(cmds, s.thinkingAnim.Resume())
					}
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
				sess.input.SetValue("")
				sess.input.SetHeight(1)
				if action != "" {
					cmds = append(cmds, m.handleCommandAction(action, sess)...)
				}
				if sess.focus != FocusRightPanel && m.activeTab != TabKindSessions && m.activeTab != TabKindSettings {
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

		case "f1":
			m.activeTab = TabKindSessions
			m.syncSessionsSelected()
			cmds = append(cmds, m.sessionsInput.Focus())
			return m, tea.Batch(cmds...)

		case "f2":
			m.activeTab = TabKindChat
			if sess := m.currentSession(); sess != nil {
				sess.unreadCount = 0
			}
			return m, tea.Batch(cmds...)

		case "f3":
			m.activeTab = TabKindSettings
			m.settingsKeys = config.ListStoredProviderKeys()
			m.settingsKeySel = 0
			m.settingsInKeyInput = false
			m.settingsActiveSection = 0
			m.settingsModelColumn = 0
			m.settingsProviderSel = 0
			m.settingsModelSel = 0
			m.settingsModelPending = ""
			initActiveModel2 := m.cfg.Model
			if initSess2 := m.currentSession(); initSess2 != nil && initSess2.modelName != "" {
				initActiveModel2 = initSess2.modelName
			}
			m.settingsProviderSel, m.settingsModelSel = locateActiveModel(initActiveModel2)
			m.modelsLoading = true
			cmds = append(cmds, loadModelsCmd(m.socketPath, m.authToken))
			return m, tea.Batch(cmds...)

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
			case "F":
				if sep, ok := m.sessionActiveForkSep(sess); ok {
					return m.doFork(sep)
				}
			case "T":
				if sep, ok := m.sessionActiveForkSep(sess); ok {
					sess.trimPrevState = sess.agentState
					sess.trimSelected = 0
					sess.trimSep = sep
					sess.agentState = StateTrimConfirm
					return m, nil
				}
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
					sess.slashMenu.Open(slashCommands, slashQuery)
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
		}
		return m, tea.Batch(cmds...)

	case sessionDisconnectedMsg:
		_, sess := m.findSessionByDaemonID(msg.daemonSessionID)
		if sess != nil {
			sess.reconnecting = true
			sess.pendingInput = nil
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

	case tea.PasteMsg:
		if m.activeTab == TabKindSettings && m.settingsInKeyInput {
			m.settingsKeyInput, _ = m.settingsKeyInput.Update(msg)
			return m, nil
		}
		sess := m.currentSession()
		if sess == nil {
			return m, nil
		}
		if sess.rightPanel.IsVisible() && sess.focus == FocusRightPanel && sess.rightPanel.mode == rpModeKeyInput {
			sess.rightPanel.keyInput, _ = sess.rightPanel.keyInput.Update(msg)
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

	case modelsLoadedMsg:
		m.modelsLoading = false
		if msg.err != nil {
			m.modelsErr = msg.err
			// Keep the curated fallback list; surface the failure briefly.
			return m, m.emitStatusMsg("Could not load live model list: "+msg.err.Error(), StatusMsgInfo)
		}
		m.modelsErr = nil
		m.dynModels = msg.catalog
		m.modelsLoaded = true
		// The fetched list may differ from the curated one, so re-clamp the
		// model cursor to the (possibly shorter) list for the selected provider.
		if m.settingsProviderSel >= 0 && m.settingsProviderSel < len(AvailableProviders) {
			n := len(m.modelsForProvider(AvailableProviders[m.settingsProviderSel].Name))
			if m.settingsModelSel >= n {
				m.settingsModelSel = n - 1
			}
			if m.settingsModelSel < 0 {
				m.settingsModelSel = 0
			}
		}
		// If the right-panel quick switcher is open, refresh its list too.
		if sess := m.currentSession(); sess != nil && sess.rightPanel.IsModelMode() {
			sess.rightPanel.SetModels(m.allModelsFlat(), sess.modelName)
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
	}

	// Forward unhandled messages to the active input for cursor blink
	sess := m.currentSession()
	if m.activeTab == TabKindSessions {
		var cmd tea.Cmd
		m.sessionsInput, cmd = m.sessionsInput.Update(msg)
		if cmd != nil {
			return m, cmd
		}
	} else if sess != nil {
		var cmd tea.Cmd
		sess.input, cmd = sess.input.Update(msg)
		if cmd != nil {
			return m, cmd
		}
	}
	return m, nil
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
				sess := m.currentSession()
				if sess != nil && sess.client != nil {
					sess.client.SendCancel()
					sess.client.SendClose()
				}
				return m, tea.Quit
			}
			m.state = StateWaitingForInput
		} else {
			if m.sessionCloseSelected == 0 {
				return m.doCloseSession(m.sessionCloseIdx)
			}
			m.state = StateWaitingForInput
		}
	case "y", "Y":
		if m.state == StateQuitConfirm {
			sess := m.currentSession()
			if sess != nil && sess.client != nil {
				sess.client.SendCancel()
				sess.client.SendClose()
			}
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

	case "event.init_state":
		data := marshalData(event.Data)
		var state protocol.EventInitState
		json.Unmarshal(data, &state)
		sess.initState = protocol.InitState(state.State)
		if state.Model != "" {
			sess.modelName = state.Model
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

	case "event.agent_done":
		sess.thinkingAnim.Stop()
		m.flushSessionBuf(sess)
		if idx != m.selectedSession || m.activeTab != TabKindChat {
			sess.unreadCount++
		}
		turnInput := sess.inputTokens - sess.turnStartInputTokens
		turnOutput := sess.outputTokens - sess.turnStartOutputTokens
		turnCacheCreation := sess.cacheCreationTokens - sess.turnStartCacheCreationTokens
		turnCacheRead := sess.cacheReadTokens - sess.turnStartCacheReadTokens
		cost := protocol.CalculateCost(sess.modelName, turnInput, turnOutput, turnCacheCreation, turnCacheRead)
		sess.chatMessages = append(sess.chatMessages, renderTurnInfo(sess.modelName, sess.elapsed, cost, m.mdRenderer.width+4, m.styles))
		sess.turnStartInputTokens = sess.inputTokens
		sess.turnStartOutputTokens = sess.outputTokens
		sess.turnStartCacheCreationTokens = sess.cacheCreationTokens
		sess.turnStartCacheReadTokens = sess.cacheReadTokens
		if sess.pendingInput != nil {
			pending := sess.pendingInput
			sess.pendingInput = nil
			if sess.client != nil {
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
		sess.chatMessages = append(sess.chatMessages, renderSystemMessage("Conversation cleared.", m.styles))

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
		layout.ChatWidth = m.width - sess.rightPanel.PanelWidth()
		if layout.ChatWidth < 10 {
			layout.ChatWidth = 10
		}
	}

	canvas := uv.NewScreenBuffer(m.width, m.height)
	screen.Clear(canvas)

	y := 0

	// Tab bar
	viewportFocused := m.activeTab == TabKindSessions || m.activeTab == TabKindSettings || (sess != nil && sess.focus == FocusChat)
	tabBarWidth := layout.ChatWidth
	if m.activeTab == TabKindSessions || m.activeTab == TabKindSettings {
		tabBarWidth = m.width
	}
	tabBar := renderTabBar(m.activeTab, tabBarWidth, m.styles, viewportFocused, m.tabAlertBlinkOn)
	uv.NewStyledString(tabBar).Draw(canvas, image.Rect(0, y, tabBarWidth, y+layout.TabBarHeight))
	y += layout.TabBarHeight

	switch m.activeTab {
	case TabKindSessions:
		sessionsHeight := m.height - layout.TabBarHeight - layout.StatusBarHeight
		sv := renderSessionsView(m.sessions, m.width, sessionsHeight, m.styles, m.sessionsInput.Value(), m.sessionsInput.View(), m.sessionsSelected)
		uv.NewStyledString(sv).Draw(canvas, image.Rect(0, y, m.width, y+sessionsHeight))
		y += sessionsHeight

	case TabKindChat:
		// Chat content
		var chatContent string
		if sess != nil {
			chatContent = buildRenderedChat(sess.chatMessages, m.styles, m.mdRenderer.width)
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
			chatContent = renderWelcomeInline(m.mdRenderer.width, layout.ChatHeight-1, m.styles)
		}

		contentHeight := layout.ChatHeight - 1
		innerWidth := m.mdRenderer.width
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

		if sess != nil && sess.chatScrollOffset > 0 && sess.client != nil {
			for _, sep := range turnSeparatorInfos(sess.chatMessages, m.styles, m.mdRenderer.width) {
				if sep.LineIdx >= startLogical && sep.LineIdx < endLogical {
					chatLines[sep.LineIdx-startLogical] = renderForkHintLine(m.mdRenderer.width+4, m.styles)
					break
				}
			}
		}

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
			rpView := sess.rightPanel.View(rpHeight, m.styles, sess.focus == FocusRightPanel, sess.modelName, &sess.workflowGraphPanel, sess.todos)
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

	case TabKindSettings:
		settingsHeight := m.height - layout.TabBarHeight - layout.StatusBarHeight
		settingsActiveModel := m.cfg.Model
		settingsShowThinking := config.ShowThinking()
		if settSess := m.currentSession(); settSess != nil {
			if settSess.modelName != "" {
				settingsActiveModel = settSess.modelName
			}
			settingsShowThinking = settSess.showThinking
		}
		sv := renderSettingsView(m.width, settingsHeight, m.styles, m.settingsActiveSection, m.settingsProviderSel, m.settingsModelSel, m.settingsModelColumn, settingsActiveModel, m.settingsKeys, m.settingsKeySel, m.settingsInKeyInput, m.settingsKeyInputProvider, m.settingsKeyInput.View(), settingsShowThinking, m.modelsLoading, m.modelsForProvider, m.providerEmptyHint)
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
	statusBar := renderStatusBar(m.width, connected, reconnecting, m.statusMsg, m.styles)
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
		overlay := renderQuitDialog(m.width, m.height, m.styles, m.quitSelected)
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
		overlay := sess.slashMenu.View(60, 8, m.styles)
		if overlay != "" {
			_, h := lipgloss.Size(overlay)
			inputTop := m.height - layout.StatusBarHeight - layout.InputHeight
			popupY := inputTop - h
			if popupY < 0 {
				popupY = 0
			}
			uv.NewStyledString(overlay).Draw(canvas, image.Rect(2, popupY, 2+60, popupY+h))
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
	case "change_model":
		if sess != nil {
			// Show the current best catalogue immediately; if we've never
			// fetched, show a loading state. Always kick off a fresh fetch so
			// the list is up to date every time it opens.
			sess.rightPanel.OpenModelSelect(m.height, sess.modelName, m.allModelsFlat(), !m.modelsLoaded)
			m.modelsLoading = true
			cmds = append(cmds, loadModelsCmd(m.socketPath, m.authToken))
			m.updateChatWidth()
			sess.focus = FocusRightPanel
			sess.input.Blur()
		}
	case "manage_keys":
		if sess != nil {
			sess.rightPanel.OpenKeyManager(m.height)
			m.updateChatWidth()
			sess.focus = FocusRightPanel
			sess.input.Blur()
		}
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
		if sess != nil && sess.client != nil {
			sess.client.SendCancel()
			sess.client.SendClose()
		}
		cmds = append(cmds, tea.Quit)
	default:
		if strings.HasPrefix(action, "switch_tab_") {
			idxStr := strings.TrimPrefix(action, "switch_tab_")
			if i, err := strconv.Atoi(idxStr); err == nil {
				switch TabKind(i) {
				case TabKindSessions:
					m.activeTab = TabKindSessions
					m.syncSessionsSelected()
					cmds = append(cmds, m.sessionsInput.Focus())
				case TabKindChat:
					m.activeTab = TabKindChat
					if sess != nil {
						sess.unreadCount = 0
						cmds = append(cmds, sess.thinkingAnim.Resume())
					}
				case TabKindSettings:
					m.activeTab = TabKindSettings
					m.settingsKeys = config.ListStoredProviderKeys()
					m.settingsKeySel = 0
					m.settingsInKeyInput = false
					m.settingsActiveSection = 0
					m.settingsModelColumn = 0
					m.settingsProviderSel = 0
					m.settingsModelSel = 0
					m.settingsModelPending = ""
					initActiveModel3 := m.cfg.Model
					if initSess3 := m.currentSession(); initSess3 != nil && initSess3.modelName != "" {
						initActiveModel3 = initSess3.modelName
					}
					m.settingsProviderSel, m.settingsModelSel = locateActiveModel(initActiveModel3)
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
	chatContent := buildRenderedChat(sess.chatMessages, m.styles, m.mdRenderer.width)
	if sess.showThinking && sess.thinkingRendered != "" {
		chatContent += sess.thinkingRendered + "\n"
	}
	if sess.assistantRendered != "" {
		chatContent += sess.assistantRendered
	}
	if chatContent == "" && !m.testMode {
		chatContent = renderWelcomeInline(m.mdRenderer.width, contentHeight, m.styles)
	}
	innerWidth := m.mdRenderer.width
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

// sessionActiveForkSep returns the topmost visible turn separator when scrolled up.
func (m *Model) sessionActiveForkSep(sess *SessionState) (TurnSepInfo, bool) {
	if sess.chatScrollOffset == 0 || sess.client == nil {
		return TurnSepInfo{}, false
	}
	layout := computeLayout(m.width, m.height, m.visualLineCount())
	contentHeight := layout.ChatHeight - 1
	chatContent := buildRenderedChat(sess.chatMessages, m.styles, m.mdRenderer.width)
	if sess.showThinking && sess.thinkingRendered != "" {
		chatContent += sess.thinkingRendered + "\n"
	}
	if sess.assistantRendered != "" {
		chatContent += sess.assistantRendered
	}
	innerWidth := m.mdRenderer.width
	allLines := strings.Split(chatContent, "\n")
	visualRowStart := make([]int, len(allLines)+1)
	for i, line := range allLines {
		visualRowStart[i+1] = visualRowStart[i] + visualRows(line, innerWidth)
	}
	totalVisualRows := visualRowStart[len(allLines)]
	endVisRow := totalVisualRows - sess.chatScrollOffset
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
	for _, s := range turnSeparatorInfos(sess.chatMessages, m.styles, m.mdRenderer.width) {
		if s.LineIdx >= startLogical && s.LineIdx < endLogical {
			return s, true
		}
	}
	return TurnSepInfo{}, false
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
	return *m, reconnectCmd
}

// updateChatWidth updates the markdown renderer width to match the current effective chat width.
func (m *Model) updateChatWidth() {
	sess := m.currentSession()
	chatWidth := computeLayout(m.width, m.height, m.visualLineCount()).ChatWidth
	if sess != nil && sess.rightPanel.IsVisible() {
		chatWidth = m.width - sess.rightPanel.PanelWidth()
		if chatWidth < 10 {
			chatWidth = 10
		}
	}
	m.mdRenderer.UpdateWidth(chatWidth - 4)
	m.rerenderSessionMessages()
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

// visibleSessionIndices returns the indices of sessions that match the current filter.
func (m *Model) visibleSessionIndices() []int {
	const colSession = 10
	const colMessage = 52
	filterLower := strings.ToLower(m.sessionsInput.Value())
	var indices []int
	for i, sess := range m.sessions {
		if filterLower == "" {
			indices = append(indices, i)
			continue
		}
		sessionCol := "connecting…"
		if sess.client != nil {
			id := sess.client.SessionID()
			if dash := strings.Index(id, "-"); dash >= 0 {
				sessionCol = id[:dash]
			} else if len(id) > colSession {
				sessionCol = id[:colSession]
			} else {
				sessionCol = id
			}
		}
		msgCol := "—"
		for _, msg := range sess.chatMessages {
			if msg.Type == MsgUser {
				line := strings.SplitN(msg.Text, "\n", 2)[0]
				if len(line) > colMessage {
					line = line[:colMessage-1] + "…"
				}
				msgCol = line
				break
			}
		}
		if strings.Contains(strings.ToLower(sessionCol), filterLower) ||
			strings.Contains(strings.ToLower(msgCol), filterLower) {
			indices = append(indices, i)
		}
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
