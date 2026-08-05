package hooks

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Registry is the in-memory, hot-reloadable index of enabled hook specs grouped
// by event. It is safe for concurrent use: the thread loop reads it on every
// matching lifecycle point while the config watcher swaps it on disk changes.
type Registry struct {
	store *Store

	mu       sync.RWMutex
	byEvent  map[string][]Spec
	all      []Spec
	disabled int
	invalid  map[string]string

	// stateMu guards the per-hook runtime state (recent-fire history), kept
	// separate from the spec index so it survives the wholesale Reload swap.
	stateMu sync.Mutex
	state   map[string]*State
}

// HookSnapshot is a read-only view of a hook for external consumers (the web UI
// hooks tab). It carries the spec fields the UI renders, with the mode resolved
// to its effective value and permissions flattened to resolved booleans.
type HookSnapshot struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Enabled        bool           `json:"enabled"`
	Trigger        HookTrigger    `json:"trigger"`
	Mode           string         `json:"mode"`
	Blocking       bool           `json:"blocking"`
	Command        string         `json:"command,omitempty"`
	WorkflowID     string         `json:"workflow_id,omitempty"`
	WorkflowInline bool           `json:"workflow_inline,omitempty"`
	Prompt         string         `json:"prompt,omitempty"`
	CWD            string         `json:"cwd,omitempty"`
	Timeout        string         `json:"timeout"`
	Description    string         `json:"description,omitempty"`
	Permissions    map[string]any `json:"permissions"`
	CreatedBy      string         `json:"created_by"`

	// Runtime history, attached from the hook's persisted State. LastFiredAt is
	// the zero time when the hook has never fired; RecentRuns is newest-last,
	// capped at maxRecentRuns.
	LastFiredAt time.Time   `json:"last_fired_at,omitempty"`
	RecentRuns  []RunRecord `json:"recent_runs,omitempty"`
}

// NewRegistry builds a registry over the store and performs the initial load.
func NewRegistry(store *Store) *Registry {
	r := &Registry{store: store, byEvent: map[string][]Spec{}, state: store.LoadState()}
	r.Reload()
	return r
}

// Reload re-reads the spec directory and atomically swaps the index. Per-hook
// runtime state (recent-fire history) is preserved across the swap; state for
// ids that no longer exist on disk is dropped and its state file removed.
func (r *Registry) Reload() {
	specs, invalid := r.store.LoadSpecs()
	byEvent := make(map[string][]Spec)
	disabled := 0
	for _, s := range specs {
		if !s.Enabled {
			disabled++
			continue
		}
		byEvent[s.Trigger.Event] = append(byEvent[s.Trigger.Event], s)
	}
	r.mu.Lock()
	r.byEvent = byEvent
	r.all = specs
	r.disabled = disabled
	r.invalid = invalid
	r.mu.Unlock()

	// Prune runtime state for hooks whose spec vanished (neither valid nor
	// invalid). The state map itself is preserved across the swap.
	present := make(map[string]bool, len(specs)+len(invalid))
	for _, s := range specs {
		present[s.ID] = true
	}
	for id := range invalid {
		present[id] = true
	}
	r.stateMu.Lock()
	for id := range r.state {
		if !present[id] {
			delete(r.state, id)
			r.store.DeleteState(id)
		}
	}
	r.stateMu.Unlock()
}

// SetEnabled flips a hook spec's `enabled` field on disk and reloads the index.
// The edit is surgical (only the enabled value is rewritten via the store), so
// the rest of the user's hook.json is preserved. Returns an error when the id is
// unknown or the file cannot be patched.
func (r *Registry) SetEnabled(id string, enabled bool) error {
	if _, ok := r.SpecByID(id); !ok {
		return fmt.Errorf("hook %q not found", id)
	}
	if err := r.store.SetEnabled(id, enabled); err != nil {
		return err
	}
	r.Reload()
	return nil
}

// RecordRun appends one fire to the hook's recent-run history, updates the
// last-* summary fields, and persists the state file. Best-effort: a persist
// error is swallowed (state is reconstructible from the run log). Safe for
// concurrent fires of the same hook.
func (r *Registry) RecordRun(id string, rec RunRecord) {
	if id == "" {
		return
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.state == nil {
		r.state = make(map[string]*State)
	}
	st := r.state[id]
	if st == nil {
		st = &State{}
		r.state[id] = st
	}
	st.LastFiredAt = rec.At
	st.LastStatus = rec.Status
	st.LastError = rec.Error
	st.appendRun(rec)
	r.store.SaveStateFor(id, st)
}

// StateByID returns a copy of the hook's runtime state, or nil when none has
// been recorded yet.
func (r *Registry) StateByID(id string) *State {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	st := r.state[id]
	if st == nil {
		return nil
	}
	cp := *st
	cp.RecentRuns = append([]RunRecord(nil), st.RecentRuns...)
	return &cp
}

// Match returns the enabled hooks for event whose matcher accepts field, split
// into synchronous and asynchronous groups in deterministic spec order.
func (r *Registry) Match(event, field string) (sync, async []Spec) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.byEvent[event] {
		if !s.Matches(field) {
			continue
		}
		if s.EffectiveMode() == ModeSync {
			sync = append(sync, s)
		} else {
			async = append(async, s)
		}
	}
	return sync, async
}

// Has reports whether any enabled hook subscribes to event (cheap pre-check so
// the thread loop can skip building a context when nothing is listening).
func (r *Registry) Has(event string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byEvent[event]) > 0
}

// SpecByID returns the hook spec with the given id, including disabled ones, so
// callers (e.g. an on-demand `vix hook trigger <id>`) can fire a hook by id
// regardless of whether it is currently enabled. The second result is false
// when no spec carries that id.
func (r *Registry) SpecByID(id string) (Spec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.all {
		if s.ID == id {
			return s, true
		}
	}
	return Spec{}, false
}

// Invalid returns the most recent validation errors keyed by id, for surfacing
// in a /hooks browser or logs.
func (r *Registry) Invalid() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.invalid))
	for k, v := range r.invalid {
		out[k] = v
	}
	return out
}

// Snapshot returns every valid hook spec (enabled and disabled) as read-only
// views, sorted by id for stable rendering. Safe to call concurrently with the
// thread loop and config watcher.
func (r *Registry) Snapshot() []HookSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]HookSnapshot, 0, len(r.all))
	for _, s := range r.all {
		snap := HookSnapshot{
			ID:             s.ID,
			Name:           s.Name,
			Enabled:        s.Enabled,
			Trigger:        s.Trigger,
			Mode:           s.EffectiveMode(),
			Blocking:       s.Blocking,
			Command:        s.Command,
			WorkflowID:     s.WorkflowID,
			WorkflowInline: s.Workflow != nil,
			Prompt:         s.Prompt,
			CWD:            s.CWD,
			Timeout:        s.EffectiveTimeout(),
			Description:    s.Description,
			Permissions: map[string]any{
				"auto_write": s.AutoWrite(),
				"auto_dirs":  s.AutoDirs(),
			},
			CreatedBy: s.CreatedBy,
		}
		if st := r.StateByID(s.ID); st != nil {
			snap.LastFiredAt = st.LastFiredAt
			snap.RecentRuns = st.RecentRuns
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
