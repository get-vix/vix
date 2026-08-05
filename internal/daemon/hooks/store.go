package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tidwall/sjson"
)

// Store reads hook specs from a directory and round-trips per-hook state files.
// Specs are user-authored (<id>/hook.json); state is machine-written
// (<id>/state.json, a sibling) so spec files never churn.
type Store struct {
	specsDir string
}

// NewStore creates a store over the given spec directory. An empty path
// disables loading (LoadSpecs returns nothing) — the "no home directory"
// degradation.
func NewStore(specsDir string) *Store {
	return &Store{specsDir: specsDir}
}

// SpecsDir returns the directory the store reads specs from.
func (st *Store) SpecsDir() string { return st.specsDir }

// LoadSpecs reads every hook spec under the hooks directory. Each hook lives in
// its own subdirectory as <id>/hook.json; the directory name is the default id.
// Returns the valid specs and a map of validation errors keyed by id (or the
// subdirectory name when the id itself is unusable). Subdirectories without a
// hook.json are ignored; ones that fail to parse or validate are reported,
// never fatal.
func (st *Store) LoadSpecs() ([]Spec, map[string]string) {
	var specs []Spec
	invalid := make(map[string]string)
	if st.specsDir == "" {
		return specs, invalid
	}
	entries, err := os.ReadDir(st.specsDir)
	if err != nil {
		return specs, invalid
	}
	seen := make(map[string]bool)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		specPath := filepath.Join(st.specsDir, name, "hook.json")
		data, err := os.ReadFile(specPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue // not a hook directory
			}
			invalid[name] = "read: " + err.Error()
			continue
		}
		var spec Spec
		if err := json.Unmarshal(data, &spec); err != nil {
			invalid[name] = "parse: " + err.Error()
			continue
		}
		if spec.ID == "" {
			spec.ID = name
		}
		if err := spec.Validate(); err != nil {
			invalid[spec.ID] = err.Error()
			continue
		}
		if seen[spec.ID] {
			invalid[spec.ID] = "duplicate hook id (two spec files share it)"
			continue
		}
		seen[spec.ID] = true
		specs = append(specs, spec)
	}
	return specs, invalid
}

// LoadState reads every hook's state file (<id>/state.json) under the spec
// directory and returns them keyed by id. Missing or corrupt files are skipped
// (state is reconstructible). Returns an empty map when the spec directory is
// unavailable.
func (st *Store) LoadState() map[string]*State {
	out := make(map[string]*State)
	if st.specsDir == "" {
		return out
	}
	entries, err := os.ReadDir(st.specsDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		data, err := os.ReadFile(filepath.Join(st.specsDir, id, "state.json"))
		if err != nil {
			continue
		}
		var s State
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}
		out[id] = &s
	}
	return out
}

// SaveStateFor atomically writes one hook's state file to <id>/state.json (temp
// file + rename). The hook's subdirectory is created if needed. No-op when the
// spec directory or id is unavailable.
func (st *Store) SaveStateFor(id string, state *State) error {
	if st.specsDir == "" || id == "" || state == nil {
		return nil
	}
	hookDir := filepath.Join(st.specsDir, id)
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(hookDir, "state.*.tmp")
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
	return os.Rename(tmpName, filepath.Join(hookDir, "state.json"))
}

// DeleteState removes one hook's state file. Used when a spec vanishes from disk
// but its subdirectory lingers. A missing file is not an error. No-op when the
// spec directory or id is unavailable.
func (st *Store) DeleteState(id string) error {
	if st.specsDir == "" || id == "" {
		return nil
	}
	err := os.Remove(filepath.Join(st.specsDir, id, "state.json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SetEnabled surgically rewrites the `enabled` field of <id>/hook.json in place,
// preserving every other key, its order, and the file's formatting. The edited
// bytes are written atomically (temp file + rename). Errors when the spec
// directory or id is unavailable, or when the hook.json cannot be read/patched.
func (st *Store) SetEnabled(id string, enabled bool) error {
	if st.specsDir == "" {
		return fmt.Errorf("hooks store has no spec directory")
	}
	if id == "" {
		return fmt.Errorf("cannot set enabled on empty id")
	}
	hookDir := filepath.Join(st.specsDir, id)
	specPath := filepath.Join(hookDir, "hook.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		return err
	}
	patched, err := sjson.SetBytes(data, "enabled", enabled)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(hookDir, "hook.*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(patched); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, specPath)
}
