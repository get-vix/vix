package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// This file exercises the Settings tab (F5) "Tools" section, which surfaces the
// grep/glob search backends the daemon actually resolved (event.tool_backends).
// It proves the daemon→client reporting path and the read-only render, including
// the PATH-fallback annotation when a configured backend isn't installed.

// settingsToolsSpec requests the rg/glob=fd backends. In the offline test image
// neither tool is installed, so the daemon falls back to grep/builtin and the
// Settings tab annotates the fallback. On a dev machine that has rg/fd the rows
// simply show rg/fd — the assertions below accept either outcome.
const settingsToolsSettings = `{
  "version": 1,
  "tools": {
    "grep": { "backend": "rg" },
    "glob": { "backend": "fd" }
  }
}`

// TestSettingsTabShowsToolBackends verifies F6 opens Settings with a Tools
// section listing the effective grep and glob backends reported by the daemon.
func TestSettingsTabShowsToolBackends(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "ui",
		Subcategory: "ui.settings",
		Description: "F6 Settings tab shows the resolved grep/glob search backends (event.tool_backends)",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile(".vix/settings.json", settingsToolsSettings),
	)

	h.UI.WaitStable(500 * time.Millisecond)

	// event.tool_backends is emitted when a thread initializes (initBrain), so
	// start one with a quick turn before opening Settings — otherwise the tab
	// shows "unknown" backends because no thread ever reported them.
	h.Mock.Enqueue(harness.Text("ready"))
	h.UI.Type("hello")
	h.UI.Enter()
	h.UI.WaitFor("ready")

	h.UI.Key("f6")
	// Wait on a Settings-body-only string; the tab bar always contains
	// "Settings [F6]", so waiting on that would false-positive before the body
	// paints.
	h.UI.WaitFor("Auto-compaction")
	h.UI.WaitFor("Grep backend")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("settings-tools")

	for _, want := range []string{
		"Settings [F6]", // tab bar
		"Tools",         // section header
		"Grep backend",
		"Glob backend",
	} {
		if !h.UI.Contains(want) {
			t.Fatalf("Settings tab missing %q; screen:\n%s", want, h.UI.Snapshot())
		}
	}

	// The grep row shows the effective backend: rg when installed, else grep
	// (with a fallback annotation). Likewise glob shows fd or builtin.
	screen := h.UI.Snapshot()
	if !strings.Contains(screen, "grep") && !strings.Contains(screen, "rg") {
		t.Fatalf("Grep backend row shows no recognized backend; screen:\n%s", screen)
	}
	if !strings.Contains(screen, "builtin") && !strings.Contains(screen, "fd") {
		t.Fatalf("Glob backend row shows no recognized backend; screen:\n%s", screen)
	}

	// When a configured backend isn't on PATH the row annotates the fallback.
	// The offline image has neither rg nor fd, so both annotations appear there;
	// a dev machine with the tools installed shows none. Accept both, but when an
	// annotation is present it must name the configured tool.
	if strings.Contains(screen, "configured — not found in PATH") {
		if !strings.Contains(screen, "(rg configured") && !strings.Contains(screen, "(fd configured") {
			t.Fatalf("fallback annotation present but doesn't name the configured backend; screen:\n%s", screen)
		}
	}
}
