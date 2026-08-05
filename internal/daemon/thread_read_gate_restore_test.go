package daemon

import (
	"path/filepath"
	"testing"

	"github.com/get-vix/vix/internal/daemon/llm"
)

// newRestoreGateThread builds a minimal thread for exercising the read-gate
// rebuild path. Directory access is automatic so resolvePathInAllowed never
// rejects an absolute path under the temp cwd.
func newRestoreGateThread(t *testing.T) *Thread {
	t.Helper()
	return &Thread{
		cwd:                            t.TempDir(),
		enableAutomaticDirectoryAccess: true,
		readFiles:                      make(map[string]bool),
	}
}

func toolUse(id, name, path string) llm.ContentBlock {
	return llm.NewToolUseBlock(id, name, map[string]any{"path": path})
}

// TestRebuildReadFilesFromHistory verifies that restoring a thread's history
// re-marks files that were successfully read/edited/written, while leaving
// errored calls and calls with no result unmarked.
func TestRebuildReadFilesFromHistory(t *testing.T) {
	s := newRestoreGateThread(t)

	readOK := filepath.Join(s.cwd, "read_ok.go")
	editOK := filepath.Join(s.cwd, "edit_ok.go")
	writeOK := filepath.Join(s.cwd, "write_ok.go")
	minReadOK := filepath.Join(s.cwd, "min_read_ok.go")
	readErr := filepath.Join(s.cwd, "read_err.go")
	noResult := filepath.Join(s.cwd, "no_result.go")

	msgs := []llm.MessageParam{
		llm.NewAssistantMessage(
			toolUse("t1", "read_file", readOK),
			toolUse("t2", "edit_file", editOK),
			toolUse("t3", "write_file", writeOK),
			toolUse("t4", "read_minified_file", minReadOK),
			toolUse("t5", "read_file", readErr),
			toolUse("t6", "read_file", noResult),
		),
		llm.NewUserMessage(
			llm.NewToolResultBlock("t1", "ok", false),
			llm.NewToolResultBlock("t2", "ok", false),
			llm.NewToolResultBlock("t3", "ok", false),
			llm.NewToolResultBlock("t4", "ok", false),
			llm.NewToolResultBlock("t5", "boom", true),
			// t6 intentionally has no matching result.
		),
	}

	s.rebuildReadFilesFromHistory(msgs)

	marked := map[string]bool{
		readOK:    true,
		editOK:    true,
		writeOK:   true,
		minReadOK: true,
	}
	for p := range marked {
		if !s.hasBeenRead(p) {
			t.Errorf("expected %q to be marked read after restore", p)
		}
	}

	for _, p := range []string{readErr, noResult} {
		if s.hasBeenRead(p) {
			t.Errorf("expected %q to NOT be marked read (errored or resultless)", p)
		}
	}
}

// TestRebuildReadFilesFromHistory_UngatesEdit verifies the end-to-end effect:
// after restoring history that read a file, enforceReadGate no longer blocks an
// edit on it, whereas an unread file is still gated.
func TestRebuildReadFilesFromHistory_UngatesEdit(t *testing.T) {
	s := newRestoreGateThread(t)

	seen := filepath.Join(s.cwd, "seen.go")
	unseen := filepath.Join(s.cwd, "unseen.go")

	msgs := []llm.MessageParam{
		llm.NewAssistantMessage(toolUse("t1", "read_file", seen)),
		llm.NewUserMessage(llm.NewToolResultBlock("t1", "ok", false)),
	}
	s.rebuildReadFilesFromHistory(msgs)

	if res := s.enforceReadGate("edit_file", map[string]any{"path": seen}); res != nil {
		t.Errorf("edit on restored-read file should be allowed, got block: %v", res.Output)
	}
	if res := s.enforceReadGate("edit_file", map[string]any{"path": unseen}); res == nil {
		t.Error("edit on never-read file should still be gated")
	}
}
