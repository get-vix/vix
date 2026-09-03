//go:build linux

package notify

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// playerCmd builds a best-effort Linux audio-player command, preferring the
// PulseAudio player and falling back to ALSA. Returns nil when neither is on
// PATH (playback then becomes a silent no-op).
func playerCmd(path string) *exec.Cmd {
	for _, bin := range []string{"paplay", "aplay"} {
		if p, err := exec.LookPath(bin); err == nil {
			return exec.Command(p, path)
		}
	}
	return nil
}

// linuxSoundExts are the file types we surface from the freedesktop sound theme.
var linuxSoundExts = map[string]bool{".oga": true, ".ogg": true, ".wav": true}

// systemSounds lists the freedesktop sound theme, when present. This is
// best-effort: on hosts without it the list is empty and only the bundled
// sounds are offered.
func systemSounds() []Sound {
	dir := "/usr/share/sounds/freedesktop/stereo"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Sound
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !linuxSoundExts[ext] {
			continue
		}
		out = append(out, Sound{Name: strings.TrimSuffix(name, filepath.Ext(name)), Path: filepath.Join(dir, name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
