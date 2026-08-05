package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestEmitStatusMsg_ErrorRoutesToPopup(t *testing.T) {
	m := &Model{}
	cmd := m.emitStatusMsg("boom", StatusMsgError)
	if m.alertPopup != "boom" {
		t.Errorf("alertPopup = %q, want %q", m.alertPopup, "boom")
	}
	if m.statusMsg.Text != "" {
		t.Errorf("error should not set the transient status bar, got %q", m.statusMsg.Text)
	}
	if cmd != nil {
		t.Error("error popup should not schedule an auto-clear timer")
	}
}

func TestEmitStatusMsg_WarningUsesStatusBar(t *testing.T) {
	m := &Model{}
	cmd := m.emitStatusMsg("heads up", StatusMsgWarning)
	if m.alertPopup != "" {
		t.Errorf("warning should not open a popup, got %q", m.alertPopup)
	}
	if m.statusMsg.Text != "heads up" {
		t.Errorf("statusMsg.Text = %q, want %q", m.statusMsg.Text, "heads up")
	}
	if cmd == nil {
		t.Error("warning should schedule an auto-clear timer")
	}
}

func TestRenderAlertDialog(t *testing.T) {
	out := renderAlertDialog(80, 24, NewStyles(true), "something went wrong")
	for _, want := range []string{"Error", "something went wrong", "dismiss"} {
		if !strings.Contains(out, want) {
			t.Errorf("alert dialog missing %q:\n%s", want, out)
		}
	}
}

func TestAlertPopupDismissedOnKey(t *testing.T) {
	m := Model{alertPopup: "boom", width: 80, height: 24}
	model, _ := m.updateInner(tea.KeyPressMsg{Code: 'a', Text: "a"})
	got, ok := model.(Model)
	if !ok {
		t.Fatalf("updateInner returned %T, want Model", model)
	}
	if got.alertPopup != "" {
		t.Errorf("alertPopup = %q, want cleared after key press", got.alertPopup)
	}
}
