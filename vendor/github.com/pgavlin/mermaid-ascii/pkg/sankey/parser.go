// Package sankey implements parsing and rendering of Sankey diagrams
// in Mermaid syntax.
package sankey

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
)

// sankeyBetaKeyword is the Mermaid keyword that identifies a Sankey diagram.
const sankeyBetaKeyword = "sankey-beta"

// Flow represents a single flow from source to target with a value.
type Flow struct {
	Source string
	Target string
	Value  float64
}

// SankeyDiagram represents a parsed Sankey diagram.
type SankeyDiagram struct {
	Flows []*Flow
}

// IsSankeyDiagram returns true if the input starts with sankey-beta keyword.
func IsSankeyDiagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return strings.HasPrefix(trimmed, sankeyBetaKeyword)
	}
	return false
}

// Parse parses a Sankey diagram from CSV-like input.
func Parse(input string) (*SankeyDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	rawLines := strings.Split(input, "\n")
	lines := diagram.RemoveComments(rawLines)
	if len(lines) == 0 {
		return nil, fmt.Errorf("no content found")
	}

	if !strings.HasPrefix(strings.TrimSpace(lines[0]), sankeyBetaKeyword) {
		return nil, fmt.Errorf("expected %q keyword", sankeyBetaKeyword)
	}

	d := &SankeyDiagram{}

	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Parse CSV: Source,Target,Value
		parts := parseCSVLine(trimmed)
		if len(parts) != 3 {
			continue // skip malformed lines
		}

		source := strings.TrimSpace(parts[0])
		target := strings.TrimSpace(parts[1])
		valueStr := strings.TrimSpace(parts[2])

		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			continue // skip lines with invalid values
		}

		d.Flows = append(d.Flows, &Flow{
			Source: source,
			Target: target,
			Value:  value,
		})
	}

	return d, nil
}

// parseCSVLine splits a CSV line handling quoted fields.
func parseCSVLine(line string) []string {
	var fields []string
	var current strings.Builder
	inQuotes := false

	for i := 0; i < len(line); i++ {
		ch := line[i]
		if ch == '"' {
			inQuotes = !inQuotes
		} else if ch == ',' && !inQuotes {
			fields = append(fields, current.String())
			current.Reset()
		} else {
			current.WriteByte(ch)
		}
	}
	fields = append(fields, current.String())
	return fields
}
