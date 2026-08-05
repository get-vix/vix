package daemon

import (
	"strings"
	"testing"

	"github.com/get-vix/vix/internal/daemon/llm"
)

func bp(b bool) *bool { return &b }

// firstText returns the concatenated text of a message's text blocks.
func firstText(m llm.MessageParam) string {
	var sb strings.Builder
	for _, b := range m.Content {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

// ── recordTranscriptEntry gating ──

func TestRecordTranscriptEntry_Gating(t *testing.T) {
	cases := []struct {
		name   string
		step   WorkflowStepDef
		output string
		want   bool // expect an entry captured
	}{
		{"visible agent", WorkflowStepDef{Type: "agent"}, "the plan", true},
		{"agent stream explicit true", WorkflowStepDef{Type: "agent", Stream: bp(true)}, "x", true},
		{"silent agent skipped", WorkflowStepDef{Type: "agent", Silent: true}, "x", false},
		{"non-stream agent skipped", WorkflowStepDef{Type: "agent", Stream: bp(false)}, "x", false},
		{"bash step skipped", WorkflowStepDef{Type: "bash"}, "x", false},
		{"tool step skipped", WorkflowStepDef{Type: "tool"}, "x", false},
		{"empty output skipped", WorkflowStepDef{Type: "agent"}, "   \n ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &WorkflowRun{}
			exec.recordTranscriptEntry(tc.step, "s1", tc.output)
			got := len(exec.snapshotTranscript()) == 1
			if got != tc.want {
				t.Fatalf("captured=%v, want %v", got, tc.want)
			}
		})
	}
}

// ── appendWorkflowTranscript: full agent history ──

// visibleAgent records one visible agent-step transcript entry for stepID.
func visibleAgent(exec *WorkflowRun, stepID string) {
	exec.recordTranscriptEntry(WorkflowStepDef{Type: "agent"}, stepID, "output")
}

// rolesAlternate reports whether no two consecutive messages share a role.
func rolesAlternate(msgs []llm.MessageParam) bool {
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == msgs[i-1].Role {
			return false
		}
	}
	return true
}

// rolesOf returns the role sequence, for failure messages.
func rolesOf(msgs []llm.MessageParam) []llm.Role {
	out := make([]llm.Role, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}

// hasBlock reports whether any message carries a block of the given type.
func hasBlock(msgs []llm.MessageParam, t llm.ContentBlockType) bool {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == t {
				return true
			}
		}
	}
	return false
}

func TestAppendWorkflowTranscript_SplicesFullAgentHistory(t *testing.T) {
	s := newWorkflowTestThread(t)
	exec := &WorkflowRun{StepAgents: map[string]*AgentRunner{}}
	visibleAgent(exec, "plan")
	exec.StepAgents["plan"] = &AgentRunner{Messages: []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("the plan-step prompt")),
		llm.NewAssistantMessage(llm.NewToolUseBlock("t1", "read_file", map[string]any{"path": "/x"})),
		llm.NewUserMessage(llm.NewToolResultBlock("t1", "file contents", false)),
		llm.NewAssistantMessage(llm.NewTextBlock("Hi, I investigated issue #29 …")),
	}}

	// Anchor is ignored when the step has an agent instance.
	s.appendWorkflowTranscript("ignored anchor", exec)

	if len(s.messages) != 4 {
		t.Fatalf("expected the 4 full-history messages, got %d", len(s.messages))
	}
	if firstText(s.messages[0]) != "the plan-step prompt" {
		t.Errorf("first message = %q, want the real prompt", firstText(s.messages[0]))
	}
	if !hasBlock(s.messages, llm.BlockToolUse) || !hasBlock(s.messages, llm.BlockToolResult) {
		t.Error("expected the real tool_use/tool_result blocks to be preserved")
	}
	if !rolesAlternate(s.messages) {
		t.Error("messages should alternate user/assistant")
	}
}

func TestAppendWorkflowTranscript_DropsThinkingBlocks(t *testing.T) {
	s := newWorkflowTestThread(t)
	exec := &WorkflowRun{StepAgents: map[string]*AgentRunner{}}
	visibleAgent(exec, "plan")
	exec.StepAgents["plan"] = &AgentRunner{Messages: []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("prompt")),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockThinking, Text: "secret reasoning", Signature: "sig"},
			{Type: llm.BlockText, Text: "the answer"},
		}},
	}}

	s.appendWorkflowTranscript("anchor", exec)

	if hasBlock(s.messages, llm.BlockThinking) {
		t.Error("thinking blocks must be stripped from the persisted transcript")
	}
	if !hasBlock(s.messages, llm.BlockText) {
		t.Error("text blocks should survive the thinking strip")
	}
}

func TestAppendWorkflowTranscript_ConcatenatesAndCoalesces(t *testing.T) {
	s := newWorkflowTestThread(t)
	exec := &WorkflowRun{StepAgents: map[string]*AgentRunner{}}
	visibleAgent(exec, "explore")
	visibleAgent(exec, "plan")
	// First step ends on a user (tool_result) turn; second begins with a user
	// prompt — the boundary must be coalesced to keep alternation valid.
	exec.StepAgents["explore"] = &AgentRunner{Messages: []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("explore prompt")),
		llm.NewAssistantMessage(llm.NewToolUseBlock("t1", "grep", map[string]any{"q": "x"})),
		llm.NewUserMessage(llm.NewToolResultBlock("t1", "hits", false)),
	}}
	exec.StepAgents["plan"] = &AgentRunner{Messages: []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("plan prompt")),
		llm.NewAssistantMessage(llm.NewTextBlock("the plan")),
	}}

	s.appendWorkflowTranscript("anchor", exec)

	if !rolesAlternate(s.messages) {
		t.Fatalf("expected coalesced alternation; roles: %v", rolesOf(s.messages))
	}
	if !hasBlock(s.messages, llm.BlockToolResult) {
		t.Error("explore step's tool_result should be preserved")
	}
}

func TestAppendWorkflowTranscript_SplicesEachStepOnce(t *testing.T) {
	s := newWorkflowTestThread(t)
	exec := &WorkflowRun{StepAgents: map[string]*AgentRunner{}}
	// A looping step records two visible entries but shares one accumulating
	// agent — its history must be spliced exactly once.
	visibleAgent(exec, "loop")
	visibleAgent(exec, "loop")
	exec.StepAgents["loop"] = &AgentRunner{Messages: []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("p")),
		llm.NewAssistantMessage(llm.NewTextBlock("a")),
	}}

	s.appendWorkflowTranscript("anchor", exec)

	if len(s.messages) != 2 {
		t.Fatalf("expected the history spliced once (2 messages), got %d", len(s.messages))
	}
}

func TestAppendWorkflowTranscript_TextFallbackWhenNoAgent(t *testing.T) {
	s := newWorkflowTestThread(t)
	exec := &WorkflowRun{StepAgents: map[string]*AgentRunner{}}
	exec.recordTranscriptEntry(WorkflowStepDef{Type: "agent"}, "plan", "Here is the plan.\n")
	// No StepAgents["plan"] → fall back to user(anchor)→assistant(text).

	s.appendWorkflowTranscript("plan the work", exec)

	if len(s.messages) != 2 {
		t.Fatalf("expected user+assistant fallback pair, got %d", len(s.messages))
	}
	if firstText(s.messages[0]) != "plan the work" {
		t.Errorf("user anchor = %q", firstText(s.messages[0]))
	}
	if got := firstText(s.messages[1]); got != "Here is the plan." {
		t.Errorf("assistant body = %q", got)
	}
}

func TestAppendWorkflowTranscript_EmptyNoop(t *testing.T) {
	s := newWorkflowTestThread(t)
	s.appendWorkflowTranscript("kickoff", &WorkflowRun{})
	if len(s.messages) != 0 {
		t.Fatalf("expected no messages for empty transcript, got %d", len(s.messages))
	}
}

// ── failed runs replay like successful ones ──

func TestRecordFailedAgentStep_Gating(t *testing.T) {
	exec := &WorkflowRun{}
	exec.recordFailedAgentStep(WorkflowStepDef{Type: "bash"}, "b")
	if len(exec.snapshotTranscript()) != 0 {
		t.Fatal("non-agent step should not be recorded on failure")
	}
	// A failed agent step is recorded even with no output, so its partial
	// working history still mirrors into the transcript.
	exec.recordFailedAgentStep(WorkflowStepDef{Type: "agent"}, "a")
	if len(exec.snapshotTranscript()) != 1 {
		t.Fatal("failed agent step should be recorded even with no output")
	}
}

func TestAppendWorkflowTranscript_FailedStepSplicesPartialHistoryAndRetries(t *testing.T) {
	s := newWorkflowTestThread(t)
	exec := &WorkflowRun{StepAgents: map[string]*AgentRunner{}}
	// Failed step: recorded via recordFailedAgentStep (no final text), but its
	// agent produced a tool_use/tool_result before dying, plus retry notices.
	exec.recordFailedAgentStep(WorkflowStepDef{Type: "agent"}, "plan")
	exec.recordRetry("plan", "API overloaded", 7, 10, 32)
	exec.StepAgents["plan"] = &AgentRunner{Messages: []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("plan prompt")),
		llm.NewAssistantMessage(llm.NewToolUseBlock("t1", "read_file", map[string]any{"path": "/x"})),
		llm.NewUserMessage(llm.NewToolResultBlock("t1", "contents", false)),
	}}

	s.appendWorkflowTranscript("anchor", exec)

	if len(s.messages) != 3 {
		t.Fatalf("expected the failed step's partial history (3 msgs), got %d", len(s.messages))
	}
	if !hasBlock(s.messages, llm.BlockToolUse) || !hasBlock(s.messages, llm.BlockToolResult) {
		t.Error("failed step's tool_use/tool_result should be preserved")
	}
	if len(s.retryNotices) != 1 {
		t.Fatalf("expected 1 retry notice captured, got %d", len(s.retryNotices))
	}
	n := s.retryNotices[0]
	if n.AfterIdx != len(s.messages)-1 {
		t.Errorf("retry AfterIdx = %d, want %d (end of transcript)", n.AfterIdx, len(s.messages)-1)
	}
	if n.Attempt != 7 || n.MaxRetries != 10 || n.WaitSecs != 32 || n.Reason != "API overloaded" {
		t.Errorf("retry notice fields wrong: %+v", n)
	}
}

func TestAppendWorkflowTranscript_RetryNoticesWithoutMessages(t *testing.T) {
	s := newWorkflowTestThread(t)
	exec := &WorkflowRun{StepAgents: map[string]*AgentRunner{}}
	// A run that failed before producing any agent message still records its
	// retries, anchored before everything (-1).
	exec.recordRetry("plan", "API overloaded", 1, 10, 1)

	s.appendWorkflowTranscript("anchor", exec)

	if len(s.messages) != 0 {
		t.Fatalf("expected no messages, got %d", len(s.messages))
	}
	if len(s.retryNotices) != 1 || s.retryNotices[0].AfterIdx != -1 {
		t.Fatalf("expected one retry notice anchored at -1, got %+v", s.retryNotices)
	}
}

// ── integration: a bash-only run records nothing ──

func TestExecuteWorkflow_BashOnlyLeavesTranscriptEmpty(t *testing.T) {
	s := newWorkflowTestThread(t)
	wf := &WorkflowDef{
		Name:       "echoer",
		EntryPoint: StepRef{ID: "say"},
		Steps: map[string]WorkflowStepDef{
			"say": {Type: "bash", Command: "echo hello"},
		},
	}
	if err := validateWorkflow(wf); err != nil {
		t.Fatalf("workflow should validate: %v", err)
	}
	if err := s.executeWorkflow(s.ctx, wf, "obj", nil); err != nil {
		t.Fatalf("executeWorkflow: %v", err)
	}
	if len(s.messages) != 0 {
		t.Fatalf("bash-only run should not touch the chat transcript, got %d messages", len(s.messages))
	}
}
