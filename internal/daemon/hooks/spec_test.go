package hooks

import (
	"testing"
	"time"

	"github.com/get-vix/vix/internal/workflow"
)

func boolPtr(b bool) *bool { return &b }

// validInlineWorkflow returns a minimal structurally-valid workflow definition
// for exercising the inline-workflow path.
func validInlineWorkflow() *workflow.Def {
	return &workflow.Def{
		Name:       "inline",
		EntryPoint: workflow.StepRef{ID: "s"},
		Steps:      map[string]workflow.StepDef{"s": {Type: "bash", Command: "true"}},
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		spec    Spec
		wantErr bool
	}{
		{"ok command", Spec{ID: "a", Trigger: HookTrigger{Event: EventPreToolUse}, Command: "true"}, false},
		{"ok workflow_id", Spec{ID: "a", Trigger: HookTrigger{Event: EventStop}, WorkflowID: "wf"}, false},
		{"ok inline workflow", Spec{ID: "a", Trigger: HookTrigger{Event: EventStop}, Workflow: validInlineWorkflow()}, false},
		{"ok prompt", Spec{ID: "a", Trigger: HookTrigger{Event: EventThreadStart}, Prompt: "hi"}, false},
		{"missing id", Spec{Trigger: HookTrigger{Event: EventPreToolUse}, Command: "true"}, true},
		{"missing event", Spec{ID: "a", Command: "true"}, true},
		{"unknown event", Spec{ID: "a", Trigger: HookTrigger{Event: "Nope"}, Command: "true"}, true},
		{"no action", Spec{ID: "a", Trigger: HookTrigger{Event: EventStop}}, true},
		{"two actions", Spec{ID: "a", Trigger: HookTrigger{Event: EventStop}, Command: "x", Prompt: "y"}, true},
		{"workflow_id and inline both", Spec{ID: "a", Trigger: HookTrigger{Event: EventStop}, WorkflowID: "wf", Workflow: validInlineWorkflow()}, true},
		{"command and workflow_id", Spec{ID: "a", Trigger: HookTrigger{Event: EventStop}, Command: "x", WorkflowID: "wf"}, true},
		{"invalid inline workflow", Spec{ID: "a", Trigger: HookTrigger{Event: EventStop}, Workflow: &workflow.Def{Name: "x"}}, true},
		{"invalid mode", Spec{ID: "a", Trigger: HookTrigger{Event: EventStop}, Command: "x", Mode: "weird"}, true},
		{"blocking needs sync", Spec{ID: "a", Trigger: HookTrigger{Event: EventPreToolUse}, Command: "x", Blocking: true}, true},
		{"blocking async rejected", Spec{ID: "a", Trigger: HookTrigger{Event: EventPreToolUse}, Command: "x", Blocking: true, Mode: ModeAsync}, true},
		{"blocking sync ok", Spec{ID: "a", Trigger: HookTrigger{Event: EventPreToolUse}, Command: "x", Blocking: true, Mode: ModeSync}, false},
		{"blocking non-blockable event", Spec{ID: "a", Trigger: HookTrigger{Event: EventStop}, Command: "x", Blocking: true, Mode: ModeSync}, true},
		{"bad matcher regex", Spec{ID: "a", Trigger: HookTrigger{Event: EventPreToolUse, Matcher: "("}, Command: "x"}, true},
		{"bad timeout", Spec{ID: "a", Trigger: HookTrigger{Event: EventStop}, Command: "x", Timeout: "nope"}, true},
		{"negative timeout", Spec{ID: "a", Trigger: HookTrigger{Event: EventStop}, Command: "x", Timeout: "-1s"}, true},
		{"ok precompact", Spec{ID: "a", Trigger: HookTrigger{Event: EventPreCompact}, Command: "true"}, false},
		{"ok postcompact", Spec{ID: "a", Trigger: HookTrigger{Event: EventPostCompact}, Command: "true"}, false},
		{"ok subagent start", Spec{ID: "a", Trigger: HookTrigger{Event: EventSubagentStart}, Command: "true"}, false},
		{"ok subagent stop", Spec{ID: "a", Trigger: HookTrigger{Event: EventSubagentStop}, Command: "true"}, false},
		{"ok permission request", Spec{ID: "a", Trigger: HookTrigger{Event: EventPermissionRequest}, Command: "true"}, false},
		{"permission request may block", Spec{ID: "a", Trigger: HookTrigger{Event: EventPermissionRequest}, Command: "x", Blocking: true, Mode: ModeSync}, false},
		{"subagent stop cannot block", Spec{ID: "a", Trigger: HookTrigger{Event: EventSubagentStop}, Command: "x", Blocking: true, Mode: ModeSync}, true},
		{"precompact cannot block", Spec{ID: "a", Trigger: HookTrigger{Event: EventPreCompact}, Command: "x", Blocking: true, Mode: ModeSync}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.spec
			err := s.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestMatches(t *testing.T) {
	mk := func(m string) *Spec {
		s := &Spec{ID: "a", Trigger: HookTrigger{Event: EventPreToolUse, Matcher: m}, Command: "x"}
		if err := s.Validate(); err != nil {
			t.Fatalf("validate %q: %v", m, err)
		}
		return s
	}
	cases := []struct {
		matcher, field string
		want           bool
	}{
		{"", "write_file", true},
		{"*", "anything", true},
		{"write_file", "write_file", true},
		{"write_file", "read_file", false},
		{"write_file|edit_file", "edit_file", true},
		{"write_file|edit_file", "read_file", false},
		{"write_file", "write_file2", false}, // anchored, no substring match
		{"mcp__.*", "mcp__fs__read", true},
		{"mcp__.*", "bash", false},
	}
	for _, tc := range cases {
		if got := mk(tc.matcher).Matches(tc.field); got != tc.want {
			t.Errorf("matcher %q field %q: got %v want %v", tc.matcher, tc.field, got, tc.want)
		}
	}
}

func TestTimeoutDuration(t *testing.T) {
	sync := Spec{Mode: ModeSync}
	if got := sync.TimeoutDuration(); got != defaultSyncTimeout {
		t.Errorf("sync default = %v, want %v", got, defaultSyncTimeout)
	}
	async := Spec{}
	if got := async.TimeoutDuration(); got != defaultAsyncTimeout {
		t.Errorf("async default = %v, want %v", got, defaultAsyncTimeout)
	}
	custom := Spec{Timeout: "12s"}
	if got := custom.TimeoutDuration(); got != 12*time.Second {
		t.Errorf("custom = %v, want 12s", got)
	}
}

func TestPermissionsDefaults(t *testing.T) {
	s := Spec{}
	if !s.AutoWrite() || !s.AutoDirs() {
		t.Errorf("defaults should be true: write=%v dirs=%v", s.AutoWrite(), s.AutoDirs())
	}
	s.Permissions = Permissions{AutoWrite: boolPtr(false), AutoDirs: boolPtr(true)}
	if s.AutoWrite() || !s.AutoDirs() {
		t.Errorf("explicit: write=%v dirs=%v", s.AutoWrite(), s.AutoDirs())
	}
}

func TestEffectiveMode(t *testing.T) {
	if (&Spec{}).EffectiveMode() != ModeAsync {
		t.Error("empty mode should default to async")
	}
	if (&Spec{Mode: ModeSync}).EffectiveMode() != ModeSync {
		t.Error("sync should stay sync")
	}
}
