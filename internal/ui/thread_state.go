package ui

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"charm.land/bubbles/v2/textarea"
	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon"
	"github.com/get-vix/vix/internal/protocol"
	"github.com/get-vix/vix/internal/providers"
)

// threadPhase distinguishes a draft (client-only, never connected) thread
// from a live one that has a daemon connection. A draft is created up front for
// a fresh launch (nothing to restore) and for every ctrl+t tab; it holds no
// ThreadClient and sends no thread.start until the user submits the first
// message, at which point its working directory is frozen for the rest of the
// thread's life.
type threadPhase int

const (
	// phaseDraft: no daemon connection yet. The welcome screen is shown and the
	// working directory (draftCWD) may still be changed. Committed on the first
	// message submit.
	phaseDraft threadPhase = iota
	// phaseLive: thread.start has been (or is being) sent; cwd is frozen.
	phaseLive
)

// newClientKey returns a random, process-unique handle for a ThreadState. It
// is stable for the thread's lifetime and independent of daemonThreadID,
// which lets the Update loop match an async connect result back to the right
// draft even when several drafts coexist (all of which have an empty
// daemonThreadID until they commit).
func newClientKey() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// streamRenderInterval caps how often the accumulated streaming buffers are
// re-rendered through glamour (see lastStreamRender/lastThinkingRender).
const streamRenderInterval = 100 * time.Millisecond

// ThreadState holds all accumulated UI state for a single agent thread.
// Threads are independent objects — the Chat tab renders whichever thread
// is currently selected. Messages accumulate continuously from daemon events
// regardless of which tab is visible.
type ThreadState struct {
	// daemonThreadID is the thread ID assigned by the daemon after the
	// initial handshake. It is used as the stable key carried by all async
	// goroutines (event loops, reconnect attempts) so the Update handler can
	// locate the right thread even after the threads slice has been
	// re-ordered by a close operation. It changes on every successful
	// reconnect, which naturally invalidates any in-flight messages from the
	// previous connection without needing a separate generation counter.
	// Empty for threads that have never successfully connected.
	daemonThreadID string

	// Daemon connection
	client       *daemon.ThreadClient
	reconnecting bool

	// startedAt caches the daemon thread's creation time, captured from the
	// client on (re)connect. It survives brief client==nil windows (reconnect,
	// orphaned) so the Threads tab can keep ordering the row by creation time.
	startedAt time.Time

	// phase is phaseDraft until the thread is committed (first message) and
	// phaseLive thereafter. clientKey is a stable, process-unique handle used to
	// match async connect results back to this thread while its
	// daemonThreadID is still empty. workDir is the thread's working
	// directory: editable on the welcome screen while a draft, then frozen and
	// used as the cwd for every (re)connect. pendingFirstInput holds the message
	// that triggered the commit, sent once the connection is established.
	phase     threadPhase
	clientKey string
	workDir   string
	// recentDirSelected is the highlighted row in the welcome screen's
	// recent-directories list while this thread is a draft and the welcome
	// area is focused. Navigated with up/down; enter applies it to workDir.
	recentDirSelected int
	pendingFirstInput *pendingMsg
	// closing is set when the TUI itself initiated this thread's close (the
	// quit-time "close all threads" flow). The daemon tears the connection
	// down as part of handling thread.close, so the subsequent disconnect is
	// expected: the handler must not treat it as a lost connection and
	// auto-reconnect, which would resurrect the just-closed thread.
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
	cancelAckPending  bool

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
	// dirPicker is the working-directory browser opened with Ctrl+O on a draft
	// thread's welcome screen. It reuses FileCompleter in directory-only mode.
	dirPicker FileCompleter

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
	// while this thread was not the active workspace view.
	unreadCount int

	// Trim confirm state
	trimPrevState AppState
	trimSelected  int
	trimSep       TurnSepInfo

	// Fork lineage (zero values for root threads)
	parentID    string
	forkTurnIdx int

	// orphaned is set when a reconnect attach reported the thread no longer
	// exists on disk (e.g. lost in a daemon restart before its first flush).
	// The conversation can't be continued; input is disabled and the user is
	// told to /copy it before it's gone.
	orphaned bool

	// awaitingReplay is set for a thread that was attached (restored) on launch
	// and is still waiting for its event.replay to rebuild the viewport. While
	// true the chat area shows a "Restoring conversation…" placeholder instead
	// of the welcome screen, so a restored conversation doesn't flash the
	// welcome view before its history arrives.
	awaitingReplay bool

	// initializing is set while a reopened thread's daemon-side initBrain is
	// still running: the content-only event.replay has arrived (transcript is
	// rendered) but event.replay_ready has not, so the conversation is shown
	// read-only and input is rejected. Cleared by event.replay_ready, or on a
	// disconnect so a dropped connection can't leave the thread stuck read-only.
	initializing bool

	// vixSummary is set when this thread was attached from a vix-initiated
	// record (job run, alert). It carries the record's trigger/status metadata
	// and keeps the thread rendered inside the Threads tab's "Vix-initiated"
	// group rather than among the user-initiated threads.
	vixSummary *protocol.ThreadSummary

	// title is the thread's display title (LLM-generated after a few turns,
	// or set at creation for job runs). Empty = the Threads tab falls back to
	// the first user message.
	title string
}

// newThreadState initialises a fresh thread state ready for a new agent thread.
// A nil client yields a draft thread (phaseDraft): no daemon connection is
// opened until the first message commits it. A non-nil client (restore/attach)
// is live immediately.
func newThreadState(cfg *config.Config, client *daemon.ThreadClient) *ThreadState {
	phase := phaseDraft
	if client != nil {
		phase = phaseLive
	}
	s := &ThreadState{
		agentState:    StateWaitingForInput,
		input:         newInput(),
		thinkingAnim:  NewThinkingAnim(),
		questionPanel: NewQuestionPanel(),
		focus:         FocusEditor,
		client:        client,
		phase:         phase,
		clientKey:     newClientKey(),
		workDir:       cfg.CWD,
		modelName:     cfg.Model,
		contextWindow: providers.Default().ContextWindow(cfg.Model),
		history:       NewHistory(cfg.Paths.Primary()),
		showThinking:  config.ShowThinking(),
	}
	if client != nil {
		s.daemonThreadID = client.ThreadID()
	}
	return s
}

// createdAt returns the daemon thread's creation time, used to order the row in
// the Threads tab. It prefers the live client's start time and falls back to
// the cached startedAt while the client is momentarily absent (reconnect,
// orphaned) or in tests. A draft that has never connected returns the zero time.
func (s *ThreadState) createdAt() time.Time {
	if s.client != nil {
		if t := s.client.StartedAt(); !t.IsZero() {
			return t
		}
	}
	return s.startedAt
}

// setModel updates the thread's model spec and refreshes the resolved context
// window used by the status-bar indicator and (daemon-side) auto-compaction.
func (s *ThreadState) setModel(spec string) {
	s.modelName = spec
	s.contextWindow = providers.Default().ContextWindow(spec)
}
