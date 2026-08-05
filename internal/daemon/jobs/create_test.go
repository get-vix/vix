package jobs

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return NewStore(filepath.Join(dir, "jobs"))
}

func noopRunner(context.Context, Spec, string) RunResult { return RunResult{Status: StatusOK} }

func TestSaveSpecRoundTrip(t *testing.T) {
	st := newTestStore(t)
	if st.SpecExists("j") {
		t.Fatal("spec should not exist before save")
	}
	s := validSpec("j")
	if err := st.SaveSpec(s); err != nil {
		t.Fatalf("SaveSpec: %v", err)
	}
	if !st.SpecExists("j") {
		t.Fatal("SpecExists should be true after save")
	}
	specs, invalid := st.LoadSpecs()
	if len(invalid) != 0 {
		t.Fatalf("unexpected invalid specs: %v", invalid)
	}
	got, ok := specs["j"]
	if !ok {
		t.Fatal("loaded specs missing id j")
	}
	if got.Prompt != s.Prompt || got.CWD != s.CWD {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestSaveSpecNoDir(t *testing.T) {
	if err := NewStore("").SaveSpec(validSpec("j")); err == nil {
		t.Fatal("want error saving with no spec dir")
	}
}

func TestSchedulerCreateJob(t *testing.T) {
	st := newTestStore(t)
	sch := NewScheduler(st, noopRunner, nil, nil, 1)

	if err := sch.CreateJob(validSpec("alpha")); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	// Reconciled synchronously, so a snapshot right after includes it.
	snap := sch.Snapshot()
	if len(snap) != 1 || snap[0].ID != "alpha" {
		t.Fatalf("snapshot = %+v, want a single job alpha", snap)
	}

	if err := sch.CreateJob(validSpec("alpha")); err == nil {
		t.Fatal("want error on duplicate id")
	}

	bad := validSpec("beta")
	bad.Prompt = " "
	if err := sch.CreateJob(bad); err == nil {
		t.Fatal("want error on invalid spec")
	}
	if st.SpecExists("beta") {
		t.Fatal("invalid spec should not have been persisted")
	}
}

func TestSchedulerCreateJobInlineWorkflow(t *testing.T) {
	st := newTestStore(t)
	sch := NewScheduler(st, noopRunner, nil, nil, 1)

	s := validSpec("with-wf")
	s.Workflow = validInlineWorkflow()
	if err := sch.CreateJob(s); err != nil {
		t.Fatalf("CreateJob inline workflow: %v", err)
	}
	snap := sch.Snapshot()
	if len(snap) != 1 || !snap[0].WorkflowInline {
		t.Fatalf("snapshot = %+v, want workflow_inline=true", snap)
	}
}

func TestUniqueIDAndSlugify(t *testing.T) {
	cases := map[string]string{
		"Plan GitHub issues (get-vix/vix)": "plan-github-issues-get-vix-vix",
		"Market research":                  "market-research",
		"!!!":                              "",
		"  trim  ":                         "trim",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}

	st := newTestStore(t)
	sch := NewScheduler(st, noopRunner, nil, nil, 1)

	id1 := sch.UniqueID("Market research")
	if id1 != "market-research" {
		t.Fatalf("UniqueID = %q", id1)
	}
	if err := sch.CreateJob(validSpec(id1)); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if id2 := sch.UniqueID("Market research"); id2 != "market-research-2" {
		t.Fatalf("UniqueID after collision = %q, want market-research-2", id2)
	}

	if got := sch.UniqueID("!!!"); got != "job" {
		t.Fatalf("UniqueID empty slug = %q, want job", got)
	}
}
