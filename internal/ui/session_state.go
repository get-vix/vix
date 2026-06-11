package ui

import (
	"time"

	"charm.land/bubbles/v2/textarea"
	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon"
	"github.com/get-vix/vix/internal/protocol"
	"github.com/get-vix/vix/internal/providers"
)

// streamRenderInterval caps how often the accumulated streaming buffers are
// re-rendered through glamour (see lastStreamRender/lastThinkingRender).
const streamRenderInterval = 100 * time.Millisecond

// SessionState holds all accumulated UI state for a single agent session.
// Sessions are independent objects — the Chat tab renders whichever session
// is currently selected. Messages accumulate continuously from daemon events
// regardless of which tab is visible.
type SessionState struct {
	// daemonSessionID is the session ID assigned by the daemon after the
	// initial handshake. It is used as the stable key carried by all async
	// goroutines (event loops, reconnect attempts) so the Update handler can
	// locate the right session even after the sessions slice has been
	// re-ordered by a close operation. It changes on every successful
	// reconnect, which naturally invalidates any in-flight messages from the
	// previous connection without needing a separate generation counter.
	// Empty for sessions that have never successfully connected.
	daemonSessionID string

	// Daemon connection
	client       *daemon.SessionClient
	reconnecting bool
	// closing is set when the TUI itself initiated this session's close (the
	// quit-time "close all sessions" flow). The daemon tears the connection
	// down as part of handling session.close, so the subsequent disconnect is
	// expected: the handler must not treat it as a lost connection and
	// auto-reconnect, which would resurrect the just-closed session.
	closing   bool
	initState protocol.InitState

	// Accumulated chat display — built from daemon events
	chatMessages     []ChatMessage
	chatScrollOffset int

	// Live streaming buffers
	assistantBuf      string
	assistantRendered string
	thinkingBuf       string
	thinkingRendered  string
	showThinking      bool

	// Agent / workflow state
	agentState     AppState
	activeWorkflow string
	workflows      []protocol.WorkflowInfo
	skills         []protocol.SkillInfo
	activePlan     *protocol.Plan
	todos          []protocol.TodoItem

	// Token accounting
	inputTokens                  int64
	outputTokens                 int64
	cacheCreationTokens          int64
	cacheReadTokens              int64
	lastOutputTokens             int64
	turnStartInputTokens         int64
	turnStartOutputTokens        int64
	turnStartCacheCreationTokens int64
	turnStartCacheReadTokens     int64
	elapsed                      time.Duration

	// Context-window indicator
	lastInputTokens int64 // true prompt size of the most recent turn
	contextWindow   int64 // 0 = unknown (model not in ContextWindow table)

	// Confirm / question state
	confirmToolName    string
	confirmDetailShown bool

	// Pending messages
	pendingInput      *pendingMsg
	pendingPlanAction *pendingPlanAction
	pendingTools      map[string]int

	// Panels
	rightPanel         RightPanel
	workflowGraphPanel WorkflowGraphPanel
	questionPanel      QuestionPanel
	attachmentPanel    AttachmentPanel
	historyPanel       HistoryPanel

	// Input area
	input         textarea.Model
	focus         FocusState
	fileCompleter FileCompleter
	slashMenu     SlashMenu

	// Animation
	thinkingAnim ThinkingAnim

	// Cached transcript render (see chatcache.go)
	chatCache chatCache

	// Memo for the bordered chat box: skips lipgloss Wrap/applyBorder when
	// the visible lines, focus, and dimensions are unchanged since the last
	// frame. chatBoxKey is "<w>|<h>|<focused>|" + the joined visible lines.
	chatBoxKey      string
	chatBoxRendered string

	// Streaming render throttle: glamour re-renders the whole accumulated
	// buffer on each chunk, which is O(n²) over a long reply. Re-render at
	// most every streamRenderInterval; event.stream_done always does a final
	// full render.
	lastStreamRender   time.Time
	lastThinkingRender time.Time

	// Input recall history (.vix/history.txt)
	history *History

	// Current model name
	modelName string

	// unreadCount is the number of completed agent responses that arrived
	// while this session was not the active workspace view.
	unreadCount int

	// Trim confirm state
	trimPrevState AppState
	trimSelected  int
	trimSep       TurnSepInfo

	// Fork lineage (zero values for root sessions)
	parentID    string
	forkTurnIdx int

	// orphaned is set when a reconnect attach reported the session no longer
	// exists on disk (e.g. lost in a daemon restart before its first flush).
	// The conversation can't be continued; input is disabled and the user is
	// told to /copy it before it's gone.
	orphaned bool

	// awaitingReplay is set for a session that was attached (restored) on launch
	// and is still waiting for its event.replay to rebuild the viewport. While
	// true the chat area shows a "Restoring conversation…" placeholder instead
	// of the welcome screen, so a restored conversation doesn't flash the
	// welcome view before its history arrives.
	awaitingReplay bool
}

// newSessionState initialises a fresh session state ready for a new agent session.
func newSessionState(cfg *config.Config, client *daemon.SessionClient) *SessionState {
	s := &SessionState{
		agentState:    StateWaitingForInput,
		input:         newInput(),
		thinkingAnim:  NewThinkingAnim(),
		questionPanel: NewQuestionPanel(),
		focus:         FocusEditor,
		client:        client,
		modelName:     cfg.Model,
		contextWindow: providers.Default().ContextWindow(cfg.Model),
		history:       NewHistory(cfg.Paths.Primary()),
		showThinking:  config.ShowThinking(),
	}
	if client != nil {
		s.daemonSessionID = client.SessionID()
	}
	return s
}

// setModel updates the session's model spec and refreshes the resolved context
// window used by the status-bar indicator and (daemon-side) auto-compaction.
func (s *SessionState) setModel(spec string) {
	s.modelName = spec
	s.contextWindow = providers.Default().ContextWindow(spec)
}
