package telemetry

import "fmt"

// TrackTUIStarted records that the TUI or headless client launched.
func TrackTUIStarted(appMode, appVersion string) {
	Track("tui_started", map[string]interface{}{
		"app_mode":    appMode,
		"app_version": appVersion,
	})
}

// TrackDaemonStarted records daemon startup duration.
func TrackDaemonStarted(startupDurationMs int64) {
	Track("daemon_started", map[string]interface{}{
		"startup_duration_ms": startupDurationMs,
	})
}

// TrackPanic reports a recovered panic as a PostHog exception, including the
// recovered value and the goroutine stack. where identifies the recover site
// (e.g. "vix.main", "session.Run"). The raw goroutine stack is attached as the
// go_stack property (authoritative trace) alongside the structured frames.
func TrackPanic(where string, r interface{}, stack []byte) {
	CaptureException("panic", fmt.Sprintf("%s: %v", where, r), map[string]interface{}{
		"go_stack": string(stack),
	})
}
