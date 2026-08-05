package ui

import (
	"os"
	"time"
)

// testRenderMode is enabled by setting VIX_TEST_RENDER=1. When on, the TUI
// replaces every wall-clock- and animation-derived value in its rendered
// output with a fixed, deterministic placeholder, so end-to-end screenshot
// tests (see the e2e/ module) produce byte-stable frames.
//
// This must NEVER be set in production — it freezes spinners and elapsed-time
// readouts, which would make the live UI look stuck. It is read once at
// startup; the e2e harness sets it in the `vix` process environment.
var testRenderMode = os.Getenv("VIX_TEST_RENDER") == "1"

// frozenClock is the fixed wall-clock instant rendered while testRenderMode is
// on (e.g. the "Sent at …" timestamp on user messages).
var frozenClock = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

// renderNow returns the current time, or the frozen instant in test-render
// mode. Use it for any time.Now() whose value reaches the rendered frame.
func renderNow() time.Time {
	if testRenderMode {
		return frozenClock
	}
	return time.Now()
}

// renderSince returns the elapsed duration since t, or zero in test-render
// mode. Use it for any time.Since() whose value reaches the rendered frame.
func renderSince(t time.Time) time.Duration {
	if testRenderMode {
		return 0
	}
	return time.Since(t)
}

// frozenStep pins an animation step counter to 0 in test-render mode so
// spinners render a single fixed frame instead of cycling.
func frozenStep(step int) int {
	if testRenderMode {
		return 0
	}
	return step
}
