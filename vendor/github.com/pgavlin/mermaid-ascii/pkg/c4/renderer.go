package c4

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/canvas"
	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const (
	boxPadding = 4 // 2 padding on each side
)

// Render renders a C4 diagram as ASCII/Unicode text.
func Render(d *C4Diagram, config *diagram.Config) (string, error) {
	if d == nil {
		return "", fmt.Errorf("nil diagram")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	chars := canvas.Select(config.UseAscii)

	var lines []string

	// Render top-level elements
	for _, elem := range d.Elements {
		lines = append(lines, renderElement(elem, chars, 0)...)
		lines = append(lines, "")
	}

	// Render boundaries
	for _, b := range d.Boundaries {
		lines = append(lines, renderBoundary(b, chars, 0)...)
		lines = append(lines, "")
	}

	// Render relationships
	if len(d.Relationships) > 0 {
		for _, rel := range d.Relationships {
			lines = append(lines, renderRelationship(rel))
		}
	}

	return strings.Join(lines, "\n") + "\n", nil
}

func renderElement(elem *Element, chars canvas.BoxChars, indent int) []string {
	prefix := strings.Repeat("  ", indent)
	label := elem.Label
	details := ""
	if elem.Technology != "" {
		details = fmt.Sprintf("[%s]", elem.Technology)
	}
	if elem.Description != "" {
		if details != "" {
			details += " " + elem.Description
		} else {
			details = elem.Description
		}
	}

	kindLabel := fmt.Sprintf("<<%s>>", elem.Kind)
	contentLines := []string{kindLabel, label}
	if details != "" {
		contentLines = append(contentLines, details)
	}

	maxWidth := 0
	for _, cl := range contentLines {
		if len(cl) > maxWidth {
			maxWidth = len(cl)
		}
	}
	boxWidth := maxWidth + boxPadding // 2 padding on each side

	var result []string
	result = append(result, prefix+chars.TopBorder(boxWidth))
	for _, cl := range contentLines {
		result = append(result, prefix+chars.CenterText(cl, boxWidth))
	}
	result = append(result, prefix+chars.BottomBorder(boxWidth))
	return result
}

func renderBoundary(b *Boundary, chars canvas.BoxChars, indent int) []string {
	prefix := strings.Repeat("  ", indent)

	var inner []string
	for _, elem := range b.Elements {
		inner = append(inner, renderElement(elem, chars, indent+1)...)
	}
	for _, sub := range b.Boundaries {
		inner = append(inner, renderBoundary(sub, chars, indent+1)...)
	}

	// Find max width
	maxWidth := len(b.Label) + boxPadding
	for _, l := range inner {
		trimmed := strings.TrimPrefix(l, prefix)
		if len(trimmed)+2 > maxWidth {
			maxWidth = len(trimmed) + 2
		}
	}

	var result []string
	result = append(result, prefix+chars.TopBorder(maxWidth))
	result = append(result, prefix+chars.CenterText(b.Label, maxWidth))
	result = append(result, prefix+string(chars.Vertical)+strings.Repeat(string(chars.Horizontal), maxWidth)+string(chars.Vertical))

	for _, l := range inner {
		// Pad inner lines to boundary width
		stripped := strings.TrimPrefix(l, prefix)
		padNeeded := maxWidth - len(stripped)
		if padNeeded < 0 {
			padNeeded = 0
		}
		result = append(result, prefix+string(chars.Vertical)+stripped+strings.Repeat(" ", padNeeded)+string(chars.Vertical))
	}

	result = append(result, prefix+chars.BottomBorder(maxWidth))
	return result
}

func renderRelationship(rel *Relationship) string {
	label := rel.Label
	if rel.Technology != "" {
		label += fmt.Sprintf(" [%s]", rel.Technology)
	}
	return fmt.Sprintf("%s --> %s : %s", rel.From, rel.To, label)
}
