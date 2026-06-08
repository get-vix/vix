package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── validateWorkflow ──

func TestValidateWorkflow(t *testing.T) {
	tests := []struct {
		name    string
		pf      WorkflowDef
		wantErr string // empty = no error expected
	}{
		{
			name: "valid workflow",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Agent: "planner", Prompt: "do something"},
				},
			},
			wantErr: "",
		},
		{
			name: "missing name",
			pf: WorkflowDef{
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Agent: "planner", Prompt: "do something"},
				},
			},
			wantErr: "missing name",
		},
		{
			name: "no steps",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
			},
			wantErr: "no steps defined",
		},
		{
			name: "empty step id key",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: ""},
				Steps: map[string]WorkflowStepDef{
					"": {Type: "agent", Agent: "planner", Prompt: "do something"},
				},
			},
			wantErr: "empty id",
		},
		{
			name: "missing type",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Agent: "planner", Prompt: "do something"},
				},
			},
			wantErr: "missing type",
		},
		{
			name: "unknown type",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "banana", Agent: "planner", Prompt: "do something"},
				},
			},
			wantErr: "unknown type 'banana'",
		},
		{
			name: "no agent or fork",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Prompt: "do something"},
				},
			},
			wantErr: "must have either",
		},
		{
			name: "both agent and fork",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step0": {Type: "agent", Agent: "planner", Prompt: "first"},
					"step1": {Type: "agent", Agent: "planner", ForkFrom: "step0", Prompt: "do something", NextSteps: []StepRef{{ID: "step0"}}},
				},
			},
			wantErr: "cannot have both",
		},
		{
			name: "fork from unknown step",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", ForkFrom: "nope", Prompt: "do something"},
				},
			},
			wantErr: "references unknown step",
		},
		{
			name: "fork from existing step is valid",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Agent: "planner", Prompt: "first", NextSteps: []StepRef{{ID: "step2"}}},
					"step2": {Type: "agent", ForkFrom: "step1", Prompt: "second"},
				},
			},
			wantErr: "",
		},
		{
			name: "missing prompt",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Agent: "planner"},
				},
			},
			wantErr: "missing prompt",
		},
		{
			name: "valid tool step with options",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Agent: "planner", Prompt: "do something", NextSteps: []StepRef{{ID: "review"}}},
					"review": {Type: "tool", Tool: "ask_question_to_user", Options: []StepOption{
						{Title: "Accept", Description: "Approve", Steps: []StepRef{{ID: "step1"}}},
						{Title: "Reject", Description: "Reject", Steps: []StepRef{{ID: "stop"}}},
					}},
				},
			},
			wantErr: "",
		},
		{
			name: "tool step missing tool field",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "tool"},
				},
			},
			wantErr: "requires 'tool' field",
		},
		{
			name: "option step references unknown step",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Agent: "planner", Prompt: "do something", NextSteps: []StepRef{{ID: "review"}}},
					"review": {Type: "tool", Tool: "ask_question_to_user", Options: []StepOption{
						{Title: "Accept", Description: "Approve", Steps: []StepRef{{ID: "nonexistent"}}},
					}},
				},
			},
			wantErr: "references unknown step",
		},
		{
			name: "option step stop is valid",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Agent: "planner", Prompt: "do something", NextSteps: []StepRef{{ID: "review"}}},
					"review": {Type: "tool", Tool: "ask_question_to_user", Options: []StepOption{
						{Title: "Reject", Description: "Reject the plan", Steps: []StepRef{{ID: "stop"}}},
					}},
				},
			},
			wantErr: "",
		},
		{
			name: "option with has_user_input is valid",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Agent: "planner", Prompt: "do something", NextSteps: []StepRef{{ID: "review"}}},
					"review": {Type: "tool", Tool: "ask_question_to_user", Options: []StepOption{
						{Title: "Accept", Description: "Approve", Steps: []StepRef{{ID: "step1"}}},
						{Title: "Modify", Description: "Provide feedback", Steps: []StepRef{{ID: "step1"}}, HasUserInput: true},
					}},
				},
			},
			wantErr: "",
		},
		{
			name: "missing entry_point",
			pf: WorkflowDef{
				Name: "Test Plan",
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Agent: "planner", Prompt: "do something"},
				},
			},
			wantErr: "missing entry_point",
		},
		{
			name: "entry_point references unknown step",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "nonexistent"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Agent: "planner", Prompt: "do something"},
				},
			},
			wantErr: "entry_point 'nonexistent' references unknown step",
		},
		{
			name: "next_step references unknown step",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Agent: "planner", Prompt: "do something", NextSteps: []StepRef{{ID: "nonexistent"}}},
				},
			},
			wantErr: "next_step 'nonexistent' references unknown step",
		},
		{
			name: "unreachable step",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1":    {Type: "agent", Agent: "planner", Prompt: "do something"},
					"orphaned": {Type: "agent", Agent: "planner", Prompt: "never reached"},
				},
			},
			wantErr: "",
		},
		{
			name: "next_step stop is valid",
			pf: WorkflowDef{
				Name:       "Test Plan",
				EntryPoint: StepRef{ID: "step1"},
				Steps: map[string]WorkflowStepDef{
					"step1": {Type: "agent", Agent: "planner", Prompt: "do something", NextSteps: []StepRef{{ID: "stop"}}},
				},
			},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWorkflow(&tt.pf)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error, got: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

// ── resolveParams ──

func TestResolveParams(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]string
		vars   map[string]string
		want   map[string]string
	}{
		{
			name:   "nil params returns nil",
			params: nil,
			vars:   map[string]string{"x": "y"},
			want:   nil,
		},
		{
			name:   "empty params returns nil",
			params: map[string]string{},
			vars:   map[string]string{"x": "y"},
			want:   nil,
		},
		{
			name:   "literal values passed through",
			params: map[string]string{"key": "hello"},
			vars:   map[string]string{},
			want:   map[string]string{"key": "hello"},
		},
		{
			name:   "$() wrapper resolves from vars",
			params: map[string]string{"prompt": "$(user_prompt)"},
			vars:   map[string]string{"user_prompt": "build a thing"},
			want:   map[string]string{"prompt": "build a thing"},
		},
		{
			name:   "$() with missing var left as-is",
			params: map[string]string{"prompt": "$(missing)"},
			vars:   map[string]string{"other": "value"},
			want:   map[string]string{"prompt": "$(missing)"},
		},
		{
			name:   "mixed literal and $() values",
			params: map[string]string{"a": "literal", "b": "$(x)"},
			vars:   map[string]string{"x": "resolved"},
			want:   map[string]string{"a": "literal", "b": "resolved"},
		},
		{
			name:   "embedded $() within longer string",
			params: map[string]string{"feedback": "The user said: $(user_text). Please address it."},
			vars:   map[string]string{"user_text": "make it faster"},
			want:   map[string]string{"feedback": "The user said: make it faster. Please address it."},
		},
		{
			name:   "multiple embedded $() in one value",
			params: map[string]string{"msg": "$(greeting) $(name)!"},
			vars:   map[string]string{"greeting": "Hello", "name": "World"},
			want:   map[string]string{"msg": "Hello World!"},
		},
		{
			name:   "embedded $() with missing var left as-is",
			params: map[string]string{"msg": "prefix $(unknown) suffix"},
			vars:   map[string]string{"other": "val"},
			want:   map[string]string{"msg": "prefix $(unknown) suffix"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveParams(tt.params, tt.vars)
			if tt.want == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("length mismatch: got %d, want %d", len(got), len(tt.want))
			}
			for k, wantV := range tt.want {
				if got[k] != wantV {
					t.Errorf("key %q: got %q, want %q", k, got[k], wantV)
				}
			}
		})
	}
}

// ── LoadWorkflowsFile ──

func TestLoadWorkflows(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "workflow.json")

		cfg := workflowsFile{
			Workflows: []WorkflowDef{
				{
					Name:       "My Plan",
					EntryPoint: StepRef{ID: "s1"},
					Steps: map[string]WorkflowStepDef{
						"s1": {Type: "agent", Agent: "planner", Prompt: "plan it"},
					},
				},
			},
		}
		data, _ := json.Marshal(cfg)
		os.WriteFile(path, data, 0644)

		result := LoadWorkflowsFile(path)
		if len(result) != 1 {
			t.Fatalf("expected 1 workflow, got %d", len(result))
		}
		if result[0].Name != "My Plan" {
			t.Errorf("expected name 'My Plan', got %q", result[0].Name)
		}
	})

	t.Run("missing file returns empty", func(t *testing.T) {
		result := LoadWorkflowsFile("/nonexistent/path/workflow.json")
		if len(result) != 0 {
			t.Errorf("expected empty result for missing file, got %d", len(result))
		}
	})

	t.Run("empty path returns empty", func(t *testing.T) {
		if result := LoadWorkflowsFile(""); len(result) != 0 {
			t.Errorf("expected empty result for empty path, got %d", len(result))
		}
	})

	t.Run("invalid JSON returns empty", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "workflow.json")
		os.WriteFile(path, []byte("not json"), 0644)

		result := LoadWorkflowsFile(path)
		if len(result) != 0 {
			t.Errorf("expected empty result for invalid JSON, got %d", len(result))
		}
	})

	t.Run("invalid workflow skipped", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "workflow.json")

		cfg := workflowsFile{
			Workflows: []WorkflowDef{
				{
					// Missing name → invalid
					EntryPoint: StepRef{ID: "s1"},
					Steps: map[string]WorkflowStepDef{
						"s1": {Type: "agent", Agent: "x", Prompt: "y"},
					},
				},
			},
		}
		data, _ := json.Marshal(cfg)
		os.WriteFile(path, data, 0644)

		result := LoadWorkflowsFile(path)
		if len(result) != 0 {
			t.Errorf("expected 0 workflows (invalid skipped), got %d", len(result))
		}
	})

	t.Run("preserves config order", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "workflow.json")

		cfg := workflowsFile{
			Workflows: []WorkflowDef{
				{
					Name:       "Workflow B",
					EntryPoint: StepRef{ID: "s1"},
					Steps: map[string]WorkflowStepDef{
						"s1": {Type: "agent", Agent: "a", Prompt: "do it"},
					},
				},
				{
					Name:       "Workflow A",
					EntryPoint: StepRef{ID: "s1"},
					Steps: map[string]WorkflowStepDef{
						"s1": {Type: "agent", Agent: "a", Prompt: "do it"},
					},
				},
			},
		}
		data, _ := json.Marshal(cfg)
		os.WriteFile(path, data, 0644)

		result := LoadWorkflowsFile(path)
		if len(result) != 2 {
			t.Fatalf("expected 2 workflows, got %d", len(result))
		}
		if result[0].Name != "Workflow B" {
			t.Errorf("expected first workflow 'Workflow B', got %q", result[0].Name)
		}
		if result[1].Name != "Workflow A" {
			t.Errorf("expected second workflow 'Workflow A', got %q", result[1].Name)
		}
	})

	t.Run("duplicate names disambiguated", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "workflow.json")

		cfg := workflowsFile{
			Workflows: []WorkflowDef{
				{
					Name:       "Plan",
					EntryPoint: StepRef{ID: "s1"},
					Steps:      map[string]WorkflowStepDef{"s1": {Type: "agent", Agent: "a", Prompt: "x"}},
				},
				{
					Name:       "Plan",
					EntryPoint: StepRef{ID: "s1"},
					Steps:      map[string]WorkflowStepDef{"s1": {Type: "agent", Agent: "a", Prompt: "x"}},
				},
			},
		}
		data, _ := json.Marshal(cfg)
		os.WriteFile(path, data, 0644)

		result := LoadWorkflowsFile(path)
		if len(result) != 2 {
			t.Fatalf("expected 2 workflows, got %d", len(result))
		}
		if result[0].Name == result[1].Name {
			t.Errorf("expected disambiguated names, both are %q", result[0].Name)
		}
	})
}

// ── LoadProjectConfig ──

func TestLoadProjectConfig(t *testing.T) {
	t.Run("loads agent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")

		cfg := configFile{
			Version: CurrentConfigVersion,
			Agent:   "custom",
		}
		data, _ := json.Marshal(cfg)
		os.WriteFile(path, data, 0644)

		result := LoadProjectConfig(path)
		if result.Agent != "custom" {
			t.Errorf("expected agent 'custom', got %q", result.Agent)
		}
	})

	t.Run("defaults to general agent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")

		cfg := configFile{
			Version: CurrentConfigVersion,
		}
		data, _ := json.Marshal(cfg)
		os.WriteFile(path, data, 0644)

		result := LoadProjectConfig(path)
		if result.Agent != "general" {
			t.Errorf("expected default agent 'general', got %q", result.Agent)
		}
	})

	t.Run("missing file returns defaults", func(t *testing.T) {
		result := LoadProjectConfig("/nonexistent/path/settings.json")
		if result.Agent != "general" {
			t.Errorf("expected default agent 'general', got %q", result.Agent)
		}
	})

	t.Run("version mismatch skips config", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")

		cfg := configFile{
			Version: 999,
			Agent:   "custom",
		}
		data, _ := json.Marshal(cfg)
		os.WriteFile(path, data, 0644)

		result := LoadProjectConfig(path)
		if result.Agent != "general" {
			t.Errorf("expected default agent 'general' (config skipped), got %q", result.Agent)
		}
	})

	t.Run("missing version (zero) skips config", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")

		cfg := configFile{
			Agent: "custom",
		}
		data, _ := json.Marshal(cfg)
		os.WriteFile(path, data, 0644)

		result := LoadProjectConfig(path)
		if result.Agent != "general" {
			t.Errorf("expected default agent 'general' (config skipped), got %q", result.Agent)
		}
	})
}

// ── LoadProjectConfig: tool_timeouts block ──

func TestLoadProjectConfigToolTimeouts(t *testing.T) {
	writeSettings := func(t *testing.T, path string, cfg configFile) {
		t.Helper()
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	ip := func(v int) *int { return &v }

	expectBounds := func(t *testing.T, got ToolTimeouts, wantDef, wantMax time.Duration) {
		t.Helper()
		if got.Default != wantDef || got.Max != wantMax {
			t.Errorf("ToolTimeouts = {Default: %v, Max: %v}, want {%v, %v}",
				got.Default, got.Max, wantDef, wantMax)
		}
	}

	t.Run("absent block falls back to hard-coded defaults", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{Version: CurrentConfigVersion, Agent: "custom"})

		result := LoadProjectConfig(path)
		expectBounds(t, result.ToolTimeouts, defaultToolTimeoutDefault, defaultToolTimeoutMax)
	})

	t.Run("valid full block is honored", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:      CurrentConfigVersion,
			ToolTimeouts: &toolTimeoutsFile{DefaultSec: ip(90), MaxSec: ip(450)},
		})

		result := LoadProjectConfig(path)
		expectBounds(t, result.ToolTimeouts, 90*time.Second, 450*time.Second)
	})

	t.Run("partial block: only default_sec set, max inherits hard-coded", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:      CurrentConfigVersion,
			ToolTimeouts: &toolTimeoutsFile{DefaultSec: ip(60)},
		})

		result := LoadProjectConfig(path)
		expectBounds(t, result.ToolTimeouts, 60*time.Second, defaultToolTimeoutMax)
	})

	t.Run("partial block: only max_sec set, default inherits hard-coded", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:      CurrentConfigVersion,
			ToolTimeouts: &toolTimeoutsFile{MaxSec: ip(300)},
		})

		result := LoadProjectConfig(path)
		expectBounds(t, result.ToolTimeouts, defaultToolTimeoutDefault, 300*time.Second)
	})

	t.Run("default_sec zero is logged and ignored", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:      CurrentConfigVersion,
			ToolTimeouts: &toolTimeoutsFile{DefaultSec: ip(0), MaxSec: ip(450)},
		})

		result := LoadProjectConfig(path)
		// default_sec=0 is rejected, default stays hard-coded;
		// max_sec=450 is honored.
		expectBounds(t, result.ToolTimeouts, defaultToolTimeoutDefault, 450*time.Second)
	})

	t.Run("max_sec zero is logged and ignored", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:      CurrentConfigVersion,
			ToolTimeouts: &toolTimeoutsFile{DefaultSec: ip(90), MaxSec: ip(0)},
		})

		result := LoadProjectConfig(path)
		expectBounds(t, result.ToolTimeouts, 90*time.Second, defaultToolTimeoutMax)
	})

	t.Run("inverted bounds revert both to hard-coded defaults", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:      CurrentConfigVersion,
			ToolTimeouts: &toolTimeoutsFile{DefaultSec: ip(300), MaxSec: ip(120)},
		})

		result := LoadProjectConfig(path)
		// default > max → both reset to hard-coded defaults.
		expectBounds(t, result.ToolTimeouts, defaultToolTimeoutDefault, defaultToolTimeoutMax)
	})

	t.Run("equal bounds are valid", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:      CurrentConfigVersion,
			ToolTimeouts: &toolTimeoutsFile{DefaultSec: ip(200), MaxSec: ip(200)},
		})

		result := LoadProjectConfig(path)
		expectBounds(t, result.ToolTimeouts, 200*time.Second, 200*time.Second)
	})

	t.Run("multi-file: later absent preserves earlier value", func(t *testing.T) {
		homeDir := t.TempDir()
		projectDir := t.TempDir()
		homePath := filepath.Join(homeDir, "settings.json")
		projectPath := filepath.Join(projectDir, "settings.json")

		writeSettings(t, homePath, configFile{
			Version:      CurrentConfigVersion,
			ToolTimeouts: &toolTimeoutsFile{DefaultSec: ip(90), MaxSec: ip(450)},
		})
		writeSettings(t, projectPath, configFile{
			Version: CurrentConfigVersion,
			Agent:   "project-agent",
		})

		result := LoadProjectConfig(homePath, projectPath)
		expectBounds(t, result.ToolTimeouts, 90*time.Second, 450*time.Second)
		if result.Agent != "project-agent" {
			t.Errorf("expected project-agent, got %q", result.Agent)
		}
	})

	t.Run("multi-file: later override wins field-by-field", func(t *testing.T) {
		homeDir := t.TempDir()
		projectDir := t.TempDir()
		homePath := filepath.Join(homeDir, "settings.json")
		projectPath := filepath.Join(projectDir, "settings.json")

		writeSettings(t, homePath, configFile{
			Version:      CurrentConfigVersion,
			ToolTimeouts: &toolTimeoutsFile{DefaultSec: ip(90), MaxSec: ip(450)},
		})
		writeSettings(t, projectPath, configFile{
			Version:      CurrentConfigVersion,
			ToolTimeouts: &toolTimeoutsFile{DefaultSec: ip(60), MaxSec: ip(300)},
		})

		result := LoadProjectConfig(homePath, projectPath)
		expectBounds(t, result.ToolTimeouts, 60*time.Second, 300*time.Second)
	})

	t.Run("multi-file: later partial override merges with earlier", func(t *testing.T) {
		homeDir := t.TempDir()
		projectDir := t.TempDir()
		homePath := filepath.Join(homeDir, "settings.json")
		projectPath := filepath.Join(projectDir, "settings.json")

		writeSettings(t, homePath, configFile{
			Version:      CurrentConfigVersion,
			ToolTimeouts: &toolTimeoutsFile{DefaultSec: ip(90), MaxSec: ip(450)},
		})
		writeSettings(t, projectPath, configFile{
			Version:      CurrentConfigVersion,
			ToolTimeouts: &toolTimeoutsFile{MaxSec: ip(300)},
		})

		result := LoadProjectConfig(homePath, projectPath)
		// Home set default=90, max=450; project overrode max to 300 only.
		expectBounds(t, result.ToolTimeouts, 90*time.Second, 300*time.Second)
	})
}

// ── LoadProjectConfig: compaction block ──

func TestLoadProjectConfigCompaction(t *testing.T) {
	writeSettings := func(t *testing.T, path string, cfg configFile) {
		t.Helper()
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	fp := func(v float64) *float64 { return &v }
	bp := func(v bool) *bool { return &v }
	ip := func(v int) *int { return &v }

	expect := func(t *testing.T, got Compaction, threshold float64, auto bool, keepN int, keepRatio float64) {
		t.Helper()
		if got.Threshold != threshold || got.Auto != auto || got.KeepLastNTurns != keepN || got.KeepRatio != keepRatio {
			t.Errorf("Compaction = %+v, want {Threshold:%v Auto:%v KeepLastNTurns:%v KeepRatio:%v}",
				got, threshold, auto, keepN, keepRatio)
		}
	}

	t.Run("absent block falls back to defaults", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{Version: CurrentConfigVersion})

		result := LoadProjectConfig(path)
		expect(t, result.Compaction, defaultCompactionThreshold, defaultCompactionAuto, defaultCompactionKeepLastN, defaultCompactionKeepRatio)
	})

	t.Run("full valid block is honored", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:    CurrentConfigVersion,
			Compaction: &compactionFile{Threshold: fp(0.6), Auto: bp(false), KeepLastNTurns: ip(4)},
		})

		result := LoadProjectConfig(path)
		expect(t, result.Compaction, 0.6, false, 4, defaultCompactionKeepRatio)
	})

	t.Run("partial block: only threshold set, others inherit defaults", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:    CurrentConfigVersion,
			Compaction: &compactionFile{Threshold: fp(0.5)},
		})

		result := LoadProjectConfig(path)
		expect(t, result.Compaction, 0.5, defaultCompactionAuto, defaultCompactionKeepLastN, defaultCompactionKeepRatio)
	})

	t.Run("invalid threshold is ignored", func(t *testing.T) {
		for _, bad := range []float64{0, 1.5, -0.2} {
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			writeSettings(t, path, configFile{
				Version:    CurrentConfigVersion,
				Compaction: &compactionFile{Threshold: fp(bad)},
			})

			result := LoadProjectConfig(path)
			if result.Compaction.Threshold != defaultCompactionThreshold {
				t.Errorf("threshold %v: got %v, want default %v", bad, result.Compaction.Threshold, defaultCompactionThreshold)
			}
		}
	})

	t.Run("threshold of 1 is valid (inclusive)", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:    CurrentConfigVersion,
			Compaction: &compactionFile{Threshold: fp(1)},
		})

		result := LoadProjectConfig(path)
		if result.Compaction.Threshold != 1 {
			t.Errorf("threshold = %v, want 1", result.Compaction.Threshold)
		}
	})

	t.Run("keep_last_n_turns below -1 is clamped to -1", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:    CurrentConfigVersion,
			Compaction: &compactionFile{KeepLastNTurns: ip(-5)},
		})

		result := LoadProjectConfig(path)
		if result.Compaction.KeepLastNTurns != -1 {
			t.Errorf("KeepLastNTurns = %v, want -1", result.Compaction.KeepLastNTurns)
		}
	})

	t.Run("multi-file: later override wins field-by-field", func(t *testing.T) {
		homeDir := t.TempDir()
		projectDir := t.TempDir()
		homePath := filepath.Join(homeDir, "settings.json")
		projectPath := filepath.Join(projectDir, "settings.json")

		writeSettings(t, homePath, configFile{
			Version:    CurrentConfigVersion,
			Compaction: &compactionFile{Auto: bp(false)},
		})
		writeSettings(t, projectPath, configFile{
			Version:    CurrentConfigVersion,
			Compaction: &compactionFile{Threshold: fp(0.6)},
		})

		result := LoadProjectConfig(homePath, projectPath)
		// Home disabled auto; project set threshold only — both apply.
		expect(t, result.Compaction, 0.6, false, defaultCompactionKeepLastN, defaultCompactionKeepRatio)
	})
}

// ── LoadConfig ──

func TestLoadConfig(t *testing.T) {
	writeSettings := func(t *testing.T, dir string, cfg configFile) {
		t.Helper()
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal settings: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0644); err != nil {
			t.Fatalf("write settings: %v", err)
		}
	}

	t.Run("project value wins over home value", func(t *testing.T) {
		homeVixDir := t.TempDir()
		projectDir := t.TempDir()
		vixDir := filepath.Join(projectDir, ".vix")
		if err := os.MkdirAll(vixDir, 0755); err != nil {
			t.Fatalf("mkdir .vix: %v", err)
		}

		writeSettings(t, homeVixDir, configFile{Version: 1, Agent: "home-agent"})
		writeSettings(t, vixDir, configFile{Version: 1, Agent: "project-agent"})

		cfg := LoadProjectConfig(
			filepath.Join(homeVixDir, "settings.json"),
			filepath.Join(vixDir, "settings.json"),
		)
		if cfg.Agent != "project-agent" {
			t.Errorf("expected project-agent to win, got %q", cfg.Agent)
		}
	})

	t.Run("empty homeVixDir uses only project config", func(t *testing.T) {
		projectDir := t.TempDir()
		vixDir := filepath.Join(projectDir, ".vix")
		if err := os.MkdirAll(vixDir, 0755); err != nil {
			t.Fatalf("mkdir .vix: %v", err)
		}

		writeSettings(t, vixDir, configFile{Version: 1, Agent: "project-agent"})

		cfg := LoadProjectConfig(filepath.Join(vixDir, "settings.json"))
		if cfg.Agent != "project-agent" {
			t.Errorf("expected project-agent, got %q", cfg.Agent)
		}
	})

	t.Run("empty cwd falls back to home config", func(t *testing.T) {
		homeVixDir := t.TempDir()

		writeSettings(t, homeVixDir, configFile{Version: 1, Agent: "home-agent"})

		cfg := LoadProjectConfig(filepath.Join(homeVixDir, "settings.json"))
		if cfg.Agent != "home-agent" {
			t.Errorf("expected home-agent, got %q", cfg.Agent)
		}
	})
}

// ── stripMarkdownFence ──

func TestStripMarkdownFence(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no fence returns as-is",
			input: `{"key":"value"}`,
			want:  `{"key":"value"}`,
		},
		{
			name:  "json fence at start",
			input: "```json\n{\"key\":\"value\"}\n```",
			want:  `{"key":"value"}`,
		},
		{
			name:  "generic fence at start",
			input: "```\n{\"key\":\"value\"}\n```",
			want:  `{"key":"value"}`,
		},
		{
			name:  "preamble before json fence",
			input: "Some preamble text\n\n```json\n{\"display\":\"summary\",\"result\":\"details\"}\n```",
			want:  `{"display":"summary","result":"details"}`,
		},
		{
			name:  "preamble before generic fence",
			input: "Here is the result:\n```\n{\"key\":\"value\"}\n```",
			want:  `{"key":"value"}`,
		},
		{
			name:  "whitespace around input",
			input: "  \n```json\n{\"a\":1}\n```\n  ",
			want:  `{"a":1}`,
		},
		{
			name:  "nested fences inside JSON string",
			input: "```json\n{\"display\":\"summary\",\"result\":\"```go\\nfunc main(){}\\n```\"}\n```",
			want:  `{"display":"summary","result":"` + "```go\\nfunc main(){}\\n```" + `"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripMarkdownFence(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// ── resolveParams with tool step params ──

// TestResolveParamsWithStepParams verifies that once stepParams are merged into
// toolVars (as the fix does), $(plan) in an option's params block resolves to the
// value that was passed into the tool step — not just $(step.plan).
func TestResolveParamsWithStepParams(t *testing.T) {
	// Simulate the toolVars map as built after the fix: baseVars merged with
	// buildStepVars output, then stepParams merged on top.
	toolVars := map[string]string{
		"step.prior.output": "prior step result",
		"plan":              "my plan content", // injected from stepParams
	}

	// An option's steps[].params block that references $(plan) — the current
	// step's own input param.
	optionParams := map[string]string{
		"plan":     "$(plan)",
		"feedback": "$(step.prior.output)",
	}

	resolved := resolveParams(optionParams, toolVars)

	if resolved["plan"] != "my plan content" {
		t.Errorf("$(plan) should resolve to stepParam value; got %q", resolved["plan"])
	}
	if resolved["feedback"] != "prior step result" {
		t.Errorf("$(step.prior.output) should resolve to prior step output; got %q", resolved["feedback"])
	}
}

// ── resolveBashExpansions ──

func TestResolveBashExpansions(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no bash tokens — unchanged",
			input: "hello world $(step.foo)",
			want:  "hello world $(step.foo)",
		},
		{
			name:  "single bash token replaced",
			input: "$(bash:echo hello)",
			want:  "hello",
		},
		{
			name:  "multiple bash tokens in one string",
			input: "$(bash:echo foo) and $(bash:echo bar)",
			want:  "foo and bar",
		},
		{
			name:  "failed command replaced with empty string",
			input: "before $(bash:exit 1) after",
			want:  "before  after",
		},
		{
			name:  "bash token mixed with regular var token",
			input: "$(step.x) and $(bash:echo dynamic)",
			want:  "$(step.x) and dynamic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveBashExpansions(tt.input, "")
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// ── evaluateExecuteIf ──

func TestEvaluateExecuteIf(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		want      bool
	}{
		{
			name:      "empty condition returns true",
			condition: "",
			want:      true,
		},
		{
			name:      "exit 0 returns true (condition met)",
			condition: "exit 0",
			want:      true,
		},
		{
			name:      "exit 1 returns false (condition not met)",
			condition: "exit 1",
			want:      false,
		},
		{
			name:      "exit 2 returns false (condition not met)",
			condition: "exit 2",
			want:      false,
		},
		{
			name:      "true test returns true",
			condition: "[ 0 = 0 ]",
			want:      true,
		},
		{
			name:      "false test returns false",
			condition: "[ 0 = 1 ]",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateExecuteIf(tt.condition, "")
			if got != tt.want {
				t.Errorf("condition %q: got %v, want %v", tt.condition, got, tt.want)
			}
		})
	}
}

// ── buildStepVars ──

func TestBuildStepVars(t *testing.T) {
	t.Run("raw output only", func(t *testing.T) {
		results := map[string]*StepResult{
			"explore": {Output: "some text"},
		}
		vars := buildStepVars(results)
		if vars["step.explore"] != "some text" {
			t.Errorf("expected raw output, got %q", vars["step.explore"])
		}
	})

	t.Run("parsed JSON fields included", func(t *testing.T) {
		results := map[string]*StepResult{
			"plan": {
				Output: `{"display":"plan summary","result":"details"}`,
				Parsed: map[string]any{"display": "plan summary", "result": "details"},
			},
		}
		vars := buildStepVars(results)
		if vars["step.plan.display"] != "plan summary" {
			t.Errorf("expected 'plan summary', got %q", vars["step.plan.display"])
		}
	})

	t.Run("step params included", func(t *testing.T) {
		results := map[string]*StepResult{
			"explore": {
				Output: "text",
				Params: map[string]string{"prompt": "user request"},
			},
		}
		vars := buildStepVars(results)
		if vars["step.explore.prompt"] != "user request" {
			t.Errorf("expected 'user request', got %q", vars["step.explore.prompt"])
		}
	})

	t.Run("nil parsed means no JSON vars", func(t *testing.T) {
		results := map[string]*StepResult{
			"plan": {Output: "not json", Parsed: nil},
		}
		vars := buildStepVars(results)
		if _, ok := vars["step.plan.display"]; ok {
			t.Error("expected no step.plan.display when Parsed is nil")
		}
	})
}

// ── resolveBashStepTimeout ──

func TestResolveBashStepTimeout(t *testing.T) {
	ip := func(v int) *int { return &v }
	cfg := BashStepTimeouts{Default: 300 * time.Second, Max: 600 * time.Second}

	cases := []struct {
		name string
		step *int
		cfg  BashStepTimeouts
		want time.Duration
	}{
		{"absent override uses default", nil, cfg, 300 * time.Second},
		{"positive override wins", ip(30), cfg, 30 * time.Second},
		{"zero override falls through to default", ip(0), cfg, 300 * time.Second},
		{"negative override falls through to default", ip(-5), cfg, 300 * time.Second},
		{"override clamped to max", ip(9999), cfg, 600 * time.Second},
		{"default clamped to max when default > max", nil,
			BashStepTimeouts{Default: 900 * time.Second, Max: 600 * time.Second},
			600 * time.Second},
		{"zero max disables cap", ip(9999),
			BashStepTimeouts{Default: 300 * time.Second, Max: 0},
			9999 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveBashStepTimeout(tc.step, tc.cfg)
			if got != tc.want {
				t.Errorf("resolveBashStepTimeout(%v, %+v) = %v, want %v", tc.step, tc.cfg, got, tc.want)
			}
		})
	}
}

// ── validateWorkflow: timeout_sec ──

func TestValidateWorkflowTimeoutSec(t *testing.T) {
	ip := func(v int) *int { return &v }

	cases := []struct {
		name    string
		pf      WorkflowDef
		wantErr string
	}{
		{
			name: "positive timeout_sec on bash step ok",
			pf: WorkflowDef{
				Name: "w", EntryPoint: StepRef{ID: "s1"},
				Steps: map[string]WorkflowStepDef{
					"s1": {Type: "bash", Command: "true", TimeoutSec: ip(30)},
				},
			},
			wantErr: "",
		},
		{
			name: "absent timeout_sec on bash step ok",
			pf: WorkflowDef{
				Name: "w", EntryPoint: StepRef{ID: "s1"},
				Steps: map[string]WorkflowStepDef{
					"s1": {Type: "bash", Command: "true"},
				},
			},
			wantErr: "",
		},
		{
			name: "zero timeout_sec rejected",
			pf: WorkflowDef{
				Name: "w", EntryPoint: StepRef{ID: "s1"},
				Steps: map[string]WorkflowStepDef{
					"s1": {Type: "bash", Command: "true", TimeoutSec: ip(0)},
				},
			},
			wantErr: "timeout_sec must be > 0",
		},
		{
			name: "negative timeout_sec rejected",
			pf: WorkflowDef{
				Name: "w", EntryPoint: StepRef{ID: "s1"},
				Steps: map[string]WorkflowStepDef{
					"s1": {Type: "bash", Command: "true", TimeoutSec: ip(-1)},
				},
			},
			wantErr: "timeout_sec must be > 0",
		},
		{
			name: "timeout_sec on agent step rejected",
			pf: WorkflowDef{
				Name: "w", EntryPoint: StepRef{ID: "s1"},
				Steps: map[string]WorkflowStepDef{
					"s1": {Type: "agent", Agent: "a", Prompt: "p", TimeoutSec: ip(30)},
				},
			},
			wantErr: "timeout_sec only valid on type='bash'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkflow(&tc.pf)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// ── LoadProjectConfig: bash_step_timeouts block ──

func TestLoadProjectConfigBashStepTimeouts(t *testing.T) {
	writeSettings := func(t *testing.T, path string, cfg configFile) {
		t.Helper()
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	ip := func(v int) *int { return &v }

	expectBounds := func(t *testing.T, got BashStepTimeouts, wantDef, wantMax time.Duration) {
		t.Helper()
		if got.Default != wantDef || got.Max != wantMax {
			t.Errorf("BashStepTimeouts = {Default: %v, Max: %v}, want {%v, %v}",
				got.Default, got.Max, wantDef, wantMax)
		}
	}

	t.Run("absent block falls back to hard-coded defaults", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{Version: CurrentConfigVersion})
		result := LoadProjectConfig(path)
		expectBounds(t, result.BashStepTimeouts, defaultBashStepTimeoutDefault, defaultBashStepTimeoutMax)
	})

	t.Run("valid full block is honored", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:          CurrentConfigVersion,
			BashStepTimeouts: &bashStepTimeoutsFile{DefaultSec: ip(60), MaxSec: ip(120)},
		})
		result := LoadProjectConfig(path)
		expectBounds(t, result.BashStepTimeouts, 60*time.Second, 120*time.Second)
	})

	t.Run("partial default_sec preserves hard-coded max", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:          CurrentConfigVersion,
			BashStepTimeouts: &bashStepTimeoutsFile{DefaultSec: ip(45)},
		})
		result := LoadProjectConfig(path)
		expectBounds(t, result.BashStepTimeouts, 45*time.Second, defaultBashStepTimeoutMax)
	})

	t.Run("zero default_sec ignored", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		// Max must stay >= hard-coded default (300s) so the ignored zero
		// doesn't accidentally trip the default > max inversion branch.
		writeSettings(t, path, configFile{
			Version:          CurrentConfigVersion,
			BashStepTimeouts: &bashStepTimeoutsFile{DefaultSec: ip(0), MaxSec: ip(450)},
		})
		result := LoadProjectConfig(path)
		expectBounds(t, result.BashStepTimeouts, defaultBashStepTimeoutDefault, 450*time.Second)
	})

	t.Run("zero max_sec ignored", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:          CurrentConfigVersion,
			BashStepTimeouts: &bashStepTimeoutsFile{DefaultSec: ip(45), MaxSec: ip(0)},
		})
		result := LoadProjectConfig(path)
		expectBounds(t, result.BashStepTimeouts, 45*time.Second, defaultBashStepTimeoutMax)
	})

	t.Run("inverted bounds revert both fields to hard-coded defaults", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:          CurrentConfigVersion,
			BashStepTimeouts: &bashStepTimeoutsFile{DefaultSec: ip(300), MaxSec: ip(60)},
		})
		result := LoadProjectConfig(path)
		expectBounds(t, result.BashStepTimeouts, defaultBashStepTimeoutDefault, defaultBashStepTimeoutMax)
	})

	t.Run("tool_timeouts and bash_step_timeouts are independent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		writeSettings(t, path, configFile{
			Version:          CurrentConfigVersion,
			ToolTimeouts:     &toolTimeoutsFile{DefaultSec: ip(90), MaxSec: ip(450)},
			BashStepTimeouts: &bashStepTimeoutsFile{DefaultSec: ip(45), MaxSec: ip(90)},
		})
		result := LoadProjectConfig(path)
		if result.ToolTimeouts.Default != 90*time.Second || result.ToolTimeouts.Max != 450*time.Second {
			t.Errorf("tool_timeouts not preserved: %+v", result.ToolTimeouts)
		}
		expectBounds(t, result.BashStepTimeouts, 45*time.Second, 90*time.Second)
	})
}

// ── runBashWithContext: per-step deadline kills process group ──

func TestRunBashWithContextTimeout(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 60s sleep with a 200ms per-step deadline — we want to confirm the
	// deadline fires and the process is killed promptly.
	start := time.Now()
	deadline, deadlineCancel := context.WithTimeout(parent, 200*time.Millisecond)
	defer deadlineCancel()
	out, err := runBashWithContext(deadline, "sleep 60", "", "", nil)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("expected prompt kill after ~200ms, took %v", elapsed)
	}
	if deadline.Err() != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", deadline.Err())
	}
	// runBashWithContext returns ctx.Err() on cancellation; the parent is
	// still alive so the workflow caller can distinguish step-timeout from
	// session-cancel via `parent.Err() == nil`.
	if err != context.DeadlineExceeded {
		t.Errorf("expected err DeadlineExceeded from runBashWithContext, got %v", err)
	}
	if parent.Err() != nil {
		t.Errorf("parent context must not be cancelled by a step deadline, got %v", parent.Err())
	}
	_ = out
}
