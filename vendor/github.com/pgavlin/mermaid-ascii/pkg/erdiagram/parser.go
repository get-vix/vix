// Package erdiagram parses and renders Mermaid entity-relationship diagrams as ASCII/Unicode art.
package erdiagram

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// erDiagramKeyword is the Mermaid keyword that identifies an entity-relationship diagram.
const erDiagramKeyword = "erDiagram"

// Cardinality represents the cardinality of a relationship end.
type Cardinality int

const (
	ExactlyOne Cardinality = iota // ||
	ZeroOrOne                     // o| or |o
	OneOrMany                     // }| or |{
	ZeroOrMany                    // }o or o{
)

// Constraint represents a column constraint.
type Constraint int

const (
	NoConstraint Constraint = iota
	PrimaryKey              // PK
	ForeignKey              // FK
	UniqueKey               // UK
)

// Attribute represents an entity attribute.
type Attribute struct {
	Type       string
	Name       string
	Constraint Constraint
}

// Entity represents an entity in the ER diagram.
type Entity struct {
	Name       string
	Attributes []*Attribute
	Index      int
}

// Relationship represents a relationship between two entities.
type Relationship struct {
	From            string
	To              string
	Label           string
	FromCardinality Cardinality
	ToCardinality   Cardinality
}

// ERDiagram represents a parsed ER diagram.
type ERDiagram struct {
	Entities      []*Entity
	Relationships []*Relationship
	entityMap     map[string]*Entity
}

// IsERDiagram returns true if the input begins with the erDiagram keyword.
func IsERDiagram(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return strings.HasPrefix(trimmed, erDiagramKeyword)
	}
	return false
}

// Parse parses an ER diagram from the given input string.
func Parse(input string) (*ERDiagram, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	tok := s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != erDiagramKeyword {
		return nil, fmt.Errorf("expected %q keyword", erDiagramKeyword)
	}
	s.Next()
	s.SkipNewlines()

	erd := &ERDiagram{
		Entities:      []*Entity{},
		Relationships: []*Relationship{},
		entityMap:     make(map[string]*Entity),
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

		// Try entity block: ENTITY {
		if tryParseEntityBlock(s, erd) {
			continue
		}

		// Try relationship: ENTITY1 cardinality--cardinality ENTITY2 : label
		if tryParseRelationship(s, erd) {
			continue
		}

		parser.SkipToEndOfLine(s)
	}

	if len(erd.Entities) == 0 {
		return nil, fmt.Errorf("no entities found")
	}

	return erd, nil
}

func tryParseEntityBlock(s *parser.Scanner, erd *ERDiagram) bool {
	saved := s.Save()

	nameTok := s.Peek()
	if nameTok.Kind != parser.TokenIdent {
		return false
	}
	name := s.Next().Text
	s.SkipWhitespace()

	if s.Peek().Kind != parser.TokenLBrace {
		s.Restore(saved)
		return false
	}
	s.Next() // consume '{'
	s.SkipNewlines()

	entity := erd.getOrCreateEntity(name)

	// Parse attributes until '}'
	for !s.AtEnd() {
		tok := s.Peek()
		if tok.Kind == parser.TokenRBrace {
			s.Next()
			break
		}

		// Parse attribute: type name [PK|FK|UK]
		if tok.Kind == parser.TokenIdent {
			attrType := s.Next().Text
			s.SkipWhitespace()
			if s.Peek().Kind == parser.TokenIdent {
				attrName := s.Next().Text
				s.SkipWhitespace()

				constraint := NoConstraint
				if s.Peek().Kind == parser.TokenIdent {
					switch s.Peek().Text {
					case "PK":
						constraint = PrimaryKey
						s.Next()
					case "FK":
						constraint = ForeignKey
						s.Next()
					case "UK":
						constraint = UniqueKey
						s.Next()
					}
				}
				entity.Attributes = append(entity.Attributes, &Attribute{
					Type:       attrType,
					Name:       attrName,
					Constraint: constraint,
				})
			}
		}
		parser.SkipToEndOfLine(s)
		s.SkipNewlines()
	}
	return true
}

// tryParseRelationship parses: ENTITY1 cardinality--cardinality ENTITY2 : label
// Cardinality markers: ||, o|, |o, }|, |{, }o, o{
// The "--" connects the two cardinality markers.
func tryParseRelationship(s *parser.Scanner, erd *ERDiagram) bool {
	saved := s.Save()

	fromTok := s.Peek()
	if fromTok.Kind != parser.TokenIdent {
		return false
	}
	from := s.Next().Text
	s.SkipWhitespace()

	// Collect left cardinality + "--" + right cardinality
	// These tokenize as combinations of Pipe, RBrace, Operator, Ident("o")
	leftCard := collectCardinality(s)
	if leftCard == "" {
		s.Restore(saved)
		return false
	}

	// Expect operator starting with "--"
	opTok := s.Peek()
	if opTok.Kind != parser.TokenOperator || !strings.HasPrefix(opTok.Text, "--") {
		s.Restore(saved)
		return false
	}
	s.Next()

	// The operator may include the start of the right cardinality
	// e.g., "--o" → separator "--" + right cardinality prefix "o"
	rightPrefix := strings.TrimPrefix(opTok.Text, "--")

	// Collect right cardinality
	rightCard := rightPrefix + collectCardinality(s)
	if rightCard == "" {
		s.Restore(saved)
		return false
	}

	s.SkipWhitespace()

	// Expect entity name
	toTok := s.Peek()
	if toTok.Kind != parser.TokenIdent {
		s.Restore(saved)
		return false
	}
	to := s.Next().Text
	s.SkipWhitespace()

	// Expect : label
	if s.Peek().Kind != parser.TokenColon {
		s.Restore(saved)
		return false
	}
	s.Next()
	s.SkipWhitespace()
	label := strings.TrimSpace(parser.ConsumeRestOfLine(s))

	erd.getOrCreateEntity(from)
	erd.getOrCreateEntity(to)

	erd.Relationships = append(erd.Relationships, &Relationship{
		From:            from,
		To:              to,
		Label:           label,
		FromCardinality: parseCardinality(leftCard),
		ToCardinality:   parseCardinality(rightCard),
	})
	return true
}

// collectCardinality collects cardinality marker tokens like ||, o|, }|, |{, }o, o{.
// These are composed of Pipe, RBrace, LBrace, and Ident("o") tokens.
func collectCardinality(s *parser.Scanner) string {
	var parts []string
	for {
		tok := s.Peek()
		switch tok.Kind {
		case parser.TokenPipe:
			parts = append(parts, s.Next().Text)
		case parser.TokenRBrace:
			parts = append(parts, s.Next().Text)
		case parser.TokenLBrace:
			parts = append(parts, s.Next().Text)
		case parser.TokenIdent:
			if tok.Text == "o" {
				parts = append(parts, s.Next().Text)
			} else {
				return strings.Join(parts, "")
			}
		default:
			return strings.Join(parts, "")
		}
	}
}

func (erd *ERDiagram) getOrCreateEntity(name string) *Entity {
	if e, ok := erd.entityMap[name]; ok {
		return e
	}
	e := &Entity{
		Name:       name,
		Attributes: []*Attribute{},
		Index:      len(erd.Entities),
	}
	erd.Entities = append(erd.Entities, e)
	erd.entityMap[name] = e
	return e
}

func parseCardinality(s string) Cardinality {
	s = strings.TrimSpace(s)
	switch s {
	case "||":
		return ExactlyOne
	case "o|", "|o":
		return ZeroOrOne
	case "}|", "|{":
		return OneOrMany
	case "}o", "o{":
		return ZeroOrMany
	default:
		if strings.Contains(s, "}") && strings.Contains(s, "o") {
			return ZeroOrMany
		}
		if strings.Contains(s, "}") {
			return OneOrMany
		}
		if strings.Contains(s, "o") {
			return ZeroOrOne
		}
		return ExactlyOne
	}
}
