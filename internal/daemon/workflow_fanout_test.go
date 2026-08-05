package daemon

import (
	"strings"
	"testing"
)

// resolveOverList turns a $(...) reference into a typed list from either the
// typed var pool, a typed step result, or a JSON-array string.
func TestResolveOverList(t *testing.T) {
	results := map[string]*StepResult{
		"discover": {Output: `["a","b"]`, Value: []any{"a", "b"}},
		"text":     {Output: `["x","y","z"]`},
	}
	t.Run("typed step value", func(t *testing.T) {
		got, err := resolveOverList("$(step.discover)", nil, results)
		if err != nil || len(got) != 2 {
			t.Fatalf("got %v err %v", got, err)
		}
	})
	t.Run("typed var pool", func(t *testing.T) {
		got, err := resolveOverList("$(mylist)", map[string]any{"mylist": []any{1.0, 2.0, 3.0}}, results)
		if err != nil || len(got) != 3 {
			t.Fatalf("got %v err %v", got, err)
		}
	})
	t.Run("json-array string", func(t *testing.T) {
		got, err := resolveOverList("$(step.text)", nil, results)
		if err != nil || len(got) != 3 {
			t.Fatalf("got %v err %v", got, err)
		}
	})
	t.Run("missing reference errors", func(t *testing.T) {
		if _, err := resolveOverList("$(nope)", nil, results); err == nil {
			t.Fatal("expected error for missing reference")
		}
	})
	t.Run("non-list errors", func(t *testing.T) {
		bad := map[string]*StepResult{"s": {Output: "plain text"}}
		if _, err := resolveOverList("$(step.s)", nil, bad); err == nil {
			t.Fatal("expected error for non-list value")
		}
	})
}

// A fan_out over a model/bash-produced list runs one branch per element and a
// fan_in joins them; the run then continues past the fan_in.
func TestExecuteWorkflow_FanOutFanIn(t *testing.T) {
	t.Run("string list, all branches run and join routes on", func(t *testing.T) {
		s := newWorkflowTestThread(t)
		wf := &WorkflowDef{
			Name:       "fan",
			EntryPoint: StepRef{ID: "discover"},
			Steps: map[string]WorkflowStepDef{
				"discover": {Type: "bash", Command: `echo '["a","b","c"]'`, JSONOutput: true, NextSteps: []StepRef{{ID: "fanout"}}},
				"fanout": {
					Type: "fan_out", Over: "$(step.discover)", As: "item", BarrierID: "B",
					Branch: &StepRef{ID: "work"}, NextSteps: []StepRef{{ID: "join"}},
				},
				"work":   {Type: "bash", Command: `echo "processed-$(item)"`},
				"join":   {Type: "fan_in", BarrierID: "B", As: "results", NextSteps: []StepRef{{ID: "report"}}},
				"report": {Type: "bash", Command: `echo JOINED_AND_CONTINUED`},
			},
		}
		if err := validateWorkflow(wf); err != nil {
			t.Fatalf("validate: %v", err)
		}
		if err := s.executeWorkflow(s.ctx, wf, "obj", nil); err != nil {
			t.Fatalf("executeWorkflow: %v", err)
		}
		out := streamedText(drainEvents(s))
		for _, want := range []string{"processed-a", "processed-b", "processed-c", "JOINED_AND_CONTINUED"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in output:\n%s", want, out)
			}
		}
	})

	t.Run("object list with per-item field access", func(t *testing.T) {
		s := newWorkflowTestThread(t)
		wf := &WorkflowDef{
			Name:       "fanobj",
			EntryPoint: StepRef{ID: "discover"},
			Steps: map[string]WorkflowStepDef{
				"discover": {Type: "bash", Command: `echo '[{"name":"x"},{"name":"y"}]'`, JSONOutput: true, NextSteps: []StepRef{{ID: "fanout"}}},
				"fanout":   {Type: "fan_out", Over: "$(step.discover)", As: "item", BarrierID: "B", Branch: &StepRef{ID: "work"}, NextSteps: []StepRef{{ID: "join"}}},
				"work":     {Type: "bash", Command: `echo "got-$(item.name)"`},
				"join":     {Type: "fan_in", BarrierID: "B", As: "results"},
			},
		}
		if err := s.executeWorkflow(s.ctx, wf, "obj", nil); err != nil {
			t.Fatalf("executeWorkflow: %v", err)
		}
		out := streamedText(drainEvents(s))
		for _, want := range []string{"got-x", "got-y"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in output:\n%s", want, out)
			}
		}
	})
}

// Branch-failure policy is configurable: abort (default) fails the run, collect
// drops the failed branch and continues.
func TestExecuteWorkflow_FanOutBranchFailure(t *testing.T) {
	mk := func(policy string) *WorkflowDef {
		return &WorkflowDef{
			Name:       "fanfail",
			EntryPoint: StepRef{ID: "discover"},
			Steps: map[string]WorkflowStepDef{
				"discover": {Type: "bash", Command: `echo '["good","bad"]'`, JSONOutput: true, NextSteps: []StepRef{{ID: "fanout"}}},
				"fanout":   {Type: "fan_out", Over: "$(step.discover)", As: "item", BarrierID: "B", Branch: &StepRef{ID: "work"}, NextSteps: []StepRef{{ID: "join"}}},
				"work":     {Type: "bash", Command: `if [ "$(item)" = "bad" ]; then exit 1; fi; echo "ok-$(item)"`},
				"join":     {Type: "fan_in", BarrierID: "B", As: "results", OnBranchError: policy, NextSteps: []StepRef{{ID: "report"}}},
				"report":   {Type: "bash", Command: `echo AFTER_JOIN`},
			},
		}
	}

	t.Run("abort fails the run", func(t *testing.T) {
		s := newWorkflowTestThread(t)
		wf := mk("abort")
		if err := s.executeWorkflow(s.ctx, wf, "obj", nil); err == nil {
			t.Fatal("expected executeWorkflow to fail when a branch aborts")
		}
		out := streamedText(drainEvents(s))
		if strings.Contains(out, "AFTER_JOIN") {
			t.Errorf("abort should stop before the fan_in continuation, got:\n%s", out)
		}
	})

	t.Run("collect drops the failed branch and continues", func(t *testing.T) {
		s := newWorkflowTestThread(t)
		wf := mk("collect")
		if err := s.executeWorkflow(s.ctx, wf, "obj", nil); err != nil {
			t.Fatalf("collect should not fail the run: %v", err)
		}
		out := streamedText(drainEvents(s))
		if !strings.Contains(out, "ok-good") || !strings.Contains(out, "AFTER_JOIN") {
			t.Errorf("collect should keep the good branch and continue, got:\n%s", out)
		}
	})
}

// Each branch can decide to run additional steps (the per-branch pipeline): an
// if node inside the branch routes only some items through an extra step.
func TestExecuteWorkflow_FanOutPerBranchPipeline(t *testing.T) {
	s := newWorkflowTestThread(t)
	wf := &WorkflowDef{
		Name:       "fanpipe",
		EntryPoint: StepRef{ID: "discover"},
		Steps: map[string]WorkflowStepDef{
			"discover": {Type: "bash", Command: `echo '["deep","shallow"]'`, JSONOutput: true, NextSteps: []StepRef{{ID: "fanout"}}},
			"fanout":   {Type: "fan_out", Over: "$(step.discover)", As: "item", BarrierID: "B", Branch: &StepRef{ID: "classify"}, NextSteps: []StepRef{{ID: "join"}}},
			"classify": {
				Type: "if", Condition: `[[ "$(item)" == *deep* ]]`,
				Then: &StepRef{ID: "extra"}, Else: &StepRef{ID: "quick"},
			},
			"extra": {Type: "bash", Command: `echo "EXTRA-$(item)"`},
			"quick": {Type: "bash", Command: `echo "QUICK-$(item)"`},
			"join":  {Type: "fan_in", BarrierID: "B", As: "results"},
		},
	}
	if err := validateWorkflow(wf); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := s.executeWorkflow(s.ctx, wf, "obj", nil); err != nil {
		t.Fatalf("executeWorkflow: %v", err)
	}
	out := streamedText(drainEvents(s))
	if !strings.Contains(out, "EXTRA-deep") {
		t.Errorf("deep item should route through the extra step, got:\n%s", out)
	}
	if !strings.Contains(out, "QUICK-shallow") {
		t.Errorf("shallow item should route through the quick step, got:\n%s", out)
	}
	if strings.Contains(out, "EXTRA-shallow") || strings.Contains(out, "QUICK-deep") {
		t.Errorf("branches routed incorrectly, got:\n%s", out)
	}
}
