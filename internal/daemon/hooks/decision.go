package hooks

import (
	"encoding/json"
	"strings"
)

// Decision behaviours a sync hook can return.
const (
	BehaviorAllow   = "allow"
	BehaviorDeny    = "deny"
	BehaviorModify  = "modify"
	BehaviorContext = "context"
)

// Decision is the structured verdict a synchronous hook returns. The zero value
// (BehaviorAllow, empty fields) means "no opinion, proceed".
type Decision struct {
	Behavior string         `json:"behavior,omitempty"`
	Reason   string         `json:"reason,omitempty"`  // shown to the model on deny
	Input    map[string]any `json:"input,omitempty"`   // replacement tool input (modify)
	Context  string         `json:"context,omitempty"` // injected developer text (context)
}

// blockSentinel is how a workflow/prompt hook signals a veto through its final
// text output (mirrors the HEARTBEAT_OK convention used by jobs).
const blockSentinel = "BLOCK:"

// ParseCommandDecision derives a Decision from a finished command hook:
//   - exit 2            → deny, reason from stderr (fallback stdout)
//   - exit 0, JSON out  → that Decision
//   - exit 0, text out  → context = stdout
//   - exit 0, no out    → allow
//   - any other exit    → allow (fail-open; the hook errored, don't wedge the loop)
func ParseCommandDecision(exitCode int, stdout, stderr string) Decision {
	if exitCode == 2 {
		reason := strings.TrimSpace(stderr)
		if reason == "" {
			reason = strings.TrimSpace(stdout)
		}
		return Decision{Behavior: BehaviorDeny, Reason: reason}
	}
	if exitCode != 0 {
		return Decision{Behavior: BehaviorAllow}
	}
	out := strings.TrimSpace(stdout)
	if out == "" {
		return Decision{Behavior: BehaviorAllow}
	}
	if d, ok := decodeDecisionJSON(out); ok {
		return d
	}
	// Plain stdout on success is treated as injected context.
	return Decision{Behavior: BehaviorContext, Context: out}
}

// ParseTextDecision derives a Decision from the final text of a workflow/prompt
// hook. A leading/trailing JSON object is honoured first, then the BLOCK:
// sentinel, otherwise the run is treated as allow (with the text available as
// context for non-blocking hooks).
func ParseTextDecision(text string) Decision {
	t := strings.TrimSpace(text)
	if t == "" {
		return Decision{Behavior: BehaviorAllow}
	}
	if d, ok := decodeDecisionJSON(t); ok {
		return d
	}
	if strings.HasPrefix(t, blockSentinel) {
		return Decision{Behavior: BehaviorDeny, Reason: strings.TrimSpace(strings.TrimPrefix(t, blockSentinel))}
	}
	return Decision{Behavior: BehaviorContext, Context: t}
}

// decodeDecisionJSON tries to parse s as a Decision object. It only accepts
// input that is a JSON object carrying a known "behavior" so arbitrary text
// that merely starts with "{" doesn't get misread.
func decodeDecisionJSON(s string) (Decision, bool) {
	if !strings.HasPrefix(s, "{") {
		return Decision{}, false
	}
	var d Decision
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		return Decision{}, false
	}
	switch d.Behavior {
	case BehaviorAllow, BehaviorDeny, BehaviorModify, BehaviorContext:
		return d, true
	}
	return Decision{}, false
}

// Combine folds several hook decisions into one, most-restrictive-wins:
// deny > modify > context > allow. Context strings from every hook are
// concatenated so observational hooks don't lose their output to a sibling.
func Combine(decisions []Decision) Decision {
	out := Decision{Behavior: BehaviorAllow}
	var contexts []string
	for _, d := range decisions {
		if d.Context != "" {
			contexts = append(contexts, d.Context)
		}
		switch d.Behavior {
		case BehaviorDeny:
			if out.Behavior != BehaviorDeny {
				out.Behavior = BehaviorDeny
				out.Reason = d.Reason
			}
		case BehaviorModify:
			if out.Behavior == BehaviorAllow || out.Behavior == BehaviorContext {
				out.Behavior = BehaviorModify
				out.Input = d.Input
			}
		case BehaviorContext:
			if out.Behavior == BehaviorAllow {
				out.Behavior = BehaviorContext
			}
		}
	}
	if len(contexts) > 0 {
		out.Context = strings.Join(contexts, "\n")
	}
	return out
}
