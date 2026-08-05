package piechart

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const (
	defaultBarWidth = 40
)

// Render renders a pie chart as a horizontal bar chart with percentages.
// True circles are very low-fidelity in ASCII, so we use bars instead.
func Render(pc *PieChart, config *diagram.Config) (string, error) {
	if pc == nil || len(pc.Slices) == 0 {
		return "", fmt.Errorf("no data to render")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	useAscii := config.UseAscii

	// Find longest label
	maxLabelLen := 0
	for _, s := range pc.Slices {
		if len(s.Label) > maxLabelLen {
			maxLabelLen = len(s.Label)
		}
	}

	var lines []string

	// Title
	if pc.Title != "" {
		totalWidth := maxLabelLen + 3 + defaultBarWidth + 10
		pad := (totalWidth - len(pc.Title)) / 2
		if pad < 0 {
			pad = 0
		}
		lines = append(lines, strings.Repeat(" ", pad)+pc.Title)
		lines = append(lines, "")
	}

	// Bar characters
	fillChars := []string{"█", "▓", "░", "▒", "▆", "▄", "▃", "▂"}
	if useAscii {
		fillChars = []string{"#", "=", "+", "*", "~", "-", ".", ":"}
	}

	// Render each slice as a horizontal bar
	for i, s := range pc.Slices {
		label := s.Label
		// Pad label
		for len(label) < maxLabelLen {
			label += " "
		}

		barLen := int(s.Percentage / 100.0 * float64(defaultBarWidth))
		if barLen < 1 && s.Value > 0 {
			barLen = 1
		}

		fillChar := fillChars[i%len(fillChars)]
		bar := strings.Repeat(fillChar, barLen)

		pctStr := fmt.Sprintf(" %5.1f%%", s.Percentage)

		vChar := "│"
		if useAscii {
			vChar = "|"
		}

		line := label + " " + vChar + bar + strings.Repeat(" ", defaultBarWidth-barLen) + vChar + pctStr
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n") + "\n", nil
}
