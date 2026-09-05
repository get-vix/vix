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
	"github.com/get-vix/vix/internal/whiteboard"
	wf "github.com/get-vix/vix/internal/workflow"
)

// ErrMaxTokens is returned when the LLM response was truncated due to the output token limit.
var ErrMaxTokens = errors.New("max_tokens")

// The workflow data model (definitions, steps, budget) and its loader/validator
// live in the standalone internal/workflow package so the daemon, jobs, and
// hooks packages can all share one definition without import cycles. These
// aliases re-expose the moved types under their historical daemon names so the
// large execution engine below compiles unchanged. WorkflowBudget is aliased
// here too (its struct moved out of workflow_state.go).
type (
	InputDef        = wf.InputDef
	StepRef         = wf.StepRef
	WorkflowDef     = wf.Def
	StepOption      = wf.StepOption
	WorkflowStepDef = wf.StepDef
	WorkflowBudget  = wf.Budget
	workflowsFile   = wf.File
)

// LoadWorkflowsFile reads a config/workflow.json file and returns its validated
// workflow list. Thin wrapper over workflow.Load kept under the historical name
// used across the daemon and its tests.
func LoadWorkflowsFile(path string) []*WorkflowDef { return wf.Load(path) }

// validateWorkflow checks that a workflow definition is consistent. Thin
// wrapper over workflow.Validate kept under the historical name used across the
// daemon and its tests.
func validateWorkflow(pf *WorkflowDef) error { return wf.Validate(pf) }

// StepResult holds output from a completed workflow step.
type StepResult struct {
	Output string            `json:"output"`
	Parsed map[string]any    `json:"parsed,omitempty"` // nil if json_output was false, parse failed, or the root wasn't an object
	Value  any               `json:"value,omitempty"`  // full parsed JSON (object OR array) when json_output succeeded; the typed value that crosses edges
	Params map[string]string `json:"params,omitempty"` // input params received by this step
}

// branchResult is one fan_out branch's terminal outcome: the typed value it
// produced (its last step's parsed Value, or raw text), plus any error. fan_in
// joins these per its on_branch_error policy.
type branchResult struct {
	Value  any
	Output string
	Err    error
}

// AgentRunner is a persistent agent with maintained history.
type AgentRunner struct {
	Config   SubagentConfig
	LLM      LLM
	Messages []llm.MessageParam
	System   []llm.SystemBlock
	Tools    []llm.ToolParam
	MaxTurns int

	// ToolTimeouts carries the parent thread's configured tool-call floor/cap
	// so this runner's tool dispatches honour the same settings.json bounds as
	// the main agent. Populated at construction in NewAgentRunner; zero values
	// fall back to package defaults in the dispatcher.
	ToolTimeouts ToolTimeouts

	// plugins is the daemon's plugin source, kept so Clone rebuilds its client
	// with the same request overrides as the original runner.
	plugins PluginSource

	// contextInjected guards the one-time injection (by
	// Thread.ensureWorkflowAgentContext) of the thread's project-context
	// system blocks (CLAUDE.md/AGENTS.md + skills metadata) and the `skill`
	// tool, so a step that calls Send more than once doesn't duplicate them.
	contextInjected bool

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

	// Barriers holds the per-branch outputs collected by a fan_out node,
	// keyed by barrier_id, in element order. The matching fan_in reads it to
	// bind its `as` results list. In-memory only: an interrupted run re-runs
	// the whole fan_out block on resume (atomic-block semantics), and fan_out
	// also persists the joined list as a StepResult so a resume landing on the
	// fan_in can still recover it.
	Barriers map[string][]branchResult

	// transcript accumulates the user-visible output of agent steps in
	// execution order so it can be mirrored into the thread's chat transcript
	// (s.messages) when the run finalizes — letting a finished run replay and a
	// follow-up chat turn pick up with real context. Guarded by transcriptMu
	// because parallel steps append concurrently. retryNotices accumulates the
	// transient-error retry notices (overload, rate limit, …) seen during the
	// run, also under transcriptMu, so a reopened run replays the same retry
	// lines an interactive run shows live.
	transcriptMu sync.Mutex
	transcript   []workflowTranscriptEntry
	retryNotices []workflowRetryNotice
}

// workflowRetryNotice is one transient-error retry that happened during an
// agent step, captured so the chat transcript can replay it like interactive.
type workflowRetryNotice struct {
	StepID     string
	Reason     string
	Attempt    int
	MaxRetries int
	WaitSecs   int
}

// workflowTranscriptEntry is one visible agent step's output captured for the
// chat transcript.
type workflowTranscriptEntry struct {
	StepID      string
	Explanation string
	Output      string
}

// recordTranscriptEntry captures a visible agent step's output for later mirror
// into the thread transcript. No-op for empty output.
func (r *WorkflowRun) recordTranscriptEntry(step WorkflowStepDef, stepID, output string) {
	if step.Type != "agent" || step.Silent || !step.IsStreamVisible() {
		return
	}
	if strings.TrimSpace(output) == "" {
		return
	}
	r.transcriptMu.Lock()
	r.transcript = append(r.transcript, workflowTranscriptEntry{
		StepID:      stepID,
		Explanation: step.Explanation,
		Output:      output,
	})
	r.transcriptMu.Unlock()
}

// recordFailedAgentStep captures a failed agent step so its partial working
// history (the resolved prompt and any tool_use/tool_result turns it produced
// before failing) is mirrored into the chat transcript — making a failed run
// replay just like a successful one. Unlike recordTranscriptEntry it does not
// gate on visibility or non-empty output: a run that aborted mid-step still
// produced a conversation the user should see.
func (r *WorkflowRun) recordFailedAgentStep(step WorkflowStepDef, stepID string) {
	if step.Type != "agent" {
		return
	}
	r.transcriptMu.Lock()
	r.transcript = append(r.transcript, workflowTranscriptEntry{
		StepID:      stepID,
		Explanation: step.Explanation,
	})
	r.transcriptMu.Unlock()
}

// recordRetry captures one transient-error retry attempt for later replay.
func (r *WorkflowRun) recordRetry(stepID, reason string, attempt, maxRetries, waitSecs int) {
	r.transcriptMu.Lock()
	r.retryNotices = append(r.retryNotices, workflowRetryNotice{
		StepID:     stepID,
		Reason:     reason,
		Attempt:    attempt,
		MaxRetries: maxRetries,
		WaitSecs:   waitSecs,
	})
	r.transcriptMu.Unlock()
}

// snapshotRetryNotices returns a copy of the accumulated retry notices.
func (r *WorkflowRun) snapshotRetryNotices() []workflowRetryNotice {
	r.transcriptMu.Lock()
	defer r.transcriptMu.Unlock()
	return append([]workflowRetryNotice(nil), r.retryNotices...)
}

// snapshotTranscript returns a copy of the accumulated transcript entries.
func (r *WorkflowRun) snapshotTranscript() []workflowTranscriptEntry {
	r.transcriptMu.Lock()
	defer r.transcriptMu.Unlock()
	return append([]workflowTranscriptEntry(nil), r.transcript...)
}

// appendWorkflowTranscript mirrors a run's agent output into the chat transcript
// (s.messages) so a finished workflow replays in a freshly attached TUI and a
// follow-up chat message picks up with real context. For each visible agent step
// (in execution order, deduplicated by step id) it splices the step agent's FULL
// working history — the resolved prompt, every tool_use and tool_result, and the
// final text — so the persisted conversation reflects what the agent actually
// did. A follow-up turn is then grounded in those real tool calls rather than a
// lossy summary. Thinking blocks are dropped (their signatures can't be
// revalidated once re-sent under the thread's own system prompt). A visible
// agent step without an agent instance falls back to a user(anchor)→assistant
// (text) pair. No-op when nothing visible was produced.
func (s *Thread) appendWorkflowTranscript(anchor string, exec *WorkflowRun) {
	entries := exec.snapshotTranscript()
	notices := exec.snapshotRetryNotices()
	if len(entries) == 0 && len(notices) == 0 {
		return
	}
	if strings.TrimSpace(anchor) == "" {
		anchor = "Workflow run"
	}
	var msgs []llm.MessageParam
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if seen[e.StepID] {
			continue
		}
		seen[e.StepID] = true
		if agent := exec.StepAgents[e.StepID]; agent != nil && len(agent.Messages) > 0 {
			msgs = append(msgs, stripThinkingMessages(agent.Messages)...)
			continue
		}
		// No agent instance: keep the step's text behind a kickoff anchor so
		// nothing is lost. A failed step that produced no output yet (recorded
		// via recordFailedAgentStep) contributes nothing here.
		if strings.TrimSpace(e.Output) == "" {
			continue
		}
		msgs = append(msgs,
			llm.NewUserMessage(llm.NewTextBlock(anchor)),
			llm.NewAssistantMessage(llm.NewTextBlock(strings.TrimRight(e.Output, "\n"))),
		)
	}
	if len(msgs) == 0 && len(notices) == 0 {
		return
	}
	if len(msgs) > 0 {
		msgs = coalesceRoles(msgs)
	}
	s.mu.Lock()
	if len(msgs) > 0 {
		s.appendMessages(msgs...)
	}
	if len(notices) > 0 {
		// Anchor every notice to the end of the transcript: a failed run's
		// retries pile up after the agent's partial work, matching what an
		// interactive run shows just before it gives up. AfterIdx == -1 when
		// no messages exist at all (rendered before everything on replay).
		afterIdx := len(s.messages) - 1
		for _, n := range notices {
			s.retryNotices = append(s.retryNotices, retryNoticeRecord{
				AfterIdx:   afterIdx,
				Reason:     n.Reason,
				Attempt:    n.Attempt,
				MaxRetries: n.MaxRetries,
				WaitSecs:   n.WaitSecs,
			})
		}
	}
	s.mu.Unlock()
}

// stripThinkingMessages copies msgs, dropping BlockThinking blocks. Messages
// left with no content after the strip are omitted so the result stays
// well-formed. Inputs are not mutated.
func stripThinkingMessages(msgs []llm.MessageParam) []llm.MessageParam {
	out := make([]llm.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		blocks := make([]llm.ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			if b.Type == llm.BlockThinking {
				continue
			}
			blocks = append(blocks, b)
		}
		if len(blocks) == 0 {
			continue
		}
		cp := m
		cp.Content = blocks
		out = append(out, cp)
	}
	return out
}

// coalesceRoles merges consecutive messages that share a role into one, keeping
// user/assistant alternation valid when several step histories are concatenated
// back-to-back (e.g. one step ending on a tool_result user turn followed by the
// next step's user prompt). Block order is preserved; content is copied so the
// inputs are not mutated.
func coalesceRoles(msgs []llm.MessageParam) []llm.MessageParam {
	out := make([]llm.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			merged := append([]llm.ContentBlock(nil), out[n-1].Content...)
			out[n-1].Content = append(merged, m.Content...)
			if out[n-1].Timestamp.IsZero() {
				out[n-1].Timestamp = m.Timestamp
			}
			continue
		}
		out = append(out, m)
	}
	return out
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
// dispatcher in thread.go.
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
// the /compact command in thread.go.
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
	SkillsDir          string                `json:"skills_dir,omitempty"`
	SkillsDirs         []string              `json:"skills_dirs,omitempty"`
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
	// DenyPathsRel holds the raw (tilde-expanded) relative deny_list.paths
	// entries, preserved so the thread can additionally resolve them against
	// the working directory. See the resolution loop in LoadProjectConfig and
	// the seeding logic in Thread for why both interpretations are unioned.
	DenyPathsRel     []string
	DenyURLs         []string
	SkillsDirs       []string
	Features         map[string]bool
	ToolTimeouts     ToolTimeouts
	BashStepTimeouts BashStepTimeouts
	Compaction       Compaction
	MCPServers       []mcp.ServerConfig
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

// expandTildePath expands a leading "~" (bare, or "~/…") to the user's home
// directory. Other forms (including "~user") are returned unchanged. When the
// home directory can't be determined the original string is returned so the
// entry still resolves as a relative path rather than being silently dropped.
func expandTildePath(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// appendUniqueStr appends v to list only if it is not already present.
func appendUniqueStr(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
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
		// Merge deny list paths (union from all config files). A leading `~`
		// is expanded to the user's home directory. Absolute entries are used
		// verbatim. Relative entries are resolved against the config file's
		// directory (matching the AllowedDirectories convention above) AND
		// recorded raw in DenyPathsRel so the thread can additionally resolve
		// them against the working directory. The dual resolution fixes the
		// footgun where a `deny_list.paths` entry in `./.vix/settings.json`
		// (e.g. ".envrc.private") was silently anchored under `.vix/` and never
		// matched the file it was meant to protect at the project root.
		for _, entry := range cfg.DenyList.Paths {
			expanded := expandTildePath(entry)
			if filepath.IsAbs(expanded) {
				result.DenyPaths = appendUniqueStr(result.DenyPaths, filepath.Clean(expanded))
				continue
			}
			result.DenyPaths = appendUniqueStr(result.DenyPaths,
				filepath.Clean(filepath.Join(filepath.Dir(configPath), expanded)))
			result.DenyPathsRel = appendUniqueStr(result.DenyPathsRel, expanded)
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
		// Merge custom skills directories (union from all config files). A
		// leading `~` is expanded to the user's home directory. Absolute
		// entries are used verbatim. Relative entries are resolved against
		// the config file's directory.
		var rawSkillsDirs []string
		if cfg.SkillsDir != "" {
			rawSkillsDirs = append(rawSkillsDirs, cfg.SkillsDir)
		}
		rawSkillsDirs = append(rawSkillsDirs, cfg.SkillsDirs...)
		for _, entry := range rawSkillsDirs {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			expanded := expandTildePath(entry)
			if filepath.IsAbs(expanded) {
				result.SkillsDirs = appendUniqueStr(result.SkillsDirs, filepath.Clean(expanded))
				continue
			}
			result.SkillsDirs = appendUniqueStr(result.SkillsDirs,
				filepath.Clean(filepath.Join(filepath.Dir(configPath), expanded)))
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
// toolTimeouts carries the parent thread's tool_timeouts bounds so the
// runner's tool dispatches honour the same settings.json floor/cap.
func NewAgentRunner(config SubagentConfig, cred config.Credential, parentModel, cwd string, plugins PluginSource, toolTimeouts ToolTimeouts, searchDirs ...string) (*AgentRunner, error) {
	maxTurns := config.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 20
	}

	client, model, err := buildRunnerClient(config.Model, config.Effort, parentModel, plugins, int64(config.MaxTokens))
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
		plugins:      plugins,
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
	clonedClient, err := llm.NewFromModel(cloneSpec, a.plugins, a.LLM.Effort(), a.LLM.MaxTokens())
	if err != nil {
		return nil, fmt.Errorf("cannot clone agent runner: %w", err)
	}

	return &AgentRunner{
		Config:          a.Config,
		LLM:             clonedClient,
		Messages:        msgs,
		System:          sys,
		Tools:           tools,
		MaxTurns:        a.MaxTurns,
		ToolTimeouts:    a.ToolTimeouts,
		plugins:         a.plugins,
		contextInjected: a.contextInjected,
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
// projectToString renders a typed value into the string world used by bash
// execute_if/condition and template interpolation: strings pass through
// unchanged; everything else (numbers, bools, lists, objects) is JSON-encoded.
// This is the one boundary rule between the typed value store and the two
// string-only surfaces.
func projectToString(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	default:
		if b, err := json.MarshalIndent(v, "", "  "); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}

// flattenTypedInto walks a typed value and writes its string projection under
// prefix, recursing into objects so nested fields resolve as $(prefix.key)
// (and further, $(prefix.key.subkey)). The prefix itself always gets the
// projection of the whole value. Lists are projected as JSON at their key (no
// per-index flattening — indexed access is a typed-pool concern).
func flattenTypedInto(vars map[string]string, prefix string, v any) {
	vars[prefix] = projectToString(v)
	if obj, ok := v.(map[string]any); ok {
		for k, sub := range obj {
			flattenTypedInto(vars, prefix+"."+k, sub)
		}
	}
}

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
				flattenTypedInto(vars, "step."+sid+"."+k, v)
			}
		}
	}
	return vars
}

// buildTypedStepVars mirrors buildStepVars but preserves typed values, so
// consumers that need real lists/objects (e.g. a fan_out node's `over`
// reference) can retrieve them without a JSON round-trip. Keys match the string
// pool: step.<id> is the raw text output, step.<id> is overridden by the parsed
// Value when json_output produced one, and step.<id>.<field> exposes each
// top-level parsed field typed.
func buildTypedStepVars(results map[string]*StepResult) map[string]any {
	vars := make(map[string]any)
	for sid, r := range results {
		if r.Value != nil {
			vars["step."+sid] = r.Value
		} else {
			vars["step."+sid] = r.Output
		}
		for k, v := range r.Params {
			vars["step."+sid+"."+k] = v
		}
		if r.Parsed != nil {
			for k, v := range r.Parsed {
				vars["step."+sid+"."+k] = v
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
		c.Env = sanitizedBashEnv()
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
	c.Env = sanitizedBashEnv()
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
func (s *Thread) executeToolStep(ctx context.Context, step WorkflowStepDef, baseVars map[string]string) (nextRefs []StepRef, output string, err error) {
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

		cmd, ok := s.waitForCommand(ctx, "thread.user_answer")
		if !ok {
			return nil, "", ctx.Err()
		}

		var answerData protocol.ThreadUserAnswerData
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

	case "whiteboard_open":
		return s.openPlanWhiteboard(baseVars)

	default:
		result := s.executeToolConfirmed(ctx, step.Tool, map[string]any{})
		if result.IsError {
			return nil, "", fmt.Errorf("tool '%s' failed: %s", step.Tool, result.Output)
		}
		return nil, result.Output, nil
	}
}

// openPlanWhiteboard converts the plan workflow's authored mermaid scenes
// (written to .vix/whiteboards/<thread>.json by the generate step) into
// positioned canvas scenes, builds the per-thread whiteboard URL (scenes + the
// plan text + voice agent), and opens it in the browser. It replaces the old
// python-based open step; layout now happens in Go.
func (s *Thread) openPlanWhiteboard(vars map[string]string) (nextRefs []StepRef, output string, err error) {
	path := filepath.Join(s.cwd, ".vix", "whiteboards", s.id+".json")
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, "", fmt.Errorf("read whiteboard scenes: %w", readErr)
	}

	base := whiteboard.WhiteboardBase(s.server.webPort)
	if base == "" {
		base = "http://localhost:1337"
	}

	url, buildErr := planWhiteboardURL(base, s.id, vars["plan"], data)
	if buildErr != nil {
		return nil, "", buildErr
	}

	openBrowser(url)
	return nil, url, nil
}

// planWhiteboardURL converts the authored mermaid scenes JSON (data) into
// positioned canvas scenes and builds the per-thread whiteboard URL carrying the
// scenes, the plan text, and the voice agent. Pure (no I/O) so it is unit
// testable.
func planWhiteboardURL(base, threadID, plan string, data []byte) (string, error) {
	// Voice agent used by the whiteboard walkthrough (matches the historical
	// pinned agent for the plan experience).
	const planAgentID = "agent_1201krde0b6jebpvqth0zxpcdqss"

	// The generate step emits a JSON array of {name, context, mermaid}. Tolerate
	// a leading/trailing markdown fence in case the model wrapped its output.
	raw := strings.TrimSpace(string(data))
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
		raw = strings.TrimSpace(raw)
	}
	var items []whiteboard.MermaidScene
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return "", fmt.Errorf("parse whiteboard scenes: %w", err)
	}

	scenesQ, err := whiteboard.CompressScenes(whiteboard.ScenesFromMermaid(items))
	if err != nil {
		return "", fmt.Errorf("encode scenes: %w", err)
	}

	return fmt.Sprintf("%s/thread/%s/whiteboard?scenes_z=%s&plan_z=%s&agent_id=%s",
		base, threadID, scenesQ, whiteboard.CompressText(plan), planAgentID), nil
}

// resolveOverList resolves a fan_out's `over` expression to a typed list. The
// expression is a single $(name) reference; the name is looked up first in the
// run's typed var pool (e.g. a prior fan_in's `as` binding), then in the typed
// step-result pool. A JSON-array string also resolves (best effort), so a
// bash/agent step that printed a JSON array works too.
func resolveOverList(over string, typedVars map[string]any, results map[string]*StepResult) ([]any, error) {
	key := strings.TrimSpace(over)
	if strings.HasPrefix(key, "$(") && strings.HasSuffix(key, ")") {
		key = key[2 : len(key)-1]
	}
	var v any
	if tv, ok := typedVars[key]; ok {
		v = tv
	} else if tv, ok := buildTypedStepVars(results)[key]; ok {
		v = tv
	} else {
		return nil, fmt.Errorf("over reference %q resolved to nothing", over)
	}
	switch val := v.(type) {
	case []any:
		return val, nil
	case string:
		var list []any
		if err := json.Unmarshal([]byte(strings.TrimSpace(val)), &list); err == nil {
			return list, nil
		}
		return nil, fmt.Errorf("over reference %q is a string that is not a JSON array", over)
	default:
		return nil, fmt.Errorf("over reference %q is not a list (got %T)", over, v)
	}
}

// firstPassingNext returns the first next_step whose execute_if passes (empty =
// always), or nil when none match. Used for sequential routing inside a branch.
func firstPassingNext(next []StepRef, vars map[string]string, cwd string) *StepRef {
	for _, ns := range next {
		cond := resolveBashExpansions(resolveTemplateString(ns.ExecuteIf, vars), cwd)
		if evaluateExecuteIf(cond, cwd) {
			return &StepRef{ID: ns.ID, Params: ns.Params}
		}
	}
	return nil
}

// branchValue returns the typed value a branch contributes to a fan_in join:
// its terminal step's parsed Value when present, else the raw text output.
func branchValue(r branchResult) any {
	if r.Value != nil {
		return r.Value
	}
	return r.Output
}

// findFanIn returns the fan_in step (id + def) that joins the given barrier, or
// ("", nil) when none exists. Validation guarantees at most one.
func findFanIn(pf *WorkflowDef, barrierID string) (string, *WorkflowStepDef) {
	for id, step := range pf.Steps {
		if step.Type == "fan_in" && step.BarrierID == barrierID {
			s := step
			return id, &s
		}
	}
	return "", nil
}

// branchCtx carries the shared, read-only context a fan_out branch needs.
type branchCtx struct {
	pf            *WorkflowDef
	exec          *WorkflowRun
	cred          config.Credential
	parentModel   string
	prompt        string
	executeTool   func(name string, params map[string]any, cwd string) (*ToolResult, error)
	baseVars      map[string]string      // snapshot: envVars + workflow.* + runtime accounting
	globalResults map[string]*StepResult // read-only snapshot of upstream step results
	itemName      string                 // the fan_out `as` binding name
	logicalStep   *int
	mu            *sync.Mutex // guards logicalStep and cross-branch reads of exec.StepAgents
}

// runBranchChain executes one fan_out branch: a sequential chain starting at
// entry with the fan_out element bound as $(<itemName>). It supports bash,
// agent, and if steps and follows single (execute_if-filtered) next_steps, so a
// branch can itself decide to run more steps — the per-branch pipeline. The
// terminal step's typed value (or text) becomes the branch result. All step
// results are branch-local, so concurrent branches never collide. idx is the
// element index, used only to disambiguate step events in the UI.
func (s *Thread) runBranchChain(ctx context.Context, bc *branchCtx, entry *StepRef, item any, idx int) branchResult {
	local := map[string]*StepResult{}
	localAgents := map[string]*AgentRunner{}
	cur := &StepRef{ID: entry.ID, Params: entry.Params}
	var last branchResult

	for guard := 0; cur != nil && cur.ID != "" && cur.ID != "stop" && guard < 200; guard++ {
		if ctx.Err() != nil {
			return branchResult{Err: ctx.Err()}
		}
		bstep := bc.pf.Steps[cur.ID]
		bstepID := cur.ID

		vars := make(map[string]string, len(bc.baseVars)+8)
		for k, v := range bc.baseVars {
			vars[k] = v
		}
		for k, v := range buildStepVars(bc.globalResults) {
			vars[k] = v
		}
		for k, v := range buildStepVars(local) {
			vars[k] = v
		}
		flattenTypedInto(vars, bc.itemName, item)
		stepParams := resolveParams(cur.Params, vars)
		for k, v := range stepParams {
			vars[k] = v
		}

		bc.mu.Lock()
		*bc.logicalStep++
		myStep := *bc.logicalStep
		bc.mu.Unlock()

		silent := bstep.Silent
		stepCtx := ctx
		if silent {
			stepCtx = withSilentCtx(ctx)
		}
		label := fmt.Sprintf("%s[%d]", bstepID, idx)

		switch bstep.Type {
		case "if":
			cond := resolveBashExpansions(resolveTemplateString(bstep.Condition, vars), s.cwd)
			nb := bstep.Else
			if evaluateExecuteIf(cond, s.cwd) {
				nb = bstep.Then
			}
			if nb == nil || nb.ID == "" || nb.ID == "stop" {
				cur = nil
			} else {
				cur = &StepRef{ID: nb.ID, Params: nb.Params}
			}
			continue

		case "bash":
			s.emitIfVisible(silent, "event.workflow_step_start", protocol.EventWorkflowStepStart{StepID: label, StepIdx: myStep, Explanation: bstep.Explanation})
			resolvedCmd := resolveBashExpansions(resolveTemplateString(bstep.Command, vars), s.cwd)
			resolvedInput := resolveBashExpansions(resolveTemplateString(bstep.Input, vars), s.cwd)
			bashTimeout := resolveBashStepTimeout(bstep.TimeoutSec, s.projectConfig.BashStepTimeouts)
			bctx, bcancel := context.WithTimeout(stepCtx, bashTimeout)
			outputStr, err := runBashWithContext(bctx, resolvedCmd, s.cwd, resolvedInput, func(line string) {
				s.emitIfVisible(silent, "event.stream_chunk", protocol.EventStreamChunk{Text: line + "\n"})
			})
			bcancel()
			local[bstepID] = &StepResult{Output: outputStr, Params: stepParams}
			last = branchResult{Value: outputStr, Output: outputStr}
			if err != nil {
				s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{StepID: label, StepIdx: myStep, Success: false, Command: resolvedCmd, BashOutput: bashOutputPreview(outputStr, 5)})
				return branchResult{Err: fmt.Errorf("branch step '%s' bash failed: %w", bstepID, err)}
			}
			s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{StepID: label, StepIdx: myStep, Success: true, Command: resolvedCmd, BashOutput: bashOutputPreview(outputStr, 5)})

		case "agent":
			var agent *AgentRunner
			if bstep.Agent != "" {
				cfg, ok := s.customAgents[bstep.Agent]
				if !ok {
					return branchResult{Err: fmt.Errorf("branch step '%s': agent '%s' not found", bstepID, bstep.Agent)}
				}
				if bstep.Effort != "" {
					cfg.Effort = bstep.Effort
				}
				ar, err := NewAgentRunner(cfg, bc.cred, bc.parentModel, s.cwd, s.server.plugins, s.projectConfig.ToolTimeouts, s.searchDirsSlice()...)
				if err != nil {
					return branchResult{Err: fmt.Errorf("branch step '%s': %w", bstepID, err)}
				}
				agent = ar
			} else if bstep.ForkFrom != "" {
				bc.mu.Lock()
				src, ok := localAgents[bstep.ForkFrom]
				if !ok {
					src, ok = bc.exec.StepAgents[bstep.ForkFrom]
				}
				bc.mu.Unlock()
				if !ok {
					return branchResult{Err: fmt.Errorf("branch step '%s': fork_from '%s' has no agent instance", bstepID, bstep.ForkFrom)}
				}
				ar, err := src.Clone(bc.cred)
				if err != nil {
					return branchResult{Err: fmt.Errorf("branch step '%s': %w", bstepID, err)}
				}
				agent = ar
			} else {
				return branchResult{Err: fmt.Errorf("branch step '%s': must have 'agent' or 'fork_from'", bstepID)}
			}
			if s.headless {
				agent.Tools = ExcludeTools(agent.Tools, "ask_question_to_user")
			}
			if bstep.Signal {
				agent.Tools = appendSignalTool(agent.Tools)
			}

			resolvedMessage := resolveBashExpansions(promptloader.GetLoader().Resolve(bstep.Prompt, vars, s.searchDirs(), nil), s.cwd)
			streamCb := func(delta string) {
				if bstep.IsStreamVisible() {
					s.emitIfVisible(silent, "event.stream_chunk", protocol.EventStreamChunk{Text: delta})
				}
			}
			stepExecuteTool := func(name string, params map[string]any, cwd string) (*ToolResult, error) {
				if name == "workflow_signal" {
					return s.handleWorkflowSignal(bc.pf, bc.exec.State, bstepID, params), nil
				}
				for _, t := range bstep.DenyTools {
					if t == name {
						return &ToolResult{Output: fmt.Sprintf("tool '%s' is denied in step '%s'", name, bstepID), IsError: true}, nil
					}
				}
				return bc.executeTool(name, params, cwd)
			}
			stepHooks := s.hooksForStep(silent)
			if bc.exec.State != nil {
				base := stepHooks.OnStreamDone
				stepHooks.OnStreamDone = func(in, out, cc, cr, el int64) {
					atomic.AddInt64(&bc.exec.State.Budget.TokensUsed, in+out+cc+cr)
					if base != nil {
						base(in, out, cc, cr, el)
					}
				}
			}
			baseOnRetry := stepHooks.OnRetry
			stepHooks.OnRetry = func(attempt, maxRetries, waitSecs int, reason string) {
				bc.exec.recordRetry(bstepID, reason, attempt, maxRetries, waitSecs)
				if baseOnRetry != nil {
					baseOnRetry(attempt, maxRetries, waitSecs, reason)
				}
			}

			s.emitIfVisible(silent, "event.workflow_step_start", protocol.EventWorkflowStepStart{StepID: label, StepIdx: myStep, Explanation: bstep.Explanation})
			s.ensureWorkflowAgentContext(agent)
			output, err := agent.Send(stepCtx, resolvedMessage, stepExecuteTool, streamCb, s.cwd, stepHooks)
			if err != nil {
				s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{StepID: label, StepIdx: myStep, Success: false})
				return branchResult{Err: fmt.Errorf("branch step '%s' failed: %w", bstepID, err)}
			}

			var parsedValue any
			if bstep.JSONOutput {
				stripped := stripMarkdownFence(output)
				var v any
				if json.Unmarshal([]byte(stripped), &v) == nil {
					parsedValue = v
				}
			}
			local[bstepID] = &StepResult{Output: output, Value: parsedValue, Params: stepParams}
			bc.mu.Lock()
			localAgents[bstepID] = agent
			bc.mu.Unlock()
			tv := any(output)
			if parsedValue != nil {
				tv = parsedValue
			}
			last = branchResult{Value: tv, Output: output}
			bc.exec.recordTranscriptEntry(bstep, bstepID, output)
			s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{StepID: label, StepIdx: myStep, Success: true})

		default:
			return branchResult{Err: fmt.Errorf("branch step '%s': type %q is not allowed inside a fan_out branch", bstepID, bstep.Type)}
		}

		cur = firstPassingNext(bstep.NextSteps, vars, s.cwd)
	}
	return last
}

// executeParallelSteps launches multiple steps in parallel goroutines.
// It returns the continuation refs chosen by any tool step (e.g. ask_question_to_user),
// so the caller can follow the user's routing decision after the parallel block completes.
func (s *Thread) executeParallelSteps(
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
				// Our deadline fired vs. the whole thread being cancelled:
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
					ar, err := NewAgentRunner(config, cred, parentModel, s.cwd, s.server.plugins, s.projectConfig.ToolTimeouts, s.searchDirsSlice()...)
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
				vars["workflow.dir"] = s.jobDir
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
				baseOnRetry := stepHooks.OnRetry
				stepHooks.OnRetry = func(attempt, maxRetries, waitSecs int, reason string) {
					exec.recordRetry(stepID, reason, attempt, maxRetries, waitSecs)
					if baseOnRetry != nil {
						baseOnRetry(attempt, maxRetries, waitSecs, reason)
					}
				}

				s.ensureWorkflowAgentContext(agent)
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
					// Keep the failed step's partial history in the transcript
					// (StepAgents was set above) so a failed run replays like a
					// successful one.
					exec.recordFailedAgentStep(step, stepID)
					errs[idx] = fmt.Errorf("step '%s' failed: %w", stepID, err)
					return
				}
				s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{
					StepID: stepID, StepIdx: myLogicalStep, Success: true, DurationMs: stepElapsed,
				})
				exec.recordTranscriptEntry(step, stepID, output)

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
func (s *Thread) executeWorkflow(ctx context.Context, pf *WorkflowDef, prompt string, resume *WorkflowRunState) error {
	exec := &WorkflowRun{
		Def:         pf,
		StepAgents:  make(map[string]*AgentRunner),
		StepResults: make(map[string]*StepResult),
		Barriers:    make(map[string][]branchResult),
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
			ar, err := NewAgentRunner(snap.Config, cred, parentModel, s.cwd, s.server.plugins, s.projectConfig.ToolTimeouts, s.searchDirsSlice()...)
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
	// workflow.dir is the run's job directory (~/.vix/jobs/<id>) for scheduled
	// runs, empty otherwise. Always present so the token never leaks unresolved.
	baseVars["workflow.dir"] = s.jobDir
	baseVars["thread.id"] = s.id
	// Legacy alias: workflows authored before the sessions->threads rename
	// reference $(session.id). Keep it resolving so they don't silently break.
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

	// typedVars carries typed values (lists/objects) bound by fan_in `as`
	// results across steps, so a later fan_out can iterate them. String
	// consumers read the projected form from baseVars under the same name.
	typedVars := map[string]any{}
	// Rehydrate typedVars/baseVars from any persisted fan_in results on resume,
	// so a run resumed after a fan_in still resolves $(<as>) for its downstream
	// steps (fan_out persists the joined list under the fan_in's step id).
	for id, st := range pf.Steps {
		if st.Type == "fan_in" {
			if res, ok := exec.StepResults[id]; ok && res.Value != nil {
				typedVars[st.As] = res.Value
				baseVars[st.As] = projectToString(res.Value)
			}
		}
	}
	var fanoutMu sync.Mutex

	// Hard iteration cap. The configured budget governs the real limit (and
	// routes to on_exceeded below); this is a safety net against runaway
	// loops, widened when a budget legitimately allows more iterations. The
	// headroom is doubled + 50 because invisible control-flow nodes (if) run in
	// this loop but don't advance the iteration budget, so a budget of N real
	// iterations can involve up to ~2N loop passes once routing is expressed as
	// if nodes.
	maxIterations := 200
	if pf.Budget != nil && pf.Budget.MaxIterations > 0 && pf.Budget.MaxIterations*2+50 > maxIterations {
		maxIterations = pf.Budget.MaxIterations*2 + 50
	}

	// Finalize on every exit path: completed runs clear their persisted state;
	// interrupted runs (cancel/crash) park as paused, failed ones as blocked —
	// both resumable from the cursor. Either way the run produced content the
	// user may not have seen: flag the thread unread.
	finished := false
	defer func() {
		s.unread = true
		// Mirror the run's visible agent output into the chat transcript before
		// persisting, on every exit path (completed, paused, blocked, timed
		// out) so the run replays in a fresh TUI and a follow-up chat turn has
		// the output as context. No-op when nothing visible was produced.
		s.appendWorkflowTranscript(prompt, exec)
		if finished {
			s.setWorkflowRunState(nil)
			// A finished inline (transient) workflow run — e.g. a scheduled job
			// — has nothing left to resume, and its definition was never
			// persisted. Leaving the thread in "workflow" mode would make a
			// later reopen warn that the workflow "no longer exists" and switch
			// to chat. Drop straight to chat mode here so that never happens.
			if s.isInlineWorkflow(pf.Name) {
				s.threadMode = "chat"
				s.activeWorkflow = ""
			}
			s.persist()
			return
		}
		if ctx.Err() != nil {
			// Cancelled (user escape / daemon shutdown): keep the run resumable.
			state.Status = WorkflowStatusPaused
		} else if s.isInlineWorkflow(pf.Name) {
			// Terminal failure of an inline (transient) job/hook workflow: its
			// definition was never persisted, so there is nothing to resume
			// against. Drop straight to chat — like the finished case — so a
			// later reopen replays the transcript (agent work + retry notices)
			// instead of warning the workflow "no longer exists".
			s.setWorkflowRunState(nil)
			s.threadMode = "chat"
			s.activeWorkflow = ""
			s.persist()
			return
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

		// Control-flow nodes (if) are invisible, zero-cost routing: they don't
		// advance the iteration budget, don't consume a logical step index, and
		// emit no step events. This keeps a workflow's real-work accounting (and
		// the Goal loop's max_iterations budget) unchanged when routing is
		// expressed as if nodes instead of execute_if edges.
		isControlNode := step.Type == "if"

		// Each workflow_signal is only visible to the routing decisions that
		// follow it: starting a new signal-capable step clears the previous one.
		if step.Signal {
			state.Signal = SignalState{}
		}
		if !isControlNode {
			logicalStep++
			state.Iteration++
		}

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

		if !isControlNode {
			s.emitIfVisible(silent, "event.workflow_step_start", protocol.EventWorkflowStepStart{
				StepID:      stepID,
				StepIdx:     logicalStep,
				Total:       0,
				Explanation: step.Explanation,
			})
		}

		stepStart := time.Now()

		switch step.Type {
		case "if":
			// Invisible control-flow node: evaluate the condition and route to
			// exactly one edge. No step events, no cost — see isControlNode.
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

			resolvedCondition := resolveBashExpansions(resolveTemplateString(step.Condition, vars), s.cwd)
			branch := step.Else
			if evaluateExecuteIf(resolvedCondition, s.cwd) {
				branch = step.Then
			}

			if branch == nil || branch.ID == "" {
				currentRef = nil
			} else if branch.ID == "stop" {
				stopped = true
				goto done
			} else {
				currentRef = &StepRef{ID: branch.ID, Params: resolveParams(branch.Params, vars)}
			}

		case "fan_out":
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

			list, listErr := resolveOverList(step.Over, typedVars, exec.StepResults)
			if listErr != nil {
				s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{StepID: stepID, StepIdx: logicalStep, Success: false, DurationMs: time.Since(stepStart).Milliseconds()})
				s.emit("event.workflow_complete", protocol.EventWorkflowComplete{WorkflowName: pf.Name, Success: false, StepCosts: stepCosts, DurationMs: time.Since(workflowStart).Milliseconds()})
				s.activePlan = nil
				return fmt.Errorf("step '%s' fan_out: %w", stepID, listErr)
			}

			maxPar := step.MaxParallel
			if maxPar <= 0 {
				maxPar = runtime.GOMAXPROCS(0)
			}
			if maxPar < 1 {
				maxPar = 1
			}

			baseSnap := make(map[string]string, len(baseVars))
			for k, v := range baseVars {
				baseSnap[k] = v
			}
			globalSnap := make(map[string]*StepResult, len(exec.StepResults))
			for k, v := range exec.StepResults {
				globalSnap[k] = v
			}
			bc := &branchCtx{
				pf: pf, exec: exec, cred: cred, parentModel: parentModel, prompt: prompt,
				executeTool: executeTool, baseVars: baseSnap, globalResults: globalSnap,
				itemName: step.As, logicalStep: &logicalStep, mu: &fanoutMu,
			}

			results := make([]branchResult, len(list))
			sem := make(chan struct{}, maxPar)
			var bwg sync.WaitGroup
			for i := range list {
				bwg.Add(1)
				sem <- struct{}{}
				go func(idx int, item any) {
					defer bwg.Done()
					defer func() { <-sem }()
					results[idx] = s.runBranchChain(stepCtx, bc, step.Branch, item, idx)
				}(i, list[i])
			}
			bwg.Wait()
			exec.Barriers[step.BarrierID] = results

			// Join immediately per the matching fan_in's policy, and persist the
			// list under the fan_in's step id so a resume can recover it.
			fanInID, fanIn := findFanIn(pf, step.BarrierID)
			onErr := "abort"
			if fanIn != nil && fanIn.OnBranchError != "" {
				onErr = fanIn.OnBranchError
			}
			values := []any{}
			for bi, r := range results {
				if r.Err != nil {
					if onErr == "collect" {
						continue
					}
					s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{StepID: stepID, StepIdx: logicalStep, Success: false, DurationMs: time.Since(stepStart).Milliseconds()})
					s.emit("event.workflow_complete", protocol.EventWorkflowComplete{WorkflowName: pf.Name, Success: false, StepCosts: stepCosts, DurationMs: time.Since(workflowStart).Milliseconds()})
					s.activePlan = nil
					return fmt.Errorf("step '%s' fan_out: branch %d failed: %w", stepID, bi, r.Err)
				}
				values = append(values, branchValue(r))
			}
			if fanIn != nil {
				typedVars[fanIn.As] = values
				baseVars[fanIn.As] = projectToString(values)
				exec.StepResults[fanInID] = &StepResult{Output: projectToString(values), Value: values}
			}
			exec.StepResults[stepID] = &StepResult{Output: fmt.Sprintf("fanned out %d branch(es) over barrier %q", len(list), step.BarrierID), Params: stepParams}

			if !silent {
				stepCosts = append(stepCosts, protocol.StepCost{StepID: stepID, Explanation: step.Explanation, DurationMs: time.Since(stepStart).Milliseconds()})
			}
			s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{StepID: stepID, StepIdx: logicalStep, Success: true, DurationMs: time.Since(stepStart).Milliseconds()})

			next := firstPassingNext(step.NextSteps, vars, s.cwd)
			if next == nil {
				currentRef = nil
			} else if next.ID == "stop" {
				stopped = true
				goto done
			} else {
				currentRef = &StepRef{ID: next.ID, Params: resolveParams(next.Params, vars)}
			}

		case "fan_in":
			// The join was computed by fan_out and persisted under this step id;
			// rebind it (idempotent, and resume-safe) then route onward.
			var values []any
			if res, ok := exec.StepResults[stepID]; ok {
				if lst, ok := res.Value.([]any); ok {
					values = lst
				}
			}
			if values == nil {
				values = []any{}
			}
			typedVars[step.As] = values
			baseVars[step.As] = projectToString(values)

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

			if !silent {
				stepCosts = append(stepCosts, protocol.StepCost{StepID: stepID, Explanation: step.Explanation, DurationMs: time.Since(stepStart).Milliseconds()})
			}
			s.emitIfVisible(silent, "event.workflow_step_done", protocol.EventWorkflowStepDone{StepID: stepID, StepIdx: logicalStep, Success: true, DurationMs: time.Since(stepStart).Milliseconds()})

			next := firstPassingNext(step.NextSteps, vars, s.cwd)
			if next == nil {
				currentRef = nil
			} else if next.ID == "stop" {
				stopped = true
				goto done
			} else {
				currentRef = &StepRef{ID: next.ID, Params: resolveParams(next.Params, vars)}
			}

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
			// Distinguish our own deadline firing from the thread context
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

			// Make this bash step's own output visible to its next_steps
			// execute_if guards (e.g. `[[ "$(step.self)" != *NO_TODO* ]]`).
			// vars was snapshotted before the step ran, so re-merge the step
			// results now — otherwise `$(step.self)` is left unsubstituted and
			// evaluateExecuteIf runs it as a bogus (empty) command substitution.
			for k, v := range buildStepVars(exec.StepResults) {
				vars[k] = v
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

			// Make this tool step's own output visible to its next_steps
			// execute_if guards: toolVars was snapshotted before the step ran.
			for k, v := range buildStepVars(exec.StepResults) {
				toolVars[k] = v
			}

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
				ar, err := NewAgentRunner(config, cred, parentModel, s.cwd, s.server.plugins, s.projectConfig.ToolTimeouts, s.searchDirsSlice()...)
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
				vars["workflow.dir"] = s.jobDir
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
				OnRetry: func(attempt, maxRetries, waitSecs int, reason string) {
					// Capture the retry so a reopened run replays the same
					// "API overloaded — retrying …" line interactive shows,
					// then emit it live too.
					exec.recordRetry(stepID, reason, attempt, maxRetries, waitSecs)
					if baseHooks.OnRetry != nil {
						baseHooks.OnRetry(attempt, maxRetries, waitSecs, reason)
					}
				},
			}

			s.ensureWorkflowAgentContext(agent)
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
				// Keep the failed step's agent so appendWorkflowTranscript can
				// splice its partial working history into the chat transcript —
				// a failed run then replays just like a successful one.
				exec.StepAgents[stepID] = agent
				exec.recordFailedAgentStep(step, stepID)
				return fmt.Errorf("step '%s' failed: %w", stepID, err)
			}

			// Parse JSON if json_output is set. Value holds the full parsed
			// document (object OR array); Parsed keeps the object-only view for
			// back-compat with $(step.id.field) expansion.
			var parsed map[string]any
			var parsedValue any
			if step.JSONOutput {
				stripped := stripMarkdownFence(output)
				var v any
				if err := json.Unmarshal([]byte(stripped), &v); err == nil {
					parsedValue = v
					if obj, ok := v.(map[string]any); ok {
						parsed = obj
					}
				}
			}

			exec.StepResults[stepID] = &StepResult{
				Output: output,
				Parsed: parsed,
				Value:  parsedValue,
				Params: stepParams,
			}
			exec.recordTranscriptEntry(step, stepID, output)

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
