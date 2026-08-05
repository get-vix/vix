package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStateAppendRunCapsAndOrders verifies the per-hook run history keeps the
// most recent maxRecentRuns entries, newest last.
func TestStateAppendRunCapsAndOrders(t *testing.T) {
	var st State
	for i := 0; i < maxRecentRuns+5; i++ {
		st.appendRun(RunRecord{At: time.Unix(int64(i), 0), Status: "allow"})
	}
	if len(st.RecentRuns) != maxRecentRuns {
		t.Fatalf("len(RecentRuns) = %d, want %d", len(st.RecentRuns), maxRecentRuns)
	}
	if got := st.RecentRuns[0].At.Unix(); got != 5 {
		t.Fatalf("oldest kept run At = %d, want 5", got)
	}
	if got := st.RecentRuns[len(st.RecentRuns)-1].At.Unix(); got != int64(maxRecentRuns+4) {
		t.Fatalf("newest run At = %d, want %d", got, maxRecentRuns+4)
	}
}

// TestStoreStateRoundTrip verifies a hook's state survives Save → Load and that
// DeleteState removes it.
func TestStoreStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "h", `{"id":"h","enabled":true,"trigger":{"event":"Stop"},"command":"true"}`)
	st := NewStore(dir)

	in := &State{LastStatus: "deny"}
	in.appendRun(RunRecord{At: time.Unix(1, 0), Status: "deny", Async: false, Event: "PreToolUse"})
	if err := st.SaveStateFor("h", in); err != nil {
		t.Fatal(err)
	}

	out := st.LoadState()["h"]
	if out == nil || out.LastStatus != "deny" || len(out.RecentRuns) != 1 || out.RecentRuns[0].Event != "PreToolUse" {
		t.Fatalf("loaded state = %+v, want one deny record for PreToolUse", out)
	}

	if err := st.DeleteState("h"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.LoadState()["h"]; ok {
		t.Fatal("state should be gone after DeleteState")
	}
	// Deleting a missing state file is not an error.
	if err := st.DeleteState("h"); err != nil {
		t.Fatalf("DeleteState on missing file should be nil, got %v", err)
	}
}

// TestRegistryRecordRunPersistsAndCaps verifies RecordRun appends to history,
// updates the last-* summary, persists to disk, and caps at maxRecentRuns.
func TestRegistryRecordRunPersistsAndCaps(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "h", `{"id":"h","enabled":true,"trigger":{"event":"Stop"},"command":"true"}`)
	store := NewStore(dir)
	r := NewRegistry(store)

	for i := 0; i < maxRecentRuns+3; i++ {
		r.RecordRun("h", RunRecord{At: time.Unix(int64(i), 0), Status: "done", Async: true})
	}

	got := r.StateByID("h")
	if got == nil || len(got.RecentRuns) != maxRecentRuns {
		t.Fatalf("StateByID = %+v, want %d recent runs", got, maxRecentRuns)
	}
	if got.LastStatus != "done" {
		t.Fatalf("LastStatus = %q, want done", got.LastStatus)
	}

	// It must have persisted to disk and survive a fresh registry.
	onDisk := store.LoadState()["h"]
	if onDisk == nil || len(onDisk.RecentRuns) != maxRecentRuns {
		t.Fatalf("persisted state = %+v, want %d recent runs", onDisk, maxRecentRuns)
	}
}

// TestRegistryStateSurvivesReloadAndPrunes verifies recorded history is
// preserved across a Reload, and dropped (with its state file) when the hook's
// spec vanishes from disk.
func TestRegistryStateSurvivesReloadAndPrunes(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "keep", `{"id":"keep","enabled":true,"trigger":{"event":"Stop"},"command":"true"}`)
	writeSpec(t, dir, "drop", `{"id":"drop","enabled":true,"trigger":{"event":"Stop"},"command":"true"}`)
	store := NewStore(dir)
	r := NewRegistry(store)

	r.RecordRun("keep", RunRecord{At: time.Unix(1, 0), Status: "done", Async: true})
	r.RecordRun("drop", RunRecord{At: time.Unix(1, 0), Status: "done", Async: true})

	// Remove the "drop" hook's spec from disk, then reload.
	if err := os.RemoveAll(filepath.Join(dir, "drop")); err != nil {
		t.Fatal(err)
	}
	r.Reload()

	if got := r.StateByID("keep"); got == nil || len(got.RecentRuns) != 1 {
		t.Fatalf("keep state lost across reload: %+v", got)
	}
	if got := r.StateByID("drop"); got != nil {
		t.Fatalf("drop state should be pruned after spec vanished, got %+v", got)
	}
	if _, ok := store.LoadState()["drop"]; ok {
		t.Fatal("drop state.json should be deleted from disk after spec vanished")
	}
}
