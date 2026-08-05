package kanban

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const (
	defaultColumnWidth = 20
	cardPadding        = 2
)

// Render renders a Kanban board as ASCII/Unicode text.
func Render(kb *KanbanBoard, config *diagram.Config) (string, error) {
	if kb == nil || len(kb.Columns) == 0 {
		return "", fmt.Errorf("no kanban data")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	useAscii := config.UseAscii

	hChar := "─"
	vChar := "│"
	cornerTL := "┌"
	cornerTR := "┐"
	cornerBL := "└"
	cornerBR := "┘"
	if useAscii {
		hChar = "-"
		vChar = "|"
		cornerTL = "+"
		cornerTR = "+"
		cornerBL = "+"
		cornerBR = "+"
	}

	// Calculate column widths
	colWidths := make([]int, len(kb.Columns))
	for i, col := range kb.Columns {
		w := len(col.Name) + cardPadding*2
		for _, card := range col.Cards {
			cardW := len(card.Title) + cardPadding*2
			if cardW > w {
				w = cardW
			}
		}
		if w < defaultColumnWidth {
			w = defaultColumnWidth
		}
		colWidths[i] = w
	}

	// Find max number of cards
	maxCards := 0
	for _, col := range kb.Columns {
		if len(col.Cards) > maxCards {
			maxCards = len(col.Cards)
		}
	}

	var lines []string

	// Top border
	topLine := ""
	for i, w := range colWidths {
		if i == 0 {
			topLine += cornerTL
		}
		topLine += strings.Repeat(hChar, w)
		if i < len(colWidths)-1 {
			topLine += "┬"
			if useAscii {
				topLine = topLine[:len(topLine)-3] + "+"
			}
		} else {
			topLine += cornerTR
		}
	}
	lines = append(lines, topLine)

	// Column headers
	headerLine := ""
	for i, col := range kb.Columns {
		w := colWidths[i]
		label := col.Name
		if len(label) > w-2 {
			label = label[:w-2]
		}
		pad := w - len(label)
		leftPad := pad / 2
		rightPad := pad - leftPad
		headerLine += vChar + strings.Repeat(" ", leftPad) + label + strings.Repeat(" ", rightPad)
	}
	headerLine += vChar
	lines = append(lines, headerLine)

	// Header separator
	sepLine := ""
	for i, w := range colWidths {
		if i == 0 {
			sepLine += "├"
			if useAscii {
				sepLine = "+"
			}
		}
		sepLine += strings.Repeat(hChar, w)
		if i < len(colWidths)-1 {
			sepLine += "┼"
			if useAscii {
				sepLine = sepLine[:len(sepLine)-3] + "+"
			}
		} else {
			sepLine += "┤"
			if useAscii {
				sepLine = sepLine[:len(sepLine)-3] + "+"
			}
		}
	}
	lines = append(lines, sepLine)

	// Cards
	for cardIdx := 0; cardIdx < maxCards; cardIdx++ {
		cardLine := ""
		for i, col := range kb.Columns {
			w := colWidths[i]
			if cardIdx < len(col.Cards) {
				title := col.Cards[cardIdx].Title
				if len(title) > w-4 {
					title = title[:w-5] + "…"
				}
				pad := w - len(title) - 2
				cardLine += vChar + " " + title + strings.Repeat(" ", pad) + " "
			} else {
				cardLine += vChar + strings.Repeat(" ", w)
			}
		}
		cardLine += vChar
		lines = append(lines, cardLine)
	}

	// Bottom border
	bottomLine := ""
	for i, w := range colWidths {
		if i == 0 {
			bottomLine += cornerBL
		}
		bottomLine += strings.Repeat(hChar, w)
		if i < len(colWidths)-1 {
			bottomLine += "┴"
			if useAscii {
				bottomLine = bottomLine[:len(bottomLine)-3] + "+"
			}
		} else {
			bottomLine += cornerBR
		}
	}
	lines = append(lines, bottomLine)

	return strings.Join(lines, "\n") + "\n", nil
}
