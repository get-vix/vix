package blockdiagram

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/canvas"
	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const defaultCellWidth = 12

// Render renders a block diagram as ASCII/Unicode text.
func Render(d *BlockDiagram, config *diagram.Config) (string, error) {
	if d == nil {
		return "", fmt.Errorf("nil diagram")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	chars := canvas.Select(config.UseAscii)

	var lines []string
	lines = append(lines, renderBlocks(d.Blocks, d.Columns, chars, 0)...)

	return strings.Join(lines, "\n") + "\n", nil
}

func renderBlocks(blocks []*Block, columns int, chars canvas.BoxChars, indent int) []string {
	if columns <= 0 {
		columns = 1
	}
	prefix := strings.Repeat("  ", indent)

	var allLines []string
	row := make([]*Block, 0, columns)
	col := 0

	for _, b := range blocks {
		row = append(row, b)
		col += b.Span
		if col >= columns {
			allLines = append(allLines, renderRow(row, columns, chars, prefix)...)
			row = row[:0]
			col = 0
		}
	}
	if len(row) > 0 {
		allLines = append(allLines, renderRow(row, columns, chars, prefix)...)
	}

	return allLines
}

func renderRow(blocks []*Block, _ int, chars canvas.BoxChars, prefix string) []string {
	// Calculate cell width
	cellWidth := defaultCellWidth

	// Find max label width
	for _, b := range blocks {
		lw := len(b.Label) + 4
		spanWidth := cellWidth * b.Span
		if lw > spanWidth {
			cellWidth = (lw + b.Span - 1) / b.Span
			if cellWidth < defaultCellWidth {
				cellWidth = defaultCellWidth
			}
		}
	}

	// Build top border line
	var topLine, midLine, botLine strings.Builder
	topLine.WriteString(prefix)
	midLine.WriteString(prefix)
	botLine.WriteString(prefix)

	for i, b := range blocks {
		w := cellWidth*b.Span - 2
		if w < len(b.Label)+2 {
			w = len(b.Label) + 2
		}

		if i > 0 {
			topLine.WriteString("  ")
			midLine.WriteString("  ")
			botLine.WriteString("  ")
		}

		if b.IsSpace {
			// Spacer block: render as empty space
			totalW := w + 2 // account for the border characters we skip
			topLine.WriteString(strings.Repeat(" ", totalW))
			midLine.WriteString(strings.Repeat(" ", totalW))
			botLine.WriteString(strings.Repeat(" ", totalW))
			continue
		}

		topLine.WriteString(chars.TopBorder(w))
		midLine.WriteString(chars.CenterText(b.Label, w))
		botLine.WriteString(chars.BottomBorder(w))
	}

	result := []string{topLine.String(), midLine.String(), botLine.String()}

	// Render children of nested blocks
	for _, b := range blocks {
		if len(b.Children) > 0 {
			cols := b.Columns
			if cols <= 0 {
				cols = 1
			}
			childLines := renderBlocks(b.Children, cols, chars, 1)
			result = append(result, childLines...)
		}
	}

	return result
}
