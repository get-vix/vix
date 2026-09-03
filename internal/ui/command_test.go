package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseTurnArg(t *testing.T) {
	cases := []struct {
		fields []string
		want   int
		ok     bool
	}{
		{[]string{"/fork", "4"}, 4, true},
		{[]string{"/trim", "1"}, 1, true},
		{[]string{"/copy"}, 0, false},
		{[]string{"/fork", "0"}, 0, false},
		{[]string{"/fork", "-3"}, 0, false},
		{[]string{"/fork", "abc"}, 0, false},
		{[]string{"/fork", "4", "extra"}, 4, true},
	}
	for _, c := range cases {
		got, ok := parseTurnArg(c.fields)
		if got != c.want || ok != c.ok {
			t.Errorf("parseTurnArg(%v) = (%d, %v), want (%d, %v)", c.fields, got, ok, c.want, c.ok)
		}
	}
}

func TestSlashCommandInsertText(t *testing.T) {
	cases := []struct {
		action string
		want   string
		ok     bool
	}{
		{"slash_fork", "/fork ", true},
		{"slash_trim", "/trim ", true},
		{"slash_copy", "/copy ", true},
		{"slash_goto", "/goto ", true},
		{"slash_clear", "", false},
		{"slash_skills", "", false},
		{"copy_conversation", "", false},
	}
	for _, c := range cases {
		got, ok := slashCommandInsertText(c.action)
		if got != c.want || ok != c.ok {
			t.Errorf("slashCommandInsertText(%q) = (%q, %v), want (%q, %v)", c.action, got, ok, c.want, c.ok)
		}
	}
}

func TestBuildRows(t *testing.T) {
	cmds := []Command{
		{Name: "fork", Action: "slash_fork", Group: "Conversation"},
		{Name: "clear", Action: "slash_clear", Group: "Conversation"},
		{Name: "skills", Action: "slash_skills", Group: "Skills"},
		{Name: "commit", Action: "slash_skill:commit", Group: "Skills"},
		{Name: "loose", Action: "slash_loose"}, // no group -> trailing, no header
	}
	rows := buildRows(cmds)

	// Expected sequence: "Conversation" header, fork, clear, "Skills" header,
	// skills, commit, then the ungrouped loose command (no header).
	type exp struct {
		header   string
		cmdIndex int
	}
	want := []exp{
		{"Conversation", -1},
		{"", 0},
		{"", 1},
		{"Skills", -1},
		{"", 2},
		{"", 3},
		{"", 4},
	}
	if len(rows) != len(want) {
		t.Fatalf("buildRows len = %d, want %d (%+v)", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i].header != w.header || rows[i].cmdIndex != w.cmdIndex {
			t.Errorf("row %d = {header:%q cmdIndex:%d}, want {header:%q cmdIndex:%d}",
				i, rows[i].header, rows[i].cmdIndex, w.header, w.cmdIndex)
		}
	}
}

func TestBuildRowsSkipsEmptyGroupHeaders(t *testing.T) {
	// Only Skills commands present: the Conversation header must not appear.
	cmds := []Command{
		{Name: "skills", Action: "slash_skills", Group: "Skills"},
		{Name: "commit", Action: "slash_skill:commit", Group: "Skills"},
	}
	rows := buildRows(cmds)
	if len(rows) == 0 || rows[0].header != "Skills" {
		t.Fatalf("expected first row to be the Skills header, got %+v", rows)
	}
	for _, r := range rows {
		if r.header == "Conversation" {
			t.Errorf("Conversation header should be omitted when its group is empty")
		}
	}
}

func TestSlashMenuSelectedActionWithGroups(t *testing.T) {
	cmds := []Command{
		{Name: "fork", Action: "slash_fork", Group: "Conversation"},
		{Name: "skills", Action: "slash_skills", Group: "Skills"},
	}
	var m SlashMenu
	m.Open(cmds, "")
	if got := m.SelectedAction(); got != "slash_fork" {
		t.Errorf("initial SelectedAction = %q, want slash_fork", got)
	}
	m.MoveDown()
	if got := m.SelectedAction(); got != "slash_skills" {
		t.Errorf("after MoveDown SelectedAction = %q, want slash_skills", got)
	}
}

func TestCountTurnSeparators(t *testing.T) {
	msgs := []ChatMessage{
		{Type: MsgUser, Text: "hi"},
		{Type: MsgSystem, TurnModel: "m"},
		{Type: MsgAssistant, Text: "a"},
		{Type: MsgSystem, TurnModel: "m"},
		{Type: MsgSystem, Text: "not a turn sep"}, // no TurnModel
	}
	if got := countTurnSeparators(msgs); got != 2 {
		t.Errorf("countTurnSeparators = %d, want 2", got)
	}
	if got := countTurnSeparators(nil); got != 0 {
		t.Errorf("countTurnSeparators(nil) = %d, want 0", got)
	}
}

func TestTurnSepByNumber(t *testing.T) {
	m := Model{
		styles:     NewStyles(true),
		mdRenderer: NewMarkdownRenderer(80, true, NewStyles(true).CodeBoxBorderStyle),
	}
	sess := &ThreadState{
		chatMessages: []ChatMessage{
			{Type: MsgUser, Text: "hi", Rendered: "hi\n"},
			{Type: MsgAssistant, Text: "a", Rendered: "a\n"},
			{Type: MsgSystem, TurnModel: "m", Rendered: "sep0\n"}, // turn 1, idx 2
			{Type: MsgUser, Text: "again", Rendered: "again\n"},
			{Type: MsgSystem, TurnModel: "m", Rendered: "sep1\n"}, // turn 2, idx 4
		},
	}

	sep, ok := m.turnSepByNumber(sess, 1)
	if !ok || sep.TurnIdx != 0 || sep.MsgIdx != 2 {
		t.Errorf("turnSepByNumber(1) = (%+v, %v), want TurnIdx=0 MsgIdx=2 ok=true", sep, ok)
	}
	sep, ok = m.turnSepByNumber(sess, 2)
	if !ok || sep.TurnIdx != 1 || sep.MsgIdx != 4 {
		t.Errorf("turnSepByNumber(2) = (%+v, %v), want TurnIdx=1 MsgIdx=4 ok=true", sep, ok)
	}
	if _, ok := m.turnSepByNumber(sess, 3); ok {
		t.Errorf("turnSepByNumber(3) returned ok=true, want false")
	}
	if _, ok := m.turnSepByNumber(sess, 0); ok {
		t.Errorf("turnSepByNumber(0) returned ok=true, want false")
	}
}

func TestGotoTurn(t *testing.T) {
	s := NewStyles(true)
	m := Model{
		width:      120,
		height:     16,
		styles:     s,
		mdRenderer: NewMarkdownRenderer(116, true, s.CodeBoxBorderStyle),
	}

	var msgs []ChatMessage
	for i := 1; i <= 8; i++ {
		msgs = append(msgs,
			ChatMessage{Type: MsgUser, Rendered: fmt.Sprintf("u%d\n", i)},
			ChatMessage{Type: MsgAssistant, Rendered: fmt.Sprintf("a%d\n", i)},
			ChatMessage{Type: MsgSystem, TurnModel: "m", Rendered: fmt.Sprintf("sep%d\n", i)},
		)
	}
	sess := &ThreadState{chatMessages: msgs}

	// Independently recompute the rendered layout to find which logical line
	// lands at the top of the viewport after gotoTurn.
	innerWidth := m.effectiveChatWidth() - 4
	allLines := strings.Split(buildRenderedChat(sess.chatMessages, s, innerWidth), "\n")
	visualRowStart := make([]int, len(allLines)+1)
	for i, line := range allLines {
		visualRowStart[i+1] = visualRowStart[i] + visualRows(line, innerWidth)
	}
	totalVisualRows := visualRowStart[len(allLines)]
	contentHeight := computeLayout(m.width, m.height, m.visualLineCount()).ChatHeight - 1

	m.gotoTurn(sess, 2)

	topVisRow := totalVisualRows - sess.chatScrollOffset - contentHeight
	topLine := 0
	for topLine < len(allLines) && visualRowStart[topLine+1] <= topVisRow {
		topLine++
	}
	if got := allLines[topLine]; got != "u2" {
		t.Errorf("gotoTurn(2) top line = %q, want %q (offset=%d)", got, "u2", sess.chatScrollOffset)
	}
	if sess.focus != FocusChat {
		t.Errorf("gotoTurn should focus chat, got %v", sess.focus)
	}

	// Turn 1 always starts at the very top of the conversation.
	m.gotoTurn(sess, 1)
	if got := m.threadMaxScrollOffset(sess); sess.chatScrollOffset != got {
		t.Errorf("gotoTurn(1) offset = %d, want max %d", sess.chatScrollOffset, got)
	}
}

func TestRenderTurnInfo_WideShowsActions(t *testing.T) {
	s := NewStyles(true)
	received := time.Date(2025, 1, 2, 15, 4, 0, 0, time.UTC)
	msg := renderTurnInfo("anthropic/claude-sonnet-4-6", 59*time.Second, 0.23, 4, received, 200, s)
	plain := ansiRe.ReplaceAllString(msg.Rendered, "")

	for _, want := range []string{"Turn #4", "From here:", "/fork", "/trim", "/copy", "59s", "$0.23", "Jan 2, 2025", "3:04 PM"} {
		if !strings.Contains(plain, want) {
			t.Errorf("wide separator missing %q in %q", want, plain)
		}
	}
}

func TestRenderTurnInfo_NarrowDropsRightZone(t *testing.T) {
	s := NewStyles(true)
	received := time.Date(2025, 1, 2, 15, 4, 0, 0, time.UTC)
	msg := renderTurnInfo("anthropic/claude-sonnet-4-6", 59*time.Second, 0.23, 4, received, 14, s)
	plain := ansiRe.ReplaceAllString(msg.Rendered, "")

	if strings.Contains(plain, "Turn #") {
		t.Errorf("narrow separator should drop the right zone, got %q", plain)
	}
}

func TestRenderTurnInfo_ZeroTurnNumHasNoRightZone(t *testing.T) {
	s := NewStyles(true)
	received := time.Date(2025, 1, 2, 15, 4, 0, 0, time.UTC)
	msg := renderTurnInfo("anthropic/claude-sonnet-4-6", 59*time.Second, 0.23, 0, received, 200, s)
	plain := ansiRe.ReplaceAllString(msg.Rendered, "")

	if strings.Contains(plain, "Turn #") {
		t.Errorf("turnNum=0 should produce no right zone, got %q", plain)
	}
}

func TestRenderTurnInfo_ShowsReceivedDateTime(t *testing.T) {
	s := NewStyles(true)
	received := time.Date(2025, 3, 9, 9, 7, 0, 0, time.UTC)
	msg := renderTurnInfo("anthropic/claude-sonnet-4-6", 59*time.Second, 0.23, 4, received, 200, s)
	plain := ansiRe.ReplaceAllString(msg.Rendered, "")

	if !strings.Contains(plain, "Mar 9, 2025 · 9:07 AM") {
		t.Errorf("separator missing received date+time in %q", plain)
	}
	if !msg.TurnReceived.Equal(received) {
		t.Errorf("TurnReceived = %v, want %v", msg.TurnReceived, received)
	}
}

func TestRenderTurnInfo_ZeroReceivedOmitsDateTime(t *testing.T) {
	s := NewStyles(true)
	msg := renderTurnInfo("anthropic/claude-sonnet-4-6", 59*time.Second, 0.23, 4, time.Time{}, 200, s)
	plain := ansiRe.ReplaceAllString(msg.Rendered, "")

	if strings.Contains(plain, " · Jan") || strings.Contains(plain, "AM") || strings.Contains(plain, "PM") {
		t.Errorf("zero received time should omit the date+time segment, got %q", plain)
	}
}

func TestRerenderTurnInfoPreservesReceived(t *testing.T) {
	received := time.Date(2025, 3, 9, 9, 7, 0, 0, time.UTC)
	orig := renderTurnInfo("anthropic/claude-sonnet-4-6", 59*time.Second, 0.23, 4, received, 200, NewStyles(true))

	got := orig.rerender(nil, NewStyles(true), 200)
	if !got.TurnReceived.Equal(received) {
		t.Errorf("rerender TurnReceived = %v, want %v", got.TurnReceived, received)
	}
	plain := ansiRe.ReplaceAllString(got.Rendered, "")
	if !strings.Contains(plain, "Mar 9, 2025 · 9:07 AM") {
		t.Errorf("rerender must keep the received date+time; got %q", plain)
	}
}
