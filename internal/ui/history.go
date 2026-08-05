package ui

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// History manages input history with file persistence.
type History struct {
	entries []string
	times   []time.Time // timestamp for each entry
	index   int
	tmp     string // stash current input when navigating
	path    string
}

// NewHistory creates a new history manager, loading from <baseDir>/history.txt.
// baseDir should be the thread's primary .vix directory (cfg.Paths.Primary()).
func NewHistory(baseDir string) *History {
	if baseDir == "" {
		baseDir = ".vix"
	}
	h := &History{
		path: filepath.Join(baseDir, "history.txt"),
	}
	h.load()
	return h
}

func (h *History) load() {
	data, err := os.ReadFile(h.path)
	if err != nil {
		return
	}
	var lastTime time.Time
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "# ") {
			ts := line[2:]
			// Strip monotonic clock suffix (e.g. " m=+118.395129584")
			if idx := strings.Index(ts, " m="); idx >= 0 {
				ts = ts[:idx]
			}
			if t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", ts); err == nil {
				lastTime = t
			}
		} else if strings.HasPrefix(line, "+") {
			h.entries = append(h.entries, line[1:])
			h.times = append(h.times, lastTime)
			lastTime = time.Time{}
		} else if strings.HasPrefix(line, "|") {
			if len(h.entries) > 0 {
				h.entries[len(h.entries)-1] += "\n" + line[1:]
			}
		}
	}
	h.index = len(h.entries)
}

// Save adds an entry to history (in memory and on disk).
func (h *History) Save(text string) {
	if text == "" {
		return
	}
	if len(h.entries) > 0 && h.entries[len(h.entries)-1] == text {
		h.index = len(h.entries)
		return
	}
	now := time.Now()
	h.entries = append(h.entries, text)
	h.times = append(h.times, now)
	h.index = len(h.entries)

	// Append to file
	os.MkdirAll(filepath.Dir(h.path), 0o755)
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	lines := strings.Split(text, "\n")
	f.WriteString("\n# " + now.Format("2006-01-02 15:04:05.999999999 -0700 MST") + "\n")
	for i, line := range lines {
		if i == 0 {
			f.WriteString("+" + line + "\n")
		} else {
			f.WriteString("|" + line + "\n")
		}
	}
}

// Previous returns the previous history entry (up arrow).
func (h *History) Previous(currentInput string) (string, bool) {
	if len(h.entries) == 0 {
		return "", false
	}
	if h.index == len(h.entries) {
		h.tmp = currentInput
	}
	if h.index > 0 {
		h.index--
		return h.entries[h.index], true
	}
	return "", false
}

// Next returns the next history entry (down arrow).
func (h *History) Next() (string, bool) {
	if len(h.entries) == 0 {
		return "", false
	}
	if h.index < len(h.entries)-1 {
		h.index++
		return h.entries[h.index], true
	}
	if h.index == len(h.entries)-1 {
		h.index = len(h.entries)
		return h.tmp, true
	}
	return "", false
}

// Reset resets the history index to the end.
func (h *History) Reset() {
	h.index = len(h.entries)
	h.tmp = ""
}
