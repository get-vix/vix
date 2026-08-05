package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/get-vix/vix/internal/daemon/hooks"
)

// TestSyncHookFireRecordsRecentRun verifies a synchronous command hook fire is
// recorded in the hook's recent-run history with its decision behaviour, not
// marked async.
func TestSyncHookFireRecordsRecentRun(t *testing.T) {
	s := newRunTriggerTestServer(t)
	hooksDir := filepath.Join(s.homeVixDir, "hooks")
	writeHookSpec(t, hooksDir, "rec",
		`{"id":"rec","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"PreToolUse"},"command":"exit 0"}`)
	s.hookRegistry = hooks.NewRegistry(hooks.NewStore(hooksDir))

	spec, ok := s.hookRegistry.SpecByID("rec")
	if !ok {
		t.Fatal("spec not loaded")
	}
	s.runSyncHook(context.Background(), spec, map[string]any{"event": "PreToolUse"})

	st := s.hookRegistry.StateByID("rec")
	if st == nil || len(st.RecentRuns) != 1 {
		t.Fatalf("sync fire not recorded: %+v", st)
	}
	if st.RecentRuns[0].Status != hooks.BehaviorAllow {
		t.Fatalf("recorded status = %q, want allow", st.RecentRuns[0].Status)
	}
	if st.RecentRuns[0].Async {
		t.Fatal("sync record must not be marked async")
	}
	if st.RecentRuns[0].Event != "PreToolUse" {
		t.Fatalf("recorded event = %q, want PreToolUse", st.RecentRuns[0].Event)
	}
}

// TestManualHookTriggerRecordsRecentRun verifies that `vix hook trigger <id>`
// (TriggerHook → fireHookAsync) records the run in the hook's history, marked
// async, and persists it to state.json.
func TestManualHookTriggerRecordsRecentRun(t *testing.T) {
	s := newRunTriggerTestServer(t)
	hooksDir := filepath.Join(s.homeVixDir, "hooks")
	writeHookSpec(t, hooksDir, "rec",
		`{"id":"rec","enabled":true,"mode":"async","trigger":{"event":"Stop"},"command":"exit 0"}`)
	s.hookRegistry = hooks.NewRegistry(hooks.NewStore(hooksDir))

	if _, _, err := s.TriggerHook("rec"); err != nil {
		t.Fatalf("TriggerHook: %v", err)
	}

	// The async fire persists its record; wait for it before TempDir cleanup.
	waitForFileContains(t, filepath.Join(hooksDir, "rec", "state.json"), `"async": true`)
}
