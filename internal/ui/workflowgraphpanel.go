package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/get-vix/vix/internal/protocol"
)

// workflowGraphStep holds per-step state for the workflow graph panel.
type workflowGraphStep struct {
	id          string
	idx         int
	explanation string
	success     *bool // nil = pending or in-progress, non-nil = done
	active      bool  // true when this is the currently running step
	durationMs  int64
}

// WorkflowGraphPanel renders a persistent step-list panel during workflow execution.
type WorkflowGraphPanel struct {
	workflowName  string
	steps         []workflowGraphStep
	currentStepID string
	totalSteps    int
	visible       bool
}

// IsVisible returns true when the panel should be shown.
func (p *WorkflowGraphPanel) IsVisible() bool {
	return p.visible
}

// Start initialises the panel for a new workflow run, pre-populating known steps.
func (p *WorkflowGraphPanel) Start(name string, totalSteps int, knownSteps []protocol.WorkflowStepInfo) {
	p.workflowName = name
	p.totalSteps = totalSteps
	p.currentStepID = ""
	p.visible = true

	p.steps = make([]workflowGraphStep, 0, len(knownSteps))
	for i, si := range knownSteps {
		p.steps = append(p.steps, workflowGraphStep{
			id:          si.ID,
			idx:         i + 1,
			explanation: si.Explanation,
		})
	}
}

// StepStart marks the given step as active. If not yet in the list, it is appended.
func (p *WorkflowGraphPanel) StepStart(id string, idx int, explanation string) {
	// Clear the previously active step flag.
	for i := range p.steps {
		p.steps[i].active = false
	}

	// Find existing entry.
	for i := range p.steps {
		if p.steps[i].id == id {
			p.steps[i].active = true
			p.steps[i].idx = idx
			if explanation != "" {
				p.steps[i].explanation = explanation
			}
			p.currentStepID = id
			return
		}
	}

	// Not pre-populated — append dynamically (e.g. branching step).
	p.steps = append(p.steps, workflowGraphStep{
		id:          id,
		idx:         idx,
		explanation: explanation,
		active:      true,
	})
	p.currentStepID = id
}

// StepDone marks a step as completed (success or failure).
func (p *WorkflowGraphPanel) StepDone(id string, success bool, durationMs int64) {
	for i := range p.steps {
		if p.steps[i].id == id {
			v := success
			p.steps[i].success = &v
			p.steps[i].durationMs = durationMs
			p.steps[i].active = false
			break
		}
	}
	if !success {
		p.currentStepID = ""
	}
}

// Reset hides the panel and clears all state.
func (p *WorkflowGraphPanel) Reset() {
	p.visible = false
	p.steps = nil
	p.workflowName = ""
	p.currentStepID = ""
	p.totalSteps = 0
}

// Height returns the number of terminal lines the panel occupies.
func (p *WorkflowGraphPanel) Height() int {
	h := len(p.steps) + 3
	if h < 3 {
		h = 3
	}
	return h
}

// stepLabel builds the display label for a step: "Step <id>: <explanation>" or "Step <id>".
func stepLabel(step workflowGraphStep) string {
	id := step.id
	// Capitalise first letter of id for nicer display.
	if len(id) > 0 {
		id = strings.ToUpper(id[:1]) + id[1:]
	}
	if step.explanation != "" {
		return fmt.Sprintf("Step %s: %s", id, step.explanation)
	}
	return fmt.Sprintf("Step %s", id)
}

// renderWorkflowGraphPanel builds the panel string.
func renderWorkflowGraphPanel(panel *WorkflowGraphPanel, width int, s Styles) string {
	borderCharStyle := lipgloss.NewStyle().Foreground(colorSecondary)

	// Custom top border: "╭─ Workflow: <name> (N/M) ──...──╮"
	title := fmt.Sprintf(" Workflow: %s (%d/%d) ", panel.workflowName, len(panel.steps), panel.totalSteps)
	titleStyle := lipgloss.NewStyle().Foreground(colorSecondary)
	titleRendered := titleStyle.Render(title)
	remainingDashes := width - 3 - lipgloss.Width(titleRendered)
	if remainingDashes < 0 {
		remainingDashes = 0
	}
	topBorder := borderCharStyle.Render("╭─") + titleRendered + borderCharStyle.Render(strings.Repeat("─", remainingDashes)) + borderCharStyle.Render("╮")

	// Build content lines — all steps are always displayed.
	var lines []string
	for _, step := range panel.steps {
		label := stepLabel(step)

		var line string
		switch {
		case step.active:
			// Currently running step: secondary color, single ▶ arrow, no leading ▸
			bullet := lipgloss.NewStyle().Foreground(colorSecondary).Render("▶ ")
			text := lipgloss.NewStyle().Foreground(colorSecondary).Render(label)
			line = bullet + text

		case step.success == nil:
			// Pending (not yet started)
			bullet := lipgloss.NewStyle().Foreground(colorDim).Render("○ ")
			text := lipgloss.NewStyle().Foreground(colorDim).Render(label)
			line = bullet + text

		case *step.success:
			// Success
			bullet := lipgloss.NewStyle().Foreground(colorSuccess).Render("✓ ")
			text := s.HistoryPanelStyle.Render(label)
			dur := lipgloss.NewStyle().Foreground(colorDim).Render(fmt.Sprintf(" (%.1fs)", float64(step.durationMs)/1000))
			line = bullet + text + dur

		default:
			// Failure
			bullet := lipgloss.NewStyle().Foreground(colorError).Render("✗ ")
			text := lipgloss.NewStyle().Foreground(colorError).Render(label)
			dur := lipgloss.NewStyle().Foreground(colorDim).Render(fmt.Sprintf(" (%.1fs)", float64(step.durationMs)/1000))
			line = bullet + text + dur
		}

		line = lipgloss.NewStyle().MaxWidth(width - 4).Render(line)
		lines = append(lines, line)
	}

	content := strings.Join(lines, "\n")

	// Wrap with rounded border (sides + bottom only — top is the custom border above)
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderTop(false).
		BorderForeground(colorSecondary).
		Width(width).
		Padding(0, 1)

	return topBorder + "\n" + boxStyle.Render(content)
}
