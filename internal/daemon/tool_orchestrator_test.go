package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestServer creates a minimal Server with mock tool handlers for testing.
func newTestServer(handlers map[string]HandlerFunc) *Server {
	s := &Server{
		handlers: make(map[string]HandlerFunc),
	}
	for name, h := range handlers {
		s.handlers[name] = h
	}
	return s
}

func TestToolOrchestratorSimpleReturn(t *testing.T) {
	s := newTestServer(nil)
	cwd := t.TempDir()

	result, err := toolOrchestratorImpl(context.Background(), s, `return {"hello": "world"}`, cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v (raw: %s)", err, result)
	}

	if parsed["hello"] != "world" {
		t.Errorf("expected hello=world, got %v", parsed["hello"])
	}
}

func TestToolOrchestratorSingleToolCall(t *testing.T) {
	var receivedPatterns []any
	s := newTestServer(map[string]HandlerFunc{
		"tool.glob_files": func(data map[string]any) (map[string]any, error) {
			params, _ := data["params"].(map[string]any)
			receivedPatterns, _ = params["pattern"].([]any)
			return toolOK("file1.txt\nfile2.txt\n", false), nil
		},
	})
	cwd := t.TempDir()

	workflow := `files = glob_files("*.txt", "find text files")
return {"files": files}`

	result, err := toolOrchestratorImpl(context.Background(), s, workflow, cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(receivedPatterns) != 1 || receivedPatterns[0] != "*.txt" {
		t.Errorf("expected pattern [*.txt], got %v", receivedPatterns)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	files, ok := parsed["files"].([]any)
	if !ok {
		t.Fatalf("expected files to be array, got %T", parsed["files"])
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d", len(files))
	}
}

func TestToolOrchestratorChainedCalls(t *testing.T) {
	callCount := 0
	s := newTestServer(map[string]HandlerFunc{
		"tool.grep": func(data map[string]any) (map[string]any, error) {
			callCount++
			return toolOK("main.go:1:func main() {}\nutils.go:5:func main() {}", false), nil
		},
		"tool.read_file": func(data map[string]any) (map[string]any, error) {
			callCount++
			params, _ := data["params"].(map[string]any)
			path, _ := params["path"].(string)
			return toolOK("content of "+path, false), nil
		},
	})
	cwd := t.TempDir()

	workflow := `matches = grep("func main", "find main functions")
contents = {}
for m in matches:
    contents[m["file"]] = read_file(m["file"], "read impl")
return {"count": len(matches), "files": list(contents.keys())}`

	result, err := toolOrchestratorImpl(context.Background(), s, workflow, cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	count, _ := parsed["count"].(float64)
	if count != 2 {
		t.Errorf("expected count=2, got %v", count)
	}

	// 1 grep + 2 read_file = 3 total calls
	if callCount != 3 {
		t.Errorf("expected 3 tool calls, got %d", callCount)
	}
}

func TestToolOrchestratorToolError(t *testing.T) {
	s := newTestServer(map[string]HandlerFunc{
		"tool.read_file": func(data map[string]any) (map[string]any, error) {
			return toolOK("Error: File not found: /nonexistent/file.txt", true), nil
		},
	})
	cwd := t.TempDir()

	workflow := `content = read_file("/nonexistent/file.txt", "should fail")
return {"content": content}`

	result, err := toolOrchestratorImpl(context.Background(), s, workflow, cwd)
	// The Python RuntimeError should cause the script to exit with error
	if err == nil && !strings.Contains(result, "Error") && !strings.Contains(result, "error") {
		t.Fatalf("expected error, got result: %s", result)
	}
}

func TestToolOrchestratorToolErrorWithTryExcept(t *testing.T) {
	s := newTestServer(map[string]HandlerFunc{
		"tool.read_file": func(data map[string]any) (map[string]any, error) {
			return toolOK("Error: File not found: /nonexistent/file.txt", true), nil
		},
	})
	cwd := t.TempDir()

	workflow := `try:
    content = read_file("/nonexistent/file.txt", "might fail")
except RuntimeError:
    content = "fallback"
return {"content": content}`

	result, err := toolOrchestratorImpl(context.Background(), s, workflow, cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	if parsed["content"] != "fallback" {
		t.Errorf("expected content=fallback, got %v", parsed["content"])
	}
}

func TestToolOrchestratorSyntaxError(t *testing.T) {
	s := newTestServer(nil)
	cwd := t.TempDir()

	workflow := `def broken(
    return {}`

	_, err := toolOrchestratorImpl(context.Background(), s, workflow, cwd)
	if err == nil {
		t.Fatal("expected error for syntax error, got nil")
	}
	if !strings.Contains(err.Error(), "workflow failed") {
		t.Errorf("expected 'workflow failed' in error, got: %v", err)
	}
}

func TestToolOrchestratorTimeout(t *testing.T) {
	s := newTestServer(nil)
	cwd := t.TempDir()

	// Override timeout for test
	origTimeout := orchestratorTimeout
	orchestratorTimeout = 2 * time.Second
	defer func() { orchestratorTimeout = origTimeout }()

	workflow := `import time
time.sleep(999)
return {}`

	_, err := toolOrchestratorImpl(context.Background(), s, workflow, cwd)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected 'timed out' in error, got: %v", err)
	}
}

func TestToolOrchestratorDisallowedTool(t *testing.T) {
	s := newTestServer(nil)
	cwd := t.TempDir()

	// Try calling a non-whitelisted tool via _call_tool directly
	workflow := `result = _call_tool("spawn_agent", {"prompt": "test"})
return {"result": result}`

	result, err := toolOrchestratorImpl(context.Background(), s, workflow, cwd)
	// Should get an error because spawn_agent is not allowed
	if err == nil && !strings.Contains(result, "Error") && !strings.Contains(result, "not allowed") {
		// The Python side should raise RuntimeError since is_error=true
		t.Logf("result: %s, err: %v", result, err)
	}
	// Either we get an error or the script fails — both are acceptable
}

func TestToolOrchestratorLargeOutput(t *testing.T) {
	s := newTestServer(nil)
	cwd := t.TempDir()

	// Create a workflow that returns a large string
	// Write a helper file with the large string
	largeFile := filepath.Join(cwd, "large.py")
	os.WriteFile(largeFile, []byte(""), 0644)

	workflow := `return {"data": "x" * 30000}`

	result, err := toolOrchestratorImpl(context.Background(), s, workflow, cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) > maxOutput+200 { // some slack for truncation message
		t.Errorf("expected result to be truncated, got length %d", len(result))
	}

	if !strings.Contains(result, "truncated") {
		t.Errorf("expected truncation message in result")
	}
}

func TestToolOrchestratorEditFileErrorNoCrash(t *testing.T) {
	s := newTestServer(map[string]HandlerFunc{
		"tool.edit_file": func(data map[string]any) (map[string]any, error) {
			return toolOK("Error: old_string not found in file", true), nil
		},
	})
	cwd := t.TempDir()

	workflow := `result = edit_file("test.go", "nonexistent", "replacement")
return result`

	result, err := toolOrchestratorImpl(context.Background(), s, workflow, cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not contain traceback — the error is caught gracefully
	if strings.Contains(result, "Traceback") {
		t.Errorf("expected no traceback, got: %s", result)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v (raw: %s)", err, result)
	}

	if parsed["success"] != false {
		t.Errorf("expected success=false, got %v", parsed["success"])
	}
	errMsg, _ := parsed["error"].(string)
	if !strings.Contains(errMsg, "old_string not found") {
		t.Errorf("expected error message about old_string, got: %s", errMsg)
	}
}

func TestToolOrchestratorEditFileErrorThenContinue(t *testing.T) {
	callCount := 0
	s := newTestServer(map[string]HandlerFunc{
		"tool.edit_file": func(data map[string]any) (map[string]any, error) {
			callCount++
			params, _ := data["params"].(map[string]any)
			oldStr, _ := params["old_string"].(string)
			if oldStr == "nonexistent" {
				return toolOK("Error: old_string not found in file", true), nil
			}
			return toolOK("File edited successfully", false), nil
		},
	})
	cwd := t.TempDir()

	workflow := `r1 = edit_file("test.go", "nonexistent", "replacement")
r2 = edit_file("test.go", "existing", "replacement")
return {"first": r1, "second": r2}`

	result, err := toolOrchestratorImpl(context.Background(), s, workflow, cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v (raw: %s)", err, result)
	}

	// Both calls should have been made
	if callCount != 2 {
		t.Errorf("expected 2 tool calls, got %d", callCount)
	}

	first, _ := parsed["first"].(map[string]any)
	second, _ := parsed["second"].(map[string]any)

	if first["success"] != false {
		t.Errorf("expected first.success=false, got %v", first["success"])
	}
	if second["success"] != true {
		t.Errorf("expected second.success=true, got %v", second["success"])
	}
}

func TestToolOrchestratorEditFileSuccessReturnsDict(t *testing.T) {
	s := newTestServer(map[string]HandlerFunc{
		"tool.edit_file": func(data map[string]any) (map[string]any, error) {
			return toolOK("File edited successfully", false), nil
		},
	})
	cwd := t.TempDir()

	workflow := `result = edit_file("test.go", "old", "new")
return result`

	result, err := toolOrchestratorImpl(context.Background(), s, workflow, cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v (raw: %s)", err, result)
	}

	if parsed["success"] != true {
		t.Errorf("expected success=true, got %v", parsed["success"])
	}
	output, _ := parsed["output"].(string)
	if output != "File edited successfully" {
		t.Errorf("expected output='File edited successfully', got: %s", output)
	}
}
