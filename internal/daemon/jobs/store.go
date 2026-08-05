package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/sjson"
)

// Run statuses recorded in State.LastStatus.
const (
	StatusOK      = "ok"
	StatusError   = "error"
	StatusSkipped = "skipped"
	StatusTimeout = "timeout"
)

// maxRecentRuns bounds how many run records State.RecentRuns retains.
const maxRecentRuns = 10

// RunRecord is one entry in a job's recent-run history (State.RecentRuns).
type RunRecord struct {
	At       time.Time `json:"at"`
	Status   string    `json:"status"` // ok | error | skipped | timeout
	Error    string    `json:"error,omitempty"`
	ThreadID string    `json:"thread_id,omitempty"`
	Duration string    `json:"duration,omitempty"` // Go duration string
}

// State is the machine-written runtime state of one job, persisted as
// <id>/state.json inside the job's own subdirectory (a sibling of job.json).
// Never hand-edited.
type State struct {
	NextRunAt         time.Time `json:"next_run_at,omitempty"`
	LastRunAt         time.Time `json:"last_run_at,omitempty"`
	LastStatus        string    `json:"last_status,omitempty"` // ok | error | skipped | timeout
	LastError         string    `json:"last_error,omitempty"`
	ConsecutiveErrors int       `json:"consecutive_errors,omitempty"`
	LastThreadID      string    `json:"last_thread_id,omitempty"`
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
	// RecentRuns is the job's recent-run history, newest last, capped at
	// maxRecentRuns. Appended on every finished run (scheduled, manual, and
	// catch-up skip) via appendRun.
	RecentRuns []RunRecord `json:"recent_runs,omitempty"`
}

// appendRun records one run in the history, newest last, trimming to the most
// recent maxRecentRuns entries.
func (s *State) appendRun(r RunRecord) {
	s.RecentRuns = append(s.RecentRuns, r)
	if len(s.RecentRuns) > maxRecentRuns {
		s.RecentRuns = s.RecentRuns[len(s.RecentRuns)-maxRecentRuns:]
	}
}

// Store reads job specs from a directory and round-trips per-job state files.
type Store struct {
	specsDir string
}

// NewStore creates a store over the given spec directory. An empty path
// disables all operations (LoadSpecs returns nothing, SaveStateFor is a no-op)
// — that's the "no home directory" degradation. Each job's runtime state lives
// inside its own subdirectory as <id>/state.json, alongside the spec.
func NewStore(specsDir string) *Store {
	return &Store{specsDir: specsDir}
}

// SpecsDir returns the directory the store reads specs from.
func (st *Store) SpecsDir() string { return st.specsDir }

// LoadSpecs reads every job spec under the jobs directory. Each job lives in
// its own subdirectory as <id>/job.json; the directory name is the default id.
// Returns the valid specs keyed by id and a map of validation errors keyed by
// id (the subdirectory name when the id itself is unusable). Subdirectories
// without a job.json (or whose job.json fails to parse/validate) are reported,
// never fatal. A subdirectory holding only state.json (no job.json) is skipped.
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
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		specPath := filepath.Join(st.specsDir, name, "job.json")
		data, err := os.ReadFile(specPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue // not a job directory
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
		if _, dup := specs[spec.ID]; dup {
			invalid[spec.ID] = "duplicate job id (two spec files share it)"
			continue
		}
		// Materialize declared helper files into the job dir (best-effort: a
		// write failure must not stop the spec from loading/running).
		_ = materializeSpecFiles(filepath.Join(st.specsDir, name), spec.Files)
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

// SaveSpec writes a single job spec to <specsDir>/<id>/job.json atomically
// (temp file + rename, same pattern as SaveStateFor). The job's subdirectory is
// created if needed. Errors when the spec directory is unavailable (no home
// directory), since there is nowhere to persist to.
func (st *Store) SaveSpec(s Spec) error {
	if st.specsDir == "" {
		return fmt.Errorf("jobs store has no spec directory")
	}
	if s.ID == "" {
		return fmt.Errorf("cannot save spec with empty id")
	}
	jobDir := filepath.Join(st.specsDir, s.ID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(jobDir, "job.*.tmp")
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
	if err := os.Rename(tmpName, filepath.Join(jobDir, "job.json")); err != nil {
		return err
	}
	// Materialize any declared helper files alongside job.json.
	return materializeSpecFiles(jobDir, s.Files)
}

// materializeSpecFiles writes a spec's declared helper files into its job
// directory (jobDir), confined to that directory. Each write is atomic (temp
// file + rename) and write-if-changed: an on-disk copy whose content already
// matches is left untouched (only its mode is corrected if it drifted), so
// repeated LoadSpecs on hot-reload don't churn mtimes. The spec is the source of
// truth, so differing content is overwritten. Paths are re-validated here as a
// defence-in-depth check even though Validate already rejected unsafe ones.
func materializeSpecFiles(jobDir string, files []SpecFile) error {
	for _, f := range files {
		rel, err := safeSpecFilePath(f.Path)
		if err != nil {
			return err
		}
		dst := filepath.Join(jobDir, rel)
		if r, err := filepath.Rel(jobDir, dst); err != nil ||
			r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return fmt.Errorf("file %q escapes the job directory", f.Path)
		}
		mode := f.FileMode()
		if existing, err := os.ReadFile(dst); err == nil && string(existing) == f.Content {
			if fi, err := os.Stat(dst); err == nil && fi.Mode().Perm() != mode.Perm() {
				_ = os.Chmod(dst, mode)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(dst), ".file.*.tmp")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		if _, err := tmp.WriteString(f.Content); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpName)
			return err
		}
		if err := os.Chmod(tmpName, mode); err != nil {
			os.Remove(tmpName)
			return err
		}
		if err := os.Rename(tmpName, dst); err != nil {
			os.Remove(tmpName)
			return err
		}
	}
	return nil
}

// SpecExists reports whether a spec file with the given id is already present.
func (st *Store) SpecExists(id string) bool {
	if st.specsDir == "" || id == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(st.specsDir, id, "job.json"))
	return err == nil
}

// SetEnabled surgically rewrites the `enabled` field of <id>/job.json in place,
// preserving every other key, its order, and the file's formatting (unlike
// SaveSpec, which re-serializes the whole struct). The edited bytes are written
// atomically (temp file + rename). Errors when the spec directory or id is
// unavailable, or when the job.json cannot be read/patched.
func (st *Store) SetEnabled(id string, enabled bool) error {
	if st.specsDir == "" {
		return fmt.Errorf("jobs store has no spec directory")
	}
	if id == "" {
		return fmt.Errorf("cannot set enabled on empty id")
	}
	jobDir := filepath.Join(st.specsDir, id)
	specPath := filepath.Join(jobDir, "job.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		return err
	}
	patched, err := sjson.SetBytes(data, "enabled", enabled)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(jobDir, "job.*.tmp")
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

// LoadState reads every job's state file (<id>/state.json) under the spec
// directory and returns them keyed by id. Missing or corrupt files are skipped
// (state is reconstructible from the specs). Returns an empty map when the spec
// directory is unavailable.
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

// SaveStateFor atomically writes one job's state file to <id>/state.json (temp
// file + rename). The job's subdirectory is created if needed. No-op when the
// spec directory or id is unavailable.
func (st *Store) SaveStateFor(id string, state *State) error {
	if st.specsDir == "" || id == "" || state == nil {
		return nil
	}
	jobDir := filepath.Join(st.specsDir, id)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(jobDir, "state.*.tmp")
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
	return os.Rename(tmpName, filepath.Join(jobDir, "state.json"))
}

// DeleteState removes one job's state file. Used when a spec vanishes from disk
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
