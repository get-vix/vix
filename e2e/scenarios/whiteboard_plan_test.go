package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// wbPlanJobSpec is a future-dated job carrying a self-contained inline workflow
// that mirrors the plan-mode whiteboard steps PR2 changed: an agent step emits
// the authored mermaid scenes to .vix/whiteboards/<thread>.json, then the
// whiteboard_open tool step converts them (mermaid → laid-out canvas scenes) and
// builds/open the whiteboard URL. Driving it via `vix job run` exercises the
// real daemon path without coupling to the interactive plan UI.
const wbPlanJobSpec = `{
  "id": "e2e-wb",
  "name": "Whiteboard plan",
  "enabled": true,
  "trigger": {"type": "at", "time": "2099-01-01T00:00:00Z"},
  "prompt": "Visualize the plan on the whiteboard.",
  "cwd": "{{WORKDIR}}",
  "created_by": "web",
  "permissions": {"auto_write": true, "auto_dirs": true},
  "workflow": {
    "name": "e2e-wb-flow",
    "entry_point": {"id": "gen"},
    "steps": {
      "gen": {
        "type": "agent",
        "agent": "general",
        "prompt": "Emit the whiteboard scenes as JSON.",
        "output": ".vix/whiteboards/$(thread.id).json",
        "stream": false,
        "next_steps": [{"id": "open"}]
      },
      "open": {
        "type": "tool",
        "tool": "whiteboard_open"
      }
    }
  }
}`

// TestPlanWhiteboardMermaidFlow drives the plan-whiteboard steps end to end: the
// agent emits a mermaid scenes array, the daemon writes it to disk, and the
// whiteboard_open tool converts + opens it. A successful run proves the tool
// parsed the mermaid array and built the URL (the browser open is a no-op in the
// sandbox); the persisted scenes file proves the new mermaid authoring format.
func TestPlanWhiteboardMermaidFlow(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "whiteboard",
		Subcategory: "whiteboard.plan",
		Description: "plan whiteboard: agent emits mermaid scenes, whiteboard_open converts + opens them",
		Wire:        harness.WireMessages,
	},
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-wb/job.json", wbPlanJobSpec),
	)

	// The gen agent step's text output (written to the scenes file) is the
	// authored mermaid scenes array in the new PR2 format.
	scenes := `[{"name":"Architecture","context":"the request path","mermaid":"graph LR\nA[Client] --> B[API]\nB --> C[(DB)]"}]`
	h.Mock.Enqueue(harness.Text(scenes))
	h.UI.WaitStable(500 * time.Millisecond)

	out, err := h.RunCLI("job", "run", "e2e-wb")
	if err != nil {
		t.Fatalf("vix job run failed: %v\n%s", err, out)
	}
	threadID := strings.TrimSpace(out)
	if threadID == "" {
		t.Fatalf("expected a thread id on stdout, got empty")
	}

	// The run must finish ok: whiteboard_open reads + converts the mermaid array
	// and builds the URL without error.
	var rec planRunRecord
	if !pollUntil(30*time.Second, func() bool {
		r, ok := planRunFor(h, "e2e-wb")
		if ok && r.JobStatus != "" {
			rec = r
			return true
		}
		return false
	}) {
		t.Fatalf("whiteboard plan run not persisted; stdout=%q\n%s", out, h.Daemon.LogTail(120))
	}
	if rec.JobStatus != "ok" {
		t.Fatalf("job status = %q, want ok (whiteboard_open failed?)\n%s", rec.JobStatus, h.Daemon.LogTail(120))
	}

	// The persisted scenes file is the authored mermaid array (new format).
	data := string(h.FS.Read(".vix/whiteboards/" + threadID + ".json"))
	if !strings.Contains(data, "\"mermaid\"") || !strings.Contains(data, "graph LR") {
		t.Errorf("scenes file should hold the authored mermaid array; got:\n%s", data)
	}
}
