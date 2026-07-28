package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// stdioClient manages a single MCP server subprocess communicating over stdio.
// The MCP stdio transport is newline-delimited JSON-RPC 2.0 (no Content-Length
// framing, unlike LSP).
type stdioClient struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	mu     sync.Mutex // guards stdin writes

	nextID  atomic.Int64
	dead    atomic.Bool
	pending map[int64]chan *jsonRPCResponse
	pendMu  sync.Mutex

	tools []ToolDef
}

// newStdioClient starts the MCP server subprocess, performs the initialize
// handshake, and fetches the tool list. Returns a ready-to-use client.
func newStdioClient(ctx context.Context, name, command string, args []string, env map[string]string) (*stdioClient, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	// Inherit the daemon's environment, then overlay server-specific vars.
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	configureStdioCommand(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp [%s]: stdin pipe: %w", name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp [%s]: stdout pipe: %w", name, err)
	}
	// Discard stderr so it doesn't pollute the daemon's output, but capture
	// it for debug if the server crashes.
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp [%s]: start %q: %w", name, command, err)
	}

	c := &stdioClient{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewScanner(stdout),
		pending: make(map[int64]chan *jsonRPCResponse),
	}
	// Increase the scanner buffer for large tool list responses.
	c.stdout.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	go c.readLoop()

	if err := c.initialize(); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp [%s]: initialize: %w", name, err)
	}
	if err := c.fetchTools(); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp [%s]: tools/list: %w", name, err)
	}

	log.Printf("[mcp] %s: ready (%d tool(s))", name, len(c.tools))
	return c, nil
}

// initialize sends the MCP initialize request and the initialized notification.
func (c *stdioClient) initialize() error {
	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "vix",
			"version": "1.0",
		},
	}
	resp, err := c.call("initialize", params)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("server error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	// Send the initialized notification (fire-and-forget).
	return c.notify("notifications/initialized", map[string]any{})
}

// fetchTools calls tools/list and stores the results on c.tools.
func (c *stdioClient) fetchTools() error {
	resp, err := c.call("tools/list", map[string]any{})
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
func (c *stdioClient) ListTools() []ToolDef {
	return c.tools
}

// Call invokes a named tool with the given arguments and returns the result.
func (c *stdioClient) Call(toolName string, args map[string]any) (CallResult, error) {
	if !c.Alive() {
		return CallResult{}, fmt.Errorf("mcp [%s]: server is no longer running", c.name)
	}
	if args == nil {
		args = map[string]any{}
	}
	params := map[string]any{
		"name":      toolName,
		"arguments": args,
	}
	resp, err := c.call("tools/call", params)
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

// callResultFromContent extracts the text output from a tools/call result.
func callResultFromContent(r toolsCallResult) CallResult {
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return CallResult{
		Output:  strings.Join(parts, "\n"),
		IsError: r.IsError,
	}
}

// Alive reports whether the server process is still running.
func (c *stdioClient) Alive() bool {
	return !c.dead.Load()
}

// Close shuts down the MCP server process.
func (c *stdioClient) Close() {
	c.stdin.Close()
	if c.cmd.Process != nil {
		killStdioProcess(c.cmd)
	}
	c.cmd.Wait()
}

// call sends a JSON-RPC 2.0 request and waits synchronously for the response.
func (c *stdioClient) call(method string, params any) (*jsonRPCResponse, error) {
	id := c.nextID.Add(1)
	ch := make(chan *jsonRPCResponse, 1)

	c.pendMu.Lock()
	c.pending[id] = ch
	c.pendMu.Unlock()

	defer func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}()

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	if err := c.send(req); err != nil {
		return nil, err
	}
	resp, ok := <-ch
	if !ok {
		return nil, fmt.Errorf("mcp [%s]: connection closed while waiting for response to %q", c.name, method)
	}
	return resp, nil
}

// notify sends a JSON-RPC 2.0 notification (no response expected).
func (c *stdioClient) notify(method string, params any) error {
	n := jsonRPCNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	return c.send(n)
}

// send marshals msg to JSON and writes it as a single newline-terminated line.
func (c *stdioClient) send(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("mcp [%s]: write: %w", c.name, err)
	}
	return nil
}

// readLoop reads newline-delimited JSON responses from the server's stdout and
// dispatches them to waiting callers via the pending map.
func (c *stdioClient) readLoop() {
	defer func() {
		c.dead.Store(true)
		// Close all pending channels so blocked callers unblock.
		c.pendMu.Lock()
		for _, ch := range c.pending {
			close(ch)
		}
		c.pendMu.Unlock()
		log.Printf("[mcp] %s: read loop exited", c.name)
	}()

	for c.stdout.Scan() {
		line := c.stdout.Bytes()
		if len(line) == 0 {
			continue
		}

		var resp jsonRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			log.Printf("[mcp] %s: failed to unmarshal message: %v", c.name, err)
			continue
		}

		// No ID means it's a server notification — ignore for now.
		if resp.ID == nil {
			continue
		}

		// Parse the numeric ID.
		var id int64
		if err := json.Unmarshal(*resp.ID, &id); err != nil {
			// Some servers send string IDs.
			var sid string
			if err2 := json.Unmarshal(*resp.ID, &sid); err2 == nil {
				id, _ = strconv.ParseInt(sid, 10, 64)
			}
		}

		c.pendMu.Lock()
		ch, ok := c.pending[id]
		c.pendMu.Unlock()
		if ok {
			ch <- &resp
		}
	}
}
