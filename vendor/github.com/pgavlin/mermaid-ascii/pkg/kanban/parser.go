// Package kanban implements parsing and rendering of Kanban board diagrams
// in Mermaid syntax.
package kanban

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

// kanbanKeyword is the Mermaid keyword that identifies a Kanban board diagram.
const kanbanKeyword = "kanban"

// KanbanBoard represents a parsed Kanban board with columns and cards.
type KanbanBoard struct {
	Columns []*Column
}

// Column represents a column in a Kanban board containing cards.
type Column struct {
	Name  string
	Cards []*Card
}

// Card represents a single card within a Kanban column.
type Card struct {
	Title string
}

// IsKanbanBoard returns true if the input starts with the kanban keyword.
func IsKanbanBoard(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return trimmed == kanbanKeyword
	}
	return false
}

// Parse parses a Kanban board from Mermaid-style input.
// Kanban uses indentation to distinguish columns from cards,
// so we use line-based parsing rather than the tokenizer.
func Parse(input string) (*KanbanBoard, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	rawLines := strings.Split(input, "\n")
	lines := diagram.RemoveComments(rawLines)
	if len(lines) == 0 {
		return nil, fmt.Errorf("no content found")
	}

	if strings.TrimSpace(lines[0]) != kanbanKeyword {
		return nil, fmt.Errorf("expected %q keyword", kanbanKeyword)
	}
	lines = lines[1:]

	kb := &KanbanBoard{Columns: []*Column{}}
	var currentColumn *Column

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// If line starts with whitespace, it's a card
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			text := strings.TrimSpace(line)
			if text == "" {
				continue
			}
			if currentColumn == nil {
				currentColumn = &Column{Name: "Default", Cards: []*Card{}}
				kb.Columns = append(kb.Columns, currentColumn)
			}
			currentColumn.Cards = append(currentColumn.Cards, &Card{Title: text})
		} else {
			// Column header
			text := strings.TrimSpace(line)
			currentColumn = &Column{Name: text, Cards: []*Card{}}
			kb.Columns = append(kb.Columns, currentColumn)
		}
	}

	if len(kb.Columns) == 0 {
		return nil, fmt.Errorf("no columns found")
	}

	return kb, nil
}
