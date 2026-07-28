//go:build windows

package ui

import tea "charm.land/bubbletea/v2"

// Windows has no SIGCONT equivalent. There is no suspend/resume signal to
// wait for, so let Bubble Tea continue immediately.
func waitForResume() tea.Msg {
	return resumeFromSleepMsg{}
}
