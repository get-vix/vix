// Package piechart parses and renders Mermaid pie charts as ASCII/Unicode art.
package piechart

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// pieKeyword is the Mermaid keyword that identifies a pie chart.
const pieKeyword = "pie"

// PieChart represents a parsed pie chart with a title and data slices.
type PieChart struct {
	Title  string
	Slices []*Slice
	Total  float64
}

// Slice represents a single data slice in a pie chart.
type Slice struct {
	Label      string
	Value      float64
	Percentage float64
}

// IsPieChart returns true if the input begins with the pie keyword.
func IsPieChart(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return trimmed == pieKeyword || strings.HasPrefix(trimmed, pieKeyword+" ")
	}
	return false
}

// Parse parses a pie chart from the given input string.
func Parse(input string) (*PieChart, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	// Expect "pie"
	tok := s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != pieKeyword {
		return nil, fmt.Errorf("expected %q keyword", pieKeyword)
	}
	s.Next()

	pc := &PieChart{Slices: []*Slice{}}

	// Check for inline title: "pie title My Title"
	s.SkipWhitespace()
	if s.Peek().Kind == parser.TokenIdent && s.Peek().Text == "title" {
		s.Next()
		s.SkipWhitespace()
		pc.Title = strings.TrimSpace(parser.ConsumeRestOfLine(s))
	}
	s.SkipNewlines()

	for !s.AtEnd() {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}

		tok := s.Peek()

		// title directive
		if tok.Kind == parser.TokenIdent && tok.Text == "title" {
			s.Next()
			s.SkipWhitespace()
			pc.Title = strings.TrimSpace(parser.ConsumeRestOfLine(s))
			continue
		}

		// showData keyword — ignored
		if tok.Kind == parser.TokenIdent && tok.Text == "showData" {
			s.Next()
			continue
		}

		// Slice: "label" : value
		if tok.Kind == parser.TokenString {
			label := s.Next().Text
			s.SkipWhitespace()
			if s.Peek().Kind == parser.TokenColon {
				s.Next()
				s.SkipWhitespace()
				if s.Peek().Kind == parser.TokenNumber {
					value, err := strconv.ParseFloat(s.Next().Text, 64)
					if err == nil {
						pc.Slices = append(pc.Slices, &Slice{
							Label: label,
							Value: value,
						})
					}
				}
			}
			parser.SkipToEndOfLine(s)
			continue
		}

		parser.SkipToEndOfLine(s)
	}

	if len(pc.Slices) == 0 {
		return nil, fmt.Errorf("no data found")
	}

	// Calculate percentages
	total := 0.0
	for _, sl := range pc.Slices {
		total += sl.Value
	}
	pc.Total = total
	for _, sl := range pc.Slices {
		sl.Percentage = (sl.Value / total) * 100
	}

	return pc, nil
}
