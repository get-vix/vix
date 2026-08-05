package ui

import (
	"strings"
	"testing"
)

// maxStrippedLineWidth returns the widest line in s after stripping ANSI escapes.
func maxStrippedLineWidth(s string) int {
	max := 0
	for _, l := range strings.Split(s, "\n") {
		if w := len(ansiRe.ReplaceAllString(l, "")); w > max {
			max = w
		}
	}
	return max
}

// TestRenderDiffDetailWidthShrinks asserts the side-by-side diff renders
// narrower when given a smaller (panel-aware) width — the core of the overflow
// fix. renderDiffDetail is pure, so no Model is needed.
func TestRenderDiffDetailWidthShrinks(t *testing.T) {
	long := strings.Repeat("x", 80)
	detail := "H Added 1 line, removed 1 line\n" +
		"R 2 old_" + long + "\n" +
		"A 2 new_" + long + "\n"

	full := renderDiffDetail(detail, NewStyles(true), 120)
	// 120 total minus a panel of panelWidth(42) columns.
	withPanel := renderDiffDetail(detail, NewStyles(true), 120-panelWidth)

	wFull := maxStrippedLineWidth(full)
	wPanel := maxStrippedLineWidth(withPanel)

	if wPanel >= wFull {
		t.Errorf("expected diff to shrink with panel open: full=%d withPanel=%d", wFull, wPanel)
	}
	// The shrink should track the reserved panel columns (allow rounding slack).
	if diff := wFull - wPanel; diff < panelWidth-4 {
		t.Errorf("expected shrink of ~%d cols, got %d (full=%d withPanel=%d)", panelWidth, diff, wFull, wPanel)
	}
}

// newWidthTestModel builds a minimal Model with one thread at the given width.
func newWidthTestModel(width int) *Model {
	m := &Model{
		width:      width,
		height:     40,
		styles:     NewStyles(true),
		mdRenderer: NewMarkdownRenderer(width-4, true, NewStyles(true).CodeBoxBorderStyle),
		testMode:   true,
	}
	m.threads = []*ThreadState{{}}
	m.selectedThread = 0
	m.lastChatWidth = m.effectiveChatWidth()
	return m
}

// TestReconcileChatWidthShrinksWhenPanelOpens verifies that opening the right
// panel and reconciling re-flows the renderer to the panel-aware width.
func TestReconcileChatWidthShrinksWhenPanelOpens(t *testing.T) {
	m := newWidthTestModel(120)

	if got, want := m.mdRenderer.width, 120-4; got != want {
		t.Fatalf("initial renderer width = %d, want %d", got, want)
	}

	// Open the panel (per-thread) without manually syncing width.
	m.threads[0].rightPanel.visible = true

	m.reconcileChatWidth()

	wantInner := (120 - panelWidth) - 4
	if got := m.mdRenderer.width; got != wantInner {
		t.Errorf("renderer width after reconcile = %d, want %d", got, wantInner)
	}
	if got := m.lastChatWidth; got != 120-panelWidth {
		t.Errorf("lastChatWidth = %d, want %d", got, 120-panelWidth)
	}
}

// TestReconcileChatWidthNoopWhenUnchanged verifies reconcile is a no-op (no
// re-flow) when the effective width has not changed.
func TestReconcileChatWidthNoopWhenUnchanged(t *testing.T) {
	m := newWidthTestModel(100)
	before := m.mdRenderer.width
	m.reconcileChatWidth()
	if got := m.mdRenderer.width; got != before {
		t.Errorf("renderer width changed on no-op reconcile: got %d, want %d", got, before)
	}
}

// TestReconcileChatWidthOnThreadSwitch verifies switching to a thread whose
// panel is open reduces the rendered width even though the renderer is global.
func TestReconcileChatWidthOnThreadSwitch(t *testing.T) {
	m := newWidthTestModel(120)
	// Second thread has its panel open.
	panelSess := &ThreadState{}
	panelSess.rightPanel.visible = true
	m.threads = append(m.threads, panelSess)

	// Switch to it and reconcile (as the central Update hook would).
	m.selectedThread = 1
	m.reconcileChatWidth()

	wantInner := (120 - panelWidth) - 4
	if got := m.mdRenderer.width; got != wantInner {
		t.Errorf("renderer width after thread switch = %d, want %d", got, wantInner)
	}

	// Switching back to the panel-less thread restores full width.
	m.selectedThread = 0
	m.reconcileChatWidth()
	if got := m.mdRenderer.width; got != 120-4 {
		t.Errorf("renderer width after switching back = %d, want %d", got, 120-4)
	}
}
