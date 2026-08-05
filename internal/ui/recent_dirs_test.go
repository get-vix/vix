package ui

import (
	"testing"

	"github.com/get-vix/vix/internal/protocol"
)

func TestTopRecentDirs_TrimsToFive(t *testing.T) {
	m := &Model{recentDirs: []protocol.DirUsage{
		{Path: "/1"}, {Path: "/2"}, {Path: "/3"},
		{Path: "/4"}, {Path: "/5"}, {Path: "/6"}, {Path: "/7"},
	}}
	if got := len(m.topRecentDirs()); got != maxRecentDirs {
		t.Errorf("topRecentDirs should trim to %d, got %d", maxRecentDirs, got)
	}
}

func TestLatestWorkDir_MostRecentThenFallback(t *testing.T) {
	// Ranked by count, but latest-used is picked by recency (LastRequestAt).
	m := &Model{
		cwd: "/launch",
		recentDirs: []protocol.DirUsage{
			{Path: "/most-used", Count: 5, LastRequestAt: "2024-01-01T00:00:00Z"},
			{Path: "/most-recent", Count: 1, LastRequestAt: "2024-06-01T00:00:00Z"},
		},
	}
	if got := m.latestWorkDir(); got != "/most-recent" {
		t.Errorf("latestWorkDir should pick most recent activity, got %q", got)
	}

	empty := &Model{cwd: "/launch"}
	if got := empty.latestWorkDir(); got != "/launch" {
		t.Errorf("latestWorkDir should fall back to launch cwd, got %q", got)
	}
}

func draftForNav() *ThreadState {
	return &ThreadState{focus: FocusChat, phase: phaseDraft}
}

func TestWelcomeDirNav_UpDownClampAndEnterApplies(t *testing.T) {
	m := &Model{recentDirs: []protocol.DirUsage{
		{Path: "/a"}, {Path: "/b"}, {Path: "/c"},
	}}
	sess := draftForNav()

	// Up at the top stays at 0.
	if !m.welcomeDirNav(sess, "up") || sess.recentDirSelected != 0 {
		t.Fatalf("up at top should clamp to 0, got %d", sess.recentDirSelected)
	}
	// Down moves through the list and clamps at the end.
	m.welcomeDirNav(sess, "down")
	m.welcomeDirNav(sess, "down")
	if sess.recentDirSelected != 2 {
		t.Fatalf("two downs should reach index 2, got %d", sess.recentDirSelected)
	}
	if !m.welcomeDirNav(sess, "down") || sess.recentDirSelected != 2 {
		t.Fatalf("down at bottom should clamp to last, got %d", sess.recentDirSelected)
	}
	// Enter applies the selected directory to workDir.
	if !m.welcomeDirNav(sess, "enter") || sess.workDir != "/c" {
		t.Fatalf("enter should apply selected dir, got workDir=%q", sess.workDir)
	}
}

func TestWelcomeDirNav_IgnoredWhenNotFocusedDraft(t *testing.T) {
	m := &Model{recentDirs: []protocol.DirUsage{{Path: "/a"}}}

	// Not focused on the welcome area.
	blurred := &ThreadState{focus: FocusEditor, phase: phaseDraft}
	if m.welcomeDirNav(blurred, "down") {
		t.Error("nav should be ignored when the editor is focused")
	}

	// Live thread (not a draft).
	live := &ThreadState{focus: FocusChat, phase: phaseLive}
	if m.welcomeDirNav(live, "down") {
		t.Error("nav should be ignored for a live thread")
	}

	// Draft with a non-empty transcript (welcome not showing).
	started := &ThreadState{focus: FocusChat, phase: phaseDraft, chatMessages: []ChatMessage{{}}}
	if m.welcomeDirNav(started, "down") {
		t.Error("nav should be ignored once the transcript is non-empty")
	}

	// No recent dirs.
	none := &Model{}
	if none.welcomeDirNav(draftForNav(), "down") {
		t.Error("nav should be ignored when there are no recent dirs")
	}
}
