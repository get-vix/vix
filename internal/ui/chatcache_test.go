package ui

import (
	"strings"
	"testing"
)

// expectedLinesAndRows recomputes lines + visual-row prefix sums the way the
// pre-cache renderer did: full string concat, split, and per-line visualRows.
func expectedLinesAndRows(messages []ChatMessage, s Styles, width int, tail string) ([]string, []int) {
	content := buildRenderedChat(messages, s, width) + tail
	lines := strings.Split(content, "\n")
	rowStart := make([]int, len(lines)+1)
	for i, line := range lines {
		rowStart[i+1] = rowStart[i] + visualRows(line, width)
	}
	return lines, rowStart
}

func TestCombineTailEquivalence(t *testing.T) {
	s := NewStyles(true)
	const width = 40
	msgs := []ChatMessage{
		{Type: MsgUser, Rendered: "hello\n"},
		{Type: MsgAssistant, Rendered: "a line that is long enough to wrap past forty columns of inner width\n"},
		{Type: MsgSystem, Rendered: "sys\n"},
	}
	lines := strings.Split(buildRenderedChat(msgs, s, width), "\n")
	rowStart := visualRowPrefix(lines, width)

	tails := []string{
		"",
		"  ⠋⠙⠹\n",
		"thinking text\nstreaming chunk that also wraps well beyond the forty column inner width limit\n",
		"no trailing newline",
	}
	for _, tail := range tails {
		gotLines, gotRows := combineTail(lines, rowStart, tail, width)
		wantLines, wantRows := expectedLinesAndRows(msgs, s, width, tail)
		if len(gotLines) != len(wantLines) {
			t.Fatalf("tail %q: got %d lines, want %d", tail, len(gotLines), len(wantLines))
		}
		for i := range gotLines {
			if gotLines[i] != wantLines[i] {
				t.Errorf("tail %q: line %d = %q, want %q", tail, i, gotLines[i], wantLines[i])
			}
		}
		if len(gotRows) != len(wantRows) {
			t.Fatalf("tail %q: got %d rowStart entries, want %d", tail, len(gotRows), len(wantRows))
		}
		for i := range gotRows {
			if gotRows[i] != wantRows[i] {
				t.Errorf("tail %q: rowStart[%d] = %d, want %d", tail, i, gotRows[i], wantRows[i])
			}
		}
	}
}

func TestCombineTailDoesNotMutateCache(t *testing.T) {
	lines := []string{"a", "b", ""}
	rowStart := visualRowPrefix(lines, 40)
	combineTail(lines, rowStart, "tail\nmore\n", 40)
	if lines[2] != "" {
		t.Errorf("combineTail mutated cached lines: %q", lines[2])
	}
}

func TestCachedChatLines(t *testing.T) {
	s := NewStyles(true)
	sess := &SessionState{chatMessages: []ChatMessage{{Type: MsgUser, Rendered: "u1\n"}}}

	lines, _ := sess.cachedChatLines(s, 40)
	if lines[0] != "u1" {
		t.Fatalf("initial render = %q, want %q", lines[0], "u1")
	}

	// In-place mutation without invalidate: cache hit returns stale content,
	// proving the second call did not rebuild.
	sess.chatMessages[0].Rendered = "changed\n"
	lines, _ = sess.cachedChatLines(s, 40)
	if lines[0] != "u1" {
		t.Errorf("expected cache hit (stale %q), got rebuild: %q", "u1", lines[0])
	}

	// invalidate() forces a rebuild.
	sess.chatCache.invalidate()
	lines, _ = sess.cachedChatLines(s, 40)
	if lines[0] != "changed" {
		t.Errorf("after invalidate: line = %q, want %q", lines[0], "changed")
	}

	// Appending a message changes len(chatMessages) and triggers a rebuild.
	sess.chatMessages = append(sess.chatMessages, ChatMessage{Type: MsgAssistant, Rendered: "a1\n"})
	lines, rowStart := sess.cachedChatLines(s, 40)
	if len(lines) != 3 || lines[1] != "a1" {
		t.Errorf("after append: lines = %q, want [changed a1 \"\"]", lines)
	}
	if rowStart[len(lines)] != 3 {
		t.Errorf("after append: total rows = %d, want 3", rowStart[len(lines)])
	}

	// A width change triggers a rebuild at the new width.
	sess.chatMessages[0].Rendered = "wide\n"
	lines, _ = sess.cachedChatLines(s, 20)
	if lines[0] != "wide" {
		t.Errorf("after width change: line = %q, want %q (rebuild expected)", lines[0], "wide")
	}
}
