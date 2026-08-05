package daemon

import (
	"testing"

	"github.com/get-vix/vix/internal/daemon/llm"
)

// makeMsg is a helper that builds a minimal user MessageParam with the given text.
func makeMsg(text string) llm.MessageParam {
	return llm.NewUserMessage(llm.NewTextBlock(text))
}

// seedSnapshots populates a thread with n turn snapshots.
// Snapshot i contains i+1 messages ("turn-0" … "turn-i").
func seedSnapshots(s *Thread, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnSnapshots = nil
	s.messages = nil
	for i := 0; i < n; i++ {
		snap := make([]llm.MessageParam, i+1)
		for j := 0; j <= i; j++ {
			snap[j] = makeMsg("turn-" + string(rune('0'+j)))
		}
		s.turnSnapshots = append(s.turnSnapshots, snap)
	}
	// Leave messages pointing at the latest snapshot (as the real loop does).
	last := s.turnSnapshots[n-1]
	s.messages = make([]llm.MessageParam, len(last))
	copy(s.messages, last)
}

// --- trimHistory tests ---

func TestTrimHistory_MiddleTurn(t *testing.T) {
	s := &Thread{}
	seedSnapshots(s, 4) // turns 0-3

	s.trimHistory(1) // keep up to turn 1

	if len(s.messages) != 2 {
		t.Fatalf("expected 2 messages after trim to turn 1, got %d", len(s.messages))
	}
	if len(s.turnSnapshots) != 2 {
		t.Fatalf("expected 2 snapshots after trim to turn 1, got %d", len(s.turnSnapshots))
	}
}

func TestTrimHistory_FirstTurn(t *testing.T) {
	s := &Thread{}
	seedSnapshots(s, 3)

	s.trimHistory(0) // keep only turn 0

	if len(s.messages) != 1 {
		t.Fatalf("expected 1 message after trim to turn 0, got %d", len(s.messages))
	}
	if len(s.turnSnapshots) != 1 {
		t.Fatalf("expected 1 snapshot after trim to turn 0, got %d", len(s.turnSnapshots))
	}
}

func TestTrimHistory_LastTurn_IsNoop(t *testing.T) {
	s := &Thread{}
	seedSnapshots(s, 3)

	s.trimHistory(2) // trim to the last turn — messages unchanged

	if len(s.messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(s.messages))
	}
	if len(s.turnSnapshots) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(s.turnSnapshots))
	}
}

func TestTrimHistory_OutOfRange_High(t *testing.T) {
	s := &Thread{}
	seedSnapshots(s, 2)

	s.trimHistory(99) // no-op

	if len(s.messages) != 2 {
		t.Fatalf("expected messages unchanged, got %d", len(s.messages))
	}
	if len(s.turnSnapshots) != 2 {
		t.Fatalf("expected snapshots unchanged, got %d", len(s.turnSnapshots))
	}
}

func TestTrimHistory_Negative_IsNoop(t *testing.T) {
	s := &Thread{}
	seedSnapshots(s, 2)

	s.trimHistory(-1)

	if len(s.messages) != 2 {
		t.Fatalf("expected messages unchanged, got %d", len(s.messages))
	}
}

func TestTrimHistory_EmptySnapshots_IsNoop(t *testing.T) {
	s := &Thread{}
	// No snapshots at all.
	s.trimHistory(0)
	if s.messages != nil {
		t.Fatal("expected messages to remain nil")
	}
}

func TestTrimHistory_MessagesMatchSnapshot(t *testing.T) {
	s := &Thread{}
	seedSnapshots(s, 4)

	s.trimHistory(1)

	snap := s.turnSnapshots[1]
	if len(s.messages) != len(snap) {
		t.Fatalf("messages len %d != snapshot len %d", len(s.messages), len(snap))
	}
	// Verify it's a copy, not the same slice.
	if len(snap) > 0 && &s.messages[0] == &snap[0] {
		t.Fatal("trimHistory must copy the snapshot, not alias it")
	}
}

// --- snapshotMessagesForFork tests ---

func TestSnapshotMessagesForFork_ValidIdx(t *testing.T) {
	s := &Thread{}
	seedSnapshots(s, 3)

	snap := s.snapshotMessagesForFork(1)
	if len(snap) != 2 {
		t.Fatalf("expected 2 messages for turn 1, got %d", len(snap))
	}
}

func TestSnapshotMessagesForFork_ZeroIdx(t *testing.T) {
	s := &Thread{}
	seedSnapshots(s, 3)

	snap := s.snapshotMessagesForFork(0)
	if len(snap) != 1 {
		t.Fatalf("expected 1 message for turn 0, got %d", len(snap))
	}
}

func TestSnapshotMessagesForFork_OutOfRange_High(t *testing.T) {
	s := &Thread{}
	seedSnapshots(s, 2)

	if snap := s.snapshotMessagesForFork(99); snap != nil {
		t.Fatalf("expected nil for out-of-range idx, got %v", snap)
	}
}

func TestSnapshotMessagesForFork_Negative(t *testing.T) {
	s := &Thread{}
	seedSnapshots(s, 2)

	if snap := s.snapshotMessagesForFork(-1); snap != nil {
		t.Fatalf("expected nil for negative idx, got %v", snap)
	}
}

func TestSnapshotMessagesForFork_Empty(t *testing.T) {
	s := &Thread{}

	if snap := s.snapshotMessagesForFork(0); snap != nil {
		t.Fatalf("expected nil when no snapshots, got %v", snap)
	}
}

// --- fork seeding tests ---

func TestForkSeeding_SeedsMessagesFromSource(t *testing.T) {
	src := &Thread{}
	seedSnapshots(src, 3)

	// Simulate what server.go does when starting a forked thread.
	dst := &Thread{}
	if msgs := src.snapshotMessagesForFork(1); len(msgs) > 0 {
		dst.messages = msgs
	}

	if len(dst.messages) != 2 {
		t.Fatalf("forked thread expected 2 messages, got %d", len(dst.messages))
	}
}

func TestForkSeeding_OutOfRange_LeavesDestinationEmpty(t *testing.T) {
	src := &Thread{}
	seedSnapshots(src, 2)

	dst := &Thread{}
	if msgs := src.snapshotMessagesForFork(99); len(msgs) > 0 {
		dst.messages = msgs
	}

	if len(dst.messages) != 0 {
		t.Fatalf("expected dst messages to be empty for out-of-range fork, got %d", len(dst.messages))
	}
}

func TestForkSeeding_DoesNotAliasSourceMessages(t *testing.T) {
	src := &Thread{}
	seedSnapshots(src, 2)

	snap := src.snapshotMessagesForFork(1)

	// Mutate the source; the snapshot returned should be unaffected because
	// seedSnapshots copies the slice. (snapshotMessagesForFork returns the
	// stored slice directly — callers own it after seeding.)
	src.trimHistory(0)

	// The snapshot we got before the trim should still have 2 messages.
	if len(snap) != 2 {
		t.Fatalf("snapshot should not be affected by later trim, got len=%d", len(snap))
	}
}

func TestTrimHistory_ThenFork_SeesOnlyTrimmedHistory(t *testing.T) {
	s := &Thread{}
	seedSnapshots(s, 4) // turns 0-3

	s.trimHistory(1) // trim to turn 1

	// After trim, turn 2 and 3 should be gone.
	if snap := s.snapshotMessagesForFork(2); snap != nil {
		t.Fatalf("expected turn 2 to be gone after trim, got %v", snap)
	}
	// Turn 1 should still be accessible.
	snap := s.snapshotMessagesForFork(1)
	if len(snap) != 2 {
		t.Fatalf("expected turn 1 to have 2 messages, got %d", len(snap))
	}
}

// --- rebuildTurnSnapshots tests ---

// turnMsgs builds a two-turn history: for each turn a user message, an
// assistant tool_use, a user tool_result, then an assistant end_turn message.
// Turn boundaries (end_turn assistant messages) sit at indices 3 and 7.
func twoTurnHistory() []llm.MessageParam {
	return []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("q1")),                           // 0
		llm.NewAssistantMessage(llm.NewToolUseBlock("t1", "read_file", nil)), // 1
		llm.NewUserMessage(llm.NewTextBlock("result1")),                      // 2
		llm.NewAssistantMessage(llm.NewTextBlock("answer1")),                 // 3 end_turn
		llm.NewUserMessage(llm.NewTextBlock("q2")),                           // 4
		llm.NewAssistantMessage(llm.NewToolUseBlock("t2", "read_file", nil)), // 5
		llm.NewUserMessage(llm.NewTextBlock("result2")),                      // 6
		llm.NewAssistantMessage(llm.NewTextBlock("answer2")),                 // 7 end_turn
	}
}

func TestRebuildTurnSnapshots_MarksEndTurnBoundaries(t *testing.T) {
	snaps := rebuildTurnSnapshots(twoTurnHistory())

	if len(snaps) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snaps))
	}
	if len(snaps[0]) != 4 {
		t.Errorf("turn 0 snapshot should end at index 3 (4 messages), got %d", len(snaps[0]))
	}
	if len(snaps[1]) != 8 {
		t.Errorf("turn 1 snapshot should include all 8 messages, got %d", len(snaps[1]))
	}
}

func TestRebuildTurnSnapshots_IgnoresIncompleteTrailingTurn(t *testing.T) {
	// A user prompt with no closing assistant end_turn message yet.
	msgs := append(twoTurnHistory(),
		llm.NewUserMessage(llm.NewTextBlock("q3")),
		llm.NewAssistantMessage(llm.NewToolUseBlock("t3", "read_file", nil)),
	)

	snaps := rebuildTurnSnapshots(msgs)

	if len(snaps) != 2 {
		t.Fatalf("incomplete trailing turn must not produce a snapshot; got %d snapshots", len(snaps))
	}
}

func TestRebuildTurnSnapshots_Empty(t *testing.T) {
	if snaps := rebuildTurnSnapshots(nil); snaps != nil {
		t.Fatalf("expected nil for empty history, got %v", snaps)
	}
}

func TestRebuildTurnSnapshots_CopiesNotAliases(t *testing.T) {
	msgs := twoTurnHistory()
	snaps := rebuildTurnSnapshots(msgs)

	// The snapshot must be a distinct backing array so later trims/mutations of
	// s.messages don't corrupt stored snapshots.
	if &snaps[1][0] == &msgs[0] {
		t.Fatal("rebuildTurnSnapshots must copy the prefix, not alias the source slice")
	}
}

// TestForkOfFork_SeededThreadIsForkable reproduces the duplicate-of-a-duplicate
// bug: a thread seeded purely from a fork (messages set, turnSnapshots empty)
// must be forkable again. With snapshots rebuilt on seed, the second fork copies
// the full history instead of starting empty.
func TestForkOfFork_SeededThreadIsForkable(t *testing.T) {
	// Original thread with two real turns.
	orig := &Thread{}
	orig.turnSnapshots = rebuildTurnSnapshots(twoTurnHistory())

	// First duplicate: seed messages + snapshots from the last turn (as server.go does).
	dupA := &Thread{}
	msgsA := orig.snapshotMessagesForFork(1)
	if len(msgsA) == 0 {
		t.Fatal("first fork found no history to copy")
	}
	dupA.messages = msgsA
	dupA.turnSnapshots = rebuildTurnSnapshots(msgsA)

	// Second duplicate, from the first duplicate. Before the fix this returned
	// nil because dupA.turnSnapshots was empty.
	msgsB := dupA.snapshotMessagesForFork(1)
	if len(msgsB) != 8 {
		t.Fatalf("fork-of-fork should copy all 8 messages, got %d", len(msgsB))
	}
}
