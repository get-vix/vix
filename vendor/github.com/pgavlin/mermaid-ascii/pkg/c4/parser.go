// Package c4 implements parsing and rendering of C4 architecture diagrams
// (C4Context, C4Container, C4Component, C4Dynamic) in Mermaid syntax.
package c4

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// C4DiagramType represents the specific C4 diagram type.
type C4DiagramType int

const (
	// C4Context represents a C4 context diagram.
	C4Context C4DiagramType = iota
	// C4Container represents a C4 container diagram.
	C4Container
	// C4Component represents a C4 component diagram.
	C4Component
	// C4Dynamic represents a C4 dynamic diagram.
	C4Dynamic
)

// Element represents a C4 element (Person, System, Container, Component).
type Element struct {
	Alias       string
	Label       string
	Description string
	Technology  string
	Kind        string // "person", "system", "container", "component"
}

// Relationship represents a C4 relationship.
type Relationship struct {
	From       string
	To         string
	Label      string
	Technology string
}

// Boundary represents a C4 boundary containing elements.
type Boundary struct {
	Alias      string
	Label      string
	Elements   []*Element
	Boundaries []*Boundary
}

// C4Diagram represents a parsed C4 diagram.
type C4Diagram struct {
	DiagramType   C4DiagramType
	Elements      []*Element
	Relationships []*Relationship
	Boundaries    []*Boundary
}

var c4Keywords = map[string]C4DiagramType{
	"C4Context":   C4Context,
	"C4Container": C4Container,
	"C4Component": C4Component,
	"C4Dynamic":   C4Dynamic,
}

// IsC4Diagram returns true if the input starts with a C4 diagram keyword.
func IsC4Diagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		for kw := range c4Keywords {
			if trimmed == kw {
				return true
			}
		}
		return false
	}
	return false
}

// Parse parses a C4 diagram from Mermaid-style input.
func Parse(input string) (*C4Diagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	// Expect C4 keyword
	tok := s.Peek()
	if tok.Kind != parser.TokenIdent {
		return nil, fmt.Errorf("expected C4 diagram keyword")
	}
	keyword := s.Next().Text
	dtype, ok := c4Keywords[keyword]
	if !ok {
		return nil, fmt.Errorf("expected C4 diagram keyword")
	}
	s.SkipNewlines()

	d := &C4Diagram{DiagramType: dtype}

	if err := parseStatements(s, d, nil); err != nil {
		return nil, err
	}

	return d, nil
}

func parseStatements(s *parser.Scanner, d *C4Diagram, currentBoundary *Boundary) error {
	for !s.AtEnd() {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}

		tok := s.Peek()

		// End of boundary: '}'
		if tok.Kind == parser.TokenRBrace {
			s.Next()
			return nil
		}

		if tok.Kind != parser.TokenIdent {
			parser.SkipToEndOfLine(s)
			continue
		}

		name := tok.Text

		// Check for boundary types
		if isBoundaryFunc(name) {
			if err := parseBoundary(s, d, currentBoundary); err != nil {
				return err
			}
			continue
		}

		// Check for element functions
		if kind := elementKind(name); kind != "" {
			elem := parseElement(s, kind)
			if elem != nil {
				addElement(d, currentBoundary, elem)
			} else {
				parser.SkipToEndOfLine(s)
			}
			continue
		}

		// Check for relationship functions
		if isRelFunc(name) {
			rel := parseRelationship(s)
			if rel != nil {
				d.Relationships = append(d.Relationships, rel)
			} else {
				parser.SkipToEndOfLine(s)
			}
			continue
		}

		// Skip unrecognized lines
		parser.SkipToEndOfLine(s)
	}
	return nil
}

func isBoundaryFunc(name string) bool {
	switch name {
	case "Enterprise_Boundary", "System_Boundary", "Container_Boundary", "Boundary":
		return true
	}
	return false
}

func elementKind(name string) string {
	switch name {
	case "Person", "Person_Ext":
		return "person"
	case "System", "System_Ext":
		return "system"
	case "Container", "Container_Ext", "ContainerDb", "ContainerQueue":
		return "container"
	case "Component", "Component_Ext", "ComponentDb", "ComponentQueue":
		return "component"
	}
	return ""
}

func isRelFunc(name string) bool {
	switch name {
	case "Rel", "Rel_Back", "Rel_Neighbor", "Rel_Back_Neighbor", "BiRel":
		return true
	}
	return false
}

// parseFuncName consumes a function-like identifier, possibly with underscore parts.
// e.g., "Person_Ext" → tokenizes as Ident("Person") Text("_") Ident("Ext") but
// actually our scanner treats underscores as part of identifiers, so it's one token.
func parseFuncName(s *parser.Scanner) string {
	tok := s.Peek()
	if tok.Kind != parser.TokenIdent {
		return ""
	}
	return s.Next().Text
}

// parseArgs parses a comma-separated argument list inside parentheses.
// Returns up to maxArgs arguments (strings or identifiers). Already consumed '('.
func parseArgs(s *parser.Scanner, maxArgs int) []string {
	var args []string
	for len(args) < maxArgs {
		s.SkipWhitespace()
		tok := s.Peek()
		if tok.Kind == parser.TokenRParen || tok.Kind == parser.TokenEOF || tok.Kind == parser.TokenNewline {
			break
		}
		if tok.Kind == parser.TokenString {
			args = append(args, s.Next().Text)
		} else if tok.Kind == parser.TokenIdent || tok.Kind == parser.TokenNumber {
			args = append(args, s.Next().Text)
		} else {
			break
		}
		s.SkipWhitespace()
		if s.Peek().Kind == parser.TokenComma {
			s.Next()
		}
	}
	// Consume closing paren
	if s.Peek().Kind == parser.TokenRParen {
		s.Next()
	}
	return args
}

// parseElement parses: FuncName(alias, "label" [, "tech"] [, "description"])
func parseElement(s *parser.Scanner, kind string) *Element {
	s.Next() // consume function name
	s.SkipWhitespace()
	if s.Peek().Kind != parser.TokenLParen {
		return nil
	}
	s.Next() // consume '('

	args := parseArgs(s, 4)
	if len(args) < 2 {
		return nil
	}

	elem := &Element{
		Alias: args[0],
		Label: args[1],
		Kind:  kind,
	}
	if kind == "container" || kind == "component" {
		if len(args) >= 3 {
			elem.Technology = args[2]
		}
		if len(args) >= 4 {
			elem.Description = args[3]
		}
	} else {
		if len(args) >= 3 {
			elem.Description = args[2]
		}
	}
	return elem
}

// parseBoundary parses: BoundaryFunc(alias, "label") { ... }
func parseBoundary(s *parser.Scanner, d *C4Diagram, currentBoundary *Boundary) error {
	s.Next() // consume function name
	s.SkipWhitespace()
	if s.Peek().Kind != parser.TokenLParen {
		parser.SkipToEndOfLine(s)
		return nil
	}
	s.Next() // consume '('

	args := parseArgs(s, 2)
	if len(args) < 2 {
		parser.SkipToEndOfLine(s)
		return nil
	}

	b := &Boundary{
		Alias: args[0],
		Label: args[1],
	}

	s.SkipWhitespace()
	// Optional '{' on the same line
	if s.Peek().Kind == parser.TokenLBrace {
		s.Next()
	}
	s.SkipNewlines()

	if err := parseStatements(s, d, b); err != nil {
		return err
	}

	if currentBoundary != nil {
		currentBoundary.Boundaries = append(currentBoundary.Boundaries, b)
	} else {
		d.Boundaries = append(d.Boundaries, b)
	}
	return nil
}

// parseRelationship parses: RelFunc(from, to, "label" [, "tech"])
func parseRelationship(s *parser.Scanner) *Relationship {
	s.Next() // consume function name
	s.SkipWhitespace()
	if s.Peek().Kind != parser.TokenLParen {
		return nil
	}
	s.Next() // consume '('

	args := parseArgs(s, 4)
	if len(args) < 3 {
		return nil
	}

	rel := &Relationship{
		From:  args[0],
		To:    args[1],
		Label: args[2],
	}
	if len(args) >= 4 {
		rel.Technology = args[3]
	}
	return rel
}

func addElement(d *C4Diagram, boundary *Boundary, elem *Element) {
	if boundary != nil {
		boundary.Elements = append(boundary.Elements, elem)
	} else {
		d.Elements = append(d.Elements, elem)
	}
}
