package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/protocol"
)

// writeHomeSettingsMCP writes a settings.json with the given raw JSON body into
// the server's home config dir.
func writeHomeSettingsMCP(t *testing.T, s *Server, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.homeVixDir, "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMCPServerSummaries(t *testing.T) {
	s := newRunTriggerTestServer(t)
	writeHomeSettingsMCP(t, s, `{
      "theme": "dark",
      "mcp_servers": [
        {"name": "broken", "type": "stdio", "command": "vix-nonexistent-binary-xyz"},
        {"name": "off", "type": "url", "url": "http://127.0.0.1:1/mcp", "enabled": false}
      ]
    }`)

	sums := s.MCPServerSummaries()
	if len(sums) != 2 {
		t.Fatalf("expected 2 summaries, got %d: %+v", len(sums), sums)
	}
	byName := map[string]protocol.MCPServerSummary{}
	for _, sum := range sums {
		byName[sum.Name] = sum
	}

	broken := byName["broken"]
	if broken.Status != "error" || broken.Enabled != true || broken.Type != "stdio" {
		t.Errorf("broken summary = %+v, want enabled stdio error", broken)
	}
	if broken.Error == "" {
		t.Error("broken server should carry a connection error")
	}

	off := byName["off"]
	if off.Status != "disabled" || off.Enabled != false || off.Type != "url" {
		t.Errorf("off summary = %+v, want disabled url (not probed)", off)
	}
}

func TestSetMCPEnabledSurgical(t *testing.T) {
	s := newRunTriggerTestServer(t)
	writeHomeSettingsMCP(t, s, `{
      "theme": "dark",
      "mcp_servers": [
        {"name": "srv", "type": "stdio", "command": "foo", "require_confirmation": true}
      ]
    }`)

	if err := s.SetMCPEnabled("srv", false); err != nil {
		t.Fatalf("SetMCPEnabled: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(s.homeVixDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Theme      string           `json:"theme"`
		MCPServers []map[string]any `json:"mcp_servers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Theme != "dark" {
		t.Errorf("unrelated key dropped: theme = %q", raw.Theme)
	}
	if len(raw.MCPServers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(raw.MCPServers))
	}
	srv := raw.MCPServers[0]
	if srv["enabled"] != false {
		t.Errorf("enabled = %v, want false", srv["enabled"])
	}
	if srv["require_confirmation"] != true {
		t.Errorf("sibling field dropped by surgical edit: require_confirmation = %v", srv["require_confirmation"])
	}

	// Re-enabling flips it back.
	if err := s.SetMCPEnabled("srv", true); err != nil {
		t.Fatalf("SetMCPEnabled(true): %v", err)
	}
	// Unknown server errors.
	if err := s.SetMCPEnabled("ghost", false); err == nil {
		t.Error("expected error for unknown server")
	}
}

func TestMCPHandlers(t *testing.T) {
	s := newRunTriggerTestServer(t)
	writeHomeSettingsMCP(t, s, `{
      "mcp_servers": [
        {"name": "srv", "type": "stdio", "command": "foo"}
      ]
    }`)

	resp, err := s.GetHandler("mcp.list")(map[string]any{})
	if err != nil {
		t.Fatalf("mcp.list err: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("mcp.list status = %v", resp["status"])
	}
	list, _ := resp["servers"].([]protocol.MCPServerSummary)
	if len(list) != 1 || list[0].Name != "srv" {
		t.Fatalf("mcp.list servers = %+v", list)
	}

	resp, _ = s.GetHandler("mcp.set_enabled")(map[string]any{"name": "srv", "enabled": false})
	if resp["status"] != "ok" {
		t.Fatalf("mcp.set_enabled status = %v", resp["status"])
	}

	// Missing name errors.
	resp, _ = s.GetHandler("mcp.set_enabled")(map[string]any{"enabled": true})
	if resp["status"] != "error" {
		t.Fatalf("missing name should error, got %v", resp)
	}
}

// TestMCPServersSurviveBootstrap verifies the invariant the e2e MCP tab test
// relies on: vixd's home bootstrap must not wipe a user's seeded mcp_servers.
// Because managed-defaults refresh now merges settings.json (rather than
// clobbering it), mcp_servers survive a version change with no special stamp,
// and MCPServerSummaries still sees them.
func TestMCPServersSurviveBootstrap(t *testing.T) {
	home := t.TempDir()
	settings := `{
      "version": 1,
      "mcp_servers": [
        {"name": "alpha", "type": "stdio", "command": "vix-nonexistent-mcp"},
        {"name": "bravo", "type": "url", "url": "http://127.0.0.1:1/mcp", "enabled": false}
      ]
    }`
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	// A fresh home has no defaults_version stamp, so bootstrap takes the
	// version-change (refresh) path — which must MERGE, preserving mcp_servers.
	if err := config.BootstrapHomeVixDir(home, "dev"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(home, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"mcp_servers"`) {
		t.Fatalf("mcp_servers wiped by bootstrap:\n%s", got)
	}

	s := &Server{handlers: make(map[string]HandlerFunc), homeVixDir: home}
	sums := s.MCPServerSummaries()
	if len(sums) != 2 {
		t.Fatalf("expected 2 servers after bootstrap, got %d: %+v", len(sums), sums)
	}
}
