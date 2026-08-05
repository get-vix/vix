// Package packet implements parsing and rendering of packet/protocol diagrams
// in Mermaid syntax.
package packet

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// packetKeyword is the Mermaid keyword that identifies a packet diagram.
const packetKeyword = "packet-beta"

// PacketDiagram represents a parsed packet/protocol diagram.
type PacketDiagram struct {
	Fields []*Field
}

// Field represents a single field in a packet diagram, spanning one or more bits.
type Field struct {
	StartBit int
	EndBit   int
	Label    string
}

// IsPacketDiagram returns true if the input starts with the packet-beta keyword.
func IsPacketDiagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return trimmed == packetKeyword
	}
	return false
}

// Parse parses a packet diagram from Mermaid-style input.
func Parse(input string) (*PacketDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	// "packet-beta" tokenizes as Ident("packet") Operator("-") Ident("beta")
	keyword := collectKeyword(s)
	if keyword != packetKeyword {
		return nil, fmt.Errorf("expected %q keyword", packetKeyword)
	}
	s.SkipNewlines()

	pd := &PacketDiagram{Fields: []*Field{}}

	for !s.AtEnd() {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}

		// Field: startBit[-endBit] : "label"
		tok := s.Peek()
		if tok.Kind == parser.TokenNumber {
			startBit, _ := strconv.Atoi(s.Next().Text)
			endBit := startBit

			// Optional -endBit
			if s.Peek().Kind == parser.TokenOperator && s.Peek().Text == "-" {
				s.Next() // consume '-'
				if s.Peek().Kind == parser.TokenNumber {
					endBit, _ = strconv.Atoi(s.Next().Text)
				}
			}
			s.SkipWhitespace()

			// Expect : "label"
			if s.Peek().Kind == parser.TokenColon {
				s.Next()
				s.SkipWhitespace()
				if s.Peek().Kind == parser.TokenString {
					label := s.Next().Text
					pd.Fields = append(pd.Fields, &Field{
						StartBit: startBit,
						EndBit:   endBit,
						Label:    label,
					})
				}
			}
			parser.SkipToEndOfLine(s)
			continue
		}

		parser.SkipToEndOfLine(s)
	}

	if len(pd.Fields) == 0 {
		return nil, fmt.Errorf("no fields found")
	}

	return pd, nil
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
