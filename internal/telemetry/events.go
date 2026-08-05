package telemetry

import "fmt"

// TrackTUIStarted records that the TUI or headless client launched.
func TrackTUIStarted(appMode, appVersion string) {
	Track("tui_started", map[string]interface{}{
		"app_mode":    appMode,
		"app_version": appVersion,
	})
}

// TrackTUIEnded records that the client is shutting down, with the duration of
// the current session. Idempotent (endOnce): the client fires it from a defer
// that may also run while a panic unwinds, but it should be emitted at most once.
func TrackTUIEnded() {
	endOnce.Do(func() {
		Track("tui_ended", map[string]interface{}{
			"session_duration_seconds": sessionDurationSeconds(),
		})
	})
}

// TrackTurn records a genuine user turn (a prompt or workflow submission),
// tagged with the model handling it. It gives PostHog an activity signal between
// tui_started and tui_ended, and keeps the session ID from rotating mid-use.
// The model is fixed for the duration of a turn, so capturing it here records
// which model was used for that turn.
func TrackTurn(model string) {
	Track("turn_sent", map[string]interface{}{
		"model": model,
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
