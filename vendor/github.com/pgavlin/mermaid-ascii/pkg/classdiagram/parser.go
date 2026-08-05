// Package classdiagram parses and renders Mermaid class diagrams as ASCII/Unicode art.
package classdiagram

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// classDiagramKeyword is the Mermaid keyword that identifies a class diagram.
const classDiagramKeyword = "classDiagram"

// RelationType represents the type of relationship between classes.
type RelationType int

const (
	Association  RelationType = iota // -->
	Inheritance                      // <|--
	Composition                      // *--
	Aggregation                      // o--
	Dependency                       // ..>
	Realization                      // <|..
	Link                             // --
	DottedLink                       // ..
)

// Visibility represents the visibility of a class member.
type Visibility int

const (
	Public    Visibility = iota // +
	Private                    // -
	Protected                  // #
	Package                    // ~
	None                       // no prefix
)

// Member represents a class member (field or method).
type Member struct {
	Visibility Visibility
	Name       string
	Type       string
	IsMethod   bool
	Parameters string // raw parameter string for methods
}

// Class represents a class in the diagram.
type Class struct {
	Name    string
	Members []*Member
	Index   int
}

// Relationship represents a relationship between two classes.
type Relationship struct {
	From      string
	To        string
	Type      RelationType
	Label     string
	FromLabel string // cardinality label near "from"
	ToLabel   string // cardinality label near "to"
}

// ClassDiagram represents a parsed class diagram.
type ClassDiagram struct {
	Classes       []*Class
	Relationships []*Relationship
	classMap      map[string]*Class
}

// IsClassDiagram returns true if the input begins with the classDiagram keyword.
func IsClassDiagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return strings.HasPrefix(trimmed, classDiagramKeyword)
	}
	return false
}

// Parse parses a class diagram from the given input string.
func Parse(input string) (*ClassDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	tok := s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != classDiagramKeyword {
		return nil, fmt.Errorf("expected %q keyword", classDiagramKeyword)
	}
	s.Next()
	s.SkipNewlines()

	cd := &ClassDiagram{
		Classes:       []*Class{},
		Relationships: []*Relationship{},
		classMap:      make(map[string]*Class),
	}

	for !s.AtEnd() {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}

		tok := s.Peek()

		// class keyword
		if tok.Kind == parser.TokenIdent && tok.Text == "class" {
			cd.parseClass(s)
			continue
		}

		// Try to parse relationship: From [cardinality] arrow [cardinality] To [: label]
		if tok.Kind == parser.TokenIdent {
			if cd.tryParseRelationship(s) {
				continue
			}
		}

		// Skip unrecognized lines
		parser.SkipToEndOfLine(s)
	}

	if len(cd.Classes) == 0 {
		return nil, fmt.Errorf("no classes found")
	}

	return cd, nil
}

func (cd *ClassDiagram) parseClass(s *parser.Scanner) {
	s.Next() // consume "class"
	s.SkipWhitespace()

	nameTok := s.Peek()
	if nameTok.Kind != parser.TokenIdent {
		parser.SkipToEndOfLine(s)
		return
	}
	className := s.Next().Text
	cls := cd.getOrCreateClass(className)
	s.SkipWhitespace()

	// Check for '{' — block with members
	if s.Peek().Kind == parser.TokenLBrace {
		s.Next() // consume '{'
		s.SkipNewlines()
		for !s.AtEnd() {
			tok := s.Peek()
			if tok.Kind == parser.TokenRBrace {
				s.Next()
				break
			}
			// Parse member line
			memberText := strings.TrimSpace(parser.ConsumeRestOfLine(s))
			s.SkipNewlines()
			if memberText == "" {
				continue
			}
			member := parseMember(memberText)
			if member != nil {
				cls.Members = append(cls.Members, member)
			}
		}
		return
	}

	// Inline class (no body)
}

// Known arrow patterns for class diagrams.
var classArrows = []string{
	"<|--", "<|..", "*--", "*..","o--", "o..", "-->", "<--", "--*", "--o", "..>", "--", "..",
}

func (cd *ClassDiagram) tryParseRelationship(s *parser.Scanner) bool {
	saved := s.Save()

	// From class name
	fromTok := s.Peek()
	if fromTok.Kind != parser.TokenIdent {
		return false
	}
	from := s.Next().Text
	s.SkipWhitespace()

	// Optional from cardinality: "1"
	fromLabel := ""
	if s.Peek().Kind == parser.TokenString {
		fromLabel = s.Next().Text
		s.SkipWhitespace()
	}

	// Collect arrow tokens — arrows like <|--, ..>, o--, etc. are multiple tokens
	arrowText := collectArrow(s)
	if arrowText == "" {
		s.Restore(saved)
		return false
	}

	// Validate it's a known arrow
	relType, ok := matchArrow(arrowText)
	if !ok {
		s.Restore(saved)
		return false
	}

	s.SkipWhitespace()

	// Optional to cardinality: "*"
	toLabel := ""
	if s.Peek().Kind == parser.TokenString {
		toLabel = s.Next().Text
		s.SkipWhitespace()
	}

	// To class name
	toTok := s.Peek()
	if toTok.Kind != parser.TokenIdent {
		s.Restore(saved)
		return false
	}
	to := s.Next().Text
	s.SkipWhitespace()

	// Optional label: : text
	label := ""
	if s.Peek().Kind == parser.TokenColon {
		s.Next()
		s.SkipWhitespace()
		label = strings.TrimSpace(parser.ConsumeRestOfLine(s))
	}

	cd.getOrCreateClass(from)
	cd.getOrCreateClass(to)
	cd.Relationships = append(cd.Relationships, &Relationship{
		From:      from,
		To:        to,
		Type:      relType,
		Label:     label,
		FromLabel: fromLabel,
		ToLabel:   toLabel,
	})
	return true
}

// collectArrow greedily collects tokens that form an arrow pattern.
// Arrows like <|-- tokenize as Operator("<") Pipe Operator("--") or similar combos.
// *-- tokenizes as Text("*") Operator("--"), o-- as Ident("o") Operator("--").
func collectArrow(s *parser.Scanner) string {
	var parts []string
	for {
		tok := s.Peek()
		switch tok.Kind {
		case parser.TokenOperator:
			parts = append(parts, s.Next().Text)
		case parser.TokenPipe:
			parts = append(parts, s.Next().Text)
		case parser.TokenDot:
			parts = append(parts, s.Next().Text)
		case parser.TokenText:
			// Handle * in *-- arrows
			if tok.Text == "*" {
				parts = append(parts, s.Next().Text)
				continue
			}
			return strings.Join(parts, "")
		case parser.TokenIdent:
			// Handle o in o-- arrows (only at start or end of arrow)
			if tok.Text == "o" && (len(parts) == 0 || len(parts) > 0) {
				// Check if followed by operator chars (at start) or preceded by operator chars (at end)
				saved := s.Save()
				parts = append(parts, s.Next().Text)
				// If the next token is an operator or this completes a valid arrow, keep it
				next := s.Peek()
				if next.Kind == parser.TokenOperator || next.Kind == parser.TokenDot {
					continue
				}
				// Check if what we have so far + "o" is a valid arrow
				candidate := strings.Join(parts, "")
				if _, ok := matchArrow(candidate); ok {
					return candidate
				}
				// Not valid — restore
				s.Restore(saved)
				parts = parts[:len(parts)-1]
				return strings.Join(parts, "")
			}
			return strings.Join(parts, "")
		default:
			return strings.Join(parts, "")
		}
	}
}

func parseRelationType(arrow string) RelationType {
	rt, ok := matchArrow(arrow)
	if !ok {
		return Association
	}
	return rt
}

func matchArrow(text string) (RelationType, bool) {
	switch text {
	case "<|--":
		return Inheritance, true
	case "<|..":
		return Realization, true
	case "*--", "--*":
		return Composition, true
	case "o--", "--o":
		return Aggregation, true
	case "..>":
		return Dependency, true
	case "-->", "<--":
		return Association, true
	case "--":
		return Link, true
	case "..", "*..":
		return DottedLink, true
	}
	return 0, false
}

func (cd *ClassDiagram) getOrCreateClass(name string) *Class {
	if cls, ok := cd.classMap[name]; ok {
		return cls
	}
	cls := &Class{
		Name:    name,
		Members: []*Member{},
		Index:   len(cd.Classes),
	}
	cd.Classes = append(cd.Classes, cls)
	cd.classMap[name] = cls
	return cls
}

func parseMember(line string) *Member {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	vis := None
	if len(line) > 0 {
		switch line[0] {
		case '+':
			vis = Public
			line = line[1:]
		case '-':
			vis = Private
			line = line[1:]
		case '#':
			vis = Protected
			line = line[1:]
		case '~':
			vis = Package
			line = line[1:]
		}
	}

	line = strings.TrimSpace(line)

	// Check if it's a method: contains ()
	if idx := strings.Index(line, "("); idx != -1 {
		endIdx := strings.LastIndex(line, ")")
		if endIdx == -1 {
			endIdx = len(line)
		}
		name := strings.TrimSpace(line[:idx])
		params := ""
		if endIdx > idx+1 {
			params = line[idx+1 : endIdx]
		}
		retType := ""
		if endIdx+1 < len(line) {
			retType = strings.TrimSpace(line[endIdx+1:])
		}
		return &Member{
			Visibility: vis,
			Name:       name,
			Type:       retType,
			IsMethod:   true,
			Parameters: params,
		}
	}

	// It's a field: Type Name or just Name
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		return &Member{
			Visibility: vis,
			Name:       parts[1],
			Type:       parts[0],
			IsMethod:   false,
		}
	} else if len(parts) == 1 {
		return &Member{
			Visibility: vis,
			Name:       parts[0],
			Type:       "",
			IsMethod:   false,
		}
	}

	return nil
}
