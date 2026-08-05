package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoader_BasicLoad(t *testing.T) {
	// Create a temporary directory for test templates
	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "test.md")

	// Write a simple template
	content := "Hello, this is a test prompt."
	if err := os.WriteFile(templatePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test template: %v", err)
	}

	loader := &Loader{cache: make(map[string]string)}
	result := loader.Load(templatePath, nil, "", nil)

	if result != content {
		t.Errorf("Expected %q, got %q", content, result)
	}
}

func TestLoader_VarSubstitution(t *testing.T) {
	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "test.md")

	content := "Working directory: $(working_directory)\nUser: $(user)"
	if err := os.WriteFile(templatePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test template: %v", err)
	}

	loader := &Loader{cache: make(map[string]string)}
	vars := map[string]string{
		"working_directory": "/home/user/project",
		"user":              "alice",
	}
	result := loader.Load(templatePath, vars, "", nil)

	expected := "Working directory: /home/user/project\nUser: alice"
	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestLoader_FileSubstitution(t *testing.T) {
	tmpDir := t.TempDir()
	brainDir := filepath.Join(tmpDir, "brain")
	contextDir := filepath.Join(brainDir, "context")
	if err := os.MkdirAll(contextDir, 0755); err != nil {
		t.Fatalf("Failed to create context dir: %v", err)
	}

	// Create a brain context file
	archFile := filepath.Join(contextDir, "architecture.md")
	archContent := "# Architecture\nThis is the architecture description."
	if err := os.WriteFile(archFile, []byte(archContent), 0644); err != nil {
		t.Fatalf("Failed to write architecture file: %v", err)
	}

	// Create a template that references the file
	templatePath := filepath.Join(tmpDir, "test.md")
	templateContent := "Here is the architecture:\n\n$(file:context/architecture.md)"
	if err := os.WriteFile(templatePath, []byte(templateContent), 0644); err != nil {
		t.Fatalf("Failed to write test template: %v", err)
	}

	loader := &Loader{cache: make(map[string]string)}
	result := loader.Load(templatePath, nil, brainDir, nil)

	expected := "Here is the architecture:\n\n# Architecture\nThis is the architecture description."
	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestLoader_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	brainDir := filepath.Join(tmpDir, "brain")

	// Create template that references non-existent file
	templatePath := filepath.Join(tmpDir, "test.md")
	templateContent := "Architecture: $(file:context/architecture.md)"
	if err := os.WriteFile(templatePath, []byte(templateContent), 0644); err != nil {
		t.Fatalf("Failed to write test template: %v", err)
	}

	loader := &Loader{cache: make(map[string]string)}
	result := loader.Load(templatePath, nil, brainDir, nil)

	// Should show error message for missing file
	expected := "Architecture: [Error: file 'context/architecture.md' doesn't exist]"
	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestLoader_MissingVar(t *testing.T) {
	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "test.md")

	content := "Working directory: $(working_directory)"
	if err := os.WriteFile(templatePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test template: %v", err)
	}

	loader := &Loader{cache: make(map[string]string)}
	result := loader.Load(templatePath, nil, "", nil)

	// Should keep placeholder on graceful degradation
	expected := "Working directory: $(working_directory)"
	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestLoader_MissingTemplate(t *testing.T) {
	loader := &Loader{cache: make(map[string]string)}
	result := loader.Load("/nonexistent/template.md", nil, "", nil)

	if !contains(result, "Error: prompt template not found") {
		t.Errorf("Expected error message, got %q", result)
	}
}

func TestLoader_Cache(t *testing.T) {
	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "test.md")

	content := "Original content"
	if err := os.WriteFile(templatePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test template: %v", err)
	}

	loader := &Loader{cache: make(map[string]string)}

	// First load - should cache
	result1 := loader.Load(templatePath, nil, "", nil)
	if result1 != content {
		t.Errorf("Expected %q, got %q", content, result1)
	}

	// Modify the file
	newContent := "Modified content"
	if err := os.WriteFile(templatePath, []byte(newContent), 0644); err != nil {
		t.Fatalf("Failed to write modified template: %v", err)
	}

	// Second load - should return cached version
	result2 := loader.Load(templatePath, nil, "", nil)
	if result2 != content {
		t.Errorf("Expected cached content %q, got %q", content, result2)
	}

	// Clear cache
	loader.ClearCache()

	// Third load - should read new content
	result3 := loader.Load(templatePath, nil, "", nil)
	if result3 != newContent {
		t.Errorf("Expected new content %q, got %q", newContent, result3)
	}
}

func TestLoader_CombinedSubstitution(t *testing.T) {
	tmpDir := t.TempDir()
	brainDir := filepath.Join(tmpDir, "brain")
	contextDir := filepath.Join(brainDir, "context")
	if err := os.MkdirAll(contextDir, 0755); err != nil {
		t.Fatalf("Failed to create context dir: %v", err)
	}

	// Create brain context file
	summaryFile := filepath.Join(contextDir, "summary.md")
	summaryContent := "Project: AI Assistant"
	if err := os.WriteFile(summaryFile, []byte(summaryContent), 0644); err != nil {
		t.Fatalf("Failed to write summary file: %v", err)
	}

	// Create template with both variable and file placeholders
	templatePath := filepath.Join(tmpDir, "test.md")
	templateContent := `Working directory: $(working_directory)

$(file:context/summary.md)

User: $(user)`
	if err := os.WriteFile(templatePath, []byte(templateContent), 0644); err != nil {
		t.Fatalf("Failed to write test template: %v", err)
	}

	loader := &Loader{cache: make(map[string]string)}
	vars := map[string]string{
		"working_directory": "/home/test",
		"user":              "bob",
	}
	result := loader.Load(templatePath, vars, brainDir, nil)

	expected := `Working directory: /home/test

Project: AI Assistant

User: bob`
	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestResolveCall(t *testing.T) {
	loader := &Loader{cache: make(map[string]string)}
	funcs := map[string]func() string{
		"greeting": func() string { return "Hello, World!" },
	}
	result := loader.Resolve("$(call:greeting)", nil, "", funcs)
	if result != "Hello, World!" {
		t.Errorf("Expected %q, got %q", "Hello, World!", result)
	}
}

func TestResolveCall_MissingFunc(t *testing.T) {
	loader := &Loader{cache: make(map[string]string)}
	funcs := map[string]func() string{
		"greeting": func() string { return "Hello" },
	}
	result := loader.Resolve("$(call:nonexistent)", nil, "", funcs)
	if result != "" {
		t.Errorf("Expected empty string, got %q", result)
	}
}

func TestResolveCall_NilFuncs(t *testing.T) {
	loader := &Loader{cache: make(map[string]string)}
	result := loader.Resolve("$(call:anything)", nil, "", nil)
	if result != "" {
		t.Errorf("Expected empty string, got %q", result)
	}
}

func TestLoad_WithCallPlaceholder(t *testing.T) {
	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "test.md")

	content := "Files: $(call:get_files)\nDir: $(dir)"
	if err := os.WriteFile(templatePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test template: %v", err)
	}

	loader := &Loader{cache: make(map[string]string)}
	vars := map[string]string{"dir": "/tmp"}
	funcs := map[string]func() string{
		"get_files": func() string { return "file1.go, file2.go" },
	}
	result := loader.Load(templatePath, vars, "", funcs)

	expected := "Files: file1.go, file2.go\nDir: /tmp"
	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestGetLoader_Singleton(t *testing.T) {
	loader1 := GetLoader()
	loader2 := GetLoader()

	if loader1 != loader2 {
		t.Error("GetLoader() should return the same singleton instance")
	}
}

func TestStripFrontmatter_WithFrontmatter(t *testing.T) {
	content := `---
name: test-agent
model: claude-sonnet-4
tools: read_file, grep
---
This is the actual content.
It should not include the frontmatter above.`

	result := stripFrontmatter(content)
	expected := `This is the actual content.
It should not include the frontmatter above.`

	if result != expected {
		t.Errorf("Expected:\n%s\n\nGot:\n%s", expected, result)
	}
}

func TestStripFrontmatter_NoFrontmatter(t *testing.T) {
	content := `This is content without frontmatter.
It should be returned as-is.`

	result := stripFrontmatter(content)
	if result != content {
		t.Errorf("Expected:\n%s\n\nGot:\n%s", content, result)
	}
}

func TestStripFrontmatter_EmptyContent(t *testing.T) {
	content := ""
	result := stripFrontmatter(content)
	if result != content {
		t.Errorf("Expected empty string, got: %q", result)
	}
}

func TestStripFrontmatter_OnlyFrontmatter(t *testing.T) {
	content := `---
name: test
---`

	result := stripFrontmatter(content)
	// After stripping, we should get an empty string (or just whitespace)
	if result != "" {
		t.Errorf("Expected empty content after stripping frontmatter-only file, got: %q", result)
	}
}

func TestStripFrontmatter_NoClosing(t *testing.T) {
	content := `---
name: test
model: claude
This is content but frontmatter was never closed`

	result := stripFrontmatter(content)
	// Should return original content if frontmatter is malformed
	if result != content {
		t.Errorf("Expected original content when frontmatter not properly closed, got: %q", result)
	}
}

func TestLoader_WithFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "agent.md")

	content := `---
name: test-agent
model: claude-sonnet-4
tools: read_file, grep
max_turns: 10
---
# Identity

You are a test agent.
Working directory: $(working_directory)`

	if err := os.WriteFile(templatePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test template: %v", err)
	}

	loader := &Loader{cache: make(map[string]string)}
	vars := map[string]string{
		"working_directory": "/home/user/project",
	}
	result := loader.Load(templatePath, vars, "", nil)

	expected := `# Identity

You are a test agent.
Working directory: /home/user/project`

	if result != expected {
		t.Errorf("Expected:\n%s\n\nGot:\n%s", expected, result)
	}
}

func TestRealGeneralAgent_FrontmatterStripped(t *testing.T) {
	// This test verifies that the actual .vix/agents/general.md file
	// has its frontmatter properly stripped
	loader := GetLoader()

	// Construct path relative to the test file
	generalPath := filepath.Join("..", "..", "config", "defaults", "agents", "general.md")
	content := loader.Load(generalPath, map[string]string{
		"working_directory": "/test/dir",
	}, filepath.Join("..", "..", "config", "defaults"), nil)

	// The frontmatter should be stripped, so content should not start with "---"
	trimmed := strings.TrimSpace(content)
	if len(trimmed) == 0 {
		t.Fatalf("Loaded content is empty - file may not exist")
	}
	if strings.HasPrefix(trimmed, "---") {
		preview := trimmed
		if len(preview) > 200 {
			preview = preview[:200]
		}
		t.Errorf("Frontmatter was not stripped from general.md. Content starts with:\n%s", preview)
	}

	// The content should start with the agent's intro line
	if !strings.HasPrefix(trimmed, "You are **vix**") {
		preview := trimmed
		if len(preview) > 100 {
			preview = preview[:100]
		}
		t.Errorf("Expected content to start with 'You are **vix**', but got:\n%s", preview)
	}

	// Verify that the frontmatter metadata is NOT in the content
	if strings.Contains(content, "name: general") {
		t.Errorf("Frontmatter metadata 'name: general' found in content - frontmatter was not stripped")
	}
	if strings.Contains(content, "max_turns: 100") {
		t.Errorf("Frontmatter metadata 'max_turns: 100' found in content - frontmatter was not stripped")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || stringContains(s, substr)))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
