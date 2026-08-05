package erdiagram

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/canvas"
	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const (
	minEntityWidth = 12
	cellPadding    = 2 // 1 space on each side
)

// Render renders an ERDiagram to a string.
func Render(erd *ERDiagram, config *diagram.Config) (string, error) {
	if erd == nil || len(erd.Entities) == 0 {
		return "", fmt.Errorf("no entities to render")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	chars := canvas.Select(config.UseAscii)

	var lines []string

	// Render each entity box vertically stacked
	for i, entity := range erd.Entities {
		box := renderEntityBox(entity, chars)
		lines = append(lines, box...)
		if i < len(erd.Entities)-1 {
			lines = append(lines, "")
		}
	}

	// Render relationships as annotations below the entities
	if len(erd.Relationships) > 0 {
		lines = append(lines, "")
		for _, rel := range erd.Relationships {
			relLine := renderRelationshipLine(rel, config.UseAscii)
			lines = append(lines, relLine)
		}
	}

	return strings.Join(lines, "\n") + "\n", nil
}

func renderEntityBox(entity *Entity, chars canvas.BoxChars) []string {
	// Calculate width
	width := len(entity.Name) + cellPadding // 1 space padding on each side
	for _, attr := range entity.Attributes {
		attrStr := formatAttribute(attr)
		aw := len(attrStr) + cellPadding
		if aw > width {
			width = aw
		}
	}
	if width < minEntityWidth {
		width = minEntityWidth
	}

	var lines []string

	// Top border
	lines = append(lines, chars.TopBorder(width))

	// Entity name centered
	lines = append(lines, chars.CenterText(entity.Name, width))

	// Separator
	lines = append(lines, string(chars.TeeRight)+strings.Repeat(string(chars.Horizontal), width)+string(chars.TeeLeft))

	// Attributes
	if len(entity.Attributes) == 0 {
		lines = append(lines, string(chars.Vertical)+strings.Repeat(" ", width)+string(chars.Vertical))
	} else {
		for _, attr := range entity.Attributes {
			attrStr := formatAttribute(attr)
			attrLine := string(chars.Vertical) + " " + attrStr + strings.Repeat(" ", width-len(attrStr)-1) + string(chars.Vertical)
			lines = append(lines, attrLine)
		}
	}

	// Bottom border
	lines = append(lines, chars.BottomBorder(width))

	return lines
}

func formatAttribute(attr *Attribute) string {
	s := attr.Type + " " + attr.Name
	if attr.Constraint != NoConstraint {
		switch attr.Constraint {
		case PrimaryKey:
			s += " PK"
		case ForeignKey:
			s += " FK"
		case UniqueKey:
			s += " UK"
		}
	}
	return s
}

func renderRelationshipLine(rel *Relationship, useAscii bool) string {
	var sb strings.Builder
	sb.WriteString(rel.From)
	sb.WriteByte(' ')
	sb.WriteString(cardinalityString(rel.FromCardinality, useAscii))
	if useAscii {
		sb.WriteString("--")
	} else {
		sb.WriteString("──")
	}
	sb.WriteString(cardinalityString(rel.ToCardinality, useAscii))
	sb.WriteByte(' ')
	sb.WriteString(rel.To)
	sb.WriteString(" : ")
	sb.WriteString(rel.Label)
	return sb.String()
}

func cardinalityString(c Cardinality, useAscii bool) string {
	switch c {
	case ExactlyOne:
		return "||"
	case ZeroOrOne:
		return "o|"
	case OneOrMany:
		if useAscii {
			return ">|"
		}
		return ">|"
	case ZeroOrMany:
		if useAscii {
			return ">o"
		}
		return ">o"
	default:
		return "||"
	}
}
