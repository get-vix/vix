package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// readRunLogLines returns the decoded JSON objects from dir/<today>.jsonl.
func readRunLogLines(t *testing.T, dir string) []map[string]any {
	t.Helper()
	name := time.Now().UTC().Format("2006-01-02") + ".jsonl"
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("open run log: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("decode line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func TestAppendRunLog(t *testing.T) {
	dir := t.TempDir()
	appendRunLog(dir, map[string]any{"phase": "started", "job_id": "alpha"})
	appendRunLog(dir, map[string]any{"phase": "finished", "job_id": "alpha", "status": "ok"})

	lines := readRunLogLines(t, dir)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0]["phase"] != "started" || lines[1]["phase"] != "finished" {
		t.Errorf("unexpected phases: %v / %v", lines[0]["phase"], lines[1]["phase"])
	}
	if _, ok := lines[0]["ts"]; !ok {
		t.Error("expected an auto-stamped ts field")
	}
}

func TestAppendRunLogEmptyDirNoPanic(t *testing.T) {
	// Empty dir is a no-op (best-effort): must not panic or create anything.
	appendRunLog("", map[string]any{"phase": "started"})
}

func TestSweepRunLogs(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	today := time.Now().UTC()
	stale := today.AddDate(0, 0, -30).Format("2006-01-02") + ".jsonl"
	fresh := today.AddDate(0, 0, -1).Format("2006-01-02") + ".jsonl"
	mk(stale)
	mk(fresh)
	mk("not-a-log.txt")

	sweepRunLogs(dir, 10)

	if _, err := os.Stat(filepath.Join(dir, stale)); !os.IsNotExist(err) {
		t.Error("stale file should have been swept")
	}
	if _, err := os.Stat(filepath.Join(dir, fresh)); err != nil {
		t.Error("fresh file should have survived")
	}
	if _, err := os.Stat(filepath.Join(dir, "not-a-log.txt")); err != nil {
		t.Error("non-log file should be left alone")
	}
}

func TestSweepRunLogsDisabled(t *testing.T) {
	dir := t.TempDir()
	stale := time.Now().UTC().AddDate(0, 0, -100).Format("2006-01-02") + ".jsonl"
	if err := os.WriteFile(filepath.Join(dir, stale), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sweepRunLogs(dir, 0)  // disabled — keep forever
	sweepRunLogs(dir, -5) // also disabled
	if _, err := os.Stat(filepath.Join(dir, stale)); err != nil {
		t.Error("retention <= 0 must keep files forever")
	}
	// Missing dir is a no-op, not a panic.
	sweepRunLogs(filepath.Join(dir, "does-not-exist"), 10)
}
