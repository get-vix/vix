package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/agent"
)

// newSkillThread builds a minimal thread whose skill registry is loaded from
// the given project skills directory, suitable for exercising the `skill` tool
// dispatch path in executeToolDirect.
func newSkillThread(t *testing.T, skillsDir string) *Thread {
	t.Helper()
	srv := &Server{handlers: make(map[string]HandlerFunc)}
	RegisterToolHandlers(srv)
	return &Thread{
		server:                         srv,
		cwd:                            t.TempDir(),
		headless:                       true,
		enableAutomaticWritePermission: true,
		enableAutomaticDirectoryAccess: true,
		readFiles:                      make(map[string]bool),
		skills:                         agent.LoadSkills(skillsDir),
		projectConfig: ProjectConfig{
			ToolTimeouts: ToolTimeouts{Default: 30 * time.Second, Max: 60 * time.Second},
		},
	}
}

func writeSkill(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSkillTool_LoadsBodyAndBundledFiles(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "deploy", `---
name: deploy
description: Deploy the app
---
Deploy $ARGUMENTS now.
`)
	os.WriteFile(filepath.Join(dir, "checklist.md"), []byte("checklist"), 0o644)

	s := newSkillThread(t, root)
	res := s.executeToolDirect(context.Background(), "skill", map[string]any{
		"name":      "deploy",
		"arguments": "staging",
	})
	if res == nil || res.IsError {
		t.Fatalf("expected success, got %+v", res)
	}
	if !strings.Contains(res.Output, "Deploy staging now.") {
		t.Errorf("body not rendered with args: %q", res.Output)
	}
	if !strings.Contains(res.Output, filepath.Join(dir, "checklist.md")) {
		t.Errorf("bundled file path not listed: %q", res.Output)
	}
}

func TestSkillTool_MissingName(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "---\nname: deploy\ndescription: x\n---\nbody\n")
	s := newSkillThread(t, root)

	res := s.executeToolDirect(context.Background(), "skill", map[string]any{})
	if res == nil || !res.IsError {
		t.Fatalf("expected error for missing name, got %+v", res)
	}
}

func TestSkillTool_UnknownSkill(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "---\nname: deploy\ndescription: x\n---\nbody\n")
	s := newSkillThread(t, root)

	res := s.executeToolDirect(context.Background(), "skill", map[string]any{"name": "nope"})
	if res == nil || !res.IsError {
		t.Fatalf("expected error for unknown skill, got %+v", res)
	}
}

func TestSkillToolSchema_Shape(t *testing.T) {
	schema := SkillToolSchema()
	if schema.Name != "skill" {
		t.Errorf("name = %q, want skill", schema.Name)
	}
	required, _ := schema.InputSchema["required"].([]string)
	if len(required) != 1 || required[0] != "name" {
		t.Errorf("required = %v, want [name]", required)
	}
}

// TestSkillTool_ConfirmedPathDispatches guards the regression where workflow
// steps and subagents — which dispatch through executeToolConfirmed, not
// executeToolDirect — got "unknown tool: skill" even though the skill tool was
// advertised to them. Both paths must resolve the skill against the registry.
func TestSkillTool_ConfirmedPathDispatches(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "review-pr", `---
name: review-pr
description: Review a PR
---
Review $ARGUMENTS thoroughly.
`)
	os.WriteFile(filepath.Join(dir, "reference.md"), []byte("ref"), 0o644)

	s := newSkillThread(t, root)
	res := s.executeToolConfirmed(context.Background(), "skill", map[string]any{
		"name":      "review-pr",
		"arguments": "https://example.com/pr/1",
	})
	if res == nil || res.IsError {
		t.Fatalf("expected success, got %+v", res)
	}
	if strings.Contains(res.Output, "unknown tool") {
		t.Fatalf("skill dispatch regressed to unknown tool: %q", res.Output)
	}
	if !strings.Contains(res.Output, "Review https://example.com/pr/1 thoroughly.") {
		t.Errorf("body not rendered with args: %q", res.Output)
	}
	if !strings.Contains(res.Output, filepath.Join(dir, "reference.md")) {
		t.Errorf("bundled file path not listed: %q", res.Output)
	}
}

func TestSkillTool_ConfirmedPathUnknownSkill(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "---\nname: deploy\ndescription: x\n---\nbody\n")
	s := newSkillThread(t, root)

	res := s.executeToolConfirmed(context.Background(), "skill", map[string]any{"name": "nope"})
	if res == nil || !res.IsError {
		t.Fatalf("expected error for unknown skill, got %+v", res)
	}
	if strings.Contains(res.Output, "unknown tool") {
		t.Errorf("should report no-such-skill, not unknown tool: %q", res.Output)
	}
}
