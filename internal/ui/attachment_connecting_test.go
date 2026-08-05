package ui

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// While a thread is still connecting (client == nil), a dropped text/PDF file
// can't be validated by the daemon, so it's added to the attachment panel
// optimistically and re-validated at send time.
func TestPaste_OptimisticChipWhileConnecting(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "book.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.7 stub"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewStyles(true)
	sess := &ThreadState{
		agentState: StateWaitingForInput,
		input:      newInput(),
		history:    NewHistory(t.TempDir()),
		phase:      phaseDraft,
		// client stays nil: the thread hasn't connected yet.
	}
	sess.input.SetWidth(76)
	m := Model{
		activeTab:      TabKindChat,
		selectedThread: 0,
		threads:        []*ThreadState{sess},
		styles:         s,
		mdRenderer:     NewMarkdownRenderer(80, true, s.CodeBoxBorderStyle),
		width:          80,
		height:         24,
	}

	model, _ := m.updateInner(tea.PasteMsg{Content: pdf})
	got, ok := model.(Model)
	if !ok {
		t.Fatalf("updateInner returned %T, want Model", model)
	}
	dropped := got.threads[0]
	if dropped.attachmentPanel.Count() != 1 {
		t.Fatalf("expected 1 optimistic chip, got %d", dropped.attachmentPanel.Count())
	}
	atts := dropped.attachmentPanel.Clear()
	if atts[0].Type != "file" || atts[0].Path != pdf {
		t.Errorf("chip = %+v, want file %q", atts[0], pdf)
	}
	// The path is stripped from the input so it isn't also sent as text.
	if v := dropped.input.Value(); v != "" {
		t.Errorf("input.Value() = %q, want empty after chip added", v)
	}
}
