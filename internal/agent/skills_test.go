package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkillFile(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "my-skill")
	os.MkdirAll(skillDir, 0755)

	content := `---
name: commit
description: Create a git commit
allowed-tools: bash, read_file
model: claude-sonnet-4-6
---

Review the changes and create a commit with message: $ARGUMENTS

The first arg is: $1
The second arg is: $2
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644)

	skill, err := parseSkillFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}

	if skill.Name != "commit" {
		t.Errorf("name = %q, want %q", skill.Name, "commit")
	}
	if skill.Description != "Create a git commit" {
		t.Errorf("description = %q", skill.Description)
	}
	if len(skill.AllowedTools) != 2 || skill.AllowedTools[0] != "bash" || skill.AllowedTools[1] != "read_file" {
		t.Errorf("allowed-tools = %v", skill.AllowedTools)
	}
	if skill.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q", skill.Model)
	}
}

func TestRenderPrompt(t *testing.T) {
	skill := &Skill{
		Name: "test",
		Body: "Do $ARGUMENTS. First: $1. Second: $2.",
	}

	result := skill.RenderPrompt("hello world")
	expected := "Do hello world. First: hello. Second: world."
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestRenderPromptQuotedArgs(t *testing.T) {
	skill := &Skill{
		Name: "test",
		Body: "Fix $1 in $2",
	}

	result := skill.RenderPrompt(`"the bug" src/main.go`)
	expected := "Fix the bug in src/main.go"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestLoadSkills(t *testing.T) {
	dir := t.TempDir()

	// Create a skill directory with SKILL.md
	skillDir := filepath.Join(dir, "project", "greet")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: greet
description: Say hello
---

Hello $ARGUMENTS!
`), 0644)

	// Create a directory without SKILL.md (should be skipped)
	os.MkdirAll(filepath.Join(dir, "project", "not-a-skill"), 0755)

	// User-level skill
	userSkillDir := filepath.Join(dir, "user", "farewell")
	os.MkdirAll(userSkillDir, 0755)
	os.WriteFile(filepath.Join(userSkillDir, "SKILL.md"), []byte(`---
name: farewell
description: Say goodbye
---

Goodbye $ARGUMENTS!
`), 0644)

	reg := LoadSkills(filepath.Join(dir, "project"), filepath.Join(dir, "user"))

	if reg.Count() != 2 {
		t.Fatalf("expected 2 skills, got %d", reg.Count())
	}
	if s := reg.Get("greet"); s == nil {
		t.Error("missing skill: greet")
	} else if s.Source != "project" {
		t.Errorf("greet source = %q, want project", s.Source)
	}
	if s := reg.Get("farewell"); s == nil {
		t.Error("missing skill: farewell")
	} else if s.Source != "user" {
		t.Errorf("farewell source = %q, want user", s.Source)
	}
}

func TestProjectSkillsTakePrecedence(t *testing.T) {
	dir := t.TempDir()

	// Same skill name in both project and user
	for _, sub := range []string{"project", "user"} {
		skillDir := filepath.Join(dir, sub, "deploy")
		os.MkdirAll(skillDir, 0755)
		os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: deploy
description: Deploy from `+sub+`
---

Deploy!
`), 0644)
	}

	reg := LoadSkills(filepath.Join(dir, "project"), filepath.Join(dir, "user"))

	if reg.Count() != 1 {
		t.Fatalf("expected 1 skill, got %d", reg.Count())
	}
	if s := reg.Get("deploy"); s.Source != "project" {
		t.Errorf("expected project to take precedence, got source=%q", s.Source)
	}
}

func TestSkillDirAndBundledFiles(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "project", "pdf")
	os.MkdirAll(filepath.Join(skillDir, "scripts"), 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: pdf
description: Work with PDFs
---
Body. Files live in ${SKILL_DIR}.
`), 0644)
	os.WriteFile(filepath.Join(skillDir, "reference.md"), []byte("ref"), 0644)
	os.WriteFile(filepath.Join(skillDir, "scripts", "extract.py"), []byte("print()"), 0644)

	reg := LoadSkills(filepath.Join(dir, "project"))
	sk := reg.Get("pdf")
	if sk == nil {
		t.Fatal("missing skill: pdf")
	}
	if sk.Dir != skillDir {
		t.Errorf("Dir = %q, want %q", sk.Dir, skillDir)
	}

	files := sk.BundledFiles()
	want := []string{"reference.md", filepath.Join("scripts", "extract.py")}
	if len(files) != len(want) {
		t.Fatalf("BundledFiles = %v, want %v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Errorf("BundledFiles[%d] = %q, want %q", i, files[i], want[i])
		}
	}

	// ${SKILL_DIR} is substituted with the skill directory.
	if got := sk.RenderPrompt(""); !strings.Contains(got, skillDir) {
		t.Errorf("RenderPrompt did not substitute ${SKILL_DIR}: %q", got)
	}

	// LoadForTool includes the body plus absolute paths to bundled files.
	out := sk.LoadForTool("")
	if !strings.Contains(out, "Bundled files") {
		t.Errorf("LoadForTool missing bundled-files section: %q", out)
	}
	if !strings.Contains(out, filepath.Join(skillDir, "reference.md")) {
		t.Errorf("LoadForTool missing absolute bundled path: %q", out)
	}
}

func TestLoadForToolNoBundledFiles(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "project", "plain")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: plain
description: No extras
---
Just instructions.
`), 0644)

	sk := LoadSkills(filepath.Join(dir, "project")).Get("plain")
	if out := sk.LoadForTool(""); strings.Contains(out, "Bundled files") {
		t.Errorf("expected no bundled-files section, got: %q", out)
	}
}

func TestDynamicCommand(t *testing.T) {
	skill := &Skill{
		Name: "test",
		Body: "Current date: !`echo hello`",
	}

	result := skill.RenderPrompt("")
	expected := "Current date: hello"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"", nil},
		{"hello", []string{"hello"}},
		{"hello world", []string{"hello", "world"}},
		{`"hello world" foo`, []string{"hello world", "foo"}},
		{`'single quoted' bar`, []string{"single quoted", "bar"}},
		{`  spaced  args  `, []string{"spaced", "args"}},
	}

	for _, tt := range tests {
		got := splitArgs(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("splitArgs(%q) = %v, want %v", tt.input, got, tt.expected)
			continue
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("splitArgs(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.expected[i])
			}
		}
	}
}

func TestFormatSkillsList(t *testing.T) {
	reg := NewSkillRegistry()
	if out := reg.FormatSkillsList(); out != "No skills loaded." {
		t.Errorf("empty registry: %q", out)
	}

	reg.skills["test"] = &Skill{Name: "test", Description: "A test skill", Source: "project"}
	out := reg.FormatSkillsList()
	if out == "No skills loaded." {
		t.Error("should list skills")
	}
}

// The shipped vix-help skill must parse through the real loader, advertise a
// description, and bundle its offline manual fallback.
func TestVixHelpBundledSkillLoads(t *testing.T) {
	reg := LoadSkills("../config/defaults/skills")

	s := reg.Get("vix-help")
	if s == nil {
		t.Fatal("vix-help skill should load from internal/config/defaults/skills")
	}
	if s.Description == "" {
		t.Error("vix-help skill should have a description (used in the system prompt)")
	}
	for _, kw := range []string{"vix", "config"} {
		if !strings.Contains(strings.ToLower(s.Description), kw) {
			t.Errorf("vix-help description should mention %q; got: %s", kw, s.Description)
		}
	}

	files := s.BundledFiles()
	found := false
	for _, f := range files {
		if filepath.ToSlash(f) == "references/vix-manual.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("vix-help should bundle references/vix-manual.md; got %v", files)
	}

	// The rendered tool payload should reference the bundled file so the model
	// can fall back offline.
	body := s.LoadForTool("")
	if !strings.Contains(body, "vix-manual.md") {
		t.Error("vix-help LoadForTool output should reference the bundled manual")
	}
}
