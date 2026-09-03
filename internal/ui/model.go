package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
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

// threadEventMsg carries a daemon thread event tagged with the daemon thread
// ID of the connection that produced it. Messages whose daemonThreadID no
// longer matches the thread's current daemonThreadID are silently dropped
// (they came from a superseded connection's goroutine).
type threadEventMsg struct {
	daemonThreadID string
	event          protocol.ThreadEvent
}

// threadDisconnectedMsg is sent when a thread's daemon connection is lost.
type threadDisconnectedMsg struct {
	daemonThreadID string
}

// updateInstallDoneMsg is delivered after the in-app update install command
// (run via tea.ExecProcess) finishes. A nil err means the new binaries are on
// disk and the user can quit-all to apply them.
type updateInstallDoneMsg struct {
	err error
}

// reconnectSuccessMsg is sent when reconnection succeeds.
// daemonThreadID is the ID of the thread we were reconnecting for (the old
// one); client is the newly established connection with its own fresh ID.
//
// clientKey disambiguates the fork/duplicate path, where the new tab has no
// daemonThreadID yet (it is empty until the fork connects). Matching an empty
// daemonThreadID would pick the first draft in the list — not necessarily the
// tab we forked — so a clientKey, when set, is matched first (mirroring the
// draft-connect path). The genuine reconnect path leaves it empty and matches
// by daemonThreadID.
type reconnectSuccessMsg struct {
	daemonThreadID string
	clientKey      string
	client         *daemon.ThreadClient
}

// reconnectFailedMsg is sent when reconnection fails.
type reconnectFailedMsg struct {
	daemonThreadID string
}

// threadOrphanedMsg is sent when an attach reconnect reports the thread no
// longer exists on disk (lost in a daemon restart before its first flush). The
// thread can't be continued; the handler orphans it and tells the user to
// /copy the conversation.
type threadOrphanedMsg struct {
	daemonThreadID string
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

// startThreadEventLoop launches a goroutine that reads daemon events for one
// thread and injects them into the Bubble Tea loop tagged with the daemon
// thread ID captured at launch time. When a thread reconnects it gets a new
// daemon thread ID, so any in-flight messages from the old goroutine are
// naturally ignored by the handler's ID check — no generation counter needed.
func startThreadEventLoop(client *daemon.ThreadClient) tea.Cmd {
	daemonThreadID := client.ThreadID()
	return func() tea.Msg {
		if teaProgram == nil {
			return threadDisconnectedMsg{daemonThreadID: daemonThreadID}
		}
		go func() {
			for {
				event, err := client.ReadEvent()
				if err != nil {
					teaProgram.Send(threadDisconnectedMsg{daemonThreadID: daemonThreadID})
					return
				}
				teaProgram.Send(threadEventMsg{daemonThreadID: daemonThreadID, event: event})
			}
		}()
		return nil
	}
}

// instanceEventMsg carries a process-level event delivered over the window's
// instance control channel (threads_changed, jobs_changed, quit). Unlike
// threadEventMsg it is not tied to any chat thread — it is delivered once per
// window, even to a launch-time draft with no thread yet.
type instanceEventMsg struct {
	event protocol.ThreadEvent
}

// startInstanceEventLoop launches a goroutine that reads process-level events
// from the window's instance control channel and injects them into the Bubble
// Tea loop. Mirrors startThreadEventLoop but is thread-independent: it runs
// from launch and delivers threads_changed/jobs_changed/quit exactly once per
// window. Returns nil (no message) when there is no control channel.
func startInstanceEventLoop(ic *daemon.InstanceClient) tea.Cmd {
	return func() tea.Msg {
		if teaProgram == nil || ic == nil {
			return nil
		}
		go func() {
			for {
				event, err := ic.ReadEvent()
				if err != nil {
					return
				}
				teaProgram.Send(instanceEventMsg{event: event})
			}
		}()
		return nil
	}
}

// draftConnectedMsg is sent when a draft thread's deferred connection (opened
// on its first message) succeeds. The handler wires the client, marks the
// thread live, and flushes the queued first message.
type draftConnectedMsg struct {
	clientKey string
	client    *daemon.ThreadClient
}

// draftConnectFailedMsg is sent when a draft's deferred connection fails. The
// thread stays a draft so the user can retry (or change the directory).
type draftConnectFailedMsg struct {
	clientKey string
	err       error
}

// connectDraft opens the daemon connection for a draft thread on its first
// message, in the chosen working directory. Unlike attemptReconnect it never
// attaches by ID (a draft has no daemon-side record yet) and echoes back the
// stable clientKey so the Update loop can match the result to the right draft.
func connectDraft(socketPath, clientKey, cwd, configDir, model, authToken string, forceInit, enableWrite, enableDir bool) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		if !client.Ping() {
			return draftConnectFailedMsg{clientKey: clientKey, err: fmt.Errorf("daemon is not responding")}
		}
		thread := daemon.NewThreadClient(socketPath)
		thread.SetAuthToken(authToken)
		if err := thread.Connect(cwd, configDir, model, forceInit, enableWrite, enableDir, false); err != nil {
			return draftConnectFailedMsg{clientKey: clientKey, err: err}
		}
		return draftConnectedMsg{clientKey: clientKey, client: thread}
	}
}

// fileAttachmentValidatedMsg carries the daemon's verdict for a drag-dropped
// text/PDF file. Matched back to its thread by clientKey.
type fileAttachmentValidatedMsg struct {
	clientKey string
	cand      fileCandidate
	status    string // "ok", "invalid", "error"
	reason    string
}

// validateFileAttachmentCmd asks the daemon (attachment.validate) whether a
// drag-dropped file can be attached, without blocking the UI loop.
func validateFileAttachmentCmd(socketPath, authToken, threadID, clientKey string, cand fileCandidate) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		status, reason, err := client.ValidateAttachment(threadID, cand.Path)
		if err != nil {
			return fileAttachmentValidatedMsg{clientKey: clientKey, cand: cand, status: "invalid", reason: err.Error()}
		}
		return fileAttachmentValidatedMsg{clientKey: clientKey, cand: cand, status: status, reason: reason}
	}
}

// attemptReconnect tries to reconnect a thread to the daemon.
// targetDaemonThreadID identifies which thread this attempt is for; it is
// echoed back in the result message so the handler can match it to the right
// thread. Pass an empty string for a thread that has never connected — the
// handler will not retry on failure in that case.
func attemptReconnect(socketPath, cwd, configDir, model, authToken string, forceInit, enableWrite, enableDir bool, targetDaemonThreadID string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		if !client.Ping() {
			time.Sleep(2 * time.Second)
			return reconnectFailedMsg{daemonThreadID: targetDaemonThreadID}
		}
		thread := daemon.NewThreadClient(socketPath)
		thread.SetAuthToken(authToken)
		// A thread that has connected before is resumed by ID (attach), so a
		// restarted daemon rebuilds it from disk. An empty target ID is a
		// brand-new thread that has never connected — start it fresh.
		var err error
		if targetDaemonThreadID == "" {
			err = thread.Connect(cwd, configDir, model, forceInit, enableWrite, enableDir, false)
		} else {
			err = thread.Attach(cwd, configDir, model, forceInit, enableWrite, enableDir, false, targetDaemonThreadID)
			if errors.Is(err, daemon.ErrThreadNotFound) {
				// The daemon restarted and lost this thread before it was
				// flushed. It can't be continued; orphan it (offer /copy).
				return threadOrphanedMsg{daemonThreadID: targetDaemonThreadID}
			}
		}
		if err != nil {
			time.Sleep(2 * time.Second)
			return reconnectFailedMsg{daemonThreadID: targetDaemonThreadID}
		}
		return reconnectSuccessMsg{daemonThreadID: targetDaemonThreadID, client: thread}
	}
}

// threadRestoredMsg is sent when a persisted open thread is successfully
// re-attached on launch. The handler adds a new ThreadState for it.
type threadRestoredMsg struct {
	summary protocol.ThreadSummary
	client  *daemon.ThreadClient
}

// threadRestoreFailedMsg is sent when reopening a persisted thread fails (the
// daemon is gone or the record vanished). The thread is simply not restored.
type threadRestoreFailedMsg struct {
	id string
}

// attachRestoreThread reopens a persisted thread on launch by attaching to it
// by ID. Used for the open threads beyond the first (which main attaches as the
// initial client).
func attachRestoreThread(socketPath, cwd, configDir, model, authToken string, enableWrite, enableDir bool, summary protocol.ThreadSummary) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		if !client.Ping() {
			return threadRestoreFailedMsg{id: summary.ID}
		}
		sc := daemon.NewThreadClient(socketPath)
		sc.SetAuthToken(authToken)
		if err := sc.Attach(cwd, configDir, model, false, enableWrite, enableDir, false, summary.ID); err != nil {
			return threadRestoreFailedMsg{id: summary.ID}
		}
		return threadRestoredMsg{summary: summary, client: sc}
	}
}

// recentDirsMsg carries the ranked working directories fetched from the daemon
// for the welcome screen's recent-directories list.
type recentDirsMsg struct {
	dirs []protocol.DirUsage
}

// fetchRecentDirs asks the daemon for the working directories used by open user
// threads, ranked for the welcome screen. A failure yields an empty list.
func fetchRecentDirs(socketPath, cwd, configDir, authToken string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		dirs, err := client.ListThreadDirs(cwd, configDir)
		if err != nil {
			return recentDirsMsg{}
		}
		return recentDirsMsg{dirs: dirs}
	}
}

// connectFork starts a new forked thread seeded from forkThreadID at forkTurnIdx.
// clientKey is the forking tab's stable handle: the fork tab has no
// daemonThreadID yet, so the success handler matches the result back to it by
// clientKey rather than the (empty) daemon id.
func connectFork(socketPath, cwd, configDir, model, authToken string, enableWrite, enableDir bool, forkThreadID string, forkTurnIdx int, targetDaemonThreadID, clientKey string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		if !client.Ping() {
			time.Sleep(2 * time.Second)
			return reconnectFailedMsg{daemonThreadID: targetDaemonThreadID}
		}
		thread := daemon.NewThreadClient(socketPath)
		thread.SetAuthToken(authToken)
		if err := thread.ConnectFork(cwd, configDir, model, false, enableWrite, enableDir, false, forkThreadID, forkTurnIdx); err != nil {
			time.Sleep(2 * time.Second)
			return reconnectFailedMsg{daemonThreadID: targetDaemonThreadID}
		}
		return reconnectSuccessMsg{daemonThreadID: targetDaemonThreadID, clientKey: clientKey, client: thread}
	}
}

// findThreadByDaemonID returns the index and pointer of the thread with the
// given daemon thread ID, or (-1, nil) if not found.
func (m *Model) findThreadByDaemonID(id string) (int, *ThreadState) {
	for i, s := range m.threads {
		if s.daemonThreadID == id {
			return i, s
		}
	}
	return -1, nil
}

// findThreadByClientKey locates a thread by its stable client-side handle.
// Used to match async draft-connect results, since a draft's daemonThreadID is
// empty until it commits (and several drafts may coexist).
func (m *Model) findThreadByClientKey(key string) (int, *ThreadState) {
	for i, s := range m.threads {
		if s.clientKey == key {
			return i, s
		}
	}
	return -1, nil
}

// pickCWD returns primary when it is non-blank, else fallback. Used to prefer a
// thread's own working directory over the model-global launch cwd.
func pickCWD(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

// maxRecentDirs bounds the recent-directories list rendered on the welcome
// screen.
const maxRecentDirs = 5

// topRecentDirs returns the highest-ranked recent working directories, trimmed
// to maxRecentDirs, for the welcome screen.
func (m *Model) topRecentDirs() []protocol.DirUsage {
	if len(m.recentDirs) <= maxRecentDirs {
		return m.recentDirs
	}
	return m.recentDirs[:maxRecentDirs]
}

// latestWorkDir returns the working directory a fresh draft thread should
// default to: the most-recently-active recorded directory, falling back to the
// launch cwd when there is no history.
func (m *Model) latestWorkDir() string {
	best := ""
	bestTS := ""
	for _, d := range m.recentDirs {
		if d.Path == "" {
			continue
		}
		// recentDirs is ranked by count, so scan for the most recent activity.
		if best == "" || d.LastRequestAt > bestTS {
			best = d.Path
			bestTS = d.LastRequestAt
		}
	}
	if best != "" {
		return best
	}
	return m.cwd
}

// welcomeDirNav handles up/down/enter for the recent-directories list on a
// focused draft welcome. A draft with an empty transcript shows the welcome
// screen; when its area is focused (Tab), up/down move the highlighted
// directory and enter applies it to the working directory. The guard mirrors
// the Ctrl+O picker: only before the thread starts connecting. It returns true
// when it consumed the key.
func (m *Model) welcomeDirNav(sess *ThreadState, key string) bool {
	if sess == nil || sess.focus != FocusChat || sess.phase != phaseDraft ||
		sess.client != nil || sess.reconnecting || len(sess.chatMessages) != 0 {
		return false
	}
	recent := m.topRecentDirs()
	if len(recent) == 0 {
		return false
	}
	switch key {
	case "up", "k":
		if sess.recentDirSelected > 0 {
			sess.recentDirSelected--
		}
		return true
	case "down", "j":
		if sess.recentDirSelected < len(recent)-1 {
			sess.recentDirSelected++
		}
		return true
	case "enter":
		if sess.recentDirSelected >= 0 && sess.recentDirSelected < len(recent) {
			sess.workDir = recent[sess.recentDirSelected].Path
		}
		// Picking a directory is a "done choosing, let me type" signal, so
		// snap focus back to the editor input (mirrors the Tab handler).
		sess.focus = FocusEditor
		sess.input.Focus()
		return true
	}
	return false
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
	StateThreadCloseConfirm
	StateThreadRename
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

func markCancelledReadyForInput(sess *ThreadState) {
	sess.thinkingAnim.Stop()
	sess.pendingInput = nil
	sess.cancelAckPending = true
	sess.agentState = StateWaitingForInput
	sess.focus = FocusEditor
	sess.input.Focus()
}

// Model is the root Bubble Tea model.
type Model struct {
	width, height int

	// Two visible tabs: Threads list and Chat display.
	activeTab TabKind

	// All active threads. Each accumulates messages independently.
	threads        []*ThreadState
	selectedThread int // index into threads; which thread the Chat tab shows

	// Global overlay dialog state (quit confirm, thread close confirm).
	// Normal operation = StateWaitingForInput (no overlay).
	state               AppState
	quitSelected        int
	quitCloseAll        bool // quit-dialog checkbox: close all threads on quit
	threadCloseIdx      int
	threadCloseSelected int
	// vixDismissID, when non-empty, marks that the close dialog targets a
	// persisted vix-initiated record (dismissed, not closed) with that ID.
	vixDismissID string

	// Rename dialog (StateThreadRename). renameInput holds the editable title
	// (pre-filled with the current one). Exactly one target is set: renameIdx
	// >= 0 for a live thread (m.threads[renameIdx]), or renameID for a
	// persisted, not-open record renamed by ID over a one-shot RPC.
	renameInput textinput.Model
	renameIdx   int
	renameID    string

	// Threads tab UI
	threadsSelected int
	// collapsedDirs tracks which User-initiated directory blocks are folded in
	// the Threads tab, keyed by the block's directory path. Session-only (not
	// persisted). A folded directory hides its thread rows behind its path
	// header; the header itself stays selectable so Enter can unfold it.
	collapsedDirs map[string]bool
	// vixThreads are the persisted vix-initiated records (job runs, alerts),
	// rendered as their own group below the user-initiated threads.
	// Refreshed on Init, on entering the tab, and on event.threads_changed.
	vixThreads []protocol.ThreadSummary
	// userThreadRecords are the persisted, not-currently-attached
	// user-initiated threads across every working directory. The Threads tab
	// groups them by directory alongside the live threads (the current cwd's
	// threads are auto-attached on launch, so they appear as live rows and are
	// excluded here by the Attached filter). Refreshed alongside vixThreads.
	userThreadRecords []protocol.ThreadSummary

	// Jobs & Triggers tab UI: the scheduled jobs and lifecycle hooks, refreshed
	// on entering the tab and on event.jobs_changed. jobsSelected is a single
	// cursor spanning the Jobs group then the Triggers group.
	jobs         []protocol.JobSummary
	hooks        []protocol.HookSummary
	jobsSelected int
	// MCP tab UI: the configured MCP servers, refreshed on entering the tab and
	// on event.mcp_changed. mcpSelected is the row cursor.
	mcpServers  []protocol.MCPServerSummary
	mcpSelected int
	// focusRestoredID, when set, names a thread the user just opened from
	// the Threads tab (enter on a vix-initiated row): the matching
	// threadRestoredMsg focuses it instead of restoring in the background.
	focusRestoredID string
	// vixSeeded guards the one-shot launch seeding of threadsTabUnseen from
	// persisted unread vix records (first vixThreadsMsg only).
	vixSeeded bool

	// Models tab UI
	modelsLoggedIn         []string                             // providers with a stored credential
	modelsAvailable        []string                             // providers without one
	modelsLocal            []string                             // local providers (Ollama, llama.cpp), own group
	modelsLocalUI          map[string]LocalProviderUI           // live probe state per local provider
	modelsStatus           map[string]config.ProviderAuthStatus // per-provider auth status (refreshed on change)
	modelsProviderSel      int                                  // index into modelsLoggedIn ++ modelsAvailable ++ modelsLocal
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

	// whiteboardBase is the local web UI origin (e.g. "http://localhost:1337")
	// reported by the daemon in thread_started and captured by ThreadClient. It
	// is a cache of the last non-empty value read from the session's client in
	// syncMermaidCtx. Empty when the web UI is disabled. Used to build whiteboard
	// links for mermaid diagrams.
	whiteboardBase string

	// lastChatWidth records the effective (panel-aware) chat width the markdown
	// renderer and cached messages were last reconciled at. reconcileChatWidth
	// uses it to detect panel/thread/resize transitions and re-flow once.
	lastChatWidth int

	// Tab alert blink (Chat tab label pulses when a thread needs attention)
	tabAlertActive   bool
	tabAlertBlinkOn  bool
	tabAlertBlinkGen int

	// Threads-list loading spinner. A single shared ticker animates the
	// per-row indicator for threads that are actively working. It runs only
	// while the Threads tab is the active view AND at least one thread is
	// busy, so it never animates for threads the user can't see.
	threadsSpinnerActive bool
	threadsSpinnerStep   int
	threadsSpinnerGen    int // bumped on stop; invalidates in-flight ticks

	// threadsTabUnseen marks that a message arrived while the Threads tab was
	// not focused; it tints the Threads tab title secondary (static, not
	// blinking) and is cleared when the user visits the Threads tab.
	threadsTabUnseen bool

	// Transient status bar message (second line)
	statusMsg StatusMessage

	// alertPopup holds the text of a persistent, centered error popup. When
	// non-empty it renders as a modal overlay that stays until the user
	// dismisses it with a key press. Error-kind status messages route here
	// instead of the transient status bar.
	alertPopup string

	// Connection parameters (for reconnect / new threads)
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

	// Search-tool backends resolved by the daemon (via event.tool_backends),
	// shown read-only in the Settings tab. The *Effective values reflect PATH
	// fallback; the *Configured values flag when the requested backend wasn't
	// available.
	grepBackendEffective  string
	grepBackendConfigured string
	globBackendEffective  string
	globBackendConfigured string

	// Update status (from the daemon's daily release check, via
	// event.update_available) and in-app upgrade flow state.
	updateCurrent   string // running version
	updateLatest    string // newer release tag, "" when up-to-date/unknown
	updateURL       string
	updateMethod    string // "brew" | "script" | "unknown"
	updateInstalled bool   // install command completed successfully
	updateErr       string // last install error, if any

	// restoreThreads holds persisted open threads (beyond the first, which is
	// the initial client) to reopen on Init.
	restoreThreads []protocol.ThreadSummary

	// recentDirs holds the working directories used by open user threads,
	// ranked by thread count (then recency). Fetched from the daemon on Init
	// and refreshed on event.threads_changed. Powers the welcome screen's
	// recent-directories list and the default cwd for new draft threads.
	recentDirs []protocol.DirUsage

	// instanceClient is the window's long-lived control channel to the daemon.
	// Its read loop (startInstanceEventLoop) delivers process-level events
	// (threads_changed, jobs_changed, quit) once per window, independent of any
	// chat thread — so a launch-time draft still refreshes live. nil in test
	// mode and when instance registration failed.
	instanceClient *daemon.InstanceClient
}

// SetInstanceClient records the window's daemon control channel. Called once by
// main before the program starts; Init launches its read loop.
func (m *Model) SetInstanceClient(ic *daemon.InstanceClient) {
	m.instanceClient = ic
}

// SetRestoreThreads records the persisted open threads the TUI should reopen
// on launch (attached lazily from Init). Called once by main before the program
// starts.
func (m *Model) SetRestoreThreads(s []protocol.ThreadSummary) {
	m.restoreThreads = s
}

// SetInitialAwaitingReplay marks the initial thread as one that was attached
// (restored) on launch and is still waiting for its event.replay. While true the
// chat area shows a "Restoring conversation…" placeholder instead of the welcome
// screen. Called once by main before the program starts.
func (m *Model) SetInitialAwaitingReplay(awaiting bool) {
	if awaiting && len(m.threads) > 0 {
		m.threads[0].awaitingReplay = true
	}
}

// currentThread returns the selected thread, or nil if there is none.
func (m *Model) currentThread() *ThreadState {
	if m.selectedThread < 0 || m.selectedThread >= len(m.threads) {
		return nil
	}
	return m.threads[m.selectedThread]
}

// NewModel creates a new root Model.
func NewModel(cfg *config.Config, client *daemon.ThreadClient, testMode bool, authToken string, enableWrite, enableDir bool) Model {
	initialThread := newThreadState(cfg, client)

	m := Model{
		state:                          StateWaitingForInput,
		activeTab:                      TabKindChat,
		threads:                        []*ThreadState{initialThread},
		selectedThread:                 0,
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
	// Start the window's control-channel read loop unconditionally, before and
	// independent of any thread, so process-level events (threads_changed,
	// jobs_changed, quit) are received even while the window is still a draft.
	if m.instanceClient != nil {
		cmds = append(cmds, startInstanceEventLoop(m.instanceClient))
	}
	if sess := m.currentThread(); sess != nil && sess.client != nil {
		cmds = append(cmds, startThreadEventLoop(sess.client))
		// A restored initial thread shows the "Restoring conversation…"
		// placeholder until its replay arrives; animate its spinner.
		if sess.awaitingReplay {
			cmds = append(cmds, sess.thinkingAnim.Start())
		}
	}
	// Reopen any persisted open threads beyond the initial one.
	for _, sum := range m.restoreThreads {
		cmds = append(cmds, attachRestoreThread(m.socketPath, pickCWD(sum.CWD, m.cwd), m.cfg.ConfigDir, m.cfg.Model, m.authToken, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, sum))
	}
	// Populate the Vix-initiated group of the Threads tab.
	cmds = append(cmds, fetchVixThreads(m.socketPath, m.cwd, m.cfg.ConfigDir, m.authToken))
	// Populate the welcome screen's recent-directories list.
	cmds = append(cmds, fetchRecentDirs(m.socketPath, m.cwd, m.cfg.ConfigDir, m.authToken))
	cmds = append(cmds, waitForResume, tea.RequestBackgroundColor)
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
// Update is the central tea.Model update entry point. It delegates to updateInner
// (the real message handler) and then reconciles the panel-aware chat width on
// the resulting model, so panel open/close, thread switches, and resizes all
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
		sess := m.currentThread()
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
				m.closeThreadsForQuit(m.quitCloseAll)
				return m, tea.Quit
			}
			m.alertPopup = ""
			m.state = StateQuitConfirm
			m.quitSelected = 0
			m.quitCloseAll = config.CloseAllThreadsOnQuit()
			return m, nil
		}

		// --- Error alert popup intercepts all keys (dismiss on any key) ---
		if m.alertPopup != "" {
			m.alertPopup = ""
			return m, nil
		}

		// --- Quit / ThreadClose / Trim dialogs intercept all keys ---
		if m.state == StateQuitConfirm || m.state == StateThreadCloseConfirm {
			return m.handleDialogKey(msg)
		}
		if m.state == StateThreadRename {
			return m.handleRenameKey(msg)
		}
		if m.state == StateKeyDeleteConfirm {
			return m.handleKeyDeleteKey(msg)
		}
		sess := m.currentThread()
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
			if !m.commandPalette.IsVisible() && sess != nil && sess.focus != FocusRightPanel && m.activeTab != TabKindThreads && m.activeTab != TabKindModels && m.activeTab != TabKindJobs && m.activeTab != TabKindSettings {
				sess.input.Focus()
				sess.focus = FocusEditor
			}
			return m, tea.Batch(cmds...)
		}

		// --- Tab switching (F1–F6), shared across all tabs/focus ---
		switch msg.String() {
		case "f1":
			return m, m.switchTab(TabKindThreads)
		case "f2":
			return m, m.switchTab(TabKindChat)
		case "f3":
			return m, m.switchTab(TabKindModels)
		case "f4":
			return m, m.switchTab(TabKindMcp)
		case "f5":
			return m, m.switchTab(TabKindJobs)
		case "f6":
			return m, m.switchTab(TabKindSettings)
		}

		// --- Global workspace shortcuts ---
		switch msg.String() {
		case "ctrl+n":
			if stepCmds, ok := m.stepWorkspaceThread(1); ok {
				cmds = append(cmds, stepCmds...)
			} else if curSess := m.currentThread(); curSess != nil {
				return m, m.emitStatusMsg("No next thread", StatusMsgWarning)
			}
			return m, tea.Batch(cmds...)

		case "ctrl+p":
			if stepCmds, ok := m.stepWorkspaceThread(-1); ok {
				cmds = append(cmds, stepCmds...)
			} else if curSess := m.currentThread(); curSess != nil {
				return m, m.emitStatusMsg("No previous thread", StatusMsgWarning)
			}
			return m, tea.Batch(cmds...)

		case "ctrl+t":
			newSess := newThreadState(m.cfg, nil)
			newSess.workDir = m.latestWorkDir()
			newSess.input.SetWidth(m.width - 4)
			newIdx := len(m.threads)
			m.threads = append(m.threads, newSess)
			m.selectedThread = newIdx
			m.activeTab = TabKindChat
			// A ctrl+t tab starts as a draft (no connection) so the user can
			// pick its working directory before committing on the first message.
			// It defaults to the most-recently-used working directory.
			cmds = append(cmds, armCursorBlink(newSess))
			return m, tea.Batch(cmds...)

		}

		// --- Threads tab key handling ---
		if m.activeTab == TabKindThreads {
			switch msg.String() {
			case "up":
				if m.threadsSelected > 0 {
					m.threadsSelected--
				}
				return m, nil
			case "down":
				if n := len(m.selectableThreadRows()); m.threadsSelected < n-1 {
					m.threadsSelected++
				}
				return m, nil
			case "space", " ":
				// Fold/unfold the directory that encloses the cursor (a header
				// row or a thread row under it). Vix rows have no directory.
				m.foldSelectedDir()
				return m, nil
			case "left":
				// Jump the cursor up to the enclosing directory header.
				m.selectEnclosingDir()
				return m, nil
			case "enter":
				// A directory header toggles its fold state; the cursor stays put.
				sel := m.selectableThreadRows()
				if m.threadsSelected >= 0 && m.threadsSelected < len(sel) && sel[m.threadsSelected].kind == rowDirHeader {
					m.foldSelectedDir()
					return m, nil
				}
				if sum, ok := m.vixSelectedSummary(); ok {
					// Open a vix-initiated record: attach it like a restored
					// thread; the replay rebuilds the conversation and the
					// matching threadRestoredMsg focuses it.
					m.focusRestoredID = sum.ID
					return m, attachRestoreThread(m.socketPath, pickCWD(sum.CWD, m.cwd), m.cfg.ConfigDir, m.cfg.Model, m.authToken, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, sum)
				}
				if idx, ok := m.threadsSelectedIdx(); ok {
					m.selectedThread = idx
					m.activeTab = TabKindChat
					selSess := m.threads[idx]
					m.markThreadRead(selSess)
					selSess.input.SetWidth(m.width - 4)
					if selSess.client == nil && !selSess.reconnecting && selSess.daemonThreadID != "" {
						selSess.reconnecting = true
						cmds = append(cmds, attemptReconnect(m.socketPath, pickCWD(selSess.workDir, m.cwd), m.cfg.ConfigDir, m.cfg.Model, m.authToken, false, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, selSess.daemonThreadID))
					}
					cmds = append(cmds, selSess.thinkingAnim.Resume())
					cmds = append(cmds, armCursorBlink(selSess))
					if !m.hasAlertThreads() {
						m.stopTabAlertBlink()
					}
				}
				return m, tea.Batch(cmds...)
			case "t":
				// Add a new thread (draft — connects on first message).
				newSess := newThreadState(m.cfg, nil)
				newSess.workDir = m.latestWorkDir()
				newSess.input.SetWidth(m.width - 4)
				newIdx := len(m.threads)
				m.threads = append(m.threads, newSess)
				m.selectedThread = newIdx
				m.activeTab = TabKindChat
				cmds = append(cmds, armCursorBlink(newSess))
				return m, tea.Batch(cmds...)
			case "d":
				// Duplicate the selected thread into a new one.
				if _, ok := m.vixSelectedSummary(); ok {
					return m, m.emitStatusMsg("Open the run first to duplicate it", StatusMsgWarning)
				}
				idx, ok := m.threadsSelectedIdx()
				if !ok {
					return m, nil
				}
				srcSess := m.threads[idx]
				if srcSess.client == nil {
					return m, m.emitStatusMsg("Thread is still connecting; cannot duplicate", StatusMsgWarning)
				}
				seps := turnSeparatorInfos(srcSess.chatMessages, m.styles, m.mdRenderer.width)
				if len(seps) == 0 {
					return m, m.emitStatusMsg("Nothing to duplicate: no completed turns yet", StatusMsgWarning)
				}
				lastSep := seps[len(seps)-1]
				nm, c := m.doDuplicate(srcSess, lastSep)
				return nm, c
			case "r":
				// Rename the selected conversation.
				return m, m.beginRenameSelected()
			case "x":
				if sum, ok := m.vixSelectedSummary(); ok {
					// Dismiss a vix-initiated record: same confirmation dialog
					// as closing a live thread.
					m.vixDismissID = sum.ID
					m.threadCloseSelected = 1 // default No
					m.state = StateThreadCloseConfirm
					return m, nil
				}
				if idx, ok := m.threadsSelectedIdx(); ok {
					m.threadCloseIdx = idx
					m.threadCloseSelected = 1 // default No
					m.state = StateThreadCloseConfirm
				}
				return m, nil
			}
		}

		// --- Models tab key handling ---
		if m.activeTab == TabKindModels {
			return m.handleModelsKey(msg)
		}

		// --- MCP tab key handling ---
		if m.activeTab == TabKindMcp {
			switch msg.String() {
			case "up", "k":
				if m.mcpSelected > 0 {
					m.mcpSelected--
				}
				return m, nil
			case "down", "j":
				if m.mcpSelected < len(m.mcpServers)-1 {
					m.mcpSelected++
				}
				return m, nil
			case "space", " ":
				if m.mcpSelected >= 0 && m.mcpSelected < len(m.mcpServers) {
					srv := m.mcpServers[m.mcpSelected]
					return m, setMCPEnabled(m.socketPath, m.authToken, srv.Name, !srv.Enabled)
				}
				return m, nil
			case "a":
				// Authenticate the selected OAuth server (needs_auth).
				if m.mcpSelected >= 0 && m.mcpSelected < len(m.mcpServers) {
					srv := m.mcpServers[m.mcpSelected]
					if srv.Auth == "needs_auth" {
						return m, authorizeMCP(m.socketPath, m.authToken, srv.Name)
					}
				}
				return m, nil
			case "o":
				// Sign out of the selected authenticated OAuth server.
				if m.mcpSelected >= 0 && m.mcpSelected < len(m.mcpServers) {
					srv := m.mcpServers[m.mcpSelected]
					if srv.Auth == "authenticated" {
						return m, logoutMCP(m.socketPath, m.authToken, srv.Name)
					}
				}
				return m, nil
			}
			return m, nil
		}

		// --- Jobs & Triggers tab key handling ---
		if m.activeTab == TabKindJobs {
			switch msg.String() {
			case "up", "k":
				if m.jobsSelected > 0 {
					m.jobsSelected--
				}
				return m, nil
			case "down", "j":
				if n := len(m.jobs) + len(m.hooks); m.jobsSelected < n-1 {
					m.jobsSelected++
				}
				return m, nil
			case "space", " ":
				// Toggle enabled on the selected job or hook. The cursor indexes
				// jobs first, then hooks.
				if m.jobsSelected < len(m.jobs) {
					j := m.jobs[m.jobsSelected]
					return m, setJobEnabled(m.socketPath, m.authToken, j.ID, !j.Enabled)
				}
				hi := m.jobsSelected - len(m.jobs)
				if hi >= 0 && hi < len(m.hooks) {
					h := m.hooks[hi]
					return m, setHookEnabled(m.socketPath, m.authToken, h.ID, !h.Enabled)
				}
				return m, nil
			}
			return m, nil
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

		// --- Chat tab key handling (thread-specific) ---
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

		// Directory picker (draft welcome screen, Ctrl+O). Intercepts keys while
		// open so navigation doesn't leak into the input.
		if sess.dirPicker.IsVisible() {
			switch msg.String() {
			case "up":
				sess.dirPicker.MoveUp()
				return m, nil
			case "down":
				sess.dirPicker.MoveDown()
				return m, nil
			case "esc":
				sess.dirPicker.Close()
				return m, nil
			case "right", "tab":
				if entry := sess.dirPicker.SelectedEntry(); entry != nil && entry.IsDir() {
					sess.dirPicker.Descend(entry)
				}
				return m, nil
			case "left":
				sess.dirPicker.Parent()
				return m, nil
			case "backspace":
				if sess.dirPicker.query != "" {
					sess.dirPicker.Refresh(strings.TrimSuffix(sess.dirPicker.query, sess.dirPicker.query[len(sess.dirPicker.query)-1:]))
				} else {
					sess.dirPicker.Parent()
				}
				return m, nil
			case "enter":
				// Choose the highlighted directory, or the listed directory
				// itself when the listing is empty.
				if entry := sess.dirPicker.SelectedEntry(); entry != nil && entry.IsDir() {
					sess.workDir = sess.dirPicker.SelectedPath()
				} else {
					sess.workDir = sess.dirPicker.CurrentDir()
				}
				sess.dirPicker.Close()
				return m, nil
			default:
				if s := msg.String(); len(s) == 1 && s >= " " {
					sess.dirPicker.Refresh(sess.dirPicker.query + s)
				}
				return m, nil
			}
		}

		// Ctrl+O opens the working-directory picker on a draft thread that has
		// not started connecting yet. Once committing/live the cwd is frozen.
		if msg.String() == "ctrl+o" {
			if sess.phase == phaseDraft && sess.client == nil && !sess.reconnecting {
				sess.dirPicker.OpenDir(sess.workDir)
			}
			return m, nil
		}

		// Recent-directories selection on a focused draft welcome (see
		// welcomeDirNav). up/down move the highlighted directory; enter applies
		// it as the thread's working directory. Handled here — before the
		// editor keymap that binds enter to sending a message — so enter on the
		// recent list switches the directory instead of committing the draft.
		if m.welcomeDirNav(sess, msg.String()) {
			return m, nil
		}

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
					if sess.focus != FocusRightPanel && m.activeTab != TabKindThreads && m.activeTab != TabKindModels && m.activeTab != TabKindJobs && m.activeTab != TabKindSettings {
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
				if sess.focus != FocusRightPanel && m.activeTab != TabKindThreads && m.activeTab != TabKindModels && m.activeTab != TabKindJobs && m.activeTab != TabKindSettings {
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

		// Read-only while a reopened thread's daemon-side init is still running:
		// the transcript is visible and (when focus is on the chat viewport)
		// scrollable, but every editing/sending key is swallowed until
		// event.replay_ready unlocks input. Focus toggling (Tab) is handled
		// above and still works, so the user can move to the chat to scroll.
		if sess.initializing {
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
					sess.chatScrollOffset = m.threadMaxScrollOffset(sess)
				case "end", "G":
					sess.chatScrollOffset = 0
				}
				m.clampScrollOffset(sess)
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
				markCancelledReadyForInput(sess)
				if sess.client != nil {
					sess.client.SendCancel()
				}
				m.flushThreadBuf(sess)
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
				sess.chatScrollOffset = m.threadMaxScrollOffset(sess)
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
				dir, prefix := resolveAtDir(query, pickCWD(sess.workDir, m.cwd))
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
					sess.slashMenu.Open(threadSlashCommands(sess), slashQuery)
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

	// --- Thread daemon events ---
	case threadEventMsg:
		idx, sess := m.findThreadByDaemonID(msg.daemonThreadID)
		if sess != nil {
			evCmds := m.applyEventToThread(idx, msg.event)
			cmds = append(cmds, evCmds...)
			cmds = append(cmds, m.maybeStartTabAlertBlink())
			cmds = append(cmds, m.maybeStartThreadsSpinner())
		}
		return m, tea.Batch(cmds...)

	case instanceEventMsg:
		// Process-level events delivered once per window over the control
		// channel, independent of any chat thread (so a draft still refreshes).
		switch msg.event.Type {
		case "event.threads_changed":
			// The persisted threads list changed outside this window (a job run
			// was persisted or swept): refresh the Vix-initiated group and the
			// set of open working directories.
			cmds = append(cmds, fetchVixThreads(m.socketPath, m.cwd, m.cfg.ConfigDir, m.authToken))
			cmds = append(cmds, fetchRecentDirs(m.socketPath, m.cwd, m.cfg.ConfigDir, m.authToken))
		case "event.jobs_changed":
			// A job/hook started, finished, was enabled/disabled, or the spec
			// directory was reloaded: refresh the Jobs & Triggers tab when it is
			// the active view so the running indicator and last-run stay current.
			if m.activeTab == TabKindJobs {
				cmds = append(cmds, fetchJobsAndHooks(m.socketPath, m.authToken))
			}
		case "event.mcp_changed":
			// An MCP server was enabled/disabled: refresh the MCP tab when it is
			// the active view.
			if m.activeTab == TabKindMcp {
				cmds = append(cmds, fetchMCPServers(m.socketPath, m.authToken))
			}
		case "event.quit":
			// Daemon-driven quit-all (post-update restart). Intentionally no
			// closeThreadsForQuit: the bare disconnect leaves every record in
			// open/ so all threads restore on relaunch.
			cmds = append(cmds, tea.Quit)
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

	case threadDisconnectedMsg:
		_, sess := m.findThreadByDaemonID(msg.daemonThreadID)
		if sess != nil {
			if sess.closing {
				// Expected disconnect: the TUI sent thread.close (quit flow).
				// Don't reconnect — attaching again would resurrect the
				// thread the daemon just closed.
				return m, nil
			}
			sess.reconnecting = true
			sess.pendingInput = nil
			// If the connection dropped before the replay arrived, abandon the
			// restoring placeholder so we don't spin forever.
			sess.awaitingReplay = false
			// A drop between the content replay and replay_ready would otherwise
			// leave the thread stuck read-only; clear it so the reconnect (which
			// re-attaches and re-runs the replay sequence) can restore state.
			sess.initializing = false
			sess.thinkingAnim.Stop()
			sess.chatMessages = append(sess.chatMessages, renderErrorMessage(fmt.Errorf("daemon connection lost")))
			if sess.agentState != StatePlanReview {
				sess.agentState = StateWaitingForInput
			}
			cmds = append(cmds, attemptReconnect(m.socketPath, pickCWD(sess.workDir, m.cwd), m.cfg.ConfigDir, m.cfg.Model, m.authToken, m.forceInit, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, msg.daemonThreadID))
		}
		return m, tea.Batch(cmds...)

	case reconnectSuccessMsg:
		// A fork/duplicate tab has no daemonThreadID yet, so it correlates the
		// result by its stable clientKey; matching the empty daemon id would pick
		// the first draft in the list, not necessarily the tab we forked. The
		// genuine reconnect path leaves clientKey empty and matches by daemon id.
		var sess *ThreadState
		if msg.clientKey != "" {
			_, sess = m.findThreadByClientKey(msg.clientKey)
		} else {
			_, sess = m.findThreadByDaemonID(msg.daemonThreadID)
		}
		if sess == nil {
			// Thread was closed while the reconnect goroutine was in flight.
			// Close the new client to avoid leaking a daemon-side thread.
			msg.client.Close()
			return m, nil
		}
		// Close the previous client before replacing it so the old event-loop
		// goroutine unblocks and exits cleanly.
		if sess.client != nil {
			sess.client.Close()
		}
		sess.client = msg.client
		sess.daemonThreadID = msg.client.ThreadID()
		sess.reconnecting = false
		if t := msg.client.StartedAt(); !t.IsZero() {
			sess.startedAt = t
		}
		// A (re)connected thread is live, never a draft. Fork/duplicate creates
		// the new thread as phaseDraft and connects it through this path; without
		// promoting it here, its first message would fall into the draft-commit
		// branch and connectDraft a fresh, empty daemon thread — discarding the
		// fork-seeded history.
		sess.phase = phaseLive
		if len(sess.chatMessages) > 0 {
			sess.chatMessages = append(sess.chatMessages, renderSystemSuccessMessage("Reconnected to daemon."))
		}
		if sess.pendingPlanAction != nil {
			pending := sess.pendingPlanAction
			sess.pendingPlanAction = nil
			sess.client.SendPlanAction(pending.action, pending.text)
			sess.agentState = StateStreaming
			return m, tea.Batch(startThreadEventLoop(msg.client), sess.thinkingAnim.Start())
		}
		return m, startThreadEventLoop(msg.client)

	case reconnectFailedMsg:
		// Don't retry if the thread has never successfully connected — there
		// is no stable daemonThreadID to match against, and a brand-new
		// thread that failed its first attempt should not loop indefinitely.
		if msg.daemonThreadID == "" {
			return m, nil
		}
		_, sess := m.findThreadByDaemonID(msg.daemonThreadID)
		if sess != nil && sess.reconnecting {
			return m, attemptReconnect(m.socketPath, pickCWD(sess.workDir, m.cwd), m.cfg.ConfigDir, m.cfg.Model, m.authToken, m.forceInit, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, msg.daemonThreadID)
		}
		return m, nil

	case draftConnectedMsg:
		_, sess := m.findThreadByClientKey(msg.clientKey)
		if sess == nil {
			// The draft was closed while the connect goroutine was in flight.
			msg.client.Close()
			return m, nil
		}
		sess.client = msg.client
		sess.daemonThreadID = msg.client.ThreadID()
		sess.phase = phaseLive
		sess.reconnecting = false
		if t := msg.client.StartedAt(); !t.IsZero() {
			sess.startedAt = t
		}
		cmds = append(cmds, startThreadEventLoop(msg.client))
		// Flush the message that triggered the commit.
		if pending := sess.pendingFirstInput; pending != nil {
			sess.pendingFirstInput = nil
			telemetry.TrackTurn(sess.modelName)
			msg.client.SendInput(pending.text, pending.attachments)
		}
		return m, tea.Batch(cmds...)

	case draftConnectFailedMsg:
		_, sess := m.findThreadByClientKey(msg.clientKey)
		if sess == nil {
			return m, nil
		}
		// Keep the thread a draft so the user can retry (or change directory).
		sess.reconnecting = false
		sess.pendingFirstInput = nil
		sess.agentState = StateWaitingForInput
		sess.thinkingAnim.Stop()
		sess.chatMessages = append(sess.chatMessages, renderErrorMessage(fmt.Errorf("couldn't start thread: %w", msg.err)))
		sess.chatScrollOffset = 0
		return m, nil

	case threadOrphanedMsg:
		_, sess := m.findThreadByDaemonID(msg.daemonThreadID)
		if sess == nil {
			return m, nil
		}
		sess.reconnecting = false
		sess.orphaned = true
		sess.awaitingReplay = false
		sess.initializing = false
		sess.client = nil
		sess.pendingInput = nil
		sess.pendingPlanAction = nil
		sess.agentState = StateWaitingForInput
		sess.thinkingAnim.Stop()
		sess.chatMessages = append(sess.chatMessages, renderErrorMessage(fmt.Errorf("This conversation was lost when the daemon restarted and can't be continued. Use /copy to save it before it's gone.")))
		return m, nil

	case threadRestoredMsg:
		// A persisted open thread was re-attached (launch restore, or a
		// vix-initiated record opened from the Threads tab). Add it as a new
		// thread; its viewport is rebuilt from the daemon's event.replay.
		if _, existing := m.findThreadByDaemonID(msg.summary.ID); existing != nil {
			msg.client.Close()
			return m, nil
		}
		restored := newThreadState(m.cfg, msg.client)
		restored.workDir = pickCWD(msg.summary.CWD, m.cwd)
		if msg.summary.Model != "" {
			restored.setModel(msg.summary.Model)
		}
		// A vix-initiated record (job run, alert) keeps its provenance: the
		// summary tag keeps it rendered in the Threads tab's "Vix-initiated"
		// group while attached. Drop it from the persisted-records group so it
		// doesn't show twice (the daemon doesn't broadcast on attach).
		if msg.summary.Origin == "vix" {
			sum := msg.summary
			restored.vixSummary = &sum
			for i, vs := range m.vixThreads {
				if vs.ID == sum.ID {
					m.vixThreads = append(m.vixThreads[:i], m.vixThreads[i+1:]...)
					break
				}
			}
		}
		// Seed the unread indicator from the persisted flag so activity that
		// happened while vix was closed (job runs, alerts) survives restarts.
		// Only a background restore raises the Threads-tab "unseen" latch: a
		// user explicitly opening (or stepping onto) a record sets
		// focusRestoredID to its ID and is marked read below, so re-tinting the
		// tab title for it — or leaving it tinted because another record is still
		// unread — would be wrong. The per-row ● dot still tracks unread.
		if msg.summary.Unread {
			restored.unreadCount = 1
			if m.focusRestoredID != msg.summary.ID {
				m.threadsTabUnseen = true
			}
		}
		// Restored threads are waiting for their replay; show the placeholder
		// (with an animated spinner) until it arrives.
		restored.awaitingReplay = true
		// Restore attaches run concurrently and complete in arbitrary order, so
		// insert by creation time instead of appending: place the thread before
		// the first one that started later, keeping the list in the order the
		// user started the conversations.
		idx := len(m.threads)
		for i, s := range m.threads {
			if s.client != nil && s.client.StartedAt().After(msg.client.StartedAt()) {
				idx = i
				break
			}
		}
		m.threads = append(m.threads, nil)
		copy(m.threads[idx+1:], m.threads[idx:])
		m.threads[idx] = restored
		if idx <= m.selectedThread {
			m.selectedThread++
		}
		// A record the user explicitly opened from the Threads tab gets
		// focused immediately (launch restores stay in the background).
		var focusCmd tea.Cmd
		if m.focusRestoredID != "" && m.focusRestoredID == msg.summary.ID {
			m.focusRestoredID = ""
			m.selectedThread = idx
			m.activeTab = TabKindChat
			restored.input.SetWidth(m.width - 4)
			m.markThreadRead(restored)
			focusCmd = armCursorBlink(restored)
		}
		m.syncThreadsSelected()
		return m, tea.Batch(startThreadEventLoop(msg.client), restored.thinkingAnim.Start(), focusCmd)

	case threadRestoreFailedMsg:
		// Best-effort: a persisted thread could not be reopened. Leave it on
		// disk; it will be offered again on the next launch.
		return m, nil

	case localProvidersMsg:
		if m.modelsLocalUI == nil {
			m.modelsLocalUI = map[string]LocalProviderUI{}
		}
		for id, st := range msg.states {
			m.modelsLocalUI[id] = localProviderUIFromState(st)
		}
		// Re-anchor the model cursor: the grid for a local provider may have
		// just gone from empty to populated.
		if m.activeTab == TabKindModels {
			prov := m.modelsSelectedProvider()
			if IsLocalProvider(prov) && m.modelsFocus != modelsFocusModels {
				m.modelsModelSel = m.modelIndexForActive(prov, m.activeModelSpec())
				m.clampModelsScroll()
			}
		}
		return m, nil

	case vixThreadsMsg:
		m.vixThreads = msg.sums
		m.userThreadRecords = msg.userSums
		// One-shot launch seeding: unread job runs/alerts that accumulated
		// while vix was closed tint the Threads tab. Live arrivals re-latch
		// via event.job_done; refreshes after the first don't, so a visited
		// tab stays calm.
		if !m.vixSeeded {
			m.vixSeeded = true
			if m.activeTab != TabKindThreads {
				for _, sum := range msg.sums {
					if sum.Unread {
						m.threadsTabUnseen = true
						break
					}
				}
			}
		}
		if n := len(m.selectableThreadRows()); m.threadsSelected >= n && n > 0 {
			m.threadsSelected = n - 1
		}
		return m, nil

	case recentDirsMsg:
		m.recentDirs = msg.dirs
		// Keep each draft thread's welcome selection within bounds as the list
		// changes underneath it.
		n := len(m.topRecentDirs())
		for _, s := range m.threads {
			if s.recentDirSelected >= n {
				s.recentDirSelected = max(0, n-1)
			}
		}
		return m, nil

	case jobsListMsg:
		m.jobs = msg.jobs
		m.hooks = msg.hooks
		m.clampJobsSelected()
		return m, m.maybeStartThreadsSpinner()

	case mcpListMsg:
		m.mcpServers = msg.servers
		m.clampMCPSelected()
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
		sess := m.currentThread()
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
				val = stripped
			}
			// Text/PDF files: when connected, the daemon validates before a
			// chip appears. While still connecting (no client yet) there's no
			// daemon to ask, so add the chip optimistically — the file is
			// re-validated at send time.
			cmds := []tea.Cmd{cmd}
			for _, cand := range detectFileCandidates(val) {
				if sess.client != nil {
					cmds = append(cmds, validateFileAttachmentCmd(m.socketPath, m.authToken, sess.client.ThreadID(), sess.clientKey, cand))
					continue
				}
				sess.attachmentPanel.Add(protocol.Attachment{Type: "file", Path: cand.Path})
				stripped := strings.TrimSpace(strings.ReplaceAll(sess.input.Value(), cand.Raw, ""))
				sess.input.SetValue(stripped)
				val = stripped
			}
			newHeight := m.visualLineCount()
			if newHeight != sess.input.Height() {
				sess.input.SetHeight(newHeight)
			}
			sess.input.MoveToBegin()
			sess.input.MoveToEnd()
			return m, tea.Batch(cmds...)
		}

	case fileAttachmentValidatedMsg:
		_, sess := m.findThreadByClientKey(msg.clientKey)
		if sess == nil {
			return m, nil
		}
		switch msg.status {
		case "ok":
			sess.attachmentPanel.Add(protocol.Attachment{Type: "file", Path: msg.cand.Path})
			stripped := strings.TrimSpace(strings.ReplaceAll(sess.input.Value(), msg.cand.Raw, ""))
			sess.input.SetValue(stripped)
		default: // "invalid" / "error"
			stripped := strings.TrimSpace(strings.ReplaceAll(sess.input.Value(), msg.cand.Raw, ""))
			sess.input.SetValue(stripped)
			return m, m.emitStatusMsg(fmt.Sprintf("Attachment skipped (%s): %s", filepath.Base(msg.cand.Path), msg.reason), StatusMsgError)
		}
		if sess == m.currentThread() {
			newHeight := m.visualLineCount()
			if newHeight != sess.input.Height() {
				sess.input.SetHeight(newHeight)
			}
		}
		return m, nil

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
		sess := m.currentThread()
		if sess != nil {
			blinkCmd := sess.input.Focus()
			return m, blinkCmd
		}
		return m, nil

	case animStepMsg:
		// Route to whichever thread owns this generation tick.
		for _, sess := range m.threads {
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
		if m.hasAlertThreads() {
			return m, m.tabBlinkTick()
		}
		m.tabAlertActive = false
		m.tabAlertBlinkOn = false
		m.tabAlertBlinkGen++
		return m, nil

	case threadsSpinnerMsg:
		if msg.gen != m.threadsSpinnerGen {
			return m, nil
		}
		m.threadsSpinnerStep++
		// Re-gate every frame: keep ticking only while the list is visible and
		// work is ongoing, otherwise stop (and bump gen so this loop dies).
		if m.spinnerShouldRun() {
			return m, m.threadsSpinnerTick()
		}
		m.stopThreadsSpinner()
		return m, nil
	}

	// Forward unhandled messages to the active input for cursor blink
	sess := m.currentThread()
	if sess != nil {
		var cmd tea.Cmd
		sess.input, cmd = sess.input.Update(msg)
		if cmd != nil {
			return m, cmd
		}
	}
	return m, nil
}

// hasUnreadThreads reports whether any unread conversation remains: a live
// thread with unread agent activity, or a persisted vix-initiated record
// still flagged unread.
func (m *Model) hasUnreadThreads() bool {
	for _, s := range m.threads {
		if s.unreadCount > 0 {
			return true
		}
	}
	for _, sum := range m.vixThreads {
		if sum.Unread {
			return true
		}
	}
	return false
}

// markThreadRead clears a thread's unread counter and, once no thread has
// unread activity left, lowers the Threads-tab highlight latch. This lets the
// highlight clear when the last unread conversation is opened directly, without
// having to visit the Threads tab. When something was actually cleared, the
// daemon is told too (thread.mark_read) so the persisted unread flag — the
// one that survives restarts — drops with it.
func (m *Model) markThreadRead(sess *ThreadState) {
	if sess.unreadCount > 0 && sess.client != nil {
		sess.client.SendMarkRead()
	}
	sess.unreadCount = 0
	if !m.hasUnreadThreads() {
		m.threadsTabUnseen = false
	}
}

// switchTab changes the active tab and performs per-tab entry side effects,
// returning any command to run (e.g. resuming the chat thinking animation).
func (m *Model) switchTab(k TabKind) tea.Cmd {
	m.activeTab = k
	if k != TabKindThreads && k != TabKindJobs {
		// Leaving the list tabs: no reason to animate a list nobody sees.
		m.stopThreadsSpinner()
	}
	switch k {
	case TabKindThreads:
		m.threadsTabUnseen = false
		m.syncThreadsSelected()
		return tea.Batch(
			m.maybeStartThreadsSpinner(),
			fetchVixThreads(m.socketPath, m.cwd, m.cfg.ConfigDir, m.authToken),
		)
	case TabKindChat:
		if sess := m.currentThread(); sess != nil {
			m.markThreadRead(sess)
			return sess.thinkingAnim.Resume()
		}
	case TabKindModels:
		m.enterModelsTab()
		return fetchLocalProviders(m.socketPath, m.authToken)
	case TabKindMcp:
		m.clampMCPSelected()
		return fetchMCPServers(m.socketPath, m.authToken)
	case TabKindJobs:
		m.clampJobsSelected()
		return tea.Batch(
			m.maybeStartThreadsSpinner(),
			fetchJobsAndHooks(m.socketPath, m.authToken),
		)
	}
	return nil
}

// clampJobsSelected keeps the Jobs & Triggers cursor within the current row
// count (jobs followed by hooks).
func (m *Model) clampJobsSelected() {
	n := len(m.jobs) + len(m.hooks)
	if m.jobsSelected >= n {
		m.jobsSelected = n - 1
	}
	if m.jobsSelected < 0 {
		m.jobsSelected = 0
	}
}

// clampMCPSelected keeps the MCP tab cursor within the current server count.
func (m *Model) clampMCPSelected() {
	if m.mcpSelected >= len(m.mcpServers) {
		m.mcpSelected = len(m.mcpServers) - 1
	}
	if m.mcpSelected < 0 {
		m.mcpSelected = 0
	}
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
	m.modelsModelSel = m.modelIndexForActive(prov, active)
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
	m.modelsLocal = m.modelsLocal[:0]
	if m.modelsStatus == nil {
		m.modelsStatus = map[string]config.ProviderAuthStatus{}
	}
	if cs, err := m.credClient().ProviderCredStatus(); err == nil {
		m.modelsStatus = cs.Providers
	}
	for _, p := range AvailableProviders() {
		if p.Local {
			m.modelsLocal = append(m.modelsLocal, p.Name)
			continue
		}
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
	total := len(m.modelsLoggedIn) + len(m.modelsLocal) + len(m.modelsAvailable)
	if m.modelsProviderSel >= total {
		m.modelsProviderSel = total - 1
	}
	if m.modelsProviderSel < 0 {
		m.modelsProviderSel = 0
	}
}

// modelsFlat returns the provider names in display order (logged in, then
// available, then local last) — the order the provider cursor navigates.
// Must stay in lockstep with renderModelsView's flat/group order.
func (m *Model) modelsFlat() []string {
	out := append([]string{}, m.modelsLoggedIn...)
	out = append(out, m.modelsAvailable...)
	return append(out, m.modelsLocal...)
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

// displayModelsForProvider returns the models shown in the grid for a
// provider: the live-discovered list for local providers (empty until the
// daemon probe answers), the static catalogue otherwise.
func (m *Model) displayModelsForProvider(provider string) []ModelInfo {
	if IsLocalProvider(provider) {
		return m.modelsLocalUI[provider].Models
	}
	return DisplayModelsForProvider(provider)
}

// modelIndexForActive returns the grid index of spec within a provider's models,
// or 0 when absent.
func (m *Model) modelIndexForActive(provider, spec string) int {
	for i, mod := range m.displayModelsForProvider(provider) {
		if mod.Spec == spec {
			return i
		}
	}
	return 0
}

// activeModelSpec returns the model spec currently in effect for the active
// thread, falling back to the configured default.
func (m *Model) activeModelSpec() string {
	spec := m.cfg.Model
	if sess := m.currentThread(); sess != nil && sess.modelName != "" {
		spec = sess.modelName
	}
	return spec
}

// applyModelSelection makes spec the default chat model and pushes it to the
// active thread (and daemon) when connected.
func (m *Model) applyModelSelection(spec string) {
	m.cfg.Model = spec
	if sess := m.currentThread(); sess != nil {
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
				if IsLocalProvider(m.modelsSelectedProvider()) {
					cmds = append(cmds, fetchLocalProviders(m.socketPath, m.authToken))
				}
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
				if IsLocalProvider(m.modelsSelectedProvider()) {
					cmds = append(cmds, fetchLocalProviders(m.socketPath, m.authToken))
				}
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
		models := FilterModels(m.displayModelsForProvider(m.modelsSelectedProvider()), m.modelsFilter)
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
	provider := m.modelsSelectedProvider()
	st := m.modelsStatus[provider]
	gridRows := modelsGridRows(m.modelsViewportHeight(), st, m.modelsLoginStatus, IsLocalProvider(provider))
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
// and remembers the pending model. Local providers are always selectable: they
// resolve a keyless placeholder credential.
func (m Model) selectModel(mod ModelInfo) (tea.Model, tea.Cmd) {
	if IsLocalProvider(mod.Provider) || m.providerHasCredential(mod.Provider) {
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
			if !auth.KeychainAvailable() {
				m.modelsLoginStatus = "Starting " + provider + " login… (token will be stored in plaintext auth.json)"
			} else {
				m.modelsLoginStatus = "Starting " + provider + " login…"
			}
			return m, startProviderLogin(provider)
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

// handleDialogKey handles keys for the global quit/thread-close dialogs.
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
			if m.threadCloseSelected == 0 {
				m.threadCloseSelected = 1
			} else {
				m.threadCloseSelected = 0
			}
		}
	case "enter":
		if m.state == StateQuitConfirm {
			if m.quitSelected == 0 {
				m.closeThreadsForQuit(m.quitCloseAll)
				return m, tea.Quit
			}
			m.state = StateWaitingForInput
		} else {
			if m.threadCloseSelected == 0 {
				return m.confirmThreadClose()
			}
			m.vixDismissID = ""
			m.state = StateWaitingForInput
		}
	case "space", " ":
		if m.state == StateQuitConfirm {
			m.quitCloseAll = !m.quitCloseAll
			_ = config.SetCloseAllThreadsOnQuit(m.quitCloseAll)
		}
	case "y", "Y":
		if m.state == StateQuitConfirm {
			m.closeThreadsForQuit(m.quitCloseAll)
			return m, tea.Quit
		}
		if m.state == StateThreadCloseConfirm {
			return m.confirmThreadClose()
		}
	case "n", "N", "esc":
		m.vixDismissID = ""
		m.state = StateWaitingForInput
	}
	return m, nil
}

// confirmThreadClose runs the action behind the close-confirmation dialog:
// dismissing a persisted vix-initiated record, or closing the live thread at
// threadCloseIdx.
func (m Model) confirmThreadClose() (tea.Model, tea.Cmd) {
	if m.vixDismissID != "" {
		id := m.vixDismissID
		m.vixDismissID = ""
		m.state = StateWaitingForInput
		return m, dismissVixThread(m.socketPath, m.cwd, m.cfg.ConfigDir, m.authToken, id)
	}
	return m.doCloseThread(m.threadCloseIdx)
}

// beginRenameSelected resolves the highlighted Threads-tab row and opens the
// rename dialog for it: a live thread (renamed over its connection) or a
// persisted, not-open record (renamed by ID). Returns the cursor-blink command,
// or a warning when the row can't be renamed yet.
func (m *Model) beginRenameSelected() tea.Cmd {
	if sum, ok := m.vixSelectedSummary(); ok {
		m.beginRename(-1, sum.ID, sum.Title)
		return textinput.Blink
	}
	if idx, ok := m.threadsSelectedIdx(); ok {
		if m.threads[idx].client == nil {
			return m.emitStatusMsg("Thread is still connecting; cannot rename", StatusMsgWarning)
		}
		m.beginRename(idx, "", m.threads[idx].title)
		return textinput.Blink
	}
	return nil
}

// beginRename opens the rename dialog for a thread. Exactly one target is set:
// liveIdx >= 0 for a live thread in m.threads, or a non-empty id for a
// persisted, not-open record. current pre-fills the text box.
func (m *Model) beginRename(liveIdx int, id, current string) {
	ti := textinput.New()
	ti.SetValue(current)
	ti.SetWidth(renameDialogInnerWidth)
	ti.CursorEnd()
	ti.Focus()
	m.renameInput = ti
	m.renameIdx = liveIdx
	m.renameID = id
	m.state = StateThreadRename
}

// handleRenameKey handles keys while the rename dialog is open: Esc cancels,
// Enter submits, everything else edits the text box.
func (m Model) handleRenameKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.state = StateWaitingForInput
		m.renameIdx = -1
		m.renameID = ""
		return m, nil
	case "enter":
		return m.submitRename()
	}
	var cmd tea.Cmd
	m.renameInput, cmd = m.renameInput.Update(msg)
	return m, cmd
}

// submitRename commits the rename: an empty (or unchanged-to-empty) title is a
// no-op that just closes the dialog. A live thread is renamed over its
// connection; a persisted record by ID. The title is updated optimistically so
// the list reflects it immediately (the daemon also broadcasts the change).
func (m Model) submitRename() (tea.Model, tea.Cmd) {
	title := strings.TrimSpace(m.renameInput.Value())
	liveIdx, id := m.renameIdx, m.renameID
	m.state = StateWaitingForInput
	m.renameIdx = -1
	m.renameID = ""
	if title == "" {
		return m, nil
	}
	if liveIdx >= 0 && liveIdx < len(m.threads) {
		sess := m.threads[liveIdx]
		if sess.client == nil {
			return m, m.emitStatusMsg("Thread is still connecting; cannot rename", StatusMsgWarning)
		}
		sess.title = title // optimistic; daemon echoes event.title_updated
		if sess.vixSummary != nil {
			sess.vixSummary.Title = title
		}
		client := sess.client
		return m, func() tea.Msg {
			client.SendRename(title)
			return nil
		}
	}
	if id != "" {
		// Optimistically reflect the new title in the persisted-record list.
		for i := range m.userThreadRecords {
			if m.userThreadRecords[i].ID == id {
				m.userThreadRecords[i].Title = title
			}
		}
		return m, renameVixThread(m.socketPath, m.cwd, m.cfg.ConfigDir, m.authToken, id, title)
	}
	return m, nil
}

// handleTrimKey handles keys for the per-thread trim confirm dialog.
func (m Model) handleTrimKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	sess := m.currentThread()
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
func (m Model) handleEnter(sess *ThreadState) (tea.Model, tea.Cmd) {
	// Read-only while a reopened thread is still initializing: reject submits
	// until event.replay_ready unlocks input.
	if sess.initializing {
		return m, nil
	}
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
		displayText, fileAtts := extractFileAttachments(displayText)
		attachments := append(append(panelAtts, textAtts...), fileAtts...)

		sess.chatMessages = append(sess.chatMessages, renderUserMessage(displayText, m.mdRenderer.width, attachments...))
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

		// Orphaned threads have no daemon-side history and can't be continued.
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
		displayText, fileAtts := extractFileAttachments(displayText)
		attachments := append(append(panelAtts, textAtts...), fileAtts...)

		sess.chatMessages = append(sess.chatMessages, renderUserMessage(displayText, m.mdRenderer.width, attachments...))
		sess.chatScrollOffset = 0

		// Draft thread: commit it now. Open the daemon connection in the
		// chosen working directory, then flush this message once connected
		// (draftConnectedMsg). The cwd is frozen from here on.
		if sess.phase == phaseDraft {
			cwd := resolveWorkDir(sess.workDir)
			if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
				sess.chatMessages = append(sess.chatMessages, renderErrorMessage(fmt.Errorf("working directory does not exist: %s", sess.workDir)))
				sess.chatScrollOffset = 0
				return m, nil
			}
			sess.workDir = cwd
			sess.pendingFirstInput = &pendingMsg{text: displayText, attachments: attachments}
			sess.reconnecting = true
			sess.agentState = StateStreaming
			return m, tea.Batch(
				connectDraft(m.socketPath, sess.clientKey, cwd, m.cfg.ConfigDir, m.cfg.Model, m.authToken, m.forceInit, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess),
				sess.thinkingAnim.Start(),
			)
		}

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

// applyEventToThread processes a single daemon event for the thread at idx.
func (m *Model) applyEventToThread(idx int, event protocol.ThreadEvent) []tea.Cmd {
	sess := m.threads[idx]
	var cmds []tea.Cmd

	switch event.Type {
	case "event.thread_started":
		data := marshalData(event.Data)
		var started protocol.EventThreadStarted
		json.Unmarshal(data, &started)
		sess.parentID = started.ParentID
		sess.forkTurnIdx = started.ForkTurnIdx

	case "event.replay":
		data := marshalData(event.Data)
		var rep protocol.EventReplay
		json.Unmarshal(data, &rep)
		m.applyReplay(sess, rep)
		// A content-only replay (emitted before the daemon finished initBrain)
		// renders the transcript read-only; input stays locked until the
		// matching event.replay_ready arrives.
		sess.initializing = rep.Initializing
		// The viewport is now rebuilt; drop the restoring placeholder and stop
		// its spinner.
		sess.awaitingReplay = false
		sess.thinkingAnim.Stop()
		// Viewing the replay counts as reading: clear the persisted unread
		// flag when this thread is the one on screen. Sent unconditionally
		// (not via markThreadRead) because the disk flag may be set even
		// when the local counter is zero — e.g. a vix job run just opened.
		if idx == m.selectedThread && m.activeTab == TabKindChat {
			sess.unreadCount = 0
			if !m.hasUnreadThreads() {
				m.threadsTabUnseen = false
			}
			if sess.client != nil {
				sess.client.SendMarkRead()
			}
		}

	case "event.title_updated":
		data := marshalData(event.Data)
		var tu protocol.EventTitleUpdated
		json.Unmarshal(data, &tu)
		sess.title = tu.Title
		if sess.vixSummary != nil {
			sess.vixSummary.Title = tu.Title
		}

	case "event.replay_ready":
		// Daemon-side initBrain finished for a reopened thread: apply the
		// resolved model/mode, render restore warnings, and unlock input
		// (drop the read-only state set by the initializing event.replay).
		data := marshalData(event.Data)
		var rr protocol.EventReplayReady
		json.Unmarshal(data, &rr)
		if rr.Model != "" {
			sess.setModel(rr.Model)
		}
		sess.activeWorkflow = rr.ActiveWorkflow
		for _, w := range rr.Warnings {
			sess.chatMessages = append(sess.chatMessages, renderSystemMessage(w, m.styles))
		}
		sess.chatCache.invalidate()
		sess.initializing = false

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

	case "event.tool_backends":
		data := marshalData(event.Data)
		var tb protocol.EventToolBackends
		json.Unmarshal(data, &tb)
		m.grepBackendEffective = tb.GrepEffective
		m.grepBackendConfigured = tb.GrepConfigured
		m.globBackendEffective = tb.GlobEffective
		m.globBackendConfigured = tb.GlobConfigured

	case "event.update_available":
		data := marshalData(event.Data)
		var ua protocol.EventUpdateAvailable
		json.Unmarshal(data, &ua)
		m.updateCurrent = ua.Current
		m.updateLatest = ua.Latest
		m.updateURL = ua.URL
		m.updateMethod = ua.Method

	case "event.job_done":
		data := marshalData(event.Data)
		var jd protocol.EventJobDone
		json.Unmarshal(data, &jd)
		text, kind := jobDoneStatusText(jd)
		m.threadsTabUnseen = true
		cmds = append(cmds,
			m.emitStatusMsg(text, kind),
			fetchVixThreads(m.socketPath, m.cwd, m.cfg.ConfigDir, m.authToken))

	case "event.job_run":
		data := marshalData(event.Data)
		var jr protocol.EventJobRun
		json.Unmarshal(data, &jr)
		// Only failures-of-policy are worth a status line; routine starts are
		// silent (the run will land in the threads list anyway).
		switch jr.Status {
		case "invalid":
			cmds = append(cmds, m.emitStatusMsg(fmt.Sprintf("Job %s has an invalid spec: %s", jr.JobID, jr.Error), StatusMsgWarning))
		case "auto_disabled":
			cmds = append(cmds, m.emitStatusMsg(fmt.Sprintf("Job %s disabled after repeated failures", jr.JobID), StatusMsgError))
		}

	case "event.job_nudge":
		// One-time hint emitted when the first user-created job appears.
		cmds = append(cmds, m.emitStatusMsg("Tip: `vix daemon install` starts vixd at login so scheduled jobs survive reboots", StatusMsgInfo))

	case "event.stream_chunk":
		data := marshalData(event.Data)
		var chunk protocol.EventStreamChunk
		json.Unmarshal(data, &chunk)
		sess.assistantBuf += chunk.Text
		if time.Since(sess.lastStreamRender) >= streamRenderInterval {
			m.syncMermaidCtx(sess)
			sess.assistantRendered = m.mdRenderer.Render(sess.assistantBuf)
			sess.lastStreamRender = time.Now()
		}

	case "event.thinking_chunk":
		data := marshalData(event.Data)
		var chunk protocol.EventThinkingChunk
		json.Unmarshal(data, &chunk)
		sess.thinkingBuf += chunk.Text
		if sess.showThinking && time.Since(sess.lastThinkingRender) >= streamRenderInterval {
			sess.thinkingRendered = renderThinkingText(sess.thinkingBuf, m.styles, m.mdRenderer.width+4)
			sess.lastThinkingRender = time.Now()
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
		m.flushThreadBuf(sess)
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
		switch {
		case sess.rightPanel.IsVisible() && sess.rightPanel.mode == rpModeWorkflow:
			// Todos render below workflow steps automatically.
		case sess.rightPanel.IsVisible() && sess.rightPanel.mode == rpModeTodos:
			if !hasPendingTodos(sess.todos) {
				sess.rightPanel.Close()
				m.updateChatWidth()
			}
		default:
			if hasPendingTodos(sess.todos) {
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
		m.flushThreadBuf(sess)
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
		if sess.cancelAckPending && sess.pendingInput == nil &&
			(sess.agentState == StateStreaming || sess.agentState == StateToolExecuting || sess.agentState == StatePlanExecuting) {
			sess.cancelAckPending = false
			return cmds
		}
		sess.cancelAckPending = false
		sess.thinkingAnim.Stop()
		m.flushThreadBuf(sess)
		if idx != m.selectedThread || m.activeTab != TabKindChat {
			sess.unreadCount++
			if m.activeTab != TabKindThreads {
				m.threadsTabUnseen = true
			}
		} else if sess.client != nil {
			// The user watched this turn complete: clear the persisted unread
			// flag the daemon just set at turn end.
			sess.client.SendMarkRead()
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
		m.flushThreadBuf(sess)
		sess.chatMessages = nil
		sess.chatCache.invalidate()
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
		m.flushThreadBuf(sess)
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
		m.flushThreadBuf(sess)
		sess.chatMessages = append(sess.chatMessages, renderRetryMessage(retry))

	case "event.error":
		data := marshalData(event.Data)
		var errEvent protocol.EventError
		json.Unmarshal(data, &errEvent)
		sess.chatMessages = append(sess.chatMessages, renderErrorMessage(fmt.Errorf("%s", errEvent.Message)))
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

	sess := m.currentThread()

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
	viewportFocused := m.activeTab == TabKindThreads || m.activeTab == TabKindModels || m.activeTab == TabKindJobs || m.activeTab == TabKindSettings || (sess != nil && sess.focus == FocusChat)
	tabBarWidth := layout.ChatWidth
	if m.activeTab == TabKindThreads || m.activeTab == TabKindModels || m.activeTab == TabKindJobs || m.activeTab == TabKindSettings {
		tabBarWidth = m.width
	}
	tabBar := renderTabBar(m.activeTab, tabBarWidth, m.styles, viewportFocused, m.hasAlertThreads(), m.tabAlertBlinkOn, m.threadsTabUnseen, m.updateLatest != "")
	uv.NewStyledString(tabBar).Draw(canvas, image.Rect(0, y, tabBarWidth, y+layout.TabBarHeight))
	y += layout.TabBarHeight

	switch m.activeTab {
	case TabKindThreads:
		threadsHeight := m.height - layout.TabBarHeight - layout.StatusBarHeight
		spinnerFrame := ""
		if m.threadsSpinnerActive {
			spinnerFrame = string(animFrames[frozenStep(m.threadsSpinnerStep)%len(animFrames)])
		}
		// The User-initiated group renders as per-directory blocks (current cwd
		// first, each headed by a foldable path line); the Vix-initiated group
		// renders as one StartedAt-ordered list. Both derive from the single
		// display list that also backs the selection index space (threadListRows).
		sv := renderThreadsView(m.threadListRows(), m.width, threadsHeight, m.styles, m.threadsSelected, spinnerFrame)
		uv.NewStyledString(sv).Draw(canvas, image.Rect(0, y, m.width, y+threadsHeight))
		y += threadsHeight

	case TabKindChat:
		// Chat content
		innerWidth := layout.ChatWidth - 4
		contentHeight := layout.ChatHeight - 1

		var allLines []string
		var visualRowStart []int
		switch {
		case sess != nil && sess.awaitingReplay && !m.testMode:
			// While waiting for the replay the spinner runs, which would
			// otherwise make the content non-empty and bypass the placeholder.
			placeholder := renderRestoringInline(innerWidth, contentHeight, m.styles, sess.thinkingAnim.View())
			allLines = strings.Split(placeholder, "\n")
			visualRowStart = visualRowPrefix(allLines, innerWidth)
		case sess != nil:
			tail := ""
			if sess.showThinking && sess.thinkingRendered != "" {
				tail += sess.thinkingRendered + "\n"
			}
			if sess.assistantRendered != "" {
				tail += sess.assistantRendered
			} else if animFrame := sess.thinkingAnim.View(); animFrame != "" {
				tail += animFrame + "\n"
			}
			lines, rowStart := sess.cachedChatLines(m.styles, innerWidth)
			if emptyChatLines(lines) && tail == "" && !m.testMode {
				recentSel := -1
				var recent []protocol.DirUsage
				if sess.phase == phaseDraft {
					recent = m.topRecentDirs()
					if sess.focus == FocusChat && len(recent) > 0 {
						recentSel = sess.recentDirSelected
					}
				}
				welcome := renderWelcomeInline(innerWidth, contentHeight, m.styles, sess.workDir, sess.phase == phaseDraft, recent, recentSel)
				allLines = strings.Split(welcome, "\n")
				visualRowStart = visualRowPrefix(allLines, innerWidth)
			} else {
				allLines, visualRowStart = combineTail(lines, rowStart, tail, innerWidth)
			}
		case !m.testMode:
			welcome := renderWelcomeInline(innerWidth, contentHeight, m.styles, m.cwd, false, nil, -1)
			allLines = strings.Split(welcome, "\n")
			visualRowStart = visualRowPrefix(allLines, innerWidth)
		default:
			allLines = []string{""}
			visualRowStart = []int{0, 1}
		}

		totalVisualRows := visualRowStart[len(allLines)]

		chatScrollOffset := 0
		if sess != nil {
			chatScrollOffset = sess.chatScrollOffset
		}

		// When scrolled up, a sticky header reserves the top rows of the chat
		// content area (the turn's user prompt + a primary-color rule). The chat
		// slice is computed against the reduced height and the header is
		// prepended below. Scroll math (offset, max, gotoTurn) is left untouched:
		// the rows the header covers are the top turn's prompt, which the header
		// itself shows.
		headerActive := sess != nil && chatScrollOffset > 0
		effHeight := contentHeight
		if headerActive {
			effHeight = contentHeight - stickyHeaderRows
			if effHeight < 1 {
				effHeight = 1
			}
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
			rows := visualRowStart[startLogical] - visualRowStart[startLogical-1]
			if accVisRows+rows > effHeight {
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
		joined := strings.Join(chatLines, "\n")
		if headerActive {
			prompt := stickyUserPromptForTop(sess.cachedUserInfos(m.styles, innerWidth), startLogical)
			if prompt != "" {
				joined = renderStickyHeader(prompt, innerWidth) + "\n" + joined
			}
		}
		var chatBox string
		if sess != nil {
			key := fmt.Sprintf("%d|%d|%t|", layout.ChatWidth, layout.ChatHeight, sess.focus == FocusChat) + joined
			if key != sess.chatBoxKey {
				sess.chatBoxKey = key
				sess.chatBoxRendered = chatBorderStyle.Width(layout.ChatWidth).Height(layout.ChatHeight).Render(joined)
			}
			chatBox = sess.chatBoxRendered
		} else {
			chatBox = chatBorderStyle.Width(layout.ChatWidth).Height(layout.ChatHeight).Render(joined)
		}
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
		} else if sess != nil && sess.initializing {
			// Reopened thread still initializing: show the transcript but make
			// the input visibly read-only (blurred, no textarea, clear label).
			inputSection = renderInputBox("Initializing — read only", false, "", m.width, false, m.styles.ColorBlurBorder)
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
			m.modelsLoggedIn, m.modelsLocal, m.modelsAvailable, m.modelsStatus, m.modelsLocalUI,
			m.modelsProviderSel, m.modelsFocus,
			m.modelsAuthRow, m.modelsAuthBtn, m.modelsModelSel, m.modelsModelScroll,
			m.modelsFilter, m.activeModelSpec(), m.modelsLoginStatus)
		uv.NewStyledString(mv).Draw(canvas, image.Rect(0, y, m.width, y+modelsHeight))
		y += modelsHeight

	case TabKindMcp:
		mcpHeight := m.height - layout.TabBarHeight - layout.StatusBarHeight
		mv := renderMCPView(m.mcpServers, m.width, mcpHeight, m.styles, m.mcpSelected)
		uv.NewStyledString(mv).Draw(canvas, image.Rect(0, y, m.width, y+mcpHeight))
		y += mcpHeight

	case TabKindJobs:
		jobsHeight := m.height - layout.TabBarHeight - layout.StatusBarHeight
		spinnerFrame := ""
		if m.threadsSpinnerActive {
			spinnerFrame = string(animFrames[frozenStep(m.threadsSpinnerStep)%len(animFrames)])
		}
		jv := renderJobsView(m.jobs, m.hooks, m.width, jobsHeight, m.styles, m.jobsSelected, spinnerFrame)
		uv.NewStyledString(jv).Draw(canvas, image.Rect(0, y, m.width, y+jobsHeight))
		y += jobsHeight

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
			closedRetentionMins: config.ClosedThreadRetentionMinutes(),
			updateCheck:         config.UpdateCheckEnabled(),
			updateCurrent:       m.updateCurrent,
			updateLatest:        m.updateLatest,
			updateMethod:        m.updateMethod,
			updateInstalled:     m.updateInstalled,
			updateErr:           m.updateErr,
			grepBackend:         backendLabel(m.grepBackendEffective, m.grepBackendConfigured),
			globBackend:         backendLabel(m.globBackendEffective, m.globBackendConfigured),
		}
		if settSess := m.currentThread(); settSess != nil {
			st.showThinking = settSess.showThinking
		}
		sv := renderSettingsView(m.width, settingsHeight, m.styles, st)
		uv.NewStyledString(sv).Draw(canvas, image.Rect(0, y, m.width, y+settingsHeight))
		y += settingsHeight
	}

	// Status bar — global: connected if any thread is up, reconnecting if none
	// are connected but at least one is trying.
	var connected, reconnecting bool
	for _, s := range m.threads {
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
	draft := sess != nil && sess.phase == phaseDraft
	statusBar := renderStatusBar(m.width, connected, reconnecting, draft, m.statusMsg, m.styles, m.activeTab, statusFocus, statusInputTokens, statusContextWindow)
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

	// Thread close confirm overlay
	if m.state == StateThreadCloseConfirm {
		threadID := m.vixDismissID
		if threadID == "" && m.threadCloseIdx >= 0 && m.threadCloseIdx < len(m.threads) {
			if s := m.threads[m.threadCloseIdx]; s.client != nil {
				threadID = s.client.ThreadID()
			}
		}
		overlay := renderThreadCloseDialog(m.width, m.height, m.styles, m.threadCloseSelected, threadID)
		w, h := lipgloss.Size(overlay)
		center := centerRect(canvas.Bounds(), w, h)
		uv.NewStyledString(overlay).Draw(canvas, center)
	}

	// Thread rename overlay
	if m.state == StateThreadRename {
		overlay := renderThreadRenameDialog(m.width, m.styles, m.renameInput.View())
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

	// Directory picker overlay (draft welcome screen, centered)
	if sess != nil && sess.dirPicker.IsVisible() {
		popupWidth := 60
		if popupWidth > m.width-4 {
			popupWidth = m.width - 4
		}
		list := sess.dirPicker.View(popupWidth, 10, m.styles)
		if list != "" {
			header := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).
				Width(popupWidth).Render(truncatePathLeft(sess.dirPicker.CurrentDir(), popupWidth))
			hint := lipgloss.NewStyle().Foreground(m.styles.ColorDimGray).
				Width(popupWidth).Render("↑↓ select · → open · ← up · Enter choose · Esc cancel")
			overlay := header + "\n" + list + "\n" + hint
			w, h := lipgloss.Size(overlay)
			center := centerRect(canvas.Bounds(), w, h)
			uv.NewStyledString(overlay).Draw(canvas, center)
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

	// Error alert popup overlay (topmost — drawn last so it sits above other
	// overlays; dismissed on any key press).
	if m.alertPopup != "" {
		overlay := renderAlertDialog(m.width, m.height, m.styles, m.alertPopup)
		w, h := lipgloss.Size(overlay)
		center := centerRect(canvas.Bounds(), w, h)
		uv.NewStyledString(overlay).Draw(canvas, center)
	}

	content := strings.ReplaceAll(canvas.Render(), "\r\n", "\n")
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// --- Helper methods ---

// handleCommandAction executes the command identified by action and returns any
// resulting tea.Cmd values. It is shared by the command palette and slash menu.
func (m *Model) handleCommandAction(action string, sess *ThreadState) []tea.Cmd {
	var cmds []tea.Cmd
	switch action {
	case "clear":
		if sess != nil && sess.client != nil {
			sess.client.SendCancel()
		}
		if sess != nil {
			m.flushThreadBuf(sess)
			sess.chatMessages = nil
			sess.chatCache.invalidate()
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
			sess.chatScrollOffset = m.threadMaxScrollOffset(sess)
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
		m.closeThreadsForQuit(config.CloseAllThreadsOnQuit())
		cmds = append(cmds, tea.Quit)
	default:
		if strings.HasPrefix(action, "switch_tab_") {
			idxStr := strings.TrimPrefix(action, "switch_tab_")
			if i, err := strconv.Atoi(idxStr); err == nil {
				switch TabKind(i) {
				case TabKindThreads:
					cmds = append(cmds, m.switchTab(TabKindThreads))
				case TabKindChat:
					cmds = append(cmds, m.switchTab(TabKindChat))
				case TabKindModels:
					cmds = append(cmds, m.switchTab(TabKindModels))
				case TabKindJobs:
					cmds = append(cmds, m.switchTab(TabKindJobs))
				case TabKindSettings:
					cmds = append(cmds, m.switchTab(TabKindSettings))
				}
			}
		}
	}
	return cmds
}

// flushThreadBuf commits the streaming assistant buffer to the thread's chatMessages.
func (m *Model) flushThreadBuf(sess *ThreadState) {
	m.syncMermaidCtx(sess)
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

// applyReplay rebuilds a thread's viewport and restores its mode/model/todos
// from a daemon event.replay (sent when attaching to a persisted thread).
// Restore-time warnings are appended as system messages.
func (m *Model) applyReplay(sess *ThreadState, rep protocol.EventReplay) {
	m.syncMermaidCtx(sess)
	sess.chatMessages = m.buildReplayChatMessages(rep)
	sess.chatCache.invalidate()
	sess.todos = rep.Todos
	if !sess.rightPanel.IsVisible() && hasPendingTodos(sess.todos) {
		sess.rightPanel.OpenTodos(m.height)
		m.updateChatWidth()
	}
	sess.activePlan = rep.ActivePlan
	if rep.Model != "" {
		sess.setModel(rep.Model)
	}
	if rep.Title != "" {
		sess.title = rep.Title
		if sess.vixSummary != nil {
			sess.vixSummary.Title = rep.Title
		}
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
//
// Turn separators (MsgSystem with TurnModel set) are UI-only markers that a live
// run appends at each turn end; they are not part of the persisted LLM history
// nor re-sent in event.replay. Without reconstructing them here, a thread
// restored on relaunch would carry zero separators, so /fork, /trim, and
// duplicate — which all key off turnSeparatorInfos — would be refused ("no
// completed turns yet"). We re-insert one after every assistant message that
// closes a turn (no tool_use block), mirroring the daemon's countEndTurns /
// rebuildTurnSnapshots rule so the UI's per-turn indices stay aligned with the
// daemon's fork snapshots. Per-turn elapsed/cost are not persisted, so they
// render as zero on a restored transcript.
func (m *Model) buildReplayChatMessages(rep protocol.EventReplay) []ChatMessage {
	var out []ChatMessage
	toolNames := map[string]string{}
	turnNum := 0
	for _, msg := range rep.Messages {
		var ts time.Time
		if msg.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339, msg.Timestamp); err == nil {
				ts = parsed
			}
		}
		for _, b := range msg.Blocks {
			switch b.Kind {
			case "text":
				if msg.Role == "user" {
					body, atts := parseAttachmentRefs(b.Text)
					out = append(out, renderUserMessageAt(body, m.width, ts, atts...))
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
			case "retry":
				out = append(out, renderRetryMessage(protocol.EventRetry{
					Attempt:    b.Attempt,
					MaxRetries: b.MaxRetries,
					WaitSecs:   b.WaitSecs,
					Reason:     b.Text,
				}))
			case "error":
				out = append(out, renderErrorMessage(fmt.Errorf("%s", b.Text)))
			}
		}
		// A text-only assistant message (no tool_use block) closes a turn. Append
		// the same separator a live turn end produces so the restored transcript
		// is forkable/trimmable/duplicable.
		if msg.Role == "assistant" && !replayMessageHasToolUse(msg) {
			turnNum++
			out = append(out, renderTurnInfo(rep.Model, 0, 0, turnNum, m.mdRenderer.width+4, m.styles))
		}
	}
	return out
}

// replayMessageHasToolUse reports whether a replayed message carries any
// tool_use block — i.e. it continues a turn rather than ending it.
func replayMessageHasToolUse(msg protocol.ReplayMessage) bool {
	for _, b := range msg.Blocks {
		if b.Kind == "tool_use" {
			return true
		}
	}
	return false
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

// visualLineCount returns the display line count for the current thread's input.
func (m *Model) visualLineCount() int {
	sess := m.currentThread()
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

// threadMaxScrollOffset returns the max scroll offset for a thread's chat.
func (m *Model) threadMaxScrollOffset(sess *ThreadState) int {
	layout := computeLayout(m.width, m.height, m.visualLineCount())
	contentHeight := layout.ChatHeight - 1
	innerWidth := m.effectiveChatWidth() - 4
	tail := ""
	if sess.showThinking && sess.thinkingRendered != "" {
		tail += sess.thinkingRendered + "\n"
	}
	if sess.assistantRendered != "" {
		tail += sess.assistantRendered
	}
	lines, rowStart := sess.cachedChatLines(m.styles, innerWidth)
	if emptyChatLines(lines) && tail == "" {
		// Empty transcript: the welcome screen is generated to fit the
		// viewport, so there is nothing to scroll.
		return 0
	}
	_, rowStart = combineTail(lines, rowStart, tail, innerWidth)
	totalVisualRows := rowStart[len(rowStart)-1]
	maxOff := totalVisualRows - contentHeight
	if maxOff < 0 {
		return 0
	}
	return maxOff
}

// clampScrollOffset ensures the thread's chatScrollOffset is within valid bounds.
func (m *Model) clampScrollOffset(sess *ThreadState) {
	if sess.chatScrollOffset < 0 {
		sess.chatScrollOffset = 0
	}
	if max := m.threadMaxScrollOffset(sess); sess.chatScrollOffset > max {
		sess.chatScrollOffset = max
	}
}

// turnSepByNumber returns the separator info for the given 1-based turn number.
func (m *Model) turnSepByNumber(sess *ThreadState, turnNum int) (TurnSepInfo, bool) {
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
func (m *Model) appendCommandError(sess *ThreadState, text string) {
	sess.input.Reset()
	sess.input.SetHeight(1)
	sess.chatMessages = append(sess.chatMessages, renderSystemMessage(text, m.styles))
	sess.chatScrollOffset = 0
}

// tryLocalCommand intercepts client-side slash commands (/fork, /trim, /copy)
// typed into the input. When the input is a recognized local command it is
// consumed (never sent to the daemon) and handled is true; the returned
// model/cmd should then be used as handleEnter's result.
func (m Model) tryLocalCommand(sess *ThreadState) (handled bool, model tea.Model, cmd tea.Cmd) {
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
func (m *Model) copyTurn(sess *ThreadState, turnNum int, sep TurnSepInfo) {
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
func (m *Model) gotoTurn(sess *ThreadState, turnNum int) {
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
	// renderer (and threadMaxScrollOffset), to convert the logical line into a
	// from-bottom scroll offset.
	tail := ""
	if sess.showThinking && sess.thinkingRendered != "" {
		tail += sess.thinkingRendered + "\n"
	}
	if sess.assistantRendered != "" {
		tail += sess.assistantRendered
	}
	lines, rowStart := sess.cachedChatLines(m.styles, innerWidth)
	allLines, visualRowStart := combineTail(lines, rowStart, tail, innerWidth)
	if targetLine > len(allLines) {
		targetLine = len(allLines)
	}
	totalVisualRows := visualRowStart[len(allLines)]
	startVisRow := visualRowStart[targetLine]

	layout := computeLayout(m.width, m.height, m.visualLineCount())
	contentHeight := layout.ChatHeight - 1

	sess.chatScrollOffset = totalVisualRows - contentHeight - startVisRow
	m.clampScrollOffset(sess)
	sess.focus = FocusChat
}

// doFork creates a new thread seeded with history up to sep, and connects a fork.
func (m *Model) doFork(sep TurnSepInfo) (Model, tea.Cmd) {
	sess := m.currentThread()

	newSess := newThreadState(m.cfg, nil)
	newSess.reconnecting = true
	newSess.workDir = pickCWD(sess.workDir, m.cwd)
	forkedMsgs := make([]ChatMessage, sep.MsgIdx+1)
	copy(forkedMsgs, sess.chatMessages[:sep.MsgIdx+1])
	newSess.chatMessages = forkedMsgs

	forkThreadID := ""
	if sess.client != nil {
		forkThreadID = sess.client.ThreadID()
	}

	newIdx := len(m.threads)
	m.threads = append(m.threads, newSess)
	m.selectedThread = newIdx

	return *m, tea.Batch(connectFork(
		m.socketPath, newSess.workDir, m.cfg.ConfigDir, m.cfg.Model, m.authToken,
		m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess,
		forkThreadID, sep.TurnIdx, newSess.daemonThreadID, newSess.clientKey,
	), armCursorBlink(newSess))
}

// doDuplicate creates a new thread that is a full copy of srcSess, seeded with
// the source's conversation history up to its last completed turn (sep), and
// connects it. Mirrors doFork but operates on an explicit source thread (so it
// can be triggered from the Threads tab against the highlighted row).
func (m *Model) doDuplicate(srcSess *ThreadState, sep TurnSepInfo) (Model, tea.Cmd) {
	newSess := newThreadState(m.cfg, nil)
	newSess.reconnecting = true
	newSess.workDir = pickCWD(srcSess.workDir, m.cwd)
	copiedMsgs := make([]ChatMessage, sep.MsgIdx+1)
	copy(copiedMsgs, srcSess.chatMessages[:sep.MsgIdx+1])
	newSess.chatMessages = copiedMsgs

	forkThreadID := ""
	if srcSess.client != nil {
		forkThreadID = srcSess.client.ThreadID()
	}

	newIdx := len(m.threads)
	m.threads = append(m.threads, newSess)
	m.selectedThread = newIdx
	m.syncThreadsSelected()

	return *m, tea.Batch(connectFork(
		m.socketPath, newSess.workDir, m.cfg.ConfigDir, m.cfg.Model, m.authToken,
		m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess,
		forkThreadID, sep.TurnIdx, newSess.daemonThreadID, newSess.clientKey,
	), armCursorBlink(newSess))
}

// doTrim trims the current thread's history to sep and tells the daemon to match.
func (m *Model) doTrim(sep TurnSepInfo) (Model, tea.Cmd) {
	sess := m.currentThread()
	trimmed := make([]ChatMessage, sep.MsgIdx+1)
	copy(trimmed, sess.chatMessages[:sep.MsgIdx+1])
	sess.chatMessages = trimmed
	sess.chatCache.invalidate()
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

// closeThreadsForQuit runs right before the TUI exits. When closeAll is set
// (the quit-dialog checkbox / persisted preference), every thread is
// explicitly closed: thread.close moves each record open/ -> closed/ in the
// daemon, so nothing is restored on next launch. When unset, it sends nothing —
// the process exit bare-disconnects every connection; the daemon cancels any
// running agent on EOF and leaves all records in open/ for next-run restore.
//
// Deliberately not called from the update quit-all flow (handleUpdateAction,
// event.quit): an update quit is a restart, not an exit, so threads must
// survive it regardless of the preference.
func (m *Model) closeThreadsForQuit(closeAll bool) {
	if !closeAll {
		return
	}
	for _, sess := range m.threads {
		if sess.client != nil {
			// Mark the thread so the disconnect that follows thread.close is
			// treated as expected rather than triggering a reconnect (which
			// would race the daemon's open/ -> closed/ move and resurrect the
			// record in open/).
			sess.closing = true
			sess.client.SendCancel()
			sess.client.SendClose()
		}
	}
}

// doCloseThread closes the thread at threadIdx and returns to the Threads tab.
func (m *Model) doCloseThread(threadIdx int) (Model, tea.Cmd) {
	if threadIdx < 0 || threadIdx >= len(m.threads) {
		m.state = StateWaitingForInput
		return *m, nil
	}

	sess := m.threads[threadIdx]
	if sess.client != nil {
		sess.client.SendCancel()
		sess.client.SendClose()
	}

	m.threads = append(m.threads[:threadIdx], m.threads[threadIdx+1:]...)

	if m.selectedThread >= len(m.threads) {
		m.selectedThread = len(m.threads) - 1
	}
	if m.selectedThread < 0 {
		m.selectedThread = 0
	}

	var reconnectCmd tea.Cmd
	if len(m.threads) == 0 {
		// All threads closed: open a fresh draft (welcome screen), which
		// connects on its first message.
		newSess := newThreadState(m.cfg, nil)
		m.threads = append(m.threads, newSess)
		m.selectedThread = 0
		reconnectCmd = armCursorBlink(newSess)
	}

	// Keep the Threads-tab cursor on the same row index: the closed row was the
	// highlighted one, so the next thread slides into its place. Clamp against
	// the full row count (live threads + persisted user/vix records). Do NOT
	// call syncThreadsSelected here — it would snap the highlight onto the
	// active workspace thread's row (usually a user-initiated one).
	if n := len(m.selectableThreadRows()); n > 0 && m.threadsSelected >= n {
		m.threadsSelected = n - 1
	}

	m.activeTab = TabKindThreads
	m.state = StateWaitingForInput
	return *m, tea.Batch(reconnectCmd, m.maybeStartThreadsSpinner())
}

// effectiveChatWidth returns the panel-aware total chat width — the single
// source of truth for every width-sensitive render. When the right panel is
// visible it reserves panelWidth columns; otherwise it is the plain layout
// chat width. Inner content width is effectiveChatWidth() - 4.
func (m *Model) effectiveChatWidth() int {
	chatWidth := computeLayout(m.width, m.height, m.visualLineCount()).ChatWidth
	if sess := m.currentThread(); sess != nil && sess.rightPanel.IsVisible() {
		chatWidth = m.width - sess.rightPanel.PanelWidth()
		if chatWidth < 10 {
			chatWidth = 10
		}
	}
	return chatWidth
}

// updateChatWidth updates the markdown renderer width to match the current
// effective chat width and re-renders the thread's cached messages.
func (m *Model) updateChatWidth() {
	m.mdRenderer.UpdateWidth(m.effectiveChatWidth() - 4)
	m.rerenderThreadMessages()
	m.lastChatWidth = m.effectiveChatWidth()
}

// reconcileChatWidth re-flows width-cached content (the glamour code box and
// cached message renders) whenever the effective panel-aware chat width has
// changed since the last reconciliation. Called centrally from Update so panel
// open/close, thread switches, and resizes all self-heal without each
// transition having to remember to call updateChatWidth.
func (m *Model) reconcileChatWidth() {
	if m.width == 0 {
		return
	}
	if w := m.effectiveChatWidth(); w != m.lastChatWidth {
		m.updateChatWidth()
	}
}

// rerenderThreadMessages re-renders the current thread's chat messages at the current width.
func (m *Model) rerenderThreadMessages() {
	sess := m.currentThread()
	if sess == nil {
		return
	}
	m.syncMermaidCtx(sess)
	width := m.mdRenderer.width + 4
	for i, msg := range sess.chatMessages {
		sess.chatMessages[i] = msg.rerender(m.mdRenderer, m.styles, width)
	}
	sess.chatCache.invalidate()
}

// syncMermaidCtx points the shared markdown renderer at a session's daemon
// thread id (and the daemon's whiteboard base) so ```mermaid blocks render with
// a correct per-thread "See it on the whiteboard" link.
func (m *Model) syncMermaidCtx(sess *ThreadState) {
	if sess == nil {
		return
	}
	// The daemon reports the whiteboard base in the thread_started event, which
	// ThreadClient consumes during connect (the TUI event loop never sees it), so
	// read it straight from the client. Cache the last non-empty value so a render
	// during a brief reconnect (client momentarily nil) still has it.
	if sess.client != nil {
		if b := sess.client.WhiteboardBase(); b != "" {
			m.whiteboardBase = b
		}
	}
	m.mdRenderer.SetWhiteboardContext(m.whiteboardBase, sess.daemonThreadID)
}

// rowTarget identifies what a single Threads-tab row points at: a live thread
// (liveIdx is an index into m.threads, sum == nil) or a persisted, not-attached
// vix-initiated record (liveIdx == -1, sum != nil).
type rowTarget struct {
	liveIdx int
	sum     *protocol.ThreadSummary
}

// rowStartedAt returns the creation time used to order a Threads-tab row. A
// live vix thread carries its origin record in vixSummary, so a record keeps
// the same StartedAt — and therefore the same list position — when it
// transitions from persisted to attached (on read). A live user thread has no
// such record, so its creation time comes from the daemon thread itself
// (ThreadState.createdAt). Not-attached records use their persisted StartedAt.
func (m *Model) rowStartedAt(r rowTarget) time.Time {
	switch {
	case r.sum != nil:
		t, _ := time.Parse(time.RFC3339, r.sum.StartedAt)
		return t
	case r.liveIdx >= 0 && r.liveIdx < len(m.threads):
		sess := m.threads[r.liveIdx]
		if vs := sess.vixSummary; vs != nil {
			t, _ := time.Parse(time.RFC3339, vs.StartedAt)
			return t
		}
		return sess.createdAt()
	}
	return time.Time{}
}

// userDirBlock is one working directory's block within the User-initiated group:
// its rows (live threads and/or persisted not-attached records) plus the most
// recent activity across them, used to order non-current directories.
type userDirBlock struct {
	dir  string
	rows []rowTarget
	last time.Time
}

// recordActivity returns a record's activity time (LastRequestAt, else
// StartedAt) for ordering directories by recency.
func recordActivity(sum *protocol.ThreadSummary) time.Time {
	raw := sum.LastRequestAt
	if raw == "" {
		raw = sum.StartedAt
	}
	t, _ := time.Parse(time.RFC3339, raw)
	return t
}

// userDirBlocks groups the User-initiated threads by working directory: live
// threads (auto-attached on launch, so mostly the current cwd) and persisted
// not-attached records (from every directory). The current cwd sorts first; the
// remaining directories follow by most-recent activity (desc), tiebroken by path
// (asc) for determinism. Within a directory, rows are ordered by creation time
// (asc), interleaving live threads and records — a live thread is not hoisted
// ahead of an older record.
func (m *Model) userDirBlocks() []userDirBlock {
	byDir := map[string]*userDirBlock{}
	var order []string
	get := func(dir string) *userDirBlock {
		b := byDir[dir]
		if b == nil {
			b = &userDirBlock{dir: dir}
			byDir[dir] = b
			order = append(order, dir)
		}
		return b
	}
	// Collect live rows first only so records can dedup against them; the final
	// per-block order is by creation time below, not live-first.
	liveIDs := map[string]bool{}
	for i, s := range m.threads {
		if s.vixSummary != nil {
			continue
		}
		if s.daemonThreadID != "" {
			liveIDs[s.daemonThreadID] = true
		}
		b := get(pickCWD(s.workDir, m.cwd))
		b.rows = append(b.rows, rowTarget{liveIdx: i})
		// Fold the live thread's activity into the block's recency key too.
		// Otherwise a directory whose threads are all currently open (e.g. a
		// restored thread that just re-attached on launch) keeps last == zero
		// and sinks below directories that still have a saved record, so its
		// group position flips as threads finish attaching.
		if t := s.createdAt(); t.After(b.last) {
			b.last = t
		}
	}
	for idx := range m.userThreadRecords {
		rec := &m.userThreadRecords[idx]
		// Skip a record that is already live in this window (it attached but the
		// list hasn't refreshed yet), so it isn't shown twice.
		if liveIDs[rec.ID] {
			continue
		}
		dir := rec.CWD
		if strings.TrimSpace(dir) == "" {
			dir = m.cwd
		}
		b := get(dir)
		b.rows = append(b.rows, rowTarget{liveIdx: -1, sum: rec})
		if t := recordActivity(rec); t.After(b.last) {
			b.last = t
		}
	}
	sort.SliceStable(order, func(a, c int) bool {
		da, dc := order[a], order[c]
		if da == m.cwd {
			return dc != m.cwd
		}
		if dc == m.cwd {
			return false
		}
		if la, lc := byDir[da].last, byDir[dc].last; !la.Equal(lc) {
			return la.After(lc)
		}
		return da < dc
	})
	out := make([]userDirBlock, 0, len(order))
	for _, dir := range order {
		b := byDir[dir]
		// Order rows by creation time (asc). A connecting/draft thread with an
		// unknown start time (zero) sorts last, matching a freshly-created row.
		sort.SliceStable(b.rows, func(i, j int) bool {
			return m.userRowSortKey(b.rows[i]).Before(m.userRowSortKey(b.rows[j]))
		})
		out = append(out, *b)
	}
	return out
}

// userRowSortKey is the creation-time key used to order rows within a directory
// block. Rows with an unknown start time (a thread still connecting) sort last
// rather than first, so a just-created thread lands at the bottom of its block.
func (m *Model) userRowSortKey(r rowTarget) time.Time {
	t := m.rowStartedAt(r)
	if t.IsZero() {
		return time.Unix(1<<62, 0)
	}
	return t
}

// vixRowTargets returns the Vix-initiated group's rows: live attached records
// and persisted not-attached records merged into one StartedAt-ordered list.
func (m *Model) vixRowTargets() []rowTarget {
	var vix []rowTarget
	for i, s := range m.threads {
		if s.vixSummary != nil {
			vix = append(vix, rowTarget{liveIdx: i})
		}
	}
	for idx := range m.vixThreads {
		vix = append(vix, rowTarget{liveIdx: -1, sum: &m.vixThreads[idx]})
	}
	sort.SliceStable(vix, func(a, b int) bool {
		return m.rowStartedAt(vix[a]).Before(m.rowStartedAt(vix[b]))
	})
	return vix
}

// threadRowTargets returns one entry per Threads-tab thread row in display
// order: the User-initiated group (live threads and persisted not-attached
// records grouped by working directory, current cwd first) followed by the
// Vix-initiated group. It excludes directory headers and ignores fold state, so
// it is the canonical thread ordering used by workspace stepping — NOT the
// selection index space (see selectableThreadRows for that).
func (m *Model) threadRowTargets() []rowTarget {
	var rows []rowTarget
	for _, b := range m.userDirBlocks() {
		rows = append(rows, b.rows...)
	}
	return append(rows, m.vixRowTargets()...)
}

// threadRowKind classifies a row of the Threads tab. Section headers are chrome
// (not selectable); directory headers and thread rows are selectable.
type threadRowKind int

const (
	rowUserHeader threadRowKind = iota // "User-initiated" group header (chrome)
	rowDirHeader                       // a foldable directory path line (selectable)
	rowUserThread                      // a User-initiated thread row (selectable)
	rowVixHeader                       // "Vix-initiated" group header (chrome)
	rowVixThread                       // a Vix-initiated thread row (selectable)
)

// threadListRow is one row of the Threads tab in display order — the single
// source of truth shared by selection (model.go) and rendering (tabs.go). Rows
// carry data already resolved against m.threads so the renderer needs no access
// to the model.
type threadListRow struct {
	kind      threadRowKind
	dir       string       // rowDirHeader: directory path
	collapsed bool         // rowDirHeader: fold state
	count     int          // rowDirHeader: number of threads under it
	live      *ThreadState // thread rows: the attached thread (nil for a persisted record)
	liveIdx   int          // thread rows: m.threads index of a live row (-1 for a record)
	// sum is the column source for a thread row: a persisted record's summary,
	// or a live Vix-initiated row's origin vixSummary.
	sum protocol.ThreadSummary
}

// selectable reports whether the navigation cursor can land on this row.
func (r threadListRow) selectable() bool {
	return r.kind == rowDirHeader || r.kind == rowUserThread || r.kind == rowVixThread
}

// threadListRows builds the full, ordered Threads-tab display list: the
// User-initiated group (a foldable path header per directory, each followed by
// its thread rows unless the directory is collapsed) then the Vix-initiated
// group. Section headers are included as chrome rows. This is the single list
// consumed by both the renderer and the selection helpers.
func (m *Model) threadListRows() []threadListRow {
	var rows []threadListRow

	blocks := m.userDirBlocks()
	userCount := 0
	for _, b := range blocks {
		userCount += len(b.rows)
	}
	if userCount > 0 {
		rows = append(rows, threadListRow{kind: rowUserHeader})
		for _, b := range blocks {
			collapsed := m.collapsedDirs[b.dir]
			rows = append(rows, threadListRow{kind: rowDirHeader, dir: b.dir, collapsed: collapsed, count: len(b.rows)})
			if collapsed {
				continue
			}
			for _, r := range b.rows {
				row := threadListRow{kind: rowUserThread, liveIdx: -1}
				if r.sum != nil {
					row.sum = *r.sum
				} else {
					row.live = m.threads[r.liveIdx]
					row.liveIdx = r.liveIdx
				}
				rows = append(rows, row)
			}
		}
	}

	vix := m.vixRowTargets()
	if len(vix) > 0 {
		rows = append(rows, threadListRow{kind: rowVixHeader})
		for _, r := range vix {
			row := threadListRow{kind: rowVixThread, liveIdx: -1}
			if r.sum != nil {
				row.sum = *r.sum
			} else {
				live := m.threads[r.liveIdx]
				row.live = live
				row.liveIdx = r.liveIdx
				row.sum = *live.vixSummary
			}
			rows = append(rows, row)
		}
	}

	return rows
}

// selectableThreadRows returns just the rows the navigation cursor can land on
// (directory headers and thread rows), in display order. m.threadsSelected is
// an index into this slice.
func (m *Model) selectableThreadRows() []threadListRow {
	var out []threadListRow
	for _, r := range m.threadListRows() {
		if r.selectable() {
			out = append(out, r)
		}
	}
	return out
}

// foldSelectedDir toggles the fold state of the directory that encloses the
// highlighted Threads-tab row. It acts when the cursor is on a directory header
// (that directory) or on a User-initiated thread row (its enclosing directory),
// and is a no-op on Vix-initiated rows, which have no header. After a fold the
// cursor lands on the directory header (the thread row it started on may have
// just been hidden). Reports whether it acted.
func (m *Model) foldSelectedDir() bool {
	sel := m.selectableThreadRows()
	if m.threadsSelected < 0 || m.threadsSelected >= len(sel) {
		return false
	}
	dir, ok := m.enclosingDir(sel, m.threadsSelected)
	if !ok {
		return false
	}
	if m.collapsedDirs == nil {
		m.collapsedDirs = map[string]bool{}
	}
	m.collapsedDirs[dir] = !m.collapsedDirs[dir]
	// Re-anchor the cursor on the directory header: after collapsing, the row
	// the cursor sat on may no longer exist.
	for i, r := range m.selectableThreadRows() {
		if r.kind == rowDirHeader && r.dir == dir {
			m.threadsSelected = i
			break
		}
	}
	return true
}

// selectEnclosingDir moves the cursor from a User-initiated thread row up to its
// enclosing directory header. It is a no-op when the cursor is already on a
// directory header or on a Vix-initiated row. Reports whether it moved.
func (m *Model) selectEnclosingDir() bool {
	sel := m.selectableThreadRows()
	if m.threadsSelected < 0 || m.threadsSelected >= len(sel) {
		return false
	}
	if sel[m.threadsSelected].kind != rowUserThread {
		return false
	}
	for i := m.threadsSelected - 1; i >= 0; i-- {
		if sel[i].kind == rowDirHeader {
			m.threadsSelected = i
			return true
		}
	}
	return false
}

// enclosingDir returns the directory path of the row at idx: the directory
// itself for a header, or the nearest preceding header for a User-initiated
// thread row. Reports false for Vix-initiated rows.
func (m *Model) enclosingDir(sel []threadListRow, idx int) (string, bool) {
	switch sel[idx].kind {
	case rowDirHeader:
		return sel[idx].dir, true
	case rowUserThread:
		for i := idx - 1; i >= 0; i-- {
			if sel[i].kind == rowDirHeader {
				return sel[i].dir, true
			}
		}
	}
	return "", false
}

// visibleThreadIndices returns the indices of all live threads in Threads-tab
// row order (user-initiated first, then attached vix-initiated ones in the same
// order they render). Persisted, not-attached records are skipped.
func (m *Model) visibleThreadIndices() []int {
	var out []int
	for _, r := range m.threadRowTargets() {
		if r.sum == nil {
			out = append(out, r.liveIdx)
		}
	}
	return out
}

// armCursorBlink re-focuses a thread's input and returns the command that
// restarts its cursor blink loop. The blink is a self-rescheduling BlinkMsg
// chain keyed to one cursor's id; switching the active workspace thread
// orphans the previous thread's chain (its BlinkMsgs are routed to the new
// input and dropped on id mismatch) without starting one for the new cursor.
// Every switch must re-arm the loop or the cursor freezes — sometimes in its
// hidden phase, so it looks like it disappeared — until the user types.
func armCursorBlink(sess *ThreadState) tea.Cmd {
	if sess == nil {
		return nil
	}
	return sess.input.Focus()
}

// stepWorkspaceThread moves the workspace to the next (dir > 0) or previous
// (dir < 0) thread in Threads-tab display order (user-initiated first, then
// vix-initiated) — which can differ from m.threads slice order, since
// attached threads are inserted by creation time. Reports false when there is
// no thread in that direction.
func (m *Model) stepWorkspaceThread(dir int) ([]tea.Cmd, bool) {
	order := m.visibleThreadIndices()
	pos := -1
	for i, idx := range order {
		if idx == m.selectedThread {
			pos = i
			break
		}
	}
	next := pos + dir
	if pos < 0 || next < 0 {
		return nil, false
	}
	if next >= len(order) {
		// Past the last live thread: the rows below are persisted,
		// not-yet-attached records (user-initiated ones grouped by directory,
		// then vix-initiated). Attach the first — same as pressing enter on it
		// in the Threads tab; the replay's threadRestoredMsg focuses it and
		// marks it read.
		for _, r := range m.threadRowTargets() {
			if r.sum != nil {
				sum := *r.sum
				m.focusRestoredID = sum.ID
				return []tea.Cmd{attachRestoreThread(m.socketPath, pickCWD(sum.CWD, m.cwd), m.cfg.ConfigDir, m.cfg.Model, m.authToken, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, sum)}, true
			}
		}
		return nil, false
	}
	var cmds []tea.Cmd
	m.selectedThread = order[next]
	m.activeTab = TabKindChat
	selSess := m.threads[m.selectedThread]
	m.markThreadRead(selSess)
	selSess.input.SetWidth(m.width - 4)
	if selSess.client == nil && !selSess.reconnecting && selSess.daemonThreadID != "" {
		selSess.reconnecting = true
		cmds = append(cmds, attemptReconnect(m.socketPath, pickCWD(selSess.workDir, m.cwd), m.cfg.ConfigDir, m.cfg.Model, m.authToken, false, m.enableAutomaticWritePermission, m.enableAutomaticDirectoryAccess, selSess.daemonThreadID))
	}
	cmds = append(cmds, selSess.thinkingAnim.Resume())
	cmds = append(cmds, armCursorBlink(selSess))
	if !m.hasAlertThreads() {
		m.stopTabAlertBlink()
	}
	return cmds, true
}

// syncThreadsSelected sets threadsSelected to the visible row that corresponds
// to the currently active workspace thread (selectedThread).
func (m *Model) syncThreadsSelected() {
	rows := m.selectableThreadRows()
	for i, r := range rows {
		if (r.kind == rowUserThread || r.kind == rowVixThread) && r.live != nil && r.liveIdx == m.selectedThread {
			m.threadsSelected = i
			return
		}
	}
	// The active thread is hidden inside a collapsed directory (its row was
	// folded away): land the cursor on that directory's header instead.
	if m.selectedThread >= 0 && m.selectedThread < len(m.threads) {
		dir := pickCWD(m.threads[m.selectedThread].workDir, m.cwd)
		for i, r := range rows {
			if r.kind == rowDirHeader && r.dir == dir {
				m.threadsSelected = i
				return
			}
		}
	}
}

// threadsSelectedIdx returns the m.threads index for the highlighted row, when
// that row is a live thread. Directory headers and persisted records report
// false (use vixSelectedSummary for records).
func (m *Model) threadsSelectedIdx() (int, bool) {
	rows := m.selectableThreadRows()
	if m.threadsSelected < 0 || m.threadsSelected >= len(rows) {
		return 0, false
	}
	r := rows[m.threadsSelected]
	if (r.kind == rowUserThread || r.kind == rowVixThread) && r.live != nil {
		return r.liveIdx, true
	}
	return 0, false
}

// vixSelectedSummary returns the record summary for the highlighted row, when
// that row is a persisted, not-attached thread record (user- or vix-initiated).
// Both are reopened the same way (attachRestoreThread). Directory headers and
// live thread rows report false.
func (m *Model) vixSelectedSummary() (protocol.ThreadSummary, bool) {
	rows := m.selectableThreadRows()
	if m.threadsSelected < 0 || m.threadsSelected >= len(rows) {
		return protocol.ThreadSummary{}, false
	}
	if r := rows[m.threadsSelected]; (r.kind == rowUserThread || r.kind == rowVixThread) && r.live == nil {
		return r.sum, true
	}
	return protocol.ThreadSummary{}, false
}

// hasAlertThreads reports whether any thread is waiting for user input.
func (m *Model) hasAlertThreads() bool {
	for _, sess := range m.threads {
		if sess.agentState == StateConfirmPending || sess.agentState == StateUserQuestion {
			return true
		}
	}
	return false
}

// maybeStartTabAlertBlink starts the tab alert blink if any thread needs attention.
func (m *Model) maybeStartTabAlertBlink() tea.Cmd {
	if m.tabAlertActive || !m.hasAlertThreads() {
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

// hasBusyThreads reports whether any thread is actively working (streaming,
// running a tool, or executing a plan) — the states the threads-list spinner
// animates.
func (m *Model) hasBusyThreads() bool {
	for _, sess := range m.threads {
		switch sess.agentState {
		case StateStreaming, StateToolExecuting, StatePlanExecuting:
			return true
		}
	}
	return false
}

// maybeStartThreadsSpinner starts the threads-list loading spinner when the
// Threads tab is active and at least one thread is busy. No-op otherwise (and
// when already running), so it is safe to call on every relevant state change.
func (m *Model) maybeStartThreadsSpinner() tea.Cmd {
	if m.threadsSpinnerActive || !m.spinnerShouldRun() {
		return nil
	}
	m.threadsSpinnerActive = true
	return m.threadsSpinnerTick()
}

// hasRunningJobs reports whether any scheduled job is currently executing.
func (m *Model) hasRunningJobs() bool {
	for _, j := range m.jobs {
		if j.Running {
			return true
		}
	}
	return false
}

// spinnerShouldRun reports whether the list-loading spinner should animate:
// busy threads on the Threads tab, or a running job on the Jobs & Triggers
// tab. Used to gate both starting and continuing the spinner loop.
func (m *Model) spinnerShouldRun() bool {
	switch m.activeTab {
	case TabKindThreads:
		return m.hasBusyThreads()
	case TabKindJobs:
		return m.hasRunningJobs()
	}
	return false
}

// stopThreadsSpinner halts the spinner loop and bumps the generation so any
// in-flight tick already queued in Bubble Tea's channel is ignored on arrival.
func (m *Model) stopThreadsSpinner() {
	m.threadsSpinnerActive = false
	m.threadsSpinnerGen++
}

// threadsSpinnerTick schedules the next threads-list spinner frame.
func (m *Model) threadsSpinnerTick() tea.Cmd {
	gen := m.threadsSpinnerGen
	return tea.Tick(threadsSpinnerPeriod, func(time.Time) tea.Msg {
		return threadsSpinnerMsg{gen: gen}
	})
}

// nextWorkflow cycles through available workflows for a thread.
func (m *Model) nextWorkflow(sess *ThreadState) string {
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
func (m *Model) currentModeName(sess *ThreadState) string {
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

// emitStatusMsg surfaces a transient status bar message and returns a tea.Cmd
// that clears it after 3 seconds. Error-kind messages are instead shown as a
// persistent centered popup (m.alertPopup) that stays until the user dismisses
// it, so failures aren't missed. Rapid successive calls are safe because each
// status message bumps a generation counter; only the matching clear fires.
func (m *Model) emitStatusMsg(text string, kind StatusMsgKind) tea.Cmd {
	if kind == StatusMsgError {
		m.alertPopup = text
		return nil
	}
	m.statusMsg.gen++
	m.statusMsg.Text = text
	m.statusMsg.Kind = kind
	gen := m.statusMsg.gen
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return clearStatusMsgMsg{gen: gen}
	})
}

// placeholderForMode returns mode-specific placeholder text.
func (m *Model) placeholderForMode(sess *ThreadState) string {
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
func (m *Model) updateInputPromptColor(sess *ThreadState) {
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

// fillTestData populates the current thread with fake messages for UI testing.
func (m *Model) fillTestData() {
	sess := m.currentThread()
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
