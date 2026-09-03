package ui

import (
	"strings"
	"testing"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/notify"
)

// TestRenderSettingsViewNotifications asserts the Notifications section renders
// both toggles and both sound pickers with the selected names.
func TestRenderSettingsViewNotifications(t *testing.T) {
	s := NewStyles(true)
	st := settingsState{
		turnEndSound:      true,
		turnEndSoundName:  "Glass",
		needsYouSound:     false,
		needsYouSoundName: "Ping",
		soundsAvailable:   true,
	}
	out := renderSettingsView(120, 40, s, st)

	for _, want := range []string{
		"Notifications",
		"Play sound when a turn ends",
		"Play sound when the agent needs you",
		"‹ Glass ›",
		"‹ Ping ›",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderSettingsView output missing %q\n---\n%s", want, out)
		}
	}
}

// TestRenderSettingsViewNotificationsUnavailable asserts the picker degrades to
// "unavailable" when the platform offers no sounds.
func TestRenderSettingsViewNotificationsUnavailable(t *testing.T) {
	s := NewStyles(true)
	st := settingsState{soundsAvailable: false, turnEndSoundName: "Chime", needsYouSoundName: "Ping"}
	out := renderSettingsView(120, 40, s, st)
	if !strings.Contains(out, "‹ unavailable ›") {
		t.Errorf("expected 'unavailable' picker when no sounds; got\n%s", out)
	}
}

// TestSettingsCursorOnSoundRow guards that the cursor index space accounts for
// the new sound rows: with the cursor on the turn-end sound picker, the "▸"
// marker lands on that row.
func TestSettingsCursorOnSoundRow(t *testing.T) {
	s := NewStyles(true)
	st := settingsState{
		cursor:           int(settingTurnEndSoundChoice),
		turnEndSound:     true,
		turnEndSoundName: "Glass",
		soundsAvailable:  true,
	}
	out := renderSettingsView(120, 40, s, st)

	var cursorLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "▸") {
			cursorLine = line
			break
		}
	}
	if !strings.Contains(cursorLine, "‹ Glass ›") {
		t.Errorf("cursor marker should be on the turn-end sound row, got %q", cursorLine)
	}
}

// TestAdjustSoundCycles verifies ←/→ cycling persists the neighbor sound. It
// runs against a temp HOME with sound playback disabled.
func TestAdjustSoundCycles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("VIX_DISABLE_SOUND", "1")

	sounds := notify.AvailableSounds()
	if len(sounds) < 2 {
		t.Skip("need at least two sounds to test cycling")
	}
	m := &Model{}

	// From the unset default (Chime == sounds[0]), one step right → sounds[1].
	m.adjustSound(settingTurnEndSoundChoice, 1)
	if got, want := config.TurnEndSoundName(), sounds[1].Name; got != want {
		t.Errorf("after right: TurnEndSoundName() = %q, want %q", got, want)
	}

	// From Chime (sounds[0]), one step left wraps to the last sound.
	_ = config.SetTurnEndSoundName(sounds[0].Name)
	m.adjustSound(settingTurnEndSoundChoice, -1)
	if got, want := config.TurnEndSoundName(), sounds[len(sounds)-1].Name; got != want {
		t.Errorf("after left-wrap: TurnEndSoundName() = %q, want %q", got, want)
	}
}
