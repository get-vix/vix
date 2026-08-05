package scenarios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// Run-log e2e scenarios. Every hook fire (and, when enabled, every job run) is
// recorded as append-only JSONL under ~/.vix/logs/{hooks,jobs}/<UTC-date>.jsonl,
// with phase lines fired→[error]→finished. These tests drive a real hook and
// assert the daemon wrote the expected lines, plus that startup retention sweeps
// stale daily files.

// hookLogLines reads and decodes today's hook run-log lines (empty if absent).
func hookLogLines(t *testing.T, h *harness.Harness) []map[string]any {
	t.Helper()
	name := time.Now().UTC().Format("2006-01-02") + ".jsonl"
	data, err := os.ReadFile(h.HomePath(".vix", "logs", "hooks", name))
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			// The daemon appends concurrently; a transient partial trailing line
			// during polling is skipped rather than failing the whole test.
			continue
		}
		out = append(out, m)
	}
	return out
}

// hasHookPhase reports whether any line for the given hook_id has the phase
// (and optional source). Scoping by hook_id is essential: the daemon also
// auto-seeds the default feedback hook, whose own lines share this log file.
func hasHookPhase(lines []map[string]any, hookID, phase, source string) bool {
	for _, m := range lines {
		if m["hook_id"] != hookID || m["phase"] != phase {
			continue
		}
		if source == "" || m["source"] == source {
			return true
		}
	}
	return false
}

// runlogAsyncHook touches a flag so the test can sync on the fire, while the
// daemon records fired/finished lines for it.
const runlogAsyncHook = `{
  "id": "runlog-async",
  "enabled": true,
  "mode": "async",
  "trigger": { "event": "PostToolUse", "matcher": "bash" },
  "command": "touch runlog-async.flag"
}`

// TestHookRunLogRecordsLifecycle proves an async hook fire is recorded as a
// fired→finished pair in ~/.vix/logs/hooks/<date>.jsonl.
func TestHookRunLogRecordsLifecycle(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "observability",
		Subcategory: "runlog.hook_lifecycle",
		Description: "an async hook fire is recorded as fired→finished lines in the hooks run log",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/runlog-async/hook.json", runlogAsyncHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"echo hi"}`),
		harness.Text("Done."),
	)
	h.UI.Type("run echo hi")
	h.UI.Enter()
	h.WaitForLLMRequests(2)

	if !pollUntil(10*time.Second, func() bool { return h.FS.Exists("runlog-async.flag") }) {
		t.Fatalf("async hook side effect never appeared")
	}
	if !pollUntil(10*time.Second, func() bool {
		lines := hookLogLines(t, h)
		return hasHookPhase(lines, "runlog-async", "fired", "") && hasHookPhase(lines, "runlog-async", "finished", "")
	}) {
		t.Fatalf("hook run log missing fired/finished lines: %v", hookLogLines(t, h))
	}
	h.UI.Shot("hook-runlog")

	// The runlog-async fired line must carry the correlation fields. Other hooks
	// (e.g. the seeded feedback hook) also write to this file, so match by id.
	found := false
	for _, m := range hookLogLines(t, h) {
		if m["hook_id"] == "runlog-async" && m["phase"] == "fired" {
			found = true
			if m["event"] != "PostToolUse" {
				t.Fatalf("fired line wrong event: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("no fired line for runlog-async in %v", hookLogLines(t, h))
	}
}

// runlogTimeoutHook is a sync hook that overruns its 1s timeout, forcing a
// command_exec error line (exit code -1, fail-open allow).
const runlogTimeoutHook = `{
  "id": "runlog-timeout",
  "enabled": true,
  "mode": "sync",
  "timeout": "1s",
  "trigger": { "event": "PostToolUse", "matcher": "bash" },
  "command": "sleep 5"
}`

// TestHookRunLogRecordsError proves a hook execution failure (a command timeout)
// is recorded as a phase:"error" line with source "command_exec".
func TestHookRunLogRecordsError(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "observability",
		Subcategory: "runlog.hook_error",
		Description: "a timing-out command hook records a command_exec error line in the hooks run log",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/runlog-timeout/hook.json", runlogTimeoutHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"echo hi"}`),
		harness.Text("Done."),
	)
	h.UI.Type("run echo hi")
	h.UI.Enter()
	h.WaitForLLMRequests(2)

	if !pollUntil(15*time.Second, func() bool {
		return hasHookPhase(hookLogLines(t, h), "runlog-timeout", "error", "command_exec")
	}) {
		t.Fatalf("hook run log missing command_exec error line: %v", hookLogLines(t, h))
	}
	h.UI.Shot("hook-runlog-error")
}

// TestRunLogRetentionSweep proves the daemon prunes stale daily run-log files at
// startup, keeping recent ones. A 2000-dated file is seeded under both jobs/ and
// hooks/; after the daemon comes up it must be gone.
func TestRunLogRetentionSweep(t *testing.T) {
	const staleName = "2000-01-01.jsonl"
	h := harness.Start(t, harness.Meta{
		Category:    "observability",
		Subcategory: "runlog.retention",
		Description: "the daemon sweeps run-log daily files older than the retention window at startup",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile(".vix/settings.json", `{"version":1,"logs":{"retention_days":10}}`),
		harness.WithHomeFile(filepath.Join(".vix/logs/jobs", staleName), `{"phase":"finished","job_id":"old"}`+"\n"),
		harness.WithHomeFile(filepath.Join(".vix/logs/hooks", staleName), `{"phase":"finished","fire_id":"old"}`+"\n"),
	)

	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Shot("retention")

	staleJobs := h.HomePath(".vix", "logs", "jobs", staleName)
	staleHooks := h.HomePath(".vix", "logs", "hooks", staleName)
	if !pollUntil(10*time.Second, func() bool {
		_, e1 := os.Stat(staleJobs)
		_, e2 := os.Stat(staleHooks)
		return os.IsNotExist(e1) && os.IsNotExist(e2)
	}) {
		t.Fatalf("stale run-log files were not swept at startup")
	}
}
