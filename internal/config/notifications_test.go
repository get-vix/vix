package config

import "testing"

func TestNotificationSounds_Defaults(t *testing.T) {
	writeHomeSettings(t, `{"version":1}`)
	if TurnEndSoundEnabled() {
		t.Error("TurnEndSoundEnabled() = true, want false when unset")
	}
	if NeedsYouSoundEnabled() {
		t.Error("NeedsYouSoundEnabled() = true, want false when unset")
	}
	if got := TurnEndSoundName(); got != "" {
		t.Errorf("TurnEndSoundName() = %q, want empty when unset", got)
	}
	if got := NeedsYouSoundName(); got != "" {
		t.Errorf("NeedsYouSoundName() = %q, want empty when unset", got)
	}
}

func TestNotificationSounds_Read(t *testing.T) {
	writeHomeSettings(t, `{
	  "notifications": {
	    "turn_end":  {"sound": true,  "sound_name": "Glass"},
	    "needs_you": {"sound": false, "sound_name": "Ping"}
	  }
	}`)
	if !TurnEndSoundEnabled() {
		t.Error("TurnEndSoundEnabled() = false, want true")
	}
	if got := TurnEndSoundName(); got != "Glass" {
		t.Errorf("TurnEndSoundName() = %q, want Glass", got)
	}
	if NeedsYouSoundEnabled() {
		t.Error("NeedsYouSoundEnabled() = true, want false")
	}
	if got := NeedsYouSoundName(); got != "Ping" {
		t.Errorf("NeedsYouSoundName() = %q, want Ping", got)
	}
}

func TestNotificationSounds_RoundTrip(t *testing.T) {
	writeHomeSettings(t, "")

	if err := SetTurnEndSoundEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := SetTurnEndSoundName("Hero"); err != nil {
		t.Fatal(err)
	}
	if err := SetNeedsYouSoundEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := SetNeedsYouSoundName("Sosumi"); err != nil {
		t.Fatal(err)
	}

	if !TurnEndSoundEnabled() || TurnEndSoundName() != "Hero" {
		t.Errorf("turn_end = (%v, %q), want (true, Hero)", TurnEndSoundEnabled(), TurnEndSoundName())
	}
	if !NeedsYouSoundEnabled() || NeedsYouSoundName() != "Sosumi" {
		t.Errorf("needs_you = (%v, %q), want (true, Sosumi)", NeedsYouSoundEnabled(), NeedsYouSoundName())
	}
}

// TestNotificationSounds_PreservesOtherKeys verifies writing a notification
// field leaves unrelated top-level settings intact.
func TestNotificationSounds_PreservesOtherKeys(t *testing.T) {
	writeHomeSettings(t, `{"features":{"telemetry":false},"theme":{"primary":"#123456"}}`)

	if err := SetTurnEndSoundEnabled(true); err != nil {
		t.Fatal(err)
	}

	if TelemetryEnabled() {
		t.Error("telemetry flag was clobbered by notification write")
	}
	if !TurnEndSoundEnabled() {
		t.Error("turn_end sound not persisted")
	}
}
