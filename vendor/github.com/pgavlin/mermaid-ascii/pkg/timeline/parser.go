// Package timeline provides parsing and rendering of Mermaid timeline diagrams.
package timeline

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// timelineKeyword is the keyword that identifies a timeline diagram in Mermaid syntax.
const timelineKeyword = "timeline"

// TimelineDiagram represents a parsed timeline diagram with optional sections and events.
type TimelineDiagram struct {
	Title    string
	Sections []*TimelineSection
	Events   []*TimelineEvent
}

// TimelineSection represents a named section within a timeline diagram.
type TimelineSection struct {
	Name   string
	Events []*TimelineEvent
}

// TimelineEvent represents a single event on the timeline, with a time period and associated events.
type TimelineEvent struct {
	Period  string
	Events  []string
	Section *TimelineSection
}

// IsTimelineDiagram reports whether the input text is a timeline diagram.
func IsTimelineDiagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return trimmed == timelineKeyword
	}
	return false
}

// Parse parses Mermaid timeline text into a TimelineDiagram.
func Parse(input string) (*TimelineDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	tok := s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != timelineKeyword {
		return nil, fmt.Errorf("expected %q keyword", timelineKeyword)
	}
	s.Next()
	s.SkipNewlines()

	td := &TimelineDiagram{
		Sections: []*TimelineSection{},
		Events:   []*TimelineEvent{},
	}
	var currentSection *TimelineSection

	for !s.AtEnd() {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}

		tok := s.Peek()
		if tok.Kind == parser.TokenIdent && tok.Text == "title" {
			s.Next()
			s.SkipWhitespace()
			td.Title = strings.TrimSpace(parser.ConsumeRestOfLine(s))
			continue
		}

		if tok.Kind == parser.TokenIdent && tok.Text == "section" {
			s.Next()
			s.SkipWhitespace()
			currentSection = &TimelineSection{
				Name:   strings.TrimSpace(parser.ConsumeRestOfLine(s)),
				Events: []*TimelineEvent{},
			}
			td.Sections = append(td.Sections, currentSection)
			continue
		}

		// Event line: period : event1 : event2 ...
		// or bare period (no colon)
		fullLine := strings.TrimSpace(parser.ConsumeRestOfLine(s))

		if idx := strings.Index(fullLine, ":"); idx >= 0 {
			period := strings.TrimSpace(fullLine[:idx])
			rest := fullLine[idx+1:]
			eventTexts := strings.Split(rest, ":")
			events := make([]string, 0, len(eventTexts))
			for _, e := range eventTexts {
				e = strings.TrimSpace(e)
				if e != "" {
					events = append(events, e)
				}
			}
			event := &TimelineEvent{
				Period:  period,
				Events:  events,
				Section: currentSection,
			}
			if currentSection != nil {
				currentSection.Events = append(currentSection.Events, event)
			}
			td.Events = append(td.Events, event)
		} else {
			// Bare period
			event := &TimelineEvent{
				Period:  fullLine,
				Events:  []string{},
				Section: currentSection,
			}
			if currentSection != nil {
				currentSection.Events = append(currentSection.Events, event)
			}
			td.Events = append(td.Events, event)
		}
	}

	if len(td.Events) == 0 {
		return nil, fmt.Errorf("no events found")
	}

	return td, nil
}
