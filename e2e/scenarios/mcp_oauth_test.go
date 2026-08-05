package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// This scenario exercises the OAuth surface of the MCP tab (F4). An MCP url
// server configured with an `oauth` block but no stored token must be reported
// as "needs auth" rather than a hard connection error, and the tab must advertise
// the authenticate keybinding. The flow is hermetic: because the server has no
// token, MCPServerSummaries reports needs_auth WITHOUT probing the network, so no
// outbound connection is attempted in the offline test image.
const mcpOAuthSettings = `{
  "version": 1,
  "mcp_servers": [
    {"name": "drive", "type": "url", "url": "https://drivemcp.example/mcp/v1",
     "oauth": {"client_id": "test-client",
               "auth_url": "https://accounts.example/authorize",
               "token_url": "https://accounts.example/token",
               "scopes": ["drive.file"]}}
  ]
}`

// TestMCPTabOAuthNeedsAuth verifies F4 shows an unauthenticated OAuth server as
// "needs auth" and advertises the authenticate action.
func TestMCPTabOAuthNeedsAuth(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "ui",
		Subcategory: "ui.mcp",
		Description: "F4 shows an OAuth MCP server without a token as 'needs auth' and advertises the authenticate key",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile(".vix/settings.json", mcpOAuthSettings),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f4")
	h.UI.WaitFor("MCP Servers")
	h.UI.WaitFor("drive")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("mcp-tab-oauth")

	for _, want := range []string{
		"drive",        // the oauth server row
		"url",          // transport
		"needs auth",   // oauth state (no token stored)
		"authenticate", // the hint line advertising the 'a' key
	} {
		if !h.UI.Contains(want) {
			t.Fatalf("MCP tab missing %q; screen:\n%s", want, h.UI.Snapshot())
		}
	}
}
