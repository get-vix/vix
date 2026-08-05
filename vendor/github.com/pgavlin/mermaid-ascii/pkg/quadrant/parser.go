// Package quadrant provides parsing and rendering of Mermaid quadrant chart diagrams.
package quadrant

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// quadrantKeyword is the keyword that identifies a quadrant chart diagram in Mermaid syntax.
const quadrantKeyword = "quadrantChart"

// QuadrantChart represents a parsed quadrant chart with axis labels, quadrant names, and data points.
type QuadrantChart struct {
	Title       string
	XAxisLeft   string
	XAxisRight  string
	YAxisBottom string
	YAxisTop    string
	Quadrant1   string // top-right
	Quadrant2   string // top-left
	Quadrant3   string // bottom-left
	Quadrant4   string // bottom-right
	Points      []*DataPoint
}

// DataPoint represents a labeled point with X and Y coordinates in the range [0, 1].
type DataPoint struct {
	Label string
	X     float64
	Y     float64
}

// IsQuadrantChart reports whether the input text is a quadrant chart diagram.
func IsQuadrantChart(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return trimmed == quadrantKeyword
	}
	return false
}

// Parse parses Mermaid quadrant chart text into a QuadrantChart.
func Parse(input string) (*QuadrantChart, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	tok := s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != quadrantKeyword {
		return nil, fmt.Errorf("expected %q keyword", quadrantKeyword)
	}
	s.Next()
	s.SkipNewlines()

	qc := &QuadrantChart{
		XAxisLeft:   "Low",
		XAxisRight:  "High",
		YAxisBottom: "Low",
		YAxisTop:    "High",
		Points:      []*DataPoint{},
	}

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
			qc.Title = strings.TrimSpace(parser.ConsumeRestOfLine(s))
			continue
		case "x":
			// "x-axis" tokenizes as Ident("x") Operator("-") Ident("axis")
			if parseHyphenatedKeyword(s, "x-axis") {
				s.SkipWhitespace()
				parseAxis(s, &qc.XAxisLeft, &qc.XAxisRight)
				continue
			}
		case "y":
			if parseHyphenatedKeyword(s, "y-axis") {
				s.SkipWhitespace()
				parseAxis(s, &qc.YAxisBottom, &qc.YAxisTop)
				continue
			}
		case "quadrant":
			// "quadrant-1" tokenizes as Ident("quadrant") Operator("-") Number("1")
			saved := s.Save()
			s.Next()
			if s.Peek().Kind == parser.TokenOperator && s.Peek().Text == "-" {
				s.Next()
				if s.Peek().Kind == parser.TokenNumber {
					num := s.Next().Text
					s.SkipWhitespace()
					text := strings.TrimSpace(parser.ConsumeRestOfLine(s))
					switch num {
					case "1":
						qc.Quadrant1 = text
					case "2":
						qc.Quadrant2 = text
					case "3":
						qc.Quadrant3 = text
					case "4":
						qc.Quadrant4 = text
					}
					continue
				}
			}
			s.Restore(saved)
		}

		// Try data point: label : [x, y]
		lineText := strings.TrimSpace(parser.ConsumeRestOfLine(s))
		if idx := strings.Index(lineText, ":"); idx >= 0 {
			label := strings.TrimSpace(lineText[:idx])
			rest := strings.TrimSpace(lineText[idx+1:])
			if strings.HasPrefix(rest, "[") && strings.HasSuffix(rest, "]") {
				coords := rest[1 : len(rest)-1]
				parts := strings.Split(coords, ",")
				if len(parts) == 2 {
					x, xerr := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
					y, yerr := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
					if xerr == nil && yerr == nil {
						qc.Points = append(qc.Points, &DataPoint{
							Label: label,
							X:     x,
							Y:     y,
						})
					}
				}
			}
		}
	}

	return qc, nil
}

// parseHyphenatedKeyword checks if the current position starts with "prefix-suffix" (e.g., "x-axis").
// Consumes the tokens if matched, restores if not.
func parseHyphenatedKeyword(s *parser.Scanner, expected string) bool {
	parts := strings.SplitN(expected, "-", 2)
	if len(parts) != 2 {
		return false
	}
	saved := s.Save()
	tok := s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != parts[0] {
		return false
	}
	s.Next()
	if s.Peek().Kind != parser.TokenOperator || s.Peek().Text != "-" {
		s.Restore(saved)
		return false
	}
	s.Next()
	if s.Peek().Kind != parser.TokenIdent || s.Peek().Text != parts[1] {
		s.Restore(saved)
		return false
	}
	s.Next()
	return true
}

// parseAxis parses: label --> label2  or just label
func parseAxis(s *parser.Scanner, left, right *string) {
	line := strings.TrimSpace(parser.ConsumeRestOfLine(s))
	if idx := strings.Index(line, "-->"); idx >= 0 {
		*left = strings.TrimSpace(line[:idx])
		*right = strings.TrimSpace(line[idx+3:])
	} else {
		*left = line
	}
}
