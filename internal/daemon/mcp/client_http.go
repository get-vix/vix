package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/oauth2"
)

// bearerSource supplies OAuth access tokens for an httpClient. It is nil for
// servers that authenticate with static headers only.
type bearerSource interface {
	// Token returns a currently-valid token, refreshing if necessary.
	Token() (*oauth2.Token, error)
	// Invalidate forces the next Token call to refresh (used after a 401).
	Invalidate()
}

// httpClient implements the MCP client interface over plain HTTP POST.
// Each JSON-RPC 2.0 message is a synchronous POST to the server URL; the
// response body contains the JSON-RPC reply. This covers the simple HTTP
// transport used by hosted MCP services that don't require SSE streaming.
type httpClient struct {
	name    string
	url     string
	headers map[string]string
	oauth   bearerSource
	http    *http.Client
	nextID  atomic.Int64
	dead    atomic.Bool
	tools   []ToolDef
}

// newHTTPClient creates an httpClient, runs the initialize handshake, and
// fetches the tool list. Headers whose values match "${VAR}" are replaced with
// the corresponding environment variable. When oauth is non-nil, an
// Authorization: Bearer header is injected (and refreshed) on every request.
func newHTTPClient(name, rawURL string, headers map[string]string, oauth bearerSource) (*httpClient, error) {
	resolved := make(map[string]string, len(headers))
	for k, v := range headers {
		resolved[k] = expandEnvValue(v)
	}

	c := &httpClient{
		name:    name,
		url:     rawURL,
		headers: resolved,
		oauth:   oauth,
		http:    &http.Client{Timeout: 30 * time.Second},
	}

	if err := c.initialize(); err != nil {
		return nil, fmt.Errorf("mcp [%s]: initialize: %w", name, err)
	}
	if err := c.fetchTools(); err != nil {
		return nil, fmt.Errorf("mcp [%s]: tools/list: %w", name, err)
	}

	log.Printf("[mcp] %s: ready (%d tool(s)) [http]", name, len(c.tools))
	return c, nil
}

// expandEnvValue replaces "${VAR}" patterns with their environment values.
func expandEnvValue(v string) string {
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		varName := v[2 : len(v)-1]
		if val := os.Getenv(varName); val != "" {
			return val
		}
	}
	return v
}

func (c *httpClient) initialize() error {
	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "vix",
			"version": "1.0",
		},
	}
	resp, err := c.post("initialize", params)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("server error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	// Send initialized notification (best-effort; some HTTP servers ignore it).
	_ = c.postNotification("notifications/initialized", map[string]any{})
	return nil
}

func (c *httpClient) fetchTools() error {
	resp, err := c.post("tools/list", map[string]any{})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("server error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	var result toolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("unmarshal tools/list result: %w", err)
	}
	c.tools = result.Tools
	return nil
}

// ListTools returns the tools discovered during initialization.
func (c *httpClient) ListTools() []ToolDef {
	return c.tools
}

// Call invokes a named tool and returns the result.
func (c *httpClient) Call(toolName string, args map[string]any) (CallResult, error) {
	if !c.Alive() {
		return CallResult{}, fmt.Errorf("mcp [%s]: client is closed", c.name)
	}
	if args == nil {
		args = map[string]any{}
	}
	params := map[string]any{
		"name":      toolName,
		"arguments": args,
	}
	resp, err := c.post("tools/call", params)
	if err != nil {
		return CallResult{}, err
	}
	if resp.Error != nil {
		return CallResult{Output: resp.Error.Message, IsError: true}, nil
	}
	var result toolsCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return CallResult{}, fmt.Errorf("unmarshal tools/call result: %w", err)
	}
	return callResultFromContent(result), nil
}

// Alive reports whether the client is still usable.
func (c *httpClient) Alive() bool {
	return !c.dead.Load()
}

// Close marks the client as dead (HTTP has no persistent connection to close).
func (c *httpClient) Close() {
	c.dead.Store(true)
}

// applyHeaders sets the static headers and, when OAuth is configured, a fresh
// Authorization: Bearer header on req.
func (c *httpClient) applyHeaders(req *http.Request) error {
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if c.oauth != nil {
		tok, err := c.oauth.Token()
		if err != nil {
			return fmt.Errorf("mcp [%s]: oauth token: %w", c.name, err)
		}
		typ := tok.Type()
		if typ == "" {
			typ = "Bearer"
		}
		req.Header.Set("Authorization", typ+" "+tok.AccessToken)
	}
	return nil
}

// post sends a JSON-RPC 2.0 request via HTTP POST and returns the response. On a
// 401 with OAuth configured, it forces a token refresh and retries once.
func (c *httpClient) post(method string, params any) (*jsonRPCResponse, error) {
	id := c.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	respBody, status, err := c.sendRaw(body)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized && c.oauth != nil {
		c.oauth.Invalidate()
		respBody, status, err = c.sendRaw(body)
		if err != nil {
			return nil, err
		}
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("mcp [%s]: HTTP %d from %s", c.name, status, method)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("mcp [%s]: unmarshal response: %w", c.name, err)
	}
	return &rpcResp, nil
}

// sendRaw POSTs body to the server and returns the response body and status. A
// transport error marks the client dead. Non-2xx statuses are returned without
// reading the body so callers can react (e.g. retry a 401).
func (c *httpClient) sendRaw(body []byte) ([]byte, int, error) {
	httpReq, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	if err := c.applyHeaders(httpReq); err != nil {
		return nil, 0, err
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		c.dead.Store(true)
		return nil, 0, fmt.Errorf("mcp [%s]: HTTP POST: %w", c.name, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, httpResp.StatusCode, nil
	}

	respBody, err := io.ReadAll(io.LimitReader(httpResp.Body, 4*1024*1024))
	if err != nil {
		return nil, httpResp.StatusCode, fmt.Errorf("mcp [%s]: read response: %w", c.name, err)
	}
	return respBody, httpResp.StatusCode, nil
}

// postNotification sends a JSON-RPC 2.0 notification (no response parsed).
func (c *httpClient) postNotification(method string, params any) error {
	n := jsonRPCNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	body, err := json.Marshal(n)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if err := c.applyHeaders(httpReq); err != nil {
		return err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
