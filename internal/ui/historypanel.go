package ui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// HistoryPanel manages the inline history browser panel state.
type HistoryPanel struct {
	visible   bool
	selected  int // index into entries (0 = oldest)
	offset    int // scroll offset for visible window
	maxHeight int // max visible rows
}

// Open shows the panel and selects the most recent entry (bottom).
func (p *HistoryPanel) Open(entryCount, termHeight int) {
	p.visible = true
	p.maxHeight = entryCount
	if p.maxHeight > 10 {
		p.maxHeight = 10
	}
	if third := termHeight / 3; p.maxHeight > third {
		p.maxHeight = third
	}
	if p.maxHeight < 1 {
		p.maxHeight = 1
	}
	p.selected = entryCount - 1
	p.offset = entryCount - p.maxHeight
	if p.offset < 0 {
		p.offset = 0
	}
}

// Close hides the panel.
func (p *HistoryPanel) Close() {
	p.visible = false
}

// IsVisible returns whether the panel is showing.
func (p *HistoryPanel) IsVisible() bool {
	return p.visible
}

// MoveUp moves the selection toward older entries.
func (p *HistoryPanel) MoveUp() {
	if p.selected > 0 {
		p.selected--
	}
	if p.selected < p.offset {
		p.offset = p.selected
	}
}

// MoveDown moves the selection toward more recent entries.
func (p *HistoryPanel) MoveDown(entryCount int) {
	if p.selected < entryCount-1 {
		p.selected++
	}
	if p.selected >= p.offset+p.maxHeight {
		p.offset = p.selected - p.maxHeight + 1
	}
}

// Height returns the total height consumed by the panel (entries + separator).
func (p *HistoryPanel) Height() int {
	return p.maxHeight + 1
}

// renderHistoryPanel builds the panel string from history entries.
func renderHistoryPanel(entries []string, times []time.Time, panel *HistoryPanel, width int, focused bool, s Styles) string {
	if len(entries) == 0 || !panel.visible {
		return ""
	}

	// Border color based on focus
	var borderColor color.Color
	if focused {
		borderColor = colorSecondary
	} else {
		borderColor = s.ColorBlurBorder
	}
	borderCharStyle := lipgloss.NewStyle().Foreground(borderColor)

	// Custom top border: "╭─ History N/M ──...──╮"
	title := fmt.Sprintf(" History %d/%d ", panel.selected+1, len(entries))
	titleStyle := lipgloss.NewStyle().Foreground(borderColor)
	titleRendered := titleStyle.Render(title)
	titleLen := lipgloss.Width(titleRendered)
	remainingDashes := width - 3 - titleLen
	if remainingDashes < 0 {
		remainingDashes = 0
	}
	topBorder := borderCharStyle.Render("╭─") + titleRendered + borderCharStyle.Render(strings.Repeat("─", remainingDashes)) + borderCharStyle.Render("╮")

	// Build inner content
	var b strings.Builder

	// Visible window
	end := panel.offset + panel.maxHeight
	if end > len(entries) {
		end = len(entries)
	}

	// Format: " ▸ 02 Jan 15:04  entry text"
	//         "   02 Jan 15:04  entry text"
	const timeFormat = "02 Jan 15:04"
	const timePrefixLen = 3 + len(timeFormat) + 2 // arrow/space(3) + time + gap(2)
	maxTextWidth := width - timePrefixLen - 4     // account for border + padding
	if maxTextWidth < 1 {
		maxTextWidth = 1
	}

	for i := panel.offset; i < end; i++ {
		entry := strings.ReplaceAll(entries[i], "\n", " ")
		entry = lipgloss.NewStyle().MaxWidth(maxTextWidth).Render(entry)

		var ts string
		if i < len(times) && !times[i].IsZero() {
			ts = times[i].Format(timeFormat)
		} else {
			ts = strings.Repeat(" ", len(timeFormat))
		}

		if i == panel.selected {
			b.WriteString(historyArrowStyle.Render(" ▸ "))
			b.WriteString(s.HistoryPanelStyle.Render(ts))
			b.WriteString("  ")
			b.WriteString(s.HistorySelectedStyle.Render(entry))
		} else {
			b.WriteString("   ")
			b.WriteString(s.HistoryPanelStyle.Render(ts))
			b.WriteString("  ")
			b.WriteString(s.HistoryPanelStyle.Render(entry))
		}
		if i < end-1 {
			b.WriteByte('\n')
		}
	}

	// Wrap with rounded border (sides + bottom)
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderTop(false).
		BorderForeground(borderColor).
		Width(width).
		Padding(0, 1)

	return topBorder + "\n" + boxStyle.Render(b.String())
}
