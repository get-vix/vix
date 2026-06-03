package mcp

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/get-vix/vix/internal/daemon/llm"
)

// toolEntry pairs a qualified tool name with the server that owns it.
type toolEntry struct {
	qualifiedName string // "mcp__<serverName>__<toolName>"
	serverName    string
	toolName      string
	def           ToolDef
}

// Pool manages MCP server connections for a single session.
// It is created in session.initBrain and torn down when the session context
// is cancelled (stdio child processes are killed via their exec.CommandContext).
type Pool struct {
	clients map[string]client // server name → client
	tools   []toolEntry
	// configs is kept for security checks (RequireConfirmation, AllowedTools).
	configs map[string]serverMeta
}

// serverMeta holds security-relevant config for a connected server.
type serverMeta struct {
	requireConfirmation bool
}

// NewPool connects to all configured MCP servers, runs the initialize+tools/list
// handshake for each, and returns a ready pool.
//
// Servers that fail to start are logged and skipped so a single broken server
// does not prevent the rest from working.
//
// The caller is responsible for deny-list URL filtering before passing configs;
// see session.initBrain which calls isURLDenied before adding an entry.
func NewPool(ctx context.Context, configs []ServerConfig) *Pool {
	p := &Pool{
		clients: make(map[string]client, len(configs)),
		configs: make(map[string]serverMeta, len(configs)),
	}

	for _, cfg := range configs {
		if cfg.Name == "" {
			continue
		}

		var c client
		var err error

		switch strings.ToLower(cfg.Type) {
		case "url", "http", "sse":
			if cfg.URL == "" {
				log.Printf("[mcp] server %q: type=%q requires a 'url' field, skipping", cfg.Name, cfg.Type)
				continue
			}
			c, err = newHTTPClient(cfg.Name, cfg.URL, cfg.Headers)
		default: // "stdio" or empty → stdio
			if cfg.Command == "" {
				log.Printf("[mcp] server %q: stdio transport requires a 'command' field, skipping", cfg.Name)
				continue
			}
			c, err = newStdioClient(ctx, cfg.Name, cfg.Command, cfg.Args, cfg.Env)
		}

		if err != nil {
			log.Printf("[mcp] server %q: failed to connect: %v (skipping)", cfg.Name, err)
			continue
		}

		p.clients[cfg.Name] = c
		p.configs[cfg.Name] = serverMeta{
			requireConfirmation: cfg.RequireConfirmation,
		}

		// Build tool entries, applying the AllowedTools filter.
		allowSet := make(map[string]bool, len(cfg.AllowedTools))
		for _, t := range cfg.AllowedTools {
			allowSet[t] = true
		}
		for _, tool := range c.ListTools() {
			if len(allowSet) > 0 && !allowSet[tool.Name] {
				continue
			}
			p.tools = append(p.tools, toolEntry{
				qualifiedName: "mcp__" + cfg.Name + "__" + tool.Name,
				serverName:    cfg.Name,
				toolName:      tool.Name,
				def:           tool,
			})
		}
	}

	return p
}

// ToolCount returns the total number of MCP tools available in the pool.
func (p *Pool) ToolCount() int {
	return len(p.tools)
}

// ServerCount returns the number of successfully connected MCP servers.
func (p *Pool) ServerCount() int {
	return len(p.clients)
}

// RequiresConfirmation reports whether the server that owns qualifiedName has
// require_confirmation: true.
func (p *Pool) RequiresConfirmation(qualifiedName string) bool {
	serverName := serverNameFrom(qualifiedName)
	if meta, ok := p.configs[serverName]; ok {
		return meta.requireConfirmation
	}
	return false
}

// ToolSchemas returns neutral llm.ToolParam definitions for every MCP tool
// in the pool. Tool names are the qualified form "mcp__<server>__<tool>".
func (p *Pool) ToolSchemas() []llm.ToolParam {
	schemas := make([]llm.ToolParam, 0, len(p.tools))
	for _, entry := range p.tools {
		props := entry.def.InputSchema["properties"]
		var required []string
		if r, ok := entry.def.InputSchema["required"].([]any); ok {
			for _, v := range r {
				if s, ok := v.(string); ok {
					required = append(required, s)
				}
			}
		}
		schema := map[string]any{
			"type":       "object",
			"properties": props,
		}
		if len(required) > 0 {
			schema["required"] = required
		}
		schemas = append(schemas, llm.ToolParam{
			Name:        entry.qualifiedName,
			Description: entry.def.Description,
			InputSchema: schema,
		})
	}
	return schemas
}

// Call dispatches a tool call to the appropriate MCP server and returns the
// result. qualifiedName must be of the form "mcp__<server>__<tool>".
// Internal vix params (cwd, allowed_dirs, headless, _session, confirmed) are
// stripped before forwarding to the server.
func (p *Pool) Call(qualifiedName string, args map[string]any) (string, bool, error) {
	serverName := serverNameFrom(qualifiedName)
	toolName := toolNameFrom(qualifiedName)
	if serverName == "" || toolName == "" {
		return "", true, fmt.Errorf("invalid MCP tool name: %q (expected mcp__<server>__<tool>)", qualifiedName)
	}

	c, ok := p.clients[serverName]
	if !ok {
		return "", true, fmt.Errorf("MCP server %q not found", serverName)
	}
	if !c.Alive() {
		return "", true, fmt.Errorf("MCP server %q is no longer running", serverName)
	}

	// Strip vix-internal params that the MCP server doesn't understand.
	clean := stripInternalParams(args)

	result, err := c.Call(toolName, clean)
	if err != nil {
		return "", true, err
	}
	return result.Output, result.IsError, nil
}

// Shutdown closes all MCP server connections.
func (p *Pool) Shutdown() {
	for name, c := range p.clients {
		log.Printf("[mcp] shutting down server %q", name)
		c.Close()
	}
	p.clients = nil
}

// serverNameFrom returns the server part of "mcp__server__tool".
func serverNameFrom(qualified string) string {
	parts := strings.SplitN(qualified, "__", 3)
	if len(parts) != 3 {
		return ""
	}
	return parts[1]
}

// toolNameFrom returns the tool part of "mcp__server__tool".
func toolNameFrom(qualified string) string {
	parts := strings.SplitN(qualified, "__", 3)
	if len(parts) != 3 {
		return ""
	}
	return parts[2]
}

// internalParams are vix-injected keys that must not be forwarded to MCP servers.
var internalParams = map[string]bool{
	"cwd":          true,
	"allowed_dirs": true,
	"headless":     true,
	"_session":     true,
	"confirmed":    true,
}

func stripInternalParams(params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		if !internalParams[k] {
			out[k] = v
		}
	}
	return out
}
