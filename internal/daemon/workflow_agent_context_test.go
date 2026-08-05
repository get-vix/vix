package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/agent"
	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/llm"
)

// TestEnsureWorkflowAgentContext verifies a workflow agent step inherits the
// thread's project context — AGENTS.md (gated by read_agents_md) and the
// skills catalog — in its system prompt, and gains the invocable `skill` tool,
// idempotently across repeated calls.
func TestEnsureWorkflowAgentContext(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("PROJECT RULES HERE"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillsRoot := t.TempDir()
	writeSkill(t, skillsRoot, "deploy", "---\nname: deploy\ndescription: Deploy the app\n---\nbody\n")

	srv := &Server{handlers: make(map[string]HandlerFunc)}
	RegisterToolHandlers(srv)
	s := &Thread{
		server:    srv,
		cwd:       cwd,
		headless:  true,
		readFiles: make(map[string]bool),
		skills:    agent.LoadSkills(skillsRoot),
		paths:     config.NewVixPaths("", t.TempDir(), cwd),
		projectConfig: ProjectConfig{
			Features:     map[string]bool{FeatureReadAgentsMD: true},
			ToolTimeouts: ToolTimeouts{Default: 30 * time.Second, Max: 60 * time.Second},
		},
	}

	a := &AgentRunner{
		System: []llm.SystemBlock{{Text: "base sub-agent prompt"}},
		Tools:  []llm.ToolParam{{Name: "read_file"}},
	}
	s.ensureWorkflowAgentContext(a)

	var joined string
	for _, b := range a.System {
		joined += b.Text + "\n"
	}
	if !strings.Contains(joined, "PROJECT RULES HERE") {
		t.Errorf("AGENTS.md not injected into the workflow agent system prompt:\n%s", joined)
	}
	if !strings.Contains(joined, "deploy") {
		t.Errorf("skills metadata not injected into the workflow agent system prompt:\n%s", joined)
	}

	hasSkill := false
	for _, tl := range a.Tools {
		if tl.Name == "skill" {
			hasSkill = true
		}
	}
	if !hasSkill {
		t.Error("skill tool was not added to the workflow agent")
	}

	// Idempotent: a second call must not duplicate blocks or tools.
	sysN, toolN := len(a.System), len(a.Tools)
	s.ensureWorkflowAgentContext(a)
	if len(a.System) != sysN || len(a.Tools) != toolN {
		t.Errorf("ensureWorkflowAgentContext not idempotent: system %d→%d, tools %d→%d",
			sysN, len(a.System), toolN, len(a.Tools))
	}
}

// TestEnsureWorkflowAgentContext_NoSkillsNoTool verifies the skill tool is not
// added when the thread has no skills loaded.
func TestEnsureWorkflowAgentContext_NoSkillsNoTool(t *testing.T) {
	srv := &Server{handlers: make(map[string]HandlerFunc)}
	RegisterToolHandlers(srv)
	s := &Thread{
		server:        srv,
		cwd:           t.TempDir(),
		headless:      true,
		readFiles:     make(map[string]bool),
		skills:        agent.LoadSkills(t.TempDir()), // empty registry
		paths:         config.NewVixPaths("", t.TempDir(), t.TempDir()),
		projectConfig: ProjectConfig{ToolTimeouts: ToolTimeouts{Default: 30 * time.Second, Max: 60 * time.Second}},
	}
	a := &AgentRunner{Tools: []llm.ToolParam{{Name: "read_file"}}}
	s.ensureWorkflowAgentContext(a)
	for _, tl := range a.Tools {
		if tl.Name == "skill" {
			t.Error("skill tool must not be added when no skills are loaded")
		}
	}
}

// TestWorkflowAgentSkillAdvertiseDispatchParity ties the two halves together:
// once ensureWorkflowAgentContext advertises the `skill` tool to a workflow
// agent, the confirmed-dispatch path used by workflow steps must actually be
// able to run it (returning the skill body, not "unknown tool: skill").
func TestWorkflowAgentSkillAdvertiseDispatchParity(t *testing.T) {
	cwd := t.TempDir()
	skillsRoot := t.TempDir()
	writeSkill(t, skillsRoot, "review-pr", "---\nname: review-pr\ndescription: Review a PR\n---\nReview it.\n")

	srv := &Server{handlers: make(map[string]HandlerFunc)}
	RegisterToolHandlers(srv)
	s := &Thread{
		server:                         srv,
		cwd:                            cwd,
		headless:                       true,
		enableAutomaticWritePermission: true,
		enableAutomaticDirectoryAccess: true,
		readFiles:                      make(map[string]bool),
		skills:                         agent.LoadSkills(skillsRoot),
		paths:                          config.NewVixPaths("", t.TempDir(), cwd),
		projectConfig: ProjectConfig{
			ToolTimeouts: ToolTimeouts{Default: 30 * time.Second, Max: 60 * time.Second},
		},
	}

	a := &AgentRunner{Tools: []llm.ToolParam{{Name: "read_file"}}}
	s.ensureWorkflowAgentContext(a)

	advertised := false
	for _, tl := range a.Tools {
		if tl.Name == "skill" {
			advertised = true
		}
	}
	if !advertised {
		t.Fatal("skill tool was not advertised to the workflow agent")
	}

	res := s.executeToolConfirmed(t.Context(), "skill", map[string]any{"name": "review-pr"})
	if res == nil || res.IsError {
		t.Fatalf("advertised skill must dispatch, got %+v", res)
	}
	if strings.Contains(res.Output, "unknown tool") {
		t.Fatalf("advertised skill dispatched to unknown tool: %q", res.Output)
	}
	if !strings.Contains(res.Output, "Review it.") {
		t.Errorf("skill body not returned: %q", res.Output)
	}
}
