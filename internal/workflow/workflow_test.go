package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func minimalDef(name string) *Def {
	return &Def{
		Name:       name,
		EntryPoint: StepRef{ID: "s"},
		Steps:      map[string]StepDef{"s": {Type: "bash", Command: "true"}},
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Def)
		wantErr bool
	}{
		{"valid bash step", func(*Def) {}, false},
		{"missing name", func(d *Def) { d.Name = "" }, true},
		{"no steps", func(d *Def) { d.Steps = nil }, true},
		{"missing entry_point", func(d *Def) { d.EntryPoint = StepRef{} }, true},
		{"entry_point unknown step", func(d *Def) { d.EntryPoint = StepRef{ID: "nope"} }, true},
		{"step missing type", func(d *Def) { d.Steps["s"] = StepDef{Command: "true"} }, true},
		{"unknown step type", func(d *Def) { d.Steps["s"] = StepDef{Type: "magic"} }, true},
		{"bash without command", func(d *Def) { d.Steps["s"] = StepDef{Type: "bash"} }, true},
		{"next_step unknown", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "bash", Command: "true", NextSteps: []StepRef{{ID: "ghost"}}}
		}, true},
		{"agent without prompt", func(d *Def) { d.Steps["s"] = StepDef{Type: "agent", Agent: "general"} }, true},
		{"valid agent step", func(d *Def) { d.Steps["s"] = StepDef{Type: "agent", Agent: "general", Prompt: "go"} }, false},
		{"budget on_exceeded unknown", func(d *Def) { d.Budget = &Budget{OnExceeded: &StepRef{ID: "nope"}} }, true},
		{"budget negative", func(d *Def) { d.Budget = &Budget{MaxIterations: -1} }, true},

		// if-node validation
		{"valid if with then+else", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "if", Condition: "true", Then: &StepRef{ID: "a"}, Else: &StepRef{ID: "b"}}
			d.Steps["a"] = StepDef{Type: "bash", Command: "true"}
			d.Steps["b"] = StepDef{Type: "bash", Command: "true"}
		}, false},
		{"valid if with then only", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "if", Condition: "true", Then: &StepRef{ID: "a"}}
			d.Steps["a"] = StepDef{Type: "bash", Command: "true"}
		}, false},
		{"valid if then=stop", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "if", Condition: "true", Then: &StepRef{ID: "stop"}}
		}, false},
		{"if missing condition", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "if", Then: &StepRef{ID: "s"}}
		}, true},
		{"if missing then", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "if", Condition: "true"}
		}, true},
		{"if then unknown", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "if", Condition: "true", Then: &StepRef{ID: "ghost"}}
		}, true},
		{"if else unknown", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "if", Condition: "true", Then: &StepRef{ID: "s"}, Else: &StepRef{ID: "ghost"}}
		}, true},
		{"if with prompt rejected", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "if", Condition: "true", Then: &StepRef{ID: "s"}, Prompt: "x"}
		}, true},

		// fan_out / fan_in validation
		{"valid fan_out+fan_in pair", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "fan_out", Over: "$(step.x)", As: "item", BarrierID: "B", Branch: &StepRef{ID: "w"}, NextSteps: []StepRef{{ID: "j"}}}
			d.Steps["w"] = StepDef{Type: "bash", Command: "true"}
			d.Steps["j"] = StepDef{Type: "fan_in", BarrierID: "B", As: "results"}
		}, false},
		{"fan_out missing over", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "fan_out", As: "item", BarrierID: "B", Branch: &StepRef{ID: "s"}}
			d.Steps["j"] = StepDef{Type: "fan_in", BarrierID: "B", As: "r"}
		}, true},
		{"fan_out missing branch", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "fan_out", Over: "$(x)", As: "item", BarrierID: "B"}
			d.Steps["j"] = StepDef{Type: "fan_in", BarrierID: "B", As: "r"}
		}, true},
		{"fan_out branch unknown", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "fan_out", Over: "$(x)", As: "item", BarrierID: "B", Branch: &StepRef{ID: "ghost"}}
			d.Steps["j"] = StepDef{Type: "fan_in", BarrierID: "B", As: "r"}
		}, true},
		{"fan_out without matching fan_in", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "fan_out", Over: "$(x)", As: "item", BarrierID: "B", Branch: &StepRef{ID: "s"}}
		}, true},
		{"fan_in without matching fan_out", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "fan_in", BarrierID: "B", As: "r"}
		}, true},
		{"fan_in bad on_branch_error", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "fan_out", Over: "$(x)", As: "item", BarrierID: "B", Branch: &StepRef{ID: "w"}}
			d.Steps["w"] = StepDef{Type: "bash", Command: "true"}
			d.Steps["j"] = StepDef{Type: "fan_in", BarrierID: "B", As: "r", OnBranchError: "explode"}
		}, true},
		{"nested fan_out rejected", func(d *Def) {
			d.Steps["s"] = StepDef{Type: "fan_out", Over: "$(x)", As: "item", BarrierID: "B", Branch: &StepRef{ID: "inner"}, NextSteps: []StepRef{{ID: "j"}}}
			d.Steps["inner"] = StepDef{Type: "fan_out", Over: "$(y)", As: "z", BarrierID: "C", Branch: &StepRef{ID: "w"}, NextSteps: []StepRef{{ID: "jc"}}}
			d.Steps["w"] = StepDef{Type: "bash", Command: "true"}
			d.Steps["j"] = StepDef{Type: "fan_in", BarrierID: "B", As: "r"}
			d.Steps["jc"] = StepDef{Type: "fan_in", BarrierID: "C", As: "rc"}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := minimalDef("wf")
			tc.mutate(d)
			err := Validate(d)
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		if got := Load(""); got != nil {
			t.Fatalf("Load(\"\") = %v, want nil", got)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if got := Load(filepath.Join(t.TempDir(), "nope.json")); got != nil {
			t.Fatalf("Load(missing) = %v, want nil", got)
		}
	})
	t.Run("valid + skip invalid", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "workflow.json")
		// One valid workflow and one invalid (no steps); only the valid one loads.
		body := `{"workflows":[
			{"name":"good","entry_point":{"id":"s"},"steps":{"s":{"type":"bash","command":"true"}}},
			{"name":"bad","entry_point":{"id":"s"},"steps":{}}
		]}`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		got := Load(path)
		if len(got) != 1 || got[0].Name != "good" {
			t.Fatalf("Load() = %+v, want one workflow named good", got)
		}
	})
	t.Run("duplicate names disambiguated", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "workflow.json")
		body := `{"workflows":[
			{"name":"dup","entry_point":{"id":"s"},"steps":{"s":{"type":"bash","command":"true"}}},
			{"name":"dup","entry_point":{"id":"s"},"steps":{"s":{"type":"bash","command":"true"}}}
		]}`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		got := Load(path)
		if len(got) != 2 || got[0].Name != "dup (1)" || got[1].Name != "dup (2)" {
			t.Fatalf("Load() names = %q, %q; want \"dup (1)\", \"dup (2)\"", got[0].Name, got[1].Name)
		}
	})
}
