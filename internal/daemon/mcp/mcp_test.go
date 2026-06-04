package mcp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/get-vix/vix/internal/daemon/mcp"
)

// buildMockServer compiles the mock MCP server binary into a temp dir and
// returns its path. The binary is compiled once per test run and cached for
// the duration of the process.
func buildMockServer(t *testing.T) string {
	t.Helper()
	// Locate testdata/mock_server relative to this file.
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine source file location")
	}
	srcDir := filepath.Join(filepath.Dir(file), "testdata", "mock_server")

	// Build into a temp directory.
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "mock_server")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = srcDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to build mock server: %v", err)
	}
	return binPath
}

// TestStdioClient_ListTools verifies that the stdio client discovers tools
// correctly from the mock MCP server.
func TestStdioClient_ListTools(t *testing.T) {
	bin := buildMockServer(t)
	pool := mcp.NewPool(context.Background(), []mcp.ServerConfig{
		{Name: "mock", Command: bin},
	})
	if pool.ServerCount() != 1 {
		t.Fatalf("expected 1 server, got %d", pool.ServerCount())
	}
	if pool.ToolCount() != 2 {
		t.Fatalf("expected 2 tools, got %d", pool.ToolCount())
	}
}

// TestStdioClient_ToolSchemas verifies that ToolSchemas returns properly
// prefixed Anthropic tool params.
func TestStdioClient_ToolSchemas(t *testing.T) {
	bin := buildMockServer(t)
	pool := mcp.NewPool(context.Background(), []mcp.ServerConfig{
		{Name: "mock", Command: bin},
	})
	schemas := pool.ToolSchemas()
	if len(schemas) != 2 {
		t.Fatalf("expected 2 schemas, got %d", len(schemas))
	}
	names := map[string]bool{}
	for _, s := range schemas {
		names[s.Name] = true
	}
	for _, want := range []string{"mcp__mock__echo", "mcp__mock__add"} {
		if !names[want] {
			t.Errorf("expected tool %q in schemas, got: %v", want, names)
		}
	}
}

// TestStdioClient_Call_Echo verifies the echo tool call round-trip.
func TestStdioClient_Call_Echo(t *testing.T) {
	bin := buildMockServer(t)
	pool := mcp.NewPool(context.Background(), []mcp.ServerConfig{
		{Name: "mock", Command: bin},
	})
	output, isError, err := pool.Call("mcp__mock__echo", map[string]any{"text": "hello mcp"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isError {
		t.Fatalf("unexpected isError=true, output: %s", output)
	}
	if output != "hello mcp" {
		t.Errorf("expected output %q, got %q", "hello mcp", output)
	}
}

// TestStdioClient_Call_Add verifies the add tool call.
func TestStdioClient_Call_Add(t *testing.T) {
	bin := buildMockServer(t)
	pool := mcp.NewPool(context.Background(), []mcp.ServerConfig{
		{Name: "mock", Command: bin},
	})
	output, isError, err := pool.Call("mcp__mock__add", map[string]any{"a": 3.0, "b": 4.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isError {
		t.Fatalf("unexpected isError=true, output: %s", output)
	}
	if output != "7" {
		t.Errorf("expected output %q, got %q", "7", output)
	}
}

// TestPool_AllowedTools verifies that AllowedTools filters the exposed schemas.
func TestPool_AllowedTools(t *testing.T) {
	bin := buildMockServer(t)
	pool := mcp.NewPool(context.Background(), []mcp.ServerConfig{
		{Name: "mock", Command: bin, AllowedTools: []string{"echo"}},
	})
	if pool.ToolCount() != 1 {
		t.Fatalf("expected 1 tool after filter, got %d", pool.ToolCount())
	}
	schemas := pool.ToolSchemas()
	if schemas[0].Name != "mcp__mock__echo" {
		t.Errorf("expected mcp__mock__echo, got %s", schemas[0].Name)
	}
}

// TestPool_UnknownServer verifies that calling a non-existent server name
// returns an error rather than panicking.
func TestPool_UnknownServer(t *testing.T) {
	bin := buildMockServer(t)
	pool := mcp.NewPool(context.Background(), []mcp.ServerConfig{
		{Name: "mock", Command: bin},
	})
	_, _, err := pool.Call("mcp__nosuchserver__tool", map[string]any{})
	if err == nil {
		t.Fatal("expected error for unknown server, got nil")
	}
}

// TestPool_InvalidToolName verifies that a malformed qualified name returns an error.
func TestPool_InvalidToolName(t *testing.T) {
	bin := buildMockServer(t)
	pool := mcp.NewPool(context.Background(), []mcp.ServerConfig{
		{Name: "mock", Command: bin},
	})
	_, _, err := pool.Call("notanmcpname", map[string]any{})
	if err == nil {
		t.Fatal("expected error for invalid tool name, got nil")
	}
}

// TestPool_RequiresConfirmation verifies the confirmation flag is reported correctly.
func TestPool_RequiresConfirmation(t *testing.T) {
	bin := buildMockServer(t)
	pool := mcp.NewPool(context.Background(), []mcp.ServerConfig{
		{Name: "mock", Command: bin, RequireConfirmation: true},
	})
	if !pool.RequiresConfirmation("mcp__mock__echo") {
		t.Error("expected RequiresConfirmation=true for mock server")
	}
	if pool.RequiresConfirmation("mcp__other__tool") {
		t.Error("expected RequiresConfirmation=false for unknown server")
	}
}

// TestPool_BrokenServer verifies that a bad command is gracefully skipped.
func TestPool_BrokenServer(t *testing.T) {
	pool := mcp.NewPool(context.Background(), []mcp.ServerConfig{
		{Name: "broken", Command: "/no/such/binary/exists"},
	})
	if pool.ServerCount() != 0 {
		t.Fatalf("expected 0 servers for broken config, got %d", pool.ServerCount())
	}
}
