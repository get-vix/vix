package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// This scenario exercises the Settings tab (F6) "Notifications" section: the two
// on/off toggles and the two sound pickers. Because vix ships bundled sounds
// (embedded in the binary), the picker is populated deterministically even on
// the offline Linux image, so ←/→ cycling is testable here. VIX_DISABLE_SOUND
// keeps the run silent — we assert the UI, not audio.

const notificationsSettings = `{
  "version": 1,
  "notifications": {
    "turn_end":  { "sound": true, "sound_name": "Chime" },
    "needs_you": { "sound": true, "sound_name": "Chime" }
  }
}`

// TestSettingsTabNotifications verifies the Notifications section renders both
// toggles and sound pickers, and that ←/→ cycles the selected sound.
func TestSettingsTabNotifications(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "ui",
		Subcategory: "ui.notifications",
		Description: "F6 Settings tab shows sound-notification toggles and pickers; ←/→ cycles the sound",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile(".vix/settings.json", notificationsSettings),
		harness.WithEnv("VIX_DISABLE_SOUND", "1"),
	)

	h.UI.WaitStable(500 * time.Millisecond)

	h.UI.Key("f6")
	h.UI.WaitFor("Notifications")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("settings-notifications")

	for _, want := range []string{
		"Notifications",
		"Play sound when a turn ends",
		"Play sound when the agent needs you",
		"‹ Chime ›",
	} {
		if !h.UI.Contains(want) {
			t.Fatalf("Settings tab missing %q; screen:\n%s", want, h.UI.Snapshot())
		}
	}

	// Both sounds start as Chime, so Ping is not shown yet.
	if h.UI.Contains("‹ Ping ›") {
		t.Fatalf("did not expect '‹ Ping ›' before cycling; screen:\n%s", h.UI.Snapshot())
	}

	// Move the cursor to the turn-end sound picker: rows 0..4 are
	// update-action, update-check, show-thinking, turn-end toggle, turn-end
	// sound. Four downs lands on the picker; → cycles Chime → Ping (the two
	// bundled sounds come first).
	for i := 0; i < 4; i++ {
		h.UI.Key("down")
	}
	h.UI.Key("right")
	h.UI.WaitFor("‹ Ping ›")
	h.UI.Shot("settings-notifications-cycled")

	if !h.UI.Contains("‹ Ping ›") {
		t.Fatalf("cycling the turn-end sound did not show '‹ Ping ›'; screen:\n%s", h.UI.Snapshot())
	}
}
