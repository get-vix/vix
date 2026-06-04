package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/get-vix/vix/internal/protocol"
)

// hasPendingTodos returns true if any item has a pending or in-progress status.
func hasPendingTodos(todos []protocol.TodoItem) bool {
	for _, t := range todos {
		if t.Status == protocol.TodoPending || t.Status == protocol.TodoInProgress {
			return true
		}
	}
	return false
}

// todoPanelHeight returns the number of terminal lines the panel occupies.
func todoPanelHeight(todos []protocol.TodoItem) int {
	h := len(todos) + 3
	if h < 3 {
		h = 3
	}
	return h
}

// renderTodoPanel builds the todo panel string.
func renderTodoPanel(todos []protocol.TodoItem, width int, s Styles) string {
	borderCharStyle := lipgloss.NewStyle().Foreground(colorSecondary)

	// Count non-completed items.
	pending := 0
	for _, t := range todos {
		if t.Status != protocol.TodoCompleted {
			pending++
		}
	}

	// Custom top border: "╭─ Todos (N/M) ──...──╮"
	title := fmt.Sprintf(" Todos (%d/%d) ", pending, len(todos))
	titleStyle := lipgloss.NewStyle().Foreground(colorSecondary)
	titleRendered := titleStyle.Render(title)
	remainingDashes := width - 3 - lipgloss.Width(titleRendered)
	if remainingDashes < 0 {
		remainingDashes = 0
	}
	topBorder := borderCharStyle.Render("╭─") + titleRendered + borderCharStyle.Render(strings.Repeat("─", remainingDashes)) + borderCharStyle.Render("╮")

	// Build content lines.
	var lines []string
	for _, t := range todos {
		var line string
		switch t.Status {
		case protocol.TodoInProgress:
			bullet := lipgloss.NewStyle().Foreground(colorSecondary).Render("▶ ")
			text := lipgloss.NewStyle().Foreground(colorSecondary).Render(t.Content)
			line = bullet + text
		case protocol.TodoCompleted:
			bullet := lipgloss.NewStyle().Foreground(colorSuccess).Render("✓ ")
			text := s.HistoryPanelStyle.Render(t.Content)
			line = bullet + text
		default: // TodoPending
			bullet := lipgloss.NewStyle().Foreground(colorDim).Render("○ ")
			text := lipgloss.NewStyle().Foreground(colorDim).Render(t.Content)
			line = bullet + text
		}
		line = lipgloss.NewStyle().MaxWidth(width - 4).Render(line)
		lines = append(lines, line)
	}

	content := strings.Join(lines, "\n")

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderTop(false).
		BorderForeground(colorSecondary).
		Width(width).
		Padding(0, 1)

	return topBorder + "\n" + boxStyle.Render(content)
}
