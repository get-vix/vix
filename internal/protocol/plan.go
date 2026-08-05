package protocol

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TaskStatus represents the state of a plan task.
type TaskStatus int

const (
	TaskPending TaskStatus = iota
	TaskInProgress
	TaskCompleted
	TaskFailed
)

// PlanTask represents a single step in a plan.
type PlanTask struct {
	ID          int        `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Substeps    []string   `json:"substeps,omitempty"`
	Status      TaskStatus `json:"status"`
	Result      string     `json:"result,omitempty"`
}

// Plan represents a structured multi-step plan.
type Plan struct {
	Name         string      `json:"name"`
	Context      string      `json:"context"`
	Architecture string      `json:"architecture,omitempty"`
	Files        []string    `json:"files,omitempty"`
	Risks        string      `json:"risks,omitempty"`
	Tasks        []*PlanTask `json:"tasks"`
	CurrentIdx   int         `json:"current_idx"`
}

// CurrentTask returns the currently executing task, or nil.
func (p *Plan) CurrentTask() *PlanTask {
	if p.CurrentIdx < 0 || p.CurrentIdx >= len(p.Tasks) {
		return nil
	}
	return p.Tasks[p.CurrentIdx]
}

// AllDone returns true if all tasks are completed or failed.
func (p *Plan) AllDone() bool {
	for _, t := range p.Tasks {
		if t.Status == TaskPending || t.Status == TaskInProgress {
			return false
		}
	}
	return true
}

// AdvanceToNextPending moves CurrentIdx to the next pending task.
// Returns false if no pending tasks remain.
func (p *Plan) AdvanceToNextPending() bool {
	for i, t := range p.Tasks {
		if t.Status == TaskPending {
			p.CurrentIdx = i
			return true
		}
	}
	return false
}

// PlanActionType represents user actions on a proposed plan.
type PlanActionType int

const (
	PlanApprove PlanActionType = iota
	PlanReject
	PlanModify
)

// PlanAction carries a user decision about a plan.
type PlanAction struct {
	Type PlanActionType
	Text string // only used for PlanModify
}

// FormatPlanAsMarkdown renders a plan as readable markdown.
func FormatPlanAsMarkdown(plan *Plan) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", plan.Name)
	fmt.Fprintf(&sb, "## Context\n\n%s\n\n", plan.Context)

	if plan.Architecture != "" {
		fmt.Fprintf(&sb, "## Architecture\n\n%s\n\n", plan.Architecture)
	}

	if len(plan.Files) > 0 {
		sb.WriteString("## Files\n\n")
		for _, f := range plan.Files {
			fmt.Fprintf(&sb, "- `%s`\n", f)
		}
		sb.WriteString("\n")
	}

	if plan.Risks != "" {
		fmt.Fprintf(&sb, "## Risks\n\n%s\n\n", plan.Risks)
	}

	sb.WriteString("## Steps\n\n")
	for _, t := range plan.Tasks {
		fmt.Fprintf(&sb, "### %d. %s\n\n%s\n", t.ID, t.Title, t.Description)
		for _, sub := range t.Substeps {
			fmt.Fprintf(&sb, "- %s\n", sub)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// SavePlanToFile writes the plan as markdown to <plansDir>/YYYY-MM-DD_HHMMSS.md.
// plansDir is typically the thread's paths.Plans() directory.
func SavePlanToFile(plan *Plan, plansDir string) error {
	if plansDir == "" {
		plansDir = filepath.Join(".vix", "plans")
	}
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		return fmt.Errorf("create plans dir: %w", err)
	}
	dir := plansDir

	filename := time.Now().Format("2006-01-02_150405") + ".md"
	path := filepath.Join(dir, filename)

	content := FormatPlanAsMarkdown(plan)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write plan file: %w", err)
	}

	return nil
}
