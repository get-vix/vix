// Package workflow holds the data model for vix workflows: the declarative,
// multi-step pipelines loaded from config/workflow.json and, increasingly,
// embedded inline in job and hook specs.
//
// It owns only the parsed shapes plus their loader and validator — execution
// lives in the daemon package. Keeping these dependency-free types in their own
// package lets daemon, jobs, and hooks share one definition without import
// cycles (daemon → jobs/hooks → workflow, daemon → workflow).
package workflow

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
)

// InputDef declares an expected input parameter.
type InputDef struct {
	Description string `json:"description"`
}

// StepRef is a structured reference to a workflow step with optional parameter mappings.
type StepRef struct {
	ID        string            `json:"id"`
	Params    map[string]string `json:"params,omitempty"`
	ExecuteIf string            `json:"execute_if,omitempty"`
}

// Def is the parsed config for a workflow.
type Def struct {
	Name       string             `json:"name"`
	EntryPoint StepRef            `json:"entry_point"`
	Steps      map[string]StepDef `json:"steps"`
	Summary    string             `json:"summary,omitempty"`
	Budget     *Budget            `json:"budget,omitempty"` // optional run budget (tokens/seconds/iterations)
	// DisplayInTUI controls whether the workflow appears in the TUI's
	// workflow switcher (Shift+Tab) and slash menu. Default true; internal
	// workflows (e.g. the shipped heartbeat one) set false. It does not
	// affect runnability — jobs and explicit invocations still work by name.
	DisplayInTUI *bool `json:"display_in_tui,omitempty"`
}

// ShowInTUI reports whether the workflow should be listed in the TUI
// (absent display_in_tui defaults to true).
func (w *Def) ShowInTUI() bool {
	return w.DisplayInTUI == nil || *w.DisplayInTUI
}

// StepOption is a structured option for tool steps using ask_question_to_user.
type StepOption struct {
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Steps        []StepRef `json:"steps,omitempty"`
	HasUserInput bool      `json:"has_user_input,omitempty"`
}

// StepDef defines one step in the workflow.
type StepDef struct {
	Type        string              `json:"type"`                   // "agent", "tool", "bash", or "if" (required)
	Effort      string              `json:"effort,omitempty"`       // "adaptive", "low", "medium", "high", "max"
	NextSteps   []StepRef           `json:"next_steps,omitempty"`   // next steps to execute (empty = end workflow)
	InputParams map[string]InputDef `json:"input_params,omitempty"` // declared input parameters for this step
	Tool        string              `json:"tool,omitempty"`         // tool name for type="tool"
	Agent       string              `json:"agent,omitempty"`        // agent name (loaded from .vix/agents/)
	ForkFrom    string              `json:"fork_from,omitempty"`    // fork from a prior step's agent
	Prompt      string              `json:"prompt,omitempty"`       // template, supports $() syntax
	Command     string              `json:"command,omitempty"`      // bash command for type="bash"
	Input       string              `json:"input,omitempty"`        // piped to stdin (supports $() expansion)
	Output      string              `json:"output,omitempty"`       // file path to write step text output
	DenyTools   []string            `json:"deny_tools,omitempty"`   // tools blocked from executing
	Stream      *bool               `json:"stream,omitempty"`       // nil defaults to true
	Silent      bool                `json:"silent,omitempty"`       // suppress all TUI events + vixd dispatch logs for this step
	JSONOutput  bool                `json:"json_output,omitempty"`  // parse LLM output as JSON for variable expansion
	DisplayKey  string              `json:"display_key,omitempty"`  // JSON key to extract as per-step display text
	Explanation string              `json:"explanation,omitempty"`  // user-facing explanation shown at step start
	Question    string              `json:"question,omitempty"`     // question text for tool steps
	Options     []StepOption        `json:"options,omitempty"`      // structured options for ask_question_to_user
	Category    string              `json:"category,omitempty"`     // tab/category label for ask_question_to_user
	TimeoutSec  *int                `json:"timeout_sec,omitempty"`  // per-step timeout (type="bash" only); pointer distinguishes absent from 0
	Signal      bool                `json:"signal,omitempty"`       // agent steps: expose the workflow_signal tool to the agent
	OnError     *StepRef            `json:"on_error,omitempty"`     // agent steps: route here instead of aborting when the step fails

	// if-step fields (type="if"): a bash condition selects a single edge.
	Condition string   `json:"condition,omitempty"` // bash test evaluated like execute_if (exit 0 = true)
	Then      *StepRef `json:"then,omitempty"`      // taken when the condition is true (required for type="if")
	Else      *StepRef `json:"else,omitempty"`      // taken when the condition is false (optional; absent = end)

	// fan_out fields (type="fan_out"): run one branch per element of a list.
	Over        string   `json:"over,omitempty"`         // $(...) reference to a typed list to iterate
	As          string   `json:"as,omitempty"`           // binding name: the per-element item (fan_out) or the collected results list (fan_in)
	BarrierID   string   `json:"barrier_id,omitempty"`   // correlates a fan_out with its fan_in
	MaxParallel int      `json:"max_parallel,omitempty"` // concurrency cap; 0/absent = min(N, GOMAXPROCS)
	Branch      *StepRef `json:"branch,omitempty"`       // per-element branch entry point (required for type="fan_out")

	// fan_in fields (type="fan_in"): join the barrier's branches into one list.
	OnBranchError string `json:"on_branch_error,omitempty"` // "abort" (default) or "collect" (drop failed branches)
}

// IsStreamVisible returns whether streaming output should be shown for this step.
func (s *StepDef) IsStreamVisible() bool {
	return s.Stream == nil || *s.Stream
}

// Budget is the optional `budget` block on a workflow definition. Zero/absent
// fields mean "unlimited" for that dimension. When any limit is exceeded the
// engine routes to OnExceeded (or stops when absent) exactly once, with the run
// status set to budget_limited.
type Budget struct {
	MaxTokens     int64    `json:"max_tokens,omitempty"`     // total tokens (input+output+cache write+cache read) across all steps
	MaxSeconds    int64    `json:"max_seconds,omitempty"`    // wall-clock seconds, accumulated across resumes
	MaxIterations int      `json:"max_iterations,omitempty"` // loop iterations (steps executed in the main chain)
	OnExceeded    *StepRef `json:"on_exceeded,omitempty"`    // step to route to when the budget trips (default: stop)
}

// File is the JSON shape of config/workflow.json: {"workflows": [...]}.
type File struct {
	Workflows []Def `json:"workflows"`
}

// Load reads a config/workflow.json file and returns its validated workflow
// list, preserving file order. Returns nil on a missing file or parse error;
// individually invalid workflows are skipped with a log line. Duplicate names
// within the file are disambiguated by appending an index so the UI can tell
// them apart.
func Load(path string) []*Def {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg File
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("[workflow] failed to parse %s: %v", path, err)
		return nil
	}

	out := make([]*Def, 0, len(cfg.Workflows))
	for i := range cfg.Workflows {
		wf := cfg.Workflows[i]
		if err := Validate(&wf); err != nil {
			log.Printf("[workflow] invalid workflow '%s': %v", wf.Name, err)
			continue
		}
		out = append(out, &wf)
	}

	// Disambiguate duplicate names within the single file.
	nameCount := make(map[string]int)
	for _, wf := range out {
		nameCount[wf.Name]++
	}
	seen := make(map[string]int)
	for _, wf := range out {
		if nameCount[wf.Name] > 1 {
			seen[wf.Name]++
			wf.Name = fmt.Sprintf("%s (%d)", wf.Name, seen[wf.Name])
		}
	}
	return out
}

// Validate checks that a workflow definition is consistent.
func Validate(pf *Def) error {
	if pf.Name == "" {
		return fmt.Errorf("missing name")
	}
	if len(pf.Steps) == 0 {
		return fmt.Errorf("no steps defined")
	}

	for stepID := range pf.Steps {
		if stepID == "" {
			return fmt.Errorf("step has empty id")
		}
	}

	if pf.EntryPoint.ID == "" {
		return fmt.Errorf("missing entry_point")
	}
	if _, ok := pf.Steps[pf.EntryPoint.ID]; !ok {
		return fmt.Errorf("entry_point '%s' references unknown step", pf.EntryPoint.ID)
	}

	if b := pf.Budget; b != nil {
		if b.MaxTokens < 0 || b.MaxSeconds < 0 || b.MaxIterations < 0 {
			return fmt.Errorf("budget limits must be >= 0")
		}
		if b.OnExceeded != nil && b.OnExceeded.ID != "" && b.OnExceeded.ID != "stop" {
			if _, ok := pf.Steps[b.OnExceeded.ID]; !ok {
				return fmt.Errorf("budget on_exceeded '%s' references unknown step", b.OnExceeded.ID)
			}
		}
	}

	for stepID, step := range pf.Steps {
		if step.Type == "" {
			return fmt.Errorf("step '%s': missing type", stepID)
		}
		if step.Type != "agent" && step.Type != "tool" && step.Type != "bash" && step.Type != "if" && step.Type != "fan_out" && step.Type != "fan_in" {
			return fmt.Errorf("step '%s': unknown type '%s' (must be 'agent', 'tool', 'bash', 'if', 'fan_out', or 'fan_in')", stepID, step.Type)
		}

		for _, ns := range step.NextSteps {
			if ns.ID != "" && ns.ID != "stop" {
				if _, ok := pf.Steps[ns.ID]; !ok {
					return fmt.Errorf("step '%s': next_step '%s' references unknown step", stepID, ns.ID)
				}
			}
		}

		// signal and on_error are agent-step features; reject elsewhere so
		// configs fail loudly at load time instead of silently no-opping.
		if step.Type != "agent" {
			if step.Signal {
				return fmt.Errorf("step '%s': signal only valid on type='agent'", stepID)
			}
			if step.OnError != nil {
				return fmt.Errorf("step '%s': on_error only valid on type='agent'", stepID)
			}
		}
		if step.OnError != nil && step.OnError.ID != "" && step.OnError.ID != "stop" {
			if _, ok := pf.Steps[step.OnError.ID]; !ok {
				return fmt.Errorf("step '%s': on_error '%s' references unknown step", stepID, step.OnError.ID)
			}
		}

		if step.Type == "tool" {
			if step.Tool == "" {
				return fmt.Errorf("step '%s': type 'tool' requires 'tool' field", stepID)
			}
			for _, opt := range step.Options {
				for _, s := range opt.Steps {
					if s.ID != "" && s.ID != "stop" {
						if _, ok := pf.Steps[s.ID]; !ok {
							return fmt.Errorf("step '%s' option '%s' step references unknown step '%s'", stepID, opt.Title, s.ID)
						}
					}
				}
			}
			continue
		}

		if step.Type == "bash" {
			if step.Command == "" {
				return fmt.Errorf("step '%s': type 'bash' requires 'command' field", stepID)
			}
			if step.Agent != "" || step.ForkFrom != "" || step.Prompt != "" {
				return fmt.Errorf("step '%s': type 'bash' cannot have 'agent', 'fork_from', or 'prompt'", stepID)
			}
			if step.TimeoutSec != nil && *step.TimeoutSec <= 0 {
				return fmt.Errorf("step '%s': timeout_sec must be > 0", stepID)
			}
			continue
		}

		if step.Type == "if" {
			if step.Condition == "" {
				return fmt.Errorf("step '%s': type 'if' requires a 'condition'", stepID)
			}
			if step.Then == nil || step.Then.ID == "" {
				return fmt.Errorf("step '%s': type 'if' requires a 'then' target", stepID)
			}
			if step.Agent != "" || step.ForkFrom != "" || step.Prompt != "" || step.Command != "" || step.Tool != "" {
				return fmt.Errorf("step '%s': type 'if' cannot have 'agent', 'fork_from', 'prompt', 'command', or 'tool'", stepID)
			}
			if step.Then.ID != "stop" {
				if _, ok := pf.Steps[step.Then.ID]; !ok {
					return fmt.Errorf("step '%s': then '%s' references unknown step", stepID, step.Then.ID)
				}
			}
			if step.Else != nil && step.Else.ID != "" && step.Else.ID != "stop" {
				if _, ok := pf.Steps[step.Else.ID]; !ok {
					return fmt.Errorf("step '%s': else '%s' references unknown step", stepID, step.Else.ID)
				}
			}
			continue
		}

		if step.Type == "fan_out" {
			if step.Over == "" {
				return fmt.Errorf("step '%s': type 'fan_out' requires 'over'", stepID)
			}
			if step.As == "" {
				return fmt.Errorf("step '%s': type 'fan_out' requires 'as'", stepID)
			}
			if step.BarrierID == "" {
				return fmt.Errorf("step '%s': type 'fan_out' requires 'barrier_id'", stepID)
			}
			if step.Branch == nil || step.Branch.ID == "" {
				return fmt.Errorf("step '%s': type 'fan_out' requires a 'branch' entry point", stepID)
			}
			if step.MaxParallel < 0 {
				return fmt.Errorf("step '%s': max_parallel must be >= 0", stepID)
			}
			if step.Agent != "" || step.ForkFrom != "" || step.Prompt != "" || step.Command != "" || step.Tool != "" {
				return fmt.Errorf("step '%s': type 'fan_out' cannot have 'agent', 'fork_from', 'prompt', 'command', or 'tool'", stepID)
			}
			if step.Branch.ID != "stop" {
				if _, ok := pf.Steps[step.Branch.ID]; !ok {
					return fmt.Errorf("step '%s': branch '%s' references unknown step", stepID, step.Branch.ID)
				}
			}
			// Reject nested fan_out: a branch subtree may not reach another
			// fan_out (v1 keeps the barrier model flat).
			if step.Branch.ID != "stop" {
				reached := map[string]bool{}
				collectReachable(pf, step.Branch.ID, reached)
				for id := range reached {
					if pf.Steps[id].Type == "fan_out" {
						return fmt.Errorf("step '%s': branch subtree reaches nested fan_out '%s' (unsupported)", stepID, id)
					}
				}
			}
			continue
		}

		if step.Type == "fan_in" {
			if step.BarrierID == "" {
				return fmt.Errorf("step '%s': type 'fan_in' requires 'barrier_id'", stepID)
			}
			if step.As == "" {
				return fmt.Errorf("step '%s': type 'fan_in' requires 'as'", stepID)
			}
			if step.OnBranchError != "" && step.OnBranchError != "abort" && step.OnBranchError != "collect" {
				return fmt.Errorf("step '%s': on_branch_error must be 'abort' or 'collect'", stepID)
			}
			if step.Agent != "" || step.ForkFrom != "" || step.Prompt != "" || step.Command != "" || step.Tool != "" {
				return fmt.Errorf("step '%s': type 'fan_in' cannot have 'agent', 'fork_from', 'prompt', 'command', or 'tool'", stepID)
			}
			continue
		}

		// timeout_sec is only enforced on bash steps today; reject it elsewhere
		// rather than silently ignoring so configs fail loudly at load time.
		if step.TimeoutSec != nil {
			return fmt.Errorf("step '%s': timeout_sec only valid on type='bash'", stepID)
		}

		// Agent step validation
		hasAgent := step.Agent != ""
		hasFork := step.ForkFrom != ""

		if !hasAgent && !hasFork {
			return fmt.Errorf("step '%s': must have either 'agent' or 'fork_from'", stepID)
		}
		if hasAgent && hasFork {
			return fmt.Errorf("step '%s': cannot have both 'agent' and 'fork_from'", stepID)
		}

		if hasFork {
			if _, ok := pf.Steps[step.ForkFrom]; !ok {
				return fmt.Errorf("step '%s': fork_from '%s' references unknown step", stepID, step.ForkFrom)
			}
		}

		if step.Prompt == "" {
			return fmt.Errorf("step '%s': missing prompt", stepID)
		}
	}

	// Barrier pairing: every fan_out barrier_id must match exactly one fan_in
	// barrier_id and vice versa, so a fan_out always has a join and no fan_in
	// waits on branches that never spawn.
	fanOutBarriers := map[string]int{}
	fanInBarriers := map[string]int{}
	for _, step := range pf.Steps {
		switch step.Type {
		case "fan_out":
			fanOutBarriers[step.BarrierID]++
		case "fan_in":
			fanInBarriers[step.BarrierID]++
		}
	}
	for bid, n := range fanOutBarriers {
		if n > 1 {
			return fmt.Errorf("barrier '%s': %d fan_out nodes share it (must be exactly one)", bid, n)
		}
		if fanInBarriers[bid] != 1 {
			return fmt.Errorf("barrier '%s': fan_out has no matching fan_in", bid)
		}
	}
	for bid, n := range fanInBarriers {
		if n > 1 {
			return fmt.Errorf("barrier '%s': %d fan_in nodes share it (must be exactly one)", bid, n)
		}
		if fanOutBarriers[bid] != 1 {
			return fmt.Errorf("barrier '%s': fan_in has no matching fan_out", bid)
		}
	}

	return nil
}

// collectReachable records every step id reachable from start by following
// next_steps, then/else, branch, on_error, and tool option edges. Used to
// reject nested fan_out. A visited set makes it cycle-safe.
func collectReachable(pf *Def, start string, visited map[string]bool) {
	if start == "" || start == "stop" || visited[start] {
		return
	}
	step, ok := pf.Steps[start]
	if !ok {
		return
	}
	visited[start] = true
	for _, ns := range step.NextSteps {
		collectReachable(pf, ns.ID, visited)
	}
	if step.Then != nil {
		collectReachable(pf, step.Then.ID, visited)
	}
	if step.Else != nil {
		collectReachable(pf, step.Else.ID, visited)
	}
	if step.Branch != nil {
		collectReachable(pf, step.Branch.ID, visited)
	}
	if step.OnError != nil {
		collectReachable(pf, step.OnError.ID, visited)
	}
	for _, opt := range step.Options {
		for _, s := range opt.Steps {
			collectReachable(pf, s.ID, visited)
		}
	}
}
