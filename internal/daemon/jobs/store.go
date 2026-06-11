package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Run statuses recorded in State.LastStatus.
const (
	StatusOK      = "ok"
	StatusError   = "error"
	StatusSkipped = "skipped"
	StatusTimeout = "timeout"
)

// State is the machine-written runtime state of one job, persisted as a single
// map keyed by job id in ~/.vix/jobs-state.json. Never hand-edited.
type State struct {
	NextRunAt         time.Time `json:"next_run_at,omitempty"`
	LastRunAt         time.Time `json:"last_run_at,omitempty"`
	LastStatus        string    `json:"last_status,omitempty"` // ok | error | skipped | timeout
	LastError         string    `json:"last_error,omitempty"`
	ConsecutiveErrors int       `json:"consecutive_errors,omitempty"`
	LastSessionID     string    `json:"last_session_id,omitempty"`
	ValidationError   string    `json:"validation_error,omitempty"`
	// AutoDisabled is set after maxConsecutiveErrors failures in a row. The
	// job stays on disk for inspection; editing its spec file clears the flag
	// (detected via SpecHash).
	AutoDisabled bool `json:"auto_disabled,omitempty"`
	// Completed marks a one-shot "at" job that already fired (disabled after
	// firing, not deleted). Cleared when the spec changes (SpecHash).
	Completed bool `json:"completed,omitempty"`
	// SpecHash fingerprints the spec this state was computed against, so an
	// edited spec resets error counters / AutoDisabled / Completed.
	SpecHash string `json:"spec_hash,omitempty"`
}

// Store reads job specs from a directory and round-trips the state file.
type Store struct {
	specsDir  string
	statePath string
}

// NewStore creates a store over the given spec directory and state file path.
// Empty paths disable the respective operation (LoadSpecs returns nothing,
// SaveState is a no-op) — that's the "no home directory" degradation.
func NewStore(specsDir, statePath string) *Store {
	return &Store{specsDir: specsDir, statePath: statePath}
}

// SpecsDir returns the directory the store reads specs from.
func (st *Store) SpecsDir() string { return st.specsDir }

// LoadSpecs reads every *.json spec in the jobs directory. Returns the valid
// specs keyed by id and a map of validation errors keyed by id (or filename
// stem when the id itself is unusable). Files that fail to parse or validate
// are reported, never fatal.
func (st *Store) LoadSpecs() (map[string]Spec, map[string]string) {
	specs := make(map[string]Spec)
	invalid := make(map[string]string)
	if st.specsDir == "" {
		return specs, invalid
	}
	entries, err := os.ReadDir(st.specsDir)
	if err != nil {
		return specs, invalid
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".json")
		data, err := os.ReadFile(filepath.Join(st.specsDir, e.Name()))
		if err != nil {
			invalid[stem] = "read: " + err.Error()
			continue
		}
		var spec Spec
		if err := json.Unmarshal(data, &spec); err != nil {
			invalid[stem] = "parse: " + err.Error()
			continue
		}
		if spec.ID == "" {
			spec.ID = stem
		}
		if err := spec.Validate(); err != nil {
			invalid[spec.ID] = err.Error()
			continue
		}
		if _, dup := specs[spec.ID]; dup {
			invalid[spec.ID] = "duplicate job id (two spec files share it)"
			continue
		}
		specs[spec.ID] = spec
	}
	return specs, invalid
}

// SpecHash fingerprints a spec's content for change detection.
func SpecHash(s Spec) string {
	data, _ := json.Marshal(s)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// LoadState reads the state file. A missing or corrupt file yields an empty
// map (state is reconstructible from the specs).
func (st *Store) LoadState() map[string]*State {
	out := make(map[string]*State)
	if st.statePath == "" {
		return out
	}
	data, err := os.ReadFile(st.statePath)
	if err != nil {
		return out
	}
	json.Unmarshal(data, &out)
	for id, s := range out {
		if s == nil {
			delete(out, id)
		}
	}
	return out
}

// SaveState atomically writes the state map (temp file + rename, same pattern
// as session records). No-op when the state path is unavailable.
func (st *Store) SaveState(state map[string]*State) error {
	if st.statePath == "" {
		return nil
	}
	dir := filepath.Dir(st.statePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(st.statePath)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, st.statePath)
}
