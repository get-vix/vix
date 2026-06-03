package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/get-vix/vix/internal/config"
)

// --- updateFrontmatterModel (pure function) ---

func TestUpdateFrontmatterModel_ReplacesExisting(t *testing.T) {
	in := `---
name: general
model: anthropic/claude-sonnet-4-6
tools: read_file, write_file
max_turns: 100
---

You are vix...`

	got, err := updateFrontmatterModel(in, "openai/gpt-5.1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "model: openai/gpt-5.1") {
		t.Errorf("expected model: openai/gpt-5.1, got:\n%s", got)
	}
	if strings.Contains(got, "anthropic/claude-sonnet-4-6") {
		t.Errorf("old model should be gone, got:\n%s", got)
	}
	// Body MUST survive.
	if !strings.Contains(got, "You are vix...") {
		t.Errorf("body lost; got:\n%s", got)
	}
	// Other frontmatter fields MUST survive.
	for _, want := range []string{"name: general", "tools: read_file, write_file", "max_turns: 100"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost frontmatter field %q; got:\n%s", want, got)
		}
	}
}

func TestUpdateFrontmatterModel_InsertsAfterName(t *testing.T) {
	in := `---
name: general
tools: read_file, write_file
max_turns: 100
---

You are vix...`

	got, err := updateFrontmatterModel(in, "anthropic/claude-opus-4-8")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	lines := strings.Split(got, "\n")
	// Expect: [---, name: general, model: anthropic/claude-opus-4-8, tools: ...]
	if len(lines) < 4 {
		t.Fatalf("not enough lines: %s", got)
	}
	if lines[0] != "---" {
		t.Errorf("line[0] = %q, want ---", lines[0])
	}
	if lines[1] != "name: general" {
		t.Errorf("line[1] = %q, want name: general", lines[1])
	}
	if lines[2] != "model: anthropic/claude-opus-4-8" {
		t.Errorf("line[2] = %q, want model: anthropic/claude-opus-4-8", lines[2])
	}
	if lines[3] != "tools: read_file, write_file" {
		t.Errorf("line[3] = %q, want tools: ...", lines[3])
	}
}

func TestUpdateFrontmatterModel_InsertsAtTopWhenNoName(t *testing.T) {
	in := `---
description: some agent
tools: read_file
---

body`

	got, err := updateFrontmatterModel(in, "openai/o3")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	lines := strings.Split(got, "\n")
	if lines[1] != "model: openai/o3" {
		t.Errorf("line[1] = %q, want model: openai/o3", lines[1])
	}
}

func TestUpdateFrontmatterModel_NoFrontmatter_Errors(t *testing.T) {
	in := `# Some agent without YAML frontmatter

body text`

	if _, err := updateFrontmatterModel(in, "openai/o3"); err == nil {
		t.Fatal("expected error for missing frontmatter delimiter")
	}
}

func TestUpdateFrontmatterModel_UnterminatedFrontmatter_Errors(t *testing.T) {
	in := `---
name: general
model: foo

body without closing delimiter`

	if _, err := updateFrontmatterModel(in, "openai/o3"); err == nil {
		t.Fatal("expected error for unterminated frontmatter")
	}
}

// --- WriteChatAgentModel (integration with the filesystem layer) ---

func TestWriteChatAgentModel_UpdatesExistingFile(t *testing.T) {
	tmp := t.TempDir()
	cwd := filepath.Join(tmp, "project")
	home := filepath.Join(tmp, "home")
	for _, d := range []string{cwd, home, filepath.Join(cwd, ".vix", "agents")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	// Project-level general.md already exists.
	agentPath := filepath.Join(cwd, ".vix", "agents", "general.md")
	original := `---
name: general
model: anthropic/claude-sonnet-4-6
tools: read_file
---

You are vix.
`
	if err := os.WriteFile(agentPath, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	paths := config.NewVixPaths("", home, cwd)
	if err := WriteChatAgentModel(paths, "general", "openai/gpt-5.1"); err != nil {
		t.Fatalf("WriteChatAgentModel: %v", err)
	}

	updated, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(updated), "model: openai/gpt-5.1") {
		t.Errorf("file not updated; got:\n%s", updated)
	}
	if strings.Contains(string(updated), "anthropic/claude-sonnet-4-6") {
		t.Errorf("old model still present; got:\n%s", updated)
	}
	if !strings.Contains(string(updated), "You are vix.") {
		t.Errorf("body lost; got:\n%s", updated)
	}
}

func TestWriteChatAgentModel_PrefersProjectOverHome(t *testing.T) {
	tmp := t.TempDir()
	cwd := filepath.Join(tmp, "project")
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(filepath.Join(cwd, ".vix", "agents"), 0o755); err != nil {
		t.Fatalf("mkdir cwd agents: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "agents"), 0o755); err != nil {
		t.Fatalf("mkdir home agents: %v", err)
	}

	homeOriginal := "---\nname: general\nmodel: home/old\n---\n\nbody home\n"
	projectOriginal := "---\nname: general\nmodel: project/old\n---\n\nbody project\n"

	if err := os.WriteFile(filepath.Join(home, "agents", "general.md"), []byte(homeOriginal), 0o644); err != nil {
		t.Fatalf("seed home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".vix", "agents", "general.md"), []byte(projectOriginal), 0o644); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	paths := config.NewVixPaths("", home, cwd)
	if err := WriteChatAgentModel(paths, "general", "openai/o3"); err != nil {
		t.Fatalf("WriteChatAgentModel: %v", err)
	}

	// Project should have been updated, home untouched.
	projUpd, _ := os.ReadFile(filepath.Join(cwd, ".vix", "agents", "general.md"))
	homeUnch, _ := os.ReadFile(filepath.Join(home, "agents", "general.md"))

	if !strings.Contains(string(projUpd), "model: openai/o3") {
		t.Errorf("project file not updated: %s", projUpd)
	}
	if string(homeUnch) != homeOriginal {
		t.Errorf("home file was modified; got:\n%s\nwant:\n%s", homeUnch, homeOriginal)
	}
}

func TestWriteChatAgentModel_FileNotFound_Errors(t *testing.T) {
	tmp := t.TempDir()
	paths := config.NewVixPaths("", filepath.Join(tmp, "home"), filepath.Join(tmp, "project"))
	err := WriteChatAgentModel(paths, "general", "openai/o3")
	if err == nil {
		t.Fatal("expected error when no agent file exists")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestWriteChatAgentModel_EmptyArgs_Errors(t *testing.T) {
	paths := config.NewVixPaths("", "", "")
	if err := WriteChatAgentModel(paths, "", "openai/o3"); err == nil {
		t.Error("expected error for empty agent name")
	}
	if err := WriteChatAgentModel(paths, "general", ""); err == nil {
		t.Error("expected error for empty model spec")
	}
}
