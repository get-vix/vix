package gantt

import (
	"fmt"
	"strings"
	"time"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const (
	defaultChartWidth   = 60
	defaultLabelWidth   = 20
	minLabelWidth       = 10
	labelPadding        = 2
	numDateMarkers      = 5
	barFillUnicode      = "█"
	barFillASCII        = "#"
	barEmptyUnicode     = "░"
	barEmptyASCII       = "."
	barActiveUnicode    = "▓"
	barActiveASCII      = "="
	barCritUnicode      = "▒"
	barCritASCII        = "!"
)

// Render renders a GanttDiagram to a string using horizontal bar chart layout.
func Render(gd *GanttDiagram, config *diagram.Config) (string, error) {
	if gd == nil || len(gd.Tasks) == 0 {
		return "", fmt.Errorf("no tasks to render")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	useAscii := config.UseAscii

	// Find overall time range
	minTime := gd.Tasks[0].StartDate
	maxTime := gd.Tasks[0].EndDate
	for _, task := range gd.Tasks {
		if task.StartDate.Before(minTime) {
			minTime = task.StartDate
		}
		if task.EndDate.After(maxTime) {
			maxTime = task.EndDate
		}
	}

	totalDuration := maxTime.Sub(minTime)
	if totalDuration <= 0 {
		totalDuration = 24 * time.Hour
	}

	// Calculate label width from longest task/section name
	labelWidth := minLabelWidth
	for _, task := range gd.Tasks {
		if len(task.Name) > labelWidth {
			labelWidth = len(task.Name)
		}
	}
	for _, section := range gd.Sections {
		if len(section.Name) > labelWidth {
			labelWidth = len(section.Name)
		}
	}
	labelWidth += labelPadding
	if labelWidth > defaultLabelWidth {
		labelWidth = defaultLabelWidth + labelPadding
	}

	chartWidth := defaultChartWidth
	var lines []string

	// Title
	if gd.Title != "" {
		titleLine := centerText(gd.Title, labelWidth+chartWidth+3)
		lines = append(lines, titleLine)
		lines = append(lines, "")
	}

	// Header with date markers
	headerLine := strings.Repeat(" ", labelWidth+2)
	numMarkers := numDateMarkers
	if chartWidth < 20 {
		numMarkers = 2
	}
	for i := 0; i < numMarkers; i++ {
		pos := i * chartWidth / (numMarkers - 1)
		t := minTime.Add(time.Duration(float64(totalDuration) * float64(i) / float64(numMarkers-1)))
		dateStr := t.Format("01/02")
		// Place date at position
		for len(headerLine) < labelWidth+2+pos {
			headerLine += " "
		}
		if pos+len(dateStr) <= labelWidth+2+chartWidth {
			headerLine = headerLine[:labelWidth+2+pos] + dateStr
		}
	}
	lines = append(lines, strings.TrimRight(headerLine, " "))

	// Separator
	hChar := "─"
	cornerTL := "┌"
	cornerTR := "┐"
	cornerBL := "└"
	cornerBR := "┘"
	if useAscii {
		hChar = "-"
		cornerTL = "+"
		cornerTR = "+"
		cornerBL = "+"
		cornerBR = "+"
	}
	sepLine := strings.Repeat(" ", labelWidth+1)
	sepLine += cornerTL + strings.Repeat(hChar, chartWidth) + cornerTR
	lines = append(lines, sepLine)

	// Render tasks, grouped by section
	barFill := barFillUnicode
	barEmpty := barEmptyUnicode
	barActive := barActiveUnicode
	barCrit := barCritUnicode
	vChar := "│"
	if useAscii {
		barFill = barFillASCII
		barEmpty = barEmptyASCII
		barActive = barActiveASCII
		barCrit = barCritASCII
		vChar = "|"
	}

	renderedTasks := make(map[*Task]bool)
	for _, section := range gd.Sections {
		// Section header
		sectionLabel := strings.TrimRight(truncateOrPad(section.Name, labelWidth), " ")
		lines = append(lines, sectionLabel)

		for _, task := range section.Tasks {
			line := renderTaskLine(task, minTime, totalDuration, labelWidth, chartWidth, barFill, barEmpty, barActive, barCrit, vChar)
			lines = append(lines, line)
			renderedTasks[task] = true
		}
	}

	// Render any tasks not in sections
	for _, task := range gd.Tasks {
		if !renderedTasks[task] {
			line := renderTaskLine(task, minTime, totalDuration, labelWidth, chartWidth, barFill, barEmpty, barActive, barCrit, vChar)
			lines = append(lines, line)
		}
	}

	// Bottom separator
	bottomSep := strings.Repeat(" ", labelWidth+1)
	bottomSep += cornerBL + strings.Repeat(hChar, chartWidth) + cornerBR
	lines = append(lines, bottomSep)

	return strings.Join(lines, "\n") + "\n", nil
}

func renderTaskLine(task *Task, minTime time.Time, totalDuration time.Duration, labelWidth, chartWidth int, barFill, barEmpty, barActive, barCrit, vChar string) string {
	label := truncateOrPad(task.Name, labelWidth)

	// Calculate bar positions
	startFrac := float64(task.StartDate.Sub(minTime)) / float64(totalDuration)
	endFrac := float64(task.EndDate.Sub(minTime)) / float64(totalDuration)

	startPos := int(startFrac * float64(chartWidth))
	endPos := int(endFrac * float64(chartWidth))
	if endPos <= startPos {
		endPos = startPos + 1
	}
	if endPos > chartWidth {
		endPos = chartWidth
	}

	// Choose bar character based on status
	fillChar := barFill
	if strings.Contains(task.Status, "active") {
		fillChar = barActive
	} else if strings.Contains(task.Status, "crit") {
		fillChar = barCrit
	}

	// Build the bar
	bar := make([]string, chartWidth)
	for i := 0; i < chartWidth; i++ {
		if i >= startPos && i < endPos {
			bar[i] = fillChar
		} else {
			bar[i] = barEmpty
		}
	}

	return label + " " + vChar + strings.Join(bar, "") + vChar
}

func truncateOrPad(s string, width int) string {
	if len(s) > width {
		return s[:width-1] + "…"
	}
	return s + strings.Repeat(" ", width-len(s))
}

func centerText(s string, width int) string {
	if len(s) >= width {
		return s
	}
	pad := (width - len(s)) / 2
	return strings.Repeat(" ", pad) + s
}

// min/max helpers removed - using standard comparisons
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Used to format durations for display
func formatDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	return d.String()
}
