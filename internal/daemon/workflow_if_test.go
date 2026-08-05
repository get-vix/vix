package daemon

import (
	"strings"
	"testing"
)

// The `if` node routes to exactly one edge (then/else) based on a bash
// condition, carries no model cost, and supports "stop" and an absent else.
func TestExecuteWorkflow_IfNode(t *testing.T) {
	// classify → gate(if) → heavy | light
	mk := func(classify string) *WorkflowDef {
		return &WorkflowDef{
			Name:       "iftest",
			EntryPoint: StepRef{ID: "classify"},
			Steps: map[string]WorkflowStepDef{
				"classify": {Type: "bash", Command: "echo " + classify, NextSteps: []StepRef{{ID: "gate"}}},
				"gate": {
					Type:      "if",
					Condition: `[[ "$(step.classify)" == *high* ]]`,
					Then:      &StepRef{ID: "heavy"},
					Else:      &StepRef{ID: "light"},
				},
				"heavy": {Type: "bash", Command: "echo HEAVY_PATH"},
				"light": {Type: "bash", Command: "echo LIGHT_PATH"},
			},
		}
	}

	t.Run("true routes to then", func(t *testing.T) {
		s := newWorkflowTestThread(t)
		wf := mk("high")
		if err := validateWorkflow(wf); err != nil {
			t.Fatalf("validate: %v", err)
		}
		if err := s.executeWorkflow(s.ctx, wf, "obj", nil); err != nil {
			t.Fatalf("executeWorkflow: %v", err)
		}
		out := streamedText(drainEvents(s))
		if !strings.Contains(out, "HEAVY_PATH") || strings.Contains(out, "LIGHT_PATH") {
			t.Errorf("condition true should take then (heavy), got:\n%s", out)
		}
	})

	t.Run("false routes to else", func(t *testing.T) {
		s := newWorkflowTestThread(t)
		wf := mk("low")
		if err := s.executeWorkflow(s.ctx, wf, "obj", nil); err != nil {
			t.Fatalf("executeWorkflow: %v", err)
		}
		out := streamedText(drainEvents(s))
		if !strings.Contains(out, "LIGHT_PATH") || strings.Contains(out, "HEAVY_PATH") {
			t.Errorf("condition false should take else (light), got:\n%s", out)
		}
	})

	t.Run("false with no else ends the run", func(t *testing.T) {
		s := newWorkflowTestThread(t)
		wf := &WorkflowDef{
			Name:       "iftest2",
			EntryPoint: StepRef{ID: "classify"},
			Steps: map[string]WorkflowStepDef{
				"classify": {Type: "bash", Command: "echo low", NextSteps: []StepRef{{ID: "gate"}}},
				"gate":     {Type: "if", Condition: `[[ "$(step.classify)" == *high* ]]`, Then: &StepRef{ID: "heavy"}},
				"heavy":    {Type: "bash", Command: "echo HEAVY_PATH"},
			},
		}
		if err := s.executeWorkflow(s.ctx, wf, "obj", nil); err != nil {
			t.Fatalf("executeWorkflow: %v", err)
		}
		out := streamedText(drainEvents(s))
		if strings.Contains(out, "HEAVY_PATH") {
			t.Errorf("absent else with false condition should end run, got:\n%s", out)
		}
	})

	t.Run("then=stop ends the run", func(t *testing.T) {
		s := newWorkflowTestThread(t)
		wf := &WorkflowDef{
			Name:       "iftest3",
			EntryPoint: StepRef{ID: "classify"},
			Steps: map[string]WorkflowStepDef{
				"classify": {Type: "bash", Command: "echo high", NextSteps: []StepRef{{ID: "gate"}}},
				"gate":     {Type: "if", Condition: `[[ "$(step.classify)" == *high* ]]`, Then: &StepRef{ID: "stop"}, Else: &StepRef{ID: "heavy"}},
				"heavy":    {Type: "bash", Command: "echo HEAVY_PATH"},
			},
		}
		if err := s.executeWorkflow(s.ctx, wf, "obj", nil); err != nil {
			t.Fatalf("executeWorkflow: %v", err)
		}
		out := streamedText(drainEvents(s))
		if strings.Contains(out, "HEAVY_PATH") {
			t.Errorf("then=stop should end run before heavy, got:\n%s", out)
		}
	})
}
