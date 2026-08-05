package harness

import (
	"strings"
	"time"
)

// The interactive TUI surfaces tool approvals as a "Permission" question panel
// (see internal/ui/questionpanel.go) with the allow option selected by default
// and navigated via up/down + Enter. These helpers detect that panel and drive
// the right keys, so scenarios don't hang when a tool needs confirmation.

// IsToolPrompt reports whether a tool-permission panel is currently on screen.
func (u *UI) IsToolPrompt() bool {
	s := u.Snapshot()
	if !strings.Contains(s, "Permission") {
		return false
	}
	l := strings.ToLower(s)
	return strings.Contains(l, "allow") || strings.Contains(l, "deny")
}

// ApproveToolPrompt approves the visible permission prompt. It presses up
// repeatedly first (clamped at the top) so the first/allow option is selected
// regardless of prior cursor position, then Enter.
func (u *UI) ApproveToolPrompt() {
	for i := 0; i < 3; i++ {
		u.Key("up")
	}
	u.Enter()
}

// DenyToolPrompt denies the visible permission prompt by moving to the last
// option (Deny / No, deny — clamped at the bottom) and pressing Enter.
func (u *UI) DenyToolPrompt() {
	for i := 0; i < 3; i++ {
		u.Key("down")
	}
	u.Enter()
}

// ResolveToolPrompts waits until doneSubstr appears on screen, auto-approving
// any tool-permission prompts that pop up in the meantime. Use it instead of a
// bare WaitFor whenever the scripted turns may trigger confirmations. Bounded
// by the per-test deadline (fails with an auto-dump on expiry).
func (u *UI) ResolveToolPrompts(doneSubstr string) {
	u.h.t.Helper()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if u.Contains(doneSubstr) {
			return
		}
		if u.IsToolPrompt() {
			u.ApproveToolPrompt()
			u.waitPromptCleared()
			continue
		}
		select {
		case <-u.h.ctx.Done():
			u.h.t.Fatalf("e2e: timed out waiting for %q (auto-approving prompts)", doneSubstr)
		case <-tick.C:
		}
	}
}

// waitPromptCleared blocks briefly until the permission panel disappears, so we
// don't double-act on the same prompt.
func (u *UI) waitPromptCleared() {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !u.IsToolPrompt() {
			return
		}
		select {
		case <-u.h.ctx.Done():
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
}
