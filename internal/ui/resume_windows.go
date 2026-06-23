//go:build windows

package ui

import "charm.land/bubbletea/v2"

// waitForResume blocks forever on Windows. There is no Ctrl-Z job-control
// suspend (SIGTSTP/SIGCONT) on Windows consoles, so the resume-after-suspend
// path is never entered. This stub keeps the package compiling; behaviour on
// Unix is unchanged.
func waitForResume() tea.Msg {
	select {}
}
