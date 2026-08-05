package config

import (
	"encoding/json"
	"testing"

	"github.com/get-vix/vix/internal/workflow"
)

// TestEmbeddedWorkflowsValidate guards the shipped config/workflow.json: every
// workflow must parse and pass workflow.Validate, so a bad edit (e.g. an if
// node with a dangling then/else) fails the build instead of shipping.
func TestEmbeddedWorkflowsValidate(t *testing.T) {
	var f workflow.File
	if err := json.Unmarshal([]byte(embeddedDefault(t, "config/workflow.json")), &f); err != nil {
		t.Fatalf("unmarshal workflow.json: %v", err)
	}
	if len(f.Workflows) == 0 {
		t.Fatal("no workflows in embedded config/workflow.json")
	}
	for i := range f.Workflows {
		wf := f.Workflows[i]
		if err := workflow.Validate(&wf); err != nil {
			t.Errorf("embedded workflow %q fails validation: %v", wf.Name, err)
		}
	}
}

// TestGoalWorkflowMigratedToIfNodes locks in the Goal migration: the pursue and
// verify routing is expressed with if nodes, and no execute_if edges linger on
// those steps (the pattern the migration replaced).
func TestGoalWorkflowMigratedToIfNodes(t *testing.T) {
	var f workflow.File
	if err := json.Unmarshal([]byte(embeddedDefault(t, "config/workflow.json")), &f); err != nil {
		t.Fatalf("unmarshal workflow.json: %v", err)
	}
	var goal *workflow.Def
	for i := range f.Workflows {
		if f.Workflows[i].Name == "Goal" {
			goal = &f.Workflows[i]
			break
		}
	}
	if goal == nil {
		t.Fatal("Goal workflow not found")
	}

	// The routing nodes exist and are if nodes.
	for _, id := range []string{"route_after_pursue", "route_pursue_complete", "route_after_verify"} {
		step, ok := goal.Steps[id]
		if !ok {
			t.Errorf("Goal missing expected if node %q", id)
			continue
		}
		if step.Type != "if" {
			t.Errorf("Goal step %q: type = %q, want if", id, step.Type)
		}
	}

	// pursue and verify route via a single unconditional edge into the if
	// chain — no execute_if edges remain on them.
	for _, id := range []string{"pursue", "verify"} {
		step := goal.Steps[id]
		for _, ns := range step.NextSteps {
			if ns.ExecuteIf != "" {
				t.Errorf("Goal step %q still carries an execute_if edge to %q after migration", id, ns.ID)
			}
		}
	}
}
