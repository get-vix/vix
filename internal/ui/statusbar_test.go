package ui

import (
	"strings"
	"testing"
	"time"
)

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1k"},
		{1500, "1k"}, // integer truncation, not rounding
		{125000, "125k"},
	}
	for _, tt := range tests {
		got := formatTokenCount(tt.n)
		if got != tt.want {
			t.Errorf("formatTokenCount(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00"},
		{30 * time.Second, "00:30"},
		{5*time.Minute + 30*time.Second, "05:30"},
		{1 * time.Hour, "01:00:00"},
		{1*time.Hour + 5*time.Minute + 30*time.Second, "01:05:30"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestRenderStatusBar_DraftState(t *testing.T) {
	s := NewStyles(true)
	out := renderStatusBar(120, false, false, true, StatusMessage{}, s, TabKindChat, FocusEditor, 0, 0)
	if !strings.Contains(out, "Draft") {
		t.Errorf("draft status bar should show a Draft indicator, got:\n%s", out)
	}
}

func TestRenderStatusBar_ConnectedBeatsDraft(t *testing.T) {
	s := NewStyles(true)
	out := renderStatusBar(120, true, false, true, StatusMessage{}, s, TabKindChat, FocusEditor, 0, 0)
	if !strings.Contains(out, "Connected") {
		t.Errorf("connected should take precedence over draft, got:\n%s", out)
	}
}

func TestRenderStatusBar_Disconnected(t *testing.T) {
	s := NewStyles(true)
	out := renderStatusBar(120, false, false, false, StatusMessage{}, s, TabKindChat, FocusEditor, 0, 0)
	if !strings.Contains(out, "Disconnected") {
		t.Errorf("no connection and not a draft should show Disconnected, got:\n%s", out)
	}
}
