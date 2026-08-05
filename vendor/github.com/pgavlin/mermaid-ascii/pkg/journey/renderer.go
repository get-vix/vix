package journey

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

const (
	maxScore         = 5
	barMaxWidth      = 20
	nameWidthPadding = 2
	scoreColumnWidth = 7
)

// Render renders a JourneyDiagram as a formatted table string using Unicode or ASCII characters.
func Render(jd *JourneyDiagram, config *diagram.Config) (string, error) {
	if jd == nil || len(jd.Sections) == 0 {
		return "", fmt.Errorf("no journey data")
	}
	if config == nil {
		config = diagram.DefaultConfig()
	}

	useAscii := config.UseAscii

	// Characters
	hChar := "─"
	vChar := "│"
	crossChar := "┼"
	fillChar := "█"
	emptyChar := "░"
	if useAscii {
		hChar = "-"
		vChar = "|"
		crossChar = "+"
		fillChar = "#"
		emptyChar = "."
	}

	// Find longest task name
	maxNameLen := 0
	for _, section := range jd.Sections {
		for _, task := range section.Tasks {
			if len(task.Name) > maxNameLen {
				maxNameLen = len(task.Name)
			}
		}
		if len(section.Name) > maxNameLen {
			maxNameLen = len(section.Name)
		}
	}
	nameWidth := maxNameLen + nameWidthPadding

	var lines []string

	// Title
	if jd.Title != "" {
		totalWidth := nameWidth + barMaxWidth + 15
		pad := (totalWidth - len(jd.Title)) / 2
		if pad < 0 {
			pad = 0
		}
		lines = append(lines, strings.Repeat(" ", pad)+jd.Title)
		lines = append(lines, "")
	}

	// Header
	header := padRight("Task", nameWidth) + vChar + " Score " + vChar + " Satisfaction"
	lines = append(lines, header)
	sep := strings.Repeat(hChar, nameWidth) + crossChar + strings.Repeat(hChar, scoreColumnWidth) + crossChar + strings.Repeat(hChar, barMaxWidth+2)
	lines = append(lines, sep)

	for _, section := range jd.Sections {
		// Section header
		if section.Name != "" {
			sectionLine := padRight(section.Name, nameWidth) + vChar + "       " + vChar
			lines = append(lines, sectionLine)
			lines = append(lines, sep)
		}

		for _, task := range section.Tasks {
			barLen := task.Score * barMaxWidth / maxScore
			if barLen < 0 {
				barLen = 0
			}
			if barLen > barMaxWidth {
				barLen = barMaxWidth
			}

			bar := strings.Repeat(fillChar, barLen) + strings.Repeat(emptyChar, barMaxWidth-barLen)
			scoreFace := scoreToFace(task.Score)

			actorStr := ""
			if len(task.Actors) > 0 {
				actorStr = " " + strings.Join(task.Actors, ", ")
			}

			line := padRight(task.Name, nameWidth) + vChar + fmt.Sprintf("  %d %s ", task.Score, scoreFace) + vChar + " " + bar + actorStr
			lines = append(lines, line)
		}
	}

	// Bottom border
	lines = append(lines, sep)

	return strings.Join(lines, "\n") + "\n", nil
}

func scoreToFace(score int) string {
	switch {
	case score >= 5:
		return "😊"
	case score >= 4:
		return "🙂"
	case score >= 3:
		return "😐"
	case score >= 2:
		return "🙁"
	default:
		return "😞"
	}
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	return s + strings.Repeat(" ", width-len(s))
}
