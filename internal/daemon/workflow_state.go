package daemon

import (
	"fmt"
	"strconv"
	"time"

	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

// Workflow run statuses. A run is "running" while executeWorkflow owns it,
// "paused" when interrupted (user cancel or daemon death — both resume the
// same way), "blocked" when a step error was routed or aborted the run,
// "budget_limited" once the budget tripped, and "complete" on normal end
// (completed runs are cleared from the session rather than kept around).
const (
	WorkflowStatusRunning       = "running"
	WorkflowStatusPaused        = "paused"
	WorkflowStatusBlocked       = "blocked"
	WorkflowStatusBudgetLimited = "budget_limited"
	WorkflowStatusComplete      = "complete"
)

// WorkflowBudget is the optional `budget` block on a workflow definition.
// Zero/absent fields mean "unlimited" for that dimension. When any limit is
// exceeded the engine routes to OnExceeded (or stops when absent) exactly
// once, with the run status set to budget_limited.
type WorkflowBudget struct {
	MaxTokens     int64    `json:"max_tokens,omitempty"`     // total tokens (input+output+cache write+cache read) across all steps
	MaxSeconds    int64    `json:"max_seconds,omitempty"`    // wall-clock seconds, accumulated across resumes
	MaxIterations int      `json:"max_iterations,omitempty"` // loop iterations (steps executed in the main chain)
	OnExceeded    *StepRef `json:"on_exceeded,omitempty"`    // step to route to when the budget trips (default: stop)
}

// BudgetState is the live usage accumulated by a run, persisted with it.
type BudgetState struct {
	TokensUsed     int64 `json:"tokens_used"`
	ElapsedSeconds int64 `json:"elapsed_seconds"`
}

// SignalState carries the last workflow_signal emitted by an agent step.
// It is cleared whenever a step with signal=true starts, so each signal is
// only visible to the routing decisions that immediately follow it.
type SignalState struct {
	Status string `json:"status,omitempty"` // "complete" or "blocked"
	Note   string `json:"note,omitempty"`
}

// StepAgentState is the serializable snapshot of a step's AgentRunner:
// everything needed to rebuild it with NewAgentRunner plus its conversation.
type StepAgentState struct {
	Config   SubagentConfig     `json:"config"`
	Messages []llm.MessageParam `json:"messages"`
}

// WorkflowRunState is the persisted position of a workflow run. It is
// snapshotted once per engine loop iteration so an interrupted run (user
// cancel or daemon restart) can resume from its cursor with all step results
// and per-step agent conversations intact.
type WorkflowRunState struct {
	Name         string                    `json:"name"`
	Status       string                    `json:"status"`
	Prompt       string                    `json:"prompt"`                  // the original $(workflow.prompt)
	CurrentRef   *StepRef                  `json:"current_ref,omitempty"`   // resume cursor: step about to execute
	Iteration    int                       `json:"iteration"`               // total iterations across resumes
	StepResults  map[string]*StepResult    `json:"step_results,omitempty"`
	StepAgents   map[string]StepAgentState `json:"step_agents,omitempty"`
	Budget       BudgetState               `json:"budget"`
	Signal       SignalState               `json:"signal"`
	BudgetRouted bool                      `json:"budget_routed,omitempty"` // OnExceeded already taken
	ErrorRouted  bool                      `json:"error_routed,omitempty"`  // an on_error route already taken
}

// Resumable reports whether an interrupted run can be continued. Completed
// runs are cleared rather than kept, so anything still stored that isn't
// actively running again is fair game.
func (st *WorkflowRunState) Resumable() bool {
	if st == nil {
		return false
	}
	switch st.Status {
	case WorkflowStatusRunning, WorkflowStatusPaused, WorkflowStatusBlocked, WorkflowStatusBudgetLimited:
		return st.CurrentRef != nil && st.CurrentRef.ID != "" && st.CurrentRef.ID != "stop"
	}
	return false
}

// runtimeVars exposes the run's live accounting to workflow templates and
// execute_if conditions. Every key is always present (empty/zero rather than
// absent) so $(workflow.*) tokens never leak unresolved into bash conditions.
func (st *WorkflowRunState) runtimeVars(budget *WorkflowBudget) map[string]string {
	remaining := ""
	if budget != nil && budget.MaxTokens > 0 {
		r := budget.MaxTokens - st.Budget.TokensUsed
		if r < 0 {
			r = 0
		}
		remaining = strconv.FormatInt(r, 10)
	}
	return map[string]string{
		"workflow.status":           st.Status,
		"workflow.iteration":        strconv.Itoa(st.Iteration),
		"workflow.tokens_used":      strconv.FormatInt(st.Budget.TokensUsed, 10),
		"workflow.tokens_remaining": remaining,
		"workflow.elapsed_seconds":  strconv.FormatInt(st.Budget.ElapsedSeconds, 10),
		"workflow.signal.status":    st.Signal.Status,
		"workflow.signal.note":      st.Signal.Note,
	}
}

// budgetExceeded reports whether any configured budget dimension is spent.
func (st *WorkflowRunState) budgetExceeded(budget *WorkflowBudget) bool {
	if budget == nil {
		return false
	}
	if budget.MaxTokens > 0 && st.Budget.TokensUsed >= budget.MaxTokens {
		return true
	}
	if budget.MaxSeconds > 0 && st.Budget.ElapsedSeconds >= budget.MaxSeconds {
		return true
	}
	if budget.MaxIterations > 0 && st.Iteration >= budget.MaxIterations {
		return true
	}
	return false
}

// mergeRuntimeVars copies the run's live vars into an existing variable map,
// refreshing values that may have changed since the map was built (e.g. a
// signal emitted during the step that just ran).
func mergeRuntimeVars(vars map[string]string, st *WorkflowRunState, budget *WorkflowBudget) {
	if st == nil {
		return
	}
	for k, v := range st.runtimeVars(budget) {
		vars[k] = v
	}
}

// snapshotAgents projects the live step agents into their serializable form.
func snapshotAgents(agents map[string]*AgentRunner) map[string]StepAgentState {
	if len(agents) == 0 {
		return nil
	}
	out := make(map[string]StepAgentState, len(agents))
	for id, a := range agents {
		msgs := make([]llm.MessageParam, len(a.Messages))
		copy(msgs, a.Messages)
		out[id] = StepAgentState{Config: a.Config, Messages: msgs}
	}
	return out
}

// elapsedTracker accumulates wall-clock seconds across resumes: the persisted
// base plus time since this run (re)started.
type elapsedTracker struct {
	base  int64
	start time.Time
}

func (e elapsedTracker) seconds() int64 {
	return e.base + int64(time.Since(e.start).Seconds())
}

// appendSignalTool adds the workflow_signal schema to an agent's tool list
// (idempotent).
func appendSignalTool(tools []llm.ToolParam) []llm.ToolParam {
	for _, t := range tools {
		if t.Name == "workflow_signal" {
			return tools
		}
	}
	return append(tools, WorkflowSignalToolSchema())
}

// setWorkflowRunState publishes (or clears, with nil) the session's persisted
// workflow run snapshot. Snapshots are immutable after publication: the
// engine mutates its own live state and re-publishes copies, so buildRecord
// can marshal the pointer it reads without racing the engine.
func (s *Session) setWorkflowRunState(st *WorkflowRunState) {
	s.mu.Lock()
	s.workflowRunState = st
	s.mu.Unlock()
}

// snapshotWorkflowRunState returns the last published run snapshot.
func (s *Session) snapshotWorkflowRunState() *WorkflowRunState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workflowRunState
}

// saveWorkflowProgress publishes an immutable snapshot of the run's live
// state — cursor, step results, per-step agent conversations, accounting —
// and persists the session record. Called once per engine loop iteration and
// from the run finalizer, always from the engine goroutine while no parallel
// steps are in flight.
func (s *Session) saveWorkflowProgress(exec *WorkflowRun, currentRef *StepRef) {
	st := exec.State
	if st == nil {
		return
	}
	snap := &WorkflowRunState{
		Name:         st.Name,
		Status:       st.Status,
		Prompt:       st.Prompt,
		Iteration:    st.Iteration,
		Budget:       st.Budget,
		Signal:       st.Signal,
		BudgetRouted: st.BudgetRouted,
		ErrorRouted:  st.ErrorRouted,
	}
	if currentRef != nil {
		ref := StepRef{ID: currentRef.ID, ExecuteIf: currentRef.ExecuteIf}
		if len(currentRef.Params) > 0 {
			ref.Params = make(map[string]string, len(currentRef.Params))
			for k, v := range currentRef.Params {
				ref.Params[k] = v
			}
		}
		snap.CurrentRef = &ref
	}
	if len(exec.StepResults) > 0 {
		snap.StepResults = make(map[string]*StepResult, len(exec.StepResults))
		for id, r := range exec.StepResults {
			snap.StepResults[id] = r
		}
	}
	snap.StepAgents = snapshotAgents(exec.StepAgents)
	s.setWorkflowRunState(snap)
	s.persist()
}

// emitWorkflowStatus surfaces a run status transition to clients.
func (s *Session) emitWorkflowStatus(pf *WorkflowDef, st *WorkflowRunState, currentRef *StepRef) {
	ev := protocol.EventWorkflowStatus{
		WorkflowName:   pf.Name,
		Status:         st.Status,
		Iteration:      st.Iteration,
		TokensUsed:     st.Budget.TokensUsed,
		ElapsedSeconds: st.Budget.ElapsedSeconds,
		Note:           st.Signal.Note,
	}
	if currentRef != nil {
		ev.StepID = currentRef.ID
	}
	if pf.Budget != nil {
		ev.TokenBudget = pf.Budget.MaxTokens
	}
	s.emit("event.workflow_status", ev)
}

// handleWorkflowSignal services a workflow_signal tool call from an agent
// step: it records the declared outcome on the run's live state, where the
// engine's next routing decision picks it up as $(workflow.signal.status).
func (s *Session) handleWorkflowSignal(pf *WorkflowDef, st *WorkflowRunState, stepID string, params map[string]any) *ToolResult {
	status, _ := params["status"].(string)
	note, _ := params["note"].(string)
	if status != "complete" && status != "blocked" {
		return &ToolResult{
			Output:  "workflow_signal requires status \"complete\" or \"blocked\"; to keep working, simply end your turn instead of calling this tool",
			IsError: true,
		}
	}
	st.Signal = SignalState{Status: status, Note: note}
	LogInfo("[workflow] step '%s' signalled %s%s", stepID, status, formatSignalNote(note))
	return &ToolResult{Output: fmt.Sprintf("Signal %q recorded for workflow %q. End your turn now; the workflow will route accordingly.", status, pf.Name)}
}

func formatSignalNote(note string) string {
	if note == "" {
		return ""
	}
	return ": " + note
}
