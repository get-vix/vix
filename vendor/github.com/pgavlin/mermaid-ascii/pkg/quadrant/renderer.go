package quadrant

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const (
	gridWidth    = 40
	gridHeight   = 20
	titlePadding = 10
	minYAxisWidth = 6
)

// Render renders a QuadrantChart as a formatted grid string using Unicode or ASCII characters.
func Render(qc *QuadrantChart, config *diagram.Config) (string, error) {
	if qc == nil {
		return "", fmt.Errorf("no quadrant data")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	useAscii := config.UseAscii

	hChar := "─"
	vChar := "│"
	crossChar := "┼"
	dotChar := "●"
	cornerBL := "└"
	if useAscii {
		hChar = "-"
		vChar = "|"
		crossChar = "+"
		dotChar = "*"
		cornerBL = "+"
	}

	var lines []string

	// Title
	if qc.Title != "" {
		pad := (gridWidth + titlePadding - len(qc.Title)) / 2
		if pad < 0 {
			pad = 0
		}
		lines = append(lines, strings.Repeat(" ", pad)+qc.Title)
		lines = append(lines, "")
	}

	// Build the grid
	yAxisWidth := maxLen(qc.YAxisTop, qc.YAxisBottom) + 2
	if yAxisWidth < minYAxisWidth {
		yAxisWidth = minYAxisWidth
	}

	// Create grid canvas
	grid := make([][]string, gridHeight)
	for y := 0; y < gridHeight; y++ {
		grid[y] = make([]string, gridWidth)
		for x := 0; x < gridWidth; x++ {
			grid[y][x] = " "
		}
	}

	// Place data points
	for _, p := range qc.Points {
		px := int(p.X * float64(gridWidth-1))
		py := gridHeight - 1 - int(p.Y*float64(gridHeight-1))
		if px < 0 {
			px = 0
		}
		if px >= gridWidth {
			px = gridWidth - 1
		}
		if py < 0 {
			py = 0
		}
		if py >= gridHeight {
			py = gridHeight - 1
		}
		grid[py][px] = dotChar
	}

	// Place quadrant labels in centers
	midX := gridWidth / 2
	midY := gridHeight / 2
	placeLabel(grid, qc.Quadrant2, midX/2, midY/2)          // top-left
	placeLabel(grid, qc.Quadrant1, midX+midX/2, midY/2)     // top-right
	placeLabel(grid, qc.Quadrant3, midX/2, midY+midY/2)     // bottom-left
	placeLabel(grid, qc.Quadrant4, midX+midX/2, midY+midY/2) // bottom-right

	// Y-axis top label
	yTopLine := padLeft(qc.YAxisTop, yAxisWidth) + " " + vChar
	lines = append(lines, yTopLine)

	// Draw grid rows
	for y := 0; y < gridHeight; y++ {
		prefix := strings.Repeat(" ", yAxisWidth) + " "
		if y == midY {
			// Horizontal center line
			row := prefix
			for x := 0; x < gridWidth; x++ {
				if grid[y][x] != " " {
					row += grid[y][x]
				} else if x == midX {
					row += crossChar
				} else {
					row += hChar
				}
			}
			lines = append(lines, row)
		} else {
			row := prefix
			for x := 0; x < gridWidth; x++ {
				if grid[y][x] != " " {
					row += grid[y][x]
				} else if x == midX {
					row += vChar
				} else {
					row += " "
				}
			}
			lines = append(lines, strings.TrimRight(row, " "))
		}
	}

	// Y-axis bottom label
	yBottomLine := padLeft(qc.YAxisBottom, yAxisWidth) + " " + cornerBL + strings.Repeat(hChar, gridWidth)
	lines = append(lines, yBottomLine)

	// X-axis labels
	xAxisLine := strings.Repeat(" ", yAxisWidth+2) + qc.XAxisLeft
	rightPad := gridWidth - len(qc.XAxisLeft) - len(qc.XAxisRight)
	if rightPad > 0 {
		xAxisLine += strings.Repeat(" ", rightPad) + qc.XAxisRight
	}
	lines = append(lines, xAxisLine)

	// Point legend
	if len(qc.Points) > 0 {
		lines = append(lines, "")
		for _, p := range qc.Points {
			lines = append(lines, fmt.Sprintf("  %s %s (%.2f, %.2f)", dotChar, p.Label, p.X, p.Y))
		}
	}

	return strings.Join(lines, "\n") + "\n", nil
}

func placeLabel(grid [][]string, label string, cx, cy int) {
	if label == "" {
		return
	}
	startX := cx - len(label)/2
	if startX < 0 {
		startX = 0
	}
	for i, ch := range label {
		x := startX + i
		if x >= 0 && x < len(grid[0]) && cy >= 0 && cy < len(grid) {
			grid[cy][x] = string(ch)
		}
	}
}

func padLeft(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat(" ", width-len(s)) + s
}

func maxLen(strs ...string) int {
	m := 0
	for _, s := range strs {
		if len(s) > m {
			m = len(s)
		}
	}
	return m
}
