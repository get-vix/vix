package jobs

import (
	"testing"
	"time"
)

// TestJobSnapshotIncludesRecentRuns verifies that Snapshot surfaces the job's
// persisted run history and last-run summary, and copies (not aliases) the
// RecentRuns slice so callers can't mutate scheduler state.
func TestJobSnapshotIncludesRecentRuns(t *testing.T) {
	st := newTestStore(t)
	sch := NewScheduler(st, noopRunner, nil, nil, 1)
	if err := sch.CreateJob(validSpec("alpha")); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	ran := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	sch.mu.Lock()
	sch.state["alpha"] = &State{
		LastRunAt:  ran,
		LastStatus: StatusOK,
		RecentRuns: []RunRecord{
			{At: ran.Add(-time.Hour), Status: StatusSkipped},
			{At: ran, Status: StatusOK, ThreadID: "sess-1", Duration: "42s"},
		},
	}
	sch.mu.Unlock()

	snap := sch.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	j := snap[0]
	if j.LastStatus != StatusOK || !j.LastRunAt.Equal(ran) {
		t.Fatalf("last-run summary = %q / %v, want ok / %v", j.LastStatus, j.LastRunAt, ran)
	}
	if len(j.RecentRuns) != 2 {
		t.Fatalf("RecentRuns len = %d, want 2", len(j.RecentRuns))
	}
	if j.RecentRuns[1].ThreadID != "sess-1" || j.RecentRuns[1].Duration != "42s" {
		t.Fatalf("newest recent run = %+v", j.RecentRuns[1])
	}

	// Snapshot must copy the slice, not alias the live state.
	sch.mu.Lock()
	sch.state["alpha"].RecentRuns[0].Status = "MUTATED"
	sch.mu.Unlock()
	if snap[0].RecentRuns[0].Status == "MUTATED" {
		t.Fatal("Snapshot RecentRuns must be a copy, not alias scheduler state")
	}
}

// TestJobSnapshotNoHistory verifies a never-run job yields an empty history and
// zero-value last-run fields (so the UI shows "No runs yet", not fake rows).
func TestJobSnapshotNoHistory(t *testing.T) {
	st := newTestStore(t)
	sch := NewScheduler(st, noopRunner, nil, nil, 1)
	if err := sch.CreateJob(validSpec("alpha")); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	snap := sch.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	if len(snap[0].RecentRuns) != 0 || snap[0].LastStatus != "" {
		t.Fatalf("never-run job carried history: %+v", snap[0])
	}
}
