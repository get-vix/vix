package ui

import (
	"testing"

	"github.com/get-vix/vix/internal/daemon"
)

// A duplicated/forked thread is created as a draft and connected through
// connectFork → reconnectSuccessMsg. That handler must promote it to phaseLive:
// otherwise its first follow-up message falls into the draft-commit branch and
// connectDraft a fresh, empty daemon thread — throwing away the fork-seeded
// history (the "duplicate-of-a-duplicate starts empty" regression).
func TestReconnectSuccess_PromotesDraftToLive(t *testing.T) {
	cfg := testCfg(t.TempDir())
	sess := newThreadState(cfg, nil) // phaseDraft, daemonThreadID ""
	sess.reconnecting = true
	sess.chatMessages = []ChatMessage{{}} // seeded copy (non-empty)
	m := &Model{cfg: cfg, threads: []*ThreadState{sess}, selectedThread: 0, width: 100}

	client := daemon.NewThreadClient(cfg.SocketPath)
	m.updateInner(reconnectSuccessMsg{daemonThreadID: "", client: client})

	if sess.phase != phaseLive {
		t.Fatalf("reconnected thread must be promoted to phaseLive, got %v", sess.phase)
	}
	if sess.reconnecting {
		t.Fatal("reconnected thread must clear reconnecting")
	}
	if sess.client != client {
		t.Fatal("reconnected thread must adopt the new client")
	}
}
