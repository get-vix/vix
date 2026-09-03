package ui

import (
	"testing"

	"github.com/get-vix/vix/internal/protocol"
)

// newInitializingModel builds a Model with a single live thread wired for the
// replay handlers (styles + markdown renderer), returning the thread too.
func newInitializingModel(t *testing.T) (*Model, *ThreadState) {
	t.Helper()
	cfg := testCfg(t.TempDir())
	sess := newThreadState(cfg, nil)
	s := NewStyles(true)
	m := &Model{
		cfg:            cfg,
		activeTab:      TabKindChat,
		selectedThread: 0,
		threads:        []*ThreadState{sess},
		styles:         s,
		mdRenderer:     NewMarkdownRenderer(80, true, s.CodeBoxBorderStyle),
		width:          100,
	}
	return m, sess
}

// A content-only replay (Initializing=true) renders the transcript but marks
// the thread read-only and drops the restoring placeholder.
func TestReplay_InitializingRendersReadOnly(t *testing.T) {
	m, sess := newInitializingModel(t)
	sess.awaitingReplay = true

	m.applyEventToThread(0, protocol.ThreadEvent{Type: "event.replay", Data: protocol.EventReplay{
		Messages: []protocol.ReplayMessage{
			{Role: "user", Blocks: []protocol.ReplayBlock{{Kind: "text", Text: "hello"}}},
		},
		Initializing: true,
	}})

	if !sess.initializing {
		t.Error("initializing should be set by a content-only replay")
	}
	if sess.awaitingReplay {
		t.Error("awaitingReplay should be cleared once the transcript arrives")
	}
	if len(sess.chatMessages) == 0 {
		t.Error("the transcript should be rendered while read-only")
	}
}

// event.replay_ready unlocks input, applies the resolved model, and renders the
// restore warnings.
func TestReplayReady_UnlocksAndWarns(t *testing.T) {
	m, sess := newInitializingModel(t)
	sess.initializing = true
	before := len(sess.chatMessages)

	m.applyEventToThread(0, protocol.ThreadEvent{Type: "event.replay_ready", Data: protocol.EventReplayReady{
		Model:    "anthropic/new-default",
		Warnings: []string{"This conversation was saved with model X; switched to Y."},
	}})

	if sess.initializing {
		t.Error("replay_ready must clear initializing (unlock input)")
	}
	if sess.modelName != "anthropic/new-default" {
		t.Errorf("model = %q, want resolved default", sess.modelName)
	}
	if len(sess.chatMessages) != before+1 {
		t.Errorf("expected the restore warning to be appended (%d -> %d)", before, len(sess.chatMessages))
	}
}

// While initializing, Enter must not submit or queue anything.
func TestHandleEnter_BlockedWhileInitializing(t *testing.T) {
	_, sess := newInitializingModel(t)
	sess.initializing = true
	sess.agentState = StateWaitingForInput
	sess.input.SetValue("should not send")

	m := &Model{styles: NewStyles(true)}
	m.handleEnter(sess)

	if sess.pendingInput != nil {
		t.Errorf("no input should be queued while initializing, got %+v", sess.pendingInput)
	}
	if sess.input.Value() != "should not send" {
		t.Errorf("input should be untouched while read-only, got %q", sess.input.Value())
	}
}

// A disconnect between the content replay and replay_ready must clear
// initializing so the thread isn't stuck read-only through the reconnect.
func TestDisconnect_ClearsInitializing(t *testing.T) {
	m, sess := newInitializingModel(t)
	sess.initializing = true
	sess.daemonThreadID = "sess-1"

	m.updateInner(threadDisconnectedMsg{daemonThreadID: "sess-1"})

	if sess.initializing {
		t.Error("disconnect must clear initializing to avoid a stuck read-only thread")
	}
}
