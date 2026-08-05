package sankey

import (
	"fmt"
	"math"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const maxBarWidth = 40

// Render renders a Sankey diagram as ASCII/Unicode text.
func Render(d *SankeyDiagram, config *diagram.Config) (string, error) {
	if d == nil {
		return "", fmt.Errorf("nil diagram")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	if len(d.Flows) == 0 {
		return "", fmt.Errorf("no flows to render")
	}

	barChar := '█'
	arrowStr := " ──> "
	if config.UseAscii {
		barChar = '#'
		arrowStr = " --> "
	}

	// Find max value for scaling
	maxVal := 0.0
	for _, f := range d.Flows {
		if f.Value > maxVal {
			maxVal = f.Value
		}
	}

	// Find max source/target label width
	maxSourceWidth := 0
	maxTargetWidth := 0
	for _, f := range d.Flows {
		if len(f.Source) > maxSourceWidth {
			maxSourceWidth = len(f.Source)
		}
		if len(f.Target) > maxTargetWidth {
			maxTargetWidth = len(f.Target)
		}
	}

	var lines []string
	for _, f := range d.Flows {
		barWidth := 1
		if maxVal > 0 {
			barWidth = int(math.Round(f.Value / maxVal * maxBarWidth))
			if barWidth < 1 {
				barWidth = 1
			}
		}

		bar := strings.Repeat(string(barChar), barWidth)
		sourcePad := strings.Repeat(" ", maxSourceWidth-len(f.Source))
		line := fmt.Sprintf("%s%s %s%s%s (%.0f)", f.Source, sourcePad, bar, arrowStr, f.Target, f.Value)
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n") + "\n", nil
}
