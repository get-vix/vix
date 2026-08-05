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

// This scenario exercises the "Schedule market research" job's inline workflow.
// The workflow's value comes from the rich source backends (Agent Reach + gh,
// Exa/mcporter, opencli/rdt, twitter-cli). Those are NOT present in the offline
// e2e container, so the run must fail LOUDLY with setup instructions at the
// detect → setup_needed gate rather than silently producing a thin brief.
//
// Graph under test: keywords (agent, one mock turn) → detect (bash, agent-reach
// absent → SETUP_NEEDED) → setup_needed (bash, prints guidance, exit 1).

// mrJobSpec is a one-shot market-research job (fire time in the past → runs once
// at startup) carrying the inline workflow. The bash commands use JSON \n / \"
// escapes, which the daemon's job loader unescapes into real newlines/quotes.
const mrJobSpec = `{
  "id": "e2e-market-research",
  "name": "Market research",
  "enabled": true,
  "trigger": {"type": "at", "time": "2000-01-01T00:00:00Z"},
  "prompt": "Research the AI developer tooling landscape.",
  "cwd": "{{WORKDIR}}",
  "permissions": {"auto_write": true, "auto_dirs": false},
  "created_by": "vix",
  "workflow": {
    "name": "market-research-weekly",
    "entry_point": {"id": "keywords"},
    "steps": {
      "keywords": {
        "type": "agent",
        "agent": "general",
        "deny_tools": ["write_file", "edit_file", "delete_file", "bash", "web_fetch", "web_search"],
        "prompt": "Output one short search query for: $(workflow.prompt)",
        "output": "keywords.txt",
        "next_steps": [{"id": "detect"}]
      },
      "detect": {
        "type": "bash",
        "command": "needs=\"\"\nadd() { echo \"  - $1\"; needs=1; }\nif ! command -v agent-reach >/dev/null 2>&1; then\n  add \"Agent Reach is not installed (the 'agent-reach' CLI).\"\n  echo SETUP_NEEDED\n  exit 0\nfi\ncommand -v gh >/dev/null 2>&1 || add \"GitHub: the 'gh' CLI is missing\"\nif [ -n \"$needs\" ]; then echo SETUP_NEEDED; else echo READY; fi",
        "next_steps": [
          {"id": "setup_needed", "execute_if": "[[ \"$(step.detect)\" == *SETUP_NEEDED* ]]"},
          {"id": "search", "execute_if": "[[ \"$(step.detect)\" != *SETUP_NEEDED* ]]"}
        ]
      },
      "setup_needed": {
        "type": "bash",
        "command": "echo \"Market research can't run: some source backends aren't set up yet.\"\necho \"To fix it, open a vix chat and ask: set up market research — vix will install Agent Reach (https://github.com/Panniantong/agent-reach).\"\nexit 1"
      },
      "search": {
        "type": "bash",
        "command": "echo searched"
      }
    },
    "budget": {"max_tokens": 400000}
  }
}`

// TestMarketResearchFailsLoudlyWithoutBackends verifies that the market-research
// job, run where its source backends aren't installed, records a failed run
// whose output carries the "set up market research" install guidance — and that
// no brief is written.
func TestMarketResearchFailsLoudlyWithoutBackends(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.market_research",
		Description: "market-research job fails loudly with setup instructions when its backends are missing",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-market-research/job.json", mrJobSpec),
	)

	// The keyword step makes exactly one LLM call; detect/setup_needed are bash,
	// and search/summarize/write are never reached on the fail-loud path.
	h.Mock.Enqueue(harness.Text("ai developer tooling trends"))
	h.UI.WaitStable(500 * time.Millisecond)

	// Wait until the one-shot run has been recorded in the job's state.json.
	statePath := h.HomePath(".vix/jobs/e2e-market-research/state.json")
	if !pollUntil(25*time.Second, func() bool {
		b, err := os.ReadFile(statePath)
		return err == nil && strings.Contains(string(b), `"recent_runs"`)
	}) {
		t.Fatalf("market-research run never recorded; vixd log:\n%s", h.Daemon.LogTail(80))
	}

	// The recorded run must be a failure (loud), not ok.
	var state struct {
		RecentRuns []struct {
			Status string `json:"status"`
		} `json:"recent_runs"`
	}
	raw, _ := os.ReadFile(statePath)
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decoding state.json: %v\nraw:\n%s", err, raw)
	}
	if len(state.RecentRuns) == 0 {
		t.Fatalf("no recent runs recorded:\n%s", raw)
	}
	if got := state.RecentRuns[len(state.RecentRuns)-1].Status; got != "error" {
		t.Fatalf("last run status = %q, want %q (the run must fail loudly):\n%s", got, "error", raw)
	}

	// The failure must carry the setup guidance (from the setup_needed step),
	// recorded in the job run-log. Read every daily jobs log file.
	logs := readAll(t, filepath.Join(h.HomePath(".vix/logs/jobs"), "*.jsonl"))
	if !strings.Contains(logs, "set up market research") || !strings.Contains(logs, "agent-reach") {
		t.Fatalf("run-log missing the setup guidance; jobs logs:\n%s\nvixd log:\n%s", logs, h.Daemon.LogTail(80))
	}

	// No brief is produced on the fail-loud path.
	if h.FS.Exists("research-brief.md") {
		t.Fatalf("research-brief.md should not exist after a failed run")
	}

	h.UI.Shot("market-research-setup-needed")
}

// readAll concatenates the contents of every file matching glob (newest-first
// order is irrelevant here — we only substring-match).
func readAll(t *testing.T, glob string) string {
	t.Helper()
	paths, _ := filepath.Glob(glob)
	var b strings.Builder
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			b.Write(data)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
