package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/get-vix/vix/internal/protocol"
)

// rightPanelMode is the display mode of the right panel.
type rightPanelMode int

const (
	rpModeWorkflow rightPanelMode = iota // live workflow step progress
	rpModeTodos                          // pending todo list
)

// RightPanelAction is the action returned by HandleKey.
type RightPanelAction int

const (
	rpActionNone  RightPanelAction = iota
	rpActionClose                  // close the panel
)

// RightPanel is a full-height sidebar on the right side of the screen that
// shows live workflow-step progress or the pending todo list. API keys are
// managed in the Models tab (F3), not here.
type RightPanel struct {
	visible bool
	mode    rightPanelMode
	height  int
}

// panelWidth is the fixed display width of the right panel.
const panelWidth = 42

// PanelWidth returns the fixed width of the right panel.
func (rp *RightPanel) PanelWidth() int { return panelWidth }

// IsVisible returns true when the panel is open.
func (rp *RightPanel) IsVisible() bool { return rp.visible }

// Close hides the panel.
func (rp *RightPanel) Close() { rp.visible = false }

// OpenWorkflow opens the panel in workflow-progress mode.
func (rp *RightPanel) OpenWorkflow(height int) {
	rp.visible = true
	rp.mode = rpModeWorkflow
	rp.height = height
}

// OpenTodos opens the panel in todo-list mode.
func (rp *RightPanel) OpenTodos(height int) {
	rp.visible = true
	rp.mode = rpModeTodos
	rp.height = height
}

// HandleKey processes a key press and returns the resulting action. The panel is
// read-only (workflow progress / todos); only ESC is actionable, which closes it.
func (rp *RightPanel) HandleKey(msg tea.KeyPressMsg) RightPanelAction {
	if msg.String() == "esc" {
		return rpActionClose
	}
	return rpActionNone
}

// View renders the right panel as a bordered, full-height string.
// focused controls whether the panel border uses the focus color.
// wfp is the workflow graph panel (used when mode is rpModeWorkflow).
// todos is the current todo list (used in rpModeTodos and appended below steps in rpModeWorkflow).
func (rp *RightPanel) View(height int, s Styles, focused bool, wfp *WorkflowGraphPanel, todos []protocol.TodoItem) string {
	innerWidth := panelWidth - 4 // border (2) + padding (2)

	var lines []string

	switch rp.mode {
	case rpModeWorkflow:
		if wfp != nil {
			title := lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Width(innerWidth).Render("Workflow: " + wfp.workflowName)
			sep := lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render(strings.Repeat("─", innerWidth))
			lines = append(lines, title, sep)
			for _, step := range wfp.steps {
				lines = append(lines, renderTodoOrStepLine(stepLabel(step), stepStatus(step), innerWidth))
			}
		}
		if hasPendingTodos(todos) {
			lines = append(lines, "", lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("Todos"))
			lines = append(lines, lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render(strings.Repeat("─", innerWidth)))
			for _, t := range todos {
				lines = append(lines, renderTodoOrStepLine(t.Content, string(t.Status), innerWidth))
			}
		}

	case rpModeTodos:
		title := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Width(innerWidth).Render("Todos")
		sep := lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth).Render(strings.Repeat("─", innerWidth))
		lines = append(lines, title, sep)
		for _, t := range todos {
			lines = append(lines, renderTodoOrStepLine(t.Content, string(t.Status), innerWidth))
		}
	}

	// Pad to fill height (subtract 2 for border top+bottom).
	// Each element in lines may contain embedded newlines from word-wrapping, so
	// we count actual terminal lines rather than slice elements.
	innerHeight := height - 2
	if innerHeight < 1 {
		innerHeight = 1
	}
	termLines := func(ss []string) int {
		n := 0
		for _, s := range ss {
			n += strings.Count(s, "\n") + 1
		}
		return n
	}
	for termLines(lines) < innerHeight {
		lines = append(lines, "")
	}
	// Trim from the end until we are within innerHeight terminal lines.
	for len(lines) > 0 && termLines(lines) > innerHeight {
		lines = lines[:len(lines)-1]
	}

	content := strings.Join(lines, "\n")
	panelStyle := s.RightPanelStyle
	if focused {
		panelStyle = panelStyle.BorderForeground(s.ColorWhite)
	}
	box := panelStyle.Width(panelWidth).Height(height).Render(content)
	return box
}

// stepStatus converts a workflowGraphStep into a string status token shared with renderTodoOrStepLine.
func stepStatus(step workflowGraphStep) string {
	switch {
	case step.active:
		return "in_progress"
	case step.success == nil:
		return "pending"
	case *step.success:
		return "completed"
	default:
		return "failed"
	}
}

// renderTodoOrStepLine renders a single labelled item with a status icon, wrapped to innerWidth.
// status values: "pending", "in_progress", "completed", "failed".
func renderTodoOrStepLine(label, status string, innerWidth int) string {
	var bullet, text string
	switch status {
	case "in_progress":
		bullet = lipgloss.NewStyle().Foreground(colorSecondary).Render("▶ ")
		text = lipgloss.NewStyle().Foreground(colorSecondary).Width(innerWidth - 2).Render(label)
	case "completed":
		bullet = lipgloss.NewStyle().Foreground(colorSuccess).Render("✓ ")
		text = lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth - 2).Render(label)
	case "failed":
		bullet = lipgloss.NewStyle().Foreground(colorError).Render("✗ ")
		text = lipgloss.NewStyle().Foreground(colorError).Width(innerWidth - 2).Render(label)
	default: // pending
		bullet = lipgloss.NewStyle().Foreground(colorDim).Render("○ ")
		text = lipgloss.NewStyle().Foreground(colorDim).Width(innerWidth - 2).Render(label)
	}
	// Indent continuation lines to align under the text, not the bullet.
	textLines := strings.Split(text, "\n")
	result := bullet + textLines[0]
	for _, l := range textLines[1:] {
		result += "\n  " + l
	}
	return result
}
