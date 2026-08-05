// Package gantt parses and renders Mermaid Gantt charts as ASCII/Unicode art.
package gantt

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// ganttKeyword is the Mermaid keyword that identifies a Gantt chart.
const ganttKeyword = "gantt"

// GanttDiagram represents a parsed Gantt chart with sections and tasks.
type GanttDiagram struct {
	Title      string
	DateFormat string
	Sections   []*Section
	Tasks      []*Task
}

// Section represents a named group of tasks in a Gantt chart.
type Section struct {
	Name  string
	Tasks []*Task
}

// Task represents a single task in a Gantt chart with timing and status information.
type Task struct {
	Name      string
	ID        string
	Status    string // done, active, crit, or empty
	StartDate time.Time
	EndDate   time.Time
	Duration  time.Duration
	After     string // dependency
	Section   *Section
}

// IsGanttDiagram returns true if the input begins with the gantt keyword.
func IsGanttDiagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return trimmed == ganttKeyword
	}
	return false
}

// Parse parses a Gantt chart from the given input string.
func Parse(input string) (*GanttDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	tok := s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != ganttKeyword {
		return nil, fmt.Errorf("expected %q keyword", ganttKeyword)
	}
	s.Next()
	s.SkipNewlines()

	gd := &GanttDiagram{
		DateFormat: "YYYY-MM-DD",
		Sections:   []*Section{},
		Tasks:      []*Task{},
	}

	var currentSection *Section
	baseDate := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tasksByID := make(map[string]*Task)

	for !s.AtEnd() {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}

		tok := s.Peek()
		if tok.Kind != parser.TokenIdent {
			parser.SkipToEndOfLine(s)
			continue
		}

		switch tok.Text {
		case "title":
			s.Next()
			s.SkipWhitespace()
			gd.Title = strings.TrimSpace(parser.ConsumeRestOfLine(s))
			continue

		case "dateFormat":
			s.Next()
			s.SkipWhitespace()
			gd.DateFormat = strings.TrimSpace(parser.ConsumeRestOfLine(s))
			continue

		case "excludes":
			s.Next()
			parser.SkipToEndOfLine(s)
			continue

		case "section":
			s.Next()
			s.SkipWhitespace()
			currentSection = &Section{
				Name:  strings.TrimSpace(parser.ConsumeRestOfLine(s)),
				Tasks: []*Task{},
			}
			gd.Sections = append(gd.Sections, currentSection)
			continue
		}

		// Task line: collect full line text and parse as "name : spec"
		lineText := strings.TrimSpace(parser.ConsumeRestOfLine(s))

		if idx := strings.Index(lineText, ":"); idx >= 0 {
			name := strings.TrimSpace(lineText[:idx])
			spec := strings.TrimSpace(lineText[idx+1:])

			task, err := parseTask(name, spec, baseDate, tasksByID, gd.DateFormat)
			if err != nil {
				continue
			}
			task.Section = currentSection
			if currentSection != nil {
				currentSection.Tasks = append(currentSection.Tasks, task)
			}
			gd.Tasks = append(gd.Tasks, task)
			if task.ID != "" {
				tasksByID[task.ID] = task
			}
			if task.EndDate.After(baseDate) {
				baseDate = task.EndDate
			}
		}
	}

	if len(gd.Tasks) == 0 {
		return nil, fmt.Errorf("no tasks found")
	}

	return gd, nil
}

func parseTask(name, spec string, baseDate time.Time, tasksByID map[string]*Task, dateFormat string) (*Task, error) {
	task := &Task{Name: strings.TrimSpace(name)}

	parts := strings.Split(spec, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	idx := 0

	// Parse optional status tags
	for idx < len(parts) {
		p := parts[idx]
		if p == "done" || p == "active" || p == "crit" {
			if task.Status != "" {
				task.Status += "," + p
			} else {
				task.Status = p
			}
			idx++
		} else {
			break
		}
	}

	// Parse optional ID
	if idx < len(parts) {
		p := parts[idx]
		if !isDateLike(p, dateFormat) && !isDuration(p) && !strings.HasPrefix(p, "after ") {
			task.ID = p
			idx++
		}
	}

	// Parse start: date, "after taskID", or implied
	if idx < len(parts) {
		p := parts[idx]
		if strings.HasPrefix(p, "after ") {
			depID := strings.TrimPrefix(p, "after ")
			task.After = depID
			if dep, ok := tasksByID[depID]; ok {
				task.StartDate = dep.EndDate
			} else {
				task.StartDate = baseDate
			}
			idx++
		} else if d, err := parseDate(p, dateFormat); err == nil {
			task.StartDate = d
			idx++
		} else {
			task.StartDate = baseDate
		}
	} else {
		task.StartDate = baseDate
	}

	// Parse duration or end date
	if idx < len(parts) {
		p := parts[idx]
		if dur, err := parseDuration(p); err == nil {
			task.Duration = dur
			task.EndDate = task.StartDate.Add(dur)
		} else if d, err := parseDate(p, dateFormat); err == nil {
			task.EndDate = d
			task.Duration = d.Sub(task.StartDate)
		} else {
			// Default 1 day
			task.Duration = 24 * time.Hour
			task.EndDate = task.StartDate.Add(task.Duration)
		}
	} else {
		task.Duration = 24 * time.Hour
		task.EndDate = task.StartDate.Add(task.Duration)
	}

	return task, nil
}

func parseDate(s string, format string) (time.Time, error) {
	goFormat := mermaidToGoDateFormat(format)
	return time.Parse(goFormat, s)
}

func mermaidToGoDateFormat(format string) string {
	r := strings.NewReplacer(
		"YYYY", "2006",
		"YY", "06",
		"MM", "01",
		"DD", "02",
		"HH", "15",
		"mm", "04",
		"ss", "05",
	)
	return r.Replace(format)
}

func isDateLike(s, format string) bool {
	_, err := parseDate(s, format)
	return err == nil
}

func isDuration(s string) bool {
	_, err := parseDuration(s)
	return err == nil
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}

	unit := s[len(s)-1]
	numStr := s[:len(s)-1]

	var multiplier time.Duration
	switch unit {
	case 'd':
		multiplier = 24 * time.Hour
	case 'h':
		multiplier = time.Hour
	case 'm':
		multiplier = time.Minute
	case 'w':
		multiplier = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("invalid duration: %s", s)
	}

	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}

	return time.Duration(n) * multiplier, nil
}
