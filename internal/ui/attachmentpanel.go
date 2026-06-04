package ui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/get-vix/vix/internal/protocol"
)

// AttachmentPanel manages a list of image attachments shown above the input box.
type AttachmentPanel struct {
	attachments []protocol.Attachment
	selected    int
	focused     bool
}

// Add appends an attachment and selects it.
func (p *AttachmentPanel) Add(att protocol.Attachment) {
	p.attachments = append(p.attachments, att)
	p.selected = len(p.attachments) - 1
}

// Remove removes the attachment at idx and adjusts selection.
func (p *AttachmentPanel) Remove(idx int) {
	if idx < 0 || idx >= len(p.attachments) {
		return
	}
	p.attachments = append(p.attachments[:idx], p.attachments[idx+1:]...)
	if p.selected >= len(p.attachments) {
		p.selected = len(p.attachments) - 1
	}
	if len(p.attachments) == 0 {
		p.focused = false
	}
}

// Clear drains and returns all attachments.
func (p *AttachmentPanel) Clear() []protocol.Attachment {
	out := p.attachments
	p.attachments = nil
	p.selected = 0
	p.focused = false
	return out
}

// MoveUp moves selection toward earlier attachments.
func (p *AttachmentPanel) MoveUp() {
	if p.selected > 0 {
		p.selected--
	}
}

// MoveDown moves selection toward later attachments.
func (p *AttachmentPanel) MoveDown() {
	if p.selected < len(p.attachments)-1 {
		p.selected++
	}
}

// IsVisible returns true when there are attachments.
func (p *AttachmentPanel) IsVisible() bool {
	return len(p.attachments) > 0
}

// IsFocused returns whether the panel currently intercepts keys.
func (p *AttachmentPanel) IsFocused() bool {
	return p.focused
}

// Focus gives keyboard focus to the panel.
func (p *AttachmentPanel) Focus() {
	if len(p.attachments) > 0 {
		p.focused = true
	}
}

// Unfocus returns keyboard focus to the input.
func (p *AttachmentPanel) Unfocus() {
	p.focused = false
}

// Count returns the number of attachments.
func (p *AttachmentPanel) Count() int {
	return len(p.attachments)
}

// renderAttachmentPanel builds the panel string.
func renderAttachmentPanel(panel *AttachmentPanel, width int, s Styles) string {
	if len(panel.attachments) == 0 {
		return ""
	}

	// Border color based on focus
	var borderColor color.Color
	if panel.focused {
		borderColor = colorSecondary
	} else {
		borderColor = s.ColorBlurBorder
	}
	borderCharStyle := lipgloss.NewStyle().Foreground(borderColor)

	// Custom top border: "╭─ Attachments N/M ──...──╮"
	title := fmt.Sprintf(" Attachments %d/%d ", panel.selected+1, len(panel.attachments))
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

	for i, att := range panel.attachments {
		filename := filepath.Base(att.Path)
		label := fmt.Sprintf("Image #%d: %s", i+1, filename)
		maxTextWidth := width - 8 // arrow/space prefix + border + padding
		if maxTextWidth < 1 {
			maxTextWidth = 1
		}
		label = lipgloss.NewStyle().MaxWidth(maxTextWidth).Render(label)

		if panel.focused && i == panel.selected {
			b.WriteString(historyArrowStyle.Render(" ▸ "))
			b.WriteString(s.HistorySelectedStyle.Render(label))
		} else {
			b.WriteString("   ")
			b.WriteString(s.HistoryPanelStyle.Render(label))
		}
		if i < len(panel.attachments)-1 {
			b.WriteByte('\n')
		}
	}

	// Help text
	b.WriteByte('\n')
	helpColor := s.ColorDimGray
	if panel.focused {
		helpColor = s.ColorWhite
	}
	helpStyle := lipgloss.NewStyle().Foreground(helpColor)
	b.WriteString(helpStyle.Render("  Tab: back to input | ↑↓: navigate | Del: remove"))

	// Wrap with rounded border (sides + bottom)
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderTop(false).
		BorderForeground(borderColor).
		Width(width).
		Padding(0, 1)

	return topBorder + "\n" + boxStyle.Render(b.String())
}
