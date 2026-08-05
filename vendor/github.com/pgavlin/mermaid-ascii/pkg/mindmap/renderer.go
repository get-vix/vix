package mindmap

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

// Render renders a MindmapDiagram as a tree-structured string using Unicode or ASCII characters.
func Render(md *MindmapDiagram, config *diagram.Config) (string, error) {
	if md == nil || md.Root == nil {
		return "", fmt.Errorf("no mindmap data")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	useAscii := config.UseAscii

	// Characters
	branchChar := "├"
	lastBranchChar := "└"
	vChar := "│"
	hChar := "──"
	if useAscii {
		branchChar = "|"
		lastBranchChar = "`"
		vChar = "|"
		hChar = "--"
	}

	var lines []string

	// Render root
	lines = append(lines, renderNodeText(md.Root, useAscii))

	// Render children recursively
	renderChildren(md.Root, "", &lines, branchChar, lastBranchChar, vChar, hChar, useAscii)

	return strings.Join(lines, "\n") + "\n", nil
}

func renderChildren(node *MindmapNode, prefix string, lines *[]string, branchChar, lastBranchChar, vChar, hChar string, useAscii bool) {
	for i, child := range node.Children {
		isLast := i == len(node.Children)-1

		connector := branchChar
		childPrefix := prefix + vChar + "   "
		if isLast {
			connector = lastBranchChar
			childPrefix = prefix + "    "
		}

		line := prefix + connector + hChar + " " + renderNodeText(child, useAscii)
		*lines = append(*lines, line)

		renderChildren(child, childPrefix, lines, branchChar, lastBranchChar, vChar, hChar, useAscii)
	}
}

func renderNodeText(node *MindmapNode, useAscii bool) string {
	text := node.Text

	switch node.Shape {
	case ShapeSquare:
		if useAscii {
			return "[" + text + "]"
		}
		return "┌" + strings.Repeat("─", len(text)+2) + "┐\n│ " + text + " │\n└" + strings.Repeat("─", len(text)+2) + "┘"
	case ShapeRounded:
		if useAscii {
			return "(" + text + ")"
		}
		return "╭" + strings.Repeat("─", len(text)+2) + "╮\n│ " + text + " │\n╰" + strings.Repeat("─", len(text)+2) + "╯"
	case ShapeHexagon:
		if useAscii {
			return "{{" + text + "}}"
		}
		return "⬡ " + text
	case ShapeBang:
		return "💥 " + text
	case ShapeCloud:
		return "☁ " + text
	default:
		return text
	}
}
