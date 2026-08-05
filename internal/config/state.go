package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// State is the global, non-project-scoped thread bookkeeping persisted to
// state.json (see VixPaths.StateFile). Keep fields additive and tolerant of
// absence.
type State struct {
	// LastUpdateCheck is the date (YYYY-MM-DD) of the most recent GitHub
	// release check. Empty when never checked.
	LastUpdateCheck string `json:"last_update_check,omitempty"`
	// LatestKnown is the newest release tag seen at the last check.
	LatestKnown string `json:"latest_known,omitempty"`
	// LatestURL is the release page for LatestKnown.
	LatestURL string `json:"latest_url,omitempty"`
	// Model is the user's chosen chat model spec (e.g. "openai/gpt-5.1"),
	// persisted when the user picks a model in the UI. Empty means use the
	// built-in default. Agent frontmatter `model:` (custom agents) still
	// overrides this.
	Model string `json:"model,omitempty"`
	// DefaultsVersion is the binary version that last seeded/refreshed the
	// managed default files (settings.json, config/*.json, prompts/**,
	// agents/**). A mismatch with the running version triggers a refresh.
	DefaultsVersion string `json:"defaults_version,omitempty"`
}

// ReadState loads state.json at path. A missing or unreadable file yields a
// zero State and no error — absence is normal.
func ReadState(path string) State {
	var st State
	if path == "" {
		return st
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

// WriteState atomically persists st to path, creating the parent directory if
// needed. A no-op (nil) when path is empty.
func WriteState(path string, st State) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
