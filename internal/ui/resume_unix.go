//go:build !windows

package ui

import (
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
)

func waitForResume() tea.Msg {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGCONT)
	<-sigCh
	signal.Stop(sigCh)
	return resumeFromSleepMsg{}
}
