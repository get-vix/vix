package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHandleWorkflowCommand_InlineRegistersAndRuns drives the inline-workflow
// dispatch path: a self-contained definition (no entry in config/workflow.json)
// is registered transiently into the thread's workflow set and executed.
func TestHandleWorkflowCommand_InlineRegistersAndRuns(t *testing.T) {
	s := newWorkflowTestThread(t)
	def := &WorkflowDef{
		Name:       "inline-watch",
		EntryPoint: StepRef{ID: "poll"},
		Steps: map[string]WorkflowStepDef{
			"poll": {Type: "bash", Command: "echo inline-ran"},
		},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}

	if len(s.snapshotWorkflows()) != 0 {
		t.Fatalf("expected no workflows preloaded, got %d", len(s.snapshotWorkflows()))
	}

	// Name is empty: the thread must resolve the run from the inline definition.
	s.handleWorkflowCommand("", "objective", raw)

	found := false
	for _, w := range s.snapshotWorkflows() {
		if w.Name == "inline-watch" {
			found = true
		}
	}
	if !found {
		t.Errorf("inline workflow was not registered into the thread set")
	}

	out := streamedText(drainEvents(s))
	if !strings.Contains(out, "inline-ran") {
		t.Errorf("expected inline workflow bash output, got:\n%s", out)
	}
}

// TestHandleWorkflowCommand_FinishedInlineRunResetsToChat verifies a finished
// inline (transient) workflow run drops back to chat mode and clears the active
// workflow, so reopening the persisted run never warns that the unpersisted
// workflow "no longer exists".
func TestHandleWorkflowCommand_FinishedInlineRunResetsToChat(t *testing.T) {
	s := newWorkflowTestThread(t)
	def := &WorkflowDef{
		Name:       "plan-issues-get-vix-vix",
		EntryPoint: StepRef{ID: "poll"},
		Steps: map[string]WorkflowStepDef{
			"poll": {Type: "bash", Command: "echo done"},
		},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}

	s.handleWorkflowCommand("", "objective", raw)

	if s.threadMode != "chat" {
		t.Errorf("threadMode = %q, want %q after a finished inline run", s.threadMode, "chat")
	}
	if s.activeWorkflow != "" {
		t.Errorf("activeWorkflow = %q, want empty after a finished inline run", s.activeWorkflow)
	}
}

// TestHandleWorkflowCommand_FailedInlineRunResetsToChat verifies that a
// *failed* inline run (terminal step error, not a cancellation) also drops back
// to chat mode and clears its run state — so reopening a failed scheduled run
// replays its transcript instead of warning the workflow "no longer exists".
func TestHandleWorkflowCommand_FailedInlineRunResetsToChat(t *testing.T) {
	s := newWorkflowTestThread(t)
	def := &WorkflowDef{
		Name:       "plan-issues-get-vix-vix",
		EntryPoint: StepRef{ID: "boom"},
		Steps: map[string]WorkflowStepDef{
			"boom": {Type: "bash", Command: "exit 1"},
		},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}

	s.handleWorkflowCommand("", "objective", raw)

	if s.threadMode != "chat" {
		t.Errorf("threadMode = %q, want %q after a failed inline run", s.threadMode, "chat")
	}
	if s.activeWorkflow != "" {
		t.Errorf("activeWorkflow = %q, want empty after a failed inline run", s.activeWorkflow)
	}
	if st := s.snapshotWorkflowRunState(); st != nil {
		t.Errorf("workflow run state = %+v, want nil (a failed inline run is not resumable)", st)
	}
}

// TestHandleWorkflowCommand_InvalidInlineErrors verifies a structurally invalid
// inline definition is rejected before any registration or execution.
func TestHandleWorkflowCommand_InvalidInlineErrors(t *testing.T) {
	s := newWorkflowTestThread(t)
	raw, _ := json.Marshal(&WorkflowDef{Name: "broken"}) // no steps → invalid

	s.handleWorkflowCommand("", "objective", raw)

	var sawErr bool
	for _, ev := range drainEvents(s) {
		if ev.Type == "event.error" {
			sawErr = true
		}
	}
	if !sawErr {
		t.Error("expected event.error for invalid inline workflow")
	}
	if len(s.snapshotWorkflows()) != 0 {
		t.Error("invalid inline workflow must not be registered")
	}
}
