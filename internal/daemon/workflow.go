package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/daemon/mcp"
	promptloader "github.com/get-vix/vix/internal/daemon/prompt"
	"github.com/get-vix/vix/internal/protocol"
)

// ErrMaxTokens is returned when the LLM response was truncated due to the output token limit.
var ErrMaxTokens = errors.New("max_tokens")

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

// WorkflowDef is the parsed config for a workflow.
type WorkflowDef struct {
	Name       string                     `json:"name"`
	EntryPoint StepRef                    `json:"entry_point"`
	Steps      map[string]WorkflowStepDef `json:"steps"`
	Summary    string                     `json:"summary,omitempty"`
	Budget     *WorkflowBudget            `json:"budget,omitempty"` // optional run budget (tokens/seconds/iterations)
	// DisplayInTUI controls whether the workflow appears in the TUI's
	// workflow switcher (Shift+Tab) and slash menu. Default true; internal
	// workflows (e.g. the shipped heartbeat one) set false. It does not
	// affect runnability — jobs and explicit invocations still work by name.
	DisplayInTUI *bool `json:"display_in_tui,omitempty"`
}

// ShowInTUI reports whether the workflow should be listed in the TUI
// (absent display_in_tui defaults to true).
func (w *WorkflowDef) ShowInTUI() bool {
	return w.DisplayInTUI == nil || *w.DisplayInTUI
}

// StepOption is a structured option for tool steps using ask_question_to_user.
type StepOption struct {
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Steps        []StepRef `json:"steps,omitempty"`
	HasUserInput bool      `json:"has_user_input,omitempty"`
}

// WorkflowStepDef defines one step in the workflow.
type WorkflowStepDef struct {
	Type        string              `json:"type"`                   // "agent", "tool", or "bash" (required)
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
}

// IsStreamVisible returns whether streaming output should be shown for this step.
func (s *WorkflowStepDef) IsStreamVisible() bool {
	return s.Stream == nil || *s.Stream
}

// StepResult holds output from a completed workflow step.
type StepResult struct {
	Output string            `json:"output"`
	Parsed map[string]any    `json:"parsed,omitempty"` // nil if json_output was false or parse failed
	Params map[string]string `json:"params,omitempty"` // input params received by this step
}

// AgentRunner is a persistent agent with maintained history.
type AgentRunner struct {
	Config   SubagentConfig
	LLM      LLM
	Messages []llm.MessageParam
	System   []llm.SystemBlock
	Tools    []llm.ToolParam
	MaxTurns int

	// ToolTimeouts carries the parent session's configured tool-call floor/cap
	// so this runner's tool dispatches honour the same settings.json bounds as
	// the main agent. Populated at construction in NewAgentRunner; zero values
	// fall back to package defaults in the dispatcher.
	ToolTimeouts ToolTimeouts

	// Per-Send() accumulated usage (reset at start of each Send call)
	LastInputTokens         int64
	LastOutputTokens        int64
	LastCacheCreationTokens int64
	LastCacheReadTokens     int64
	LastElapsed             time.Duration
}

// WorkflowRun tracks a running workflow.
type WorkflowRun struct {
	Def         *WorkflowDef
	StepAgents  map[string]*AgentRunner // step_id -> runner used
	StepResults map[string]*StepResult  // step_id -> result
	State       *WorkflowRunState       // live persisted position/accounting for this run
}

// FeatureToolOrchestrator is the feature flag name for the tool orchestrator mode.
const FeatureToolOrchestrator = "tool_orchestrator"

// FeatureReadClaudeMD enables loading CLAUDE.md files into the system prompt.
const FeatureReadClaudeMD = "read_claude_md"

// FeatureReadAgentsMD enables loading AGENTS.md files into the system prompt.
const FeatureReadAgentsMD = "read_agents_md"

// CurrentConfigVersion is the expected version number for settings.json files.
// Bump this when the config format changes in a breaking way.
const CurrentConfigVersion = 1

// Package-level defaults for tool-call timeouts. Used both as the ultimate
// fall-back in LoadProjectConfig and as the defaults passed to
// resolveToolTimeout when no override is configured via settings.json.
const (
	defaultToolTimeoutDefault = 120 * time.Second
	defaultToolTimeoutMax     = 600 * time.Second
)

// Package-level defaults for per-step timeouts on workflow bash steps. Unlike
// the tool-call timeouts above, a step breaching its deadline is killed (via
// process-group SIGKILL in runBashWithContext) but does NOT abort the
// workflow — control falls through to the step's next_steps evaluation so
// branches like `execute_if: [ "$(cat /tmp/.vix-reward)" = "1" ]` can route
// into a retry path. Defaults chosen to match tool_timeouts for consistency.
const (
	defaultBashStepTimeoutDefault = 300 * time.Second
	defaultBashStepTimeoutMax     = 600 * time.Second
)

// Package-level defaults for conversation compaction. Used as the fall-back in
// LoadProjectConfig when the `compaction` block is absent or partially set.
const (
	defaultCompactionThreshold = 0.8  // fraction of context window that triggers auto-compaction
	defaultCompactionAuto      = true // master switch for automatic compaction
	defaultCompactionKeepLastN = -1   // -1 = use ratio; >0 = keep exactly N turns
	defaultCompactionKeepRatio = 0.25 // trailing fraction of turns kept when KeepLastNTurns <= 0
)

// toolTimeoutsFile is the JSON shape of the `tool_timeouts` block in
// settings.json. Fields are *int so we can distinguish "absent" from "0",
// where "0" is explicitly invalid.
type toolTimeoutsFile struct {
	DefaultSec *int `json:"default_sec,omitempty"`
	MaxSec     *int `json:"max_sec,omitempty"`
}

// ToolTimeouts is the resolved (validated, defaulted) form of the
// tool_timeouts block, stored on ProjectConfig and consumed by the tool
// dispatcher in session.go.
type ToolTimeouts struct {
	Default time.Duration
	Max     time.Duration
}

// bashStepTimeoutsFile is the JSON shape of the `bash_step_timeouts` block
// in settings.json. Same pointer-int pattern as toolTimeoutsFile so we can
// distinguish "absent" (nil) from "explicitly zero" (0, which is invalid).
type bashStepTimeoutsFile struct {
	DefaultSec *int `json:"default_sec,omitempty"`
	MaxSec     *int `json:"max_sec,omitempty"`
}

// BashStepTimeouts is the resolved form of the bash_step_timeouts block,
// consumed by resolveBashStepTimeout when scheduling workflow bash steps.
type BashStepTimeouts struct {
	Default time.Duration
	Max     time.Duration
}

// compactionFile is the JSON shape of the `compaction` block in settings.json.
// Pointer fields distinguish "absent" (nil) from an explicit zero value.
type compactionFile struct {
	Threshold      *float64 `json:"threshold,omitempty"`
	Auto           *bool    `json:"auto,omitempty"`
	KeepLastNTurns *int     `json:"keep_last_n_turns,omitempty"`
}

// Compaction is the resolved (validated, defaulted) form of the `compaction`
// block, stored on ProjectConfig and consumed by the auto-compaction logic and
// the /compact command in session.go.
type Compaction struct {
	Threshold      float64 // (0,1]; default 0.8
	Auto           bool    // default true
	KeepLastNTurns int     // -1 = use ratio; >0 = keep exactly N trailing turns
	KeepRatio      float64 // default 0.25; used when KeepLastNTurns <= 0
}

// configFile represents the top-level settings.json structure.
//
// Note: workflows and languages are intentionally NOT parsed here. They live
// in their own files (config/workflow.json, config/languages.json) loaded via
// LoadWorkflowsFile and lsp.LoadLanguageConfigs respectively. A legacy
// settings.json may still carry "workflows"/"languages" keys, but they are
// ignored.
type configFile struct {
	Version            int                   `json:"version,omitempty"`
	Agent              string                `json:"agent,omitempty"`
	AllowedDirectories []string              `json:"allowed_directories,omitempty"`
	DenyList           denyListField         `json:"deny_list,omitempty"`
	Features           map[string]bool       `json:"features,omitempty"`
	ToolTimeouts       *toolTimeoutsFile     `json:"tool_timeouts,omitempty"`
	BashStepTimeouts   *bashStepTimeoutsFile `json:"bash_step_timeouts,omitempty"`
	Compaction         *compactionFile       `json:"compaction,omitempty"`
	MCPServers         []mcp.ServerConfig    `json:"mcp_servers,omitempty"`
}

// denyListField accepts either the structured form
// {"paths": [...], "urls": [...]} or the legacy flat array form
// ["path1", "path2"] (treated as paths only). Storing the raw form lets
// LoadProjectConfig do the path-resolution / URL-normalization work in one
// place.
type denyListField struct {
	Paths []string `json:"paths,omitempty"`
	URLs  []string `json:"urls,omitempty"`
}

// UnmarshalJSON tolerates the legacy `deny_list: [...]` shape that shipped
// in the first cut of this feature. New configs should use the object form.
func (d *denyListField) UnmarshalJSON(data []byte) error {
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		d.Paths = arr
		d.URLs = nil
		return nil
	}
	type rawDenyList denyListField
	var raw rawDenyList
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*d = denyListField(raw)
	return nil
}

// ProjectConfig holds parsed values from settings.json.
type ProjectConfig struct {
	Agent              string
	AllowedDirectories []string
	DenyPaths          []string
	DenyURLs           []string
	Features           map[string]bool
	ToolTimeouts       ToolTimeouts
	BashStepTimeouts   BashStepTimeouts
	Compaction         Compaction
	MCPServers         []mcp.ServerConfig
}

// HasFeature returns whether the named feature flag is enabled.
func (c ProjectConfig) HasFeature(name string) bool {
	return c.Features[name]
}

// resolveBashStepTimeout returns the effective deadline for a workflow bash
// step. A positive per-step override wins; otherwise cfg.Default is used.
// The result is always clamped to cfg.Max when cfg.Max > 0. Unset or
// non-positive inputs fall through to whatever default/cap the caller
// provides via cfg, which in normal use carries the package-level defaults
// seeded in LoadProjectConfig.
func resolveBashStepTimeout(stepTimeoutSec *int, cfg BashStepTimeouts) time.Duration {
	d := cfg.Default
	if stepTimeoutSec != nil && *stepTimeoutSec > 0 {
		d = time.Duration(*stepTimeoutSec) * time.Second
	}
	if cfg.Max > 0 && d > cfg.Max {
		d = cfg.Max
	}
	return d
}

// LoadProjectConfig reads config from one or more paths (applied in order, later overrides earlier)
// and returns agent name, workflows, and features.
func LoadProjectConfig(configPaths ...string) ProjectConfig {
	result := ProjectConfig{
		Agent: "general", // default
		ToolTimeouts: ToolTimeouts{
			Default: defaultToolTimeoutDefault,
			Max:     defaultToolTimeoutMax,
		},
		BashStepTimeouts: BashStepTimeouts{
			Default: defaultBashStepTimeoutDefault,
			Max:     defaultBashStepTimeoutMax,
		},
		Compaction: Compaction{
			Threshold:      defaultCompactionThreshold,
			Auto:           defaultCompactionAuto,
			KeepLastNTurns: defaultCompactionKeepLastN,
			KeepRatio:      defaultCompactionKeepRatio,
		},
	}

	for _, configPath := range configPaths {
		if configPath == "" {
			continue
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}

		var cfg configFile
		if err := json.Unmarshal(data, &cfg); err != nil {
			log.Printf("[config] failed to parse config %s: %v", configPath, err)
			continue
		}

		if cfg.Version != CurrentConfigVersion {
			log.Printf("[config] %s: config version %d does not match expected version %d — please update your config file", configPath, cfg.Version, CurrentConfigVersion)
			continue
		}

		if cfg.Agent != "" {
			result.Agent = cfg.Agent
		}
		// Merge allowed directories (union from all config files).
		for _, dir := range cfg.AllowedDirectories {
			absDir := dir
			if !filepath.IsAbs(absDir) {
				absDir = filepath.Clean(filepath.Join(filepath.Dir(configPath), absDir))
			}
			// Deduplicate
			found := false
			for _, existing := range result.AllowedDirectories {
				if existing == absDir {
					found = true
					break
				}
			}
			if !found {
				result.AllowedDirectories = append(result.AllowedDirectories, absDir)
			}
		}
		// Merge deny list paths (union from all config files). Path entries
		// resolve relative to the config file that declared them, matching
		// the AllowedDirectories convention above.
		for _, dir := range cfg.DenyList.Paths {
			absDir := dir
			if !filepath.IsAbs(absDir) {
				absDir = filepath.Clean(filepath.Join(filepath.Dir(configPath), absDir))
			} else {
				absDir = filepath.Clean(absDir)
			}
			found := false
			for _, existing := range result.DenyPaths {
				if existing == absDir {
					found = true
					break
				}
			}
			if !found {
				result.DenyPaths = append(result.DenyPaths, absDir)
			}
		}
		// Merge deny list URLs (union, normalized). URL entries are stored
		// verbatim — the matcher in deny_list.go handles canonicalization at
		// check time so we don't lose user intent (e.g. trailing slashes).
		for _, u := range cfg.DenyList.URLs {
			u = strings.TrimSpace(u)
			if u == "" {
				continue
			}
			found := false
			for _, existing := range result.DenyURLs {
				if existing == u {
					found = true
					break
				}
			}
			if !found {
				result.DenyURLs = append(result.DenyURLs, u)
			}
		}
		if len(cfg.Features) > 0 {
			if result.Features == nil {
				result.Features = make(map[string]bool)
			}
			for k, v := range cfg.Features {
				result.Features[k] = v
			}
		}
		if cfg.ToolTimeouts != nil {
			// Start from whatever is currently in result (either the hard-coded
			// defaults seeded at the top, or an earlier file's override). Then
			// apply the two fields independently so a partial block honours
			// what it does set without clobbering the other knob.
			next := result.ToolTimeouts
			if cfg.ToolTimeouts.DefaultSec != nil {
				if *cfg.ToolTimeouts.DefaultSec > 0 {
					next.Default = time.Duration(*cfg.ToolTimeouts.DefaultSec) * time.Second
				} else {
					log.Printf("[config] %s: tool_timeouts.default_sec must be > 0, ignoring", configPath)
				}
			}
			if cfg.ToolTimeouts.MaxSec != nil {
				if *cfg.ToolTimeouts.MaxSec > 0 {
					next.Max = time.Duration(*cfg.ToolTimeouts.MaxSec) * time.Second
				} else {
					log.Printf("[config] %s: tool_timeouts.max_sec must be > 0, ignoring", configPath)
				}
			}
			if next.Default > next.Max {
				log.Printf("[config] %s: tool_timeouts.default_sec (%s) > max_sec (%s), reverting to defaults",
					configPath, next.Default, next.Max)
				next = ToolTimeouts{Default: defaultToolTimeoutDefault, Max: defaultToolTimeoutMax}
			}
			result.ToolTimeouts = next
		}
		if cfg.BashStepTimeouts != nil {
			// Mirrors the tool_timeouts merge above: partial blocks honour
			// whichever knob they set, absent blocks preserve an earlier file's
			// value, and inverted bounds (default > max) revert both fields to
			// the hard-coded package defaults.
			next := result.BashStepTimeouts
			if cfg.BashStepTimeouts.DefaultSec != nil {
				if *cfg.BashStepTimeouts.DefaultSec > 0 {
					next.Default = time.Duration(*cfg.BashStepTimeouts.DefaultSec) * time.Second
				} else {
					log.Printf("[config] %s: bash_step_timeouts.default_sec must be > 0, ignoring", configPath)
				}
			}
			if cfg.BashStepTimeouts.MaxSec != nil {
				if *cfg.BashStepTimeouts.MaxSec > 0 {
					next.Max = time.Duration(*cfg.BashStepTimeouts.MaxSec) * time.Second
				} else {
					log.Printf("[config] %s: bash_step_timeouts.max_sec must be > 0, ignoring", configPath)
				}
			}
			if next.Default > next.Max {
				log.Printf("[config] %s: bash_step_timeouts.default_sec (%s) > max_sec (%s), reverting to defaults",
					configPath, next.Default, next.Max)
				next = BashStepTimeouts{Default: defaultBashStepTimeoutDefault, Max: defaultBashStepTimeoutMax}
			}
			result.BashStepTimeouts = next
		}
		if cfg.Compaction != nil {
			// Mirrors the timeout merges above: partial blocks honour whichever
			// knob they set, absent blocks preserve an earlier file's value, and
			// invalid values are ignored with a log line.
			next := result.Compaction
			if cfg.Compaction.Threshold != nil {
				if t := *cfg.Compaction.Threshold; t > 0 && t <= 1 {
					next.Threshold = t
				} else {
					log.Printf("[config] %s: compaction.threshold must be in (0,1], ignoring", configPath)
				}
			}
			if cfg.Compaction.Auto != nil {
				next.Auto = *cfg.Compaction.Auto
			}
			if cfg.Compaction.KeepLastNTurns != nil {
				n := *cfg.Compaction.KeepLastNTurns
				if n < -1 {
					n = -1
				}
				next.KeepLastNTurns = n
			}
			result.Compaction = next
		}
		// Merge MCP servers: later layer overrides by name, new names are appended.
		for _, srv := range cfg.MCPServers {
			if srv.Name == "" {
				log.Printf("[config] %s: mcp_servers entry missing 'name', skipping", configPath)
				continue
			}
			replaced := false
			for i, existing := range result.MCPServers {
				if existing.Name == srv.Name {
					result.MCPServers[i] = srv // project overrides home
					replaced = true
					break
				}
			}
			if !replaced {
				result.MCPServers = append(result.MCPServers, srv)
			}
		}
	}

	return result
}

// PersistAllowedDirectory appends directories to the allowed_directories list
// in a settings.json file. Uses map[string]any for round-trip safety so that
// unknown fields are preserved.
func PersistAllowedDirectory(configPath string, dirs []string) error {
	var raw map[string]any

	data, err := os.ReadFile(configPath)
	if err != nil {
		// File doesn't exist — create a minimal config.
		raw = map[string]any{"version": float64(CurrentConfigVersion)}
	} else {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("failed to parse %s: %w", configPath, err)
		}
	}

	// Extract existing allowed_directories.
	existing := make(map[string]bool)
	if arr, ok := raw["allowed_directories"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				existing[s] = true
			}
		}
	}

	// Append new dirs, deduplicating.
	for _, d := range dirs {
		existing[d] = true
	}

	sorted := make([]string, 0, len(existing))
	for d := range existing {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)
	raw["allowed_directories"] = sorted

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}

	// Atomic write via temp file + rename.
	tmp, err := os.CreateTemp(filepath.Dir(configPath), ".settings-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), configPath)
}

// workflowsFile is the JSON shape of config/workflow.json: {"workflows": [...]}.
type workflowsFile struct {
	Workflows []WorkflowDef `json:"workflows"`
}

// LoadWorkflowsFile reads a config/workflow.json file and returns its
// validated workflow list, preserving file order. Returns nil on a missing
// file or parse error; individually invalid workflows are skipped with a log
// line. Duplicate names within the file are disambiguated by appending an
// index so the UI can tell them apart.
func LoadWorkflowsFile(path string) []*WorkflowDef {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg workflowsFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		LogError("[workflow] failed to parse %s: %v", path, err)
		return nil
	}

	out := make([]*WorkflowDef, 0, len(cfg.Workflows))
	for i := range cfg.Workflows {
		wf := cfg.Workflows[i]
		if err := validateWorkflow(&wf); err != nil {
			LogError("[workflow] invalid workflow '%s': %v", wf.Name, err)
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

// validateWorkflow checks that a workflow definition is consistent.
func validateWorkflow(pf *WorkflowDef) error {
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
		if step.Type != "agent" && step.Type != "tool" && step.Type != "bash" {
			return fmt.Errorf("step '%s': unknown type '%s' (must be 'agent', 'tool', or 'bash')", stepID, step.Type)
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

	return nil
}

// envVars returns template variables describing the runtime environment.
func envVars(cwd, model string) map[string]string {
	vars := map[string]string{
		"working_directory": cwd,
		"platform":          runtime.GOOS,
		"model":             model,
	}

	// Shell
	if sh := os.Getenv("SHELL"); sh != "" {
		vars["shell"] = filepath.Base(sh)
	} else {
		vars["shell"] = "sh"
	}

	// OS version (best-effort)
	if out, err := osexec.Command("uname", "-r").Output(); err == nil {
		vars["os_version"] = strings.TrimSpace(string(out))
	}

	// Git repo check
	if _, err := os.Stat(filepath.Join(cwd, ".git")); err == nil {
		vars["is_git_repo"] = "Yes"
	} else {
		vars["is_git_repo"] = "No"
	}

	return vars
}

// NewAgentRunner creates a persistent agent for a workflow.
// searchDirs is the ordered set of .vix root directories to resolve system
// prompt includes from, in precedence order (highest first).
// toolTimeouts carries the parent session's tool_timeouts bounds so the
// runner's tool dispatches honour the same settings.json floor/cap.
func NewAgentRunner(config SubagentConfig, cred config.Credential, parentModel, cwd string, toolTimeouts ToolTimeouts, searchDirs ...string) (*AgentRunner, error) {
	model := config.Model
	if model == "" {
		model = parentModel
	}

	maxTurns := config.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 20
	}

	effort := config.Effort
	if effort == "" {
		effort = llm.DefaultEffortFromSpec(model)
	}
	client, err := llm.NewFromModel(model, PluginConfig{}, effort, int64(config.MaxTokens))
	if err != nil {
		return nil, fmt.Errorf("cannot build agent runner: %w", err)
	}
	tools := FilterToolSchemasWithBounds(config.Tools, toolTimeouts.Default, toolTimeouts.Max)

	sysPrompt := promptloader.GetLoader().Resolve(
		config.SystemPrompt,
		envVars(cwd, model),
		promptloader.JoinSearchDirs(searchDirs...),
		nil,
	)

	return &AgentRunner{
		Config:       config,
		LLM:          client,
		Messages:     nil,
		System:       []llm.SystemBlock{{Text: sysPrompt}},
		Tools:        tools,
		MaxTurns:     maxTurns,
		ToolTimeouts: toolTimeouts,
	}, nil
}

// Clone creates a deep copy of the agent runner (for fork_from).
func (a *AgentRunner) Clone(cred config.Credential) (*AgentRunner, error) {
	msgs := make([]llm.MessageParam, len(a.Messages))
	copy(msgs, a.Messages)

	sys := make([]llm.SystemBlock, len(a.System))
	copy(sys, a.System)

	tools := make([]llm.ToolParam, len(a.Tools))
	copy(tools, a.Tools)

	cloneSpec := llm.Spec(a.LLM) // e.g. "openai/gpt-5.1"
	clonedClient, err := llm.NewFromModel(cloneSpec, PluginConfig{}, a.LLM.Effort(), a.LLM.MaxTokens())
	if err != nil {
		return nil, fmt.Errorf("cannot clone agent runner: %w", err)
	}

	return &AgentRunner{
		Config:       a.Config,
		LLM:          clonedClient,
		Messages:     msgs,
		System:       sys,
		Tools:        tools,
		MaxTurns:     a.MaxTurns,
		ToolTimeouts: a.ToolTimeouts,
	}, nil
}

// Send sends a message to the agent, runs the LLM loop with tool dispatch,
// and returns the text output. Conversation history is preserved across calls.
func (a *AgentRunner) Send(
	ctx context.Context,
	userPrompt string,
	executeTool func(name string, params map[string]any, cwd string) (*ToolResult, error),
	streamCallback func(delta string),
	cwd string,
	hooks *TurnHooks,
) (string, error) {
	a.LastInputTokens = 0
	a.LastOutputTokens = 0
	a.LastCacheCreationTokens = 0
	a.LastCacheReadTokens = 0
	a.LastElapsed = 0

	a.Messages = append(a.Messages, llm.NewUserMessage(
		llm.NewTextBlock(userPrompt),
	))

	for turn := 0; turn < a.MaxTurns; turn++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		var msg *llm.Message
		var elapsed time.Duration
		var onThinkingDelta func(string)
		if hooks != nil && hooks.OnThinkingDelta != nil {
			onThinkingDelta = hooks.OnThinkingDelta
		}
		// turnID correlates all retry attempts in the daemon log so
		// `grep req=<turnID>` shows the whole retry sequence as one story.
		turnID := newRequestID()
		// sawAnyStall is a sticky flag: once any attempt in this turn hits
		// a thinking stall, the final attempt will run with extended
		// thinking disabled — even if intervening attempts failed for
		// other reasons (idle timeouts, transient API errors). Gating on
		// only the *immediately* previous attempt was too narrow in
		// practice: stalls interleaved with idle_timeouts kept resetting
		// the flag and the saved final shot was wasted.
		var sawAnyStall bool
		for attempt := range maxRetries {
			attemptCtx := withRequestID(ctx, fmt.Sprintf("%s.%d", turnID, attempt+1))
			streamCtx, streamCancel := context.WithCancel(attemptCtx)
			if hooks != nil && hooks.OnBeforeStream != nil {
				hooks.OnBeforeStream(streamCancel)
			}
			var streamOpts StreamOpts
			if attempt == maxRetries-1 && sawAnyStall {
				empty := ""
				streamOpts.EffortOverride = &empty
				log.Printf("\033[33m[workflow req=%s] final attempt — disabling extended thinking for this call\033[0m", turnID)
			}
			var streamErr error
			msg, elapsed, streamErr = a.LLM.StreamMessageWith(streamCtx, a.System, a.Messages, a.Tools, streamCallback, onThinkingDelta, streamOpts)
			streamCancel()
			if streamErr == nil {
				break
			}
			if errors.Is(streamErr, context.Canceled) {
				return "", streamErr
			}
			// Thinking stall: append the nudge to the workflow agent's
			// messages and retry in the standard backoff loop (counts as
			// one of the maxRetries attempts). finalNext signals the next
			// call (attempt+1) will run with thinking disabled — the nudge
			// tells the model so it doesn't reopen a thinking block.
			finalNext := attempt == maxRetries-2
			if stallErr, nudge, ok := asThinkingStall(streamErr, attempt+1, maxRetries, finalNext); ok {
				sawAnyStall = true
				a.Messages = append(a.Messages, nudge)
				log.Printf("\033[31m[workflow req=%s] thinking stall after %s (attempt %d/%d, nudging and retrying)\033[0m",
					turnID, stallErr.Elapsed, attempt+1, maxRetries)
				if hooks != nil && hooks.OnThinkingStall != nil {
					hooks.OnThinkingStall(stallErr.Elapsed.Milliseconds(), len(stallErr.Summary))
				}
				// Safety net: if thinking-disable was skipped (e.g. prior
				// attempts were API errors, not stalls) and the final
				// attempt still stalls, bail cleanly to avoid the
				// post-loop nil-deref.
				if attempt == maxRetries-1 {
					return "", fmt.Errorf("thinking stall: exhausted %d retries (last elapsed %s)", maxRetries, stallErr.Elapsed)
				}
				CloseIdleHTTPConnections()
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				default:
				}
				continue
			}
			// Note: sawAnyStall intentionally NOT cleared on non-stall
			// errors — once any stall happens in this turn, the final
			// attempt should still run with thinking disabled.
			retryable, reason := classifyError(streamErr)
			if !retryable {
				log.Printf("\033[31m[workflow req=%s] API error: %s — %v\033[0m", turnID, reason, streamErr)
				return "", fmt.Errorf("%s", reason)
			}
			log.Printf("\033[31m[workflow req=%s] API error (attempt %d/%d, retrying): %s — %v\033[0m", turnID, attempt+1, maxRetries, reason, streamErr)
			if attempt == maxRetries-1 {
				return "", fmt.Errorf("%s", reason)
			}
			var wait time.Duration
			var waitSecs int
			if ra := rateLimitRetryAfter(streamErr); ra > 0 {
				wait = ra
				waitSecs = int(math.Ceil(ra.Seconds()))
			} else {
				backoffCap := 60.0
				if isRateLimitError(streamErr) {
					backoffCap = 300.0
				}
				delaySec := math.Min(math.Pow(2, float64(attempt)), backoffCap)
				jitter := rand.Float64() * 0.5
				wait = time.Duration((delaySec + jitter) * float64(time.Second))
				waitSecs = int(math.Ceil(delaySec + jitter))
			}
			if hooks != nil && hooks.OnRetry != nil {
				hooks.OnRetry(attempt+1, maxRetries, waitSecs, reason)
			}
			// Drop pooled conns so the next attempt uses a fresh TCP
			// connection. Cheap; fixes the case where a half-open conn
			// is pinned in the pool and silently swallows every retry.
			CloseIdleHTTPConnections()
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		// Retry loop only breaks on success (streamErr==nil) or returns
		// directly. If we somehow exit with msg still nil, fail loudly
		// instead of nil-dereferencing in the usage-accumulator below.
		if msg == nil {
			return "", fmt.Errorf("workflow agent exhausted %d retries without a response", maxRetries)
		}

		LogLLMCall(a.LLM.Model(), a.System, a.Messages, a.Tools, msg)

		a.LastInputTokens += msg.Usage.InputTokens
		a.LastOutputTokens += msg.Usage.OutputTokens
		a.LastCacheCreationTokens += msg.Usage.CacheCreationTokens
		a.LastCacheReadTokens += msg.Usage.CacheReadTokens
		a.LastElapsed += elapsed

		if hooks != nil && hooks.OnStreamDone != nil {
			hooks.OnStreamDone(msg.Usage.InputTokens, msg.Usage.OutputTokens, msg.Usage.CacheCreationTokens, msg.Usage.CacheReadTokens, elapsed.Milliseconds())
		}

		a.Messages = append(a.Messages, msg.ToParam())

		if msg.StopReason == llm.StopEndTurn {
			text := extractTextFromMessage(msg)
			return text, nil
		}

		if msg.StopReason == llm.StopToolUse {
			toolResults := subagentDispatchToolCalls(ctx, msg, executeTool, cwd, hooks, a.ToolTimeouts.Default, a.ToolTimeouts.Max)
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			a.Messages = append(a.Messages, llm.NewUserMessage(toolResults...))
			continue
		}

		if msg.StopReason == llm.StopMaxTokens {
			return extractTextFromMessage(msg), ErrMaxTokens
		}

		return "", fmt.Errorf("unexpected stop reason: %s", msg.StopReason)
	}

	lastText := ""
	for i := len(a.Messages) - 1; i >= 0; i-- {
		for _, block := range a.Messages[i].Content {
			if block.Type == llm.BlockText {
				lastText += block.Text
			}
		}
		if lastText != "" {
			break
		}
	}
	if lastText == "" {
		lastText = fmt.Sprintf("Workflow agent '%s' reached max turns (%d) without completing.", a.Config.Name, a.MaxTurns)
	}
	return lastText, nil
}

// stripMarkdownFence removes optional markdown code fences from a string.
// It searches for the first ```json or ``` fence anywhere in the string,
// so preamble text before the fence is handled correctly.
func stripMarkdownFence(s string) string {
	s = strings.TrimSpace(s)
	// Find a ```json fence anywhere in the string
	if idx := strings.Index(s, "```json"); idx >= 0 {
		inner := s[idx+len("```json"):]
		if end := strings.LastIndex(inner, "```"); end >= 0 {
			return strings.TrimSpace(inner[:end])
		}
	}
	// Fall back to a generic ``` fence
	if idx := strings.Index(s, "```"); idx >= 0 {
		inner := s[idx+len("```"):]
		if end := strings.LastIndex(inner, "```"); end >= 0 {
			return strings.TrimSpace(inner[:end])
		}
	}
	return s
}

// buildStepVars builds a variable map from step results.
// For each step, it sets "step.<id>" to the raw output and includes input params
// as "step.<id>.<param>". If the step had json_output and parsing succeeded,
// each JSON key becomes "step.<id>.<key>".
func buildStepVars(results map[string]*StepResult) map[string]string {
	vars := make(map[string]string)
	for sid, r := range results {
		vars["step."+sid] = r.Output
		// Include step input params
		for k, v := range r.Params {
			vars["step."+sid+"."+k] = v
		}
		// Include parsed JSON fields (only when json_output was true and parse succeeded)
		if r.Parsed != nil {
			for k, v := range r.Parsed {
				switch val := v.(type) {
				case string:
					vars["step."+sid+"."+k] = val
				default:
					_ = val
					if b, err := json.MarshalIndent(v, "", "  "); err == nil {
						vars["step."+sid+"."+k] = string(b)
					}
				}
			}
		}
	}
	return vars
}

// resolveParams resolves parameter values against a variable pool.
// All $(...) references within values are replaced with their corresponding vars.
func resolveParams(params map[string]string, vars map[string]string) map[string]string {
	if len(params) == 0 {
		return nil
	}
	resolved := make(map[string]string, len(params))
	for k, v := range params {
		result := v
		for varName, varVal := range vars {
			result = strings.ReplaceAll(result, "$("+varName+")", varVal)
		}
		resolved[k] = result
	}
	return resolved
}

// resolveTemplateString replaces all $(key) occurrences in a string with values from vars.
func resolveTemplateString(tmpl string, vars map[string]string) string {
	result := tmpl
	for varName, varVal := range vars {
		result = strings.ReplaceAll(result, "$("+varName+")", varVal)
	}
	return result
}

// resolveBashExpansions scans s for $(bash:...) tokens, executes each command
// with the given cwd, and replaces the token with the trimmed stdout+stderr output.
// On command error the token is replaced with an empty string (non-fatal).
func resolveBashExpansions(s string, cwd string) string {
	const prefix = "$(bash:"
	for {
		start := strings.Index(s, prefix)
		if start == -1 {
			break
		}
		// Find the matching closing paren after the prefix.
		rest := s[start+len(prefix):]
		end := strings.Index(rest, ")")
		if end == -1 {
			break
		}
		cmd := rest[:end]
		token := prefix + cmd + ")"

		var replacement string
		c := osexec.Command("bash", "-c", cmd)
		c.Dir = cwd
		out, err := c.CombinedOutput()
		if err == nil {
			replacement = strings.TrimRight(string(out), "\n")
		}
		s = strings.Replace(s, token, replacement, 1)
	}
	return s
}

// evaluateExecuteIf runs the condition string as a bash expression and returns
// true if the command exits with code 0 (standard Unix: success = condition met).
// An empty condition always returns true (backward-compatible default).
func evaluateExecuteIf(condition string, cwd string) bool {
	if condition == "" {
		return true
	}
	c := osexec.Command("bash", "-c", condition)
	c.Dir = cwd
	err := c.Run()
	// Exit code 0 → condition true → run this step.
	return err == nil
}

// bashOutputPreview returns the first n lines of output for display in the UI.
func bashOutputPreview(output string, n int) string {
	lines := strings.SplitN(strings.TrimRight(output, "\n"), "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// extractStepSummary extracts a display summary from JSON output using the given key.
func extractStepSummary(raw string, key string) string {
	if key == "" {
		return ""
	}
	stripped := stripMarkdownFence(raw)
	var obj map[string]any
	if err := json.Unmarshal([]byte(stripped), &obj); err != nil {
		return ""
	}
	if s, ok := obj[key].(string); ok {
		return s
	}
	return ""
}

// stepToolTracker counts tool calls and accumulates output line counts per tool.
type stepToolTracker struct {
	calls map[string]*toolCallAcc
	order []string
}

type toolCallAcc struct {
	Count     int
	LineCount int
}

func newStepToolTracker() *stepToolTracker {
	return &stepToolTracker{calls: make(map[string]*toolCallAcc)}
}

func (t *stepToolTracker) RecordCall(name string) {
	acc, ok := t.calls[name]
	if !ok {
		acc = &toolCallAcc{}
		t.calls[name] = acc
		t.order = append(t.order, name)
	}
	acc.Count++
}

func (t *stepToolTracker) RecordResult(name, output string) {
	acc, ok := t.calls[name]
	if !ok {
		acc = &toolCallAcc{}
		t.calls[name] = acc
		t.order = append(t.order, name)
	}
	lines := strings.Count(output, "\n")
	if output != "" && !strings.HasSuffix(output, "\n") {
		lines++
	}
	acc.LineCount += lines
}

func (t *stepToolTracker) Stats() []protocol.ToolStat {
	var stats []protocol.ToolStat
	for _, name := range t.order {
		acc := t.calls[name]
		stats = append(stats, protocol.ToolStat{
			Name:    name,
			Calls:   acc.Count,
			Summary: aggregateToolSummary(name, acc),
		})
	}
	return stats
}

func aggregateToolSummary(name string, acc *toolCallAcc) string {
	switch name {
	case "read_file", "read_minified_file":
		return fmt.Sprintf("%d lines read", acc.LineCount)
	case "grep":
		if acc.LineCount == 0 {
			return "no matches"
		}
		return fmt.Sprintf("%d results", acc.LineCount)
	case "glob_files":
		if acc.LineCount == 0 {
			return "no matches"
		}
		return fmt.Sprintf("%d files", acc.LineCount)
	case "bash":
		return fmt.Sprintf("%d lines of output", acc.LineCount)
	case "write_file", "write_minified_file":
		return fmt.Sprintf("%d files written", acc.Count)
	case "edit_file", "edit_minified_file":
		return fmt.Sprintf("%d edits", acc.Count)
	default:
		return ""
	}
}

// executeToolStep runs a tool-type step and returns the next step refs and output text.
func (s *Session) executeToolStep(ctx context.Context, step WorkflowStepDef, baseVars map[string]string) (nextRefs []StepRef, output string, err error) {
	switch step.Tool {
	case "ask_question_to_user":
		question := step.Question
		if question == "" {
			question = "Review the output and provide feedback."
		}
		category := step.Category
		if category == "" {
			category = "Review"
		}

		var richOptions []protocol.EventQuestionOption
		for _, opt := range step.Options {
			richOptions = append(richOptions, protocol.EventQuestionOption{
				Title:        opt.Title,
				Description:  opt.Description,
				HasUserInput: opt.HasUserInput,
			})
		}

		s.emit("event.user_question", protocol.EventUserQuestion{
			Question:    question,
			RichOptions: richOptions,
			Category:    category,
		})

		cmd, ok := s.waitForCommand(ctx, "session.user_answer")
		if !ok {
			return nil, "", ctx.Err()
		}

		var answerData protocol.SessionUserAnswerData
		json.Unmarshal(cmd.Data, &answerData)
		answer := strings.TrimSpace(answerData.Answer)

		for _, opt := range step.Options {
			if strings.EqualFold(answer, opt.Title) {
				outputText := "User selected: " + opt.Title
				if opt.HasUserInput && strings.TrimSpace(answerData.Text) != "" {
					outputText = strings.TrimSpace(answerData.Text)
				}

				// Resolve option params against base vars + user_text
				if len(opt.Steps) > 0 {
					resolveVars := make(map[string]string, len(baseVars)+1)
					for k, v := range baseVars {
						resolveVars[k] = v
					}
					if opt.HasUserInput {
						resolveVars["user_text"] = strings.TrimSpace(answerData.Text)
					}
					var resolved []StepRef
					for _, s := range opt.Steps {
						resolved = append(resolved, StepRef{
							ID:     s.ID,
							Params: resolveParams(s.Params, resolveVars),
						})
					}
					return resolved, outputText, nil
				}
				return nil, outputText, nil
			}
		}

		// No match — fallback to NextSteps
		if len(step.NextSteps) > 0 {
			return step.NextSteps, "User selected: " + answer, nil
		}
		return nil, "User selected: " + answer, nil

	default:
		result := s.executeToolConfirmed(ctx, step.Tool, map[string]any{})
		if result.IsError {
			return nil, "", fmt.Errorf("tool '%s' failed: %s", step.Tool, result.Output)
		}
		return nil, result.Output, nil
	}
}

// executeParallelSteps launches multiple steps in parallel goroutines.
// It returns the continuation refs chosen by any tool step (e.g. ask_question_to_user),
// so the caller can follow the user's routing decision after the parallel block completes.
func (s *Session) executeParallelSteps(
	ctx context.Context,
	refs []StepRef,
	pf *WorkflowDef,
	exec *WorkflowRun,
	baseVars map[string]string,
	stepCosts *[]protocol.StepCost,
	logicalStep *int,
	workflowStart time.Time,
	cred config.Credential, parentModel string,
	prompt string,
	executeTool func(name string, params map[string]any, cwd string) (*ToolResult, error),
) ([]StepRef, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	errs := make([]error, len(refs))
	var contRefs []StepRef

	for i, ref := range refs {
		wg.Add(1)
		go func(idx int, ref StepRef) {
			defer wg.Done()
			step := pf.Steps[ref.ID]
			stepID := ref.ID
			stepParams := ref.Params

			mu.Lock()
			*logicalStep++
			myLogicalStep := *logicalStep
			mu.Unlock()

			silent := step.Silent
			stepCtx := ctx
			if silent {
				stepCtx = withSilentCtx(ctx)
			}

			s.emitIfVisible(silent, "event.workflow_step_start", protocol.EventWorkflowStepStart{
				StepID:      stepID,
				StepIdx:     myLogicalStep,
				Total:       0,
				Explanation: step.Explanation,
			})

			stepStart := time.Now()

			switch step.Type {
			case "bash":
				vars := make(map[string]string, len(baseVars))
				for k, v := range baseVars {
					vars[k] = v
				}
				mu.Lock()
				for k, v := range buildStepVars(exec.StepResults) {
					vars[k] = v
				}
				mu.Unlock()
				for k, v := range stepParams {
					vars[k] = v
				}
				resolvedCmd := resolveBashExpansions(resolveTemplateString(step.Command, vars), s.cwd)
				resolvedInput := resolveBashExpansions(resolveTemplateString(step.Input, vars), s.cwd)

				// Per-step deadline so a wedged bash command can't hold the
				// whole parallel batch. Killing is handled inside
				// runBashWithContext via process-group SIGKILL.
				bashTimeout := resolveBashStepTimeout(step.TimeoutSec, s.projectConfig.BashStepTimeouts)
				bashCtx, bashCancel := context.WithTimeout(stepCtx, bashTimeout)
				outputStr, err := runBashWithContext(bashCtx, resolvedCmd, s.cwd, resolvedInput, func(line string) {
					s.emitIfVisible(silent, "event.stream_chunk", protocol.EventStreamChunk{Text: line + "\n"})
				})
				bashCancel()
				// Our deadline fired vs. the whole session being cancelled:
				// treat the former as a non-fatal step-level timeout so the
				// parallel batch continues (caller still gets a failed step
				// event + a step result with captured output).
				timedOut := bashCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil
				output := []byte(outputStr)
				stepElapsed := time.Since(stepStart).Milliseconds()

				mu.Lock()
				exec.StepResults[stepID] = &StepResult{Output: string(output), Params: stepParams}
				if !silent {
					*stepCosts = append(*stepCosts, protocol.StepCost{
						StepID:      stepID,
						Explanation: step.Explanation,
						DurationMs:  stepElapsed,
					})
				}
				mu.Unlock()

				if timedOut {
					s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
						StepID: stepID, StepIdx: myLogicalStep, Success: false, TimedOut: true, DurationMs: stepElapsed,
						Command: resolvedCmd, BashOutput: bashOutputPreview(string(output), 5),
					})
					return
				}
				if err != nil {
					s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
						StepID: stepID, StepIdx: myLogicalStep, Success: false, DurationMs: stepElapsed,
						Command: resolvedCmd, BashOutput: bashOutputPreview(string(output), 5),
					})
					errs[idx] = fmt.Errorf("step '%s' bash failed: %w (output: %s)", stepID, err, string(output))
					return
				}
				s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
					StepID: stepID, StepIdx: myLogicalStep, Success: true, DurationMs: stepElapsed,
					Command: resolvedCmd, BashOutput: bashOutputPreview(string(output), 5),
				})

			case "agent":
				var agent *AgentRunner
				if step.Agent != "" {
					config, ok := s.customAgents[step.Agent]
					if !ok {
						errs[idx] = fmt.Errorf("step '%s': agent '%s' not found", stepID, step.Agent)
						return
					}
					if step.Effort != "" {
						config.Effort = step.Effort
					}
					ar, err := NewAgentRunner(config, cred, parentModel, s.cwd, s.projectConfig.ToolTimeouts, s.searchDirsSlice()...)
					if err != nil {
						errs[idx] = fmt.Errorf("step '%s': %w", stepID, err)
						return
					}
					agent = ar
					if s.headless {
						agent.Tools = ExcludeTools(agent.Tools, "ask_question_to_user")
					}
					if step.Signal {
						agent.Tools = appendSignalTool(agent.Tools)
					}
				} else if step.ForkFrom != "" {
					mu.Lock()
					source, ok := exec.StepAgents[step.ForkFrom]
					mu.Unlock()
					if !ok {
						errs[idx] = fmt.Errorf("step '%s': fork_from '%s' has no agent instance", stepID, step.ForkFrom)
						return
					}
					ar, err := source.Clone(cred)
					if err != nil {
						errs[idx] = fmt.Errorf("step '%s': %w", stepID, err)
						return
					}
					agent = ar
					if step.Signal {
						agent.Tools = appendSignalTool(agent.Tools)
					}
				}

				vars := envVars(s.cwd, s.model)
				vars["workflow.prompt"] = prompt
				mu.Lock()
				for k, v := range buildStepVars(exec.StepResults) {
					vars[k] = v
				}
				mu.Unlock()
				mergeRuntimeVars(vars, exec.State, pf.Budget)
				for k, v := range stepParams {
					vars[k] = v
				}

				resolvedMessage := resolveBashExpansions(promptloader.GetLoader().Resolve(
					step.Prompt, vars, s.searchDirs(), nil,
				), s.cwd)

				streamCb := func(delta string) {
					if step.IsStreamVisible() {
						s.emitIfVisible(silent, "event.stream_chunk", protocol.EventStreamChunk{Text: delta})
					}
				}

				stepExecuteTool := func(name string, params map[string]any, cwd string) (*ToolResult, error) {
					if name == "workflow_signal" {
						return s.handleWorkflowSignal(pf, exec.State, stepID, params), nil
					}
					for _, t := range step.DenyTools {
						if t == name {
							return &ToolResult{Output: fmt.Sprintf("tool '%s' is denied in step '%s'", name, stepID), IsError: true}, nil
						}
					}
					return executeTool(name, params, cwd)
				}

				stepHooks := s.hooksForStep(silent)
				if exec.State != nil {
					base := stepHooks.OnStreamDone
					stepHooks.OnStreamDone = func(inputTokens, outputTokens, cacheCreation, cacheRead, elapsedMs int64) {
						atomic.AddInt64(&exec.State.Budget.TokensUsed, inputTokens+outputTokens+cacheCreation+cacheRead)
						if base != nil {
							base(inputTokens, outputTokens, cacheCreation, cacheRead, elapsedMs)
						}
					}
				}

				output, err := agent.Send(stepCtx, resolvedMessage, stepExecuteTool, streamCb, s.cwd, stepHooks)
				stepElapsed := time.Since(stepStart).Milliseconds()

				if err == nil && step.Output != "" {
					outPath := resolveTemplateString(step.Output, vars)
					if !filepath.IsAbs(outPath) {
						outPath = filepath.Join(s.cwd, outPath)
					}
					os.MkdirAll(filepath.Dir(outPath), 0o755)
					os.WriteFile(outPath, []byte(output), 0o644)
				}

				mu.Lock()
				exec.StepResults[stepID] = &StepResult{Output: output, Params: stepParams}
				exec.StepAgents[stepID] = agent
				if !silent {
					*stepCosts = append(*stepCosts, protocol.StepCost{
						StepID:              stepID,
						Explanation:         step.Explanation,
						Model:               agent.LLM.Model(),
						InputTokens:         agent.LastInputTokens,
						OutputTokens:        agent.LastOutputTokens,
						CacheCreationTokens: agent.LastCacheCreationTokens,
						CacheReadTokens:     agent.LastCacheReadTokens,
						Cost:                protocol.CalculateCost(llm.Spec(agent.LLM), agent.LastInputTokens, agent.LastOutputTokens, agent.LastCacheCreationTokens, agent.LastCacheReadTokens),
						DurationMs:          stepElapsed,
					})
				}
				mu.Unlock()

				if err != nil {
					s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
						StepID: stepID, StepIdx: myLogicalStep, Success: false, DurationMs: stepElapsed,
					})
					errs[idx] = fmt.Errorf("step '%s' failed: %w", stepID, err)
					return
				}
				s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
					StepID: stepID, StepIdx: myLogicalStep, Success: true, DurationMs: stepElapsed,
				})

			case "tool":
				toolVars := make(map[string]string, len(baseVars))
				for k, v := range baseVars {
					toolVars[k] = v
				}
				mu.Lock()
				for k, v := range buildStepVars(exec.StepResults) {
					toolVars[k] = v
				}
				mu.Unlock()
				for k, v := range stepParams {
					toolVars[k] = v
				}

				toolNextRefs, output, err := s.executeToolStep(ctx, step, toolVars)
				stepElapsed := time.Since(stepStart).Milliseconds()

				mu.Lock()
				contRefs = append(contRefs, toolNextRefs...)
				exec.StepResults[stepID] = &StepResult{Output: output, Params: stepParams}
				if !silent {
					*stepCosts = append(*stepCosts, protocol.StepCost{
						StepID: stepID, Explanation: step.Explanation, DurationMs: stepElapsed,
					})
				}
				mu.Unlock()

				if err != nil {
					s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
						StepID: stepID, StepIdx: myLogicalStep, Success: false, DurationMs: stepElapsed,
					})
					errs[idx] = fmt.Errorf("step '%s' failed: %w", stepID, err)
					return
				}
				s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
					StepID: stepID, StepIdx: myLogicalStep, Success: true, DurationMs: stepElapsed,
				})
			}
		}(i, ref)
	}

	wg.Wait()

	// Collect errors
	var errMsgs []string
	for _, e := range errs {
		if e != nil {
			errMsgs = append(errMsgs, e.Error())
		}
	}
	if len(errMsgs) > 0 {
		return nil, fmt.Errorf("parallel steps failed: %s", strings.Join(errMsgs, "; "))
	}
	return contRefs, nil
}

// executeWorkflow runs a full workflow to completion. When resume is non-nil
// the run continues from the persisted cursor: step results, per-step agent
// conversations, and budget accounting are restored, and the entry point is
// replaced by resume.CurrentRef.
func (s *Session) executeWorkflow(ctx context.Context, pf *WorkflowDef, prompt string, resume *WorkflowRunState) error {
	exec := &WorkflowRun{
		Def:         pf,
		StepAgents:  make(map[string]*AgentRunner),
		StepResults: make(map[string]*StepResult),
	}

	if resume != nil {
		prompt = resume.Prompt
	}
	state := &WorkflowRunState{
		Name:   pf.Name,
		Status: WorkflowStatusRunning,
		Prompt: prompt,
	}
	elapsed := elapsedTracker{start: time.Now()}
	if resume != nil {
		state.Iteration = resume.Iteration
		state.Budget = resume.Budget
		state.BudgetRouted = resume.BudgetRouted
		state.ErrorRouted = resume.ErrorRouted
		elapsed.base = resume.Budget.ElapsedSeconds
		for id, r := range resume.StepResults {
			exec.StepResults[id] = r
		}
	}
	exec.State = state

	cred := s.llm.Credential()
	parentModel := s.model

	// Rebuild step agents from a resume snapshot. A runner is fully derivable
	// from its SubagentConfig plus conversation; rebuild failures are logged
	// and skipped so the step falls back to a fresh agent when re-visited.
	if resume != nil {
		for id, snap := range resume.StepAgents {
			stepDef, ok := pf.Steps[id]
			if !ok {
				continue
			}
			ar, err := NewAgentRunner(snap.Config, cred, parentModel, s.cwd, s.projectConfig.ToolTimeouts, s.searchDirsSlice()...)
			if err != nil {
				LogError("[workflow] resume: cannot rebuild agent for step '%s': %v", id, err)
				continue
			}
			ar.Messages = append([]llm.MessageParam(nil), snap.Messages...)
			if s.headless {
				ar.Tools = ExcludeTools(ar.Tools, "ask_question_to_user")
			}
			if stepDef.Signal {
				ar.Tools = appendSignalTool(ar.Tools)
			}
			exec.StepAgents[id] = ar
		}
	}

	executeTool := func(name string, params map[string]any, cwd string) (*ToolResult, error) {
		return s.executeToolConfirmed(ctx, name, params), nil
	}

	// Build an ordered linear step list by walking the entry-point chain.
	// We follow only the first next_step at each node so we get the happy-path
	// order; branching steps added at runtime will be appended dynamically.
	var orderedSteps []protocol.WorkflowStepInfo
	{
		seen := map[string]bool{}
		cur := pf.EntryPoint.ID
		for cur != "" && cur != "stop" && !seen[cur] {
			seen[cur] = true
			step, ok := pf.Steps[cur]
			if !ok {
				break
			}
			orderedSteps = append(orderedSteps, protocol.WorkflowStepInfo{
				ID:          cur,
				Explanation: step.Explanation,
			})
			if len(step.NextSteps) > 0 {
				cur = step.NextSteps[0].ID
			} else {
				break
			}
		}
	}

	// Emit workflow start
	s.emit("event.workflow_start", protocol.EventWorkflowStart{
		WorkflowName: pf.Name,
		TotalSteps:   len(pf.Steps),
		Steps:        orderedSteps,
	})

	var stepCosts []protocol.StepCost
	workflowStart := time.Now()
	var stopped bool

	// Base vars: workflow.prompt is the magic variable
	baseVars := envVars(s.cwd, s.model)
	baseVars["workflow.prompt"] = prompt
	baseVars["session.id"] = s.id

	// Resolve entry point params — or, on resume, pick up at the saved cursor.
	currentRef := &StepRef{
		ID:     pf.EntryPoint.ID,
		Params: resolveParams(pf.EntryPoint.Params, baseVars),
	}
	if resume != nil && resume.CurrentRef != nil {
		if _, ok := pf.Steps[resume.CurrentRef.ID]; ok {
			currentRef = resume.CurrentRef
		} else {
			LogError("[workflow] resume: step '%s' no longer exists in %q, restarting from entry point", resume.CurrentRef.ID, pf.Name)
		}
	}
	var routedFrom string
	var logicalStep int

	// Hard iteration cap. The configured budget governs the real limit (and
	// routes to on_exceeded below); this is a safety net against runaway
	// loops, widened when a budget legitimately allows more iterations.
	maxIterations := 200
	if pf.Budget != nil && pf.Budget.MaxIterations > 0 && pf.Budget.MaxIterations+50 > maxIterations {
		maxIterations = pf.Budget.MaxIterations + 50
	}

	// Finalize on every exit path: completed runs clear their persisted state;
	// interrupted runs (cancel/crash) park as paused, failed ones as blocked —
	// both resumable from the cursor. Either way the run produced content the
	// user may not have seen: flag the session unread.
	finished := false
	defer func() {
		s.unread = true
		if finished {
			s.setWorkflowRunState(nil)
			s.persist()
			return
		}
		if ctx.Err() != nil {
			state.Status = WorkflowStatusPaused
		} else if state.Status == WorkflowStatusRunning {
			state.Status = WorkflowStatusBlocked
		}
		state.Budget.ElapsedSeconds = elapsed.seconds()
		s.saveWorkflowProgress(exec, currentRef)
		s.emitWorkflowStatus(pf, state, currentRef)
	}()

	for iteration := 0; currentRef != nil && currentRef.ID != "" && currentRef.ID != "stop" && iteration < maxIterations; iteration++ {
		step := pf.Steps[currentRef.ID]
		stepID := currentRef.ID
		stepParams := currentRef.Params
		logicalStep++
		state.Budget.ElapsedSeconds = elapsed.seconds()

		if ctx.Err() != nil {
			s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
				WorkflowName: pf.Name,
				Success:      false,
				DurationMs:   time.Since(workflowStart).Milliseconds(),
			})
			s.activePlan = nil
			return ctx.Err()
		}

		// Budget gate: when any configured limit is spent, flip the run to
		// budget_limited and divert — once — to the on_exceeded step (so the
		// workflow can wrap up gracefully), or stop when none is configured.
		if !state.BudgetRouted && state.budgetExceeded(pf.Budget) {
			state.Status = WorkflowStatusBudgetLimited
			state.BudgetRouted = true
			s.emitWorkflowStatus(pf, state, currentRef)
			routeVars := make(map[string]string, len(baseVars))
			for k, v := range baseVars {
				routeVars[k] = v
			}
			for k, v := range buildStepVars(exec.StepResults) {
				routeVars[k] = v
			}
			mergeRuntimeVars(routeVars, state, pf.Budget)
			if oe := pf.Budget.OnExceeded; oe != nil && oe.ID != "" && oe.ID != "stop" {
				currentRef = &StepRef{ID: oe.ID, Params: resolveParams(oe.Params, routeVars)}
				step = pf.Steps[currentRef.ID]
				stepID = currentRef.ID
				stepParams = currentRef.Params
			} else {
				stopped = true
				break
			}
		}

		// Each workflow_signal is only visible to the routing decisions that
		// follow it: starting a new signal-capable step clears the previous one.
		if step.Signal {
			state.Signal = SignalState{}
		}
		state.Iteration++

		// Refresh live accounting vars ($(workflow.*)) for prompts and
		// execute_if conditions, then checkpoint the run so an interruption
		// anywhere in this step resumes from this exact cursor.
		mergeRuntimeVars(baseVars, state, pf.Budget)
		s.saveWorkflowProgress(exec, currentRef)

		silent := step.Silent
		stepCtx := ctx
		if silent {
			stepCtx = withSilentCtx(ctx)
		}

		s.emitIfVisible(silent, "event.workflow_step_start", protocol.EventWorkflowStepStart{
			StepID:      stepID,
			StepIdx:     logicalStep,
			Total:       0,
			Explanation: step.Explanation,
		})

		stepStart := time.Now()

		switch step.Type {
		case "bash":
			vars := make(map[string]string, len(baseVars))
			for k, v := range baseVars {
				vars[k] = v
			}
			for k, v := range buildStepVars(exec.StepResults) {
				vars[k] = v
			}
			for k, v := range stepParams {
				vars[k] = v
			}
			resolvedCmd := resolveBashExpansions(resolveTemplateString(step.Command, vars), s.cwd)
			resolvedInput := resolveBashExpansions(resolveTemplateString(step.Input, vars), s.cwd)

			// Per-step deadline: on breach the process group is SIGKILLed
			// (see runBashWithContext) but — unlike a non-zero exit code —
			// the workflow does NOT abort. Control falls through to the
			// next_steps evaluation below so branches like
			// `execute_if: [ "$(cat /tmp/.vix-reward)" = "1" ]` can route
			// into retry/fallback paths.
			bashTimeout := resolveBashStepTimeout(step.TimeoutSec, s.projectConfig.BashStepTimeouts)
			bashCtx, bashCancel := context.WithTimeout(stepCtx, bashTimeout)
			cmdOutputStr, cmdErr := runBashWithContext(bashCtx, resolvedCmd, s.cwd, resolvedInput, func(line string) {
				s.emitIfVisible(silent, "event.stream_chunk", protocol.EventStreamChunk{Text: line + "\n"})
			})
			bashCancel()
			// Distinguish our own deadline firing from the session context
			// being cancelled — only the former is "carry on"; the latter
			// still aborts the workflow via the generic cmdErr branch below.
			timedOut := bashCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil
			cmdOutput := []byte(cmdOutputStr)
			exec.StepResults[stepID] = &StepResult{Output: string(cmdOutput), Params: stepParams}
			stepElapsed := time.Since(stepStart).Milliseconds()
			if !silent {
				stepCosts = append(stepCosts, protocol.StepCost{
					StepID:      stepID,
					Explanation: step.Explanation,
					DurationMs:  stepElapsed,
				})
			}
			if timedOut {
				s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
					StepID: stepID, StepIdx: logicalStep, Success: false, TimedOut: true, DurationMs: stepElapsed,
					Command: resolvedCmd, BashOutput: bashOutputPreview(string(cmdOutput), 5),
				})
				// Fall through to the next_steps block below — no abort.
			} else if cmdErr != nil {
				s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
					StepID: stepID, StepIdx: logicalStep, Success: false, DurationMs: stepElapsed,
					Command: resolvedCmd, BashOutput: bashOutputPreview(string(cmdOutput), 5),
				})
				s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
					WorkflowName: pf.Name, Success: false, StepCosts: stepCosts,
					DurationMs: time.Since(workflowStart).Milliseconds(),
				})
				s.activePlan = nil
				return fmt.Errorf("step '%s' bash failed: %w (output: %s)", stepID, cmdErr, string(cmdOutput))
			} else {
				s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
					StepID: stepID, StepIdx: logicalStep, Success: true, DurationMs: stepElapsed,
					Command: resolvedCmd, BashOutput: bashOutputPreview(string(cmdOutput), 5),
				})
			}

			// Advance to next step(s)
			if len(step.NextSteps) > 0 {
				if len(step.NextSteps) == 1 {
					ns := step.NextSteps[0]
					resolvedCondition := resolveBashExpansions(resolveTemplateString(ns.ExecuteIf, vars), s.cwd)
					if evaluateExecuteIf(resolvedCondition, s.cwd) {
						currentRef = &StepRef{
							ID:     ns.ID,
							Params: resolveParams(ns.Params, vars),
						}
					} else {
						currentRef = nil
					}
				} else {
					// Multiple next steps — filter by execute_if.
					// If exactly one passes, advance to it sequentially.
					// If more than one passes, run them in parallel.
					var resolved []StepRef
					for _, ns := range step.NextSteps {
						resolvedCondition := resolveBashExpansions(resolveTemplateString(ns.ExecuteIf, vars), s.cwd)
						if evaluateExecuteIf(resolvedCondition, s.cwd) {
							resolved = append(resolved, StepRef{ID: ns.ID, Params: resolveParams(ns.Params, vars)})
						}
					}
					if len(resolved) == 0 {
						currentRef = nil
					} else if len(resolved) == 1 {
						if resolved[0].ID == "stop" {
							stopped = true
							goto done
						}
						currentRef = &resolved[0]
					} else {
						contRefs, err := s.executeParallelSteps(ctx, resolved, pf, exec, baseVars, &stepCosts, &logicalStep, workflowStart, cred, parentModel, prompt, executeTool)
						if err != nil {
							s.activePlan = nil
							s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
								WorkflowName: pf.Name, Success: false, StepCosts: stepCosts,
								DurationMs: time.Since(workflowStart).Milliseconds(),
							})
							return err
						}
						if len(contRefs) == 1 {
							if contRefs[0].ID == "stop" {
								stopped = true
								goto done
							}
							currentRef = &contRefs[0]
						} else {
							currentRef = nil
						}
					}
				}
			} else {
				currentRef = nil
			}

		case "tool":
			toolVars := make(map[string]string, len(baseVars))
			for k, v := range baseVars {
				toolVars[k] = v
			}
			for k, v := range buildStepVars(exec.StepResults) {
				toolVars[k] = v
			}
			for k, v := range stepParams {
				toolVars[k] = v
			}

			nextRefs, output, err := s.executeToolStep(stepCtx, step, toolVars)
			if err != nil {
				s.activePlan = nil
				s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
					StepID:     stepID,
					StepIdx:    logicalStep,
					Success:    false,
					DurationMs: time.Since(stepStart).Milliseconds(),
				})
				s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
					WorkflowName: pf.Name,
					Success:      false,
					StepCosts:    stepCosts,
					DurationMs:   time.Since(workflowStart).Milliseconds(),
				})
				return fmt.Errorf("step '%s' failed: %w", stepID, err)
			}
			exec.StepResults[stepID] = &StepResult{Output: output}
			stepElapsed := time.Since(stepStart).Milliseconds()
			if !silent {
				stepCosts = append(stepCosts, protocol.StepCost{
					StepID:      stepID,
					Explanation: step.Explanation,
					DurationMs:  stepElapsed,
				})
			}
			s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
				StepID:     stepID,
				StepIdx:    logicalStep,
				Success:    true,
				DurationMs: stepElapsed,
			})

			if len(nextRefs) > 0 {
				// Check for stop
				for _, nr := range nextRefs {
					if nr.ID == "stop" {
						stopped = true
						goto done
					}
				}
				if len(nextRefs) == 1 {
					resolvedCondition := resolveBashExpansions(resolveTemplateString(nextRefs[0].ExecuteIf, toolVars), s.cwd)
					if evaluateExecuteIf(resolvedCondition, s.cwd) {
						routedFrom = stepID
						currentRef = &nextRefs[0]
					} else {
						currentRef = nil
					}
					continue
				}
				// Multiple next refs — filter by execute_if, then parallel execution
				var filteredRefs []StepRef
				for _, nr := range nextRefs {
					resolvedCondition := resolveBashExpansions(resolveTemplateString(nr.ExecuteIf, toolVars), s.cwd)
					if evaluateExecuteIf(resolvedCondition, s.cwd) {
						filteredRefs = append(filteredRefs, nr)
					}
				}
				if len(filteredRefs) == 0 {
					currentRef = nil
					continue
				}
				contRefs, err := s.executeParallelSteps(ctx, filteredRefs, pf, exec, baseVars, &stepCosts, &logicalStep, workflowStart, cred, parentModel, prompt, executeTool)
				if err != nil {
					s.activePlan = nil
					s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
						WorkflowName: pf.Name, Success: false, StepCosts: stepCosts,
						DurationMs: time.Since(workflowStart).Milliseconds(),
					})
					return err
				}
				if len(contRefs) == 1 {
					if contRefs[0].ID == "stop" {
						stopped = true
						goto done
					}
					currentRef = &contRefs[0]
				} else {
					currentRef = nil
				}
				continue
			}
			if len(step.NextSteps) > 0 {
				if len(step.NextSteps) == 1 {
					ns := step.NextSteps[0]
					resolvedCondition := resolveBashExpansions(resolveTemplateString(ns.ExecuteIf, toolVars), s.cwd)
					if evaluateExecuteIf(resolvedCondition, s.cwd) {
						currentRef = &StepRef{
							ID:     ns.ID,
							Params: resolveParams(ns.Params, toolVars),
						}
					} else {
						currentRef = nil
					}
				} else {
					// Parallel next steps from tool step — filter by execute_if
					var resolved []StepRef
					for _, ns := range step.NextSteps {
						resolvedCondition := resolveBashExpansions(resolveTemplateString(ns.ExecuteIf, toolVars), s.cwd)
						if evaluateExecuteIf(resolvedCondition, s.cwd) {
							resolved = append(resolved, StepRef{ID: ns.ID, Params: resolveParams(ns.Params, toolVars)})
						}
					}
					if len(resolved) == 0 {
						currentRef = nil
						continue
					}
					contRefs, err := s.executeParallelSteps(ctx, resolved, pf, exec, baseVars, &stepCosts, &logicalStep, workflowStart, cred, parentModel, prompt, executeTool)
					if err != nil {
						s.activePlan = nil
						s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
							WorkflowName: pf.Name, Success: false, StepCosts: stepCosts,
							DurationMs: time.Since(workflowStart).Milliseconds(),
						})
						return err
					}
					if len(contRefs) == 1 {
						if contRefs[0].ID == "stop" {
							stopped = true
							goto done
						}
						currentRef = &contRefs[0]
					} else {
						currentRef = nil
					}
					continue
				}
			} else {
				currentRef = nil
			}

		case "agent":
			var agent *AgentRunner
			var agentLabel string

			if existing, ok := exec.StepAgents[stepID]; ok {
				// Loop-back: reuse existing agent instance
				agent = existing
				agentLabel = stepID + " (resumed)"
			} else if step.Agent != "" {
				config, ok := s.customAgents[step.Agent]
				if !ok {
					s.activePlan = nil
					s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
						WorkflowName: pf.Name,
						Success:      false,
						DurationMs:   time.Since(workflowStart).Milliseconds(),
					})
					return fmt.Errorf("step '%s': agent '%s' not found in custom agents", stepID, step.Agent)
				}
				if step.Effort != "" {
					config.Effort = step.Effort
				}
				ar, err := NewAgentRunner(config, cred, parentModel, s.cwd, s.projectConfig.ToolTimeouts, s.searchDirsSlice()...)
				if err != nil {
					s.activePlan = nil
					s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
						WorkflowName: pf.Name,
						Success:      false,
						DurationMs:   time.Since(workflowStart).Milliseconds(),
					})
					return fmt.Errorf("step '%s': %w", stepID, err)
				}
				agent = ar
				if s.headless {
					agent.Tools = ExcludeTools(agent.Tools, "ask_question_to_user")
				}
				if step.Signal {
					agent.Tools = appendSignalTool(agent.Tools)
				}
				agentLabel = step.Agent
			} else if step.ForkFrom != "" {
				source, ok := exec.StepAgents[step.ForkFrom]
				if !ok {
					s.activePlan = nil
					s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
						WorkflowName: pf.Name,
						Success:      false,
						DurationMs:   time.Since(workflowStart).Milliseconds(),
					})
					return fmt.Errorf("step '%s': fork_from '%s' has no agent instance", stepID, step.ForkFrom)
				}
				ar, err := source.Clone(cred)
				if err != nil {
					s.activePlan = nil
					s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
						WorkflowName: pf.Name,
						Success:      false,
						DurationMs:   time.Since(workflowStart).Milliseconds(),
					})
					return fmt.Errorf("step '%s': %w", stepID, err)
				}
				agent = ar
				if step.Signal {
					agent.Tools = appendSignalTool(agent.Tools)
				}
				agentLabel = stepID + " (from " + step.ForkFrom + ")"
			}
			_ = agentLabel

			// Resolve prompt message
			var resolvedMessage string
			if routedFrom != "" && exec.StepAgents[stepID] != nil {
				// Loop-back: use step params or previous step output
				if len(stepParams) > 0 {
					keys := make([]string, 0, len(stepParams))
					for k := range stepParams {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					var parts []string
					for _, k := range keys {
						if v := stepParams[k]; v != "" {
							parts = append(parts, v)
						}
					}
					if len(parts) > 0 {
						resolvedMessage = strings.Join(parts, "\n")
					} else if prev := exec.StepResults[routedFrom]; prev != nil {
						resolvedMessage = prev.Output
					}
				} else if prev := exec.StepResults[routedFrom]; prev != nil {
					resolvedMessage = prev.Output
				}
				routedFrom = ""
			} else {
				vars := envVars(s.cwd, s.model)
				vars["workflow.prompt"] = prompt
				for k, v := range buildStepVars(exec.StepResults) {
					vars[k] = v
				}
				mergeRuntimeVars(vars, state, pf.Budget)
				for k, v := range stepParams {
					vars[k] = v
				}

				resolvedMessage = resolveBashExpansions(promptloader.GetLoader().Resolve(
					step.Prompt,
					vars,
					s.searchDirs(),
					nil,
				), s.cwd)
				routedFrom = ""
			}

			// Tool executor with deny_tools enforcement
			stepExecuteTool := func(name string, params map[string]any, cwd string) (*ToolResult, error) {
				if name == "workflow_signal" {
					return s.handleWorkflowSignal(pf, state, stepID, params), nil
				}
				if len(step.DenyTools) > 0 {
					for _, t := range step.DenyTools {
						if t == name {
							return &ToolResult{
								Output:  fmt.Sprintf("tool '%s' is denied in step '%s'", name, stepID),
								IsError: true,
							}, nil
						}
					}
				}
				if name == "ask_question_to_user" {
					return s.handleAskQuestionsBatch(s.ctx, params)
				}
				return executeTool(name, params, cwd)
			}

			streamCb := func(delta string) {
				if step.IsStreamVisible() {
					s.emitIfVisible(silent, "event.stream_chunk", protocol.EventStreamChunk{Text: delta})
				}
			}

			tracker := newStepToolTracker()
			baseHooks := s.hooksForStep(silent)
			stepHooks := &TurnHooks{
				OnStreamDelta:   baseHooks.OnStreamDelta,
				OnThinkingDelta: baseHooks.OnThinkingDelta,
				OnStreamDone: func(inputTokens, outputTokens, cacheCreation, cacheRead, elapsedMs int64) {
					// Budget accounting: fires once per LLM call, so every
					// turn inside this step (tool loops, drains, continues)
					// counts toward the run's token budget.
					atomic.AddInt64(&state.Budget.TokensUsed, inputTokens+outputTokens+cacheCreation+cacheRead)
					if baseHooks.OnStreamDone != nil {
						baseHooks.OnStreamDone(inputTokens, outputTokens, cacheCreation, cacheRead, elapsedMs)
					}
				},
				OnToolCall: func(ev protocol.EventToolCall) {
					tracker.RecordCall(ev.Name)
					if baseHooks.OnToolCall != nil {
						baseHooks.OnToolCall(ev)
					}
				},
				OnToolResult: func(toolID, name string, input map[string]any, output string, isError bool) {
					if !isError {
						tracker.RecordResult(name, output)
					}
					if baseHooks.OnToolResult != nil {
						baseHooks.OnToolResult(toolID, name, input, output, isError)
					}
				},
				OnBeforeStream: baseHooks.OnBeforeStream,
			}

			output, err := agent.Send(stepCtx, resolvedMessage, stepExecuteTool, streamCb, s.cwd, stepHooks)

			// If the user enqueued a message during this step, inject it into the agent
			// before advancing to the next step.
			for err == nil {
				userMsg := s.drainWorkflowMsg()
				if userMsg == "" {
					break
				}
				s.emitIfVisible(silent, "event.stream_chunk", protocol.EventStreamChunk{Text: "\n"})
				output, err = agent.Send(stepCtx, userMsg, stepExecuteTool, streamCb, s.cwd, stepHooks)
			}

			// Handle max_tokens: the LLM's response was truncated before it
			// could emit a tool call or finish its text. In interactive mode,
			// ask the user whether to continue; in headless mode,
			// auto-continue — there's no one to ask, and aborting wastes
			// the entire thinking budget that was just spent.
			if errors.Is(err, ErrMaxTokens) {
				shouldContinue := false
				if s.headless {
					LogInfo("[workflow] max_tokens in headless mode — auto-continuing")
					shouldContinue = true
				} else {
					result, askErr := s.handleAskQuestionsBatch(ctx, map[string]any{
						"questions": []any{map[string]any{
							"id":       "continue",
							"category": "Output limit",
							"question": "The AI reached its maximum output length for this step. This can happen with large or complex tasks. Would you like to let it continue from where it stopped?",
							"options":  []any{"Continue", "Stop"},
						}},
					})
					shouldContinue = askErr == nil && result != nil && result.Output == "Continue"
				}
				if shouldContinue {
					output, err = agent.Send(stepCtx, "Continue from where you left off.", stepExecuteTool, streamCb, s.cwd, stepHooks)
				}
			}

			if err != nil {
				stepElapsed := time.Since(stepStart).Milliseconds()
				if !silent {
					stepCosts = append(stepCosts, protocol.StepCost{
						StepID:              stepID,
						Explanation:         step.Explanation,
						Model:               agent.LLM.Model(),
						InputTokens:         agent.LastInputTokens,
						OutputTokens:        agent.LastOutputTokens,
						CacheCreationTokens: agent.LastCacheCreationTokens,
						CacheReadTokens:     agent.LastCacheReadTokens,
						Cost:                protocol.CalculateCost(llm.Spec(agent.LLM), agent.LastInputTokens, agent.LastOutputTokens, agent.LastCacheCreationTokens, agent.LastCacheReadTokens),
						DurationMs:          stepElapsed,
					})
				}
				s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
					StepID:              stepID,
					StepIdx:             logicalStep,
					Success:             false,
					Model:               agent.LLM.Model(),
					InputTokens:         agent.LastInputTokens,
					OutputTokens:        agent.LastOutputTokens,
					CacheCreationTokens: agent.LastCacheCreationTokens,
					CacheReadTokens:     agent.LastCacheReadTokens,
					ToolStats:           tracker.Stats(),
					DurationMs:          stepElapsed,
				})
				// on_error: route to the configured fallback step (once)
				// instead of aborting, so the workflow can wrap up — e.g.
				// summarize progress after a terminal API error. Cancellation
				// still aborts: there is no point steering a dead context.
				if step.OnError != nil && ctx.Err() == nil && !state.ErrorRouted {
					state.ErrorRouted = true
					state.Status = WorkflowStatusBlocked
					exec.StepResults[stepID] = &StepResult{
						Output: fmt.Sprintf("Step '%s' failed: %v", stepID, err),
						Params: stepParams,
					}
					exec.StepAgents[stepID] = agent
					s.emitWorkflowStatus(pf, state, currentRef)
					if step.OnError.ID == "" || step.OnError.ID == "stop" {
						stopped = true
						goto done
					}
					routeVars := make(map[string]string, len(baseVars))
					for k, v := range baseVars {
						routeVars[k] = v
					}
					for k, v := range buildStepVars(exec.StepResults) {
						routeVars[k] = v
					}
					mergeRuntimeVars(routeVars, state, pf.Budget)
					currentRef = &StepRef{ID: step.OnError.ID, Params: resolveParams(step.OnError.Params, routeVars)}
					continue
				}
				s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
					WorkflowName: pf.Name,
					Success:      false,
					StepCosts:    stepCosts,
					DurationMs:   time.Since(workflowStart).Milliseconds(),
				})
				s.activePlan = nil
				return fmt.Errorf("step '%s' failed: %w", stepID, err)
			}

			// Parse JSON if json_output is set
			var parsed map[string]any
			if step.JSONOutput {
				stripped := stripMarkdownFence(output)
				var obj map[string]any
				if err := json.Unmarshal([]byte(stripped), &obj); err == nil {
					parsed = obj
				}
			}

			exec.StepResults[stepID] = &StepResult{
				Output: output,
				Parsed: parsed,
				Params: stepParams,
			}

			displayText := extractStepSummary(output, step.DisplayKey)
			if step.DisplayKey != "" && !step.Silent {
				sf := stripMarkdownFence(output)
				if len(sf) > 200 {
					sf = sf[:200]
				}
				log.Printf("[DEBUG] step=%q display_key=%q output_len=%d stripped_fence=%q display_text=%q",
					stepID, step.DisplayKey, len(output), sf, displayText)
			}
			exec.StepAgents[stepID] = agent

			// Write step output to file if Output path is set
			if step.Output != "" {
				outPath := resolveTemplateString(step.Output, baseVars)
				if !filepath.IsAbs(outPath) {
					outPath = filepath.Join(s.cwd, outPath)
				}
				os.MkdirAll(filepath.Dir(outPath), 0o755)
				os.WriteFile(outPath, []byte(output), 0o644)
			}

			stepElapsed := time.Since(stepStart).Milliseconds()
			if !silent {
				stepCosts = append(stepCosts, protocol.StepCost{
					StepID:              stepID,
					Explanation:         step.Explanation,
					Model:               agent.LLM.Model(),
					InputTokens:         agent.LastInputTokens,
					OutputTokens:        agent.LastOutputTokens,
					CacheCreationTokens: agent.LastCacheCreationTokens,
					CacheReadTokens:     agent.LastCacheReadTokens,
					Cost:                protocol.CalculateCost(llm.Spec(agent.LLM), agent.LastInputTokens, agent.LastOutputTokens, agent.LastCacheCreationTokens, agent.LastCacheReadTokens),
					DurationMs:          stepElapsed,
				})
			}

			s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
				StepID:              stepID,
				StepIdx:             logicalStep,
				Success:             true,
				Display:             displayText,
				Model:               agent.LLM.Model(),
				InputTokens:         agent.LastInputTokens,
				OutputTokens:        agent.LastOutputTokens,
				CacheCreationTokens: agent.LastCacheCreationTokens,
				CacheReadTokens:     agent.LastCacheReadTokens,
				ToolStats:           tracker.Stats(),
				DurationMs:          stepElapsed,
			})

			// Advance to next step(s)
			if len(step.NextSteps) > 0 {
				advanceVars := make(map[string]string, len(baseVars))
				for k, v := range baseVars {
					advanceVars[k] = v
				}
				for k, v := range buildStepVars(exec.StepResults) {
					advanceVars[k] = v
				}
				// Re-merge live vars: a workflow_signal emitted during this
				// step must be visible to the execute_if routing below.
				mergeRuntimeVars(advanceVars, state, pf.Budget)
				if len(step.NextSteps) == 1 {
					ns := step.NextSteps[0]
					resolvedCondition := resolveBashExpansions(resolveTemplateString(ns.ExecuteIf, advanceVars), s.cwd)
					if evaluateExecuteIf(resolvedCondition, s.cwd) {
						currentRef = &StepRef{
							ID:     ns.ID,
							Params: resolveParams(ns.Params, advanceVars),
						}
					} else {
						currentRef = nil
					}
				} else {
					// Parallel next steps — filter by execute_if
					var resolved []StepRef
					for _, ns := range step.NextSteps {
						resolvedCondition := resolveBashExpansions(resolveTemplateString(ns.ExecuteIf, advanceVars), s.cwd)
						if evaluateExecuteIf(resolvedCondition, s.cwd) {
							resolved = append(resolved, StepRef{ID: ns.ID, Params: resolveParams(ns.Params, advanceVars)})
						}
					}
					if len(resolved) == 0 {
						currentRef = nil
					} else {
						contRefs, err := s.executeParallelSteps(ctx, resolved, pf, exec, baseVars, &stepCosts, &logicalStep, workflowStart, cred, parentModel, prompt, executeTool)
						if err != nil {
							s.activePlan = nil
							s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
								WorkflowName: pf.Name, Success: false, StepCosts: stepCosts,
								DurationMs: time.Since(workflowStart).Milliseconds(),
							})
							return err
						}
						if len(contRefs) == 1 {
							if contRefs[0].ID == "stop" {
								stopped = true
								goto done
							}
							currentRef = &contRefs[0]
						} else {
							currentRef = nil
						}
					}
				}
			} else {
				currentRef = nil
			}
		}
	}

done:
	// Normal end of the run: mark finished so the deferred finalizer clears
	// the persisted run state (completed runs are not resumable).
	if state.Status == WorkflowStatusRunning {
		state.Status = WorkflowStatusComplete
	}
	finished = true

	var summary string
	if pf.Summary != "" {
		summaryVars := buildStepVars(exec.StepResults)
		resolved := promptloader.GetLoader().Resolve(
			pf.Summary, summaryVars, s.searchDirs(), nil,
		)
		if !strings.Contains(resolved, "$(") {
			summary = resolved
		}
	}

	s.emit("event.workflow_complete", protocol.EventWorkflowComplete{
		WorkflowName: pf.Name,
		Success:      true,
		Summary:      summary,
		StepCosts:    stepCosts,
		DurationMs:   time.Since(workflowStart).Milliseconds(),
	})

	// Mark plan complete if there's an active plan and workflow wasn't stopped
	if s.activePlan != nil && !stopped {
		for _, t := range s.activePlan.Tasks {
			t.Status = protocol.TaskCompleted
		}
		s.emit("event.plan_complete", protocol.EventPlanComplete{Plan: s.activePlan})
		s.activePlan = nil
	}

	return nil
}
