package hooks

import "time"

// maxRecentRuns bounds how many run records State.RecentRuns retains.
const maxRecentRuns = 10

// RunRecord is one entry in a hook's recent-fire history (State.RecentRuns).
// Status carries the resolved outcome: for synchronous hooks it is the
// decision behavior (allow | deny | context | modify); for asynchronous hooks
// it is done | error. ThreadID is set only for workflow/prompt hooks that ran
// in their own thread (command hooks have none).
type RunRecord struct {
	At       time.Time `json:"at"`
	Status   string    `json:"status"`
	Async    bool      `json:"async"`
	Event    string    `json:"event,omitempty"`
	Error    string    `json:"error,omitempty"`
	ThreadID string    `json:"thread_id,omitempty"`
	Duration string    `json:"duration,omitempty"` // Go duration string
}

// State is the machine-written runtime state of one hook, persisted as
// <id>/state.json inside the hook's own subdirectory (a sibling of hook.json).
// Never hand-edited. Unlike a hook spec, this is appended to on every fire.
type State struct {
	LastFiredAt time.Time   `json:"last_fired_at,omitempty"`
	LastStatus  string      `json:"last_status,omitempty"`
	LastError   string      `json:"last_error,omitempty"`
	LastFireID  string      `json:"last_fire_id,omitempty"`
	RecentRuns  []RunRecord `json:"recent_runs,omitempty"` // newest last, capped at maxRecentRuns
}

// appendRun records one fire in the history, newest last, trimming to the most
// recent maxRecentRuns entries.
func (s *State) appendRun(r RunRecord) {
	s.RecentRuns = append(s.RecentRuns, r)
	if len(s.RecentRuns) > maxRecentRuns {
		s.RecentRuns = s.RecentRuns[len(s.RecentRuns)-maxRecentRuns:]
	}
}
