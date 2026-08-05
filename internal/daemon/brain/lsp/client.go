package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Diagnostic represents an LSP diagnostic (error, warning, etc.).
type Diagnostic struct {
	Range    LspRange `json:"range"`
	Severity int      `json:"severity"` // 1=Error, 2=Warning, 3=Information, 4=Hint
	Message  string   `json:"message"`
	Source   string   `json:"source"`
}

// Client manages a single LSP server subprocess over stdio.
type Client struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	nextID   atomic.Int64
	language string
	rootURI  string
	ready    bool
	dead     atomic.Bool // set when readLoop exits (process crashed/exited)
	pending  map[int64]chan *jsonRPCResponse
	pendMu   sync.Mutex

	diagMu      sync.RWMutex
	diagnostics map[string][]Diagnostic  // URI -> diagnostics
	diagChans   map[string]chan struct{} // URI -> notification channel
}

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *jsonRPCError    `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NewClient starts an LSP server subprocess and returns a client for it.
func NewClient(ctx context.Context, language, rootDir, command string, args ...string) (*Client, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = rootDir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", command, err)
	}

	c := &Client{
		cmd:         cmd,
		stdin:       stdin,
		stdout:      bufio.NewReaderSize(stdout, 1<<16),
		language:    language,
		rootURI:     "file://" + rootDir,
		pending:     make(map[int64]chan *jsonRPCResponse),
		diagnostics: make(map[string][]Diagnostic),
		diagChans:   make(map[string]chan struct{}),
	}

	go c.readLoop()
	return c, nil
}

// Initialize performs the LSP initialize handshake.
func (c *Client) Initialize() error {
	params := map[string]any{
		"processId": nil,
		"rootUri":   c.rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"documentSymbol": map[string]any{
					"hierarchicalDocumentSymbolSupport": true,
				},
				"definition":     map[string]any{},
				"references":     map[string]any{},
				"hover":          map[string]any{},
				"implementation": map[string]any{},
				"callHierarchy": map[string]any{
					"dynamicRegistration": false,
				},
				"publishDiagnostics": map[string]any{
					"relatedInformation": true,
				},
			},
			"workspace": map[string]any{
				"symbol": map[string]any{},
			},
		},
	}

	_, err := c.Call(context.Background(), "initialize", params)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	if err := c.Notify("initialized", map[string]any{}); err != nil {
		return fmt.Errorf("initialized notification: %w", err)
	}

	c.ready = true
	return nil
}

// Call sends a JSON-RPC request and waits for the response.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
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

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("LSP error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// Notify sends a JSON-RPC notification (no response expected).
func (c *Client) Notify(method string, params any) error {
	notif := jsonRPCNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	return c.send(notif)
}

// DidOpen sends a textDocument/didOpen notification.
func (c *Client) DidOpen(uri, languageID, text string) error {
	return c.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": languageID,
			"version":    1,
			"text":       text,
		},
	})
}

// DidClose sends a textDocument/didClose notification.
func (c *Client) DidClose(uri string) error {
	return c.Notify("textDocument/didClose", map[string]any{
		"textDocument": map[string]any{
			"uri": uri,
		},
	})
}

// DocumentSymbol sends textDocument/documentSymbol and returns the raw response.
func (c *Client) DocumentSymbol(ctx context.Context, uri string) (json.RawMessage, error) {
	return c.Call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{
			"uri": uri,
		},
	})
}

// Definition sends textDocument/definition and returns the raw response.
func (c *Client) Definition(ctx context.Context, uri string, line, character int) (json.RawMessage, error) {
	return c.Call(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{
			"uri": uri,
		},
		"position": map[string]any{
			"line":      line,
			"character": character,
		},
	})
}

// References sends textDocument/references and returns the raw response.
func (c *Client) References(ctx context.Context, uri string, line, character int, includeDecl bool) (json.RawMessage, error) {
	return c.Call(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{
			"uri": uri,
		},
		"position": map[string]any{
			"line":      line,
			"character": character,
		},
		"context": map[string]any{
			"includeDeclaration": includeDecl,
		},
	})
}

// Hover sends textDocument/hover and returns the raw response.
func (c *Client) Hover(ctx context.Context, uri string, line, character int) (json.RawMessage, error) {
	return c.Call(ctx, "textDocument/hover", map[string]any{
		"textDocument": map[string]any{
			"uri": uri,
		},
		"position": map[string]any{
			"line":      line,
			"character": character,
		},
	})
}

// WorkspaceSymbol sends workspace/symbol and returns the raw response.
func (c *Client) WorkspaceSymbol(ctx context.Context, query string) (json.RawMessage, error) {
	return c.Call(ctx, "workspace/symbol", map[string]any{
		"query": query,
	})
}

// LSPError is an error that includes the JSON-RPC error code from the LSP server.
type LSPError struct {
	Code    int
	Message string
}

func (e *LSPError) Error() string {
	return fmt.Sprintf("LSP error %d: %s", e.Code, e.Message)
}

// CallWithCode sends a JSON-RPC request and returns the error code on failure.
// Unlike Call(), the returned error is *LSPError when the server returns a JSON-RPC error,
// allowing callers to inspect the error code (e.g. -32601 = method not found).
func (c *Client) CallWithCode(ctx context.Context, method string, params any) (json.RawMessage, error) {
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

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, &LSPError{Code: resp.Error.Code, Message: resp.Error.Message}
		}
		return resp.Result, nil
	}
}

// CallHierarchyItem represents an LSP CallHierarchyItem.
type CallHierarchyItem struct {
	Name           string   `json:"name"`
	Kind           int      `json:"kind"`
	Detail         string   `json:"detail"`
	URI            string   `json:"uri"`
	Range          LspRange `json:"range"`
	SelectionRange LspRange `json:"selectionRange"`
	Data           any      `json:"data,omitempty"`
}

// CallHierarchyOutgoingCall represents an outgoing call from a CallHierarchyItem.
type CallHierarchyOutgoingCall struct {
	To         CallHierarchyItem `json:"to"`
	FromRanges []LspRange        `json:"fromRanges"`
}

// CallHierarchyIncomingCall represents an incoming call to a CallHierarchyItem.
type CallHierarchyIncomingCall struct {
	From       CallHierarchyItem `json:"from"`
	FromRanges []LspRange        `json:"fromRanges"`
}

// PrepareCallHierarchy sends textDocument/prepareCallHierarchy and returns the items.
func (c *Client) PrepareCallHierarchy(ctx context.Context, uri string, line, character int) ([]CallHierarchyItem, error) {
	raw, err := c.CallWithCode(ctx, "textDocument/prepareCallHierarchy", map[string]any{
		"textDocument": map[string]any{
			"uri": uri,
		},
		"position": map[string]any{
			"line":      line,
			"character": character,
		},
	})
	if err != nil {
		return nil, err
	}
	if raw == nil || string(raw) == "null" {
		return nil, nil
	}
	var items []CallHierarchyItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("unmarshal prepareCallHierarchy: %w", err)
	}
	return items, nil
}

// OutgoingCalls sends callHierarchy/outgoingCalls for the given item.
func (c *Client) OutgoingCalls(ctx context.Context, item CallHierarchyItem) ([]CallHierarchyOutgoingCall, error) {
	raw, err := c.CallWithCode(ctx, "callHierarchy/outgoingCalls", map[string]any{
		"item": item,
	})
	if err != nil {
		return nil, err
	}
	if raw == nil || string(raw) == "null" {
		return nil, nil
	}
	var calls []CallHierarchyOutgoingCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil, fmt.Errorf("unmarshal outgoingCalls: %w", err)
	}
	return calls, nil
}

// IncomingCalls sends callHierarchy/incomingCalls for the given item.
func (c *Client) IncomingCalls(ctx context.Context, item CallHierarchyItem) ([]CallHierarchyIncomingCall, error) {
	raw, err := c.CallWithCode(ctx, "callHierarchy/incomingCalls", map[string]any{
		"item": item,
	})
	if err != nil {
		return nil, err
	}
	if raw == nil || string(raw) == "null" {
		return nil, nil
	}
	var calls []CallHierarchyIncomingCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil, fmt.Errorf("unmarshal incomingCalls: %w", err)
	}
	return calls, nil
}

// Implementation sends textDocument/implementation and returns the raw response.
func (c *Client) Implementation(ctx context.Context, uri string, line, character int) (json.RawMessage, error) {
	return c.Call(ctx, "textDocument/implementation", map[string]any{
		"textDocument": map[string]any{
			"uri": uri,
		},
		"position": map[string]any{
			"line":      line,
			"character": character,
		},
	})
}

// GetDiagnostics returns the most recently published diagnostics for a URI.
func (c *Client) GetDiagnostics(uri string) []Diagnostic {
	c.diagMu.RLock()
	defer c.diagMu.RUnlock()
	return c.diagnostics[uri]
}

// WaitForDiagnostics blocks until diagnostics are published for the given URI or timeout.
func (c *Client) WaitForDiagnostics(uri string, timeout time.Duration) []Diagnostic {
	c.diagMu.Lock()
	ch, ok := c.diagChans[uri]
	if !ok {
		ch = make(chan struct{}, 1)
		c.diagChans[uri] = ch
	}
	c.diagMu.Unlock()

	select {
	case <-ch:
	case <-time.After(timeout):
	}

	return c.GetDiagnostics(uri)
}

// Close gracefully shuts down the LSP server.
func (c *Client) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*1e9) // 2 seconds
	defer cancel()

	// Try graceful shutdown
	c.Call(ctx, "shutdown", nil)
	c.Notify("exit", nil)

	c.stdin.Close()
	c.cmd.Wait()
}

func (c *Client) send(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, err := io.WriteString(c.stdin, header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := c.stdin.Write(data); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// jsonRPCNotificationMsg represents an incoming server notification.
type jsonRPCNotificationMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// publishDiagnosticsParams matches the LSP publishDiagnostics notification params.
type publishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Alive returns true if the LSP server process is still running.
func (c *Client) Alive() bool {
	return !c.dead.Load()
}

func (c *Client) readLoop() {
	defer c.dead.Store(true)
	for {
		// Read headers
		var contentLength int
		for {
			line, err := c.stdout.ReadString('\n')
			if err != nil {
				return // process exited
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break // end of headers
			}
			if strings.HasPrefix(line, "Content-Length: ") {
				n, err := strconv.Atoi(strings.TrimPrefix(line, "Content-Length: "))
				if err == nil {
					contentLength = n
				}
			}
		}

		if contentLength == 0 {
			continue
		}

		// Read body
		body := make([]byte, contentLength)
		if _, err := io.ReadFull(c.stdout, body); err != nil {
			return
		}

		// First try as a response (has ID field)
		var resp jsonRPCResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			LogError("LSP [%s]: failed to unmarshal message: %v", c.language, err)
			continue
		}

		// If no ID, it's a server notification
		if resp.ID == nil {
			c.handleNotification(body)
			continue
		}

		var id int64
		if err := json.Unmarshal(*resp.ID, &id); err != nil {
			// Try as string
			var sid string
			if err2 := json.Unmarshal(*resp.ID, &sid); err2 == nil {
				n, _ := strconv.ParseInt(sid, 10, 64)
				id = n
			} else {
				continue
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

func (c *Client) handleNotification(body []byte) {
	var notif jsonRPCNotificationMsg
	if err := json.Unmarshal(body, &notif); err != nil {
		return
	}

	switch notif.Method {
	case "textDocument/publishDiagnostics":
		var params publishDiagnosticsParams
		if err := json.Unmarshal(notif.Params, &params); err != nil {
			return
		}
		c.diagMu.Lock()
		c.diagnostics[params.URI] = params.Diagnostics
		ch, ok := c.diagChans[params.URI]
		if ok {
			// Non-blocking send to wake up any waiter
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		c.diagMu.Unlock()
	}
}
