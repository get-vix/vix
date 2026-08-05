package daemon

import (
	"strings"
	"testing"
)

func TestWorkflowStepIDFromError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"bash step", errString("step 'deny' bash failed: exit status 1 (output: nope)"), "deny"},
		{"agent step", errString("step 'act' failed: boom"), "act"},
		{"no step", errString("something else went wrong"), ""},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workflowStepIDFromError(tc.err); got != tc.want {
				t.Errorf("workflowStepIDFromError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestRecordFailureNotice_EmptyReasonIgnored(t *testing.T) {
	s := newWorkflowTestThread(t)
	s.recordFailureNotice("deny", "   ")
	if len(s.failureNotices) != 0 {
		t.Fatalf("blank reason should be ignored, got %+v", s.failureNotices)
	}
}

func TestRecordFailureNotice_AnchorsAtEnd(t *testing.T) {
	s := newWorkflowTestThread(t)
	s.recordFailureNotice("detect", "workflow failed: step 'detect' bash failed")
	if len(s.failureNotices) != 1 {
		t.Fatalf("expected 1 failure notice, got %d", len(s.failureNotices))
	}
	n := s.failureNotices[0]
	if n.AfterIdx != -1 {
		t.Errorf("AfterIdx = %d, want -1 (no messages)", n.AfterIdx)
	}
	if n.StepID != "detect" || !strings.Contains(n.Reason, "step 'detect'") {
		t.Errorf("failure notice fields wrong: %+v", n)
	}
}

// A real workflow that aborts at a failing bash preflight step yields an error
// naming the step, which the runWorkflow path turns into a persisted failure
// notice so the reopened thread is not blank.
func TestExecuteWorkflow_FailingBashStepRecordsFailureNotice(t *testing.T) {
	s := newWorkflowTestThread(t)
	wf := &WorkflowDef{
		Name:       "preflight",
		EntryPoint: StepRef{ID: "deny"},
		Steps: map[string]WorkflowStepDef{
			"deny": {Type: "bash", Command: "echo 'no GitHub access' && exit 1"},
		},
	}
	if err := validateWorkflow(wf); err != nil {
		t.Fatalf("workflow should validate: %v", err)
	}
	err := s.executeWorkflow(s.ctx, wf, "obj", nil)
	if err == nil {
		t.Fatal("expected workflow error from failing bash step")
	}
	if stepID := workflowStepIDFromError(err); stepID != "deny" {
		t.Fatalf("step id from error = %q, want deny (err: %v)", stepID, err)
	}
	// Mirror what runWorkflow does with the returned error.
	s.recordFailureNotice(workflowStepIDFromError(err), "workflow failed: "+err.Error())

	rec := s.buildRecord()
	if len(rec.FailureNotices) != 1 {
		t.Fatalf("expected 1 persisted failure notice, got %d", len(rec.FailureNotices))
	}
	if rec.FailureNotices[0].StepID != "deny" || !strings.Contains(rec.FailureNotices[0].Reason, "no GitHub access") {
		t.Errorf("persisted failure notice wrong: %+v", rec.FailureNotices[0])
	}

	// The reopened thread replays the failure as a system/error block instead
	// of a blank transcript.
	out := buildReplayMessages(rec.Messages, rec.RetryNotices, rec.FailureNotices)
	var sawError bool
	for _, m := range out {
		for _, b := range m.Blocks {
			if b.Kind == "error" && strings.Contains(b.Text, "no GitHub access") {
				sawError = true
			}
		}
	}
	if !sawError {
		t.Fatalf("replay should surface the failure as an error block, got %+v", out)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
