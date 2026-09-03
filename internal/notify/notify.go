// Package notify plays short notification sounds from the vix TUI: one when a
// turn ends and one when the agent needs the user. Sounds are chosen from a set
// that always includes vix's own bundled sounds (embedded in the binary, so the
// list is never empty on any platform) plus whatever sounds the operating
// system ships. Playback shells out to the platform's audio player and is
// best-effort: if no player is available it is a silent no-op.
package notify

import (
	"embed"
	"os"
	"path/filepath"
	"sync"
)

//go:embed sounds/*.wav
var bundledFS embed.FS

// Sound is a selectable notification sound.
type Sound struct {
	Name string // display + stored key, e.g. "Chime" or "Glass"
	Path string // absolute path to a playable file on disk
}

// Default sound names. Both are bundled, so they resolve on every platform.
const (
	DefaultTurnEndSound  = "Chime"
	DefaultNeedsYouSound = "Ping"
)

// bundled sounds ship inside the binary and are always offered first. They are
// materialized to a cache file on first use so external players get a real path.
var bundled = []struct {
	name string
	file string
}{
	{"Chime", "sounds/chime.wav"},
	{"Ping", "sounds/ping.wav"},
}

var (
	listOnce  sync.Once
	listCache []Sound
)

// AvailableSounds returns the bundled sounds first, then any system sounds found
// on this platform, sorted by name. The result is computed once per process and
// cached — callers may hold onto the slice for the lifetime of the process.
func AvailableSounds() []Sound {
	listOnce.Do(func() {
		var out []Sound
		for _, b := range bundled {
			if p, err := materialize(b.file); err == nil {
				out = append(out, Sound{Name: b.name, Path: p})
			}
		}
		out = append(out, systemSounds()...)
		listCache = out
	})
	return listCache
}

// resolve maps a stored sound name to a playable path. Returns "" when the name
// is empty or no longer matches an available sound (e.g. a system sound that was
// removed, or a name from another machine).
func resolve(name string) string {
	if name == "" {
		return ""
	}
	for _, s := range AvailableSounds() {
		if s.Name == name {
			return s.Path
		}
	}
	return ""
}

// disabled reports whether sound is turned off process-wide via the
// VIX_DISABLE_SOUND kill switch (used by tests and preview/recording runs).
func disabled() bool {
	v := os.Getenv("VIX_DISABLE_SOUND")
	return v == "1" || v == "true"
}

// playFunc launches a fire-and-forget sound for the resolved path. Overridable
// in tests.
var playFunc = defaultPlay

func defaultPlay(path string) {
	if path == "" {
		return
	}
	cmd := playerCmd(path)
	if cmd == nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}

// Play plays the named sound, fire-and-forget. It is a silent no-op when sound
// is disabled, the name does not resolve, or no player is available.
func Play(name string) {
	if disabled() {
		return
	}
	playFunc(resolve(name))
}

// previewFunc starts a preview and returns a stop function (or nil when nothing
// was started). Overridable in tests.
var previewFunc = defaultPreview

func defaultPreview(path string) func() {
	if path == "" {
		return nil
	}
	cmd := playerCmd(path)
	if cmd == nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil
	}
	go func() { _ = cmd.Wait() }()
	return func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
}

var (
	previewMu   sync.Mutex
	previewStop func()
)

// PlayPreview plays the named sound as a preview, stopping any preview still
// playing first. Used by the Settings picker so scrolling through sounds does
// not stack them on top of each other.
func PlayPreview(name string) {
	if disabled() {
		return
	}
	previewMu.Lock()
	defer previewMu.Unlock()
	if previewStop != nil {
		previewStop()
		previewStop = nil
	}
	previewStop = previewFunc(resolve(name))
}

// materialize writes an embedded sound to a per-user cache file and returns its
// path, rewriting only when missing or a different size.
func materialize(embedPath string) (string, error) {
	data, err := bundledFS.ReadFile(embedPath)
	if err != nil {
		return "", err
	}
	dir := cacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, filepath.Base(embedPath))
	if fi, err := os.Stat(dst); err != nil || fi.Size() != int64(len(data)) {
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return "", err
		}
	}
	return dst, nil
}

func cacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "vix", "sounds")
	}
	return filepath.Join(os.TempDir(), "vix-sounds")
}
