// Package xychart provides parsing and rendering of Mermaid xychart-beta diagrams.
package xychart

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// xyChartKeyword is the keyword that identifies an XY chart diagram in Mermaid syntax.
const xyChartKeyword = "xychart-beta"

// DataSeries represents a named data series in a chart.
type DataSeries struct {
	Name string
	Data []float64
}

// XYChart represents a parsed XY chart with optional bar and line data series.
type XYChart struct {
	Title      string
	XLabel     string
	XValues    []string
	YLabel     string
	YMin       float64
	YMax       float64
	BarSeries  []DataSeries
	LineSeries []DataSeries
}

// IsXYChart reports whether the input text is an XY chart diagram.
func IsXYChart(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return trimmed == xyChartKeyword
	}
	return false
}

// Parse parses Mermaid xychart-beta text into an XYChart.
func Parse(input string) (*XYChart, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	// "xychart-beta" tokenizes as Ident("xychart") Operator("-") Ident("beta")
	keyword := collectKeyword(s)
	if keyword != xyChartKeyword {
		return nil, fmt.Errorf("expected %q keyword", xyChartKeyword)
	}
	s.SkipNewlines()

	chart := &XYChart{XValues: []string{}}

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
			if s.Peek().Kind == parser.TokenString {
				chart.Title = s.Next().Text
			} else {
				chart.Title = strings.TrimSpace(parser.ConsumeRestOfLine(s))
			}
			continue

		case "x":
			// "x-axis" tokenizes as Ident("x") Operator("-") Ident("axis")
			if parseHyphenatedKeyword(s, "x-axis") {
				s.SkipWhitespace()
				// Optional label in quotes
				if s.Peek().Kind == parser.TokenString {
					chart.XLabel = s.Next().Text
					s.SkipWhitespace()
				}
				// Expect [values]
				if s.Peek().Kind == parser.TokenLBracket {
					chart.XValues = parseBracketedStringList(s)
				}
				parser.SkipToEndOfLine(s)
				continue
			}

		case "y":
			if parseHyphenatedKeyword(s, "y-axis") {
				s.SkipWhitespace()
				// Optional label in quotes
				if s.Peek().Kind == parser.TokenString {
					chart.YLabel = s.Next().Text
					s.SkipWhitespace()
				}
				// Optional range: min --> max
				if s.Peek().Kind == parser.TokenNumber {
					chart.YMin, _ = strconv.ParseFloat(s.Next().Text, 64)
					s.SkipWhitespace()
					// Expect -->
					if s.Peek().Kind == parser.TokenOperator && s.Peek().Text == "-->" {
						s.Next()
						s.SkipWhitespace()
						if s.Peek().Kind == parser.TokenNumber {
							chart.YMax, _ = strconv.ParseFloat(s.Next().Text, 64)
						}
					}
				}
				parser.SkipToEndOfLine(s)
				continue
			}

		case "bar":
			s.Next()
			s.SkipWhitespace()
			ds := DataSeries{}
			if s.Peek().Kind == parser.TokenString {
				ds.Name = s.Next().Text
				s.SkipWhitespace()
			}
			if s.Peek().Kind == parser.TokenLBracket {
				ds.Data = parseBracketedFloatList(s)
			}
			chart.BarSeries = append(chart.BarSeries, ds)
			parser.SkipToEndOfLine(s)
			continue

		case "line":
			s.Next()
			s.SkipWhitespace()
			ds := DataSeries{}
			if s.Peek().Kind == parser.TokenString {
				ds.Name = s.Next().Text
				s.SkipWhitespace()
			}
			if s.Peek().Kind == parser.TokenLBracket {
				ds.Data = parseBracketedFloatList(s)
			}
			chart.LineSeries = append(chart.LineSeries, ds)
			parser.SkipToEndOfLine(s)
			continue
		}

		parser.SkipToEndOfLine(s)
	}

	if len(chart.BarSeries) == 0 && len(chart.LineSeries) == 0 {
		return nil, fmt.Errorf("no data found")
	}

	// Auto-calculate Y range if not specified
	if chart.YMax == 0 {
		for _, ser := range chart.BarSeries {
			for _, v := range ser.Data {
				if v > chart.YMax {
					chart.YMax = v
				}
			}
		}
		for _, ser := range chart.LineSeries {
			for _, v := range ser.Data {
				if v > chart.YMax {
					chart.YMax = v
				}
			}
		}
	}

	return chart, nil
}

func collectKeyword(s *parser.Scanner) string {
	tok := s.Peek()
	if tok.Kind != parser.TokenIdent {
		return ""
	}
	var b strings.Builder
	b.WriteString(s.Next().Text)
	if s.Peek().Kind == parser.TokenOperator && s.Peek().Text == "-" {
		b.WriteString(s.Next().Text)
		if s.Peek().Kind == parser.TokenIdent {
			b.WriteString(s.Next().Text)
		}
	}
	return b.String()
}

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

// parseBracketedStringList parses [value1, value2, ...] where values can be quoted or bare.
func parseBracketedStringList(s *parser.Scanner) []string {
	s.Next() // consume '['
	var result []string
	for !s.AtEnd() {
		s.SkipWhitespace()
		tok := s.Peek()
		if tok.Kind == parser.TokenRBracket {
			s.Next()
			break
		}
		if tok.Kind == parser.TokenComma {
			s.Next()
			continue
		}
		if tok.Kind == parser.TokenString {
			result = append(result, s.Next().Text)
		} else if tok.Kind == parser.TokenIdent || tok.Kind == parser.TokenNumber {
			result = append(result, s.Next().Text)
		} else if tok.Kind == parser.TokenNewline || tok.Kind == parser.TokenEOF {
			break
		} else {
			s.Next() // skip unexpected
		}
	}
	return result
}

// parseBracketedFloatList parses [1.0, 2.0, ...].
func parseBracketedFloatList(s *parser.Scanner) []float64 {
	s.Next() // consume '['
	var result []float64
	for !s.AtEnd() {
		s.SkipWhitespace()
		tok := s.Peek()
		if tok.Kind == parser.TokenRBracket {
			s.Next()
			break
		}
		if tok.Kind == parser.TokenComma {
			s.Next()
			continue
		}
		if tok.Kind == parser.TokenNumber {
			v, err := strconv.ParseFloat(s.Next().Text, 64)
			if err == nil {
				result = append(result, v)
			}
		} else if tok.Kind == parser.TokenNewline || tok.Kind == parser.TokenEOF {
			break
		} else {
			s.Next() // skip unexpected
		}
	}
	return result
}

func parseStringList(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func parseFloatList(s string) []float64 {
	parts := strings.Split(s, ",")
	result := make([]float64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		v, err := strconv.ParseFloat(p, 64)
		if err == nil {
			result = append(result, v)
		}
	}
	return result
}
