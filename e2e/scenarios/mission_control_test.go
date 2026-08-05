package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
	"golang.org/x/net/websocket"
)

// This file exercises the mission-control web payload that backs the jobs/hooks
// detail pages. The daemon's /ws snapshot must carry *real* job-run and
// hook-fire history (plus the real hook fields) — the web UI's static mock data
// (gh-issue-triage, block-rm-rf, sess-ght-…) must never appear on this wire.
//
// The harness disables mission control and jobs by default; this scenario opts
// both back in and pins the web port so it can read the live snapshot.

const mcWebPort = "39137"

// mcJobSpec is a one-shot job whose fire time is in the past, so it runs once at
// startup and records a real run in its history (surfaced in the snapshot).
const mcJobSpec = `{
  "id": "e2e-mc-job",
  "name": "E2E Mission Control Job",
  "enabled": true,
  "trigger": {"type": "at", "time": "2000-01-01T00:00:00Z"},
  "prompt": "Say hello.",
  "cwd": "{{WORKDIR}}",
  "created_by": "vix"
}`

// mcHookSpec is a DISABLED hook (so it never fires during the test) carrying the
// fields that used to be missing/faked in the web UI: blocking, a non-default
// timeout, cwd, and a description.
const mcHookSpec = `{
  "id": "e2e-mc-hook",
  "name": "E2E Mission Control Hook",
  "enabled": false,
  "trigger": {"event": "PreToolUse", "matcher": "bash"},
  "mode": "sync",
  "blocking": true,
  "command": "exit 0",
  "cwd": "{{WORKDIR}}",
  "timeout": "2s",
  "description": "guards destructive commands",
  "created_by": "user:e2e"
}`

// mcHookState seeds a recent-fire history so the snapshot has firings to expose
// without the hook ever firing live.
const mcHookState = `{
  "last_fired_at": "2026-06-16T09:00:00Z",
  "last_status": "deny",
  "recent_runs": [
    {"at": "2026-06-16T09:00:00Z", "status": "deny", "async": false, "event": "PreToolUse", "duration": "12ms"}
  ]
}`

// wsSnapshot mirrors the fields of the daemon's /ws envelope this test asserts.
type wsSnapshot struct {
	Jobs []struct {
		ID         string `json:"id"`
		RecentRuns []struct {
			Status   string `json:"status"`
			ThreadID string `json:"thread_id"`
		} `json:"recent_runs"`
	} `json:"jobs"`
	Hooks []struct {
		ID          string `json:"id"`
		Blocking    bool   `json:"blocking"`
		Timeout     string `json:"timeout"`
		CWD         string `json:"cwd"`
		Description string `json:"description"`
		RecentRuns  []struct {
			Status string `json:"status"`
			Event  string `json:"event"`
		} `json:"recent_runs"`
	} `json:"hooks"`
}

// TestMissionControlSnapshotIsReal verifies the /ws payload carries real job-run
// and hook-fire history and the real hook fields (timeout != the old hardcoded
// "10m", blocking, cwd, description), and that none of the web UI's mock
// sentinels appear on the wire.
func TestMissionControlSnapshotIsReal(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "mission_control",
		Subcategory: "mission_control.snapshot",
		Description: "the /ws snapshot exposes real job/hook history and fields, no mock data",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithEnv("VIX_NO_MISSION_CONTROL", "0"),
		harness.WithEnv("VIX_WEB_PORT", mcWebPort),
		harness.WithHomeFile(".vix/jobs/e2e-mc-job/job.json", mcJobSpec),
		harness.WithHomeFile(".vix/hooks/e2e-mc-hook/hook.json", mcHookSpec),
		harness.WithHomeFile(".vix/hooks/e2e-mc-hook/state.json", mcHookState),
	)

	// The one-shot job fires at startup and runs against the mock.
	h.Mock.Enqueue(harness.Text("hello from the scheduled job"))
	h.UI.WaitStable(500 * time.Millisecond)

	// Wait until the job's run has been recorded (its state.json gains history),
	// which means the in-memory snapshot also carries it.
	statePath := h.HomePath(".vix/jobs/e2e-mc-job/state.json")
	if !pollUntil(20*time.Second, func() bool {
		b, err := os.ReadFile(statePath)
		return err == nil && strings.Contains(string(b), `"recent_runs"`)
	}) {
		t.Fatalf("job recent_runs never recorded; vixd log:\n%s", h.Daemon.LogTail(80))
	}

	// Read the live snapshot off the mission-control WebSocket.
	raw, err := readWSSnapshot(mcWebPort)
	if err != nil {
		t.Fatalf("reading /ws snapshot: %v\nvixd log:\n%s", err, h.Daemon.LogTail(80))
	}

	// No mock sentinels may ride the real wire.
	for _, sentinel := range []string{"gh-issue-triage", "block-rm-rf", "sess-ght-", "weekly-market-research"} {
		if strings.Contains(raw, sentinel) {
			t.Fatalf("mock sentinel %q leaked into the real /ws payload:\n%s", sentinel, raw)
		}
	}

	var snap wsSnapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		t.Fatalf("decoding /ws payload: %v\nraw:\n%s", err, raw)
	}

	// Job: real run history with an ok status and a real thread id.
	var job *struct {
		ID         string `json:"id"`
		RecentRuns []struct {
			Status   string `json:"status"`
			ThreadID string `json:"thread_id"`
		} `json:"recent_runs"`
	}
	for i := range snap.Jobs {
		if snap.Jobs[i].ID == "e2e-mc-job" {
			job = &snap.Jobs[i]
		}
	}
	if job == nil {
		t.Fatalf("job e2e-mc-job missing from snapshot:\n%s", raw)
	}
	if len(job.RecentRuns) == 0 || job.RecentRuns[len(job.RecentRuns)-1].Status != "ok" {
		t.Fatalf("job recent_runs missing an ok run: %+v", job.RecentRuns)
	}
	if job.RecentRuns[len(job.RecentRuns)-1].ThreadID == "" {
		t.Fatalf("job run carried no thread id: %+v", job.RecentRuns)
	}

	// Hook: real fields (timeout is the spec's "2s", not the old hardcoded
	// "10m") plus the seeded fire history.
	var hook *struct {
		ID          string `json:"id"`
		Blocking    bool   `json:"blocking"`
		Timeout     string `json:"timeout"`
		CWD         string `json:"cwd"`
		Description string `json:"description"`
		RecentRuns  []struct {
			Status string `json:"status"`
			Event  string `json:"event"`
		} `json:"recent_runs"`
	}
	for i := range snap.Hooks {
		if snap.Hooks[i].ID == "e2e-mc-hook" {
			hook = &snap.Hooks[i]
		}
	}
	if hook == nil {
		t.Fatalf("hook e2e-mc-hook missing from snapshot:\n%s", raw)
	}
	if hook.Timeout != "2s" {
		t.Fatalf("hook timeout = %q, want the real %q (not a hardcoded default)", hook.Timeout, "2s")
	}
	if !hook.Blocking || hook.CWD == "" || hook.Description != "guards destructive commands" {
		t.Fatalf("hook fields not surfaced: %+v", hook)
	}
	if len(hook.RecentRuns) != 1 || hook.RecentRuns[0].Status != "deny" || hook.RecentRuns[0].Event != "PreToolUse" {
		t.Fatalf("hook recent_runs not surfaced: %+v", hook.RecentRuns)
	}

	h.UI.Shot("mission-control-snapshot")
}

// readWSSnapshot dials the mission-control WebSocket and returns the first
// payload the server pushes (the full snapshot it sends on connect).
func readWSSnapshot(port string) (string, error) {
	var lastErr error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ws, err := websocket.Dial(fmt.Sprintf("ws://127.0.0.1:%s/ws", port), "", "http://127.0.0.1/")
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		var payload string
		err = websocket.Message.Receive(ws, &payload)
		ws.Close()
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		return payload, nil
	}
	return "", fmt.Errorf("no /ws payload within deadline: %w", lastErr)
}
