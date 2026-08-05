package daemon

import (
	"strings"
	"testing"
)

// ── SummarizeToolInput ──
// Note: read_file and edit_file tests exist in readtracker_test.go.
// These tests cover the remaining tool types.

func TestSummarizeToolInput(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input map[string]any
		want  string
	}{
		{
			name:  "write_file",
			tool:  "write_file",
			input: map[string]any{"path": "out.go", "content": "package main\n"},
			want:  "out.go (13 chars)",
		},
		{
			name:  "delete_file",
			tool:  "delete_file",
			input: map[string]any{"path": "old.go"},
			want:  "old.go",
		},
		{
			name:  "bash short command",
			tool:  "bash",
			input: map[string]any{"command": "ls -la"},
			want:  "$ ls -la",
		},
		{
			name:  "bash truncation at 500",
			tool:  "bash",
			input: map[string]any{"command": strings.Repeat("x", 600)},
			want:  "$ " + strings.Repeat("x", 500) + "...",
		},
		{
			name:  "bash background prefix",
			tool:  "bash",
			input: map[string]any{"command": "sleep 60", "background": true},
			want:  "[bg] $ sleep 60",
		},
		{
			name:  "bash background false is foreground prefix",
			tool:  "bash",
			input: map[string]any{"command": "ls", "background": false},
			want:  "$ ls",
		},
		{
			name:  "grep",
			tool:  "grep",
			input: map[string]any{"pattern": "func main"},
			want:  "func main",
		},
		{
			name:  "glob_files",
			tool:  "glob_files",
			input: map[string]any{"pattern": "**/*.go"},
			want:  "**/*.go",
		},
		{
			name:  "glob_files array",
			tool:  "glob_files",
			input: map[string]any{"pattern": []any{"*.go", "*.ts"}},
			want:  "*.go, *.ts",
		},
		{
			name:  "lsp_query with file",
			tool:  "lsp_query",
			input: map[string]any{"operation": "go_to_definition", "file": "main.go"},
			want:  "go_to_definition main.go",
		},
		{
			name:  "lsp_query with query",
			tool:  "lsp_query",
			input: map[string]any{"operation": "workspace_symbols", "query": "Handler"},
			want:  "workspace_symbols 'Handler'",
		},
		{
			name:  "lsp_query operation only",
			tool:  "lsp_query",
			input: map[string]any{"operation": "diagnostics"},
			want:  "diagnostics",
		},
		{
			name:  "spawn_agent default type",
			tool:  "spawn_agent",
			input: map[string]any{"prompt": "find all tests"},
			want:  "general: find all tests",
		},
		{
			name:  "spawn_agent with type",
			tool:  "spawn_agent",
			input: map[string]any{"agent_type": "explore", "prompt": "find it"},
			want:  "explore: find it",
		},
		{
			name:  "spawn_agent background flag",
			tool:  "spawn_agent",
			input: map[string]any{"agent_type": "explore", "prompt": "search", "background": true},
			want:  "explore (background): search",
		},
		{
			name:  "spawn_agent prompt truncation",
			tool:  "spawn_agent",
			input: map[string]any{"prompt": strings.Repeat("a", 100)},
			want:  "general: " + strings.Repeat("a", 60) + "...",
		},
		{
			name:  "ask_question_to_user single",
			tool:  "ask_question_to_user",
			input: map[string]any{"questions": []any{map[string]any{"id": "q1", "category": "Pref", "question": "Which approach do you prefer?"}}},
			want:  "Which approach do you prefer?",
		},
		{
			name:  "ask_question_to_user single truncation",
			tool:  "ask_question_to_user",
			input: map[string]any{"questions": []any{map[string]any{"id": "q1", "category": "Pref", "question": strings.Repeat("q", 100)}}},
			want:  strings.Repeat("q", 60) + "...",
		},
		{
			name:  "ask_question_to_user multi",
			tool:  "ask_question_to_user",
			input: map[string]any{"questions": []any{map[string]any{"id": "q1", "category": "A", "question": "Q1"}, map[string]any{"id": "q2", "category": "B", "question": "Q2"}}},
			want:  "2 questions",
		},
		{
			name:  "task_output",
			tool:  "task_output",
			input: map[string]any{"task_id": "task_42"},
			want:  "task_42",
		},
		{
			name:  "unknown tool",
			tool:  "unknown_tool",
			input: map[string]any{"foo": "bar"},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SummarizeToolInput(tt.tool, tt.input)
			if got != tt.want {
				t.Errorf("SummarizeToolInput(%q, ...) = %q, want %q", tt.tool, got, tt.want)
			}
		})
	}
}

// ── FilterToolSchemas ──

func TestFilterToolSchemas(t *testing.T) {
	t.Run("nil returns all", func(t *testing.T) {
		all := ToolSchemas()
		filtered := FilterToolSchemas(nil)
		if len(filtered) != len(all) {
			t.Errorf("nil filter: got %d tools, want %d", len(filtered), len(all))
		}
	})

	t.Run("subset filtering", func(t *testing.T) {
		filtered := FilterToolSchemas([]string{"read_file", "bash"})
		if len(filtered) != 2 {
			t.Fatalf("expected 2 tools, got %d", len(filtered))
		}
		names := make(map[string]bool)
		for _, t := range filtered {
			names[t.Name] = true
		}
		if !names["read_file"] || !names["bash"] {
			t.Errorf("expected read_file and bash, got %v", names)
		}
	})

	t.Run("empty list returns empty", func(t *testing.T) {
		filtered := FilterToolSchemas([]string{})
		if len(filtered) != 0 {
			t.Errorf("expected 0 tools, got %d", len(filtered))
		}
	})

	t.Run("nonexistent tool", func(t *testing.T) {
		filtered := FilterToolSchemas([]string{"does_not_exist"})
		if len(filtered) != 0 {
			t.Errorf("expected 0 tools, got %d", len(filtered))
		}
	})
}

// ── IsReadOnlyTool ──

func TestIsReadOnlyTool(t *testing.T) {
	tests := []struct {
		name string
		tool string
		want bool
	}{
		{"read_file is readonly", "read_file", true},
		{"grep is readonly", "grep", true},
		{"glob_files is readonly", "glob_files", true},
		{"lsp_query is readonly", "lsp_query", true},
		{"write_file is not readonly", "write_file", false},
		{"bash is not readonly", "bash", false},
		{"delete_file is not readonly", "delete_file", false},
		{"edit_file is not readonly", "edit_file", false},
		{"spawn_agent is not readonly", "spawn_agent", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsReadOnlyTool(tt.tool)
			if got != tt.want {
				t.Errorf("IsReadOnlyTool(%q) = %v, want %v", tt.tool, got, tt.want)
			}
		})
	}
}
