package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/protocol"
)

// newWorkflowTestSession builds a Session wired just enough to drive
// executeWorkflow with bash-only workflows: no network, persistence disabled
// (zero VixPaths), and a generously buffered event channel.
func newWorkflowTestSession(t *testing.T) *Session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Session{
		id:        "wf-test",
		cwd:       t.TempDir(),
		model:     "anthropic/claude-opus-4-8",
		llm:       &fakeCompactionLLM{},
		eventChan: make(chan protocol.SessionEvent, 1024),
		ctx:       ctx,
		projectConfig: ProjectConfig{
			ToolTimeouts:     ToolTimeouts{Default: defaultToolTimeoutDefault, Max: defaultToolTimeoutMax},
			BashStepTimeouts: BashStepTimeouts{Default: defaultBashStepTimeoutDefault, Max: defaultBashStepTimeoutMax},
		},
	}
}

// drainEvents collects all buffered events from the session channel.
func drainEvents(s *Session) []protocol.SessionEvent {
	var evs []protocol.SessionEvent
	for {
		select {
		case ev := <-s.eventChan:
			evs = append(evs, ev)
		default:
			return evs
		}
	}
}

func streamedText(evs []protocol.SessionEvent) string {
	var sb strings.Builder
	for _, ev := range evs {
		if ev.Type == "event.stream_chunk" {
			if c, ok := ev.Data.(protocol.EventStreamChunk); ok {
				sb.WriteString(c.Text)
			}
		}
	}
	return sb.String()
}

// ── budget gating ──

func TestExecuteWorkflow_BudgetIterationsRoutesToOnExceeded(t *testing.T) {
	s := newWorkflowTestSession(t)
	wf := &WorkflowDef{
		Name:       "loop",
		Budget:     &WorkflowBudget{MaxIterations: 3, OnExceeded: &StepRef{ID: "wrapup"}},
		EntryPoint: StepRef{ID: "work"},
		Steps: map[string]WorkflowStepDef{
			"work":   {Type: "bash", Command: "echo iter-$(workflow.iteration)", NextSteps: []StepRef{{ID: "work"}}},
			"wrapup": {Type: "bash", Command: "echo wrapping-$(workflow.status)"},
		},
	}
	if err := validateWorkflow(wf); err != nil {
		t.Fatalf("workflow should validate: %v", err)
	}

	if err := s.executeWorkflow(s.ctx, wf, "obj", nil); err != nil {
		t.Fatalf("executeWorkflow: %v", err)
	}

	evs := drainEvents(s)
	out := streamedText(evs)
	for _, want := range []string{"iter-1", "iter-2", "iter-3", "wrapping-budget_limited"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in streamed output, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "iter-4") {
		t.Errorf("budget should have stopped the loop after 3 iterations, got:\n%s", out)
	}

	var sawBudgetStatus bool
	for _, ev := range evs {
		if ev.Type == "event.workflow_status" {
			if ws, ok := ev.Data.(protocol.EventWorkflowStatus); ok && ws.Status == WorkflowStatusBudgetLimited {
				sawBudgetStatus = true
			}
		}
	}
	if !sawBudgetStatus {
		t.Error("expected an event.workflow_status with status budget_limited")
	}

	// Completed runs clear their persisted state.
	if st := s.snapshotWorkflowRunState(); st != nil {
		t.Errorf("run state should be cleared after completion, got %+v", st)
	}
}

func TestExecuteWorkflow_BudgetWithoutOnExceededStops(t *testing.T) {
	s := newWorkflowTestSession(t)
	wf := &WorkflowDef{
		Name:       "loop",
		Budget:     &WorkflowBudget{MaxIterations: 2},
		EntryPoint: StepRef{ID: "work"},
		Steps: map[string]WorkflowStepDef{
			"work": {Type: "bash", Command: "echo iter-$(workflow.iteration)", NextSteps: []StepRef{{ID: "work"}}},
		},
	}

	if err := s.executeWorkflow(s.ctx, wf, "obj", nil); err != nil {
		t.Fatalf("executeWorkflow: %v", err)
	}
	out := streamedText(drainEvents(s))
	if !strings.Contains(out, "iter-2") || strings.Contains(out, "iter-3") {
		t.Errorf("expected exactly 2 iterations, got:\n%s", out)
	}
}

// ── resume ──

func TestExecuteWorkflow_ResumeFromCursor(t *testing.T) {
	s := newWorkflowTestSession(t)
	wf := &WorkflowDef{
		Name:       "two",
		EntryPoint: StepRef{ID: "first"},
		Steps: map[string]WorkflowStepDef{
			"first":  {Type: "bash", Command: "echo first-ran", NextSteps: []StepRef{{ID: "second"}}},
			"second": {Type: "bash", Command: "echo got:$(step.first)+$(workflow.prompt)"},
		},
	}
	resume := &WorkflowRunState{
		Name:       "two",
		Status:     WorkflowStatusPaused,
		Prompt:     "orig prompt",
		CurrentRef: &StepRef{ID: "second"},
		Iteration:  1,
		StepResults: map[string]*StepResult{
			"first": {Output: "FIRSTRESULT"},
		},
	}

	if err := s.executeWorkflow(s.ctx, wf, "ignored new text", resume); err != nil {
		t.Fatalf("executeWorkflow resume: %v", err)
	}

	out := streamedText(drainEvents(s))
	if strings.Contains(out, "first-ran") {
		t.Errorf("resume must not re-run completed steps, got:\n%s", out)
	}
	if !strings.Contains(out, "got:FIRSTRESULT+orig prompt") {
		t.Errorf("resume should restore step results and the original prompt, got:\n%s", out)
	}
	if st := s.snapshotWorkflowRunState(); st != nil {
		t.Errorf("run state should be cleared after completion, got %+v", st)
	}
}

func TestExecuteWorkflow_CancelParksRunAsPaused(t *testing.T) {
	s := newWorkflowTestSession(t)
	runCtx, cancel := context.WithCancel(s.ctx)
	wf := &WorkflowDef{
		Name:       "slowwf",
		EntryPoint: StepRef{ID: "slow"},
		Steps: map[string]WorkflowStepDef{
			"slow": {Type: "bash", Command: "sleep 5"},
		},
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	if err := s.executeWorkflow(runCtx, wf, "obj", nil); err == nil {
		t.Fatal("cancelled run should return an error")
	}

	st := s.snapshotWorkflowRunState()
	if st == nil {
		t.Fatal("interrupted run must keep its state for resume")
	}
	if st.Status != WorkflowStatusPaused {
		t.Errorf("interrupted run status = %q, want %q", st.Status, WorkflowStatusPaused)
	}
	if st.CurrentRef == nil || st.CurrentRef.ID != "slow" {
		t.Errorf("cursor should point at the interrupted step, got %+v", st.CurrentRef)
	}
	if !st.Resumable() {
		t.Error("paused run should be resumable")
	}
}

// ── workflow_signal ──

func TestHandleWorkflowSignal(t *testing.T) {
	s := newWorkflowTestSession(t)
	pf := &WorkflowDef{Name: "g"}
	st := &WorkflowRunState{Name: "g", Status: WorkflowStatusRunning}

	res := s.handleWorkflowSignal(pf, st, "pursue", map[string]any{"status": "complete", "note": "all done"})
	if res.IsError {
		t.Fatalf("valid signal rejected: %s", res.Output)
	}
	if st.Signal.Status != "complete" || st.Signal.Note != "all done" {
		t.Errorf("signal not recorded: %+v", st.Signal)
	}

	res = s.handleWorkflowSignal(pf, st, "pursue", map[string]any{"status": "paused"})
	if !res.IsError {
		t.Error("signal with status other than complete/blocked must be rejected")
	}
	if st.Signal.Status != "complete" {
		t.Errorf("rejected signal must not overwrite state, got %+v", st.Signal)
	}
}

// ── runtime vars / budget state ──

func TestRuntimeVarsAlwaysPresent(t *testing.T) {
	st := &WorkflowRunState{Name: "g", Status: WorkflowStatusRunning}
	vars := st.runtimeVars(nil)
	for _, key := range []string{
		"workflow.status", "workflow.iteration", "workflow.tokens_used",
		"workflow.tokens_remaining", "workflow.elapsed_seconds",
		"workflow.signal.status", "workflow.signal.note",
	} {
		if _, ok := vars[key]; !ok {
			t.Errorf("runtime var %q must always be present", key)
		}
	}
	if vars["workflow.signal.status"] != "" {
		t.Errorf("unset signal should resolve to empty string")
	}

	st.Budget.TokensUsed = 400
	vars = st.runtimeVars(&WorkflowBudget{MaxTokens: 1000})
	if vars["workflow.tokens_remaining"] != "600" {
		t.Errorf("tokens_remaining = %q, want 600", vars["workflow.tokens_remaining"])
	}
}

func TestBudgetExceeded(t *testing.T) {
	st := &WorkflowRunState{}
	if st.budgetExceeded(nil) {
		t.Error("nil budget never exceeds")
	}
	st.Budget.TokensUsed = 100
	if st.budgetExceeded(&WorkflowBudget{MaxTokens: 101}) {
		t.Error("under token budget")
	}
	if !st.budgetExceeded(&WorkflowBudget{MaxTokens: 100}) {
		t.Error("at token budget should be exceeded")
	}
	st.Iteration = 5
	if !st.budgetExceeded(&WorkflowBudget{MaxIterations: 5}) {
		t.Error("at iteration budget should be exceeded")
	}
	st.Budget.ElapsedSeconds = 60
	if !st.budgetExceeded(&WorkflowBudget{MaxSeconds: 30}) {
		t.Error("over time budget should be exceeded")
	}
}

// ── validation ──

func TestValidateWorkflow_NewFields(t *testing.T) {
	base := func() *WorkflowDef {
		return &WorkflowDef{
			Name:       "g",
			EntryPoint: StepRef{ID: "a"},
			Steps: map[string]WorkflowStepDef{
				"a": {Type: "agent", Agent: "general", Prompt: "p"},
				"b": {Type: "bash", Command: "true"},
			},
		}
	}

	wf := base()
	wf.Budget = &WorkflowBudget{OnExceeded: &StepRef{ID: "nope"}}
	if err := validateWorkflow(wf); err == nil {
		t.Error("budget on_exceeded referencing unknown step must fail validation")
	}

	wf = base()
	wf.Budget = &WorkflowBudget{MaxIterations: -1}
	if err := validateWorkflow(wf); err == nil {
		t.Error("negative budget must fail validation")
	}

	wf = base()
	st := wf.Steps["b"]
	st.Signal = true
	wf.Steps["b"] = st
	if err := validateWorkflow(wf); err == nil {
		t.Error("signal on a bash step must fail validation")
	}

	wf = base()
	st = wf.Steps["b"]
	st.OnError = &StepRef{ID: "a"}
	wf.Steps["b"] = st
	if err := validateWorkflow(wf); err == nil {
		t.Error("on_error on a bash step must fail validation")
	}

	wf = base()
	st = wf.Steps["a"]
	st.OnError = &StepRef{ID: "missing"}
	wf.Steps["a"] = st
	if err := validateWorkflow(wf); err == nil {
		t.Error("on_error referencing unknown step must fail validation")
	}

	wf = base()
	st = wf.Steps["a"]
	st.Signal = true
	st.OnError = &StepRef{ID: "b"}
	wf.Steps["a"] = st
	wf.Budget = &WorkflowBudget{MaxTokens: 1000, MaxIterations: 10, OnExceeded: &StepRef{ID: "b"}}
	if err := validateWorkflow(wf); err != nil {
		t.Errorf("valid workflow with new fields should pass, got: %v", err)
	}
}

// ── persistence round-trip ──

func TestSessionRecord_WorkflowRunRoundTrip(t *testing.T) {
	s := newWorkflowTestSession(t)
	s.sessionMode = "workflow"
	s.activeWorkflow = "Goal"
	s.workflowRunState = &WorkflowRunState{
		Name:       "Goal",
		Status:     WorkflowStatusRunning,
		Prompt:     "ship it",
		CurrentRef: &StepRef{ID: "pursue", Params: map[string]string{"objective": "ship it"}},
		Iteration:  4,
		Budget:     BudgetState{TokensUsed: 1234, ElapsedSeconds: 56},
		Signal:     SignalState{Status: "complete", Note: "n"},
		StepResults: map[string]*StepResult{
			"pursue": {Output: "progress", Params: map[string]string{"objective": "ship it"}},
		},
	}

	rec := s.buildRecord()
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	var loaded sessionRecord
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}

	restored := newWorkflowTestSession(t)
	restored.seedFromRecord(&loaded)

	st := restored.snapshotWorkflowRunState()
	if st == nil {
		t.Fatal("workflow run state lost in round-trip")
	}
	// A run persisted as "running" means the daemon died mid-workflow; it
	// must come back as paused so it reads correctly and resumes.
	if st.Status != WorkflowStatusPaused {
		t.Errorf("restored status = %q, want %q", st.Status, WorkflowStatusPaused)
	}
	if st.Prompt != "ship it" || st.Iteration != 4 || st.Budget.TokensUsed != 1234 {
		t.Errorf("restored state mismatch: %+v", st)
	}
	if st.CurrentRef == nil || st.CurrentRef.ID != "pursue" {
		t.Errorf("restored cursor mismatch: %+v", st.CurrentRef)
	}
	if r := st.StepResults["pursue"]; r == nil || r.Output != "progress" {
		t.Errorf("restored step results mismatch: %+v", st.StepResults)
	}
	if !st.Resumable() {
		t.Error("restored run should be resumable")
	}
}

// ── default Goal workflow ships and validates ──

func TestDefaultWorkflows_GoalIsFirstAndValid(t *testing.T) {
	wfs := LoadWorkflowsFile("../config/defaults/config/workflow.json")
	if len(wfs) == 0 {
		t.Fatal("default workflow.json should load")
	}
	if wfs[0].Name != "Goal" {
		t.Errorf("Goal must be the first workflow (cycle order chat -> Goal -> Plan), got %q", wfs[0].Name)
	}
	goal := wfs[0]
	if goal.Budget == nil || goal.Budget.MaxIterations <= 0 {
		t.Error("Goal workflow should ship with an iteration budget")
	}
	if goal.Budget.OnExceeded == nil || goal.Budget.OnExceeded.ID != "wrap_up" {
		t.Error("Goal budget should route to wrap_up on exhaustion")
	}
	pursue, ok := goal.Steps["pursue"]
	if !ok || !pursue.Signal {
		t.Error("Goal pursue step must expose the workflow_signal tool")
	}
	if pursue.OnError == nil || pursue.OnError.ID != "wrap_up" {
		t.Error("Goal pursue step should wrap up on terminal errors")
	}
}
