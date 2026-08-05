package mcp

import "encoding/json"

// ServerConfig describes a single MCP server entry from settings.json.
type ServerConfig struct {
	// Name is a unique identifier used to prefix tool names: mcp__<name>__<tool>.
	Name string `json:"name"`
	// Type is the transport: "stdio" (default) or "url".
	Type string `json:"type,omitempty"`
	// Command and Args are used for stdio servers.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Env adds extra environment variables for the stdio child process.
	Env map[string]string `json:"env,omitempty"`
	// URL is used for HTTP/SSE servers.
	URL string `json:"url,omitempty"`
	// Headers are sent with every HTTP request (e.g. Authorization).
	// Values of the form "${VAR}" are expanded from the environment at connect time.
	Headers map[string]string `json:"headers,omitempty"`
	// OAuth, when set on a url server, enables an OAuth 2.0 authorization-code
	// flow: the access token is injected as an Authorization: Bearer header and
	// refreshed automatically. Endpoints are auto-discovered (RFC 9728 + RFC
	// 8414) unless AuthURL/TokenURL are set explicitly.
	OAuth *OAuthConfig `json:"oauth,omitempty"`
	// AllowedTools, when non-empty, restricts which tools from this server are
	// exposed to the LLM. Unlisted tools are silently dropped after tools/list.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// RequireConfirmation makes every tool call from this server require explicit
	// user approval via the standard confirm_request / thread.confirm flow.
	RequireConfirmation bool `json:"require_confirmation,omitempty"`
	// Enabled gates whether the server is connected at all. A nil pointer (field
	// omitted from settings.json) means enabled — the opt-out default — so
	// existing configs keep working. The MCP tab's Space toggle writes this.
	Enabled *bool `json:"enabled,omitempty"`
}

// IsEnabled reports whether the server should be connected. A missing `enabled`
// field (nil pointer) defaults to true.
func (c ServerConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// UsesOAuth reports whether this server authenticates via the OAuth 2.0 flow.
func (c ServerConfig) UsesOAuth() bool {
	return c.OAuth != nil && c.OAuth.ClientID != ""
}

// OAuthConfig describes an OAuth 2.0 authorization-code flow for a url MCP
// server. ClientSecret may be given as "${VAR}" to read it from the environment.
// AuthURL/TokenURL are optional: when omitted, vix discovers them from the
// server via RFC 9728 (protected-resource metadata) and RFC 8414 (authorization-
// server metadata).
type OAuthConfig struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	AuthURL      string   `json:"auth_url,omitempty"`
	TokenURL     string   `json:"token_url,omitempty"`
	// RedirectPort pins the loopback callback port (0 = an ephemeral free port).
	RedirectPort int `json:"redirect_port,omitempty"`
}

// ToolDef is a tool discovered from an MCP server via tools/list.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// CallResult is the outcome of a tools/call request.
type CallResult struct {
	Output  string
	IsError bool
}

// ServerStatus is a per-server probe result: whether it connected, how many
// tools it exposed (after the AllowedTools filter), and any connection error.
type ServerStatus struct {
	Name      string
	Type      string // "stdio" or "url"
	Connected bool
	ToolCount int
	Error     string
}

// client is the interface satisfied by both stdioClient and httpClient.
type client interface {
	// ListTools returns the tools discovered during initialization.
	ListTools() []ToolDef
	// Call invokes a tool by name with the given arguments.
	Call(toolName string, args map[string]any) (CallResult, error)
	// Alive reports whether the server connection is still healthy.
	Alive() bool
	// Close shuts down the connection / child process.
	Close()
}

// jsonRPCRequest is a JSON-RPC 2.0 request message.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonRPCNotification is a JSON-RPC 2.0 notification (no ID, no response expected).
type jsonRPCNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response message.
type jsonRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *jsonRPCError    `json:"error,omitempty"`
}

// jsonRPCError is the error object inside a JSON-RPC 2.0 response.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// toolsListResult is the result shape of a tools/list response.
type toolsListResult struct {
	Tools []ToolDef `json:"tools"`
}

// toolsCallResult is the result shape of a tools/call response.
type toolsCallResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError"`
}

// toolContent is a single content item in a tools/call result.
type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
