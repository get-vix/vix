package scenarios

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// This file exercises the MCP tab (F4): it lists the MCP servers configured in
// the home settings.json with their transport type, connection status, and tool
// count, and toggles a server's enabled state with space (a surgical edit of
// settings.json). MCP servers are home-only, so the tab is daemon-global.
//
// The two seeded servers never actually connect (offline image, no MCP server
// binary): the enabled one reports "error", the disabled one is listed without
// a probe. That is enough to exercise the listing, status rendering, and the
// enable/disable toggle path end to end.
const mcpTabSettings = `{
  "version": 1,
  "mcp_servers": [
    {"name": "alpha", "type": "stdio", "command": "vix-nonexistent-mcp"},
    {"name": "bravo", "type": "url", "url": "http://127.0.0.1:1/mcp", "enabled": false}
  ]
}`

// TestMCPTabListsAndToggles verifies F4 opens the MCP tab listing the configured
// servers (type + status), and that space toggles the selected server off —
// persisted to the home settings.json.
func TestMCPTabListsAndToggles(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "ui",
		Subcategory: "ui.mcp",
		Description: "F4 lists MCP servers (type + status); space toggles a server off, persisted to settings.json",
		Wire:        harness.WireMessages,
	},
		// MCP config is home-only. vixd's bootstrap merges (rather than
		// overwrites) settings.json on refresh, so our seeded mcp_servers survive.
		harness.WithHomeFile(".vix/settings.json", mcpTabSettings),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f4")
	// Wait on an MCP-body-only string; the tab bar always contains "MCP [F4]",
	// so waiting on that would false-positive before the body paints.
	h.UI.WaitFor("MCP Servers")
	h.UI.WaitFor("alpha")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("mcp-tab")

	for _, want := range []string{
		"MCP [F4]", "Jobs & Triggers [F5]", "Settings [F6]", // tab bar after remap
		"MCP Servers",    // section header
		"alpha", "bravo", // both server rows
		"stdio", "url", // transport types
		"disabled",   // the disabled server's status
		"[✓]", "[ ]", // enabled + disabled checkboxes
	} {
		if !h.UI.Contains(want) {
			t.Fatalf("MCP tab missing %q; screen:\n%s", want, h.UI.Snapshot())
		}
	}

	// The cursor starts on the first server (alpha, enabled); space toggles it off.
	h.UI.Key("space")
	settingsPath := h.HomePath(".vix/settings.json")
	readSettings := func() string {
		b, _ := os.ReadFile(settingsPath)
		return string(b)
	}
	if !pollUntil(8*time.Second, func() bool {
		s := readSettings()
		return strings.Contains(s, `"alpha"`) && strings.Contains(s, `"enabled": false`)
	}) {
		t.Fatalf("settings.json at %s not flipped to disabled; got:\n%s", settingsPath, readSettings())
	}
	// The list refreshes live via event.mcp_changed and shows alpha disabled.
	if !pollUntil(5*time.Second, func() bool {
		screen := h.UI.Snapshot()
		// After toggling alpha off, both servers are now disabled.
		return strings.Count(screen, "disabled") >= 2
	}) {
		t.Fatalf("alpha not shown disabled after toggle; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("mcp-tab-toggled")
}
