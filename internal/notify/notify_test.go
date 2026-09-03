package notify

import "testing"

func TestAvailableSounds_BundledAlwaysPresent(t *testing.T) {
	sounds := AvailableSounds()
	if len(sounds) < 2 {
		t.Fatalf("AvailableSounds() returned %d sounds, want at least the 2 bundled ones", len(sounds))
	}
	// The two bundled sounds are always offered first, in registration order.
	if sounds[0].Name != "Chime" || sounds[1].Name != "Ping" {
		t.Fatalf("first two sounds = %q, %q; want Chime, Ping", sounds[0].Name, sounds[1].Name)
	}
	for _, s := range sounds[:2] {
		if s.Path == "" {
			t.Errorf("bundled sound %q has empty path", s.Name)
		}
	}
}

func TestResolve(t *testing.T) {
	if got := resolve(""); got != "" {
		t.Errorf("resolve(\"\") = %q, want empty", got)
	}
	if got := resolve("does-not-exist-xyz"); got != "" {
		t.Errorf("resolve(unknown) = %q, want empty", got)
	}
	if got := resolve("Chime"); got == "" {
		t.Error("resolve(\"Chime\") = empty, want the bundled path")
	}
}

func TestPlay_CallsPlayFuncWithResolvedPath(t *testing.T) {
	t.Setenv("VIX_DISABLE_SOUND", "")
	var got string
	orig := playFunc
	playFunc = func(path string) { got = path }
	defer func() { playFunc = orig }()

	Play("Chime")
	if got == "" {
		t.Fatal("Play(\"Chime\") did not call playFunc with a resolved path")
	}
	if got != resolve("Chime") {
		t.Errorf("playFunc got %q, want %q", got, resolve("Chime"))
	}
}

func TestPlay_DisabledIsNoOp(t *testing.T) {
	t.Setenv("VIX_DISABLE_SOUND", "1")
	called := false
	orig := playFunc
	playFunc = func(path string) { called = true }
	defer func() { playFunc = orig }()

	Play("Chime")
	if called {
		t.Error("Play called playFunc while VIX_DISABLE_SOUND=1")
	}
}

func TestPlay_UnknownNameIsSilent(t *testing.T) {
	t.Setenv("VIX_DISABLE_SOUND", "")
	var got string
	orig := playFunc
	playFunc = func(path string) { got = path }
	defer func() { playFunc = orig }()

	Play("no-such-sound")
	if got != "" {
		t.Errorf("Play(unknown) resolved to %q, want empty (silent)", got)
	}
}

// TestPlayPreview_StopsPrevious verifies that starting a new preview stops the
// one still playing — the behavior that keeps the Settings picker from stacking
// sounds as the user scrolls.
func TestPlayPreview_StopsPrevious(t *testing.T) {
	t.Setenv("VIX_DISABLE_SOUND", "")
	var stopped []string
	orig := previewFunc
	previewFunc = func(path string) func() {
		p := path
		return func() { stopped = append(stopped, p) }
	}
	defer func() {
		previewFunc = orig
		// Reset the singleton so we don't leak a stop closure across tests.
		previewMu.Lock()
		previewStop = nil
		previewMu.Unlock()
	}()

	first := resolve("Chime")
	PlayPreview("Chime")
	if len(stopped) != 0 {
		t.Fatalf("first preview stopped something: %v", stopped)
	}
	PlayPreview("Ping")
	if len(stopped) != 1 || stopped[0] != first {
		t.Fatalf("second preview did not stop the first; stopped=%v want [%q]", stopped, first)
	}
}
