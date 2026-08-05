package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

// fakeCompactionLLM is a minimal llm.Client that returns a canned summary for
// the one-shot summarization call made by compactMessages.
type fakeCompactionLLM struct {
	summary   string
	gotSystem []llm.SystemBlock
	gotMsgs   []llm.MessageParam
	callCount int
}

func (f *fakeCompactionLLM) StreamMessage(ctx context.Context, system []llm.SystemBlock, messages []llm.MessageParam, tools []llm.ToolParam, onDelta func(string), onThinkingDelta func(string)) (*llm.Message, time.Duration, error) {
	f.callCount++
	f.gotSystem = system
	f.gotMsgs = messages
	return &llm.Message{StopReason: llm.StopEndTurn, TextContent: f.summary}, 0, nil
}

func (f *fakeCompactionLLM) StreamMessageWith(ctx context.Context, system []llm.SystemBlock, messages []llm.MessageParam, tools []llm.ToolParam, onDelta func(string), onThinkingDelta func(string), opts llm.StreamOpts) (*llm.Message, time.Duration, error) {
	return f.StreamMessage(ctx, system, messages, tools, onDelta, onThinkingDelta)
}

func (f *fakeCompactionLLM) Provider() llm.ProviderID      { return "anthropic" }
func (f *fakeCompactionLLM) Model() string                 { return "claude-opus-4-8" }
func (f *fakeCompactionLLM) Credential() config.Credential { return config.Credential{} }
func (f *fakeCompactionLLM) MaxTokens() int64              { return 0 }
func (f *fakeCompactionLLM) Effort() string                { return "" }

// newCompactionTestThread builds a Thread wired with a fake LLM, a drained
// event channel, and three turns of history that include a tool_use/tool_result
// pair in the first turn.
//
// messages (8 total), with snapshot boundaries marked:
//
//	0 user "u0"
//	1 assistant tool_use t1        ─┐ turn 0 (snapshot len 4)
//	2 user tool_result t1          ─┤
//	3 assistant "a0"               ─┘
//	4 user "u1"                    ─┐ turn 1 (snapshot len 6)
//	5 assistant "a1"               ─┘
//	6 user "u2"                    ─┐ turn 2 (snapshot len 8)
//	7 assistant "a2"               ─┘
func newCompactionTestThread(t *testing.T, fake *fakeCompactionLLM) (*Thread, chan protocol.ThreadEvent) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := make(chan protocol.ThreadEvent, 64)

	msgs := []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("u0")),
		llm.NewAssistantMessage(llm.NewToolUseBlock("t1", "read_file", map[string]any{"path": "/x"})),
		llm.NewUserMessage(llm.NewToolResultBlock("t1", "contents", false)),
		llm.NewAssistantMessage(llm.NewTextBlock("a0")),
		llm.NewUserMessage(llm.NewTextBlock("u1")),
		llm.NewAssistantMessage(llm.NewTextBlock("a1")),
		llm.NewUserMessage(llm.NewTextBlock("u2")),
		llm.NewAssistantMessage(llm.NewTextBlock("a2")),
	}
	snapshotAt := func(n int) []llm.MessageParam {
		snap := make([]llm.MessageParam, n)
		copy(snap, msgs[:n])
		return snap
	}

	s := &Thread{
		ctx:       ctx,
		eventChan: events,
		llm:       fake,
		model:     "anthropic/claude-opus-4-8",
		messages:  append([]llm.MessageParam(nil), msgs...),
		turnSnapshots: [][]llm.MessageParam{
			snapshotAt(4),
			snapshotAt(6),
			snapshotAt(8),
		},
		lastInputTokens: 1000,
	}
	s.projectConfig.Compaction = Compaction{
		Threshold:      defaultCompactionThreshold,
		Auto:           defaultCompactionAuto,
		KeepLastNTurns: defaultCompactionKeepLastN,
		KeepRatio:      defaultCompactionKeepRatio,
	}
	return s, events
}

func drainCompacted(t *testing.T, events chan protocol.ThreadEvent) protocol.EventCompacted {
	t.Helper()
	for {
		select {
		case ev := <-events:
			if ev.Type == "event.error" {
				t.Fatalf("unexpected error event: %+v", ev.Data)
			}
			if ev.Type == "event.compacted" {
				return ev.Data.(protocol.EventCompacted)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for event.compacted")
		}
	}
}

// assertNoOrphanToolResult fails if any tool_result block lacks a preceding
// tool_use with the same ID earlier in the message slice.
func assertNoOrphanToolResult(t *testing.T, msgs []llm.MessageParam) {
	t.Helper()
	seen := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolUse:
				seen[b.ID] = true
			case llm.BlockToolResult:
				if !seen[b.ToolUseID] {
					t.Fatalf("orphan tool_result for id %q (no preceding tool_use)", b.ToolUseID)
				}
			}
		}
	}
}

func TestResolveCompactionKeep(t *testing.T) {
	cases := []struct {
		name      string
		explicitN int
		keepLastN int
		keepRatio float64
		wantIdx   int
		wantTurns int
		wantOK    bool
	}{
		// ratio 0.25 of 3 turns → keep ceil(0.75)=1, drop 2, boundary = len(snap[1]) = 6
		{"ratio default", 0, -1, 0.25, 6, 2, true},
		// keep_last_n = 1 → drop 2, boundary 6
		{"keep last 1", 0, 1, 0.25, 6, 2, true},
		// keep_last_n = 2 → drop 1, boundary = len(snap[0]) = 4
		{"keep last 2", 0, 2, 0.25, 4, 1, true},
		// keep_last_n >= total → nothing to compact
		{"keep last 3 (all)", 0, 3, 0.25, 0, 0, false},
		// /compact 1 → drop 1, boundary 4
		{"explicit 1", 1, -1, 0.25, 4, 1, true},
		// /compact 2 → drop 2, boundary 6
		{"explicit 2", 2, -1, 0.25, 6, 2, true},
		// /compact 3 leaves no tail → not ok
		{"explicit 3 (no tail)", 3, -1, 0.25, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newCompactionTestThread(t, &fakeCompactionLLM{summary: "S"})
			s.projectConfig.Compaction.KeepLastNTurns = c.keepLastN
			s.projectConfig.Compaction.KeepRatio = c.keepRatio
			idx, turns, ok := s.resolveCompactionKeep(c.explicitN)
			if ok != c.wantOK || idx != c.wantIdx || turns != c.wantTurns {
				t.Errorf("resolveCompactionKeep(%d) = (%d, %d, %v), want (%d, %d, %v)",
					c.explicitN, idx, turns, ok, c.wantIdx, c.wantTurns, c.wantOK)
			}
		})
	}
}

func TestResolveCompactionKeep_NoSnapshots(t *testing.T) {
	s := &Thread{}
	s.projectConfig.Compaction = Compaction{KeepLastNTurns: -1, KeepRatio: 0.25}
	if _, _, ok := s.resolveCompactionKeep(0); ok {
		t.Errorf("expected ok=false with no snapshots")
	}
}

func TestCompactMessages_KeepsToolPairsAndTail(t *testing.T) {
	fake := &fakeCompactionLLM{summary: "SUMMARY-TEXT"}
	s, events := newCompactionTestThread(t, fake)

	// /compact 1: drop turn 0 (which holds the tool pair), keep turns 1 & 2.
	s.compactMessages(4, 1, false)

	ev := drainCompacted(t, events)
	if ev.SummarizedTurns != 1 || ev.Auto {
		t.Errorf("EventCompacted = %+v, want SummarizedTurns=1 Auto=false", ev)
	}
	if ev.FromTokens != 1000 {
		t.Errorf("FromTokens = %d, want 1000", ev.FromTokens)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// New history: [summary, u1, a1, u2, a2] = 5 messages.
	if len(s.messages) != 5 {
		t.Fatalf("messages len = %d, want 5", len(s.messages))
	}
	first := s.messages[0]
	if first.Role != llm.RoleUser || len(first.Content) == 0 || first.Content[0].Text == "" {
		t.Fatalf("first message is not the summary user message: %+v", first)
	}
	if got := first.Content[0].Text; got != compactionSummaryPrefix+"SUMMARY-TEXT" {
		t.Errorf("summary text = %q", got)
	}
	// Tail preserved verbatim.
	if s.messages[1].Content[0].Text != "u1" || s.messages[4].Content[0].Text != "a2" {
		t.Errorf("tail not preserved: %+v", s.messages)
	}
	assertNoOrphanToolResult(t, s.messages)

	// turnSnapshots rebuilt for the 2 kept turns, re-based onto new messages.
	if len(s.turnSnapshots) != 2 {
		t.Fatalf("turnSnapshots len = %d, want 2", len(s.turnSnapshots))
	}
	// kept turn 1: boundary 1 + (6 - 4) = 3 ; kept turn 2: 1 + (8 - 4) = 5
	if len(s.turnSnapshots[0]) != 3 || len(s.turnSnapshots[1]) != 5 {
		t.Errorf("snapshot lengths = [%d, %d], want [3, 5]", len(s.turnSnapshots[0]), len(s.turnSnapshots[1]))
	}
	if s.lastInputTokens != 0 {
		t.Errorf("lastInputTokens = %d, want 0 after compaction", s.lastInputTokens)
	}
	if fake.callCount != 1 {
		t.Errorf("summarization called %d times, want 1", fake.callCount)
	}
	// The dropped prefix (4 messages) plus a trailing user message asking for the
	// summary — the trailing user turn keeps the request from ending on an
	// assistant message (which the API rejects as a prefill).
	if len(fake.gotMsgs) != 5 {
		t.Fatalf("summarized %d messages, want 5 (4 dropped prefix + request prompt)", len(fake.gotMsgs))
	}
	last := fake.gotMsgs[4]
	if last.Role != llm.RoleUser || last.Content[0].Text != compactionRequestPrompt {
		t.Errorf("trailing summarization message = %+v, want user message with compactionRequestPrompt", last)
	}
}

func TestCompactMessages_GuardsInvalidBoundary(t *testing.T) {
	fake := &fakeCompactionLLM{summary: "S"}
	s, events := newCompactionTestThread(t, fake)

	s.compactMessages(0, 0, false) // invalid boundary → error, no LLM call

	select {
	case ev := <-events:
		if ev.Type != "event.error" {
			t.Fatalf("expected event.error, got %q", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("expected an error event")
	}
	if fake.callCount != 0 {
		t.Errorf("summarization should not run on invalid boundary, ran %d times", fake.callCount)
	}
}
