package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/get-vix/vix/internal/daemon/mcp"
	"github.com/get-vix/vix/internal/protocol"
)

// mcpProbeTimeout bounds how long the MCP tab waits while dialing every enabled
// server. A slow or hung server yields an "error" status instead of blocking the
// whole listing.
const mcpProbeTimeout = 8 * time.Second

// homeSettingsPath returns the path to the daemon's home settings.json, where
// MCP servers are configured. MCP config is home-only (never project-local), so
// this single file is both the read and write target for the MCP tab.
func (s *Server) homeSettingsPath() string {
	return filepath.Join(s.homeVixDir, "settings.json")
}

// readHomeMCPServers parses the mcp_servers array from the home settings.json.
// A missing or unreadable file yields an empty slice (no servers configured).
func (s *Server) readHomeMCPServers() []mcp.ServerConfig {
	data, err := os.ReadFile(s.homeSettingsPath())
	if err != nil {
		return nil
	}
	var parsed struct {
		MCPServers []mcp.ServerConfig `json:"mcp_servers"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	return parsed.MCPServers
}

// MCPServerSummaries lists every configured MCP server for the MCP tab. Disabled
// servers are reported without a probe; enabled servers are dialed (with a
// bounded timeout) to report connection status and tool count. OAuth servers
// without a stored token are reported as "needs_auth" without a probe. Order
// follows the settings.json declaration order.
func (s *Server) MCPServerSummaries() []protocol.MCPServerSummary {
	configs := s.readHomeMCPServers()
	if len(configs) == 0 {
		return nil
	}

	// Probe only the enabled servers that can actually connect: non-OAuth
	// servers, plus OAuth servers that already have a stored token.
	var toProbe []mcp.ServerConfig
	for _, cfg := range configs {
		if cfg.Name == "" || !cfg.IsEnabled() {
			continue
		}
		if cfg.UsesOAuth() && !hasMCPToken(cfg.Name) {
			continue
		}
		toProbe = append(toProbe, cfg)
	}

	statuses := make(map[string]mcp.ServerStatus, len(toProbe))
	if len(toProbe) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), mcpProbeTimeout)
		defer cancel()
		for _, st := range mcp.ProbeServers(ctx, toProbe, mcp.WithTokenStore(newMCPTokenStore())) {
			statuses[st.Name] = st
		}
	}

	out := make([]protocol.MCPServerSummary, 0, len(configs))
	for _, cfg := range configs {
		if cfg.Name == "" {
			continue
		}
		sum := protocol.MCPServerSummary{
			Name:    cfg.Name,
			Enabled: cfg.IsEnabled(),
		}
		if cfg.UsesOAuth() {
			if hasMCPToken(cfg.Name) {
				sum.Auth = "authenticated"
			} else {
				sum.Auth = "needs_auth"
			}
		}
		if !cfg.IsEnabled() {
			sum.Status = "disabled"
			// Type is still useful in the disabled row.
			sum.Type = mcpNormalizedType(cfg)
			out = append(out, sum)
			continue
		}
		// OAuth server with no token yet: report needs_auth without a probe.
		if cfg.UsesOAuth() && !hasMCPToken(cfg.Name) {
			sum.Type = mcpNormalizedType(cfg)
			sum.Status = "needs_auth"
			out = append(out, sum)
			continue
		}
		st := statuses[cfg.Name]
		sum.Type = st.Type
		if sum.Type == "" {
			sum.Type = mcpNormalizedType(cfg)
		}
		sum.ToolCount = st.ToolCount
		if st.Connected {
			sum.Status = "connected"
		} else {
			sum.Status = "error"
			sum.Error = st.Error
		}
		out = append(out, sum)
	}
	return out
}

// mcpNormalizedType mirrors mcp transport normalization for summaries built
// without a probe result (disabled servers).
func mcpNormalizedType(cfg mcp.ServerConfig) string {
	switch cfg.Type {
	case "url", "http", "sse", "URL", "HTTP", "SSE":
		return "url"
	default:
		return "stdio"
	}
}

// SetMCPEnabled toggles the `enabled` field of the named MCP server in the home
// settings.json (a surgical in-place edit that preserves every other key) and
// notifies attached instances so the MCP tab refreshes. The change takes effect
// for threads started afterwards. Errors when the server is not found.
func (s *Server) SetMCPEnabled(name string, enabled bool) error {
	path := s.homeSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	serversRaw, ok := raw["mcp_servers"]
	if !ok {
		return fmt.Errorf("MCP server %q not found", name)
	}
	var servers []map[string]any
	if err := json.Unmarshal(serversRaw, &servers); err != nil {
		return fmt.Errorf("parse mcp_servers: %w", err)
	}
	found := false
	for _, srv := range servers {
		if n, _ := srv["name"].(string); n == name {
			srv["enabled"] = enabled
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("MCP server %q not found", name)
	}
	patched, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	raw["mcp_servers"] = patched
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	s.broadcastMCPChanged()
	return nil
}

// broadcastMCPChanged pushes event.mcp_changed to every live instance control
// connection so open MCP tabs re-fetch mcp.list.
func (s *Server) broadcastMCPChanged() {
	s.BroadcastToInstances(protocol.ThreadEvent{Type: "event.mcp_changed", Data: protocol.EventMCPChanged{}})
}
