//go:build darwin

package notify

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// playerCmd builds the macOS audio-player command, or nil when afplay is
// somehow unavailable.
func playerCmd(path string) *exec.Cmd {
	bin, err := exec.LookPath("afplay")
	if err != nil {
		return nil
	}
	return exec.Command(bin, path)
}

// macSoundExts are the file types afplay can play that we surface in the picker.
var macSoundExts = map[string]bool{
	".aiff": true, ".aif": true, ".wav": true, ".m4a": true, ".mp3": true, ".caf": true,
}

// systemSounds lists the sounds macOS ships plus the user's personal sounds.
func systemSounds() []Sound {
	dirs := []string{"/System/Library/Sounds"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Library", "Sounds"))
	}
	var out []Sound
	seen := map[string]bool{}
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			ext := strings.ToLower(filepath.Ext(name))
			if !macSoundExts[ext] {
				continue
			}
			display := strings.TrimSuffix(name, filepath.Ext(name))
			if seen[display] {
				continue
			}
			seen[display] = true
			out = append(out, Sound{Name: display, Path: filepath.Join(d, name)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
