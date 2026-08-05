// Package journey provides parsing and rendering of Mermaid user journey diagrams.
package journey

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// journeyKeyword is the keyword that identifies a journey diagram in Mermaid syntax.
const journeyKeyword = "journey"

// JourneyDiagram represents a parsed user journey diagram with sections and tasks.
type JourneyDiagram struct {
	Title    string
	Sections []*JourneySection
}

// JourneySection represents a named section within a journey diagram containing tasks.
type JourneySection struct {
	Name  string
	Tasks []*JourneyTask
}

// JourneyTask represents a single task in a journey diagram with a satisfaction score.
type JourneyTask struct {
	Name   string
	Score  int
	Actors []string
}

// IsJourneyDiagram reports whether the input text is a journey diagram.
func IsJourneyDiagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return trimmed == journeyKeyword
	}
	return false
}

// Parse parses Mermaid journey text into a JourneyDiagram.
func Parse(input string) (*JourneyDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	tok := s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != journeyKeyword {
		return nil, fmt.Errorf("expected %q keyword", journeyKeyword)
	}
	s.Next()
	s.SkipNewlines()

	jd := &JourneyDiagram{
		Sections: []*JourneySection{},
	}
	var currentSection *JourneySection

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

		// title directive
		if tok.Text == "title" {
			s.Next()
			s.SkipWhitespace()
			jd.Title = strings.TrimSpace(parser.ConsumeRestOfLine(s))
			continue
		}

		// section directive
		if tok.Text == "section" {
			s.Next()
			s.SkipWhitespace()
			currentSection = &JourneySection{
				Name:  strings.TrimSpace(parser.ConsumeRestOfLine(s)),
				Tasks: []*JourneyTask{},
			}
			jd.Sections = append(jd.Sections, currentSection)
			continue
		}

		// Task line: name : score [: actors]
		// Collect the full line and parse it
		lineText := strings.TrimSpace(parser.ConsumeRestOfLine(s))

		if idx := strings.Index(lineText, ":"); idx >= 0 {
			name := strings.TrimSpace(lineText[:idx])
			rest := lineText[idx+1:]

			// Split by ":" — first part is score, optional second is actors
			parts := strings.SplitN(rest, ":", 2)
			scoreStr := strings.TrimSpace(parts[0])
			score, err := strconv.Atoi(scoreStr)
			if err != nil {
				continue
			}

			var actors []string
			if len(parts) > 1 {
				for _, a := range strings.Split(parts[1], ",") {
					a = strings.TrimSpace(a)
					if a != "" {
						actors = append(actors, a)
					}
				}
			}

			task := &JourneyTask{
				Name:   name,
				Score:  score,
				Actors: actors,
			}
			if currentSection != nil {
				currentSection.Tasks = append(currentSection.Tasks, task)
			} else {
				currentSection = &JourneySection{
					Name:  "",
					Tasks: []*JourneyTask{task},
				}
				jd.Sections = append(jd.Sections, currentSection)
			}
		}
	}

	if len(jd.Sections) == 0 {
		return nil, fmt.Errorf("no tasks found")
	}

	return jd, nil
}
