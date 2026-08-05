// Package hooks implements vixd's lifecycle-hooks engine: user-authored hook
// specs (~/.vix/hooks/<id>/hook.json) that fire on agent-loop events (a tool
// about to run, a prompt submitted, a thread starting, …) rather than on a
// timer.
//
// A hook either runs synchronously and returns a Decision that can veto/rewrite
// the triggering action (mode "sync"), or fires-and-forgets in an isolated
// thread (mode "async"). The package owns parsing, validation, and the
// in-memory registry; actual execution is delegated to the daemon, keeping the
// dependency direction daemon → hooks.
package hooks

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/get-vix/vix/internal/workflow"
)

// Lifecycle events a hook can subscribe to. Only events listed in
// supportedEvents are accepted by Validate; the rest of the catalogue is added
// as it gets wired into the thread loop.
const (
	EventPreToolUse        = "PreToolUse"
	EventPostToolUse       = "PostToolUse"
	EventUserPromptSubmit  = "UserPromptSubmit"
	EventThreadStart       = "ThreadStart"
	EventStop              = "Stop"
	EventPreCompact        = "PreCompact"
	EventPostCompact       = "PostCompact"
	EventSubagentStart     = "SubagentStart"
	EventSubagentStop      = "SubagentStop"
	EventPermissionRequest = "PermissionRequest"
)

// Execution modes.
const (
	ModeSync  = "sync"
	ModeAsync = "async"
)

// supportedEvents is the set of events the engine currently fires. Authoring a
// hook for any other event is rejected so users get an error instead of a hook
// that silently never runs.
var supportedEvents = map[string]bool{
	EventPreToolUse:        true,
	EventPostToolUse:       true,
	EventUserPromptSubmit:  true,
	EventThreadStart:       true,
	EventStop:              true,
	EventPreCompact:        true,
	EventPostCompact:       true,
	EventSubagentStart:     true,
	EventSubagentStop:      true,
	EventPermissionRequest: true,
}

// blockableEvents is the subset whose sync hooks may veto (Decision deny) or
// rewrite (Decision modify) the triggering action. Other events can only inject
// context or run async.
var blockableEvents = map[string]bool{
	EventPreToolUse:        true,
	EventUserPromptSubmit:  true,
	EventPermissionRequest: true,
}

// defaultSyncTimeout bounds a synchronous hook so it can never wedge the agent
// loop. defaultAsyncTimeout bounds a fire-and-forget run.
const (
	defaultSyncTimeout  = 5 * time.Second
	defaultAsyncTimeout = 10 * time.Minute
)

// HookTrigger selects which event fires the hook and (optionally) narrows it
// with a regex matched against the event's match field (tool name for tool
// events, source for ThreadStart, …). An empty or "*" matcher matches all.
type HookTrigger struct {
	Event   string `json:"event"`
	Matcher string `json:"matcher,omitempty"`
}

// Permissions maps onto the isolated thread's automatic-permission flags for
// workflow/prompt hooks. Pointers so "absent" defaults to true.
type Permissions struct {
	AutoWrite *bool `json:"auto_write,omitempty"`
	AutoDirs  *bool `json:"auto_dirs,omitempty"`
}

// Spec is a user-authored hook definition, one hook.json per hook under
// ~/.vix/hooks/<id>/. Exactly one action runs: Command, a workflow (named via
// WorkflowID or embedded inline in Workflow), or Prompt.
type Spec struct {
	ID       string      `json:"id"`
	Name     string      `json:"name,omitempty"`
	Enabled  bool        `json:"enabled"`
	Trigger  HookTrigger `json:"trigger"`
	Mode     string      `json:"mode,omitempty"`     // sync | async (default async)
	Blocking bool        `json:"blocking,omitempty"` // sync only; may veto

	Command    string        `json:"command,omitempty"`     // shell command (fast path)
	WorkflowID string        `json:"workflow_id,omitempty"` // named workflow (config/workflow.json)
	Workflow   *workflow.Def `json:"workflow,omitempty"`    // inline workflow definition
	Prompt     string        `json:"prompt,omitempty"`      // plain prompt (LLM)

	CWD         string      `json:"cwd,omitempty"`
	Permissions Permissions `json:"permissions,omitempty"`
	Timeout     string      `json:"timeout,omitempty"`
	CreatedBy   string      `json:"created_by,omitempty"`
	Description string      `json:"description,omitempty"` // free-form, shown in the web UI

	// matcherRe is the compiled, anchored matcher. nil means match-all.
	matcherRe *regexp.Regexp
}

// EffectiveMode returns the resolved execution mode, defaulting to async so a
// hook never blocks the loop unless it opts in.
func (s *Spec) EffectiveMode() string {
	if s.Mode == ModeSync {
		return ModeSync
	}
	return ModeAsync
}

// Validate reports the first problem with the spec, or nil. It also compiles
// the matcher.
func (s *Spec) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("missing id")
	}
	if s.Trigger.Event == "" {
		return fmt.Errorf("missing trigger.event")
	}
	if !supportedEvents[s.Trigger.Event] {
		return fmt.Errorf("unsupported trigger.event %q", s.Trigger.Event)
	}
	if s.Mode != "" && s.Mode != ModeSync && s.Mode != ModeAsync {
		return fmt.Errorf("invalid mode %q (want \"sync\" or \"async\")", s.Mode)
	}
	// At most one way to name the workflow action.
	if s.WorkflowID != "" && s.Workflow != nil {
		return fmt.Errorf("set only one of workflow_id or workflow, not both")
	}
	// Exactly one action: command, a workflow (named or inline), or prompt.
	hasWorkflow := strings.TrimSpace(s.WorkflowID) != "" || s.Workflow != nil
	n := 0
	if strings.TrimSpace(s.Command) != "" {
		n++
	}
	if hasWorkflow {
		n++
	}
	if strings.TrimSpace(s.Prompt) != "" {
		n++
	}
	if n == 0 {
		return fmt.Errorf("hook must set exactly one of command, workflow, or prompt")
	}
	if n > 1 {
		return fmt.Errorf("hook must set only one of command, workflow, or prompt")
	}
	if s.Workflow != nil {
		if err := workflow.Validate(s.Workflow); err != nil {
			return fmt.Errorf("inline workflow: %w", err)
		}
	}
	if s.Blocking {
		if s.EffectiveMode() != ModeSync {
			return fmt.Errorf("blocking hooks require mode \"sync\"")
		}
		if !blockableEvents[s.Trigger.Event] {
			return fmt.Errorf("event %q cannot block (only PreToolUse, UserPromptSubmit and PermissionRequest may veto)", s.Trigger.Event)
		}
	}
	if s.Timeout != "" {
		d, err := time.ParseDuration(s.Timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("invalid timeout: must be positive")
		}
	}
	re, err := compileMatcher(s.Trigger.Matcher)
	if err != nil {
		return fmt.Errorf("invalid matcher: %w", err)
	}
	s.matcherRe = re
	return nil
}

// Matches reports whether field satisfies the hook's matcher. A nil/absent
// matcher matches everything.
func (s *Spec) Matches(field string) bool {
	if s.matcherRe == nil {
		return true
	}
	return s.matcherRe.MatchString(field)
}

// TimeoutDuration returns the per-run wall-clock budget, defaulting by mode.
func (s *Spec) TimeoutDuration() time.Duration {
	if s.Timeout != "" {
		if d, err := time.ParseDuration(s.Timeout); err == nil && d > 0 {
			return d
		}
	}
	if s.EffectiveMode() == ModeSync {
		return defaultSyncTimeout
	}
	return defaultAsyncTimeout
}

// EffectiveTimeout returns the resolved timeout as a short human string: the
// author's own value when set, otherwise the mode-based default ("5s" sync,
// "10m" async). Used by HookSnapshot so the web UI never has to guess.
func (s *Spec) EffectiveTimeout() string {
	if s.Timeout != "" {
		return s.Timeout
	}
	if s.EffectiveMode() == ModeSync {
		return "5s"
	}
	return "10m"
}

// AutoWrite reports the effective auto_write permission (default true).
func (s *Spec) AutoWrite() bool {
	if s.Permissions.AutoWrite == nil {
		return true
	}
	return *s.Permissions.AutoWrite
}

// AutoDirs reports the effective auto_dirs permission (default true).
func (s *Spec) AutoDirs() bool {
	if s.Permissions.AutoDirs == nil {
		return true
	}
	return *s.Permissions.AutoDirs
}

// compileMatcher turns a matcher string into an anchored regexp. "", "*" yield
// nil (match-all). Everything else is compiled as a full-string-anchored regex,
// so "write_file|edit_file" matches those names exactly, not as substrings.
func compileMatcher(m string) (*regexp.Regexp, error) {
	m = strings.TrimSpace(m)
	if m == "" || m == "*" {
		return nil, nil
	}
	return regexp.Compile("^(?:" + m + ")$")
}
