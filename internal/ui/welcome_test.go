package ui

import (
	"strings"
	"testing"

	"github.com/get-vix/vix/internal/protocol"
)

func TestRenderWelcomeInline_DraftShowsWorkDirAndHint(t *testing.T) {
	s := NewStyles(true)
	out := renderWelcomeInline(120, 40, s, "/tmp/project", true, nil, -1)
	if !strings.Contains(out, "/tmp/project") {
		t.Error("welcome should show the working directory")
	}
	if !strings.Contains(out, "Ctrl+o") {
		t.Error("draft welcome should advertise Ctrl+o to change directory")
	}
}

func TestRenderWelcomeInline_LiveOmitsChangeHint(t *testing.T) {
	s := NewStyles(true)
	out := renderWelcomeInline(120, 40, s, "/tmp/project", false, nil, -1)
	if !strings.Contains(out, "/tmp/project") {
		t.Error("welcome should still show the working directory when not a draft")
	}
	if strings.Contains(out, "your first message starts the thread here") {
		t.Error("non-draft welcome should not show the draft start note")
	}
}

// The welcome screen advertises only the working-directory picker shortcut; the
// rest of the keymap lives in the docs.
func TestRenderWelcomeInline_ShortcutsTrimmedToCtrlO(t *testing.T) {
	s := NewStyles(true)
	out := renderWelcomeInline(120, 40, s, "/tmp/project", true, nil, -1)
	if !strings.Contains(out, "Ctrl+o") {
		t.Error("welcome should advertise Ctrl+o")
	}
	for _, removed := range []string{"Shift+Tab", "Ctrl+N", "Ctrl+P", "Ctrl+R", "Cycle mode", "Search history"} {
		if strings.Contains(out, removed) {
			t.Errorf("welcome should no longer advertise %q", removed)
		}
	}
}

// A draft welcome lists the recent working directories between the working
// directory row (top) and the shortcuts (bottom).
func TestRenderWelcomeInline_RecentDirsListed(t *testing.T) {
	s := NewStyles(true)
	recent := []protocol.DirUsage{
		{Path: "/home/alice/projA", Count: 3},
		{Path: "/home/alice/projB", Count: 1},
	}
	out := renderWelcomeInline(160, 40, s, "/home/alice/projA", true, recent, -1)
	if !strings.Contains(out, "/home/alice/projB") {
		t.Error("welcome should list recent directories")
	}
	if !strings.Contains(out, "Recent") {
		t.Error("welcome should label the recent-directories section")
	}
	// Vertical order: working directory, then recent list, then shortcuts.
	wd := strings.Index(out, "Working directory")
	recentIdx := strings.Index(out, "Recent")
	sc := strings.Index(out, "Ctrl+o")
	if !(wd >= 0 && recentIdx >= 0 && sc >= 0 && wd < recentIdx && recentIdx < sc) {
		t.Errorf("welcome order should be working dir < recent < shortcuts; got wd=%d recent=%d shortcut=%d", wd, recentIdx, sc)
	}
}

// A non-draft welcome never shows the recent-directories list even if provided.
func TestRenderWelcomeInline_RecentDirsDraftOnly(t *testing.T) {
	s := NewStyles(true)
	recent := []protocol.DirUsage{{Path: "/home/alice/projB", Count: 1}}
	out := renderWelcomeInline(160, 40, s, "/home/alice/projA", false, recent, -1)
	if strings.Contains(out, "/home/alice/projB") {
		t.Error("non-draft welcome should not list recent directories")
	}
}
