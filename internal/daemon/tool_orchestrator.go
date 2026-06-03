package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/get-vix/vix/internal/protocol"
)

// orchestratorAllowedTools is the whitelist of tools callable from tool_orchestrator.
var orchestratorAllowedTools = map[string]bool{
	"read_file":  true,
	"write_file": true,
	"edit_file":  true,
	"delete_file": true,
	"bash":       true,
	"grep":       true,
	"glob_files": true,
	"lsp_query":  true,
}

// orchestratorTimeout is the maximum duration for a tool_orchestrator workflow.
var orchestratorTimeout = 5 * time.Minute

// pythonPreambleTemplate contains %s placeholder for CWD injection.
const pythonPreambleTemplate = `import sys, json, os

_original_stdout = sys.stdout
sys.stdout = sys.stderr  # redirect print() to stderr

CWD = %q

def _resolve_path(path):
    """Resolve a path against CWD if it's relative."""
    if not os.path.isabs(path):
        return os.path.join(CWD, path)
    return path

def _call_tool(name, params):
    req = json.dumps({"call": name, "params": params})
    _original_stdout.write(req + "\n")
    _original_stdout.flush()
    resp = json.loads(input())
    if resp.get("is_error"):
        raise RuntimeError("Tool " + name + " failed: " + resp["output"])
    return resp["output"]

def read_file(path, reason, start_pct=0, end_pct=100):
    params = {"path": _resolve_path(path), "reason": reason}
    if start_pct > 0: params["start_pct"] = start_pct
    if end_pct < 100: params["end_pct"] = end_pct
    return _call_tool("read_file", params)

def grep(pattern, reason, path=None, include=None):
    params = {"pattern": pattern, "reason": reason}
    if path: params["path"] = _resolve_path(path)
    if include: params["include"] = include
    raw = _call_tool("grep", params)
    results = []
    for line in raw.strip().split("\n"):
        if ":" in line:
            parts = line.split(":", 2)
            if len(parts) >= 3:
                try:
                    results.append({"file": parts[0], "line": int(parts[1]), "text": parts[2]})
                except ValueError:
                    pass
    return results

def glob_files(pattern, reason, path=None, type=None, include_hidden=True):
    # pattern and path are always sent as arrays. Bare strings are wrapped for
    # convenience so callers can pass either form.
    if isinstance(pattern, str):
        pattern = [pattern]
    else:
        pattern = list(pattern)
    params = {"pattern": pattern, "reason": reason}
    if path is not None:
        if isinstance(path, str):
            path = [path]
        params["path"] = [_resolve_path(p) for p in path]
    if type: params["type"] = type
    if not include_hidden: params["include_hidden"] = False
    raw = _call_tool("glob_files", params)
    return [f for f in raw.strip().split("\n") if f]

def lsp_query(operation, reason, file=None, line=None, character=None, query=None, include_declaration=False):
    params = {"operation": operation, "reason": reason}
    if file: params["file"] = _resolve_path(file)
    if line is not None: params["line"] = line
    if character is not None: params["character"] = character
    if query: params["query"] = query
    if include_declaration: params["include_declaration"] = include_declaration
    return _call_tool("lsp_query", params)

def bash(command):
    return _call_tool("bash", {"command": command})

def edit_file(path, old_string, new_string):
    try:
        result = _call_tool("edit_file", {"path": _resolve_path(path), "old_string": old_string, "new_string": new_string})
        return {"success": True, "output": result}
    except RuntimeError as e:
        return {"success": False, "error": str(e)}

def write_file(path, content):
    try:
        result = _call_tool("write_file", {"path": _resolve_path(path), "content": content})
        return {"success": True, "output": result}
    except RuntimeError as e:
        return {"success": False, "error": str(e)}

def delete_file(path):
    try:
        result = _call_tool("delete_file", {"path": _resolve_path(path)})
        return {"success": True, "output": result}
    except RuntimeError as e:
        return {"success": False, "error": str(e)}
`

// toolOrchestratorImpl runs a Python workflow script that can call tools via IPC.
func toolOrchestratorImpl(ctx context.Context, server *Server, workflow, cwd string) (string, error) {
	// Build the full Python script: preamble + user workflow wrapped in _workflow() + epilogue
	var script strings.Builder
	script.WriteString(fmt.Sprintf(pythonPreambleTemplate, cwd))
	script.WriteString("\ndef _workflow():\n")

	// Indent user workflow by 4 spaces
	for _, line := range strings.Split(workflow, "\n") {
		script.WriteString("    ")
		script.WriteString(line)
		script.WriteString("\n")
	}

	script.WriteString("\ntry:\n")
	script.WriteString("    _res = _workflow()\n")
	script.WriteString(`    _original_stdout.write(json.dumps({"__done__": True, "result": _res}) + "\n")` + "\n")
	script.WriteString("    _original_stdout.flush()\n")
	script.WriteString("except Exception as _exc:\n")
	script.WriteString("    import traceback\n")
	script.WriteString("    _tb = traceback.format_exc()\n")
	script.WriteString("    print(_tb, file=sys.stderr)\n")
	script.WriteString(`    _original_stdout.write(json.dumps({"__done__": True, "result": "ERROR: Workflow failed with exception:\n" + _tb}) + "\n")` + "\n")
	script.WriteString("    _original_stdout.flush()\n")

	// Write to temp file
	tmpFile, err := os.CreateTemp("", "orchestrator-*.py")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(script.String()); err != nil {
		tmpFile.Close()
		return "", fmt.Errorf("failed to write temp file: %w", err)
	}
	tmpFile.Close()

	// Spawn Python subprocess
	ctx, cancel := context.WithTimeout(ctx, orchestratorTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "python3", tmpFile.Name())
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Capture stderr for debug output
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start python3: %w", err)
	}
	setOOMScore(cmd.Process.Pid, 1000)

	// IPC loop: read stdout line by line
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), protocol.MaxMessageSize)

	var result string
	var ipcErr error
	toolCallCount := 0

	for scanner.Scan() {
		line := scanner.Text()

		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			// Unparseable line — treat as debug output
			LogInfo("[orchestrator] debug: %s", line)
			continue
		}

		// Check for __done__
		if done, ok := msg["__done__"].(bool); ok && done {
			resultData := msg["result"]
			resultBytes, err := json.Marshal(resultData)
			if err != nil {
				ipcErr = fmt.Errorf("failed to marshal result: %w", err)
				break
			}
			result = string(resultBytes)
			break
		}

		// Check for tool call
		callName, ok := msg["call"].(string)
		if !ok {
			LogInfo("[orchestrator] unknown message: %s", line)
			continue
		}

		params, _ := msg["params"].(map[string]any)
		if params == nil {
			params = map[string]any{}
		}

		// Check allowed tools
		if !orchestratorAllowedTools[callName] {
			resp := map[string]any{
				"output":   fmt.Sprintf("Tool '%s' is not allowed in tool_orchestrator", callName),
				"is_error": true,
			}
			respBytes, _ := json.Marshal(resp)
			io.WriteString(stdin, string(respBytes)+"\n")
			continue
		}

		// Add cwd for tools that need it
		if callName == "bash" || callName == "grep" || callName == "glob_files" || callName == "lsp_query" {
			params["cwd"] = cwd
		}

		// Execute tool via server handler
		handler := server.GetHandler("tool." + callName)
		if handler == nil {
			resp := map[string]any{
				"output":   fmt.Sprintf("Tool handler not found: %s", callName),
				"is_error": true,
			}
			respBytes, _ := json.Marshal(resp)
			io.WriteString(stdin, string(respBytes)+"\n")
			continue
		}

		toolResp, err := handler(map[string]any{
			"command": "tool." + callName,
			"params":  params,
		})

		var resp map[string]any
		if err != nil {
			resp = map[string]any{
				"output":   fmt.Sprintf("Error: %v", err),
				"is_error": true,
			}
		} else {
			data, _ := toolResp["data"].(map[string]any)
			output, _ := data["output"].(string)
			isError, _ := data["is_error"].(bool)
			resp = map[string]any{
				"output":   output,
				"is_error": isError,
			}
		}

		respBytes, _ := json.Marshal(resp)
		io.WriteString(stdin, string(respBytes)+"\n")
		toolCallCount++
	}

	stdin.Close()
	cmdErr := cmd.Wait()

	// Check for timeout
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("workflow timed out after %v", orchestratorTimeout)
	}

	// If we got a result, return it (even if process exited non-zero after __done__)
	if result != "" {
		// Truncate if too large
		if len(result) > maxOutput {
			result = result[:maxOutput] + fmt.Sprintf("\n... (truncated at %d chars)", maxOutput)
		}
		return result, nil
	}

	// No result — check for errors
	if ipcErr != nil {
		return "", ipcErr
	}

	stderr := stderrBuf.String()
	if cmdErr != nil {
		if stderr != "" {
			return "", fmt.Errorf("workflow failed: %v\nstderr: %s", cmdErr, stderr)
		}
		return "", fmt.Errorf("workflow failed: %v", cmdErr)
	}

	return "null", nil
}
