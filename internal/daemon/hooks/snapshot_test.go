package hooks

import (
	"testing"
	"time"
)

// TestHookSnapshotFieldsAndRecentRuns verifies Snapshot surfaces the spec's
// authoring fields (blocking, cwd, timeout, description, command) plus the
// hook's persisted recent-fire history and last-fired summary.
func TestHookSnapshotFieldsAndRecentRuns(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "blk", `{"id":"blk","name":"Block rm","enabled":true,"trigger":{"event":"PreToolUse","matcher":"bash"},"mode":"sync","blocking":true,"command":"exit 2","cwd":"/work","timeout":"2s","description":"guards rm","created_by":"user:me"}`)
	r := NewRegistry(NewStore(dir))

	r.RecordRun("blk", RunRecord{At: time.Unix(100, 0), Status: "deny", Event: "PreToolUse", Duration: "12ms"})

	snap := r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	h := snap[0]
	if !h.Blocking || h.Mode != "sync" {
		t.Fatalf("blocking/mode = %v/%q, want true/sync", h.Blocking, h.Mode)
	}
	if h.Timeout != "2s" || h.CWD != "/work" || h.Description != "guards rm" || h.Command != "exit 2" {
		t.Fatalf("snapshot fields = %+v", h)
	}
	if len(h.RecentRuns) != 1 || h.RecentRuns[0].Status != "deny" || h.RecentRuns[0].Duration != "12ms" {
		t.Fatalf("recent runs = %+v", h.RecentRuns)
	}
	if h.LastFiredAt.Unix() != 100 {
		t.Fatalf("LastFiredAt = %v, want unix 100", h.LastFiredAt)
	}
}

// TestHookSnapshotTimeoutDefaultByMode verifies the resolved timeout string
// defaults by mode (sync 5s, async 10m) when the spec omits it — replacing the
// web UI's old hardcoded "10m".
func TestHookSnapshotTimeoutDefaultByMode(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "async-default", `{"id":"async-default","enabled":true,"trigger":{"event":"Stop"},"command":"true"}`)
	writeSpec(t, dir, "sync-default", `{"id":"sync-default","enabled":true,"trigger":{"event":"PreToolUse"},"mode":"sync","command":"true"}`)
	r := NewRegistry(NewStore(dir))

	byID := map[string]HookSnapshot{}
	for _, h := range r.Snapshot() {
		byID[h.ID] = h
	}
	if got := byID["async-default"].Timeout; got != "10m" {
		t.Fatalf("async default timeout = %q, want 10m", got)
	}
	if got := byID["sync-default"].Timeout; got != "5s" {
		t.Fatalf("sync default timeout = %q, want 5s", got)
	}
}

// TestHookSnapshotInlineWorkflow verifies an inline-workflow hook reports
// WorkflowInline so the UI can label it even with no command/prompt string.
func TestHookSnapshotInlineWorkflow(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "wf", `{"id":"wf","enabled":true,"trigger":{"event":"Stop"},"workflow":{"name":"inline","entry_point":{"id":"s"},"steps":{"s":{"type":"bash","command":"true"}}}}`)
	r := NewRegistry(NewStore(dir))

	snap := r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	if !snap[0].WorkflowInline || snap[0].Command != "" || snap[0].WorkflowID != "" {
		t.Fatalf("inline-workflow snapshot = %+v", snap[0])
	}
}
